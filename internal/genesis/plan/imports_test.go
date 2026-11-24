package plan

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The package is a pure library: of this repository it imports the brief,
// the catalog and the family's strict JSON reader and writer; of the
// standard library, nothing that reads or writes a file, opens a socket,
// runs a program or reads the clock. Its templates are read from the
// binary, not from the disk.
func TestImports(t *testing.T) {
	allowed := map[string]bool{
		"ataniq/internal/genesis/brief":          true,
		"ataniq/internal/genesis/catalog":        true,
		"ataniq/internal/genesis/internal/canon": true,
	}
	banned := map[string]bool{
		"os": true, "os/exec": true, "os/user": true, "os/signal": true, "net": true, "net/http": true,
		"io/ioutil": true, "path/filepath": true, "syscall": true, "unsafe": true, "plugin": true,
		"math/rand": true, "math/rand/v2": true, "crypto/rand": true, "log": true, "log/slog": true,
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files++
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(path, "/")
			switch {
			case first == "ataniq":
				if !allowed[path] {
					t.Errorf("%s imports %s", name, path)
				}
			case strings.Contains(first, "."):
				t.Errorf("%s imports %s, which is not the standard library", name, path)
			case banned[path]:
				t.Errorf("%s imports %s", name, path)
			}
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "time.Now(") {
			t.Errorf("%s reads the clock", name)
		}
	}
	if files == 0 {
		t.Fatal("no Go file read")
	}
}
