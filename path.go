package jqgo

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"unsafe"
)

// index implements t[k] for every key kind jq supports. The element it
// returns is normalized (see norm).
func index(t, k any) (any, error) {
	k = top(k)
	switch t := t.(type) {
	case nil:
		switch k.(type) {
		case string, int, float64, map[string]any, nil:
			return nil, nil
		}
	case map[string]any:
		if ks, ok := k.(string); ok {
			return norm(t[ks])
		}
	case []any:
		switch k := k.(type) {
		case int:
			if k < 0 {
				k += len(t)
			}
			if k < 0 || k >= len(t) {
				return nil, nil
			}
			return norm(t[k])
		case float64:
			if math.IsNaN(k) {
				return nil, nil
			}
			i := floatToInt(math.Floor(k))
			if i < 0 {
				i += len(t)
			}
			if i < 0 || i >= len(t) {
				return nil, nil
			}
			return norm(t[i])
		case map[string]any:
			start, end, err := sliceBounds(len(t), k)
			if err != nil {
				return nil, err
			}
			return t[start:end:end], nil
		case []any:
			return arrayIndices(t, k), nil
		}
	case string:
		if km, ok := k.(map[string]any); ok {
			start, end, err := sliceBounds(runeLen(t), km)
			if err != nil {
				return nil, err
			}
			return substr(t, start, end), nil
		}
	}
	if ks, ok := k.(string); ok {
		return nil, errIndex(t, ks)
	}
	return nil, errIndex(t, k)
}

// sliceBounds resolves a {"start","end"} slice key against a length.
func sliceBounds(n int, key map[string]any) (int, int, error) {
	from, to := key["start"], key["end"]
	start, end := 0, n
	if from != nil {
		f, ok := toFloat(from)
		if !ok {
			return 0, 0, fmt.Errorf("Start and end indices of an array slice must be numbers")
		}
		if math.IsNaN(f) {
			f = 0
		}
		if f < 0 {
			f += float64(n)
		}
		f = math.Floor(f)
		switch {
		case f < 0:
			start = 0
		case f > float64(n):
			start = n
		default:
			start = int(f)
		}
	}
	if to != nil {
		f, ok := toFloat(to)
		if !ok {
			return 0, 0, fmt.Errorf("Start and end indices of an array slice must be numbers")
		}
		if math.IsNaN(f) {
			f = float64(n)
		}
		if f < 0 {
			f += float64(n)
		}
		f = math.Ceil(f)
		switch {
		case f < 0:
			end = 0
		case f > float64(n):
			end = n
		default:
			end = int(f)
		}
	}
	if end < start {
		end = start
	}
	return start, end, nil
}

// arrayIndices is t[sub]: every offset at which sub occurs in t.
func arrayIndices(t, sub []any) any {
	res := []any{}
	if len(sub) == 0 {
		return nil
	}
	for i := 0; i+len(sub) <= len(t); i++ {
		match := true
		for j := range sub {
			if !equal(t[i+j], sub[j]) {
				match = false
				break
			}
		}
		if match {
			res = append(res, i)
		}
	}
	return res
}

func getPath(v any, path []any) (any, error) {
	for _, k := range path {
		if v == nil {
			return nil, nil
		}
		var err error
		if v, err = index(v, k); err != nil {
			return nil, err
		}
	}
	return v, nil
}

// ownSet records containers allocated by the current update. Those are
// not reachable from anywhere else, so they can be modified in place
// instead of being copied again for every path. A nil ownSet disables the
// optimisation.
type ownSet map[unsafe.Pointer]struct{}

func contID(v any) unsafe.Pointer {
	switch v := v.(type) {
	case map[string]any:
		return reflect.ValueOf(v).UnsafePointer()
	case []any:
		if cap(v) == 0 {
			return nil
		}
		return unsafe.Pointer(unsafe.SliceData(v))
	}
	return nil
}

func (o ownSet) has(v any) bool {
	if o == nil {
		return false
	}
	id := contID(v)
	if id == nil {
		return false
	}
	_, ok := o[id]
	return ok
}

