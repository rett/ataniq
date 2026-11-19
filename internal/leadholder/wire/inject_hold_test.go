package wire_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadholder/wire"
)

// TestInjectCapsAreTheSeamsValues pins the numbers the shared names note
// fixes. The other cap tests are relative to the constants, so they would
// follow a change of one; the fence's own text cap is 4096.
func TestInjectCapsAreTheSeamsValues(t *testing.T) {
	if wire.MaxInjectText != 4096 || wire.MaxInjectDetail != 512 || wire.MaxMessageID != 64 {
		t.Fatalf("caps %d %d %d, want 4096 512 64", wire.MaxInjectText, wire.MaxInjectDetail, wire.MaxMessageID)
	}
}

// wantImports is the exact set of packages the non-test files of the package
// import. A new one, standard library or not, is a reviewed change to this
// list.
const wantImports = "bytes crypto/rand crypto/sha256 encoding/binary encoding/hex encoding/json errors fmt io reflect sort strconv strings sync unicode unicode/utf8"

func TestPackageImportSet(t *testing.T) {
	seen := map[string]bool{}
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			seen[p] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for p := range seen {
		got = append(got, p)
	}
	sort.Strings(got)
	if joined := strings.Join(got, " "); joined != wantImports {
		t.Fatalf("imports\n got  %s\n want %s", joined, wantImports)
	}
}

// TestInjectVocabularyIsNeutral holds that inject.go names no paste and no
// Enter: not in an identifier, not in a string value, not in a comment. It is
// limited to this file: the attach protocol's own vocabulary (a paste frame)
// is elsewhere and stays.
func TestInjectVocabularyIsNeutral(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "inject.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	check := func(kind, s string, pos token.Pos) {
		low := strings.ToLower(s)
		for _, w := range []string{"paste", "enter"} {
			if strings.Contains(low, w) {
				t.Errorf("%s: %s %q has %q", fset.Position(pos), kind, s, w)
			}
		}
	}
	names := 0
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Ident:
			names++
			check("name", n.Name, n.Pos())
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				check("value", n.Value, n.Pos())
			}
		}
		return true
	})
	comments := 0
	for _, g := range f.Comments {
		for _, c := range g.List {
			comments++
			check("comment", c.Text, c.Pos())
		}
	}
	if names == 0 || comments == 0 {
		t.Fatalf("looked at %d names and %d comments", names, comments)
	}
}
