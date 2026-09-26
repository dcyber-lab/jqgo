package jqgo

import (
	"fmt"
	"math"
)

// Generators with native loops (range, repeat, while, until), so long
// iterations do not grow the Go stack.

func registerGenerators() {
	defGen("range", 1, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		return e.evalArg(n, 0, env, v, func(upto any) error {
			return e.emitRange(0, upto, 1, p, out)
		})
	})
	defGen("range", 2, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		return e.evalArg(n, 0, env, v, func(from any) error {
			return e.evalArg(n, 1, env, v, func(upto any) error {
				return e.emitRange(from, upto, 1, p, out)
			})
		})
	})
	defGen("range", 3, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		return e.evalArg(n, 0, env, v, func(from any) error {
			return e.evalArg(n, 1, env, v, func(upto any) error {
				return e.evalArg(n, 2, env, v, func(by any) error {
					return e.emitRange(from, upto, by, p, out)
				})
			})
		})
	})
	defGen("repeat", 1, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		// jq 1.7: def repeat(f): def _repeat: f, _repeat; _repeat;
		for {
			if err := e.run(n.args[0], env, v, p, out); err != nil {
				return err
			}
			if err := e.ctx.Err(); err != nil {
				return err
			}
		}
	})
	defGen("while", 2, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		var rec func(x any, xp *pathT) error
		rec = func(x any, xp *pathT) error {
			for {
				conds, err := e.collect(n.args[0], env, x, nil)
				if err != nil {
					return err
				}
				if len(conds) != 1 {
					for _, c := range conds {
						if truthy(c.v) {
							if err := out(x, xp); err != nil {
								return err
							}
							if err := e.run(n.args[1], env, x, xp, rec); err != nil {
								return err
							}
						}
					}
					return nil
				}
				if !truthy(conds[0].v) {
					return nil
				}
				if err := out(x, xp); err != nil {
					return err
				}
				next, err := e.collect(n.args[1], env, x, xp)
				if err != nil {
					return err
				}
				if len(next) != 1 {
					for _, y := range next {
						if err := rec(y.v, y.p); err != nil {
							return err
						}
					}
					return nil
				}
				x, xp = next[0].v, next[0].p
			}
		}
		return rec(v, p)
	})
	defGen("until", 2, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		var rec func(x any, xp *pathT) error
		rec = func(x any, xp *pathT) error {
			for {
				conds, err := e.collect(n.args[0], env, x, nil)
				if err != nil {
					return err
				}
				if len(conds) != 1 {
					for _, c := range conds {
						if truthy(c.v) {
							if err := out(x, xp); err != nil {
								return err
							}
						} else if err := e.run(n.args[1], env, x, xp, rec); err != nil {
							return err
						}
					}
					return nil
				}
				if truthy(conds[0].v) {
					return out(x, xp)
				}
				next, err := e.collect(n.args[1], env, x, xp)
				if err != nil {
					return err
				}
				if len(next) != 1 {
					for _, y := range next {
						if err := rec(y.v, y.p); err != nil {
							return err
						}
					}
					return nil
				}
				x, xp = next[0].v, next[0].p
			}
		}
		return rec(v, p)
	})
}

type pv struct {
	v any
	p *pathT
}

// collect gathers every output of n (with paths when p != nil).
func (e *evaluator) collect(n node, env *envT, v any, p *pathT) ([]pv, error) {
	var out []pv
	err := e.run(n, env, v, p, func(x any, xp *pathT) error {
		out = append(out, pv{x, xp})
		return nil
	})
	return out, err
}

// emitRange produces range($from; $upto; $by). Its loop may emit straight
// into a collector ([range(1e18)]) without re-entering run, so it checks
// for cancellation itself.
func (e *evaluator) emitRange(from, upto, by any, p *pathT, out emitFunc) error {
	for _, x := range []any{from, upto, by} {
		if !isNumber(x) {
			return fmt.Errorf("Range bounds must be numeric")
		}
	}
	emit := func(x any) error {
		if err := e.tick(); err != nil {
			return err
		}
		return emitValue(out, x, p)
	}
	fi, ok1 := from.(int)
	ui, ok2 := upto.(int)
	bi, ok3 := by.(int)
	if ok1 && ok2 && ok3 {
		switch {
		case bi > 0:
			for x := fi; x < ui; x += bi {
				if err := emit(x); err != nil {
					return err
				}
				if x > math.MaxInt-bi {
					break
				}
			}
		case bi < 0:
			for x := fi; x > ui; x += bi {
				if err := emit(x); err != nil {
					return err
				}
				if x < math.MinInt-bi {
					break
				}
			}
		}
		return nil
	}
	f, _ := toFloat(from)
	u, _ := toFloat(upto)
	b, _ := toFloat(by)
	switch {
	case b > 0:
		for x := f; x < u; x += b {
			if err := emit(intIfExactNum(x, from, by)); err != nil {
				return err
			}
			if x+b == x {
				break // step too small to make progress
			}
		}
	case b < 0:
		for x := f; x > u; x += b {
			if err := emit(intIfExactNum(x, from, by)); err != nil {
				return err
			}
			if x+b == x {
				break
			}
		}
	}
	return nil
}

// intIfExactNum keeps range(0; 2.5) producing ints 0, 1, 2 when the start
// and step are integers.
func intIfExactNum(x float64, from, by any) any {
	_, fi := from.(int)
	_, bi := by.(int)
	if fi && bi {
		return intIfExact(x)
	}
	return x
}
