package jqgo

import (
	_ "embed"
	"fmt"
	"sync"
)

type valueFunc func(e *evaluator, v any, args []any) (any, error)

type genFunc func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error

// globalFunc is a function resolved at compile time: a jq-defined builtin
// (def), a native value function (fn) or a native generator (gen).
type globalFunc struct {
	name  string
	arity int
	def   *funcDef
	fn    valueFunc
	gen   genFunc
}

//go:embed builtin.jq
var builtinSrc string

var (
	builtinsOnce sync.Once
	builtinsErr  error
	globals      map[string]*globalFunc // "name/arity"
)

func funcKey(name string, arity int) string {
	return fmt.Sprintf("%s/%d", name, arity)
}

func loadBuiltins() error {
	builtinsOnce.Do(func() {
		globals = map[string]*globalFunc{}
		for k, g := range natives {
			globals[k] = g
		}
		defs, err := parseDefs(builtinSrc)
		if err != nil {
			builtinsErr = fmt.Errorf("jqgo: builtin library: %w", err)
			return
		}
		for _, d := range defs {
			globals[funcKey(d.name, len(d.params))] = &globalFunc{name: d.name, arity: len(d.params), def: d}
		}
		c := &checker{vars: map[string]bool{"ENV": true}, lookup: func(name string, arity int) *globalFunc { return globals[funcKey(name, arity)] }}
		for _, d := range defs {
			if err := c.checkDefIn(d, nil); err != nil {
				builtinsErr = fmt.Errorf("jqgo: builtin library: %s: %w", d.name, err)
				return
			}
		}
	})
	return builtinsErr
}

var natives = map[string]*globalFunc{}

func defFn(name string, arity int, fn valueFunc) {
	natives[funcKey(name, arity)] = &globalFunc{name: name, arity: arity, fn: fn}
}

func defGen(name string, arity int, fn genFunc) {
	natives[funcKey(name, arity)] = &globalFunc{name: name, arity: arity, gen: fn}
}

func init() {
	registerCore()
	registerPaths()
	registerGenerators()
	registerEnv()
	registerValues()
	registerArrays()
	registerStrings()
	registerTime()
	registerMath()
}
