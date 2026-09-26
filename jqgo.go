// Package jqgo is an implementation of the jq language for Go programs.
//
// Compile a query once and run it as many times as needed, from as many
// goroutines as needed:
//
//	q, err := jqgo.Compile(`.items[] | select(.price > $min) | .name`, jqgo.WithVariables("min"))
//	...
//	for v, err := range q.Run(ctx, data, 10) {
//		if err != nil { ... }
//		fmt.Println(v)
//	}
//
// Inputs may be anything encoding/json can marshal; they are converted to
// the value model jq works with, which is plain Go values:
//
//	nil, bool, int, float64, string, []any, map[string]any
//
// Results use the same types. Integers that fit in an int are ints, other
// numbers are float64. Results may share memory with the input and with
// each other, so treat them as read-only (or copy before mutating).
package jqgo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"strings"
)

// Query is a compiled jq program. It is safe for concurrent use.
type Query struct {
	src     string
	root    node
	vars    []string
	custom  map[string]*globalFunc
	environ map[string]any
	debug   io.Writer
}

type config struct {
	vars    []string
	custom  map[string]*globalFunc
	environ map[string]any
	debug   io.Writer
}

// Option configures Compile.
type Option func(*config)

// WithVariables declares variables ($name) the query may use. Their values
// are passed positionally to Run, in the same order. Names may be given
// with or without the leading $.
func WithVariables(names ...string) Option {
	return func(c *config) {
		for _, n := range names {
			c.vars = append(c.vars, strings.TrimPrefix(n, "$"))
		}
	}
}

// Func is a Go function callable from a query. input is the value of "."
// and args holds one value per argument; when an argument produces several
// values the function is called once per combination, as with jq's own
// builtins. The returned value is normalized like any input.
type Func func(input any, args []any) (any, error)

// WithFunction makes fn callable as name/arity for every arity between
// minArity and maxArity. It takes precedence over a builtin of the same
// name and arity.
func WithFunction(name string, minArity, maxArity int, fn Func) Option {
	return func(c *config) {
		if c.custom == nil {
			c.custom = map[string]*globalFunc{}
		}
		for a := minArity; a <= maxArity; a++ {
			c.custom[funcKey(name, a)] = &globalFunc{name: name, arity: a, fn: func(e *evaluator, v any, args []any) (any, error) {
				cp := append([]any(nil), args...)
				r, err := fn(v, cp)
				if err != nil {
					return nil, err
				}
				return normalize(r)
			}}
		}
	}
}

// WithEnviron sets what $ENV and env return, from "KEY=value" strings
// (the format of os.Environ). Without it, $ENV is an empty object: a query
// cannot read the process environment unless you hand it over.
func WithEnviron(environ []string) Option {
	return func(c *config) {
		m := make(map[string]any, len(environ))
		for _, kv := range environ {
			if k, v, ok := strings.Cut(kv, "="); ok {
				m[k] = v
			}
		}
		c.environ = m
	}
}

// WithDebugWriter sets where debug and stderr write (os.Stderr by default;
// nil discards).
func WithDebugWriter(w io.Writer) Option {
	return func(c *config) { c.debug = w }
}

// Compile parses and checks a query.
func Compile(src string, opts ...Option) (*Query, error) {
	if err := loadBuiltins(); err != nil {
		return nil, err
	}
	cfg := config{debug: os.Stderr}
	for _, o := range opts {
		o(&cfg)
	}
	root, err := parse(src)
	if err != nil {
		return nil, err
	}
	vars := map[string]bool{"ENV": true, "__prog_args": true}
	for _, v := range cfg.vars {
		vars[v] = true
	}
	c := &checker{
		vars: vars,
		lookup: func(name string, arity int) *globalFunc {
			if g, ok := cfg.custom[funcKey(name, arity)]; ok {
				return g
			}
			return globals[funcKey(name, arity)]
		},
	}
	if err := c.check(root, nil); err != nil {
		return nil, &CompileError{Query: src, Err: err}
	}
	env := cfg.environ
	if env == nil {
		env = map[string]any{}
	}
	return &Query{src: src, root: root, vars: cfg.vars, custom: cfg.custom, environ: env, debug: cfg.debug}, nil
}

// MustCompile is like Compile but panics on error.
func MustCompile(src string, opts ...Option) *Query {
	q, err := Compile(src, opts...)
	if err != nil {
		panic(err)
	}
	return q
}

