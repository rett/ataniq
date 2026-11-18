package tests

// This file holds the line around the build tag that compiles canary mode. It
// is not itself behind the tag and passes with and without it. Every check is
// written so that an empty set passes: none of the tagged packages need exist.
// The checks are plain functions over a tree, a text or a package listing, and
// the Test...Check functions below run them over small synthetic inputs, so the
// guard is itself tested.

import (
	"bytes"
	"errors"
	"fmt"
	"go/build/constraint"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

const canaryTag = "ataniq_canary"

// canaryAllowedPaths are the only places a Go file's build constraint may name
// the tag. Each is a folder, with its subfolders.
var canaryAllowedPaths = []string{
	"internal/leadcanary/",
	"internal/leadholder/sandboxstart/",
	"internal/leadholder/injectbridge/",
	"internal/leadholder/canaryroot/",
	"internal/leadholder/serve/",
	"internal/leaddaemon/",
	"cmd/ataniqd/",
	"cmd/ataniq-holder/",
	"cmd/ataniq-canary/",
	"cmd/ataniq-canary-standin/",
}

// canaryWholePaths hold packages that have no file outside the tag: an untagged
// build cannot import them, and every Go file in them, tests included, carries
// the tag.
var canaryWholePaths = []string{
	"internal/leadcanary/",
	"internal/leadholder/sandboxstart/",
	"internal/leadholder/injectbridge/",
	"internal/leadholder/canaryroot/",
	"cmd/ataniq-canary/",
	"cmd/ataniq-canary-standin/",
}

// canaryForbiddenDeps are the packages that no command may depend on without
// the tag, as paths below the module.
var canaryForbiddenDeps = []string{
	"internal/leadcanary",
	"internal/leadholder/sandboxstart",
	"internal/leadholder/injectbridge",
	"internal/leadholder/canaryroot",
}

// canaryMakeTargets are the Makefile targets whose recipes may name the tag.
var canaryMakeTargets = []string{"test-canary", "canary-build"}

func underAnyPath(p string, folders []string) bool {
	for _, folder := range folders {
		if strings.HasPrefix(p, folder) {
			return true
		}
	}
	return false
}

// headerConstraints returns the build constraints in the header of a Go file,
// the lines before the package clause, and the constraint lines that do not
// parse.
func headerConstraints(data string) ([]constraint.Expr, []string) {
	all, _, bad := headerConstraintsGo(data)
	return all, bad
}

// headerConstraintsGo also returns the constraints the go command applies: the
// go:build line alone when the file has one, and the plus build lines
// otherwise. Placement uses every line that names the tag, which is the
// conservative reading, and requirement uses the applied constraints.
func headerConstraintsGo(data string) (all, applied []constraint.Expr, bad []string) {
	var goBuild, plusBuild []constraint.Expr
	inBlock := false
	for _, line := range strings.Split(data, "\n") {
		s := strings.TrimSpace(line)
		if inBlock {
			if strings.Contains(s, "*/") {
				inBlock = false
			}
			continue
		}
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "/*") {
			inBlock = !strings.Contains(s[2:], "*/")
			continue
		}
		if !strings.HasPrefix(s, "//") {
			break
		}
		if constraint.IsGoBuild(s) || constraint.IsPlusBuild(s) {
			expr, err := constraint.Parse(s)
			if err != nil {
				bad = append(bad, s)
				continue
			}
			all = append(all, expr)
			if constraint.IsGoBuild(s) {
				goBuild = append(goBuild, expr)
			} else {
				plusBuild = append(plusBuild, expr)
			}
		}
	}
	if len(goBuild) > 1 && slices.ContainsFunc(goBuild, exprNamesTag) {
		bad = append(bad, "more than one go:build line, one of which names "+canaryTag)
	}
	if len(goBuild) > 0 {
		return all, goBuild, bad
	}
	return all, plusBuild, bad
}

func exprNamesTag(e constraint.Expr) bool {
	switch e := e.(type) {
	case *constraint.TagExpr:
		return e.Tag == canaryTag
	case *constraint.NotExpr:
		return exprNamesTag(e.X)
	case *constraint.AndExpr:
		return exprNamesTag(e.X) || exprNamesTag(e.Y)
	case *constraint.OrExpr:
		return exprNamesTag(e.X) || exprNamesTag(e.Y)
	}
	return false
}

// buildsWithoutTag reports whether the file would be built with every other
// tag set and the canary tag not.
func buildsWithoutTag(exprs []constraint.Expr) bool {
	for _, e := range exprs {
		if !e.Eval(func(tag string) bool { return tag != canaryTag }) {
			return false
		}
	}
	return true
}

// skipCanaryDir reports the folders no Go package is built from: testdata, the
// folders that start with a dot or an underscore, and the top-level build
// folder, which holds ignored build output. A folder named build deeper in the
// tree is a package like any other.
func skipCanaryDir(p, name string) bool {
	return name == "testdata" || p == "build" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
}

// checkCanaryPlacement is check (1): a Go file whose constraint names the tag
// lies under canaryAllowedPaths, and every Go file of a wholly tagged package
// carries the tag, that is, is not built without it.
func checkCanaryPlacement(fsys fs.FS) []string {
	var problems []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && skipCanaryDir(p, d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		exprs, applied, bad := headerConstraintsGo(string(data))
		for _, line := range bad {
			if strings.Contains(line, canaryTag) {
				problems = append(problems, fmt.Sprintf("%s: unreadable build constraint %q", p, line))
			}
		}
		named := slices.ContainsFunc(exprs, exprNamesTag)
		if named && !underAnyPath(p, canaryAllowedPaths) {
			problems = append(problems, fmt.Sprintf("%s: the build constraint names %s outside the allowed paths", p, canaryTag))
		}
		if underAnyPath(p, canaryWholePaths) && (!named || buildsWithoutTag(applied)) {
			problems = append(problems, fmt.Sprintf("%s: a file of a wholly tagged package does not require %s", p, canaryTag))
		}
		return nil
	})
	if err != nil {
		problems = append(problems, "walking the tree: "+err.Error())
	}
	return problems
}

var makeRuleLine = regexp.MustCompile(`^([A-Za-z0-9_.%/ -]+):($|[^=])`)

