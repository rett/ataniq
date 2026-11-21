package tests

// This file holds the line around the build tag of the owner channel,
// ataniq_ownerchannel, which keeps the daemon's side of the approval card out
// of release builds until the card is qualified and the owner says so. It is
// the counterpart of canary_tag_test.go for that tag and shares its helpers. It
// is not itself behind the tag and passes with and without it, and with the
// canary tag too. Every check is written so that an empty set passes: none of
// the tagged packages need exist. The checks are plain functions over a tree, a
// text or a package listing, and the TestOwnerchannelTag... functions below
// run them over small synthetic inputs, so the guard is itself tested.
//
// The checks are:
//  1. placement: every Go file whose build constraint names the tag lies under
//     the allowed paths and in no folder that ./... skips, needs the tag and
//     linux together, and every file of a wholly tagged package needs the tag;
//  2. the Makefile and the scripts: the tag is named only in the recipe of
//     make test-ownerchannel, which builds nothing, and nowhere under .github
//     or scripts;
//  3. without the tag no command depends on the service, the journal, the host
//     adapter or the stand-in for the door;
//  4. with the tag, and with it and the canary tag, every package loads;
//  5. the tagged builds use no module that the untagged build does not;
//  6. the workflow names the make target and never the tag.

import (
	"bytes"
	"fmt"
	"go/build/constraint"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

const ownerTag = "ataniq_ownerchannel"

// ownerAllowedPaths are the only places a Go file's build constraint may name
// the tag. Each is a folder, with its subfolders. They are the daemon and its
// command, which serve the owner connection, and the host adapter. The service
// and the journal under internal/approval, the console and its client carry no
// tag: they are in the release build.
var ownerAllowedPaths = []string{
	"internal/leaddaemon/",
	"cmd/ataniqd/",
	"internal/approval/hostadapter/",
}

// ownerWholePaths hold packages that have no file outside the tag: an untagged
// build cannot import them, and every Go file in them, tests included, carries
// the tag.
var ownerWholePaths = []string{
	"internal/approval/hostadapter/",
}

// ownerForbiddenDeps are the packages that no command may depend on without
// the tag, as paths below the module: the service, the journal, the host
// adapter and the stand-in for the door (the door's interface package is the
// service's own and is not listed).
var ownerForbiddenDeps = []string{
	"internal/approval/ownerchannel",
	"internal/approval/ownerjournal",
	"internal/approval/hostadapter",
	"internal/approval/door/doortest",
}

// ownerMakeTarget is the one Makefile target whose recipe may name the tag.
const ownerMakeTarget = "test-ownerchannel"

// exprNames reports whether an expression names the tag.
func exprNames(e constraint.Expr, tag string) bool {
	switch e := e.(type) {
	case *constraint.TagExpr:
		return e.Tag == tag
	case *constraint.NotExpr:
		return exprNames(e.X, tag)
	case *constraint.AndExpr:
		return exprNames(e.X, tag) || exprNames(e.Y, tag)
	case *constraint.OrExpr:
		return exprNames(e.X, tag) || exprNames(e.Y, tag)
	}
	return false
}

func exprNamesOwnerTag(e constraint.Expr) bool { return exprNames(e, ownerTag) }

// allHold reports whether every expression is met when the tags are set as the
// function says.
func allHold(exprs []constraint.Expr, on func(string) bool) bool {
	for _, e := range exprs {
		if !e.Eval(on) {
			return false
		}
	}
	return true
}

// ownerNeedsTag reports whether the file is not built when the tag is off,
// every other tag being set.
func ownerNeedsTag(exprs []constraint.Expr) bool {
	return !allHold(exprs, func(tag string) bool { return tag != ownerTag })
}

// ownerSystems are the operating systems a build line may name. Any other tag
// of a build line is taken as set when the line is tried.
var ownerSystems = []string{"linux", "darwin", "windows", "freebsd", "openbsd", "netbsd", "solaris", "plan9", "js", "aix", "dragonfly", "illumos", "android", "ios", "wasip1"}

// ownerBuildsFor reports whether the file is built for the operating system
// with the tag set, every tag that is no operating system being set too.
func ownerBuildsFor(exprs []constraint.Expr, goos string) bool {
	return allHold(exprs, func(tag string) bool {
		return tag == goos || !slices.Contains(ownerSystems, tag)
	})
}

// ownerBuildsOutsideLinux returns an operating system other than linux for
// which the file would be built with the tag set, or "".
func ownerBuildsOutsideLinux(exprs []constraint.Expr) string {
	for _, goos := range ownerSystems[1:] {
		if ownerBuildsFor(exprs, goos) {
			return goos
		}
	}
	return ""
}

// ownerBuildsOnLinux reports whether the file is built for linux with the tag.
func ownerBuildsOnLinux(exprs []constraint.Expr) bool { return ownerBuildsFor(exprs, "linux") }

// checkOwnerPlacement is check (1): a Go file whose constraint names the tag
// lies under ownerAllowedPaths and in no folder that ./... skips, names it in
// the go:build line the go command applies, and when the file needs the tag it
// is built with linux and for no other system; and every Go file of a wholly
// tagged package needs the tag.
func checkOwnerPlacement(fsys fs.FS) []string {
	var problems []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != "." && skipCanaryDir(fsys, p, d.Name()) {
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
		exprs, applied, hasGoBuild, bad := headerConstraintsGo(string(data))
		for _, line := range bad {
			if strings.Contains(line, ownerTag) {
				problems = append(problems, fmt.Sprintf("%s: unreadable build constraint %q", p, line))
			}
		}
		named := slices.ContainsFunc(exprs, exprNamesOwnerTag)
		if hasGoBuild && len(applied) > 1 && slices.ContainsFunc(applied, exprNamesOwnerTag) {
			problems = append(problems, fmt.Sprintf("%s: more than one go:build line, one of which names %s", p, ownerTag))
		}
		if inGoSkippedDir(p) {
			if named {
				problems = append(problems, fmt.Sprintf("%s: the build constraint names %s in a folder that ./... and make test-ownerchannel skip", p, ownerTag))
			}
			return nil
		}
		switch {
		case named && !underAnyPath(p, ownerAllowedPaths):
			problems = append(problems, fmt.Sprintf("%s: the build constraint names %s outside the allowed paths", p, ownerTag))
		case named && !(hasGoBuild && slices.ContainsFunc(applied, exprNamesOwnerTag)):
			problems = append(problems, fmt.Sprintf("%s: %s is named, but not in a go:build line that the go command applies", p, ownerTag))
		case underAnyPath(p, ownerWholePaths) && (!hasGoBuild || !named || !ownerNeedsTag(applied)):
			problems = append(problems, fmt.Sprintf("%s: a file of a wholly tagged package does not require %s", p, ownerTag))
		case named && ownerNeedsTag(applied) && !ownerBuildsOnLinux(applied):
			problems = append(problems, fmt.Sprintf("%s: a file that needs %s is not built for linux with it", p, ownerTag))
		case named && ownerNeedsTag(applied) && ownerBuildsOutsideLinux(applied) != "":
			problems = append(problems, fmt.Sprintf("%s: a file that needs %s is also built for %s", p, ownerTag, ownerBuildsOutsideLinux(applied)))
		}
		return nil
	})
	if err != nil {
		problems = append(problems, "walking the tree: "+err.Error())
	}
	return problems
}