// String returns the query source.
func (q *Query) String() string { return q.src }

// Run evaluates the query against input and yields each result. After an
// error nothing more is yielded. vars are the values of the variables
// declared with WithVariables, in order.
func (q *Query) Run(ctx context.Context, input any, vars ...any) iter.Seq2[any, error] {
	return q.RunWithInputs(ctx, input, nil, vars...)
}

// RunWithInputs is Run with a source for the input and inputs builtins.
func (q *Query) RunWithInputs(ctx context.Context, input any, inputs Inputs, vars ...any) iter.Seq2[any, error] {
	return q.exec(ctx, input, inputs, false, vars)
}

// exec runs the query. copyOnly turns off in-place updates, so tests can
// check that they never change a result.
func (q *Query) exec(ctx context.Context, input any, inputs Inputs, copyOnly bool, vars []any) iter.Seq2[any, error] {
	return func(yield func(any, error) bool) {
		if ctx == nil {
			ctx = context.Background()
		}
		if len(vars) != len(q.vars) {
			yield(nil, fmt.Errorf("jqgo: query declares %d variable(s) but %d value(s) were given", len(q.vars), len(vars)))
			return
		}
		in, err := normalize(input)
		if err != nil {
			yield(nil, err)
			return
		}
		e := &evaluator{ctx: ctx, q: q, inputs: inputs, copyOnly: copyOnly, vars: make(map[string]any, len(q.vars)+2)}
		e.vars["ENV"] = q.environ
		named := make(map[string]any, len(q.vars))
		for i, name := range q.vars {
			v, err := normalize(vars[i])
			if err != nil {
				yield(nil, fmt.Errorf("jqgo: variable $%s: %w", name, err))
				return
			}
			e.vars[name] = v
			named[name] = v
		}
		e.vars["__prog_args"] = map[string]any{"positional": []any{}, "named": named}
		stop := &stopError{}
		inYield := false
		defer func() {
			// A bug in the evaluator must not take the host program down;
			// a panic in the caller's loop body is theirs to keep.
			if r := recover(); r != nil {
				if inYield {
					panic(r)
				}
				yield(nil, fmt.Errorf("jqgo: internal error: %v", r))
			}
		}()
		err = e.run(q.root, nil, in, nil, func(v any, _ *pathT) error {
			inYield = true
			more := yield(v, nil)
			inYield = false
			if !more {
				return stop
			}
			return nil
		})
		if err != nil && err != stop {
			var pe *passError
			for errors.As(err, &pe) {
				err = pe.err
			}
			yield(nil, err)
		}
	}
}

// All collects every result.
func (q *Query) All(ctx context.Context, input any, vars ...any) ([]any, error) {
	out := []any{}
	for v, err := range q.Run(ctx, input, vars...) {
		if err != nil {
			return out, err
		}
		out = append(out, v)
	}
	return out, nil
}

// ErrNoResult is returned by First when the query produces no output.
var ErrNoResult = errors.New("jqgo: query produced no result")

// First returns the first result, stopping evaluation there.
func (q *Query) First(ctx context.Context, input any, vars ...any) (any, error) {
	for v, err := range q.Run(ctx, input, vars...) {
		return v, err
	}
	return nil, ErrNoResult
}

// Eval compiles and runs src in one go; handy for one-off queries.
func Eval(ctx context.Context, src string, input any) ([]any, error) {
	q, err := Compile(src)
	if err != nil {
		return nil, err
	}
	return q.All(ctx, input)
}

// CompileError is returned by Compile when a query refers to something
// that does not exist.
type CompileError struct {
	Query string
	Err   error
}

func (e *CompileError) Error() string { return e.Err.Error() }
func (e *CompileError) Unwrap() error { return e.Err }

// ValueError is raised by error/0 and error/1; Value is the error value
// (what catch receives).
type ValueError struct {
	Value any
}

func (e *ValueError) Error() string {
	if s, ok := e.Value.(string); ok {
		return s
	}
	return toJSON(e.Value) + " (not a string)"
}

// HaltError is raised by halt and halt_error. Code is the exit status the
// jq CLI would use; Value is what halt_error was given.
type HaltError struct {
	Code   int
	Value  any
	Silent bool // halt: stop without printing anything
}

func (e *HaltError) Error() string {
	if e.Silent {
		return "halt"
	}
	if s, ok := e.Value.(string); ok {
		return s
	}
	return toJSON(e.Value)
}
