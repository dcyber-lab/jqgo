// Command jqgo is a jq-compatible command-line JSON processor built on the
// jqgo library.
package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/dcyber-lab/jqgo"
)

const version = "jqgo-0.1.0"

const usage = `Usage: jqgo [OPTIONS] FILTER [FILES...]

jqgo is a jq-compatible JSON processor. It reads JSON values from FILES
(or stdin) and writes the results of FILTER on each of them.

Options:
  -n, --null-input          use null as the single input value
  -R, --raw-input           read each line as a string instead of JSON
  -s, --slurp               read all inputs into one array (or one string with -R)
  -r, --raw-output          write strings without quotes
  -j, --join-output         like -r, without a newline after each output
      --raw-output0         like -r, with a NUL after each output
      --seq                 use RS-separated JSON text sequences (RFC 7464)
  -a, --ascii-output        escape non-ASCII characters
  -c, --compact-output      one line per output
      --tab                 indent with tabs
      --indent n            indent with n spaces (default 2)
  -C, --color-output        colorize output
  -M, --monochrome-output   do not colorize output
  -S, --sort-keys           sort object keys (always on in jqgo)
      --unbuffered          flush the output after each input
  -e, --exit-status         set the exit status from the last output
  -f, --from-file file      read the filter from file
      --arg name value      set $name to the string value
      --argjson name json   set $name to the JSON value
      --slurpfile name file set $name to an array of the JSON values in file
      --rawfile name file   set $name to the contents of file
      --args                remaining arguments are string positional args
      --jsonargs            remaining arguments are JSON positional args
  -h, --help                show this help
  -V, --version             show the version
`

type options struct {
	nullInput, rawInput, slurp, rawOutput, joinOutput, ascii bool
	rawOutput0, seq, unbuffered                              bool
	compact, tab, exitStatus                                 bool
	color                                                    int // -1 off, 0 auto, 1 on
	indent                                                   int
	filter                                                   string
	haveFilter                                               bool
	files                                                    []string
	varNames                                                 []string
	varValues                                                []any
	named                                                    map[string]any
	positional                                               []any
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	opts, action, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "jqgo: %s\nUse jqgo --help for help with command-line options.\n", err)
		return 2
	}
	switch action {
	case "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "version":
		fmt.Fprintln(stdout, version)
		return 0
	}

	names := append([]string{"ARGS"}, opts.varNames...)
	values := append([]any{map[string]any{"positional": opts.positional, "named": opts.named}}, opts.varValues...)
	q, err := jqgo.Compile(opts.filter,
		jqgo.WithVariables(names...),
		jqgo.WithEnviron(os.Environ()),
		jqgo.WithDebugWriter(stderr))
	if err != nil {
		fmt.Fprintf(stderr, "jqgo: error: %s\njqgo: 1 compile error\n", err)
		return 3
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	out := bufio.NewWriterSize(stdout, 64*1024)
	defer out.Flush()
	enc := jqgo.EncodeOptions{Indent: opts.indent, Tab: opts.tab, ASCII: opts.ascii}
	if opts.compact {
		enc.Indent, enc.Tab = 0, false
	}
	if opts.color > 0 || (opts.color == 0 && isTerminal(stdout) && os.Getenv("NO_COLOR") == "") {
		enc.Colors = &jqgo.DefaultColors
		if spec := os.Getenv("JQ_COLORS"); spec != "" {
			if c, ok := jqgo.ParseColors(spec); ok {
				enc.Colors = c
			} else {
				fmt.Fprintln(stderr, "jqgo: failed to set $JQ_COLORS")
			}
		}
	}

	flushEach := opts.unbuffered || isTerminal(stdout)
	in := newInputStream(opts, stdin, stderr)
	encoder := jqgo.NewEncoder(out, enc)
	exit := 0
	var last any
	produced := false

	// consume writes the results of one input, or of a whole source when
	// streaming (fatal is then set, and tells input errors apart); it
	// reports false when processing must stop.
	consume := func(results iter.Seq2[any, error], fatal func(error) bool) bool {
		streamed := fatal != nil
		for r, err := range results {
			if err != nil {
				if fatal != nil && fatal(err) {
					out.Flush()
					fmt.Fprintf(stderr, "jqgo: error (at %s): %s\n", in.position(), err)
					exit = 2
					return false
				}
				var halt *jqgo.HaltError
				if errors.As(err, &halt) {
					out.Flush()
					if !halt.Silent {
						if s, ok := halt.Value.(string); ok {
							fmt.Fprint(stderr, s)
						} else {
							fmt.Fprintf(stderr, "%s\n", jqgo.Marshal(halt.Value))
						}
					}
					exit = halt.Code
					return false
				}
				out.Flush()
				if errors.Is(err, context.Canceled) {
					exit = 130
					return false
				}
				fmt.Fprintf(stderr, "jqgo: error (at %s): %s\n", in.position(), err)
				exit = 5
				if streamed {
					continue // RunReader goes on with the next value
				}
				return true
			}
			produced, last = true, r
			if err := writeValue(out, encoder, r, opts); err != nil {
				out.Flush()
				fmt.Fprintf(stderr, "jqgo: error (at %s): %s\n", in.position(), err)
				exit = 5
				if streamed {
					continue
				}
				return true
			}
			if streamed && flushEach {
				out.Flush()
			}
		}
		// Flushing after every input costs a write per record; jq only does
		// it for a terminal or with --unbuffered (e.g. tail -f | jq).
		if flushEach {
			out.Flush()
		}
		return true
	}
	process := func(v any) bool {
		return consume(q.RunWithInputs(ctx, v, in, values...), nil)
	}

	// A query that only iterates over its input (.[], .items[] ...) is
	// run while the input is parsed, so a huge array is never held whole.
	streaming := q.Streamable() && !opts.nullInput && !opts.slurp && !opts.rawInput && !opts.seq
	if streaming {
		in.sources(func(r io.Reader) bool {
			rec := &errReader{r: r}
			fatal := func(err error) bool {
				var se *jqgo.SyntaxError
				return errors.As(err, &se) || (rec.err != nil && err == rec.err)
			}
			var src io.Reader = rec
			if f, ok := r.(interface{ Name() string }); ok {
				src = namedReader{rec, f.Name()}
			}
			return consume(q.RunReader(ctx, src, values...), fatal)
		})
	} else if opts.nullInput {
		process(nil)
	} else if opts.slurp {
		v, err := in.slurp()
		if err != nil {
			fmt.Fprintf(stderr, "jqgo: error (at %s): %s\n", in.position(), err)
			return 2
		}
		process(v)
	} else {
		for {
			v, err := in.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				out.Flush()
				fmt.Fprintf(stderr, "jqgo: error (at %s): %s\n", in.position(), err)
				exit = 2
				break
			}
			if !process(v) {
				break
			}
		}
	}
	if in.err != nil && exit == 0 {
		exit = 2 // already reported
	}
	if exit == 0 && opts.exitStatus {
		switch {
		case !produced:
			exit = 4
		case last == nil || last == false:
			exit = 1
		}
	}
	return exit
}

