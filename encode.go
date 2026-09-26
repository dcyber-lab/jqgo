package jqgo

import (
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
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
	return MarshalWith(v, EncodeOptions{})
}

// MarshalWith encodes v using opts.
func MarshalWith(v any, opts EncodeOptions) []byte {
	e := getEncoder(opts)
	e.encode(v, 0)
	var out []byte
	if len(e.buf) <= 4<<10 {
		out = slices.Clone(e.buf) // small: keep the pooled buffer for reuse
	} else {
		out, e.buf = e.buf, nil // large: hand it over rather than copy it
	}
	putEncoder(e)
	return out
}

func toJSON(v any) string {
	e := getEncoder(EncodeOptions{})
	e.encode(v, 0)
	s := string(e.buf)
	putEncoder(e)
	return s
}

// Encoder writes values to a stream the way jq prints them, without
// building the whole text in memory first.
type Encoder struct {
	e encoder
}

// NewEncoder returns an Encoder writing to w.
func NewEncoder(w io.Writer, opts EncodeOptions) *Encoder {
	enc := &Encoder{}
	enc.e.reset(opts)
	enc.e.w = w
	return enc
}

// Encode writes v, with no trailing newline.
func (enc *Encoder) Encode(v any) error {
	enc.e.encode(v, 0)
	enc.e.flush()
	err := enc.e.err
	enc.e.err = nil
	return err
}

// flushAt is how much an Encoder buffers before writing.
const flushAt = 32 << 10

type encoder struct {
	buf    []byte
	indent string
	ascii  bool
	colors *Colors
	keys   [][]string // sort buffers, one per nesting level
	w      io.Writer  // Encoder only
	err    error
}

var encoderPool = sync.Pool{New: func() any { return new(encoder) }}

func getEncoder(opts EncodeOptions) *encoder {
	e := encoderPool.Get().(*encoder)
	e.reset(opts)
	return e
}

func putEncoder(e *encoder) {
	if cap(e.buf) > 1<<20 {
		e.buf = nil // do not pin huge buffers
	}
	encoderPool.Put(e)
}

func (e *encoder) reset(opts EncodeOptions) {
	e.buf = e.buf[:0]
	e.ascii, e.colors, e.w, e.err = opts.ASCII, opts.Colors, nil, nil
	switch {
	case opts.Tab:
		e.indent = "\t"
	case opts.Indent > 0:
		e.indent = strings.Repeat(" ", opts.Indent)
	default:
		e.indent = ""
	}
}

// flush hands the buffer to the Encoder's writer.
func (e *encoder) flush() {
	if e.w == nil || len(e.buf) == 0 {
		return
	}
	if e.err == nil {
		_, e.err = e.w.Write(e.buf)
	}
	e.buf = e.buf[:0]
}

// grow is called between elements: it flushes an Encoder's buffer, and
// otherwise doubles a filling buffer (append grows big slices by only
// ~1.25x, which means many more copies for large outputs).
func (e *encoder) grow() {
	if e.w != nil {
		if len(e.buf) >= flushAt {
			e.flush()
		}
		return
	}
	if cap(e.buf)-len(e.buf) < 512 {
		e.buf = slices.Grow(e.buf, len(e.buf)+512)
	}
}

func (e *encoder) color(c string) {
	if e.colors != nil {
		e.buf = append(e.buf, "\x1b["...)
		e.buf = append(e.buf, c...)
		e.buf = append(e.buf, 'm')
	}
}

func (e *encoder) reset0() {
	if e.colors != nil {
		e.buf = append(e.buf, "\x1b[0m"...)
	}
}

func (e *encoder) newline(level int) {
	if e.indent == "" {
		return
	}
	e.buf = append(e.buf, '\n')
	for i := 0; i < level; i++ {
		e.buf = append(e.buf, e.indent...)
	}
}

// sortedKeys returns m's keys sorted, in a buffer reused per level.
func (e *encoder) sortedKeys(m map[string]any, level int) []string {
	for len(e.keys) <= level {
		e.keys = append(e.keys, nil)
	}
	keys := e.keys[level][:0]
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	e.keys[level] = keys
	return keys
}

