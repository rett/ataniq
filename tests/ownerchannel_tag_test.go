package tests

// This file holds the line around the build tag of the owner channel,
// ataniq_ownerchannel, which keeps the daemon's side of the approval card out
// of release builds until the card is qualified and the owner says so. It is
// the counterpart of canary_tag_test.go for that tag and shares its helpers. It
// is not itself behind the tag and passes with and without it, and with the
// canary tag too. Every check is written so that an empty set passes: none of
// the tagged packages need exist. The checks are plain functions over a tree, a
// text or a package listing, and the TestOwnerChannelTag... functions below
// run them over small synthetic inputs, so the guard is itself tested.
//
// The checks are:
//  1. placement: every Go file whose build constraint names the tag lies under
//     the allowed paths and in no folder that ./... skips, needs the tag and
//     linux together, and every file of a wholly tagged package needs the tag;
//  2. the Makefile and the scripts: the tag is named only in the recipe of
//     make test-ownerchannel, which builds nothing, in the recipe of
//     make ownerchannel-build, which builds the daemon alone into build/owner/
//     with the tag alone and runs nothing, and in the recipe of
//     make scratch-seat-build, which is exactly the mkdir of build/scratch-seat
//     and one build of the daemon into it with the tag and the canary tag;
//     nothing names the tag under .github or scripts, nothing but those
//     recipes names either build target, and nothing under .github, scripts or
//     deploy names scratch-seat-build;
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

// ownerMakeTarget is the Makefile target whose recipe may name the tag to run
// the tests; ownerBuildTarget is the one other target whose recipe may name it,
// to build the daemon. No other recipe names it.
const (
	ownerMakeTarget  = "test-ownerchannel"
	ownerBuildTarget = "ownerchannel-build"
)

// ownerBuildDir is the only folder the build target writes to, which git
// ignores, and ownerBuildPackages are the only packages it builds.
const ownerBuildDir = "build/owner/"

var ownerBuildPackages = []string{"./cmd/ataniqd"}

// scratchSeatTarget is the one target that builds a binary with the pair of
// tags, the owner channel's and the canary's: the daemon with the owner channel
// and the scratch leads, for the owner's own trial of real scratch leads with
// the seat gate. Its rule is scratchSeatRule, with doctor its sole prerequisite,
// and its recipe is exactly the two lines of scratchSeatRecipe: the mkdir of
// scratchSeatDir, which git ignores, and one build of ./cmd/ataniqd into it
// with the pair of tags. No other line names it.
const (
	scratchSeatTarget = "scratch-seat-build"
	scratchSeatDir    = "build/scratch-seat/"
	scratchSeatTags   = ownerTag + "," + canaryTag
	scratchSeatRule   = scratchSeatTarget + ": doctor"
)

var scratchSeatRecipe = []string{
	"@mkdir -p " + "build/scratch-seat",
	"@sh scripts/go-tool.sh build -tags " + scratchSeatTags + " -trimpath -o " + scratchSeatDir + "ataniqd ./cmd/ataniqd",
}

// scratchSeatFolders are the folders under which no file names
// scratchSeatTarget outside a comment line: the workflows, the scripts and the
// deployment, so that no CI job, release step, script or deployment builds the
// daemon with the pair of tags.
var scratchSeatFolders = []string{".github", "scripts", "deploy"}

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