// ownerTargetRef finds the name of the make target as a word.
var ownerTargetRef = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])` + ownerMakeTarget + `($|[^A-Za-z0-9_.-])`)

// ownerGoToolVerb finds the Go command a recipe line runs through the helper.
var ownerGoToolVerb = regexp.MustCompile(`go-tool\.sh\s+(\S+)`)

// checkOwnerMakefile is the first half of check (2): in a Makefile the tag
// appears only inside the recipe of test-ownerchannel, and that recipe runs the
// Go command only as test, so it builds no binary. A comment, a variable, the
// .PHONY line, a prerequisite line and the recipe of any other target are all
// outside. Two more lines are problems, so that no other target can reach the
// tagged one: a mention of the target anywhere but a comment, the .PHONY line
// and the rule line that defines it, and an include, whose file this check does
// not read. The limit of a text check is a name that is assembled at run time
// or by make's own expansion.
func checkOwnerMakefile(text string) []string {
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
		inOwnRecipe := recipe && len(targets) == 1 && targets[0] == ownerMakeTarget
		if problem == "" && strings.Contains(line, ownerTag) && !inOwnRecipe {
			problem = fmt.Sprintf("names %s outside the recipe of %s", ownerTag, ownerMakeTarget)
		}
		if problem == "" && inOwnRecipe {
			for _, m := range ownerGoToolVerb.FindAllStringSubmatch(line, -1) {
				if m[1] != "test" {
					problem = fmt.Sprintf("the recipe of %s runs the Go command %s, and nothing but test is allowed", ownerMakeTarget, m[1])
				}
			}
			if problem == "" && regexp.MustCompile(`(^|[^A-Za-z0-9_-])go\s+(build|install|run|generate)\b`).MatchString(line) {
				problem = "the recipe of " + ownerMakeTarget + " runs go directly to build or run"
			}
		}
		if problem == "" && ownerTargetRef.MatchString(line) && !strings.HasPrefix(trimmed, "#") && !phony {
			own := false
			if m := makeRuleLine.FindStringSubmatch(line); m != nil && !recipe && !wasContinued {
				own = !ownerTargetRef.MatchString(line[len(m[1])+1:])
			}
			if !own {
				problem = "another line names " + ownerMakeTarget
			}
		}
		if problem != "" {
			problems = append(problems, fmt.Sprintf("Makefile:%d: %s", i+1, problem))
		}
	}
	return problems
}

// checkNoOwnerTagUnder is the second half of check (2): no file under the
// folders names the tag.
func checkNoOwnerTagUnder(fsys fs.FS, folders ...string) []string {
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
			if bytes.Contains(data, []byte(ownerTag)) {
				problems = append(problems, fmt.Sprintf("%s: names %s", p, ownerTag))
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) && !strings.Contains(err.Error(), "file does not exist") {
			problems = append(problems, folder+": "+err.Error())
		}
	}
	return problems
}

// checkOwnerCmdDeps is check (3): without the tag, no command depends on a
// package of ownerForbiddenDeps, and every command loads. The listing is of the
// commands, with their dependencies, in canaryDepsFormat.
func checkOwnerCmdDeps(module, listing string) []string {
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
			for _, forbidden := range ownerForbiddenDeps {
				if pathIsOrUnder(dep, module+"/"+forbidden) {
					problems = append(problems, fmt.Sprintf("%s depends on %s without the tag", pkg, dep))
				}
			}
		}
	}
	return problems
}

// checkOwnerLoads is check (4): no package of the listing, in canaryDepsFormat,
// fails to load, itself or in a dependency, with the given tags set.
func checkOwnerLoads(tags, listing string) []string {
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
		if parts[1] != "" {
			problems = append(problems, fmt.Sprintf("%s does not load with the tags %s", parts[0], tags))
		}
	}
	return problems
}

// ownerWorkflowJob is the key of the job that runs the make target.
const ownerWorkflowJob = "test-ownerchannel"

// ownerJobKeys are the only keys the job, its steps and their with blocks may
// hold, for the reasons canaryJobKeys gives. The list is flat.
var ownerJobKeys = []string{ownerWorkflowJob, "runs-on", "timeout-minutes", "permissions", "contents", "steps", "name",
	"uses", "with", "run", "persist-credentials", "fetch-depth", "fetch-tags", "go-version-file"}

// ownerJobRuns are the only commands the job runs.
var ownerJobRuns = []string{"make -n bootstrap", "make bootstrap", "make doctor", "make " + ownerMakeTarget}

// checkOwnerWorkflow is check (6). Its input is every file under .github, as
// for checkCanaryWorkflow. No file names the tag, even in a comment. test.yml
// has a job test-ownerchannel that runs the make target and never names the
// tag, runs and uses only what the canary job does, has no if or
// continue-on-error that could keep it from failing, has read-only permissions,
// is written in no form these checks do not read, and holds only the keys of
// ownerJobKeys, test.yml itself only the top-level keys of canaryWorkflowKeys.
// The limits of checkCanaryWorkflow hold here too.
func checkOwnerWorkflow(files map[string]string) []string {
	var problems []string
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if strings.Contains(files[name], ownerTag) {
			problems = append(problems, name+": names the tag "+ownerTag)
		}
	}
	text, ok := files["test.yml"]
	if !ok {
		return append(problems, "test.yml: missing")
	}
	job, ok := workflowJobLines(text, ownerWorkflowJob)
	if !ok {
		return append(problems, "test.yml: no job "+ownerWorkflowJob)
	}
	runs := workflowRunCommands(job)
	if !slices.Contains(runs, "make "+ownerMakeTarget) {
		problems = append(problems, fmt.Sprintf("test.yml: the %s job does not run make %s", ownerWorkflowJob, ownerMakeTarget))
	}
	for _, run := range runs {
		if !slices.Contains(ownerJobRuns, run) {
			problems = append(problems, fmt.Sprintf("test.yml: the %s job runs %q, and nothing but %s is allowed", ownerWorkflowJob, run, strings.Join(ownerJobRuns, ", ")))
		}
	}
	for _, use := range workflowUses(job) {
		if !slices.ContainsFunc(canaryJobUses, func(prefix string) bool { return strings.HasPrefix(use, prefix) }) {
			problems = append(problems, fmt.Sprintf("test.yml: the %s job uses %s, and nothing but %s is allowed", ownerWorkflowJob, use, strings.Join(canaryJobUses, ", ")))
		}
	}
	forms := workflowUnreadForms(job)
	for _, form := range forms {
		problems = append(problems, fmt.Sprintf("test.yml: the %s job has %q, a form this check does not read", ownerWorkflowJob, form))
	}
	for _, line := range workflowLines(job) {
		switch {
		case line.key == "" && !slices.Contains(forms, line.text):
			problems = append(problems, fmt.Sprintf("test.yml: the %s job has the line %q, which does not start with a key this check reads", ownerWorkflowJob, line.text))
		case line.key != "" && !slices.Contains(ownerJobKeys, line.key):
			problems = append(problems, fmt.Sprintf("test.yml: the %s job has the key %s, and nothing but %s is allowed", ownerWorkflowJob, line.key, strings.Join(ownerJobKeys, ", ")))
		}
	}
	for _, line := range workflowLines(strings.Split(text, "\n")) {
		switch {
		case line.indent != 0:
		case line.key == "":
			problems = append(problems, fmt.Sprintf("test.yml: the workflow has the top-level line %q, which does not start with a key this check reads", line.text))
		case !slices.Contains(canaryWorkflowKeys, line.key):
			problems = append(problems, fmt.Sprintf("test.yml: the workflow has the top-level key %s, which applies to the %s job too, and nothing but %s is allowed", line.key, ownerWorkflowJob, strings.Join(canaryWorkflowKeys, ", ")))
		}
	}
	for _, raw := range job {
		key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(stripYAMLComment(raw)), "- "))
		if canaryJobGuardKey.MatchString(key) {
			problems = append(problems, fmt.Sprintf("test.yml: the %s job has %q, which could keep it from failing", ownerWorkflowJob, key))
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
		problems = append(problems, fmt.Sprintf("test.yml: the %s job is not read-only: %s", ownerWorkflowJob, msg))
	}
	return problems
}

// namesOwnerPackage reports whether a go list pattern names a wholly tagged
// package or something below one.
func namesOwnerPackage(pattern string) bool {
	p := "/" + strings.TrimPrefix(pattern, "./") + "/"
	for _, whole := range ownerWholePaths {
		if strings.Contains(p, "/"+whole) {
			return true
		}
	}
	return false
}

// ownerGoList runs go list in the repository, offline, with the given tags (a
// comma separated list, or none). Without the tag it refuses a pattern that
// names a wholly tagged package: with no Go file left, that is an error, and
// the checks must not depend on it.
func ownerGoList(t *testing.T, tags string, flags []string, patterns ...string) string {
	t.Helper()
	args := []string{"list"}
	if tags != "" {
		args = append(args, "-tags", tags)
	} else {
		for _, pattern := range patterns {
			if namesOwnerPackage(pattern) {
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

// ownerTagSets are the two tag settings the tagged build is checked under.
var ownerTagSets = []string{ownerTag, ownerTag + "," + canaryTag}

// TestOwnerchannelTag holds the checks of the build tag on the repository.
func TestOwnerchannelTag(t *testing.T) {
	dir := root(t)
	repo := os.DirFS(dir)

	t.Run("1_placement", func(t *testing.T) {
		reportCanaryProblems(t, checkOwnerPlacement(repo))
	})

	t.Run("2_makefile_and_scripts", func(t *testing.T) {
		makefile, err := os.ReadFile(filepath.Join(dir, "Makefile"))
		if err != nil {
			t.Fatal(err)
		}
		reportCanaryProblems(t, checkOwnerMakefile(string(makefile)))
		reportCanaryProblems(t, checkNoOwnerTagUnder(repo, ".github", "scripts"))
	})

	t.Run("3_untagged_dependencies", func(t *testing.T) {
		module := strings.TrimSpace(ownerGoList(t, "", nil, "-m"))
		listing := ownerGoList(t, "", []string{"-e", "-f", canaryDepsFormat}, "./cmd/...")
		reportCanaryProblems(t, checkOwnerCmdDeps(module, listing))
	})

	t.Run("4_tagged_builds_load", func(t *testing.T) {
		for _, tags := range ownerTagSets {
			listing := ownerGoList(t, tags, []string{"-e", "-f", canaryDepsFormat}, "./...")
			reportCanaryProblems(t, checkOwnerLoads(tags, listing))
		}
	})

	t.Run("5_modules", func(t *testing.T) {
		flags := []string{"-deps", "-test", "-f", "{{with .Module}}{{.Path}}{{end}}"}
		untagged := ownerGoList(t, "", flags, "./...")
		for _, tags := range ownerTagSets {
			reportCanaryProblems(t, checkCanaryModules(untagged, ownerGoList(t, tags, flags, "./...")))
		}
	})

	t.Run("6_workflow", func(t *testing.T) {
		files, err := canaryGithubFiles(repo)
		if err != nil {
			t.Fatal(err)
		}
		reportCanaryProblems(t, checkOwnerWorkflow(files))
	})
}

// The constraint lines of the sample Go files. They are built from strings so
// that no line of this file starts with a constraint.
const (
	ownHdrTagged    = "//go:build linux && ataniq_ownerchannel"
	ownHdrNegated   = "//go:build linux && !ataniq_ownerchannel"
	ownHdrNoLinux   = "//go:build ataniq_ownerchannel"
	ownHdrOrSystem  = "//go:build ataniq_ownerchannel && (linux || darwin)"
	ownHdrNotLinux  = "//go:build !linux && ataniq_ownerchannel"
	ownHdrBothTags  = "//go:build linux && ataniq_ownerchannel && ataniq_canary"
	ownHdrOrTag     = "//go:build linux && (ataniq_ownerchannel || ataniq_canary)"
	ownHdrPlain     = "//go:build linux"
	ownHdrPlusOwn   = "// +build linux,ataniq_ownerchannel"
	ownHdrCanaryTag = "//go:build linux && ataniq_canary"
)

func TestOwnerchannelTagPlacementCheck(t *testing.T) {
	src := func(header string) *fstest.MapFile { return goSource(header) }
	for _, tc := range []struct {
		name  string
		files fstest.MapFS
		want  string // a substring of the one problem, or "" for none
	}{
		{"an empty tree", fstest.MapFS{}, ""},
		{"no tagged file", fstest.MapFS{"internal/a/a.go": src("")}, ""},
		{"a tagged file in the daemon", fstest.MapFS{"internal/leaddaemon/o.go": src(ownHdrTagged)}, ""},
		{"a tagged file in the daemon's folder below", fstest.MapFS{"internal/leaddaemon/sub/o.go": src(ownHdrTagged)}, ""},
		{"a tagged file in the daemon command", fstest.MapFS{"cmd/ataniqd/o.go": src(ownHdrTagged)}, ""},
		{"a tagged file of the host adapter", fstest.MapFS{"internal/approval/hostadapter/a.go": src(ownHdrTagged)}, ""},
		{"a tagged test file of the host adapter", fstest.MapFS{"internal/approval/hostadapter/a_test.go": src(ownHdrTagged)}, ""},
		{"a file that needs both tags", fstest.MapFS{"internal/leaddaemon/o.go": src(ownHdrBothTags)}, ""},
		{"a file for either tag is built without this one", fstest.MapFS{"internal/leaddaemon/o.go": src(ownHdrOrTag)}, ""},
		{"the release counterpart that forbids the tag", fstest.MapFS{"internal/leaddaemon/o.go": src(ownHdrNegated)}, ""},
		{"a file that names only the canary tag", fstest.MapFS{"internal/a/a.go": src(ownHdrCanaryTag)}, ""},
		{"a plain linux file", fstest.MapFS{"internal/a/a.go": src(ownHdrPlain)}, ""},
		{"the service is untagged", fstest.MapFS{"internal/approval/ownerchannel/s.go": src("")}, ""},
		{"the service tagged", fstest.MapFS{"internal/approval/ownerchannel/s.go": src(ownHdrTagged)}, "outside the allowed paths"},
		{"the journal tagged", fstest.MapFS{"internal/approval/ownerjournal/s.go": src(ownHdrTagged)}, "outside the allowed paths"},
		{"the console client tagged", fstest.MapFS{"internal/consoleclient/c.go": src(ownHdrTagged)}, "outside the allowed paths"},
		{"the console command tagged", fstest.MapFS{"cmd/ataniq-console/c.go": src(ownHdrTagged)}, "outside the allowed paths"},
		{"a folder that only starts like an allowed one", fstest.MapFS{"internal/leaddaemonx/o.go": src(ownHdrTagged)}, "outside the allowed paths"},
		{"a command that only starts like an allowed one", fstest.MapFS{"cmd/ataniqd-x/o.go": src(ownHdrTagged)}, "outside the allowed paths"},
		{"a tagged test file outside", fstest.MapFS{"tests/x_test.go": src(ownHdrTagged)}, "outside the allowed paths"},
		{"a tagged file in testdata", fstest.MapFS{"internal/leaddaemon/testdata/o.go": src(ownHdrTagged)}, "skip"},
		{"a tagged file in a dot folder", fstest.MapFS{"internal/leaddaemon/.x/o.go": src(ownHdrTagged)}, "skip"},
		{"a tagged file in an underscore folder", fstest.MapFS{"internal/leaddaemon/_x/o.go": src(ownHdrTagged)}, "skip"},
		{"a tagged file in the build output folder is not read", fstest.MapFS{"build/o.go": src(ownHdrTagged)}, ""},
		{"a nested module under a hidden folder is not read", fstest.MapFS{
			".tool/w/go.mod":        {Data: []byte("module x\n")},
			".tool/w/internal/o.go": src(ownHdrTagged),
		}, ""},
		{"a plus build line alone", fstest.MapFS{"internal/leaddaemon/o.go": src(ownHdrPlusOwn)}, "not in a go:build line"},
		{"a plus build line after a go:build line that does not name it", fstest.MapFS{
			"internal/leaddaemon/o.go": src(ownHdrPlain + "\n" + ownHdrPlusOwn),
		}, "not in a go:build line"},
		{"two go:build lines, one naming the tag", fstest.MapFS{
			"internal/leaddaemon/o.go": src(ownHdrTagged + "\n" + ownHdrPlain),
		}, "more than one go:build line"},
		{"a constraint that does not parse", fstest.MapFS{
			"internal/leaddaemon/o.go": src("//go:build linux && && ataniq_ownerchannel"),
		}, "unreadable build constraint"},
		{"a mention of the tag in a comment is no constraint", fstest.MapFS{
			"internal/a/a.go": src("// the ataniq_ownerchannel tag is described elsewhere"),
		}, ""},
		{"a tag without linux", fstest.MapFS{"internal/leaddaemon/o.go": src(ownHdrNoLinux)}, "also built for"},
		{"a tag that is not for linux", fstest.MapFS{"internal/leaddaemon/o.go": src(ownHdrNotLinux)}, "not built for linux with it"},
		{"a tag for linux or another system", fstest.MapFS{"internal/leaddaemon/o.go": src(ownHdrOrSystem)}, "also built for darwin"},
		// The host adapter holds no file outside the tag.
		{"an untagged file in the host adapter", fstest.MapFS{"internal/approval/hostadapter/a.go": src("")}, "does not require"},
		{"a linux only file in the host adapter", fstest.MapFS{"internal/approval/hostadapter/a.go": src(ownHdrPlain)}, "does not require"},
		{"a release counterpart in the host adapter", fstest.MapFS{"internal/approval/hostadapter/a.go": src(ownHdrNegated)}, "does not require"},
		{"an untagged test file in the host adapter", fstest.MapFS{"internal/approval/hostadapter/a_test.go": src("")}, "does not require"},
		{"a plus build line alone in the host adapter", fstest.MapFS{"internal/approval/hostadapter/a.go": src(ownHdrPlusOwn)}, "not in a go:build line"},
		{"an untagged file in a folder below the host adapter", fstest.MapFS{"internal/approval/hostadapter/sub/a.go": src("")}, "does not require"},
		{"a file that is not Go", fstest.MapFS{"internal/approval/hostadapter/a.txt": {Data: []byte("x")}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkOwnerPlacement(tc.files)
			switch {
			case tc.want == "" && len(got) != 0:
				t.Errorf("want no problem, got %q", got)
			case tc.want != "" && (len(got) != 1 || !strings.Contains(got[0], tc.want)):
				t.Errorf("want one problem with %q, got %q", tc.want, got)
			}
		})
	}
}

func TestOwnerchannelTagMakefileCheck(t *testing.T) {
	good := ".PHONY: test test-ownerchannel\n" +
		"\n# comment that names test-ownerchannel\n" +
		"test-ownerchannel: doctor\n" +
		"\t@own=$$(grep ataniq_ownerchannel .); \\\n" +
		"\tsh scripts/go-tool.sh test -tags ataniq_ownerchannel $$own ./tests/ && \\\n" +
		"\tsh scripts/go-tool.sh test -tags ataniq_ownerchannel,ataniq_canary ./tests/\n" +
		"\ntest: doctor\n\t@sh scripts/go-tool.sh test ./...\n"
	// ownRecipe adds a line to the recipe of the target.
	ownRecipe := func(line string) string {
		return strings.Replace(good, "ataniq_canary ./tests/\n\ntest:", "ataniq_canary ./tests/\n"+line+"\ntest:", 1)
	}
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{"an empty Makefile", "", ""},
		{"the sample", good, ""},
		{"the Makefile of the repository's shape without the target", "test: doctor\n\t@sh scripts/go-tool.sh test ./...\n", ""},
		{"the tag in a variable", "TAGS = ataniq_ownerchannel\n" + good, "outside the recipe"},
		{"the tag in a comment", "# ataniq_ownerchannel\n" + good, "outside the recipe"},
		{"the tag in the phony line", strings.Replace(good, ".PHONY: test", ".PHONY: ataniq_ownerchannel test", 1), "outside the recipe"},
		{"the tag in the recipe of the test target", good + "\t@sh scripts/go-tool.sh test -tags ataniq_ownerchannel ./...\n", "outside the recipe"},
		{"the tag in another target", good + "other: doctor\n\t@sh scripts/go-tool.sh test -tags ataniq_ownerchannel ./...\n", "outside the recipe"},
		{"the tag in a recipe shared by two targets", strings.Replace(good, "test-ownerchannel: doctor", "test-ownerchannel other: doctor", 1), "outside the recipe"},
		{"the tag in the canary build", good + "canary-build: doctor\n\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel ./cmd/ataniqd\n", "outside the recipe"},
		{"the tag in a prerequisite line", good + "other: ataniq_ownerchannel\n", "outside the recipe"},
		{"a build in the recipe", ownRecipe("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel ./cmd/ataniqd\n"), "nothing but test"},
		{"an install in the recipe", ownRecipe("\t@sh scripts/go-tool.sh install ./cmd/ataniqd\n"), "nothing but test"},
		{"a direct go build in the recipe", ownRecipe("\t@go build -tags ataniq_ownerchannel ./cmd/ataniqd\n"), "runs go directly"},
		{"a direct go run in the recipe", ownRecipe("\t@go  run ./cmd/ataniqd\n"), "runs go directly"},
		{"another target depends on it", good + "release: test-ownerchannel\n", "another line names"},
		{"another target runs it", good + "release: doctor\n\t@$(MAKE) test-ownerchannel\n", "another line names"},
		{"the test target runs it", strings.Replace(good, "test: doctor\n", "test: doctor test-ownerchannel\n", 1), "another line names"},
		{"a variable names it", "ALL = test-ownerchannel\n" + good, "another line names"},
		{"the target is only a word of another name", good + "other: test-ownerchannels\n", ""},
		{"an include", "include other.mk\n" + good, "includes another makefile"},
		{"an optional include", "-include other.mk\n" + good, "includes another makefile"},
		{"the line after a continued phony line", ".PHONY: test \\\n test-ownerchannel\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkOwnerMakefile(tc.text)
			switch {
			case tc.want == "" && len(got) != 0:
				t.Errorf("want no problem, got %q", got)
			case tc.want != "" && (len(got) == 0 || !strings.Contains(strings.Join(got, "\n"), tc.want)):
				t.Errorf("want a problem with %q, got %q", tc.want, got)
			}
		})
	}
}

func TestOwnerchannelTagNoMentionUnderCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files fstest.MapFS
		want  []string
	}{
		{"clean folders", fstest.MapFS{"scripts/a.sh": {Data: []byte("echo\n")}, ".github/workflows/test.yml": {Data: []byte("name: test\n")}}, nil},
		{"no folders", fstest.MapFS{"other/a.sh": {Data: []byte("ataniq_ownerchannel\n")}}, nil},
		{"a script names the tag", fstest.MapFS{"scripts/a.sh": {Data: []byte("go test -tags ataniq_ownerchannel\n")}}, []string{"scripts/a.sh"}},
		{"a workflow comment names the tag", fstest.MapFS{".github/workflows/x.yml": {Data: []byte("# ataniq_ownerchannel\n")}}, []string{".github/workflows/x.yml"}},
		{"a nested file under .github", fstest.MapFS{".github/actions/b/action.yml": {Data: []byte("run: ataniq_ownerchannel\n")}}, []string{".github/actions/b/action.yml"}},
		{"the canary tag is not this tag", fstest.MapFS{"scripts/a.sh": {Data: []byte("ataniq_canary\n")}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkNoOwnerTagUnder(tc.files, ".github", "scripts")
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want files %q", got, tc.want)
			}
			for i, w := range tc.want {
				if !strings.HasPrefix(got[i], w+":") {
					t.Errorf("problem %d is %q, want it to name %s", i, got[i], w)
				}
			}
		})
	}
}

func TestOwnerchannelTagDependencyCheck(t *testing.T) {
	const module = "example.test/m"
	line := func(pkg, failed string, deps ...string) string {
		return pkg + "|" + failed + "|" + strings.Join(deps, " ") + "\n"
	}
	for _, tc := range []struct {
		name    string
		listing string
		want    string
	}{
		{"an empty listing", "", ""},
		{"a command with plain dependencies", line(module+"/cmd/ataniqd", "", "fmt", module+"/internal/leaddaemon"), ""},
		{"the console command uses the sample backend", line(module+"/cmd/ataniq-console", "", module+"/internal/approval/demo", module+"/internal/approval/card"), ""},
		{"a command uses the door's interface package", line(module+"/cmd/ataniqd", "", module+"/internal/approval/door"), ""},
		{"a command uses the service", line(module+"/cmd/ataniqd", "", module+"/internal/approval/ownerchannel"), "depends on " + module + "/internal/approval/ownerchannel without the tag"},
		{"a command uses the journal", line(module+"/cmd/ataniq-console", "", module+"/internal/approval/ownerjournal"), "ownerjournal"},
		{"a command uses the host adapter", line(module+"/cmd/ataniqd", "", module+"/internal/approval/hostadapter"), "hostadapter"},
		{"a command uses a package below the host adapter", line(module+"/cmd/ataniqd", "", module+"/internal/approval/hostadapter/sub"), "hostadapter/sub"},
		{"a command uses the stand-in", line(module+"/cmd/ataniqd", "", module+"/internal/approval/door/doortest"), "doortest"},
		{"a package that only starts like a forbidden one", line(module+"/cmd/ataniqd", "", module+"/internal/approval/ownerchannelx"), ""},
		{"a library depends on the service", line(module+"/internal/leaddaemon", "", module+"/internal/approval/ownerchannel"), ""},
		{"a command that does not load", line(module+"/cmd/ataniqd", "err", "fmt"), "does not load"},
		{"an unreadable line", "no bars here\n", "unreadable listing line"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkOwnerCmdDeps(module, tc.listing)
			switch {
			case tc.want == "" && len(got) != 0:
				t.Errorf("want no problem, got %q", got)
			case tc.want != "" && (len(got) != 1 || !strings.Contains(got[0], tc.want)):
				t.Errorf("want one problem with %q, got %q", tc.want, got)
			}
		})
	}
}

func TestOwnerchannelTagLoadsCheck(t *testing.T) {
	for _, tc := range []struct {
		name    string
		listing string
		want    string
	}{
		{"an empty listing", "", ""},
		{"packages that load", "a|| fmt\nb||\n", ""},
		{"a package that does not load", "a||\nb|err|fmt\n", "b does not load with the tags t1,t2"},
		{"an unreadable line", "x\n", "unreadable listing line"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkOwnerLoads("t1,t2", tc.listing)
			switch {
			case tc.want == "" && len(got) != 0:
				t.Errorf("want no problem, got %q", got)
			case tc.want != "" && (len(got) != 1 || got[0] != tc.want && !strings.Contains(got[0], tc.want)):
				t.Errorf("want one problem with %q, got %q", tc.want, got)
			}
		})
	}
}

func TestOwnerchannelTagModuleCheck(t *testing.T) {
	// The module check is the canary guard's, called here for each tag setting:
	// a module that only a tagged build uses is a problem.
	if got := checkCanaryModules("a\nb\n", "a\nb\n"); len(got) != 0 {
		t.Errorf("same modules: %q", got)
	}
	if got := checkCanaryModules("a\n", "a\nb\n"); len(got) != 1 || !strings.Contains(got[0], "module b") {
		t.Errorf("an extra module: %q", got)
	}
	if got := checkCanaryModules("a\nb\n", "a\n"); len(got) != 0 {
		t.Errorf("a module that only the untagged build uses is no problem: %q", got)
	}
}

// ownerGoodWorkflow is a workflow file with the three jobs.
const ownerGoodWorkflow = `name: test

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
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@v4
      - run: make test-canary
  test-ownerchannel:
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
      - run: make test-ownerchannel
