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
	"internal/leadcontrol/",
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
	"internal/leadcontrol/",
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
	"internal/leadcontrol",
}

// canaryMakeTargets are the Makefile targets whose recipes may name the tag.
// test-ownerchannel runs the pair of tags together, so it names this one too.
var canaryMakeTargets = []string{"test-canary", "canary-build", "test-ownerchannel"}

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
	all, _, _, bad := headerConstraintsGo(data)
	return all, bad
}

// headerConstraintsGo also returns the constraints the go command applies: the
// go:build line alone when the file has one, and the plus build lines
// otherwise, where a plus build line counts only when a blank line follows it
// before anything else and no block comment comes before it. It reports
// whether the file has a go:build line. Placement uses every line that names
// the tag, which is the conservative reading, and requirement uses the applied
// constraints.
func headerConstraintsGo(data string) (all, applied []constraint.Expr, hasGoBuild bool, bad []string) {
	var goBuild, plusBuild, pending []constraint.Expr
	inBlock, ended := false, false
	for _, line := range strings.Split(data, "\n") {
		s := strings.TrimSpace(line)
		if !inBlock && s == "" {
			if !ended {
				plusBuild = append(plusBuild, pending...)
				pending = nil
			}
			continue
		}
		if inBlock || strings.HasPrefix(s, "/*") {
			ended = true
			var rest string
			rest, inBlock = afterBlockComments(s, inBlock)
			if !inBlock && rest != "" && !strings.HasPrefix(rest, "//") {
				break // code follows the comment: the header is over
			}
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
				pending = append(pending, expr)
			}
		}
	}
	if len(goBuild) > 1 && slices.ContainsFunc(goBuild, exprNamesTag) {
		bad = append(bad, "more than one go:build line, one of which names "+canaryTag)
	}
	if len(goBuild) > 0 {
		return all, goBuild, true, bad
	}
	return all, plusBuild, false, bad
}

// afterBlockComments drops the block comments that open or continue on a line
// and returns what follows them and whether one is still open.
func afterBlockComments(s string, inBlock bool) (string, bool) {
	for {
		if inBlock {
			i := strings.Index(s, "*/")
			if i < 0 {
				return "", true
			}
			s, inBlock = strings.TrimSpace(s[i+2:]), false
		} else if strings.HasPrefix(s, "/*") {
			s, inBlock = s[2:], true
		} else {
			return s, false
		}
	}
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

// skipCanaryDir reports the folders check (1) does not read: .git, the
// top-level build folder, which holds ignored build output, and a folder that
// holds its own go.mod file inside a folder that ./... and make test-canary
// skip, which is another module, such as a nested checkout under a hidden tool
// folder. A module that make test-canary does reach, such as a directory
// replacement under third_party, is built into the binaries and is read. A
// folder named go.mod is no module, and a folder named build deeper in the
// tree is a package like any other.
func skipCanaryDir(fsys fs.FS, p, name string) bool {
	if name == ".git" || p == "build" {
		return true
	}
	if !inGoSkippedDir(p + "/x.go") {
		return false
	}
	fi, err := fs.Stat(fsys, path.Join(p, "go.mod"))
	return err == nil && !fi.IsDir()
}

// inGoSkippedDir reports whether a file lies in a folder that ./... and the
// grep of make test-canary skip: testdata, or a folder whose name starts with
// a dot or an underscore. Go still builds such a package when something
// imports it, so a file there must not name the tag: its tests would never
// run.
func inGoSkippedDir(p string) bool {
	dir := path.Dir(p)
	if dir == "." {
		return false
	}
	for _, part := range strings.Split(dir, "/") {
		if part == "testdata" || strings.HasPrefix(part, ".") || strings.HasPrefix(part, "_") {
			return true
		}
	}
	return false
}

// checkCanaryPlacement is check (1): a Go file whose constraint names the tag
// lies under canaryAllowedPaths and in no folder that ./... skips, and every
// Go file of a wholly tagged package carries a go:build line that needs the
// tag, that is, is not built without it. A plus build line alone does not
// satisfy the second rule.
func checkCanaryPlacement(fsys fs.FS) []string {
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
			if strings.Contains(line, canaryTag) {
				problems = append(problems, fmt.Sprintf("%s: unreadable build constraint %q", p, line))
			}
		}
		named := slices.ContainsFunc(exprs, exprNamesTag)
		if inGoSkippedDir(p) {
			if named {
				problems = append(problems, fmt.Sprintf("%s: the build constraint names %s in a folder that ./... and make test-canary skip", p, canaryTag))
			}
			return nil
		}
		switch {
		case named && !underAnyPath(p, canaryAllowedPaths):
			problems = append(problems, fmt.Sprintf("%s: the build constraint names %s outside the allowed paths", p, canaryTag))
		case named && !(hasGoBuild && slices.ContainsFunc(applied, exprNamesTag)):
			problems = append(problems, fmt.Sprintf("%s: %s is named, but not in a go:build line that the go command applies", p, canaryTag))
		case underAnyPath(p, canaryWholePaths) && (!hasGoBuild || !named || buildsWithoutTag(applied)):
			problems = append(problems, fmt.Sprintf("%s: a file of a wholly tagged package does not require %s", p, canaryTag))
		}
		return nil
	})
	if err != nil {
		problems = append(problems, "walking the tree: "+err.Error())
	}
	return problems
}

// canaryNamedPaths are the folders whose files named canary_* are canary mode
// or its release counterpart, and so carry a constraint that says which.
var canaryNamedPaths = []string{
	"cmd/ataniq-holder/",
	"internal/leaddaemon/",
	"cmd/ataniqd/",
	"internal/leadholder/serve/",
}

// canaryNamedPrefix starts the name of such a file, and canaryOffPrefix the
// name of the release counterpart of one, which forbids the tag.
const (
	canaryNamedPrefix = "canary_"
	canaryOffPrefix   = "canary_off_"
)

// canaryUntaggedByDesign are the files named canary_* that have no constraint
// on purpose: they test the whole folder, whatever its constraints, and the
// same test must hold in a build with the tag and in one without it.
var canaryUntaggedByDesign = []string{"canary_imports_test.go"}

