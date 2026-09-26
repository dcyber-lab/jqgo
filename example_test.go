package jqgo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/dcyber-lab/jqgo"
)

func Example() {
	var data any
	_ = json.Unmarshal([]byte(`{"items":[{"name":"a","price":5},{"name":"b","price":15}]}`), &data)

	q := jqgo.MustCompile(`.items[] | select(.price > $min) | .name`, jqgo.WithVariables("min"))
	for v, err := range q.Run(context.Background(), data, 10) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(v)
	}
	// Output: b
}

func ExampleQuery_First() {
	q := jqgo.MustCompile(`.user.name // "anonymous"`)
	v, err := q.First(context.Background(), map[string]any{"user": map[string]any{}})
	fmt.Println(v, err)
	// Output: anonymous <nil>
}

func ExampleQuery_All() {
	type order struct {
		ID    int     `json:"id"`
		Total float64 `json:"total"`
	}
	orders := []order{{1, 20}, {2, 35.5}, {3, 12}}

	// Structs and typed slices are accepted as input.
	q := jqgo.MustCompile(`(map(.total) | add), (sort_by(-.total) | .[0].id)`)
	out, err := q.All(context.Background(), orders)
	fmt.Println(out, err)
	// Output: [67.5 2] <nil>
}

func ExampleWithFunction() {
	q := jqgo.MustCompile(`.tags | map(upper) | join(",")`,
		jqgo.WithFunction("upper", 0, 0, func(in any, _ []any) (any, error) {
			s, ok := in.(string)
			if !ok {
				return nil, fmt.Errorf("upper: %v is not a string", in)
			}
			return strings.ToUpper(s), nil
		}))
	v, _ := q.First(context.Background(), map[string]any{"tags": []any{"go", "jq"}})
	fmt.Println(v)
	// Output: GO,JQ
}

func ExampleMarshal() {
	v, _ := jqgo.MustCompile(`{b: 1, a: [1.5, "x"]}`).First(context.Background(), nil)
	fmt.Println(string(jqgo.Marshal(v)))
	fmt.Println(string(jqgo.MarshalWith(v, jqgo.EncodeOptions{Indent: 2})))
	// Output:
	// {"a":[1.5,"x"],"b":1}
	// {
	//   "a": [
	//     1.5,
	//     "x"
	//   ],
	//   "b": 1
	// }
}

func ExampleValueError() {
	_, err := jqgo.MustCompile(`error({code: 404})`).All(context.Background(), nil)
	if ve, ok := err.(*jqgo.ValueError); ok {
		fmt.Println(ve.Value.(map[string]any)["code"])
	}
	// Output: 404
}

func ExampleQuery_RunReader() {
	// Any io.Reader: a file, an HTTP body... Only the elements of .items
	// are built, one at a time.
	r := strings.NewReader(`{"total": 3, "items": [{"id": 1, "ok": true}, {"id": 2}, {"id": 3, "ok": true}]}`)
	q := jqgo.MustCompile(`.items[] | select(.ok) | .id`)
	fmt.Println("streamable:", q.Streamable())
	for v, err := range q.RunReader(context.Background(), r) {
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(v)
	}
	// Output:
	// streamable: true
	// 1
	// 3
}
