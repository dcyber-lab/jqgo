package jqgo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

// Streaming must be invisible too: RunReader on the text of a value gives
// what Run gives on the value.

// runOnText runs q with Run on each value of text in turn, stopping each
// run at its first error, the way RunReader treats every top-level value.
func runOnText(q *Query, text string) (runRecord, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var rec runRecord
	d := NewDecoder(strings.NewReader(text))
	for {
		v, err := d.Decode()
		if err == io.EOF {
			return rec, nil
		}
		if err != nil {
			return rec, err
		}
		for v, err := range q.exec(ctx, v, decoderInputs{d: d}, evalOptions{}, nil) {
			if err != nil {
				rec.err += err.Error() + ";"
				if isHalt(err) {
					return rec, nil
				}
				break
			}
			rec.outputs = append(rec.outputs, toJSON(v))
			if len(rec.outputs) > 200 {
				return rec, nil
			}
		}
	}
}

func readerRecord(q *Query, r io.Reader) runRecord {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var rec runRecord
	for v, err := range q.RunReader(ctx, r) {
		if err != nil {
			rec.addErr(err)
			continue
		}
		rec.outputs = append(rec.outputs, toJSON(v))
		if len(rec.outputs) > 200 {
			break
		}
	}
	return rec
}

// checkStream compares Run and RunReader on text, reading it whole and one
// byte at a time (to cross every buffer boundary).
func checkStream(t *testing.T, q *Query, program, text string) {
	t.Helper()
	want, err := runOnText(q, text)
	if err != nil {
		return // not valid JSON: covered by the decoder tests
	}
	for _, r := range []io.Reader{strings.NewReader(text), iotest.OneByteReader(strings.NewReader(text))} {
		got := readerRecord(q, r)
		if inconclusive(want, got) {
			return
		}
		if strings.Join(got.outputs, "\n") != strings.Join(want.outputs, "\n") || got.err != want.err {
			t.Errorf("%s on %s (streamable: %v):\n reader: %v %q\n    run: %v %q",
				program, text, q.Streamable(), got.outputs, got.err, want.outputs, want.err)
			return
		}
	}
}

var streamPrograms = []string{
	`.[]`, `.[]?`, `.a[]`, `.a.b[]`, `.["a"][]`, `.a["b"][]`,
	`.[] | . * 2`, `.a[] | select(. > 1)`, `[.[]]`, `[.a[] | tostring]`,
	`reduce .[] as $x (0; . + $x)`, `reduce .a[] as [$x] (null; [$x, .])`,
	`foreach .[] as $x (0; . + 1; [$x, .])`,
	`first(.[])`, `first(.a[])`, `limit(2; .[])`, `limit(0; .[])`, `isempty(.[])`,
	`label $out | .[] | if . == 2 then break $out else . end`,
	`try (.[] | if . == 2 then error("two") else . end) catch "caught: \(.)"`,
	`.[] | error`, `(.[] | tojson), "end"`, `"start", .a[]`, `empty, .[]`,
	`def f: . + 1; .[] | f`, `[limit(3; .[])] | length`,
	`.[] as $x | $x`, // not streamable: the binding sees the input
	`.[], .`,         // not streamable: . is used twice
	`.a[], .b`,       // not streamable
}

var streamInputs = []string{
	`[1,2,3]`, `[]`, `{"a":[1,2,3],"b":2}`, `{"b":0,"a":[[1],[2]]}`,
	`{"a":{"b":[4,5]}}`, `{"a":{"b":{"x":1}}}`, `{"a":null}`, `{}`, `null`,
	`1`, `"s"`, `true`, `{"a":1}`, `{"a":"x"}`,
	`[1,2,3] {"a":[4]} [5]`, `[[1,2],{"x":3}]`, `{"x":1,"y":2}`,
	` [1, 2 ,3 ] `, `{"a":{"b":null}}`, `{"a":[]}`, `[1,2,3,4,5,6]`,
	`{"a":{"c":1,"b":[7]}}`, `{"a":[1,"x"]}`, `[1,[2,[3]]]`,
}

func TestStreamEquivalence(t *testing.T) {
	streamed := 0
	for _, p := range streamPrograms {
		q := MustCompile(p, WithDebugWriter(nil))
		if q.Streamable() {
			streamed++
		}
		for _, in := range streamInputs {
			checkStream(t, q, p, in)
		}
	}
	if streamed < len(streamPrograms)-3 {
		t.Errorf("only %d of %d programs are streamable", streamed, len(streamPrograms))
	}
}

func TestStreamDuplicateKeys(t *testing.T) {
	// Documented difference: the first occurrence is streamed, where a
	// decoded object keeps the last.
	q := MustCompile(`.a[]`)
	got := readerRecord(q, strings.NewReader(`{"a":[1],"b":0,"a":[2]}`))
	if strings.Join(got.outputs, ",") != "1" || got.err != "" {
		t.Errorf("got %v %q", got.outputs, got.err)
	}
}