func (e *encoder) encode(v any, level int) {
	c := e.colors
	switch v := v.(type) {
	case nil:
		if c != nil {
			e.color(c.Null)
		}
		e.buf = append(e.buf, "null"...)
		e.reset0()
	case bool:
		if c != nil {
			if v {
				e.color(c.True)
			} else {
				e.color(c.False)
			}
		}
		if v {
			e.buf = append(e.buf, "true"...)
		} else {
			e.buf = append(e.buf, "false"...)
		}
		e.reset0()
	case int:
		if c != nil {
			e.color(c.Number)
		}
		e.buf = strconv.AppendInt(e.buf, int64(v), 10)
		e.reset0()
	case float64:
		if c != nil {
			e.color(c.Number)
		}
		e.buf = appendFloat(e.buf, v)
		e.reset0()
	case string:
		if c != nil {
			e.color(c.String)
		}
		e.writeString(v)
		e.reset0()
	case []any:
		if c != nil {
			e.color(c.Array)
		}
		e.buf = append(e.buf, '[')
		if len(v) == 0 {
			e.buf = append(e.buf, ']')
			e.reset0()
			return
		}
		for i, x := range v {
			if i > 0 {
				if c != nil {
					e.color(c.Array)
				}
				e.buf = append(e.buf, ',')
			}
			e.reset0()
			e.newline(level + 1)
			e.encode(x, level+1)
			e.grow()
		}
		if c != nil {
			e.color(c.Array)
		}
		e.newline(level)
		if c != nil {
			e.color(c.Array)
		}
		e.buf = append(e.buf, ']')
		e.reset0()
	case map[string]any:
		if c != nil {
			e.color(c.Object)
		}
		e.buf = append(e.buf, '{')
		if len(v) == 0 {
			e.buf = append(e.buf, '}')
			e.reset0()
			return
		}
		for i, k := range e.sortedKeys(v, level) {
			if i > 0 {
				if c != nil {
					e.color(c.Object)
				}
				e.buf = append(e.buf, ',')
			}
			e.reset0()
			e.newline(level + 1)
			if c != nil {
				e.color(c.ObjectKey)
				e.writeString(k)
				e.reset0()
				e.color(c.Object)
				e.buf = append(e.buf, ':')
				e.reset0()
			} else {
				e.writeString(k)
				e.buf = append(e.buf, ':')
			}
			if e.indent != "" {
				e.buf = append(e.buf, ' ')
			}
			e.encode(v[k], level+1)
			e.grow()
		}
		if c != nil {
			e.color(c.Object)
		}
		e.newline(level)
		if c != nil {
			e.color(c.Object)
		}
		e.buf = append(e.buf, '}')
		e.reset0()
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
	return string(appendFloat(nil, f))
}

func appendFloat(b []byte, f float64) []byte {
	switch {
	case math.IsNaN(f):
		return append(b, "null"...)
	case math.IsInf(f, 1) || f > math.MaxFloat64:
		return append(b, "1.7976931348623157e+308"...)
	case math.IsInf(f, -1) || f < -math.MaxFloat64:
		return append(b, "-1.7976931348623157e+308"...)
	}
	a := math.Abs(f)
	if a == 0 || (a >= 1e-5 && a < 1e17) {
		return strconv.AppendFloat(b, f, 'f', -1, 64)
	}
	return strconv.AppendFloat(b, f, 'e', -1, 64)
}

const hexDigits = "0123456789abcdef"

// safeASCII marks the ASCII bytes that need no escaping.
var safeASCII = func() (t [utf8.RuneSelf]bool) {
	for c := 0x20; c < utf8.RuneSelf; c++ {
		t[c] = c != '"' && c != '\\' && c != 0x7f
	}
	return
}()

func (e *encoder) writeString(s string) {
	b := append(e.buf, '"')
	// Valid UTF-8 needs no work on its multibyte sequences unless they are
	// escaped (-a); check once instead of decoding rune by rune.
	passMulti := !e.ascii && utf8.ValidString(s)
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if safeASCII[c] {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '"':
				b = append(b, `\"`...)
			case '\\':
				b = append(b, `\\`...)
			case '\n':
				b = append(b, `\n`...)
			case '\t':
				b = append(b, `\t`...)
			case '\r':
				b = append(b, `\r`...)
			case '\b':
				b = append(b, `\b`...)
			case '\f':
				b = append(b, `\f`...)
			default:
				b = append(b, `\u00`...)
				b = append(b, hexDigits[c>>4], hexDigits[c&0xf])
			}
			i++
			start = i
			continue
		}
		if passMulti {
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			if e.ascii {
				b = append(b, `\ufffd`...)
			} else {
				b = append(b, "\ufffd"...)
			}
			i += size
			start = i
			continue
		}
		if e.ascii {
			b = append(b, s[start:i]...)
			b = appendUEscape(b, r)
			i += size
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	e.buf = append(b, '"')
}

func appendUEscape(b []byte, r rune) []byte {
	if r > 0xffff {
		r -= 0x10000
		b = appendUEscape(b, 0xd800+(r>>10))
		return appendUEscape(b, 0xdc00+(r&0x3ff))
	}
	return append(b, '\\', 'u', hexDigits[r>>12&0xf], hexDigits[r>>8&0xf], hexDigits[r>>4&0xf], hexDigits[r&0xf])
}
