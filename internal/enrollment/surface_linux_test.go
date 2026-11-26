package enrollment

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestAdmittedWriterSurface(t *testing.T) {
	for typ, want := range map[reflect.Type][]string{
		reflect.TypeFor[*FileRoot]():   nil,
		reflect.TypeFor[LedgerStore](): nil,
		reflect.TypeFor[*fileLedger](): nil,
		reflect.TypeFor[*Store]():      {"Close", "LastReceipt", "Ledger"},
	} {
		var got []string
		for m := range typ.Methods() {
			if m.IsExported() {
				got = append(got, m.Name)
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s exports %v, want %v", typ, got, want)
		}
	}
	for _, typ := range []reflect.Type{reflect.TypeFor[Store](), reflect.TypeFor[FileRoot]()} {
		for field := range typ.Fields() {
			if field.IsExported() && !(typ == reflect.TypeFor[FileRoot]() && field.Name == "Path" && field.Type == reflect.TypeFor[string]()) {
				t.Errorf("%s exports field %s", typ, field.Name)
			}
		}
	}
	files, err := os.ReadDir(".")
	check(t, err)
	var entry []string
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".go") || strings.HasSuffix(f.Name(), "_test.go") {
			continue
		}
		src, err := parser.ParseFile(token.NewFileSet(), f.Name(), nil, 0)
		check(t, err)
		for _, d := range src.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.IsExported() {
				entry = append(entry, fn.Name.Name)
			}
		}
	}
	slices.Sort(entry)
	if !slices.Equal(entry, []string{"AppendAndAdvance", "Enroll", "Start"}) {
		t.Errorf("exported functions %v", entry)
	}
}