func writeValue(w *bufio.Writer, enc *jqgo.Encoder, v any, opts options) error {
	if s, ok := v.(string); ok && (opts.rawOutput || opts.joinOutput || opts.rawOutput0) {
		if opts.rawOutput0 && strings.IndexByte(s, 0) >= 0 {
			return errors.New("Cannot dump a string containing NUL with --raw-output0 option")
		}
		w.WriteString(s)
	} else {
		if opts.seq {
			w.WriteByte(0x1e) // RFC 7464 record separator; jq omits it for raw strings
		}
		if err := enc.Encode(v); err != nil {
			return err
		}
	}
	switch {
	case opts.rawOutput0:
		w.WriteByte(0)
	case !opts.joinOutput:
		w.WriteByte('\n')
	}
	return nil
}

func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// parseArgs returns the action to take: "run", "help" or "version".
func parseArgs(args []string) (options, string, error) {
	opts := options{indent: 2, named: map[string]any{}, positional: []any{}}
	restMode := "" // "", "args", "jsonargs"
	need := func(i, n int, flag string) error {
		if i+n >= len(args) {
			return fmt.Errorf("%s takes %d parameter(s)", flag, n)
		}
		return nil
	}
	setVar := func(name string, v any) {
		opts.varNames = append(opts.varNames, name)
		opts.varValues = append(opts.varValues, v)
		opts.named[name] = v
	}
	positional := func(a string) error {
		if !opts.haveFilter {
			opts.filter, opts.haveFilter = a, true
			return nil
		}
		switch restMode {
		case "args":
			opts.positional = append(opts.positional, a)
		case "jsonargs":
			v, err := parseJSONArg(a)
			if err != nil {
				return fmt.Errorf("invalid JSON text passed to --jsonargs")
			}
			opts.positional = append(opts.positional, v)
		default:
			opts.files = append(opts.files, a)
		}
		return nil
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			for _, rest := range args[i+1:] {
				if err := positional(rest); err != nil {
					return opts, "", err
				}
			}
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			if err := positional(a); err != nil {
				return opts, "", err
			}
			continue
		}
		if strings.HasPrefix(a, "--") {
			switch a {
			case "--null-input":
				opts.nullInput = true
			case "--raw-input":
				opts.rawInput = true
			case "--slurp":
				opts.slurp = true
			case "--raw-output":
				opts.rawOutput = true
			case "--raw-output0":
				opts.rawOutput0 = true
			case "--seq":
				opts.seq = true
			case "--join-output":
				opts.joinOutput = true
			case "--ascii-output":
				opts.ascii = true
			case "--compact-output":
				opts.compact = true
			case "--tab":
				opts.tab = true
			case "--color-output":
				opts.color = 1
			case "--monochrome-output":
				opts.color = -1
			case "--unbuffered":
				opts.unbuffered = true
			case "--sort-keys":
			case "--exit-status":
				opts.exitStatus = true
			case "--indent":
				if err := need(i, 1, a); err != nil {
					return opts, "", err
				}
				n, err := strconv.Atoi(args[i+1])
				if err != nil || n < 0 || n > 7 {
					return opts, "", fmt.Errorf("Cannot indent more than 7 characters")
				}
				opts.indent = n
				i++
			case "--from-file":
				if err := need(i, 1, a); err != nil {
					return opts, "", err
				}
				if err := readFilterFile(&opts, args[i+1]); err != nil {
					return opts, "", err
				}
				i++
			case "--arg":
				if err := need(i, 2, a); err != nil {
					return opts, "", err
				}
				setVar(args[i+1], args[i+2])
				i += 2
			case "--argjson":
				if err := need(i, 2, a); err != nil {
					return opts, "", err
				}
				v, err := parseJSONArg(args[i+2])
				if err != nil {
					return opts, "", fmt.Errorf("invalid JSON text passed to --argjson")
				}
				setVar(args[i+1], v)
				i += 2
			case "--slurpfile", "--rawfile":
				if err := need(i, 2, a); err != nil {
					return opts, "", err
				}
				data, err := os.ReadFile(args[i+2])
				if err != nil {
					return opts, "", fmt.Errorf("Bad JSON in %s %s %s: %v", a, args[i+1], args[i+2], err)
				}
				if a == "--rawfile" {
					setVar(args[i+1], string(data))
				} else {
					vals := []any{}
					dec := jqgo.NewDecoder(bytes.NewReader(data))
					for {
						v, err := dec.Decode()
						if err == io.EOF {
							break
						}
						if err != nil {
							return opts, "", fmt.Errorf("Bad JSON in %s %s %s: %v", a, args[i+1], args[i+2], err)
						}
						vals = append(vals, v)
					}
					setVar(args[i+1], vals)
				}
				i += 2
			case "--args":
				restMode = "args"
			case "--jsonargs":
				restMode = "jsonargs"
			case "--help":
				return opts, "help", nil
			case "--version":
				return opts, "version", nil
			default:
				return opts, "", fmt.Errorf("Unknown option: %s", a)
			}
			continue
		}
		// short flags, possibly combined (-nr)
		for j := 1; j < len(a); j++ {
			switch a[j] {
			case 'n':
				opts.nullInput = true
			case 'R':
				opts.rawInput = true
			case 's':
				opts.slurp = true
			case 'r':
				opts.rawOutput = true
			case 'j':
				opts.joinOutput = true
			case 'a':
				opts.ascii = true
			case 'c':
				opts.compact = true
			case 'C':
				opts.color = 1
			case 'M':
				opts.color = -1
			case 'S':
			case 'e':
				opts.exitStatus = true
			case 'h':
				return opts, "help", nil
			case 'V':
				return opts, "version", nil
			case 'f':
				if err := need(i, 1, "-f"); err != nil {
					return opts, "", err
				}
				if err := readFilterFile(&opts, args[i+1]); err != nil {
					return opts, "", err
				}
				i++
			default:
				return opts, "", fmt.Errorf("Unknown option: %s", a)
			}
		}
	}
	if !opts.haveFilter {
		return opts, "", fmt.Errorf("no filter given")
	}
	return opts, "run", nil
}

func readFilterFile(opts *options, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if opts.haveFilter {
		// the filter slot was taken by a positional argument: it is a file
		opts.files = append([]string{opts.filter}, opts.files...)
	}
	opts.filter, opts.haveFilter = string(data), true
	return nil
}

func parseJSONArg(s string) (any, error) {
	dec := jqgo.NewDecoder(strings.NewReader(s))
	v, err := dec.Decode()
	if err != nil {
		return nil, err
	}
	if _, err := dec.Decode(); err != io.EOF {
		return nil, errors.New("trailing data")
	}
	return v, nil
}

// errReader remembers the error its reader returned, to tell read errors
// from runtime errors.
type errReader struct {
	r   io.Reader
	err error
}

func (e *errReader) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF {
		e.err = err
	}
	return n, err
}

// namedReader passes a file name on to input_filename.
type namedReader struct {
	io.Reader
	name string
}

func (n namedReader) Name() string { return n.name }
