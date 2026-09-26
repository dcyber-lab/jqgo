package jqgo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func mustAll(t *testing.T, q *Query, input any, vars ...any) []any {
	t.Helper()
	out, err := q.All(context.Background(), input, vars...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return out
}

func TestVariables(t *testing.T) {
	q := MustCompile(`.items[] | select(.price > $min) | .name`, WithVariables("$min"))
	input := map[string]any{"items": []any{
		map[string]any{"name": "a", "price": 5},
		map[string]any{"name": "b", "price": 15},
	}}
	got := mustAll(t, q, input, 10)
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("got %v", got)
	}
	if _, err := q.All(context.Background(), input); err == nil {
		t.Fatal("expected an error for a missing variable value")
	}
	if _, err := Compile(`$nope`); err == nil || !strings.Contains(err.Error(), "$nope is not defined") {
		t.Fatalf("undeclared variable: %v", err)
	}
}

func TestGoInputTypes(t *testing.T) {
	type item struct {
		ID   int64    `json:"id"`
		Tags []string `json:"tags"`
		Skip string   `json:"-"`
	}
	input := map[string]any{
		"n8":   int8(-3),
		"u64":  uint64(1 << 40),
		"f32":  float32(1.5),
		"num":  json.Number("12345678901234567"),
		"item": item{ID: 9007199254740993, Tags: []string{"x"}},
		"ptr":  (*item)(nil),
		"strs": []string{"a", "b"},
	}
	q := MustCompile(`[.n8, .u64, .f32, .num, .item.id, .item.tags[0], .ptr, (.strs | join(","))]`)
	got := mustAll(t, q, input)
	want := []any{-3, 1 << 40, 1.5, 12345678901234567, 9007199254740993, "x", nil, "a,b"}
	if !equal(got[0], want) {
		t.Fatalf("got %s, want %s", toJSON(got[0]), toJSON(want))
	}
	// big integers stay exact
	if got[0].([]any)[4] != 9007199254740993 {
		t.Fatalf("int64 lost precision: %#v", got[0].([]any)[4])
	}
}

func TestResultTypes(t *testing.T) {
	got := mustAll(t, MustCompile(`1 + 2, 7 / 2, 6 / 3, 1.5 * 2, ("a" | length), ([1,2] | add), (2.7 | floor)`), nil)
	want := []any{3, 3.5, 2, 3.0, 1, 3, 2}
	for i := range want {
		if fmt.Sprintf("%T %v", got[i], got[i]) != fmt.Sprintf("%T %v", want[i], want[i]) {
			t.Errorf("result %d: got %T %v, want %T %v", i, got[i], got[i], want[i], want[i])
		}
	}
}

func TestInputNotMutated(t *testing.T) {
	input := map[string]any{"a": []any{1, 2, 3}, "b": map[string]any{"c": 1}}
	before := toJSON(input)
	for _, src := range []string{
		`.a[0] = 9`, `.a[] |= . * 2`, `.b.c += 1`, `del(.a[1])`, `.a += [4]`,
		`reduce range(3) as $i (.; .a[$i] = $i)`, `to_entries | from_entries`,
		`.b |= with_entries(.value = 0)`, `.a |= sort`, `reduce .a[] as $x (.; . + {($x|tostring): $x})`,
	} {
		mustAll(t, MustCompile(src), input)
		if after := toJSON(input); after != before {
			t.Fatalf("%s mutated its input: %s -> %s", src, before, after)
		}
	}
}

func TestReduceAccumulatorNotShared(t *testing.T) {
	// The in-place reduce optimisation must not leak: each init value
	// starts a fresh accumulator, and emitted results are never modified.
	got := mustAll(t, MustCompile(`[reduce (1,2) as $x ({}, {"z":0}; .[$x|tostring] = $x)]`), nil)
	want := `[{"1":1,"2":2},{"1":1,"2":2,"z":0}]`
	if toJSON(got[0]) != want {
		t.Fatalf("got %s, want %s", toJSON(got[0]), want)
	}
	got = mustAll(t, MustCompile(`[foreach (1,2,3) as $x ([]; . + [$x])]`), nil)
	if toJSON(got[0]) != `[[1],[1,2],[1,2,3]]` {
		t.Fatalf("got %s", toJSON(got[0]))
	}
	got = mustAll(t, MustCompile(`reduce range(3) as $i ([]; . + [.])`), nil)
	if toJSON(got[0]) != `[[],[[]],[[],[[]]]]` {
		t.Fatalf("got %s", toJSON(got[0]))
	}
	got = mustAll(t, MustCompile(`reduce range(3) as $i ({}; .a = .)`), nil)
	if toJSON(got[0]) != `{"a":{"a":{"a":{}}}}` {
		t.Fatalf("got %s", toJSON(got[0]))
	}
}

