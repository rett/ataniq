package seatnotice

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The package is the standard library only, in its tests as well. It never
// starts a process and never speaks HTTP: the seat imports it, and the seat
// reaches nothing but the gate and this socket.
var never = map[string]bool{
	"os/exec": true, "os/user": true, "os/signal": true, "unsafe": true, "plugin": true,
	"net/http": true, "net/rpc": true, "runtime/cgo": true,
}

func TestImportsStandardLibraryOnly(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		files++
		f, err := parser.ParseFile(token.NewFileSet(), e.Name(), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			first, _, _ := strings.Cut(p, "/")
			if strings.Contains(first, ".") || first == "ataniq" {
				t.Errorf("%s imports %s, which is not the standard library", e.Name(), p)
			}
			if never[p] {
				t.Errorf("%s imports %s, which this package never uses", e.Name(), p)
			}
		}
	}
	if files < 8 {
		t.Errorf("read only %d files", files)
	}
}
