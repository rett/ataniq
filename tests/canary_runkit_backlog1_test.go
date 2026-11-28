package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests of the run kit's first backlog series: step 5's narrower question and
// step 23's way to identify the login (pass B), step 20's printed digest and
// its re-asked sign-off, the clean-up chain keeping the first proof of the
// stop, the daemon's place in the printed lists, and the docs that say so.

// Step 5 asks for the account page's rows before the login and says what pass
// B run 1 saw, and that its cause is not verified.
func TestCanaryRunKitBacklog1Step5Question(t *testing.T) {
	text := canaryScriptText(t)
	f := canaryFuncs(t, text)["step_5"]
	for _, want := range []string{
		"the rows the account page lists now",
		"a separate throwaway account may be used",
		"otherwise type no",
		"rows named Unknown",
		"could not match one",
		"not verified",
		"checks before and after",
	} {
		if !strings.Contains(f, want) {
			t.Errorf("step 5 does not say %q", want)
		}
	}
	if strings.Contains(f, "type confirmed when the account page lists logins") {
		t.Error("step 5 still asks the old question")
	}
	for _, old := range []string{"its own entry that can be revoked on its own", "a page that lists logins is not enough"} {
		if strings.Contains(f, old) {
			t.Errorf("step 5 still states the old premise %q as a fact", old)
		}
	}
	for _, c := range []struct {
		answer string
		ok     bool
	}{{"confirmed", true}, {"no", false}, {"", false}, {"yes", false}} {
		dir := t.TempDir()
		pre := "PASS=B IN_CHAIN=0\nstep_head() { :; }\nask() { ANSWER='" + c.answer + "'; }\nresult() { echo \"RESULT: $*\"; }\ndone_step() { echo DONE-STEP; }"
		o, code := canaryStepHarness(t, dir, []string{"out", "note", "dry", "fail", "check", "is_eq", "step_5"}, pre, "step_5")
		if code != 0 && c.ok {
			t.Fatalf("%q: exit status %d:\n%s", c.answer, code, o)
		}
		if got := strings.Contains(o, "DONE-STEP"); got != c.ok {
			t.Errorf("answer %q: done=%v, want %v:\n%s", c.answer, got, c.ok, o)
		}
	}
}

// Step 23 says how to identify the login and takes cannot-identify as a
// finding, not a failure.
func TestCanaryRunKitBacklog1Step23CannotIdentify(t *testing.T) {
	for _, c := range []struct {
		answer   string
		done     bool
		findings string
	}{
		{"revoked", true, "findings=0"},
		{"no-login", true, "findings=0"},
		{"cannot-identify: three Unknown rows", true, "findings=1"},
		{"cannot-identify", false, "findings=0"},
		{"cannot-identify:", false, "findings=0"},
		{"cannot-identify:   ", false, "findings=0"},
		{"done", false, "findings=0"},
	} {
		dir := t.TempDir()
		pre := "PASS=B STAMP=20260101T000000Z LOGIN_AT= IN_CHAIN=0 FAIL_STEP=0\nstep_head() { :; }\nwas_done() { return 1; }\nask() { ANSWER='" + c.answer + "'; }\nfail() { echo \"FAILED: $*\"; }\nresult() { echo \"RESULT: $*\"; }\ndone_step() { echo DONE-STEP; }"
		o, code := canaryStepHarness(t, dir, []string{"out", "note", "dry", "step_23"}, pre, "step_23")
		if code != 0 {
			t.Fatalf("%q: exit status %d:\n%s", c.answer, code, o)
		}
		if got := strings.Contains(o, "DONE-STEP"); got != c.done {
			t.Errorf("answer %q: done=%v, want %v:\n%s", c.answer, got, c.done, o)
		}
		if !strings.Contains(o, c.findings) {
			t.Errorf("answer %q: want %s:\n%s", c.answer, c.findings, o)
		}
		if c.done && strings.HasPrefix(c.answer, "cannot-identify:") && !strings.Contains(o, "a finding") {
			t.Errorf("the finding is not said:\n%s", o)
		}
	}
	o, _ := canaryDry(t, "--pass", "B", "--config", "/nonexistent/sample/canary.json")
	for _, want := range []string{"new row whose last-active time falls during this run", "log out of all devices", "cannot-identify: <what you see>", "its row is gone", "the session cannot be tried here"} {
		if !strings.Contains(o, want) {
			t.Errorf("pass B's dry run does not say %q", want)
		}
	}
}

