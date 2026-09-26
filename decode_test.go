package jqgo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"unicode/utf8"
)

// decodeAll decodes a whole stream, recording values and the first error.
func decodeAll(d *Decoder) (vals []string, errText string) {
	for {
		v, err := d.Decode()
		if err == io.EOF {
			return vals, ""
		}
		if err != nil {
			return vals, err.Error()
		}
		vals = append(vals, toJSON(v))
	}
}

func checkDecode(t *testing.T, data []byte) {
	// The same stream read in one piece and one byte at a time: token
	// boundaries fall on buffer boundaries in the second case.
	whole, werr := decodeAll(newBytesDecoder(data))
	bytewise, berr := decodeAll(NewDecoder(iotest.OneByteReader(bytes.NewReader(data))))
	if strings.Join(whole, "\n") != strings.Join(bytewise, "\n") || (werr == "") != (berr == "") {
		t.Fatalf("%q:\n whole:    %v %q\n bytewise: %v %q", data, whole, werr, bytewise, berr)
	}
	// Whatever encoding/json accepts must decode to the same value. (It
	// replaces invalid UTF-8 byte by byte; jqgo follows jq, see
	// TestInvalidUTF8.)
	if !json.Valid(data) || !utf8.Valid(data) {
		return
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var std any
	if err := dec.Decode(&std); err != nil {
		return
	}
	want, err := Normalize(std)
	if err != nil {
		t.Fatalf("%q: normalize: %v", data, err)
	}
	got, err := parseJSON(data)
	if err != nil {
		t.Fatalf("%q: encoding/json accepts it, jqgo says %v", data, err)
	}
	if !equal(got, want) && toJSON(got) != toJSON(want) {
		t.Fatalf("%q: got %s, encoding/json %s", data, toJSON(got), toJSON(want))
	}
}

var decodeSeeds = []string{
	`{"a":[1,-2.5e3,true,false,null],"b":{"c":"d\n\"\\\/\u00e9\ud83d\ude00"}}`,
	`[0, -0, 1e400, -1e400, 12345678901234567890, 9007199254740993, 1.5E-10]`,
	"1 2\n3\t[4] {\"x\":5}\"s\"",
	"\xef\xbb\xbf{\"bom\":1}",
	`[NaN, -Infinity, Infinity, nan, -nan]`,
	`"\ud800" "\udc00\ud800x" "bad \x" "\u12"`,
	`{"a" 1}`, `[1,]`, `01`, `1.`, `tru`, `"abc`, `[1 2]`, `{"a":1,}`,
	`"` + strings.Repeat("long string ", 1000) + `"`,
	`[` + strings.Repeat(`{"key":"value","n":123},`, 500) + `0]`,
}

func TestDecodeEquivalence(t *testing.T) {
	for _, s := range decodeSeeds {
		checkDecode(t, []byte(s))
	}
	// the test suites' inputs, as one stream
	var all bytes.Buffer
	for _, file := range []string{"jq/jq.test", "jq/man.test", "extra.test"} {
		for _, c := range readJQTests(t, "testdata/"+file) {
			all.WriteString(c.input + "\n")
			checkDecode(t, []byte(c.input))
		}
	}
	checkDecode(t, all.Bytes())
}

func FuzzDecode(f *testing.F) {
	for _, s := range decodeSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkDecode(t, data)
	})
}

// Expected outputs from jq 1.7: explode of strings with invalid UTF-8.
func TestInvalidUTF8(t *testing.T) {
	for in, want := range map[string]string{
		"\"\x82\x96\"":     `[65533,65533]`,
		"\"a\xe2\x82b\"":   `[97,65533,98]`,
		"\"\xf0\x9f\x98\"": `[65533]`,
		"\"\xed\xa0\x80|\xc0\xaf|\xf5\x80\x80\x80|\xe2a\"": `[65533,124,65533,65533,124,65533,65533,65533,65533,124,65533]`,
		"\"x\xe2\x82\"": `[120,65533]`,
	} {
		v, err := parseJSON([]byte(in))
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		got, err := MustCompile(`explode`).First(context.Background(), v)
		if err != nil || toJSON(got) != want {
			t.Errorf("%q: got %s %v, want %s", in, toJSON(got), err, want)
		}
	}
}