// canaryTargetRef finds the name of one of canaryMakeTargets as a word.
var canaryTargetRef = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])(test-canary|canary-build)($|[^A-Za-z0-9_.-])`)

var makeInclude = regexp.MustCompile(`^-?s?include\s`)

var makePhony = regexp.MustCompile(`^\.PHONY\s*:`)

// checkMakefileTag is the first half of check (2): in a Makefile the tag
// appears only inside the recipes of canaryMakeTargets. A comment, a variable,
// the .PHONY line, a prerequisite line and the recipe of any other target are
// all outside. Two more lines are problems, so that no other target and no
// other file can reach the tagged targets: a mention of a name in
// canaryMakeTargets anywhere but a comment, the .PHONY line and the rule line
// that defines it (a prerequisite or a recipe of another target), and an
// include, whose file this check does not read. The limit of a text check is
// a name that is assembled at run time or by make's own expansion.
func checkMakefileTag(text string) []string {
	var problems []string
	var targets []string
	continued, continuedRecipe, continuedPhony := false, false, false
	for i, line := range strings.Split(text, "\n") {
		recipe := strings.HasPrefix(line, "\t")
		phony := continued && continuedPhony || makePhony.MatchString(line)
		if continued {
			recipe = continuedRecipe
		} else if !recipe {
			trimmed := strings.TrimSpace(line)
			if m := makeRuleLine.FindStringSubmatch(line); m != nil {
				targets = strings.Fields(m[1])
			} else if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
				targets = nil
			}
		}
		wasContinued := continued
		continued = strings.HasSuffix(line, "\\")
		continuedRecipe = recipe
		continuedPhony = phony
		trimmed := strings.TrimSpace(line)
		var problem string
		if !recipe && !wasContinued && makeInclude.MatchString(trimmed+" ") {
			problem = "includes another makefile, which this check does not read"
		}
		if problem == "" && strings.Contains(line, canaryTag) {
			allowed := recipe && len(targets) > 0
			for _, target := range targets {
				allowed = allowed && slices.Contains(canaryMakeTargets, target)
			}
			if !allowed {
				problem = fmt.Sprintf("names %s outside the recipes of %s", canaryTag, strings.Join(canaryMakeTargets, " and "))
			}
		}
		if problem == "" && canaryTargetRef.MatchString(line) && !strings.HasPrefix(trimmed, "#") && !phony {
			own := false
			if m := makeRuleLine.FindStringSubmatch(line); m != nil && !recipe && !wasContinued {
				own = !canaryTargetRef.MatchString(line[len(m[1])+1:])
			}
			if !own {
				problem = "another line names " + strings.TrimSpace(canaryTargetRef.FindString(line))
			}
		}
		if problem != "" {
			problems = append(problems, fmt.Sprintf("Makefile:%d: %s", i+1, problem))
		}
	}
	return problems
}

// checkNoCanaryTagUnder is the second half of check (2): no file under the
// folders names the tag.
func checkNoCanaryTagUnder(fsys fs.FS, folders ...string) []string {
	var problems []string
	for _, folder := range folders {
		err := fs.WalkDir(fsys, folder, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			data, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte(canaryTag)) {
				problems = append(problems, fmt.Sprintf("%s: names %s", p, canaryTag))
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) && !strings.Contains(err.Error(), "file does not exist") {
			problems = append(problems, folder+": "+err.Error())
		}
	}
	return problems
}

// A listing line of check (3) is import path, "|", "err" when the package or a
// dependency does not load, "|", and the dependencies, space separated.
const canaryDepsFormat = `{{.ImportPath}}|{{if or .Error .DepsErrors}}err{{end}}|{{join .Deps " "}}`

func pathIsOrUnder(p, base string) bool {
	return p == base || strings.HasPrefix(p, base+"/")
}

// checkCanaryCmdDeps is check (3): without the tag, no command depends on a
// wholly tagged library package, and the daemon depends on nothing under
// internal/inject. The listing is of the commands, with their dependencies.
func checkCanaryCmdDeps(module, listing string) []string {
	var problems []string
	for _, line := range strings.Split(listing, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 3)
		if len(parts) != 3 {
			problems = append(problems, fmt.Sprintf("unreadable listing line %q", line))
			continue
		}
		pkg, failed, deps := parts[0], parts[1], strings.Fields(parts[2])
		if !strings.HasPrefix(pkg, module+"/cmd/") {
			continue
		}
		if failed != "" {
			problems = append(problems, fmt.Sprintf("%s does not load without the tag", pkg))
		}
		for _, dep := range deps {
			for _, forbidden := range canaryForbiddenDeps {
				if pathIsOrUnder(dep, module+"/"+forbidden) {
					problems = append(problems, fmt.Sprintf("%s depends on %s without the tag", pkg, dep))
				}
			}
			if pkg == module+"/cmd/ataniqd" && pathIsOrUnder(dep, module+"/internal/inject") {
				problems = append(problems, fmt.Sprintf("%s depends on %s", pkg, dep))
			}
		}
	}
	return problems
}

func listedModules(listing string) map[string]bool {
	modules := map[string]bool{}
	for _, line := range strings.Split(listing, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			modules[line] = true
		}
	}
	return modules
}

// checkCanaryModules is check (4): the tagged listing names no module that the
// untagged listing does not.
func checkCanaryModules(untagged, tagged string) []string {
	have := listedModules(untagged)
	var extra []string
	for module := range listedModules(tagged) {
		if !have[module] {
			extra = append(extra, module)
		}
	}
	sort.Strings(extra)
	var problems []string
	for _, module := range extra {
		problems = append(problems, fmt.Sprintf("module %s is used only with the tag", module))
	}
	return problems
}

func yamlIndent(s string) int { return len(s) - len(strings.TrimLeft(s, " ")) }

// stripYAMLComment drops a whole-line comment, and a trailing one that follows
// a space. It is for the plain workflow files of this project, not for YAML in
// general.
func stripYAMLComment(s string) string {
	if strings.HasPrefix(strings.TrimSpace(s), "#") {
		return ""
	}
	if i := strings.Index(s, " #"); i >= 0 {
		return strings.TrimRight(s[:i], " ")
	}
	return s
}

// workflowJobLines returns the lines of a job of a workflow file, the job key
// included, and whether there is one.
func workflowJobLines(text, name string) ([]string, bool) {
	var out []string
	inJobs, inJob := false, false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimRight(stripYAMLComment(raw), " ")
		switch {
		case line == "":
			if inJob {
				out = append(out, raw)
			}
		case yamlIndent(line) == 0:
			inJobs = line == "jobs:"
			inJob = false
		case inJobs && yamlIndent(line) == 2:
			inJob = line == "  "+name+":"
			if inJob {
				out = append(out, raw)
			}
		case inJob:
			out = append(out, raw)
		}
	}
	return out, len(out) > 0
}

// workflowPermissions reads the permissions key at the given indent in the
// lines. It reports whether the key is there and, if it is not read-only, what
// is wrong.
func workflowPermissions(lines []string, indent int) (bool, string) {
	key := strings.Repeat(" ", indent) + "permissions:"
	for i, raw := range lines {
		line := strings.TrimRight(stripYAMLComment(raw), " ")
		if !strings.HasPrefix(line, key) {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, key))
		readOnly := func(entry string) string {
			name, v, _ := strings.Cut(strings.TrimSpace(entry), ":")
			if v = strings.TrimSpace(v); v != "read" && v != "none" {
				return fmt.Sprintf("permission %s is %q", strings.TrimSpace(name), v)
			}
			return ""
		}
		switch {
		case value == "read-all" || value == "{}":
			return true, ""
		case strings.HasPrefix(value, "{"):
			for _, entry := range strings.Split(strings.Trim(value, "{} "), ",") {
				if msg := readOnly(entry); msg != "" {
					return true, msg
				}
			}
			return true, ""
		case value == "":
			seen := false
			for _, next := range lines[i+1:] {
				next = stripYAMLComment(next)
				if strings.TrimSpace(next) == "" {
					continue
				}
				if yamlIndent(next) <= indent {
					break
				}
				seen = true
				if msg := readOnly(next); msg != "" {
					return true, msg
				}
			}
			if !seen {
				return true, "the permissions are empty"
			}
			return true, ""
		}
		return true, fmt.Sprintf("the permissions are %q", value)
	}
	return false, ""
}

// workflowRunCommands returns the commands of the run steps in the lines, one
// per command line.
func workflowRunCommands(lines []string) []string {
	var commands []string
	for i, raw := range lines {
		line := stripYAMLComment(raw)
		trimmed := strings.TrimSpace(line)
		keyIndent := yamlIndent(line)
		if strings.HasPrefix(trimmed, "- ") {
			trimmed = strings.TrimSpace(trimmed[2:])
			keyIndent += 2
		}
		if !strings.HasPrefix(trimmed, "run:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, "run:"))
		if strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
			for _, next := range lines[i+1:] {
				if strings.TrimSpace(next) == "" {
					continue
				}
				if yamlIndent(next) <= keyIndent {
					break
				}
				commands = append(commands, strings.TrimSpace(next))
			}
			continue
		}
		commands = append(commands, strings.Trim(value, `"'`))
	}
	return commands
}

