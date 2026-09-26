// Package bench compares jqgo with gojq on the same data and queries.
// It is a separate module so jqgo itself does not depend on gojq.
//
//	cd bench && go test -run '^$' -bench . -count 6 | tee out.txt
//	benchstat -col /impl out.txt
package bench

import (
	"context"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/dcyber-lab/jqgo"
	"github.com/itchyny/gojq"
)

// records mirrors benchRecords in the jqgo package.
func records(n int) []any {
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

// queries mirrors benchQueries in the jqgo package.
var queries = []struct{ name, src string }{
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

func runJQGo(q *jqgo.Query, data any) ([]any, error) {
	return q.All(context.Background(), data)
}

func runGojq(c *gojq.Code, data any) ([]any, error) {
	var out []any
	it := c.Run(data)
	for {
		v, ok := it.Next()
		if !ok {
			return out, nil
		}
		if err, ok := v.(error); ok {
			return out, err
		}
		out = append(out, v)
	}
}

func compileGojq(tb testing.TB, src string) *gojq.Code {
	p, err := gojq.Parse(src)
	if err != nil {
		tb.Fatal(err)
	}
	c, err := gojq.Compile(p)
	if err != nil {
		tb.Fatal(err)
	}
	return c
}

// TestSameResults makes sure the comparison is apples to apples.
func TestSameResults(t *testing.T) {
	data := records(1000)
	for _, q := range queries {
		a, err := runJQGo(jqgo.MustCompile(q.src), data)
		if err != nil {
			t.Fatalf("%s: jqgo: %v", q.name, err)
		}
		b, err := runGojq(compileGojq(t, q.src), data)
		if err != nil {
			t.Fatalf("%s: gojq: %v", q.name, err)
		}
		if string(jqgo.Marshal(a)) != string(jqgo.Marshal(b)) {
			t.Errorf("%s: jqgo %s, gojq %s", q.name, jqgo.Marshal(a), jqgo.Marshal(b))
		}
	}
}

func BenchmarkQuery(b *testing.B) {
	data := records(10000)
	for _, q := range queries {
		jq := jqgo.MustCompile(q.src)
		gq := compileGojq(b, q.src)
		b.Run(q.name+"/impl=jqgo", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := runJQGo(jq, data); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(q.name+"/impl=gojq", func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := runGojq(gq, data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkSmallInput(b *testing.B) {
	doc := records(1)[0]
	src := `{name, city: .addr.city, adult: (.age >= 18), tags: (.tags | join(","))}`
	jq := jqgo.MustCompile(src)
	gq := compileGojq(b, src)
	b.Run("impl=jqgo", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := jq.First(context.Background(), doc); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("impl=gojq", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if v, _ := gq.Run(doc).Next(); v == nil {
				b.Fatal("no result")
			}
		}
	})
}

func BenchmarkCompile(b *testing.B) {
	src := `def f($x): .[] | select(.age > $x); [f(30) | {name, city: .addr.city}] | group_by(.city) | map(length)`
	b.Run("impl=jqgo", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			jqgo.MustCompile(src)
		}
	})
	b.Run("impl=gojq", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			compileGojq(b, src)
		}
	})
}