// step20Pre stubs what step 20 calls: the review writes a Digest: and a Tip:
// line, the verify output holds a plan digest.
const step20Pre = `PASS=A IN_CHAIN=0 CAPPED=0 BIN=/b STATE=/s PROJECT=p LAYOUT=l BRANCH=br UPSTREAM=up TEMPLATE=/t MIRROR=/m TOKEN=tok
step_head() { :; }
wait_key() { :; }
check() { return 0; }
show_token_file() { :; }
result() { echo "RESULT: $*"; }
done_step() { echo DONE-STEP; }
lead_branch_id() { echo c1; }
upstream_branch_id() { echo c1; }
fail() { echo "FAILED: $*"; exit 1; }
N=0
runcap() {
  RC=0
  LASTOUT=$EV/out-$1.txt
  case $1 in
    review) printf '%b' "${REVIEW:-Tip: tip123\\nDigest: digest456\\nOther: x\\n}" >"$LASTOUT" ;;
    verify) printf 'plan digest: plan789\n' >"$LASTOUT" ;;
    upstream-tip) echo c1 >"$LASTOUT" ;;
    *) : >"$LASTOUT" ;;
  esac
}
ask() {
  N=$((N + 1)); echo "ASK$N"
  if [ -z "$ANSWERS" ]; then ANSWER=; _askok=0; return 0; fi
  set -- $ANSWERS; ANSWER=$1; shift; ANSWERS=$*; _askok=1
}
`

var step20Real = []string{"out", "note", "dry", "step_20"}

// Step 20 prints the review's lines, says which digest to use, and asks for
// yes up to three times.
func TestCanaryRunKitBacklog1Step20AsksAgain(t *testing.T) {
	for _, c := range []struct {
		answers string
		done    bool
		asks    int
	}{
		{"yes", true, 1},
		{"y yes", true, 2},
		{"y no yes", true, 3},
		{"y no yess yes", false, 3},
		{"no", false, 2},
		{"", false, 1},
	} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "ev"), 0o700); err != nil {
			t.Fatal(err)
		}
		o, code := canaryStepHarness(t, dir, step20Real, step20Pre+"ANSWERS='"+c.answers+"'", "step_20")
		if code != 0 && c.done {
			t.Fatalf("%q: exit status %d:\n%s", c.answers, code, o)
		}
		if got := strings.Contains(o, "DONE-STEP"); got != c.done {
			t.Errorf("answers %q: done=%v, want %v:\n%s", c.answers, got, c.done, o)
		}
		if got := strings.Count(o, "ASK"); got != c.asks {
			t.Errorf("answers %q: %d asks, want %d:\n%s", c.answers, got, c.asks, o)
		}
		if !c.done && !strings.Contains(o, "FAILED: ") {
			t.Errorf("answers %q: the pass did not fail:\n%s", c.answers, o)
		}
		for _, want := range []string{"Tip: tip123", "Digest: digest456", "Use the review's digest, not the verify output's plan digest"} {
			if !strings.Contains(o, want) {
				t.Errorf("answers %q: the step does not print %q:\n%s", c.answers, want, o)
			}
		}
		if strings.Contains(o, "plan789") || strings.Contains(o, "Other: x") {
			t.Errorf("answers %q: the step prints more than the two lines:\n%s", c.answers, o)
		}
	}
}

// Both passes' dry output names the new step 20 lines.
func TestCanaryRunKitBacklog1Step20Dry(t *testing.T) {
	for _, pass := range []string{"A", "B"} {
		o, code := canaryDry(t, "--pass", pass, "--config", "/nonexistent/sample/canary.json")
		if code != 0 {
			t.Fatalf("pass %s: exit status %d", pass, code)
		}
		for _, want := range []string{
			"print the review's Digest: and Tip: lines, read from",
			"/out/20-e1-review.out",
			"Use the review's digest, not the verify output's plan digest",
			"Owner: integrate succeeded (type: yes)",
			"ask again when the answer is not exactly yes, up to 2 more times",
		} {
			if !strings.Contains(o, want) {
				t.Errorf("pass %s: the dry run does not say %q", pass, want)
			}
		}
	}
}

