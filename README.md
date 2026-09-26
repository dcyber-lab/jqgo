# jqgo

An implementation of [jq](https://jqlang.github.io/jq/) in pure Go, built to
be embedded in Go programs. It also ships a jq-compatible CLI.

- **Plain Go values in and out.** Queries take anything `encoding/json` can
  marshal and return `nil`, `bool`, `int`, `float64`, `string`, `[]any` and
  `map[string]any`, the same shapes `json.Unmarshal` gives you. There is no
  wrapper type to convert through.
- **Exact integers.** Integers stay `int`, so 64-bit IDs survive a query
  unchanged (jq rounds them through a double).
- **Compile once, run concurrently.** A compiled `*Query` is immutable and
  safe to share between goroutines. Results come out as a Go 1.23 iterator,
  and evaluation honours `context.Context` cancellation.
- **Checked against jq's own tests.** jq 1.7.1's test suites (`jq.test`,
  `man.test`, `onig.test`) run in CI: 685 cases pass, and the 26 skipped ones
  are listed with the reason for each in
  [`testdata/jq/skip.txt`](testdata/jq/skip.txt).

```go
import "github.com/dcyber-lab/jqgo"

q, err := jqgo.Compile(`.items[] | select(.price > $min) | {name, price}`,
	jqgo.WithVariables("min"))
if err != nil {
	return err // syntax errors and undefined names are reported here
}

for v, err := range q.Run(ctx, data, 10) { // 10 is the value of $min
	if err != nil {
		return err
	}
	fmt.Println(v) // map[name:b price:15]
}
```

## Install

```sh
go get github.com/dcyber-lab/jqgo          # library
go install github.com/dcyber-lab/jqgo/cmd/jqgo@latest   # CLI
```

Requires Go 1.23 or newer.

## Library

| API | Purpose |
| --- | --- |
| `Compile(src, opts...)` / `MustCompile` | Parse and check a query. |
| `q.Run(ctx, input, vars...)` | `iter.Seq2[any, error]` over the results. Stopping the loop stops evaluation. |
| `q.All(ctx, input, vars...)` | Collect every result. |
| `q.First(ctx, input, vars...)` | First result only. Returns `ErrNoResult` if there is none. |
| `q.RunWithInputs(ctx, input, inputs, vars...)` | Supply the stream behind `input` / `inputs`. |
| `Eval(ctx, src, input)` | Compile and run in one call. |
| `Marshal(v)` / `MarshalWith(v, EncodeOptions)` | Encode a value the way jq prints it. |
| `NewEncoder(w, EncodeOptions)` | Same, streamed to an `io.Writer` without building the text in memory. |
| `NewDecoder(r)` | Read a stream of JSON values (ints stay ints). |

Options:

- `WithVariables("a", "b")` declares `$a` and `$b`. Their values are passed
  positionally to `Run`.
- `WithFunction(name, minArity, maxArity, fn)` exposes a Go function to
  queries. Its arguments are evaluated like jq's builtins (one call per
  combination of argument values), and its errors can be caught with
  `try`/`catch`.
- `WithEnviron(os.Environ())` makes `$ENV` and `env` see the environment.
  **By default they are empty**, so a query cannot read secrets from the
  process environment unless you pass it in.
- `WithDebugWriter(w)` sets where `debug` and `stderr` write (default
  `os.Stderr`).

Errors:

- `*ParseError`: syntax error, with the offset in the query.
- `*CompileError`: undefined function, variable or label.
- `*ValueError`: raised by `error(v)`. `Value` is exactly what `catch` would
  see.
- `*HaltError`: raised by `halt` and `halt_error`, with the exit code.
- anything else: a runtime error message, identical to jq's in most cases.

Things to know when embedding:

- Results can share memory with the input and with each other. Treat them as
  read-only. jqgo never mutates the input you pass in.
- Input is converted on demand, not up front. Canonical input (what
  `json.Unmarshal` gives) costs nothing extra. Other Go types (structs,
  `int64`, `json.Number`, `[]string`...) are converted when a query reads
  them. Parts of the input a query passes through untouched come back
  as you gave them, so a result can contain your own `int64` or struct.
  Call `Normalize` first if you want plain JSON types all the way down, or
  if you query the same non-canonical value many times.
- Numbers are `int` when integral and in range, `float64` otherwise. For
  example, `7 / 2` is `3.5` but `6 / 3` is `2`.
