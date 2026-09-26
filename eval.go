package jqgo

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// The evaluator is continuation-passing: every node pushes each of its
// outputs into an emit callback. Backtracking falls out of ordinary Go
// control flow, and stopping early (limit, first, label/break, a consumer
// that stops ranging) is just returning an error up the stack.
//
// When p is non-nil the evaluator is tracking paths (for path(f), |=, del
// and friends): every emitted value carries the path it was found at.

type pathT struct {
	parent  *pathT
	key     any
	invalid bool // value was computed, not located; value holds it
	value   any
}

var rootPath = &pathT{}

type emitFunc func(v any, p *pathT) error

type envT struct {
	parent *envT
	kind   byte // 'v' variable, 'f' function, 'l' label
	name   string
	arity  int
	value  any
	fn     *closure
	label  *labelT
}

type closure struct {
	def *funcDef
	env *envT
}

type labelT struct{ name string }

// breakError unwinds to the label it names; it cannot be caught by try.
type breakError struct{ label *labelT }

func (e *breakError) Error() string { return "break" }

// stopError is how a native generator stops its argument early.
type stopError struct{}

func (e *stopError) Error() string { return "stop" }

// passError wraps an error raised downstream of a try body so the try can
// tell it apart from errors raised by the body itself.
type passError struct {
	err error
	tok *byte
}

func (e *passError) Error() string { return e.err.Error() }

var errDepth = errors.New("jqgo: maximum recursion depth exceeded")

const maxDepth = 100000

type evaluator struct {
	ctx    context.Context
	q      *Query
	vals   []any // values of q.vars, in order
	inputs Inputs
	steps  int
	depth  int

	evalOptions
}

// catchable reports whether try/catch, ? and // may intercept err.
func catchable(err error) bool {
	switch err.(type) {
	case *breakError, *stopError, *passError, *HaltError:
		return false
	}
	return err != errDepth && err != context.Canceled && err != context.DeadlineExceeded &&
		!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
}

// errorValue is what catch receives for err.
func errorValue(err error) any {
	var ve *ValueError
	if errors.As(err, &ve) {
		return ve.Value
	}
	return err.Error()
}

func emitValue(out emitFunc, v any, p *pathT) error {
	if p != nil {
		return out(v, &pathT{invalid: true, value: v})
	}
	return out(v, nil)
}

func (p *pathT) extend(key any) (*pathT, error) {
	if p == nil {
		return nil, nil
	}
	if p.invalid {
		return nil, errPathAccess(key, p.value)
	}
	return &pathT{parent: p, key: key}, nil
}

func (p *pathT) toArray() ([]any, error) {
	if p.invalid {
		return nil, errPathResult(p.value)
	}
	n := 0
	for x := p; x.parent != nil; x = x.parent {
		n++
	}
	arr := make([]any, n)
	for x := p; x.parent != nil; x = x.parent {
		n--
		arr[n] = x.key
	}
	return arr, nil
}

// tick counts a unit of work and checks for cancellation every 1024 of
// them. Loops that emit without re-entering run (iteration, range, ..)
// call it too, or a big fan-out could run for a long time unchecked.
func (e *evaluator) tick() error {
	e.steps++
	if e.steps&0x3ff == 0 {
		return e.ctx.Err()
	}
	return nil
}

func (e *evaluator) run(n node, env *envT, v any, p *pathT, out emitFunc) error {
	if err := e.tick(); err != nil {
		return err
	}
	if p == nil && !e.noFastPath && isSimple(n) {
		r, err := e.eval1(n, env, v)
		if err != nil {
			return err
		}
		return out(r, nil)
	}
	e.depth++
	if e.depth > maxDepth {
		e.depth--
		return errDepth
	}
	err := e.dispatch(n, env, v, p, out)
	e.depth--
	return err
}

