package leadregistry

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const registryImportPath = "ataniq/internal/leadregistry"

// unstatedExpectations returns the position and reason of every StateChange
// in f that does not state its expectation of the shown value: an unkeyed
// literal, a literal without the Expected key, one whose Expected is the zero
// Expectation{} written out, and a StateChange that is declared without a
// value (var c lr.StateChange, var a [2]lr.StateChange, new(lr.StateChange)),
// whose Expected is the zero Expectation too. Literals whose type is elided
// (the elements of []lr.StateChange{{...}}, and the elements and values of
// the arrays, slices and maps of StateChange or *StateChange) are literals
// of StateChange and are checked the same way. A caller states
// ExpectShown(entry.Shown), or NoExpectation() with a comment saying why none
// applies. Test files are not checked: the zero value stays unchecked there.
//
// A StateChange reached any other way (a struct field, the result of a
// function) is not seen: the zero Expectation there keeps the unchecked
// behaviour, and only a reviewer can catch it.
func unstatedExpectations(fset *token.FileSet, f *ast.File) []string {
	local := f.Name.Name == "leadregistry"
	alias := ""
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != registryImportPath {
			continue
		}
		alias = "leadregistry"
		if imp.Name != nil {
			alias = imp.Name.Name
		}
	}
	isType := func(e ast.Expr, name string) bool {
		switch t := e.(type) {
		case *ast.Ident:
			return local && t.Name == name
		case *ast.SelectorExpr:
			x, ok := t.X.(*ast.Ident)
			return ok && alias != "" && x.Name == alias && t.Sel.Name == name
		}
		return false
	}
	// zeroValued reports whether a variable of type e holds zero StateChange
	// values from its declaration: a StateChange or an array of them. A
	// pointer, a slice and a map start empty or nil, so their elements come
	// from literals that are checked on their own.
	var zeroValued func(e ast.Expr) bool
	zeroValued = func(e ast.Expr) bool {
		if isType(e, "StateChange") {
			return true
		}
		if a, ok := e.(*ast.ArrayType); ok && a.Len != nil {
			return zeroValued(a.Elt)
		}
		return false
	}
	var out []string
	flag := func(pos token.Pos, why string) {
		out = append(out, fset.Position(pos).String()+": "+why)
	}
	checkLiteral := func(lit *ast.CompositeLit) {
		var expected ast.Expr
		for _, el := range lit.Elts {
			kv, ok := el.(*ast.KeyValueExpr)
			if !ok {
				flag(lit.Pos(), "unkeyed StateChange literal")
				return
			}
			if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Expected" {
				expected = kv.Value
			}
		}
		switch v := expected.(type) {
		case nil:
			flag(lit.Pos(), "StateChange literal without Expected")
		case *ast.CompositeLit:
			if isType(v.Type, "Expectation") {
				flag(v.Pos(), "Expected is the zero Expectation{}; name ExpectShown or NoExpectation")
			}
		}
	}
	// visit checks lit, whose type is ty (the literal's own, or the one its
	// container gives it when the type is elided).
	var visit func(lit *ast.CompositeLit, ty ast.Expr)
	visit = func(lit *ast.CompositeLit, ty ast.Expr) {
		if star, ok := ty.(*ast.StarExpr); ok {
			ty = star.X
		}
		if isType(ty, "StateChange") {
			checkLiteral(lit)
			return
		}
		var elem ast.Expr
		switch t := ty.(type) {
		case *ast.ArrayType:
			elem = t.Elt
		case *ast.MapType:
			elem = t.Value
		default:
			return
		}
		for _, el := range lit.Elts {
			if kv, ok := el.(*ast.KeyValueExpr); ok {
				el = kv.Value
			}
			// A literal with its own type is reached by the walk itself.
			if inner, ok := el.(*ast.CompositeLit); ok && inner.Type == nil {
				visit(inner, elem)
			}
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CompositeLit:
			if n.Type != nil {
				visit(n, n.Type)
			}
		case *ast.ValueSpec:
			if len(n.Values) == 0 && n.Type != nil && zeroValued(n.Type) {
				flag(n.Pos(), "StateChange declared without a value, so with the zero Expectation{}; name ExpectShown or NoExpectation")
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "new" && len(n.Args) == 1 && zeroValued(n.Args[0]) {
				flag(n.Pos(), "new(StateChange) has the zero Expectation{}; name ExpectShown or NoExpectation")
			}
		}
		return true
	})
	return out
}

func parseSnippet(t *testing.T, name, src string) (*token.FileSet, *ast.File) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, src, 0)
	if err != nil {
		t.Fatal(err)
	}
	return fset, f
}

