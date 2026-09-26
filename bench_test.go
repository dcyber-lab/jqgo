package jqgo

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"testing"
)

// Benchmarks over a synthetic but realistic dataset: an array of user
// records with nested objects, arrays, strings and numbers. Compare runs
// with benchstat:
//
//	go test -run '^$' -bench . -count 10 > old.txt
//	(change something)
//	go test -run '^$' -bench . -count 10 > new.txt
//	benchstat old.txt new.txt
//
// bench/ compares the same queries against gojq.

// benchRecords builds n deterministic records.
func benchRecords(n int) []any {
	r := rand.New(rand.NewPCG(1, 2))
	out := make([]any, n)
	for i := range out {
		tags := make([]any, 3)
		for j := range tags {
			tags[j] = fmt.Sprintf("t%d", r.IntN(20))
		}
		out[i] = map[string]any{
			"id":     i,
			"name":   fmt.Sprintf("user%d", i),
			"age":    18 + r.IntN(60),
			"score":  r.Float64() * 100,
			"active": r.IntN(2) == 0,
			"tags":   tags,
			"addr": map[string]any{
				"city": fmt.Sprintf("c%d", r.IntN(100)),
				"zip":  fmt.Sprintf("%05d", r.IntN(100000)),
			},
		}
	}
	return out
}

// BenchQueries is shared with bench/ so both implementations run the same
// programs.
var benchQueries = []struct{ name, src string }{
	{"field", `[.[] | .name] | length`},
	{"select", `[.[] | select(.age > 50 and .active) | .name] | length`},
	{"construct", `map({id, city: .addr.city, n: (.tags | length)}) | length`},
	{"group_by", `group_by(.addr.city) | map({city: .[0].addr.city, n: length}) | length`},
	{"reduce_count", `reduce .[] as $x ({}; .[$x.addr.city] += 1) | length`},
	{"index", `INDEX(.id) | length`},
	{"sort_by", `sort_by(.score) | .[0].id`},
	{"update", `map(.age += 1) | length`},
	{"update_all", `.[] |= (.age += 1) | length`},
	{"paths", `[paths] | length`},
	{"strings", `[.[] | "\(.name)-\(.age)" | ascii_upcase] | length`},
	{"regex", `[.[] | select(.name | test("7$"))] | length`},
	{"tojson", `map(tojson) | length`},
	{"entries", `map(with_entries(.value |= tostring)) | length`},
	{"unique", `[.[].tags[]] | unique | length`},
	{"stats", `(map(.score) | add / length), (map(.age) | max)`},
}

func BenchmarkQuery(b *testing.B) {
	data := benchRecords(10000)
	for _, bq := range benchQueries {
		q := MustCompile(bq.src)
		b.Run(bq.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := q.All(context.Background(), data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkSmallInput is the typical embedded use: one small document per
// call, many calls.
func BenchmarkSmallInput(b *testing.B) {
	doc := benchRecords(1)[0]
	q := MustCompile(`{name, city: .addr.city, adult: (.age >= 18), tags: (.tags | join(","))}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := q.First(context.Background(), doc); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompile(b *testing.B) {
	src := `def f($x): .[] | select(.age > $x); [f(30) | {name, city: .addr.city}] | group_by(.city) | map(length)`
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Compile(src); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDecode(b *testing.B) {
	data := Marshal(benchRecords(10000))
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		d := NewDecoder(bytes.NewReader(data))
		if _, err := d.Decode(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMarshal(b *testing.B) {
	data := benchRecords(10000)
	b.SetBytes(int64(len(Marshal(data))))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = MarshalWith(data, EncodeOptions{Indent: 2})
	}
}
