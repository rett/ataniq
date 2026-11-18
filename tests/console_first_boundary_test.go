package tests

import (
	"go/build/constraint"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// This file is a tripwire for the import rules of the console-first build:
// which of the new packages may import which, and what the console and the
// commands must not reach. It reads the direct imports of the Go files in the
// repository with go/parser, so it needs no network, no module cache and no
// go list, and a rule about a package that is not written yet simply finds
// nothing to object to.
//
// Each rule is a function from a set of files to a list of findings. It is
// called once on the repository and again on small samples that break it, so
// that a rule which can no longer trip is noticed.

// boundaryModule is the module path of this repository. Every package path in
// the rules below is written without it.
const boundaryModule = "ataniq"

// boundaryTag is the build tag of the owner channel. A file that cannot be
// built without it is skipped by the import rules here: a later tag guard
// checks those files, with the rules that hold when the tag is set. A file
// whose build line names the tag and can still be built without it, such as
// one that asks for the tag to be off, is read like any other: it is part of
// the build the rules protect.
const boundaryTag = "ataniq_ownerchannel"

// Short names for the packages the rules speak of, as paths below the module.
const (
	pkgApproval     = "internal/approval"
	pkgRecord       = "internal/approval/record"
	pkgOwnerWire    = "internal/approval/ownerwire"
	pkgLimits       = "internal/approval/limits"
	pkgPresence     = "internal/approval/presence"
	pkgCard         = "internal/approval/card"
	pkgMergeBuild   = "internal/approval/mergebuild"
	pkgDoor         = "internal/approval/door"
	pkgDoorTest     = "internal/approval/door/doortest"
	pkgOwnerJournal = "internal/approval/ownerjournal"
	pkgOwnerChannel = "internal/approval/ownerchannel"
	pkgDemo         = "internal/approval/demo"
	pkgClientAPI    = "internal/clientapi"
	pkgConsoleView  = "internal/consoleclient"
	pkgLeadAttach   = "internal/leadattach"
	pkgLeadText     = "internal/exchange/leadtext"
	pkgGitEnv       = "internal/exchange/gitenv"
	pkgBoundedExec  = "internal/exchange/boundedexec"
	pkgConsole      = "internal/terminalconsole"
	pkgSessionPane  = "internal/terminalconsole/sessionpane"
	pkgSessionPlay  = "internal/terminalconsole/sessionreplay"
	pkgPager        = "internal/terminalconsole/pager"
	pkgArrival      = "internal/terminalconsole/arrival"
	pkgApprovals    = "internal/terminalconsole/approvals"
	pkgLimitView    = "internal/terminalconsole/limitview"
	pkgConsoleCmd   = "cmd/ataniq-console"
)

// boundaryTable says, for each package of the build, which packages of this
// repository its non-test files may import. A package with an empty row
// imports nothing of this repository. A package that has no row is not checked
// by the table rule. The stand-in for the door (doortest) may also import the
// door it stands in for.
var boundaryTable = map[string][]string{
	pkgRecord:       {},
	pkgOwnerWire:    {pkgRecord},
	pkgLimits:       {},
	pkgPresence:     {},
	pkgCard:         {pkgRecord, pkgLimits, pkgLeadText},
	pkgMergeBuild:   {pkgRecord, pkgGitEnv, pkgBoundedExec},
	pkgDoor:         {pkgRecord},
	pkgDoorTest:     {pkgRecord, pkgDoor},
	pkgOwnerJournal: {pkgRecord},
	pkgOwnerChannel: {pkgRecord, pkgCard, pkgLimits, pkgPresence, pkgDoor, pkgOwnerJournal, pkgOwnerWire},
	pkgDemo:         {pkgRecord, pkgCard, pkgLimits, pkgPresence, pkgLeadText},
	pkgConsoleView:  {pkgClientAPI},
	pkgPager:        {pkgLeadText},
	pkgArrival:      {pkgPresence},
	pkgApprovals:    {pkgCard, pkgRecord, pkgLimits, pkgLeadText, pkgPager},
	pkgLimitView:    {pkgLimits},
	pkgConsole: {
		pkgSessionPane, pkgSessionPlay,
		pkgPager, pkgArrival, pkgApprovals, pkgLimitView, pkgCard, pkgRecord, pkgLimits, pkgPresence, pkgLeadText,
	},
	pkgConsoleCmd: {pkgConsole, pkgLeadAttach, pkgConsoleView, pkgDemo, pkgApprovals},
}

// boundaryPure lists the packages that, beyond their row of the table, import
// only the standard library.
var boundaryPure = []string{pkgRecord, pkgCard, pkgLimits, pkgPresence, pkgOwnerWire}

// boundaryActing lists the packages that act for the owner channel or stand in
// for the part that does: the door, its stand-in, the service, the journal and
// the merge builder. No command imports one of them.
var boundaryActing = []string{pkgDoor, pkgDoorTest, pkgOwnerChannel, pkgOwnerJournal, pkgMergeBuild}

// boundaryWords is a small list of model and vendor words that no Go file
// under internal/approval may hold. Remove this list, the rule that reads it
// and its tests when a repository-wide word test exists: the new packages then
// come under that test.
var boundaryWords = []string{
	"claude", "codex", "anthropic", "openai", "gemini", "gpt", "sonnet", "opus", "haiku", "fable",
}

// boundaryFile is one Go file as the rules see it.
type boundaryFile struct {
	path    string   // relative to the repository root, with slashes
	test    bool     // the name ends in _test.go
	tagged  bool     // the build lines need boundaryTag: without it the file is not built
	imports []string // the direct imports, unquoted
	text    string   // the whole file
}

// pkg is the folder of the file, which is its package path below the module.
func (f boundaryFile) pkg() string { return path.Dir(f.path) }

// imported reports whether the import rules read the file: they leave out test
// files and files that need the owner channel's tag.
func (f boundaryFile) imported() bool { return !f.test && !f.tagged }

// needsBoundaryTag reports whether a build line cannot be met while the owner
// channel's tag is off. It is false for a line that does not name the tag.
// For a line that does, it tries the line with the tag off and every setting
// of the other tags the line names; a build line names only a few.
func needsBoundaryTag(expr constraint.Expr) bool {
	named := false
	var others []string
	seen := map[string]bool{}
	var collect func(constraint.Expr)
	collect = func(x constraint.Expr) {
		switch x := x.(type) {
		case *constraint.TagExpr:
			if x.Tag == boundaryTag {
				named = true
			} else if !seen[x.Tag] {
				seen[x.Tag] = true
				others = append(others, x.Tag)
			}
		case *constraint.NotExpr:
			collect(x.X)
		case *constraint.AndExpr:
			collect(x.X)
			collect(x.Y)
		case *constraint.OrExpr:
			collect(x.X)
			collect(x.Y)
		}
	}
	collect(expr)
	if !named || len(others) > 16 {
		// A line with that many tags is not tried: the file is read.
		return false
	}
	for set := 0; set < 1<<len(others); set++ {
		on := map[string]bool{}
		for i, tag := range others {
			on[tag] = set&(1<<i) != 0
		}
		if expr.Eval(func(tag string) bool { return on[tag] }) {
			return false
		}
	}
	return true
}

// parseBoundaryFile reads the imports and the build lines of one file. Only
// the part of the file up to the end of its imports has to be valid Go.
func parseBoundaryFile(name, src string) (boundaryFile, error) {
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, name, src, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return boundaryFile{}, err
	}
	f := boundaryFile{path: name, test: strings.HasSuffix(name, "_test.go"), text: src}
	for _, imp := range parsed.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return boundaryFile{}, err
		}
		f.imports = append(f.imports, p)
	}
	// A build line stands above the package clause. A //go:build line decides
	// alone, as it does for the compiler; without one, every // +build line
	// has to be met, so one of them that needs the tag is enough.
	goBuild := false
	for _, group := range parsed.Comments {
		if group.Pos() > parsed.Package {
			break
		}
		for _, c := range group.List {
			if !constraint.IsGoBuild(c.Text) && !constraint.IsPlusBuild(c.Text) {
				continue
			}
			expr, err := constraint.Parse(c.Text)
			if err != nil {
				// The compiler does not take the line as a build line.
				continue
			}
			if constraint.IsGoBuild(c.Text) {
				goBuild = true
				f.tagged = needsBoundaryTag(expr)
			} else if !goBuild {
				f.tagged = f.tagged || needsBoundaryTag(expr)
			}
		}
	}
	return f, nil
}

