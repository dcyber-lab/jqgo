package jqgo

import "fmt"

// Control builtins: empty, error, select, limit, first and friends.

func registerCore() {
	defGen("empty", 0, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		return nil
	})
	defFn("error", 0, func(e *evaluator, v any, _ []any) (any, error) {
		return nil, &ValueError{Value: v}
	})
	defFn("not", 0, func(e *evaluator, v any, _ []any) (any, error) { return !truthy(v), nil })
	defGen("select", 1, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		return e.evalArg(n, 0, env, v, func(c any) error {
			if truthy(c) {
				return out(v, p)
			}
			return nil
		})
	})
	defGen("limit", 2, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		return e.evalArg(n, 0, env, v, func(nv any) error {
			f, ok := toFloat(nv)
			if !ok {
				return fmt.Errorf("Invalid limit: %s", typeDump(nv))
			}
			if f <= 0 {
				if f < 0 {
					return e.run(n.args[1], env, v, p, out)
				}
				return nil
			}
			count := 0
			stop := &stopError{}
			err := e.run(n.args[1], env, v, p, func(x any, xp *pathT) error {
				count++
				if err := out(x, xp); err != nil {
					return err
				}
				if float64(count) >= f {
					return stop
				}
				return nil
			})
			if err == stop {
				return nil
			}
			return err
		})
	})
	defGen("first", 1, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		stop := &stopError{}
		err := e.run(n.args[0], env, v, p, func(x any, xp *pathT) error {
			if err := out(x, xp); err != nil {
				return err
			}
			return stop
		})
		if err == stop {
			return nil
		}
		return err
	})
	defGen("isempty", 1, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		stop := &stopError{}
		empty := true
		err := e.run(n.args[0], env, v, nil, func(any, *pathT) error {
			empty = false
			return stop
		})
		if err != nil && err != stop {
			return err
		}
		return emitValue(out, empty, p)
	})
}
