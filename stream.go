package jqgo

import (
	"context"
	"errors"
	"io"
	"iter"
	"slices"
)

// Streaming large inputs.
//
// A query like `.items[] | select(.price > 10)` needs the elements of one
// array and nothing else from its input. When the input is read from a
// Reader, jqgo can hand those elements to the rest of the query as they
// are parsed, skipping everything else without building it, so memory
// stays proportional to one element instead of the whole document.
//
// That is only correct when the query uses its input exactly once, and
// only through that iteration. analyzeStream checks this on the AST: the
// input must reach a `PATH[]` (PATH made of literal field names) through
// nodes that evaluate that branch once, and every other branch that sees
// the input must ignore it. It then puts a streamNode in place of the
// iteration; when not streaming, a streamNode just evaluates the original
// iteration, so Run is unaffected.

type streamNode struct {
	orig *iterateNode
	path []string
}

func (*streamNode) isNode() {}

// analyzeStream installs a streamNode in root if the query qualifies and
// returns it.
func analyzeStream(root *node) *streamNode {
	if calls(*root, "input", "inputs") {
		return nil
	}
	return streamPoint(*root, func(n node) { *root = n })
}

// streamPoint looks for the stream point in n, which is evaluated once
// with the query's input as its input; set replaces n in its parent.
func streamPoint(n node, set func(node)) *streamNode {
	switch n := n.(type) {
	case *iterateNode:
		if path, ok := literalPath(n.term); ok {
			s := &streamNode{orig: n, path: path}
			set(s)
			return s
		}
	case *pipeNode:
		// The right side sees the left side's outputs, never the input.
		return streamPoint(n.left, func(x node) { n.left = x })
	case *commaNode:
		if ignoresInput(n.right) {
			return streamPoint(n.left, func(x node) { n.left = x })
		}
		if ignoresInput(n.left) {
			return streamPoint(n.right, func(x node) { n.right = x })
		}
	case *arrayNode:
		if n.x != nil {
			return streamPoint(n.x, func(x node) { n.x = x })
		}
	case *reduceNode:
		// init must produce exactly one value, or src runs more than once.
		if isSimple(n.init) && ignoresInput(n.init) {
			return streamPoint(n.src, func(x node) { n.src = x })
		}
	case *foreachNode:
		if isSimple(n.init) && ignoresInput(n.init) {
			return streamPoint(n.src, func(x node) { n.src = x })
		}
	case *tryNode:
		return streamPoint(n.body, func(x node) { n.body = x })
	case *labelNode:
		return streamPoint(n.body, func(x node) { n.body = x })
	case *funcDefNode:
		// A definition's body sees its caller's input, not the query's.
		return streamPoint(n.rest, func(x node) { n.rest = x })
	case *callNode:
		if n.lexical || n.global == nil || n.global.gen == nil {
			return nil
		}
		arg := -1
		switch {
		case n.name == "first" && len(n.args) == 1, n.name == "isempty" && len(n.args) == 1:
			arg = 0
		case n.name == "limit" && len(n.args) == 2 && isSimple(n.args[0]) && ignoresInput(n.args[0]):
			arg = 1
		}
		if arg >= 0 {
			return streamPoint(n.args[arg], func(x node) {
				n.args[arg] = x
				n.argDefs[arg].body = x
			})
		}
	}
	return nil
}

// literalPath recognizes ., .a, .a.b, .["a"] ...
func literalPath(n node) ([]string, bool) {
	switch n := n.(type) {
	case identityNode:
		return nil, true
	case *indexNode:
		key := n.name
		if n.key != nil {
			lit, ok := n.key.(literalNode)
			if !ok {
				return nil, false
			}
			s, ok := lit.v.(string)
			if !ok {
				return nil, false
			}
			key = s
		}
		prefix, ok := literalPath(n.term)
		if !ok {
			return nil, false
		}
		return append(prefix, key), true
	}
	return nil, false
}

// ignoresInput reports (conservatively) that n never reads its input.
func ignoresInput(n node) bool {
	switch n := n.(type) {
	case nil, literalNode, textNode, *varNode, *breakNode:
		return true
	case *arrayNode:
		return ignoresInput(n.x)
	case *objectNode:
		for _, ent := range n.entries {
			if !ignoresInput(ent.key) || !ignoresInput(ent.value) {
				return false
			}
		}
		return true
	case *stringNode:
		for _, p := range n.parts {
			if !ignoresInput(p) {
				return false
			}
		}
		return true
	case *pipeNode:
		return ignoresInput(n.left)
	case *commaNode:
		return ignoresInput(n.left) && ignoresInput(n.right)
	case *binopNode:
		return ignoresInput(n.left) && ignoresInput(n.right)
	case *andNode:
		return ignoresInput(n.left) && ignoresInput(n.right)
	case *orNode:
		return ignoresInput(n.left) && ignoresInput(n.right)
	case *negNode:
		return ignoresInput(n.x)
	case *callNode:
		return !n.lexical && len(n.args) == 0 && (n.name == "empty" || n.name == "now")
	}
	return false
}

