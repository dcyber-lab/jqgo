package jqgo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"
)

// RunReaderParallel must give exactly what RunReader gives: same results
// in the same order, same errors (syntax errors included, with their line
// numbers).

func parallelRecord(q *Query, r io.Reader, target, limit int) runRecord {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var rec runRecord
	q.runParallel(ctx, r, 3, target, limit, nil, func(v any, err error) bool {
		if err != nil {
			rec.addErr(err)
			return true
		}
		rec.outputs = append(rec.outputs, toJSON(v))
		return len(rec.outputs) <= 200
	})
	return rec
}

func checkParallel(t *testing.T, q *Query, program, text string) {
	t.Helper()
	want := readerRecord(q, strings.NewReader(text))
	for _, size := range [][2]int{{1, 1 << 20}, {7, 40}, {64, 1 << 20}, {1 << 20, 1 << 20}} {
		for _, r := range []io.Reader{strings.NewReader(text), iotest.OneByteReader(strings.NewReader(text)), iotest.HalfReader(strings.NewReader(text))} {
			got := parallelRecord(q, r, size[0], size[1])
			if inconclusive(want, got) {
				return
			}
			if strings.Join(got.outputs, "\n") != strings.Join(want.outputs, "\n") || got.err != want.err || got.lines != want.lines {
				t.Fatalf("%s on %q (chunks %v):\n parallel:   %v %q at %s\n sequential: %v %q at %s",
					program, text, size, got.outputs, got.err, got.lines, want.outputs, want.err, want.lines)
			}
		}
	}
}

// layouts turns one input into several multi-value, multi-line streams.
func layouts(in string) []string {
	var pretty string
	if v, err := parseJSON([]byte(in)); err == nil {
		pretty = string(MarshalWith(v, EncodeOptions{Indent: 2}))
	}
	return []string{
		in + "\n" + in + "\n" + in,
		pretty + "\n" + in + "\n\n" + pretty + "\n",
		"\xef\xbb\xbf" + in + "\n" + in,          // BOM
		in + "\n" + in + "\n{\"a\":\n1,]\n" + in, // syntax error on line 4
		in + "\n[\"x\n\"]\n" + in,                // raw newline in a string
		in + " " + in + "\n\t" + in + "\n",
	}
}

func TestParallelEquivalence(t *testing.T) {
	for _, p := range streamPrograms {
		q := MustCompile(p, WithDebugWriter(nil))
		for _, in := range streamInputs {
			for _, text := range layouts(in) {
				checkParallel(t, q, p, text)
			}
		}
	}
}

func TestParallelSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("slow")
	}
	for _, file := range []string{"jq/jq.test", "jq/man.test", "extra.test"} {
		for _, c := range readJQTests(t, filepath.Join("testdata", file)) {
			if c.fail {
				continue
			}
			q, err := Compile(c.program, WithDebugWriter(nil), WithEnviron([]string{"PAGER=less"}))
			if err != nil || !q.parallel {
				continue
			}
			for _, text := range layouts(c.input)[:2] {
				checkParallel(t, q, c.program, text)
			}
		}
	}
}

func FuzzParallel(f *testing.F) {
	for i, p := range streamPrograms {
		f.Add(p, streamInputs[i%len(streamInputs)]+"\n"+streamInputs[(i+1)%len(streamInputs)])
	}
	f.Add(`.`, "1\n[2,\n3]\n\"x\\\"\n\" {\"a\":\n{}}\n")
	f.Add(`.a`, "{\"a\":1}\n{\"a\":[\n\"]\\n\"]}\ntru\ne\n")
	f.Fuzz(func(t *testing.T, program, input string) {
		q, err := Compile(program, WithDebugWriter(nil))
		if err != nil || !q.parallel {
			return
		}
		checkParallel(t, q, program, input)
	})
}

