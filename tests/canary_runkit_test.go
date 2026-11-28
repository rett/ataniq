package tests

// This file checks the kit of the attended canary run: the Makefile target
// that builds its binaries, the lines of AGENTS.md about it, and the run
// script, which the tests run with --dry only. It is not behind the canary
// build tag and passes with and without it. Nothing here starts a unit, a
// namespace, a service or an agent: the dry runs get a PATH that holds only
// stubs which record being called, and none may be called.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadsandbox/profile"
)

const canaryRunScript = "qualification/canary/run/canary-run.sh"

// canaryRunBinaries are the commands the run script uses.
var canaryRunBinaries = []string{
	"ataniqd", "ataniq-holder", "ataniq-canary", "ataniq-canary-standin",
	"ataniq-console", "ataniq-intake", "ataniq-probe",
}

// canaryStaticBinaries are built without cgo for use inside the sandbox.
var canaryStaticBinaries = []string{"ataniq-canary-standin", "ataniq-probe", "ataniq-hook"}

// canaryStep is one row of the plan's table of steps (section 6), written
// from the plan and not from the script: the gates the step serves ("none"
// where the table names none) and whether each pass runs it.
type canaryStep struct {
	serves string
	a, b   bool
}

var canarySteps = map[int]canaryStep{
	1:  {"G4, L10", true, true},
	2:  {"G4, G5, G2", true, true},
	3:  {"G8", true, true},
	4:  {"G6", true, true},
	5:  {"G2", false, true},
	6:  {"G7", false, true},
	7:  {"G2", false, true},
	8:  {"G5", true, true},
	9:  {"G3, M9", true, true},
	10: {"G4", true, true},
	11: {"G4, M6", true, true},
	12: {"G5", true, true},
	13: {"none", true, false},
	14: {"none", true, true},
	15: {"G2", false, true},
	16: {"B1", false, true},
	17: {"G1, G3", true, true},
	18: {"G2", true, true},
	19: {"G1", true, true},
	20: {"G6", true, true},
	21: {"G2", false, true},
	22: {"G4", true, true},
	23: {"G2", false, true},
	24: {"G7", false, true},
	25: {"G2", false, true},
	26: {"G2", true, true},
}

// canaryChain is the plan's failure path, written from the plan: the
// clean-up steps a failure at step n runs, in order. In pass B a failure from
// step 6 on runs 22, 24, 25 and 26, and 21 and 23 as well once step 15 has
// begun; in pass A a failure from step 11 on runs 22 and 26. A step the pass
// does not run cannot fail.
func canaryChain(pass string, n int) []int {
	step := canarySteps[n]
	switch {
	case pass == "A" && step.a && n >= 11:
		return []int{22, 26}
	case pass == "B" && step.b && n >= 15:
		return []int{21, 22, 23, 24, 25, 26}
	case pass == "B" && step.b && n >= 6:
		return []int{22, 24, 25, 26}
	}
	return nil
}

func canaryRuns(pass string, n int) bool {
	if pass == "A" {
		return canarySteps[n].a
	}
	return canarySteps[n].b
}

// canaryStubTools are the programs a real run of the script calls by name.
// The dry run gets a PATH that holds only stubs of them.
var canaryStubTools = []string{
	"systemctl", "ss", "loginctl", "git", "bwrap", "pasta", "date", "mkdir", "rmdir", "rm", "cp",
	"sha256sum", "stat", "awk", "sed", "grep", "cat", "find", "sort", "cut", "tr", "cmp", "diff",
	"sleep", "env", "mktemp", "id", "sh", "timeout", "journalctl",
}

// canaryDry runs the script with --dry and the further arguments, as
// canaryScript does.
func canaryDry(t *testing.T, args ...string) (string, int) {
	t.Helper()
	return canaryScript(t, append([]string{"--dry"}, args...)...)
}

// canaryScript runs the script with the arguments (--dry and more, or the
// --self-test entry) in a fresh folder, with a PATH and a build/canary folder
// that hold only recording stubs. It returns the output and the exit status,
// and fails the test when a stub was called or the run made any file.
func canaryScript(t *testing.T, args ...string) (string, int) {
	t.Helper()
	if len(args) == 0 || (args[0] != "--dry" && args[0] != "--self-test") {
		t.Fatalf("the tests run the script with --dry or --self-test only: %v", args)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "stubs")
	work := filepath.Join(dir, "work")
	home := filepath.Join(dir, "home")
	calls := filepath.Join(dir, "calls")
	for _, d := range []string{bin, home, filepath.Join(work, "build", "canary")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stub := "#!/bin/sh\necho \"$0 $*\" >>'" + calls + "'\nexit 97\n"
	for _, name := range canaryStubTools {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(stub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range canaryRunBinaries {
		if err := os.WriteFile(filepath.Join(work, "build", "canary", name), []byte(stub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	before := canaryTree(t, dir)

	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(root(t), canaryRunScript)}, args...)...)
	cmd.Dir = work
	cmd.Env = []string{"PATH=" + bin, "HOME=" + home, "XDG_RUNTIME_DIR=" + filepath.Join(dir, "run")}
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("the script did not run: %v", err)
		}
		code = exit.ExitCode()
	}
	if called, err := os.ReadFile(calls); err == nil {
		t.Fatalf("the run %v started something:\n%s", args, called)
	}
	if after := canaryTree(t, dir); !slices.Equal(before, after) {
		t.Fatalf("the run %v changed the folder:\nbefore %v\nafter  %v", args, before, after)
	}
	if bytes.Contains(out, []byte("not found")) {
		t.Fatalf("the run %v looked for a program:\n%s", args, out)
	}
	return string(out), code
}

// canaryTree lists every path below dir.
func canaryTree(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		paths = append(paths, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	return paths
}

// canaryBlock is one heading of the dry output with the lines below it.
type canaryBlock struct {
	heading string
	lines   []string
}

func canaryBlocks(out string) []canaryBlock {
	var blocks []canaryBlock
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "== ") {
			blocks = append(blocks, canaryBlock{heading: line})
			continue
		}
		if len(blocks) > 0 {
			b := &blocks[len(blocks)-1]
			b.lines = append(b.lines, line)
		}
	}
	return blocks
}

func (b canaryBlock) line(prefix string) (string, bool) {
	for _, l := range b.lines {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(l), prefix); ok {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

func (b canaryBlock) count(sub string) int {
	n := 0
	for _, l := range b.lines {
		if strings.Contains(l, sub) {
			n++
		}
	}
	return n
}

var (
	canaryStepHeading    = regexp.MustCompile(`^== step ([0-9]+) \[pass ([AB])(, second epoch)?\]: (.*)$`)
	canaryCleanupHeading = regexp.MustCompile(`^== clean-up step ([0-9]+) \[pass ([AB])\]: `)
)

func TestCanaryRunKitMakefileTarget(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	if !slices.Contains(strings.Fields(lines[0]), "canary-build") || !strings.HasPrefix(lines[0], ".PHONY:") {
		t.Errorf("the .PHONY line does not list canary-build: %q", lines[0])
	}
	at := slices.Index(lines, "canary-build: doctor")
	if at < 0 {
		t.Fatal("the Makefile has no target \"canary-build: doctor\"")
	}
	var recipe []string
	for _, l := range lines[at+1:] {
		if !strings.HasPrefix(l, "\t") {
			break
		}
		recipe = append(recipe, strings.TrimPrefix(l, "\t"))
	}
	if len(recipe) == 0 || recipe[0] != "@mkdir -p build/canary" {
		t.Fatalf("the recipe does not begin by making build/canary: %q", recipe)
	}
	// Every other line is one build through the Go wrapper, with the tag, into
	// build/canary: nothing else, so the target runs nothing it builds.
	build := regexp.MustCompile(`^@(CGO_ENABLED=0 GOOS=linux GOARCH=amd64 )?sh scripts/go-tool\.sh build -tags ` + canaryTag + ` -trimpath -o build/canary/([a-z-]+) \./cmd/([a-z-]+)$`)
	var built []string
	for _, l := range recipe[1:] {
		m := build.FindStringSubmatch(l)
		if m == nil {
			t.Errorf("a recipe line is not one tagged build into build/canary: %q", l)
			continue
		}
		if m[2] != m[3] {
			t.Errorf("the binary %s is built from ./cmd/%s", m[2], m[3])
		}
		if static := slices.Contains(canaryStaticBinaries, m[2]); static != (m[1] != "") {
			t.Errorf("%s: static build is %v, want %v", m[2], m[1] != "", static)
		}
		if st, err := os.Stat(filepath.Join(root(t), "cmd", m[3])); err != nil || !st.IsDir() {
			t.Errorf("./cmd/%s does not exist", m[3])
		}
		built = append(built, m[2])
	}
	want := append(slices.Clone(canaryRunBinaries), "ataniq-hook")
	sort.Strings(want)
	sort.Strings(built)
	if !slices.Equal(built, want) {
		t.Errorf("canary-build builds %v, want %v", built, want)
	}

	// The script takes its commands from build/canary.
	script, err := os.ReadFile(filepath.Join(root(t), canaryRunScript))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range canaryRunBinaries {
		if !regexp.MustCompile(`(\$BIN/|[ "])` + regexp.QuoteMeta(name) + `([ ";)]|$)`).Match(script) {
			t.Errorf("the script does not use %s", name)
		}
	}
	if !bytes.Contains(script, []byte("BIN=$REPO/build/canary\n")) {
		t.Error("the script does not take its binaries from build/canary of its checkout")
	}

	ignore, err := os.ReadFile(filepath.Join(root(t), ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(strings.Split(string(ignore), "\n"), "build/") {
		t.Error(".gitignore does not ignore build/")
	}
}

func TestCanaryRunKitInstructionLines(t *testing.T) {
	agents, err := os.ReadFile(filepath.Join(root(t), "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	var kit string
	for _, l := range strings.Split(string(agents), "\n") {
		if strings.HasPrefix(l, "- Canary run kit: ") {
			kit = l
		}
	}
	if kit == "" {
		t.Fatal("AGENTS.md has no item \"- Canary run kit: \"")
	}
	for _, want := range append([]string{
		"`make canary-build`", "`" + canaryTag + "`", "`build/canary/`", "`" + canaryRunScript + "`",
		"`--dry`", "starts nothing", "only by the owner's hand",
		"`sh scripts/go-tool.sh test ./tests/ -run CanaryRunKit`", "(qualification/canary/run/README.md)",
	}, canaryRunBinaries...) {
		if !strings.Contains(kit, want) {
			t.Errorf("the run kit item of AGENTS.md does not say %s", want)
		}
	}
	// Three targets build with a tag, each the one for its tags: the run kit
	// with the canary tag alone, the owner's daemon with the owner channel's tag
	// alone, and the daemon for real scratch leads with the seat gate with both.
	const oneTarget = "`make canary-build` is the one target that builds binaries with the canary tag"
	oneTargets := []string{
		oneTarget + " alone",
		"`make ownerchannel-build` is the one target that builds a binary with that tag alone",
		"`make scratch-seat-build` is the one target that builds the daemon with both tags",
	}
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "no other target builds with the tag") {
			t.Errorf("%s still says that no other target builds with the tag", name)
		}
		if strings.Contains(string(data), "no other target builds the pair") {
			t.Errorf("%s still says that no other target builds the pair of tags", name)
		}
		if !strings.Contains(string(data), oneTarget) {
			t.Errorf("%s does not say: %s", name, oneTarget)
		}
		for _, want := range oneTargets {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s does not say: %s", name, want)
			}
		}
		// The pair's build is for the owner's own trial and no release's.
		if !regexp.MustCompile("`make scratch-seat-build` is the one target that builds the daemon with both tags[^.]*`build/scratch-seat/`[^.]*for the owner's own trial of real scratch leads with the seat gate[^.]*no release and no CI job uses it").Match(data) {
			t.Errorf("%s does not say that make scratch-seat-build builds into build/scratch-seat/ for the owner's own trial and that no release and no CI job uses it", name)
		}
	}
	// The guides that name the builds with a tag name the pair's build too.
	for _, name := range []string{"README.md", "docs/lead-daemon.md", "docs/seat-gate.md"} {
		data, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "`make scratch-seat-build`") {
			t.Errorf("%s does not name make scratch-seat-build", name)
		}
	}
	// The two helpers are fenced too: the reset deletes after a typed word, the
	// prepare writes under the cache folder.
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"`qualification/canary/run/canary-reset.sh` also deletes directories",
			"only by the owner's hand, never by an agent",
			"`--dry` and `--self-test`",
			"`qualification/canary/run/canary-prepare.sh`",
		} {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s does not say: %s", name, want)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root(t), "qualification/canary/run/README.md")); err != nil {
		t.Error(err)
	}
}

// The script keeps two values of the sandbox profile as plain assignments, and
// no shell can import a Go constant: the mirror's place inside the sandbox (the
// check of step 8 and of the tree's remote) and the profile version that step
// 12 reads in holder.json. A change of the constant without the script would
// fail a good run, so the test ties them.
func TestCanaryRunKitConstantsMatchTheProfile(t *testing.T) {
	lines := strings.Split(canaryScriptText(t), "\n")
	for _, want := range []string{
		"MIRROR_ROOT_INSIDE=" + profile.MirrorRootInside,
		"PROFILE_VERSION=" + strconv.Itoa(profile.Version),
	} {
		if !slices.Contains(lines, want) {
			t.Errorf("%s has no line %q: the script and the profile package drifted", canaryRunScript, want)
		}
	}
	// Each is assigned once, so the line above is the value the script uses.
	for _, name := range []string{"MIRROR_ROOT_INSIDE=", "PROFILE_VERSION="} {
		n := 0
		for _, l := range lines {
			if strings.HasPrefix(l, name) {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s assigns %s %d times, want once", canaryRunScript, strings.TrimSuffix(name, "="), n)
		}
	}
}

func TestCanaryRunKitScriptIsPlainShell(t *testing.T) {
	canaryPlainShell(t, canaryRunScript, true)
	// It never names the tag's build: it only runs what canary-build built.
	text := canaryScriptText(t)
	if strings.Contains(text, "go-tool.sh") || strings.Contains(text, "go build") {
		t.Error("the script builds something")
	}
}

// canaryPlainShell checks one script of the kit (path from the repository
// root): it parses with sh -n and uses no form of another shell. A run script
// starts with #!/bin/sh and sets -eu; the shared library (runnable false) is
// only sourced.
func canaryPlainShell(t *testing.T, rel string, runnable bool) {
	t.Helper()
	path := filepath.Join(root(t), rel)
	if out, err := exec.Command("/bin/sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("sh -n %s: %v\n%s", rel, err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if runnable && !strings.HasPrefix(text, "#!/bin/sh\n") {
		t.Errorf("%s does not start with #!/bin/sh", rel)
	}
	if runnable && !strings.Contains(text, "\nset -eu\n") {
		t.Errorf("%s does not set -eu", rel)
	}
	// The forms of another shell that sh -n of a larger shell accepts.
	for name, re := range map[string]*regexp.Regexp{
		"[[":            regexp.MustCompile(`(^|[ \t;])\[\[ `),
		"local":         regexp.MustCompile(`(?m)^\s*local `),
		"function":      regexp.MustCompile(`(?m)^\s*function `),
		"an array":      regexp.MustCompile(`(?m)^\s*[A-Za-z_]+=\(`),
		"source":        regexp.MustCompile(`(?m)^\s*source `),
		"a here string": regexp.MustCompile(`<<<`),
		"echo -e":       regexp.MustCompile(`echo -[en]`),
		"read -p":       regexp.MustCompile(`read -[a-qs-z]`),
	} {
		if loc := re.FindStringIndex(text); loc != nil {
			t.Errorf("%s uses %s, which is not POSIX sh: %q", rel, name, text[loc[0]:min(len(text), loc[1]+40)])
		}
	}
}

func TestCanaryRunKitDryListsEveryStep(t *testing.T) {
	for _, pass := range []string{"A", "B"} {
		t.Run("pass "+pass, func(t *testing.T) {
			out, code := canaryDry(t, "--pass", pass, "--config", "/nonexistent/sample/canary.json")
			if code != 0 {
				t.Fatalf("exit status %d:\n%s", code, out)
			}
			first := map[int]canaryBlock{}
			var order []int
			secondEpoch := []int{}
			for _, b := range canaryBlocks(out) {
				m := canaryStepHeading.FindStringSubmatch(b.heading)
				if m == nil {
					continue
				}
				n, _ := strconv.Atoi(m[1])
				if m[2] != pass {
					t.Errorf("a heading of pass %s in pass %s: %s", m[2], pass, b.heading)
				}
				if m[3] != "" {
					secondEpoch = append(secondEpoch, n)
				}
				if _, seen := first[n]; !seen {
					first[n] = b
					order = append(order, n)
				}
			}
			for n := 1; n <= 26; n++ {
				b, ok := first[n]
				if !ok {
					t.Errorf("step %d is not in the dry output", n)
					continue
				}
				if got, _ := b.line("serves:"); got != canarySteps[n].serves {
					t.Errorf("step %d serves %q, want %q", n, got, canarySteps[n].serves)
				}
				skipped := strings.Contains(b.heading, ": skipped in pass "+pass+": ")
				if skipped == canaryRuns(pass, n) {
					t.Errorf("step %d: skipped is %v in pass %s: %s", n, skipped, pass, b.heading)
				}
				if skipped {
					if len(b.lines) > 3 || b.count("[dry]") != 0 {
						t.Errorf("the skipped step %d does something:\n%s", n, strings.Join(b.lines, "\n"))
					}
					continue
				}
				for _, part := range []string{"does:", "expected:"} {
					if text, ok := b.line(part); !ok || text == "" {
						t.Errorf("step %d has no %s line", n, part)
					}
				}
			}
			if len(first) != 26 {
				t.Errorf("%d different step numbers, want 26", len(first))
			}
			if !sort.IntsAreSorted(order) {
				t.Errorf("the steps first appear out of order: %v", order)
			}
			// The second epoch of pass A: steps 11, 12 and 14 again, one
			// inject, then step 22 in full. Pass B has none.
			wantSecond := []int{}
			if pass == "A" {
				wantSecond = []int{11, 12, 14, 17, 22}
			}
			if !slices.Equal(secondEpoch, wantSecond) {
				t.Errorf("the second epoch runs steps %v, want %v", secondEpoch, wantSecond)
			}
			if has := strings.Contains(out, "== second epoch of pass A (after step 20)"); has != (pass == "A") {
				t.Errorf("the second epoch heading is present: %v", has)
			}
			if pass == "A" {
				for _, want := range []string{"ataniq-canary replay-on --config", "order.txt says 7501-first", "result.txt says committed", "submitted, no reason, whole"} {
					if !strings.Contains(out, want) {
						t.Errorf("the second epoch does not say %q", want)
					}
				}
			}

			step10 := strings.Join(first[10].lines, "\n")
			if !strings.Contains(step10, "ataniqd init --state-dir \"/sample/canary/state\"\n"+
				"     build/canary/ataniqd project-home --state-dir \"/sample/canary/state\" --home self throwaway-one\n"+
				"     build/canary/ataniqd run --canary ") {
				t.Errorf("step 10 lacks the ordered enrollment and start commands:\n%s", step10)
			}

			// Step 11 is one step: one key, before define, and none between
			// the start and the read-back of the balanced limit.
			b := first[11]
			if n := b.count("wait for Enter"); n != 1 {
				t.Errorf("step 11 waits for %d keys, want 1", n)
			}
			text := strings.Join(b.lines, "\n")
			var at []int
			for _, part := range []string{
				"would wait for Enter",
				"would run: build/canary/ataniqd canary define",
				"would run: build/canary/ataniqd canary start",
				"would read MemoryMax, MemorySwapMax, TasksMax",
				"would read memory.max, memory.swap.max and pids.max",
				"would read MemAvailable",
				"would compute the balanced value",
				"would go to the clean-up chain when the value is below the floor",
				"would run: systemctl --user set-property --runtime",
				"would check: both readings of the memory limit",
			} {
				i := strings.Index(text, "[dry] "+part)
				if i < 0 {
					t.Errorf("step 11 does not say %q", part)
				}
				at = append(at, i)
			}
			if !sort.IntsAreSorted(at) {
				t.Errorf("step 11 is out of order: %v\n%s", at, text)
			}
			// Every step that runs a command waits for a key before it.
			for n := 1; n <= 26; n++ {
				b := first[n]
				run, wait := -1, -1
				for i, l := range b.lines {
					if run < 0 && strings.Contains(l, "[dry] would run: ") {
						run = i
					}
					if wait < 0 && strings.Contains(l, "[dry] would wait for Enter") {
						wait = i
					}
				}
				if run >= 0 && (wait < 0 || wait > run) {
					t.Errorf("step %d runs a command before it waits for a key:\n%s", n, strings.Join(b.lines, "\n"))
				}
			}
			// Step 17's one-Enter rule, in pass B.
			if pass == "B" {
				b := first[17]
				for _, want := range []string{
					"on uncertain, not-submitted, partial and on nothing else: the owner presses Enter once",
					"record the line owner-enter",
					"on uncertain, died: press nothing",
				} {
					if b.count(want) != 1 {
						t.Errorf("step 17 does not say %q", want)
					}
				}
			}
			if !strings.Contains(out, "== pass "+pass+" is complete") {
				t.Error("the dry run does not end with the pass complete")
			}
		})
	}
}

func TestCanaryRunKitFailurePath(t *testing.T) {
	for _, pass := range []string{"A", "B"} {
		for n := 1; n <= 26; n++ {
			t.Run(fmt.Sprintf("pass %s step %d", pass, n), func(t *testing.T) {
				out, code := canaryDry(t, "--pass", pass, "--config", "/nonexistent/sample/canary.json", "--fail-at", strconv.Itoa(n))
				want := canaryChain(pass, n)
				var got []int
				for _, b := range canaryBlocks(out) {
					m := canaryCleanupHeading.FindStringSubmatch(b.heading)
					if m == nil {
						continue
					}
					k, _ := strconv.Atoi(m[1])
					got = append(got, k)
					// Each step of the chain waits for its key or the owner's
					// answer, records a result, and says what it does when
					// there is nothing to act on.
					if b.count("wait for Enter") == 0 && b.count("ask and record the answer") == 0 {
						t.Errorf("clean-up step %d waits for nothing:\n%s", k, strings.Join(b.lines, "\n"))
					}
					if _, ok := b.line("result:"); !ok {
						t.Errorf("clean-up step %d records no result", k)
					}
					if b.count("nothing to") == 0 {
						t.Errorf("clean-up step %d does not say what it does with nothing to act on", k)
					}
					if got, _ := b.line("serves:"); got != canarySteps[k].serves {
						t.Errorf("clean-up step %d serves %q, want %q", k, got, canarySteps[k].serves)
					}
				}
				if !slices.Equal(got, want) {
					t.Errorf("a failure at step %d of pass %s runs the clean-up steps %v, want %v", n, pass, got, want)
				}
				if !canaryRuns(pass, n) {
					// The pass does not run the step, so nothing fails.
					if code != 0 || strings.Contains(out, "FAILED") {
						t.Errorf("exit status %d for a step the pass skips:\n%s", code, out)
					}
					return
				}
				if code != 1 {
					t.Errorf("exit status %d, want 1", code)
				}
				if !strings.Contains(out, fmt.Sprintf("FAILED at step %d: a failure injected by --fail-at", n)) {
					t.Errorf("the failure at step %d is not reported", n)
				}
				// Nothing of the pass follows the failure: the last ordinary
				// step heading is the failed step's.
				last := 0
				for _, b := range canaryBlocks(out) {
					if m := canaryStepHeading.FindStringSubmatch(b.heading); m != nil {
						last, _ = strconv.Atoi(m[1])
					}
				}
				if last != n {
					t.Errorf("the pass went on to step %d after the failure at step %d", last, n)
				}
				chain := fmt.Sprintf("== clean-up chain after the failure at step %d [pass %s]: steps", n, pass)
				if has := strings.Contains(out, chain); has != (len(want) > 0) {
					t.Errorf("the chain heading is present: %v, want %v", has, len(want) > 0)
				}
				if has := strings.Contains(out, "== no clean-up chain: "); has != (len(want) == 0) {
					t.Errorf("the no-chain heading is present: %v, want %v", has, len(want) == 0)
				}
			})
		}
	}
}

func TestCanaryRunKitDryRefusesBadArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"no pass":            {"--config", "/nonexistent/sample/canary.json"},
		"an unknown pass":    {"--pass", "C", "--config", "/nonexistent/sample/canary.json"},
		"no config":          {"--pass", "A"},
		"an unknown flag":    {"--pass", "A", "--config", "/nonexistent/sample/canary.json", "--yes"},
		"a step that is not": {"--pass", "A", "--config", "/nonexistent/sample/canary.json", "--fail-at", "27"},
		"a step in words":    {"--pass", "A", "--config", "/nonexistent/sample/canary.json", "--fail-at", "two"},
	} {
		t.Run(name, func(t *testing.T) {
			out, code := canaryDry(t, args...)
			if code != 2 || !strings.Contains(out, "usage: canary-run.sh --pass A|B --config CONFIG [--dry] [--evidence DIR]") || strings.Contains(out, "== step") {
				t.Errorf("exit status %d:\n%s", code, out)
			}
		})
	}
}

// This file checks the code a real run of the canary run script takes, not
// the text its dry run prints: the dry run of a step is a branch of its own,
// so a test of the dry text alone passes while the real branch is wrong. Two
// means are used. The script's --self-test entry gives sample values to one of
// the script's own functions (the same function the step calls) and prints
// its answer; it only computes and starts nothing. And the script's text is
// read: the body of a step is searched for what it must and must not hold.
// Like the rest of the kit's tests, nothing here starts a unit, a namespace,
// a service or an agent, and the script is never run as a pass without --dry.

// canarySelfTest runs the script's test entry over the recording stubs and
// returns its output without the last line feed, and the exit status.
func canarySelfTest(t *testing.T, args ...string) (string, int) {
	t.Helper()
	out, code := canaryScript(t, append([]string{"--self-test"}, args...)...)
	return strings.TrimSuffix(out, "\n"), code
}

// canaryScriptText is the script.
func canaryScriptText(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root(t), canaryRunScript))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

var canaryFuncStart = regexp.MustCompile(`(?m)^([a-z_][a-z_0-9]*)\(\) \{(.*)$`)

// canaryFuncs returns the body of every function the script defines, by name.
// A function is written "name() {" at the start of a line and ends at the
// next line that is "}" alone, or on its own line when that line ends in "}".
func canaryFuncs(t *testing.T, script string) map[string]string {
	t.Helper()
	funcs := map[string]string{}
	for _, m := range canaryFuncStart.FindAllStringSubmatchIndex(script, -1) {
		name, rest := script[m[2]:m[3]], script[m[4]:m[5]]
		if _, twice := funcs[name]; twice {
			t.Fatalf("the script defines %s twice", name)
		}
		if strings.HasSuffix(strings.TrimSpace(rest), "}") {
			funcs[name] = rest
			continue
		}
		end := strings.Index(script[m[5]:], "\n}\n")
		if end < 0 {
			t.Fatalf("the function %s does not end", name)
		}
		funcs[name] = script[m[5] : m[5]+end+1]
	}
	return funcs
}

// canaryCalls counts the places where the body names the word: the name of a
// function or a builtin, not a part of a longer name. Comment lines do not
// count.
func canaryCalls(body, word string) int {
	re := regexp.MustCompile(`(^|[^A-Za-z0-9_$-])` + regexp.QuoteMeta(word) + `($|[^A-Za-z0-9_=-])`)
	n := 0
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		n += len(re.FindAllStringIndex(line, -1))
	}
	return n
}

// canaryReach returns the names of the script's functions that the function
// calls, directly or through another one, without the function itself.
func canaryReach(funcs map[string]string, from string) []string {
	seen := map[string]bool{from: true}
	queue := []string{from}
	var reach []string
	for len(queue) > 0 {
		body := funcs[queue[0]]
		queue = queue[1:]
		for name := range funcs {
			if !seen[name] && canaryCalls(body, name) > 0 {
				seen[name] = true
				queue = append(queue, name)
				reach = append(reach, name)
			}
		}
	}
	return reach
}

// R1: the balanced value is the smaller of the ceiling and the share of the
// available memory, rounded down to whole pages, never above the ceiling;
// below the floor nothing is set. The values are the plan's rule worked by
// hand, not the script's own arithmetic.
func TestCanaryRunKitBalanceIsR1(t *testing.T) {
	const gib = 1 << 30
	for _, c := range []struct {
		name                           string
		ceiling, share, floor, availKB int64
		wantValue                      int64
		wantFloor                      string
	}{
		{"the share is below the ceiling", 4 * gib, 50, gib, 6291456, 3 * gib, "ok"},
		{"the ceiling caps a large host", 4 * gib, 50, gib, 64 * 1024 * 1024, 4 * gib, "ok"},
		{"the share equals the ceiling", 4 * gib, 50, gib, 8 * 1024 * 1024, 4 * gib, "ok"},
		{"rounded down to whole pages", 4 * gib, 50, gib, 6291461, 3*gib + 0, "ok"},
		{"a page more is a page more", 4 * gib, 50, gib, 6291464, 3*gib + 4096, "ok"},
		{"a ceiling between pages is rounded down, never up", 4*gib + 100, 50, gib, 64 * 1024 * 1024, 4 * gib, "ok"},
		{"another share", 4 * gib, 25, gib, 8 * 1024 * 1024, 2 * gib, "ok"},
		{"exactly the floor is not below it", 4 * gib, 50, gib, 2 * 1024 * 1024, gib, "ok"},
		{"one page under the floor", 4 * gib, 50, gib, 2*1024*1024 - 8, gib - 4096, "below"},
		{"a busy host", 4 * gib, 50, gib, 1000001, 512000000, "below"},
		{"no memory at all", 4 * gib, 50, gib, 0, 0, "below"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, code := canarySelfTest(t, "balance",
				fmt.Sprint(c.ceiling), fmt.Sprint(c.share), fmt.Sprint(c.floor), fmt.Sprint(c.availKB))
			want := fmt.Sprintf("value=%d\nfloor=%s", c.wantValue, c.wantFloor)
			if code != 0 || out != want {
				t.Errorf("exit status %d, got:\n%s\nwant:\n%s", code, out, want)
			}
			if c.wantValue > c.ceiling || c.wantValue%4096 != 0 {
				t.Fatalf("the case is wrong: %d", c.wantValue)
			}
		})
	}

	// Step 11 uses these two functions and no arithmetic of its own.
	body := canaryFuncs(t, canaryScriptText(t))["step_11"]
	for _, want := range []string{
		`BAL=$(balance "$_avail" "$MAXB" "$SHARE")`,
		`if below_floor "$BAL" "$FLOORB"; then`,
		`systemctl --user set-property --runtime "$UNIT" "MemoryMax=$BAL"`,
	} {
		if strings.Count(body, want) != 1 {
			t.Errorf("step 11 does not hold once: %s", want)
		}
	}
	if at, set := strings.Index(body, `if below_floor "$BAL" "$FLOORB"; then`), strings.Index(body, "set-property --runtime \"$UNIT\""); at < 0 || set < at {
		t.Error("step 11 sets the limit before it compares the value with the floor")
	}
}

// Step 11 is one step with one key (R1): the real branch waits once, before
// define, and neither it nor anything it calls waits or asks again.
func TestCanaryRunKitStepElevenHasOneKey(t *testing.T) {
	funcs := canaryFuncs(t, canaryScriptText(t))
	body, ok := funcs["step_11"]
	if !ok {
		t.Fatal("the script has no step_11")
	}
	if n := canaryCalls(body, "wait_key"); n != 1 {
		t.Errorf("the body of step 11 waits for %d keys, want 1", n)
	}
	for _, word := range []string{"ask", "affirm", "read_answer", "sleep"} {
		if n := canaryCalls(body, word); n != 0 {
			t.Errorf("the body of step 11 calls %s", word)
		}
	}
	if strings.Contains(body, "read -r") {
		t.Error("the body of step 11 reads a line itself")
	}
	if key, define := strings.Index(body, "wait_key "), strings.Index(body, "runcap define "); key < 0 || define < key {
		t.Error("step 11 does not wait for its key before define")
	}
	for _, name := range canaryReach(funcs, "step_11") {
		for _, word := range []string{"wait_key", "ask", "affirm"} {
			if canaryCalls(funcs[name], word) != 0 {
				t.Errorf("step 11 calls %s, which calls %s", name, word)
			}
		}
	}
	for _, name := range []string{"runcap", "check", "read_limits", "balance", "below_floor", "fld"} {
		if canaryCalls(body, name) == 0 {
			t.Errorf("step 11 does not call %s: the test reads another script than it expects", name)
		}
	}
}

// The words of the wire (the seams note, section 3): each of the three is
// from a closed set. The empty reason is "none".
var (
	canaryOutcomes = []string{"not-now", "refused", "submitted", "uncertain"}
	canaryReasons  = []string{"", "not-ready", "blocked", "untested", "text", "not-submitted", "died"}
	canaryWritten  = []string{"nothing", "partial", "whole", "unknown"}
)

// N3: exactly one result of the injection gets the owner's Enter, uncertain
// with the reason not-submitted and written partial; died gets a look and no
// key. Every combination of the wire's words is asked of the function step 17
// uses, valid or not.
func TestCanaryRunKitOnlyOneResultGetsTheEnter(t *testing.T) {
	enters := 0
	for _, o := range canaryOutcomes {
		for _, r := range canaryReasons {
			for _, w := range canaryWritten {
				got, code := canarySelfTest(t, "inject-action", "B", o, r, w)
				if code != 0 {
					t.Fatalf("%s/%s/%s: exit status %d: %s", o, r, w, code, got)
				}
				want := got
				switch {
				case o == "uncertain" && r == "not-submitted" && w == "partial":
					want = "owner-enter"
					enters++
				case o == "uncertain" && r == "died":
					want = "look"
				case o == "submitted" && r == "" && w == "whole":
					want = "none"
				case o == "refused" || (o == "not-now" && w == "nothing"):
					// Nothing was written: the owner supplies the message, on
					// a refusal and on a not-now alike (the screen was not ready).
					want = "owner-supplies"
				case got == "owner-enter" || got == "look" || got == "none":
					want = "another action than " + got
				}
				if got != want {
					t.Errorf("pass B, %s/%s/%s: the action is %q, want %q", o, r, w, got, want)
				}
			}
		}
	}
	if enters != 1 {
		t.Fatalf("%d combinations get the Enter, want 1", enters)
	}
	// Pass A never asks for the owner's Enter in the pane.
	for _, o := range canaryOutcomes {
		for _, r := range canaryReasons {
			for _, w := range canaryWritten {
				if got, _ := canarySelfTest(t, "inject-action", "A", o, r, w); got == "owner-enter" || got == "" {
					t.Errorf("pass A, %s/%s/%s: the action is %q", o, r, w, got)
				}
			}
		}
	}

	// Step 17 takes its action from that function, and the owner-enter line
	// and the instruction to press Enter are in the arm of that action only.
	body := canaryFuncs(t, canaryScriptText(t))["step_17"]
	if strings.Count(body, `_act=$(inject_action "$PASS" "$_o" "$_r" "$_w")`) != 1 {
		t.Fatal("step 17 does not take its action from inject_action")
	}
	if strings.Count(body, "case $_act in\n") != 1 {
		t.Fatal("step 17 does not branch on the action once")
	}
	arms := regexp.MustCompile(`(?m)^    ([a-z*|" -]+)\)\n`).FindAllStringSubmatchIndex(body, -1)
	found := false
	for i, m := range arms {
		end := len(body)
		if i+1 < len(arms) {
			end = arms[i+1][0]
		}
		name, arm := body[m[2]:m[3]], body[m[1]:end]
		enter := strings.Contains(arm, "owner-enter") || strings.Contains(arm, "press Enter ONCE")
		if name == "owner-enter" {
			found = true
			for _, want := range []string{"press Enter ONCE in the pane", `>>"$EV/owner-enter.txt"`, "note 'owner-enter'", "wait_key "} {
				if strings.Count(arm, want) != 1 {
					t.Errorf("the owner-enter arm of step 17 does not hold once: %s", want)
				}
			}
		} else if enter {
			t.Errorf("the arm %q of step 17 asks for the owner's Enter:\n%s", name, arm)
		}
	}
	if !found {
		t.Fatal("step 17 has no arm for the action owner-enter")
	}
	if n := strings.Count(body, "owner-enter.txt"); n != 1 {
		t.Errorf("step 17 writes the owner-enter line in %d places, want 1", n)
	}
	if before := body[:strings.Index(body, "case $_act in\n")]; strings.Contains(before, "press Enter ONCE") || strings.Contains(before, "owner-enter.txt") {
		t.Error("step 17 asks for the owner's Enter before it branches on the action")
	}
}

