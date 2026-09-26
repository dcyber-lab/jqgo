package jqgo

import (
	"bytes"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// EncodeOptions controls how Marshal renders JSON.
type EncodeOptions struct {
	// Indent is the number of spaces per level; 0 means compact output.
	Indent int
	// Tab indents with one tab per level (overrides Indent).
	Tab bool
	// ASCII escapes every non-ASCII character as \uXXXX.
	ASCII bool
	// Colors, when non-nil, wraps values in ANSI escapes (see DefaultColors).
	Colors *Colors
}

// Colors holds the SGR parameters used for each kind of value, in the same
// format as the JQ_COLORS environment variable.
type Colors struct {
	Null, False, True, Number, String, Array, Object, ObjectKey string
}

// DefaultColors matches jq 1.7.1.
var DefaultColors = Colors{
	Null: "1;30", False: "0;39", True: "0;39", Number: "0;39",
	String: "0;32", Array: "1;39", Object: "1;39", ObjectKey: "34;1",
}

// ParseColors applies a JQ_COLORS-style spec ("null:false:true:numbers:
// strings:arrays:objects:objkeys") on top of DefaultColors.
func ParseColors(spec string) (*Colors, bool) {
	c := DefaultColors
	fields := []*string{&c.Null, &c.False, &c.True, &c.Number, &c.String, &c.Array, &c.Object, &c.ObjectKey}
	for i, part := range strings.Split(spec, ":") {
		if i >= len(fields) {
			break
		}
		for _, r := range part {
			if r != ';' && (r < '0' || r > '9') {
				return nil, false
			}
		}
		*fields[i] = part
	}
	return &c, true
}

// Marshal encodes v as compact JSON the way jq prints it: object keys
// sorted, NaN as null, infinities as ±1.7976931348623157e+308.
func Marshal(v any) []byte {
	var buf bytes.Buffer
	(&encoder{buf: &buf}).encode(v, 0)
	return buf.Bytes()
}

// MarshalWith encodes v using opts.
func MarshalWith(v any, opts EncodeOptions) []byte {
	var buf bytes.Buffer
	newEncoder(&buf, opts).encode(v, 0)
	return buf.Bytes()
}

type encoder struct {
	buf    *bytes.Buffer
	indent string
	ascii  bool
	colors *Colors
}

func newEncoder(buf *bytes.Buffer, opts EncodeOptions) *encoder {
	e := &encoder{buf: buf, ascii: opts.ASCII, colors: opts.Colors}
	if opts.Tab {
		e.indent = "\t"
	} else if opts.Indent > 0 {
		e.indent = strings.Repeat(" ", opts.Indent)
	}
	return e
}

func toJSON(v any) string {
	return string(Marshal(v))
}

func (e *encoder) color(c string) {
	if e.colors != nil {
		e.buf.WriteString("\x1b[")
		e.buf.WriteString(c)
		e.buf.WriteByte('m')
	}
}

func (e *encoder) reset() {
	if e.colors != nil {
		e.buf.WriteString("\x1b[0m")
	}
}

func (e *encoder) newline(level int) {
	if e.indent == "" {
		return
	}
	e.buf.WriteByte('\n')
	for i := 0; i < level; i++ {
		e.buf.WriteString(e.indent)
	}
}

func (e *encoder) encode(v any, level int) {
	c := e.colors
	switch v := v.(type) {
	case nil:
		if c != nil {
			e.color(c.Null)
		}
		e.buf.WriteString("null")
		e.reset()
	case bool:
		if c != nil {
			if v {
				e.color(c.True)
			} else {
				e.color(c.False)
			}
		}
		if v {
			e.buf.WriteString("true")
		} else {
			e.buf.WriteString("false")
		}
		e.reset()
	case int:
		if c != nil {
			e.color(c.Number)
		}
		e.buf.WriteString(strconv.Itoa(v))
		e.reset()
	case float64:
		if c != nil {
			e.color(c.Number)
		}
		e.buf.WriteString(formatFloat(v))
		e.reset()
	case string:
		if c != nil {
			e.color(c.String)
		}
		e.writeString(v)
		e.reset()
	case []any:
		if c != nil {
			e.color(c.Array)
		}
		e.buf.WriteByte('[')
		if len(v) == 0 {
			e.buf.WriteByte(']')
			e.reset()
			return
		}
		for i, x := range v {
			if i > 0 {
				if c != nil {
					e.color(c.Array)
				}
				e.buf.WriteByte(',')
			}
			e.reset()
			e.newline(level + 1)
			e.encode(x, level+1)
		}
		if c != nil {
			e.color(c.Array)
		}
		e.newline(level)
		if c != nil {
			e.color(c.Array)
		}
		e.buf.WriteByte(']')
		e.reset()
	case map[string]any:
		if c != nil {
			e.color(c.Object)
		}
		e.buf.WriteByte('{')
		if len(v) == 0 {
			e.buf.WriteByte('}')
			e.reset()
			return
		}
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for i, k := range keys {
			if i > 0 {
				if c != nil {
					e.color(c.Object)
				}
				e.buf.WriteByte(',')
			}
			e.reset()
			e.newline(level + 1)
			if c != nil {
				e.color(c.ObjectKey)
				e.writeString(k)
				e.reset()
				e.color(c.Object)
				e.buf.WriteByte(':')
				e.reset()
			} else {
				e.writeString(k)
				e.buf.WriteByte(':')
			}
			if e.indent != "" {
				e.buf.WriteByte(' ')
			}
			e.encode(v[k], level+1)
		}
		if c != nil {
			e.color(c.Object)
		}
		e.newline(level)
		if c != nil {
			e.color(c.Object)
		}
		e.buf.WriteByte('}')
		e.reset()
	default:
		// A value from the caller that has not been converted yet.
		if nv := top(v); isCanonicalTop(nv) {
			e.encode(nv, level)
			return
		}
		e.writeString(typeName(v))
	}
}

// formatFloat prints a float the way jq does when it has no literal to
// preserve: integers without a fraction up to 1e17, shortest round-trip
// representation otherwise.
func formatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "null"
	case math.IsInf(f, 1) || f > math.MaxFloat64:
		return "1.7976931348623157e+308"
	case math.IsInf(f, -1) || f < -math.MaxFloat64:
		return "-1.7976931348623157e+308"
	}
	a := math.Abs(f)
	if a == 0 || (a >= 1e-5 && a < 1e17) {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return strconv.FormatFloat(f, 'e', -1, 64)
}