func workflowUses(lines []string) []string {
	var uses []string
	for _, raw := range lines {
		trimmed := strings.TrimSpace(stripYAMLComment(raw))
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
		if strings.HasPrefix(trimmed, "uses:") {
			uses = append(uses, strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "uses:")), `"'`))
		}
	}
	return uses
}

// canaryJobRuns are the only commands the test-canary job runs.
var canaryJobRuns = []string{"make -n bootstrap", "make bootstrap", "make doctor", "make test-canary"}

// canaryJobUses are the only actions the test-canary job uses. Anything else,
// a composite action of the repository included, could keep build output or
// run a tagged build.
var canaryJobUses = []string{"actions/checkout@", "actions/setup-go@"}

// canaryRunKitPath is the folder of the canary run kit. A file there may build
// with the tag, so no workflow run line may name it.
var canaryRunKitPath = regexp.MustCompile(`qualification/canary($|[^A-Za-z0-9_.-])`)

// checkCanaryWorkflow is check (5). Its input is every file under .github:
// the workflow files directly below .github/workflows by their file name, and
// every other file (composite actions, nested folders, anything else) by its
// path. No file names the tag or canary-build outside a comment, and no run
// line of any file names the run kit's folder. test.yml has a job test-canary
// that runs the make target and never names the tag, uses only the actions of
// canaryJobUses, has no if or continue-on-error that could keep it from
// failing, and has read-only permissions.
//
// The limit no text check closes is a run line that assembles the text of the
// tag or of canary-build at run time, for example from two shell variables.
func checkCanaryWorkflow(files map[string]string) []string {
	var problems []string
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		var kept []string
		for _, line := range strings.Split(files[name], "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "#") {
				kept = append(kept, line)
			}
		}
		if strings.Contains(strings.Join(kept, "\n"), "canary-build") {
			problems = append(problems, name+": names canary-build")
		}
		if strings.Contains(files[name], canaryTag) {
			problems = append(problems, name+": names the tag "+canaryTag)
		}
		for _, run := range workflowRunCommands(kept) {
			if canaryRunKitPath.MatchString(run) {
				problems = append(problems, fmt.Sprintf("%s: a run line names the canary run kit: %q", name, run))
			}
		}
	}
	text, ok := files["test.yml"]
	if !ok {
		return append(problems, "test.yml: missing")
	}
	job, ok := workflowJobLines(text, "test-canary")
	if !ok {
		return append(problems, "test.yml: no job test-canary")
	}
	runs := workflowRunCommands(job)
	if !slices.Contains(runs, "make test-canary") {
		problems = append(problems, "test.yml: the test-canary job does not run make test-canary")
	}
	for _, run := range runs {
		if !slices.Contains(canaryJobRuns, run) {
			problems = append(problems, fmt.Sprintf("test.yml: the test-canary job runs %q, and nothing but %s is allowed", run, strings.Join(canaryJobRuns, ", ")))
		}
	}
	for _, use := range workflowUses(job) {
		if !slices.ContainsFunc(canaryJobUses, func(prefix string) bool { return strings.HasPrefix(use, prefix) }) {
			problems = append(problems, fmt.Sprintf("test.yml: the test-canary job uses %s, and nothing but %s is allowed", use, strings.Join(canaryJobUses, ", ")))
		}
	}
	for _, raw := range job {
		key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(stripYAMLComment(raw)), "- "))
		if strings.HasPrefix(key, "continue-on-error:") || strings.HasPrefix(key, "if:") {
			problems = append(problems, fmt.Sprintf("test.yml: the test-canary job has %q, which could keep it from failing", key))
		}
	}
	found, msg := workflowPermissions(job, 4)
	if !found {
		found, msg = workflowPermissions(strings.Split(text, "\n"), 0)
		if !found {
			msg = "neither the job nor the workflow states its permissions"
		}
	}
	if msg != "" {
		problems = append(problems, "test.yml: the test-canary job is not read-only: "+msg)
	}
	return problems
}

// canaryGithubFiles reads every file under .github, the input of
// checkCanaryWorkflow: a file directly below .github/workflows by its file
// name, every other file by its path.
func canaryGithubFiles(fsys fs.FS) (map[string]string, error) {
	files := map[string]string{}
	err := fs.WalkDir(fsys, ".github", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		if path.Dir(p) == ".github/workflows" {
			p = path.Base(p)
		}
		files[p] = string(data)
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return files, nil
	}
	return files, err
}

// namesCanaryPackage reports whether a go list pattern names a wholly tagged
// package or something below one.
func namesCanaryPackage(pattern string) bool {
	p := "/" + strings.TrimPrefix(pattern, "./") + "/"
	for _, whole := range canaryWholePaths {
		if strings.Contains(p, "/"+whole) {
			return true
		}
	}
	return false
}

// canaryGoTool finds the Go executable the way the helper script does for the
// doctor: the one on PATH when it is the pinned version, else the one recorded
// by the bootstrap.
func canaryGoTool(t *testing.T) string {
	t.Helper()
	dir := root(t)
	pin, err := os.ReadFile(filepath.Join(dir, ".go-version"))
	if err != nil {
		t.Fatal(err)
	}
	version := strings.TrimSpace(string(pin))
	exact := func(tool string) bool {
		cmd := exec.Command(tool, "version")
		cmd.Env = canaryGoEnv()
		out, err := cmd.Output()
		return err == nil && strings.HasPrefix(string(out), "go version go"+version+" ")
	}
	if tool, err := exec.LookPath("go"); err == nil && exact(tool) {
		return tool
	}
	record, err := os.ReadFile(filepath.Join(dir, "build", "go-toolchain"))
	if err == nil {
		lines := strings.Split(strings.TrimRight(string(record), "\n"), "\n")
		if len(lines) == 2 && lines[0] == version && filepath.IsAbs(lines[1]) && strings.HasSuffix(lines[1], "/bin/go") && exact(lines[1]) {
			return lines[1]
		}
	}
	t.Fatalf("no Go %s: run make bootstrap", version)
	return ""
}