func TestParallelFallsBack(t *testing.T) {
	for _, p := range []string{`[., input]`, `[inputs]`, `debug`, `debug("x")`, `stderr`} {
		if q := MustCompile(p, WithDebugWriter(nil)); q.parallel {
			t.Errorf("%s runs in parallel", p)
		}
	}
	// And they still see the following values.
	q := MustCompile(`[., input]`)
	got := readerRecord(q, strings.NewReader("1\n2\n3\n4\n"))
	if strings.Join(got.outputs, " ") != "[1,2] [3,4]" {
		t.Errorf("got %v", got.outputs)
	}
}

// failAfter returns data, then err.
type failAfter struct {
	data string
	err  error
}

func (f *failAfter) Read(p []byte) (int, error) {
	if f.data == "" {
		return 0, f.err
	}
	n := copy(p, f.data)
	f.data = f.data[n:]
	return n, nil
}

func TestParallelReadError(t *testing.T) {
	boom := errors.New("boom")
	q := MustCompile(`.a`)
	text := strings.Repeat("{\"a\":1}\n", 100) + "{\"a\":2"
	want := readerRecord(q, &failAfter{text, boom})
	got := parallelRecord(q, &failAfter{text, boom}, 100, 1<<20)
	if fmt.Sprint(got) != fmt.Sprint(want) || !strings.HasSuffix(got.err, "boom;") {
		t.Errorf("got %v, want %v", got, want)
	}
}

// lines produces NDJSON forever.
type lines struct{ n atomic.Int64 }

func (l *lines) Read(p []byte) (int, error) {
	i := 0
	for i+32 < len(p) {
		i += copy(p[i:], fmt.Sprintf("{\"id\":%d}\n", l.n.Load()))
		l.n.Add(1)
	}
	return i, nil
}

func TestParallelEarlyExit(t *testing.T) {
	q := MustCompile(`.id`)
	r := &lines{}
	n := 0
	for v, err := range q.RunReaderParallel(context.Background(), r, 4) {
		if err != nil || v != n {
			t.Fatalf("result %d: %v %v", n, v, err)
		}
		if n++; n == 50_000 {
			break
		}
	}
	// Returning means every worker has stopped; the splitter stops at its
	// next send, a few chunks later.
	time.Sleep(10 * time.Millisecond)
	if read := r.n.Load(); read > 50_000+20*chunkTarget/10 {
		t.Errorf("read %d records", read)
	}
}

func TestParallelCancel(t *testing.T) {
	q := MustCompile(`select(false)`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var last error
	for _, err := range q.RunReaderParallel(ctx, &lines{}, 4) {
		last = err
	}
	if !errors.Is(last, context.DeadlineExceeded) {
		t.Errorf("got %v, want a deadline error", last)
	}
}

func TestParallelHalt(t *testing.T) {
	q := MustCompile(`if . == 3 then halt_error else . end`)
	var got []any
	var last error
	for v, err := range q.RunReaderParallel(context.Background(), strings.NewReader("1\n2\n3\n4\n5\n"), 4) {
		if err != nil {
			last = err
			continue
		}
		got = append(got, v)
	}
	var he *HaltError
	if fmt.Sprint(got) != "[1 2]" || !errors.As(last, &he) {
		t.Errorf("got %v, %v", got, last)
	}
}

func TestParallelHugeValue(t *testing.T) {
	// A value longer than the limit switches to sequential, streamed
	// processing for the rest.
	if testing.Short() {
		t.Skip("reads 200 MB")
	}
	q := MustCompile(`.items[] | select(.id % 500000 == 0) | .name`)
	r := io.MultiReader(strings.NewReader("{\"items\":[{\"id\":0,\"name\":\"first\"}]}\n"), &bigArray{n: 1_500_000})
	var got []any
	for v, err := range q.RunReaderParallel(context.Background(), r, 4) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if fmt.Sprint(got) != "[first item-0 item-500000 item-1000000]" {
		t.Errorf("got %v", got)
	}
}
