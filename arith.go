package jqgo

import (
	"fmt"
	"math"
	"strings"
)

var nan = math.NaN()

func binop(op string, l, r any) (any, error) {
	switch op {
	case "+":
		return add(l, r)
	case "-":
		return subtract(l, r)
	case "*":
		return multiply(l, r)
	case "/":
		return divide(l, r)
	case "%":
		return modulo(l, r)
	case "==":
		return equal(l, r), nil
	case "!=":
		return !equal(l, r), nil
	case "<":
		return compare(l, r) < 0, nil
	case "<=":
		return compare(l, r) <= 0, nil
	case ">":
		return compare(l, r) > 0, nil
	case ">=":
		return compare(l, r) >= 0, nil
	}
	return nil, fmt.Errorf("unknown operator %s", op)
}

func add(l, r any) (any, error) {
	if l == nil {
		return r, nil
	}
	if r == nil {
		return l, nil
	}
	switch a := l.(type) {
	case int:
		switch b := r.(type) {
		case int:
			if s := a + b; (s > a) == (b > 0) {
				return s, nil
			}
			return float64(a) + float64(b), nil
		case float64:
			return float64(a) + b, nil
		}
	case float64:
		if b, ok := toFloat(r); ok {
			return a + b, nil
		}
	case string:
		if b, ok := r.(string); ok {
			return a + b, nil
		}
	case []any:
		if b, ok := r.([]any); ok {
			out := make([]any, 0, len(a)+len(b))
			out = append(out, a...)
			return append(out, b...), nil
		}
	case map[string]any:
		if b, ok := r.(map[string]any); ok {
			out := cloneMap(a, len(b))
			for k, v := range b {
				out[k] = v
			}
			return out, nil
		}
	}
	return nil, opError(l, r, "added")
}

func subtract(l, r any) (any, error) {
	switch a := l.(type) {
	case int:
		switch b := r.(type) {
		case int:
			if d := a - b; (d < a) == (b > 0) {
				return d, nil
			}
			return float64(a) - float64(b), nil
		case float64:
			return float64(a) - b, nil
		}
	case float64:
		if b, ok := toFloat(r); ok {
			return a - b, nil
		}
	case []any:
		if b, ok := r.([]any); ok {
			out := make([]any, 0, len(a))
		outer:
			for _, x := range a {
				for _, y := range b {
					if equal(x, y) {
						continue outer
					}
				}
				out = append(out, x)
			}
			return out, nil
		}
	}
	return nil, opError(l, r, "subtracted")
}

func multiply(l, r any) (any, error) {
	switch a := l.(type) {
	case int:
		switch b := r.(type) {
		case int:
			if a == 0 || b == 0 {
				return 0, nil
			}
			if p := a * b; p/b == a && !(a == -1 && b == math.MinInt) && !(b == -1 && a == math.MinInt) {
				return p, nil
			}
			return float64(a) * float64(b), nil
		case float64:
			return float64(a) * b, nil
		case string:
			return repeatString(b, float64(a))
		}
	case float64:
		switch b := r.(type) {
		case int:
			return a * float64(b), nil
		case float64:
			return a * b, nil
		case string:
			return repeatString(b, a)
		}
	case string:
		if f, ok := toFloat(r); ok {
			return repeatString(a, f)
		}
	case map[string]any:
		if b, ok := r.(map[string]any); ok {
			return deepMerge(a, b), nil
		}
	}
	return nil, opError(l, r, "multiplied")
}

// repeatString follows jq 1.7.1: a negative count gives null, otherwise
// the string is repeated trunc(n) times.
func repeatString(s string, n float64) (any, error) {
	if n < 0 || math.IsNaN(n) {
		return nil, nil
	}
	if s == "" {
		return "", nil // also for n = +Inf, where n*len(s) would be NaN
	}
	if n*float64(len(s)) > 1<<27 {
		return nil, fmt.Errorf("Repeat string result too long")
	}
	return strings.Repeat(s, int(n)), nil
}

func deepMerge(a, b map[string]any) map[string]any {
	out := cloneMap(a, len(b))
	for k, bv := range b {
		if bm, ok := top(bv).(map[string]any); ok {
			if am, ok := top(out[k]).(map[string]any); ok {
				out[k] = deepMerge(am, bm)
				continue
			}
		}
		out[k] = bv
	}
	return out
}

func divide(l, r any) (any, error) {
	switch a := l.(type) {
	case int, float64:
		if !isNumber(r) {
			break
		}
		fb, _ := toFloat(r)
		if fb == 0 {
			return nil, fmt.Errorf("%s and %s cannot be divided because the divisor is zero", typeDump(l), typeDump(r))
		}
		if ai, ok := a.(int); ok {
			if bi, ok := r.(int); ok && ai%bi == 0 && !(ai == math.MinInt && bi == -1) {
				return ai / bi, nil
			}
		}
		fa, _ := toFloat(a)
		return fa / fb, nil
	case string:
		if b, ok := r.(string); ok {
			return splitString(a, b), nil
		}
	}
	return nil, opError(l, r, "divided")
}

func splitString(s, sep string) any {
	if s == "" {
		return []any{}
	}
	var parts []string
	if sep == "" {
		parts = strings.Split(s, "")
	} else {
		parts = strings.Split(s, sep)
	}
	out := make([]any, len(parts))
	for i, p := range parts {
		out[i] = p
	}
	return out
}

func modulo(l, r any) (any, error) {
	if !isNumber(l) || !isNumber(r) {
		return nil, opError(l, r, "divided")
	}
	fa, _ := toFloat(l)
	fb, _ := toFloat(r)
	if math.IsNaN(fa) || math.IsNaN(fb) {
		return nan, nil
	}
	a, _ := toInt(l)
	b, _ := toInt(r)
	if b == 0 {
		return nil, fmt.Errorf("%s and %s cannot be divided (remainder) because the divisor is zero", typeDump(l), typeDump(r))
	}
	if b == -1 {
		return 0, nil
	}
	return a % b, nil
}