func (o ownSet) add(v any) {
	if o == nil {
		return
	}
	if id := contID(v); id != nil {
		o[id] = struct{}{}
	}
}

// exposeAt is called before the value at path is handed to user code.
// Anything owned inside that value may now be shared, so only the strict
// ancestors of path stay owned. A slice hands out a view into its parent's
// backing array, so the parent is given up too.
func (o ownSet) exposeAt(root any, path []any) {
	if len(o) == 0 {
		return
	}
	var keep []unsafe.Pointer
	cur := root
	for i, k := range path {
		if _, isSlice := k.(map[string]any); isSlice && i == len(path)-1 {
			break
		}
		if o.has(cur) {
			keep = append(keep, contID(cur))
		}
		next, err := index(cur, k)
		if err != nil || next == nil {
			break
		}
		cur = next
	}
	clear(o)
	for _, id := range keep {
		o[id] = struct{}{}
	}
}

func setPath(v any, path []any, x any, own ownSet) (any, error) {
	return setAt(v, path, 0, x, own)
}

func setAt(v any, path []any, i int, x any, own ownSet) (any, error) {
	if i == len(path) {
		return x, nil
	}
	v, err := norm(v)
	if err != nil {
		return nil, err
	}
	switch k := top(path[i]).(type) {
	case string:
		var m map[string]any
		switch t := v.(type) {
		case nil:
			m = make(map[string]any, 1)
			own.add(m)
		case map[string]any:
			if own.has(t) {
				m = t
			} else {
				m = cloneMap(t, 1)
				own.add(m)
			}
		default:
			return nil, errIndex(v, k)
		}
		child, err := setAt(m[k], path, i+1, x, own)
		if err != nil {
			return nil, err
		}
		m[k] = child
		return m, nil
	case int, float64:
		var arr []any
		switch t := v.(type) {
		case nil:
		case []any:
			arr = t
		default:
			return nil, errIndex(v, 0)
		}
		idx, _ := toInt(k)
		if f, ok := k.(float64); ok {
			if math.IsNaN(f) {
				return nil, fmt.Errorf("Cannot set array element at NaN index")
			}
			idx = floatToInt(math.Floor(f))
		}
		if idx < 0 {
			idx += len(arr)
			if idx < 0 {
				return nil, fmt.Errorf("Out of bounds negative array index")
			}
		}
		if idx > 1<<26 && idx >= len(arr) {
			return nil, fmt.Errorf("Array index too large")
		}
		owned := own.has(arr)
		switch {
		case idx >= len(arr) && owned:
			arr = append(arr, make([]any, idx+1-len(arr))...)
			own.add(arr)
		case idx >= len(arr):
			n := make([]any, idx+1)
			copy(n, arr)
			arr = n
			own.add(arr)
		case !owned:
			arr = cloneSlice(arr, 0)
			own.add(arr)
		}
		child, err := setAt(arr[idx], path, i+1, x, own)
		if err != nil {
			return nil, err
		}
		arr[idx] = child
		return arr, nil
	case map[string]any:
		var arr []any
		switch t := v.(type) {
		case nil:
		case []any:
			arr = t
		case string:
			return nil, fmt.Errorf("Cannot update string slices")
		default:
			return nil, fmt.Errorf("Cannot update field at object index of %s", typeName(v))
		}
		start, end, err := sliceBounds(len(arr), k)
		if err != nil {
			return nil, err
		}
		child, err := setAt(arr[start:end:end], path, i+1, x, own)
		if err != nil {
			return nil, err
		}
		repl, ok := child.([]any)
		if !ok {
			return nil, fmt.Errorf("A slice of an array can only be assigned another array")
		}
		n := make([]any, 0, len(arr)-(end-start)+len(repl))
		n = append(n, arr[:start]...)
		n = append(n, repl...)
		n = append(n, arr[end:]...)
		own.add(n)
		return n, nil
	case nil:
		if v == nil {
			return setAt(nil, path, i+1, x, own)
		}
	}
	if ks, ok := path[i].(string); ok {
		return nil, errIndex(v, ks)
	}
	return nil, fmt.Errorf("Cannot update field at %s index of %s", typeName(path[i]), typeName(v))
}