func (e *evaluator) dispatch(n node, env *envT, v any, p *pathT, out emitFunc) error {
	switch n := n.(type) {
	case identityNode:
		return out(v, p)
	case literalNode:
		return emitValue(out, n.v, p)
	case *indexNode:
		return e.evalIndex(n, env, v, p, out)
	case *pipeNode:
		return e.run(n.left, env, v, p, func(x any, xp *pathT) error {
			return e.run(n.right, env, x, xp, out)
		})
	case *commaNode:
		if err := e.run(n.left, env, v, p, out); err != nil {
			return err
		}
		return e.run(n.right, env, v, p, out)
	case *callNode:
		return e.call(n, env, v, p, out)
	case *varNode:
		val, err := e.lookupVar(env, n.name)
		if err != nil {
			return err
		}
		return emitValue(out, val, p)
	case *iterateNode:
		return e.run(n.term, env, v, p, func(x any, xp *pathT) error {
			return e.iterate(x, xp, out)
		})
	case *sliceNode:
		return e.evalSlice(n, env, v, p, out)
	case recurseAllNode:
		return e.recurseAll(v, p, out)
	case *binopNode:
		return e.run(n.right, env, v, nil, func(r any, _ *pathT) error {
			return e.run(n.left, env, v, nil, func(l any, _ *pathT) error {
				res, err := binop(n.op, l, r)
				if err != nil {
					return err
				}
				return emitValue(out, res, p)
			})
		})
	case *andNode:
		return e.run(n.left, env, v, nil, func(l any, _ *pathT) error {
			if !truthy(l) {
				return emitValue(out, false, p)
			}
			return e.run(n.right, env, v, nil, func(r any, _ *pathT) error {
				return emitValue(out, truthy(r), p)
			})
		})
	case *orNode:
		return e.run(n.left, env, v, nil, func(l any, _ *pathT) error {
			if truthy(l) {
				return emitValue(out, true, p)
			}
			return e.run(n.right, env, v, nil, func(r any, _ *pathT) error {
				return emitValue(out, truthy(r), p)
			})
		})
	case *negNode:
		return e.run(n.x, env, v, nil, func(x any, _ *pathT) error {
			r, err := negate(x)
			if err != nil {
				return err
			}
			return emitValue(out, r, p)
		})
	case *altNode:
		return e.evalAlt(n, env, v, p, out)
	case *ifNode:
		return e.run(n.cond, env, v, nil, func(c any, _ *pathT) error {
			if truthy(c) {
				return e.run(n.then, env, v, p, out)
			}
			if n.els == nil {
				return out(v, p)
			}
			return e.run(n.els, env, v, p, out)
		})
	case *tryNode:
		return e.evalTry(n, env, v, p, out)
	case *arrayNode:
		arr := []any{}
		if n.x != nil {
			err := e.run(n.x, env, v, nil, func(x any, _ *pathT) error {
				arr = append(arr, x)
				return nil
			})
			if err != nil {
				return err
			}
		}
		return emitValue(out, arr, p)
	case *objectNode:
		return e.evalObject(n, env, v, p, out)
	case *stringNode:
		return e.evalString(n, env, v, p, out)
	case *formatNode:
		s, err := applyFormat(n.name, v)
		if err != nil {
			return err
		}
		return emitValue(out, s, p)
	case *funcDefNode:
		env2 := &envT{parent: env, kind: 'f', name: n.def.name, arity: len(n.def.params)}
		env2.fn = &closure{def: n.def, env: env2}
		return e.run(n.rest, env2, v, p, out)
	case *bindNode:
		return e.evalBind(n, env, v, p, out)
	case *reduceNode:
		return e.evalReduce(n, env, v, p, out)
	case *foreachNode:
		return e.evalForeach(n, env, v, p, out)
	case *labelNode:
		l := &labelT{name: n.name}
		err := e.run(n.body, &envT{parent: env, kind: 'l', name: n.name, label: l}, v, p, out)
		if be, ok := err.(*breakError); ok && be.label == l {
			return nil
		}
		return err
	case *breakNode:
		for x := env; x != nil; x = x.parent {
			if x.kind == 'l' && x.name == n.name {
				return &breakError{label: x.label}
			}
		}
		return fmt.Errorf("$*label-%s is not defined", n.name)
	case *assignNode:
		return e.evalAssign(n, env, v, p, out, nil)
	}
	return fmt.Errorf("jqgo: unknown node %T", n)
}

