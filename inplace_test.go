package jqgo

import (
	"context"
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
}

// record runs q and snapshots each result as it is yielded; mutated reports
// a result that changed afterwards.
func record(q *Query, input any, copyOnly bool) (rec runRecord, mutated string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var kept []any
	for v, err := range q.exec(ctx, input, nil, copyOnly, nil) {
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
	fast, mutated := record(q, input, false)
	if mutated != "" {
		t.Fatalf("%s on %s: %s", program, before, mutated)
	}
	if after := toJSON(input); after != before {
		t.Fatalf("%s mutated its input: %s -> %s", program, before, after)
	}
	slow, _ := record(q, deepCopy(input), true)
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