// calls reports calls to any of the named functions (whatever their
// arity) anywhere in n. input and inputs read the next top-level value,
// which a stream is in the middle of.
func calls(n node, names ...string) bool {
	found := false
	walk(n, func(x node) {
		if c, ok := x.(*callNode); ok && !c.lexical && slices.Contains(names, c.name) {
			found = true
		}
	})
	return found
}

// walk visits every node of the tree.
func walk(n node, f func(node)) {
	if n == nil {
		return
	}
	f(n)
	kids := func(ns ...node) {
		for _, x := range ns {
			walk(x, f)
		}
	}
	switch n := n.(type) {
	case *indexNode:
		kids(n.term, n.key)
	case *sliceNode:
		kids(n.term, n.from, n.to)
	case *iterateNode:
		kids(n.term)
	case *pipeNode:
		kids(n.left, n.right)
	case *commaNode:
		kids(n.left, n.right)
	case *negNode:
		kids(n.x)
	case *binopNode:
		kids(n.left, n.right)
	case *andNode:
		kids(n.left, n.right)
	case *orNode:
		kids(n.left, n.right)
	case *altNode:
		kids(n.left, n.right)
	case *assignNode:
		kids(n.left, n.right)
	case *ifNode:
		kids(n.cond, n.then, n.els)
	case *tryNode:
		kids(n.body, n.catch)
	case *reduceNode:
		kids(n.src, n.init, n.update)
	case *foreachNode:
		kids(n.src, n.init, n.update, n.extract)
	case *funcDefNode:
		kids(n.def.body, n.rest)
	case *callNode:
		kids(n.args...)
	case *bindNode:
		kids(n.src, n.body)
	case *labelNode:
		kids(n.body)
	case *arrayNode:
		kids(n.x)
	case *objectNode:
		for _, ent := range n.entries {
			kids(ent.key, ent.value)
		}
	case *stringNode:
		kids(n.parts...)
	case *streamNode:
		kids(n.orig)
	}
}

// streamSource feeds a streamNode from a Decoder positioned at the start
// of a top-level value.
type streamSource struct {
	d         *Decoder
	started   bool
	readErr   error // a syntax or read error: the stream is unusable after it
	abandoned bool  // the caller stopped iterating
}

// over reports that nothing more will be read: the rest of the value is
// only skipped when the query stopped by itself (first, limit, an error),
// since the next top-level value comes after it.
func (s *streamSource) over(err error) bool {
	return s.readErr != nil || s.abandoned || isCancel(err) || isHalt(err)
}

// fatal records a decoder error; emission errors come from the query.
func (s *streamSource) fatal(err error) error {
	if s.readErr == nil {
		s.readErr = err
	}
	return err
}

func (s *streamSource) run(e *evaluator, n *streamNode, out emitFunc) error {
	if s.started {
		return errors.New("jqgo: internal error: streamed input used twice")
	}
	s.started = true
	c, err := s.d.start()
	if err != nil {
		return s.fatal(err)
	}
	return s.walkPath(e, c, n.path, out)
}

// walkPath consumes the value starting with c (already consumed), emitting
// the elements found at path. It always consumes the whole value, even
// when the query stops early, so the next top-level value can be read.
func (s *streamSource) walkPath(e *evaluator, c byte, path []string, out emitFunc) error {
	d := s.d
	if len(path) == 0 {
		return s.iterateValue(e, c, out)
	}
	if c != '{' {
		v, err := s.scalarOrSkip(c)
		if err != nil {
			return err
		}
		if v == nil {
			// null.a is null all the way down, and iterating null fails
			return errIterate(nil)
		}
		return errIndex(v, path[0])
	}
	var result error
	found := false
	c, err := d.next()
	if err != nil {
		return s.fatal(err)
	}
	for c != '}' {
		if c != '"' {
			return s.fatal(d.errorf("Object keys must be strings"))
		}
		k, err := d.strBytes()
		if err != nil {
			return s.fatal(err)
		}
		match := !found && string(k) == path[0]
		if c, err = d.next(); err != nil {
			return s.fatal(err)
		}
		if c != ':' {
			return s.fatal(d.errorf("Objects must consist of key:value pairs"))
		}
		if c, err = d.next(); err != nil {
			return s.fatal(err)
		}
		if match {
			found = true
			result = s.walkPath(e, c, path[1:], out)
			if s.over(result) {
				return result
			}
		} else if err := d.skip(c); err != nil {
			return s.fatal(err)
		}
		if c, err = d.next(); err != nil {
			return s.fatal(err)
		}
		if c == ',' {
			if c, err = d.next(); err != nil {
				return s.fatal(err)
			}
		} else if c != '}' {
			return s.fatal(d.errorf("Expected separator between values"))
		}
	}
	if !found {
		return errIterate(nil) // a missing key is null
	}
	return result
}