func TestStreamSuite(t *testing.T) {
	streamed := 0
	for _, file := range []string{"jq/jq.test", "jq/man.test", "jq/onig.test", "extra.test"} {
		for _, c := range readJQTests(t, filepath.Join("testdata", file)) {
			if c.fail {
				continue
			}
			q, err := Compile(c.program, WithDebugWriter(nil), WithEnviron([]string{"PAGER=less"}))
			if err != nil {
				continue
			}
			if q.Streamable() {
				streamed++
			}
			checkStream(t, q, c.program, c.input)
			checkStream(t, q, c.program, c.input+"\n"+c.input) // two values in a row
		}
	}
	t.Logf("%d streamable cases", streamed)
	if streamed < 50 {
		t.Errorf("only %d streamable cases", streamed)
	}
}

func FuzzStream(f *testing.F) {
	addSuiteSeeds(f)
	for i, p := range streamPrograms {
		f.Add(p, streamInputs[i%len(streamInputs)])
	}
	f.Fuzz(func(t *testing.T, program, input string) {
		q, err := Compile(program, WithDebugWriter(nil))
		if err != nil {
			return
		}
		checkStream(t, q, program, input)
	})
}

func TestStreamable(t *testing.T) {
	for _, tc := range []struct {
		program string
		want    bool
	}{
		{`.[]`, true},
		{`.items[] | select(.price > 10) | .name`, true},
		{`[.a.b[] | .x] | length`, true},
		{`reduce .rows[] as $r ({}; .[$r.k] += 1)`, true},
		{`first(.[] | select(.id == 7))`, true},
		{`.`, false},
		{`.a`, false},
		{`.[] | ., input`, false},
		{`.[$k][]`, false},
		{`.[0][]`, false},
		{`.a[], .b[]`, false},
		{`reduce .[] as $x (.; . + $x)`, false},
		{`limit(.n; .[])`, false},
		{`.[] |= . + 1`, false},
		{`path(.[])`, false},
		{`def f: .[]; f`, false},
	} {
		q := MustCompile(tc.program, WithVariables("k"))
		if got := q.Streamable(); got != tc.want {
			t.Errorf("Streamable(%s) = %v, want %v", tc.program, got, tc.want)
		}
	}
}

// bigArray produces `{"meta":{...},"items":[{"id":0,...},...],"tail":1}`
// without ever holding it in memory.
type bigArray struct {
	n, i  int
	state int
	buf   []byte
}

func (b *bigArray) Read(p []byte) (int, error) {
	for len(b.buf) == 0 {
		switch b.state {
		case 0:
			b.buf = []byte(`{"meta":{"skip":[1,2,{"x":"y"}]},"items":[`)
			b.state = 1
		case 1:
			if b.i == b.n {
				b.buf = []byte(`],"tail":1}`)
				b.state = 2
				break
			}
			sep := ","
			if b.i == 0 {
				sep = ""
			}
			b.buf = fmt.Appendf(nil, `%s{"id":%d,"name":"item-%d","tags":["a","b","c"],"pad":"%s"}`, sep, b.i, b.i, strings.Repeat("x", 100))
			b.i++
		default:
			return 0, io.EOF
		}
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}

func TestStreamMemory(t *testing.T) {
	if testing.Short() {
		t.Skip("reads 200 MB")
	}
	const n = 1_500_000 // about 200 MB of JSON
	q := MustCompile(`.items[] | select(.id % 500000 == 0) | .name`)
	if !q.Streamable() {
		t.Fatal("not streamable")
	}
	var peak uint64
	var ms runtime.MemStats
	var got []any
	count := 0
	for v, err := range q.RunReader(context.Background(), &bigArray{n: n}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
		count++
		runtime.ReadMemStats(&ms)
		peak = max(peak, ms.HeapInuse)
	}
	if fmt.Sprint(got) != "[item-0 item-500000 item-1000000]" {
		t.Errorf("got %v", got)
	}
	runtime.ReadMemStats(&ms)
	peak = max(peak, ms.HeapInuse)
	if peak > 64<<20 {
		t.Errorf("heap in use reached %d MB", peak>>20)
	}
}

func TestStreamEarlyExit(t *testing.T) {
	// Stopping the loop must not read the rest of a huge value.
	q := MustCompile(`.items[]`)
	r := &bigArray{n: 1 << 40}
	for range q.RunReader(context.Background(), r) {
		break
	}
	if r.i > 100_000 {
		t.Errorf("read %d elements after the consumer stopped", r.i)
	}
}

func TestStreamCancel(t *testing.T) {
	q := MustCompile(`.items[] | select(false)`)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	var last error
	for _, err := range q.RunReader(ctx, &bigArray{n: 1 << 40}) {
		last = err
	}
	if !errors.Is(last, context.DeadlineExceeded) {
		t.Errorf("got %v, want a deadline error", last)
	}
}