// ownerBuildTargetRef finds the name of the build target as a word.
var ownerBuildTargetRef = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])` + ownerBuildTarget + `($|[^A-Za-z0-9_.-])`)

// scratchSeatTargetRef finds the name of the pair's build target as a word.
var scratchSeatTargetRef = regexp.MustCompile(`(^|[^A-Za-z0-9_.-])` + scratchSeatTarget + `($|[^A-Za-z0-9_.-])`)

// scratchSeatLineProblem returns what is wrong with the line of the recipe of
// scratchSeatTarget at index n (from 0), or "": the recipe is exactly the lines
// of scratchSeatRecipe, each after one tab, in that order, and nothing more.
func scratchSeatLineProblem(line string, n int) string {
	if n >= len(scratchSeatRecipe) {
		return fmt.Sprintf("the recipe of %s has the line %q after its %d lines, and nothing but %q is allowed", scratchSeatTarget, strings.TrimSpace(line), len(scratchSeatRecipe), strings.Join(scratchSeatRecipe, "\n"))
	}
	if line != "\t"+scratchSeatRecipe[n] {
		return fmt.Sprintf("the recipe of %s has %q as its line %d, and nothing but %q is allowed", scratchSeatTarget, strings.TrimSpace(line), n+1, scratchSeatRecipe[n])
	}
	return ""
}

// ownerMkdirLine is the one line of the build recipe that is not a build.
var ownerMkdirLine = regexp.MustCompile(`^@?mkdir -p ` + regexp.QuoteMeta(strings.TrimSuffix(ownerBuildDir, "/")) + `$`)

// ownerBuildLineProblem returns what is wrong with one line of the recipe of
// the build target, or "". A line is the mkdir of the output folder or a build
// through the helper whose only flags are the tag, set to this tag alone,
// -trimpath and -o into the output folder, and whose arguments after them are
// packages of ownerBuildPackages: nothing that runs the daemon or any other
// program, no other tag and no other output.
func ownerBuildLineProblem(line string) string {
	body := strings.TrimSpace(line)
	if strings.HasSuffix(body, "\\") {
		return "the recipe of " + ownerBuildTarget + " has a continued line, which this check does not read"
	}
	if ownerMkdirLine.MatchString(body) {
		return ""
	}
	body = strings.TrimPrefix(body, "@")
	prefix := "sh scripts/go-tool.sh build "
	if !strings.HasPrefix(body, prefix) {
		return fmt.Sprintf("the recipe of %s has the line %q, and nothing but the mkdir of %s and a build through scripts/go-tool.sh is allowed", ownerBuildTarget, strings.TrimSpace(line), strings.TrimSuffix(ownerBuildDir, "/"))
	}
	args := strings.Fields(strings.TrimPrefix(body, prefix))
	tags, out, pkgs := "", "", 0
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "-trimpath":
		case a == "-tags" && i+1 < len(args):
			i++
			tags = args[i]
		case a == "-o" && i+1 < len(args):
			i++
			out = args[i]
		case strings.HasPrefix(a, "-"):
			return fmt.Sprintf("the recipe of %s passes %s to the build, and nothing but -tags, -trimpath and -o is allowed", ownerBuildTarget, a)
		case slices.Contains(ownerBuildPackages, a):
			pkgs++
		default:
			return fmt.Sprintf("the recipe of %s builds %s, and nothing but %s is allowed", ownerBuildTarget, a, strings.Join(ownerBuildPackages, ", "))
		}
	}
	switch {
	case tags != ownerTag:
		return fmt.Sprintf("the recipe of %s builds with the tags %q, and nothing but %s is allowed", ownerBuildTarget, tags, ownerTag)
	case !strings.HasPrefix(out, ownerBuildDir) || strings.Contains(out, ".."):
		return fmt.Sprintf("the recipe of %s writes %q, and nothing outside %s is allowed", ownerBuildTarget, out, ownerBuildDir)
	case pkgs == 0:
		return "the recipe of " + ownerBuildTarget + " builds no package"
	}
	return ""
}

// ownerGoToolVerb finds the Go command a recipe line runs through the helper.
var ownerGoToolVerb = regexp.MustCompile(`go-tool\.sh\s+(\S+)`)

// checkOwnerMakefile is the first half of check (2): in a Makefile the tag
// appears only inside the recipe of test-ownerchannel, which runs the Go
// command only as test and so builds no binary, inside the recipe of
// ownerchannel-build, which only makes build/owner/ and builds the daemon into
// it with the tag alone (see ownerBuildLineProblem) and so runs nothing, and
// inside the recipe of scratch-seat-build, which is exactly the two lines of
// scratchSeatRecipe (see scratchSeatLineProblem): the mkdir of
// build/scratch-seat and one build of the daemon into it with the pair of tags.
// A comment, a variable, the .PHONY line, a prerequisite line and the recipe of
// any other target are all outside. Every rule line whose targets hold
// scratch-seat-build is exactly scratchSeatRule, so that it has no other
// prerequisite, shares its recipe with no target and has no recipe on its rule
// line, and there is at most one. Two more lines are problems, so that no other
// target can reach a tagged one: a mention of any of the three targets anywhere
// but a comment, the .PHONY line and the rule line that defines it, and an
// include, whose file this check does not read. The limit of a text check is a
// name that is assembled at run time or by make's own expansion.
func checkOwnerMakefile(text string) []string {
	var problems []string
	var targets []string
	// seatRules are the line numbers of the rules of scratch-seat-build, and
	// seatLines the number of recipe lines of each.
	var seatRules, seatLines []int
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
				if slices.Contains(targets, scratchSeatTarget) {
					seatRules = append(seatRules, i+1)
					seatLines = append(seatLines, 0)
				}
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
		inBuildRecipe := recipe && len(targets) == 1 && targets[0] == ownerBuildTarget
		inSeatRecipe := recipe && len(targets) == 1 && targets[0] == scratchSeatTarget
		seatRule := len(seatRules) > 0 && seatRules[len(seatRules)-1] == i+1
		if problem == "" && strings.Contains(line, ownerTag) && !inOwnRecipe && !inBuildRecipe && !inSeatRecipe {
			problem = fmt.Sprintf("names %s outside the recipes of %s, %s and %s", ownerTag, ownerMakeTarget, ownerBuildTarget, scratchSeatTarget)
		}
		if problem == "" && seatRule && line != scratchSeatRule {
			problem = fmt.Sprintf("the rule of %s is %q, and nothing but %q is allowed: doctor its sole prerequisite, no other target, no recipe on the rule line", scratchSeatTarget, line, scratchSeatRule)
		}
		if problem == "" && seatRule && len(seatRules) > 1 {
			problem = fmt.Sprintf("a second rule of %s, and one is allowed", scratchSeatTarget)
		}
		if inSeatRecipe {
			n := seatLines[len(seatLines)-1]
			seatLines[len(seatLines)-1]++
			if problem == "" {
				problem = scratchSeatLineProblem(line, n)
			}
		}
		if problem == "" && inBuildRecipe {
			if regexp.MustCompile(`(^|[^A-Za-z0-9_-])go\s+\S`).MatchString(line) {
				problem = "the recipe of " + ownerBuildTarget + " runs go directly"
			} else {
				problem = ownerBuildLineProblem(line)
			}
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
		for _, ref := range []struct {
			name string
			re   *regexp.Regexp
		}{{ownerMakeTarget, ownerTargetRef}, {ownerBuildTarget, ownerBuildTargetRef}, {scratchSeatTarget, scratchSeatTargetRef}} {
			if problem == "" && ref.re.MatchString(line) && !strings.HasPrefix(trimmed, "#") && !phony {
				own := false
				if m := makeRuleLine.FindStringSubmatch(line); m != nil && !recipe && !wasContinued {
					own = !ref.re.MatchString(line[len(m[1])+1:])
				}
				if !own {
					problem = "another line names " + ref.name
				}
			}
		}
		if problem != "" {
			problems = append(problems, fmt.Sprintf("Makefile:%d: %s", i+1, problem))
		}
	}
	for k, n := range seatLines {
		if n < len(scratchSeatRecipe) {
			problems = append(problems, fmt.Sprintf("Makefile:%d: the recipe of %s has %d lines, and it is exactly the %d lines %q", seatRules[k], scratchSeatTarget, n, len(scratchSeatRecipe), strings.Join(scratchSeatRecipe, "\n")))
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

// checkNoOwnerBuildUnder is the third part of check (2): no file under the
// folders names the build target outside a comment line, so no workflow, release
// step or script can build the tagged daemon.
func checkNoOwnerBuildUnder(fsys fs.FS, folders ...string) []string {
	return checkNoTargetUnder(fsys, ownerBuildTarget, folders...)
}

// checkNoScratchSeatBuildUnder is the fourth part of check (2): no file under
// the folders names the pair's build target outside a comment line, so no
// workflow, release step, script or deployment can build the daemon with the
// pair of tags.
func checkNoScratchSeatBuildUnder(fsys fs.FS, folders ...string) []string {
	return checkNoTargetUnder(fsys, scratchSeatTarget, folders...)
}

// checkNoTargetUnder reports every line of a file under the folders that names
// the make target outside a comment line.
func checkNoTargetUnder(fsys fs.FS, target string, folders ...string) []string {
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
			for i, line := range strings.Split(string(data), "\n") {
				if t := strings.TrimSpace(line); !strings.HasPrefix(t, "#") && strings.Contains(line, target) {
					problems = append(problems, fmt.Sprintf("%s:%d: names %s", p, i+1, target))
				}
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

// TestOwnerChannelTag holds the checks of the build tag on the repository.
func TestOwnerChannelTag(t *testing.T) {
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
		reportCanaryProblems(t, checkNoOwnerBuildUnder(repo, ".github", "scripts"))
		reportCanaryProblems(t, checkNoScratchSeatBuildUnder(repo, scratchSeatFolders...))
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

func TestOwnerChannelTagPlacementCheck(t *testing.T) {
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

func TestOwnerChannelTagMakefileCheck(t *testing.T) {
	good := ".PHONY: test test-ownerchannel\n" +
		"\n# comment that names test-ownerchannel\n" +
		"test-ownerchannel: doctor\n" +
		"\t@own=$$(grep ataniq_ownerchannel .); \\\n" +
		"\tsh scripts/go-tool.sh test -tags ataniq_ownerchannel $${own:-./tests/} && \\\n" +
		"\tsh scripts/go-tool.sh test -tags ataniq_ownerchannel,ataniq_canary $${both:-./tests/}\n" +
		"\ntest: doctor\n\t@sh scripts/go-tool.sh test ./...\n"
	// ownRecipe adds a line to the recipe of the target.
	ownRecipe := func(line string) string {
		const last = "ataniq_canary $${both:-./tests/}\n"
		return strings.Replace(good, last+"\ntest:", last+line+"\ntest:", 1)
	}
	// withBuild is the sample with the recipe of the build target, whose one
	// build line is buildLine; build replaces that line, and buildRecipe adds a
	// line after it.
	const buildLine = "\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -trimpath -o build/owner/ataniqd ./cmd/ataniqd\n"
	withBuild := strings.Replace(good, ".PHONY: test test-ownerchannel", ".PHONY: test test-ownerchannel ownerchannel-build", 1) +
		"\n# the daemon with the owner channel, into an ignored folder\n" +
		"ownerchannel-build: doctor\n\t@mkdir -p build/owner\n" + buildLine
	build := func(line string) string { return strings.Replace(withBuild, buildLine, line, 1) }
	buildRecipe := func(line string) string { return withBuild + line }
	// withSeat is the sample with both build targets, the second building the
	// daemon with the pair of tags in its one build line, seatLine; seat
	// replaces that line, seatRecipe adds a line after it and seatRule replaces
	// its rule line.
	const seatLine = "\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd\n"
	withSeat := strings.Replace(withBuild, "ownerchannel-build\n", "ownerchannel-build scratch-seat-build\n", 1) +
		"\n# the daemon with both tags, into an ignored folder\n" +
		"scratch-seat-build: doctor\n\t@mkdir -p build/scratch-seat\n" + seatLine
	seat := func(line string) string { return strings.Replace(withSeat, seatLine, line, 1) }
	seatRecipe := func(line string) string { return withSeat + line }
	seatRule := func(rule string) string {
		return strings.Replace(withSeat, "scratch-seat-build: doctor\n", rule+"\n", 1)
	}
	const seatExact = "nothing but \"@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd\" is allowed"
	const seatRuleOnly = "nothing but \"scratch-seat-build: doctor\" is allowed"
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{"an empty Makefile", "", ""},
		{"the sample", good, ""},
		{"the sample with the build target", withBuild, ""},
		{"the build target after the canary build", good + "canary-build: doctor\n\t@sh scripts/go-tool.sh build -tags ataniq_canary ./cmd/ataniqd\n" + strings.TrimPrefix(withBuild, good), ""},
		{"the build with a line without at", build("\tsh scripts/go-tool.sh build -tags ataniq_ownerchannel -o build/owner/ataniqd ./cmd/ataniqd\n"), ""},
		{"the build target runs a test", build("\t@sh scripts/go-tool.sh test -tags ataniq_ownerchannel ./cmd/ataniqd\n"), "nothing but the mkdir"},
		{"the build target builds another package", build("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -trimpath -o build/owner/x ./cmd/ataniq-console\n"), "nothing but ./cmd/ataniqd is allowed"},
		{"the build target builds everything", build("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -trimpath -o build/owner/ ./...\n"), "nothing but ./cmd/ataniqd is allowed"},
		{"the build target also builds a second package", buildRecipe("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -o build/owner/h ./cmd/ataniq-holder\n"), "nothing but ./cmd/ataniqd is allowed"},
		{"the build target writes outside its folder", build("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -o build/ataniqd ./cmd/ataniqd\n"), "nothing outside build/owner/"},
		{"the build target writes into the canary folder", build("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -o build/canary/ataniqd ./cmd/ataniqd\n"), "nothing outside build/owner/"},
		{"the build target climbs out of its folder", build("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -o build/owner/../ataniqd ./cmd/ataniqd\n"), "nothing outside build/owner/"},
		{"the build target has no output", build("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel ./cmd/ataniqd\n"), "nothing outside build/owner/"},
		{"the build target builds with both tags", build("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -o build/owner/ataniqd ./cmd/ataniqd\n"), "nothing but ataniq_ownerchannel is allowed"},
		{"the build target builds with the canary tag", build("\t@sh scripts/go-tool.sh build -tags ataniq_canary -o build/owner/ataniqd ./cmd/ataniqd\n"), "nothing but ataniq_ownerchannel is allowed"},
		{"the build target builds without a tag", build("\t@sh scripts/go-tool.sh build -o build/owner/ataniqd ./cmd/ataniqd\n"), "nothing but ataniq_ownerchannel is allowed"},
		{"the build target builds no package", build("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -o build/owner/ataniqd\n"), "builds no package"},
		{"the build target passes a flag that runs a program", build("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -toolexec x -o build/owner/ataniqd ./cmd/ataniqd\n"), "nothing but -tags, -trimpath and -o"},
		{"the build target installs", build("\t@sh scripts/go-tool.sh install -tags ataniq_ownerchannel ./cmd/ataniqd\n"), "nothing but the mkdir"},
		{"the build target runs go directly", build("\t@go build -tags ataniq_ownerchannel -o build/owner/ataniqd ./cmd/ataniqd\n"), "runs go directly"},
		{"the build target runs the daemon", buildRecipe("\t@build/owner/ataniqd run --owner-channel\n"), "nothing but the mkdir"},
		{"the build target makes another folder", buildRecipe("\t@mkdir -p build/other\n"), "nothing but the mkdir"},
		{"the build target removes things", buildRecipe("\t@rm -rf build\n"), "nothing but the mkdir"},
		{"the build target runs the tests", buildRecipe("\t@$(MAKE) test-ownerchannel\n"), "nothing but the mkdir"},
		{"the build target has a continued line", build("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -o build/owner/ataniqd \\\n\t\t./cmd/ataniqd\n"), "continued line"},
		{"the tag in another target next to the build target", withBuild + "other: doctor\n\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -o build/owner/x ./cmd/ataniqd\n", "outside the recipes"},
		{"the tag in a recipe shared with the build target", strings.Replace(withBuild, "ownerchannel-build: doctor", "ownerchannel-build other: doctor", 1), "outside the recipes"},
		{"another target depends on the build target", withBuild + "release: ownerchannel-build\n", "another line names ownerchannel-build"},
		{"another target runs the build target", withBuild + "release: doctor\n\t@$(MAKE) ownerchannel-build\n", "another line names ownerchannel-build"},
		{"the test target runs the build target", strings.Replace(withBuild, "test: doctor\n", "test: doctor ownerchannel-build\n", 1), "another line names ownerchannel-build"},
		{"the canary build runs the build target", withBuild + "canary-build: doctor\n\t@$(MAKE) ownerchannel-build\n", "another line names ownerchannel-build"},
		{"a variable names the build target", "ALL = ownerchannel-build\n" + withBuild, "another line names ownerchannel-build"},
		{"the build target is only a word of another name", withBuild + "other: my-ownerchannel-build ownerchannel-builds\n", ""},
		{"the build target in a comment", "# ownerchannel-build\n" + good, ""},
		{"the build target in .PHONY alone", strings.Replace(good, ".PHONY: test test-ownerchannel", ".PHONY: test test-ownerchannel ownerchannel-build", 1), ""},
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
		// The daemon with the pair of tags: exactly its rule and its two lines.
		{"the sample with the pair build", withSeat, ""},
		{"the pair build without the owner's build", strings.Replace(good, ".PHONY: test test-ownerchannel", ".PHONY: test test-ownerchannel scratch-seat-build", 1) + "\nscratch-seat-build: doctor\n\t@mkdir -p build/scratch-seat\n" + seatLine, ""},
		{"the pair build in a comment and in .PHONY alone", "# scratch-seat-build\n" + strings.Replace(good, ".PHONY: test test-ownerchannel", ".PHONY: test test-ownerchannel scratch-seat-build", 1), ""},
		{"the pair build is only a word of another name", withSeat + "other: my-scratch-seat-build scratch-seat-builds\n", ""},
		{"the pair build with the owner tag alone", seat("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd\n"), seatExact},
		{"the pair build with the canary tag alone", seat("\t@sh scripts/go-tool.sh build -tags ataniq_canary -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd\n"), seatExact},
		{"the pair build with the tags in the other order", seat("\t@sh scripts/go-tool.sh build -tags ataniq_canary,ataniq_ownerchannel -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd\n"), seatExact},
		{"the pair build with a third tag", seat("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary,other -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd\n"), seatExact},
		{"the pair build without a tag", seat("\t@sh scripts/go-tool.sh build -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd\n"), seatExact},
		{"the pair build writes into the owner's folder", seat("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/owner/ataniqd ./cmd/ataniqd\n"), seatExact},
		{"the pair build writes another file in its folder", seat("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/x ./cmd/ataniqd\n"), seatExact},
		{"the pair build climbs out of its folder", seat("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/../ataniqd ./cmd/ataniqd\n"), seatExact},
		{"the pair build has no output", seat("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath ./cmd/ataniqd\n"), seatExact},
		{"the pair build builds another package", seat("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniq-console\n"), seatExact},
		{"the pair build builds everything", seat("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/ ./...\n"), seatExact},
		{"the pair build builds a second package on its line", seat("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd ./cmd/ataniq-holder\n"), seatExact},
		{"the pair build passes another flag", seat("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -toolexec x -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd\n"), seatExact},
		{"the pair build runs go directly", seat("\t@go build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd\n"), seatExact},
		{"the pair build without at", seat("\tsh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd\n"), seatExact},
		{"the pair build with an extra command on its line", seat(strings.TrimSuffix(seatLine, "\n") + " && build/scratch-seat/ataniqd run --owner-channel\n"), seatExact},
		{"the pair build with a continued line", seat("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/ataniqd \\\n\t\t./cmd/ataniqd\n"), seatExact},
		{"the pair build runs the daemon", seatRecipe("\t@build/scratch-seat/ataniqd run --owner-channel --scratch x\n"), "after its 2 lines"},
		{"the pair build builds a second binary", seatRecipe("\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/h ./cmd/ataniq-holder\n"), "after its 2 lines"},
		{"the pair build removes things", seatRecipe("\t@rm -rf build\n"), "after its 2 lines"},
		{"the pair build runs a line after a blank line", seatRecipe("\n\t@true\n"), "after its 2 lines"},
		{"the pair build makes another folder", strings.Replace(withSeat, "\t@mkdir -p build/scratch-seat\n", "\t@mkdir -p build/other\n", 1), "nothing but \"@mkdir -p build/scratch-seat\" is allowed"},
		{"the pair build has no mkdir", strings.Replace(withSeat, "\t@mkdir -p build/scratch-seat\n", "", 1), "nothing but \"@mkdir -p build/scratch-seat\" is allowed"},
		{"the pair build's lines in the other order", strings.Replace(withSeat, "\t@mkdir -p build/scratch-seat\n"+seatLine, seatLine+"\t@mkdir -p build/scratch-seat\n", 1), "as its line 1"},
		{"the pair build has only its mkdir", seat(""), "has 1 lines"},
		{"the pair build has no recipe", strings.Replace(withSeat, "\t@mkdir -p build/scratch-seat\n"+seatLine, "", 1), "has 0 lines"},
		{"the pair build with an added prerequisite", seatRule("scratch-seat-build: doctor canary-build"), seatRuleOnly},
		{"the pair build with another prerequisite", seatRule("scratch-seat-build: doctor test"), seatRuleOnly},
		{"the pair build without doctor", seatRule("scratch-seat-build:"), seatRuleOnly},
		{"the pair build as a double colon rule", seatRule("scratch-seat-build:: doctor"), seatRuleOnly},
		{"the pair build with a recipe on its rule line", seatRule("scratch-seat-build: doctor ; build/scratch-seat/ataniqd run"), seatRuleOnly},
		{"the pair build with a variable of its own", seatRule("scratch-seat-build: GOFLAGS=-toolexec=x"), seatRuleOnly},
		{"the pair build with a continued rule line", seatRule("scratch-seat-build: doctor \\\n other"), seatRuleOnly},
		{"a second rule adds a prerequisite", withSeat + "scratch-seat-build: release\n", seatRuleOnly},
		{"a second rule that looks like the first", withSeat + "scratch-seat-build: doctor\n", "a second rule of scratch-seat-build"},
		{"a rule of several targets adds a prerequisite", withSeat + "other scratch-seat-build: release\n", seatRuleOnly},
		{"the pair build shares its recipe", seatRule("scratch-seat-build other: doctor"), "outside the recipes"},
		{"the pair build shares its recipe with the owner's build", seatRule("scratch-seat-build ownerchannel-build: doctor"), "outside the recipes"},
		{"the pair tags in another target", withSeat + "other: doctor\n\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/scratch-seat/ataniqd ./cmd/ataniqd\n", "outside the recipes"},
		{"the pair tags in the canary build", withSeat + "canary-build: doctor\n\t@sh scripts/go-tool.sh build -tags ataniq_ownerchannel,ataniq_canary -trimpath -o build/canary/ataniqd ./cmd/ataniqd\n", "outside the recipes"},
		{"another target depends on the pair build", withSeat + "release: scratch-seat-build\n", "another line names scratch-seat-build"},
		{"another target runs the pair build", withSeat + "release: doctor\n\t@$(MAKE) scratch-seat-build\n", "another line names scratch-seat-build"},
		{"the test target runs the pair build", strings.Replace(withSeat, "test: doctor\n", "test: doctor scratch-seat-build\n", 1), "another line names scratch-seat-build"},
		{"the owner's build runs the pair build", strings.Replace(withSeat, "ownerchannel-build: doctor\n", "ownerchannel-build: doctor scratch-seat-build\n", 1), "another line names scratch-seat-build"},
		{"the canary build runs the pair build", withSeat + "canary-build: doctor\n\t@$(MAKE) scratch-seat-build\n", "another line names scratch-seat-build"},
		{"a variable names the pair build", "ALL = scratch-seat-build\n" + withSeat, "another line names scratch-seat-build"},
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

func TestOwnerChannelTagNoMentionUnderCheck(t *testing.T) {
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

func TestOwnerChannelTagNoBuildMentionUnderCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files fstest.MapFS
		want  []string
	}{
		{"clean folders", fstest.MapFS{"scripts/a.sh": {Data: []byte("echo\n")}, ".github/workflows/test.yml": {Data: []byte("name: test\n- run: make test-ownerchannel\n")}}, nil},
		{"no folders", fstest.MapFS{"other/a.sh": {Data: []byte("make ownerchannel-build\n")}}, nil},
		{"a workflow runs the target", fstest.MapFS{".github/workflows/x.yml": {Data: []byte("jobs:\n  b:\n    steps:\n      - run: make ownerchannel-build\n")}}, []string{".github/workflows/x.yml:4"}},
		{"a script runs the target", fstest.MapFS{"scripts/a.sh": {Data: []byte("#!/bin/sh\nmake ownerchannel-build\n")}}, []string{"scripts/a.sh:2"}},
		{"a nested file under .github", fstest.MapFS{".github/actions/b/action.yml": {Data: []byte("run: make ownerchannel-build\n")}}, []string{".github/actions/b/action.yml:1"}},
		{"a comment line is no use", fstest.MapFS{"scripts/a.sh": {Data: []byte("# make ownerchannel-build is run by hand\n")}, ".github/workflows/x.yml": {Data: []byte("  # ownerchannel-build\n")}}, nil},
		{"the canary build is not this target", fstest.MapFS{"scripts/a.sh": {Data: []byte("make canary-build\n")}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkNoOwnerBuildUnder(tc.files, ".github", "scripts")
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i, w := range tc.want {
				if !strings.HasPrefix(got[i], w+":") {
					t.Errorf("problem %d is %q, want it to name %s", i, got[i], w)
				}
			}
		})
	}
}

func TestOwnerChannelTagNoScratchSeatMentionUnderCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files fstest.MapFS
		want  []string
	}{
		{"clean folders", fstest.MapFS{"scripts/a.sh": {Data: []byte("echo\n")}, ".github/workflows/test.yml": {Data: []byte("name: test\n- run: make test-ownerchannel\n")}, "deploy/x/deploy.sh": {Data: []byte("echo\n")}}, nil},
		{"no folders", fstest.MapFS{"other/a.sh": {Data: []byte("make scratch-seat-build\n")}}, nil},
		{"a workflow runs the target", fstest.MapFS{".github/workflows/x.yml": {Data: []byte("jobs:\n  b:\n    steps:\n      - run: make scratch-seat-build\n")}}, []string{".github/workflows/x.yml:4"}},
		{"a release workflow runs the target", fstest.MapFS{".github/workflows/release.yml": {Data: []byte("jobs:\n  r:\n    steps:\n      - run: make scratch-seat-build && cp build/scratch-seat/ataniqd dist/\n")}}, []string{".github/workflows/release.yml:4"}},
		{"a script runs the target", fstest.MapFS{"scripts/a.sh": {Data: []byte("#!/bin/sh\nmake scratch-seat-build\n")}}, []string{"scripts/a.sh:2"}},
		{"a deployment script runs the target", fstest.MapFS{"deploy/authority-host/deploy.sh": {Data: []byte("#!/bin/sh\nset -eu\nmake scratch-seat-build\n")}}, []string{"deploy/authority-host/deploy.sh:3"}},
		{"a nested file under .github", fstest.MapFS{".github/actions/b/action.yml": {Data: []byte("run: make scratch-seat-build\n")}}, []string{".github/actions/b/action.yml:1"}},
		{"a comment line is no use", fstest.MapFS{"scripts/a.sh": {Data: []byte("# make scratch-seat-build is run by hand\n")}, "deploy/a.sh": {Data: []byte("  # scratch-seat-build\n")}}, nil},
		{"the other builds are not this target", fstest.MapFS{"scripts/a.sh": {Data: []byte("make canary-build\nmake ownerchannel-build\n")}}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := checkNoScratchSeatBuildUnder(tc.files, scratchSeatFolders...)
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i, w := range tc.want {
				if !strings.HasPrefix(got[i], w+":") {
					t.Errorf("problem %d is %q, want it to name %s", i, got[i], w)
				}
			}
		})
	}
}

func TestOwnerChannelTagDependencyCheck(t *testing.T) {
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

func TestOwnerChannelTagLoadsCheck(t *testing.T) {
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

func TestOwnerChannelTagModuleCheck(t *testing.T) {
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

func TestOwnerChannelTagWorkflowCheck(t *testing.T) {
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
		{"the job builds the owner daemon", edit(job, job+"      - run: make ownerchannel-build\n"), "runs \"make ownerchannel-build\""},
		{"the job builds the daemon with the pair of tags", edit(job, job+"      - run: make scratch-seat-build\n"), "runs \"make scratch-seat-build\""},
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

func TestOwnerChannelTagListRefusesTaggedPackages(t *testing.T) {
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

func TestOwnerChannelTagCorrectTree(t *testing.T) {
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
func TestOwnerChannelTagMakeTargetDeclaration(t *testing.T) {
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

// The real Makefile has the build target in .PHONY, depending on doctor alone,
// defined once, and no other rule depends on it.
func TestOwnerChannelTagBuildTargetDeclaration(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(root(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(makefile)
	if !regexp.MustCompile(`(?m)^\.PHONY:.*\bownerchannel-build\b`).MatchString(text) {
		t.Error("ownerchannel-build is not in .PHONY")
	}
	if !regexp.MustCompile(`(?m)^ownerchannel-build: doctor$`).MatchString(text) {
		t.Error("ownerchannel-build does not depend on doctor alone")
	}
	rules := 0
	for _, line := range strings.Split(text, "\n") {
		if m := makeRuleLine.FindStringSubmatch(line); m != nil && !makePhony.MatchString(line) && ownerBuildTargetRef.MatchString(line[len(m[1])+1:]) {
			t.Errorf("a rule depends on ownerchannel-build: %q", line)
		}
		if strings.HasPrefix(line, "ownerchannel-build:") {
			rules++
		}
	}
	if rules != 1 {
		t.Errorf("the Makefile has %d rules for ownerchannel-build, want 1", rules)
	}
}

// The real Makefile has the pair's build target in .PHONY, with the one rule
// scratchSeatRule and the two lines of scratchSeatRecipe, no other rule depends
// on it, and git ignores its output folder.
func TestOwnerChannelTagScratchSeatTargetDeclaration(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join(root(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(makefile)
	if !regexp.MustCompile(`(?m)^\.PHONY:.*\bscratch-seat-build\b`).MatchString(text) {
		t.Error("scratch-seat-build is not in .PHONY")
	}
	want := scratchSeatRule + "\n\t" + strings.Join(scratchSeatRecipe, "\n\t") + "\n"
	if strings.Count(text, want) != 1 {
		t.Errorf("the Makefile does not hold the rule and recipe of scratch-seat-build once, exactly as\n%s", want)
	}
	rules := 0
	for _, line := range strings.Split(text, "\n") {
		if m := makeRuleLine.FindStringSubmatch(line); m != nil && !makePhony.MatchString(line) {
			if scratchSeatTargetRef.MatchString(line[len(m[1])+1:]) {
				t.Errorf("a rule depends on scratch-seat-build: %q", line)
			}
			if slices.Contains(strings.Fields(m[1]), scratchSeatTarget) {
				rules++
			}
		}
	}
	if rules != 1 {
		t.Errorf("the Makefile has %d rules for scratch-seat-build, want 1", rules)
	}
	ignore, err := os.ReadFile(filepath.Join(root(t), ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^build/$`).Match(ignore) {
		t.Error(".gitignore does not ignore build/, which holds build/scratch-seat/")
	}
}

