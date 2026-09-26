package jqgo

import (
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"
)

// Decoder reads a stream of whitespace-separated JSON values, the input
// format jq accepts. Objects become map[string]any, integers that fit
// become int and other numbers float64. Like jq it also accepts NaN,
// Infinity and a leading byte order mark.
//
// It scans an internal buffer directly: a string without escapes costs one
// allocation, numbers are parsed in place, and repeated object keys share
// one string.
type Decoder struct {
	r    io.Reader
	buf  []byte
	pos  int // next byte to read
	end  int // end of valid data in buf
	eof  bool
	rerr error // read error other than io.EOF

	off       int // stream offset of buf[0]
	line      int
	lineStart int // stream offset of the first byte of the current line
	started   bool
	keys      map[string]string // interned object keys
}

// NewDecoder returns a Decoder reading from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{r: r, buf: make([]byte, 64*1024), line: 1}
}

// newBytesDecoder decodes b without copying it; b is never written to.
func newBytesDecoder(b []byte) *Decoder {
	return &Decoder{buf: b, end: len(b), eof: true, line: 1}
}

// SyntaxError reports malformed JSON input.
type SyntaxError struct {
	Msg          string
	Line, Column int
}

func (e *SyntaxError) Error() string {
	return fmt.Sprintf("%s at line %d, column %d", e.Msg, e.Line, e.Column)
}

func parseJSON(b []byte) (any, error) {
	d := newBytesDecoder(b)
	v, err := d.Decode()
	if err != nil {
		if err == io.EOF {
			return nil, d.errorf("Expected JSON value")
		}
		return nil, err
	}
	if _, ok := d.skipSpace(); ok {
		return nil, d.errorf("Unexpected extra JSON values")
	}
	return v, nil
}

// Decode returns the next value, or io.EOF when the stream is exhausted.
func (d *Decoder) Decode() (any, error) {
	if !d.started {
		d.started = true
		// Only wait for three bytes when the first could start a BOM, so an
		// interactive stream is not held up.
		if d.ensure(1); d.pos < d.end && d.buf[d.pos] == 0xEF {
			d.ensure(3)
			if d.end-d.pos >= 3 && string(d.buf[d.pos:d.pos+3]) == "\xef\xbb\xbf" {
				d.pos += 3
			}
		}
	}
	c, ok := d.skipSpace()
	if !ok {
		if d.rerr != nil {
			return nil, d.rerr
		}
		return nil, io.EOF
	}
	d.pos++
	return d.value(c)
}

func (d *Decoder) errorf(format string, args ...any) error {
	return &SyntaxError{Msg: fmt.Sprintf(format, args...), Line: d.line, Column: d.off + d.pos - d.lineStart}
}

// fill reads more input, keeping buf[pos:end] and moving it to the front.
// Callers holding an index into buf must subtract the returned shift.
func (d *Decoder) fill() (shift int, ok bool) {
	if d.eof {
		return 0, false
	}
	if d.pos > 0 {
		shift = d.pos
		d.end = copy(d.buf, d.buf[d.pos:d.end])
		d.off += d.pos
		d.pos = 0
	}
	if d.end == len(d.buf) {
		nb := make([]byte, 2*len(d.buf))
		copy(nb, d.buf[:d.end])
		d.buf = nb
	}
	for {
		n, err := d.r.Read(d.buf[d.end:])
		d.end += n
		if err != nil {
			d.eof = true
			if err != io.EOF {
				d.rerr = err
			}
			return shift, n > 0
		}
		if n > 0 {
			return shift, true
		}
	}
}

// ensure tries to have n unread bytes buffered.
func (d *Decoder) ensure(n int) {
	for d.end-d.pos < n {
		if _, ok := d.fill(); !ok {
			return
		}
	}
}

// skipSpace returns the next non-space byte without consuming it.
func (d *Decoder) skipSpace() (byte, bool) {
	for {
		for d.pos < d.end {
			switch c := d.buf[d.pos]; c {
			case ' ', '\t', '\r':
				d.pos++
			case '\n':
				d.pos++
				d.line++
				d.lineStart = d.off + d.pos
			default:
				return c, true
			}
		}
		if _, ok := d.fill(); !ok {
			return 0, false
		}
	}
}

// next consumes the next non-space byte; running out of input is an error
// in the middle of a value.
func (d *Decoder) next() (byte, error) {
	c, ok := d.skipSpace()
	if !ok {
		if d.rerr != nil {
			return 0, d.rerr
		}
		return 0, d.errorf("Unfinished JSON term at EOF")
	}
	d.pos++
	return c, nil
}

