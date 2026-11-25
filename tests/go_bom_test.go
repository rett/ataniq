package tests

// The tagged runs of make test-canary and make test-ownerchannel find the
// packages to test with a grep anchored at the start of a line: a constraint
// line that follows a UTF-8 byte order mark at the start of a file does not
// match it, while the go command drops the mark and applies the constraint.
// So no Go file the grep reads may begin with one, and this check holds the
// grep to that.

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
	"testing/fstest"
)

// utf8BOM is the UTF-8 byte order mark.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// checkNoGoFileStartsWithBOM reports every Go file of fsys that the tagged
// runs' grep reads and that begins with a byte order mark. The grep reads the
// .go files of the tree but those in a folder whose name begins with a dot
// and another character, or with an underscore, or is testdata. Only the
// first three bytes of a file are read.
func checkNoGoFileStartsWithBOM(fsys fs.FS) []string {
	var problems []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			problems = append(problems, p+": "+err.Error())
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != "." && ((strings.HasPrefix(name, ".") && len(name) > 1) || strings.HasPrefix(name, "_") || name == "testdata") {
				return fs.SkipDir
			}
			return nil
		}
		if path.Ext(name) != ".go" || !d.Type().IsRegular() {
			return nil
		}
		f, err := fsys.Open(p)
		if err != nil {
			problems = append(problems, p+": "+err.Error())
			return nil
		}
		defer f.Close()
		head := make([]byte, len(utf8BOM))
		n, err := io.ReadFull(f, head)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			problems = append(problems, p+": "+err.Error())
			return nil
		}
		if bytes.Equal(head[:n], utf8BOM) {
			problems = append(problems, p+" begins with a UTF-8 byte order mark: the tagged runs' grep does not see a constraint after it, which the go command applies; remove the mark")
		}
		return nil
	})
	if err != nil {
		problems = append(problems, err.Error())
	}
	return problems
}

// TestNoGoFileStartsWithBOM: no Go file of the repository begins with a byte
// order mark.
func TestNoGoFileStartsWithBOM(t *testing.T) {
	reportCanaryProblems(t, checkNoGoFileStartsWithBOM(os.DirFS(root(t))))
}

func TestNoGoFileStartsWithBOMCheck(t *testing.T) {
	bom := string(utf8BOM)
	for _, tc := range []struct {
		name  string
		files fstest.MapFS
		bad   []string
	}{
		{"no mark", fstest.MapFS{"a/a.go": goSource(hdrTagged), "a/b.go": {Data: []byte("")}}, nil},
		{"a mark before a tagged constraint", fstest.MapFS{"a/a.go": goSource(bom + hdrTagged)}, []string{"a/a.go"}},
		{"a mark before plain code", fstest.MapFS{"cmd/x/main.go": {Data: []byte(bom + "package main\n")}}, []string{"cmd/x/main.go"}},
		{"a mark alone", fstest.MapFS{"a/a.go": {Data: []byte(bom)}}, []string{"a/a.go"}},
		{"a mark later in a file", fstest.MapFS{"a/a.go": {Data: []byte("package a\n\n// " + bom + "\n")}}, nil},
		{"a file of another kind", fstest.MapFS{"a/a.s": {Data: []byte(bom + hdrTagged + "\n")}, "a/a.go.txt": {Data: []byte(bom)}}, nil},
		{"folders the grep does not read", fstest.MapFS{
			".git/x.go": {Data: []byte(bom)}, "a/.cache/x.go": {Data: []byte(bom)}, "_old/x.go": {Data: []byte(bom)}, "a/testdata/x.go": {Data: []byte(bom)},
		}, nil},
		{"a folder whose name has a dot inside", fstest.MapFS{"a.b/x.go": {Data: []byte(bom)}}, []string{"a.b/x.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkNoGoFileStartsWithBOM(tc.files)
			if len(got) != len(tc.bad) {
				t.Fatalf("want %d problems naming %q, got %q", len(tc.bad), tc.bad, got)
			}
			for i, want := range tc.bad {
				if !strings.HasPrefix(got[i], want+" ") {
					t.Errorf("problem %d is %q, want one naming %s", i, got[i], want)
				}
			}
		})
	}
}
