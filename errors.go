package jqgo

import (
	"fmt"
	"unicode/utf8"
)

// Runtime error messages that follow jq's wording. Scripts match on them
// and jq's test suite checks them, so they are part of the compatibility
// surface and are built here rather than spelled out at each call site.

// errIndex is t[k] failing: `Cannot index number with string "a"`.
func errIndex(t, k any) error {
	if ks, ok := k.(string); ok {
		return fmt.Errorf("Cannot index %s with string %q", typeName(t), ks)
	}
	return fmt.Errorf("Cannot index %s with %s", typeName(t), typeName(k))
}

func errIterate(v any) error {
	return fmt.Errorf("Cannot iterate over %s", typeDump(v))
}

func errNotSortable(v any) error {
	return fmt.Errorf("%s cannot be sorted, as it is not an array", typeDump(v))
}

func errNumberRequired(v any) error {
	return fmt.Errorf("%s number required", typeDump(v))
}

func errNotMatchable(v any) error {
	return fmt.Errorf("%s cannot be matched, as it is not a string", typeDump(v))
}

// opError is a binary operator rejecting its operands:
// `string ("a") and number (1) cannot be added`.
func opError(l, r any, verb string) error {
	return fmt.Errorf("%s and %s cannot be %s", typeDump(l), typeDump(r), verb)
}

// Path expression errors: a computed (not located) value reached a place
// that needs a path.
func errPathResult(v any) error {
	return fmt.Errorf("Invalid path expression with result %s", dumpTrunc(v))
}

func errPathAccess(key, v any) error {
	return fmt.Errorf("Invalid path expression near attempt to access element %s of %s", dumpTrunc(key), dumpTrunc(v))
}

func errPathIterate(v any) error {
	return fmt.Errorf("Invalid path expression near attempt to iterate through %s", dumpTrunc(v))
}

// typeDump is the "type (value)" phrase jq uses in error messages.
func typeDump(v any) string {
	return typeName(v) + " (" + dumpTrunc(v) + ")"
}

// dumpTrunc renders v for an error message, cut to jq's 11 characters.
func dumpTrunc(v any) string {
	s := toJSON(v)
	if len(s) > 14 {
		cut := 11
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		return s[:cut] + "..."
	}
	return s
}
