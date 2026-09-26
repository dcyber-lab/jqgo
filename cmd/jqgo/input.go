package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dcyber-lab/jqgo"
)

// inputStream reads JSON values (or raw lines) from the input files in
// order, or from stdin when there are none. It also backs the input and
// inputs builtins, so a query can pull values ahead of the main loop.
type inputStream struct {
	files []string
	stdin io.Reader
	raw   bool
	seq   bool      // RFC 7464: records separated by RS
	warn  io.Writer // where skipped records are reported

	next  int // index of the next file to open
	name  string
	f     *os.File
	dec   *jqgo.Decoder
	lines *bufio.Reader
	count int // values read from the current source
	err   error
}

func newInputStream(opts options, stdin io.Reader, warn io.Writer) *inputStream {
	s := &inputStream{files: opts.files, stdin: stdin, raw: opts.rawInput, seq: opts.seq && !opts.rawInput, warn: warn}
	if len(s.files) == 0 {
		s.open("<stdin>", stdin)
		s.next = -1
	}
	return s
}

func (s *inputStream) open(name string, r io.Reader) {
	s.name = name
	s.count = 0
	br := bufio.NewReaderSize(r, 64*1024)
	if s.raw || s.seq {
		s.lines, s.dec = br, nil
	} else {
		s.dec, s.lines = jqgo.NewDecoder(br), nil
	}
}

// advance opens the next file; it reports false when none are left.
func (s *inputStream) advance() bool {
	if s.f != nil {
		s.f.Close()
		s.f = nil
	}
	for s.next >= 0 && s.next < len(s.files) {
		name := s.files[s.next]
		s.next++
		f, err := os.Open(name)
		if err != nil {
			s.err = fmt.Errorf("Could not open %s: %v", name, err)
			fmt.Fprintf(s.warn, "jqgo: error: %s\n", s.err)
			continue
		}
		s.f = f
		s.open(name, f)
		return true
	}
	s.dec, s.lines = nil, nil
	return false
}

// Next implements jqgo.Inputs.
func (s *inputStream) Next() (any, error) {
	for {
		if s.dec == nil && s.lines == nil {
			if !s.advance() {
				return nil, io.EOF
			}
		}
		if s.seq {
			v, ok, err := s.nextRecord()
			if err != nil {
				return nil, err
			}
			if !ok {
				s.lines = nil
				continue
			}
			return v, nil
		}
		if s.raw {
			line, err := s.lines.ReadString('\n')
			if err == io.EOF && line == "" {
				s.lines = nil
				continue
			}
			if err != nil && err != io.EOF {
				return nil, err
			}
			s.count++
			return strings.TrimSuffix(line, "\n"), nil
		}
		v, err := s.dec.Decode()
		if err == io.EOF {
			s.dec = nil
			continue
		}
		if err != nil {
			s.dec = nil // give up on this source after a syntax error
			return nil, err
		}
		s.count++
		return v, nil
	}
}

// sources calls f with each input in turn (stdin when there are no
// files), for Query.RunReader. Files that cannot be opened are reported
// and skipped, as with Next.
func (s *inputStream) sources(f func(r io.Reader) bool) {
	if s.next < 0 {
		s.dec = nil
		f(struct{ io.Reader }{s.stdin}) // hides os.Stdin's Name: input_filename is null
		return
	}
	for s.advance() {
		s.dec = nil
		if !f(s.f) { // *os.File: RunReader takes input_filename from its Name
			break
		}
	}
	if s.f != nil {
		s.f.Close()
		s.f = nil
	}
}

// Filename implements input_filename.
func (s *inputStream) Filename() any {
	if s.name == "" || s.name == "<stdin>" {
		return nil
	}
	return s.name
}

func (s *inputStream) position() string {
	name := s.name
	if name == "" {
		name = "<stdin>"
	}
	return fmt.Sprintf("%s:%d", name, s.count)
}

// slurp reads everything: an array of values, or one string with -R.
func (s *inputStream) slurp() (any, error) {
	if s.raw {
		var sb strings.Builder
		for {
			if s.lines == nil && !s.advance() {
				return sb.String(), nil
			}
			if _, err := io.Copy(&sb, s.lines); err != nil {
				return nil, err
			}
			s.lines = nil
		}
	}
	all := []any{}
	for {
		v, err := s.Next()
		if err == io.EOF {
			return all, nil
		}
		if err != nil {
			return nil, err
		}
		all = append(all, v)
	}
}

// nextRecord reads RS-separated records until one holds exactly one JSON
// value. Records that do not are reported and skipped, as jq does. ok is
// false at the end of the current source.
func (s *inputStream) nextRecord() (v any, ok bool, err error) {
	for {
		rec, rerr := s.lines.ReadBytes(0x1e)
		if rerr != nil && rerr != io.EOF {
			return nil, false, rerr
		}
		rec = bytes.TrimSuffix(rec, []byte{0x1e})
		if len(bytes.TrimSpace(rec)) > 0 {
			dec := jqgo.NewDecoder(bytes.NewReader(rec))
			val, derr := dec.Decode()
			if derr == nil {
				if _, extra := dec.Decode(); extra == io.EOF {
					s.count++
					return val, true, nil
				}
				derr = errors.New("more than one value in a record")
			}
			fmt.Fprintf(s.warn, "jqgo: ignoring parse error: %v\n", derr)
		}
		if rerr == io.EOF {
			return nil, false, nil
		}
	}
}
