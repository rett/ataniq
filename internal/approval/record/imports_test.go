package record

import (
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The package imports the standard library only, in its tests as well: a
// standard package has no dot in the first element of its path.
func TestImportsTheStandardLibraryOnly(t *testing.T) {
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
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") || first == "ataniq" {
				t.Errorf("%s imports %s, which is not in the standard library", e.Name(), path)
			}
		}
	}
	if files < 5 {
		t.Fatalf("read %d Go files, want the whole package", files)
	}
}

// No record has a field for the verdict block or for the held mark: both
// are the journal's and the card's, and neither is under any digest here.
func TestNoRecordHasAVerdictOrAHeldMark(t *testing.T) {
	seen := make(map[reflect.Type]bool)
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			for _, name := range []string{f.Name, f.Tag.Get("json")} {
				lower := strings.ToLower(name)
				if strings.Contains(lower, "verdict") || strings.Contains(lower, "held") {
					t.Errorf("%s.%s names a verdict or a held mark", typ.Name(), f.Name)
				}
			}
			walk(f.Type)
		}
	}
	for _, v := range []any{Request{}, Pending{}, Decision{}} {
		walk(reflect.TypeOf(v))
	}
	// The walk reached every type the forms are made of.
	for _, v := range []any{Binding{}, Subject{}, Holder{}, Effect{}, MergedInto{}, Merge{}, Summary{},
		PathChange{}, Flag{}, Evidence{}, ExecConfigSet{}, SignOff{}, Console{}} {
		if !seen[reflect.TypeOf(v)] {
			t.Errorf("%T is not part of any record", v)
		}
	}
}
