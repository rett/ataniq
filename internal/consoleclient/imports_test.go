package consoleclient

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestImportsOnlyTheClientProtocol holds the package's boundary: of this
// repository it imports only the client protocol, in its tests as well. The
// console, above all, is not imported: the command maps the values.
func TestImportsOnlyTheClientProtocol(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no files: %v", err)
	}
	fset := token.NewFileSet()
	sawClient := false
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if path != "ataniq" && !strings.HasPrefix(path, "ataniq/") {
				continue
			}
			if path != "ataniq/internal/clientapi" {
				t.Errorf("%s imports %s", name, path)
			}
			sawClient = true
		}
	}
	if !sawClient {
		t.Error("no file imports ataniq/internal/clientapi: the check read nothing")
	}
}

// TestOnlyAFileForOneSystemImportsSyscall holds the package building where
// the client side of the client protocol builds: the syscall package differs
// from system to system, so only a file named for one system may import it.
func TestOnlyAFileForOneSystemImportsSyscall(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("no files: %v", err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		base := strings.TrimSuffix(strings.TrimSuffix(name, ".go"), "_test")
		if strings.HasSuffix(base, "_linux") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			if path, _ := strconv.Unquote(imp.Path.Value); path == "syscall" || strings.HasPrefix(path, "golang.org/x/sys/") {
				t.Errorf("%s builds on every system and imports %s", name, path)
			}
		}
	}
}