// canaryGoEnv is the environment of the helper script: offline, no module or
// flag setting of the caller, and in particular no GOFLAGS, so that no tag
// comes in from outside.
func canaryGoEnv() []string {
	return append(os.Environ(), "GOENV=off", "GOWORK=off", "GOFLAGS=", "GO111MODULE=on", "GOPRIVATE=", "GONOPROXY=none", "GONOSUMDB=none", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off")
}

// canaryGoList runs go list in the repository, offline. Without the tag it
// refuses a pattern that names a wholly tagged package: with no Go file left,
// that is an error, and the checks must not depend on it.
func canaryGoList(t *testing.T, tagged bool, flags []string, patterns ...string) string {
	t.Helper()
	args := []string{"list"}
	if tagged {
		args = append(args, "-tags", canaryTag)
	} else {
		for _, pattern := range patterns {
			if namesCanaryPackage(pattern) {
				t.Fatalf("go list without the tag must not name the wholly tagged package %s", pattern)
			}
		}
	}
	args = append(append(args, flags...), patterns...)
	cmd := exec.Command(canaryGoTool(t), args...)
	cmd.Dir = root(t)
	cmd.Env = canaryGoEnv()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return string(out)
}

func reportCanaryProblems(t *testing.T, problems []string) {
	t.Helper()
	for _, p := range problems {
		t.Error(p)
	}
}

// TestCanaryTag holds the five checks of the build tag on the repository.
func TestCanaryTag(t *testing.T) {
	dir := root(t)
	repo := os.DirFS(dir)

	t.Run("1_placement", func(t *testing.T) {
		reportCanaryProblems(t, checkCanaryPlacement(repo))
	})

	t.Run("2_makefile_and_scripts", func(t *testing.T) {
		makefile, err := os.ReadFile(filepath.Join(dir, "Makefile"))
		if err != nil {
			t.Fatal(err)
		}
		reportCanaryProblems(t, checkMakefileTag(string(makefile)))
		reportCanaryProblems(t, checkNoCanaryTagUnder(repo, ".github", "scripts"))
	})

	t.Run("3_untagged_dependencies", func(t *testing.T) {
		module := strings.TrimSpace(canaryGoList(t, false, nil, "-m"))
		listing := canaryGoList(t, false, []string{"-e", "-f", canaryDepsFormat}, "./cmd/...")
		reportCanaryProblems(t, checkCanaryCmdDeps(module, listing))
	})

	t.Run("4_modules", func(t *testing.T) {
		flags := []string{"-deps", "-test", "-f", "{{with .Module}}{{.Path}}{{end}}"}
		untagged := canaryGoList(t, false, flags, "./...")
		tagged := canaryGoList(t, true, flags, "./...")
		reportCanaryProblems(t, checkCanaryModules(untagged, tagged))
	})

	t.Run("5_workflow", func(t *testing.T) {
		files, err := canaryGithubFiles(repo)
		if err != nil {
			t.Fatal(err)
		}
		reportCanaryProblems(t, checkCanaryWorkflow(files))
	})
}

func goSource(header string) *fstest.MapFile {
	return &fstest.MapFile{Data: []byte(header + "\n\npackage x\n")}
}

// The constraint lines of the sample Go files. They are built from strings so
// that no line of this file starts with a constraint.
const (
	hdrTagged  = "//go:build linux && ataniq_canary"
	hdrNegated = "//go:build linux && !ataniq_canary"
	hdrPlain   = "//go:build linux"
	// The old style lines, alone or after a go:build line.
	hdrPlusTagged = "// +build linux,ataniq_canary"
	hdrPlusPlain  = "// +build linux"
)

func TestCanaryTagPlacementCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files fstest.MapFS
		bad   []string // paths named by the problems
	}{
		{"no tagged file", fstest.MapFS{"cmd/x/main.go": goSource(hdrPlain), "go.mod": {Data: []byte("module m\n")}}, nil},
		{"empty tree", fstest.MapFS{}, nil},
		{"a tagged file in an allowed folder", fstest.MapFS{
			"cmd/ataniqd/canary.go":          goSource(hdrTagged),
			"cmd/ataniq-holder/canary.go":    goSource(hdrTagged),
			"internal/leaddaemon/deep/x.go":  goSource(hdrTagged),
			"internal/leadholder/serve/x.go": goSource(hdrTagged),
		}, nil},
		{"a tagged file in a wholly tagged package", fstest.MapFS{
			"internal/leadcanary/a.go":                  goSource(hdrTagged),
			"internal/leadcanary/sub/a_test.go":         goSource(hdrTagged),
			"internal/leadholder/sandboxstart/a.go":     goSource(hdrTagged),
			"internal/leadholder/injectbridge/a.go":     goSource(hdrTagged),
			"internal/leadholder/canaryroot/a.go":       goSource(hdrTagged),
			"cmd/ataniq-canary/main.go":                 goSource(hdrTagged),
			"cmd/ataniq-canary-standin/main.go":         goSource(hdrTagged),
			"cmd/ataniq-canary-standin/main_test.go":    goSource(hdrTagged),
			"internal/leadholder/serve/untagged_one.go": goSource(hdrPlain),
		}, nil},
		{"a tagged file in the wrong place", fstest.MapFS{
			"internal/other/x.go":         goSource(hdrTagged),
			"cmd/ataniqx/main.go":         goSource(hdrTagged),
			"internal/leadholder/x.go":    goSource(hdrTagged),
			"internal/leadholder/serve/x": &fstest.MapFile{Data: []byte("not go")},
		}, []string{"internal/other/x.go", "cmd/ataniqx/main.go", "internal/leadholder/x.go"}},
		{"a test file in the wrong place", fstest.MapFS{"internal/other/x_test.go": goSource(hdrTagged)}, []string{"internal/other/x_test.go"}},
		{"a negated mention counts as naming", fstest.MapFS{
			"internal/other/x.go":            goSource(hdrNegated),
			"internal/leadholder/serve/x.go": goSource(hdrNegated),
		}, []string{"internal/other/x.go"}},
		{"an untagged file in a wholly tagged package", fstest.MapFS{
			"internal/leadcanary/a.go":              goSource(hdrTagged),
			"internal/leadcanary/sub/b.go":          goSource(hdrPlain),
			"internal/leadholder/sandboxstart/c.go": goSource(""),
			"cmd/ataniq-canary/main_test.go":        goSource(hdrPlain),
		}, []string{"internal/leadcanary/sub/b.go", "internal/leadholder/sandboxstart/c.go", "cmd/ataniq-canary/main_test.go"}},
		{"a negated file in a wholly tagged package", fstest.MapFS{
			"internal/leadholder/injectbridge/a.go": goSource(hdrNegated),
		}, []string{"internal/leadholder/injectbridge/a.go"}},
		{"a constraint that does not require the tag", fstest.MapFS{
			"internal/leadholder/canaryroot/a.go": goSource("//go:build linux || ataniq_canary"),
		}, []string{"internal/leadholder/canaryroot/a.go"}},
		{"the folder name is matched whole", fstest.MapFS{
			"cmd/ataniq-canary-extra/main.go":    goSource(hdrPlain),
			"internal/leadcanaryx/a.go":          goSource(hdrPlain),
			"internal/leadholder/serve/x.go":     goSource(hdrTagged),
			"cmd/ataniq-canary-standin/main.go":  goSource(hdrTagged),
			"cmd/ataniq-canary-standin2/main.go": goSource(hdrTagged),
		}, []string{"cmd/ataniq-canary-standin2/main.go"}},
		{"a string that mentions the tag is not a constraint", fstest.MapFS{
			"tests/canary_x_test.go": &fstest.MapFile{Data: []byte("package tests\n\nconst tag = \"ataniq_canary\"\n\n// //go:build linux && ataniq_canary\n")},
			"internal/other/x.go":    &fstest.MapFile{Data: []byte("package x\n\n//go:build ataniq_canary\n")},
		}, nil},
		{"a constraint after a license comment", fstest.MapFS{
			"internal/other/x.go": &fstest.MapFile{Data: []byte("/* license\n//go:build ataniq_canary\n*/\n\n// Copyright\n" + hdrTagged + "\n\npackage x\n")},
		}, []string{"internal/other/x.go"}},
		{"testdata, the top-level build and hidden folders are not packages", fstest.MapFS{
			"internal/other/testdata/x.go": goSource(hdrTagged),
			"build/x.go":                   goSource(hdrTagged),
			"build/sub/x.go":               goSource(hdrTagged),
			".hidden/x.go":                 goSource(hdrTagged),
			"internal/_old/x.go":           goSource(hdrTagged),
		}, nil},
		{"an old style line is read", fstest.MapFS{
			"internal/other/x.go": goSource("// +build ataniq_canary"),
		}, []string{"internal/other/x.go"}},
		// The go command applies the go:build line alone when a file has one
		// and the plus build lines only otherwise.
		{"a plus build line that the go command ignores", fstest.MapFS{
			"cmd/ataniq-canary-standin/main.go": goSource(hdrPlain + "\n" + hdrPlusTagged),
			"internal/leadcanary/a.go":          goSource(hdrPlain + "\n" + hdrPlusTagged),
			"internal/leadcanary/b.go":          goSource(hdrTagged + "\n" + hdrPlusPlain),
		}, []string{"cmd/ataniq-canary-standin/main.go", "internal/leadcanary/a.go"}},
		{"a plus build line alone is applied", fstest.MapFS{
			"internal/leadcanary/a.go":       goSource(hdrPlusTagged),
			"internal/leadcanary/b.go":       goSource(hdrPlusPlain),
			"internal/leadholder/serve/c.go": goSource(hdrPlusPlain),
		}, []string{"internal/leadcanary/b.go"}},
		{"two go:build lines, one naming the tag", fstest.MapFS{
			"internal/leadcanary/a.go": goSource(hdrTagged + "\n" + hdrPlain),
			"internal/leadcanary/b.go": goSource(hdrPlain + "\n" + hdrPlain),
		}, []string{"internal/leadcanary/a.go", "internal/leadcanary/b.go"}},
		// Only the top-level build folder holds build output.
		{"a build folder below the top level is a package", fstest.MapFS{
			"internal/leadcanary/build/b.go":  goSource(hdrPlain),
			"internal/other/build/b.go":       goSource(hdrTagged),
			"internal/leadcanary/build/ok.go": goSource(hdrTagged),
			"build/x.go":                      goSource(hdrTagged),
		}, []string{"internal/leadcanary/build/b.go", "internal/other/build/b.go"}},
		{"an unreadable constraint that names the tag", fstest.MapFS{
			"internal/other/x.go": goSource("//go:build ataniq_canary &&"),
		}, []string{"internal/other/x.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := checkCanaryPlacement(tc.files)
			var got []string
			for _, p := range problems {
				got = append(got, strings.SplitN(p, ":", 2)[0])
			}
			sort.Strings(got)
			want := slices.Clone(tc.bad)
			sort.Strings(want)
			if !slices.Equal(got, want) {
				t.Fatalf("problems for %v, want %v:\n%s", got, want, strings.Join(problems, "\n"))
			}
		})
	}
}