func TestLargeReduceIsLinear(t *testing.T) {
	// Quadratic copying would take tens of seconds here.
	n := 200000
	arr := make([]any, n)
	for i := range arr {
		arr[i] = map[string]any{"id": i, "v": i % 7}
	}
	start := time.Now()
	for _, src := range []string{
		`reduce .[] as $x ({}; .[$x.id | tostring] = $x) | length`,
		`INDEX(.id) | length`,
		`reduce .[] as $x ([]; . + [$x.v]) | length`,
		`map(.v) | length`,
		`.[] |= .id | length`,
		`INDEX(.id) | to_entries | from_entries | length`,
		`reduce .[] as $x ({}; .[$x.v | tostring] += 1) | length`,
	} {
		got := mustAll(t, MustCompile(src), arr)
		if len(got) != 1 {
			t.Fatalf("%s: %v", src, got)
		}
	}
	if d := time.Since(start); d > 20*time.Second {
		t.Fatalf("too slow: %v", d)
	}
}

func TestCustomFunction(t *testing.T) {
	q := MustCompile(`[.[] | double], upper("x"), add3(1; 2; 3)`,
		WithFunction("double", 0, 0, func(in any, _ []any) (any, error) {
			n, ok := in.(int)
			if !ok {
				return nil, fmt.Errorf("double: not an int")
			}
			return int64(n) * 2, nil // any Go number is fine; it is normalized
		}),
		WithFunction("upper", 1, 1, func(_ any, args []any) (any, error) {
			return strings.ToUpper(args[0].(string)), nil
		}),
		WithFunction("add3", 3, 3, func(_ any, args []any) (any, error) {
			return args[0].(int) + args[1].(int) + args[2].(int), nil
		}),
	)
	got := mustAll(t, q, []any{1, 2})
	if toJSON(got) != `[[2,4],"X",6]` {
		t.Fatalf("got %s", toJSON(got))
	}
	_, err := MustCompile(`"x" | double`, WithFunction("double", 0, 0, func(any, []any) (any, error) {
		return nil, errors.New("boom")
	})).All(context.Background(), nil)
	if err == nil || err.Error() != "boom" {
		t.Fatalf("got %v", err)
	}
	// errors from custom functions are catchable
	got = mustAll(t, MustCompile(`try boom catch .`, WithFunction("boom", 0, 0, func(any, []any) (any, error) {
		return nil, errors.New("boom")
	})), nil)
	if got[0] != "boom" {
		t.Fatalf("got %v", got)
	}
}

func TestErrors(t *testing.T) {
	_, err := MustCompile(`error({"code": 42})`).All(context.Background(), nil)
	var ve *ValueError
	if !errors.As(err, &ve) || toJSON(ve.Value) != `{"code":42}` {
		t.Fatalf("got %#v", err)
	}
	_, err = MustCompile(`"bye" | halt_error(3)`).All(context.Background(), nil)
	var he *HaltError
	if !errors.As(err, &he) || he.Code != 3 || he.Value != "bye" {
		t.Fatalf("got %#v", err)
	}
	_, err = Compile(`.a | foo(1)`)
	var ce *CompileError
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "foo/1 is not defined") {
		t.Fatalf("got %#v", err)
	}
	_, err = Compile(`.a[`)
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("got %#v", err)
	}
	// results before an error are delivered, then the error, then nothing
	var got []any
	var gotErr error
	for v, err := range MustCompile(`1, 2, error("x"), 3`).Run(context.Background(), nil) {
		if err != nil {
			gotErr = err
			continue
		}
		got = append(got, v)
	}
	if toJSON(got) != `[1,2]` || gotErr == nil || gotErr.Error() != "x" {
		t.Fatalf("got %v, %v", got, gotErr)
	}
}

func TestContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	for _, src := range []string{`def f: f; f`, `repeat(1) | empty`, `last(range(1e18))`, `[limit(1e9; repeat(1))] | length`} {
		start := time.Now()
		_, err := MustCompile(src).All(ctx, nil)
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, errDepth) {
			t.Fatalf("%s: got %v", src, err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("%s: cancellation took %v", src, time.Since(start))
		}
	}
}

// A fan-out that emits straight into a collector (no nested run calls)
// must still notice cancellation quickly; this one grows quadratically.
func TestContextCancelFanOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := MustCompile(`[.[]|[.,1]|until(.[0]<1;[.[0]-1,.[]*.[]])|.[1]]`).All(ctx, []any{1, 2, 7, 4, 5})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("cancellation took %v", d)
	}
}

// Repeating a string is capped, so recurse(2*.) on a string stops with an
// error instead of doubling until memory runs out. (Found by fuzzing.)
func TestStringRepeatLimit(t *testing.T) {
	_, err := MustCompile(`[recurse(2 * .)] | length`).All(context.Background(), "ab")
	if err == nil || err.Error() != "Repeat string result too long" {
		t.Fatalf("got %v", err)
	}
}

func TestDeepRecursionIsAnError(t *testing.T) {
	_, err := MustCompile(`def f: 1 + f; f`).All(context.Background(), nil)
	if !errors.Is(err, errDepth) {
		t.Fatalf("got %v", err)
	}
}

func TestFirstStopsEarly(t *testing.T) {
	v, err := MustCompile(`range(1e18)`).First(context.Background(), nil)
	if err != nil || v != 0 {
		t.Fatalf("got %v %v", v, err)
	}
	if _, err := MustCompile(`empty`).First(context.Background(), nil); !errors.Is(err, ErrNoResult) {
		t.Fatalf("got %v", err)
	}
}

func TestBreakOutOfRange(t *testing.T) {
	n := 0
	for v, err := range MustCompile(`range(1e18)`).Run(context.Background(), nil) {
		if err != nil {
			t.Fatal(err)
		}
		n++
		if v == 9 {
			break
		}
	}
	if n != 10 {
		t.Fatalf("n = %d", n)
	}
}

func TestLoopBodyPanicPropagates(t *testing.T) {
	defer func() {
		if r := recover(); r != "mine" {
			t.Fatalf("recovered %v", r)
		}
	}()
	for range MustCompile(`1`).Run(context.Background(), nil) {
		panic("mine")
	}
}

