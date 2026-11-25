package tests

// This file holds the check that lets ./tests/ run once, in make test, and not
// again in the tagged runs of make test-canary and make test-ownerchannel. That
// is sound only while the package builds the same under every tag set, which
// holds while no file of its test closure, its own test files and the files of
// every package they import, has a build constraint that names the canary tag
// or the owner channel's tag. The tagged runs' grep reads only the folders it
// finds, so it would not notice a tagged file in a package that ./tests/
// imports; this check does, under each of the four tag sets the targets build
// with, so that any return of ./tests/ to a tagged run is a deliberate change.
// It reads the constraints of the Go files and of the other sources (assembly,
// C and the like, which a constraint selects as it does a Go file), and it
// compares the sources each tag set selects with those the untagged build
// selects, so that a selection that differs is found even where reading a
// constraint would miss it.
// The checks of the tags in this package run go list with the tags they need
// and do not depend on the tags the package itself is built with.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// closurePackage is the part of a go list -json entry the check reads.
type closurePackage struct {
	ImportPath string
	Dir        string
	Standard   bool
	GoFiles    []string
	CgoFiles   []string
	// The sources that are not Go, selected by constraints as Go files are.
	CFiles       []string
	CXXFiles     []string
	MFiles       []string
	HFiles       []string
	FFiles       []string
	SFiles       []string
	SwigFiles    []string
	SwigCXXFiles []string
	// SysoFiles are objects, selected by their names alone; they are compared
	// and not read.
	SysoFiles []string
	// The files a constraint leaves out, Go and other.
	IgnoredGoFiles    []string
	IgnoredOtherFiles []string
	TestGoFiles       []string
	XTestGoFiles      []string
}

// closureListFlags list the test closure of a package with the fields of
// closurePackage.
var closureListFlags = []string{"-deps", "-test", "-json=ImportPath,Dir,Standard,GoFiles,CgoFiles," +
	"CFiles,CXXFiles,MFiles,HFiles,FFiles,SFiles,SwigFiles,SwigCXXFiles,SysoFiles," +
	"IgnoredGoFiles,IgnoredOtherFiles,TestGoFiles,XTestGoFiles"}

// otherSources are the selected sources of p that are not Go and that hold
// text a constraint is read from.
func (p closurePackage) otherSources() []string {
	return slices.Concat(p.CFiles, p.CXXFiles, p.MFiles, p.HFiles, p.FFiles, p.SFiles, p.SwigFiles, p.SwigCXXFiles)
}

// closureTagSets are the tag settings the targets build with: none (make
// test), the canary tag (make test-canary), and the owner channel's alone and
// with the canary tag (make test-ownerchannel).
var closureTagSets = []string{"", canaryTag, ownerTag, ownerTag + "," + canaryTag}

// decodeClosure reads the stream of JSON objects go list -json prints.
func decodeClosure(listing string) ([]closurePackage, error) {
	var pkgs []closurePackage
	dec := json.NewDecoder(strings.NewReader(listing))
	for {
		var p closurePackage
		err := dec.Decode(&p)
		if errors.Is(err, io.EOF) {
			return pkgs, nil
		}
		if err != nil {
			return nil, err
		}
		pkgs = append(pkgs, p)
	}
}