// The test entry computes and prints: it refuses what it does not know, and
// it is not a way to run a pass.
func TestCanaryRunKitSelfTestRefusesBadArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"no name":            {},
		"an unknown name":    {"step", "11"},
		"too few values":     {"balance", "4294967296", "50", "1073741824"},
		"too many values":    {"balance", "4294967296", "50", "1073741824", "6291456", "1"},
		"a value in words":   {"balance", "4294967296", "half", "1073741824", "6291456"},
		"a negative value":   {"balance", "4294967296", "50", "1073741824", "-1"},
		"an action, 3 words": {"inject-action", "B", "uncertain", "died"},
	} {
		t.Run(name, func(t *testing.T) {
			out, code := canarySelfTest(t, args...)
			if code != 2 || !strings.Contains(out, "canary-run.sh --self-test NAME [VALUE...]") || strings.Contains(out, "== step") {
				t.Errorf("exit status %d:\n%s", code, out)
			}
		})
	}
	// With the test entry no step runs, whatever else the command line says.
	out, code := canarySelfTest(t, "balance", "4294967296", "50", "1073741824", "6291456", "--pass", "A")
	if code != 2 || strings.Contains(out, "== step") {
		t.Errorf("exit status %d:\n%s", code, out)
	}
	text := canaryScriptText(t)
	at := strings.Index(text, "\nif [ \"$SELFTEST\" -eq 1 ]; then\n  self_test \"$@\"\n  exit 0\nfi\n")
	if at < 0 {
		t.Fatal("the script does not end after the test entry")
	}
	for _, later := range []string{"\nif [ \"$DRY\" -eq 1 ]; then\n  REPO=", "\ntrap on_exit EXIT\n", "\nstep_1\n"} {
		if i := strings.Index(text, later); i < at {
			t.Errorf("the test entry does not end the script before %q", strings.TrimSpace(later))
		}
	}
	// The test entry runs no step and no command of a step. The two entries
	// ask-path and affirm-word put a question to the standard input, as the
	// hint tests need, and so reach the questions (ask, ask_path, affirm):
	// they write nothing (no evidence folder), and nothing else reaches them.
	asks := map[string]bool{"self_test": true, "ask": true, "ask_path": true, "ask_file": true, "affirm": true}
	// The one exception is the entry holder-wait, which reaches step 12's wait:
	// show_prop asks the unit's state (read-only; a stub in the tests) and
	// show_unit_journal prints the journal through runcap (a stub, too).
	allowed := map[string]string{"show_prop": "systemctl", "show_unit_journal": "runcap"}
	// The two exempt bodies are pinned, so a later change to what they run
	// fails here instead of passing the name-based exemption.
	pinned := canaryFuncs(t, text)
	if got, want := pinned["show_prop"], " systemctl --user show \"$UNIT\" --property=\"$1\" --value; }"; got != want {
		t.Errorf("show_prop changed: %q", got)
	}
	wantJournal := "\n  if [ -z \"$T_START\" ]; then return 0; fi\n" +
		"  out \"   the journal of the unit since $T_START (the last 40 lines): the holder says why it ended there\"\n" +
		"  runcap unit-journal-tail unit_journal 40\n"
	if got := pinned["show_unit_journal"]; got != wantJournal {
		t.Errorf("show_unit_journal changed: %q", got)
	}
	for _, name := range append([]string{"self_test"}, canaryReach(canaryFuncs(t, text), "self_test")...) {
		for _, word := range []string{"runcap", "systemctl", "step_head", "fact", "manifest_of"} {
			if allowed[name] == word {
				continue
			}
			if canaryCalls(canaryFuncs(t, text)[name], word) != 0 {
				t.Errorf("the test entry reaches %s, which calls %s", name, word)
			}
		}
		for _, word := range []string{"ask", "ask_path", "ask_file", "affirm"} {
			if canaryCalls(canaryFuncs(t, text)[name], word) != 0 && !asks[name] {
				t.Errorf("the test entry reaches %s, which calls the question %s", name, word)
			}
		}
	}
}

// L6 and the seams note, section 3: in the first epoch of pass A the config
// declares a tested version and the stand-in draws a screen no recogniser
// knows, so the fence answers not-now, not-ready, nothing, and only that
// passes step 17. A refusal for an untested version is a wrong config, and
// the daemon's own refusal (exit status 30) is not the fence's answer.
func TestCanaryRunKitPassAExpectsNotNow(t *testing.T) {
	for _, o := range canaryOutcomes {
		for _, r := range canaryReasons {
			for _, w := range canaryWritten {
				want := "unexpected"
				switch {
				case o == "not-now" && r == "not-ready" && w == "nothing":
					want = "owner-supplies"
				case o == "refused" && r == "untested":
					want = "wrong-config"
				}
				if got, code := canarySelfTest(t, "inject-action", "A", o, r, w); code != 0 || got != want {
					t.Errorf("pass A, %s/%s/%s: the action is %q (exit status %d), want %q", o, r, w, got, code, want)
				}
			}
		}
	}

	body := canaryFuncs(t, canaryScriptText(t))["step_17"]
	at := func(part string) int {
		t.Helper()
		if strings.Count(body, part) != 1 {
			t.Fatalf("step 17 does not hold once: %s", part)
		}
		return strings.Index(body, part)
	}
	// Exit status 30 fails the step in both passes before any action is taken
	// from the result: it is not the fence's refusal.
	if n := strings.Count(body, "-eq 30"); n != 1 {
		t.Errorf("step 17 tests exit status 30 %d times, want once, in both passes", n)
	}
	passB := at(`elif [ "$RC" -eq 30 ]; then`)
	daemon := at("fail 'the daemon refused the message (exit 30)")
	action := at(`_act=$(inject_action "$PASS" "$_o" "$_r" "$_w")`)
	wrong := at(`if [ "$_act" = wrong-config ]; then`)
	wrongFail := at(`fail "a wrong config: the fence refused an untested version`)
	check := at(`is_eq "$_act" owner-supplies || return 0`)
	supplied := at("wait_key 'Owner: press Enter here when you have ")
	order := []int{passB, daemon, action, wrong, wrongFail, check, supplied}
	if !sort.IntsAreSorted(order) {
		t.Errorf("step 17 of pass A is out of order: %v", order)
	}
	if strings.Contains(body, "refused/nothing") {
		t.Error("step 17 still expects refused, nothing")
	}

	out, _ := canaryDry(t, "--pass", "A", "--config", "/nonexistent/sample/canary.json")
	for _, b := range canaryBlocks(out) {
		if !strings.HasPrefix(b.heading, "== step 17 [pass A]: ") {
			continue
		}
		expected, _ := b.line("expected:")
		for _, want := range []string{"not-now with the reason not-ready and writes nothing (L6)", "exit status 30", "refused, untested: a wrong config"} {
			if !strings.Contains(expected, want) {
				t.Errorf("the expected result of step 17 in pass A does not say %q: %s", want, expected)
			}
		}
		if b.count("[dry] would check: the result is not-now, not-ready, written nothing") != 1 {
			t.Errorf("the dry run of step 17 in pass A does not check for not-now, not-ready, nothing:\n%s", strings.Join(b.lines, "\n"))
		}
		return
	}
	t.Error("the dry run of pass A has no step 17")
}

// Where the owner supplies the message himself, the instruction must produce
// the act the next step checks: the stand-in commits only from a bracketed
// paste followed by Enter, and the body has two lines. So the script tells
// him to paste the file it names as one paste and to press Enter once, and
// never to type the message.
func TestCanaryRunKitOwnerPastesTheMessage(t *testing.T) {
	text := canaryScriptText(t)
	funcs := canaryFuncs(t, text)
	paste, ok := funcs["paste_message"]
	if !ok {
		t.Fatal("the script has no paste_message")
	}
	for _, want := range []string{
		"Do NOT type it", `out "     $BODY"`, "with your terminal'\"'\"'s paste", "ONE paste",
		"a bracketed paste", "press Enter in the pane ONCE", `"$BODY"`,
		// The console drops a paste that arrives while it has the focus itself,
		// without a word: the owner is told to focus the session pane first, and
		// before the instruction to paste.
		"first give the session pane the focus", "A paste while the console has the focus itself is dropped",
	} {
		if !strings.Contains(paste, want) {
			t.Errorf("paste_message does not say %s", want)
		}
	}
	if f, p := strings.Index(paste, "first give the session pane the focus"), strings.Index(paste, "Do NOT type it"); f < 0 || p < f {
		t.Error("paste_message does not tell the owner to give the pane the focus before it tells him to paste")
	}
	body := funcs["step_17"]
	if n := canaryCalls(body, "paste_message"); n != 2 {
		t.Errorf("step 17 gives the paste instruction in %d places, want 2 (pass A, and in pass B a refusal or not-now/nothing)", n)
	}
	passA := body[strings.Index(body, `if [ "$_act" = wrong-config ]; then`):strings.Index(body, "case $_act in\n")]
	if canaryCalls(passA, "paste_message") != 1 {
		t.Error("pass A's branch of step 17 does not give the paste instruction")
	}
	supplies := body[strings.Index(body, "\n    owner-supplies)\n"):]
	supplies = supplies[:strings.Index(supplies, "\n      ;;\n")]
	if canaryCalls(supplies, "paste_message") != 1 || !strings.Contains(supplies, `if [ "$ANSWER" != pasted ]; then`) {
		t.Errorf("the refusal arm of step 17 does not give the paste instruction and ask for \"pasted\":\n%s", supplies)
	}
	for _, old := range []string{"type this message", "typed the message", "types the message", "typed and submitted"} {
		if strings.Contains(text, old) {
			t.Errorf("the script still tells the owner to type the message: %q", old)
		}
	}
	for _, pass := range []string{"A", "B"} {
		out, _ := canaryDry(t, "--pass", pass, "--config", "/nonexistent/sample/canary.json")
		for _, b := range canaryBlocks(out) {
			if !strings.HasPrefix(b.heading, "== step 17 [pass "+pass+"]: ") {
				continue
			}
			if b.count("print the file to paste from, ") != 1 || b.count("as one paste, and presses Enter once: he does not type it") != 1 || b.count("gives the session pane the focus") != 1 {
				t.Errorf("the dry run of step 17 in pass %s does not give the paste instruction:\n%s", pass, strings.Join(b.lines, "\n"))
			}
		}
	}
}

// R6: the second epoch of pass A runs steps 11, 12 and 14 again for a new
// epoch, in the same state folder. Step 12 must not pass on the records the
// first epoch left there: it waits for holder.json to name the epoch of this
// start, and matches the lines of the event log and of the evidence by that
// epoch.
func TestCanaryRunKitStepTwelveReadsItsOwnEpoch(t *testing.T) {
	// The lines of two epochs, as ataniq-canary fields prints them.
	const (
		firstEvent  = "epoch=1\nevent=state\nkind=holder\nreason=assertion-10-waived\nrole=throwaway-one/lead\nseq=4\nstate=sandbox-started"
		secondEvent = "epoch=2\nevent=state\nkind=holder\nreason=assertion-10-waived\nrole=throwaway-one/lead\nseq=4\nstate=sandbox-started"
		tenthEvent  = "epoch=12\nevent=state\nkind=holder\nreason=assertion-10-waived\nseq=4\nstate=sandbox-started"
		otherEvent  = "epoch=2\nevent=state\nkind=holder\nreason=signal\nseq=5\nstate=ended"
	)
	for _, c := range []struct {
		name, fields string
		want         string
		pairs        []string
	}{
		{"the line of this epoch", secondEvent, "yes", []string{"epoch=2", "state=sandbox-started", "reason=assertion-10-waived"}},
		{"the first epoch's line does not pass for the second", firstEvent, "no", []string{"epoch=2", "state=sandbox-started", "reason=assertion-10-waived"}},
		{"an epoch that only begins with the digits", tenthEvent, "no", []string{"epoch=1", "state=sandbox-started"}},
		{"another event of this epoch", otherEvent, "no", []string{"epoch=2", "state=sandbox-started"}},
		{"a value that only begins alike", "epoch=2\nstate=sandbox-started-late", "no", []string{"epoch=2", "state=sandbox-started"}},
		{"a path that only ends alike", "last_epoch=2\nstate=sandbox-started", "no", []string{"epoch=2", "state=sandbox-started"}},
		{"a pattern character is not a pattern", "epoch=2\nstate=sandbox-started", "no", []string{"epoch=*"}},
		{"no line at all", "", "no", []string{"epoch=2"}},
		{"the waiver, with its spaces", "epoch=2\nevent=start\nwaiver=" + canaryWaiver, "yes", []string{"epoch=2", "event=start", "waiver=" + canaryWaiver}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, code := canarySelfTest(t, append([]string{"fields-hold", c.fields}, c.pairs...)...)
			if code != 0 || got != c.want {
				t.Errorf("exit status %d, got %q, want %q", code, got, c.want)
			}
		})
	}

	text := canaryScriptText(t)
	if !strings.Contains(text, "\nWAIVER='"+canaryWaiver+"'\n") {
		t.Error("the script's waiver text is not the exact wording")
	}
	funcs := canaryFuncs(t, text)
	eleven, twelve, hasLine := funcs["step_11"], funcs["step_12"], funcs["has_line"]
	if strings.Count(eleven, `START_EPOCH=$(fld "$START_OUT" epoch)`) != 1 || !strings.Contains(eleven, `is_number "$START_EPOCH"`) {
		t.Error("step 11 does not keep the epoch of the start result")
	}
	for _, want := range []string{
		`holder_started || return 0`,
		`is_eq "$(fld "$HSTATE/holder.json" epoch)" "$START_EPOCH" || return 0`,
		`has_line "$HSTATE/events.jsonl" "epoch=$START_EPOCH" 'state=sandbox-started' 'reason=assertion-10-waived' || return 0`,
		`has_line "$EVIDENCE" "epoch=$START_EPOCH" 'event=start' "waiver=$WAIVER" || return 0`,
	} {
		if strings.Count(twelve, want) != 1 {
			t.Errorf("step 12 does not hold once: %s", want)
		}
	}
	// Nothing in step 12 matches a record as text, without its epoch.
	if canaryCalls(twelve, "grep") != 0 || strings.Contains(twelve, `-s "$HSTATE/holder.json"`) {
		t.Error("step 12 matches a record without its epoch")
	}
	if n := strings.Count(twelve, "has_line "); n != 3 {
		t.Errorf("step 12 matches %d lines, want 3 (the wait, the event log, the evidence)", n)
	}
	for _, line := range strings.Split(twelve, "\n") {
		if strings.Contains(line, "has_line ") && !strings.Contains(line, `"epoch=$START_EPOCH"`) {
			t.Errorf("step 12 matches a line without its epoch: %s", strings.TrimSpace(line))
		}
	}
	for _, want := range []string{`"$BIN/ataniq-canary" fields --file -`, `if fields_hold "$_got" "$@"; then return 0; fi`, `done <"$_file"`} {
		if !strings.Contains(hasLine, want) {
			t.Errorf("has_line does not hold: %s", want)
		}
	}

	// The second epoch runs step 12 after its own step 11, and the dry run
	// says what is matched.
	second := funcs["second_epoch"]
	if a, b := strings.Index(second, "\n  step_11\n"), strings.Index(second, "\n  step_12\n"); a < 0 || b < a {
		t.Error("the second epoch does not run step 12 after step 11")
	}
	out, _ := canaryDry(t, "--pass", "A", "--config", "/nonexistent/sample/canary.json")
	n := 0
	for _, b := range canaryBlocks(out) {
		if !strings.HasPrefix(b.heading, "== step 12 [pass A") {
			continue
		}
		n++
		for _, want := range []string{"names the epoch of the start of step 11", "events.jsonl with that epoch holds the state sandbox-started", "canary-evidence.jsonl with that epoch holds the waiver"} {
			if b.count(want) != 1 {
				t.Errorf("the dry run of %s does not say %q", b.heading, want)
			}
		}
	}
	if n != 2 {
		t.Errorf("the dry run of pass A has %d step 12, want 2", n)
	}
}

// canaryWaiver is the waiver's exact wording (the seams note, section 5).
const canaryWaiver = "assertion 10 waived: canary slice, no qualification record"

// The undo's order is: stop, prove the unit gone, then delete the roots.
// Step 22 of the chain may not have stopped the unit (the daemon gone, the
// stop interrupted), so step 26 asks the unit's state before it shows the
// command that deletes the roots, and does not show it for a unit that is
// not proven stopped.
func TestCanaryRunKitDeleteCommandNeedsAStoppedUnit(t *testing.T) {
	for state, want := range map[string]string{
		"inactive": "stopped", "failed": "stopped",
		"active": "not-stopped", "activating": "not-stopped", "deactivating": "not-stopped",
		"reloading": "not-stopped", "refreshing": "not-stopped", "maintenance": "not-stopped",
		"": "not-stopped", "inactive ": "not-stopped", "not-inactive": "not-stopped", "unknown": "not-stopped",
	} {
		if got, code := canarySelfTest(t, "unit-stopped", state); code != 0 || got != want {
			t.Errorf("the active state %q: %q (exit status %d), want %q", state, got, code, want)
		}
	}

	funcs := canaryFuncs(t, canaryScriptText(t))
	body := funcs["step_26"]
	const (
		ask    = "_ust=$(unit_state)"
		test   = `if ! unit_stopped "$_ust"; then`
		stop   = `out "     systemctl --user stop $UNIT"`
		key    = "wait_key 'Owner: press Enter when the unit is stopped.'"
		refuse = `fail "the unit $UNIT is still \"$_ust\": the command that deletes the roots is not shown`
		remove = `out "     rm -rf -- $STATE $RUNTIME"`
	)
	for part, n := range map[string]int{ask: 2, test: 2, stop: 1, key: 1, refuse: 1, remove: 1} {
		if strings.Count(body, part) != n {
			t.Fatalf("step 26 holds %d times, want %d: %s", strings.Count(body, part), n, part)
		}
	}
	// The order of the real branch: ask, tell the owner to stop and wait, ask
	// again, and only in the branch of a stopped unit print the command.
	firstAsk, firstTest := strings.Index(body, ask), strings.Index(body, test)
	secondAsk, secondTest := strings.LastIndex(body, ask), strings.LastIndex(body, test)
	order := []int{firstAsk, firstTest, strings.Index(body, stop), strings.Index(body, key), secondAsk, secondTest, strings.Index(body, refuse), strings.Index(body, remove)}
	if !sort.IntsAreSorted(order) {
		t.Fatalf("step 26 is out of order: %v", order)
	}
	between := body[strings.Index(body, refuse):strings.Index(body, remove)]
	if !strings.Contains(between, "\n    else\n") || strings.Contains(between, "\n    fi\n") {
		t.Errorf("the delete command is not in the branch of a stopped unit:\n%s", between)
	}
	if strings.Contains(body, "rm -rf -- \"$STATE\"") || canaryCalls(body, "rmdir") != 0 {
		t.Error("step 26 deletes a root itself")
	}
	if state := funcs["unit_state"]; !strings.Contains(state, `systemctl --user show "$UNIT" --property=ActiveState --value`) {
		t.Errorf("unit_state does not ask systemd for the active state: %s", state)
	}

	// The dry run says so, in both passes and in the chain.
	for _, args := range [][]string{{"--pass", "A"}, {"--pass", "B"}, {"--pass", "A", "--fail-at", "12"}} {
		out, _ := canaryDry(t, append(args, "--config", "/nonexistent/sample/canary.json")...)
		blocks := canaryBlocks(out)
		b := blocks[len(blocks)-1]
		for i := len(blocks) - 1; i >= 0; i-- {
			if strings.Contains(blocks[i].heading, "step 26 [pass ") {
				b = blocks[i]
				break
			}
		}
		text := strings.Join(b.lines, "\n")
		var at []int
		for _, part := range []string{
			"would run: systemctl --user show ataniq-lead-throwaway-one.service --property=ActiveState --value",
			"would when the unit is not stopped (its state is neither inactive nor failed): print systemctl --user stop ataniq-lead-throwaway-one.service",
			"would fail the step when the unit is still not stopped: the command that deletes the roots is then not shown",
			"would wait for Enter: Owner: delete the canary's two roots yourself",
		} {
			i := strings.Index(text, "[dry] "+part)
			if i < 0 {
				t.Errorf("%v: step 26 does not say %q", args, part)
			}
			at = append(at, i)
		}
		if !sort.IntsAreSorted(at) {
			t.Errorf("%v: step 26 is out of order: %v", args, at)
		}
	}
}

// R6: a refusal in the second epoch of pass A is a finding for a fix task
// before pass B. So step 2 of pass B takes a record of pass A that shows no
// finding, or one with findings and the lead's recorded word that the fixes
// are merged, and nothing else.
func TestCanaryRunKitPassBNeedsACleanPassA(t *testing.T) {
	dir := t.TempDir()
	for name, c := range map[string]struct{ content, want string }{
		"clean":                {"pass A: complete\nfindings: 0\n", "clean"},
		"one finding":          {"pass A: complete\nfindings: 1\n", "findings 1"},
		"twelve findings":      {"pass A: complete\nfindings: 12\n", "findings 12"},
		"no last line feed":    {"pass A: complete\nfindings: 2", "findings 2"},
		"no findings line":     {"pass A: complete\n", "unknown"},
		"findings in words":    {"pass A: complete\nfindings: none\n", "unknown"},
		"a negative number":    {"pass A: complete\nfindings: -1\n", "unknown"},
		"failed":               {"pass A: failed at step 17: the fence\nfindings: 0\n", "incomplete"},
		"failed, no chain":     {"pass A: failed at step 3: git\n", "incomplete"},
		"pass B's record":      {"pass B: complete\nfindings: 0\n", "incomplete"},
		"complete in a longer": {"pass A: complete, almost\nfindings: 0\n", "incomplete"},
		"empty":                {"", "incomplete"},
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(dir, strings.ReplaceAll(name, " ", "-")+".txt")
			if err := os.WriteFile(file, []byte(c.content), 0o600); err != nil {
				t.Fatal(err)
			}
			if got, code := canarySelfTest(t, "pass-a-record", file); code != 0 || got != c.want {
				t.Errorf("%q (exit status %d), want %q", got, code, c.want)
			}
		})
	}
	if got, code := canarySelfTest(t, "pass-a-record", filepath.Join(dir, "missing.txt")); code != 0 || got != "incomplete" {
		t.Errorf("a missing record: %q (exit status %d), want incomplete", got, code)
	}

	// Step 2: only "clean" passes without a question; findings need the
	// lead's "yes" through affirm, which records it; the rest fails.
	body := canaryFuncs(t, canaryScriptText(t))["step_2"]
	at := func(part string) int {
		t.Helper()
		if strings.Count(body, part) != 1 {
			t.Fatalf("step 2 does not hold once: %s", part)
		}
		return strings.Index(body, part)
	}
	order := []int{
		at(`_rec=$(passa_record "$PASSA/result.txt")`),
		at("case $_rec in\n"),
		at("        clean) out "),
		at("        'findings '*)\n"),
		at(`affirm "Lead: every finding of pass A (${_rec#findings }) has its fix merged" yes || return 0`),
		at("        incomplete) fail 'the record of pass A does not say the pass completed'; return 0 ;;"),
		at("        *) fail 'the record of pass A does not say how many findings it had'; return 0 ;;"),
		at(`fact facts.txt "$PASSA/result.txt"`),
	}
	if !sort.IntsAreSorted(order) {
		t.Errorf("step 2 is out of order: %v", order)
	}
	if strings.Contains(body, "grep -qx 'pass A: complete'") {
		t.Error("step 2 still takes a record of pass A on its first line alone")
	}
	out, _ := canaryDry(t, "--pass", "B", "--config", "/nonexistent/sample/canary.json")
	for _, b := range canaryBlocks(out) {
		if strings.HasPrefix(b.heading, "== step 2 [pass B]: ") {
			if b.count("the record shows no finding (findings: 0); with findings, print the number and ask the lead to confirm that every finding has its fix merged") != 1 {
				t.Errorf("the dry run of step 2 in pass B does not say what it needs of pass A:\n%s", strings.Join(b.lines, "\n"))
			}
		}
	}
}

// The 7501 counters (the seams note, sections 4 and 7): while the holder
// runs they are only in its status answer, and no counters event is written;
// after the child ends they are in final.json. So step 16 shows no counter
// and says where they are read, and step 22 prints them from the final
// record, with a line that can print when there is none.
func TestCanaryRunKitSaysWhereTheCountersAreRead(t *testing.T) {
	const none = "     (none is named for 7501)"
	for name, c := range map[string]struct{ fields, want string }{
		"three counters": {
			"child.exit_status=0\ncounters.held_output_full=0\ncounters.osc7501-invalid=0\ncounters.osc7501-queries=1\ncounters.osc7501-reports=4\nversion=1",
			"     counters.osc7501-invalid=0\n     counters.osc7501-queries=1\n     counters.osc7501-reports=4",
		},
		"no counter for 7501":    {"child.exit_status=0\ncounters.held_output_full=0\nversion=1", none},
		"7501 in another value":  {"screen.text=sha256:7501\ndetail=counters.osc7501-reports=4\nversion=7501", none},
		"an empty record":        {"", none},
		"one line, no line feed": {"counters.osc7501-reports=2", "     counters.osc7501-reports=2"},
	} {
		t.Run(name, func(t *testing.T) {
			if got, code := canarySelfTest(t, "counters-7501", c.fields); code != 0 || got != c.want {
				t.Errorf("exit status %d, got:\n%s\nwant:\n%s", code, got, c.want)
			}
		})
	}

	text := canaryScriptText(t)
	funcs := canaryFuncs(t, text)
	if strings.Count(funcs["step_22"], `counters_7501 "$_fin" || out '`+none+`'`) != 1 {
		t.Error("step 22 does not print the counters of the final record with a line for none")
	}
	// A fallback after a pipeline never prints: the status is the last
	// command's. No message of the script hangs on one.
	if m := regexp.MustCompile(`(?m)^.*\|[^|\n]*\|\| out .*$`).FindString(text); m != "" && strings.Contains(m, " | ") {
		t.Errorf("a message hangs on the status of a pipeline: %s", strings.TrimSpace(m))
	}
	sixteen := funcs["step_16"]
	for _, word := range []string{"grep", "fld", "has_line", "counters_7501"} {
		if canaryCalls(sixteen, word) != 0 {
			t.Errorf("step 16 calls %s: it has no place to read the counters from while the holder runs", word)
		}
	}
	// The holder reports unknown for idle in this feature: no check of the
	// step fails on the value of the idle field.
	if canaryCalls(sixteen, "check") != 0 || canaryCalls(sixteen, "is_eq") != 0 || strings.Contains(sixteen, "idle") && !strings.Contains(sixteen, `note "holder idle field: $ANSWER"`) {
		t.Error("step 16 judges the idle field's value, or does not record it")
	}
	if strings.Contains(sixteen, "events.jsonl") {
		t.Error("step 16 reads the event log, which holds no counters")
	}
	for _, want := range []string{
		"while the holder runs they are only in its status answer. No command of the kit reads",
		"the event log holds no counters: the script cannot show them now.",
		"Lead: read them in the console and answer below; the script records what you read.",
		`after the child ends they are in $HSTATE/final.json: step 22 prints them from there.`,
	} {
		if strings.Count(sixteen, want) != 1 {
			t.Errorf("step 16 does not say: %s", want)
		}
	}
	if said, asked := strings.Index(sixteen, "the script cannot show them now."), strings.Index(sixteen, "ask 'Lead: was the OSC 7501 query answered"); said < 0 || asked < said {
		t.Error("step 16 asks for the counters before it says where they are read")
	}
	out, _ := canaryDry(t, "--pass", "B", "--config", "/nonexistent/sample/canary.json")
	for _, b := range canaryBlocks(out) {
		if !strings.HasPrefix(b.heading, "== step 16 [pass B]: ") {
			continue
		}
		does, _ := b.line("does:")
		if !strings.Contains(does, "no command of the kit reads") || !strings.Contains(does, "from final.json after the child ends (step 22)") {
			t.Errorf("step 16 does not say where the counters are read: %s", does)
		}
		if b.count("say plainly where the 7501 counters are read") != 1 || b.count("events.jsonl that hold counters") != 0 {
			t.Errorf("the dry run of step 16:\n%s", strings.Join(b.lines, "\n"))
		}
	}
}

// The evidence folder must not lie in the checkout or in a root of the
// canary. The comparison is of resolved paths: a symbolic link that leads
// into one of them does not pass, whether the link is the folder's parent, a
// part further up, or the root is itself reached through a link.
func TestCanaryRunKitEvidenceFolderIsOutsideByResolvedPaths(t *testing.T) {
	// The folders are made here, by the test; the script only compares.
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p := func(parts ...string) string { return filepath.Join(append([]string{dir}, parts...)...) }
	for _, d := range []string{p("checkout", "sub"), p("state"), p("runtime"), p("evidence"), p("other", "deep")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		p("to-checkout"):         p("checkout"),
		p("to-sub"):              p("checkout", "sub"),
		p("to-state"):            p("state"),
		p("to-evidence"):         p("evidence"),
		p("other", "to-runtime"): p("runtime"),
		p("checkout-link"):       p("checkout"),
		p("dangling"):            p("nowhere"),
		p("relative"):            "checkout",
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	roots := []string{p("checkout"), p("state"), p("runtime")}
	for name, c := range map[string]struct {
		folder string
		roots  []string
		want   string
	}{
		"elsewhere":                         {p("evidence", "canary-pass-A-1"), roots, "outside"},
		"elsewhere, the parent not made":    {p("not-made", "yet", "canary-pass-A-1"), roots, "outside"},
		"a link to elsewhere":               {p("to-evidence", "canary-pass-A-1"), roots, "outside"},
		"a name that only begins alike":     {p("checkout-two", "canary-pass-A-1"), roots, "outside"},
		"in the checkout":                   {p("checkout", "canary-pass-A-1"), roots, "inside"},
		"deep in the checkout":              {p("checkout", "sub", "x", "canary-pass-A-1"), roots, "inside"},
		"in the state root":                 {p("state", "canary-pass-B-1"), roots, "inside"},
		"in the runtime root":               {p("runtime", "canary-pass-B-1"), roots, "inside"},
		"through a link to the checkout":    {p("to-checkout", "canary-pass-A-1"), roots, "inside"},
		"through a link into the checkout":  {p("to-sub", "not-made", "canary-pass-A-1"), roots, "inside"},
		"through a link to the state root":  {p("to-state", "canary-pass-A-1"), roots, "inside"},
		"through a link further up":         {p("other", "to-runtime", "a", "b", "canary-pass-A-1"), roots, "inside"},
		"through a relative link":           {p("relative", "canary-pass-A-1"), roots, "inside"},
		"the root is named through a link":  {p("checkout", "canary-pass-A-1"), []string{p("checkout-link")}, "inside"},
		"both through links":                {p("to-checkout", "canary-pass-A-1"), []string{p("checkout-link")}, "inside"},
		"a parent part":                     {p("evidence", "..", "checkout", "canary-pass-A-1"), roots, "inside"},
		"a parent part to elsewhere":        {p("evidence", "..", "evidence", "canary-pass-A-1"), roots, "inside"},
		"a dot part":                        {p("evidence", ".", "canary-pass-A-1"), roots, "inside"},
		"a path that is not absolute":       {"evidence/canary-pass-A-1", roots, "inside"},
		"a root that does not exist":        {p("evidence", "canary-pass-A-1"), []string{p("no-such-root")}, "outside"},
		"in a root that does not exist yet": {p("no-such-root", "canary-pass-A-1"), []string{p("no-such-root")}, "inside"},
		"the root itself":                   {p("state"), roots, "inside"},
	} {
		t.Run(name, func(t *testing.T) {
			// filepath.Join cleans a ".." away: put the parts back.
			folder := c.folder
			switch name {
			case "a parent part":
				folder = p("evidence") + "/../checkout/canary-pass-A-1"
			case "a parent part to elsewhere":
				folder = p("evidence") + "/../evidence/canary-pass-A-1"
			case "a dot part":
				folder = p("evidence") + "/./canary-pass-A-1"
			}
			got, code := canarySelfTest(t, append([]string{"outside-roots", folder}, c.roots...)...)
			if code != 0 || got != c.want {
				t.Errorf("%s against %v: %q (exit status %d), want %q", folder, c.roots, got, code, c.want)
			}
		})
	}

	// The real run checks its evidence folder with that function, against the
	// checkout and both roots, before any step.
	text := canaryScriptText(t)
	check := `  if ! outside_roots "$EV" "$REPO" "$STATE" "$RUNTIME"; then` + "\n"
	if strings.Count(text, check) != 1 || strings.Index(text, check) > strings.Index(text, "\nstep_1() {\n") {
		t.Error("the script does not check its evidence folder against the checkout and the roots before step 1")
	}
	if after := text[strings.Index(text, check):]; !strings.HasPrefix(after[len(check):], "    printf '%s\\n' 'canary-run.sh: the evidence folder must not lie in the checkout or in a root of the canary") ||
		!strings.Contains(after[:400], "\n    exit 1\n") {
		t.Error("the script does not refuse an evidence folder inside the checkout or a root")
	}
	funcs := canaryFuncs(t, text)
	if !strings.Contains(funcs["resolved"], `cd -P -- "$_rp"`) || !strings.Contains(funcs["resolved"], "pwd -P") {
		t.Error("resolved does not resolve with cd -P")
	}
	if strings.Contains(text, `case $EV/ in "$REPO"/*`) {
		t.Error("the script still compares the evidence folder as a string alone")
	}
}

// This file checks what a signal does to a key the clean-up chain of the
// canary run script waits for (the plan's N3: each step of the chain waits
// for its key). It runs the script's test entry "wait-key", which waits as a
// step of the chain does and with the chain's own answer to a signal; it
// starts nothing: the PATH is an empty folder, so the script cannot run any
// program at all.

// canaryKeyWait is one run of the test entry wait-key.
type canaryKeyWait struct {
	t     *testing.T
	cmd   *exec.Cmd
	stdin io.WriteCloser
	mu    sync.Mutex
	out   strings.Builder
	done  chan struct{}
}

func startCanaryKeyWait(t *testing.T) *canaryKeyWait {
	t.Helper()
	return startCanaryKit(t, canaryRunScript, t.TempDir(), []string{"PATH=" + t.TempDir(), "HOME=" + t.TempDir()}, "--self-test", "wait-key")
}

// startCanaryKit starts a script of the kit (path from the repository root)
// in the folder dir with exactly the environment env, and returns it running,
// with its standard input a pipe the test writes the keys to and its output
// collected.
func startCanaryKit(t *testing.T, rel, dir string, env []string, args ...string) *canaryKeyWait {
	t.Helper()
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(root(t), rel)}, args...)...)
	cmd.Dir = dir
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w := &canaryKeyWait{t: t, cmd: cmd, stdin: stdin, done: make(chan struct{})}
	go func() {
		defer close(w.done)
		buf := make([]byte, 4096)
		for {
			n, err := stdout.Read(buf)
			w.mu.Lock()
			w.out.Write(buf[:n])
			w.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		<-w.done
		_ = cmd.Wait()
	})
	return w
}

func (w *canaryKeyWait) text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.out.String()
}