const goodMakefile = `.PHONY: test test-canary

# A comment.
VAR = value

test: doctor
	@sh scripts/go-tool.sh test ./...

# The tagged run.
test-canary: doctor
	@pkgs=$$(grep -rl 'ataniq_canary' .); \
	sh scripts/go-tool.sh test -tags ataniq_canary $$pkgs ./tests/

canary-build: doctor
	@sh scripts/go-tool.sh build -tags ataniq_canary ./cmd/ataniq-canary
`

func TestCanaryTagMakefileCheck(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		bad  []int // line numbers
	}{
		{"no mention", ".PHONY: test\n\ntest: doctor\n\t@sh scripts/go-tool.sh test ./...\n", nil},
		{"empty", "", nil},
		{"the two targets", goodMakefile, nil},
		{"after a blank line and a comment in the recipe", "test-canary: doctor\n\t@true\n\n# note\n\t@echo ataniq_canary\n", nil},
		{"in another target", "test: doctor\n\t@sh scripts/go-tool.sh test -tags ataniq_canary ./...\n", []int{2}},
		{"in the other target's continued line", "test: doctor\n\t@true; \\\n\techo ataniq_canary\n", []int{3}},
		{"in a continuation that does not start with a tab", "test-canary: doctor\n\t@true; \\\necho ataniq_canary\n\n.PHONY: all\n", nil},
		{"in a comment", "# the ataniq_canary tag\ntest-canary:\n\t@true\n", []int{1}},
		{"in the phony line", ".PHONY: test-canary ataniq_canary\n", []int{1}},
		{"in a variable", "TAG = ataniq_canary\n", []int{1}},
		{"in a prerequisite line", "test-canary: ataniq_canary\n\t@true\n", []int{1}},
		{"in a recipe that follows a variable", "test-canary:\n\t@true\nX = 1\n\t@echo ataniq_canary\n", []int{4}},
		{"in a recipe after the allowed target ended", "test-canary:\n\t@true\n\ntest:\n\t@echo ataniq_canary\n", []int{5}},
		{"in a rule of several targets", "test-canary test:\n\t@echo ataniq_canary\n", []int{2}},
		{"in a variable that continues", "X = a \\\n\tataniq_canary\ntest-canary:\n\t@true\n", []int{2}},
		// No other target may run the tagged ones, and no other file may hold a
		// target that builds with the tag.
		{"a prerequisite of another target", "test: doctor test-canary\n\t@true\n", []int{1}},
		{"a prerequisite of the build target", "release-build: canary-build\n\t@true\n", []int{1}},
		{"a recipe that calls a tagged target", "test: doctor\n\t@$(MAKE) canary-build\n", []int{2}},
		{"a recipe that calls the other tagged target", "test: doctor\n\t@make -C . test-canary\n", []int{2}},
		{"a tagged target that runs the other", "test-canary: doctor\n\t@$(MAKE) canary-build\n", []int{2}},
		{"a tagged target that depends on the other", "test-canary: canary-build\n\t@true\n", []int{1}},
		{"a call that also names the tag is one problem", "test-canary: doctor\n\t@$(MAKE) canary-build ataniq_canary\n", []int{2}},
		{"a variable that names a tagged target", "T = test-canary\n", []int{1}},
		{"an include", "include canary.mk\n", []int{1}},
		{"an include that may be missing", "-include canary.mk\n", []int{1}},
		{"a silent include", "sinclude canary.mk\n", []int{1}},
		{"an indented include", "ifdef X\n  include canary.mk\nendif\n", []int{2}},
		{"names that only look like an include", "includedir = x\nINCLUDES = y\n", nil},
		{"the targets in comments and a continued phony line", ".PHONY: test \\\n\ttest-canary\n# test-canary and canary-build\n", nil},
		{"a name that only contains a tagged target's", "test-canary-x: doctor\n\t@true\nmy-canary-build:\n\t@true\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []int
			for _, p := range checkMakefileTag(tc.text) {
				var n int
				if _, err := fmt.Sscanf(p, "Makefile:%d:", &n); err != nil {
					t.Fatalf("problem %q: %v", p, err)
				}
				got = append(got, n)
			}
			if !slices.Equal(got, tc.bad) {
				t.Fatalf("lines %v, want %v", got, tc.bad)
			}
		})
	}
}

func TestCanaryTagNoMentionUnderCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files fstest.MapFS
		bad   []string
	}{
		{"neither folder exists", fstest.MapFS{"Makefile": {Data: []byte("x")}}, nil},
		{"clean folders", fstest.MapFS{"scripts/a.sh": {Data: []byte("echo\n")}, ".github/workflows/test.yml": {Data: []byte("name: test\n")}}, nil},
		{"a script names the tag", fstest.MapFS{"scripts/a.sh": {Data: []byte("go test -tags ataniq_canary\n")}}, []string{"scripts/a.sh"}},
		{"a workflow comment names the tag", fstest.MapFS{".github/workflows/x.yml": {Data: []byte("# ataniq_canary\n")}}, []string{".github/workflows/x.yml"}},
		{"a deep file names the tag", fstest.MapFS{".github/actions/y/action.yml": {Data: []byte("x: ataniq_canary\n")}}, []string{".github/actions/y/action.yml"}},
		{"another folder may", fstest.MapFS{"docs/a.md": {Data: []byte("ataniq_canary\n")}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, p := range checkNoCanaryTagUnder(tc.files, ".github", "scripts") {
				got = append(got, strings.SplitN(p, ":", 2)[0])
			}
			if !slices.Equal(got, tc.bad) {
				t.Fatalf("problems for %v, want %v", got, tc.bad)
			}
		})
	}
}

func TestCanaryTagDependencyCheck(t *testing.T) {
	const m = "example.test/m"
	line := func(pkg, failed string, deps ...string) string {
		return pkg + "|" + failed + "|" + strings.Join(deps, " ") + "\n"
	}
	for _, tc := range []struct {
		name    string
		listing string
		want    int
	}{
		{"no command", "", 0},
		{"commands with ordinary dependencies", line(m+"/cmd/ataniqd", "", "fmt", m+"/internal/leaddaemon", m+"/internal/leadholder/serve") +
			line(m+"/cmd/ataniq-holder", "", m+"/internal/leadholder/eventlog"), 0},
		{"a packages that is not a command is not checked", line(m+"/internal/x", "", m+"/internal/leadcanary"), 0},
		{"a command that needs leadcanary", line(m+"/cmd/ataniq-intake", "", m+"/internal/leadcanary"), 1},
		{"a command that needs a package below leadcanary", line(m+"/cmd/ataniq-intake", "", m+"/internal/leadcanary/preflight"), 1},
		{"each of the four", line(m+"/cmd/a", "", m+"/internal/leadholder/sandboxstart") +
			line(m+"/cmd/b", "", m+"/internal/leadholder/injectbridge") +
			line(m+"/cmd/c", "", m+"/internal/leadholder/canaryroot"), 3},
		{"a package with a similar name", line(m+"/cmd/a", "", m+"/internal/leadcanaryx", m+"/internal/leadholder/sandboxstarter"), 0},
		{"the daemon needs the injection library", line(m+"/cmd/ataniqd", "", m+"/internal/inject"), 1},
		{"the daemon needs a package of it", line(m+"/cmd/ataniqd", "", m+"/internal/inject/deliver"), 1},
		{"another command may need it", line(m+"/cmd/ataniq-holder", "", m+"/internal/inject"), 0},
		{"the daemon and a package with a similar name", line(m+"/cmd/ataniqd", "", m+"/internal/injectbridge"), 0},
		{"a command that does not load", line(m+"/cmd/a", "err"), 1},
		{"an unreadable line", "garbage\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkCanaryCmdDeps(m, tc.listing); len(got) != tc.want {
				t.Fatalf("%d problems, want %d: %v", len(got), tc.want, got)
			}
		})
	}
}

func TestCanaryTagModuleCheck(t *testing.T) {
	for _, tc := range []struct {
		name             string
		untagged, tagged string
		want             []string
	}{
		{"both empty", "", "", nil},
		{"the same", "a\nb\n\n", "b\n\na\n", nil},
		{"fewer with the tag", "a\nb\n", "a\n", nil},
		{"one more with the tag", "a\n", "a\nz\n", []string{"module z is used only with the tag"}},
		{"two more, in order", "a\n", "y\nz\na\n", []string{"module y is used only with the tag", "module z is used only with the tag"}},
		{"nothing without, something with", "", "z\n", []string{"module z is used only with the tag"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkCanaryModules(tc.untagged, tc.tagged)
			if !slices.Equal(got, tc.want) {
				t.Fatalf("%v, want %v", got, tc.want)
			}
		})
	}
}

const goodWorkflow = `# Runs the tests.
name: test

on:
  pull_request:

permissions:
  contents: read

jobs:
  test:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@v4
      - run: make test
  test-canary:
    runs-on: ubuntu-24.04
    timeout-minutes: 30
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@v4
        with:
          persist-credentials: false
      - uses: actions/setup-go@v5
        with:
          go-version-file: .go-version
      - run: make -n bootstrap
      - run: make bootstrap
      - run: make doctor
      - run: make test-canary
`

