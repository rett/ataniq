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
	ImportPath     string
	Dir            string
	Standard       bool
	GoFiles        []string
	CgoFiles       []string
	IgnoredGoFiles []string
	TestGoFiles    []string
	XTestGoFiles   []string
}

// closureListFlags list the test closure of a package with the fields of
// closurePackage.
var closureListFlags = []string{"-deps", "-test", "-json=ImportPath,Dir,Standard,GoFiles,CgoFiles,IgnoredGoFiles,TestGoFiles,XTestGoFiles"}

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
// library is not read. Of every other package it reads the files built and the
// files a constraint leaves out (a file left out under one tag set is built
// under another), and of the package in testsDir also its test files. The test
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
		files := slices.Concat(p.GoFiles, p.CgoFiles)
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

// TestCanaryTagTestsClosure lists the test closure of ./tests/ under each tag
// set the targets build with and finds no file whose constraint names a tag.
func TestCanaryTagTestsClosure(t *testing.T) {
	testsDir := filepath.Join(root(t), "tests")
	for _, tags := range closureTagSets {
		pkgs, err := decodeClosure(ownerGoList(t, tags, closureListFlags, "./tests/"))
		if err != nil {
			t.Fatalf("tags %q: %v", tags, err)
		}
		reportCanaryProblems(t, checkTestsClosure(tags, pkgs, testsDir, os.ReadFile))
	}
}

func TestCanaryTagTestsClosureCheck(t *testing.T) {
	const testsDir = "/r/tests"
	files := map[string]string{
		"/r/tests/a_test.go":         "package tests\n",
		"/r/tests/b_test.go":         "package tests_test\n",
		"/r/internal/a/a.go":         "package a\n",
		"/r/internal/a/linux.go":     hdrPlain + "\n\npackage a\n",
		"/r/internal/a/a_test.go":    hdrTagged + "\n\npackage a\n",
		"/r/internal/a/x_test.go":    ownHdrTagged + "\n\npackage a\n",
		"/r/internal/a/comment.go":   "// the ataniq_canary tag is not a constraint\n\npackage a\n",
		"/r/internal/t/canary.go":    hdrTagged + "\n\npackage t\n",
		"/r/internal/t/release.go":   hdrNegated + "\n\npackage t\n",
		"/r/internal/t/owner.go":     ownHdrTagged + "\n\npackage t\n",
		"/r/internal/t/plus.go":      hdrPlusTagged + "\n\npackage t\n",
		"/r/internal/t/bad.go":       "//go:build linux && (ataniq_canary\n\npackage t\n",
		"/r/tests/canary_x_test.go":  hdrTagged + "\n\npackage tests\n",
		"/r/tests/release_x_test.go": ownHdrNegated + "\n\npackage tests\n",
		"/cache/testmain.go":         "package main\n",
		"/std/os/os.go":              hdrTagged + "\n\npackage os\n",
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