// waitFor waits until the output holds the text n times.
func (w *canaryKeyWait) waitFor(text string, n int) {
	w.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for strings.Count(w.text(), text) < n {
		if time.Now().After(deadline) {
			w.t.Fatalf("the output does not hold %q %d times:\n%s", text, n, w.text())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ended reports whether the script ended within the time.
func (w *canaryKeyWait) ended(within time.Duration) bool {
	select {
	case <-w.done:
		return true
	case <-time.After(within):
		return false
	}
}

// finish waits for the script to end and returns its exit status.
func (w *canaryKeyWait) finish() int {
	w.t.Helper()
	if !w.ended(10 * time.Second) {
		w.t.Fatalf("the script did not end:\n%s", w.text())
	}
	err := w.cmd.Wait()
	if err == nil {
		return 0
	}
	exit, ok := err.(*exec.ExitError)
	if !ok {
		w.t.Fatal(err)
	}
	return exit.ExitCode()
}

// In the clean-up chain a signal cannot stand in for the owner's key: the
// wait goes on after it, for every signal the chain answers, and ends only
// with a line.
func TestCanaryRunKitChainKeyOutlastsASignal(t *testing.T) {
	for name, sig := range map[string]syscall.Signal{
		"interrupt": syscall.SIGINT, "terminate": syscall.SIGTERM, "hangup": syscall.SIGHUP,
	} {
		t.Run(name, func(t *testing.T) {
			w := startCanaryKeyWait(t)
			w.waitFor("Press Enter: ", 1)
			for i := 1; i <= 3; i++ {
				// The prompt is printed just before the read: give the
				// script the moment to be reading.
				time.Sleep(50 * time.Millisecond)
				if err := w.cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				w.waitFor("the clean-up chain cannot be skipped", i)
				if w.ended(200 * time.Millisecond) {
					t.Fatalf("the wait ended with signal %d and no key:\n%s", i, w.text())
				}
			}
			if strings.Contains(w.text(), "\nkey\n") || strings.Contains(w.text(), "no-key") {
				t.Fatalf("a signal was taken for a key:\n%s", w.text())
			}
			if _, err := io.WriteString(w.stdin, "\n"); err != nil {
				t.Fatal(err)
			}
			if code := w.finish(); code != 0 {
				t.Errorf("exit status %d:\n%s", code, w.text())
			}
			out := w.text()
			if !strings.HasSuffix(out, "\nkey\n") && !strings.HasSuffix(out, ">> key\n") {
				t.Errorf("the key was not read after the signals:\n%s", out)
			}
			if strings.Contains(out, "not found") {
				t.Errorf("the test entry looked for a program:\n%s", out)
			}
		})
	}
}

// The end of the input ends the wait without a key, also after a signal: the
// wait cannot go on for ever when the terminal is gone.
func TestCanaryRunKitChainKeyEndsWithTheInput(t *testing.T) {
	for _, signalFirst := range []bool{false, true} {
		w := startCanaryKeyWait(t)
		w.waitFor("Press Enter: ", 1)
		if signalFirst {
			time.Sleep(50 * time.Millisecond)
			if err := w.cmd.Process.Signal(syscall.SIGHUP); err != nil {
				t.Fatal(err)
			}
			w.waitFor("the clean-up chain cannot be skipped", 1)
		}
		if err := w.stdin.Close(); err != nil {
			t.Fatal(err)
		}
		if code := w.finish(); code != 0 {
			t.Errorf("exit status %d:\n%s", code, w.text())
		}
		if out := w.text(); !strings.HasSuffix(out, "\nno-key\n") || strings.Contains(out, "\nkey\n") {
			t.Errorf("signal first %v: the end of the input was not the end of the wait:\n%s", signalFirst, out)
		}
	}
}

// The two readers of the script, the wait for a key and the question, read
// through the one function that outlasts a signal, and the chain installs
// the answer to a signal that the test entry uses.
func TestCanaryRunKitChainReadsThroughOneReader(t *testing.T) {
	text := canaryScriptText(t)
	funcs := canaryFuncs(t, text)
	for _, name := range []string{"wait_key", "ask"} {
		if canaryCalls(funcs[name], "read_answer") != 1 || strings.Contains(funcs[name], "read -r") {
			t.Errorf("%s does not read through read_answer alone", name)
		}
	}
	if n := strings.Count(text, "read -r ANSWER"); n != 1 {
		t.Errorf("the script reads an answer in %d places, want 1 (read_answer)", n)
	}
	reader := funcs["read_answer"]
	for _, want := range []string{`[ "$IN_CHAIN" -eq 1 ] && on_terminal && `, `[ "$SIGNALLED" -eq 1 ]`, "continue", `return "$_rs"`} {
		if !strings.Contains(reader, want) {
			t.Errorf("read_answer does not hold: %s", want)
		}
	}
	if !strings.Contains(funcs["on_terminal"], "[ -t 0 ]") {
		t.Error("on_terminal does not ask whether standard input is a terminal")
	}
	if canaryCalls(funcs["on_exit"], "chain_trap") != 1 || strings.Count(text, "INT TERM HUP") != 2 {
		t.Error("the chain does not install its answer to a signal through chain_trap")
	}
	if strings.Count(text, "TTY_ASSUMED=1") != 1 || !strings.Contains(funcs["self_test"], "TTY_ASSUMED=1") {
		t.Error("the input is taken for a terminal outside the test entry")
	}
}

// canaryHasLine runs the has-line entry on a file of the given lines, with a
// stub helper that answers by line: a line holding "one" prints the fields of
// epoch 1, "two" those of epoch 2, "partial" prints the fields of epoch 2
// and then fails, as for a line that is not a whole JSON object.
func canaryHasLine(t *testing.T, lines []string, pairs ...string) string {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	bin := filepath.Join(work, "build", "canary")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	stub := `#!/bin/sh
read -r line
case $line in
  *one*) printf 'epoch=1\nstate=up\nreason=first\n' ;;
  *two*) printf 'epoch=2\nstate=up\nreason=second\n' ;;
  *partial*) printf 'epoch=2\nstate=up\nreason=second\n'; exit 1 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "ataniq-canary"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	args := append([]string{filepath.Join(root(t), canaryRunScript), "--self-test", "has-line", file}, pairs...)
	cmd := exec.Command(path, args...)
	cmd.Dir = work
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("has-line %v: %v\n%s", pairs, err, out)
	}
	return strings.TrimSpace(string(out))
}

// A match on a later line passes, a line passed over for a partial match
// does not stop the search, and a line that is not a whole object is passed
// over.
func TestCanaryRunKitHasLine(t *testing.T) {
	for _, c := range []struct {
		name  string
		lines []string
		pairs []string
		want  string
	}{
		{"a match on a later line", []string{"one", "two"}, []string{"epoch=2", "state=up"}, "yes"},
		{"a partial line is passed over", []string{"one", "two"}, []string{"epoch=1", "reason=second"}, "no"},
		{"a match on the first line", []string{"one", "two"}, []string{"epoch=1", "reason=first"}, "yes"},
		{"no match", []string{"one", "two"}, []string{"epoch=3"}, "no"},
		{"a line that is not a whole object is passed over", []string{"partial", "two"}, []string{"epoch=2", "reason=second"}, "yes"},
		{"only such a line", []string{"partial"}, []string{"epoch=2"}, "no"},
		{"no line", []string{}, []string{"epoch=2"}, "no"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := canaryHasLine(t, c.lines, c.pairs...); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// Provisioning sets the tree's origin to the mirror's place inside the
// sandbox, so step 8 expects that URL and refuses the host path.
func TestCanaryRunKitStep8ExpectsMirrorInside(t *testing.T) {
	const inside = "/ataniq/mirror/throwaway-one.git"
	for _, c := range []struct{ url, want string }{
		{inside, "yes"},
		{"file://" + inside, "yes"},
		{"/sample/canary/state/mirrors/throwaway-one.git", "no"},
		{"/ataniq/mirror/other.git", "no"},
	} {
		out, code := canarySelfTest(t, "mirror-remote", c.url, "throwaway-one")
		if code != 0 || out != c.want {
			t.Errorf("%s: exit status %d, got %q, want %q", c.url, code, out, c.want)
		}
	}
	body := canaryFuncs(t, canaryScriptText(t))["step_8"]
	if strings.Contains(body, `is_eq "${_u#file://}"`) {
		t.Error("step 8 compares the remote with the host mirror path")
	}
	if !strings.Contains(body, `remote_is_mirror "$_u" "$PROJECT"`) || strings.Contains(body, "_u=$MIRROR") {
		t.Error("step 8 does not check the remote against the mirror's place inside")
	}
}

// canaryRead runs a self-test entry that reads text (awk, sed and cat of the
// host, no stub): the entries of the machine reads that replace the lead's
// words. It returns the output without the last line feed.
func canaryRead(t *testing.T, args ...string) string {
	t.Helper()
	path, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	dir := t.TempDir()
	cmd := exec.Command(path, append([]string{filepath.Join(root(t), canaryRunScript), "--self-test"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSuffix(string(out), "\n")
}

func canaryWrite(t *testing.T, dir, name, text string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// The Lead: prompts that the script can answer itself are gone; the ones it
// cannot are still there.
func TestCanaryRunKitLeadWordsReplacedByReads(t *testing.T) {
	script := canaryScriptText(t)
	for _, gone := range []string{
		"'Lead: no Qargi run is open' yes || return 0",
		"shows every check passed",
		"this is the develop commit the pass is to run",
		"the mirror holds the upstream branch",
		"profile version 4 with the pinned digest",
		"is at the lead's commit\" yes",
	} {
		if strings.Contains(script, gone) {
			t.Errorf("the script still asks the lead: %s", gone)
		}
	}
	for _, kept := range []string{
		"Lead: no agent session is open that has been told about the slice",
		"Lead: the probe reported once and every assertion is ok except 10",
		"Lead: the daemon's start line and hello list the canary operations",
	} {
		if !strings.Contains(script, kept) {
			t.Errorf("the script lost a prompt that has no machine read: %s", kept)
		}
	}
	// The dry run prints each machine check and asks for none of them.
	out, code := canaryDry(t, "--pass", "B", "--config", "/nonexistent/sample/canary.json")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	for _, want := range []string{
		"check: no Qargi run of this project is open",
		"check: the report parses, its verdict is pass",
		"check: the summary parses, every step has the status ok or skipped",
		"check: the checkout's commit is the commit of develop",
		"check: the work tree has no change to a tracked file",
		"check: the mirror lists the upstream branch",
		"check: holder.json says the sandbox started",
		"check: the upstream branch ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run lacks %q", want)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		for _, word := range []string{"no Qargi run is open", "shows every check passed", "develop commit the pass", "the mirror holds the upstream", "pinned digest (type", "is at the lead's commit (type"} {
			if strings.Contains(line, "ask and record") && strings.Contains(line, word) {
				t.Errorf("the dry run still asks: %s", line)
			}
		}
	}
}

func TestCanaryRunKitOpenRuns(t *testing.T) {
	dir := t.TempDir()
	run := func(name, repo string) string {
		canaryWrite(t, dir, name+"/repo.path", repo+"\n")
		return filepath.Join(dir, name)
	}
	mine1, mine2, mine3 := run("r1", "/sample/ataniq"), run("r2", "/sample/ataniq"), run("r3", "/sample/ataniq")
	done, closed, other := run("r4", "/sample/ataniq"), run("r5", "/sample/ataniq"), run("r6", "/sample/other")
	list := canaryWrite(t, dir, "list.txt", strings.Join([]string{
		"ROLE       SLUG               AGE        APPLIED MERGED   RUN",
		"build      one                1d         no     no       " + mine1,
		"review     two                2d         no     no       " + mine2,
		"build      three              3d         yes    no       " + mine3,
		"build      four               4d         yes    yes      " + done,
		"build      five               5d         closed -        " + closed,
		"build      six                6d         no     no       " + other,
		"build      gone               7d         no     no       /nonexistent/sample/gone",
		"  residue: " + mine1 + "/home/.codex/auth.json (a link a start left)",
		"",
	}, "\n"))
	got := canaryRead(t, "open-runs", list, "/sample/ataniq")
	if want := mine1 + "\n" + mine2 + "\n" + mine3; got != want {
		t.Errorf("open runs:\n%s\nwant:\n%s", got, want)
	}
	if got := canaryRead(t, "open-runs", list, "/sample/none"); got != "" {
		t.Errorf("a project without a run has open runs: %q", got)
	}
}

// An open run whose repo.path cannot be read (missing, empty or unreadable)
// is not skipped as if it were another project's: unread-runs names it, so
// that step 1 asks the lead instead of passing. A readable repo.path, a run
// that is applied and merged or closed, and a run whose directory is gone are
// not named.
func TestCanaryRunKitOpenRunWithoutRepoPathIsUnknown(t *testing.T) {
	dir := t.TempDir()
	canaryWrite(t, dir, "r1/repo.path", "/sample/ataniq\n")
	canaryWrite(t, dir, "r2/repo.path", "\n")
	canaryWrite(t, dir, "r4/repo.path", "")
	canaryWrite(t, dir, "r5/other", "x")
	canaryWrite(t, dir, "r6/repo.path", "/sample/ataniq\n")
	canaryWrite(t, dir, "r7/repo.path", "")
	r := func(n string) string { return filepath.Join(dir, n) }
	// r3 has no folder at all, r5 has no repo.path file.
	list := canaryWrite(t, dir, "list.txt", strings.Join([]string{
		"ROLE       SLUG               AGE        APPLIED MERGED   RUN",
		"build      one                1d         no     no       " + r("r1"),
		"build      two                1d         no     no       " + r("r2"),
		"build      three              1d         yes    no       " + r("r3"),
		"build      four               1d         no     no       " + r("r4"),
		"build      five               1d         no     no       " + r("r5"),
		"build      six                1d         yes    yes      " + r("r6"),
		"build      seven              1d         closed -        " + r("r7"),
		"build      gone               7d         no     no       /nonexistent/sample/gone  directory gone",
		"",
	}, "\n"))
	want := strings.Join([]string{r("r2"), r("r3"), r("r4"), r("r5")}, "\n")
	if got := canaryRead(t, "unread-runs", list); got != want {
		t.Errorf("unread runs:\n%s\nwant:\n%s", got, want)
	}
	// The readable run of the project is still an open run; the unread ones are
	// not open runs of the project either, which is why they are named apart.
	if got := canaryRead(t, "open-runs", list, "/sample/ataniq"); got != r("r1") {
		t.Errorf("open runs of the project: got %q, want %q", got, r("r1"))
	}
	ok := canaryWrite(t, dir, "ok.txt", "ROLE       SLUG               AGE        APPLIED MERGED   RUN\nbuild      one                1d         no     no       "+r("r1")+"\n")
	if got := canaryRead(t, "unread-runs", ok); got != "" {
		t.Errorf("a readable repo.path is named: %q", got)
	}
	// Step 1 asks the lead for such a list: the dry plan says so.
	if out, _ := canaryDry(t, "--pass", "B", "--config", "/nonexistent/sample/canary.json"); !strings.Contains(out, "when an open run has no readable repo.path, ask the lead as before") {
		t.Error("the dry run of step 1 does not say that an open run without a readable repo.path is asked")
	}
}

// qargi records a run's repo.path as the resolved --repo folder, which may be
// a linked work tree. The repository the script is given may be reached through
// a symbolic link, with a trailing slash, or be a linked work tree of the same
// repository; the main work tree is compared on both sides.
func TestCanaryRunKitOpenRunsOfLinkedWorkTrees(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main, wa, wb, other := filepath.Join(dir, "main"), filepath.Join(dir, "wa"), filepath.Join(dir, "wb"), filepath.Join(dir, "other")
	for _, args := range [][]string{
		{"init", "-q", main},
		{"-C", main, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "x"},
		{"-C", main, "worktree", "add", "-q", "--detach", wa},
		{"-C", main, "worktree", "add", "-q", "--detach", wb},
		{"init", "-q", other},
	} {
		if out, err := exec.Command(git, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	for _, c := range []struct {
		name, recorded string
		repos          map[string]string
	}{
		{"a run recorded for a linked work tree", wa, map[string]string{
			"the main checkout": main, "the same work tree": wa, "another work tree": wb, "a work tree with a slash": wb + "/",
			"an unrelated repository": other, "an empty argument": ""}},
		{"a run recorded for the main checkout", main, map[string]string{
			"the main checkout": main, "a linked work tree": wa, "another work tree": wb,
			"an unrelated repository": other, "an empty argument": ""}},
	} {
		canaryWrite(t, dir, "run1/repo.path", c.recorded+"\n")
		run := filepath.Join(dir, "run1")
		list := canaryWrite(t, dir, "list.txt", strings.Join([]string{
			"ROLE       SLUG               AGE        APPLIED MERGED   RUN",
			"build      one                1d         no     no       " + run,
			"",
		}, "\n"))
		for name, repo := range c.repos {
			want := run
			if name == "an unrelated repository" || name == "an empty argument" {
				want = ""
			}
			if got := canaryRead(t, "open-runs", list, repo); got != want {
				t.Errorf("%s, REPO is %s: got %q, want %q", c.name, name, got, want)
			}
		}
	}
	// A run whose repo.path is empty or missing is not matched by an empty REPO.
	canaryWrite(t, dir, "run1/repo.path", "")
	run := filepath.Join(dir, "run1")
	list := canaryWrite(t, dir, "list.txt", "ROLE       SLUG               AGE        APPLIED MERGED   RUN\nbuild      one                1d         no     no       "+run+"\n")
	for _, repo := range []string{"", main} {
		if got := canaryRead(t, "open-runs", list, repo); got != "" {
			t.Errorf("a run without a repo.path matched REPO %q: %q", repo, got)
		}
	}
}

func TestCanaryRunKitOpenRunsCompareResolvedPaths(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	main := filepath.Join(dir, "main")
	linked := filepath.Join(dir, "linked")
	link := filepath.Join(dir, "link")
	for _, args := range [][]string{
		{"init", "-q", main},
		{"-C", main, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "commit", "-q", "--allow-empty", "-m", "x"},
		{"-C", main, "worktree", "add", "-q", "--detach", linked},
	} {
		if out, err := exec.Command(git, args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.Symlink(main, link); err != nil {
		t.Skip("no symbolic link")
	}
	canaryWrite(t, dir, "run1/repo.path", main+"\n")
	run := filepath.Join(dir, "run1")
	list := canaryWrite(t, dir, "list.txt", strings.Join([]string{
		"ROLE       SLUG               AGE        APPLIED MERGED   RUN",
		"build      one                1d         no     no       " + run,
		"",
	}, "\n"))
	for _, c := range []struct{ name, repo, want string }{
		{"the resolved path", main, run},
		{"a trailing slash", main + "/", run},
		{"a symbolic link", link, run},
		{"a symbolic link and a trailing slash", link + "/", run},
		{"a linked work tree", linked, run},
		{"a linked work tree with a trailing slash", linked + "/", run},
		{"a folder that is not the repository", dir, ""},
		{"a folder that does not exist", filepath.Join(dir, "absent"), ""},
	} {
		if got := canaryRead(t, "open-runs", list, c.repo); got != c.want {
			t.Errorf("open runs, %s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The list is read as qargi local list prints it: the exact header, and every
// line of a kind it knows. Rows it passes over are counted; a drifted list is
// not read, and the step then asks the lead as before.
func TestCanaryRunKitListIsReadAsPrinted(t *testing.T) {
	dir := t.TempDir()
	const header = "ROLE       SLUG               AGE        APPLIED MERGED   RUN"
	row := "build      one                1d         no     no       /x/run"
	gone := "build      old                20d        no     no       /x/old  directory gone, saved copy /x/saved (qargi review apply --from-saved saved)"
	residue := "  residue: /x/run/home/.codex/auth.json (a link a start left)"
	for _, c := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"the list as printed", []string{header, row, residue, gone, ""}, "gone 1\nother 0"},
		{"only the header", []string{header}, "gone 0\nother 0"},
		{"a column inserted before RUN", []string{"ROLE       SLUG               AGE        APPLIED MERGED   EXTRA RUN", "build one 1d no no x /x/run"}, "header bad\ngone 0\nother 1"},
		{"a column renamed", []string{"ROLE SLUG AGE APPLIED MERGED PATH", row}, "header bad\ngone 0\nother 0"},
		{"a row of seven fields", []string{header, "build one 1d no no x /x/run"}, "gone 0\nother 1"},
		{"a row with an unknown applied word", []string{header, "build one 1d maybe no /x/run"}, "gone 0\nother 1"},
		{"a line of another kind", []string{header, row, "note: something new"}, "gone 0\nother 1"},
		{"a role that holds the words of a gone row", []string{header, "build  directory gone one 1d no no /x/run"}, "gone 0\nother 1"},
		{"a role that holds the words and a path", []string{header, "/x/a  directory gone one 1d no no"}, "gone 0\nother 1"},
		{"a role of five words, a path and the words of a gone row", []string{header, fmt.Sprintf("%-10s %-18s %-10s %-6s %-8s %s", "x y z no no /fake  directory gone", "one", "1d", "no", "no", "/x/run")}, "gone 0\nother 1"},
		{"a role of the words of a gone row with a saved copy", []string{header, fmt.Sprintf("%-10s %-18s %-10s %-6s %-8s %s", "x y z no no /fake  directory gone, saved copy s (qargi review apply --from-saved s)", "one", "1d", "no", "no", "/x/run")}, "gone 0\nother 1"},
		{"a gone row with a saved copy and one without", []string{header, "build old 20d no no /x/old  directory gone", gone}, "gone 2\nother 0"},
		{"an empty file", []string{}, "header bad\ngone 0\nother 0"},
	} {
		f := canaryWrite(t, dir, "list.txt", strings.Join(c.lines, "\n")+"\n")
		if c.name == "an empty file" {
			f = canaryWrite(t, dir, "list.txt", "")
		}
		if got := canaryRead(t, "list-scan", f); got != c.want {
			t.Errorf("list scan, %s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The step 12 word is kept, and what the machine check covers is said: the
// holder writes sandbox_started only after its own gate passed. The comment and
// the README must not say that the probe's result is "in no file".
func TestCanaryRunKitStep12SaysWhatTheCheckCovers(t *testing.T) {
	script := canaryScriptText(t)
	readme, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run/README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(script, "are in no file") {
		t.Error("the script says the probe's results are in no file")
	}
	for _, want := range []string{"sandbox_started=true only", "assertion 10, and only in canary mode", "affirm 'Lead: the probe reported once and every assertion is ok except 10'"} {
		if !strings.Contains(script, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	for _, want := range []string{"the holder writes `sandbox_started` only", "required, and the script reads that"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("the README lacks %q", want)
		}
	}
}

func TestCanaryRunKitLoadedUnitsLeaveSlicesOut(t *testing.T) {
	dir := t.TempDir()
	only := canaryWrite(t, dir, "slices.txt", "ataniq-qualify-ab12.slice loaded active active x\nataniq-old.slice loaded inactive dead y\n")
	if got := canaryRead(t, "loaded-units", only); got != "" {
		t.Errorf("slices counted: %q", got)
	}
	mixed := canaryWrite(t, dir, "mixed.txt", "ataniq-old.slice loaded inactive dead y\nataniq-lead-x.service loaded active running z\n")
	if got := canaryRead(t, "loaded-units", mixed); got != "ataniq-lead-x.service" {
		t.Errorf("got %q", got)
	}
}

func TestCanaryRunKitTrackedChangesOnly(t *testing.T) {
	dir := t.TempDir()
	clean := canaryWrite(t, dir, "clean.txt", "?? .codex/\n?? notes.txt\n")
	if got := canaryRead(t, "tracked-changes", clean); got != "" {
		t.Errorf("untracked entries refused: %q", got)
	}
	dirty := canaryWrite(t, dir, "dirty.txt", "?? .codex/\n M a.go\nA  b.go\n")
	if got := canaryRead(t, "tracked-changes", dirty); got != " M a.go\nA  b.go" {
		t.Errorf("got %q", got)
	}
}

func TestCanaryRunKitFactsVerdicts(t *testing.T) {
	unit := func(verdict string, results ...string) string {
		lines := []string{"tool=leadunit-qualify", "verdict=" + verdict}
		for i, r := range results {
			lines = append(lines, "checks."+strconv.Itoa(i)+".name=c"+strconv.Itoa(i), "checks."+strconv.Itoa(i)+".result="+r)
		}
		return strings.Join(lines, "\n")
	}
	// withLimits is a report of a run with limits: the two read-back checks are
	// among the checks (named as leadunit-qualify names them).
	withLimits := func(tool, show, cgroup string) string {
		return strings.Join([]string{
			"tool=" + tool, "verdict=pass",
			"checks.0.name=unit-starts", "checks.0.result=pass",
			"checks.1.name=limits-show-read-back", "checks.1.result=" + show,
			"checks.2.name=limits-cgroup-read-back", "checks.2.result=" + cgroup,
		}, "\n")
	}
	const limitsOK = "ok verdict pass, 3 checks, every result pass, the limits read back"
	for _, c := range []struct{ name, fields, want string }{
		{"a run with limits", withLimits("leadunit-qualify", "pass", "pass"), limitsOK},
		{"a run without limits", unit("pass", "pass", "pass"), "bad the report has no passing check limits-show-read-back (a run without limits?)"},
		{"no cgroup read-back", strings.Replace(withLimits("leadunit-qualify", "pass", "pass"), "limits-cgroup-read-back", "other", 1), "bad the report has no passing check limits-cgroup-read-back (a run without limits?)"},
		{"another tool", withLimits("other-tool", "pass", "pass"), "bad the report is of the tool other-tool, not leadunit-qualify"},
		{"no tool", strings.Replace(withLimits("x", "pass", "pass"), "tool=x\n", "", 1), "bad the report is of the tool , not leadunit-qualify"},
		{"a failed limit check", withLimits("leadunit-qualify", "pass", "fail"), "bad check 2 (limits-cgroup-read-back) has the result fail, not pass"},
		{"a failed check", unit("pass", "pass", "fail"), "bad check 1 (c1) has the result fail, not pass"},
		{"a failed verdict", unit("fail", "pass"), "bad the verdict is fail, not pass"},
		{"no verdict", "tool=leadunit-qualify\nchecks.0.name=a\nchecks.0.result=pass", "bad the report has no verdict"},
		{"no checks", "tool=leadunit-qualify\nverdict=pass", "bad the report has no checks"},
		{"a check without a result", "tool=leadunit-qualify\nverdict=pass\nchecks.0.name=a", "bad 1 check(s) have no result"},
	} {
		if got := canaryRead(t, "unit-facts", c.fields); got != c.want {
			t.Errorf("unit facts, %s: got %q, want %q", c.name, got, c.want)
		}
	}
	plan := func(step, code, result string) string {
		return strings.Join([]string{
			"steps.0.name=validate-config", "steps.0.status=ok",
			"steps.1.name=resolv-conf", "steps.1.status=" + step,
			"exit.launched=true", "exit.exited=true", "exit.code=" + code, "report_outcome=line-read",
			"cases.0.case=S1", "cases.0.results.0.id=holder-hidden", "cases.0.results.0.status=ok",
			"cases.1.results.0.id=qualification-record", "cases.1.results.0.status=" + result,
		}, "\n")
	}
	neverLaunched := strings.Join([]string{
		"dry_run=true", "report_outcome=not-launched", "stop.state=not-launched",
		"steps.0.name=validate-config", "steps.0.status=ok",
		"steps.1.name=resolv-conf", "steps.1.status=skipped",
		"exit.launched=false", "exit.exited=false", "exit.code=0",
		"cases.0.case=S1", "cases.0.results.0.id=holder-hidden", "cases.0.results.0.status=not-checked",
	}, "\n")
	for _, c := range []struct{ name, fields, want string }{
		{"a pass", plan("ok", "0", "ok"), "ok 2 steps ok or skipped, exit.code 0, 2 case results ok or not-checked"},
		{"skipped and not checked", plan("skipped", "0", "not-checked"), "ok 2 steps ok or skipped, exit.code 0, 2 case results ok or not-checked"},
		{"a failed step", plan("failed", "0", "ok"), "bad step 1 (resolv-conf) has the status failed"},
		{"a nonzero exit", plan("ok", "1", "ok"), "bad exit.code is 1, not 0"},
		{"a failed case result", plan("ok", "0", "fail"), "bad case result 1.0 (qualification-record) has the status fail"},
		{"no exit", "steps.0.status=ok\nexit.launched=true\nexit.exited=true\nreport_outcome=line-read\ncases.0.results.0.status=ok", "bad the summary has no exit.code"},
		{"no step", "exit.launched=true\nexit.exited=true\nreport_outcome=line-read\nexit.code=0", "bad the summary has no step"},
		{"no case result", "steps.0.status=ok\nexit.launched=true\nexit.exited=true\nreport_outcome=line-read\nexit.code=0", "bad the summary has no case result"},
		// The shape of the summary of a dry run, which launches nothing: every
		// step is ok or skipped, exit.code is the zero value, and the case
		// results are not-checked.
		{"never launched", neverLaunched, "bad the plan was not launched (exit.launched is false)"},
		{"launched, did not exit", strings.Replace(plan("ok", "0", "ok"), "exit.exited=true", "exit.exited=false", 1), "bad the plan did not exit (exit.exited is false)"},
		{"no report line read", strings.Replace(plan("ok", "0", "ok"), "report_outcome=line-read", "report_outcome=not-launched", 1), "bad report_outcome is not-launched, not line-read"},
		{"no launch field", strings.Replace(plan("ok", "0", "ok"), "exit.launched=true\n", "", 1), "bad the plan was not launched (exit.launched is )"},
	} {
		if got := canaryRead(t, "plan-facts", c.fields); got != c.want {
			t.Errorf("plan facts, %s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCanaryRunKitHolderStartVerdict(t *testing.T) {
	const digest = "0123abcd"
	rec := func(started, version, dig string) string {
		return "profile_digest=" + dig + "\nprofile_version=" + version + "\nsandbox_started=" + started + "\nwaiver=x"
	}
	for _, c := range []struct{ name, fields, version, digest, want string }{
		{"a good start", rec("true", "4", digest), "4", digest, "ok sandbox started, profile version 4, profile digest is the pinned digest"},
		{"not started", rec("false", "4", digest), "4", digest, "bad holder.json does not say the sandbox started (sandbox_started is not true)"},
		{"another version", rec("true", "3", digest), "4", digest, "bad holder.json profile_version is not 4"},
		{"another digest", rec("true", "4", "ffff"), "4", digest, "bad holder.json profile_digest is not the pinned digest of the config"},
		{"no digest in the config", rec("true", "4", digest), "4", "", "bad the config names no profile digest"},
		{"no sandbox_started line", "profile_version=4\nprofile_digest=" + digest, "4", digest, "bad holder.json does not say the sandbox started (sandbox_started is not true)"},
	} {
		if got := canaryRead(t, "holder-start", c.fields, c.version, c.digest); got != c.want {
			t.Errorf("holder start, %s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestCanaryRunKitBranchesNameTheCommits(t *testing.T) {
	dir := t.TempDir()
	const tip, base = "1111111111111111111111111111111111111111", "2222222222222222222222222222222222222222"
	text := canaryWrite(t, dir, "branches.txt", strings.Join([]string{
		"Lead branches (text of the lead):",
		"  lead  canary/one  " + tip,
		"  lead  main  " + base,
		"Upstream branches:",
		"  main  " + base,
		"",
	}, "\n"))
	empty := canaryWrite(t, dir, "empty.txt", "Lead branches (text of the lead):\n  (none)\nUpstream branches:\n  (none)\n")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"upstream-id", text, "main"}, base},
		{[]string{"upstream-id", text, "canary/one"}, ""},
		{[]string{"upstream-id", text, "other"}, ""},
		{[]string{"upstream-id", empty, "main"}, ""},
		{[]string{"lead-id", text, "lead", "canary/one"}, tip},
		{[]string{"lead-id", text, "lead", "absent"}, ""},
		{[]string{"lead-id", text, "main", "main"}, ""},
		{[]string{"lead-id", empty, "lead", "canary/one"}, ""},
	} {
		if got := canaryRead(t, c.args...); got != c.want {
			t.Errorf("%v: got %q, want %q", c.args[:1], got, c.want)
		}
	}
}

// Each step that reads a fact itself fails the step when the fact is false:
// the read is a check, and the steps call the helpers the cases above test.
func TestCanaryRunKitStepsCallTheirReads(t *testing.T) {
	funcs := canaryFuncs(t, canaryScriptText(t))
	for _, c := range []struct{ fn, word string }{
		{"no_open_runs", `open_runs "$LASTOUT" "$REPO"`},
		{"no_open_runs", `list_gone "$LASTOUT"`},
		{"step_1", "no_open_runs || return 0"},
		{"step_1", `loaded_units "$LASTOUT"`},
		{"facts_verdict", "unit_facts_verdict"},
		{"facts_verdict", "plan_facts_verdict"},
		{"step_2", `facts_verdict "$_kind" || return 0`},
		{"step_3", `is_eq "$_head" "$_dev"`},
		{"step_3", `tracked_changes "$_wtf"`},
		{"step_8", `lists_upstream "$LASTOUT" "$UPSTREAM"`},
		{"step_12", "holder_start_verdict"},
		{"step_12", "holder.profile_digest"},
		{"step_20", `is_eq "$_up" "$_lead"`},
		{"step_20", `--git-dir "$MIRROR" rev-parse`},
	} {
		if !strings.Contains(funcs[c.fn], c.word) {
			t.Errorf("%s does not hold %s", c.fn, c.word)
		}
	}
	// A failing command must not read as an empty answer: each listing that a
	// step reads is followed by a check of its status.
	for _, c := range []struct{ fn, cmd, check string }{
		{"step_1", "runcap lead-units systemctl", "check 'the ataniq-lead-* units can be listed' is_rc 0"},
		{"step_1", "runcap units-before systemctl", "check 'the ataniq units can be listed' is_rc 0"},
		{"step_3", `runcap worktree git -C "$REPO" status --porcelain`, "check 'the work tree can be read' is_rc 0"},
	} {
		body := funcs[c.fn]
		i := strings.Index(body, c.cmd)
		if i < 0 {
			t.Errorf("%s does not run %s", c.fn, c.cmd)
			continue
		}
		rest := body[i:]
		if nl := strings.Index(rest, "\n"); nl < 0 || !strings.HasPrefix(strings.TrimLeft(rest[nl:], " \n"), c.check) {
			t.Errorf("%s: %s is not followed by %s", c.fn, c.cmd, c.check)
		}
	}
	// And the order is not all: a command that exits non-zero must fail the
	// step. The two lines run under the script's own runcap, check, is_rc and
	// fail, over stubs of git and systemctl.
	for _, c := range []struct{ fn, cmd string }{
		{"step_1", "runcap lead-units systemctl"},
		{"step_1", "runcap units-before systemctl"},
		{"step_3", `runcap worktree git -C "$REPO" status --porcelain`},
	} {
		lines := strings.Split(funcs[c.fn], "\n")
		var pair string
		for i, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), c.cmd) && i+1 < len(lines) {
				pair = strings.TrimSpace(l) + "\n" + strings.TrimSpace(lines[i+1])
			}
		}
		if pair == "" {
			t.Errorf("%s does not run %s", c.fn, c.cmd)
			continue
		}
		for _, code := range []int{1, 0} {
			out := canaryStepPair(t, pair, code)
			failed := strings.Contains(out, "FAILED at step 1") && !strings.Contains(out, "STEP-PASSED") && strings.Contains(out, "findings=1")
			passed := strings.Contains(out, "STEP-PASSED") && !strings.Contains(out, "FAILED") && strings.Contains(out, "findings=0")
			if code != 0 && !failed {
				t.Errorf("%s: %s exits %d and the step does not fail:\n%s", c.fn, c.cmd, code, out)
			}
			if code == 0 && !passed {
				t.Errorf("%s: %s exits 0 and the step does not pass:\n%s", c.fn, c.cmd, out)
			}
		}
	}
	// Without qargi, with a list it cannot read, or with an open run whose
	// repo.path cannot be read, the old prompt stays and the log says so.
	body := funcs["no_open_runs"]
	if strings.Count(body, "affirm 'Lead: no Qargi run is open'") != 3 || strings.Count(body, "note '") < 2 || !strings.Contains(body, "open_unread") {
		t.Error("the fallback of the open-run check does not ask and say so in the log")
	}
}

// canaryDef returns the whole definition of the script's function.
func canaryDef(t *testing.T, script, name string) string {
	t.Helper()
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `\(\) \{.*$`)
	m := re.FindStringIndex(script)
	if m == nil {
		t.Fatalf("the script lacks %s", name)
	}
	if strings.HasSuffix(strings.TrimSpace(script[m[0]:m[1]]), "}") {
		return script[m[0]:m[1]] + "\n"
	}
	end := strings.Index(script[m[1]:], "\n}\n")
	if end < 0 {
		t.Fatalf("the function %s does not end", name)
	}
	return script[m[0] : m[1]+end+3]
}

// canaryStepPair runs a runcap line and the check line after it, as a step
// has them, under the script's own helpers, with git and systemctl stubs that
// exit with code. It returns the output, then the number of findings.
func canaryStepPair(t *testing.T, pair string, code int) string {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	script := canaryScriptText(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	for _, name := range []string{"git", "systemctl"} {
		canaryWrite(t, bin, name, fmt.Sprintf("#!/bin/sh\nexit %d\n", code))
		if err := os.Chmod(filepath.Join(bin, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "ev", "out"), 0o700); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("DRY=0 CUR=1 EPOCH_NO=1 IN_CHAIN=1 FINDINGS=0 LOG= RC=0 LASTOUT= LASTERR= REPO=/x\nEV=" + filepath.Join(dir, "ev") + "\n")
	for _, name := range []string{"out", "note", "dry", "fail", "runcap", "check", "is_rc"} {
		b.WriteString(canaryDef(t, script, name))
	}
	b.WriteString("s() {\n" + pair + "\n  echo STEP-PASSED\n}\ns\necho findings=$FINDINGS\n")
	cmd := exec.Command(sh, "-c", b.String())
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + dir}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// The helpers of the kit: canary-prepare.sh and canary-reset.sh. Their tests
// run them in a folder of their own with stub binaries that record being
// called, and with --dry or --self-test, or (prepare) for real: prepare
// starts nothing, it makes files and runs the stubs of ataniq-intake and
// ataniq-canary. Nothing here starts a unit, a namespace or an agent.

const (
	canaryPrepareScript = "qualification/canary/run/canary-prepare.sh"
	canaryResetScript   = "qualification/canary/run/canary-reset.sh"
	canaryLibScript     = "qualification/canary/run/canary-lib.sh"
	canaryStubDigest    = "abababababababababababababababababababababababababababababababab"
)

func TestCanaryRunKitHelpersArePlainShell(t *testing.T) {
	canaryPlainShell(t, canaryPrepareScript, true)
	canaryPlainShell(t, canaryResetScript, true)
	canaryPlainShell(t, canaryPreflightScript, true)
	canaryPlainShell(t, canaryLibScript, false)
	// The kit's scripts are these and no others: a new one is listed here.
	entries, err := os.ReadDir(filepath.Join(root(t), "qualification/canary/run"))
	if err != nil {
		t.Fatal(err)
	}
	var shells []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sh") {
			shells = append(shells, e.Name())
		}
	}
	if want := []string{"canary-lib.sh", "canary-preflight.sh", "canary-prepare.sh", "canary-reset.sh", "canary-run.sh"}; !slices.Equal(shells, want) {
		t.Errorf("the scripts of the kit are %v, want %v", shells, want)
	}
	// The helpers never answer a prompt and start no unit, daemon or sandbox.
	for _, rel := range []string{canaryPrepareScript, canaryResetScript, canaryPreflightScript, canaryLibScript} {
		data, err := os.ReadFile(filepath.Join(root(t), rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range strings.Split(string(data), "\n") {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "#") {
				continue
			}
			for _, bad := range []string{"systemd-run", "systemctl", "unshare", "yes |", "echo reset", "printf 'reset"} {
				// The reset reads the loaded units, and does nothing else with
				// systemctl.
				if bad == "systemctl" && rel == canaryResetScript && (strings.HasPrefix(l, "cc_die ") || strings.Contains(l, "systemctl --user list-units --all --no-legend --plain 'ataniq-*'")) {
					continue
				}
				// The preflight asks the list of units, and the commands it
				// prints for the owner to type are text.
				if bad == "systemctl" && rel == canaryPreflightScript {
					ok := false
					for _, allowed := range []string{"systemctl --user list-units --all --no-legend --plain 'ataniq-*' 2>", "for _t in systemctl loginctl", `fix "systemctl --user cannot list the units`, `"systemctl --user list-units --all 'ataniq-*'"`, `"systemctl --user stop $_loaded"`, "systemctl, loginctl, git, bwrap, pasta and ss are on PATH", "_tools='systemctl loginctl", "_list='systemctl, loginctl"} {
						ok = ok || strings.Contains(l, allowed)
					}
					if ok {
						continue
					}
				}
				if strings.Contains(l, bad) {
					t.Errorf("%s: %q: %s", rel, l, bad)
				}
			}
			// The reset waits for the owner's word and puts nothing in its place.
			if rel == canaryResetScript && strings.Contains(l, "ANSWER=") && l != "ANSWER=" {
				t.Errorf("%s gives the answer a value: %q", rel, l)
			}
			// A binary run by name is one of the two the helpers need.
			for _, run := range []string{"\"$BIN/", "\"$2/", "\"$BWRAP\"", "\"$PASTA_USE\"", "\"$PASTA\""} {
				if strings.HasPrefix(l, run) && !strings.HasPrefix(l, "\"$BIN/ataniq-canary\" ") && !strings.HasPrefix(l, "\"$2/ataniq-intake\" ") {
					t.Errorf("%s runs a program other than ataniq-canary and ataniq-intake: %q", rel, l)
				}
			}
		}
	}
}

