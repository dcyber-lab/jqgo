package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, stdin string, args ...string) (string, string, int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), errOut.String(), code
}

func TestCLI(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	f1 := write("a.json", `{"a":1} {"a":2}`)
	f2 := write("b.json", `[3]`)
	prog := write("prog.jq", `.a + $x`)
	raw := write("raw.txt", "line1\nline2\n")

	tests := []struct {
		name  string
		stdin string
		args  []string
		want  string
		code  int
	}{
		{"pretty", `{"b":[1,{"c":2}],"a":"x"}`, []string{"."}, "{\n  \"a\": \"x\",\n  \"b\": [\n    1,\n    {\n      \"c\": 2\n    }\n  ]\n}\n", 0},
		{"compact", `{"a":[1,2]}`, []string{"-c", "."}, "{\"a\":[1,2]}\n", 0},
		{"tab", `{"a":1}`, []string{"--tab", "."}, "{\n\t\"a\": 1\n}\n", 0},
		{"indent", `[1]`, []string{"--indent", "1", "."}, "[\n 1\n]\n", 0},
		{"stream of inputs", "1 2\n3", []string{".*10"}, "10\n20\n30\n", 0},
		{"raw output", `["a","b"]`, []string{"-r", ".[]"}, "a\nb\n", 0},
		{"join output", `["a",1]`, []string{"-j", ".[]"}, "a1", 0},
		{"ascii", `"é"`, []string{"-a", "."}, "\"\\u00e9\"\n", 0},
		{"combined short flags", ``, []string{"-nrc", `"x", [1]`}, "x\n[1]\n", 0},
		{"null input", ``, []string{"-n", "1+1"}, "2\n", 0},
		{"slurp", "1 2 3", []string{"-c", "-s", "."}, "[1,2,3]\n", 0},
		{"raw input", "a\nb\n", []string{"-R", "."}, "\"a\"\n\"b\"\n", 0},
		{"raw input slurp", "a\nb\n", []string{"-Rs", "."}, "\"a\\nb\\n\"\n", 0},
		{"arg", ``, []string{"-n", "--arg", "v", "x", "$v"}, "\"x\"\n", 0},
		{"argjson", ``, []string{"-nc", "--argjson", "v", `{"a":[1]}`, "$v.a"}, "[1]\n", 0},
		{"named args", ``, []string{"-nc", "--arg", "a", "1", "--argjson", "b", "2", "$ARGS"}, "{\"named\":{\"a\":\"1\",\"b\":2},\"positional\":[]}\n", 0},
		{"positional args", ``, []string{"-nc", "$ARGS.positional", "--args", "x", "y"}, "[\"x\",\"y\"]\n", 0},
		{"positional jsonargs", ``, []string{"-nc", "$ARGS.positional", "--jsonargs", "1", `{"a":2}`}, "[1,{\"a\":2}]\n", 0},
		{"files", ``, []string{"-c", ".", f1, f2}, "{\"a\":1}\n{\"a\":2}\n[3]\n", 0},
		{"filter from file", `{"a":1}`, []string{"--argjson", "x", "1", "-f", prog}, "2\n", 0},
		{"slurpfile", ``, []string{"-nc", "--slurpfile", "s", f1, "$s"}, "[{\"a\":1},{\"a\":2}]\n", 0},
		{"rawfile", ``, []string{"-n", "--rawfile", "r", raw, "$r"}, "\"line1\\nline2\\n\"\n", 0},
		{"input and inputs", "1 2 3 4", []string{"-c", "[., input]"}, "[1,2]\n[3,4]\n", 0},
		{"inputs with -n", "1 2 3", []string{"-n", "[inputs] | add"}, "6\n", 0},
		{"exit status false", `false`, []string{"-e", "."}, "false\n", 1},
		{"exit status null", `{}`, []string{"-e", ".a"}, "null\n", 1},
		{"exit status truthy", `1`, []string{"-e", "."}, "1\n", 0},
		{"exit status no output", `1`, []string{"-e", "empty"}, "", 4},
		{"runtime error continues", "1 \"x\" 3", []string{".+1"}, "2\n4\n", 5},
		{"compile error", ``, []string{"-n", "foo"}, "", 3},
		{"syntax error", ``, []string{"-n", ".["}, "", 3},
		{"bad input", `{"a":`, []string{"."}, "", 2},
		{"halt", ``, []string{"-n", "1, halt, 2"}, "1\n", 0},
		{"halt_error", ``, []string{"-n", `"bye\n" | halt_error(7)`}, "", 7},
		{"env", ``, []string{"-n", `$ENV | type`}, "\"object\"\n", 0},
		{"seq output", ``, []string{"-nc", "--seq", `1, "a", [2]`}, "\x1e1\n\x1e\"a\"\n\x1e[2]\n", 0},
		{"seq omits RS for raw strings", ``, []string{"-nr", "--seq", `"s", 1`}, "s\n\x1e1\n", 0},
		{"seq input skips bad records", "\x1e1\n\x1e[3\n\x1e4\n", []string{"-c", "--seq", "."}, "\x1e1\n\x1e4\n", 0},
		{"raw output0", ``, []string{"-n", "--raw-output0", `"a", 1, [2]`}, "a\x001\x00[\n  2\n]\x00", 0},
		{"raw output0 rejects NUL", ``, []string{"-n", "--raw-output0", `"ok", "a\u0000b", "never"`}, "ok\x00", 5},
		{"help", ``, []string{"--help"}, usage, 0},
		{"unknown option", ``, []string{"--nope", "."}, "", 2},
		{"missing filter", ``, []string{}, "", 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, errOut, code := runCLI(t, tc.stdin, tc.args...)
			if out != tc.want || code != tc.code {
				t.Errorf("jqgo %q\n got %q (exit %d)\nwant %q (exit %d)\nstderr: %s", tc.args, out, code, tc.want, tc.code, errOut)
			}
		})
	}
}

func TestCLIErrorMessages(t *testing.T) {
	_, errOut, _ := runCLI(t, "\x1e[3\n\x1e1 2\n", "--seq", ".")
	if strings.Count(errOut, "jqgo: ignoring parse error") != 2 {
		t.Errorf("--seq warnings: %q", errOut)
	}
	_, errOut, _ = runCLI(t, `"x"`, ".+1")
	if !strings.Contains(errOut, `jqgo: error (at <stdin>:1): string ("x") and number (1) cannot be added`) {
		t.Errorf("stderr: %q", errOut)
	}
	_, errOut, _ = runCLI(t, ``, "-n", `"bye\n" | halt_error(7)`)
	if errOut != "bye\n" {
		t.Errorf("halt_error stderr: %q", errOut)
	}
	_, errOut, _ = runCLI(t, ``, "-n", `{"a":1} | halt_error`)
	if errOut != "{\"a\":1}\n" {
		t.Errorf("halt_error stderr: %q", errOut)
	}
	_, errOut, _ = runCLI(t, ``, "-n", "nope(1)")
	if !strings.Contains(errOut, "nope/1 is not defined") || !strings.Contains(errOut, "1 compile error") {
		t.Errorf("stderr: %q", errOut)
	}
}