// loadBoundaryFiles reads every Go file below dir, test files and files of any
// build line included. It leaves out hidden folders, testdata, vendored and
// third-party code, and the build output folder at the top.
func loadBoundaryFiles(t *testing.T, dir string) []boundaryFile {
	t.Helper()
	var files []boundaryFile
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		name := d.Name()
		if d.IsDir() {
			skip := strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") ||
				name == "testdata" || name == "vendor" || rel == "third_party" || rel == "build"
			if skip && rel != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(name, ".go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f, err := parseBoundaryFile(rel, string(src))
		if err != nil {
			return err
		}
		files = append(files, f)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// repositoryBoundaryFiles loads the repository. It stops the test when the
// module is not the one the rules are written for or when the console's own
// files are not found, since every rule would then pass without reading
// anything.
func repositoryBoundaryFiles(t *testing.T) []boundaryFile {
	t.Helper()
	dir := root(t)
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if line, _, _ := strings.Cut(string(mod), "\n"); strings.TrimSpace(line) != "module "+boundaryModule {
		t.Fatalf("go.mod starts with %q, and the rules are written for module %s", line, boundaryModule)
	}
	files := loadBoundaryFiles(t, dir)
	seen := map[string]bool{}
	for _, f := range files {
		if f.imported() {
			seen[f.pkg()] = true
		}
	}
	for _, pkg := range []string{pkgConsole, pkgConsoleCmd} {
		if !seen[pkg] {
			t.Fatalf("no Go file read in %s", pkg)
		}
	}
	return files
}

// boundarySample makes a file set from sources held in the test.
func boundarySample(t *testing.T, sources map[string]string) []boundaryFile {
	t.Helper()
	names := make([]string, 0, len(sources))
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	var files []boundaryFile
	for _, name := range names {
		f, err := parseBoundaryFile(name, sources[name])
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	return files
}

// boundarySource writes a small Go file that imports the given paths. A path
// that starts with a slash is taken below the module.
func boundarySource(imports ...string) string {
	var b strings.Builder
	b.WriteString("package sample\n\nimport (\n")
	for _, imp := range imports {
		b.WriteString("\t" + strconv.Quote(boundaryImport(imp)) + "\n")
	}
	b.WriteString(")\n")
	return b.String()
}

// boundaryImport turns "/internal/x" into the import path of that package of
// this repository, and leaves any other path as it is.
func boundaryImport(p string) string {
	if strings.HasPrefix(p, "/") {
		return boundaryModule + p
	}
	return p
}

// inModule returns the path of an import below the module, and whether the
// import is of this repository at all.
func inModule(imp string) (string, bool) {
	if imp == boundaryModule {
		return ".", true
	}
	if rest, ok := strings.CutPrefix(imp, boundaryModule+"/"); ok {
		return rest, true
	}
	return "", false
}

// within reports whether p is the folder dir or lies below it.
func within(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// standardLibrary reports whether an import path is of the standard library:
// its first element has no dot, and it is neither this module nor cgo.
func standardLibrary(imp string) bool {
	if _, ok := inModule(imp); ok || imp == "C" {
		return false
	}
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

// ruleTable is rule 1: a package with a row in the table imports, of this
// repository, only what its row names.
func ruleTable(files []boundaryFile) []string {
	var found []string
	for _, f := range files {
		row, ok := boundaryTable[f.pkg()]
		if !ok || !f.imported() {
			continue
		}
		for _, imp := range f.imports {
			p, ours := inModule(imp)
			if !ours {
				continue
			}
			allowed := false
			for _, r := range row {
				allowed = allowed || r == p
			}
			if !allowed {
				found = append(found, f.path+" imports "+imp+", which is not in the row of "+f.pkg())
			}
		}
	}
	return found
}

// rulePure is rule 2: the pure packages import, outside this repository, only
// the standard library.
func rulePure(files []boundaryFile) []string {
	var found []string
	for _, f := range files {
		pure := false
		for _, pkg := range boundaryPure {
			pure = pure || f.pkg() == pkg
		}
		if !pure || !f.imported() {
			continue
		}
		for _, imp := range f.imports {
			if _, ours := inModule(imp); !ours && !standardLibrary(imp) {
				found = append(found, f.path+" imports "+imp+", which is not in the standard library")
			}
		}
	}
	return found
}

// ruleApprovalHeadless is rule 3: nothing under internal/approval imports the
// terminal toolkit, the console or the client protocol.
func ruleApprovalHeadless(files []boundaryFile) []string {
	var found []string
	for _, f := range files {
		if !within(f.pkg(), pkgApproval) || !f.imported() {
			continue
		}
		for _, imp := range f.imports {
			p, ours := inModule(imp)
			denied := strings.HasPrefix(imp, "charm.land/") || strings.HasPrefix(imp, "github.com/charmbracelet/") ||
				(ours && (within(p, pkgConsole) || within(p, pkgClientAPI)))
			if denied {
				found = append(found, f.path+" imports "+imp+", which nothing under "+pkgApproval+" may import")
			}
		}
	}
	return found
}

// pkgFixtureTool is the command that writes the replay's fixture recordings.
// It is older than the build, is no part of the console, and writes files, so
// rule 4 lets it import os and nothing else of what the rule denies.
const pkgFixtureTool = "internal/terminalconsole/sessionreplay/cmd/genfixtures"

// ruleConsoleReach is rule 4: no package under internal/terminalconsole, the
// console package itself included, imports a package that acts, the sample
// backend, the daemon view, or the network, file and process packages. The
// sample backend and the daemon view reach the console as values that the
// command hands over. The one exception is pkgFixtureTool.
func ruleConsoleReach(files []boundaryFile) []string {
	deniedOurs := append([]string{pkgDemo, pkgConsoleView}, boundaryActing...)
	var found []string
	for _, f := range files {
		if !within(f.pkg(), pkgConsole) || !f.imported() {
			continue
		}
		for _, imp := range f.imports {
			denied := imp == "net" || strings.HasPrefix(imp, "net/") || imp == "os" || imp == "os/exec" || imp == "syscall"
			if p, ours := inModule(imp); ours {
				for _, d := range deniedOurs {
					denied = denied || within(p, d)
				}
			}
			if denied && !(f.pkg() == pkgFixtureTool && imp == "os") {
				found = append(found, f.path+" imports "+imp+", which nothing under "+pkgConsole+" may import")
			}
		}
	}
	return found
}

// ruleStandIn is rule 5: the stand-in for the door is imported by test files
// only, anywhere in the repository, and the sample backend imports neither the
// door nor the stand-in. The file set holds test files as well; the rule
// leaves them alone.
func ruleStandIn(files []boundaryFile) []string {
	var found []string
	for _, f := range files {
		if !f.imported() {
			continue
		}
		for _, imp := range f.imports {
			p, ours := inModule(imp)
			if !ours {
				continue
			}
			if within(p, pkgDoorTest) {
				found = append(found, f.path+" imports "+imp+", which only a test file may import")
			} else if f.pkg() == pkgDemo && within(p, pkgDoor) {
				found = append(found, f.path+" imports "+imp+", which the sample backend may not import")
			}
		}
	}
	return found
}

// ruleCommands is rule 6: no package under cmd imports a package that acts.
func ruleCommands(files []boundaryFile) []string {
	var found []string
	for _, f := range files {
		if !within(f.pkg(), "cmd") || !f.imported() {
			continue
		}
		for _, imp := range f.imports {
			p, ours := inModule(imp)
			if !ours {
				continue
			}
			for _, d := range boundaryActing {
				if within(p, d) {
					found = append(found, f.path+" imports "+imp+", which no command may import")
					break
				}
			}
		}
	}
	return found
}

var boundaryWordPattern = regexp.MustCompile(`(?i)\b(?:` + strings.Join(boundaryWords, "|") + `)\b`)

// ruleWords is rule 7: no Go file under internal/approval holds a word of the
// list, as a whole word and without regard to case. It reads test files and
// files of every build line, and the whole text, not only the imports.
func ruleWords(files []boundaryFile) []string {
	var found []string
	for _, f := range files {
		if !within(f.pkg(), pkgApproval) {
			continue
		}
		for i, line := range strings.Split(f.text, "\n") {
			if word := boundaryWordPattern.FindString(line); word != "" {
				found = append(found, f.path+":"+strconv.Itoa(i+1)+" holds the word "+strings.ToLower(word))
			}
		}
	}
	return found
}

// boundaryRules names the rules in the order of the task that asked for them.
var boundaryRules = []struct {
	name string
	rule func([]boundaryFile) []string
}{
	{"import table", ruleTable},
	{"pure packages", rulePure},
	{"approval without the console", ruleApprovalHeadless},
	{"console reach", ruleConsoleReach},
	{"stand-in", ruleStandIn},
	{"commands", ruleCommands},
	{"words", ruleWords},
}

// TestConsoleFirstBoundaryRepository runs every rule on the repository.
func TestConsoleFirstBoundaryRepository(t *testing.T) {
	files := repositoryBoundaryFiles(t)
	for _, r := range boundaryRules {
		t.Run(r.name, func(t *testing.T) {
			for _, finding := range r.rule(files) {
				t.Error(finding)
			}
		})
	}
}

// TestConsoleFirstBoundaryAbsentPackages shows that every rule passes on a
// tree that holds none of the build's packages, and on no files at all.
func TestConsoleFirstBoundaryAbsentPackages(t *testing.T) {
	other := boundarySample(t, map[string]string{
		"internal/other/other.go":      boundarySource("os", "net", "/internal/clientapi", "example.org/module"),
		"internal/other/other_test.go": boundarySource("os/exec"),
	})
	for _, r := range boundaryRules {
		for name, files := range map[string][]boundaryFile{"no files": nil, "other packages": other} {
			if found := r.rule(files); len(found) != 0 {
				t.Errorf("%s, %s: %v", r.name, name, found)
			}
		}
	}
}

// expectFindings checks that a rule finds exactly the named files, in any
// order, and nothing else. Each want is the path a finding starts with.
func expectFindings(t *testing.T, rule func([]boundaryFile) []string, files []boundaryFile, want ...string) {
	t.Helper()
	found := rule(files)
	left := append([]string(nil), found...)
	for _, w := range want {
		at := -1
		for i, finding := range left {
			if strings.HasPrefix(finding, w+" ") || strings.HasPrefix(finding, w+":") {
				at = i
				break
			}
		}
		if at < 0 {
			t.Errorf("no finding for %s in %q", w, found)
			continue
		}
		left = append(left[:at], left[at+1:]...)
	}
	for _, finding := range left {
		t.Errorf("unexpected finding: %s", finding)
	}
}

// boundaryTagged is a sample file that needs the owner channel's tag and
// breaks every import rule at once wherever it is put.
var boundaryTagged = "//go:build linux && " + boundaryTag + "\n\n" + boundarySource(
	"os", "net", "example.org/module", "charm.land/bubbletea/v2",
	"/internal/clientapi", "/internal/terminalconsole", "/"+pkgDoor, "/"+pkgDoorTest, "/"+pkgOwnerChannel,
)

func TestConsoleFirstBoundaryImportTable(t *testing.T) {
	// Every row of the table trips on an import of this repository that it
	// does not name, and passes on what it does name.
	for pkg, row := range boundaryTable {
		allowed := []string{"strings", "example.org/module"}
		for _, r := range row {
			allowed = append(allowed, "/"+r)
		}
		expectFindings(t, ruleTable, boundarySample(t, map[string]string{
			pkg + "/good.go":      boundarySource(allowed...),
			pkg + "/bad.go":       boundarySource("/internal/journal"),
			pkg + "/root.go":      boundarySource(boundaryModule),
			pkg + "/bad_test.go":  boundarySource("/internal/journal"),
			pkg + "/tagged.go":    boundaryTagged,
			pkg + "/below/sub.go": boundarySource("/internal/journal"),
		}), pkg+"/bad.go", pkg+"/root.go")
	}
	// The three packages that are built first import none of the others.
	for _, pkg := range []string{pkgRecord, pkgLimits, pkgPresence} {
		for _, imp := range []string{pkgRecord, pkgLimits, pkgPresence, pkgLeadText, pkgCard} {
			if imp == pkg {
				continue
			}
			expectFindings(t, ruleTable, boundarySample(t, map[string]string{
				pkg + "/a.go": boundarySource("/" + imp),
			}), pkg+"/a.go")
		}
	}
	expectFindings(t, ruleTable, boundarySample(t, map[string]string{
		pkgOwnerWire + "/a.go":  boundarySource("/"+pkgRecord, "/"+pkgCard),
		pkgCard + "/a.go":       boundarySource("/"+pkgLeadText, "/"+pkgPresence),
		pkgDemo + "/a.go":       boundarySource("/"+pkgCard, "/"+pkgOwnerJournal),
		pkgConsole + "/a.go":    boundarySource("/"+pkgSessionPane, "/"+pkgLeadAttach),
		pkgConsoleCmd + "/a.go": boundarySource("/"+pkgDemo, "/internal/dispatch"),
		pkgPager + "/a.go":      boundarySource("/"+pkgLeadText, "/"+pkgCard),
		pkgDoor + "/a.go":       boundarySource("/"+pkgRecord, "/"+pkgDoorTest),
	}),
		pkgOwnerWire+"/a.go", pkgCard+"/a.go", pkgDemo+"/a.go", pkgConsole+"/a.go", pkgConsoleCmd+"/a.go",
		pkgPager+"/a.go", pkgDoor+"/a.go")
}

func TestConsoleFirstBoundaryPurePackages(t *testing.T) {
	for _, pkg := range boundaryPure {
		expectFindings(t, rulePure, boundarySample(t, map[string]string{
			pkg + "/good.go":     boundarySource("strings", "encoding/json", "crypto/sha256", "/"+pkgRecord),
			pkg + "/module.go":   boundarySource("example.org/module"),
			pkg + "/x.go":        boundarySource("golang.org/x/sys/unix"),
			pkg + "/cgo.go":      boundarySource("C"),
			pkg + "/bad_test.go": boundarySource("example.org/module"),
			pkg + "/tagged.go":   boundaryTagged,
		}), pkg+"/module.go", pkg+"/x.go", pkg+"/cgo.go")
	}
	// A package that is not in the list may import a module.
	expectFindings(t, rulePure, boundarySample(t, map[string]string{
		pkgMergeBuild + "/a.go": boundarySource("example.org/module"),
	}))
}

func TestConsoleFirstBoundaryApprovalWithoutTheConsole(t *testing.T) {
	expectFindings(t, ruleApprovalHeadless, boundarySample(t, map[string]string{
		pkgCard + "/good.go":         boundarySource("strings", "/"+pkgLeadText, "/internal/clientapix", "example.org/module"),
		pkgCard + "/tea.go":          boundarySource("charm.land/bubbletea/v2"),
		pkgDemo + "/ansi.go":         boundarySource("github.com/charmbracelet/x/ansi"),
		pkgDoorTest + "/console.go":  boundarySource("/" + pkgConsole),
		pkgOwnerChannel + "/pane.go": boundarySource("/" + pkgSessionPane),
		pkgApproval + "/later/a.go":  boundarySource("/" + pkgClientAPI),
		pkgOwnerWire + "/below.go":   boundarySource("/" + pkgClientAPI + "/frames"),
		pkgCard + "/bad_test.go":     boundarySource("charm.land/bubbletea/v2"),
		pkgOwnerChannel + "/tag.go":  boundaryTagged,
		pkgConsoleView + "/a.go":     boundarySource("/"+pkgClientAPI, "charm.land/bubbletea/v2"),
	}),
		pkgCard+"/tea.go", pkgDemo+"/ansi.go", pkgDoorTest+"/console.go", pkgOwnerChannel+"/pane.go",
		pkgApproval+"/later/a.go", pkgOwnerWire+"/below.go")
}

func TestConsoleFirstBoundaryConsoleReach(t *testing.T) {
	denied := []string{
		"net", "net/http", "net/netip", "os", "os/exec", "syscall",
		"/" + pkgDoor, "/" + pkgDoorTest, "/" + pkgOwnerChannel, "/" + pkgOwnerJournal, "/" + pkgMergeBuild,
		"/" + pkgDemo, "/" + pkgConsoleView,
	}
	// Every package of the build under the console, the console itself, a
	// package that is there today and one that is not written yet.
	for _, pkg := range []string{pkgPager, pkgArrival, pkgApprovals, pkgLimitView, pkgConsole, pkgSessionPane, pkgConsole + "/later"} {
		sources := map[string]string{
			pkg + "/good.go": boundarySource(
				"strings", "time", "network", "charm.land/bubbletea/v2", "/"+pkgCard, "/"+pkgRecord, "/"+pkgLimits,
				"/"+pkgPresence, "/"+pkgLeadText, "/"+pkgPager),
			pkg + "/bad_test.go": boundarySource(denied...),
			pkg + "/tagged.go":   boundaryTagged,
		}
		var want []string
		for i, imp := range denied {
			name := pkg + "/bad" + strconv.Itoa(i) + ".go"
			sources[name] = boundarySource(imp)
			want = append(want, name)
		}
		expectFindings(t, ruleConsoleReach, boundarySample(t, sources), want...)
	}
	// The fixture tool may import os, and nothing else that is denied.
	expectFindings(t, ruleConsoleReach, boundarySample(t, map[string]string{
		pkgFixtureTool + "/main.go":    boundarySource("os", "path/filepath"),
		pkgFixtureTool + "/exec.go":    boundarySource("os/exec"),
		pkgFixtureTool + "/net.go":     boundarySource("net"),
		pkgFixtureTool + "/demo.go":    boundarySource("/" + pkgDemo),
		pkgFixtureTool + "/below/a.go": boundarySource("os"),
	}), pkgFixtureTool+"/exec.go", pkgFixtureTool+"/net.go", pkgFixtureTool+"/demo.go", pkgFixtureTool+"/below/a.go")
	// The command may import what the console may not.
	expectFindings(t, ruleConsoleReach, boundarySample(t, map[string]string{
		pkgConsoleCmd + "/main.go": boundarySource("os", "/"+pkgDemo, "/"+pkgConsoleView),
	}))
}

func TestConsoleFirstBoundaryStandIn(t *testing.T) {
	expectFindings(t, ruleStandIn, boundarySample(t, map[string]string{
		// Test files may import the stand-in, wherever they are.
		pkgOwnerChannel + "/service_test.go": boundarySource("/" + pkgDoorTest),
		pkgDoor + "/door_test.go":            boundarySource("/" + pkgDoorTest),
		"tests/owner_test.go":                boundarySource("/" + pkgDoorTest),
		pkgDemo + "/demo_test.go":            boundarySource("/"+pkgDoor, "/"+pkgDoorTest),
		// The stand-in may import the door, and the service may too.
		pkgDoorTest + "/standin.go":     boundarySource("/"+pkgDoor, "/"+pkgRecord),
		pkgOwnerChannel + "/service.go": boundarySource("/"+pkgDoor, "/"+pkgCard),
		pkgDemo + "/good.go":            boundarySource("/"+pkgCard, "/"+pkgRecord),
		pkgOwnerChannel + "/tagged.go":  boundaryTagged,
		pkgDemo + "/tagged.go":          boundaryTagged,
		// Non-test files may not import the stand-in.
		pkgOwnerChannel + "/standin.go": boundarySource("/" + pkgDoorTest),
		"internal/other/other.go":       boundarySource("/" + pkgDoorTest),
		"qualification/host/host.go":    boundarySource("/" + pkgDoorTest + "/below"),
		pkgConsoleCmd + "/main.go":      boundarySource("/" + pkgDoorTest),
		// The sample backend imports neither.
		pkgDemo + "/door.go":    boundarySource("/" + pkgDoor),
		pkgDemo + "/standin.go": boundarySource("/" + pkgDoorTest),
	}),
		pkgOwnerChannel+"/standin.go", "internal/other/other.go", "qualification/host/host.go",
		pkgConsoleCmd+"/main.go", pkgDemo+"/door.go", pkgDemo+"/standin.go")
}

func TestConsoleFirstBoundaryCommands(t *testing.T) {
	sources := map[string]string{
		"cmd/ataniq-console/good.go":     boundarySource("os", "/"+pkgDemo, "/"+pkgConsole, "/"+pkgConsoleView, "/"+pkgCard),
		"cmd/ataniq-console/bad_test.go": boundarySource("/"+pkgDoorTest, "/"+pkgOwnerChannel),
		"cmd/ataniqd/tagged.go":          boundaryTagged,
		"cmd/later/below/a.go":           boundarySource("/" + pkgDoor + "/below"),
		pkgOwnerChannel + "/a.go":        boundarySource("/"+pkgDoor, "/"+pkgOwnerJournal),
	}
	want := []string{"cmd/later/below/a.go"}
	for i, imp := range boundaryActing {
		name := "cmd/ataniqd/bad" + strconv.Itoa(i) + ".go"
		sources[name] = boundarySource("/" + imp)
		want = append(want, name)
	}
	expectFindings(t, ruleCommands, boundarySample(t, sources), want...)
}

func TestConsoleFirstBoundaryWords(t *testing.T) {
	// The samples are made from the list, so that no word of it is written
	// here a second time.
	for _, word := range boundaryWords {
		upper := strings.ToUpper(word)
		title := upper[:1] + word[1:]
		expectFindings(t, ruleWords, boundarySample(t, map[string]string{
			// As a part of a longer word it is not found.
			pkgCard + "/good.go": "package card\n\n// x" + word + ", " + word + "s, " + word + "_a and " + word + "4 are other words.\n" +
				"const runtime, provider, model = \"runtime-a\", \"provider-a\", \"model-a\"\n",
			pkgCard + "/comment.go":           "package card\n\n// Made for " + word + ".\n",
			pkgRecord + "/string.go":          "package record\n\nconst sample = \"" + title + "-4\"\n",
			pkgDemo + "/name.go":              "package demo\n\nvar " + upper + " int\n",
			pkgDoorTest + "/standin_test.go":  "package doortest\n\nconst sample = \"a/" + word + ".b\"\n",
			pkgOwnerChannel + "/tagged.go":    "//go:build " + boundaryTag + "\n\npackage ownerchannel // " + word + "\n",
			pkgApproval + "/later/below/a.go": "package below\n\nfunc f() { _ = `(" + upper + ")` }\n",
			"internal/other/other.go":         "package other\n\n// Made for " + word + ".\n",
		}),
			pkgCard+"/comment.go", pkgRecord+"/string.go", pkgDemo+"/name.go", pkgDoorTest+"/standin_test.go",
			pkgOwnerChannel+"/tagged.go", pkgApproval+"/later/below/a.go")
	}
	// A finding names the line.
	found := ruleWords(boundarySample(t, map[string]string{
		pkgCard + "/a.go": "package card\n\n// One.\n// Two " + boundaryWords[0] + ".\n",
	}))
	if want := pkgCard + "/a.go:4 holds the word " + boundaryWords[0]; len(found) != 1 || found[0] != want {
		t.Errorf("found %q, want %q", found, want)
	}
}

// TestConsoleFirstBoundaryTaggedFiles checks which build lines need the owner
// channel's tag. The import rules skip a file that cannot be built without it;
// the word rule does not. A file that is built when the tag is off is read,
// although its build line names the tag.
func TestConsoleFirstBoundaryTaggedFiles(t *testing.T) {
	for _, c := range []struct {
		head   string
		tagged bool
	}{
		{"", false},
		{"//go:build linux\n\n", false},
		{"//go:build " + boundaryTag + "\n\n", true},
		{"//go:build !" + boundaryTag + "\n\n", false},
		{"//go:build !linux || !" + boundaryTag + "\n\n", false},
		{"//go:build linux && !" + boundaryTag + "\n\n", false},
		{"//go:build linux && " + boundaryTag + "\n\n", true},
		{"//go:build !(linux || !" + boundaryTag + ")\n\n", true},
		{"//go:build linux && (" + boundaryTag + " || other)\n\n", false},
		{"//go:build linux && (" + boundaryTag + " || (other && " + boundaryTag + "))\n\n", true},
		{"//go:build " + boundaryTag + " || other\n\n", false},
		{"// A comment.\n\n//go:build other && " + boundaryTag + "\n\n", true},
		{"// +build linux," + boundaryTag + "\n\n", true},
		{"// +build linux " + boundaryTag + "\n\n", false},
		{"// +build !" + boundaryTag + "\n\n", false},
		{"// +build linux\n// +build " + boundaryTag + "\n\n", true},
		{"// +build " + boundaryTag + "\n// +build linux\n\n", true},
		// A //go:build line decides alone, wherever it stands.
		{"//go:build linux\n// +build " + boundaryTag + "\n\n", false},
		{"// +build " + boundaryTag + "\n\n//go:build linux\n\n", false},
		{"//go:build " + boundaryTag + "\n// +build linux\n\n", true},
		{"//go:build " + boundaryTag + "_more\n\n", false},
		{"//go:build more_" + boundaryTag + "\n\n", false},
		{"// The tag " + boundaryTag + " is named in a comment only.\n\n", false},
	} {
		f, err := parseBoundaryFile("cmd/sample/main.go", c.head+boundarySource("/"+pkgDoor))
		if err != nil {
			t.Fatal(err)
		}
		if f.tagged != c.tagged {
			t.Errorf("head %q: tagged %v, want %v", c.head, f.tagged, c.tagged)
		}
		if found := ruleCommands([]boundaryFile{f}); (len(found) == 0) != c.tagged {
			t.Errorf("head %q: findings %q, tagged %v", c.head, found, c.tagged)
		}
	}
	// A build line after the package clause is a comment.
	f, err := parseBoundaryFile("cmd/sample/main.go", "package main\n\n//go:build "+boundaryTag+"\n\nimport \"os\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if f.tagged {
		t.Error("a build line below the package clause was read as one")
	}
}

// TestConsoleFirstBoundaryLoader checks, in a temporary folder, which files
// the loader reads: test files and files of any build line are read, and only
// the imports have to be valid Go.
func TestConsoleFirstBoundaryLoader(t *testing.T) {
	dir := t.TempDir()
	for name, src := range map[string]string{
		"cmd/sample/main.go":                    boundarySource("/"+pkgDoor) + "\nfunc main() { this is not Go\n",
		"cmd/sample/main_test.go":               boundarySource("/" + pkgDoorTest),
		"cmd/sample/tagged.go":                  boundaryTagged,
		"cmd/sample/other_windows.go":           "//go:build windows\n\n" + boundarySource("/"+pkgOwnerJournal),
		"cmd/sample/notes.txt":                  "import \"" + boundaryImport("/"+pkgDoor) + "\"\n",
		"cmd/sample/testdata/data.go":           "not Go at all\n",
		"cmd/sample/.hidden/a.go":               "not Go at all\n",
		"cmd/sample/_old/a.go":                  "not Go at all\n",
		"cmd/sample/vendor/a.go":                "not Go at all\n",
		"third_party/patched/a.go":              "not Go at all\n",
		"build/a.go":                            "not Go at all\n",
		"internal/build/a.go":                   boundarySource("strings"),
		"internal/approval/card/third_party.go": boundarySource("strings"),
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := loadBoundaryFiles(t, dir)
	var got []string
	for _, f := range files {
		mark := ""
		if f.test {
			mark += " test"
		}
		if f.tagged {
			mark += " tagged"
		}
		got = append(got, f.path+mark)
	}
	sort.Strings(got)
	want := []string{
		"cmd/sample/main.go",
		"cmd/sample/main_test.go test",
		"cmd/sample/other_windows.go",
		"cmd/sample/tagged.go tagged",
		"internal/approval/card/third_party.go",
		"internal/build/a.go",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("read %q, want %q", got, want)
	}
	expectFindings(t, ruleCommands, files, "cmd/sample/main.go", "cmd/sample/other_windows.go")
}