// checkCanaryNamedFiles is the naming rule of check (1): a Go file named
// canary_* in canaryNamedPaths carries a go:build line that the go command
// applies and that needs the tag, so that it is not built without it, or,
// when its name starts with canary_off_, one that names the tag and is not
// built with it. A file in canaryUntaggedByDesign has no requirement. A
// constraint that was weakened to linux alone, or left out, is a problem.
func checkCanaryNamedFiles(fsys fs.FS) []string {
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
		base := path.Base(p)
		if !strings.HasSuffix(p, ".go") || !strings.HasPrefix(base, canaryNamedPrefix) ||
			!underAnyPath(p, canaryNamedPaths) || inGoSkippedDir(p) ||
			slices.Contains(canaryUntaggedByDesign, base) {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		_, applied, hasGoBuild, _ := headerConstraintsGo(string(data))
		named := hasGoBuild && slices.ContainsFunc(applied, exprNamesTag)
		if strings.HasPrefix(base, canaryOffPrefix) {
			if !named || !buildsNeverWithTag(applied) {
				problems = append(problems, fmt.Sprintf("%s: a release counterpart does not forbid %s in a go:build line", p, canaryTag))
			}
			return nil
		}
		if !hasGoBuild || !named || buildsWithoutTag(applied) {
			problems = append(problems, fmt.Sprintf("%s: a file named %s* does not require %s in a go:build line", p, canaryNamedPrefix, canaryTag))
		}
		return nil
	})
	if err != nil {
		problems = append(problems, "walking the tree: "+err.Error())
	}
	return problems
}

// buildsNeverWithTag reports whether the file is not built when the canary tag
// is set, whatever the other tags are.
func buildsNeverWithTag(exprs []constraint.Expr) bool {
	for _, tags := range []func(string) bool{
		func(tag string) bool { return true },
		func(tag string) bool { return tag == canaryTag },
	} {
		for _, e := range exprs {
			if e.Eval(tags) {
				return false
			}
		}
	}
	return true
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

// yamlKey finds a key at the start of a line, quoted or not and with or
// without white space before its colon, so that it can be written plain.
var yamlKey = regexp.MustCompile(`^(\s*(?:-\s+)?)["']?([A-Za-z0-9_-]+)["']?\s*:`)

// stripYAMLComment drops a whole-line comment and a trailing one that follows
// a space, and writes a quoted key plain, as YAML and GitHub read it. It is for
// the plain workflow files of this project, not for YAML in general.
func stripYAMLComment(s string) string {
	if strings.HasPrefix(strings.TrimSpace(s), "#") {
		return ""
	}
	if i := strings.Index(s, " #"); i >= 0 {
		s = strings.TrimRight(s[:i], " ")
	}
	return yamlKey.ReplaceAllString(s, "$1$2:")
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

// workflowFlowKey finds a run or uses key written inside a flow mapping
// (a step as {uses: ./x}, or a key after a comma) and captures its value.
var workflowFlowKey = regexp.MustCompile(`[{,]\s*["']?(run|uses)["']?\s*:\s*("[^"]*"|'[^']*'|[^,}]*)`)

// workflowKeyValues returns the values of a run or uses key in the lines. A
// key in a block mapping gives its value on the line, or, when the value is
// empty or a block scalar (| or >), the lines below it that are indented more
// than the key, one value per line. A key inside a flow mapping on one line
// gives its value too. Quotes around a value are dropped.
func workflowKeyValues(lines []string, key string) []string {
	var values []string
	unquote := func(v string) string { return strings.TrimSpace(strings.Trim(strings.TrimSpace(v), `"'`)) }
	for i, raw := range lines {
		line := stripYAMLComment(raw)
		for _, m := range workflowFlowKey.FindAllStringSubmatch(line, -1) {
			if m[1] == key {
				values = append(values, unquote(m[2]))
			}
		}
		trimmed := strings.TrimSpace(line)
		keyIndent := yamlIndent(line)
		if strings.HasPrefix(trimmed, "- ") {
			trimmed = strings.TrimSpace(trimmed[2:])
			keyIndent += 2
		}
		if !strings.HasPrefix(trimmed, key+":") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(trimmed, key+":"))
		if value == "" || strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
			block := value != ""
			for _, next := range lines[i+1:] {
				t := strings.TrimSpace(next)
				if t == "" || !block && strings.HasPrefix(t, "#") {
					continue
				}
				if yamlIndent(next) <= keyIndent {
					break
				}
				values = append(values, unquote(t))
			}
			continue
		}
		values = append(values, unquote(value))
	}
	return values
}

// workflowRunCommands returns the commands of the run steps in the lines.
func workflowRunCommands(lines []string) []string { return workflowKeyValues(lines, "run") }

// workflowUses returns the actions the lines use.
func workflowUses(lines []string) []string { return workflowKeyValues(lines, "uses") }

// workflowUnreadForms returns the lines of a job written in a form that the
// readers above do not see through: a flow mapping or sequence, which can hold
// a step, a run, an if or a continue-on-error (permissions may be written as a
// flow mapping, which workflowPermissions reads), a complex or merge key, and
// an anchor, an alias or a tag at the start of a value. The lines of a block
// scalar are text, not YAML, and are not looked at.
func workflowUnreadForms(lines []string) []string {
	var out []string
	blockIndent := -1
	for _, raw := range lines {
		line := stripYAMLComment(raw)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		indent := yamlIndent(line)
		if blockIndent >= 0 {
			if indent > blockIndent {
				continue
			}
			blockIndent = -1
		}
		for strings.HasPrefix(trimmed, "- ") {
			trimmed = strings.TrimSpace(trimmed[2:])
			indent += 2
		}
		key, value := "", trimmed
		if m := workflowPlainKey.FindStringSubmatch(trimmed); m != nil {
			key, value = m[1], strings.TrimSpace(m[2])
		}
		if strings.HasPrefix(value, "|") || strings.HasPrefix(value, ">") {
			blockIndent = indent
			continue
		}
		flow := strings.HasPrefix(value, "{") && key != "permissions" || strings.HasPrefix(value, "[")
		if flow || strings.HasPrefix(trimmed, "? ") || strings.HasPrefix(trimmed, "<<") || value != "" && strings.ContainsRune("&*!", rune(value[0])) {
			out = append(out, trimmed)
		}
	}
	return out
}