func (e *evaluator) lookupVar(env *envT, name string) (any, error) {
	for x := env; x != nil; x = x.parent {
		if x.kind == 'v' && x.name == name {
			return x.value, nil
		}
	}
	for i, n := range e.q.vars {
		if n == name {
			return e.vals[i], nil
		}
	}
	switch name {
	case "ENV":
		return e.q.environ, nil
	case "__prog_args":
		named := make(map[string]any, len(e.q.vars))
		for i, n := range e.q.vars {
			named[n] = e.vals[i]
		}
		return map[string]any{"positional": []any{}, "named": named}, nil
	}
	return nil, fmt.Errorf("$%s is not defined", name)
}

func (e *evaluator) evalIndex(n *indexNode, env *envT, v any, p *pathT, out emitFunc) error {
	if n.key == nil {
		key := n.name
		return e.run(n.term, env, v, p, func(t any, tp *pathT) error {
			return indexEmit(t, key, tp, out)
		})
	}
	// Keys come from the original input; the key loop is the outer one.
	return e.run(n.key, env, v, nil, func(k any, _ *pathT) error {
		return e.run(n.term, env, v, p, func(t any, tp *pathT) error {
			return indexEmit(t, k, tp, out)
		})
	})
}

func indexEmit(t, k any, tp *pathT, out emitFunc) error {
	if tp != nil && tp.invalid {
		return errPathAccess(k, tp.value)
	}
	r, err := index(t, k)
	if err != nil {
		return err
	}
	np, _ := tp.extend(k)
	return out(r, np)
}

func (e *evaluator) evalSlice(n *sliceNode, env *envT, v any, p *pathT, out emitFunc) error {
	emit := func(from, to any) error {
		return e.run(n.term, env, v, p, func(t any, tp *pathT) error {
			key := map[string]any{"start": from, "end": to}
			if tp != nil && tp.invalid {
				return errPathAccess(key, tp.value)
			}
			r, err := index(t, key)
			if err != nil {
				return err
			}
			np, _ := tp.extend(key)
			return out(r, np)
		})
	}
	withTo := func(from any) error {
		if n.to == nil {
			return emit(from, nil)
		}
		return e.run(n.to, env, v, nil, func(to any, _ *pathT) error { return emit(from, to) })
	}
	if n.from == nil {
		return withTo(nil)
	}
	return e.run(n.from, env, v, nil, func(from any, _ *pathT) error { return withTo(from) })
}

