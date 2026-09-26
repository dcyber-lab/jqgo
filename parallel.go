package jqgo

import (
	"bytes"
	"context"
	"io"
	"iter"
	"runtime"
	"slices"
	"sync"
)

// Parallel input processing.
//
// A stream of many JSON values (NDJSON, or the output of another jq) is
// cut into chunks at newlines that lie between top-level values; each
// chunk is decoded and queried on its own goroutine with the same loop
// RunReader uses, and the results are yielded in input order. The cut
// points are found by a scanner that only tracks nesting and strings,
// which is far cheaper than parsing.
//
// A newline at depth 0 outside a string is always between tokens in valid
// JSON, so decoding the chunks separately gives what decoding the whole
// stream gives. On invalid input the first chunk with an error is exactly
// the one where the sequential decoder would stop, with the same error:
// each chunk decoder starts at the right line number, and chunks always
// end with the newline, so no token is cut short.

const (
	chunkTarget = 1 << 20 // bytes per chunk
	// A value longer than this is processed sequentially, together with
	// the rest of the input. A streamable query gives up on a value sooner:
	// streaming it takes less memory than buffering it.
	chunkMax       = 64 << 20
	chunkMaxStream = 8 << 20
)

type result struct {
	v   any
	err error
}

type chunk struct {
	data  []byte
	line  int       // line number at data[0]
	first bool      // at the start of the stream, where a BOM may be
	tail  io.Reader // instead of data: the rest of the input, processed sequentially

	results []result
	more    bool // the chunk ended normally; processing goes on
	done    chan struct{}
}

func (c *chunk) decoder(r io.Reader) *Decoder {
	var d *Decoder
	if c.tail != nil {
		d = NewDecoder(r)
	} else {
		d = newBytesDecoder(c.data)
	}
	d.line, d.started = c.line, !c.first
	return d
}

// ParallelSafe reports whether RunReaderParallel can process inputs
// concurrently: the query does not call input, inputs, debug or stderr.
func (q *Query) ParallelSafe() bool { return q.parallel }

// RunReaderParallel is RunReader spread over several goroutines, for
// inputs made of many values, such as NDJSON. Results come out in the same
// order, with the same errors, as from RunReader.
//
// workers <= 0 means runtime.GOMAXPROCS(0). Queries that call input,
// inputs, debug or stderr (whose effects depend on order) run
// sequentially, as does any value longer than 64 MB (8 MB if the query
// is Streamable) together with the rest of the input after it. Functions registered with WithFunction are
// called concurrently.
//
// If the loop stops early, a goroutine may still be blocked in r.Read; it
// exits when that call returns.
func (q *Query) RunReaderParallel(ctx context.Context, r io.Reader, workers int, vars ...any) iter.Seq2[any, error] {
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers == 1 || !q.parallel {
		return q.RunReader(ctx, r, vars...)
	}
	return func(yield func(any, error) bool) {
		limit := chunkMax
		if q.stream != nil {
			limit = chunkMaxStream
		}
		q.runParallel(ctx, r, workers, chunkTarget, limit, vars, yield)
	}
}

