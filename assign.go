package jqgo

// Assignment operators. Paths are always computed against the original
// input; the right-hand side of =, op= and //= is evaluated against the
// original input too, once per output (jq semantics).

func (e *evaluator) evalAssign(n *assignNode, env *envT, v any, p *pathT, out emitFunc, own ownSet) error {
	return e.assign(n, env, v, own, func(r any) error { return emitValue(out, r, p) })
}

// assign runs one assignment. own, when non-nil, may already contain v's
// top-level container (see reduceStep); every container the assignment
// allocates is added to it.
func (e *evaluator) assign(n *assignNode, env *envT, v any, own ownSet, out func(any) error) error {
	paths, err := e.collectPaths(n.left, env, v)
	if err != nil {
		return err
	}
	if own == nil && !e.copyOnly {
		own = ownSet{}
	}
	if n.op == "|=" {
		return e.modify(n, env, v, paths, own, out)
	}
	var rhs []any
	err = e.run(n.right, env, v, nil, func(x any, _ *pathT) error {
		rhs = append(rhs, x)
		return nil
	})
	if err != nil {
		return err
	}
	if len(rhs) > 1 {
		// Each output starts over from v, so v itself must not be touched.
		clear(own)
	}
	for i, x := range rhs {
		if i > 0 && own != nil {
			own = ownSet{}
		}
		acc := v
		for _, path := range paths {
			nv := x
			if n.op != "=" {
				old, err := getPath(acc, path)
				if err != nil {
					return err
				}
				if n.op == "//=" {
					if truthy(old) {
						nv = old
					}
				} else if nv, err = binop(n.op[:len(n.op)-1], old, x); err != nil {
					return err
				}
			}
			if acc, err = setPath(acc, path, nv, own); err != nil {
				return err
			}
		}
		if err := out(acc); err != nil {
			return err
		}
	}
	return nil
}

// modify implements lhs |= f: the first output of f replaces each value,
// and paths for which f produces nothing are deleted afterwards.
func (e *evaluator) modify(n *assignNode, env *envT, v any, paths [][]any, own ownSet, out func(any) error) error {
	acc := v
	var dels [][]any
	stop := &stopError{}
	for _, path := range paths {
		old, err := getPath(acc, path)
		if err != nil {
			return err
		}
		own.exposeAt(acc, path)
		var nv any
		got := false
		err = e.run(n.right, env, old, nil, func(x any, _ *pathT) error {
			nv, got = x, true
			return stop
		})
		if err != nil && err != stop {
			return err
		}
		if !got {
			dels = append(dels, path)
			continue
		}
		if acc, err = setPath(acc, path, nv, own); err != nil {
			return err
		}
	}
	if len(dels) > 0 {
		var err error
		if acc, err = delPaths(acc, dels); err != nil {
			return err
		}
	}
	return out(acc)
}

type stepResult struct {
	acc   any
	owned bool
	err   error
}

// reduceStep applies a reduce update in place when that is provably
// invisible: the accumulator is a container this reduce allocated, and the
// update is an assignment or ". + x" whose right side never looks at the
// accumulator (checked once by canUpdateInPlace). This keeps reduce .[] as $x ({}; .[$x.k] = $x) and
// reduce .[] as $x ([]; . + [$x]) linear instead of quadratic.
func (e *evaluator) reduceStep(update node, env *envT, acc any, owned bool) stepResult {
	switch u := update.(type) {
	case *assignNode:
		own := ownSet{}
		if owned {
			own.add(acc)
		}
		var last any
		n := 0
		err := e.assign(u, env, acc, own, func(r any) error {
			last = r
			n++
			return nil
		})
		if err != nil {
			return stepResult{err: err}
		}
		if n != 1 {
			return stepResult{acc: last}
		}
		return stepResult{acc: last, owned: own.has(last)}
	case *binopNode:
		var rhs []any
		err := e.run(u.right, env, acc, nil, func(x any, _ *pathT) error {
			rhs = append(rhs, x)
			return nil
		})
		if err != nil {
			return stepResult{err: err}
		}
		if len(rhs) != 1 {
			// the last output wins, as for any update
			if len(rhs) == 0 {
				return stepResult{acc: nil}
			}
			r, err := binop("+", acc, rhs[len(rhs)-1])
			return stepResult{acc: r, err: err}
		}
		x := rhs[0]
		if owned {
			switch a := acc.(type) {
			case []any:
				if xs, ok := x.([]any); ok {
					return stepResult{acc: append(a, xs...), owned: true}
				}
			case map[string]any:
				if xm, ok := x.(map[string]any); ok {
					for k, val := range xm {
						a[k] = val
					}
					return stepResult{acc: a, owned: true}
				}
			}
		}
		r, err := binop("+", acc, x)
		if err != nil {
			return stepResult{err: err}
		}
		// binop allocates a fresh container unless one side was null.
		fresh := acc != nil && x != nil
		if _, ok := r.([]any); !ok {
			if _, ok := r.(map[string]any); !ok {
				fresh = false
			}
		}
		return stepResult{acc: r, owned: fresh}
	}
	panic("jqgo: reduceStep on an update canUpdateInPlace rejected")
}

// canUpdateInPlace decides, once at compile time, whether reduceStep may
// be used for a reduce update.
func canUpdateInPlace(update node) bool {
	switch u := update.(type) {
	case *assignNode:
		return u.op == "|=" || inputFree(u.right)
	case *binopNode:
		_, ok := u.left.(identityNode)
		return ok && u.op == "+" && inputFree(u.right)
	}
	return false
}

// inputFree reports (conservatively) whether n's outputs cannot contain
// its input value.
func inputFree(n node) bool {
	switch n := n.(type) {
	case nil:
		return true
	case literalNode, *varNode, *breakNode:
		return true
	case *indexNode:
		return inputFree(n.term) && (n.key == nil || inputFree(n.key))
	case *sliceNode:
		return inputFree(n.term) && inputFree(n.from) && inputFree(n.to)
	case *iterateNode:
		return inputFree(n.term)
	case *pipeNode:
		return inputFree(n.left)
	case *commaNode:
		return inputFree(n.left) && inputFree(n.right)
	case *arrayNode:
		return inputFree(n.x)
	case *objectNode:
		for _, ent := range n.entries {
			if !inputFree(ent.key) || !inputFree(ent.value) {
				return false
			}
		}
		return true
	case *stringNode:
		return true // always produces a fresh string
	case *binopNode:
		return inputFree(n.left) && inputFree(n.right)
	case *andNode, *orNode:
		return true // booleans
	case *altNode:
		return inputFree(n.left) && inputFree(n.right)
	case *negNode:
		return true
	case *ifNode:
		return inputFree(n.cond) && inputFree(n.then) && n.els != nil && inputFree(n.els)
	case *tryNode:
		return inputFree(n.body) && inputFree(n.catch)
	case *bindNode:
		return inputFree(n.src) && inputFree(n.body)
	case *labelNode:
		return inputFree(n.body)
	case *reduceNode:
		return inputFree(n.src) && inputFree(n.init)
	case *foreachNode:
		return inputFree(n.src) && inputFree(n.init)
	}
	return false
}