func (d *Decoder) value(c byte) (any, error) {
	switch c {
	case '{':
		return d.object()
	case '[':
		return d.array()
	case '"':
		return d.str()
	case 't':
		return true, d.literal("rue")
	case 'f':
		return false, d.literal("alse")
	case 'n':
		d.ensure(1)
		if d.pos < d.end && d.buf[d.pos] == 'a' {
			return nan, d.literal("an")
		}
		return nil, d.literal("ull")
	case 'N':
		return nan, d.literal("aN")
	case 'I':
		return math.Inf(1), d.literal("nfinity")
	case '-':
		// Put the '-' back first: looking ahead may refill the buffer,
		// which drops everything before pos.
		d.pos--
		d.ensure(2)
		if d.end-d.pos >= 2 {
			switch d.buf[d.pos+1] {
			case 'I':
				d.pos += 2
				return math.Inf(-1), d.literal("nfinity")
			case 'N':
				d.pos += 2
				return nan, d.literal("aN")
			case 'n':
				d.pos += 2
				return nan, d.literal("an")
			}
		}
		return d.number()
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		d.pos-- // just consumed by next, still in the buffer
		return d.number()
	}
	return nil, d.errorf("Invalid literal %q", rune(c))
}

func (d *Decoder) literal(rest string) error {
	d.ensure(len(rest))
	if d.end-d.pos < len(rest) || string(d.buf[d.pos:d.pos+len(rest)]) != rest {
		return d.errorf("Invalid literal")
	}
	d.pos += len(rest)
	return d.checkDelim()
}

// checkDelim makes sure a scalar is not glued to the next token, e.g. "truex".
func (d *Decoder) checkDelim() error {
	d.ensure(1)
	if d.pos == d.end {
		return nil
	}
	switch d.buf[d.pos] {
	case ' ', '\t', '\n', '\r', ',', ']', '}', ':', '[', '{', '"':
		return nil
	}
	return d.errorf("Invalid literal")
}

