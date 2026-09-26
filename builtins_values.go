package jqgo

import (
	"fmt"
	"math"
	"strings"
)

// Builtins about values in general: length, keys, has, conversions.

func registerValues() {
	defFn("length", 0, func(e *evaluator, v any, _ []any) (any, error) {
		switch v := v.(type) {
		case nil:
			return 0, nil
		case int:
			if v < 0 {
				if v == math.MinInt {
					return -float64(v), nil
				}
				return -v, nil
			}
			return v, nil
		case float64:
			return math.Abs(v), nil
		case string:
			return runeLen(v), nil
		case []any:
			return len(v), nil
		case map[string]any:
			return len(v), nil
		}
		return nil, fmt.Errorf("%s has no length", typeDump(v))
	})
	defFn("utf8bytelength", 0, func(e *evaluator, v any, _ []any) (any, error) {
		if s, ok := v.(string); ok {
			return len(s), nil
		}
		return nil, fmt.Errorf("%s only strings have UTF-8 byte length", typeDump(v))
	})
	defFn("type", 0, func(e *evaluator, v any, _ []any) (any, error) { return typeName(v), nil })
	defFn("keys", 0, keysFn)
	defFn("keys_unsorted", 0, keysFn)
	defFn("has", 1, func(e *evaluator, v any, args []any) (any, error) {
		switch t := v.(type) {
		case map[string]any:
			if k, ok := args[0].(string); ok {
				_, has := t[k]
				return has, nil
			}
		case []any:
			if f, ok := toFloat(args[0]); ok {
				return f >= 0 && f < float64(len(t)), nil
			}
		}
		return nil, fmt.Errorf("Cannot check whether %s has a %s key", typeName(v), typeName(args[0]))
	})
	defFn("contains", 1, func(e *evaluator, v any, args []any) (any, error) {
		return contains(v, args[0])
	})
	defFn("add", 0, func(e *evaluator, v any, _ []any) (any, error) { return addAll(v) })
	defFn("tostring", 0, func(e *evaluator, v any, _ []any) (any, error) { return toString(v), nil })
	defFn("tojson", 0, func(e *evaluator, v any, _ []any) (any, error) { return toJSON(v), nil })
	defFn("fromjson", 0, func(e *evaluator, v any, _ []any) (any, error) {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("%s cannot be parsed as JSON", typeDump(v))
		}
		r, err := parseJSON([]byte(s))
		if err != nil {
			return nil, fmt.Errorf("%s (while parsing '%s')", err, s)
		}
		return r, nil
	})
	defFn("tonumber", 0, func(e *evaluator, v any, _ []any) (any, error) {
		switch x := v.(type) {
		case int, float64:
			return v, nil
		case string:
			t := strings.TrimSpace(x)
			if validNumber(t) {
				return parseNumber(t)
			}
			switch t {
			case "nan", "NaN":
				return nan, nil
			}
			return nil, fmt.Errorf("Cannot parse '%s' as a number", x)
		}
		return nil, fmt.Errorf("%s cannot be parsed as a number", typeDump(v))
	})
	defFn("infinite", 0, func(*evaluator, any, []any) (any, error) { return math.Inf(1), nil })
	defFn("nan", 0, func(*evaluator, any, []any) (any, error) { return nan, nil })
	defFn("isinfinite", 0, numPred(func(f float64) bool { return math.IsInf(f, 0) }))
	defFn("isnan", 0, numPred(math.IsNaN))
	defFn("isnormal", 0, numPred(func(f float64) bool {
		if math.IsNaN(f) || math.IsInf(f, 0) || f == 0 {
			return false
		}
		return math.Abs(f) >= 2.2250738585072014e-308
	}))
	defFn("abs", 0, func(e *evaluator, v any, _ []any) (any, error) {
		switch x := v.(type) {
		case int:
			if x < 0 {
				if x == math.MinInt {
					return -float64(x), nil
				}
				return -x, nil
			}
			return x, nil
		case float64:
			if x < 0 {
				return -x, nil
			}
			return x, nil
		case nil, bool:
			return nil, fmt.Errorf("%s cannot be negated", typeDump(v))
		}
		return v, nil
	})

}

func keysFn(e *evaluator, v any, _ []any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		keys := sortedKeys(x)
		out := make([]any, len(keys))
		for i, k := range keys {
			out[i] = k
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = i
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s has no keys", typeDump(v))
}

func numPred(f func(float64) bool) valueFunc {
	return func(e *evaluator, v any, _ []any) (any, error) {
		x, ok := toFloat(v)
		if !ok {
			return nil, errNumberRequired(v)
		}
		return f(x), nil
	}
}

func contains(a, b any) (any, error) {
	a, b = top(a), top(b)
	if kindOrder(a) != kindOrder(b) && !(isBool(a) && isBool(b)) {
		return nil, fmt.Errorf("%s and %s cannot have their containment checked", typeDump(a), typeDump(b))
	}
	switch x := a.(type) {
	case map[string]any:
		y := b.(map[string]any)
		for k, bv := range y {
			av, ok := x[k]
			if !ok {
				return false, nil
			}
			c, err := contains(av, bv)
			if err != nil {
				return nil, err
			}
			if c != true {
				return false, nil
			}
		}
		return true, nil
	case []any:
		y := b.([]any)
		for _, bv := range y {
			found := false
			for _, av := range x {
				if kindOrder(av) != kindOrder(bv) {
					continue
				}
				c, err := contains(av, bv)
				if err != nil {
					return nil, err
				}
				if c == true {
					found = true
					break
				}
			}
			if !found {
				return false, nil
			}
		}
		return true, nil
	case string:
		return strings.Contains(x, b.(string)), nil
	}
	return equal(a, b), nil
}

func isBool(v any) bool { _, ok := v.(bool); return ok }

func addAll(v any) (any, error) {
	var items []any
	switch x := v.(type) {
	case nil:
		return nil, nil
	case []any:
		items = make([]any, len(x))
		for i, it := range x {
			items[i] = top(it)
		}
	case map[string]any:
		for _, k := range sortedKeys(x) {
			items = append(items, top(x[k]))
		}
	default:
		return nil, errIterate(v)
	}
	// Fast paths avoid quadratic concatenation.
	kind := -1
	for _, it := range items {
		if it == nil {
			continue
		}
		k := kindOrder(it)
		if kind == -1 {
			kind = k
		} else if kind != k {
			kind = -2
			break
		}
	}
	switch kind {
	case 4: // strings
		var sb strings.Builder
		for _, it := range items {
			if s, ok := it.(string); ok {
				sb.WriteString(s)
			}
		}
		return sb.String(), nil
	case 5: // arrays
		out := []any{}
		for _, it := range items {
			if a, ok := it.([]any); ok {
				out = append(out, a...)
			}
		}
		return out, nil
	case 6: // objects
		out := map[string]any{}
		for _, it := range items {
			if m, ok := it.(map[string]any); ok {
				for k, val := range m {
					out[k] = val
				}
			}
		}
		return out, nil
	}
	var acc any
	for _, it := range items {
		var err error
		if acc, err = add(acc, it); err != nil {
			return nil, err
		}
	}
	return acc, nil
}