// The scripts find their folder (and so canary-lib.sh and the checkout) from
// their own name. Run by its bare name from its own folder, $0 has no slash:
// the folder must be the current one, not the name of the file. The same
// scripts run by a relative path, an absolute one and a bare name in a folder
// reached through a link all work.
func TestCanaryRunKitHelpersRunByBareName(t *testing.T) {
	runDir := filepath.Join(root(t), "qualification/canary/run")
	link := filepath.Join(canaryOffTmp(t), "kit-link")
	if err := os.Symlink(runDir, link); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		script string
		args   []string
		want   string
	}{
		{"canary-preflight.sh", nil, "usage: canary-preflight.sh --root DIR"},
		{"canary-reset.sh", []string{"--self-test", "typed-word", "reset"}, "go"},
		{"canary-prepare.sh", []string{"--self-test", "pasta-plan", "/bin/sh"}, ""},
	} {
		for how, dirAndName := range map[string][2]string{
			"bare name":           {runDir, c.script},
			"dot slash":           {runDir, "./" + c.script},
			"through a link":      {link, c.script},
			"absolute":            {t.TempDir(), filepath.Join(runDir, c.script)},
			"relative with a dir": {filepath.Dir(runDir), "run/" + c.script},
		} {
			t.Run(c.script+" "+how, func(t *testing.T) {
				cmd := exec.Command("/bin/sh", append([]string{dirAndName[1]}, c.args...)...)
				cmd.Dir = dirAndName[0]
				cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
				out, _ := cmd.CombinedOutput()
				text := string(out)
				for _, bad := range []string{"Not a directory", "No such file", "cannot cd", "can't cd", "not found"} {
					if strings.Contains(text, bad) {
						t.Fatalf("the script does not find its folder:\n%s", text)
					}
				}
				if !strings.Contains(text, c.want) || (c.want == "" && !strings.Contains(text, "use ") && !strings.Contains(text, "copy ")) {
					t.Errorf("the script did not run:\n%s", text)
				}
			})
		}
	}
	// The pattern is the same in the three, and runs no program to make it.
	for _, rel := range []string{canaryPrepareScript, canaryResetScript, canaryPreflightScript} {
		data, err := os.ReadFile(filepath.Join(root(t), rel))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, "case $0 in\n  */*) _d=${0%/*} ;;\n  *) _d=. ;;\nesac\n[ -n \"$_d\" ] || _d=/\n_d=$(cd -P -- \"$_d\" && pwd -P)\n") {
			t.Errorf("%s does not find its folder as the others do", rel)
		}
		if strings.Contains(text, `cd -P -- "${0%/*}"`) {
			t.Errorf("%s takes the folder of $0 as it is", rel)
		}
	}
}

// canaryOffTmp makes a folder outside /tmp and /var/tmp, which the helpers
// refuse as a root: in the user's cache folder, else in /dev/shm. It is
// removed with the test.
func canaryOffTmp(t *testing.T) string {
	t.Helper()
	var bases []string
	if c, err := os.UserCacheDir(); err == nil {
		bases = append(bases, c)
	}
	bases = append(bases, "/dev/shm")
	for _, b := range bases {
		if err := os.MkdirAll(b, 0o700); err != nil {
			continue
		}
		dir, err := os.MkdirTemp(b, "ataniq-runkit-test-")
		if err != nil {
			continue
		}
		real, err := filepath.EvalSymlinks(dir)
		if err != nil || strings.HasPrefix(real, "/tmp/") || strings.HasPrefix(real, "/var/tmp/") {
			os.RemoveAll(dir)
			continue
		}
		t.Cleanup(func() {
			// A provisioned clone has read-only folders: open them first.
			filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
				if err == nil && d.IsDir() {
					os.Chmod(p, 0o700)
				}
				return nil
			})
			os.RemoveAll(dir)
		})
		return real
	}
	t.Skip("no writable folder outside /tmp for the root of a prepare")
	return ""
}

// canaryHelperEnv is the place a helper test runs in: a folder outside /tmp
// that holds the stub binaries (bin), the stub tools (tools) and the calls
// file.
type canaryHelperEnv struct {
	dir, bin, tools, calls string
}

// canaryStubKit writes the stubs. Every stub of the seven binaries records
// its call; ataniq-intake init makes the mirror folder as a bare repository,
// ataniq-canary prints a fixed digest. The others are never called.
func canaryStubKit(t *testing.T) canaryHelperEnv {
	t.Helper()
	dir := canaryOffTmp(t)
	e := canaryHelperEnv{dir: dir, bin: filepath.Join(dir, "bin"), tools: filepath.Join(dir, "tools"), calls: filepath.Join(dir, "calls")}
	for _, d := range []string{e.bin, e.tools, filepath.Join(dir, "home")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	record := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >>'" + e.calls + "'\n"
	stubs := map[string]string{
		"ataniq-intake": record + `[ "$1" = init ] || exit 97
mkdir -p "$3/mirrors"
git init -q --bare "$3/mirrors/$5.git"
`,
		"ataniq-canary": record + `case $1 in
  profile-digest) echo ` + canaryStubDigest + ` ;;
  check-inputs) echo ok ;;
  *) exit 97 ;;
esac
`,
	}
	for _, name := range canaryRunBinaries {
		body, ok := stubs[name]
		if !ok {
			body = record + "exit 97\n"
		}
		if name == "ataniq-canary-standin" {
			body = "#!/bin/sh\n# stand-in stub\n" + record + "exit 97\n"
		}
		if err := os.WriteFile(filepath.Join(e.bin, name), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// A systemctl that records and reports the units in the file "units".
	units := filepath.Join(dir, "units")
	sysctl := "#!/bin/sh\necho \"systemctl $*\" >>'" + e.calls + "'\ncase $* in\n  *list-units*) [ -f '" + units + "-fail' ] && exit 1; [ -f '" + units + "' ] && cat '" + units + "'; exit 0 ;;\nesac\nexit 97\n"
	if err := os.WriteFile(filepath.Join(e.tools, "systemctl"), []byte(sysctl), 0o700); err != nil {
		t.Fatal(err)
	}
	return e
}

// run runs the script with the arguments, with the stub tools before the
// host's on PATH, and returns the output and the exit status.
func (e canaryHelperEnv) run(t *testing.T, rel string, stdin string, args ...string) (string, int) {
	t.Helper()
	home := filepath.Join(e.dir, "home")
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(root(t), rel)}, args...)...)
	cmd.Dir = e.dir
	cmd.Env = []string{"PATH=" + e.tools + ":/usr/bin:/bin", "HOME=" + home, "TMPDIR=" + t.TempDir(), "XDG_RUNTIME_DIR=" + filepath.Join(e.dir, "run")}
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("the script did not run: %v", err)
		}
		code = exit.ExitCode()
	}
	return string(out), code
}

func (e canaryHelperEnv) callLines(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(e.calls)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// pasta writes a regular executable that stands for pasta.
func (e canaryHelperEnv) pasta(t *testing.T) string {
	t.Helper()
	p := filepath.Join(e.dir, "pasta-file")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 97\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}

func (e canaryHelperEnv) prepareArgs(t *testing.T, root string, more ...string) []string {
	t.Helper()
	return append([]string{"--root", root, "--bin", e.bin, "--pasta", e.pasta(t), "--bwrap", "/usr/bin/bwrap"}, more...)
}

func canaryMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return st.Mode().Perm()
}

func TestCanaryRunKitPrepareMakesTheSetUp(t *testing.T) {
	e := canaryStubKit(t)
	r := filepath.Join(e.dir, "set-up")
	out, code := e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r)...)
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	modes := map[string]os.FileMode{
		"":                                    0o700,
		"state":                               0o700,
		"runtime":                             0o700,
		"evidence":                            0o700,
		"config.json":                         0o600,
		"conf/sandbox-inputs.json":            0o600,
		"conf/managed-settings.json":          0o600,
		"conf/guard-hook":                     0o700,
		"conf/replay-screens/1.json":          0o600,
		"conf/replay-screens/2.txt":           0o600,
		"conf/replay-screens/report.txt":      0o600,
		"agent-install/ataniq-canary-standin": 0o700,
		"bin/ataniq-probe":                    0o700,
		"mirror-qual/config.json":             0o600,
		".canary-prepared":                    0o600,
	}
	for name, want := range modes {
		if got := canaryMode(t, filepath.Join(r, name)); got != want {
			t.Errorf("%s: mode %o, want %o", name, got, want)
		}
	}
	// The config: pass A, the stand-in, the proposed limits, the pin from the
	// stub's digest, and every path below the root.
	var cfg struct {
		Role        string
		StateRoot   string `json:"state_root"`
		RuntimeRoot string `json:"runtime_root"`
		Holder      struct {
			Executable    string
			SandboxInputs string `json:"sandbox_inputs"`
			ProfileDigest string `json:"profile_digest"`
		}
		Agent struct {
			Kind, Runtime  string
			FooterMode     string `json:"footer_mode"`
			RuntimeVersion string `json:"runtime_version"`
			Argv           []string
		}
		Inject    bool
		Limits    map[string]int64
		Provision struct {
			Mirror         string
			UpstreamBranch string `json:"upstream_branch"`
			GitEmail       string `json:"git_email"`
			ReplayScreens  string `json:"replay_screens"`
		}
	}
	cfgData, err := os.ReadFile(filepath.Join(r, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(cfgData))
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("config.json: %v\n%s", err, cfgData)
	}
	cfg.Provision.ReplayScreens = strings.TrimPrefix(cfg.Provision.ReplayScreens, r)
	for what, ok := range map[string]bool{
		"the digest is the stub's":    cfg.Holder.ProfileDigest == canaryStubDigest,
		"the stand-in":                cfg.Agent.Kind == "stand-in" && cfg.Agent.Runtime == "claude" && cfg.Agent.RuntimeVersion == "2.1.325" && cfg.Agent.FooterMode == "manual",
		"the agent's command":         len(cfg.Agent.Argv) == 1 && cfg.Agent.Argv[0] == "/ataniq/agent/ataniq-canary-standin",
		"inject":                      cfg.Inject,
		"the roots":                   cfg.StateRoot == r+"/state" && cfg.RuntimeRoot == r+"/runtime",
		"the inputs file":             cfg.Holder.SandboxInputs == r+"/conf/sandbox-inputs.json",
		"the holder":                  cfg.Holder.Executable == e.bin+"/ataniq-holder",
		"the mirror and branch":       cfg.Provision.Mirror == r+"/state/mirrors/throwaway-one.git" && cfg.Provision.UpstreamBranch == "develop",
		"an example.invalid identity": strings.HasSuffix(cfg.Provision.GitEmail, "@example.invalid"),
		"the limits": cfg.Limits["memory_max_bytes"] == 4<<30 && cfg.Limits["swap_max_bytes"] == 0 && cfg.Limits["tasks_max"] == 512 &&
			cfg.Limits["memory_floor_bytes"] == 1<<30 && cfg.Limits["memory_share_percent"] == 50,
	} {
		if !ok {
			t.Errorf("config.json: %s is wrong:\n%s", what, cfgData)
		}
	}
	inputs, err := os.ReadFile(filepath.Join(r, "conf/sandbox-inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var in struct {
		Canary       bool
		ProviderHost string `json:"provider_host"`
		Executables  map[string]string
		Sources      map[string]string
	}
	if err := json.Unmarshal(inputs, &in); err != nil {
		t.Fatalf("sandbox-inputs.json: %v\n%s", err, inputs)
	}
	settings, _ := os.ReadFile(filepath.Join(r, "conf/managed-settings.json"))
	sum := sha256.Sum256(settings)
	if !in.Canary || in.ProviderHost != "api.anthropic.com" || in.Executables["pasta"] != e.pasta(t) || in.Executables["probe"] != r+"/bin/ataniq-probe" ||
		in.Sources["managed_settings_sha256"] != hex.EncodeToString(sum[:]) || in.Sources["agent_install"] != r+"/agent-install" {
		t.Errorf("sandbox-inputs.json:\n%s", inputs)
	}
	// The managed settings are valid JSON that name the guard hook as the
	// PreToolUse hook command at the place the sandbox binds the hook, and hold
	// the marker text the inputs file carries: the holder refuses a claude start
	// without the marker, and its assertion 8 searches the file for it. The hash
	// the inputs file records is the hash of the file as written.
	var ms struct {
		Hooks struct {
			PreToolUse []struct {
				Hooks []struct{ Type, Command string }
			}
		}
	}
	if err := json.Unmarshal(settings, &ms); err != nil {
		t.Fatalf("managed-settings.json is not JSON: %v\n%s", err, settings)
	}
	if len(ms.Hooks.PreToolUse) != 1 || len(ms.Hooks.PreToolUse[0].Hooks) != 1 || ms.Hooks.PreToolUse[0].Hooks[0].Type != "command" || ms.Hooks.PreToolUse[0].Hooks[0].Command != "/ataniq/qargi/guard-hook" {
		t.Errorf("managed-settings.json does not name the guard hook as the PreToolUse command:\n%s", settings)
	}
	if in.Sources["managed_marker"] != "guard-hook" || !strings.Contains(string(settings), in.Sources["managed_marker"]) {
		t.Errorf("the inputs file's sources.managed_marker is %q, which the managed settings must hold:\n%s\n%s", in.Sources["managed_marker"], inputs, settings)
	}
	if hook, _ := os.ReadFile(filepath.Join(r, "conf/guard-hook")); !strings.HasPrefix(string(hook), "#!/bin/sh\n") || canaryMode(t, filepath.Join(r, "conf/guard-hook"))&0o100 == 0 {
		t.Errorf("conf/guard-hook is not an executable script: %q", hook)
	}
	// What was copied, and what was recorded.
	screens := filepath.Join(root(t), "internal/inject/screen/testdata/claude-2.1.325")
	for dst, src := range map[string]string{
		"conf/replay-screens/1.json":          filepath.Join(screens, "idle-before-paste.json"),
		"conf/replay-screens/1.txt":           filepath.Join(screens, "idle-before-paste.txt"),
		"conf/replay-screens/2.json":          filepath.Join(screens, "collapsed-paste-footer-c.json"),
		"conf/replay-screens/2.txt":           filepath.Join(screens, "collapsed-paste-footer-c.txt"),
		"agent-install/ataniq-canary-standin": filepath.Join(e.bin, "ataniq-canary-standin"),
		"bin/ataniq-probe":                    filepath.Join(e.bin, "ataniq-probe"),
	} {
		a, _ := os.ReadFile(filepath.Join(r, dst))
		b, err := os.ReadFile(src)
		if err != nil || len(a) == 0 || !bytes.Equal(a, b) {
			t.Errorf("%s is not a copy of %s", dst, src)
		}
	}
	if marker, _ := os.ReadFile(filepath.Join(r, ".canary-prepared")); !strings.HasPrefix(string(marker), "ataniq-canary-prepare 1\n") {
		t.Errorf("the marker: %q", marker)
	}
	// The mirror holds develop, and the throwaway commit is by a fake author.
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
		o, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, o)
		}
		return strings.TrimSpace(string(o))
	}
	up := git("-C", filepath.Join(r, "upstream"), "rev-parse", "refs/heads/develop")
	if mirror := git("-C", filepath.Join(r, "state/mirrors/throwaway-one.git"), "rev-parse", "refs/heads/develop"); mirror != up {
		t.Errorf("the mirror's develop is %s, the upstream's %s", mirror, up)
	}
	if who := git("-C", filepath.Join(r, "upstream"), "log", "--format=%ae %ce"); who != "author@example.invalid author@example.invalid" {
		t.Errorf("the throwaway commit's people: %q", who)
	}
	if n := git("-C", filepath.Join(r, "upstream"), "rev-list", "--count", "develop"); n != "1" {
		t.Errorf("the upstream has %s commits, want 1", n)
	}
	// Only the two stubs of the work were called, with these arguments.
	wantCalls := []string{
		"ataniq-intake init --state " + r + "/state --project throwaway-one",
		"ataniq-canary profile-digest --config " + r + "/config.json --inputs " + r + "/conf/sandbox-inputs.json",
		"ataniq-canary check-inputs --config " + r + "/config.json --inputs " + r + "/conf/sandbox-inputs.json",
	}
	if got := e.callLines(t); !slices.Equal(got, wantCalls) {
		t.Errorf("calls:\n%v\nwant\n%v", got, wantCalls)
	}
	// The output names the next commands and holds no secret.
	for _, want := range []string{"canary-run.sh --pass A --config " + r + "/config.json --evidence " + r + "/evidence", "build/qualify-mirror --config " + r + "/mirror-qual/config.json", "canary-reset.sh --root " + r, "Nothing was started"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not say %q:\n%s", want, out)
		}
	}
	// The qualify-mirror acknowledgement is the owner's.
	if mq, _ := os.ReadFile(filepath.Join(r, "mirror-qual/config.json")); !strings.Contains(string(mq), `"i_run_this_on_purpose": false`) {
		t.Errorf("mirror-qual/config.json writes the owner's acknowledgement:\n%s", mq)
	}
	if _, err := os.Stat(filepath.Join(r, "mirror-qual/state")); err == nil {
		t.Error("prepare made the qualification state")
	}
}

func TestCanaryRunKitPrepareRefuses(t *testing.T) {
	e := canaryStubKit(t)
	nonEmpty := filepath.Join(e.dir, "full")
	if err := os.MkdirAll(nonEmpty, 0o700); err != nil {
		t.Fatal(err)
	}
	canaryWrite(t, nonEmpty, "x", "x")
	link := filepath.Join(e.dir, "link")
	if err := os.Symlink(nonEmpty, link); err != nil {
		t.Fatal(err)
	}
	inRepo, err := os.MkdirTemp(root(t), "canary-prepare-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(inRepo) })
	tmpRoot, err := os.MkdirTemp("/tmp", "canary-prepare-test-")
	if err != nil {
		t.Skip("no /tmp to try")
	}
	t.Cleanup(func() { os.RemoveAll(tmpRoot) })
	noBin := filepath.Join(e.dir, "bin-missing-one")
	if err := os.MkdirAll(noBin, 0o700); err != nil {
		t.Fatal(err)
	}
	canaryWrite(t, noBin, "ataniqd", "x")
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"a root that is not empty", e.prepareArgs(t, nonEmpty), "not empty"},
		{"a root that is not empty names no delete command", e.prepareArgs(t, nonEmpty), "was not made by canary-prepare.sh; check it before you remove anything"},
		{"a root in the repository", e.prepareArgs(t, filepath.Join(inRepo, "root")), "repository checkout"},
		{"the repository itself", e.prepareArgs(t, root(t)), "repository checkout"},
		{"a root in /tmp", e.prepareArgs(t, filepath.Join(tmpRoot, "root")), "/tmp"},
		{"a root that is /tmp itself", e.prepareArgs(t, "/tmp"), "/tmp"},
		{"a relative root", e.prepareArgs(t, "set-up"), "not an absolute path"},
		{"a root with a .. part", e.prepareArgs(t, e.dir+"/a/../b"), ".. part"},
		{"a root with a quote", e.prepareArgs(t, filepath.Join(e.dir, "a\"b")), "character"},
		{"a root that is a link", e.prepareArgs(t, link), "symbolic link"},
		{"pass B without --claude", e.prepareArgs(t, filepath.Join(e.dir, "b"), "--pass", "B"), "--pass B needs --claude"},
		{"a missing binary", []string{"--root", filepath.Join(e.dir, "c"), "--bin", noBin, "--pasta", e.pasta(t)}, "not built in"},
		{"a pasta link", []string{"--root", filepath.Join(e.dir, "d"), "--bin", e.bin, "--pasta", link}, "link is refused"},
		{"an unknown argument", []string{"--root", filepath.Join(e.dir, "e"), "--yes"}, "usage:"},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, dry := range [][]string{nil, {"--dry"}} {
				out, code := e.run(t, canaryPrepareScript, "", append(slices.Clone(c.args), dry...)...)
				if code == 0 || !strings.Contains(out, c.want) {
					t.Errorf("dry %v: exit status %d, want a refusal naming %q:\n%s", dry, code, c.want, out)
				}
			}
			if c.name == "a root that is not empty names no delete command" {
				out, _ := e.run(t, canaryPrepareScript, "", c.args...)
				if strings.Contains(out, "rm -r") {
					t.Errorf("a root without the mark is given a delete command:\n%s", out)
				}
			}
			if calls := e.callLines(t); len(calls) != 0 {
				t.Errorf("a refusal ran %v", calls)
			}
		})
	}
	if names, _ := os.ReadDir(nonEmpty); len(names) != 1 {
		t.Errorf("the refused root was changed: %v", names)
	}
	if _, err := os.Stat(filepath.Join(inRepo, "root")); err == nil {
		t.Error("a root was made in the repository")
	}
}

// A prepare that fails leaves no marker, so that canary-reset.sh does not
// take its folder for a finished one; and an empty folder that already
// exists is a fine root.
func TestCanaryRunKitPrepareFailureLeavesNoMarker(t *testing.T) {
	e := canaryStubKit(t)
	r := filepath.Join(e.dir, "set-up")
	if err := os.MkdirAll(r, 0o700); err != nil {
		t.Fatal(err)
	}
	failing := "#!/bin/sh\necho \"ataniq-canary $*\" >>'" + e.calls + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(e.bin, "ataniq-canary"), []byte(failing), 0o700); err != nil {
		t.Fatal(err)
	}
	out, code := e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r)...)
	if code == 0 || !strings.Contains(out, "profile-digest failed") {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	if _, err := os.Stat(filepath.Join(r, ".canary-prepared")); err == nil {
		t.Error("a failed prepare wrote its marker")
	}
	// A digest that is not 64 hex digits is no digest.
	bad := "#!/bin/sh\necho not-a-digest\n"
	if err := os.WriteFile(filepath.Join(e.bin, "ataniq-canary"), []byte(bad), 0o700); err != nil {
		t.Fatal(err)
	}
	r2 := filepath.Join(e.dir, "set-up-2")
	if out, code := e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r2)...); code == 0 || !strings.Contains(out, "did not print one digest") {
		t.Errorf("exit status %d:\n%s", code, out)
	}
}

func TestCanaryRunKitPrepareDryWritesNothing(t *testing.T) {
	e := canaryStubKit(t)
	e.pasta(t)
	before := canaryTree(t, e.dir)
	r := filepath.Join(e.dir, "set-up")
	for _, existing := range []bool{false, true} {
		if existing {
			if err := os.MkdirAll(r, 0o700); err != nil {
				t.Fatal(err)
			}
			before = canaryTree(t, e.dir)
		}
		out, code := e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r, "--dry")...)
		if code != 0 {
			t.Fatalf("exit status %d:\n%s", code, out)
		}
		for _, want := range []string{"[dry]", r + "/config.json", r + "/conf/sandbox-inputs.json", "api.anthropic.com", "canary-reset.sh", ".canary-prepared"} {
			if !strings.Contains(out, want) {
				t.Errorf("the dry run does not name %q:\n%s", want, out)
			}
		}
		if after := canaryTree(t, e.dir); !slices.Equal(before, after) {
			t.Errorf("the dry run changed the folder:\nbefore %v\nafter  %v", before, after)
		}
		if calls := e.callLines(t); len(calls) != 0 {
			t.Errorf("the dry run ran %v", calls)
		}
	}
	// --build in a dry run says what it would run and runs nothing.
	out, code := e.run(t, canaryPrepareScript, "", "--root", filepath.Join(e.dir, "other"), "--pasta", e.pasta(t), "--bwrap", "/usr/bin/bwrap", "--build", "--dry")
	if code != 0 || !strings.Contains(out, "make canary-build") {
		t.Errorf("exit status %d:\n%s", code, out)
	}
}

// An empty folder is a root, and a fresh prepare in it works.
func TestCanaryRunKitPrepareTakesAnEmptyFolder(t *testing.T) {
	e := canaryStubKit(t)
	r := filepath.Join(e.dir, "empty")
	if err := os.MkdirAll(r, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, code := e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r)...); code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	if got := canaryMode(t, r); got != 0o700 {
		t.Errorf("the root has mode %o, want 700", got)
	}
}

// The harness refuses a link as pasta, so the script copies what a link leads
// to and says so; a regular file is used as it is; a missing one is refused.
func TestCanaryRunKitPastaPlan(t *testing.T) {
	e := canaryStubKit(t)
	reg := e.pasta(t)
	link := filepath.Join(e.dir, "pasta-link")
	if err := os.Symlink(reg, link); err != nil {
		t.Fatal(err)
	}
	chain := filepath.Join(e.dir, "pasta-chain")
	if err := os.Symlink("pasta-link", chain); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(e.dir, "pasta-dangling")
	if err := os.Symlink("nowhere", dangling); err != nil {
		t.Fatal(err)
	}
	plain := canaryWrite(t, e.dir, "not-executable", "x")
	for _, c := range []struct {
		name, path, want string
		ok               bool
	}{
		{"a regular file", reg, "use " + reg, true},
		{"a link", link, "copy " + reg, true},
		{"a link of a link", chain, "copy " + reg, true},
		{"a missing file", filepath.Join(e.dir, "none"), "give --pasta", false},
		{"a dangling link", dangling, "give --pasta", false},
		{"a file that does not run", plain, "give --pasta", false},
	} {
		out, code := e.run(t, canaryPrepareScript, "", "--self-test", "pasta-plan", c.path)
		if (code == 0) != c.ok || !strings.Contains(out, c.want) {
			t.Errorf("%s: exit status %d, want %q:\n%s", c.name, code, c.want, out)
		}
	}
	if out, code := e.run(t, canaryPrepareScript, "", "--self-test", "pasta-plan"); code != 2 || !strings.Contains(out, "usage:") {
		t.Errorf("a pasta plan with no path: exit status %d:\n%s", code, out)
	}
}

// prepared makes a set-up with the stub binaries, a few entries in state/ and
// runtime/ as a provisioned attempt leaves, and one in each folder the reset
// must keep. It returns the root.
func (e canaryHelperEnv) prepared(t *testing.T) string {
	t.Helper()
	r := filepath.Join(e.dir, "set-up")
	if out, code := e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r)...); code != 0 {
		t.Fatalf("prepare: exit status %d:\n%s", code, out)
	}
	canaryWrite(t, r, "state/leads/throwaway-one-lead/home/file", "x")
	canaryWrite(t, r, "state/canary-root.json", "{}")
	canaryWrite(t, r, "runtime/leads/throwaway-one-lead/run", "x")
	// A read-only entry, as a clone's object store has.
	canaryWrite(t, r, "state/leads/throwaway-one-lead/home/objects/ro", "x")
	if err := os.Chmod(filepath.Join(r, "state/leads/throwaway-one-lead/home/objects/ro"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(r, "state/leads/throwaway-one-lead/home/objects"), 0o500); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"evidence/pass", "conf/extra", "bin/extra", "mirror-qual/state/x"} {
		canaryWrite(t, r, k, "keep")
	}
	if err := os.Truncate(e.calls, 0); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestCanaryRunKitResetRefuses(t *testing.T) {
	e := canaryStubKit(t)
	r := e.prepared(t)
	units := filepath.Join(e.dir, "units")
	read := func(want string, args ...string) {
		t.Helper()
		out, code := e.run(t, canaryResetScript, "", append([]string{"--bin", e.bin}, args...)...)
		if code == 0 || !strings.Contains(out, want) {
			t.Errorf("%v: exit status %d, want a refusal naming %q:\n%s", args, code, want, out)
		}
		if strings.Contains(out, "\ndeleted ") || strings.Contains(out, "Type the word") {
			t.Errorf("%v: the refusal went on:\n%s", args, out)
		}
	}
	// Without a terminal on standard input, nothing is deleted.
	read("not a terminal", "--root", r)
	// Loaded units: a lead's unit, a service of the canary; and a systemctl that fails.
	canaryWrite(t, e.dir, "units", "ataniq-lead-throwaway-one.service loaded active running a lead\n")
	read("unit is loaded", "--root", r)
	canaryWrite(t, e.dir, "units", "ataniq-canary.slice loaded active active a slice\nataniq-lead-x.service loaded inactive dead a lead\n")
	read("ataniq-lead-x.service", "--root", r)
	canaryWrite(t, e.dir, "units-fail", "")
	read("cannot be read", "--root", r)
	os.Remove(units + "-fail")
	// Slices alone are inert: the refusal is then the terminal's.
	canaryWrite(t, e.dir, "units", "ataniq-canary.slice loaded active active a slice\n")
	read("not a terminal", "--root", r)
	os.Remove(units)

	// The root: no --root, a folder without the mark, with another mark, with a link in place of a root.
	other := filepath.Join(e.dir, "other")
	canaryWrite(t, other, "config.json", "{}")
	read("usage:")
	read("no such folder", "--root", filepath.Join(e.dir, "none"))
	read("does not carry the mark", "--root", other)
	canaryWrite(t, other, ".canary-prepared", "something else\n")
	read("not the one canary-prepare.sh writes", "--root", other)
	canaryWrite(t, other, ".canary-prepared", "ataniq-canary-prepare 1\nproject x\n")
	read("lacks upstream", "--root", other)
	read("not an absolute path", "--root", "set-up")
	tmpRoot, err := os.MkdirTemp("/tmp", "canary-reset-test-")
	if err == nil {
		t.Cleanup(func() { os.RemoveAll(tmpRoot) })
		canaryWrite(t, tmpRoot, ".canary-prepared", "ataniq-canary-prepare 1\n")
		read("/tmp", "--root", tmpRoot)
	}
	read("lie one in the other", "--root", root(t))
	link := filepath.Join(e.dir, "link-to-root")
	if err := os.Symlink(r, link); err != nil {
		t.Fatal(err)
	}
	read("symbolic link", "--root", link)
	// A mark that is a link.
	if err := os.Rename(filepath.Join(r, ".canary-prepared"), filepath.Join(e.dir, "mark")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(e.dir, "mark"), filepath.Join(r, ".canary-prepared")); err != nil {
		t.Fatal(err)
	}
	read("does not carry the mark", "--root", r)
	os.Remove(filepath.Join(r, ".canary-prepared"))
	if err := os.Rename(filepath.Join(e.dir, "mark"), filepath.Join(r, ".canary-prepared")); err != nil {
		t.Fatal(err)
	}
	// A state/ that is a link to a folder outside the root: the folder is not touched.
	outside := filepath.Join(e.dir, "outside")
	canaryWrite(t, outside, "precious", "x")
	if err := os.Rename(filepath.Join(r, "state"), filepath.Join(e.dir, "state-moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(r, "state")); err != nil {
		t.Fatal(err)
	}
	read("is a symbolic link", "--root", r)
	os.Remove(filepath.Join(r, "state"))
	if err := os.Rename(filepath.Join(e.dir, "state-moved"), filepath.Join(r, "state")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "precious")); err != nil {
		t.Error("a refused reset touched a folder outside the root")
	}
	// No intake to make the mirror again: refused before anything is deleted.
	read("no ataniq-intake", "--root", r, "--bin", filepath.Join(e.dir, "nowhere"))
	// No refusal deleted anything.
	if got, err := os.ReadFile(filepath.Join(r, "state/leads/throwaway-one-lead/home/file")); err != nil || string(got) != "x" {
		t.Errorf("a refusal deleted the provisioned state: %v", err)
	}
	for _, c := range e.callLines(t) {
		if !strings.HasPrefix(c, "systemctl --user list-units") {
			t.Errorf("a refusal ran %q", c)
		}
	}
}

func TestCanaryRunKitResetDryDeletesNothing(t *testing.T) {
	e := canaryStubKit(t)
	r := e.prepared(t)
	before := canaryTree(t, e.dir)
	out, code := e.run(t, canaryResetScript, "", "--root", r, "--bin", e.bin, "--dry")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	for _, want := range []string{"DELETE", r + "/state", r + "/runtime", "KEEPS config.json, upstream, bin, conf, evidence", "leads/throwaway-one-lead", "[dry] nothing was deleted"} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Type the word") {
		t.Errorf("the dry run asks:\n%s", out)
	}
	if after := canaryTree(t, e.dir); !slices.Equal(before, after) {
		t.Errorf("the dry run changed the folder:\nbefore %v\nafter  %v", before, after)
	}
	// It reads the loaded units, and runs nothing else.
	for _, c := range e.callLines(t) {
		if !strings.HasPrefix(c, "systemctl --user list-units") {
			t.Errorf("the dry run ran %q", c)
		}
	}
}

// What the owner types decides, and only the exact word: the function the
// real prompt uses.
func TestCanaryRunKitResetWordIsExact(t *testing.T) {
	e := canaryStubKit(t)
	for word, want := range map[string]string{
		"reset": "go", "Reset": "stop", "RESET": "stop", "reset ": "stop", " reset": "stop", "": "stop",
		"yes": "stop", "y": "stop", "resets": "stop", "re set": "stop", "reset\n": "stop", "integrate": "stop",
	} {
		if out, code := e.run(t, canaryResetScript, "", "--self-test", "typed-word", word); code != 0 || strings.TrimSpace(out) != want {
			t.Errorf("typed-word %q: exit status %d, got %q, want %q", word, code, out, want)
		}
	}
	// The prompt reads its answer and compares it with that function: the
	// script's text holds no other comparison with the word and never gives
	// the answer a value of its own.
	text, err := os.ReadFile(filepath.Join(root(t), canaryResetScript))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"IFS= read -r ANSWER", `if [ "$(typed_word "$ANSWER")" != go ]; then`, "if [ ! -t 0 ]; then"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("the reset does not hold %q", want)
		}
	}
	if n := strings.Count(string(text), "ANSWER="); n != 1 {
		t.Errorf("ANSWER is given a value %d times, want 1 (its empty start; the read is the other)", n)
	}
	if out, code := e.run(t, canaryResetScript, "", "--self-test", "typed-word"); code != 2 || !strings.Contains(out, "usage:") {
		t.Errorf("typed-word with no word: exit status %d:\n%s", code, out)
	}
}

