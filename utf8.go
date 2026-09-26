package jqgo

import (
	"strings"
	"unicode/utf8"
)

// sanitizeUTF8 converts b to a string, replacing invalid UTF-8 the way jq
// does (jvp_utf8_next): a stray byte becomes one U+FFFD, a sequence with
// the right lead byte becomes one U+FFFD up to its first non-continuation
// byte, and a sequence cut short by the end of the string swallows the
// rest of it. This differs from Go's one-replacement-per-byte rule.
func sanitizeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b) + 8)
	for i := 0; i < len(b); {
		r, n := jqNextRune(b[i:])
		if r < 0 {
			sb.WriteString("\ufffd")
		} else {
			sb.Write(b[i : i+n])
		}
		i += n
	}
	return sb.String()
}

// jqNextRune decodes one code point, returning -1 for an invalid sequence
// and the number of bytes it covers.
func jqNextRune(b []byte) (rune, int) {
	first := b[0]
	var length int
	switch {
	case first < 0x80:
		return rune(first), 1
	case first >= 0xC2 && first <= 0xDF:
		length = 2
	case first >= 0xE0 && first <= 0xEF:
		length = 3
	case first >= 0xF0 && first <= 0xF4:
		length = 4
	default: // continuation byte, C0, C1, F5..FF
		return -1, 1
	}
	if length > len(b) {
		return -1, len(b)
	}
	cp := rune(first & (0xFF >> (length + 1)))
	for i := 1; i < length; i++ {
		c := b[i]
		if c < 0x80 || c > 0xBF {
			return -1, i
		}
		cp = cp<<6 | rune(c&0x3F)
	}
	min := [...]rune{0, 0, 0x80, 0x800, 0x10000}[length]
	if cp < min || (cp >= 0xD800 && cp <= 0xDFFF) || cp > 0x10FFFF {
		return -1, length
	}
	return cp, length
}
