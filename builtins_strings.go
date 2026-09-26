package jqgo

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf8"
)

// String builtins; the regex ones live in regex.go.

func registerStrings() {
	defFn("explode", 0, func(e *evaluator, v any, _ []any) (any, error) {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s cannot be exploded, as it is not a string", typeDump(v))
		}
		out := make([]any, 0, len(s))
		for _, r := range s {
			out = append(out, int(r))
		}
		return out, nil
	})
	defFn("implode", 0, func(e *evaluator, v any, _ []any) (any, error) {
		arr, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("implode input must be an array")
		}
		var sb strings.Builder
		for _, x := range arr {
			f, ok := toFloat(x)
			if !ok || math.IsNaN(f) {
				if !ok {
					return nil, fmt.Errorf("%s can't be imploded, unicode codepoint needs to be numeric", typeDump(x))
				}
				return nil, fmt.Errorf("number (null) can't be imploded, unicode codepoint needs to be numeric")
			}
			r := rune(f)
			if f > utf8.MaxRune || f < 0 || (r >= 0xd800 && r < 0xe000) {
				r = utf8.RuneError
			}
			sb.WriteRune(r)
		}
		return sb.String(), nil
	})
	defFn("ltrimstr", 1, func(e *evaluator, v any, args []any) (any, error) {
		s, ok1 := v.(string)
		pre, ok2 := args[0].(string)
		if ok1 && ok2 && strings.HasPrefix(s, pre) {
			return s[len(pre):], nil
		}
		return v, nil
	})
	defFn("rtrimstr", 1, func(e *evaluator, v any, args []any) (any, error) {
		s, ok1 := v.(string)
		suf, ok2 := args[0].(string)
		if ok1 && ok2 && strings.HasSuffix(s, suf) && len(suf) > 0 {
			return s[:len(s)-len(suf)], nil
		}
		return v, nil
	})
	defFn("startswith", 1, func(e *evaluator, v any, args []any) (any, error) {
		s, ok1 := v.(string)
		pre, ok2 := args[0].(string)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("startswith() requires string inputs")
		}
		return strings.HasPrefix(s, pre), nil
	})
	defFn("endswith", 1, func(e *evaluator, v any, args []any) (any, error) {
		s, ok1 := v.(string)
		suf, ok2 := args[0].(string)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("endswith() requires string inputs")
		}
		return strings.HasSuffix(s, suf), nil
	})
	trim := func(name string, f func(string) string) {
		defFn(name, 0, func(e *evaluator, v any, _ []any) (any, error) {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("%s input must be a string", name)
			}
			return f(s), nil
		})
	}
	trim("trim", func(s string) string { return strings.TrimSpace(s) })
	trim("ltrim", func(s string) string { return strings.TrimLeft(s, " \t\n\r\f\v") })
	trim("rtrim", func(s string) string { return strings.TrimRight(s, " \t\n\r\f\v") })
	defFn("ascii_downcase", 0, func(e *evaluator, v any, _ []any) (any, error) {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("ascii_downcase input must be a string")
		}
		return asciiMap(s, 'A', 'Z', 'a'-'A'), nil
	})
	defFn("ascii_upcase", 0, func(e *evaluator, v any, _ []any) (any, error) {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("ascii_upcase input must be a string")
		}
		return asciiMap(s, 'a', 'z', 'A'-'a'), nil
	})
	defFn("split", 1, func(e *evaluator, v any, args []any) (any, error) {
		s, ok1 := v.(string)
		sep, ok2 := args[0].(string)
		if !ok1 || !ok2 {
			return nil, fmt.Errorf("split input and separator must be strings")
		}
		return splitString(s, sep), nil
	})
	defFn("join", 1, func(e *evaluator, v any, args []any) (any, error) { return join(v, args[0]) })
	defFn("format", 1, func(e *evaluator, v any, args []any) (any, error) {
		name, ok := args[0].(string)
		if !ok {
			return nil, fmt.Errorf("%s is not a valid format", typeDump(args[0]))
		}
		return applyFormat(name, v)
	})
	defFn("_match_impl", 3, matchImpl)
	defFn("split", 2, splitRegex)
	defGen("sub", 3, subImpl)
}

func asciiMap(s string, lo, hi byte, delta int) string {
	b := []byte(s)
	for i, c := range b {
		if c >= lo && c <= hi {
			b[i] = byte(int(c) + delta)
		}
	}
	return string(b)
}

func join(v, sep any) (any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, errIterate(v)
	}
	if len(arr) == 0 {
		return "", nil
	}
	s, ok := sep.(string)
	if !ok {
		// jq builds the result with +, so the error comes from there.
		if _, err := add("", sep); err != nil {
			return nil, err
		}
	}
	var sb strings.Builder
	for i, x := range arr {
		if i > 0 {
			sb.WriteString(s)
		}
		switch x := x.(type) {
		case nil:
		case string:
			sb.WriteString(x)
		case bool, int, float64:
			sb.WriteString(toJSON(x))
		default:
			acc := sb.String()
			return nil, fmt.Errorf("%s and %s cannot be added", typeDump(acc), typeDump(x))
		}
	}
	return sb.String(), nil
}