func TestCanaryRunKitResetFindsTheRootsProcesses(t *testing.T) {
	e := canaryStubKit(t)
	proc := filepath.Join(e.dir, "proc")
	// The root lies below a folder that a link also names.
	r := filepath.Join(e.dir, "parent", "set-up")
	canaryWrite(t, r, "config.json", "{}")
	canaryWrite(t, r, "state/x", "x")
	canaryWrite(t, e.dir, "parent/set-up0/x", "x")
	canaryWrite(t, e.dir, "parent/other/x", "x")
	canaryWrite(t, e.dir, "elsewhere/x", "x")
	if err := os.Symlink(filepath.Join(e.dir, "parent"), filepath.Join(e.dir, "parentlink")); err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(e.dir, "elsewhere")
	ourUID, otherUID := os.Getuid(), os.Getuid()+1
	status := func(uid int) string {
		return fmt.Sprintf("Name:\tataniqd\nUid:\t%d\t%d\t%d\t%d\n", uid, uid, uid, uid)
	}
	type fake struct {
		args []string
		cwd  string // a folder; "dangling" for a link that leads nowhere; "" for no link
		uid  int    // 0: no status file
	}
	const dangling = "dangling"
	d := "/x/build/canary/ataniqd"
	h := "/x/build/canary/ataniq-holder"
	fakes := map[string]fake{
		// Named in the arguments, as before.
		"100": {args: []string{d, "run", "--canary", r + "/config.json"}, cwd: elsewhere},
		"101": {args: []string{h, "--state", r + "/state/leads/x"}, cwd: elsewhere},
		"106": {args: []string{d, r}, cwd: elsewhere},
		// Not this root: a name that only begins like it, an unrelated daemon, other commands.
		"102": {args: []string{d, "run", "--canary", r + "-other/config.json"}, cwd: elsewhere},
		"103": {args: []string{"sleep", r + "/state"}, cwd: r},
		"104": {args: []string{h, "--state", "/elsewhere"}, cwd: elsewhere},
		"105": {args: []string{"vi", r + "/ataniqd"}, cwd: r},
		"115": {args: []string{d, "run", "--canary", "/somewhere/else/config.json"}, cwd: elsewhere},
		"116": {args: []string{d, "run", "--canary", "config.json"}, cwd: filepath.Join(e.dir, "parent", "set-up0")},
		// A relative config path, from the root or from a folder below it.
		"107": {args: []string{d, "run", "--canary", "config.json"}, cwd: r},
		"110": {args: []string{h, "--state", "x"}, cwd: filepath.Join(r, "state")},
		// A relative path that leads to the root from another folder.
		"111": {args: []string{d, "run", "--canary", "../set-up/config.json"}, cwd: filepath.Join(e.dir, "parent", "other")},
		// Only the current folder ties the daemon to the root: no word of its arguments is relative or in the root.
		"117": {args: []string{d}, cwd: r},
		"118": {args: []string{d, "/somewhere/else/config.json"}, cwd: filepath.Join(r, "state")},
		// A binary path that holds a space is one program name; a space in the path of another program does not make it an ataniqd.
		"119": {args: []string{"/opt/my builds/ataniqd"}, cwd: r},
		"120": {args: []string{"/opt/my ataniqd/tool"}, cwd: r},
		// The root named through a symbolic link of its parent, as a word and as the value of a flag.
		"108": {args: []string{d, "run", "--canary", filepath.Join(e.dir, "parentlink", "set-up", "config.json")}, cwd: elsewhere},
		"109": {args: []string{d, "run", "--canary=" + filepath.Join(e.dir, "parentlink", "set-up", "config.json")}, cwd: elsewhere},
		// A current folder that cannot be read: the process of this user is taken for a match; another user's is not; no status is read as ours.
		"112": {args: []string{d, "run"}, cwd: dangling, uid: ourUID},
		"113": {args: []string{d, "run"}},
		"114": {args: []string{d, "run"}, cwd: dangling, uid: otherUID},
	}
	for pid, f := range fakes {
		canaryWrite(t, proc, pid+"/cmdline", strings.Join(f.args, "\x00")+"\x00")
		switch f.cwd {
		case "":
		case dangling:
			if err := os.Symlink(filepath.Join(e.dir, "nowhere"), filepath.Join(proc, pid, "cwd")); err != nil {
				t.Fatal(err)
			}
		default:
			if err := os.Symlink(f.cwd, filepath.Join(proc, pid, "cwd")); err != nil {
				t.Fatal(err)
			}
		}
		if f.uid != 0 {
			canaryWrite(t, proc, pid+"/status", status(f.uid))
		}
	}
	canaryWrite(t, proc, "self/cmdline", "")
	canaryWrite(t, proc, "stat", "x")
	want := "100 101 106 107 108 109 110 111 112 113 117 118 119"
	out, code := e.run(t, canaryResetScript, "", "--self-test", "procs-of", r, proc)
	if got := strings.Join(strings.Fields(out), " "); code != 0 || got != want {
		t.Errorf("exit status %d, processes: %q, want %s", code, got, want)
	}
	// No /proc, or one with no process folder in it: nothing can be said, so it refuses.
	empty := filepath.Join(e.dir, "empty-proc")
	canaryWrite(t, empty, "stat", "x")
	canaryWrite(t, empty, "self/cmdline", "")
	for _, p := range []string{filepath.Join(e.dir, "no-proc"), empty} {
		out, code := e.run(t, canaryResetScript, "", "--self-test", "procs-of", r, p)
		if code == 0 || !strings.Contains(out, "refused") {
			t.Errorf("procs-of %s: exit status %d, want a refusal:\n%s", p, code, out)
		}
	}
}

// The reset's main flow asks the real /proc: a process that is named ataniqd
// and runs in the root (a relative config path, so nothing of the root is in
// its arguments) refuses the reset, in the dry run and without one, and
// nothing is deleted. Removing the process check from the flow fails this.
func TestCanaryRunKitResetRefusesWhileADaemonRuns(t *testing.T) {
	if _, err := os.Stat("/proc/self/cwd"); err != nil {
		t.Skip("no /proc")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	e := canaryStubKit(t)
	r := e.prepared(t)
	start := func(dir string, args ...string) {
		t.Helper()
		cmd := &exec.Cmd{Path: sleep, Args: append([]string{"ataniqd"}, args...), Dir: dir}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	}
	// One of this user in another folder is not this root's.
	start(e.dir, "300")
	if out, code := e.run(t, canaryResetScript, "", "--root", r, "--bin", e.bin, "--dry"); code != 0 {
		t.Fatalf("a daemon elsewhere refused the dry run: exit status %d:\n%s", code, out)
	}
	start(r, "300")
	for _, more := range [][]string{{"--dry"}, nil} {
		out, code := e.run(t, canaryResetScript, "", append([]string{"--root", r, "--bin", e.bin}, more...)...)
		if code == 0 || !strings.Contains(out, "ataniqd or ataniq-holder of this root runs") {
			t.Errorf("%v: exit status %d, want the refusal of the process check:\n%s", more, code, out)
		}
		if strings.Contains(out, "DELETE") || strings.Contains(out, "Type the word") {
			t.Errorf("%v: the reset went on:\n%s", more, out)
		}
	}
	if _, err := os.Stat(filepath.Join(r, "state/leads/throwaway-one-lead/home/file")); err != nil {
		t.Errorf("a refusal deleted the provisioned state: %v", err)
	}
}

// The word typed in a terminal deletes, and the script's part is what it
// shows: this runs it under util-linux script, which gives it a terminal, and
// types the words from the test. It is skipped where script cannot.
// canaryNeedScript is the path of script(1), or skips the test where it cannot
// give a command a terminal.
func canaryNeedScript(t *testing.T) string {
	t.Helper()
	scriptCmd, err := exec.LookPath("script")
	if err != nil {
		t.Skip("no script(1) to give the reset a terminal")
	}
	if err := exec.Command(scriptCmd, "-qec", "true", "/dev/null").Run(); err != nil {
		t.Skip("script(1) cannot make a terminal here")
	}
	return scriptCmd
}

// canaryLockedBuf is a buffer that one goroutine writes and another reads.
type canaryLockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *canaryLockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *canaryLockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// canaryTyped runs the reset of the root r under script(1), waits until it
// asks for the word, calls during (which may change the folder, as the owner's
// other terminal might while the prompt waits), types the word and returns
// what the script printed and how it ended.
func canaryTyped(t *testing.T, scriptCmd string, e canaryHelperEnv, r, word string, during func()) (string, error) {
	t.Helper()
	run := "sh " + filepath.Join(root(t), canaryResetScript) + " --root " + r + " --bin " + e.bin
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	defer wr.Close()
	cmd := exec.Command(scriptCmd, "-qec", run, "/dev/null")
	cmd.Dir = e.dir
	cmd.Env = []string{"PATH=" + e.tools + ":/usr/bin:/bin", "HOME=" + filepath.Join(e.dir, "home"), "TERM=dumb"}
	cmd.Stdin = rd
	var buf canaryLockedBuf
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	asked := false
	for i := 0; i < 100 && !asked; i++ {
		time.Sleep(100 * time.Millisecond)
		asked = strings.Contains(buf.String(), "Type the word reset")
	}
	if !asked {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatalf("the script did not ask for the word:\n%s", buf.String())
	}
	if during != nil {
		during()
	}
	io.WriteString(wr, word+"\n")
	err = cmd.Wait()
	return buf.String(), err
}

// The word typed in a terminal deletes, and the script's part is what it
// shows: this runs it under util-linux script, which gives it a terminal, and
// types the words from the test. It is skipped where script cannot.
func TestCanaryRunKitResetDeletesAfterTheWord(t *testing.T) {
	scriptCmd := canaryNeedScript(t)
	for _, c := range []struct {
		word   string
		delete bool
	}{{"nope", false}, {"Reset", false}, {"reset", true}} {
		t.Run(c.word, func(t *testing.T) {
			e := canaryStubKit(t)
			r := e.prepared(t)
			// A folder that has no mode at all, which only a chmod before the read opens.
			canaryWrite(t, r, "state/closed/inner", "x")
			if err := os.Chmod(filepath.Join(r, "state/closed"), 0); err != nil {
				t.Fatal(err)
			}
			out, err := canaryTyped(t, scriptCmd, e, r, c.word, nil)
			if c.delete != (err == nil) {
				t.Fatalf("word %q: %v\n%s", c.word, err, out)
			}
			if !strings.Contains(out, "Type the word reset") {
				t.Errorf("the script did not ask:\n%s", out)
			}
			_, junkErr := os.Stat(filepath.Join(r, "state/leads/throwaway-one-lead/home/file"))
			if gone := junkErr != nil; gone != c.delete {
				t.Errorf("word %q: the provisioned state is gone: %v, want %v\n%s", c.word, gone, c.delete, out)
			}
			calls := strings.Join(e.callLines(t), "\n")
			if c.delete {
				// The roots and the mirror are made again, empty; the rest is kept.
				for _, k := range []string{"state", "runtime"} {
					if canaryMode(t, filepath.Join(r, k)) != 0o700 {
						t.Errorf("%s is not 0700 after the reset", k)
					}
				}
				if left := canaryTree(t, filepath.Join(r, "runtime")); len(left) != 1 {
					t.Errorf("runtime/ is not empty: %v", left)
				}
				if _, err := os.Stat(filepath.Join(r, "state/canary-root.json")); err == nil {
					t.Error("the provisioned marker of the canary survived")
				}
				mirror := filepath.Join(r, "state/mirrors/throwaway-one.git")
				cmd := exec.Command("git", "-C", mirror, "rev-parse", "refs/heads/develop")
				cmd.Env = []string{"PATH=/usr/bin:/bin", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
				if o, err := cmd.CombinedOutput(); err != nil {
					t.Errorf("the mirror has no develop: %v\n%s", err, o)
				}
				if !strings.Contains(calls, "ataniq-intake init --state "+r+"/state --project throwaway-one") {
					t.Errorf("the mirror was not made with ataniq-intake init:\n%s", calls)
				}
				if strings.Contains(calls, "profile-digest") || strings.Contains(calls, "check-inputs") {
					t.Errorf("the reset computed more than the mirror:\n%s", calls)
				}
			} else if strings.Contains(calls, "ataniq-intake") {
				t.Errorf("a refused word made a mirror:\n%s", calls)
			}
			// What the reset keeps, whatever the word.
			for _, k := range []string{"config.json", "evidence/pass", "conf/extra", "conf/sandbox-inputs.json", "bin/extra", "mirror-qual/state/x", "upstream/README.txt", ".canary-prepared", "agent-install/ataniq-canary-standin", "answers.hint", "CHECKLIST.txt"} {
				if _, err := os.Stat(filepath.Join(r, k)); err != nil {
					t.Errorf("the reset lost %s: %v", k, err)
				}
			}
		})
	}
}

// The prompt can wait for minutes. What changes meanwhile (a unit or a daemon
// started in another terminal, a state/ swapped for a link to a folder
// outside the root) is seen after the word is typed: nothing is deleted, and
// nothing outside the root is deleted or has its mode changed.
func TestCanaryRunKitResetChecksAgainAfterTheWord(t *testing.T) {
	scriptCmd := canaryNeedScript(t)
	sleep, sleepErr := exec.LookPath("sleep")
	_, procErr := os.Stat("/proc/self/cwd")
	for _, c := range []struct {
		name, want string
		during     func(t *testing.T, e canaryHelperEnv, r, outside string)
	}{
		{"unit", "an ataniq-* unit is loaded", func(t *testing.T, e canaryHelperEnv, r, outside string) {
			canaryWrite(t, e.dir, "units", "ataniq-lead-throwaway-one.service loaded active running a lead\n")
		}},
		{"daemon", "ataniqd or ataniq-holder of this root runs", func(t *testing.T, e canaryHelperEnv, r, outside string) {
			if sleepErr != nil || procErr != nil {
				t.Skip("no sleep or no /proc to run a stand-in daemon")
			}
			cmd := &exec.Cmd{Path: sleep, Args: []string{"ataniq-holder", "300"}, Dir: r}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		}},
		{"state-link", "/state is a symbolic link", func(t *testing.T, e canaryHelperEnv, r, outside string) {
			if err := os.Rename(filepath.Join(r, "state"), filepath.Join(e.dir, "state-moved")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(r, "state")); err != nil {
				t.Fatal(err)
			}
		}},
		{"runtime-link", "/runtime is a symbolic link", func(t *testing.T, e canaryHelperEnv, r, outside string) {
			if err := os.Rename(filepath.Join(r, "runtime"), filepath.Join(e.dir, "runtime-moved")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(r, "runtime")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := canaryStubKit(t)
			r := e.prepared(t)
			// A folder outside the root, read-only as a clone's is, that a link may lead to.
			outside := filepath.Join(e.dir, "outside")
			canaryWrite(t, outside, "sub/precious", "x")
			if err := os.Chmod(filepath.Join(outside, "sub/precious"), 0o400); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(outside, "sub"), 0o500); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(outside, 0o500); err != nil {
				t.Fatal(err)
			}
			modes := func() string {
				var b strings.Builder
				for _, p := range []string{outside, filepath.Join(outside, "sub"), filepath.Join(outside, "sub/precious")} {
					fmt.Fprintf(&b, "%s %v\n", p, canaryMode(t, p))
				}
				return b.String()
			}
			before := modes()
			out, err := canaryTyped(t, scriptCmd, e, r, "reset", func() { c.during(t, e, r, outside) })
			if err == nil || !strings.Contains(out, c.want) {
				t.Errorf("after the word the reset did not refuse with %q: %v\n%s", c.want, err, out)
			}
			if strings.Contains(out, "\ndeleted ") || strings.Contains(out, "made ") {
				t.Errorf("the reset went on after the change:\n%s", out)
			}
			if after := modes(); after != before {
				t.Errorf("the modes outside the root changed:\nbefore\n%safter\n%s", before, after)
			}
			if _, err := os.Stat(filepath.Join(outside, "sub/precious")); err != nil {
				t.Errorf("a file outside the root is gone: %v", err)
			}
			// The root's own folders are where they were: kept, or moved by the test, and not touched.
			kept := filepath.Join(r, "state/leads/throwaway-one-lead/home/file")
			switch c.name {
			case "state-link":
				kept = filepath.Join(e.dir, "state-moved/leads/throwaway-one-lead/home/file")
			case "runtime-link":
				kept = filepath.Join(r, "state/leads/throwaway-one-lead/home/file")
				if _, err := os.Stat(filepath.Join(e.dir, "runtime-moved/leads/throwaway-one-lead/run")); err != nil {
					t.Errorf("the moved runtime/ lost its entry: %v", err)
				}
			}
			if _, err := os.Stat(kept); err != nil {
				t.Errorf("a refusal deleted the provisioned state: %v", err)
			}
			for _, c := range e.callLines(t) {
				if !strings.HasPrefix(c, "systemctl --user list-units") {
					t.Errorf("the reset ran %q", c)
				}
			}
		})
	}
}

// The window after the second check: while systemctl and the process scan
// run, a same-user process swaps what the reset is about to delete. The stub
// systemctl does it on its second call (the first is before the plan). The
// root replaced by a link to a folder that holds a state/ and a runtime/ is
// refused, and a state/ replaced by a link is refused by the deletion's own
// check; in both nothing outside the root is deleted (what the make-again step
// does is the test after this one), and the real
// folders are not touched. Removing the entry into the root, or the link
// check of the deletion loop, fails one of these.
func TestCanaryRunKitResetSwapAfterTheSecondCheck(t *testing.T) {
	scriptCmd := canaryNeedScript(t)
	for _, c := range []struct {
		name, want string
		swap       func(r, outside string) string // the shell lines of the second systemctl call
	}{
		{"root", "was replaced", func(r, outside string) string {
			return "mv '" + r + "' '" + r + "-real'\nln -s '" + outside + "' '" + r + "'\n"
		}},
		{"state", "/state became a symbolic link", func(r, outside string) string {
			return "mv '" + r + "/state' '" + r + "/state-moved'\nln -s '" + outside + "/state' '" + r + "/state'\n"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := canaryStubKit(t)
			r := e.prepared(t)
			outside := filepath.Join(e.dir, "outside")
			for _, k := range []string{"state/sub/precious", "runtime/precious"} {
				canaryWrite(t, outside, k, "x")
			}
			if err := os.Chmod(filepath.Join(outside, "state/sub"), 0o500); err != nil {
				t.Fatal(err)
			}
			modes := func() string {
				var b strings.Builder
				for _, p := range canaryTree(t, outside) {
					fmt.Fprintf(&b, "%s %v\n", p, canaryMode(t, p))
				}
				return b.String()
			}
			before := modes()
			sysctl := "#!/bin/sh\necho \"systemctl $*\" >>'" + e.calls + "'\n" + c.swap(r, outside) + "exit 0\n"
			out, err := canaryTyped(t, scriptCmd, e, r, "reset", func() {
				tmp := filepath.Join(e.tools, "systemctl.new")
				if err := os.WriteFile(tmp, []byte(sysctl), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmp, filepath.Join(e.tools, "systemctl")); err != nil {
					t.Fatal(err)
				}
			})
			if err == nil || !strings.Contains(out, c.want) {
				t.Errorf("after the swap the reset did not refuse with %q: %v\n%s", c.want, err, out)
			}
			if strings.Contains(out, "\ndeleted ") || strings.Contains(out, "made ") {
				t.Errorf("the reset went on after the swap:\n%s", out)
			}
			if after := modes(); after != before {
				t.Errorf("the folder outside the root changed:\nbefore\n%safter\n%s", before, after)
			}
			for _, k := range []string{"state/sub/precious", "runtime/precious"} {
				if _, err := os.Stat(filepath.Join(outside, k)); err != nil {
					t.Errorf("a file outside the root is gone: %v", err)
				}
			}
			// What the swap moved aside is the root's own: it is kept.
			kept := filepath.Join(r+"-real", "state/leads/throwaway-one-lead/home/file")
			moved := filepath.Join(r+"-real", "runtime/leads/throwaway-one-lead/run")
			if c.name == "state" {
				kept = filepath.Join(r, "state-moved/leads/throwaway-one-lead/home/file")
				moved = filepath.Join(r, "runtime/leads/throwaway-one-lead/run")
			}
			for _, p := range []string{kept, moved} {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("the provisioned state of the root is gone: %v", err)
				}
			}
		})
	}
}

// The make-again step is checked right before each step, not fully pinned: a
// swap of the root by a same-user process during the mkdir is refused after
// it, and nothing is created outside the root. A stub mkdir on PATH swaps the
// root when it is asked for ./state (the step names its folders relative to
// the folder it entered), before or after it makes them: for a link to a
// folder outside, and for a new folder of its own that has a state/ (only the
// check of the folder entered sees that one). What is not tested, because the
// stub cannot be run between a check and the next call: a swap in the
// milliseconds between the last check and ataniq-intake. Removing the
// same-folder check of the make-again step fails the last case.
func TestCanaryRunKitResetSwapDuringTheMakeAgain(t *testing.T) {
	scriptCmd := canaryNeedScript(t)
	for _, c := range []struct {
		name  string
		after bool // swap after the real mkdir, not before
		swap  func(r, outside string) string
	}{
		{"link-before-mkdir", false, func(r, outside string) string {
			return "mv '" + r + "' '" + r + "-real'\nln -s '" + outside + "' '" + r + "'\n"
		}},
		{"link-after-mkdir", true, func(r, outside string) string {
			return "mv '" + r + "' '" + r + "-real'\nln -s '" + outside + "' '" + r + "'\n"
		}},
		{"folder-after-mkdir", true, func(r, outside string) string {
			return "mv '" + r + "' '" + r + "-real'\n/bin/mkdir -p '" + r + "/state'\n"
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := canaryStubKit(t)
			r := e.prepared(t)
			outside := filepath.Join(e.dir, "outside")
			canaryWrite(t, outside, "keep/precious", "x")
			list := func() string {
				var b strings.Builder
				for _, p := range canaryTree(t, outside) {
					fmt.Fprintf(&b, "%s %v\n", p, canaryMode(t, p))
				}
				return b.String()
			}
			before := list()
			realMkdir := "PATH=/usr/bin:/bin\n"
			var stub string
			if c.after {
				stub = "#!/bin/sh\n" + realMkdir + "mkdir \"$@\" || exit $?\ncase \" $* \" in *' ./state '*)\n" + c.swap(r, outside) + ";;\nesac\n"
			} else {
				stub = "#!/bin/sh\n" + realMkdir + "case \" $* \" in *' ./state '*)\n" + c.swap(r, outside) + ";;\nesac\nexec mkdir \"$@\"\n"
			}
			out, err := canaryTyped(t, scriptCmd, e, r, "reset", func() {
				tmp := filepath.Join(e.tools, "mkdir.new")
				if err := os.WriteFile(tmp, []byte(stub), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(tmp, filepath.Join(e.tools, "mkdir")); err != nil {
					t.Fatal(err)
				}
			})
			if err == nil || !strings.Contains(out, "was replaced while the folders were made") {
				t.Errorf("after the swap the reset did not refuse: %v\n%s", err, out)
			}
			if strings.Contains(out, "again, and the mirror") {
				t.Errorf("the reset went on after the swap:\n%s", out)
			}
			if after := list(); after != before {
				t.Errorf("the folder outside the root changed:\nbefore\n%safter\n%s", before, after)
			}
			// Nothing of the mirror was made where the root's name points.
			if _, err := os.Stat(filepath.Join(r, "state", "mirrors")); err == nil {
				t.Errorf("ataniq-intake ran after the swap: %s/state/mirrors exists", r)
			}
			if calls, _ := os.ReadFile(e.calls); strings.Contains(string(calls), "ataniq-intake init") {
				t.Errorf("ataniq-intake init was called after the swap:\n%s", calls)
			}
		})
	}
}

// The helper that decides "is the folder I entered still the one this name
// leads to" asks for the inode numbers, through the reset's test entry: the
// folder itself is the same; a folder moved away with a new one put at its
// name is different; a link to another folder, and a link to the very folder
// entered, are different (a name that is a link is not the folder); a name
// that is gone, or a folder entered that is gone, is different too.
func TestCanaryRunKitResetSameDirAsksTheKernel(t *testing.T) {
	e := canaryStubKit(t)
	d := filepath.Join(e.dir, "same-dir")
	for _, p := range []string{"a", "other"} {
		if err := os.MkdirAll(filepath.Join(d, p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(entered, name string) string {
		t.Helper()
		out, code := e.run(t, canaryResetScript, "", "--self-test", "same-dir", entered, name)
		if code != 0 {
			t.Fatalf("same-dir %s %s: exit status %d:\n%s", entered, name, code, out)
		}
		return strings.TrimSpace(out)
	}
	a, other := filepath.Join(d, "a"), filepath.Join(d, "other")
	if got := ask(a, a); got != "same" {
		t.Errorf("the folder against its own name: %q, want same", got)
	}
	if got := ask(a, other); got != "different" {
		t.Errorf("a folder against the name of another: %q, want different", got)
	}
	if got := ask(a, filepath.Join(d, "none")); got != "different" {
		t.Errorf("a name that leads nowhere: %q, want different", got)
	}
	// The folder is moved away and a new one put at its name.
	moved := filepath.Join(d, "a-moved")
	if err := os.Rename(a, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(a, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := ask(moved, a); got != "different" {
		t.Errorf("a folder moved away, a new one at its name: %q, want different", got)
	}
	if got := ask(a, a); got != "same" {
		t.Errorf("the new folder against its name: %q, want same", got)
	}
	// The name becomes a link, to another folder and to the folder entered.
	if err := os.Remove(a); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, a); err != nil {
		t.Fatal(err)
	}
	if got := ask(moved, a); got != "different" {
		t.Errorf("a link to another folder: %q, want different", got)
	}
	if got := ask(other, a); got != "different" {
		t.Errorf("a link to the folder entered: %q, want different", got)
	}
	// A folder that cannot be entered is a refusal of the test entry.
	if out, code := e.run(t, canaryResetScript, "", "--self-test", "same-dir", filepath.Join(d, "gone"), a); code == 0 || !strings.Contains(out, "refused") {
		t.Errorf("a folder that cannot be entered: exit status %d, want a refusal:\n%s", code, out)
	}
	if out, code := e.run(t, canaryResetScript, "", "--self-test", "same-dir", a); code != 2 || !strings.Contains(out, "usage:") {
		t.Errorf("same-dir with one folder: exit status %d:\n%s", code, out)
	}
}

// The kit's scripts hold no bare comparison of the output of pwd -P with a
// path. Under dash, pwd -P prints the path cached at the last cd -P and does
// not ask the kernel again, so [ "$(pwd -P)" = "$ROOT" ] after the folder was
// moved away is still true: such a check is a no-op there (it passed under
// bash, which asks the kernel, and CI's dash did not refuse). The check that
// the folder entered is still the one a name leads to is cc_same_dir, which
// compares inode numbers. A pwd -P that follows a cd -P in the same
// command substitution is right in either shell and is not matched.
func TestCanaryRunKitNoBarePwdComparison(t *testing.T) {
	bare := regexp.MustCompile(`\$\(\s*pwd(\s+-[LP])?\s*\)"?\s*(=|==|!=)\s|\s(=|==|!=)\s+"?\$\(\s*pwd(\s+-[LP])?\s*\)`)
	for _, rel := range []string{canaryResetScript, canaryLibScript} {
		data, err := os.ReadFile(filepath.Join(root(t), rel))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if bare.MatchString(line) {
				t.Errorf("%s:%d compares a bare pwd with a path (dash does not ask the kernel again; use cc_same_dir): %s", rel, i+1, line)
			}
		}
	}
	// The pattern itself catches the form that was there.
	for _, line := range []string{
		`[ "$(pwd -P)" = "$ROOT" ] || cc_die x`,
		`  [ "$(cd -P -- "$1/state" && pwd -P)" = "$1/state" ] && [ "$(pwd -P)" = "$1" ] ||`,
		`[ "$ROOT" = "$(pwd -P)" ] || cc_die x`,
		`[ "$(pwd)" != "$ROOT" ] && exit 1`,
	} {
		if !bare.MatchString(line) {
			t.Errorf("the pattern does not catch %q", line)
		}
	}
	if bare.MatchString(`  [ "$(cd -P -- "$1/state" && pwd -P)" = "$1/state" ] && cc_same_dir "$1" ||`) {
		t.Error("the pattern catches a cd -P and pwd -P in one substitution")
	}
}

// The checks are one function, and the main flow calls it twice: once before
// the plan and once after the word, before the first deletion; the deletion
// opens modes with find and not with chmod -R.
func TestCanaryRunKitResetChecksTwiceAndOpensModesWithFind(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), canaryResetScript))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	call := `still_quiet "$ROOT" /proc`
	if n := strings.Count(text, call+"\n"); n != 2 {
		t.Fatalf("the main flow calls %q %d times, want 2", call, n)
	}
	first := strings.Index(text, call)
	prompt := strings.Index(text, "Type the word %s")
	second := strings.LastIndex(text, call)
	del := strings.Index(text, "rm -rf")
	if !(first < prompt && prompt < second && second < del) {
		t.Errorf("the calls are not before the plan and prompt, and after the word and before the deletion: %d %d %d %d", first, prompt, second, del)
	}
	if judged := strings.Index(text, `typed_word "$ANSWER"`); judged < 0 || judged > second {
		t.Error("the second check is not after the word is judged")
	}
	fn := canaryDef(t, text, "still_quiet")
	for _, want := range []string{`procs_of "$1" "$2"`, "systemctl --user list-units", "is a symbolic link", "resolves to another path"} {
		if !strings.Contains(fn, want) {
			t.Errorf("still_quiet does not hold %q", want)
		}
	}
	if strings.Contains(text, "chmod -R u+") {
		t.Error("the reset opens modes with chmod -R, which follows a link given to it")
	}
	if !strings.Contains(text, `find "./$_k" -type d -exec chmod u+rwx {} \;`) {
		t.Error("the reset does not open the modes with find")
	}
	// The deletion runs relative to the root entered once, after the second check.
	enter := strings.LastIndex(text, `cd -P -- "$ROOT"`)
	if !(second < enter && enter < del) || strings.Count(text, `cc_same_dir "$ROOT" || cc_die`) != 2 {
		t.Error("the reset does not enter the root, and check where it is, between the second check and the deletion")
	}
	if !strings.Contains(text, "rm -rf -- \"./$_k\"") || strings.Contains(text, "rm -rf -- \"$ROOT") || strings.Contains(text, "rm -rf -- \"${ROOT") {
		t.Error("the reset deletes through a path that goes through the root")
	}
}

// ---------------------------------------------------------------------------
// What the owner does in another terminal. A step that waits for something he
// starts or stops there (the daemon, the console's ticket, the listeners) asks
// for it again for up to a minute instead of failing at once, and step 26
// asks again before it fails. The tests run the script's own functions through
// its test entry, over a stub ataniqd that counts its calls, with the wait
// made short by the two variables that are for the tests only. Nothing starts
// a daemon, a unit or a sandbox.

// canaryDaemonKit is a folder whose build/canary holds a stub ataniqd. The
// stub counts its calls in the file count and answers by the count: in mode
// "up" it says no daemon (exit status 20) until call number at and answers
// (exit status 0) from then on; in mode "down" it answers until call at-1 and
// says no daemon from call at on. at 0 means it never changes.
type canaryDaemonKit struct{ dir string }

func newCanaryDaemonKit(t *testing.T, mode string, at int) canaryDaemonKit {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "build", "canary")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(dir, "count")
	stub := fmt.Sprintf(`#!/bin/sh
n=$(cat '%[1]s' 2>/dev/null || echo 0)
n=$((n + 1))
echo $n >'%[1]s'
[ "$1" = status ] || exit 97
# A socket that accepts and never answers: the ask only ends when it is cut off.
if [ '%[2]s' = hang ]; then exec sleep 30; fi
if [ '%[2]s' = up ]; then
  if [ %[3]d -ne 0 ] && [ "$n" -ge %[3]d ]; then exit 0; fi
  exit 20
fi
if [ %[3]d -ne 0 ] && [ "$n" -ge %[3]d ]; then exit 20; fi
exit 0
`, count, mode, at)
	if err := os.WriteFile(filepath.Join(bin, "ataniqd"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	return canaryDaemonKit{dir: dir}
}

// calls is the number of times the stub was asked.
func (k canaryDaemonKit) calls(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(k.dir, "count"))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// start starts the test entry in the kit's folder with the short wait and
// returns the running script, whose standard input the test writes the keys
// to.
func (k canaryDaemonKit) start(t *testing.T, tries int, args ...string) *canaryKeyWait {
	t.Helper()
	env := []string{"PATH=/usr/bin:/bin", "HOME=" + k.dir, "CANARY_RUN_WAIT_TRIES=" + strconv.Itoa(tries), "CANARY_RUN_WAIT_INTERVAL=0", "CANARY_RUN_STATUS_TIMEOUT=1"}
	return startCanaryKit(t, canaryRunScript, k.dir, env, append([]string{"--self-test"}, args...)...)
}

// run runs the test entry with the keys as its standard input, all at once,
// and returns its output and exit status.
func (k canaryDaemonKit) run(t *testing.T, tries int, keys string, args ...string) (string, int) {
	t.Helper()
	w := k.start(t, tries, args...)
	if _, err := io.WriteString(w.stdin, keys); err != nil {
		t.Fatal(err)
	}
	_ = w.stdin.Close()
	code := w.finish()
	return w.text(), code
}

// Step 10: the owner presses Enter before the daemon in his other terminal is
// up. The script says once that it waits, and goes on as soon as the daemon
// answers; only a daemon that never answers is a failure, after the wait.
func TestCanaryRunKitStep10PollsForTheDaemon(t *testing.T) {
	const waiting = "waiting for the daemon to listen..."
	for name, c := range map[string]struct {
		at, tries     int
		want          string
		calls, dots   int
		saysWaiting   bool
		asksDuringRun bool
	}{
		"answers at once":           {at: 1, tries: 12, want: "listens", calls: 1},
		"answers on the third ask":  {at: 3, tries: 12, want: "listens", calls: 3, saysWaiting: true},
		"answers late, after dots":  {at: 12, tries: 12, want: "listens", calls: 12, saysWaiting: true, dots: 2},
		"answers on the last ask":   {at: 13, tries: 12, want: "listens", calls: 13, saysWaiting: true, dots: 2},
		"never answers":             {at: 0, tries: 12, want: "silent", calls: 13, saysWaiting: true, dots: 2},
		"never answers, short wait": {at: 0, tries: 4, want: "silent", calls: 5, saysWaiting: true},
		"answers one ask too late":  {at: 6, tries: 4, want: "silent", calls: 5, saysWaiting: true},
	} {
		t.Run(name, func(t *testing.T) {
			k := newCanaryDaemonKit(t, "up", c.at)
			out, code := k.run(t, c.tries, "", "wait-daemon")
			if code != 0 {
				t.Fatalf("exit status %d:\n%s", code, out)
			}
			if got := strings.Count(out, waiting); got != map[bool]int{true: 1, false: 0}[c.saysWaiting] {
				t.Errorf("%q is said %d times:\n%s", waiting, got, out)
			}
			if got := k.calls(t); got != c.calls {
				t.Errorf("the daemon was asked %d times, want %d", got, c.calls)
			}
			if got := strings.Count(strings.ReplaceAll(out, waiting, ""), "."); got != c.dots {
				t.Errorf("%d dots, want %d (one for every 5 asks):\n%s", got, c.dots, out)
			}
			if got := canaryLastWord(out); got != c.want {
				t.Errorf("the answer is %q, want %q:\n%s", got, c.want, out)
			}
		})
	}
}

// canaryLastWord is the last word of the output: what a test entry ends with.
func canaryLastWord(out string) string {
	f := strings.Fields(out)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// The wait is a minute, one ask a second, unless the tests shorten it; a bad
// value of the variables is refused, not taken.
func TestCanaryRunKitWaitDefaultsAndVariables(t *testing.T) {
	text := canaryScriptText(t)
	for _, want := range []string{"WAIT_TRIES=${CANARY_RUN_WAIT_TRIES:-60}", "WAIT_INTERVAL=${CANARY_RUN_WAIT_INTERVAL:-1}", "STATUS_TIMEOUT=${CANARY_RUN_STATUS_TIMEOUT:-5}", "ASK_AGAIN=5", `[ $((_pun % 5)) -eq 0 ]`} {
		if !strings.Contains(text, want) {
			t.Errorf("the script does not hold %q", want)
		}
	}
	// The variables are read in the test entry only: the defaults come first,
	// and the lines that read the environment are inside the self-test branch.
	for _, v := range []string{"CANARY_RUN_WAIT_TRIES", "CANARY_RUN_WAIT_INTERVAL", "CANARY_RUN_STATUS_TIMEOUT"} {
		read := "${" + v + ":-"
		if n := strings.Count(text, read); n != 1 {
			t.Fatalf("%s is read %d times, want once", v, n)
		}
		at := strings.Index(text, read)
		if open := strings.LastIndex(text[:at], "if [ \"$SELFTEST\" -eq 1 ]; then"); open < 0 || strings.Contains(text[open:at], "\nfi\n") {
			t.Errorf("%s is read outside the test entry", v)
		}
	}
	for name, env := range map[string]string{
		"tries in words": "CANARY_RUN_WAIT_TRIES=many", "negative tries": "CANARY_RUN_WAIT_TRIES=-1",
		"interval in words": "CANARY_RUN_WAIT_INTERVAL=soon", "two dots": "CANARY_RUN_WAIT_INTERVAL=1.2.3", "a lone dot": "CANARY_RUN_WAIT_INTERVAL=.",
		"timeout in words": "CANARY_RUN_STATUS_TIMEOUT=soon", "timeout zero": "CANARY_RUN_STATUS_TIMEOUT=0", "timeout with a dot": "CANARY_RUN_STATUS_TIMEOUT=1.5",
	} {
		t.Run(name, func(t *testing.T) {
			k := newCanaryDaemonKit(t, "up", 0)
			cmd := exec.Command("/bin/sh", filepath.Join(root(t), canaryRunScript), "--self-test", "wait-daemon")
			cmd.Dir = k.dir
			cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + k.dir, env}
			out, err := cmd.CombinedOutput()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.ExitCode() != 2 || !strings.Contains(string(out), strings.SplitN(env, "=", 2)[0]) {
				t.Errorf("%s: %v\n%s", env, err, out)
			}
			if k.calls(t) != 0 {
				t.Error("the daemon was asked with a bad variable")
			}
		})
	}
	// A real run (and a dry run) does not read them: a bad value there is
	// not even looked at, so nothing in the environment shortens the wait.
	k := newCanaryDaemonKit(t, "up", 0)
	cmd := exec.Command("/bin/sh", filepath.Join(root(t), canaryRunScript), "--pass", "A", "--config", "/nonexistent/sample/canary.json", "--dry")
	cmd.Dir = k.dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + k.dir, "CANARY_RUN_WAIT_TRIES=many", "CANARY_RUN_WAIT_INTERVAL=soon", "CANARY_RUN_STATUS_TIMEOUT=soon"}
	if out, err := cmd.CombinedOutput(); err != nil || strings.Contains(string(out), "CANARY_RUN_") || !strings.Contains(string(out), "every second for up to 60 s") {
		t.Errorf("a dry run reads the variables of the test entry: %v\n%s", err, out)
	}
}

// An ask of the daemon's status that never ends is cut off, and the wait as a
// whole is bounded by the limit it states: 60 asks one second apart, plus one
// ask's own limit. The stub sleeps on a socket that never answers; the tests
// shorten the wait to 30 asks with no pause and an ask limit of 1 s, so an
// unbounded wait would take half a minute.
func TestCanaryRunKitHungStatusIsBounded(t *testing.T) {
	t.Run("step 10", func(t *testing.T) {
		k := newCanaryDaemonKit(t, "hang", 0)
		start := time.Now()
		out, code := k.run(t, 30, "", "wait-daemon")
		took := time.Since(start)
		if code != 0 || canaryLastWord(out) != "silent" {
			t.Fatalf("exit status %d:\n%s", code, out)
		}
		if took > 12*time.Second {
			t.Errorf("the wait took %v: an ask that hangs is not cut off, or the wait is not bounded", took)
		}
		if n := k.calls(t); n < 2 || n > 5 {
			t.Errorf("the daemon was asked %d times in a wait of about 1 s with an ask limit of 1 s", n)
		}
	})
	t.Run("step 26", func(t *testing.T) {
		k := newCanaryDaemonKit(t, "hang", 0)
		start := time.Now()
		// Two Enters, then the input ends: two rounds of about 2 asks, each
		// cut off after 1 s, and one more ask when no key can be read.
		out, code := k.run(t, 1, "\n\n", "stop-daemon")
		if code != 0 || canaryLastWord(out) != "failed" {
			t.Fatalf("exit status %d:\n%s", code, out)
		}
		if took := time.Since(start); took > 9*time.Second {
			t.Errorf("the step took %v", took)
		}
		if !strings.Contains(out, "FAILED at step 26: a daemon still answers at ") || !strings.Contains(out, "exit 124") {
			t.Errorf("a status that hangs is not told as cut off (exit 124):\n%s", out)
		}
	})
}

// The ticket file of step 14 is taken when it is absent or empty. A ticket
// result that names no file gives an empty name, which is no file and so never
// taken: the step must not say "the ticket was taken" for it.
func TestCanaryRunKitTicketTakenNeedsAFile(t *testing.T) {
	dir := t.TempDir()
	full, empty := filepath.Join(dir, "full"), filepath.Join(dir, "empty")
	canaryWrite(t, dir, "full", "a-ticket")
	canaryWrite(t, dir, "empty", "")
	for name, c := range map[string]struct{ arg, want string }{
		"a file that holds a ticket": {full, "waiting"},
		"an empty file":              {empty, "taken"},
		"a file that is absent":      {filepath.Join(dir, "absent"), "taken"},
		"no name":                    {"", "waiting"},
	} {
		if got := strings.TrimSpace(canaryRead(t, "ticket-taken", c.arg)); got != c.want {
			t.Errorf("%s: %q, want %q", name, got, c.want)
		}
	}
	// Step 14 knows the name before it watches: it is derived from the holder's
	// runtime folder, which must be known, not read back from the mint.
	step14 := canaryFuncs(t, canaryScriptText(t))["step_14"]
	i, j, k := strings.Index(step14, `test -n "$HRUN"`), strings.Index(step14, "_tf=$HRUN/ticket"), strings.Index(step14, "poll_until")
	if i < 0 || j < i || k < j {
		t.Error("step 14 does not know the ticket file's name, from a folder that is known, before it watches the file")
	}
}

// Step 14: the console is started first and the script mints after the
// owner's Enter, because a ticket lives 30 seconds and the console waits for
// one. The command is printed with its real folder and ticket file before the
// mint; the mint's own answer is checked against that file; the second
// console is the owner's test that nothing attaches, and the script reads the
// ticket file and the attach events of the holder's event log.
func TestCanaryRunKitStep14StartsTheConsoleBeforeTheMint(t *testing.T) {
	body := canaryFuncs(t, canaryScriptText(t))["step_14"]
	at := func(sub string) int {
		i := strings.Index(body, sub)
		if i < 0 {
			t.Fatalf("step 14 does not hold %q", sub)
		}
		return i
	}
	show := at(`out "     $BIN/ataniq-console --attach $HRUN --ticket-file $_tf$_viewer"`)
	key := at("wait_key 'Owner: press Enter when the console is up and says it waits for a ticket.")
	mint := at(`runcap ticket "$BIN/ataniqd" canary ticket --config "$CONFIG" --attach-role "$_role"`)
	same := at(`is_eq "$(fld "$LASTOUT" ticket_file)" "$_tf"`)
	watch := at("poll_until 'the console to take the ticket' ticket_taken")
	yes := at("affirm 'Owner: the pane shows the agent' yes")
	if !(show < key && key < mint && mint < same && same < watch && watch < yes) {
		t.Errorf("step 14 does not show the command, wait for the key, mint, compare the file and watch it, in that order")
	}
	if n := strings.Count(body, "runcap ticket"); n != 1 {
		t.Errorf("step 14 mints in %d places, want 1", n)
	}
	// The second console: counted before, asked, and counted after.
	before := at(`_att0=$(count_lines "$HSTATE/events.jsonl" event=attach)`)
	second := at("affirm 'Owner: the second console did not attach and shows \"waiting for a ticket\"' yes")
	after := at(`_att1=$(count_lines "$HSTATE/events.jsonl" event=attach)`)
	cmp := at(`is_eq "$_att1" "$_att0"`)
	empty := strings.LastIndex(body, `ticket_taken "$_tf"`)
	if !(yes < before && before < second && second < after && after < cmp) || empty < second {
		t.Error("step 14 does not count the attach events before the second console and after it, and compare them, after the owner's answer")
	}
	if !strings.Contains(body, `if [ "$EPOCH_NO" -eq 1 ]; then`) {
		t.Error("the second console is not tried in the first epoch only")
	}
	// What the script cannot show it does not claim: a used ticket is never
	// presented to the holder by the kit.
	for _, bad := range []string{"used ticket", "was refused", "is used"} {
		if strings.Contains(body, bad) {
			t.Errorf("step 14 claims %q", bad)
		}
	}
	// What the owner sees and what the script reads: the dry run.
	out, code := canaryDry(t, "--pass", "A", "--config", "/nonexistent/sample/canary.json")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	var first, secondEpoch *canaryBlock
	for _, b := range canaryBlocks(out) {
		m := canaryStepHeading.FindStringSubmatch(b.heading)
		if m == nil || m[1] != "14" {
			continue
		}
		b := b
		if m[3] == "" {
			first = &b
		} else {
			secondEpoch = &b
		}
	}
	if first == nil || secondEpoch == nil {
		t.Fatalf("the dry run has no step 14 of both epochs:\n%s", out)
	}
	console := "build/canary/ataniq-console --attach /sample/canary/runtime/leads/throwaway-one-lead --ticket-file /sample/canary/runtime/leads/throwaway-one-lead/ticket"
	lineOf := func(b *canaryBlock, sub string) int {
		for i, l := range b.lines {
			if strings.Contains(l, sub) {
				return i
			}
		}
		return -1
	}
	for name, b := range map[string]*canaryBlock{"first epoch": first, "second epoch": secondEpoch} {
		// The viewer's epoch prints the command with --viewer, the writer's does not.
		want := console
		if b == secondEpoch {
			want += " --viewer"
		} else if lineOf(b, "--viewer\n") >= 0 || lineOf(b, console+" --viewer") >= 0 {
			t.Errorf("%s: the writer's console command carries --viewer", name)
		}
		shown, keyed, minted := lineOf(b, want), lineOf(b, "wait for Enter: Owner: press Enter when the console is up"), lineOf(b, "run: build/canary/ataniqd canary ticket")
		if shown < 0 || !(shown < keyed && keyed < minted) {
			t.Errorf("%s: the dry run does not show the console command with its real folder and ticket file, then wait for Enter, then mint:\n%s", name, strings.Join(b.lines, "\n"))
		}
		if lineOf(b, "waiting for a ticket") < 0 || lineOf(b, "a ticket lives 30 seconds") < 0 {
			t.Errorf("%s: the dry run does not tell the owner what the console says and how long the ticket lives", name)
		}
		if lineOf(b, "used ticket") >= 0 {
			t.Errorf("%s: the dry run still asks about a used ticket", name)
		}
	}
	if lineOf(first, "does NOT attach") < 0 || lineOf(first, `Owner: the second console did not attach and shows "waiting for a ticket" (type: yes)`) < 0 ||
		lineOf(first, "the attach events of") < 0 || lineOf(first, "no second attach happened") < 0 {
		t.Errorf("the first epoch's dry run does not hold the second console's test:\n%s", strings.Join(first.lines, "\n"))
	}
	if lineOf(secondEpoch, "does NOT attach") >= 0 || lineOf(secondEpoch, "the console of the first epoch must be closed") < 0 {
		t.Errorf("the second epoch's dry run has the second console, or does not tell the owner to close the first one:\n%s", strings.Join(secondEpoch.lines, "\n"))
	}
	// Between the epochs the owner is told, before the unit is stopped, to close
	// the console that is open: it would take the next ticket.
	i, j := strings.Index(out, "Owner: close the console of the first epoch now"), strings.Index(out, "Close the first console first.")
	if i < 0 || j < i {
		t.Error("the second epoch does not tell the owner to close the first console before it stops the unit")
	}
}

// The holder says why it ended on its standard error, which is the unit's
// journal, and nothing else records it. Step 11 prints the tail of the journal
// before it fails because the unit is not active, and step 22 keeps the journal
// in the evidence. The first is read from the text (the step runs systemd); the
// second runs through its test entry over a stub journalctl.
func TestCanaryRunKitKeepsTheUnitJournal(t *testing.T) {
	text := canaryScriptText(t)
	funcs := canaryFuncs(t, text)
	if !strings.Contains(funcs["unit_journal"], `journalctl --user --unit "$UNIT" --since "$T_START" --no-pager --output=short-iso --lines="$1"`) {
		t.Errorf("unit_journal does not ask journalctl for the unit since the start, bounded: %s", funcs["unit_journal"])
	}
	step11 := funcs["step_11"]
	start := strings.Index(step11, "runcap start ")
	stamp := strings.Index(step11, `T_START=$(date '+%Y-%m-%d %H:%M:%S')`)
	if stamp < 0 || start < stamp || !strings.Contains(step11, `[ -z "$T_START" ]`) {
		t.Error("step 11 does not note the time of the first start, once, before it starts the unit")
	}
	for _, want := range []string{`show_unit_journal; fail "the unit is \"$_active\", not active"`, `show_unit_journal; fail "the role is \"$_state\", not starting"`} {
		if !strings.Contains(step11, want) {
			t.Errorf("step 11 does not print the journal before it fails: %s", want)
		}
	}
	if body := funcs["show_unit_journal"]; !strings.Contains(body, "unit_journal 40") {
		t.Errorf("the tail printed at step 11 is not bounded to 40 lines: %s", body)
	}
	step22 := funcs["step_22"]
	if r, j := strings.Index(step22, `runcap revert systemctl --user revert "$UNIT"`), strings.Index(step22, "save_unit_journal"); r < 0 || j < r {
		t.Error("step 22 does not keep the journal after the stop and the revert")
	}
	// The dry run says both.
	out, code := canaryDry(t, "--pass", "A", "--config", "/nonexistent/sample/canary.json")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	for _, b := range canaryBlocks(out) {
		m := canaryStepHeading.FindStringSubmatch(b.heading)
		if m == nil {
			continue
		}
		switch m[1] {
		case "11":
			if b.count("print the last 40 lines of its journal, journalctl --user --unit ataniq-lead-throwaway-one.service --since") != 1 {
				t.Errorf("the dry run of step 11 does not say it prints the journal's tail:\n%s", strings.Join(b.lines, "\n"))
			}
		case "22":
			if b.count("journalctl --user --unit ataniq-lead-throwaway-one.service --since <the time of the first start> --no-pager --output=short-iso --lines=500") != 1 {
				t.Errorf("the dry run of step 22 does not say it keeps the journal:\n%s", strings.Join(b.lines, "\n"))
			}
		}
	}

	// The entry over a stub journalctl.
	for name, c := range map[string]struct {
		stub, start string
		wantOut     []string
		wantFile    string // what the journal file holds ("" when nothing is kept)
	}{
		"the journal is kept": {
			stub: "echo \"$*\" >>\"$CALLS\"\nprintf 'line one\\nline two\\n'\n", start: "2026-11-19 12:00:00",
			wantOut: []string{"as the holder wrote it on its standard error, is in %EV%/out/22-e1-unit-journal.out (2 lines)"}, wantFile: "line one\nline two\n",
		},
		"journalctl fails": {
			stub: "echo \"$*\" >>\"$CALLS\"\necho 'no access' >&2\nexit 1\n", start: "2026-11-19 12:00:00",
			wantOut: []string{"the unit's journal could not be read (journalctl failed: %EV%/out/22-e1-unit-journal.out.err)"}, wantFile: "",
		},
		"no start was asked": {stub: "echo \"$*\" >>\"$CALLS\"\necho x\n", start: "", wantOut: nil, wantFile: ""},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			ev := filepath.Join(dir, "ev")
			tools := filepath.Join(dir, "tools")
			calls := filepath.Join(dir, "calls")
			for _, d := range []string{filepath.Join(ev, "out"), tools} {
				if err := os.MkdirAll(d, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(tools, "journalctl"), []byte("#!/bin/sh\n"+c.stub), 0o700); err != nil {
				t.Fatal(err)
			}
			for _, tool := range []string{"awk", "date", "sed", "cat", "printf"} {
				if real, err := exec.LookPath(tool); err == nil {
					os.Symlink(real, filepath.Join(tools, tool))
				}
			}
			cmd := exec.Command("/bin/sh", filepath.Join(root(t), canaryRunScript), "--self-test", "journal-save", ev, c.start, "ataniq-lead-throwaway-one.service")
			cmd.Dir = dir
			cmd.Env = []string{"PATH=" + tools, "HOME=" + dir, "CALLS=" + calls}
			o, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v\n%s", err, o)
			}
			for _, want := range c.wantOut {
				if !strings.Contains(string(o), strings.ReplaceAll(want, "%EV%", ev)) {
					t.Errorf("the output does not hold %q:\n%s", want, o)
				}
			}
			got, _ := os.ReadFile(filepath.Join(ev, "out", "22-e1-unit-journal.out"))
			if string(got) != c.wantFile {
				t.Errorf("the journal in the evidence is %q, want %q", got, c.wantFile)
			}
			asked, _ := os.ReadFile(calls)
			if c.start == "" {
				if len(asked) != 0 {
					t.Errorf("journalctl was asked with no start: %s", asked)
				}
			} else if want := "--user --unit ataniq-lead-throwaway-one.service --since " + c.start + " --no-pager --output=short-iso --lines=500\n"; string(asked) != want {
				t.Errorf("journalctl was asked %q, want %q", asked, want)
			}
		})
	}
}