// number parses a number starting at pos.
func (d *Decoder) number() (any, error) {
	i := d.pos
	for {
		if i == d.end {
			shift, ok := d.fill() // keeps buf[pos:], so the digits so far survive
			i -= shift
			if !ok {
				break
			}
			continue
		}
		c := d.buf[i]
		if (c >= '0' && c <= '9') || c == '-' || c == '+' || c == '.' || c == 'e' || c == 'E' {
			i++
			continue
		}
		break
	}
	b := d.buf[d.pos:i]
	d.pos = i
	s := unsafe.String(unsafe.SliceData(b), len(b)) // not retained
	if !validNumber(s) {
		return nil, d.errorf("Invalid numeric literal")
	}
	if n, ok := parseSmallInt(b); ok {
		return n, d.checkDelim()
	}
	if isIntLiteral(b) {
		if n, err := strconv.ParseInt(s, 10, 64); err == nil && int64(int(n)) == n {
			return int(n), d.checkDelim()
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return nil, d.errorf("Invalid numeric literal")
	}
	return f, d.checkDelim()
}

// parseSmallInt handles the common case of an integer with at most 18
// digits, which cannot overflow.
func parseSmallInt(b []byte) (int, bool) {
	neg := false
	if len(b) > 0 && b[0] == '-' {
		neg, b = true, b[1:]
	}
	if len(b) == 0 || len(b) > 18 {
		return 0, false
	}
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	return n, true
}

func isIntLiteral(b []byte) bool {
	for _, c := range b {
		if c == '.' || c == 'e' || c == 'E' {
			return false
		}
	}
	return true
}

// validNumber checks JSON number grammar (strconv is more permissive).
func validNumber(s string) bool {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	if i >= len(s) {
		return false
	}
	if s[i] == '0' {
		i++
	} else if s[i] >= '1' && s[i] <= '9' {
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
	} else {
		return false
	}
	if i < len(s) && s[i] == '.' {
		i++
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		start := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == start {
			return false
		}
	}
	return i == len(s)
}

// strBytes scans a string whose opening quote was consumed. Without
// escapes it returns a view into the buffer (valid until the next read);
// otherwise it decodes into a fresh slice.
func (d *Decoder) strBytes() ([]byte, error) {
	i := d.pos
	for {
		if i == d.end {
			shift, ok := d.fill()
			i -= shift
			if !ok {
				return nil, d.errorf("Unfinished string at EOF")
			}
			continue
		}
		switch c := d.buf[i]; {
		case c == '"':
			b := d.buf[d.pos:i]
			d.pos = i + 1
			return b, nil
		case c == '\\':
			return d.strSlow(i)
		case c == '\n':
			d.line++
			d.lineStart = d.off + i + 1
		}
		i++
	}
}

// strSlow continues a string that has escapes, from buf[i] onwards.
func (d *Decoder) strSlow(i int) ([]byte, error) {
	out := append([]byte(nil), d.buf[d.pos:i]...)
	d.pos = i
	for {
		if d.pos == d.end {
			if _, ok := d.fill(); !ok {
				return nil, d.errorf("Unfinished string at EOF")
			}
			continue
		}
		c := d.buf[d.pos]
		d.pos++
		switch c {
		case '"':
			return out, nil
		case '\\':
			var err error
			if out, err = d.escape(out); err != nil {
				return nil, err
			}
		case '\n':
			d.line++
			d.lineStart = d.off + d.pos
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
}

func (d *Decoder) str() (string, error) {
	b, err := d.strBytes()
	if err != nil {
		return "", err
	}
	return sanitizeUTF8(b), nil
}

func (d *Decoder) escape(out []byte) ([]byte, error) {
	d.ensure(1)
	if d.pos == d.end {
		return nil, d.errorf("Unfinished string at EOF")
	}
	c := d.buf[d.pos]
	d.pos++
	switch c {
	case '"', '\\', '/':
		return append(out, c), nil
	case 'b':
		return append(out, '\b'), nil
	case 'f':
		return append(out, '\f'), nil
	case 'n':
		return append(out, '\n'), nil
	case 'r':
		return append(out, '\r'), nil
	case 't':
		return append(out, '\t'), nil
	case 'u':
		r, err := d.hex4()
		if err != nil {
			return nil, err
		}
		if utf16.IsSurrogate(r) {
			// Try to pair with a following \uXXXX low surrogate.
			d.ensure(2)
			if d.end-d.pos >= 2 && d.buf[d.pos] == '\\' && d.buf[d.pos+1] == 'u' {
				d.pos += 2
				r2, err := d.hex4()
				if err != nil {
					return nil, err
				}
				if dec := utf16.DecodeRune(r, r2); dec != utf8.RuneError {
					return utf8.AppendRune(out, dec), nil
				}
				out = utf8.AppendRune(out, utf8.RuneError)
				if utf16.IsSurrogate(r2) {
					return utf8.AppendRune(out, utf8.RuneError), nil
				}
				return utf8.AppendRune(out, r2), nil
			}
			r = utf8.RuneError
		}
		return utf8.AppendRune(out, r), nil
	}
	return nil, d.errorf("Invalid escape")
}

func (d *Decoder) hex4() (rune, error) {
	d.ensure(4)
	if d.end-d.pos < 4 {
		return 0, d.errorf("Invalid \\uXXXX escape")
	}
	var r rune
	for _, c := range d.buf[d.pos : d.pos+4] {
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, d.errorf("Invalid \\uXXXX escape")
		}
	}
	d.pos += 4
	return r, nil
}

func (d *Decoder) array() (any, error) {
	arr := []any{}
	c, err := d.next()
	if err != nil {
		return nil, err
	}
	if c == ']' {
		return arr, nil
	}
	for {
		v, err := d.value(c)
		if err != nil {
			return nil, err
		}
		arr = append(arr, v)
		if c, err = d.next(); err != nil {
			return nil, err
		}
		if c == ']' {
			return arr, nil
		}
		if c != ',' {
			return nil, d.errorf("Expected separator between values")
		}
		if c, err = d.next(); err != nil {
			return nil, err
		}
	}
}

// key decodes an object key, sharing the string with earlier identical
// keys: arrays of records repeat the same few keys over and over.
func (d *Decoder) key() (string, error) {
	b, err := d.strBytes()
	if err != nil {
		return "", err
	}
	if len(b) > 64 || !utf8.Valid(b) {
		return sanitizeUTF8(b), nil
	}
	if s, ok := d.keys[string(b)]; ok { // no allocation for the lookup
		return s, nil
	}
	s := string(b)
	if d.keys == nil {
		d.keys = map[string]string{}
	}
	if len(d.keys) < 4096 {
		d.keys[s] = s
	}
	return s, nil
}

func (d *Decoder) object() (any, error) {
	obj := map[string]any{}
	c, err := d.next()
	if err != nil {
		return nil, err
	}
	if c == '}' {
		return obj, nil
	}
	for {
		if c != '"' {
			return nil, d.errorf("Object keys must be strings")
		}
		k, err := d.key()
		if err != nil {
			return nil, err
		}
		if c, err = d.next(); err != nil {
			return nil, err
		}
		if c != ':' {
			return nil, d.errorf("Objects must consist of key:value pairs")
		}
		if c, err = d.next(); err != nil {
			return nil, err
		}
		v, err := d.value(c)
		if err != nil {
			return nil, err
		}
		obj[k] = v
		if c, err = d.next(); err != nil {
			return nil, err
		}
		if c == '}' {
			return obj, nil
		}
		if c != ',' {
			return nil, d.errorf("Expected separator between values")
		}
		if c, err = d.next(); err != nil {
			return nil, err
		}
	}
}
