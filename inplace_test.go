package jqgo

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// In-place updates (ownSet, reduceStep) are an optimisation that must be
// invisible. These tests run each program twice, with and without them,
// and require identical results, an untouched input, and results that do
// not change after they were yielded.

func deepCopy(v any) any {
	switch v := v.(type) {
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = deepCopy(x)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			out[k] = deepCopy(x)
		}
		return out
	}
	return v
}

type runRecord struct {
	outputs []string
	err     string
	lines   string // of LineErrors
}

func (r *runRecord) addErr(err error) {
	r.err += err.Error() + ";"
	var le *LineError
	if errors.As(err, &le) {
		r.lines += fmt.Sprint(le.Line, ";")
	}
}

// record runs q and snapshots each result as it is yielded; mutated reports
// a result that changed afterwards.
func record(q *Query, input any, opts evalOptions) (rec runRecord, mutated string) {
	// Short, so fuzzed programs that grow exponentially stop before they
	// exhaust memory.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	var kept []any
	for v, err := range q.exec(ctx, input, nil, opts, nil) {
		if err != nil {
			rec.err = err.Error()
			break
		}
		kept = append(kept, v)
		rec.outputs = append(rec.outputs, toJSON(v))
		if len(kept) > 200 {
			break
		}
	}
	for i, v := range kept {
		if now := toJSON(v); now != rec.outputs[i] {
			return rec, "result " + rec.outputs[i] + " later became " + now
		}
	}
	return rec, ""
}

// inconclusive reports runs cut short by a limit (time, recursion depth)
// rather than by the program: where they stop depends on how much work
// each strategy does per step, so their results cannot be compared.
func inconclusive(a, b runRecord) bool {
	for _, r := range []runRecord{a, b} {
		if strings.Contains(r.err, "deadline exceeded") || strings.Contains(r.err, "maximum recursion depth") {
			return true
		}
	}
	return false
}

func checkInPlace(t *testing.T, program string, input any) {
	t.Helper()
	q, err := Compile(program, WithDebugWriter(nil))
	if err != nil {
		return
	}
	checkQueryInPlace(t, q, program, input)
}

func checkQueryInPlace(t *testing.T, q *Query, program string, input any) {
	t.Helper()
	before := toJSON(input)
	fast, mutated := record(q, input, evalOptions{})
	if mutated != "" {
		t.Fatalf("%s on %s: %s", program, before, mutated)
	}
	if after := toJSON(input); after != before {
		t.Fatalf("%s mutated its input: %s -> %s", program, before, after)
	}
	slow, _ := record(q, deepCopy(input), evalOptions{copyOnly: true})
	if inconclusive(fast, slow) {
		return
	}
	if strings.Join(fast.outputs, "\n") != strings.Join(slow.outputs, "\n") || fast.err != slow.err {
		t.Fatalf("%s on %s:\n in place: %v %q\n copying:  %v %q", program, before, fast.outputs, fast.err, slow.outputs, slow.err)
	}
}

func inPlacePrograms() []string {
	sources := []string{`.[]`, `range(4)`, `(.[], "k")`, `(.[] | tostring)`}
	inits := []string{`{}`, `[]`, `.`, `null`, `{"z":[1]}`, `[.]`, `(. as $o | $o)`}
	updates := []string{
		`.[$x|tostring] = $x`,
		`.[$x|tostring] += [$x]`,
		`.[$x|tostring] |= [., .]`,
		`.a[$x|tostring] = $x`,
		`.[$x|tostring] = {} | .[$x|tostring].y = $x`,
		`. + [$x]`,
		`. + [.]`,
		`. + {($x|tostring): $x}`,
		`. + $x`,
		`(.a, .b) |= [.]`,
		`.a = .`,
		`.a = [.]`,
		`.a = $x | .b = .a`,
		`.[0] = $x`,
		`.[1:] = [$x]`,
		`.[$x] = .`,
		`.a |= . + 1`,
		`.a //= $x`,
		`.[] |= $x`,
		`.[] += 1`,
		`.x = (1, 2)`,
		`.n += (1, 10)`,
		`.n += (if $x == 1 or $x == 0 or $x == "k" then 1 else 1, 10 end)`,
		`.[$x|tostring] += (1, 10)`,
		`.[$x|tostring] //= (1, 2)`,
		`.x |= empty`,
		`.q[$x|tostring] |= (.n += 1)`,
	}
	wrappers := []string{
		`reduce SRC as $x (INIT; UPD)`,
		`[foreach SRC as $x (INIT; UPD)]`,
		`reduce SRC as $x (INIT; UPD) | [., (.q = 1)]`,
		`[reduce SRC as $x (INIT, INIT; UPD)]`,
		`. as $in | reduce SRC as $x (INIT; UPD) | [., $in]`,
		`reduce SRC as $x (INIT; UPD) | . as $r | reduce SRC as $x ($r; UPD) | [., $r]`,
		`[limit(3; reduce SRC as $x (INIT; UPD), 1)]`,
	}
	var out []string
	for _, w := range wrappers {
		for _, s := range sources {
			for _, i := range inits {
				for _, u := range updates {
					p := strings.NewReplacer("SRC", s, "INIT", i, "UPD", u).Replace(w)
					out = append(out, p)
				}
			}
		}
	}
	for _, u := range updates {
		out = append(out, `first(.[]) as $x | `+u, `[.[] as $x | `+u+`]`)
	}
	// Multi-path |= where f copies a container the update already owns into
	// two places and a later path writes inside one of them. This is what
	// ownSet.exposeAt guards against.
	f := `(if type == "object" then {k: ., o: .} elif type == "array" then [., .] else 5 end)`
	for _, paths := range []string{
		`.[0].a, .[0], .[0].k.y`,
		`.a[0], .a, .a[0][0]`,
		`.b.c, .b, .b.k.c`,
		`.[0][0], .[0], .[0][0][0]`,
		`.["0"], .["1"].y, .["1"], .["1"].k.y`,
		`.[0].a, .[0][1:], .[0]`,
	} {
		out = append(out, `(`+paths+`) |= `+f, `reduce range(2) as $i (.; (`+paths+`) |= `+f+`)`)
	}
	return out
}

