package jqgo

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Array builtins: sorting, grouping, searching, entries.

func registerArrays() {
	defFn("sort", 0, func(e *evaluator, v any, _ []any) (any, error) {
		arr, ok := v.([]any)
		if !ok {
			return nil, errNotSortable(v)
		}
		out := cloneSlice(arr, 0)
		sort.SliceStable(out, func(i, j int) bool { return compare(out[i], out[j]) < 0 })
		return out, nil
	})
	defFn("_sort_by_impl", 1, func(e *evaluator, v any, args []any) (any, error) {
		arr, keys, err := byImplArgs(v, args[0], "sorted")
		if err != nil {
			return nil, err
		}
		idx := sortedIndex(keys)
		out := make([]any, len(arr))
		for i, j := range idx {
			out[i] = arr[j]
		}
		return out, nil
	})
	defFn("_group_by_impl", 1, func(e *evaluator, v any, args []any) (any, error) {
		arr, keys, err := byImplArgs(v, args[0], "grouped")
		if err != nil {
			return nil, err
		}
		out := []any{}
		idx := sortedIndex(keys)
		for i := 0; i < len(idx); {
			j := i + 1
			for j < len(idx) && compare(keys[idx[i]], keys[idx[j]]) == 0 {
				j++
			}
			group := make([]any, 0, j-i)
			for _, k := range idx[i:j] {
				group = append(group, arr[k])
			}
			out = append(out, group)
			i = j
		}
		return out, nil
	})
	defFn("_unique_by_impl", 1, func(e *evaluator, v any, args []any) (any, error) {
		arr, keys, err := byImplArgs(v, args[0], "sorted")
		if err != nil {
			return nil, err
		}
		out := []any{}
		idx := sortedIndex(keys)
		for i := 0; i < len(idx); i++ {
			if i > 0 && compare(keys[idx[i-1]], keys[idx[i]]) == 0 {
				continue
			}
			out = append(out, arr[idx[i]])
		}
		return out, nil
	})
	defFn("_min_by_impl", 1, func(e *evaluator, v any, args []any) (any, error) {
		return extremeBy(v, args[0], false)
	})
	defFn("_max_by_impl", 1, func(e *evaluator, v any, args []any) (any, error) {
		return extremeBy(v, args[0], true)
	})
	defFn("unique", 0, func(e *evaluator, v any, _ []any) (any, error) {
		arr, ok := v.([]any)
		if !ok {
			return nil, errNotSortable(v)
		}
		s := cloneSlice(arr, 0)
		sort.SliceStable(s, func(i, j int) bool { return compare(s[i], s[j]) < 0 })
		out := []any{}
		for i, x := range s {
			if i > 0 && compare(s[i-1], x) == 0 {
				continue
			}
			out = append(out, x)
		}
		return out, nil
	})
	defFn("min", 0, func(e *evaluator, v any, _ []any) (any, error) { return extremeBy(v, v, false) })
	defFn("max", 0, func(e *evaluator, v any, _ []any) (any, error) { return extremeBy(v, v, true) })
	defFn("reverse", 0, func(e *evaluator, v any, _ []any) (any, error) {
		switch x := v.(type) {
		case nil:
			return []any{}, nil
		case []any:
			out := make([]any, len(x))
			for i, el := range x {
				out[len(x)-1-i] = el
			}
			return out, nil
		case string:
			if x == "" {
				return []any{}, nil
			}
		}
		return nil, errIndex(v, 0)
	})
	defFn("flatten", 0, func(e *evaluator, v any, _ []any) (any, error) { return flatten(v, 1e9) })
	defFn("flatten", 1, func(e *evaluator, v any, args []any) (any, error) {
		d, ok := toFloat(args[0])
		if !ok {
			return nil, fmt.Errorf("flatten depth must not be negative")
		}
		if d < 0 {
			return nil, fmt.Errorf("flatten depth must not be negative")
		}
		return flatten(v, d)
	})
	defFn("indices", 1, func(e *evaluator, v any, args []any) (any, error) { return indicesOf(v, args[0]) })
	defFn("index", 1, func(e *evaluator, v any, args []any) (any, error) {
		r, err := indicesOf(v, args[0])
		if arr, ok := r.([]any); ok {
			if len(arr) == 0 {
				return nil, err
			}
			return arr[0], err
		}
		return r, err
	})
	defFn("rindex", 1, func(e *evaluator, v any, args []any) (any, error) {
		r, err := indicesOf(v, args[0])
		if arr, ok := r.([]any); ok {
			if len(arr) == 0 {
				return nil, err
			}
			return arr[len(arr)-1], err
		}
		return r, err
	})
	defFn("to_entries", 0, func(e *evaluator, v any, _ []any) (any, error) {
		switch x := v.(type) {
		case map[string]any:
			out := make([]any, 0, len(x))
			for _, k := range sortedKeys(x) {
				out = append(out, map[string]any{"key": k, "value": x[k]})
			}
			return out, nil
		case []any:
			out := make([]any, len(x))
			for i, el := range x {
				out[i] = map[string]any{"key": i, "value": el}
			}
			return out, nil
		}
		return nil, fmt.Errorf("%s has no keys", typeDump(v))
	})
	defFn("from_entries", 0, func(e *evaluator, v any, _ []any) (any, error) {
		arr, ok := v.([]any)
		if !ok {
			return nil, errIterate(v)
		}
		out := make(map[string]any, len(arr))
		for _, ent := range arr {
			m, ok := ent.(map[string]any)
			if !ok {
				if ent == nil {
					return nil, fmt.Errorf("Cannot use null (null) as object key")
				}
				return nil, errIndex(ent, "key")
			}
			var key any
			for _, name := range []string{"key", "Key", "name", "Name"} {
				if k := m[name]; truthy(k) {
					key = k
					break
				}
			}
			ks, ok := key.(string)
			if !ok {
				if key == nil {
					return nil, fmt.Errorf("Cannot use null (null) as object key")
				}
				return nil, fmt.Errorf("Object keys must be strings")
			}
			if val, ok := m["value"]; ok {
				out[ks] = val
			} else {
				out[ks] = m["Value"]
			}
		}
		return out, nil
	})

	defFn("bsearch", 1, func(e *evaluator, v any, args []any) (any, error) {
		arr, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("%s cannot be searched from", typeDump(v))
		}
		i := sort.Search(len(arr), func(i int) bool { return compare(arr[i], args[0]) >= 0 })
		if i < len(arr) && compare(arr[i], args[0]) == 0 {
			return i, nil
		}
		return -1 - i, nil
	})
}

