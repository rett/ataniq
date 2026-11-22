package seatgate

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The package is the standard library only, in its tests as well: a standard
// package has no dot in the first element of its path and is not of this
// module. It never starts a process, and it touches the file system and the
// network only in the files its doc names.

// only maps an import to the non-test files that may hold it.
var only = map[string][]string{
	"os":      {"files.go"},
	"syscall": {"files.go"},
}

// never lists imports no file of the package may hold.
var never = map[string]bool{
	"os/exec":     true,
	"os/user":     true,
	"os/signal":   true,
	"unsafe":      true,
	"plugin":      true,
	"net/http":    true,
	"net/rpc":     true,
	"runtime/cgo": true,
}

// importProblems reads the Go files of dir and returns one line for each
// import that is outside the rules, and the number of files read.
func importProblems(dir string) (problems []string, files int, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		files++
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ImportsOnly)
		if err != nil {
			return nil, 0, err
		}
		isTest := strings.HasSuffix(name, "_test.go")
		for _, imp := range f.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return nil, 0, err
			}
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") || first == "ataniq" {
				problems = append(problems, fmt.Sprintf("%s imports %s, which is not the standard library", name, path))
			}
			if never[path] {
				problems = append(problems, fmt.Sprintf("%s imports %s, which this package never uses", name, path))
			}
			if allowed, ok := only[path]; ok && !isTest {
				found := false
				for _, a := range allowed {
					found = found || a == name
				}
				if !found {
					problems = append(problems, fmt.Sprintf("%s imports %s, which only %v may", name, path, allowed))
				}
			}
		}
	}
	return problems, files, nil
}

func TestImportsTheStandardLibraryOnly(t *testing.T) {
	problems, files, err := importProblems(".")
	if err != nil {
		t.Fatal(err)
	}
	if files < 4 {
		t.Fatalf("read %d Go files, want the whole package", files)
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// The check itself must fail on what it exists to refuse.
func TestImportCheckFlagsWhatItRefuses(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("a.go", "package x\nimport _ \"os/exec\"\n")
	write("b.go", "package x\nimport _ \"golang.org/x/crypto/ssh\"\n")
	write("c.go", "package x\nimport _ \"ataniq/internal/approval/record\"\n")
	write("d.go", "package x\nimport _ \"os\"\n")
	write("e.go", "package x\nimport _ \"net/http\"\n")
	write("files.go", "package x\nimport _ \"os\"\n")
	problems, files, err := importProblems(dir)
	if err != nil {
		t.Fatal(err)
	}
	if files != 6 {
		t.Fatalf("read %d files, want 6", files)
	}
	if len(problems) != 5 {
		t.Fatalf("want 5 problems (exec, a module, ataniq, os outside files.go, net/http), got %d: %v", len(problems), problems)
	}
}