func TestCanaryTagWorkflowCheck(t *testing.T) {
	edit := func(old, new string) map[string]string {
		if !strings.Contains(goodWorkflow, old) {
			t.Fatalf("the sample workflow lacks %q", old)
		}
		return map[string]string{"test.yml": strings.Replace(goodWorkflow, old, new, 1)}
	}
	for _, tc := range []struct {
		name  string
		files map[string]string
		bad   bool
	}{
		{"the sample", map[string]string{"test.yml": goodWorkflow}, false},
		{"another workflow without the tag", map[string]string{"test.yml": goodWorkflow, "other.yml": "name: other\n"}, false},
		{"a comment that names the tag in the sample is caught by the tag check", edit("# Runs the tests.", "# ataniq_canary"), true},
		{"the tag in a step", edit("- run: make test-canary", "- run: sh scripts/go-tool.sh test -tags ataniq_canary ./..."), true},
		{"no test.yml", map[string]string{"other.yml": "name: other\n"}, true},
		{"no job", map[string]string{"test.yml": strings.Replace(goodWorkflow, "  test-canary:", "  other:", 1)}, true},
		{"the job does not run the target", edit("      - run: make test-canary\n", ""), true},
		{"another workflow runs canary-build", map[string]string{"test.yml": goodWorkflow, "release.yml": "jobs:\n  b:\n    steps:\n      - run: make canary-build\n"}, true},
		{"the first job runs canary-build", edit("- run: make test\n", "- run: make canary-build\n"), true},
		{"a comment that names canary-build is not a run", map[string]string{"test.yml": goodWorkflow, "x.yml": "# make canary-build is for people\nname: x\n"}, false},
		{"an extra step", edit("      - run: make doctor\n", "      - run: make doctor\n      - run: make test-patched\n"), true},
		{"a block run with an extra command", edit("      - run: make doctor\n", "      - run: |\n          make doctor\n          make canary-build\n"), true},
		{"a block run with the allowed commands", edit("      - run: make doctor\n", "      - run: |\n          make doctor\n"), false},
		{"an artifact upload", edit("      - run: make test-canary\n", "      - run: make test-canary\n      - uses: actions/upload-artifact@v4\n"), true},
		{"a cache save", edit("      - run: make test-canary\n", "      - run: make test-canary\n      - uses: actions/cache@v4\n"), true},
		{"a cache save action", edit("      - run: make test-canary\n", "      - run: make test-canary\n      - uses: actions/cache/save@v4\n"), true},
		{"a cache restore is not on the list", edit("      - run: make test-canary\n", "      - run: make test-canary\n      - uses: actions/cache/restore@v4\n"), true},
		{"an action that is not on the list", edit("      - run: make test-canary\n", "      - run: make test-canary\n      - uses: some-org/ship@v1\n"), true},
		{"a composite action of the repository", edit("      - run: make test-canary\n", "      - run: make test-canary\n      - uses: ./.github/actions/b\n"), true},
		{"a reusable workflow as the job", edit("    timeout-minutes: 30\n    permissions", "    uses: ./.github/workflows/other.yml\n    permissions"), true},
		{"the actions on the list at another version", edit("actions/setup-go@v5", "actions/setup-go@v6"), false},
		{"an action that only starts like one on the list", edit("actions/setup-go@v5", "actions/setup-go-x@v5"), true},
		{"an upload in a composite action that the job does not use", map[string]string{"test.yml": goodWorkflow, ".github/actions/b/action.yml": "runs:\n  using: composite\n  steps:\n    - uses: actions/upload-artifact@v4\n"}, false},
		{"a composite action that runs canary-build", map[string]string{"test.yml": goodWorkflow, ".github/actions/b/action.yml": "runs:\n  using: composite\n  steps:\n    - run: make canary-build\n      shell: sh\n"}, true},
		{"a composite action that names the tag", map[string]string{"test.yml": goodWorkflow, ".github/actions/b/action.yml": "runs:\n  using: composite\n  steps:\n    - run: go build -tags ataniq_canary ./...\n      shell: sh\n"}, true},
		{"a composite action that only mentions canary-build in a comment", map[string]string{"test.yml": goodWorkflow, ".github/actions/b/action.yml": "# not canary-build\nruns:\n  using: composite\n  steps: []\n"}, false},
		{"a file that is not yaml names canary-build", map[string]string{"test.yml": goodWorkflow, ".github/scripts/ship.sh": "make canary-build\n"}, true},
		{"another workflow names the tag", map[string]string{"test.yml": goodWorkflow, "other.yml": "# ataniq_canary\n"}, true},
		{"continue-on-error on the job", edit("    timeout-minutes: 30\n    permissions", "    continue-on-error: true\n    permissions"), true},
		{"if on the job", edit("    timeout-minutes: 30\n    permissions", "    if: always()\n    permissions"), true},
		{"if on a step of the job", edit("      - run: make test-canary\n", "      - if: always()\n        run: make test-canary\n"), true},
		{"continue-on-error on a step of the job", edit("      - run: make test-canary\n", "      - run: make test-canary\n        continue-on-error: true\n"), true},
		{"if and continue-on-error in the first job are not this job's", edit("      - run: make test\n", "      - if: github.event_name == 'push'\n        continue-on-error: true\n        run: make test\n"), false},
		{"a run line that runs a file of the run kit", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - run: sh qualification/canary/build.sh\n"}, true},
		{"a block run line that runs a file of the run kit", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - run: |\n          cd .\n          ./qualification/canary/run/x\n"}, true},
		{"a run line that enters the folder of the run kit", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - run: cd qualification/canary\n"}, true},
		{"a run line in a composite action that runs the run kit", map[string]string{"test.yml": goodWorkflow, ".github/actions/b/action.yml": "runs:\n  using: composite\n  steps:\n    - run: sh qualification/canary/x.sh\n      shell: sh\n"}, true},
		{"the run kit named in a comment or a path filter is not a run line", map[string]string{"test.yml": goodWorkflow, "other.yml": "# qualification/canary/x\non:\n  push:\n    paths:\n      - qualification/canary/**\n"}, false},
		{"a folder that only starts like the run kit's", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - run: sh qualification/canary-notes/x.sh\n"}, false},
		{"an artifact upload in the first job is not this job's", edit("      - run: make test\n", "      - run: make test\n      - uses: actions/upload-artifact@v4\n"), false},
		{"write permission", edit("      contents: read\n    steps", "      contents: write\n    steps"), true},
		{"a second permission that writes", edit("      contents: read\n    steps", "      contents: read\n      packages: write\n    steps"), true},
		{"permissions of none", edit("      contents: read\n    steps", "      contents: none\n    steps"), false},
		{"permissions of read-all", edit("    permissions:\n      contents: read\n    steps", "    permissions: read-all\n    steps"), false},
		{"permissions of write-all", edit("    permissions:\n      contents: read\n    steps", "    permissions: write-all\n    steps"), true},
		{"permissions inline", edit("    permissions:\n      contents: read\n    steps", "    permissions: {contents: read}\n    steps"), false},
		{"permissions inline that write", edit("    permissions:\n      contents: read\n    steps", "    permissions: {contents: read, id-token: write}\n    steps"), true},
		{"permissions inherited from a read-only workflow", edit("    permissions:\n      contents: read\n    steps", "    steps"), false},
		{"permissions inherited from a workflow that writes", map[string]string{"test.yml": strings.NewReplacer("    permissions:\n      contents: read\n    steps", "    steps", "\npermissions:\n  contents: read\n", "\npermissions:\n  contents: write\n").Replace(goodWorkflow)}, true},
		{"permissions stated nowhere", map[string]string{"test.yml": strings.NewReplacer("    permissions:\n      contents: read\n    steps", "    steps", "\npermissions:\n  contents: read\n", "\n").Replace(goodWorkflow)}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := tc.files
			problems := checkCanaryWorkflow(files)
			if (len(problems) > 0) != tc.bad {
				t.Fatalf("problems %v, want bad=%v", problems, tc.bad)
			}
		})
	}
}

func TestCanaryTagGithubFiles(t *testing.T) {
	files, err := canaryGithubFiles(fstest.MapFS{
		".github/workflows/test.yml":         {Data: []byte("a")},
		".github/workflows/nested/other.yml": {Data: []byte("b")},
		".github/actions/b/action.yml":       {Data: []byte("c")},
		".github/CODEOWNERS":                 {Data: []byte("d")},
		"docs/x.md":                          {Data: []byte("e")},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"test.yml":                           "a",
		".github/workflows/nested/other.yml": "b",
		".github/actions/b/action.yml":       "c",
		".github/CODEOWNERS":                 "d",
	}
	if !maps.Equal(files, want) {
		t.Fatalf("files %v, want %v", files, want)
	}
	if files, err := canaryGithubFiles(fstest.MapFS{}); err != nil || len(files) != 0 {
		t.Fatalf("no .github: %v, %v", files, err)
	}
}

func TestCanaryTagListRefusesTaggedPackages(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		names   bool
	}{
		{"./...", false},
		{"./cmd/...", false},
		{"./internal/leadholder/...", false},
		{"./internal/leadholder/serve", false},
		{"./cmd/ataniq-canaryx", false},
		{"./tests/", false},
		{"-m", false},
		{"./internal/leadcanary", true},
		{"internal/leadcanary/...", true},
		{"example.test/m/internal/leadcanary/preflight", true},
		{"./internal/leadholder/sandboxstart", true},
		{"./internal/leadholder/injectbridge/...", true},
		{"./internal/leadholder/canaryroot", true},
		{"./cmd/ataniq-canary", true},
		{"./cmd/ataniq-canary-standin", true},
	} {
		if got := namesCanaryPackage(tc.pattern); got != tc.names {
			t.Errorf("namesCanaryPackage(%q) = %v, want %v", tc.pattern, got, tc.names)
		}
	}
}

// make test-canary runs doctor, then the tagged tests of every package that
// holds a file whose constraint names the tag, found at run time, and ./tests/,
// and with no such package runs ./tests/ alone.
func TestCanaryTagMakeTarget(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"no tagged package", map[string]string{
			"internal/a/a.go": "package a\n",
			"tests/x_test.go": "package tests\n\nconst tag = \"ataniq_canary\"\n",
		}, "test -tags ataniq_canary ./tests/"},
		{"tagged packages", map[string]string{
			"internal/leadcanary/a.go":          hdrTagged + "\n\npackage a\n",
			"internal/leadcanary/sub/b_test.go": hdrTagged + "\n\npackage a\n",
			"internal/leadcanary/sub/b.go":      hdrTagged + "\n\npackage a\n",
			"internal/leaddaemon/c.go":          hdrNegated + "\n\npackage a\n",
			"internal/leaddaemon/d.go":          hdrTagged + "\n\npackage a\n",
			"internal/plain/e.go":               "package a\n",
			"internal/leadcanary/testdata/f.go": hdrTagged + "\n\npackage a\n",
			"cmd/ataniqd/main.go":               "// +build ignore\n\npackage main\n",
			"cmd/ataniq-holder/canary.go":       "//go:build linux && ataniq_canary\n\npackage main\n",
			"tests/y_test.go":                   "package tests\n",
		}, "test -tags ataniq_canary ./cmd/ataniq-holder ./internal/leadcanary ./internal/leadcanary/sub ./internal/leaddaemon ./tests/"},
		// Every form Go builds with the tag is found.
		{"every form Go builds with the tag", map[string]string{
			"internal/plusonly/p.go":     hdrPlusTagged + "\n\npackage a\n",
			"internal/plusonly/q.go":     "// +build\tlinux,ataniq_canary\n\npackage a\n",
			"internal/plusonly/r.go":     "//+build linux,ataniq_canary\n\npackage a\n",
			"internal/tabbed/t.go":       "//go:build\tlinux && ataniq_canary\n\npackage a\n",
			"internal/indented/i.go":     "  \t" + hdrTagged + "\n\npackage a\n",
			"internal/nested/build/n.go": hdrTagged + "\n\npackage a\n",
			"build/o.go":                 hdrTagged + "\n\npackage a\n",
			"internal/plain/p.go":        hdrPlusPlain + "\n\npackage a\n",
			"internal/plain/q.go":        "// the ataniq_canary tag is not a constraint\n\npackage a\n",
		}, "test -tags ataniq_canary ./build ./internal/indented ./internal/nested/build ./internal/plusonly ./internal/tabbed ./tests/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, pin := fixture(t)
			makefile, err := os.ReadFile(filepath.Join(root(t), "Makefile"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "Makefile"), makefile, 0644); err != nil {
				t.Fatal(err)
			}
			for name, content := range tc.files {
				full := filepath.Join(dir, name)
				if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(full, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}
			path := fakePath(t, `case "$1" in
version) printf 'go version go`+pin+` linux/amd64\n';;
list|mod) :;;
test) shift; printf '%s\n' "test $*" >> "$LOG";;
*) exit 10;;
esac
`)
			sortPath, err := exec.LookPath("sort")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(sortPath, filepath.Join(path, "sort")); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(t.TempDir(), "log")
			cmd := exec.Command(filepath.Join(path, "make"), "test-canary")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+path, "LOG="+log)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("make test-canary: %s %v", out, err)
			}
			got, err := os.ReadFile(log)
			if err != nil || string(got) != tc.want+"\n" {
				t.Fatalf("go ran %q (%v), want %q\n%s", got, err, tc.want, out)
			}
			if !strings.Contains(string(out), "ok: go version go"+pin) {
				t.Fatalf("doctor did not run first:\n%s", out)
			}
		})
	}
}

// test-canary is a phony target and, like the other test targets, depends on
// doctor.
func TestCanaryTagMakeTargetDeclaration(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(root(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(makefile)
	if !regexp.MustCompile(`(?m)^\.PHONY:.*\btest-canary\b`).MatchString(text) {
		t.Error("test-canary is not in .PHONY")
	}
	if !regexp.MustCompile(`(?m)^test-canary: doctor$`).MatchString(text) {
		t.Error("test-canary does not depend on doctor")
	}
}
