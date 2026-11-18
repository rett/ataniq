//go:build linux

package session

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The session imports neither the injection library nor its parser, directly
// or through any package of this repository it imports: its view and hook
// types are its own, and the injection adapter converts them field by field.
func TestSessionImportsNeitherTheInjectionLibraryNorItsParser(t *testing.T) {
	root := moduleRoot(t)
	seen := map[string]bool{}
	var walk func(dir, from string)
	walk = func(dir, from string) {
		if seen[dir] {
			return
		}
		seen[dir] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s (imported by %s): %v", dir, from, err)
		}
		for _, en := range entries {
			name := en.Name()
			if en.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, im := range f.Imports {
				p, _ := strconv.Unquote(im.Path.Value)
				if p == "ataniq/internal/inject" || strings.HasPrefix(p, "ataniq/internal/inject/") {
					t.Errorf("%s imports %s (reached from %s)", filepath.Join(dir, name), p, from)
				}
				if rest, ok := strings.CutPrefix(p, "ataniq/"); ok {
					walk(filepath.Join(root, filepath.FromSlash(rest)), p)
				}
			}
		}
	}
	walk(".", "the session")
	if len(seen) < 4 {
		t.Errorf("the walk saw only %d packages: %v", len(seen), seen)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package")
		}
		dir = parent
	}
}