const hexDigits = "0123456789abcdef"

func (e *encoder) writeString(s string) {
	b := e.buf
	b.WriteByte('"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' && c != 0x7f {
				i++
				continue
			}
			b.WriteString(s[start:i])
			switch c {
			case '"':
				b.WriteString(`\"`)
			case '\\':
				b.WriteString(`\\`)
			case '\n':
				b.WriteString(`\n`)
			case '\t':
				b.WriteString(`\t`)
			case '\r':
				b.WriteString(`\r`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			default:
				b.WriteString(`\u00`)
				b.WriteByte(hexDigits[c>>4])
				b.WriteByte(hexDigits[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteString(s[start:i])
			if e.ascii {
				b.WriteString(`\ufffd`)
			} else {
				b.WriteString("\ufffd")
			}
			i += size
			start = i
			continue
		}
		if e.ascii {
			b.WriteString(s[start:i])
			writeUEscape(b, r)
			i += size
			start = i
			continue
		}
		i += size
	}
	b.WriteString(s[start:])
	b.WriteByte('"')
}

func writeUEscape(b *bytes.Buffer, r rune) {
	if r > 0xffff {
		r -= 0x10000
		writeUEscape(b, 0xd800+(r>>10))
		writeUEscape(b, 0xdc00+(r&0x3ff))
		return
	}
	b.WriteString(`\u`)
	b.WriteByte(hexDigits[r>>12&0xf])
	b.WriteByte(hexDigits[r>>8&0xf])
	b.WriteByte(hexDigits[r>>4&0xf])
	b.WriteByte(hexDigits[r&0xf])
}
