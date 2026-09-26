package jqgo

import (
	"fmt"
	"sort"
	"strings"
)

// Builtins that talk to the outside: inputs, debug output, halting.

func registerEnv() {
	defFn("input", 0, func(e *evaluator, v any, _ []any) (any, error) {
		return e.nextInput()
	})
	defGen("inputs", 0, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
		for {
			x, err := e.nextInput()
			if err != nil {
				if err.Error() == "No more inputs" {
					return nil
				}
				return err
			}
			if err := emitValue(out, x, p); err != nil {
				return err
			}
		}
	})
	defFn("input_filename", 0, func(e *evaluator, v any, _ []any) (any, error) {
		if f, ok := e.inputs.(interface{ Filename() any }); ok {
			return f.Filename(), nil
		}
		return nil, nil
	})
	defFn("input_line_number", 0, func(e *evaluator, v any, _ []any) (any, error) { return 0, nil })
	defFn("debug", 0, func(e *evaluator, v any, _ []any) (any, error) {
		if w := e.q.debug; w != nil {
			fmt.Fprintf(w, "[\"DEBUG:\",%s]\n", toJSON(v))
		}
		return v, nil
	})
	defFn("stderr", 0, func(e *evaluator, v any, _ []any) (any, error) {
		if w := e.q.debug; w != nil {
			fmt.Fprint(w, toJSON(v))
		}
		return v, nil
	})
	defFn("halt", 0, func(e *evaluator, v any, _ []any) (any, error) {
		return nil, &HaltError{Code: 0, Value: nil, Silent: true}
	})
	defFn("halt_error", 1, func(e *evaluator, v any, args []any) (any, error) {
		code, ok := args[0].(int)
		if !ok {
			return nil, fmt.Errorf("halt_error/1: number required")
		}
		return nil, &HaltError{Code: code, Value: v}
	})
	defFn("builtins", 0, func(e *evaluator, v any, _ []any) (any, error) {
		out := []any{}
		for k := range globals {
			if !strings.HasPrefix(k, "_") {
				out = append(out, k)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].(string) < out[j].(string) })
		return out, nil
	})
	defFn("have_decnum", 0, func(*evaluator, any, []any) (any, error) { return false, nil })
	defFn("have_literal_numbers", 0, func(*evaluator, any, []any) (any, error) { return false, nil })

}
