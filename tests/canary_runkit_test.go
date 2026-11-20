package tests

// This file checks the kit of the attended canary run: the Makefile target
// that builds its binaries, the lines of AGENTS.md about it, and the run
// script, which the tests run with --dry only. It is not behind the canary
// build tag and passes with and without it. Nothing here starts a unit, a
// namespace, a service or an agent: the dry runs get a PATH that holds only
// stubs which record being called, and none may be called.

import (
	"bytes"
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
	path := filepath.Join(root(t), canaryRunScript)
	if out, err := exec.Command("/bin/sh", "-n", path).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "#!/bin/sh\n") {
		t.Error("the script does not start with #!/bin/sh")
	}
	if !strings.Contains(text, "\nset -eu\n") {
		t.Error("the script does not set -eu")
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
			t.Errorf("the script uses %s, which is not POSIX sh: %q", name, text[loc[0]:min(len(text), loc[1]+40)])
		}
	}
	// It never names the tag's build: it only runs what canary-build built.
	if strings.Contains(text, "go-tool.sh") || strings.Contains(text, "go build") {
		t.Error("the script builds something")
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
	// Without qargi, or with a list it cannot read, the old prompt stays and the log says so.
	body := funcs["no_open_runs"]
	if strings.Count(body, "affirm 'Lead: no Qargi run is open'") != 2 || strings.Count(body, "note '") < 2 {
		t.Error("the fallback of the open-run check does not ask and say so in the log")
	}
}