func (e *evaluator) iterate(x any, xp *pathT, out emitFunc) error {
	if xp != nil && xp.invalid {
		return errPathIterate(xp.value)
	}
	switch x := x.(type) {
	case []any:
		for i, el := range x {
			if err := e.tick(); err != nil {
				return err
			}
			el, err := norm(el)
			if err != nil {
				return err
			}
			var np *pathT
			if xp != nil {
				np = &pathT{parent: xp, key: i}
			}
			if err := out(el, np); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for _, k := range sortedKeys(x) {
			if err := e.tick(); err != nil {
				return err
			}
			el, err := norm(x[k])
			if err != nil {
				return err
			}
			var np *pathT
			if xp != nil {
				np = &pathT{parent: xp, key: k}
			}
			if err := out(el, np); err != nil {
				return err
			}
		}
		return nil
	}
	return errIterate(x)
}

// recurseAll implements "..", i.e. recurse(.[]?).
func (e *evaluator) recurseAll(v any, p *pathT, out emitFunc) error {
	if err := out(v, p); err != nil {
		return err
	}
	if p != nil && p.invalid {
		return nil
	}
	switch x := v.(type) {
	case []any:
		for i, el := range x {
			if err := e.tick(); err != nil {
				return err
			}
			el, err := norm(el)
			if err != nil {
				return err
			}
			var np *pathT
			if p != nil {
				np = &pathT{parent: p, key: i}
			}
			if err := e.recurseAll(el, np, out); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, k := range sortedKeys(x) {
			if err := e.tick(); err != nil {
				return err
			}
			el, err := norm(x[k])
			if err != nil {
				return err
			}
			var np *pathT
			if p != nil {
				np = &pathT{parent: p, key: k}
			}
			if err := e.recurseAll(el, np, out); err != nil {
				return err
			}
		}
	}
	return nil
}

func passThrough(out emitFunc, tok *byte) emitFunc {
	return func(v any, p *pathT) error {
		if err := out(v, p); err != nil {
			return &passError{err: err, tok: tok}
		}
		return nil
	}
}

// unwrapPass returns (inner, true) if err came from downstream of the
// body that was wrapped with tok.
func unwrapPass(err error, tok *byte) (error, bool) {
	if pe, ok := err.(*passError); ok && pe.tok == tok {
		return pe.err, true
	}
	return nil, false
}

func (e *evaluator) evalTry(n *tryNode, env *envT, v any, p *pathT, out emitFunc) error {
	tok := new(byte)
	err := e.run(n.body, env, v, p, passThrough(out, tok))
	if err == nil {
		return nil
	}
	if inner, ok := unwrapPass(err, tok); ok {
		return inner
	}
	if !catchable(err) {
		return err
	}
	if n.catch == nil {
		return nil
	}
	return e.run(n.catch, env, errorValue(err), nil, func(x any, _ *pathT) error {
		return emitValue(out, x, p)
	})
}

// evalAlt is a // b. Errors on the left propagate (jq 1.7 behaviour);
// b runs only if a produced nothing but false and null.
func (e *evaluator) evalAlt(n *altNode, env *envT, v any, p *pathT, out emitFunc) error {
	found := false
	err := e.run(n.left, env, v, p, func(x any, xp *pathT) error {
		if !truthy(x) {
			return nil
		}
		found = true
		return out(x, xp)
	})
	if err != nil || found {
		return err
	}
	return e.run(n.right, env, v, p, out)
}

func (e *evaluator) evalObject(n *objectNode, env *envT, v any, p *pathT, out emitFunc) error {
	obj := make(map[string]any, len(n.entries))
	var build func(i int) error
	build = func(i int) error {
		if i == len(n.entries) {
			return emitValue(out, cloneMap(obj, 0), p)
		}
		ent := n.entries[i]
		return e.run(ent.key, env, v, nil, func(k any, _ *pathT) error {
			ks, err := objectKey(k)
			if err != nil {
				return err
			}
			return e.run(ent.value, env, v, nil, func(val any, _ *pathT) error {
				old, had := obj[ks]
				obj[ks] = val
				err := build(i + 1)
				if had {
					obj[ks] = old
				} else {
					delete(obj, ks)
				}
				return err
			})
		})
	}
	return build(0)
}

func (e *evaluator) evalString(n *stringNode, env *envT, v any, p *pathT, out emitFunc) error {
	parts := make([]string, len(n.parts))
	// Later interpolations vary slowest, as in jq (string addition
	// evaluates its right operand first).
	var build func(i int) error
	build = func(i int) error {
		if i < 0 {
			size := 0
			for _, s := range parts {
				size += len(s)
			}
			buf := make([]byte, 0, size)
			for _, s := range parts {
				buf = append(buf, s...)
			}
			return emitValue(out, string(buf), p)
		}
		if t, ok := n.parts[i].(textNode); ok {
			parts[i] = t.s
			return build(i - 1)
		}
		return e.run(n.parts[i], env, v, nil, func(x any, _ *pathT) error {
			s, err := interpolate(n.format, x)
			if err != nil {
				return err
			}
			parts[i] = s
			return build(i - 1)
		})
	}
	return build(len(n.parts) - 1)
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return toJSON(v)
}

func (e *evaluator) evalBind(n *bindNode, env *envT, v any, p *pathT, out emitFunc) error {
	return e.run(n.src, env, v, nil, func(x any, _ *pathT) error {
		if len(n.pats) == 1 {
			return e.bindPattern(&n.pats[0], env, x, func(env2 *envT) error {
				return e.run(n.body, env2, v, p, out)
			})
		}
		// ?// alternatives: every variable of every pattern is bound (to
		// null unless the chosen pattern sets it); on error, try the next.
		base := env
		for _, pat := range n.pats {
			for _, name := range patternVars(&pat, nil) {
				base = &envT{parent: base, kind: 'v', name: name}
			}
		}
		var err error
		for i := range n.pats {
			tok := new(byte)
			err = e.bindPattern(&n.pats[i], base, x, func(env2 *envT) error {
				return e.run(n.body, env2, v, p, passThrough(out, tok))
			})
			if err == nil {
				return nil
			}
			if inner, ok := unwrapPass(err, tok); ok {
				return inner
			}
			if !catchable(err) {
				return err
			}
		}
		return err
	})
}

func patternVars(pat *pattern, acc []string) []string {
	switch pat.kind {
	case 'v':
		acc = append(acc, pat.name)
	case 'a':
		for i := range pat.elems {
			acc = patternVars(&pat.elems[i], acc)
		}
	case 'o':
		for _, op := range pat.obj {
			if op.keyVar != "" {
				acc = append(acc, op.keyVar)
			}
			if op.val != nil {
				acc = patternVars(op.val, acc)
			}
		}
	}
	return acc
}

func (e *evaluator) bindPattern(pat *pattern, env *envT, v any, k func(*envT) error) error {
	switch pat.kind {
	case 'v':
		return k(&envT{parent: env, kind: 'v', name: pat.name, value: v})
	case 'a':
		if v != nil {
			if _, ok := v.([]any); !ok {
				return errIndex(v, 0)
			}
		}
		var elems func(i int, env *envT) error
		elems = func(i int, env *envT) error {
			if i == len(pat.elems) {
				return k(env)
			}
			x, _ := index(v, i)
			return e.bindPattern(&pat.elems[i], env, x, func(env2 *envT) error {
				return elems(i+1, env2)
			})
		}
		return elems(0, env)
	case 'o':
		var entries func(i int, env *envT) error
		entries = func(i int, env *envT) error {
			if i == len(pat.obj) {
				return k(env)
			}
			op := pat.obj[i]
			withKey := func(key any) error {
				ks, ok := key.(string)
				if !ok {
					return errIndex(v, key)
				}
				if v != nil {
					if _, ok := v.(map[string]any); !ok {
						return errIndex(v, ks)
					}
				}
				x, _ := index(v, ks)
				env2 := env
				if op.keyVar != "" {
					env2 = &envT{parent: env, kind: 'v', name: op.keyVar, value: x}
				}
				if op.val == nil {
					return entries(i+1, env2)
				}
				return e.bindPattern(op.val, env2, x, func(env3 *envT) error {
					return entries(i+1, env3)
				})
			}
			if lit, ok := op.key.(literalNode); ok {
				return withKey(lit.v)
			}
			return e.run(op.key, env, v, nil, func(key any, _ *pathT) error { return withKey(key) })
		}
		return entries(0, env)
	}
	return fmt.Errorf("jqgo: bad pattern")
}

func (e *evaluator) evalReduce(n *reduceNode, env *envT, v any, p *pathT, out emitFunc) error {
	return e.run(n.init, env, v, nil, func(acc any, _ *pathT) error {
		owned := false
		err := e.run(n.src, env, v, nil, func(x any, _ *pathT) error {
			return e.bindPattern(&n.pat, env, x, func(env2 *envT) error {
				if n.inPlace && !e.copyOnly {
					fast := e.reduceStep(n.update, env2, acc, owned)
					if fast.err != nil {
						return fast.err
					}
					acc, owned = fast.acc, fast.owned
					return nil
				}
				var last any
				has := false
				err := e.run(n.update, env2, acc, nil, func(u any, _ *pathT) error {
					last, has = u, true
					return nil
				})
				if err != nil {
					return err
				}
				if has {
					acc = last
				} else {
					acc = nil
				}
				owned = false
				return nil
			})
		})
		if err != nil {
			return err
		}
		return emitValue(out, acc, p)
	})
}

func (e *evaluator) evalForeach(n *foreachNode, env *envT, v any, p *pathT, out emitFunc) error {
	return e.run(n.init, env, v, nil, func(state any, _ *pathT) error {
		return e.run(n.src, env, v, nil, func(x any, _ *pathT) error {
			return e.bindPattern(&n.pat, env, x, func(env2 *envT) error {
				return e.run(n.update, env2, state, nil, func(u any, _ *pathT) error {
					state = u
					if n.extract == nil {
						return emitValue(out, u, p)
					}
					return e.run(n.extract, env2, u, nil, func(r any, _ *pathT) error {
						return emitValue(out, r, p)
					})
				})
			})
		})
	})
}

// ---- function calls ----

func (e *evaluator) call(n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
	arity := len(n.args)
	if n.lexical {
		for x := env; x != nil; x = x.parent {
			if x.kind == 'f' && x.arity == arity && x.name == n.name {
				return e.callClosure(x.fn, n, env, v, p, out)
			}
		}
	}
	g := n.global
	if g == nil {
		return fmt.Errorf("%s/%d is not defined", n.name, arity)
	}
	switch {
	case g.def != nil:
		return e.callClosure(&closure{def: g.def}, n, env, v, p, out)
	case g.gen != nil:
		return g.gen(e, n, env, v, p, out)
	default:
		return e.callValueFn(g.fn, n, env, v, p, out)
	}
}

func (e *evaluator) callClosure(c *closure, n *callNode, callerEnv *envT, v any, p *pathT, out emitFunc) error {
	def := c.def
	if len(def.params) == 0 {
		return e.run(def.body, c.env, v, p, out)
	}
	var bind func(i int, env *envT) error
	bind = func(i int, env *envT) error {
		if i == len(def.params) {
			return e.run(def.body, env, v, p, out)
		}
		prm := def.params[i]
		if !prm.isVar {
			return bind(i+1, &envT{parent: env, kind: 'f', name: prm.name, fn: &closure{def: n.argDefs[i], env: callerEnv}})
		}
		return e.run(n.args[i], callerEnv, v, nil, func(x any, _ *pathT) error {
			env2 := &envT{parent: env, kind: 'v', name: prm.name, value: x}
			env2 = &envT{parent: env2, kind: 'f', name: prm.name, fn: &closure{def: &funcDef{body: literalNode{v: x}}}}
			return bind(i+1, env2)
		})
	}
	return bind(0, c.env)
}

// callValueFn evaluates the arguments of a value function as a cartesian
// product, the last argument varying slowest (as jq does for C builtins).
func (e *evaluator) callValueFn(fn valueFunc, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
	if len(n.args) == 0 {
		r, err := fn(e, v, nil)
		if err != nil {
			return err
		}
		return emitValue(out, r, p)
	}
	args := make([]any, len(n.args))
	var rec func(i int) error
	rec = func(i int) error {
		if i < 0 {
			r, err := fn(e, v, args)
			if err != nil {
				return err
			}
			return emitValue(out, r, p)
		}
		return e.run(n.args[i], env, v, nil, func(x any, _ *pathT) error {
			args[i] = x
			return rec(i - 1)
		})
	}
	return rec(len(args) - 1)
}

// evalArg runs a closure argument of a native generator.
func (e *evaluator) evalArg(n *callNode, i int, env *envT, v any, out func(any) error) error {
	return e.run(n.args[i], env, v, nil, func(x any, _ *pathT) error { return out(x) })
}

// collectPaths gathers path(f) as arrays.
func (e *evaluator) collectPaths(f node, env *envT, v any) ([][]any, error) {
	var paths [][]any
	err := e.run(f, env, v, rootPath, func(_ any, xp *pathT) error {
		arr, err := xp.toArray()
		if err != nil {
			return err
		}
		paths = append(paths, arr)
		return nil
	})
	return paths, err
}

// ---- inputs ----

// Inputs supplies values to the input and inputs builtins.
type Inputs interface {
	// Next returns the next input, or io.EOF when there are none left.
	Next() (any, error)
}

func (e *evaluator) nextInput() (any, error) {
	if e.inputs == nil {
		return nil, fmt.Errorf("No more inputs")
	}
	v, err := e.inputs.Next()
	if err == io.EOF {
		return nil, fmt.Errorf("No more inputs")
	}
	if err != nil {
		return nil, err
	}
	return norm(v)
}
