package ownerwire

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// The package imports the standard library and the record package only, in
// its tests as well: a standard package has no dot in the first element of
// its path and is not of this module. So it cannot come to depend on the
// client protocol, the console or the daemon.
func TestImportsTheStandardLibraryAndRecordOnly(t *testing.T) {
	const allowed = "ataniq/internal/approval/record"
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	files, usesRecord := 0, false
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
			if path == allowed {
				usesRecord = true
				continue
			}
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") || first == "ataniq" {
				t.Errorf("%s imports %s, which is neither the standard library nor %s", e.Name(), path, allowed)
			}
		}
	}
	if files < 8 {
		t.Fatalf("read %d Go files, want the whole package", files)
	}
	if !usesRecord {
		t.Fatalf("no file imports %s: the test did not read the package", allowed)
	}
}