// iterateValue emits the elements of the value starting with c.
func (s *streamSource) iterateValue(e *evaluator, c byte, out emitFunc) error {
	d := s.d
	switch c {
	case '[':
		var result error
		c, err := d.next()
		if err != nil {
			return s.fatal(err)
		}
		for c != ']' {
			if result == nil {
				if err := e.tick(); err != nil {
					return err
				}
				v, err := d.value(c)
				if err != nil {
					return s.fatal(err)
				}
				result = out(v, nil)
				if s.over(result) {
					return result
				}
			} else if err := d.skip(c); err != nil {
				// the query has stopped; just consume the rest
				return s.fatal(err)
			}
			if c, err = d.next(); err != nil {
				return s.fatal(err)
			}
			if c == ',' {
				if c, err = d.next(); err != nil {
					return s.fatal(err)
				}
			} else if c != ']' {
				return s.fatal(d.errorf("Expected separator between values"))
			}
		}
		return result
	case '{':
		// Object values come out in key order, so the object is needed whole.
		v, err := d.value(c)
		if err != nil {
			return s.fatal(err)
		}
		return e.iterate(v, nil, out)
	}
	v, err := d.value(c)
	if err != nil {
		return s.fatal(err)
	}
	return errIterate(v)
}

// scalarOrSkip consumes a value that is not an object: scalars are
// decoded (errors mention them), arrays are skipped and reported as an
// empty array (errors only mention their type).
func (s *streamSource) scalarOrSkip(c byte) (any, error) {
	if c == '[' {
		if err := s.d.skip(c); err != nil {
			return nil, s.fatal(err)
		}
		return []any{}, nil
	}
	v, err := s.d.value(c)
	if err != nil {
		return nil, s.fatal(err)
	}
	return v, nil
}

// finish consumes the top-level value if the query never reached it.
func (s *streamSource) finish() error {
	if s.started || s.readErr != nil {
		return s.readErr
	}
	s.started = true
	c, err := s.d.start()
	if err != nil {
		return s.fatal(err)
	}
	if err := s.d.skip(c); err != nil {
		return s.fatal(err)
	}
	return nil
}

func isCancel(err error) bool {
	return err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

func isHalt(err error) bool {
	var pe *passError
	for errors.As(err, &pe) {
		err = pe.err
	}
	var he *HaltError
	return errors.As(err, &he)
}

// Streamable reports whether RunReader can process a large top-level
// value without loading it: the query uses its input once, through an
// iteration such as .[] or .items[] (see the README).
func (q *Query) Streamable() bool { return q.stream != nil }

// RunReader runs the query on every JSON value read from r, the way the
// jq command line does, and yields all results in order.
//
// If the query is Streamable, each value is processed while it is being
// parsed: only the iterated elements are built, one at a time, so a
// multi-gigabyte array needs about as much memory as one element. Other
// queries get each value fully decoded, as with Run; input and inputs
// then read the following values.
//
// A runtime error is yielded as a *LineError and processing continues
// with the next value, like jq. A syntax or read error is yielded and ends the
// iteration. If r has a Name method, as *os.File does, input_filename
// returns it. When streaming, results from a value can come before a
// syntax error later in that same value, and of duplicate keys on the
// streamed path the first one is used (jq's own parser keeps the last).
func (q *Query) RunReader(ctx context.Context, r io.Reader, vars ...any) iter.Seq2[any, error] {
	return func(yield func(any, error) bool) {
		q.readLoop(ctx, NewDecoder(r), r, vars, yield)
	}
}

// readLoop runs q on each value d reads; r is only asked for its name. It
// reports whether it reached the end of the input, as opposed to stopping
// because yield did, or on halt, a syntax or read error, or cancellation.
func (q *Query) readLoop(ctx context.Context, d *Decoder, r io.Reader, vars []any, yield func(any, error) bool) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	inputs := decoderInputs{d, r}
	emit := func(v any, err error) bool {
		if err != nil && !isHalt(err) {
			err = &LineError{Line: d.line, Err: err}
		}
		return yield(v, err)
	}
	for {
		if q.stream != nil {
			if _, ok := d.skipSpaceAfterBOM(); !ok {
				if d.rerr != nil {
					yield(nil, d.rerr)
					return false
				}
				return true
			}
			src := &streamSource{d: d}
			for v, err := range q.exec(ctx, nil, inputs, evalOptions{stream: src}, vars) {
				if src.readErr != nil {
					break // yielded below
				}
				if !emit(v, err) || isHalt(err) {
					src.abandoned = true
					return false
				}
			}
			if err := src.finish(); err != nil {
				yield(nil, err)
				return false
			}
		} else {
			v, err := d.Decode()
			if err == io.EOF {
				return true
			}
			if err != nil {
				yield(nil, err)
				return false
			}
			for v, err := range q.exec(ctx, v, inputs, evalOptions{}, vars) {
				if !emit(v, err) || isHalt(err) {
					return false
				}
			}
		}
		if isCancel(ctx.Err()) {
			return false
		}
	}
}

// decoderInputs lets input and inputs read the values that follow.
type decoderInputs struct {
	d *Decoder
	r io.Reader
}

func (in decoderInputs) Next() (any, error) { return in.d.Decode() }

// Filename implements input_filename for readers that know their name.
func (in decoderInputs) Filename() any {
	if f, ok := in.r.(interface{ Name() string }); ok {
		return f.Name()
	}
	return nil
}
