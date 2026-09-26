package jqgo

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// jq uses Oniguruma; we use Go's RE2. The common syntax (classes,
// quantifiers, named groups "(?<name>...)", anchors) is the same;
// backreferences and lookaround are not supported.

type regexKey struct{ re, flags string }

type compiledRegex struct {
	re      *regexp.Regexp
	global  bool
	noEmpty bool
}

var (
	regexMu    sync.RWMutex
	regexCache = map[regexKey]*compiledRegex{}
)

func compileRegex(re any, flags any) (*compiledRegex, error) {
	rs, ok := re.(string)
	if !ok {
		return nil, errNotMatchable(re)
	}
	fs := ""
	if flags != nil {
		s, ok := flags.(string)
		if !ok {
			return nil, fmt.Errorf("%s is not a string", typeDump(flags))
		}
		fs = s
	}
	key := regexKey{rs, fs}
	regexMu.RLock()
	c, ok := regexCache[key]
	regexMu.RUnlock()
	if ok {
		return c, nil
	}
	c = &compiledRegex{}
	var prefix string
	longest, extended := false, false
	for _, f := range fs {
		switch f {
		case 'g':
			c.global = true
		case 'i':
			prefix += "i"
		case 'x':
			extended = true
		case 's':
			prefix += "s"
		case 'p':
			prefix += "s"
		case 'n':
			c.noEmpty = true
		case 'l':
			longest = true
		default:
			return nil, fmt.Errorf("%s is not a valid modifier string", fs)
		}
	}
	src := rs
	if extended {
		src = stripExtended(src)
	}
	if prefix != "" {
		src = "(?" + prefix + ")" + src
	}
	compiled, err := regexp.Compile(src)
	if err != nil {
		return nil, fmt.Errorf("%s (at offset 0) is not a valid regex: %s", rs, strings.TrimPrefix(err.Error(), "error parsing regexp: "))
	}
	if longest {
		compiled.Longest()
	}
	c.re = compiled
	regexMu.Lock()
	if len(regexCache) >= 1024 { // queries building regexes from data
		clear(regexCache)
	}
	regexCache[key] = c
	regexMu.Unlock()
	return c, nil
}

// stripExtended removes whitespace and #-comments outside character
// classes, like the x flag does.
func stripExtended(s string) string {
	var sb strings.Builder
	inClass := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			sb.WriteByte(c)
			sb.WriteByte(s[i+1])
			i++
		case inClass:
			if c == ']' {
				inClass = false
			}
			sb.WriteByte(c)
		case c == '[':
			inClass = true
			sb.WriteByte(c)
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		case c == '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

// runeOffsets converts byte offsets to code point offsets.
type runeOffsets struct {
	s     string
	ascii bool
	idx   []int
}

func newRuneOffsets(s string) *runeOffsets {
	return &runeOffsets{s: s, ascii: isASCII(s)}
}

func (r *runeOffsets) at(b int) int {
	if r.ascii || b < 0 {
		return b
	}
	if r.idx == nil {
		r.idx = make([]int, len(r.s)+1)
		n := 0
		for i := 0; i < len(r.s); i++ {
			r.idx[i] = n
			if utf8.RuneStart(r.s[i]) {
				n++
			}
		}
		r.idx[len(r.s)] = n
	}
	return r.idx[b]
}

func (c *compiledRegex) matches(s string) [][]int {
	n := 1
	if c.global {
		n = -1
	}
	all := c.re.FindAllStringSubmatchIndex(s, n)
	if !c.noEmpty {
		return all
	}
	out := all[:0]
	for _, m := range all {
		if m[1] > m[0] {
			out = append(out, m)
		}
	}
	return out
}

func matchObject(c *compiledRegex, s string, m []int, ro *runeOffsets) map[string]any {
	names := c.re.SubexpNames()
	caps := make([]any, 0, len(names)-1)
	for g := 1; g < len(names); g++ {
		var name any
		if names[g] != "" {
			name = names[g]
		}
		start, end := m[2*g], m[2*g+1]
		if start < 0 {
			caps = append(caps, map[string]any{"offset": -1, "length": 0, "string": nil, "name": name})
			continue
		}
		so := ro.at(start)
		caps = append(caps, map[string]any{
			"offset": so, "length": ro.at(end) - so, "string": s[start:end], "name": name,
		})
	}
	so := ro.at(m[0])
	return map[string]any{
		"offset": so, "length": ro.at(m[1]) - so, "string": s[m[0]:m[1]], "captures": caps,
	}
}

func registerRegex() {
	defFn("_match_impl", 3, matchImpl)
	defFn("split", 2, splitRegex)
	defGen("sub", 3, subImpl)
	defFn("test", 2, func(e *evaluator, v any, args []any) (any, error) {
		return matchImpl(e, v, []any{args[0], args[1], true})
	})
	defFn("test", 1, func(e *evaluator, v any, args []any) (any, error) {
		re, flags, err := regexArg(args[0])
		if err != nil {
			return nil, err
		}
		return matchImpl(e, v, []any{re, flags, true})
	})
	emitMatches := func(e *evaluator, v, re, flags any, capture bool, p *pathT, out emitFunc) error {
		ms, err := matchImpl(e, v, []any{re, flags, false})
		if err != nil {
			return err
		}
		for _, m := range ms.([]any) {
			if capture {
				m = captureFromMatch(m.(map[string]any))
			}
			if err := emitValue(out, m, p); err != nil {
				return err
			}
		}
		return nil
	}
	for _, capture := range []bool{false, true} {
		capture := capture
		name := "match"
		if capture {
			name = "capture"
		}
		// match(re; flags): flags vary slowest, as for _match_impl.
		defGen(name, 2, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
			return e.evalArg(n, 1, env, v, func(flags any) error {
				return e.evalArg(n, 0, env, v, func(re any) error {
					return emitMatches(e, v, re, flags, capture, p, out)
				})
			})
		})
		defGen(name, 1, func(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
			return e.evalArg(n, 0, env, v, func(val any) error {
				re, flags, err := regexArg(val)
				if err != nil {
					return err
				}
				return emitMatches(e, v, re, flags, capture, p, out)
			})
		})
	}
}

