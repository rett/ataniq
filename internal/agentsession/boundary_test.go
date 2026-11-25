package agentsession

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Pin member order and types, including the private history representation.
func TestTR28ReadinessShape(t *testing.T) {
	fn := reflect.TypeOf(DeliveryReady)
	if fn.NumIn() != 1 || fn.In(0) != reflect.TypeOf(Independent{}) || fn.NumOut() != 1 || fn.Out(0) != reflect.TypeOf(DeliveryResult{}) {
		t.Fatal(fn)
	}
	var lines []string
	seen := map[reflect.Type]bool{}
	var visit func(reflect.Type)
	visit = func(v reflect.Type) {
		if v.Kind() == reflect.Slice || v.Kind() == reflect.Array {
			visit(v.Elem())
			return
		}
		if v.Kind() != reflect.Struct || seen[v] {
			return
		}
		seen[v] = true
		var fields []string
		for _, f := range reflect.VisibleFields(v) {
			if len(f.Index) != 1 {
				continue
			}
			for _, word := range []string{"fetch", "message", "placement", "receipt", "answer"} {
				if strings.Contains(strings.ToLower(f.Name), word) {
					t.Fatal(v, f.Name)
				}
			}
			if (v == reflect.TypeOf(I4{}) || v == reflect.TypeOf(HookView{})) && f.IsExported() {
				t.Fatal(v, f.Name)
			}
			fields = append(fields, f.Name+" "+strings.ReplaceAll(f.Type.String(), "agentsession.", ""))
		}
		lines = append(lines, v.Name()+" {"+strings.Join(fields, ", ")+"}")
		for n := range v.NumField() {
			visit(v.Field(n).Type)
		}
	}
	for _, v := range []any{Independent{}, HookView{}, Result{}, I4{}} {
		visit(reflect.TypeOf(v))
	}
	lines = append(lines, strings.ReplaceAll(fn.String(), "agentsession.", ""))
	slices.Sort(lines)
	b, err := os.ReadFile("sessiontest/testdata/readiness-inputs.golden")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(lines, "\n") + "\n"; got != string(b) {
		t.Fatalf("readiness shape:\n%s", got)
	}
}
func TestTR29ModelBoundary(t *testing.T) {
	allowed := strings.Fields("bytes context crypto/sha256 encoding/binary encoding/json errors fmt slices sort strings unicode/utf8 time ataniq/internal/pack")
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	model := map[string]*ast.File{}
	globals := map[string]*ast.Object{}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		model[name] = f
		for _, decl := range f.Decls {
			g, ok := decl.(*ast.GenDecl)
			if !ok || g.Tok != token.VAR {
				continue
			}
			for _, spec := range g.Specs {
				for _, id := range spec.(*ast.ValueSpec).Names {
					globals[id.Name] = id.Obj
				}
			}
		}
	}
	checkWrite := func(name string, lhs ast.Expr) {
		ast.Inspect(lhs, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			if obj, global := globals[id.Name]; global && (id.Obj == nil || id.Obj == obj) {
				t.Fatal("write to package variable", name, id.Name)
			}
			return true
		})
	}
	for _, name := range files {
		f := model[name]
		if f == nil {
			continue
		}
		timeNames := map[string]bool{}
		var hasPack bool
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if !slices.Contains(allowed, p) {
				t.Fatal(name, p)
			}
			if p == "ataniq/internal/pack" {
				hasPack = true
			}
			if imp.Name != nil && imp.Name.Name == "." {
				t.Fatal("dot import", name, p)
			}
			if p == "time" {
				alias := "time"
				if imp.Name != nil {
					alias = imp.Name.Name
				}
				timeNames[alias] = true
			}
		}
		if hasPack {
			checkPackClosure(t)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv == nil && ast.IsExported(d.Name.Name) && d.Name.Name != "ClassifyReadback" && d.Type.Results != nil {
					for _, result := range d.Type.Results.List {
						if id, ok := result.Type.(*ast.Ident); ok && id.Name == "I4" {
							t.Fatal("additional I4 producer", name, d.Name)
						}
					}
				}
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.GoStmt:
				t.Fatal("goroutine", name)
			case *ast.SelectorExpr:
				if id, ok := x.X.(*ast.Ident); ok && timeNames[id.Name] && slices.Contains([]string{"Now", "Since", "Until", "After", "Sleep"}, x.Sel.Name) {
					t.Fatal("clock", name, x.Sel.Name)
				}
			case *ast.AssignStmt:
				for _, lhs := range x.Lhs {
					checkWrite(name, lhs)
				}
			case *ast.IncDecStmt:
				checkWrite(name, x.X)
			case *ast.UnaryExpr:
				if x.Op == token.AND {
					checkWrite(name, x.X)
				}
			}
			return true
		})
	}
}
func checkPackClosure(t *testing.T) {
	paths, err := filepath.Glob("../pack/*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			name, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(name, "ataniq/") {
				if name != "ataniq/internal/pack" {
					t.Fatal(p, name)
				}
			}
		}
	}
}
