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
)

const canaryRunScript = "qualification/canary/run/canary-run.sh"

// canaryRunBinaries are the commands the run script uses, and so the ones
// make canary-build builds, and no more.
var canaryRunBinaries = []string{
	"ataniqd", "ataniq-holder", "ataniq-canary", "ataniq-canary-standin",
	"ataniq-console", "ataniq-intake", "ataniq-probe",
}

// canaryStaticBinaries are built without cgo: both run inside the sandbox.
var canaryStaticBinaries = []string{"ataniq-canary-standin", "ataniq-probe"}

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
	"sleep", "env", "mktemp", "id", "sh",
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
	want := slices.Clone(canaryRunBinaries)
	sort.Strings(want)
	sort.Strings(built)
	if !slices.Equal(built, want) {
		t.Errorf("canary-build builds %v, want %v", built, want)
	}

	// The script uses each of them, from build/canary and from nowhere else.
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
	const oneTarget = "`make canary-build` is the one target that builds binaries with the tag"
	for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
		data, err := os.ReadFile(filepath.Join(root(t), name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "no other target builds with the tag") {
			t.Errorf("%s still says that no other target builds with the tag", name)
		}
		if !strings.Contains(string(data), oneTarget) {
			t.Errorf("%s does not say: %s", name, oneTarget)
		}
	}
	if _, err := os.Stat(filepath.Join(root(t), "qualification/canary/run/README.md")); err != nil {
		t.Error(err)
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
	for _, name := range append([]string{"self_test"}, canaryReach(canaryFuncs(t, text), "self_test")...) {
		for _, word := range []string{"runcap", "ask", "affirm", "systemctl", "step_head"} {
			if canaryCalls(canaryFuncs(t, text)[name], word) != 0 {
				t.Errorf("the test entry reaches %s, which calls %s", name, word)
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
	} {
		if !strings.Contains(paste, want) {
			t.Errorf("paste_message does not say %s", want)
		}
	}
	body := funcs["step_17"]
	if n := canaryCalls(body, "paste_message"); n != 2 {
		t.Errorf("step 17 gives the paste instruction in %d places, want 2 (pass A, and a refusal in pass B)", n)
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
			if b.count("print the file to paste from, ") != 1 || b.count("as one paste, and presses Enter once: he does not type it") != 1 {
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
		`while [ "$(fld "$HSTATE/holder.json" epoch 2>/dev/null)" != "$START_EPOCH" ]`,
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
	cmd := exec.Command("/bin/sh", filepath.Join(root(t), canaryRunScript), "--self-test", "wait-key")
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=" + t.TempDir(), "HOME=" + t.TempDir()}
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
		{"facts_pass", "unit_facts_verdict"},
		{"facts_pass", "plan_facts_verdict"},
		{"step_2", `facts_pass "$_kind" "$ANSWER" || return 0`},
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
	// Without qargi, or with a list it cannot read, the old prompt stays and the log says so.
	body := funcs["no_open_runs"]
	if strings.Count(body, "affirm 'Lead: no Qargi run is open'") != 2 || strings.Count(body, "note '") < 2 {
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
	if want := []string{"canary-lib.sh", "canary-prepare.sh", "canary-reset.sh", "canary-run.sh"}; !slices.Equal(shells, want) {
		t.Errorf("the scripts of the kit are %v, want %v", shells, want)
	}
	// The helpers never answer a prompt and start no unit, daemon or sandbox.
	for _, rel := range []string{canaryPrepareScript, canaryResetScript, canaryLibScript} {
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
	cmd.Env = []string{"PATH=" + e.tools + ":/usr/bin:/bin", "HOME=" + home, "XDG_RUNTIME_DIR=" + filepath.Join(e.dir, "run")}
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
	if !in.Canary || in.ProviderHost != "api.anthropic.com" || in.Executables["pasta"] != e.pasta(t) || in.Executables["probe"] != e.bin+"/ataniq-probe" ||
		in.Sources["managed_settings_sha256"] != hex.EncodeToString(sum[:]) || in.Sources["agent_install"] != r+"/agent-install" {
		t.Errorf("sandbox-inputs.json:\n%s", inputs)
	}
	// What was copied, and what was recorded.
	screens := filepath.Join(root(t), "internal/inject/screen/testdata/claude-2.1.325")
	for dst, src := range map[string]string{
		"conf/replay-screens/1.json":          filepath.Join(screens, "idle-before-paste.json"),
		"conf/replay-screens/1.txt":           filepath.Join(screens, "idle-before-paste.txt"),
		"conf/replay-screens/2.json":          filepath.Join(screens, "collapsed-paste-footer-a.json"),
		"conf/replay-screens/2.txt":           filepath.Join(screens, "collapsed-paste-footer-a.txt"),
		"agent-install/ataniq-canary-standin": filepath.Join(e.bin, "ataniq-canary-standin"),
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
		{"a root in the repository", e.prepareArgs(t, filepath.Join(inRepo, "root")), "repository checkout"},
		{"the repository itself", e.prepareArgs(t, root(t)), "repository checkout"},
		{"a root in /tmp", e.prepareArgs(t, filepath.Join(tmpRoot, "root")), "/tmp"},
		{"a root that is /tmp itself", e.prepareArgs(t, "/tmp"), "/tmp"},
		{"a relative root", e.prepareArgs(t, "set-up"), "not an absolute path"},
		{"a root with a .. part", e.prepareArgs(t, e.dir+"/a/../b"), ".. part"},
		{"a root with a quote", e.prepareArgs(t, filepath.Join(e.dir, "a\"b")), "character"},
		{"a root that is a link", e.prepareArgs(t, link), "symbolic link"},
		{"pass B", e.prepareArgs(t, filepath.Join(e.dir, "b"), "--pass", "B"), "only pass A"},
		{"a missing binary", append([]string{"--root", filepath.Join(e.dir, "c"), "--bin", noBin, "--pasta", e.pasta(t)}), "not built in"},
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
	want := "100 101 106 107 108 109 110 111 112 113"
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
			for _, k := range []string{"config.json", "evidence/pass", "conf/extra", "conf/sandbox-inputs.json", "bin/extra", "mirror-qual/state/x", "upstream/README.txt", ".canary-prepared", "agent-install/ataniq-canary-standin"} {
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
	if !strings.Contains(text, `find "$ROOT/$_k" -type d -exec chmod u+rwx {} \;`) {
		t.Error("the reset does not open the modes with find")
	}
}
