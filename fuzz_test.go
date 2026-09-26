package jqgo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// FuzzQuery feeds arbitrary programs and inputs through Compile and Run.
// Errors are fine; panics (which Run would report as "internal error")
// and hangs are not.
func FuzzQuery(f *testing.F) {
	addSuiteSeeds(f)
	f.Fuzz(fuzzQuery)
}

// addSuiteSeeds seeds a fuzz target with every program/input pair of the
// test suites.
func addSuiteSeeds(f *testing.F) {
	for _, file := range []string{"jq/jq.test", "jq/man.test", "jq/onig.test", "extra.test"} {
		data, err := os.ReadFile(filepath.Join("testdata", file))
		if err != nil {
			f.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		for i := 0; i+1 < len(lines); i++ {
			if lines[i] == "" || strings.HasPrefix(lines[i], "#") || strings.HasPrefix(lines[i], "%%") {
				continue
			}
			f.Add(lines[i], lines[i+1])
			for i < len(lines) && lines[i] != "" {
				i++
			}
		}
	}
}

func fuzzQuery(t *testing.T, program, input string) {
	q, err := Compile(program, WithDebugWriter(nil))
	if err != nil {
		return
	}
	in, err := parseJSON([]byte(input))
	if err != nil {
		in = input
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	n := 0
	for v, err := range q.Run(ctx, in) {
		if err != nil {
			if strings.Contains(err.Error(), "internal error") {
				t.Fatalf("program %q input %q: %v", program, input, err)
			}
			break
		}
		_ = Marshal(v)
		if n++; n > 1000 {
			break
		}
	}
}