// The clean-up chain's second run of step 22 keeps the first proof of the
// stop and writes its own with the suffix -chain.
func TestCanaryRunKitBacklog1ChainKeepsTheStopProof(t *testing.T) {
	for _, c := range []struct {
		name    string
		chain   string
		prior   bool
		wantOld string
		wantNew string
	}{
		{"chain with a proof", "1", true, "first", "out/22-e1-stop-chain.out"},
		{"chain without a proof", "1", false, "", "out/22-e1-stop.out"},
		{"no chain overwrites", "0", true, "", "out/22-e1-stop.out"},
	} {
		dir := t.TempDir()
		ev := filepath.Join(dir, "ev")
		if err := os.MkdirAll(filepath.Join(ev, "out"), 0o700); err != nil {
			t.Fatal(err)
		}
		first := filepath.Join(ev, "out", "22-e1-stop.out")
		if c.prior {
			if err := os.WriteFile(first, []byte("first\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		pre := "CUR=22 EPOCH_NO=1 IN_CHAIN=" + c.chain + "\nstopper() { echo second; }"
		o, code := canaryStepHarness(t, dir, []string{"out", "note", "dry", "chain_sfx", "runcap"}, pre, "runcap stop stopper")
		if code != 0 {
			t.Fatalf("%s: exit status %d:\n%s", c.name, code, o)
		}
		if c.wantOld != "" {
			b, err := os.ReadFile(first)
			if err != nil || strings.TrimSpace(string(b)) != c.wantOld {
				t.Errorf("%s: the first proof changed: %q (%v)", c.name, b, err)
			}
		}
		b, err := os.ReadFile(filepath.Join(ev, c.wantNew))
		if err != nil || strings.TrimSpace(string(b)) != "second" {
			t.Errorf("%s: %s holds %q (%v)", c.name, c.wantNew, b, err)
		}
		if c.name == "chain with a proof" {
			if _, err := os.Stat(filepath.Join(ev, "out", "22-e1-stop-chain.err")); err != nil {
				t.Errorf("%s: no -chain error file: %v", c.name, err)
			}
		}
	}
	f := funcsOf(t)["step_22"]
	for _, want := range []string{"chain_sfx", "_ua=", "_ufa="} {
		if !strings.Contains(f, want) {
			t.Errorf("step 22 does not use %q", want)
		}
	}
}

// The daemon is listed at step 10, with the reason, in the preflight's list
// and in both checklist templates.
func TestCanaryRunKitBacklog1DaemonAtStep10(t *testing.T) {
	pre, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run/canary-preflight.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"1. At step 10, in a second terminal (T2), the daemon", "step 8 provisions the roots and writes the", "marker file (canary-root.json) that the daemon needs"} {
		if !strings.Contains(string(pre), want) {
			t.Errorf("the preflight does not say %q", want)
		}
	}
	for _, name := range []string{"checklist.txt.in", "checklist-b.txt.in"} {
		b, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run", name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "marker") {
			t.Errorf("%s does not say why the daemon waits for step 8", name)
		}
		// The template wraps the enrollment command to fit on the owner's page.
		commands := strings.Join(strings.Fields(strings.ReplaceAll(string(b), "\\\n", " ")), " ")
		want := "ataniqd init --state-dir \"@ROOT@/state\" @BIN@/ataniqd project-home --state-dir \"@ROOT@/state\" --home self throwaway-one @BIN@/ataniqd run --canary @CONFIG@"
		if !strings.Contains(commands, want) {
			t.Errorf("%s lacks the ordered init, enrollment and start commands", name)
		}
	}
	b, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run/checklist-b.txt.in"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"throwaway", "cannot-identify:", "Unknown"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the pass B checklist does not say %q", want)
		}
	}
}

