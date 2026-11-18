package presence

import (
	"bytes"
	"errors"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/iotest"
)

func TestNewSession(t *testing.T) {
	src := []byte{0x00, 0x01, 0xab, 0xcd, 0xef, 0x10, 0x7f, 0xff, 0x99, 0x98}
	r := bytes.NewReader(src)

	id, err := NewSession(r)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if id != "0001abcdef107fff" {
		t.Errorf("id = %q, want 0001abcdef107fff", id)
	}
	if !ValidSession(id) {
		t.Errorf("ValidSession(%q) = false", id)
	}
	// It takes eight bytes and no more.
	if r.Len() != 2 {
		t.Errorf("%d bytes left in the reader, want 2", r.Len())
	}
	// The id is fit for a tracker.
	if _, err := New(DefaultWindow, SurfaceConsole, id); err != nil {
		t.Errorf("New with a made id: %v", err)
	}
}

func TestNewSessionReadsInPieces(t *testing.T) {
	// A reader may hand out one byte at a time.
	id, err := NewSession(iotest.OneByteReader(bytes.NewReader([]byte("ABCDEFGH"))))
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	if id != "4142434445464748" {
		t.Errorf("id = %q", id)
	}
}

func TestNewSessionFailures(t *testing.T) {
	boom := errors.New("boom")
	for _, c := range []struct {
		name string
		r    io.Reader
		want error
	}{
		{"an empty reader", bytes.NewReader(nil), io.EOF},
		{"a short reader", bytes.NewReader([]byte{1, 2, 3, 4, 5, 6, 7}), io.ErrUnexpectedEOF},
		{"a failing reader", iotest.ErrReader(boom), boom},
	} {
		id, err := NewSession(c.r)
		if id != "" || !errors.Is(err, c.want) {
			t.Errorf("%s: NewSession = %q, %v, want no id and %v", c.name, id, err, c.want)
		}
	}
}

func TestValidSession(t *testing.T) {
	for _, s := range []string{"0123456789abcdef", "0000000000000000", "ffffffffffffffff"} {
		if !ValidSession(s) {
			t.Errorf("ValidSession(%q) = false", s)
		}
	}
	for _, s := range []string{
		"", "0123456789abcde", "0123456789abcdef0", "0123456789ABCDEF", "0123456789abcdeg",
		"0123456789abcde ", "0123456789abcde\n", "0x23456789abcdef",
	} {
		if ValidSession(s) {
			t.Errorf("ValidSession(%q) = true", s)
		}
	}
}

// TestImportsOnlyTheStandardLibrary holds the package to its rule: no file
// of it, tests included, imports this repository or any other module, and no
// file but a test reads a clock, starts a process or opens a file.
func TestImportsOnlyTheStandardLibrary(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no Go files found: %v", err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, im := range f.Imports {
			path, err := strconv.Unquote(im.Path.Value)
			if err != nil {
				t.Fatalf("%s: import %s: %v", name, im.Path.Value, err)
			}
			// A standard library path has no dot in its first element;
			// the module's own paths start with its name.
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") || first == "ataniq" {
				t.Errorf("%s imports %s, which is not the standard library", name, path)
			}
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			switch path {
			case "errors", "fmt", "time", "io", "encoding/hex":
			default:
				t.Errorf("%s imports %s; the package is pure and needs only errors, fmt, time, io and encoding/hex", name, path)
			}
		}
	}

	// No file but a test asks a clock for the time.
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, call := range []string{"time.Now", "time.Since", "time.Until", "time.After(", "time.Tick", "time.NewTimer", "time.Sleep", "go func"} {
			if bytes.Contains(src, []byte(call)) {
				t.Errorf("%s uses %s; the package takes every time from its caller", name, call)
			}
		}
	}
}
