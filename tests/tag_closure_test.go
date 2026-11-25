package tests

// This file holds the check that lets ./tests/ run once, in make test, and not
// again in the tagged runs of make test-canary and make test-ownerchannel. That
// is sound only while the package builds the same under every tag set, which
// holds while no file of its test closure, its own test files and the files of
// every package they import, has a build constraint that names the canary tag
// or the owner channel's tag. The tagged runs' grep reads only the folders it finds, so it
// would not notice a tagged file in a package that ./tests/ imports; this check
// does, under each of the four tag sets the targets build with, so that any
// return of ./tests/ to a tagged run is a deliberate change.
//
// It reads the constraints of the Go files and of the other sources (assembly,
// C and the like, which a constraint selects as it does a Go file): only their
// headers, each once, and only the sources the go command reads a constraint
// from on this host (no object, and no file whose name leaves it out for the
// host's system or architecture, which no tag set can select). And it compares
// the sources each tag set selects with those the untagged build selects, so
// that a selection that differs is found even where reading a constraint would
// miss it.
//
// The checks of the tags in this package run go list with the tags they need
// and do not depend on the tags the package itself is built with.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/build"
	"go/build/constraint"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
// with the canary tag (make test-ownerchannel). The untagged one is first:
// the others are compared with it.
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

// closureHeaders reads the header of each source the check looks at, once
// for all four listings: the lines up to the first that is neither blank nor
// a comment, which is all the go command reads a constraint from. It counts
// what it reads.
type closureHeaders struct {
	// open opens a file; reads reports whether the go command, on this
	// host, reads the file's text for constraints (hostReadsText).
	open  func(path string) (io.ReadCloser, error)
	reads func(dir, name string) bool
	cache map[string]closureHeader
	// bytes is what was read from the files, files how many were opened.
	bytes int64
	files int
}

type closureHeader struct {
	text string
	err  error
}

func newClosureHeaders() *closureHeaders {
	return &closureHeaders{
		open:  func(path string) (io.ReadCloser, error) { return os.Open(path) },
		reads: hostReadsText,
		cache: map[string]closureHeader{},
	}
}

// header is the header of the file at path, read once.
func (h *closureHeaders) header(path string) (string, error) {
	if c, ok := h.cache[path]; ok {
		return c.text, c.err
	}
	text, err := h.read(path)
	h.cache[path] = closureHeader{text, err}
	return text, err
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n *int64
}

func (c countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	*c.n += int64(n)
	return n, err
}

