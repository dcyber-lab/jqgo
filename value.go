package jqgo

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"unicode/utf8"
)

// Values flowing through a query are plain Go values:
//
//	nil, bool, int, float64, string, []any, map[string]any
//
// Integers that fit in an int stay ints; everything else numeric is a
// float64. Values are treated as immutable: the evaluator never mutates a
// map or slice it did not allocate itself during the current operation.
//
// Values from the caller are converted lazily. Everything the evaluator
// handles is canonical at the top level, but the elements of a container
// that came from the caller may still be other Go types (int64,
// json.Number, structs, []string...). They are converted by norm when they
// are taken out of their container (index, iteration), and the functions
// that look inside containers directly (compare, equal, the encoder...)
// convert what they meet through top. Canonical input, which is what
// json.Unmarshal produces, is never walked or copied.

// Normalize converts v and everything inside it into the value model.
// It accepts anything encoding/json can marshal. Parts that are already
// canonical are shared, not copied. Queries do this on demand, so calling
// it first is only useful to convert once and query many times.
func Normalize(v any) (any, error) {
	out, _, err := normalizeDeep(v)
	return out, err
}

// normalizeDeep reports whether it had to build a new value.
func normalizeDeep(v any) (any, bool, error) {
	nv, err := norm(v)
	if err != nil {
		return nil, false, err
	}
	changed := !isCanonicalTop(v)
	switch x := nv.(type) {
	case []any:
		var out []any
		for i, e := range x {
			ne, c, err := normalizeDeep(e)
			if err != nil {
				return nil, false, err
			}
			if c && out == nil {
				out = cloneSlice(x, 0)
			}
			if out != nil {
				out[i] = ne
			}
		}
		if out != nil {
			return out, true, nil
		}
	case map[string]any:
		var out map[string]any
		for k, e := range x {
			ne, c, err := normalizeDeep(e)
			if err != nil {
				return nil, false, err
			}
			if c && out == nil {
				out = cloneMap(x, 0)
			}
			if out != nil {
				out[k] = ne
			}
		}
		if out != nil {
			return out, true, nil
		}
	}
	return nv, changed, nil
}

func isCanonicalTop(v any) bool {
	switch x := v.(type) {
	case nil, bool, string, int, float64:
		return true
	case []any:
		return x != nil
	case map[string]any:
		return x != nil
	}
	return false
}

// norm converts the top level of v into the value model. It is free for
// values that already are canonical.
func norm(v any) (any, error) {
	if isCanonicalTop(v) {
		return v, nil
	}
	return normSlow(v)
}

// top is norm for places that cannot report an error; a value that cannot
// be converted is returned unchanged and later reported as invalid.
func top(v any) any {
	if isCanonicalTop(v) {
		return v
	}
	if nv, err := normSlow(v); err == nil {
		return nv
	}
	return v
}

func normSlow(v any) (any, error) {
	switch x := v.(type) {
	case []any, map[string]any:
		return nil, nil // typed nil container
	case int8:
		return int(x), nil
	case int16:
		return int(x), nil
	case int32:
		return int(x), nil
	case int64:
		if int64(int(x)) == x {
			return int(x), nil
		}
		return float64(x), nil
	case uint:
		return uintValue(uint64(x)), nil
	case uint8:
		return int(x), nil
	case uint16:
		return int(x), nil
	case uint32:
		return uintValue(uint64(x)), nil
	case uint64:
		return uintValue(x), nil
	case float32:
		return float64(x), nil
	case json.Number:
		return parseNumber(string(x))
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out, nil
	case map[string]string:
		out := make(map[string]any, len(x))
		for k, s := range x {
			out[k] = s
		}
		return out, nil
	case error:
		return nil, fmt.Errorf("jqgo: cannot use error value %q as input", x.Error())
	}
	// Anything else (structs, typed slices and maps, pointers, custom
	// string types...) goes through encoding/json, which is exactly what a
	// caller would do by hand. The result is canonical all the way down.
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface:
		if rv.IsNil() {
			return nil, nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("jqgo: cannot convert %T to a JSON value: %w", v, err)
	}
	return parseJSON(b)
}

func uintValue(u uint64) any {
	if u <= math.MaxInt {
		return int(u)
	}
	return float64(u)
}

func parseNumber(s string) (any, error) {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil && int64(int(i)) == i {
		return int(i), nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f, nil // ±Inf or 0, like jq's strtod
		}
		return nil, err
	}
	return f, nil
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case int, float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	if nv := top(v); isCanonicalTop(nv) {
		return typeName(nv)
	}
	return fmt.Sprintf("invalid(%T)", v)
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int, float64, string, []any, map[string]any:
		return true
	}
	if nv := top(v); isCanonicalTop(nv) {
		return truthy(nv)
	}
	return true
}

func isNumber(v any) bool {
	switch v.(type) {
	case int, float64:
		return true
	case nil, bool, string, []any, map[string]any:
		return false
	}
	if nv := top(v); isCanonicalTop(nv) {
		return isNumber(nv)
	}
	return false
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int:
		return float64(x), true
	case float64:
		return x, true
	case nil, bool, string, []any, map[string]any:
		return 0, false
	}
	if nv := top(v); isCanonicalTop(nv) {
		return toFloat(nv)
	}
	return 0, false
}

