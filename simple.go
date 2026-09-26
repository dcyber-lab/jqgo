package jqgo

import (
	"fmt"
	"math"
)

// Simple expressions are the ones that always produce exactly one value
// (or an error) and never need path tracking: field access, literals,
// variables, arithmetic, object and string construction, calls to value
// builtins with simple arguments. Most of a typical query is made of them.
//
// run evaluates them with eval1, a plain recursive function, instead of
// the continuation-passing machinery: no closure per node, no callback per
// value. eval1 must behave exactly like run on these nodes, including the
// order in which operands are evaluated (which decides which error wins).

// isSimple reports whether eval1 can evaluate n. The flags on the pointer
// nodes are set once by markSimple after compilation.
func isSimple(n node) bool {
	switch n := n.(type) {
	case identityNode, literalNode, *varNode, *formatNode:
		return true
	case *indexNode:
		return n.simple
	case *sliceNode:
		return n.simple
	case *pipeNode:
		return n.simple
	case *negNode:
		return n.simple
	case *binopNode:
		return n.simple
	case *andNode:
		return n.simple
	case *orNode:
		return n.simple
	case *ifNode:
		return n.simple
	case *arrayNode:
		return true // collects every output of its body into one array
	case *objectNode:
		return n.simple
	case *stringNode:
		return n.simple
	case *callNode:
		return n.simple
	}
	return false
}

// markSimple walks the whole tree, sets the simple flags bottom-up and
// reports whether n itself is simple.
func markSimple(n node) bool {
	all := func(ns ...node) bool {
		ok := true
		for _, x := range ns {
			if x != nil && !markSimple(x) {
				ok = false
			}
		}
		return ok
	}
	switch n := n.(type) {
	case nil:
		return true
	case *indexNode:
		n.simple = all(n.term, n.key)
		return n.simple
	case *sliceNode:
		n.simple = all(n.term, n.from, n.to)
		return n.simple
	case *pipeNode:
		n.simple = all(n.left, n.right)
		return n.simple
	case *negNode:
		n.simple = all(n.x)
		return n.simple
	case *binopNode:
		n.simple = all(n.left, n.right)
		return n.simple
	case *andNode:
		n.simple = all(n.left, n.right)
		return n.simple
	case *orNode:
		n.simple = all(n.left, n.right)
		return n.simple
	case *ifNode:
		n.simple = all(n.cond, n.then, n.els)
		return n.simple
	case *arrayNode:
		all(n.x)
		return true
	case *objectNode:
		ok := true
		for _, ent := range n.entries {
			if !all(ent.key, ent.value) {
				ok = false
			}
		}
		n.simple = ok
		return ok
	case *stringNode:
		n.simple = all(n.parts...)
		return n.simple
	case *callNode:
		ok := all(n.args...)
		n.simple = ok && !n.lexical && n.global != nil && n.global.fn != nil
		return n.simple
	case *iterateNode:
		all(n.term)
	case *commaNode:
		all(n.left, n.right)
	case *altNode:
		all(n.left, n.right)
	case *assignNode:
		all(n.left, n.right)
	case *tryNode:
		all(n.body, n.catch)
	case *reduceNode:
		all(n.src, n.init, n.update)
		markPattern(&n.pat)
	case *foreachNode:
		all(n.src, n.init, n.update, n.extract)
		markPattern(&n.pat)
	case *funcDefNode:
		all(n.def.body, n.rest)
	case *bindNode:
		all(n.src, n.body)
		for i := range n.pats {
			markPattern(&n.pats[i])
		}
	case *labelNode:
		all(n.body)
	case identityNode, literalNode, *varNode, *formatNode, textNode:
		return true
	}
	return false
}

func markPattern(p *pattern) {
	for i := range p.elems {
		markPattern(&p.elems[i])
	}
	for _, op := range p.obj {
		markSimple(op.key)
		if op.val != nil {
			markPattern(op.val)
		}
	}
}