func byImplArgs(v, keys any, verb string) ([]any, []any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, nil, errIndex(v, 0)
	}
	ks, ok := keys.([]any)
	if !ok || len(ks) != len(arr) {
		return nil, nil, fmt.Errorf("%s cannot be %s, as it is not an array", typeDump(v), verb)
	}
	return arr, ks, nil
}

func sortedIndex(keys []any) []int {
	idx := make([]int, len(keys))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(i, j int) bool { return compare(keys[idx[i]], keys[idx[j]]) < 0 })
	return idx
}

// extremeBy returns the element with the smallest (first one wins) or
// largest (last one wins) key, as jq's min_by/max_by do.
func extremeBy(v, keys any, max bool) (any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, errNotSortable(v)
	}
	ks, ok := keys.([]any)
	if !ok || len(ks) != len(arr) {
		return nil, errNotSortable(v)
	}
	if len(arr) == 0 {
		return nil, nil
	}
	best := 0
	for i := 1; i < len(arr); i++ {
		c := compare(ks[i], ks[best])
		if (max && c >= 0) || (!max && c < 0) {
			best = i
		}
	}
	return arr[best], nil
}

func flatten(v any, depth float64) (any, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, errIterate(v)
	}
	out := []any{}
	var rec func(a []any, d float64)
	rec = func(a []any, d float64) {
		for _, x := range a {
			if sub, ok := x.([]any); ok && d > 0 {
				rec(sub, d-1)
			} else {
				out = append(out, x)
			}
		}
	}
	rec(arr, depth)
	return out, nil
}

func indicesOf(v, x any) (any, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		sub, ok := x.(string)
		if !ok {
			break
		}
		out := []any{}
		if sub == "" {
			return nil, nil
		}
		ascii := isASCII(t)
		for i := 0; i+len(sub) <= len(t); {
			j := strings.Index(t[i:], sub)
			if j < 0 {
				break
			}
			pos := i + j
			if ascii {
				out = append(out, pos)
			} else {
				out = append(out, utf8.RuneCountInString(t[:pos]))
			}
			_, size := utf8.DecodeRuneInString(t[pos:])
			i = pos + size
		}
		return out, nil
	case []any:
		if sub, ok := x.([]any); ok {
			return arrayIndices(t, sub), nil
		}
		return arrayIndices(t, []any{x}), nil
	}
	return index(v, x)
}
