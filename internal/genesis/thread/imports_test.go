package thread

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The store imports, of this repository, the wire's words, the brief, the
// catalog, the plan's compiler and the family's strict JSON writer; of the standard library,
// nothing that opens a socket or runs a program. It reads and writes files
// through an os.Root on its own folder and calls no other function of os that
// names a path, but the two that make and look at the folder itself.
func TestImportsAndNoExecution(t *testing.T) {
	allowed := map[string]bool{
		"ataniq/internal/approval/ownerwire":     true,
		"ataniq/internal/genesis/brief":          true,
		"ataniq/internal/genesis/catalog":        true,
		"ataniq/internal/genesis/internal/canon": true,
		"ataniq/internal/genesis/plan":           true,
	}
	banned := map[string]bool{
		"net": true, "net/http": true, "os/exec": true, "unsafe": true, "plugin": true,
		"path/filepath": true, "io/ioutil": true, "os/user": true, "os/signal": true,
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
			case path == "syscall" && name != "fileowner_linux.go":
				t.Errorf("%s imports syscall", name)
			}
		}
	}
	if files == 0 {
		t.Fatal("no Go file read")
	}
}

// The calls of package os that take a path are the folder's own: one to look at
// it, one to make it, one to open it as a root. Every other file is reached
// through the root.
func TestOnlyTheFolderIsReachedByPath(t *testing.T) {
	data, err := os.ReadFile("store.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(data)
	for _, call := range []string{"os.Open(", "os.OpenFile(", "os.ReadFile(", "os.WriteFile(", "os.Remove(", "os.RemoveAll(", "os.Rename(", "os.Create(", "os.ReadDir(", "os.Stat(", "os.MkdirAll(", "os.Chmod("} {
		if strings.Contains(src, call) {
			t.Errorf("store.go calls %s", call)
		}
	}
	file, err := os.ReadFile("file.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []string{"os.Open(", "os.ReadFile(", "os.WriteFile(", "os.Remove(", "os.Rename(", "os.Create(", "os.Lstat(", "os.Stat(", "os.Mkdir("} {
		if strings.Contains(string(file), call) {
			t.Errorf("file.go calls %s", call)
		}
	}
}