func (e *evaluator) eval1(n node, env *envT, v any) (any, error) {
	switch n := n.(type) {
	case identityNode:
		return v, nil
	case literalNode:
		return n.v, nil
	case *varNode:
		return e.lookupVar(env, n.name)
	case *indexNode:
		if n.key == nil {
			t, err := e.eval1(n.term, env, v)
			if err != nil {
				return nil, err
			}
			return index(t, n.name)
		}
		k, err := e.eval1(n.key, env, v)
		if err != nil {
			return nil, err
		}
		t, err := e.eval1(n.term, env, v)
		if err != nil {
			return nil, err
		}
		return index(t, k)
	case *sliceNode:
		var from, to any
		var err error
		if n.from != nil {
			if from, err = e.eval1(n.from, env, v); err != nil {
				return nil, err
			}
		}
		if n.to != nil {
			if to, err = e.eval1(n.to, env, v); err != nil {
				return nil, err
			}
		}
		t, err := e.eval1(n.term, env, v)
		if err != nil {
			return nil, err
		}
		return index(t, map[string]any{"start": from, "end": to})
	case *pipeNode:
		x, err := e.eval1(n.left, env, v)
		if err != nil {
			return nil, err
		}
		return e.eval1(n.right, env, x)
	case *negNode:
		x, err := e.eval1(n.x, env, v)
		if err != nil {
			return nil, err
		}
		return negate(x)
	case *binopNode:
		r, err := e.eval1(n.right, env, v)
		if err != nil {
			return nil, err
		}
		l, err := e.eval1(n.left, env, v)
		if err != nil {
			return nil, err
		}
		return binop(n.op, l, r)
	case *andNode:
		l, err := e.eval1(n.left, env, v)
		if err != nil || !truthy(l) {
			return false, err
		}
		r, err := e.eval1(n.right, env, v)
		return truthy(r), err
	case *orNode:
		l, err := e.eval1(n.left, env, v)
		if err != nil {
			return nil, err
		}
		if truthy(l) {
			return true, nil
		}
		r, err := e.eval1(n.right, env, v)
		return truthy(r), err
	case *ifNode:
		c, err := e.eval1(n.cond, env, v)
		if err != nil {
			return nil, err
		}
		if truthy(c) {
			return e.eval1(n.then, env, v)
		}
		if n.els == nil {
			return v, nil
		}
		return e.eval1(n.els, env, v)
	case *arrayNode:
		arr := []any{}
		if n.x == nil {
			return arr, nil
		}
		if isSimple(n.x) {
			x, err := e.eval1(n.x, env, v)
			if err != nil {
				return nil, err
			}
			return append(arr, x), nil
		}
		err := e.run(n.x, env, v, nil, func(x any, _ *pathT) error {
			arr = append(arr, x)
			return nil
		})
		return arr, err
	case *objectNode:
		obj := make(map[string]any, len(n.entries))
		for _, ent := range n.entries {
			k, err := e.eval1(ent.key, env, v)
			if err != nil {
				return nil, err
			}
			ks, err := objectKey(k)
			if err != nil {
				return nil, err
			}
			val, err := e.eval1(ent.value, env, v)
			if err != nil {
				return nil, err
			}
			obj[ks] = val
		}
		return obj, nil
	case *stringNode:
		parts := make([]string, len(n.parts))
		size := 0
		for i := len(n.parts) - 1; i >= 0; i-- {
			if t, ok := n.parts[i].(textNode); ok {
				parts[i] = t.s
			} else {
				x, err := e.eval1(n.parts[i], env, v)
				if err != nil {
					return nil, err
				}
				if parts[i], err = interpolate(n.format, x); err != nil {
					return nil, err
				}
			}
			size += len(parts[i])
		}
		buf := make([]byte, 0, size)
		for _, s := range parts {
			buf = append(buf, s...)
		}
		return string(buf), nil
	case *formatNode:
		return applyFormat(n.name, v)
	case *callNode:
		var args []any
		if len(n.args) > 0 {
			args = make([]any, len(n.args))
			for i := len(n.args) - 1; i >= 0; i-- {
				x, err := e.eval1(n.args[i], env, v)
				if err != nil {
					return nil, err
				}
				args[i] = x
			}
		}
		return n.global.fn(e, v, args)
	}
	panic(fmt.Sprintf("jqgo: eval1 on %T, which is not simple", n))
}

func negate(x any) (any, error) {
	switch x := x.(type) {
	case int:
		if x == math.MinInt {
			return -float64(x), nil
		}
		return -x, nil
	case float64:
		return -x, nil
	}
	return nil, fmt.Errorf("%s cannot be negated", typeDump(x))
}

// objectKey checks a computed object key.
func objectKey(k any) (string, error) {
	if ks, ok := k.(string); ok {
		return ks, nil
	}
	if k == nil {
		return "", fmt.Errorf("Cannot use null (null) as object key")
	}
	return "", fmt.Errorf("Object keys must be strings")
}

// interpolate renders one \(...) of a string template.
func interpolate(format string, x any) (string, error) {
	if format == "" {
		return toString(x), nil
	}
	s, err := applyFormat(format, x)
	if err != nil {
		return "", err
	}
	return s.(string), nil
}