// make -n scratch-seat-build, which runs nothing, prints doctor's line and
// then the two lines of the recipe and nothing else: make itself adds no
// other prerequisite and no other line to the target.
func TestOwnerChannelTagScratchSeatDryRun(t *testing.T) {
	dir, _ := fixture(t)
	makefile, err := os.ReadFile(filepath.Join(root(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), makefile, 0644); err != nil {
		t.Fatal(err)
	}
	// The Go tool fails anything it is asked, so a line that ran would show.
	path := fakePath(t, "exit 10\n")
	cmd := exec.Command(filepath.Join(path, "make"), "-n", "--no-print-directory", scratchSeatTarget)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+path, "MAKEFLAGS=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n %s: %s %v", scratchSeatTarget, out, err)
	}
	want := "sh scripts/go-tool.sh doctor\n"
	for _, line := range scratchSeatRecipe {
		want += strings.TrimPrefix(line, "@") + "\n"
	}
	if string(out) != want {
		t.Errorf("make -n printed\n%s\nwant\n%s", out, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "build")); !os.IsNotExist(err) {
		t.Errorf("make -n made build/: %v", err)
	}
}

// The instructions say what the target does, in both files that list the
// targets.
func TestOwnerChannelTagInstructions(t *testing.T) {
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"make test-ownerchannel", ownerTag, "make test-canary", "make ownerchannel-build", "build/owner/", "make scratch-seat-build", "build/scratch-seat/"} {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s does not mention %s", name, want)
			}
		}
	}
	agents, err := os.ReadFile(filepath.Join(root(t), "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(agents), "OwnerChannelTag") {
		t.Error("AGENTS.md does not name the targeted check of the tag")
	}
	// No instruction still says that no target builds a binary with the tag.
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		for _, stale := range []string{"no target builds a binary with it", "no target builds a binary with the tag"} {
			if strings.Contains(string(data), stale) {
				t.Errorf("%s still says %q", name, stale)
			}
		}
	}
}

