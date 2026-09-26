# How jqgo works

A map of the implementation for someone about to change it. The README
covers what jqgo does; this covers how, and which invariants hold it
together.

## Pipeline

```
source ──lex──▶ tokens ──parse──▶ AST ──check──▶ resolved AST ──eval──▶ results
lexer.go         parser.go, ast.go    check.go, simple.go    eval.go and friends
```

- **lexer.go** tokenizes. String literals keep `\(...)` interpolations as
  nested token lists, which the parser parses recursively.
- **parser.go** is a recursive-descent parser following jq 1.7's
  precedence: `|` < `,` < `//` < assignment ops < `or` < `and` <
  comparisons < `+ -` < `* / %` < unary minus < postfix. `def`, `as`
  bindings and `label` extend as far right as possible, like bison's
  grammar in jq.
- **check.go** resolves every name once. A call either refers to an
  enclosing definition or parameter (`callNode.lexical`, found at run time
  by walking the environment) or to a global function (`callNode.global`:
  a jq-defined builtin, a native, or a `WithFunction`). Undefined names are
  compile errors. It also decides `reduceNode.inPlace` (see Assignment).
- **simple.go** `markSimple` flags the nodes `eval1` can evaluate
  directly (see Fast path).
- The builtin library (`builtin.jq`, plus natives registered from the
  `builtins_*.go` files) is parsed and checked once per process, lazily.

## Values

Values are plain Go values: `nil, bool, int, float64, string, []any,
map[string]any`. Integers that fit stay `int`; arithmetic falls back to
float64 on overflow. Object keys come out sorted because Go maps have no
order.

**Invariant: everything the evaluator handles is canonical at the top
level, but the elements of a container may not be.** Input from the
caller is converted lazily (`value.go`):

- `norm` converts the top level of a value (int64 → int, json.Number,
  []string, structs through encoding/json...). It is free for canonical
  values.
- Elements are converted when they are taken out of a container: `index`,
  iteration, `..`, and the walks in `setpath`/`delpaths`.
- Functions that look inside containers without taking elements out
  (`compare`, `equal`, `contains`, the encoder, `add`, `join`, `flatten`,
  `@csv`...) call `top` on what they meet.

So canonical input (what `json.Unmarshal` gives) is never walked or
copied. `TestJQSuiteLooseInput` reruns jq's test suites with every input
rewritten into non-canonical shapes; a missing conversion fails dozens of
its cases.

Values are immutable. Nothing mutates a container it did not allocate
itself during the current operation (see Assignment).

## Evaluation

`eval.go` walks the AST in continuation-passing style:

```go
func (e *evaluator) run(n node, env *envT, v any, p *pathT, out emitFunc) error
```

Each node calls `out` once per output. Backtracking is ordinary Go
control flow. Stopping early is returning an error up the stack:

| error | raised by | caught by |
|---|---|---|
| `*breakError` | `break $l` | the matching `label` |
| `*stopError` | `limit`, `first`, `isempty`, `\|=`, a consumer that stops ranging | whoever allocated that particular `*stopError` |
| `*passError` | `try`, `?//`, wrapping errors raised downstream of their body | the same `try`, which unwraps it and re-raises the original |
| `*ValueError` | `error(v)` | `try`/`catch` (the value is what `catch` sees) |
| `*HaltError` | `halt`, `halt_error` | nobody; it ends the run |

`try` must only catch errors raised by its body, not errors raised by
whatever consumes the body's outputs. It wraps its output callback so
downstream errors come back as a `passError` carrying its own token, and
passes those through. `catchable` lists what `try`, `?` and `//` must
never intercept.

**Environment.** `envT` is a linked list of variable, function and label
bindings. A closure is a `funcDef` plus the environment it was defined in.
A filter argument is passed as a zero-arity closure over the caller's
environment. `$x` parameters are evaluated in the caller (a cartesian
product over the arguments, first argument varying slowest) and bound as
both `$x` and `x`.

**Order of evaluation** follows jq, and tests depend on it:
- binary operators evaluate the right operand first (`[(1,2) + (10,20)]`
  is `[11,12,21,22]`);
- `and`/`or` evaluate the left first;
- in `.[k]` the key is evaluated first, against the same input as the
  term;
- string interpolations are evaluated last to first;
- native value functions (`defFn`) get the cartesian product of their
  arguments with the last argument varying slowest.

**Path tracking.** When `p != nil` the evaluator is computing paths
(`path(f)`, the left side of assignments, `del`, `paths`). Every value
travels with a `pathT`, a linked list of keys. Nodes that can extend a
path (index, slice, iterate, `..`, `getpath`, and control flow such as
`if`, `//`, `select`, `recurse`, `first`, `limit`, `label`) pass it
along. Anything else emits an *invalid* path carrying the computed
value. The error is only raised if that invalid path is later indexed or
returned, which is how jq behaves.