// delPaths deletes every path at once, so indices always refer to the
// original value (the effect of jq deleting them longest-first).
func delPaths(v any, paths [][]any) (any, error) {
	for _, p := range paths {
		if len(p) == 0 {
			return nil, nil
		}
	}
	v, err := norm(v)
	if err != nil {
		return nil, err
	}
	if v == nil || len(paths) == 0 {
		return v, nil
	}
	switch t := v.(type) {
	case map[string]any:
		drop := map[string]bool{}
		sub := map[string][][]any{}
		for _, p := range paths {
			k, ok := top(p[0]).(string)
			if !ok {
				return nil, fmt.Errorf("Cannot delete field at %s index of object", typeName(p[0]))
			}
			if len(p) == 1 {
				drop[k] = true
			} else {
				sub[k] = append(sub[k], p[1:])
			}
		}
		out := cloneMap(t, 0)
		for k := range drop {
			delete(out, k)
		}
		for k, ps := range sub {
			if drop[k] {
				continue
			}
			child, ok := t[k]
			if !ok {
				continue
			}
			nc, err := delPaths(child, ps)
			if err != nil {
				return nil, err
			}
			out[k] = nc
		}
		return out, nil
	case []any:
		n := len(t)
		drop := make([]bool, n)
		sub := map[int][][]any{}
		var sliceEdits [][]any
		for _, p := range paths {
			switch k := top(p[0]).(type) {
			case int, float64:
				idx, _ := toInt(k)
				if f, ok := k.(float64); ok {
					idx = floatToInt(math.Floor(f))
				}
				if idx < 0 {
					idx += n
				}
				if idx < 0 || idx >= n {
					continue
				}
				if len(p) == 1 {
					drop[idx] = true
				} else {
					sub[idx] = append(sub[idx], p[1:])
				}
			case map[string]any:
				start, end, err := sliceBounds(n, k)
				if err != nil {
					return nil, err
				}
				if len(p) == 1 {
					for i := start; i < end; i++ {
						drop[i] = true
					}
				} else {
					sliceEdits = append(sliceEdits, p)
				}
			default:
				return nil, fmt.Errorf("Cannot delete field at %s index of array", typeName(k))
			}
		}
		if len(sliceEdits) > 0 {
			// Deleting inside a slice (del(.[1:3][0])) shifts things around;
			// fall back to jq's one-at-a-time order for this rare case.
			return delSequential(v, paths)
		}
		out := make([]any, 0, n)
		for i, x := range t {
			if drop[i] {
				continue
			}
			if ps, ok := sub[i]; ok {
				nx, err := delPaths(x, ps)
				if err != nil {
					return nil, err
				}
				x = nx
			}
			out = append(out, x)
		}
		return out, nil
	}
	return nil, fmt.Errorf("Cannot delete field at %s index of %s", typeName(paths[0][0]), typeName(v))
}

func delSequential(v any, paths [][]any) (any, error) {
	sorted := make([][]any, len(paths))
	copy(sorted, paths)
	sort.SliceStable(sorted, func(i, j int) bool {
		return compare(any(sorted[i]), any(sorted[j])) > 0
	})
	for _, p := range sorted {
		var err error
		if v, err = delOne(v, p); err != nil {
			return nil, err
		}
	}
	return v, nil
}

func delOne(v any, p []any) (any, error) {
	if len(p) == 1 {
		return delPaths(v, [][]any{p})
	}
	child, err := index(v, p[0])
	if err != nil {
		return nil, err
	}
	if child == nil {
		return v, nil
	}
	nc, err := delOne(child, p[1:])
	if err != nil {
		return nil, err
	}
	return setPath(v, p[:1], nc, nil)
}