var inPlaceInputs = []string{`[1,2,3]`, `{"a":[1],"b":{"c":2}}`, `[[1],[2]]`, `[]`, `{"0":1,"1":{"y":0}}`, `[{"a":1,"k":{"y":0}}]`, `{"a":[[1]],"b":{"c":1,"k":{"c":0}}}`}

func TestInPlaceEquivalence(t *testing.T) {
	progs := inPlacePrograms()
	if testing.Short() {
		progs = progs[:len(progs)/10]
	}
	for _, p := range progs {
		q, err := Compile(p, WithDebugWriter(nil))
		if err != nil {
			continue
		}
		for _, in := range inPlaceInputs {
			v, err := parseJSON([]byte(in))
			if err != nil {
				t.Fatal(err)
			}
			checkQueryInPlace(t, q, p, v)
		}
	}
}

func FuzzInPlace(f *testing.F) {
	progs := inPlacePrograms()
	for i, p := range progs {
		if i%7 == 0 { // a spread of shapes; the fuzzer mutates from there
			f.Add(p, inPlaceInputs[i%len(inPlaceInputs)])
		}
	}
	f.Fuzz(func(t *testing.T, program, input string) {
		v, err := parseJSON([]byte(input))
		if err != nil {
			return
		}
		checkInPlace(t, program, v)
	})
}

// FuzzFastPath checks that eval1 (the direct evaluation of simple
// expressions) gives the same results and errors as the general evaluator.
func FuzzFastPath(f *testing.F) {
	addSuiteSeeds(f)
	f.Fuzz(func(t *testing.T, program, input string) {
		q, err := Compile(program, WithDebugWriter(nil))
		if err != nil {
			return
		}
		in, err := parseJSON([]byte(input))
		if err != nil {
			return
		}
		fast, _ := record(q, in, evalOptions{})
		slow, _ := record(q, in, evalOptions{noFastPath: true})
		if inconclusive(fast, slow) {
			return
		}
		if strings.Join(fast.outputs, "\n") != strings.Join(slow.outputs, "\n") || fast.err != slow.err {
			t.Fatalf("%s on %s:\n fast: %v %q\n slow: %v %q", program, input, fast.outputs, fast.err, slow.outputs, slow.err)
		}
	})
}

// TestFastPathSuite runs every case of the test suites both ways.
func TestFastPathSuite(t *testing.T) {
	for _, file := range []string{"jq/jq.test", "jq/man.test", "jq/onig.test", "extra.test"} {
		for _, c := range readJQTests(t, filepath.Join("testdata", file)) {
			if c.fail {
				continue
			}
			q, err := Compile(c.program, WithDebugWriter(nil), WithEnviron([]string{"PAGER=less"}))
			if err != nil {
				continue
			}
			in, err := parseJSON([]byte(c.input))
			if err != nil {
				continue
			}
			fast, _ := record(q, in, evalOptions{})
			slow, _ := record(q, in, evalOptions{noFastPath: true})
			if strings.Join(fast.outputs, "\n") != strings.Join(slow.outputs, "\n") || fast.err != slow.err {
				t.Errorf("%s:%d %s:\n fast: %v %q\n slow: %v %q", c.file, c.line, c.program, fast.outputs, fast.err, slow.outputs, slow.err)
			}
		}
	}
}
