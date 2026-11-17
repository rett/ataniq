//go:build linux

package fakeagent

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// fakeagentPath is the import path of this package.
const fakeagentPath = "ataniq/internal/leadholder/fakeagent"

// moduleRoot returns the directory that holds go.mod, found upwards from the
// test's directory.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if b, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil {
			if !strings.HasPrefix(string(b), "module ataniq\n") {
				t.Fatalf("%s/go.mod is not the module of this repository", dir)
			}
			return dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			t.Fatal("no go.mod above the test directory")
		}
		dir = up
	}
}

// importersOf lists the non-test Go files under root that import path, with
// the number of files it read. Only imports are parsed; build tags are not
// evaluated, so a file for another platform counts too. skip is a directory
// (relative to root, slash separated) that is not walked.
func importersOf(root, path, skip string) (importers []string, files int, err error) {
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == skip || d.Name() == ".git" || d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), p, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		files++
		for _, imp := range f.Imports {
			got, uerr := strconv.Unquote(imp.Path.Value)
			if uerr != nil {
				return uerr
			}
			if got == path {
				importers = append(importers, rel)
			}
		}
		return nil
	})
	sort.Strings(importers)
	return importers, files, err
}

// The package runs a scripted agent when a test binary re-executes itself, and
// a script can start processes, so no production code may link it (the
// lead-hosting design, section 7). Only test files import it.
func TestNoNonTestPackageImportsTheFakeAgent(t *testing.T) {
	importers, files, err := importersOf(moduleRoot(t), fakeagentPath, "internal/leadholder/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	if files < 50 {
		t.Fatalf("only %d Go files read: the walk did not reach the repository", files)
	}
	for _, f := range importers {
		t.Errorf("%s imports %s, which only test files may do", f, fakeagentPath)
	}
}

// The guard itself: a file that imports the package is found, a test file and
// a file under testdata are not, and the package's own directory is skipped.
func TestImportGuardFindsAnImporter(t *testing.T) {
	root := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	imp := "package x\n\nimport (\n\t_ \"" + fakeagentPath + "\"\n)\n"
	write("cmd/tool/main.go", imp)
	write("internal/other/other_linux.go", "package other\n\nimport fa \""+fakeagentPath+"\"\n\nvar _ = fa.EnvScript\n")
	write("internal/other/other_test.go", imp)
	write("internal/other/testdata/x.go", imp)
	write("internal/leadholder/fakeagent/self.go", imp)
	write("internal/clean/clean.go", "package clean\n\nimport _ \"os\"\n")

	got, files, err := importersOf(root, fakeagentPath, "internal/leadholder/fakeagent")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"cmd/tool/main.go", "internal/other/other_linux.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("importers %v, want %v", got, want)
	}
	if files != 3 {
		t.Errorf("read %d files, want 3", files)
	}
}