// checkTestsClosure reports every file of the test closure of the package in
// testsDir whose build constraint names either tag. A package of the standard
// library is not read. Of every other package it reads the files built, Go and
// other sources, and the files a constraint leaves out, Go and other (a file
// left out under one tag set is built under another), and of the package in
// testsDir also its test files. The test
// files of the other packages are not built and are not read. A closure without
// the test files of the package in testsDir is a problem, so that the check
// cannot pass by reading nothing.
func checkTestsClosure(tags string, pkgs []closurePackage, testsDir string, read func(string) ([]byte, error)) []string {
	var problems []string
	seen := map[string]bool{}
	tests := 0
	for _, p := range pkgs {
		if p.Standard {
			continue
		}
		own := p.Dir == testsDir
		files := slices.Concat(p.GoFiles, p.CgoFiles, p.otherSources(), p.IgnoredOtherFiles)
		for _, f := range p.IgnoredGoFiles {
			if own || !strings.HasSuffix(f, "_test.go") {
				files = append(files, f)
			}
		}
		if own {
			tests += len(p.TestGoFiles) + len(p.XTestGoFiles)
			files = slices.Concat(files, p.TestGoFiles, p.XTestGoFiles)
		}
		for _, f := range files {
			if !filepath.IsAbs(f) {
				f = filepath.Join(p.Dir, f)
			}
			if seen[f] {
				continue
			}
			seen[f] = true
			data, err := read(f)
			if err != nil {
				problems = append(problems, fmt.Sprintf("tags %q: cannot read %s of %s: %v", tags, f, p.ImportPath, err))
				continue
			}
			all, bad := headerConstraints(string(data))
			for _, e := range all {
				if exprNames(e, canaryTag) || exprNames(e, ownerTag) {
					problems = append(problems, fmt.Sprintf("tags %q: the test closure of ./tests/ holds %s (package %s), whose build constraint %q names a tag, so ./tests/ no longer builds the same under every tag set and must run in the tagged runs again", tags, f, p.ImportPath, e.String()))
				}
			}
			for _, line := range bad {
				if strings.Contains(line, canaryTag) || strings.Contains(line, ownerTag) {
					problems = append(problems, fmt.Sprintf("tags %q: %s (package %s) has a constraint line that names a tag and does not parse: %q", tags, f, p.ImportPath, line))
				}
			}
		}
	}
	if tests == 0 {
		problems = append(problems, fmt.Sprintf("tags %q: the listing holds no test file of %s", tags, testsDir))
	}
	return problems
}

// closureInputs are the sources the test build of the package in testsDir
// selects, one entry for each, its package and its path: the Go files and the
// other sources of every package of the closure, the standard library's too,
// and the test files of the package in testsDir, which alone are built.
func closureInputs(pkgs []closurePackage, testsDir string) []string {
	var in []string
	for _, p := range pkgs {
		files := slices.Concat(p.GoFiles, p.CgoFiles, p.otherSources(), p.SysoFiles)
		if p.Dir == testsDir {
			files = slices.Concat(files, p.TestGoFiles, p.XTestGoFiles)
		}
		for _, f := range files {
			if !filepath.IsAbs(f) {
				f = filepath.Join(p.Dir, f)
			}
			in = append(in, p.ImportPath+" "+f)
		}
	}
	slices.Sort(in)
	return slices.Compact(in)
}

// compareClosureInputs reports every source that the build under tags
// selects and the untagged build does not, or the other way round: either
// means ./tests/ does not build the same under every tag set.
func compareClosureInputs(tags string, untagged, tagged []string) []string {
	var problems []string
	for _, in := range tagged {
		if _, found := slices.BinarySearch(untagged, in); !found {
			problems = append(problems, fmt.Sprintf("tags %q: the test build of ./tests/ selects %s, which the untagged build does not, so ./tests/ must run in the tagged runs again", tags, in))
		}
	}
	for _, in := range untagged {
		if _, found := slices.BinarySearch(tagged, in); !found {
			problems = append(problems, fmt.Sprintf("tags %q: the test build of ./tests/ leaves out %s, which the untagged build selects, so ./tests/ must run in the tagged runs again", tags, in))
		}
	}
	return problems
}

// TestCanaryTagTestsClosure lists the test closure of ./tests/ under each tag
// set the targets build with, finds no file whose constraint names a tag, and
// finds that every tag set selects the sources the untagged build does.
func TestCanaryTagTestsClosure(t *testing.T) {
	testsDir := filepath.Join(root(t), "tests")
	var untagged []string
	for _, tags := range closureTagSets {
		pkgs, err := decodeClosure(ownerGoList(t, tags, closureListFlags, "./tests/"))
		if err != nil {
			t.Fatalf("tags %q: %v", tags, err)
		}
		reportCanaryProblems(t, checkTestsClosure(tags, pkgs, testsDir, os.ReadFile))
		in := closureInputs(pkgs, testsDir)
		if tags == "" {
			untagged = in
			if len(in) == 0 {
				t.Fatal("the untagged build of ./tests/ selects no source")
			}
			continue
		}
		reportCanaryProblems(t, compareClosureInputs(tags, untagged, in))
	}
}