// make test-ownerchannel runs doctor, then two runs of the tests, each over
// the packages that hold a file whose constraint names a tag it sets, found at
// run time. The second run sets the pair of tags. With no such package a run
// is ./tests/ alone, so that go test is never given an empty list; ./tests/ is
// otherwise left to make test (see TestCanaryTagTestsClosure). The second run
// does not start when the first fails.
func TestOwnerChannelTagMakeTarget(t *testing.T) {
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
		}, "./tests/", "./tests/"},
		// A run with no package of its own runs ./tests/ alone, the other
		// run its packages.
		{"only the pair run has a package", map[string]string{
			"internal/leadcanary/k.go": ownHdrCanaryTag + "\n\npackage a\n",
		}, "./tests/", "./internal/leadcanary"},
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
		}, "./cmd/ataniqd ./internal/approval/hostadapter ./internal/leaddaemon",
			"./cmd/ataniqd ./internal/approval/hostadapter ./internal/leadcanary ./internal/leaddaemon"},
		// Every form Go builds with the tag is found, as in make test-canary.
		{"every form Go builds with the tag", map[string]string{
			"internal/plusonly/p.go":     ownHdrPlusOwn + "\n\npackage a\n",
			"internal/plusonly/q.go":     "// +build\tlinux,ataniq_ownerchannel\n\npackage a\n",
			"internal/tabbed/t.go":       "//go:build\tlinux && ataniq_ownerchannel\n\npackage a\n",
			"internal/indented/i.go":     "  \t" + ownHdrTagged + "\n\npackage a\n",
			"internal/nested/build/n.go": ownHdrTagged + "\n\npackage a\n",
		}, "./internal/indented ./internal/nested/build ./internal/plusonly ./internal/tabbed",
			"./internal/indented ./internal/nested/build ./internal/plusonly ./internal/tabbed"},
		{"a nested checkout under a hidden folder", map[string]string{
			"cmd/ataniqd/o.go":                   ownHdrTagged + "\n\npackage a\n",
			".tool/worktrees/w/go.mod":           "module ataniq\n",
			".tool/worktrees/w/cmd/ataniqd/o.go": ownHdrTagged + "\n\npackage a\n",
		}, "./cmd/ataniqd", "./cmd/ataniqd"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := own + tc.first + "\n" + pair + tc.both + "\n"
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
		if want := own + "./cmd/ataniqd\n"; got != want {
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

// make ownerchannel-build runs doctor, makes build/owner/ and runs one Go
// build of the daemon with the tag alone, writing into that folder. The Go
// tool is replaced by one that logs a build and fails anything else.
func TestOwnerChannelTagBuildTarget(t *testing.T) {
	dir, pin := fixture(t)
	makefile, err := os.ReadFile(filepath.Join(root(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), makefile, 0644); err != nil {
		t.Fatal(err)
	}
	path := fakePath(t, `case "$1" in
version) printf 'go version go`+pin+` linux/amd64\n';;
list|mod) :;;
build) shift; printf '%s\n' "build $*" >> "$LOG";;
*) printf '%s\n' "other $*" >> "$LOG"; exit 10;;
esac
`)
	mkdirPath, err := exec.LookPath("mkdir")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(mkdirPath, filepath.Join(path, "mkdir")); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "log")
	cmd := exec.Command(filepath.Join(path, "make"), "ownerchannel-build")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "PATH="+path, "LOG="+log)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make ownerchannel-build: %s %v", out, err)
	}
	if !strings.Contains(string(out), "ok: go version go") {
		t.Errorf("doctor did not run first:\n%s", out)
	}
	got, _ := os.ReadFile(log)
	if want := "build -tags ataniq_ownerchannel -trimpath -o build/owner/ataniqd ./cmd/ataniqd\n"; string(got) != want {
		t.Errorf("go ran\n%s\nwant\n%s", got, want)
	}
	if fi, err := os.Stat(filepath.Join(dir, "build", "owner")); err != nil || !fi.IsDir() {
		t.Errorf("build/owner/ was not made: %v", err)
	}
	// The folder is ignored, so nothing built there is committed.
	ignore, err := os.ReadFile(filepath.Join(root(t), ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^build/$`).Match(ignore) {
		t.Error(".gitignore does not ignore build/")
	}
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