// toInt truncates a number toward zero, clamping to the int range.
func toInt(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case float64:
		return floatToInt(x), true
	case nil, bool, string, []any, map[string]any:
		return 0, false
	}
	if nv := top(v); isCanonicalTop(nv) {
		return toInt(nv)
	}
	return 0, false
}

func floatToInt(f float64) int {
	switch {
	case math.IsNaN(f):
		return 0
	case f >= math.MaxInt:
		return math.MaxInt
	case f <= math.MinInt:
		return math.MinInt
	}
	return int(f)
}

// intIfExact turns a float result back into an int when that is exact, so
// integer-valued results round-trip as ints.
func intIfExact(f float64) any {
	if f == math.Trunc(f) && f >= -(1<<53) && f <= 1<<53 && !(f == 0 && math.Signbit(f)) {
		return int(f)
	}
	return f
}

// kindOrder is jq's ordering between types.
func kindOrder(v any) int {
	switch v := v.(type) {
	case nil:
		return 0
	case bool:
		if v {
			return 2
		}
		return 1
	case int, float64:
		return 3
	case string:
		return 4
	case []any:
		return 5
	case map[string]any:
		return 6
	}
	if nv := top(v); isCanonicalTop(nv) {
		return kindOrder(nv)
	}
	return 7
}

// compare implements jq's total order over values.
func compare(a, b any) int {
	a, b = top(a), top(b)
	ka, kb := kindOrder(a), kindOrder(b)
	if ka != kb {
		if ka < kb {
			return -1
		}
		return 1
	}
	switch a := a.(type) {
	case int, float64:
		return compareNumbers(a, b)
	case string:
		bs := b.(string)
		switch {
		case a < bs:
			return -1
		case a > bs:
			return 1
		}
		return 0
	case []any:
		bs := b.([]any)
		for i := 0; i < len(a) && i < len(bs); i++ {
			if c := compare(a[i], bs[i]); c != 0 {
				return c
			}
		}
		return cmpInt(len(a), len(bs))
	case map[string]any:
		bm := b.(map[string]any)
		ak, bk := sortedKeys(a), sortedKeys(bm)
		for i := 0; i < len(ak) && i < len(bk); i++ {
			if ak[i] != bk[i] {
				if ak[i] < bk[i] {
					return -1
				}
				return 1
			}
		}
		if c := cmpInt(len(ak), len(bk)); c != 0 {
			return c
		}
		for _, k := range ak {
			if c := compare(a[k], bm[k]); c != 0 {
				return c
			}
		}
	}
	return 0
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// compareNumbers orders numbers the way jq does: NaN sorts below every
// number, including another NaN.
func compareNumbers(a, b any) int {
	if ai, ok := a.(int); ok {
		if bi, ok := b.(int); ok {
			return cmpInt(ai, bi)
		}
	}
	fa, _ := toFloat(a)
	fb, _ := toFloat(b)
	if math.IsNaN(fa) {
		return -1
	}
	if math.IsNaN(fb) {
		return 1
	}
	switch {
	case fa < fb:
		return -1
	case fa > fb:
		return 1
	}
	return 0
}

func equal(a, b any) bool {
	a, b = top(a), top(b)
	switch a := a.(type) {
	case nil:
		return b == nil
	case bool:
		bb, ok := b.(bool)
		return ok && a == bb
	case string:
		bs, ok := b.(string)
		return ok && a == bs
	case int:
		switch b := b.(type) {
		case int:
			return a == b
		case float64:
			return float64(a) == b
		}
		return false
	case float64:
		switch b := b.(type) {
		case int:
			return a == float64(b)
		case float64:
			return a == b
		}
		return false
	case []any:
		bs, ok := b.([]any)
		if !ok || len(a) != len(bs) {
			return false
		}
		for i := range a {
			if !equal(a[i], bs[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		bm, ok := b.(map[string]any)
		if !ok || len(a) != len(bm) {
			return false
		}
		for k, v := range a {
			bv, ok := bm[k]
			if !ok || !equal(v, bv) {
				return false
			}
		}
		return true
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func cloneMap(m map[string]any, extra int) map[string]any {
	out := make(map[string]any, len(m)+extra)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneSlice(s []any, extra int) []any {
	out := make([]any, len(s), len(s)+extra)
	copy(out, s)
	return out
}

// runeLen counts code points, which is what jq's length and string indices use.
func runeLen(s string) int {
	return utf8.RuneCountInString(s)
}

// substr slices a string by code point offsets.
func substr(s string, start, end int) string {
	if isASCII(s) {
		return s[start:end]
	}
	i, bstart, bend := 0, len(s), len(s)
	for off := range s {
		if i == start {
			bstart = off
		}
		if i == end {
			bend = off
			break
		}
		i++
	}
	return s[bstart:bend]
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}