// workflowPlainKey splits a line into a plain key and the rest of the line.
var workflowPlainKey = regexp.MustCompile(`^([A-Za-z0-9_-]+):(?:\s+(.*))?$`)

// workflowLine is a line of a workflow file outside a block scalar: its key
// written plain (empty when the line does not start with a key these checks
// read), the indent of the key's mapping, and its text.
type workflowLine struct {
	key    string
	indent int
	text   string
}

// workflowLines returns the lines outside comments and block scalars. A dash
// before a key opens a mapping two columns in, as the workflow files of this
// project are written.
func workflowLines(lines []string) []workflowLine {
	var out []workflowLine
	blockIndent := -1
	for _, raw := range lines {
		line := stripYAMLComment(raw)
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		indent := yamlIndent(line)
		if blockIndent >= 0 {
			if indent > blockIndent {
				continue
			}
			blockIndent = -1
		}
		for strings.HasPrefix(trimmed, "- ") {
			trimmed = strings.TrimSpace(trimmed[2:])
			indent += 2
		}
		m := workflowPlainKey.FindStringSubmatch(trimmed)
		if m == nil {
			out = append(out, workflowLine{"", indent, trimmed})
			continue
		}
		out = append(out, workflowLine{m[1], indent, trimmed})
		if v := strings.TrimSpace(m[2]); strings.HasPrefix(v, "|") || strings.HasPrefix(v, ">") {
			blockIndent = indent
		}
	}
	return out
}

// canaryJobKeys are the only keys the test-canary job, its steps and their
// with blocks may hold, and canaryWorkflowKeys the only top-level keys of
// test.yml, which apply to that job too. Any other key (env, defaults, shell,
// working-directory, container, services, strategy and the like) could change
// what make test-canary does or whether it runs: make reads MAKEFLAGS from the
// environment, a custom shell need not run the script, and another working
// directory holds another Makefile. The list is flat: a key on it is allowed
// at any depth of the job.
var (
	canaryJobKeys = []string{"test-canary", "runs-on", "timeout-minutes", "permissions", "contents", "steps", "name",
		"uses", "with", "run", "persist-credentials", "fetch-depth", "fetch-tags", "go-version-file"}
	canaryWorkflowKeys = []string{"name", "on", "permissions", "concurrency", "jobs"}
)

// canaryJobRuns are the only commands the test-canary job runs.
var canaryJobRuns = []string{"make -n bootstrap", "make bootstrap", "make doctor", "make test-canary"}

// canaryJobUses are the only actions the test-canary job uses. Anything else,
// a composite action of the repository included, could keep build output or
// run a tagged build.
var canaryJobUses = []string{"actions/checkout@", "actions/setup-go@"}

// canaryRunKitPath is the folder of the canary run kit. A file there may build
// with the tag, so no workflow run line may name it.
var canaryRunKitPath = regexp.MustCompile(`qualification/canary($|[^A-Za-z0-9_.-])`)

// canaryJobGuardKey finds a key that can keep a job or a step from failing,
// quoted or not. The job is refused when it holds a flow mapping, which could
// hold one (workflowUnreadForms).
var canaryJobGuardKey = regexp.MustCompile(`^["']?(if|continue-on-error)["']?\s*:`)