## Fast path

Most of a query is made of expressions that produce exactly one value:
field access, literals, variables, arithmetic, object and string
construction, calls to value builtins with such arguments.
`markSimple` flags them after compilation, and `run` evaluates a flagged
node with `eval1` (simple.go), a plain recursive function with no
closures or callbacks. `eval1` must evaluate operands in exactly the
order `run` does, because that decides which error wins.
`TestFastPathSuite` and `FuzzFastPath` run programs with and without it
and compare.

## Assignment and in-place updates

`assign.go` implements `=`, `|=`, `op=` and `//=`:
- paths come from `path(lhs)` on the original input;
- the right side of `=`, `op=` and `//=` is evaluated on the original
  input, once per output;
- `|=` takes the first output of `f`, and deletes the paths for which `f`
  produced nothing (jq 1.7.1 semantics).

Copying a container for every path would make `.[] |= f` and
`reduce ... (.[$k] = $v)` quadratic. Instead, an `ownSet` records the
containers the current operation allocated itself; those are modified in
place. The rules:

1. Only containers in the ownSet are mutated. Input, literals and
   anything else are copied on first write.
2. Before `|=` hands the value at a path to `f`, `exposeAt` keeps
   ownership only of that value's strict ancestors, since `f` may return
   parts of it. For a slice path the parent is dropped too, because a
   slice shares its parent's backing array.
3. `reduceStep` lets a reduce keep ownership of its accumulator across
   iterations, but only for updates that cannot capture the accumulator:
   - `|=` (whose `f` only sees subvalues);
   - `=`, `op=` or `. + x` whose right side is `inputFree`, a
     conservative syntactic check;
   - and only when the update produced exactly one output.

   An assignment whose right side yields several values must start each
   output from the same accumulator, so it gives ownership up.

`TestInPlaceEquivalence` and `FuzzInPlace` check that all of this is
invisible: every program is run with the evaluator's `copyOnly` switch on
and off, and the results must be identical, the input untouched, and
yielded results never modified afterwards. Each of the three rules above
has been removed once to confirm the test fails without it.

## Builtins

- `builtin.jq` holds definitions written in jq (from jq 1.7.1's own
  library where possible).
- Natives are registered in `builtins_*.go`, `regex.go`, `time.go` and
  `math.go`:
  - `defFn(name, arity, valueFunc)` gets its arguments already evaluated
    (one call per combination) and returns one value.
  - `defGen(name, arity, genFunc)` gets the argument nodes and emits any
    number of values. Use it for generators, for anything that must keep
    path tracking alive, and for control like `limit`.
- Name resolution precedence: enclosing definitions, then `WithFunction`,
  then natives and `builtin.jq`.

To add a builtin: write it in `builtin.jq` if it is expressible in jq and
not hot; otherwise register a native in the file for its domain. Then
add cases to `testdata/extra.test` with outputs generated by real jq (see
Tests).

Error messages that match jq's wording live in `errors.go`; they are part
of the compatibility surface.

## Safety when embedded

- `tick` checks the context every 1024 units of work. It is called from
  `run` and from loops that emit without re-entering `run` (iteration,
  `..`, `range`).
- Recursion deeper than 100,000 evaluator frames is an error
  (`errDepth`) instead of a Go stack overflow.
- A panic inside the evaluator becomes an "internal error" result. A
  panic in the caller's loop body propagates normally.
- String repetition is capped at 128 MB. Memory in general is not
  limited, so hostile queries belong in a separate process.

## Tests

| What | Where |
|---|---|
| jq 1.7.1's own suites, with documented skips | `testdata/jq/`, `jqtest_test.go` |
| Extra edge cases, expected outputs generated by jq 1.7 | `testdata/extra.test` |
| Same suites with non-canonical Go inputs | `TestJQSuiteLooseInput` |
| In-place updates vs copying | `inplace_test.go` (`TestInPlaceEquivalence`, `FuzzInPlace`) |
| Fast path vs general evaluator | `TestFastPathSuite`, `FuzzFastPath` |
| Decoder: whole vs byte-at-a-time, vs encoding/json | `decode_test.go`, `FuzzDecode` |
| No panics or hangs on arbitrary programs | `FuzzQuery` |
| Library API and CLI behaviour | `api_test.go`, `cmd/jqgo/main_test.go` |
| Benchmarks; comparison with gojq | `bench_test.go`, `bench/` |

`extra.test` uses jq's test format: program, input, then expected
outputs, with a blank line after each case. To add cases, write the
program and input, and take the expected lines from `jq -c` (jq 1.7 or
newer); never write expected outputs by hand.
