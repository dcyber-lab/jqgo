package jqgo

import "fmt"

// Path builtins; path, getpath and recurse keep path tracking alive.

func registerPaths() {
	defGen("path", 1, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		return e.run(n.args[0], env, v, rootPath, func(_ any, xp *pathT) error {
			arr, err := xp.toArray()
			if err != nil {
				return err
			}
			return emitValue(out, arr, p)
		})
	})
	defGen("getpath", 1, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		return e.evalArg(n, 0, env, v, func(pa any) error {
			path, ok := pa.([]any)
			if !ok {
				return fmt.Errorf("Path must be specified as an array")
			}
			r, err := getPath(v, path)
			if err != nil {
				return err
			}
			if p == nil {
				return out(r, nil)
			}
			if p.invalid {
				return errPathResult(p.value)
			}
			np := p
			for _, k := range path {
				np = &pathT{parent: np, key: k}
			}
			return out(r, np)
		})
	})
	defFn("setpath", 2, func(e *evaluator, v any, args []any) (any, error) {
		path, ok := args[0].([]any)
		if !ok {
			return nil, fmt.Errorf("Path must be specified as an array")
		}
		return setPath(v, path, args[1], nil)
	})
	defFn("delpaths", 1, func(e *evaluator, v any, args []any) (any, error) {
		ps, ok := args[0].([]any)
		if !ok {
			return nil, fmt.Errorf("Paths must be specified as an array")
		}
		paths := make([][]any, len(ps))
		for i, x := range ps {
			path, ok := top(x).([]any)
			if !ok {
				return nil, fmt.Errorf("Path must be specified as an array")
			}
			paths[i] = path
		}
		return delPaths(v, paths)
	})
	defGen("recurse", 0, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		return e.recurseAll(v, p, out)
	})
	defGen("recurse", 1, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		var rec func(x any, xp *pathT) error
		rec = func(x any, xp *pathT) error {
			if err := out(x, xp); err != nil {
				return err
			}
			return e.run(n.args[0], env, x, xp, rec)
		}
		return rec(v, p)
	})
}