// Between the epochs the unit is stopped, and reverted too: step 11 of the
// first epoch set MemoryMax on it with --runtime, and the second start must not
// load that drop-in (the revert of step 22 comes only at the end of the pass).
func TestCanaryRunKitSecondEpochRevertsTheUnit(t *testing.T) {
	body := canaryFuncs(t, canaryScriptText(t))["second_epoch"]
	stop, revert, eleven := strings.Index(body, "stop_role || return 0"), strings.Index(body, `runcap revert-between-epochs systemctl --user revert "$UNIT"`), strings.Index(body, "step_11")
	if stop < 0 || revert < stop || eleven < revert {
		t.Error("the second epoch does not revert the unit after it stopped it and before it starts it again")
	}
	out, code := canaryDry(t, "--pass", "A", "--config", "/nonexistent/sample/canary.json")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	from := strings.Index(out, "== second epoch of pass A")
	to := strings.Index(out, "== step 11 [pass A, second epoch]")
	if from < 0 || to < from {
		t.Fatalf("the dry run has no second epoch:\n%s", out)
	}
	between := out[from:to]
	stopAt := strings.Index(between, "run: build/canary/ataniqd canary stop")
	revertAt := strings.Index(between, "run: systemctl --user revert ataniq-lead-throwaway-one.service")
	if stopAt < 0 || revertAt < stopAt {
		t.Errorf("the dry run does not revert the unit after the stop, between the epochs:\n%s", between)
	}
}

// count-lines is the reading step 14 makes of the event log: how many lines
// hold every PATH=VALUE, each line read as JSON by the helper.
func TestCanaryRunKitCountLines(t *testing.T) {
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	bin := filepath.Join(work, "build", "canary")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	stub := `#!/bin/sh
read -r line
case $line in
  *attach*) printf 'event=attach\nreason=writer\n' ;;
  *detach*) printf 'event=detach\n' ;;
  *partial*) printf 'event=attach\n'; exit 1 ;;
  *) exit 1 ;;
esac
`
	if err := os.WriteFile(filepath.Join(bin, "ataniq-canary"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	count := func(lines []string, pairs ...string) string {
		file := filepath.Join(dir, "events.jsonl")
		if err := os.WriteFile(file, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return canaryCount(t, work, dir, append([]string{file}, pairs...)...)
	}
	for _, c := range []struct {
		name  string
		lines []string
		pairs []string
		want  string
	}{
		{"two attaches and a detach", []string{"attach", "detach", "attach"}, []string{"event=attach"}, "2"},
		{"one", []string{"detach", "attach"}, []string{"event=attach"}, "1"},
		{"every pair must hold", []string{"attach", "attach"}, []string{"event=attach", "reason=viewer"}, "0"},
		{"a line that is not a whole object is passed over", []string{"partial", "attach"}, []string{"event=attach"}, "1"},
		{"no line holds it", []string{"detach"}, []string{"event=attach"}, "0"},
	} {
		if got := count(c.lines, c.pairs...); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
	// A file that is not there counts 0, so the first attach is counted from 0.
	if got := canaryCount(t, work, dir, filepath.Join(dir, "absent"), "event=attach"); got != "0" {
		t.Errorf("a file that is not there: %s, want 0", got)
	}
}

// canaryCount runs the count-lines test entry in the folder work.
func canaryCount(t *testing.T, work, home string, args ...string) string {
	t.Helper()
	path, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	cmd := exec.Command(path, append([]string{filepath.Join(root(t), canaryRunScript), "--self-test", "count-lines"}, args...)...)
	cmd.Dir = work
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("count-lines %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// Step 26, the daemon: it still answers after the owner's Enter, so the script
// says so and asks again, up to 5 more times, and fails only after that.
func TestCanaryRunKitStep26AsksAgainWhileTheDaemonAnswers(t *testing.T) {
	const still = "the daemon still answers: stop it with Ctrl-C in its terminal, then press Enter"
	enters := func(n int) string { return strings.Repeat("\n", n) }
	for name, c := range map[string]struct {
		at, tries, keys int
		want            string
		said, prompts   int
	}{
		"stopped before the Enter":      {at: 1, tries: 2, keys: 1, want: "stopped", said: 0, prompts: 1},
		"stopped while the wait polls":  {at: 3, tries: 5, keys: 1, want: "stopped", said: 0, prompts: 1},
		"stopped after one more ask":    {at: 5, tries: 2, keys: 2, want: "stopped", said: 1, prompts: 2},
		"stopped after the last ask":    {at: 16, tries: 2, keys: 6, want: "stopped", said: 5, prompts: 6},
		"never stopped":                 {at: 0, tries: 2, keys: 9, want: "failed", said: 5, prompts: 6},
		"the input ends while it waits": {at: 0, tries: 2, keys: 2, want: "failed", said: 2, prompts: 3},
	} {
		t.Run(name, func(t *testing.T) {
			k := newCanaryDaemonKit(t, "down", c.at)
			out, code := k.run(t, c.tries, enters(c.keys), "stop-daemon")
			if code != 0 {
				t.Fatalf("exit status %d:\n%s", code, out)
			}
			if got := strings.Count(out, still); got != c.said {
				t.Errorf("%q is said %d times, want %d:\n%s", still, got, c.said, out)
			}
			if got := strings.Count(out, "Press Enter: "); got != c.prompts {
				t.Errorf("%d keys were asked for, want %d:\n%s", got, c.prompts, out)
			}
			if got := canaryLastWord(out); got != c.want {
				t.Errorf("the answer is %q, want %q:\n%s", got, c.want, out)
			}
			if c.want == "failed" && c.keys > 6 && !strings.Contains(out, "FAILED at step 26: a daemon still answers") {
				t.Errorf("the step does not fail with the old reason after 6 asks:\n%s", out)
			}
			if c.want == "stopped" && strings.Contains(out, "FAILED") {
				t.Errorf("a step that stopped has failed:\n%s", out)
			}
		})
	}
}

// Step 26, the roots: one that is still there when the owner says he deleted
// them is said, and he is asked again, up to 5 more times.
func TestCanaryRunKitStep26AsksAgainWhileARootExists(t *testing.T) {
	k := newCanaryDaemonKit(t, "down", 0)
	state, runtime := filepath.Join(k.dir, "state"), filepath.Join(k.dir, "runtime")
	for _, d := range []string{state, runtime} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	out, code := k.run(t, 1, strings.Repeat("\n", 9), "roots-deleted", state, runtime)
	if code != 0 || canaryLastWord(out) != "failed" {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	if n := strings.Count(out, "a root of the canary still exists: delete it as shown above, then press Enter"); n != 5 {
		t.Errorf("the owner is told %d times, want 5:\n%s", n, out)
	}
	if n := strings.Count(out, "still there: "+state); n != 6 {
		t.Errorf("the state root is named %d times, want 6:\n%s", n, out)
	}
	if !strings.Contains(out, "FAILED at step 26: a root of the canary still exists") {
		t.Errorf("the step does not fail after the asks:\n%s", out)
	}
	if _, err := os.Stat(state); err != nil {
		t.Error("the script deleted a root")
	}

	// The owner deletes one while the script asks again, and then the other.
	w := k.start(t, 1, "roots-deleted", state, runtime)
	w.waitFor("Press Enter: ", 1)
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	io.WriteString(w.stdin, "\n")
	w.waitFor("Press Enter: ", 2)
	if !strings.Contains(w.text(), "still there: "+runtime) || strings.Contains(w.text(), "still there: "+state) {
		t.Errorf("the script does not name the root that is left:\n%s", w.text())
	}
	if err := os.Remove(runtime); err != nil {
		t.Fatal(err)
	}
	io.WriteString(w.stdin, "\n")
	if code := w.finish(); code != 0 || canaryLastWord(w.text()) != "deleted" || strings.Contains(w.text(), "FAILED") {
		t.Errorf("exit status %d:\n%s", code, w.text())
	}
}

// Step 26 when no key can be read (Ctrl-D, or the terminal hangs up): the
// clean-up chain's wait for a key records nothing then, so the step looks once
// itself. A daemon that still answers, or a root that still exists, is a
// failure with the reason; with nothing left there is nothing to record.
func TestCanaryRunKitStep26EndOfInputStillFails(t *testing.T) {
	t.Run("a daemon still answers", func(t *testing.T) {
		k := newCanaryDaemonKit(t, "down", 0)
		out, code := k.run(t, 1, "", "stop-daemon")
		if code != 0 || canaryLastWord(out) != "failed" {
			t.Fatalf("exit status %d:\n%s", code, out)
		}
		if !strings.Contains(out, "FAILED at step 26: a daemon still answers at ") || !strings.Contains(out, "no key could be read") {
			t.Errorf("the step does not fail when the input ends and a daemon answers:\n%s", out)
		}
		if got := k.calls(t); got != 1 {
			t.Errorf("the daemon was asked %d times, want 1 (one look, no polling)", got)
		}
	})
	t.Run("a daemon still answers after the first Enter", func(t *testing.T) {
		k := newCanaryDaemonKit(t, "down", 0)
		out, code := k.run(t, 1, "\n", "stop-daemon")
		if code != 0 || canaryLastWord(out) != "failed" || !strings.Contains(out, "FAILED at step 26: a daemon still answers at ") || !strings.Contains(out, "no key could be read") {
			t.Errorf("exit status %d:\n%s", code, out)
		}
	})
	t.Run("no daemon answers", func(t *testing.T) {
		k := newCanaryDaemonKit(t, "up", 0)
		out, code := k.run(t, 1, "", "stop-daemon")
		if code != 0 || strings.Contains(out, "FAILED") {
			t.Errorf("exit status %d, and a step with nothing left to find has failed:\n%s", code, out)
		}
	})
	t.Run("a root still exists", func(t *testing.T) {
		k := newCanaryDaemonKit(t, "down", 0)
		state, runtime := filepath.Join(k.dir, "state"), filepath.Join(k.dir, "runtime")
		if err := os.Mkdir(state, 0o700); err != nil {
			t.Fatal(err)
		}
		out, code := k.run(t, 1, "", "roots-deleted", state, runtime)
		if code != 0 || canaryLastWord(out) != "failed" {
			t.Fatalf("exit status %d:\n%s", code, out)
		}
		if !strings.Contains(out, "FAILED at step 26: a root of the canary still exists; no key could be read") {
			t.Errorf("the step does not fail when the input ends and a root exists:\n%s", out)
		}
		if _, err := os.Stat(state); err != nil {
			t.Error("the script deleted a root")
		}
	})
	t.Run("no root exists", func(t *testing.T) {
		k := newCanaryDaemonKit(t, "down", 0)
		out, code := k.run(t, 1, "", "roots-deleted", filepath.Join(k.dir, "state"), filepath.Join(k.dir, "runtime"))
		if code != 0 || strings.Contains(out, "FAILED") {
			t.Errorf("exit status %d:\n%s", code, out)
		}
	})
	t.Run("a dangling link is a root that exists", func(t *testing.T) {
		k := newCanaryDaemonKit(t, "down", 0)
		state := filepath.Join(k.dir, "state")
		if err := os.Symlink(filepath.Join(k.dir, "nowhere"), state); err != nil {
			t.Fatal(err)
		}
		out, _ := k.run(t, 1, "", "roots-deleted", state, filepath.Join(k.dir, "runtime"))
		if !strings.Contains(out, "FAILED at step 26: a root of the canary still exists") {
			t.Errorf("a dangling link is not counted:\n%s", out)
		}
	})
}

// The other steps that wait for the owner's own act watch for it: the dry run
// says how, and the real branch calls the polling function.
func TestCanaryRunKitStepsWatchWhatTheOwnerDoes(t *testing.T) {
	funcs := canaryFuncs(t, canaryScriptText(t))
	for _, c := range []struct{ fn, word string }{
		{"step_10", "poll_until 'the daemon to listen' daemon_answers"},
		{"step_10", "check 'the daemon listens' is_rc 0"},
		{"step_14", "poll_until 'the console to take the ticket' ticket_taken"},
		{"step_6", "poll_until 'the listeners to stop' listeners_are"},
		{"step_24", "poll_until 'the listeners to come back' listeners_are"},
		{"daemon_stopped_by_owner", "poll_until 'the daemon to stop' daemon_gone"},
		{"step_26", "daemon_stopped_by_owner"},
		{"step_26", "roots_deleted_by_owner"},
	} {
		if !strings.Contains(funcs[c.fn], c.word) {
			t.Errorf("%s does not hold %s", c.fn, c.word)
		}
	}
	// What is watched is never answered: the polling and the asking again
	// read no answer and type nothing.
	for _, fn := range []string{"poll_until", "daemon_answers", "daemon_gone", "ticket_taken", "listeners_are", "daemon_stopped_by_owner", "roots_deleted_by_owner"} {
		body := funcs[fn]
		for _, bad := range []string{"ANSWER", "affirm", `ask "`, "ask '", "read ", "systemd-run", "ataniqd run", "canary start", "rm "} {
			if strings.Contains(body, bad) {
				t.Errorf("%s holds %q", fn, bad)
			}
		}
	}
	// The ticket is never read: only its size is asked.
	if body := funcs["ticket_taken"]; !strings.Contains(body, `[ ! -s "$1" ]`) || strings.Contains(body, "cat") {
		t.Errorf("ticket_taken: %s", body)
	}
	// A wait that runs out at step 14 goes on to the owner's question: the
	// ticket file is not a failure.
	step14 := funcs["step_14"]
	from, to := strings.Index(step14, "poll_until"), strings.Index(step14, "affirm 'Owner: the pane shows the agent'")
	if from < 0 || to < from || strings.Contains(step14[from:to], "fail ") {
		t.Error("step 14 fails on the ticket file, or does not ask its question after the watch")
	}

	for pass, steps := range map[string][]int{"A": {10, 14, 26}, "B": {6, 10, 14, 24, 26}} {
		out, _ := canaryDry(t, "--pass", pass, "--config", "/nonexistent/sample/canary.json")
		for _, b := range canaryBlocks(out) {
			m := canaryStepHeading.FindStringSubmatch(b.heading)
			if m == nil || m[3] != "" {
				continue
			}
			n, _ := strconv.Atoi(m[1])
			if !slices.Contains(steps, n) {
				continue
			}
			if got := b.count("every second for up to 60 s"); got < 1 {
				t.Errorf("pass %s step %d: the dry run does not say it asks every second for up to 60 s:\n%s", pass, n, strings.Join(b.lines, "\n"))
			}
			if n == 26 && b.count("ask again, up to 5 more times") != 2 {
				t.Errorf("pass %s step 26: the dry run does not say the two asks again", pass)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Hints. At a path prompt, and there only, the run script shows a suggested
// path from answers.hint beside the config, and takes an empty line as that
// path. The test entries ask-path and affirm-word give the script's own
// functions a hint file and the owner's keys on standard input.

// canaryRunWith runs the run script's test entry in a fresh folder with the
// text as its standard input and returns its output and exit status.
func canaryRunWith(t *testing.T, stdin string, args ...string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(root(t), canaryRunScript), "--self-test"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("the script did not run: %v", err)
		}
		code = exit.ExitCode()
	}
	return string(out), code
}

// canaryHintFile writes a hint file with the lines and returns its path.
func canaryHintFile(t *testing.T, lines ...string) string {
	t.Helper()
	return canaryWrite(t, t.TempDir(), "answers.hint", strings.Join(lines, "\n")+"\n")
}

func TestCanaryRunKitHintIsTakenByAnEmptyAnswer(t *testing.T) {
	hints := canaryHintFile(t, "unit-report=/sample/facts/unit.json", "template-review=/sample/drafts/review.md", "other=/sample/x")
	for name, c := range map[string]struct {
		key, stdin           string
		wantAnswer, wantFrom string
		shows                bool
	}{
		"an empty line takes the hint":     {"unit-report", "\n", "/sample/facts/unit.json", "hint", true},
		"a typed path wins":                {"unit-report", "/sample/typed/unit.json\n", "/sample/typed/unit.json", "typed", true},
		"the end of the input takes none":  {"unit-report", "", "", "typed", true},
		"another key, another hint":        {"template-review", "\n", "/sample/drafts/review.md", "hint", true},
		"no hint for the key, no answer":   {"plan-summary", "\n", "", "typed", false},
		"no hint for an unknown key":       {"nothing", "\n", "", "typed", false},
		"a typed path wins over no hint":   {"plan-summary", "/sample/typed/plan.json\n", "/sample/typed/plan.json", "typed", false},
		"spaces are an answer, not a hint": {"unit-report", " \n", " ", "typed", true},
	} {
		t.Run(name, func(t *testing.T) {
			out, code := canaryRunWith(t, c.stdin, "ask-path", hints, c.key)
			if code != 0 {
				t.Fatalf("exit status %d:\n%s", code, out)
			}
			out = strings.ReplaceAll(out, "   > ", "   > \n") // the prompt has no line feed when nothing echoes the key
			if !strings.Contains(out, "\nanswer="+c.wantAnswer+"\nsource="+c.wantFrom+"\n") {
				t.Errorf("the answer is not %q (%s):\n%s", c.wantAnswer, c.wantFrom, out)
			}
			if got := strings.Contains(out, "(Enter for: "); got != c.shows {
				t.Errorf("the prompt shows the hint: %v, want %v:\n%s", got, c.shows, out)
			}
			if c.shows && c.key == "unit-report" && !strings.Contains(out, "(Enter for: /sample/facts/unit.json)") {
				t.Errorf("the prompt does not name the hint:\n%s", out)
			}
		})
	}

	// A hint that is not a safe suggestion is not shown: no file, a link, a
	// path that is not absolute, a control character, a line that is only a
	// part of a key.
	dir := t.TempDir()
	if err := os.Symlink(hints, filepath.Join(dir, "link.hint")); err != nil {
		t.Fatal(err)
	}
	for name, file := range map[string]string{
		"no file":             filepath.Join(dir, "none.hint"),
		"a link":              filepath.Join(dir, "link.hint"),
		"not absolute":        canaryHintFile(t, "unit-report=relative/unit.json"),
		"empty":               canaryHintFile(t, "unit-report="),
		"a control character": canaryHintFile(t, "unit-report=/sample/a\x1b[2Jb"),
		"a longer key":        canaryHintFile(t, "unit-report-x=/sample/a"),
		"a comment":           canaryHintFile(t, "# unit-report=/sample/a"),
	} {
		t.Run(name, func(t *testing.T) {
			out, code := canaryRunWith(t, "\n", "ask-path", file, "unit-report")
			out = strings.ReplaceAll(out, "   > ", "   > \n")
			if code != 0 || !strings.Contains(out, "\nanswer=\nsource=typed\n") || strings.Contains(out, "Enter for") {
				t.Errorf("exit status %d:\n%s", code, out)
			}
		})
	}
}

// A word is typed: no hint, whatever the hint file holds, stands in for yes,
// read, integrate, confirmed or reset, and an empty answer is not one.
func TestCanaryRunKitHintNeverAnswersAWord(t *testing.T) {
	words := []string{"yes", "read", "integrate", "confirmed", "reset"}
	var lines []string
	for _, w := range words {
		lines = append(lines, w+"=/sample/"+w, "unit-report=/sample/"+w)
	}
	hints := canaryHintFile(t, lines...)
	for _, word := range words {
		for name, c := range map[string]struct{ stdin, want string }{
			"an empty answer":        {"\n", "refused"},
			"the end of the input":   {"", "refused"},
			"the word, typed":        {word + "\n", "accepted"},
			"another word":           {"maybe\n", "refused"},
			"the hint's path":        {"/sample/" + word + "\n", "refused"},
			"the word with a space":  {word + " \n", "refused"},
			"the word in capitals":   {strings.ToUpper(word) + "\n", "refused"},
			"an empty one, then yes": {"\n" + word + "\n", "refused"},
		} {
			t.Run(word+", "+name, func(t *testing.T) {
				out, code := canaryRunWith(t, c.stdin, "affirm-word", hints, word)
				if code != 0 {
					t.Fatalf("exit status %d:\n%s", code, out)
				}
				if got := canaryLastWord(out); got != c.want {
					t.Errorf("the answer is %q, want %q:\n%s", got, c.want, out)
				}
				if strings.Contains(out, "Enter for") || strings.Contains(out, "the hint is taken") {
					t.Errorf("a word prompt shows or takes a hint:\n%s", out)
				}
			})
		}
	}
}

// The code says the same: the one function that gives a question a hint is
// ask_path, only the four path prompts call it, and no word is asked through
// it.
func TestCanaryRunKitHintsReachOnlyThePathPrompts(t *testing.T) {
	text := canaryScriptText(t)
	funcs := canaryFuncs(t, text)
	if n := strings.Count(text, "hint_for"); n != 3 {
		t.Errorf("hint_for is named %d times, want 3 (its comment, its definition and ask_path)", n)
	}
	if canaryCalls(funcs["ask_path"], "hint_for") != 1 || canaryCalls(funcs["affirm"], "hint_for") != 0 || canaryCalls(funcs["ask"], "hint_for") != 0 {
		t.Error("a hint reaches a question other than through ask_path")
	}
	if body := funcs["affirm"]; strings.Count(body, "ask ") != 1 || !strings.Contains(body, `ask "$1 (type: $2)"`) {
		t.Errorf("affirm asks with more than the question:\n%s", body)
	}
	if body := funcs["ask"]; !strings.Contains(body, `[ "$_askok" -eq 1 ] && [ -z "$ANSWER" ] && [ -n "${2:-}" ]`) {
		t.Errorf("ask takes a hint when the input ends:\n%s", body)
	}
	// Only ask_path calls ask with a second argument.
	twoArgs := regexp.MustCompile(`(?m)^\s*ask ("[^"]*"|'[^']*') \S`)
	for name, body := range funcs {
		if name != "ask_path" && twoArgs.MatchString(body) {
			t.Errorf("%s asks a question with a second argument", name)
		}
	}
	// ask_path is called by ask_file and by the test entry, and ask_file by the
	// four path prompts, by key.
	if n := len(regexp.MustCompile(`(?m)^\s*ask_path "\$_afq" "\$_afk"$`).FindAllString(text, -1)); n != 2 {
		t.Errorf("ask_file calls ask_path %d times (the dry run and the real one), want 2", n)
	}
	calls := regexp.MustCompile(`(?m)^\s*ask_file (\S+)`).FindAllStringSubmatch(text, -1)
	var keys []string
	for _, m := range calls {
		if m[1] != `"$2"` {
			keys = append(keys, m[1])
		}
	}
	slices.Sort(keys)
	if want := []string{`"$_hk"`, "mirror-report", "template-review"}; !slices.Equal(keys, want) {
		t.Errorf("ask_file is called with %v, want %v", keys, want)
	}
	if n := len(regexp.MustCompile(`(?m)^\s*ask_path `).FindAllString(text, -1)); n != 3 {
		t.Errorf("ask_path is called in %d places, want 3 (ask_file twice, the test entry once)", n)
	}
	if step2 := funcs["step_2"]; !strings.Contains(step2, "unit-report") || !strings.Contains(step2, "plan-summary") {
		t.Error("step 2 does not take the hints of the two fact reports")
	}
	// Pass B's question for the evidence folder of pass A stays unhinted.
	if !strings.Contains(funcs["step_2"], "ask 'Lead: the evidence folder of pass A'") {
		t.Error("the folder of pass A is not asked as before")
	}
	// The dry run reads no file, so it reads no hint file.
	if funcs["hint_for"] == "" || !strings.Contains(funcs["ask_path"], `[ "$DRY" -eq 1 ]`) {
		t.Error("ask_path does not leave the dry run alone")
	}
	for pass := range map[string]bool{"A": true, "B": true} {
		out, _ := canaryDry(t, "--pass", pass, "--config", "/nonexistent/sample/canary.json")
		if n := strings.Count(out, `show "(Enter for: PATH)"`); n != 4 {
			t.Errorf("pass %s: the dry run says the hint %d times, want 4", pass, n)
		}
	}
}

// What a path answer records: its source is in answers.txt, and the log
// says whether it was a hint or typed.
func TestCanaryRunKitAnswerRecordsHintOrTyped(t *testing.T) {
	script := canaryScriptText(t)
	for _, c := range []struct{ stdin, answer, source string }{
		{"\n", "/sample/facts/unit.json", "hint"},
		{"/sample/typed.json\n", "/sample/typed.json", "typed"},
	} {
		dir := t.TempDir()
		ev := filepath.Join(dir, "ev")
		if err := os.MkdirAll(ev, 0o700); err != nil {
			t.Fatal(err)
		}
		hints := canaryHintFile(t, "unit-report=/sample/facts/unit.json")
		var b strings.Builder
		b.WriteString("DRY=0 CUR=2 IN_CHAIN=0 SIGNALLED=0 TTY_ASSUMED=0 ANSWER= ANSWER_SRC=typed\nEV=" + ev + "\nLOG=" + ev + "/log.txt\nHINTS=" + hints + "\n")
		for _, name := range []string{"out", "now", "note", "dry", "on_terminal", "read_answer", "ask", "hint_for", "ask_path"} {
			b.WriteString(canaryDef(t, script, name))
		}
		b.WriteString("ask_path 'Lead: the path of the report' unit-report\necho \"got=$ANSWER from=$ANSWER_SRC\"\n")
		cmd := exec.Command("/bin/sh", "-c", b.String())
		cmd.Dir = dir
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
		cmd.Stdin = strings.NewReader(c.stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.Contains(string(out), "got="+c.answer+" from="+c.source) {
			t.Errorf("%q: %s", c.stdin, out)
		}
		answers, _ := os.ReadFile(filepath.Join(ev, "answers.txt"))
		fields := strings.Split(strings.TrimSuffix(string(answers), "\n"), "\t")
		if len(fields) != 5 || fields[1] != "step 2" || fields[2] != "Lead: the path of the report" || fields[3] != c.answer || fields[4] != c.source {
			t.Errorf("answers.txt: %q", answers)
		}
		if log, _ := os.ReadFile(filepath.Join(ev, "log.txt")); !strings.Contains(string(log), "answer recorded ("+c.source+"): Lead: the path of the report") {
			t.Errorf("log.txt: %q", log)
		}
	}
}

// Prepare writes the hint file beside the config: mode 0600, key=path lines,
// the mirror report's path always, the others only for a file that was named
// and is there, and nothing that is not a path.
func TestCanaryRunKitPrepareWritesTheHints(t *testing.T) {
	e := canaryStubKit(t)
	drafts := filepath.Join(e.dir, "drafts")
	review := canaryWrite(t, drafts, "template-review.md", "verdict: sound\n")
	unit := canaryWrite(t, drafts, "unit.json", "{}\n")
	plan := canaryWrite(t, drafts, "plan.json", "{}\n")

	r := filepath.Join(e.dir, "set-up")
	out, code := e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r)...)
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	hint := filepath.Join(r, "answers.hint")
	if got := canaryMode(t, hint); got != 0o600 {
		t.Errorf("answers.hint has mode %o, want 600", got)
	}
	data, err := os.ReadFile(hint)
	if err != nil {
		t.Fatal(err)
	}
	// Without a path given, only the one prepare itself chooses: the review
	// is taken from the argument and from nothing else.
	if want := "mirror-report=" + r + "/mirror-qual/report.json\n"; string(data) != want {
		t.Errorf("answers.hint:\n%s\nwant\n%s", data, want)
	}
	if !strings.Contains(out, hint) {
		t.Errorf("the output does not name answers.hint:\n%s", out)
	}

	r2 := filepath.Join(e.dir, "set-up-two")
	out, code = e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r2, "--template-review", review, "--unit-report", unit, "--plan-summary", plan)...)
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	data, err = os.ReadFile(filepath.Join(r2, "answers.hint"))
	if err != nil {
		t.Fatal(err)
	}
	want := "mirror-report=" + r2 + "/mirror-qual/report.json\nunit-report=" + unit + "\nplan-summary=" + plan + "\ntemplate-review=" + review + "\n"
	if string(data) != want {
		t.Errorf("answers.hint:\n%s\nwant\n%s", data, want)
	}
	// Every line is a key from the list and an absolute path; nothing else
	// (no word, no secret) is in it.
	for _, l := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		k, v, ok := strings.Cut(l, "=")
		if !ok || !slices.Contains([]string{"mirror-report", "unit-report", "plan-summary", "template-review"}, k) || !strings.HasPrefix(v, "/") {
			t.Errorf("answers.hint holds %q", l)
		}
	}

	// A file that is not there is no hint, and the output says so; a path that
	// is not absolute is refused.
	r3 := filepath.Join(e.dir, "set-up-three")
	out, code = e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r3, "--template-review", filepath.Join(drafts, "missing.md"))...)
	if code != 0 || !strings.Contains(out, "no hint is written for --template-review") {
		t.Errorf("exit status %d:\n%s", code, out)
	}
	if data, _ := os.ReadFile(filepath.Join(r3, "answers.hint")); strings.Contains(string(data), "template-review") {
		t.Errorf("a hint for a file that is not there:\n%s", data)
	}
	for _, flag := range []string{"--template-review", "--unit-report", "--plan-summary"} {
		r4 := filepath.Join(e.dir, "set-up-four")
		out, code = e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r4, flag, "relative/path")...)
		if code != 1 || !strings.Contains(out, flag+" is not an absolute path") {
			t.Errorf("%s: exit status %d:\n%s", flag, code, out)
		}
		if _, err := os.Stat(r4); err == nil {
			t.Errorf("%s: a refused prepare made the root", flag)
		}
	}

	// The dry run says so and writes nothing.
	r5 := filepath.Join(e.dir, "set-up-five")
	out, code = e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r5, "--dry")...)
	if code != 0 || !strings.Contains(out, "[dry]   "+r5+"/answers.hint (0600)") {
		t.Errorf("exit status %d:\n%s", code, out)
	}
	if _, err := os.Stat(r5); err == nil {
		t.Error("the dry run made the root")
	}
}