func (q *Query) runParallel(ctx context.Context, r io.Reader, workers, target, limit int, vars []any, yield func(any, error) bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel() // stops the workers and the splitter
		wg.Wait()
	}()

	order := make(chan *chunk, workers+1) // every chunk, in input order
	jobs := make(chan *chunk, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				var c *chunk
				select {
				case c = <-jobs:
				case <-ctx.Done():
					return
				}
				if c == nil {
					return
				}
				c.more = q.readLoop(ctx, c.decoder(nil), r, vars, func(v any, err error) bool {
					c.results = append(c.results, result{v, err})
					return true
				})
				close(c.done)
			}
		}()
	}
	go func() {
		defer close(jobs)
		defer close(order)
		split(r, target, limit, func(c *chunk) bool {
			select {
			case order <- c:
			case <-ctx.Done():
				return false
			}
			if c.tail != nil {
				return false
			}
			select {
			case jobs <- c:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()

	for c := range order {
		if c.tail != nil {
			q.readLoop(ctx, c.decoder(c.tail), r, vars, yield)
			return
		}
		select {
		case <-c.done:
		case <-ctx.Done():
			yield(nil, ctx.Err()) // only the caller's context can be done here
			return
		}
		for _, res := range c.results {
			if !yield(res.v, res.err) {
				return
			}
		}
		if !c.more {
			return
		}
		c.results = nil
	}
	// The splitter also stops when the caller's context is done.
	if err := ctx.Err(); err != nil {
		yield(nil, err)
	}
}

// split reads r and hands out chunks that end at a newline between
// top-level values, of about target bytes, or less when r returns less
// (a pipe), so values that trickle in are not held back. It stops when
// emit returns false or after emitting a tail.
func split(r io.Reader, target, limit int, emit func(*chunk) bool) {
	sc := splitScanner{line: 1}
	buf := make([]byte, 0, target)
	scanned := 0 // bytes of buf the scanner has seen
	cut := -1    // end of the last cut point in buf
	cutLine := 0 // line number at cut
	bufLine := 1 // line number at buf[0]
	first := true
	send := func(c *chunk) bool {
		c.first, first = first, false
		c.done = make(chan struct{})
		return emit(c)
	}
	for {
		if len(buf) == cap(buf) && cut < 0 {
			if cap(buf) >= limit {
				send(&chunk{tail: io.MultiReader(bytes.NewReader(buf), r), line: bufLine})
				return
			}
			buf = slices.Grow(buf, cap(buf))
		}
		want := cap(buf) - len(buf)
		n, err := r.Read(buf[len(buf):cap(buf)])
		buf = buf[:len(buf)+n]
		if c, l := sc.scan(buf[scanned:]); c >= 0 {
			cut, cutLine = scanned+c, l
		}
		scanned = len(buf)
		if err != nil {
			if err == io.EOF {
				if len(buf) > 0 {
					send(&chunk{data: buf, line: bufLine})
				}
				return
			}
			// The values before the error are processed as usual, then
			// the decoder reports it.
			send(&chunk{tail: io.MultiReader(bytes.NewReader(buf), errorReader{err}), line: bufLine})
			return
		}
		if cut >= 0 && (len(buf) == cap(buf) || n < want) {
			rest := buf[cut:]
			next := make([]byte, len(rest), max(target, 2*len(rest)))
			copy(next, rest)
			if !send(&chunk{data: buf[:cut], line: bufLine}) {
				return
			}
			buf, scanned, bufLine, cut = next, len(rest), cutLine, -1
		}
	}
}

type errorReader struct{ err error }

func (e errorReader) Read([]byte) (int, error) { return 0, e.err }

// splitScanner finds newlines at nesting depth 0 outside strings, and
// counts lines the way the Decoder does.
type splitScanner struct {
	depth      int
	inStr, esc bool
	line       int // line number at the scan position
}

// scan continues over b and returns the offset just past its last depth-0
// newline and the line number there, or -1.
func (s *splitScanner) scan(b []byte) (cut, line int) {
	cut = -1
	for i := 0; i < len(b); i++ {
		c := b[i]
		if s.inStr {
			switch {
			case s.esc:
				s.esc = false
			case c == '\\':
				s.esc = true
			case c == '"':
				s.inStr = false
			case c == '\n':
				s.line++ // jq accepts raw newlines in strings
			default:
				// Skip ahead to the next byte that matters.
				if j := bytes.IndexAny(b[i:], "\"\\\n"); j > 0 {
					i += j - 1
				} else if j < 0 {
					i = len(b)
				}
			}
			continue
		}
		switch c {
		case '"':
			s.inStr = true
		case '[', '{':
			s.depth++
		case ']', '}':
			s.depth--
		case '\n':
			s.line++
			if s.depth == 0 {
				cut, line = i+1, s.line
			}
		}
	}
	return cut, line
}