// checkCanaryWorkflow is check (5). Its input is every file under .github:
// the workflow files directly below .github/workflows by their file name, and
// every other file (composite actions, nested folders, anything else) by its
// path. No file names the tag or canary-build outside a comment, no run line of
// any file names the run kit's folder, and no file uses a local action outside
// .github. test.yml has a job test-canary that runs the make target and never
// names the tag, uses only the actions of canaryJobUses, has no if or
// continue-on-error that could keep it from failing, has read-only
// permissions, and is written in no flow style, with no complex or merge key
// and with no anchor, alias or YAML tag. The job holds only the keys of
// canaryJobKeys and test.yml only the top-level keys of canaryWorkflowKeys, so
// that no environment, shell, default or working directory changes what the
// job's make test-canary does; a line of the job, or a top-level line of
// test.yml, that does not start with a key these checks read is refused.
//
// A text check reads lines. A run or uses key is read wherever it starts a
// line (after a dash or not), follows a brace or a comma on a line, or is
// quoted, and its value is read on its line or below it as a block scalar. The
// test-canary job also refuses every form that could hide a key from these
// readers: a flow mapping or sequence (except permissions), a complex or merge
// key, and an anchor, an alias or a YAML tag. A uses value that has one, or an
// escape, is refused in every file.
//
// The limits that remain in every file but that job are: a key spelled with
// escapes or reached only through a complex key; a flow mapping whose key and
// value are on different lines; a value split over lines inside quotes; a run
// value that is an alias of text defined elsewhere; a run line that assembles
// at run time, or spells with escapes, the text of the tag, of canary-build or
// of the run kit's folder (for example from two shell variables); and what an
// action of another repository runs once the repository is checked out, which
// is allowed outside the test-canary job.
//
// The limits that remain for the job itself are: the key lists are flat, so a
// listed key is accepted at any depth of the job and its value is read only
// where a check above names it (runs-on, timeout-minutes, name and the values
// under with are not read, and neither is the version of a listed action nor
// what that action does); the values of the top-level keys of test.yml other
// than permissions are not read (on and concurrency decide when the job
// starts and whether a newer run cancels it); a permissions value written as
// a flow mapping over several lines; and whatever acts on the job from
// outside test.yml, such as settings of the repository or the organisation,
// the runner's image and environment, and the Makefile, which check (2) reads
// only for the tag.
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
		// A local action outside .github is a file this check does not read.
		// A value written with escapes, a tag, an anchor or an alias cannot be
		// read as a path, so it is refused too.
		for _, use := range workflowUses(kept) {
			switch {
			case strings.Contains(use, `\`) || use != "" && strings.ContainsRune("&*!", rune(use[0])):
				problems = append(problems, fmt.Sprintf("%s: uses %s, which this check cannot read as a path", name, use))
			case strings.HasPrefix(use, "./") && !strings.HasPrefix(path.Clean(use), ".github/"):
				problems = append(problems, fmt.Sprintf("%s: uses the local action %s, outside .github", name, use))
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
	forms := workflowUnreadForms(job)
	for _, form := range forms {
		problems = append(problems, fmt.Sprintf("test.yml: the test-canary job has %q, a form this check does not read", form))
	}
	// The job holds only listed keys, and every line of it starts with a key.
	for _, line := range workflowLines(job) {
		switch {
		case line.key == "" && !slices.Contains(forms, line.text):
			problems = append(problems, fmt.Sprintf("test.yml: the test-canary job has the line %q, which does not start with a key this check reads", line.text))
		case line.key != "" && !slices.Contains(canaryJobKeys, line.key):
			problems = append(problems, fmt.Sprintf("test.yml: the test-canary job has the key %s, and nothing but %s is allowed", line.key, strings.Join(canaryJobKeys, ", ")))
		}
	}
	// A top-level key of the workflow applies to the job too.
	for _, line := range workflowLines(strings.Split(text, "\n")) {
		switch {
		case line.indent != 0:
		case line.key == "":
			problems = append(problems, fmt.Sprintf("test.yml: the workflow has the top-level line %q, which does not start with a key this check reads", line.text))
		case !slices.Contains(canaryWorkflowKeys, line.key):
			problems = append(problems, fmt.Sprintf("test.yml: the workflow has the top-level key %s, which applies to the test-canary job too, and nothing but %s is allowed", line.key, strings.Join(canaryWorkflowKeys, ", ")))
		}
	}
	for _, raw := range job {
		key := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(stripYAMLComment(raw)), "- "))
		if canaryJobGuardKey.MatchString(key) {
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
		reportCanaryProblems(t, checkCanaryNamedFiles(repo))
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
		{"the top-level build folder and another module are not read", fstest.MapFS{
			"build/x.go":               goSource(hdrTagged),
			"build/sub/x.go":           goSource(hdrTagged),
			".tool/worktrees/w/go.mod": {Data: []byte("module m\n")},
			".tool/worktrees/w/internal/leadcanary/a.go":     goSource(hdrTagged),
			".tool/worktrees/w/internal/other/x.go":          goSource(hdrTagged),
			"internal/leadcanary/testdata/untagged_input.go": goSource(""),
			"internal/_old/plain.go":                         goSource(hdrPlain),
		}, nil},
		// Go builds a package in testdata or in a dot or underscore folder when
		// something imports it, but ./... and make test-canary skip it, so its
		// tests would never run.
		{"a tagged file in a folder Go skips", fstest.MapFS{
			"internal/other/testdata/x.go":   goSource(hdrTagged),
			".hidden/x.go":                   goSource(hdrTagged),
			"internal/_old/x.go":             goSource(hdrTagged),
			"internal/leadcanary/_impl/x.go": goSource(hdrTagged),
			"internal/leadcanary/.impl/x.go": goSource(hdrTagged),
		}, []string{".hidden/x.go", "internal/_old/x.go", "internal/leadcanary/.impl/x.go", "internal/leadcanary/_impl/x.go", "internal/other/testdata/x.go"}},
		// A tagged package under internal/_x that a tagged package imports is
		// built by every tagged binary, and make test-canary would not run it.
		{"a tagged package that a tagged package imports, in an underscore folder", fstest.MapFS{
			"internal/leadcanary/b.go": &fstest.MapFile{Data: []byte(hdrTagged + "\n\npackage b\n\nimport _ \"example.test/m/internal/_x\"\n")},
			"internal/_x/x.go":         goSource(hdrTagged),
			"internal/_x/x_test.go":    goSource(hdrTagged),
		}, []string{"internal/_x/x.go", "internal/_x/x_test.go"}},
		// A nested checkout in a folder ./... skips holds its own module,
		// which is not read.
		{"a nested checkout with tagged code", fstest.MapFS{
			"go.mod":                                    {Data: []byte("module m\n")},
			"internal/leadcanary/a.go":                  goSource(hdrTagged),
			".tool/worktrees/w/go.mod":                  {Data: []byte("module m\n")},
			".tool/worktrees/w/internal/other/x.go":     goSource(hdrTagged),
			".tool/worktrees/w/internal/_x/x.go":        goSource(hdrTagged),
			"scratch/checkout/go.mod":                   {Data: []byte("module m\n")},
			"scratch/checkout/internal/leadcanary/a.go": goSource(hdrPlain),
		}, nil},
		// A module that ./... and make test-canary reach, such as a directory
		// replacement, is built into the binaries and read; a folder named
		// go.mod is no module, in a skipped folder or not.
		{"a module make test-canary reaches, and a folder named go.mod", fstest.MapFS{
			"go.mod":                            {Data: []byte("module m\n")},
			"third_party/patched/p/go.mod":      {Data: []byte("module p\n")},
			"third_party/patched/p/x.go":        goSource(hdrTagged),
			"internal/evil/go.mod/README":       {Data: []byte("x\n")},
			"internal/evil/x.go":                goSource(hdrTagged),
			".tool/worktrees/w/go.mod/README":   {Data: []byte("x\n")},
			".tool/worktrees/w/internal/x/x.go": goSource(hdrTagged),
		}, []string{".tool/worktrees/w/internal/x/x.go", "internal/evil/x.go", "third_party/patched/p/x.go"}},
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
		// Wherever a file names the tag, it does so in a go:build line: a plus
		// build line alone is refused outside the wholly tagged paths too, where
		// it would be read or not by the go command as the blank line and the
		// block comments decide.
		{"a plus build line alone naming the tag", fstest.MapFS{
			"internal/leaddaemon/a.go":       goSource(hdrPlusTagged),
			"internal/leaddaemon/b.go":       goSource(hdrPlusPlain),
			"internal/leadholder/serve/c.go": goSource(hdrPlusPlain),
			"internal/leadholder/serve/d.go": goSource(hdrPlusTagged),
			"cmd/ataniqd/e.go":               goSource(hdrPlain + "\n" + hdrPlusTagged),
			"cmd/ataniq-holder/f.go":         goSource(hdrTagged + "\n" + hdrPlusPlain),
		}, []string{"cmd/ataniqd/e.go", "internal/leaddaemon/a.go", "internal/leadholder/serve/d.go"}},
		// The go command does not read a plus build line that no blank line
		// follows, so the file builds without the tag.
		{"a plus build line that no blank line follows", fstest.MapFS{
			"internal/leaddaemon/canary_q2.go": &fstest.MapFile{Data: []byte(hdrPlusTagged + "\npackage leaddaemon\n")},
			"cmd/ataniqd/canary_q3.go":         &fstest.MapFile{Data: []byte("/* c */\n" + hdrPlusTagged + "\n\npackage main\n")},
			"cmd/ataniqd/canary_ok.go":         &fstest.MapFile{Data: []byte(hdrTagged + "\npackage main\n")},
		}, []string{"cmd/ataniqd/canary_q3.go", "internal/leaddaemon/canary_q2.go"}},
		// The header ends at the code that follows a block comment on its line,
		// so a line after that is no constraint, and a wholly tagged file that
		// has no constraint before it does not require the tag.
		{"a go:build line after code that follows a block comment", fstest.MapFS{
			"internal/leadcanary/a.go": &fstest.MapFile{Data: []byte("/* c */ package leadcanary\n" + hdrTagged + "\n")},
			"internal/leadcanary/b.go": &fstest.MapFile{Data: []byte("/* c\n*/ package leadcanary\n" + hdrTagged + "\n")},
			"internal/leadcanary/c.go": &fstest.MapFile{Data: []byte("/* c */ // note\n" + hdrTagged + "\n\npackage leadcanary\n")},
			"internal/leaddaemon/v.go": &fstest.MapFile{Data: []byte("/* c */ package leaddaemon\n" + hdrTagged + "\n")},
			"internal/leadcanary/d.go": &fstest.MapFile{Data: []byte("/* c */ /* d */\n" + hdrTagged + "\n\npackage leadcanary\n")},
			"internal/leadcanary/e.go": &fstest.MapFile{Data: []byte("/* c\nd */\n" + hdrTagged + "\n\npackage leadcanary\n")},
		}, []string{"internal/leadcanary/a.go", "internal/leadcanary/b.go"}},
		{"a plus build line alone in a wholly tagged package", fstest.MapFS{
			"internal/leadcanary/a.go":          goSource(hdrPlusTagged),
			"internal/leadcanary/b.go":          goSource(hdrPlusPlain),
			"cmd/ataniq-canary-standin/main.go": goSource(hdrPlusTagged),
			"internal/leadcanary/c.go":          goSource(hdrTagged),
		}, []string{"cmd/ataniq-canary-standin/main.go", "internal/leadcanary/a.go", "internal/leadcanary/b.go"}},
		// The go command reads a plus build line only when a blank line follows
		// it and no block comment comes before it, and in these packages a plus
		// build line is not enough either way.
		{"a plus build line that the go command does not read in a wholly tagged package", fstest.MapFS{
			"internal/leadcanary/a.go":          &fstest.MapFile{Data: []byte(hdrPlusTagged + "\npackage a\n")},
			"internal/leadcanary/b.go":          &fstest.MapFile{Data: []byte("/* license */\n" + hdrPlusTagged + "\n\npackage a\n")},
			"cmd/ataniq-canary-standin/main.go": &fstest.MapFile{Data: []byte(hdrPlusTagged + "\npackage main\n")},
		}, []string{"cmd/ataniq-canary-standin/main.go", "internal/leadcanary/a.go", "internal/leadcanary/b.go"}},
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

// canaryCorrectTree is a tree as the slice leaves it: the wholly tagged
// packages, a helper package for the tests of one of them, and tagged files
// beside untagged ones in the packages that also build without the tag, with
// a nested checkout of the same tree under a hidden tool folder.
func TestCanaryTagNamedFilesCheck(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files fstest.MapFS
		bad   []string // paths named by the problems
	}{
		{"empty tree", fstest.MapFS{}, nil},
		{"tagged files and their counterparts", fstest.MapFS{
			"cmd/ataniq-holder/canary_linux.go":            goSource(hdrTagged),
			"cmd/ataniq-holder/canary_x_test.go":           goSource(hdrTagged),
			"cmd/ataniqd/canary_off_linux.go":              goSource(hdrNegated),
			"internal/leaddaemon/canary_off_linux_test.go": goSource(hdrNegated),
			"internal/leadholder/serve/canary_y.go":        goSource(hdrTagged),
		}, nil},
		{"the file with no constraint on purpose", fstest.MapFS{
			"cmd/ataniqd/canary_imports_test.go":         goSource(""),
			"internal/leaddaemon/canary_imports_test.go": goSource(""),
		}, nil},
		{"a test file weakened to linux", fstest.MapFS{
			"cmd/ataniq-holder/canary_a_test.go": goSource(hdrPlain),
		}, []string{"cmd/ataniq-holder/canary_a_test.go"}},
		{"a source file weakened to linux", fstest.MapFS{
			"internal/leaddaemon/canary_a.go": goSource(hdrPlain),
		}, []string{"internal/leaddaemon/canary_a.go"}},
		{"a file with no constraint", fstest.MapFS{
			"cmd/ataniqd/canary_b.go":                    goSource(""),
			"internal/leadholder/serve/canary_c_test.go": goSource(""),
		}, []string{"cmd/ataniqd/canary_b.go", "internal/leadholder/serve/canary_c_test.go"}},
		{"a plus build line alone does not count", fstest.MapFS{
			"cmd/ataniqd/canary_d.go": goSource(hdrPlusTagged),
		}, []string{"cmd/ataniqd/canary_d.go"}},
		{"a negated file that is not a counterpart", fstest.MapFS{
			"cmd/ataniqd/canary_e.go": goSource(hdrNegated),
		}, []string{"cmd/ataniqd/canary_e.go"}},
		{"a counterpart that does not forbid the tag", fstest.MapFS{
			"cmd/ataniqd/canary_off_a.go":      goSource(hdrPlain),
			"cmd/ataniqd/canary_off_b.go":      goSource(hdrTagged),
			"cmd/ataniqd/canary_off_c.go":      goSource(""),
			"cmd/ataniqd/canary_off_d_test.go": goSource(hdrNegated),
		}, []string{"cmd/ataniqd/canary_off_a.go", "cmd/ataniqd/canary_off_b.go", "cmd/ataniqd/canary_off_c.go"}},
		{"an or that lets the file build without the tag", fstest.MapFS{
			"cmd/ataniqd/canary_f.go": goSource("//go:build linux && ataniq_canary || darwin"),
		}, []string{"cmd/ataniqd/canary_f.go"}},
		{"other names and other folders are not read", fstest.MapFS{
			"cmd/ataniqd/main_linux.go":        goSource(hdrPlain),
			"cmd/ataniqd/xcanary_g.go":         goSource(hdrPlain),
			"cmd/ataniqd/canary_h.txt":         &fstest.MapFile{Data: []byte("x")},
			"internal/other/canary_i.go":       goSource(hdrPlain),
			"cmd/ataniqd/testdata/canary_j.go": goSource(hdrPlain),
		}, nil},
		{"a subfolder is read", fstest.MapFS{
			"internal/leaddaemon/sub/canary_k.go": goSource(hdrPlain),
		}, []string{"internal/leaddaemon/sub/canary_k.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			problems := checkCanaryNamedFiles(tc.files)
			var got []string
			for _, p := range problems {
				got = append(got, strings.SplitN(p, ":", 2)[0])
			}
			sort.Strings(got)
			want := slices.Clone(tc.bad)
			sort.Strings(want)
			if !slices.Equal(got, want) {
				t.Errorf("problems for %v, want %v: %q", got, want, problems)
			}
		})
	}
}

func canaryCorrectTree() map[string]string {
	tagged := hdrTagged + "\n\npackage x\n"
	plain := "package x\n"
	files := map[string]string{
		"go.mod":                                           "module example.test/m\n",
		"internal/leadcanary/a.go":                         tagged,
		"internal/leadcanary/a_test.go":                    tagged,
		"internal/leadcanary/standin/a.go":                 tagged,
		"internal/leadcanary/standin/a_test.go":            tagged,
		"internal/leadcanary/provision/a.go":               tagged,
		"internal/leadcanary/provision/a_test.go":          tagged,
		"internal/leadcanary/provision/provisiontest/a.go": tagged,
		"internal/leadholder/canaryroot/a.go":              tagged,
		"internal/leadholder/sandboxstart/a.go":            tagged,
		"internal/leadholder/sandboxstart/a_test.go":       tagged,
		"internal/leadholder/injectbridge/a.go":            tagged,
		"cmd/ataniq-canary/main.go":                        tagged,
		"cmd/ataniq-canary-standin/main.go":                tagged,
		"cmd/ataniq-canary-standin/main_test.go":           tagged,
		"internal/leadholder/eventlog/a.go":                plain,
		"tests/a_test.go":                                  plain,
	}
	for _, dir := range []string{"internal/leadholder/serve", "internal/leaddaemon", "cmd/ataniqd", "cmd/ataniq-holder"} {
		files[dir+"/a.go"] = plain
		files[dir+"/b.go"] = hdrPlain + "\n\npackage x\n"
		files[dir+"/canary.go"] = tagged
		files[dir+"/canary_test.go"] = tagged
	}
	for name, content := range maps.Clone(files) {
		if name != "tests/a_test.go" {
			files[".tool/worktrees/w/"+name] = content
		}
	}
	return files
}

// A correct tree passes the placement check, read from a temporary folder, and
// make test-canary (below) lists exactly its tagged packages.
func TestCanaryTagCorrectTree(t *testing.T) {
	dir := t.TempDir()
	for name, content := range canaryCorrectTree() {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	reportCanaryProblems(t, checkCanaryPlacement(os.DirFS(dir)))
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

// The go command applies a plus build line only when a blank line follows it
// before anything else and no block comment comes before it. Placement still
// reads every line that names the tag, and the applied list is what a file is
// built by.
func TestCanaryTagHeaderConstraints(t *testing.T) {
	for _, tc := range []struct {
		name       string
		data       string
		all        int
		applied    int
		hasGoBuild bool
	}{
		{"a plus build line, a blank line, the package", hdrPlusTagged + "\n\npackage a\n", 1, 1, false},
		{"a plus build line then the package", hdrPlusTagged + "\npackage a\n", 1, 0, false},
		{"a plus build line, a blank line, a doc comment", hdrPlusTagged + "\n\n// Package a.\npackage a\n", 1, 1, false},
		{"a plus build line then a comment then a blank line", hdrPlusTagged + "\n// A comment.\n\npackage a\n", 1, 1, false},
		{"a plus build line after a line comment", "// A comment.\n" + hdrPlusTagged + "\n\npackage a\n", 1, 1, false},
		{"a block comment before a plus build line", "/* license */\n" + hdrPlusTagged + "\n\npackage a\n", 1, 0, false},
		{"a multi-line block comment before a plus build line", "/* license\n*/\n" + hdrPlusTagged + "\n\npackage a\n", 1, 0, false},
		{"a plus build line, a blank line, a block comment", hdrPlusTagged + "\n\n/* license */\npackage a\n", 1, 1, false},
		{"a plus build line then a block comment", hdrPlusTagged + "\n/* license */\n\npackage a\n", 1, 0, false},
		{"a tab after the plus build line", "// +build\tlinux,ataniq_canary\n\npackage a\n", 1, 1, false},
		{"a go:build line needs no blank line", hdrTagged + "\npackage a\n", 1, 1, true},
		{"a go:build line wins over a plus build line", hdrPlain + "\n" + hdrPlusTagged + "\n\npackage a\n", 2, 1, true},
		{"no constraint", "package a\n", 0, 0, false},
		{"code after a block comment ends the header", "/* c */ package a\n" + hdrTagged + "\n", 0, 0, false},
		{"code after a multi-line block comment ends the header", "/* c\n*/ package a\n" + hdrTagged + "\n", 0, 0, false},
		{"a line comment after a block comment is no constraint", "/* c */ " + hdrTagged + "\n\npackage a\n", 0, 0, false},
		{"a block comment closed on its line, a constraint below", "/* c */\n" + hdrTagged + "\n\npackage a\n", 1, 1, true},
		{"two block comments on a line", "/* c */ /* d */\n" + hdrTagged + "\n\npackage a\n", 1, 1, true},
		{"a block comment that closes then opens again", "/* c\n*/ /* d\n*/\n" + hdrTagged + "\n\npackage a\n", 1, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			all, applied, hasGoBuild, bad := headerConstraintsGo(tc.data)
			if len(all) != tc.all || len(applied) != tc.applied || hasGoBuild != tc.hasGoBuild || len(bad) != 0 {
				t.Fatalf("all %d, applied %d, go:build %v, bad %v; want %d, %d, %v", len(all), len(applied), hasGoBuild, bad, tc.all, tc.applied, tc.hasGoBuild)
			}
		})
	}
}

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
		{"a quoted if on the job", edit("    timeout-minutes: 30\n    permissions", "    'if': always()\n    permissions"), true},
		{"a quoted continue-on-error on a step of the job", edit("      - run: make test-canary\n", "      - run: make test-canary\n        \"continue-on-error\": true\n"), true},
		// A quoted key is the plain key to YAML and to GitHub, and white space
		// may come before the colon.
		{"a quoted permissions key that writes", edit("    permissions:\n      contents: read\n    steps", "    'permissions': write-all\n    steps"), true},
		{"a double quoted permissions key that writes", edit("    permissions:\n      contents: read\n    steps", "    \"permissions\": write-all\n    steps"), true},
		{"a permissions key with a space before the colon that writes", edit("    permissions:\n      contents: read\n    steps", "    permissions : write-all\n    steps"), true},
		{"a quoted permissions key that is read-only", edit("    permissions:\n      contents: read\n    steps", "    'permissions':\n      'contents': read\n    steps"), false},
		{"a quoted permission that writes", edit("      contents: read\n    steps", "      'contents': write\n    steps"), true},
		{"a quoted job key", edit("  test-canary:", "  \"test-canary\":"), false},
		{"a quoted run key that runs the run kit in another workflow", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - 'run': sh qualification/canary/run/build.sh\n"}, true},
		{"a double quoted run key with a space before the colon", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - \"run\" : sh qualification/canary/run/build.sh\n"}, true},
		{"a quoted run key with a block value that runs the run kit", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - 'run': |\n          sh qualification/canary/run/build.sh\n"}, true},
		{"a quoted run key that is not the run kit", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - 'run': make test\n"}, false},
		{"a quoted run key in the job that is not on the list", edit("      - run: make doctor\n", "      - run: make doctor\n      - \"run\": sh qualification/canary/run/ship.sh\n"), true},
		{"a quoted run key in the job that is on the list", edit("      - run: make doctor\n", "      - 'run': make doctor\n"), false},
		{"a quoted run key in a composite action that runs the run kit", map[string]string{"test.yml": goodWorkflow, ".github/actions/b/action.yml": "runs:\n  using: composite\n  steps:\n    - 'run': sh qualification/canary/x.sh\n      shell: sh\n"}, true},
		{"a quoted uses key for an action that is not on the list", edit("      - run: make test-canary\n", "      - run: make test-canary\n      - 'uses': actions/cache@v4\n"), true},
		// A local action or a run line in a form other than a block key.
		{"a local action in a flow mapping step", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - {uses: ./tools/ship}\n"}, true},
		{"a local action after a comma in a flow mapping", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - {name: ship, uses: \"./tools/ship\"}\n"}, true},
		{"a local action in a flow sequence of steps", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n    steps: [{uses: './tools/ship'}]\n"}, true},
		{"a local action in a flow mapping under a quoted key", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - {'uses' : ./tools/ship}\n"}, true},
		{"a local action under .github in a flow mapping", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - {uses: ./.github/actions/b}\n"}, false},
		{"an action of another repository in a flow mapping", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - {uses: some-org/ship@v1}\n"}, false},
		{"a local action in a folded block scalar", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - uses: >-\n          ./tools/ship\n"}, true},
		{"a local action in a literal block scalar", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - uses: |\n          ./tools/ship\n"}, true},
		{"a local action on the line below its key", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - uses:\n          ./tools/ship\n"}, true},
		{"a local action under .github in a block scalar", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - uses: >-\n          ./.github/actions/b\n"}, false},
		{"a local action written with escapes", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - uses: \"\\x2e/tools/ship\"\n"}, true},
		{"a local action that is an alias", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - uses: *ship\n"}, true},
		{"a local action with a YAML tag", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - uses: !!str ./tools/ship\n"}, true},
		{"a run kit run line in a flow mapping", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - {run: sh qualification/canary/run/build.sh}\n"}, true},
		{"a run kit run line after a comma in a flow mapping", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - {shell: sh, 'run': \"sh qualification/canary/run/build.sh\"}\n"}, true},
		{"a run kit run line on the line below its key", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - run:\n          sh qualification/canary/run/build.sh\n"}, true},
		{"a run line in a flow mapping that is not the run kit", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - {run: make test}\n"}, false},
		// The test-canary job holds only listed keys and test.yml only listed
		// top-level keys: each of these would keep make test-canary from
		// running while the job stays green.
		{"make flags in the job's environment", edit("    timeout-minutes: 30\n", "    timeout-minutes: 30\n    env:\n      MAKEFLAGS: n\n"), true},
		{"make flags in the workflow's environment", edit("\npermissions:\n  contents: read\n", "\nenv:\n  MAKEFLAGS: n\npermissions:\n  contents: read\n"), true},
		{"a shell that runs nothing on the step", edit("      - run: make test-canary\n", "      - run: make test-canary\n        shell: true {0}\n"), true},
		{"a default shell of the workflow", edit("\npermissions:\n  contents: read\n", "\ndefaults:\n  run:\n    shell: true {0}\npermissions:\n  contents: read\n"), true},
		{"a working directory with another Makefile", edit("      - run: make test-canary\n", "      - run: make test-canary\n        working-directory: .github/fake\n"), true},
		{"a quoted shell key on the step", edit("      - run: make test-canary\n", "      - run: make test-canary\n        'shell' : true {0}\n"), true},
		{"a shell key spelled with an escape on the step", edit("      - run: make test-canary\n", "      - run: make test-canary\n        \"sh\\x65ll\": true {0}\n"), true},
		{"an environment key spelled with an escape in the workflow", edit("\npermissions:\n  contents: read\n", "\n\"\\x65nv\":\n  MAKEFLAGS: n\npermissions:\n  contents: read\n"), true},
		{"a key of the job in another case", edit("    timeout-minutes: 30\n", "    timeout-minutes: 30\n    Env:\n      MAKEFLAGS: n\n"), true},
		{"a container on the job", edit("    timeout-minutes: 30\n", "    timeout-minutes: 30\n    container: some-org/image\n"), true},
		{"a step of the job with a name", edit("      - run: make test-canary\n", "      - name: the tagged tests\n        run: make test-canary\n"), false},
		{"an environment on the first job is not this job's", edit("      - run: make test\n", "      - run: make test\n        env:\n          X: y\n"), false},
		// The test-canary job is held to the forms the readers see through.
		{"a flow mapping step in the job", edit("      - run: make test-canary\n", "      - run: make test-canary\n      - {uses: actions/checkout@v4}\n"), true},
		{"a flow mapping step that runs in the job", edit("      - run: make test-canary\n", "      - run: make test-canary\n      - {run: make doctor}\n"), true},
		{"a flow mapping step that guards the job", edit("      - run: make test-canary\n", "      - {if: always(), run: make test-canary}\n"), true},
		{"a flow mapping step with a local action in the job", edit("      - run: make test-canary\n", "      - run: make test-canary\n      - {uses: ./tools/ship}\n"), true},
		{"a flow sequence of steps in the job", edit("    steps:\n      - uses: actions/checkout@v4\n        with:\n          persist-credentials: false\n      - uses: actions/setup-go@v5\n        with:\n          go-version-file: .go-version\n      - run: make -n bootstrap\n      - run: make bootstrap\n      - run: make doctor\n      - run: make test-canary\n", "    steps: [{run: make test-canary}]\n"), true},
		{"a flow mapping that holds the guard keys", edit("      - uses: actions/checkout@v4\n        with:\n          persist-credentials: false\n", "      - uses: actions/checkout@v4\n        with: {persist-credentials: false}\n"), true},
		{"a complex key in the job", edit("    timeout-minutes: 30\n", "    timeout-minutes: 30\n    ? if\n    : always()\n"), true},
		{"a merge key in the job", edit("    timeout-minutes: 30\n", "    timeout-minutes: 30\n    <<: *guard\n"), true},
		{"an alias in the job", edit("      - run: make test-canary\n", "      - run: make test-canary\n      - *extra\n"), true},
		{"an anchor in the job", edit("      - run: make doctor\n", "      - run: &d make doctor\n"), true},
		{"a YAML tag in the job", edit("      - run: make doctor\n", "      - run: !!str make doctor\n"), true},
		{"permissions written as a flow mapping stay allowed", edit("    permissions:\n      contents: read\n    steps", "    permissions: {contents: read}\n    steps"), false},
		{"a local action outside .github in another workflow", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - uses: ./tools/ship\n"}, true},
		{"a local action at the root in another workflow", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - uses: ./\n"}, true},
		{"a quoted local action outside .github under a quoted key", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - 'uses': \"./tools/ship\"\n"}, true},
		{"a local action that leaves .github", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - uses: ./.github/../tools/ship\n"}, true},
		{"a local action outside .github in a composite action", map[string]string{"test.yml": goodWorkflow, ".github/actions/b/action.yml": "runs:\n  using: composite\n  steps:\n    - uses: ./tools/ship\n"}, true},
		{"a local action under .github in another workflow", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - uses: ./.github/actions/b\n"}, false},
		{"an action of another repository in another workflow", map[string]string{"test.yml": goodWorkflow, "other.yml": "jobs:\n  b:\n    steps:\n      - uses: some-org/ship@v1\n"}, false},
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

func TestCanaryTagUnreadForms(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []string
	}{
		{"block forms only", "    runs-on: x\n    permissions:\n      contents: read\n    steps:\n      - uses: a@v1\n        with:\n          k: v\n      - run: ${{ x }}\n", nil},
		{"permissions as a flow mapping", "    permissions: {contents: read}\n", nil},
		{"the lines of a block scalar are text", "      - run: |\n          { make doctor; }\n          [ -f x ] && *.go\n      - run: make x\n", nil},
		{"a folded block scalar", "      - run: >-\n          {a}\n    name: x\n", nil},
		{"text after the block scalar is read again", "      - run: |\n          a\n      - {run: b}\n", []string{"{run: b}"}},
		{"a flow step", "      - {uses: ./x}\n", []string{"{uses: ./x}"}},
		{"a flow sequence", "    steps: [a, b]\n", []string{"steps: [a, b]"}},
		{"a flow sequence entry", "      - [a]\n", []string{"[a]"}},
		{"a flow mapping value", "        with: {a: b}\n", []string{"with: {a: b}"}},
		{"a complex key", "    ? if\n", []string{"? if"}},
		{"a merge key", "    <<: *x\n", []string{"<<: *x"}},
		{"an anchor", "    a: &x b\n", []string{"a: &x b"}},
		{"an alias", "      - *x\n", []string{"*x"}},
		{"a YAML tag", "    a: !!str b\n", []string{"a: !!str b"}},
		{"a comment", "    # {a: b}\n    a: b # [c]\n", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := workflowUnreadForms(strings.Split(tc.text, "\n"))
			if !slices.Equal(got, tc.want) {
				t.Fatalf("%q, want %q", got, tc.want)
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
		// Go's ./... skips folders that start with a dot or an underscore,
		// such as a nested checkout under a hidden tool folder with its own
		// go.mod.
		{"folders Go skips", map[string]string{
			"internal/leadcanary/a.go":                   hdrTagged + "\n\npackage a\n",
			".tool/worktrees/w/go.mod":                   "module ataniq\n",
			".tool/worktrees/w/internal/leadcanary/a.go": hdrTagged + "\n\npackage a\n",
			"internal/_old/a.go":                         hdrTagged + "\n\npackage a\n",
			".hidden/a.go":                               hdrTagged + "\n\npackage a\n",
		}, "test -tags ataniq_canary ./internal/leadcanary ./tests/"},
		// The tree the slice leaves, with a nested checkout under a hidden folder.
		{"a correct tree with tagged packages", canaryCorrectTree(), "test -tags ataniq_canary " +
			"./cmd/ataniq-canary ./cmd/ataniq-canary-standin ./cmd/ataniq-holder ./cmd/ataniqd " +
			"./internal/leadcanary ./internal/leadcanary/provision ./internal/leadcanary/provision/provisiontest " +
			"./internal/leadcanary/standin ./internal/leaddaemon ./internal/leadholder/canaryroot " +
			"./internal/leadholder/injectbridge ./internal/leadholder/sandboxstart ./internal/leadholder/serve ./tests/"},
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