// ---------------------------------------------------------------------------
// A wrong file is refused before it is recorded. At step 4 the template's
// review must not be a report of qualify-mirror, and the report must be JSON
// whose template.sha256 is the hash step 4 just computed; a refused file is
// said and the question asked again, up to 3 more times. The checks run through
// the test entries review-verdict, mirror-verdict and ask-file, over a stub of
// the helper ataniq-canary fields that prints, for a file, the lines of a
// sidecar file FILE.fields (the lines the real helper prints for that JSON:
// "cases.0.id=...", "template.sha256=..."), or fails when there is none, as
// the helper does for a file that is not one JSON object.

const canaryTemplateHash = "5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a5a"

type canaryFilesKit struct{ dir string }

func newCanaryFilesKit(t *testing.T) canaryFilesKit {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "build", "canary")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	stub := "#!/bin/sh\n[ \"$1\" = fields ] && [ \"$2\" = --file ] || exit 97\n[ -f \"$3.fields\" ] || { echo 'not one JSON object' >&2; exit 1; }\ncat \"$3.fields\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ataniq-canary"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	return canaryFilesKit{dir: dir}
}

// file writes a file of the kit and, when fields is not empty, the lines the
// helper prints for it, and returns its path.
func (k canaryFilesKit) file(t *testing.T, name, text, fields string) string {
	t.Helper()
	p := canaryWrite(t, k.dir, name, text)
	if fields != "" {
		canaryWrite(t, k.dir, name+".fields", fields+"\n")
	}
	return p
}

func (k canaryFilesKit) run(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	w := startCanaryKit(t, canaryRunScript, k.dir, []string{"PATH=/usr/bin:/bin", "HOME=" + k.dir}, append([]string{"--self-test"}, args...)...)
	if _, err := io.WriteString(w.stdin, stdin); err != nil {
		t.Fatal(err)
	}
	_ = w.stdin.Close()
	if code := w.finish(); code != 0 {
		t.Fatalf("exit status %d:\n%s", code, w.text())
	}
	return w.text()
}

func TestCanaryRunKitReviewIsNotTheMirrorReport(t *testing.T) {
	k := newCanaryFilesKit(t)
	const mirrorFields = "cases.0.id=s16-fetch\nformat=1\nrole=coder\ntemplate.sha256=" + canaryTemplateHash
	const mirrorJSON = `{"format": 1, "template": {"sha256": "` + canaryTemplateHash + `"}, "role": "coder", "cases": [{"id": "s16-fetch"}]}` + "\n"
	for name, c := range map[string]struct {
		text, fields, want, why string
	}{
		"the report of qualify-mirror":               {mirrorJSON, mirrorFields, "bad", "qualify-mirror report"},
		"the report after blank lines and spaces":    {"\n\n   " + mirrorJSON, mirrorFields, "bad", "qualify-mirror report"},
		"a report of another template":               {mirrorJSON, "cases.0.id=x\ntemplate.sha256=" + strings.Repeat("c", 64), "bad", "qualify-mirror report"},
		"another JSON file":                          {"{\"a\": 1}\n", "a=1", "bad", "a JSON file"},
		"JSON the helper cannot read":                {"{ nope\n", "", "bad", "a JSON file"},
		"a review that holds the hash":               {"# Review of canary-intake.json\n\nsha256: " + canaryTemplateHash + "\n", "", "ok", "the template's hash"},
		"a review that holds the hash, no verdict":   {"the template " + canaryTemplateHash + " reads clean\n", "", "ok", "the template's hash"},
		"a review that holds the word verdict":       {"# Review\n\nVerdict: sound\n", "", "ok", "verdict"},
		"the word in capitals":                       {"VERDICT\n", "", "ok", "verdict"},
		"a markdown list that starts with a bracket": {"[x] read the binds\n[x] verdict: fine\n", "", "ok", "verdict"},
		"neither the hash nor the word":              {"Looks fine to me.\nThe verdicts are in the other file.\n", "", "bad", "neither"},
		"another hash":                               {"sha256: " + strings.Repeat("c", 64) + "\n", "", "bad", "neither"},
		"a file with a NUL byte":                     {"verdict\x00\n", "", "bad", "NUL"},
		"an empty file":                              {"", "", "bad", "empty"},
	} {
		t.Run(name, func(t *testing.T) {
			p := k.file(t, strings.ReplaceAll(name, " ", "-")+".md", c.text, c.fields)
			out := strings.TrimSpace(k.run(t, "", "review-verdict", p, canaryTemplateHash))
			if !strings.HasPrefix(out, c.want+" ") || !strings.Contains(out, c.why) {
				t.Errorf("the verdict is %q, want %s ... %s", out, c.want, c.why)
			}
			if strings.Contains(out, "\n") {
				t.Errorf("the verdict is more than a line: %q", out)
			}
		})
	}
}

func TestCanaryRunKitMirrorReportNamesTheTemplate(t *testing.T) {
	k := newCanaryFilesKit(t)
	const body = `{"format": 1}` + "\n"
	for name, c := range map[string]struct{ text, fields, want, why string }{
		"the report of this template":        {body, "format=1\nrole=coder\ntemplate.sha256=" + canaryTemplateHash, "ok", "template's hash"},
		"a report of another template":       {body, "template.sha256=" + strings.Repeat("c", 64), "bad", "another template"},
		"a report with no template":          {body, "format=1\nrole=coder", "bad", "no template.sha256"},
		"a report that names it elsewhere":   {body, "role=" + canaryTemplateHash, "bad", "no template.sha256"},
		"a longer key":                       {body, "template.sha256x=" + canaryTemplateHash, "bad", "no template.sha256"},
		"the review, which is not JSON":      {"# Review\n\nVerdict: sound\n" + canaryTemplateHash + "\n", "", "bad", "not one JSON object"},
		"an empty file":                      {"", "", "bad", "not one JSON object"},
		"an empty hash in the report":        {body, "template.sha256=", "bad", "no template.sha256"},
		"the hash with a different suffix":   {body, "template.sha256=" + canaryTemplateHash + "0", "bad", "another template"},
		"the hash with a different prefix":   {body, "template.sha256=0" + canaryTemplateHash, "bad", "another template"},
		"the hash as a value of another key": {body, "format=1\ntemplate.sha256x=1\nx.template.sha256=" + canaryTemplateHash, "bad", "no template.sha256"},
	} {
		t.Run(name, func(t *testing.T) {
			p := k.file(t, strings.ReplaceAll(name, " ", "-")+".json", c.text, c.fields)
			out := strings.TrimSpace(k.run(t, "", "mirror-verdict", p, canaryTemplateHash))
			if !strings.HasPrefix(out, c.want+" ") || !strings.Contains(out, c.why) {
				t.Errorf("the verdict is %q, want %s ... %s", out, c.want, c.why)
			}
		})
	}
}

// A refused file is said with its reason and the question is asked again, up
// to 3 more times; a typed path always wins over the hint; the step fails only
// after the last refusal, and nothing is recorded before a file is accepted.
func TestCanaryRunKitAsksAgainForAWrongFile(t *testing.T) {
	k := newCanaryFilesKit(t)
	report := k.file(t, "report.json", `{"format": 1}`+"\n", "cases.0.id=x\ntemplate.sha256="+canaryTemplateHash)
	review := k.file(t, "review.md", "Verdict: sound\n", "")
	other := k.file(t, "other-report.json", `{"format": 1}`+"\n", "template.sha256="+strings.Repeat("c", 64))
	missing := filepath.Join(k.dir, "missing.md")
	hints := canaryHintFile(t, "template-review="+report, "mirror-report="+other)
	line := func(ps ...string) string { return strings.Join(ps, "\n") + "\n" }

	for name, c := range map[string]struct {
		kind, key, stdin string
		want             string
		refused          int
		prompts          int
	}{
		"right at the first ask":                           {"review", "template-review", line(review), "accepted " + review, 0, 1},
		"a mirror report, then the review":                 {"review", "template-review", line(report, review), "accepted " + review, 1, 2},
		"the hint is the wrong file, Enter is not enough":  {"review", "template-review", line("", review), "accepted " + review, 1, 2},
		"a missing file, then an empty answer, then right": {"review", "other-key", line(missing, "", review), "accepted " + review, 2, 3},
		"right at the last ask":                            {"review", "template-review", line(report, missing, report, review), "accepted " + review, 3, 4},
		"wrong four times":                                 {"review", "template-review", line(report, missing, report, report, review), "failed", 4, 4},
		"the input ends":                                   {"review", "template-review", "", "failed", 4, 4},
		"the report of another template":                   {"mirror", "mirror-report", line("", report), "accepted " + report, 1, 2},
		"the review where the report is":                   {"mirror", "mirror-report", line(review, report), "accepted " + report, 1, 2},
	} {
		t.Run(name, func(t *testing.T) {
			out := k.run(t, c.stdin, "ask-file", hints, c.key, c.kind, canaryTemplateHash)
			out = strings.ReplaceAll(out, "   > ", "   > \n")
			trimmed := strings.TrimSpace(out)
			if got := trimmed[strings.LastIndex(trimmed, "\n")+1:]; got != c.want {
				t.Errorf("the answer is %q, want %q:\n%s", got, c.want, out)
			}
			if got := strings.Count(out, "   refused: "); got != c.refused {
				t.Errorf("%d refusals, want %d:\n%s", got, c.refused, out)
			}
			if got := strings.Count(out, "   ?? Lead: a path"); got != c.prompts {
				t.Errorf("%d questions, want %d:\n%s", got, c.prompts, out)
			}
			if got, want := strings.Count(out, "ask again ("), c.prompts-1; got != want {
				t.Errorf("the owner is told %d times that he is asked again, want %d:\n%s", got, want, out)
			}
			if c.want == "failed" && !strings.Contains(out, "FAILED at step 4: the file was refused 4 times") {
				t.Errorf("the step does not fail after four refusals:\n%s", out)
			}
			if c.want != "failed" && strings.Contains(out, "FAILED") {
				t.Errorf("an accepted file failed the step:\n%s", out)
			}
		})
	}

	// The reasons are the ones the checks give.
	out := k.run(t, line(report, missing, other), "ask-file", hints, "other-key", "review", canaryTemplateHash)
	for _, want := range []string{"this is a qualify-mirror report", "the file is not present: " + missing, "this is a qualify-mirror report"} {
		if !strings.Contains(out, "refused: "+want) {
			t.Errorf("the refusal %q is not said:\n%s", want, out)
		}
	}
	out = k.run(t, line(other), "ask-file", hints, "other-key", "mirror", canaryTemplateHash)
	if !strings.Contains(out, "it was made with another template") {
		t.Errorf("the report of another template is not refused for its template:\n%s", out)
	}

	// With the end of the input, the hint is not taken for an answer.
	out = k.run(t, "", "ask-file", hints, "template-review", "review", canaryTemplateHash)
	if strings.Contains(out, "the hint is taken") {
		t.Errorf("the end of the input took the hint:\n%s", out)
	}
}

// Step 2 and step 4 look at the file before they record it: the questions go
// through ask_file, the record (fact) follows, and the checks the old code made
// after the record are gone.
func TestCanaryRunKitStepsLookBeforeTheyRecord(t *testing.T) {
	funcs := canaryFuncs(t, canaryScriptText(t))
	for _, c := range []struct{ fn, ask, record string }{
		{"step_2", "ask_file ", `fact facts.txt "$ANSWER"`},
		{"step_4", "ask_file template-review", "fact template-records.txt"},
		{"step_4", "ask_file mirror-report", "fact template-records.txt"},
	} {
		body := funcs[c.fn]
		first, last := strings.Index(body, c.ask), strings.LastIndex(body, c.record)
		if first < 0 || last < first {
			t.Errorf("%s does not ask (%s) before it records (%s)", c.fn, c.ask, c.record)
		}
	}
	step4 := funcs["step_4"]
	if i, j := strings.Index(step4, "fact template-records.txt"), strings.Index(step4, "ask_file template-review"); j < 0 || i < j {
		t.Error("step 4 records a file before the review's question is asked")
	}
	if i, j := strings.Index(step4, "ask_file mirror-report"), strings.LastIndex(step4, "fact template-records.txt"); i < 0 || j < i {
		t.Error("step 4 records the report before its question is asked")
	}
	if strings.Contains(step4, "the review is present") || strings.Contains(step4, "the report is present") || strings.Contains(funcs["step_2"], "the file is present: $_what") {
		t.Error("a step checks the file's presence after it recorded the question's answer")
	}
	for _, c := range []struct{ fn, word string }{
		{"step_4", `review_verdict "$_th"`},
		{"step_4", `mirror_verdict "$_th"`},
		{"mirror_verdict", `"$_mvh" != "$2"`},
		{"mirror_verdict", `template.sha256=`},
		{"review_verdict", `cases.`},
		{"review_verdict", "verdict"},
	} {
		if !strings.Contains(funcs[c.fn], c.word) {
			t.Errorf("%s does not hold %s", c.fn, c.word)
		}
	}
	// The hash step 4 compares with is the one it computed and wrote.
	if !strings.Contains(step4, `_th=$(cat "$EV/template.sha256")`) {
		t.Error("step 4 does not take the hash it wrote")
	}
	// The dry run says what each path prompt looks at and that it asks again.
	for _, pass := range []string{"A", "B"} {
		out, _ := canaryDry(t, "--pass", pass, "--config", "/nonexistent/sample/canary.json")
		if n := strings.Count(out, "when the file is refused, say why and ask again, up to 3 more times; fail only after that"); n != 4 {
			t.Errorf("pass %s: the dry run says it %d times, want 4 (steps 2 and 4)", pass, n)
		}
		for _, want := range []string{"is a text or markdown file that is not a qualify-mirror report", "is one JSON object whose template.sha256 is the hash computed above"} {
			if !strings.Contains(out, want) {
				t.Errorf("pass %s: the dry run does not say %q", pass, want)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// canary-preflight.sh: the read-only look before the attended run. The tests
// build a fake checkout (a git repository that holds copies of the kit's
// scripts, with controlled commit times) and a prepared root, stub every
// program the preflight asks (systemctl, loginctl, qargi, the helper
// ataniq-canary and the binaries' --version), and run it with a PATH of its
// own. Nothing starts a unit, a daemon or a sandbox, and the run must leave the
// checkout and the root as they were.

const canaryPreflightScript = "qualification/canary/run/canary-preflight.sh"

// canaryPF is one such place.
type canaryPF struct {
	t                                                     *testing.T
	dir, repo, runDir, bin, root, tools, tmp, proc, calls string
	template                                              string
	gitPath                                               string
}

// canaryPFTools are the real programs the scripts use, linked into the tools
// folder, which is the whole PATH of a run: so that a program a test leaves
// out (qargi) is not found anywhere else.
var canaryPFTools = []string{"sh", "awk", "sed", "tr", "find", "stat", "sha256sum", "cat", "id", "mktemp", "rm", "head", "tail", "cmp", "grep", "git", "env", "date", "ls", "sort", "readlink", "mkdir", "cp", "wc", "sleep", "timeout"}

const canaryPFDate = "2021-01-01T00:00:00Z"

func newCanaryPF(t *testing.T) *canaryPF {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	dir := canaryOffTmp(t)
	p := &canaryPF{t: t, dir: dir, gitPath: gitPath}
	p.repo, p.root = filepath.Join(dir, "repo"), filepath.Join(dir, "root")
	p.runDir = filepath.Join(p.repo, "qualification/canary/run")
	p.bin = filepath.Join(p.repo, "build/canary")
	p.tools, p.tmp, p.proc, p.calls = filepath.Join(dir, "tools"), filepath.Join(dir, "tmp"), filepath.Join(dir, "proc"), filepath.Join(dir, "calls")
	p.template = filepath.Join(p.repo, "qualification/canary/templates/canary-intake.json")
	for _, d := range []string{p.tools, p.tmp, filepath.Join(dir, "home"), p.bin} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// The checkout: the scripts, a template, the code's paths, one commit.
	for _, name := range []string{"canary-lib.sh", "canary-preflight.sh", "canary-run.sh"} {
		data, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run", name))
		if err != nil {
			t.Fatal(err)
		}
		canaryWrite(t, p.runDir, name, string(data))
	}
	canaryWrite(t, p.repo, "qualification/canary/templates/canary-intake.json", "{\"template\": true}\n")
	for name, text := range map[string]string{"cmd/main.go": "package main\n", "internal/x.go": "package x\n", "go.mod": "module x\n", "go.sum": "\n", "third_party/t.txt": "t\n", "README.md": "readme\n", ".gitignore": "build/\n"} {
		canaryWrite(t, p.repo, name, text)
	}
	p.git("init", "-q", "-b", "develop")
	p.git("add", "-A")
	p.commit(canaryPFDate, "first")

	// The binaries, which record being called.
	p.stub(p.bin, "ataniq-canary", `case $1 in
  --version) cat '`+p.dir+`/version-ataniq-canary' 2>/dev/null || echo 'ataniq-canary (build tag ataniq_canary)' ;;
  fields) [ "$2" = --file ] && [ -f "$3.fields" ] || { echo 'not one JSON object' >&2; exit 1; }; cat "$3.fields" ;;
  profile-digest) cat '`+p.dir+`/digest' ;;
  check-inputs) [ -f '`+p.dir+`/check-inputs-fail' ] && { cat '`+p.dir+`/check-inputs-fail' >&2; exit 1; }; echo ok ;;
  show) cat '`+p.dir+`/show.out' ;;
  *) exit 97 ;;
esac
`)
	for _, name := range []string{"ataniqd", "ataniq-holder", "ataniq-canary-standin"} {
		p.stub(p.bin, name, `[ "$1" = --version ] || exit 97
cat '`+p.dir+`/version-`+name+`' 2>/dev/null || echo '`+name+` (build tag ataniq_canary)'
cat '`+p.dir+`/version-extra' 2>/dev/null || :
`)
	}
	for _, name := range []string{"ataniq-console", "ataniq-intake", "ataniq-probe"} {
		p.stub(p.bin, name, "exit 97\n")
	}
	canaryWrite(t, p.dir, "digest", strings.Repeat("ab", 32)+"\n")
	p.age(time.Now())

	// The prepared root.
	canaryWrite(t, p.root, ".canary-prepared", "ataniq-canary-prepare 1\nproject throwaway-one\n")
	canaryWrite(t, p.root, "config.json", "{}\n")
	canaryWrite(t, p.root, "config.json.fields", "holder.executable="+p.bin+"/ataniq-holder\nholder.profile_digest="+strings.Repeat("ab", 32)+"\nholder.sandbox_inputs="+p.root+"/conf/sandbox-inputs.json\nprovision.managed_settings="+p.root+"/conf/managed-settings.json\n")
	canaryWrite(t, p.root, "conf/sandbox-inputs.json", "{}\n")
	canaryWrite(t, p.root, "conf/sandbox-inputs.json.fields", "runtime=claude\nsources.managed_marker=guard-hook\n")
	canaryWrite(t, p.root, "conf/managed-settings.json", "{\"hooks\":{\"PreToolUse\":[{\"hooks\":[{\"type\":\"command\",\"command\":\"/ataniq/qargi/guard-hook\"}]}]}}\n")
	for _, d := range []string{"state/mirrors/throwaway-one.git", "runtime", "evidence", "mirror-qual"} {
		if err := os.MkdirAll(filepath.Join(p.root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sum := sha256.Sum256([]byte("{\"template\": true}\n"))
	canaryWrite(t, p.root, "mirror-qual/report.json", "{}\n")
	canaryWrite(t, p.root, "mirror-qual/report.json.fields", "format=1\ntemplate.sha256="+hex.EncodeToString(sum[:]))
	canaryWrite(t, p.root, "answers.hint", "mirror-report="+p.root+"/mirror-qual/report.json\n")
	canaryWrite(t, p.dir, "show.out", "holder_runtime_dir="+p.root+"/runtime/leads/throwaway-one-lead\nproject=throwaway-one\nagent_kind=stand-in\n")

	// The tools: real ones linked, the rest stubs that record their calls.
	for _, name := range canaryPFTools {
		if real, err := exec.LookPath(name); err == nil {
			if err := os.Symlink(real, filepath.Join(p.tools, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	p.stub(p.tools, "systemctl", `[ "$1" = --user ] && [ "$2" = list-units ] || exit 97
[ -f '`+p.dir+`/units-fail' ] && exit 1
cat '`+p.dir+`/units' 2>/dev/null || :
`)
	p.stub(p.tools, "loginctl", `[ -f '`+p.dir+`/loginctl-fail' ] && exit 1
echo Linger=no
`)
	p.stub(p.tools, "bwrap", "exit 97\n")
	p.stub(p.tools, "pasta", "exit 97\n")
	p.stub(p.tools, "ss", "exit 97\n")
	p.stub(p.tools, "qargi", `[ "$1" = local ] && [ "$2" = list ] || exit 97
[ -f '`+p.dir+`/qargi-fail' ] && exit 1
cat '`+p.dir+`/qargi-list'
`)
	canaryWrite(t, p.dir, "qargi-list", "ROLE SLUG AGE APPLIED MERGED RUN\n")
	canaryWrite(t, p.dir, "tiocsti", "0\n")
	canaryWrite(t, p.proc, "1/cmdline", "init\x00")
	return p
}

// stub writes an executable shell script that records its call.
func (p *canaryPF) stub(dir, name, body string) {
	p.t.Helper()
	text := "#!/bin/sh\necho \"" + name + " $*\" >>'" + p.calls + "'\n" + body
	path := canaryWrite(p.t, dir, name, text)
	if err := os.Chmod(path, 0o700); err != nil {
		p.t.Fatal(err)
	}
}

func (p *canaryPF) gitEnv(date string) []string {
	return []string{"PATH=" + filepath.Dir(p.gitPath) + ":/usr/bin:/bin", "HOME=" + p.dir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
}

func (p *canaryPF) gitDated(date string, args ...string) string {
	p.t.Helper()
	// No background maintenance: a commit starts "git maintenance run --auto
	// --detach", whose lock file may appear in the folder while a run is
	// compared with the folder as it was.
	cmd := exec.Command(p.gitPath, append([]string{"-C", p.repo, "-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)...)
	cmd.Env = p.gitEnv(date)
	out, err := cmd.CombinedOutput()
	if err != nil {
		p.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (p *canaryPF) git(args ...string) string { return p.gitDated(canaryPFDate, args...) }

// commit commits what is added, at the date.
func (p *canaryPF) commit(date, msg string) { p.gitDated(date, "commit", "-q", "-m", msg) }

// change writes a file of the checkout, adds it and commits it at the date.
func (p *canaryPF) change(file, text, date string) {
	p.t.Helper()
	canaryWrite(p.t, p.repo, file, text)
	p.git("add", file)
	p.commit(date, "change "+file)
}

// age sets the time of every binary of the kit.
func (p *canaryPF) age(when time.Time) {
	p.t.Helper()
	for _, name := range canaryRunBinaries {
		if err := os.Chtimes(filepath.Join(p.bin, name), when, when); err != nil {
			p.t.Fatal(err)
		}
	}
}

// run runs the preflight with the arguments after --root and returns the output
// and the exit status, and fails the test when it changed the checkout or the
// root or left a file in its temporary folder.
func (p *canaryPF) run(extra ...string) (string, int) {
	p.t.Helper()
	return p.runRaw(append([]string{"--root", p.root}, extra...)...)
}

func (p *canaryPF) runRaw(args ...string) (string, int) {
	p.t.Helper()
	snapshot := func() ([]string, []byte) {
		paths := append(canaryTree(p.t, p.repo), canaryTree(p.t, p.root)...)
		index, _ := os.ReadFile(filepath.Join(p.repo, ".git", "index"))
		return paths, index
	}
	beforePaths, beforeIndex := snapshot()
	cmd := exec.Command(filepath.Join(p.tools, "sh"), append([]string{filepath.Join(p.runDir, "canary-preflight.sh")}, args...)...)
	cmd.Dir = p.dir
	cmd.Env = []string{"PATH=" + p.tools, "HOME=" + filepath.Join(p.dir, "home"), "TMPDIR=" + p.tmp, "XDG_RUNTIME_DIR=" + filepath.Join(p.dir, "run"),
		"CANARY_PREFLIGHT_PROC=" + p.proc, "CANARY_PREFLIGHT_TIOCSTI=" + filepath.Join(p.dir, "tiocsti"), "CANARY_PREFLIGHT_ATANIQ=" + filepath.Join(p.dir, "no-ataniq")}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			p.t.Fatalf("the preflight did not run: %v", err)
		}
		code = exit.ExitCode()
	}
	afterPaths, afterIndex := snapshot()
	if !slices.Equal(beforePaths, afterPaths) || !bytes.Equal(beforeIndex, afterIndex) {
		p.t.Errorf("the preflight changed the checkout or the root:\nbefore %v\nafter  %v", beforePaths, afterPaths)
	}
	if left := canaryTree(p.t, p.tmp); len(left) != 1 {
		p.t.Errorf("the preflight left files in its temporary folder: %v", left)
	}
	return string(out), code
}

// callsMade is what the stubs recorded.
func (p *canaryPF) callsMade() []string {
	data, err := os.ReadFile(p.calls)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// The preflight of a good set-up says READY and what to type, with the real
// paths; it changes nothing and runs nothing but the read-only questions.
func TestCanaryRunKitPreflightReady(t *testing.T) {
	p := newCanaryPF(t)
	out, code := p.run()
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	body, _, _ := strings.Cut(out, "\nREADY\n")
	if !strings.HasSuffix(strings.TrimSpace(out), p.runDir+"/canary-run.sh --pass A --config "+p.root+"/config.json --evidence "+p.root+"/evidence") {
		t.Errorf("the output does not end with the run command:\n%s", out)
	}
	checks := 0
	for _, l := range strings.Split(strings.TrimSpace(body), "\n") {
		if l == "" {
			continue
		}
		if !strings.HasPrefix(l, "ok:   ") {
			t.Errorf("a line of a ready set-up is not ok: %q", l)
		}
		checks++
	}
	if checks != 16 {
		t.Errorf("%d checks, want 16:\n%s", checks, out)
	}
	for _, want := range []string{
		"ok:   the binaries are newer than the newest commit that touches the code (by file time: --version names no commit;",
		"after every canary-reset",
		"     " + p.bin + "/ataniqd init --state-dir \"" + p.root + "/state\"\n" +
			"     " + p.bin + "/ataniqd project-home --state-dir \"" + p.root + "/state\" --home self throwaway-one\n" +
			"     " + p.bin + "/ataniqd run --canary " + p.root + "/config.json",
		"     " + p.bin + "/ataniq-console --attach " + p.root + "/runtime/leads/throwaway-one-lead --ticket-file " + p.root + "/runtime/leads/throwaway-one-lead/ticket",
		"BEFORE you press Enter at step 10",
		"The run never starts the daemon or the console for you.",
		// The daemon stays in front of its terminal until Ctrl-C at step 26,
		// so the console and the second console of step 14 have terminals of
		// their own.
		"1. At step 10, in a second terminal (T2), the daemon",
		"marker file (canary-root.json) that the daemon needs",
		"It stays in front of that terminal until you stop it with",
		"2. At step 14 (and again in the second epoch), in a third terminal (T3), the console, started",
		"BEFORE you press Enter at step 14: it waits for a ticket, and the run mints one only after",
		"your Enter (a ticket lives 30 seconds).",
		"fourth terminal (T4), in the first epoch, where the integrate command of step 20 goes too:",
		"3. In this terminal (T1), the run",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not hold %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "NOT READY") || strings.Contains(out, "FIX:") {
		t.Errorf("a ready set-up has a fix:\n%s", out)
	}
	// Only read-only questions were asked: of systemd the list of units, of
	// logind one user's lingering, of qargi the list; of the binaries their
	// version and, of the helper, what it prints.
	for _, c := range p.callsMade() {
		switch {
		case c == "systemctl --user list-units --all --no-legend --plain ataniq-*", c == "qargi local list", strings.HasPrefix(c, "loginctl show-user "):
		case strings.HasSuffix(c, " --version") && slices.Contains(canaryRunBinaries, strings.TrimSuffix(c, " --version")):
		case strings.HasPrefix(c, "ataniq-canary fields --file "), strings.HasPrefix(c, "ataniq-canary profile-digest --config "), strings.HasPrefix(c, "ataniq-canary show --config "), strings.HasPrefix(c, "ataniq-canary check-inputs --config "):
		default:
			t.Errorf("the preflight made the call %q", c)
		}
	}
}

// The command line: a root, outside /tmp and the checkout, is needed; nothing
// else is taken; it never asks for a terminal and needs no word.
func TestCanaryRunKitPreflightArguments(t *testing.T) {
	p := newCanaryPF(t)
	for name, c := range map[string]struct {
		args []string
		code int
		want string
	}{
		"no root":                {nil, 2, "usage: canary-preflight.sh --root DIR"},
		"an unknown flag":        {[]string{"--root", p.root, "--yes"}, 2, "usage: canary-preflight.sh"},
		"a flag without a value": {[]string{"--root"}, 2, "usage: canary-preflight.sh"},
		"a relative root":        {[]string{"--root", "root"}, 1, "the root is not an absolute path"},
		"a root in /tmp":         {[]string{"--root", "/tmp/canary-preflight-test"}, 1, "the root is in /tmp"},
		"a root in the checkout": {[]string{"--root", p.repo + "/root"}, 1, "lie one in the other"},
		"a relative bin":         {[]string{"--root", p.root, "--bin", "build/canary"}, 1, "--bin is not an absolute path"},
		"a relative evidence":    {[]string{"--root", p.root, "--evidence", "evidence"}, 1, "--evidence is not an absolute path"},
		"a root with a bad char": {[]string{"--root", p.root + "/a b"}, 1, "character outside"},
		"a root that is a link":  {[]string{"--root", p.dir + "/link"}, 1, "symbolic link"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.Symlink(p.root, filepath.Join(p.dir, "link")); err != nil && !os.IsExist(err) {
				t.Fatal(err)
			}
			out, code := p.runRaw(c.args...)
			if code != c.code || !strings.Contains(out, c.want) || strings.Contains(out, "ok:") {
				t.Errorf("exit status %d:\n%s", code, out)
			}
		})
	}
	// It never asks a question of the terminal: no read of an answer, no test
	// of standard input, no word of the run in it.
	data, err := os.ReadFile(filepath.Join(root(t), canaryPreflightScript))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"ANSWER", "[ -t 0 ]", "read -r ANSWER", "affirm", "wait_key"} {
		if strings.Contains(string(data), bad) {
			t.Errorf("the preflight holds %q", bad)
		}
	}
}

// Every check that finds something says FIX, with the exact command, and the
// end says how many.
func TestCanaryRunKitPreflightFixes(t *testing.T) {
	type pf = canaryPF
	for name, c := range map[string]struct {
		mutate func(t *testing.T, p *pf)
		extra  []string
		fixes  int
		want   []string
	}{
		"the root is not prepared": {
			func(t *testing.T, p *pf) { os.Remove(filepath.Join(p.root, ".canary-prepared")) }, nil, 1,
			[]string{"FIX:  the root is not prepared", "and it holds files, so canary-prepare.sh refuses it: %ROOT% -- type: sh " + "%RUN%/canary-prepare.sh --root <a new or empty folder>", "use a fresh --root", "skip: the holder and the profile digest pin", "skip: state/ and runtime/"},
		},
		"the root has another mark": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.root, ".canary-prepared", "something else\n") }, nil, 1,
			[]string{"FIX:  the root is not prepared"},
		},
		"a binary is not built": {
			func(t *testing.T, p *pf) { os.Remove(filepath.Join(p.bin, "ataniq-probe")) }, nil, 1,
			[]string{"FIX:  not built in %BIN%: ataniq-probe -- type: make -C %REPO% canary-build", "skip: the holder and the profile digest pin", "skip: the report of qualify-mirror"},
		},
		"a binary is a link": {
			func(t *testing.T, p *pf) {
				os.Remove(filepath.Join(p.bin, "ataniq-intake"))
				os.Symlink(filepath.Join(p.bin, "ataniq-probe"), filepath.Join(p.bin, "ataniq-intake"))
			}, nil, 1, []string{"not built in %BIN%: ataniq-intake"},
		},
		"a tagged binary does not name the tag": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.dir, "version-ataniq-holder", "ataniq-holder dev\n") }, nil, 1,
			[]string{"FIX:  ataniq-holder --version does not name the build tag ataniq_canary -- type: make -C %REPO% canary-build"},
		},
		"the binary names another commit": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.dir, "version-extra", "commit "+strings.Repeat("a", 40)+"\n")
			}, nil, 1,
			[]string{"FIX:  ataniqd was built from " + strings.Repeat("a", 40) + ", not from the HEAD commit %HEAD% (by its --version)"},
		},
		"the checkout is not at develop": {
			func(t *testing.T, p *pf) {
				p.git("checkout", "-q", "-b", "other")
				p.change("README.md", "other\n", "2021-06-01T00:00:00Z")
			}, nil, 1,
			[]string{"FIX:  the checkout's commit is not the commit of develop (step 3 refuses it) -- type: git -C %REPO% checkout develop"},
		},
		"there is no develop": {
			func(t *testing.T, p *pf) { p.git("branch", "-m", "develop", "main") }, nil, 1,
			[]string{"FIX:  the branch develop cannot be read in %REPO%"},
		},
		"a tracked file has a change": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.repo, "README.md", "changed\n") }, nil, 1,
			[]string{"FIX:  a tracked file of the checkout has a change (step 3 refuses it):", "-- type: git -C %REPO% status"},
		},
		"the config names no holder": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.root, "config.json.fields", "holder.profile_digest=x\n") }, nil, 1,
			[]string{"FIX:  config.json names no holder (holder.executable)"},
		},
		"the holder is not there": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.root, "config.json.fields", "holder.executable="+p.dir+"/none/ataniq-holder\n")
			}, nil, 1, []string{"FIX:  the holder the config names is not an executable file: %DIR%/none/ataniq-holder"},
		},
		"the holder is another one": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.root, "config.json.fields", "holder.executable="+p.bin+"/ataniqd\n")
			}, nil, 1, []string{"FIX:  the holder the config names is not the one of %BIN% (step 3 refuses it): %BIN%/ataniqd"},
		},
		"the pin is stale": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.dir, "digest", strings.Repeat("cd", 32)+"\n") }, nil, 1,
			[]string{"FIX:  the profile digest pinned in config.json (" + strings.Repeat("ab", 32) + ") is not the one ataniq-canary computes now (" + strings.Repeat("cd", 32) + ")",
				"-- type: sed -i 's/\"profile_digest\": \"[0-9a-f]*\"/\"profile_digest\": \"" + strings.Repeat("cd", 32) + "\"/' %ROOT%/config.json && %BIN%/ataniq-canary check-inputs --config %ROOT%/config.json --inputs %ROOT%/conf/sandbox-inputs.json"},
		},
		"check-inputs refuses the inputs": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.dir, "check-inputs-fail", "ataniq-canary: sources.managed_settings: does not match\x01 the config\nsecond line\n")
			}, nil, 1,
			[]string{"FIX:  ataniq-canary check-inputs refuses the sandbox inputs (ataniq-canary: sources.managed_settings: does not match the config): %ROOT%/conf/sandbox-inputs.json -- type: sh %RUN%/canary-prepare.sh --root <a new or empty folder>", "use a fresh --root"},
		},
		"the inputs have no marker": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.root, "conf/sandbox-inputs.json.fields", "runtime=claude\n")
			}, nil, 1,
			[]string{"FIX:  the sandbox inputs have no sources.managed_marker, which the holder needs to start claude (validate-config refuses it at step 11): %ROOT%/conf/sandbox-inputs.json -- type: sh %RUN%/canary-prepare.sh --root <a new or empty folder>", "use a fresh --root"},
		},
		"the managed settings lack the marker": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.root, "conf/managed-settings.json", "{\"permissions\":{\"defaultMode\":\"default\"}}\n")
			}, nil, 1,
			[]string{"FIX:  the managed-settings file %ROOT%/conf/managed-settings.json does not hold the marker text guard-hook (assertion 8 searches it) -- type: sh %RUN%/canary-prepare.sh --root <a new or empty folder>"},
		},
		"the managed settings are gone": {
			func(t *testing.T, p *pf) { os.Remove(filepath.Join(p.root, "conf/managed-settings.json")) }, nil, 1,
			[]string{"FIX:  the managed-settings file %ROOT%/conf/managed-settings.json does not hold the marker text guard-hook"},
		},
		"the inputs are checked only after the pin": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.dir, "digest", strings.Repeat("cd", 32)+"\n")
				canaryWrite(t, p.dir, "check-inputs-fail", "refused\n")
			}, nil, 1,
			[]string{"FIX:  the profile digest pinned in config.json", "skip: the sandbox inputs (needs the holder and the profile digest pin)"},
		},
		"the config cannot be shown": {
			func(t *testing.T, p *pf) { os.Remove(filepath.Join(p.dir, "show.out")) }, nil, 1,
			[]string{"FIX:  ataniq-canary cannot read the config -- type: %BIN%/ataniq-canary show --config %ROOT%/config.json"},
		},
		"state holds an earlier attempt": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.root, "state/leads/throwaway-one-lead/x", "x") }, nil, 1,
			[]string{"FIX:  state/ or runtime/ holds an earlier attempt (%ROOT%/state/leads)", "-- type: sh %RUN%/canary-reset.sh --root %ROOT%   (run canary-reset.sh)"},
		},
		"runtime holds an earlier attempt": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.root, "runtime/daemon.sock", "x") }, nil, 1,
			[]string{"FIX:  state/ or runtime/ holds an earlier attempt (%ROOT%/runtime/daemon.sock)", "run canary-reset.sh"},
		},
		"the mirror is gone": {
			func(t *testing.T, p *pf) { os.RemoveAll(filepath.Join(p.root, "state/mirrors")) }, nil, 1,
			[]string{"FIX:  the mirror is missing: %ROOT%/state/mirrors/throwaway-one.git", "run canary-reset.sh"},
		},
		"state is not a folder": {
			func(t *testing.T, p *pf) {
				os.RemoveAll(filepath.Join(p.root, "state"))
				os.Symlink(p.dir, filepath.Join(p.root, "state"))
			}, nil, 1, []string{"FIX:  %ROOT%/state is not a folder of the root -- type: sh %RUN%/canary-reset.sh --root %ROOT%"},
		},
		"a unit is loaded": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.dir, "units", "ataniq-lead-throwaway-one.service loaded active running x\nataniq-old.slice loaded active active x\n")
			}, nil, 1,
			[]string{"FIX:  an ataniq-* unit is loaded: ataniq-lead-throwaway-one.service  -- type: systemctl --user stop ataniq-lead-throwaway-one.service "},
		},
		"the units cannot be listed": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.dir, "units-fail", "x") }, nil, 1,
			[]string{"FIX:  systemctl --user cannot list the units", "-- type: systemctl --user list-units --all 'ataniq-*'"},
		},
		"a daemon runs": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.proc, "7/cmdline", "/x/build/canary/ataniqd\x00run\x00--canary\x00c.json\x00")
			}, nil, 1,
			[]string{"FIX:  an ataniqd or ataniq-holder runs (process 7)", "-- type: stop it with Ctrl-C in its terminal, or: kill -INT 7"},
		},
		"a holder runs": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.proc, "8/cmdline", "ataniq-holder\x00--x\x00") }, nil, 1,
			[]string{"FIX:  an ataniqd or ataniq-holder runs (process 8)"},
		},
		"the processes cannot be listed": {
			func(t *testing.T, p *pf) { os.RemoveAll(p.proc); os.MkdirAll(p.proc, 0o700) }, nil, 1,
			[]string{"FIX:  " + "%DIR%/proc cannot be read, so the processes cannot be listed"},
		},
		"a Qargi run of this repository is open": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.dir, "runs/r1/repo.path", p.repo+"\n")
				canaryWrite(t, p.dir, "qargi-list", "ROLE SLUG AGE APPLIED MERGED RUN\nbuild feat 1h no no "+p.dir+"/runs/r1\n")
			}, nil, 1,
			[]string{"FIX:  a Qargi run of this repository is open: %DIR%/runs/r1 ", "-- type: qargi local list   (verify and close the run)"},
		},
		"/ataniq exists": {
			func(t *testing.T, p *pf) { os.Mkdir(filepath.Join(p.dir, "no-ataniq"), 0o700) }, nil, 1,
			[]string{"FIX:  %DIR%/no-ataniq exists on the host (the template's note: it must not) -- type: ls -la %DIR%/no-ataniq"},
		},
		"there is no report of qualify-mirror": {
			func(t *testing.T, p *pf) { os.Remove(filepath.Join(p.root, "mirror-qual/report.json")) }, nil, 1,
			[]string{"FIX:  the report of qualify-mirror is not there: %ROOT%/mirror-qual/report.json", "build/qualify-mirror --config %ROOT%/mirror-qual/config.json --out %ROOT%/mirror-qual/report.json   (in %REPO%)"},
		},
		"the report does not parse": {
			func(t *testing.T, p *pf) { os.Remove(filepath.Join(p.root, "mirror-qual/report.json.fields")) }, nil, 1,
			[]string{"FIX:  the report of qualify-mirror does not parse as one JSON object"},
		},
		"the report is of another template": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.root, "mirror-qual/report.json.fields", "template.sha256="+strings.Repeat("c", 64)+"\n")
			}, nil, 1,
			[]string{"FIX:  the report is of another template (" + strings.Repeat("c", 64) + ", the template is "},
		},
		"the report has no template": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.root, "mirror-qual/report.json.fields", "format=1\n") }, nil, 1,
			[]string{"FIX:  the report has no template.sha256"},
		},
		"the report is elsewhere, by the hint": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.root, "answers.hint", "mirror-report="+p.dir+"/elsewhere.json\n")
			}, nil, 1, []string{"FIX:  the report of qualify-mirror is not there: %DIR%/elsewhere.json"},
		},
		"the report is elsewhere, by the argument": {
			func(t *testing.T, p *pf) {}, []string{"--mirror-report", "%DIR%/given.json"}, 1,
			[]string{"FIX:  the report of qualify-mirror is not there: %DIR%/given.json"},
		},
		"the sysctl is 1": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.dir, "tiocsti", "1\n") }, nil, 1,
			[]string{"FIX:  the sysctl dev.tty.legacy_tiocsti is \"1\", not 0 (step 3 refuses it) -- type: sudo sysctl dev.tty.legacy_tiocsti=0"},
		},
		"the sysctl is empty": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.dir, "tiocsti", "") }, nil, 1,
			[]string{"FIX:  the sysctl dev.tty.legacy_tiocsti is \"unreadable\""},
		},
		"lingering cannot be read": {
			func(t *testing.T, p *pf) { canaryWrite(t, p.dir, "loginctl-fail", "x") }, nil, 1,
			[]string{"FIX:  lingering cannot be read with loginctl (step 3 needs it) -- type: loginctl show-user "},
		},
		"pasta is not on PATH": {
			func(t *testing.T, p *pf) { os.Remove(filepath.Join(p.tools, "pasta")) }, nil, 1,
			[]string{"FIX:  not on PATH (step 3 runs them): pasta -- type: put them on PATH"},
		},
		"timeout is not on PATH": {
			func(t *testing.T, p *pf) { os.Remove(filepath.Join(p.tools, "timeout")) }, nil, 1,
			[]string{"FIX:  timeout is not on PATH (the run cuts off each ask of the daemon's status with it) -- type: install coreutils, or put timeout on PATH"},
		},
		"the evidence folder is missing": {
			func(t *testing.T, p *pf) { os.Remove(filepath.Join(p.root, "evidence")) }, nil, 1,
			[]string{"FIX:  the evidence folder is not a writable folder: %ROOT%/evidence -- type: mkdir -p -m 0700 %ROOT%/evidence"},
		},
		"the evidence folder is given and missing": {
			func(t *testing.T, p *pf) {}, []string{"--evidence", "%DIR%/ev"}, 1,
			[]string{"FIX:  the evidence folder is not a writable folder: %DIR%/ev"},
		},
		"three things are wrong": {
			func(t *testing.T, p *pf) {
				canaryWrite(t, p.dir, "tiocsti", "1\n")
				os.Mkdir(filepath.Join(p.dir, "no-ataniq"), 0o700)
				canaryWrite(t, p.dir, "units", "ataniq-lead-throwaway-one.service loaded active running x\n")
			}, nil, 3, []string{"NOT READY (3 to fix)"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			p := newCanaryPF(t)
			c.mutate(t, p)
			expand := func(s string) string {
				r := strings.NewReplacer("%RUN%", p.runDir, "%ROOT%", p.root, "%BIN%", p.bin, "%REPO%", p.repo, "%DIR%", p.dir, "%HEAD%", p.git("rev-parse", "HEAD"))
				return r.Replace(s)
			}
			extra := slices.Clone(c.extra)
			for i := range extra {
				extra[i] = expand(extra[i])
			}
			out, code := p.run(extra...)
			if code != 1 {
				t.Errorf("exit status %d, want 1:\n%s", code, out)
			}
			for _, want := range c.want {
				if !strings.Contains(out, expand(want)) {
					t.Errorf("the output does not hold %q:\n%s", expand(want), out)
				}
			}
			if got, want := strings.Count(out, "FIX:  "), c.fixes; got != want {
				t.Errorf("%d fixes, want %d:\n%s", got, want, out)
			}
			if !strings.HasSuffix(strings.TrimSpace(out), fmt.Sprintf("NOT READY (%d to fix)", c.fixes)) {
				t.Errorf("the output does not end with NOT READY (%d to fix):\n%s", c.fixes, out)
			}
			if strings.Contains(out, "\nREADY\n") || strings.Contains(out, "What you type") {
				t.Errorf("a set-up with a fix says READY:\n%s", out)
			}
			// No FIX line is more than one line, and every one has a command.
			for _, l := range strings.Split(out, "\n") {
				if strings.HasPrefix(l, "FIX:  ") && !strings.Contains(l, " -- type: ") {
					t.Errorf("a FIX line without its command: %q", l)
				}
			}
		})
	}
}