func TestConcurrentUse(t *testing.T) {
	q := MustCompile(`reduce .[] as $x ({}; .[$x.k] += [$x.v]) | map_values(add)`)
	input := []any{
		map[string]any{"k": "a", "v": 1}, map[string]any{"k": "b", "v": 2}, map[string]any{"k": "a", "v": 3},
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				out, err := q.All(context.Background(), input)
				if err != nil || toJSON(out) != `[{"a":4,"b":2}]` {
					t.Errorf("got %s %v", toJSON(out), err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestEnvIsOptIn(t *testing.T) {
	got := mustAll(t, MustCompile(`$ENV, env`), nil)
	if toJSON(got) != `[{},{}]` {
		t.Fatalf("got %s", toJSON(got))
	}
	got = mustAll(t, MustCompile(`$ENV.HOME, env.X`, WithEnviron([]string{"HOME=/h", "X=1=2"})), nil)
	if toJSON(got) != `["/h","1=2"]` {
		t.Fatalf("got %s", toJSON(got))
	}
}

type sliceInputs struct{ vals []any }

func (s *sliceInputs) Next() (any, error) {
	if len(s.vals) == 0 {
		return nil, ioEOF
	}
	v := s.vals[0]
	s.vals = s.vals[1:]
	return v, nil
}

func TestInputs(t *testing.T) {
	q := MustCompile(`[., input, [inputs]]`)
	var got []any
	for v, err := range q.RunWithInputs(context.Background(), 1, &sliceInputs{vals: []any{2, 3, 4}}) {
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, v)
	}
	if toJSON(got) != `[[1,2,[3,4]]]` {
		t.Fatalf("got %s", toJSON(got))
	}
	if _, err := MustCompile(`input`).All(context.Background(), nil); err == nil || err.Error() != "No more inputs" {
		t.Fatalf("got %v", err)
	}
}

func TestMarshal(t *testing.T) {
	v := map[string]any{"b": []any{1, 2.5, "é\n", nil, true}, "a": map[string]any{}}
	if got := string(Marshal(v)); got != `{"a":{},"b":[1,2.5,"é\n",null,true]}` {
		t.Fatalf("got %s", got)
	}
	got := string(MarshalWith(v, EncodeOptions{Indent: 2, ASCII: true}))
	want := "{\n  \"a\": {},\n  \"b\": [\n    1,\n    2.5,\n    \"\\u00e9\\n\",\n    null,\n    true\n  ]\n}"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	tenth, fifth := 0.1, 0.2 // variables: Go folds constant 0.1+0.2 exactly
	for f, s := range map[float64]string{1e17: "1e+17", 1e16: "10000000000000000", tenth + fifth: "0.30000000000000004", 1e-5: "0.00001", 1e-7: "1e-07"} {
		if got := string(Marshal(f)); got != s {
			t.Errorf("Marshal(%v) = %s, want %s", f, got, s)
		}
	}
}

func TestDecoder(t *testing.T) {
	dec := NewDecoder(strings.NewReader("1 [2, {\"a\": 3.5}] \"x\"\n12345678901234567890 NaN"))
	var got []any
	for {
		v, err := dec.Decode()
		if err != nil {
			break
		}
		got = append(got, v)
	}
	if len(got) != 5 || got[0] != 1 || got[3] != 1.2345678901234567e19 {
		t.Fatalf("got %#v", got)
	}
	for _, bad := range []string{"[1,]", "{\"a\" 1}", "01", "1.", "tru", "\"abc", "[1 2]"} {
		if _, err := parseJSON([]byte(bad)); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
}

func TestEval(t *testing.T) {
	var data any
	if err := json.Unmarshal([]byte(`{"users":[{"name":"ann","age":31},{"name":"bob","age":25}]}`), &data); err != nil {
		t.Fatal(err)
	}
	got, err := Eval(context.Background(), `[.users[] | select(.age > 30) | .name]`, data)
	if err != nil || toJSON(got) != `[["ann"]]` {
		t.Fatalf("got %s %v", toJSON(got), err)
	}
}

var ioEOF = io.EOF

func TestMarshalInvalidUTF8(t *testing.T) {
	// A Go string from the caller can hold invalid UTF-8; it is printed as
	// U+FFFD, escaped with -a.
	if got := string(Marshal("a\xffb")); got != "\"a\ufffdb\"" {
		t.Errorf("got %q", got)
	}
	if got := string(MarshalWith("a\xffb\u00e9", EncodeOptions{ASCII: true})); got != `"a\ufffdb\u00e9"` {
		t.Errorf("ASCII: got %q", got)
	}
}

func TestEncoder(t *testing.T) {
	var b strings.Builder
	enc := NewEncoder(&b, EncodeOptions{Indent: 1})
	big := benchRecords(2000) // larger than the flush threshold
	for _, v := range []any{map[string]any{"a": 1}, big} {
		if err := enc.Encode(v); err != nil {
			t.Fatal(err)
		}
		b.WriteByte('\n')
	}
	want := string(MarshalWith(map[string]any{"a": 1}, EncodeOptions{Indent: 1})) + "\n" +
		string(MarshalWith(big, EncodeOptions{Indent: 1})) + "\n"
	if b.String() != want {
		t.Fatal("streamed output differs from MarshalWith")
	}
}

func TestColors(t *testing.T) {
	c, ok := ParseColors("0;31:0;32")
	if !ok || c.Null != "0;31" || c.False != "0;32" || c.True != DefaultColors.True {
		t.Fatalf("ParseColors: %+v %v", c, ok)
	}
	if _, ok := ParseColors("bad;x"); ok {
		t.Fatal("expected an invalid spec")
	}
	got := string(MarshalWith(map[string]any{"a": []any{nil, 1}}, EncodeOptions{Colors: &DefaultColors}))
	want := "\x1b[1;39m{\x1b[0m\x1b[34;1m\"a\"\x1b[0m\x1b[1;39m:\x1b[0m\x1b[1;39m[\x1b[0m\x1b[1;30mnull\x1b[0m\x1b[1;39m,\x1b[0m\x1b[0;39m1\x1b[0m\x1b[1;39m\x1b[1;39m]\x1b[0m\x1b[1;39m\x1b[1;39m}\x1b[0m"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}