func TestCanaryTagTestsClosureCheck(t *testing.T) {
	const testsDir = "/r/tests"
	files := map[string]string{
		"/r/tests/a_test.go":          "package tests\n",
		"/r/tests/b_test.go":          "package tests_test\n",
		"/r/internal/a/a.go":          "package a\n",
		"/r/internal/a/linux.go":      hdrPlain + "\n\npackage a\n",
		"/r/internal/a/a_test.go":     hdrTagged + "\n\npackage a\n",
		"/r/internal/a/x_test.go":     ownHdrTagged + "\n\npackage a\n",
		"/r/internal/a/comment.go":    "// the ataniq_canary tag is not a constraint\n\npackage a\n",
		"/r/internal/t/canary.go":     hdrTagged + "\n\npackage t\n",
		"/r/internal/t/release.go":    hdrNegated + "\n\npackage t\n",
		"/r/internal/t/owner.go":      ownHdrTagged + "\n\npackage t\n",
		"/r/internal/t/plus.go":       hdrPlusTagged + "\n\npackage t\n",
		"/r/internal/t/bad.go":        "//go:build linux && (ataniq_canary\n\npackage t\n",
		"/r/tests/canary_x_test.go":   hdrTagged + "\n\npackage tests\n",
		"/r/tests/release_x_test.go":  ownHdrNegated + "\n\npackage tests\n",
		"/r/tests/bom_x_test.go":      "\uFEFF" + hdrTagged + "\n\npackage tests\n",
		"/r/internal/s/asm_amd64.s":   ownHdrTagged + "\n\n#include \"textflag.h\"\n",
		"/r/internal/s/asm_other.s":   hdrNegated + "\n\n#include \"textflag.h\"\n",
		"/r/internal/s/plain_amd64.s": "#include \"textflag.h\"\n\n" + ownHdrTagged + "\n",
		"/r/internal/s/c.c":           hdrTagged + "\n\nint x;\n",
		"/cache/testmain.go":          "package main\n",
		"/std/os/os.go":               hdrTagged + "\n\npackage os\n",
	}
	read := func(name string) ([]byte, error) {
		data, ok := files[name]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(data), nil
	}
	clean := []closurePackage{
		{ImportPath: "os", Dir: "/std/os", Standard: true, GoFiles: []string{"os.go"}},
		// The test files of an imported package are not built, tagged or not.
		{ImportPath: "m/internal/a", Dir: "/r/internal/a", GoFiles: []string{"a.go", "comment.go"}, IgnoredGoFiles: []string{"linux.go", "a_test.go"}, TestGoFiles: []string{"x_test.go"}},
		{ImportPath: "m/tests", Dir: testsDir, TestGoFiles: []string{"a_test.go"}, XTestGoFiles: []string{"b_test.go"}},
		{ImportPath: "m/tests [m/tests.test]", Dir: testsDir, GoFiles: []string{"a_test.go"}},
		{ImportPath: "m/tests.test", Dir: testsDir, GoFiles: []string{"/cache/testmain.go"}},
	}
	with := func(extra ...closurePackage) []closurePackage { return append(slices.Clone(clean), extra...) }
	for _, tc := range []struct {
		name string
		pkgs []closurePackage
		bad  []string // files named by the problems, or one substring
	}{
		{"a closure with no tagged file", clean, nil},
		{"a built file of an import names the canary tag", with(closurePackage{ImportPath: "m/internal/t", Dir: "/r/internal/t", GoFiles: []string{"canary.go"}}), []string{"/r/internal/t/canary.go"}},
		{"a left-out release counterpart in an import", with(closurePackage{ImportPath: "m/internal/t", Dir: "/r/internal/t", IgnoredGoFiles: []string{"release.go"}}), []string{"/r/internal/t/release.go"}},
		{"an import names the owner channel's tag", with(closurePackage{ImportPath: "m/internal/t", Dir: "/r/internal/t", IgnoredGoFiles: []string{"owner.go"}}), []string{"/r/internal/t/owner.go"}},
		{"an old style plus build line", with(closurePackage{ImportPath: "m/internal/t", Dir: "/r/internal/t", CgoFiles: []string{"plus.go"}}), []string{"/r/internal/t/plus.go"}},
		{"a constraint line that does not parse", with(closurePackage{ImportPath: "m/internal/t", Dir: "/r/internal/t", IgnoredGoFiles: []string{"bad.go"}}), []string{"/r/internal/t/bad.go"}},
		{"a tagged test file of ./tests/", with(closurePackage{ImportPath: "m/tests", Dir: testsDir, IgnoredGoFiles: []string{"canary_x_test.go"}}), []string{"/r/tests/canary_x_test.go"}},
		{"a release counterpart among the tests of ./tests/", with(closurePackage{ImportPath: "m/tests_test [m/tests.test]", Dir: testsDir, XTestGoFiles: []string{"release_x_test.go"}}), []string{"/r/tests/release_x_test.go"}},
		{"a byte order mark before a tagged test file of ./tests/", with(closurePackage{ImportPath: "m/tests", Dir: testsDir, IgnoredGoFiles: []string{"bom_x_test.go"}}), []string{"/r/tests/bom_x_test.go"}},
		{"a tagged assembly file of an import, selected", with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", SFiles: []string{"asm_amd64.s"}}), []string{"/r/internal/s/asm_amd64.s"}},
		{"a tagged assembly file of an import, left out", with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", IgnoredOtherFiles: []string{"asm_amd64.s", "asm_other.s"}}), []string{"/r/internal/s/asm_amd64.s", "/r/internal/s/asm_other.s"}},
		{"an untagged assembly file whose tag is below its code", with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", SFiles: []string{"plain_amd64.s"}}), nil},
		{"a tagged C file of an import", with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", CFiles: []string{"c.c"}}), []string{"/r/internal/s/c.c"}},
		{"a file that cannot be read", with(closurePackage{ImportPath: "m/internal/gone", Dir: "/r/internal/gone", GoFiles: []string{"gone.go"}}), []string{"/r/internal/gone/gone.go"}},
		{"no test file of ./tests/", clean[:2], []string{"no test file of " + testsDir}},
		{"an empty listing", nil, []string{"no test file of " + testsDir}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkTestsClosure("x", tc.pkgs, testsDir, read)
			if len(got) != len(tc.bad) {
				t.Fatalf("want %d problems naming %q, got %q", len(tc.bad), tc.bad, got)
			}
			for i, want := range tc.bad {
				if !strings.Contains(got[i], want) {
					t.Errorf("problem %d is %q, want one naming %s", i, got[i], want)
				}
			}
		})
	}

	// A tag set that selects other sources than the untagged build: a tagged
	// assembly file of an import, and a test file of ./tests/ that only the tag
	// builds.
	asmUntagged := with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", GoFiles: []string{"s.go"}, IgnoredOtherFiles: []string{"asm_amd64.s"}})
	asmTagged := with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", GoFiles: []string{"s.go"}, SFiles: []string{"asm_amd64.s"}})
	if got := compareClosureInputs("x", closureInputs(asmUntagged, testsDir), closureInputs(asmTagged, testsDir)); len(got) != 1 ||
		!strings.Contains(got[0], "selects m/internal/s /r/internal/s/asm_amd64.s") {
		t.Errorf("a tagged assembly file selected by the tag: %q", got)
	}
	if got := compareClosureInputs("x", closureInputs(asmTagged, testsDir), closureInputs(asmUntagged, testsDir)); len(got) != 1 ||
		!strings.Contains(got[0], "leaves out m/internal/s /r/internal/s/asm_amd64.s") {
		t.Errorf("an assembly file the tag leaves out: %q", got)
	}
	testTagged := slices.Clone(clean)
	testTagged[2].TestGoFiles = []string{"a_test.go", "bom_x_test.go"}
	if got := compareClosureInputs("x", closureInputs(clean, testsDir), closureInputs(testTagged, testsDir)); len(got) != 1 ||
		!strings.Contains(got[0], "/r/tests/bom_x_test.go") {
		t.Errorf("a test file of ./tests/ that only the tag builds: %q", got)
	}
	// An imported package's test files are not built, so they are not inputs.
	importTests := slices.Clone(clean)
	importTests[1].TestGoFiles = []string{"x_test.go", "y_test.go"}
	if got := compareClosureInputs("x", closureInputs(clean, testsDir), closureInputs(importTests, testsDir)); len(got) != 0 {
		t.Errorf("an import's test files counted as inputs: %q", got)
	}
	if got := compareClosureInputs("x", closureInputs(clean, testsDir), closureInputs(clean, testsDir)); len(got) != 0 {
		t.Errorf("the same closure: %q", got)
	}

	// The listing go list prints, a stream of objects, is read whole.
	pkgs, err := decodeClosure(`{"ImportPath": "m/tests", "Dir": "/r/tests", "TestGoFiles": ["a_test.go"]}
{"ImportPath": "os", "Standard": true}
`)
	if err != nil || len(pkgs) != 2 || pkgs[0].TestGoFiles[0] != "a_test.go" || !pkgs[1].Standard {
		t.Errorf("decodeClosure = %+v, %v", pkgs, err)
	}
	if _, err := decodeClosure("{"); err == nil {
		t.Error("decodeClosure accepted a broken listing")
	}
}