// read reads the header of the file at path, with the line that ends it,
// which headerConstraintsGo stops at.
func (h *closureHeaders) read(path string) (string, error) {
	f, err := h.open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h.files++
	br := bufio.NewReaderSize(countingReader{r: f, n: &h.bytes}, 512)
	var b strings.Builder
	inBlock := false
	for first := true; ; first = false {
		line, err := br.ReadString('\n')
		b.WriteString(line)
		if first {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		if headerEnds(line, &inBlock) || errors.Is(err, io.EOF) {
			return b.String(), nil
		}
		if err != nil {
			return "", err
		}
	}
}

// headerEnds reports whether line ends a file's header, by the rules of
// headerConstraintsGo: a line that is not blank and not a comment, or code
// after a block comment. inBlock carries an open block comment from line to
// line.
func headerEnds(line string, inBlock *bool) bool {
	s := strings.TrimSpace(line)
	if !*inBlock && s == "" {
		return false
	}
	if *inBlock || strings.HasPrefix(s, "/*") {
		rest, open := afterBlockComments(s, *inBlock)
		*inBlock = open
		return !open && rest != "" && !strings.HasPrefix(rest, "//")
	}
	return !strings.HasPrefix(s, "//")
}

// errNotOpened is the answer of the probe's OpenFile in hostReadsText.
var errNotOpened = errors.New("probe: not opened")

// hostReadsText reports whether the go command, on this host, reads the text
// of the file name in dir for its constraints: a source of a kind it reads
// (Go, C, assembly and the like; not an object such as a .syso, which is
// selected by its name alone) whose name does not leave it out for the host's
// system or architecture. go/build decides it by its own rules: MatchFile
// asks to open the file only then. A file it does not read is selected or
// left out by its name alone, the same way under every tag set.
func hostReadsText(dir, name string) bool {
	ctx := build.Default
	opened := false
	ctx.OpenFile = func(string) (io.ReadCloser, error) {
		opened = true
		return nil, errNotOpened
	}
	ctx.MatchFile(dir, name)
	return opened
}

// namesEitherTag reports whether e names the canary tag or the owner
// channel's.
func namesEitherTag(e constraint.Expr) bool { return exprNames(e, canaryTag) || exprNames(e, ownerTag) }

// checkTestsClosure reports every file of the test closure of the package in
// testsDir whose build constraint names either tag. A package of the standard
// library is not read. Of every other package it reads the files built, Go and
// other sources, and the files a constraint leaves out, Go and other (a file
// left out under one tag set is built under another), and of the package in
// testsDir also its test files. The test files of the other packages are not
// built and are not read. Only the files whose text the go command reads on
// this host are read (headers.reads), each header once.
//
// A file is reported when a constraint the go command applies names a tag.
// A Go file is also reported when a constraint line in its header names a
// tag where the go command does not apply it (a plus build line with no
// blank line after it): vet and gofmt treat such a line as a mistake, and the
// placement checks of the tags read Go files the same way. A source that is
// not Go has no such check, and the go command's placement rule is all
// there is: a plus build line counts only with a blank line after it, and the
// first line that is not a comment (an #include, say) ends the header, so a
// line it does not apply is not reported. A closure without the test files
// of the package in testsDir is a problem, so that the check cannot pass by
// reading nothing.
func checkTestsClosure(tags string, pkgs []closurePackage, testsDir string, headers *closureHeaders) []string {
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
			if !headers.reads(filepath.Dir(f), filepath.Base(f)) {
				continue
			}
			text, err := headers.header(f)
			if err != nil {
				problems = append(problems, fmt.Sprintf("tags %q: cannot read %s of %s: %v", tags, f, p.ImportPath, err))
				continue
			}
			all, applied, _, bad := headerConstraintsGo(text)
			named := false
			for _, e := range applied {
				if namesEitherTag(e) {
					named = true
					problems = append(problems, fmt.Sprintf("tags %q: the test closure of ./tests/ holds %s (package %s), whose build constraint %q names a tag, so ./tests/ no longer builds the same under every tag set and must run in the tagged runs again", tags, f, p.ImportPath, e.String()))
				}
			}
			if strings.HasSuffix(f, ".go") && !named {
				for _, e := range all {
					if namesEitherTag(e) && !slices.Contains(applied, e) {
						problems = append(problems, fmt.Sprintf("tags %q: %s (package %s) has a constraint line %q that names a tag where the go command does not apply it; a Go file of the test closure of ./tests/ must not hold one", tags, f, p.ImportPath, e.String()))
					}
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

// closureInputs are the inputs the test build of the package in testsDir
// selects, one entry for each, its package first: the Go files and the other
// sources of every package of the closure, the standard library's too, and the
// test files of the package in testsDir, which alone are built.
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

// compareClosureInputs reports every input that the build under tags
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

// closureGuard is the whole check: list gives the listing (go list with
// closureListFlags) of the test closure of the package in testsDir under the
// tags given, and the guard checks it under every tag set of closureTagSets
// and compares each tagged one's inputs with the untagged one's. headers
// reads each source once for all four.
func closureGuard(list func(tags string) (string, error), testsDir string, headers *closureHeaders) []string {
	var problems, untagged []string
	for _, tags := range closureTagSets {
		listing, err := list(tags)
		if err != nil {
			problems = append(problems, fmt.Sprintf("tags %q: %v", tags, err))
			continue
		}
		pkgs, err := decodeClosure(listing)
		if err != nil {
			problems = append(problems, fmt.Sprintf("tags %q: %v", tags, err))
			continue
		}
		problems = append(problems, checkTestsClosure(tags, pkgs, testsDir, headers)...)
		in := closureInputs(pkgs, testsDir)
		if tags == "" {
			if len(in) == 0 {
				return append(problems, "the untagged build of ./tests/ selects no source")
			}
			untagged = in
			continue
		}
		problems = append(problems, compareClosureInputs(tags, untagged, in)...)
	}
	return problems
}

// TestCanaryTagTestsClosure lists the test closure of ./tests/ under each tag
// set the targets build with, finds no file whose constraint names a tag, and
// finds that every tag set selects the sources the untagged build does.
func TestCanaryTagTestsClosure(t *testing.T) {
	headers := newClosureHeaders()
	list := func(tags string) (string, error) { return ownerGoList(t, tags, closureListFlags, "./tests/"), nil }
	reportCanaryProblems(t, closureGuard(list, filepath.Join(root(t), "tests"), headers))
	t.Logf("the guard read %d bytes, the headers of %d files, for the four listings", headers.bytes, headers.files)
}

// The guard on small modules of its own, listed by the real go command,
// under the four tag sets: an
// assembly file whose plus build line names the tag right above an include,
// where the go command does not apply it, which it must accept, after reading
// it; and the same file with a blank line after the plus build line, which it
// must reject.
func TestCanaryTagTestsClosureFixtureModules(t *testing.T) {
	tool := canaryGoTool(t)
	const goMod = "module review\n\ngo 1.21\n"
	const testFile = "package tests\n\nimport (\n\t\"testing\"\n\n\t\"review/dep\"\n)\n\n" +
		"func TestValue(t *testing.T) {\n\tif v := dep.Value(); v != 0 {\n\t\tt.Fatalf(\"dependency returned %d, want 0\", v)\n\t}\n}\n"
	plainDep := "package dep\n\n// Value is 0.\nfunc Value() int { return 0 }\n"
	plusTag := "// +build " + canaryTag + "\n"
	include := "#include \"textflag.h\"\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		// want are the substrings, one for each problem, in order; read is
		// a file the guard must have read.
		want []string
		read string
	}{
		{"a plus build line right above an include", map[string]string{"dep/dep.go": plainDep, "dep/value.s": plusTag + include},
			nil, "dep/value.s"},
		{"a plus build line with a blank line after it", map[string]string{"dep/dep.go": plainDep, "dep/value.s": plusTag + "\n" + include},
			[]string{
				fmt.Sprintf("tags %q: the test closure of ./tests/ holds", ""),
				fmt.Sprintf("tags %q: the test closure of ./tests/ holds", canaryTag),
				fmt.Sprintf("tags %q: the test build of ./tests/ selects review/dep", canaryTag),
				fmt.Sprintf("tags %q: the test closure of ./tests/ holds", ownerTag),
				fmt.Sprintf("tags %q: the test closure of ./tests/ holds", ownerTag+","+canaryTag),
				fmt.Sprintf("tags %q: the test build of ./tests/ selects review/dep", ownerTag+","+canaryTag),
			}, "dep/value.s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{"go.mod": goMod, "tests/dep_test.go": testFile}
			for name, data := range tc.files {
				files[name] = data
			}
			for name, data := range files {
				p := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			list := func(tags string) (string, error) {
				args := []string{"list"}
				if tags != "" {
					args = append(args, "-tags", tags)
				}
				cmd := exec.Command(tool, append(append(args, closureListFlags...), "./tests/")...)
				cmd.Dir = dir
				cmd.Env = canaryGoEnv()
				var stderr bytes.Buffer
				cmd.Stderr = &stderr
				out, err := cmd.Output()
				if err != nil {
					return "", fmt.Errorf("go list: %v\n%s", err, stderr.String())
				}
				return string(out), nil
			}
			headers := newClosureHeaders()
			got := closureGuard(list, filepath.Join(dir, "tests"), headers)
			if len(got) != len(tc.want) {
				t.Fatalf("want %d problems, got %d:\n%s", len(tc.want), len(got), strings.Join(got, "\n"))
			}
			for i, want := range tc.want {
				if !strings.Contains(got[i], want) {
					t.Errorf("problem %d is %q, want one with %q", i, got[i], want)
				}
			}
			if _, ok := headers.cache[filepath.Join(dir, tc.read)]; !ok {
				t.Errorf("the guard did not read %s", tc.read)
			}
		})
	}
}

func TestCanaryTagTestsClosureCheck(t *testing.T) {
	const testsDir = "/r/tests"
	files := map[string]string{
		"/r/tests/a_test.go":             "package tests\n",
		"/r/tests/b_test.go":             "package tests_test\n",
		"/r/internal/a/a.go":             "package a\n",
		"/r/internal/a/linux.go":         hdrPlain + "\n\npackage a\n",
		"/r/internal/a/a_test.go":        hdrTagged + "\n\npackage a\n",
		"/r/internal/a/x_test.go":        ownHdrTagged + "\n\npackage a\n",
		"/r/internal/a/comment.go":       "// the ataniq_canary tag is not a constraint\n\npackage a\n",
		"/r/internal/t/canary.go":        hdrTagged + "\n\npackage t\n",
		"/r/internal/t/release.go":       hdrNegated + "\n\npackage t\n",
		"/r/internal/t/owner.go":         ownHdrTagged + "\n\npackage t\n",
		"/r/internal/t/plus.go":          hdrPlusTagged + "\n\npackage t\n",
		"/r/internal/t/misplaced.go":     hdrPlusTagged + "\npackage t\n",
		"/r/internal/t/pair.go":          hdrTagged + "\n" + hdrPlusTagged + "\n\npackage t\n",
		"/r/internal/t/bad.go":           "//go:build linux && (ataniq_canary\n\npackage t\n",
		"/r/internal/t/tagged_other.go":  hdrTagged + "\n\npackage t\n",
		"/r/internal/t/release_other.go": hdrNegated + "\n\npackage t\n",
		"/r/tests/canary_x_test.go":      hdrTagged + "\n\npackage tests\n",
		"/r/tests/release_x_test.go":     ownHdrNegated + "\n\npackage tests\n",
		"/r/tests/bom_x_test.go":         "\uFEFF" + hdrTagged + "\n\npackage tests\n",
		"/r/internal/s/asm_amd64.s":      ownHdrTagged + "\n\n#include \"textflag.h\"\n",
		"/r/internal/s/asm_other.s":      hdrNegated + "\n\n#include \"textflag.h\"\n",
		"/r/internal/s/plain_amd64.s":    "#include \"textflag.h\"\n\n" + ownHdrTagged + "\n",
		"/r/internal/s/include_amd64.s":  hdrPlusTagged + "\n#include \"textflag.h\"\n",
		"/r/internal/s/block_amd64.s":    "/* a block\n" + hdrTagged + "\n*/ TEXT x(SB),0,$0\n",
		"/r/internal/s/c.c":              hdrTagged + "\n\nint x;\n",
		"/r/internal/s/blob.syso":        hdrTagged + "\n",
		"/r/internal/s/tagged_other.s":   hdrTagged + "\n\n#include \"textflag.h\"\n",
		"/r/internal/s/large_amd64.s":    "// a large source\n\n#include \"textflag.h\"\n" + strings.Repeat("TEXT x(SB),0,$0\n", 4096),
		"/cache/testmain.go":             "package main\n",
		"/std/os/os.go":                  hdrTagged + "\n\npackage os\n",
	}
	newHeaders := func() *closureHeaders {
		return &closureHeaders{
			open: func(name string) (io.ReadCloser, error) {
				data, ok := files[name]
				if !ok {
					return nil, os.ErrNotExist
				}
				return io.NopCloser(strings.NewReader(data)), nil
			},
			// This host leaves out the names with "_other", and reads no
			// object.
			reads: func(_, name string) bool {
				return !strings.Contains(name, "_other") && !strings.HasSuffix(name, ".syso")
			},
			cache: map[string]closureHeader{},
		}
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
		{"a misplaced plus build line in a Go file", with(closurePackage{ImportPath: "m/internal/t", Dir: "/r/internal/t", GoFiles: []string{"misplaced.go"}}), []string{"/r/internal/t/misplaced.go (package m/internal/t) has a constraint line"}},
		{"a go:build line and its plus build pair, reported once", with(closurePackage{ImportPath: "m/internal/t", Dir: "/r/internal/t", IgnoredGoFiles: []string{"pair.go"}}), []string{"/r/internal/t/pair.go (package m/internal/t), whose build constraint"}},
		{"a constraint line that does not parse", with(closurePackage{ImportPath: "m/internal/t", Dir: "/r/internal/t", IgnoredGoFiles: []string{"bad.go"}}), []string{"/r/internal/t/bad.go"}},
		{"a tagged file the host leaves out by its name", with(closurePackage{ImportPath: "m/internal/t", Dir: "/r/internal/t", IgnoredGoFiles: []string{"tagged_other.go", "release_other.go"}}), nil},
		{"a tagged test file of ./tests/", with(closurePackage{ImportPath: "m/tests", Dir: testsDir, IgnoredGoFiles: []string{"canary_x_test.go"}}), []string{"/r/tests/canary_x_test.go"}},
		{"a release counterpart among the tests of ./tests/", with(closurePackage{ImportPath: "m/tests_test [m/tests.test]", Dir: testsDir, XTestGoFiles: []string{"release_x_test.go"}}), []string{"/r/tests/release_x_test.go"}},
		{"a byte order mark before a tagged test file of ./tests/", with(closurePackage{ImportPath: "m/tests", Dir: testsDir, IgnoredGoFiles: []string{"bom_x_test.go"}}), []string{"/r/tests/bom_x_test.go"}},
		{"a tagged assembly file of an import, selected", with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", SFiles: []string{"asm_amd64.s"}}), []string{"/r/internal/s/asm_amd64.s"}},
		{"a tagged assembly file of an import, left out", with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", IgnoredOtherFiles: []string{"asm_amd64.s", "asm_other.s"}}), []string{"/r/internal/s/asm_amd64.s"}},
		{"an untagged assembly file whose tag is below its code", with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", SFiles: []string{"plain_amd64.s"}}), nil},
		{"an assembly file whose plus build line is right above an include", with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", SFiles: []string{"include_amd64.s"}}), nil},
		{"an assembly file whose tag is in a block comment before code", with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", SFiles: []string{"block_amd64.s"}}), nil},
		{"a tagged C file of an import", with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", CFiles: []string{"c.c"}}), []string{"/r/internal/s/c.c"}},
		{"an object and a tagged source the host leaves out", with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", SysoFiles: []string{"blob.syso"}, IgnoredOtherFiles: []string{"blob.syso", "tagged_other.s"}}), nil},
		{"a file that cannot be read", with(closurePackage{ImportPath: "m/internal/gone", Dir: "/r/internal/gone", GoFiles: []string{"gone.go"}}), []string{"/r/internal/gone/gone.go"}},
		{"no test file of ./tests/", clean[:2], []string{"no test file of " + testsDir}},
		{"an empty listing", nil, []string{"no test file of " + testsDir}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			headers := newHeaders()
			got := checkTestsClosure("x", tc.pkgs, testsDir, headers)
			if len(got) != len(tc.bad) {
				t.Fatalf("want %d problems naming %q, got %q", len(tc.bad), tc.bad, got)
			}
			for i, want := range tc.bad {
				if !strings.Contains(got[i], want) {
					t.Errorf("problem %d is %q, want one naming %s", i, got[i], want)
				}
			}
			for name := range headers.cache {
				if strings.Contains(name, "_other") || strings.HasSuffix(name, ".syso") {
					t.Errorf("the check read %s, which the host does not read", name)
				}
			}
		})
	}

	// Each header is read once for all the listings, and only the header: a
	// large source costs its first lines.
	headers := newHeaders()
	large := with(closurePackage{ImportPath: "m/internal/s", Dir: "/r/internal/s", SFiles: []string{"large_amd64.s", "asm_other.s"}, CFiles: []string{"c.c"}})
	for _, tags := range closureTagSets {
		checkTestsClosure(tags, large, testsDir, headers)
	}
	// a.go, comment.go, linux.go, a_test.go and b_test.go of ./tests/, the
	// test main, large_amd64.s and c.c: asm_other.s is not read.
	if want := 8; headers.files != want {
		t.Errorf("the check opened %d files for four listings, want each of the %d it reads once", headers.files, want)
	}
	if size := int64(len(files["/r/internal/s/large_amd64.s"])); headers.bytes >= size/4 {
		t.Errorf("the check read %d bytes, a quarter of the large source alone (%d) or more", headers.bytes, size)
	}

	// A tag set that selects other inputs than the untagged build: a tagged
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

// hostReadsText follows the go command: it reads Go and other sources whose
// name the host's system and architecture select, and no object, and no file
// of a kind the go command does not build.
func TestCanaryTagTestsClosureHostReads(t *testing.T) {
	other := "windows"
	if runtime.GOOS == other {
		other = "linux"
	}
	for name, want := range map[string]bool{
		"a.go": true, "a_test.go": true, "a.s": true, "a.c": true, "a.h": true,
		"a_" + runtime.GOOS + ".go": true, "a_" + runtime.GOARCH + ".s": true,
		"a_" + other + ".go": false, "a_" + other + ".s": false,
		"a.syso": false, "a.txt": false, "_a.go": false,
	} {
		if got := hostReadsText("/r/x", name); got != want {
			t.Errorf("hostReadsText(%q) = %v, want %v", name, got, want)
		}
	}
}