// The doc comment of CheckInputs names its real callers, and the slice's guide
// holds the short section of what pass B showed.
func TestCanaryRunKitBacklog1Docs(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(root(t), "internal/leadcanary/provision/inputs.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(src), "The run kit and the\n// daemon call it") || strings.Contains(string(src), "daemon call it before a start") {
		t.Error("the comment of CheckInputs still says the daemon calls it")
	}
	if !strings.Contains(string(src), "the daemon does not.") {
		t.Error("the comment of CheckInputs does not say the daemon does not call it")
	}
	doc, err := os.ReadFile(filepath.Join(root(t), "docs/canary-slice.md"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(doc)
	i := strings.Index(s, "## What pass B showed\n")
	if i < 0 {
		t.Fatal("docs/canary-slice.md has no section \"What pass B showed\"")
	}
	sec := s[i:]
	if j := strings.Index(sec[3:], "\n## "); j >= 0 {
		sec = sec[:j+3]
	}
	if n := len(strings.Split(strings.TrimSpace(sec), "\n")); n >= 25 {
		t.Errorf("the section has %d lines, want fewer than 25", n)
	}
	for _, want := range []string{"2.1.325", "1 query and 8 reports", "24-minute", "not-now", "not on the alternate screen", "one turn", "450 MB of the 4 GiB", "Unknown", "cannot-identify"} {
		if !strings.Contains(sec, want) {
			t.Errorf("the section does not say %q", want)
		}
	}
	readme, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run/README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"throwaway account", "cannot-identify: <what you see>", "-chain", "review's `Digest:`"} {
		if !strings.Contains(string(readme), want) {
			t.Errorf("the README does not say %q", want)
		}
	}
}

// Step 20 caps what it prints from the review's output, replaces control
// characters, and says so when the review printed no Digest: or Tip: line.
func TestCanaryRunKitBacklog1Step20PrintIsCapped(t *testing.T) {
	long := strings.Repeat("x", 900)
	for _, c := range []struct {
		name    string
		review  string
		want    []string
		notWant []string
	}{
		{"no lines", `Other: x\n`, []string{"no Digest line in the review output"}, []string{"Tip:", "Digest: "}},
		{"capped", `Tip: t\nDigest: ` + long + `\n`, []string{"Tip: t", "Digest: xxx"}, []string{strings.Repeat("x", 301)}},
		{"control characters", `Digest: a\033[31mb\rc\n`, []string{"Digest: a?[31mb?c"}, []string{"\x1b", "\r"}},
		{"at most four lines", `Tip: 1\nTip: 2\nTip: 3\nTip: 4\nTip: 5\n`, []string{"Tip: 4"}, []string{"Tip: 5"}},
	} {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "ev"), 0o700); err != nil {
			t.Fatal(err)
		}
		o, code := canaryStepHarness(t, dir, step20Real, step20Pre+"REVIEW='"+c.review+"' ANSWERS=yes", "step_20")
		if code != 0 || !strings.Contains(o, "DONE-STEP") {
			t.Fatalf("%s: exit status %d:\n%s", c.name, code, o)
		}
		for _, w := range c.want {
			if !strings.Contains(o, w) {
				t.Errorf("%s: the output lacks %q:\n%s", c.name, w, o)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(o, w) {
				t.Errorf("%s: the output holds %q:\n%s", c.name, w, o)
			}
		}
	}
}

// In the clean-up chain the journal and the unit lists of step 22 keep the
// first run's files: the chain writes its own with the suffix -chain.
func TestCanaryRunKitBacklog1ChainKeepsJournalAndLists(t *testing.T) {
	for _, chain := range []string{"1", "0"} {
		dir := t.TempDir()
		ev := filepath.Join(dir, "ev")
		if err := os.MkdirAll(filepath.Join(ev, "out"), 0o700); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"out/22-e1-unit-journal.out", "units-after-1.txt", "unit-files-after-1.txt", "units-before.txt", "unit-files-before.txt"} {
			if err := os.WriteFile(filepath.Join(ev, f), []byte("first\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		pre := "CUR=22 EPOCH_NO=1 IN_CHAIN=" + chain + " T_START=t0 UNIT=u STARTED=0 STOPPED=0 ME=me LINGER0=x HSTATE=/nonexistent BIN=/b\n" +
			"step_head() { :; }\nwait_key() { :; }\nstop_role() { :; }\nresult() { :; }\ndone_step() { :; }\nfld() { echo x; }\nis_eq() { :; }\n" +
			"unit_journal() { echo second; }\n" +
			"runcap() { LASTOUT=$EV/lo.txt; echo 'ataniq-second.service loaded' >$EV/lo.txt; RC=0; }\n" +
			"check() { shift; \"$@\" || :; }\n"
		real := []string{"out", "note", "dry", "chain_sfx", "save_unit_journal", "step_22"}
		o, code := canaryStepHarness(t, dir, real, pre, "step_22")
		if code != 0 {
			t.Fatalf("chain %s: exit status %d:\n%s", chain, code, o)
		}
		for _, f := range []struct{ base, ext string }{{"out/22-e1-unit-journal", ".out"}, {"units-after-1", ".txt"}, {"unit-files-after-1", ".txt"}} {
			old, err := os.ReadFile(filepath.Join(ev, f.base+f.ext))
			if err != nil {
				t.Fatal(err)
			}
			chained, cerr := os.ReadFile(filepath.Join(ev, f.base+"-chain"+f.ext))
			if chain == "1" {
				if strings.TrimSpace(string(old)) != "first" {
					t.Errorf("chain: %s was overwritten: %q", f.base, old)
				}
				if cerr != nil || !strings.Contains(string(chained), "second") {
					t.Errorf("chain: %s-chain%s holds %q (%v)", f.base, f.ext, chained, cerr)
				}
			} else {
				if strings.TrimSpace(string(old)) == "first" {
					t.Errorf("no chain: %s was not rewritten", f.base)
				}
				if cerr == nil {
					t.Errorf("no chain: %s-chain%s exists", f.base, f.ext)
				}
			}
		}
	}
}