// regexArg splits the one-argument form of test, match and capture: a
// regex string, or [regex, flags].
func regexArg(val any) (re, flags any, err error) {
	switch x := val.(type) {
	case string:
		return x, nil, nil
	case []any:
		switch {
		case len(x) > 1:
			return top(x[0]), top(x[1]), nil
		case len(x) > 0:
			return top(x[0]), nil, nil
		}
	}
	return nil, nil, fmt.Errorf("%s not a string or array", typeName(val))
}

// captureFromMatch turns a match object into {name: string} for its named
// groups.
func captureFromMatch(m map[string]any) map[string]any {
	obj := map[string]any{}
	for _, c := range m["captures"].([]any) {
		c := c.(map[string]any)
		if name, ok := c["name"].(string); ok {
			obj[name] = c["string"]
		}
	}
	return obj
}

// matchImpl is _match_impl(re; flags; test).
func matchImpl(e *evaluator, v any, args []any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, errNotMatchable(v)
	}
	c, err := compileRegex(args[0], args[1])
	if err != nil {
		return nil, err
	}
	if truthy(args[2]) {
		if c.noEmpty {
			return len(c.matches(s)) > 0, nil
		}
		return c.re.MatchString(s), nil
	}
	ro := newRuneOffsets(s)
	out := []any{}
	for _, m := range c.matches(s) {
		out = append(out, matchObject(c, s, m, ro))
	}
	return out, nil
}

// splitRegex is split($re; flags).
func splitRegex(e *evaluator, v any, args []any) (any, error) {
	s, ok := v.(string)
	if !ok {
		return nil, errNotMatchable(v)
	}
	flags := "g"
	if args[1] != nil {
		f, ok := args[1].(string)
		if !ok {
			return nil, fmt.Errorf("%s is not a string", typeDump(args[1]))
		}
		flags += f
	}
	c, err := compileRegex(args[0], flags)
	if err != nil {
		return nil, err
	}
	out := []any{}
	prev := 0
	for _, m := range c.matches(s) {
		out = append(out, s[prev:m[0]])
		prev = m[1]
	}
	return append(out, s[prev:]), nil
}

func captureObject(c *compiledRegex, s string, m []int) map[string]any {
	obj := map[string]any{}
	for g, name := range c.re.SubexpNames() {
		if g == 0 || name == "" {
			continue
		}
		if m[2*g] < 0 {
			obj[name] = nil
		} else {
			obj[name] = s[m[2*g]:m[2*g+1]]
		}
	}
	return obj
}

// subImpl is sub($re; str; $flags). The replacement is a filter run on
// the object of named captures. When it has several outputs, the i-th
// result is built from the i-th output at every match; if there is no
// result at all the input comes back unchanged (jq 1.7.1 semantics).
func subImpl(e *evaluator, n *callNode, env *envT, v any, p *pathT, out emitFunc) error {
	return e.evalArg(n, 0, env, v, func(re any) error {
		return e.evalArg(n, 2, env, v, func(flags any) error {
			s, ok := v.(string)
			if !ok {
				return errNotMatchable(v)
			}
			c, err := compileRegex(re, flags)
			if err != nil {
				return err
			}
			var results []*strings.Builder
			prev := 0
			for _, m := range c.matches(s) {
				gap := s[prev:m[0]]
				prev = m[1]
				var inserts []string
				err := e.evalArg(n, 1, env, captureObject(c, s, m), func(r any) error {
					rs, ok := r.(string)
					if !ok {
						return fmt.Errorf("%s and %s cannot be added", typeDump(gap), typeDump(r))
					}
					inserts = append(inserts, rs)
					return nil
				})
				if err != nil {
					return err
				}
				for i, ins := range inserts {
					if i == len(results) {
						results = append(results, &strings.Builder{})
					}
					results[i].WriteString(gap)
					results[i].WriteString(ins)
				}
			}
			if len(results) == 0 {
				return emitValue(out, s, p)
			}
			for _, sb := range results {
				sb.WriteString(s[prev:])
				if err := emitValue(out, sb.String(), p); err != nil {
					return err
				}
			}
			return nil
		})
	})
}