func TestStateChangeGuardFindsUnstatedExpectations(t *testing.T) {
	const caller = `package x

import lr "ataniq/internal/leadregistry"

var (
	a = lr.StateChange{Epoch: 1}
	b = lr.StateChange{Epoch: 1, Expected: lr.NoExpectation()}
	c = lr.StateChange{Epoch: 1, Expected: lr.ExpectShown(lr.Shown{})}
	d = lr.StateChange{1, lr.Expectation{}, "ready", "", lr.Reason{}, ""}
	e = lr.StateChange{Epoch: 1, Expected: lr.Expectation{}}
	f = other.StateChange{Epoch: 1}
)
`
	fset, f := parseSnippet(t, "caller.go", caller)
	got := unstatedExpectations(fset, f)
	if len(got) != 3 {
		t.Fatalf("flagged %d literals, want 3 (a, d, e): %q", len(got), got)
	}
	for i, want := range []string{"without Expected", "unkeyed", "zero Expectation{}"} {
		if !strings.Contains(got[i], want) {
			t.Errorf("finding %d = %q, want it to say %q", i, got[i], want)
		}
	}
	const inPackage = `package leadregistry

var a = StateChange{Epoch: 1}
var b = StateChange{Epoch: 1, Expected: NoExpectation()}
`
	fset, f = parseSnippet(t, "inpackage.go", inPackage)
	if got := unstatedExpectations(fset, f); len(got) != 1 {
		t.Fatalf("in-package literals flagged %d, want 1: %q", len(got), got)
	}
}

// TestStateChangeGuardSeesDeclaredVariablesAndElidedLiterals: a StateChange
// that is declared and filled in afterwards, or written in a literal whose
// type is elided, reaches SetState with the zero Expectation just as an
// unnamed literal does.
func TestStateChangeGuardSeesDeclaredVariablesAndElidedLiterals(t *testing.T) {
	const caller = `package x

import lr "ataniq/internal/leadregistry"

func f(reg *lr.Registry) {
	var c lr.StateChange
	c.State = "ready"
	var p *lr.StateChange
	var arr [2]lr.StateChange
	q := new(lr.StateChange)
	var empty []lr.StateChange
	var none map[string]lr.StateChange
	_, _, _, _, _, _ = c, p, arr, q, empty, none
}

var (
	slice  = []lr.StateChange{{Epoch: 1}}
	ptrs   = []*lr.StateChange{{Epoch: 1}}
	byName = map[string]*lr.StateChange{"a": {Epoch: 1}}
	nested = [][]lr.StateChange{{{Epoch: 1}}}
	array  = [...]lr.StateChange{3: {Epoch: 1}}
	unkeyed = []lr.StateChange{{1, lr.Expectation{}, "ready", "", lr.Reason{}, ""}}
	zero    = []lr.StateChange{{Epoch: 1, Expected: lr.Expectation{}}}

	okSlice  = []lr.StateChange{{Epoch: 1, Expected: lr.NoExpectation()}}
	okMap    = map[string]*lr.StateChange{"a": {Epoch: 1, Expected: lr.ExpectShown(lr.Shown{})}}
	okNested = [][]lr.StateChange{{{Epoch: 1, Expected: lr.NoExpectation()}}}
	okOther  = []other.StateChange{{Epoch: 1}}
	okInts   = [][]int{{1}}
	okFilled lr.StateChange = lr.StateChange{Epoch: 1, Expected: lr.NoExpectation()}
)
`
	fset, f := parseSnippet(t, "caller.go", caller)
	got := unstatedExpectations(fset, f)
	want := []string{
		"declared without a value", // var c
		"declared without a value", // var arr
		"new(StateChange)",         // q
		"without Expected",         // slice
		"without Expected",         // ptrs
		"without Expected",         // byName
		"without Expected",         // nested
		"without Expected",         // array
		"unkeyed",                  // unkeyed
		"zero Expectation{}",       // zero
	}
	if len(got) != len(want) {
		t.Fatalf("flagged %d, want %d: %q", len(got), len(want), got)
	}
	for i, w := range want {
		if !strings.Contains(got[i], w) {
			t.Errorf("finding %d = %q, want it to say %q", i, got[i], w)
		}
	}
	// The same forms of the registry's own package.
	const inPackage = `package leadregistry

var a = []StateChange{{Epoch: 1}}
var b = []StateChange{{Epoch: 1, Expected: NoExpectation()}}
var c StateChange
`
	fset, f = parseSnippet(t, "inpackage.go", inPackage)
	if got := unstatedExpectations(fset, f); len(got) != 2 {
		t.Fatalf("in-package forms flagged %d, want 2 (a, c): %q", len(got), got)
	}
}

// TestProductionStateChangesStateTheirExpectation walks the module's
// non-test Go files, whatever their build constraints, and requires every
// StateChange literal to say what the writer observed of the shown value, or
// that it observed nothing the registry could have changed. A new caller that
// leaves the field out would silently keep the unchecked behaviour.
func TestProductionStateChangesStateTheirExpectation(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		up := filepath.Dir(root)
		if up == root {
			t.Skip("no go.mod above the package")
		}
		root = up
	}
	var checked, literals int
	var bad []string
	fset := token.NewFileSet()
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "third_party", "build", "testdata", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", path, err)
			return nil
		}
		checked++
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.CompositeLit); ok {
				switch ty := lit.Type.(type) {
				case *ast.SelectorExpr:
					if ty.Sel.Name == "StateChange" {
						literals++
					}
				case *ast.Ident:
					if ty.Name == "StateChange" {
						literals++
					}
				}
			}
			return true
		})
		bad = append(bad, unstatedExpectations(fset, f)...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 50 || literals == 0 {
		t.Fatalf("the walk saw %d files and %d StateChange literals; it is not looking at the tree", checked, literals)
	}
	for _, b := range bad {
		rel := strings.TrimPrefix(b, root+string(filepath.Separator))
		t.Errorf("%s", rel)
	}
}
