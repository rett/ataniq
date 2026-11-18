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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
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

// canaryDry runs the script with --dry and the further arguments in a fresh
// folder, with a PATH and a build/canary folder that hold only recording
// stubs. It returns the output and the exit status, and fails the test when a
// stub was called or the run made any file.
func canaryDry(t *testing.T, args ...string) (string, int) {
	t.Helper()
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

	cmd := exec.Command("/bin/sh", append([]string{filepath.Join(root(t), canaryRunScript), "--dry"}, args...)...)
	cmd.Dir = work
	cmd.Env = []string{"PATH=" + bin, "HOME=" + home, "XDG_RUNTIME_DIR=" + filepath.Join(dir, "run")}
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("the dry run did not run: %v", err)
		}
		code = exit.ExitCode()
	}
	if called, err := os.ReadFile(calls); err == nil {
		t.Fatalf("the dry run %v started something:\n%s", args, called)
	}
	if after := canaryTree(t, dir); !slices.Equal(before, after) {
		t.Fatalf("the dry run %v changed the folder:\nbefore %v\nafter  %v", args, before, after)
	}
	if bytes.Contains(out, []byte("not found")) {
		t.Fatalf("the dry run %v looked for a program:\n%s", args, out)
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