// The guard hook's marker is a claude matter: for another runtime the inputs
// need none, and the preflight asks the helper's check only.
func TestCanaryRunKitPreflightMarkerIsForClaude(t *testing.T) {
	p := newCanaryPF(t)
	canaryWrite(t, p.root, "conf/sandbox-inputs.json.fields", "runtime=other\n")
	out, code := p.run()
	if code != 0 || !strings.Contains(out, "ok:   ataniq-canary check-inputs accepts the sandbox inputs\n") {
		t.Errorf("exit status %d:\n%s", code, out)
	}
	// And with claude, the ok line names the marker the settings hold.
	p = newCanaryPF(t)
	out, code = p.run()
	if code != 0 || !strings.Contains(out, "ok:   ataniq-canary check-inputs accepts the sandbox inputs, which name the marker guard-hook that the managed settings hold\n") {
		t.Errorf("exit status %d:\n%s", code, out)
	}
}

// A root that is not prepared is fixed by preparing it only when prepare takes
// it: a new or empty folder. A folder that holds anything is refused by
// canary-prepare.sh, so the fix names a fresh root instead.
func TestCanaryRunKitPreflightNotPreparedFix(t *testing.T) {
	for name, c := range map[string]struct {
		make func(t *testing.T, dir string) string
		want string
	}{
		"an empty folder": {func(t *testing.T, dir string) string {
			r := filepath.Join(dir, "empty-root")
			if err := os.Mkdir(r, 0o700); err != nil {
				t.Fatal(err)
			}
			return r
		}, "-- type: sh %RUN%/canary-prepare.sh --root %ROOT%\n"},
		"a folder that is not there": {func(t *testing.T, dir string) string { return filepath.Join(dir, "no-root") }, "-- type: sh %RUN%/canary-prepare.sh --root %ROOT%\n"},
		"a folder with a file": {func(t *testing.T, dir string) string {
			r := filepath.Join(dir, "full-root")
			canaryWrite(t, r, "something", "x")
			return r
		}, "-- type: sh %RUN%/canary-prepare.sh --root <a new or empty folder>   (use a fresh --root; remove %ROOT% yourself if you do not need it)\n"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newCanaryPF(t)
			r := c.make(t, p.dir)
			out, code := p.runRaw("--root", r)
			want := strings.NewReplacer("%RUN%", p.runDir, "%ROOT%", r).Replace(c.want)
			if code != 1 || !strings.Contains(out, "FIX:  the root is not prepared") || !strings.Contains(out, want) {
				t.Errorf("exit status %d, want the fix %q:\n%s", code, want, out)
			}
		})
	}
}

// The binaries must be built from the checkout's HEAD: by the commit their
// --version names when it names one, else by the time of their files against the
// newest commit that touches the code (not a commit that touches only the docs),
// and the output says which.
func TestCanaryRunKitPreflightBuiltFromHead(t *testing.T) {
	year := func(y int) time.Time { return time.Date(y, 6, 1, 0, 0, 0, 0, time.UTC) }
	for name, c := range map[string]struct {
		setup func(p *canaryPF)
		want  string // the ok line, or the fix line, up to its command
		ready bool
	}{
		"newer than the code, by file time": {func(p *canaryPF) { p.age(year(2022)) }, "ok:   the binaries are newer than the newest commit that touches the code (by file time: --version names no commit;", true},
		"older than a commit of the code": {func(p *canaryPF) {
			p.change("cmd/main.go", "package main\n// newer\n", "2023-01-01T00:00:00Z")
			p.age(year(2022))
		}, "FIX:  ataniqd is older than the newest commit that touches the code (by file time: --version names no commit) -- type: make -C", false},
		"older than a commit of go.mod": {func(p *canaryPF) {
			p.change("go.mod", "module y\n", "2023-01-01T00:00:00Z")
			p.age(year(2022))
		}, "is older than the newest commit that touches the code", false},
		"older than a commit of a vendored module": {func(p *canaryPF) {
			p.change("third_party/t.txt", "u\n", "2023-01-01T00:00:00Z")
			p.age(year(2022))
		}, "is older than the newest commit that touches the code", false},
		"a commit of the docs only does not count": {func(p *canaryPF) {
			p.change("README.md", "docs\n", "2023-01-01T00:00:00Z")
			p.age(year(2022))
		}, "ok:   the binaries are newer than the newest commit that touches the code (by file time", true},
		"one binary is older": {func(p *canaryPF) {
			p.age(year(2022))
			old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
			os.Chtimes(filepath.Join(p.bin, "ataniq-console"), old, old)
		}, "FIX:  ataniq-console is older than the newest commit that touches the code", false},
		"the version names the HEAD commit": {func(p *canaryPF) {
			canaryWrite(p.t, p.dir, "version-extra", "commit "+p.git("rev-parse", "HEAD")+"\n")
			p.age(year(2010))
		}, "ok:   the binaries were built from the HEAD commit ", true},
		"the version names an older commit": {func(p *canaryPF) {
			first := p.git("rev-parse", "HEAD")
			p.change("README.md", "docs\n", "2021-02-01T00:00:00Z")
			canaryWrite(p.t, p.dir, "version-extra", "commit "+first+"\n")
		}, "FIX:  ataniqd was built from ", false},
		"a short hash is not a commit": {func(p *canaryPF) {
			canaryWrite(p.t, p.dir, "version-extra", "commit "+p.git("rev-parse", "--short", "HEAD")+"\n")
		}, "ok:   the binaries are newer than the newest commit that touches the code (by file time", true},
	} {
		t.Run(name, func(t *testing.T) {
			p := newCanaryPF(t)
			c.setup(p)
			out, code := p.run()
			if !strings.Contains(out, c.want) {
				t.Errorf("the output does not hold %q:\n%s", c.want, out)
			}
			if (code == 0) != c.ready {
				t.Errorf("exit status %d, want ready %v:\n%s", code, c.ready, out)
			}
		})
	}
}

// What qargi says about the open runs is read as the run reads it: with the
// run script's own helpers. An open run of this repository is a fix; every
// other answer leaves the run to ask the lead or has nothing open.
func TestCanaryRunKitPreflightOpenRuns(t *testing.T) {
	header := "ROLE SLUG AGE APPLIED MERGED RUN\n"
	for name, c := range map[string]struct {
		setup func(p *canaryPF)
		want  string
	}{
		"no run":                  {func(p *canaryPF) {}, "ok:   no Qargi run of this repository is open"},
		"qargi is not there":      {func(p *canaryPF) { os.Remove(filepath.Join(p.tools, "qargi")) }, "ok:   qargi is not on PATH: the run asks the lead"},
		"qargi fails":             {func(p *canaryPF) { canaryWrite(p.t, p.dir, "qargi-fail", "x") }, "ok:   qargi local list failed: the run asks the lead"},
		"a list it cannot read":   {func(p *canaryPF) { canaryWrite(p.t, p.dir, "qargi-list", "NOT THE HEADER\n") }, "ok:   the list of qargi local list cannot be read as the run reads it"},
		"a line it does not know": {func(p *canaryPF) { canaryWrite(p.t, p.dir, "qargi-list", header+"something else entirely\n") }, "ok:   the list of qargi local list cannot be read as the run reads it"},
		"a run applied and merged": {func(p *canaryPF) {
			canaryWrite(p.t, p.dir, "runs/r1/repo.path", p.repo+"\n")
			canaryWrite(p.t, p.dir, "qargi-list", header+"build feat 1h yes yes "+p.dir+"/runs/r1\n")
		}, "ok:   no Qargi run of this repository is open"},
		"an open run of another repository": {func(p *canaryPF) {
			canaryWrite(p.t, p.dir, "runs/r1/repo.path", p.dir+"/other\n")
			canaryWrite(p.t, p.dir, "qargi-list", header+"build feat 1h no no "+p.dir+"/runs/r1\n")
		}, "ok:   no Qargi run of this repository is open"},
		"an open run without a repo.path": {func(p *canaryPF) {
			canaryWrite(p.t, p.dir, "qargi-list", header+"build feat 1h no no "+p.dir+"/runs/r2\n")
			os.MkdirAll(filepath.Join(p.dir, "runs/r2"), 0o700)
		}, "ok:   no open run is known, but one has no readable repo.path ("},
		"a run whose folder is gone": {func(p *canaryPF) {
			canaryWrite(p.t, p.dir, "qargi-list", header+"build feat 1h no no "+p.dir+"/runs/gone  directory gone\n")
		}, "ok:   no Qargi run of this repository is open"},
	} {
		t.Run(name, func(t *testing.T) {
			p := newCanaryPF(t)
			c.setup(p)
			out, code := p.run()
			if code != 0 || !strings.Contains(out, c.want) {
				t.Errorf("exit status %d, want the line %q:\n%s", code, c.want, out)
			}
		})
	}
	// A run of this repository from a linked work tree is seen from the main one.
	p := newCanaryPF(t)
	p.git("worktree", "add", "-q", "-b", "wt", filepath.Join(p.dir, "wt"))
	canaryWrite(t, p.dir, "runs/r1/repo.path", filepath.Join(p.dir, "wt")+"\n")
	canaryWrite(t, p.dir, "qargi-list", header+"build feat 1h no no "+p.dir+"/runs/r1\n")
	out, code := p.run()
	if code != 1 || !strings.Contains(out, "FIX:  a Qargi run of this repository is open: "+p.dir+"/runs/r1") {
		t.Errorf("exit status %d:\n%s", code, out)
	}
}

// Evidence folder: given, and not writable.
func TestCanaryRunKitPreflightEvidenceFolder(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes anywhere")
	}
	p := newCanaryPF(t)
	ev := filepath.Join(p.dir, "ev")
	if err := os.Mkdir(ev, 0o500); err != nil {
		t.Fatal(err)
	}
	out, code := p.run("--evidence", ev)
	if code != 1 || !strings.Contains(out, "FIX:  the evidence folder is not a writable folder: "+ev) {
		t.Errorf("exit status %d:\n%s", code, out)
	}
	if err := os.Chmod(ev, 0o700); err != nil {
		t.Fatal(err)
	}
	out, code = p.run("--evidence", ev)
	if code != 0 || !strings.Contains(out, "--evidence "+ev) {
		t.Errorf("exit status %d:\n%s", code, out)
	}
}

// ---------------------------------------------------------------------------
// The owner's page. canary-prepare.sh writes ROOT/CHECKLIST.txt from the
// template checklist.txt.in of the kit, with the real paths: what to start and
// where, and what each prompt of pass A expects.

const canaryChecklistTemplate = "qualification/canary/run/checklist.txt.in"

var canaryPlaceholder = regexp.MustCompile(`@([A-Z_]+)@`)

func TestCanaryRunKitChecklistTemplate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), canaryChecklistTemplate))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	// One page, and no line wider than a terminal.
	if len(lines) > 60 {
		t.Errorf("the checklist has %d lines, want one page (60 at most)", len(lines))
	}
	for i, l := range lines {
		if len([]rune(l)) > 80 {
			t.Errorf("line %d is %d columns wide: %q", i+1, len([]rune(l)), l)
		}
	}
	// No private name: no home path of anyone, no home folder of this machine.
	for _, bad := range []string{"/home/", "/Users/", "/root/", "$HOME", "~/", ".ssh", ".claude", ".codex"} {
		if strings.Contains(text, bad) {
			t.Errorf("the template holds %q", bad)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && len(home) > 1 && strings.Contains(text, home) {
		t.Errorf("the template holds this machine's home folder %q", home)
	}
	// The placeholders are the ones prepare fills, and each is used.
	used := map[string]bool{}
	for _, m := range canaryPlaceholder.FindAllStringSubmatch(text, -1) {
		used[m[1]] = true
	}
	for _, name := range []string{"ROOT", "REPO", "BIN", "CONFIG", "EVIDENCE", "HINTS", "HOLDER_RUN", "RUN", "PREFLIGHT", "RESET"} {
		if !used[name] {
			t.Errorf("the template does not use @%s@", name)
		}
		delete(used, name)
	}
	for name := range used {
		t.Errorf("the template holds @%s@, which prepare does not fill", name)
	}
	// The daemon runs in the foreground of its terminal until Ctrl-C at step
	// 26: the page names a terminal for each of the four things the owner runs,
	// and sends neither the console nor the intake to the daemon's.
	for _, want := range []string{
		"17   In T3 give the session pane the focus first (Enter on it; a paste into\n      an unfocused console is dropped)",
		"Four terminals: T1 runs\nthe script, T2 the daemon (it stays in front until Ctrl-C at step 26), T3 the\nconsole, T4 the second console of step 14 and the integrate command of 20.",
		"14   T3: start the console BEFORE you press Enter at step 14; it waits for\n      a ticket",
		"--ticket-file @HOLDER_RUN@/ticket",
		"it must NOT\n      attach and only says \"waiting for a ticket\"",
		"2nd  Second epoch: first close the console in T3 (Ctrl+\\ then q)",
		"20   T4: the integrate command",
		"(in T2)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the template does not say %q", want)
		}
	}
	if strings.Contains(text, "T2 is yours") || strings.Contains(text, "T2: ") {
		t.Error("the template sends the owner to the daemon's terminal for something else")
	}
	// What the owner is told about the two orders that failed an attempt.
	for _, want := range []string{
		"Start the daemon in T2 BEFORE pressing Enter at step 10, wait for the\n      listening line",
		"At step 26 stop the daemon with Ctrl-C first",
		"Enter alone\nis refused",
		"answers nothing for you and starts nothing",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the template does not say %q", want)
		}
	}
}

func TestCanaryRunKitPrepareWritesTheChecklist(t *testing.T) {
	e := canaryStubKit(t)
	r := filepath.Join(e.dir, "set-up")
	out, code := e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r)...)
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	path := filepath.Join(r, "CHECKLIST.txt")
	if got := canaryMode(t, path); got != 0o600 {
		t.Errorf("CHECKLIST.txt has mode %o, want 600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	tmpl, err := os.ReadFile(filepath.Join(root(t), canaryChecklistTemplate))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(text, "\n") != strings.Count(string(tmpl), "\n") {
		t.Error("the page has not the lines of the template")
	}
	if strings.Contains(text, "@") {
		t.Errorf("a placeholder is left:\n%s", text)
	}
	runDir := filepath.Join(root(t), "qualification/canary/run")
	for _, want := range []string{
		"Root " + r + "\n",
		"sh " + runDir + "/canary-preflight.sh --root " + r + "\n",
		"sh " + runDir + "/canary-run.sh --pass A --config " + r + "/config.json --dry\n",
		"sh " + runDir + "/canary-run.sh --pass A --config " + r + "/config.json --evidence " + r + "/evidence\n",
		"        " + e.bin + "/ataniqd run --canary " + r + "/config.json\n",
		"        " + e.bin + "/ataniq-console --attach " + r + "/runtime/leads/throwaway-one-lead \\\n          --ticket-file " + r + "/runtime/leads/throwaway-one-lead/ticket\n",
		"        rm -rf -- " + r + "/state " + r + "/runtime\n",
		"sh " + runDir + "/canary-reset.sh --root " + r + " ",
		r + "/answers.hint: Enter takes it",
		"build/qualify-mirror --config " + r + "/mirror-qual/config.json \\\n        --out " + r + "/mirror-qual/report.json\n",
		"in " + root(t) + ":\n",
		"Start the daemon in T2 BEFORE pressing Enter at step 10, wait for the\n      listening line",
		"At step 26 stop the daemon with Ctrl-C first",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the page does not hold %q:\n%s", want, text)
		}
	}
	if !strings.Contains(out, path) {
		t.Errorf("the output does not name the page:\n%s", out)
	}
	// The dry run says so and writes nothing.
	r2 := filepath.Join(e.dir, "set-up-dry")
	if out, code := e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r2, "--dry")...); code != 0 || !strings.Contains(out, "[dry]   "+r2+"/CHECKLIST.txt (0600)") {
		t.Errorf("exit status %d:\n%s", code, out)
	}
	// A path with a character sed would take for its own is put in as written.
	odd := filepath.Join(e.dir, "a|b&c")
	if err := os.MkdirAll(odd, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range canaryRunBinaries {
		data, err := os.ReadFile(filepath.Join(e.bin, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(odd, name), data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	r3 := filepath.Join(e.dir, "set-up-odd")
	args := e.prepareArgs(t, r3)
	for i, a := range args {
		if a == e.bin {
			args[i] = odd
		}
	}
	if out, code := e.run(t, canaryPrepareScript, "", args...); code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	if data, _ := os.ReadFile(filepath.Join(r3, "CHECKLIST.txt")); !strings.Contains(string(data), "        "+odd+"/ataniqd run --canary "+r3+"/config.json\n") {
		t.Errorf("the page does not hold the folder with the odd name:\n%s", data)
	}
}

// Every prompt of pass A has its row, and every word it asks for is in the page:
// both are read from the dry run of the script, so a prompt that is added
// without a row fails here.
func TestCanaryRunKitChecklistCoversTheOwnerPrompts(t *testing.T) {
	e := canaryStubKit(t)
	r := filepath.Join(e.dir, "set-up")
	if out, code := e.run(t, canaryPrepareScript, "", e.prepareArgs(t, r)...); code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	page, err := os.ReadFile(filepath.Join(r, "CHECKLIST.txt"))
	if err != nil {
		t.Fatal(err)
	}
	rows := map[int]bool{}
	row := regexp.MustCompile(`(?m)^ {1,2}([0-9]+) {2,}\S`)
	for _, m := range row.FindAllStringSubmatch(string(page), -1) {
		n, _ := strconv.Atoi(m[1])
		rows[n] = true
	}
	out, code := canaryDry(t, "--pass", "A", "--config", "/nonexistent/sample/canary.json")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	words := map[string]bool{}
	typed := regexp.MustCompile(`\(type: ([a-z-]+)\)|type ([a-z-]+) when`)
	want := map[int]bool{}
	for _, b := range canaryBlocks(out) {
		m := canaryStepHeading.FindStringSubmatch(b.heading)
		if m == nil || m[3] != "" || strings.Contains(b.heading, "skipped") {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		body := strings.Join(b.lines, "\n")
		if strings.Contains(body, "ask and record the answer") || strings.Contains(body, "wait for Enter: Owner") || strings.Contains(body, "the owner pastes") {
			want[n] = true
		}
		for _, w := range typed.FindAllStringSubmatch(body, -1) {
			words[w[1]+w[2]] = true
		}
	}
	for _, n := range []int{1, 2, 4, 8, 10, 12, 14, 17, 18, 20, 26} {
		if !want[n] {
			t.Errorf("the dry run of pass A has no prompt of the owner at step %d: the test's list is stale", n)
		}
	}
	for n := range want {
		if !rows[n] {
			t.Errorf("pass A asks the owner something at step %d and the page has no row for it:\n%s", n, page)
		}
	}
	for w := range words {
		if !strings.Contains(string(page), w) {
			t.Errorf("pass A asks for the word %q and the page does not say it", w)
		}
	}
	for _, w := range []string{"yes", "read", "clean"} {
		if !words[w] {
			t.Errorf("the dry run of pass A no longer asks for %q: the test's list is stale", w)
		}
	}
}

// The guide names what the kit offers: every test entry of the run script and
// of the helpers, the variables that are for the tests only, the new commands
// and files, and the section "Easier on the owner".
func TestCanaryRunKitReadmeDocumentsTheEntriesAndTheCommands(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run/README.md"))
	if err != nil {
		t.Fatal(err)
	}
	readme := string(data)
	entry := regexp.MustCompile(`(?m)^    ([a-z][a-z0-9-]*)\)$`)
	body := canaryFuncs(t, canaryScriptText(t))["self_test"]
	names := entry.FindAllStringSubmatch(body, -1)
	if len(names) < 20 {
		t.Fatalf("found %d test entries in the script: the pattern is stale", len(names))
	}
	for _, m := range names {
		if !strings.Contains(readme, "`--self-test "+m[1]) && !strings.Contains(readme, "`"+m[1]+"`") && !strings.Contains(readme, "`"+m[1]+" ") &&
			!strings.Contains(readme, ", "+"`--self-test "+m[1]) && !strings.Contains(readme, "| "+m[1]) && !strings.Contains(readme, "`, `"+m[1]) {
			t.Errorf("the README does not document the test entry %s", m[1])
		}
	}
	for _, want := range []string{
		"CANARY_RUN_WAIT_TRIES", "CANARY_RUN_WAIT_INTERVAL", "CANARY_RUN_STATUS_TIMEOUT", "CANARY_PREFLIGHT_PROC", "CANARY_PREFLIGHT_TIOCSTI", "CANARY_PREFLIGHT_ATANIQ",
		"## Easier on the owner", "canary-preflight.sh", "answers.hint", "CHECKLIST.txt", "--template-review PATH", "--unit-report PATH", "--plan-summary PATH",
		"READY", "NOT READY (n to fix)", "for the tests only",
		"ataniqd init --state-dir \"ROOT/state\"\nbuild/canary/ataniqd project-home --state-dir \"ROOT/state\" --home self throwaway-one\n" +
			"build/canary/ataniqd run --canary ROOT/config.json",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("the README does not mention %q", want)
		}
	}
	// The variables the scripts read are the ones the README names.
	for _, rel := range []string{canaryRunScript, canaryPreflightScript} {
		text, err := os.ReadFile(filepath.Join(root(t), rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`\$\{(CANARY_[A-Z_]+):-`).FindAllStringSubmatch(string(text), -1) {
			if !strings.Contains(readme, m[1]) {
				t.Errorf("%s reads %s and the README does not name it", rel, m[1])
			}
		}
	}
	// The README's own commands exist.
	for _, name := range []string{"canary-preflight.sh", "canary-prepare.sh", "canary-reset.sh", "canary-run.sh", "canary-lib.sh", "checklist.txt.in"} {
		if _, err := os.Stat(filepath.Join(root(t), "qualification/canary/run", name)); err != nil {
			t.Errorf("the kit has no %s", name)
		}
	}
}