- Object keys are always emitted sorted. Go maps have no order, so
  `keys_unsorted` and `to_entries` follow sorted order too (jq keeps
  insertion order).
- Guard against untrusted queries with a context deadline. Evaluation checks
  the context regularly, and runaway recursion becomes an error instead of
  crashing the process. Memory use is not limited, so run truly hostile
  queries out of process.

## CLI

```sh
curl -s https://api.github.com/repos/jqlang/jq/commits | jqgo '.[0].commit.author'
jqgo -n --arg who world '"hello \($who)"'
jqgo -r '.[] | [.id, .name] | @tsv' users.json
```

Supported flags: `-n -r -j -a -c -s -e -R -C -M -S -f --tab --indent n
--raw-output0 --seq --arg --argjson --slurpfile --rawfile --args
--jsonargs`, plus the `JQ_COLORS` and `NO_COLOR` environment variables.
Exit codes follow jq: 2 for usage or input errors, 3 for compile errors, 5
for runtime errors, and `-e` semantics. `--stream`, `--stream-errors` and
`-L` (modules) are not implemented.



## Performance

Benchmarks run over 10,000 generated records. `bench/` runs the same
queries through jqgo and [gojq](https://github.com/itchyny/gojq) after
checking that both give the same results. On the machine where
`bench/baseline.txt` was recorded, jqgo was faster on all 18 benchmarks,
2.5× faster in geometric mean:

| query | jqgo | gojq |
| --- | --- | --- |
| `[.[] \| .name]` | 0.89 ms | 1.62 ms |
| `select(.age > 50 and .active)` | 2.84 ms | 4.06 ms |
| `map({id, city: .addr.city, ...})` | 4.90 ms | 6.65 ms |
| `select(.name \| test("7$"))` | 3.37 ms | 11.7 ms |
| `reduce .[] as $x ({}; .[$x.addr.city] += 1)` | 9.2 ms | 63.7 ms |
| `INDEX(.id)` | 16 ms | 2.7 s |
| one small document | 1.4 µs | 2.0 µs |

The CLI against jq 1.7 on a 24 MB file of 200,000 records, best of three
runs, with byte-identical output: jqgo was 1.5–2.7× faster on every query
tried, including plain pretty-printing (`.`: 0.59 s vs 0.99 s).

Updates such as `reduce`, `|=` and `INDEX` stay linear because jqgo
modifies containers it allocated itself in place (see
[DESIGN.md](DESIGN.md)). Rerun `bench/` on your own hardware before
relying on these numbers.

## Coverage

Against jq 1.7:

- **Builtins:** 214 of jq's 218. The four missing ones (`modulemeta`,
  `get_search_list`, `get_jq_origin`, `get_prog_origin`) belong to the
  module system. jqgo also has a few builtins from other jq versions (see
  below).
- **jq's own test suites:** 685 of 711 cases pass. The 26 skipped cases
  are the deliberate differences listed next.
- **CLI flags:** all but `--stream`, `--stream-errors`, `-L` and
  `--build-configuration`.

## Differences from jq 1.7.1

- **Modules** (`import`, `include`, `modulemeta`, `-L`) are not supported.
- **Object key order** is sorted, as described above.
- **Numbers.** Integers are exact 64-bit ints. Other numbers are float64, and
  jq 1.7's literal preservation is not implemented, so `1.000` becomes `1`
  and `100000000000000000000` becomes `1e+20`.
- **Regular expressions** use Go's RE2, not Oniguruma. The usual syntax works,
  including named groups `(?<name>…)` and the `g i x s n l` flags.
  Backreferences and lookaround do not, and `\b` is ASCII-only.
- **`$__loc__`** reports the line only (the file is always `<top-level>`).
- `input_line_number` always returns 0.
- A few builtins from later jq releases are available too: `toarray`,
  `trim`, `ltrim`, `rtrim` and `add(f)`.

## Development

See [DESIGN.md](DESIGN.md) for how the implementation fits together.


```sh
go test ./...                               # includes jq's own test suites
go test -race ./...
go test -run '^$' -fuzz FuzzQuery -fuzztime 60s .   # also FuzzInPlace, FuzzFastPath, FuzzDecode
go test -run '^$' -bench . -count 6 > new.txt && benchstat old.txt new.txt
```

`testdata/extra.test` holds extra edge cases whose expected outputs were
generated by jq 1.7. `testdata/jq/` holds jq's test files (MIT, see
`testdata/jq/COPYING`).

## License

MIT