`

func TestOwnerchannelTagWorkflowCheck(t *testing.T) {
	edit := func(old, new string) map[string]string {
		if !strings.Contains(ownerGoodWorkflow, old) {
			t.Fatalf("the sample workflow lacks %q", old)
		}
		return map[string]string{"test.yml": strings.Replace(ownerGoodWorkflow, old, new, 1)}
	}
	job := "      - run: make test-ownerchannel\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"the sample", map[string]string{"test.yml": ownerGoodWorkflow}, ""},
		{"another workflow without the tag", map[string]string{"test.yml": ownerGoodWorkflow, "other.yml": "name: other\n"}, ""},
		{"no test.yml", map[string]string{"other.yml": "name: other\n"}, "test.yml: missing"},
		{"no job", edit("  test-ownerchannel:", "  other:"), "no job test-ownerchannel"},
		{"the job does not run the target", edit(job, "      - run: make doctor\n"), "does not run make test-ownerchannel"},
		{"the job runs the canary target instead", edit(job, "      - run: make test-canary\n"), "does not run make test-ownerchannel"},
		{"the job names the tag", edit(job, job+"      - run: go test -tags ataniq_ownerchannel ./...\n"), "names the tag"},
		{"the job names the tag in a comment", edit(job, "      # ataniq_ownerchannel\n"+job), "names the tag"},
		{"another workflow names the tag", map[string]string{"test.yml": ownerGoodWorkflow, "other.yml": "# ataniq_ownerchannel\n"}, "other.yml: names the tag"},
		{"the job runs another command", edit(job, job+"      - run: make test\n"), "runs \"make test\""},
		{"the job builds", edit(job, job+"      - run: make canary-build\n"), "runs \"make canary-build\""},
		{"the job uploads", edit(job, job+"      - uses: actions/upload-artifact@v4\n"), "uses actions/upload-artifact@v4"},
		{"the job uses a local action", edit(job, job+"      - uses: ./.github/actions/x\n"), "uses ./.github/actions/x"},
		{"a continue-on-error", edit("    timeout-minutes: 30\n    permissions:\n      contents: read\n    steps:\n      - uses: actions/checkout@v4\n        with:\n          persist-credentials", "    continue-on-error: true\n    timeout-minutes: 30\n    permissions:\n      contents: read\n    steps:\n      - uses: actions/checkout@v4\n        with:\n          persist-credentials"), "continue-on-error"},
		{"an if on a step", edit(job, "        if: always()\n"+job), "which could keep it from failing"},
		{"an environment in the job", edit("    timeout-minutes: 30\n", "    timeout-minutes: 30\n    env:\n      MAKEFLAGS: n\n"), "has the key env"},
		{"an environment in the workflow", edit("\npermissions:\n  contents: read\n", "\nenv:\n  MAKEFLAGS: n\npermissions:\n  contents: read\n"), "top-level key env"},
		{"a default shell in the workflow", edit("\npermissions:\n  contents: read\n", "\ndefaults:\n  run:\n    shell: true {0}\npermissions:\n  contents: read\n"), "top-level key defaults"},
		{"a working directory in a step", edit(job, "        working-directory: build\n"+job), "has the key working-directory"},
		{"a flow mapping step", edit(job, job+"      - {run: make test}\n"), "a form this check does not read"},
		{"write permission", edit("    permissions:\n      contents: read\n    steps:\n      - uses: actions/checkout@v4\n        with:\n          persist-credentials", "    permissions:\n      contents: write\n    steps:\n      - uses: actions/checkout@v4\n        with:\n          persist-credentials"), "not read-only"},
		{"permissions stated nowhere", map[string]string{"test.yml": strings.NewReplacer("    permissions:\n      contents: read\n    steps:\n      - uses: actions/checkout@v4\n        with:", "    steps:\n      - uses: actions/checkout@v4\n        with:", "\npermissions:\n  contents: read\n", "\n").Replace(ownerGoodWorkflow)}, "states its permissions"},
		{"permissions inherited from the workflow", map[string]string{"test.yml": strings.Replace(ownerGoodWorkflow, "    permissions:\n      contents: read\n    steps:\n      - uses: actions/checkout@v4\n        with:", "    steps:\n      - uses: actions/checkout@v4\n        with:", 1)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkOwnerWorkflow(tc.files)
			switch {
			case tc.want == "" && len(got) != 0:
				t.Errorf("want no problem, got %q", got)
			case tc.want != "" && !strings.Contains(strings.Join(got, "\n"), tc.want):
				t.Errorf("want a problem with %q, got %q", tc.want, got)
			}
		})
	}
}

func TestOwnerchannelTagListRefusesTaggedPackages(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		names   bool
	}{
		{"./...", false},
		{"./cmd/...", false},
		{"./internal/approval/...", false},
		{"./internal/approval/ownerchannel", false},
		{"./internal/approval/hostadaptersx", false},
		{"-m", false},
		{"./internal/approval/hostadapter", true},
		{"internal/approval/hostadapter/...", true},
		{"example.test/m/internal/approval/hostadapter/sub", true},
	} {
		if got := namesOwnerPackage(tc.pattern); got != tc.names {
			t.Errorf("namesOwnerPackage(%q) = %v, want %v", tc.pattern, got, tc.names)
		}
	}
}

// ownerCorrectTree is the tree the daemon wire and the host adapter leave: the
// tagged files in the allowed paths, the service and the journal untagged.
func ownerCorrectTree() fstest.MapFS {
	return fstest.MapFS{
		"cmd/ataniqd/main.go":                        goSource(""),
		"cmd/ataniqd/owner_on.go":                    goSource(ownHdrTagged),
		"cmd/ataniqd/owner_off.go":                   goSource(ownHdrNegated),
		"cmd/ataniqd/owner_on_test.go":               goSource(ownHdrTagged),
		"internal/leaddaemon/owner_wire.go":          goSource(ownHdrBothTags),
		"internal/leaddaemon/canary_x.go":            goSource(ownHdrCanaryTag),
		"internal/approval/hostadapter/adapter.go":   goSource(ownHdrTagged),
		"internal/approval/hostadapter/e2e_test.go":  goSource(ownHdrTagged),
		"internal/approval/ownerchannel/service.go":  goSource(""),
		"internal/approval/ownerjournal/journal.go":  goSource(""),
		"internal/leaddaemon/testdata/notes.txt":     {Data: []byte("x")},
		".tool/worktrees/w/go.mod":                   {Data: []byte("module ataniq\n")},
		".tool/worktrees/w/internal/leaddaemon/x.go": goSource(ownHdrTagged),
		"scripts/go-tool.sh":                         {Data: []byte("echo\n")},
		".github/workflows/test.yml":                 {Data: []byte(ownerGoodWorkflow)},
	}
}

func TestOwnerchannelTagCorrectTree(t *testing.T) {
	tree := ownerCorrectTree()
	reportCanaryProblems(t, checkOwnerPlacement(tree))
	reportCanaryProblems(t, checkNoOwnerTagUnder(tree, ".github", "scripts"))
	files, err := canaryGithubFiles(tree)
	if err != nil {
		t.Fatal(err)
	}
	reportCanaryProblems(t, checkOwnerWorkflow(files))
	// The canary guard also accepts the tree: the owner tag adds no problem to
	// its checks.
	reportCanaryProblems(t, checkCanaryPlacement(tree))
	// And one wrong file in it is found.
	tree["internal/approval/ownerchannel/service.go"] = goSource(ownHdrTagged)
	if got := checkOwnerPlacement(tree); len(got) != 1 || !strings.Contains(got[0], "service.go") {
		t.Errorf("the service made a tagged file: %q", got)
	}
}

// The real Makefile has the target in .PHONY, after doctor, and names the tag
// in its recipe only.
func TestOwnerchannelTagMakeTargetDeclaration(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(root(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(makefile)
	if !regexp.MustCompile(`(?m)^\.PHONY:.*\btest-ownerchannel\b`).MatchString(text) {
		t.Error("test-ownerchannel is not in .PHONY")
	}
	if !regexp.MustCompile(`(?m)^test-ownerchannel: doctor$`).MatchString(text) {
		t.Error("test-ownerchannel does not depend on doctor")
	}
	if regexp.MustCompile(`(?m)^(test|test-canary|test-dev|bootstrap|doctor|canary-build)\b[^\n]*:.*\btest-ownerchannel\b`).MatchString(text) {
		t.Error("another target depends on test-ownerchannel")
	}
}

// The instructions say what the target does, in both files that list the
// targets.
func TestOwnerchannelTagInstructions(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"make test-ownerchannel", ownerTag, "make test-canary"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s does not mention %s", name, want)
			}
		}
	}
	agents, err := os.ReadFile(filepath.Join(root(t), "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), "OwnerchannelTag") {
		t.Error("AGENTS.md does not name the targeted check of the tag")
	}
}

// make test-ownerchannel runs doctor, then two runs of the tests, each over
// the packages that hold a file whose constraint names a tag it sets, found at
// run time, and ./tests/. The second run sets the pair of tags. With no such
// package each run is ./tests/ alone, and the second run does not start when
// the first fails.
func TestOwnerchannelTagMakeTarget(t *testing.T) {
	const own, pair = "test -tags ataniq_ownerchannel ", "test -tags ataniq_ownerchannel,ataniq_canary "
	for _, tc := range []struct {
		name  string
		files map[string]string
		first string // the packages of the first run
		both  string // the packages of the second run
	}{
		{"no tagged package", map[string]string{
			"internal/a/a.go": "package a\n",
			"tests/x_test.go": "package tests\n\nconst tag = \"ataniq_ownerchannel\"\n",
		}, "", ""},
		{"each run reads the tags it sets", map[string]string{
			"cmd/ataniqd/o.go":                    ownHdrTagged + "\n\npackage a\n",
			"internal/leaddaemon/o.go":            ownHdrBothTags + "\n\npackage a\n",
			"internal/leaddaemon/c.go":            ownHdrCanaryTag + "\n\npackage a\n",
			"internal/leadcanary/k.go":            ownHdrCanaryTag + "\n\npackage a\n",
			"internal/approval/hostadapter/h.go":  ownHdrTagged + "\n\npackage a\n",
			"internal/approval/ownerchannel/s.go": "package a\n",
			"internal/leaddaemon/n.go":            ownHdrNegated + "\n\npackage a\n",
			"internal/plain/e.go":                 "package a\n",
			"internal/leaddaemon/testdata/f.go":   ownHdrTagged + "\n\npackage a\n",
			"internal/leadcanary/_old/g.go":       ownHdrTagged + "\n\npackage a\n",
			".hidden/a.go":                        ownHdrTagged + "\n\npackage a\n",
			"internal/other/comment.go":           "// the ataniq_ownerchannel tag\n\npackage a\n",
			"tests/y_test.go":                     "package tests\n",
		}, "./cmd/ataniqd ./internal/approval/hostadapter ./internal/leaddaemon ",
			"./cmd/ataniqd ./internal/approval/hostadapter ./internal/leadcanary ./internal/leaddaemon "},
		// Every form Go builds with the tag is found, as in make test-canary.
		{"every form Go builds with the tag", map[string]string{
			"internal/plusonly/p.go":     ownHdrPlusOwn + "\n\npackage a\n",
			"internal/plusonly/q.go":     "// +build\tlinux,ataniq_ownerchannel\n\npackage a\n",
			"internal/tabbed/t.go":       "//go:build\tlinux && ataniq_ownerchannel\n\npackage a\n",
			"internal/indented/i.go":     "  \t" + ownHdrTagged + "\n\npackage a\n",
			"internal/nested/build/n.go": ownHdrTagged + "\n\npackage a\n",
		}, "./internal/indented ./internal/nested/build ./internal/plusonly ./internal/tabbed ",
			"./internal/indented ./internal/nested/build ./internal/plusonly ./internal/tabbed "},
		{"a nested checkout under a hidden folder", map[string]string{
			"cmd/ataniqd/o.go":                   ownHdrTagged + "\n\npackage a\n",
			".tool/worktrees/w/go.mod":           "module ataniq\n",
			".tool/worktrees/w/cmd/ataniqd/o.go": ownHdrTagged + "\n\npackage a\n",
		}, "./cmd/ataniqd ", "./cmd/ataniqd "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := own + tc.first + "./tests/\n" + pair + tc.both + "./tests/\n"
			got, out, err := runOwnerMakeTarget(t, tc.files, "")
			if err != nil {
				t.Fatalf("make test-ownerchannel: %s %v", out, err)
			}
			if got != want {
				t.Fatalf("go ran\n%s\nwant\n%s\n%s", got, want, out)
			}
			if !strings.Contains(out, "ok: go version go") {
				t.Fatalf("doctor did not run first:\n%s", out)
			}
		})
	}

	t.Run("the second run does not start when the first fails", func(t *testing.T) {
		files := map[string]string{"cmd/ataniqd/o.go": ownHdrTagged + "\n\npackage a\n"}
		got, out, err := runOwnerMakeTarget(t, files, "-tags ataniq_ownerchannel ")
		if err == nil {
			t.Fatalf("make test-ownerchannel passed although the first run failed:\n%s", out)
		}
		if want := own + "./cmd/ataniqd ./tests/\n"; got != want {
			t.Fatalf("go ran\n%s\nwant\n%s", got, want)
		}
	})

	t.Run("a failing second run fails the target", func(t *testing.T) {
		_, out, err := runOwnerMakeTarget(t, nil, "-tags ataniq_ownerchannel,")
		if err == nil {
			t.Fatalf("make test-ownerchannel passed although the second run failed:\n%s", out)
		}
	})
}

// runOwnerMakeTarget runs make test-ownerchannel over a tree of the given
// files with a fake Go tool, which logs each test run and fails a run whose
// arguments start with failPrefix, when that is not empty. It returns the log.
func runOwnerMakeTarget(t *testing.T, files map[string]string, failPrefix string) (string, string, error) {
	t.Helper()
	dir, pin := fixture(t)
	makefile, err := os.ReadFile(filepath.Join(root(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), makefile, 0644); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
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
test) shift; printf '%s\n' "test $*" >> "$LOG"
	case "$*" in "$FAIL"*) [ -z "$FAIL" ] || exit 1;; esac;;
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
	cmd := exec.Command(filepath.Join(path, "make"), "test-ownerchannel")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+path, "LOG="+log, "FAIL="+failPrefix)
	out, err := cmd.CombinedOutput()
	got, _ := os.ReadFile(log)
	return string(got), string(out), err
}
