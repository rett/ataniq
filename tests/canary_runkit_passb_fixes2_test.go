package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Tests of the fixes after the advisor review of the pass B run kit: step 17's
// word and its asking again, step 25's proof of a login and its second
// manifest, the checklist against the run's words, the preflight's model and
// ss checks, and netcheck's resolv.conf rule.

// step17Harness runs step 17 of pass B with a fence answer of not-now, not
// ready, nothing written (so the owner supplies the message) and the owner's
// answers one per ask; the end of the answers is the end of the input.
func step17Harness(t *testing.T, answers string) string {
	t.Helper()
	dir := t.TempDir()
	pre := `PASS=B BRANCH=b MSGID=m BODY=/x/body CONFIG=/x/c BIN=/x/bin N=0 ANSWERS='` + answers + `'
step_head() { :; }
over_cap() { return 1; }
make_message() { :; }
wait_key() { :; }
runcap() { RC=0; LASTOUT=x; }
fld() { case $2 in result.outcome) echo not-now ;; result.reason) echo not-ready ;; result.written) echo nothing ;; esac; }
paste_message() { echo PASTE-MESSAGE; }
result() { echo "RESULT: $*"; }
done_step() { echo DONE-STEP; }
ask() {
  N=$((N + 1)); echo "ASK$N: $1"
  if [ -z "$ANSWERS" ]; then ANSWER=; _askok=0; return 0; fi
  set -- $ANSWERS; ANSWER=$1; shift; ANSWERS=$*; _askok=1
}
`
	// An empty answer is given by the word EMPTY.
	pre = strings.Replace(pre, "ANSWER=$1; shift;", "ANSWER=$1; [ \"$ANSWER\" = EMPTY ] && ANSWER=; shift;", 1)
	o, code := canaryStepHarness(t, dir, []string{"out", "note", "dry", "fail", "now", "inject_action", "step_17"}, pre, "step_17")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, o)
	}
	return o
}

// F1: on the owner-supplies path an empty line is asked again (up to three
// times) and does not end the pass after the login is spent; any other word
// than pasted ends it.
func TestCanaryRunKitStep17AsksPastedAgain(t *testing.T) {
	for _, c := range []struct {
		answers string
		done    bool
		asks    int
	}{
		{"pasted", true, 1},
		{"EMPTY pasted", true, 2},
		{"EMPTY EMPTY EMPTY pasted", true, 4},
		{"EMPTY EMPTY EMPTY EMPTY pasted", false, 4},
		{"EMPTY no", false, 2},
		{"done", false, 1},
		{"", false, 1},
	} {
		o := step17Harness(t, c.answers)
		if got := strings.Contains(o, "DONE-STEP"); got != c.done {
			t.Errorf("%q: done=%v, want %v:\n%s", c.answers, got, c.done, o)
		}
		if got := strings.Contains(o, "findings=0"); got != c.done {
			t.Errorf("%q: findings=0 is %v, want %v:\n%s", c.answers, got, c.done, o)
		}
		if n := strings.Count(o, "ASK"); n != c.asks {
			t.Errorf("%q: asked %d times, want %d:\n%s", c.answers, n, c.asks, o)
		}
	}
}

// F1: the dry run of pass B names the word in step 17's text, and pass A's does
// not gain it.
func TestCanaryRunKitStep17DryNamesPasted(t *testing.T) {
	b, code := canaryDry(t, "--pass", "B", "--config", "/nonexistent/sample/canary.json")
	if code != 0 || !strings.Contains(b, "ask for the word pasted") {
		t.Errorf("pass B's dry run does not name the word pasted (exit %d)", code)
	}
	a, _ := canaryDry(t, "--pass", "A", "--config", "/nonexistent/sample/canary.json")
	if strings.Contains(a, "word pasted") {
		t.Error("pass A's dry run names the word pasted")
	}
}

// F1 and F4: every word pass B asks for is in the checklist, the page is one
// page, and the BEFORE order is the one prepare prints and the README holds.
func TestCanaryRunKitPassBChecklistMatchesTheRun(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run/checklist-b.txt.in"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, w := range []string{"confirmed", "logged-in", "go-on", "pasted", "logged-out", "revoked", "done", "not-applicable"} {
		if !regexp.MustCompile(`\b` + w + `\b`).MatchString(text) {
			t.Errorf("the checklist does not name the word %q", w)
		}
	}
	// Each of those words is one the run asks for.
	script := canaryScriptText(t)
	for _, w := range []string{"confirmed", "logged-in", "go-on", "pasted", "logged-out", "revoked", "done"} {
		if !strings.Contains(script, w) {
			t.Errorf("the run script does not mention %q", w)
		}
	}
	for _, want := range []string{"an Enter alone there ends the pass", "go-on if the pane", "an Enter is a finding", "H4: done"} {
		if !strings.Contains(text, want) {
			t.Errorf("the checklist does not say %q", want)
		}
	}
	if n := len(strings.Split(strings.TrimSuffix(text, "\n"), "\n")); n > 60 {
		t.Errorf("the template has %d lines", n)
	}
	// The BEFORE order: the mirror qualification, then netcheck, then the preflight.
	m, nc, pf := strings.Index(text, "qualify-mirror for THIS root"), strings.Index(text, "sh @NETCHECK@"), strings.Index(text, "sh @PREFLIGHT@")
	if !(m >= 0 && m < nc && nc < pf) {
		t.Errorf("the BEFORE order is not mirror, netcheck, preflight (%d, %d, %d)", m, nc, pf)
	}
	prep, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run/canary-prepare.sh"))
	if err != nil {
		t.Fatal(err)
	}
	pb := string(prep)
	pb = pb[strings.Index(pb, `if [ "$PASS" = B ]; then
  cat <<EOF`):]
	m, nc, pf = strings.Index(pb, "mirror qualification of this root"), strings.Index(pb, "sh $NETCHECK"), strings.Index(pb, "canary-preflight.sh --root")
	if !(m >= 0 && m < nc && nc < pf) {
		t.Errorf("prepare's printed order is not mirror, netcheck, preflight (%d, %d, %d)", m, nc, pf)
	}
	rd, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run/README.md"))
	if err != nil {
		t.Fatal(err)
	}
	r := string(rd)
	m, nc = strings.Index(r, "Then run `qualify-mirror` for this root"), strings.Index(r, "2. **`netcheck.sh`, by hand.**")
	if !(m >= 0 && m < nc) {
		t.Errorf("the README's order is not mirror, then netcheck (%d, %d)", m, nc)
	}
}

// F2 and F3: step 25 takes only a credential as proof that the canary wrote a
// login, and compares the real folder with the canary's folder as it is at step
// 25 too.
func TestCanaryRunKitStep25ProofOfALogin(t *testing.T) {
	// The reproduced case: the canary's login wrote only its state file; the
	// credential of the real folder changed. No question, no yes to give.
	o, ans, code := step25Real(t, "s1 2 2 ./.claude.json\n", "", "yes\nyes\n")
	if code != 0 || ans != "" || strings.Contains(o, "??") {
		t.Fatalf("a question was asked of a canary that wrote only its state file (exit %d):\n%s\n%q", code, o, ans)
	}
	if !strings.Contains(o, "findings=1") || !strings.Contains(o, "MEANINGFUL (the canary's login wrote no file of its kind") || !strings.Contains(o, "./.credentials.json") {
		t.Errorf("the changed credential is not a finding:\n%s", o)
	}
	// A token the canary refreshed after step 15 still matches, from the
	// manifest of step 25 of its folder.
	o, ans, _ = step25Real(t, "z9 2 2 ./.credentials.json\n", "a1 3 3 ./.credentials.json\n", "yes\nyes\n")
	if !strings.Contains(o, "MEANINGFUL: changed ./.credentials.json") || !strings.Contains(o, "findings=1") {
		t.Errorf("a refreshed canary credential is not judged:\n%s", o)
	}
	if strings.Count(ans, "\n") != 1 {
		t.Errorf("only the other file should be asked about:\n%q", ans)
	}
	// Without the second manifest the same refresh is only a question.
	o, _, _ = step25Real(t, "z9 2 2 ./.credentials.json\n", "", "yes\nyes\n")
	if strings.Contains(o, "MEANINGFUL: changed ./.credentials.json") {
		t.Errorf("the credential is judged without a hash to prove it:\n%s", o)
	}
	// F3: no second manifest of step 15 (no login was completed): the owner's
	// own change is a question, never a finding without it.
	o, ans, _ = step25Real(t, "", "", "yes\nyes\nyes\n")
	if strings.Contains(o, "MEANINGFUL") || !strings.Contains(o, "findings=0") || strings.Count(ans, "\n") != 2 {
		t.Errorf("no login was made, but a change is a finding or goes unasked:\n%s\n%q", o, ans)
	}
}

// F5 and F8: a live root without --model in agent.argv is a FIX; ss is wanted
// for a live root only.
func TestCanaryRunKitPassBPreflightModelAndSs(t *testing.T) {
	p, _ := passBPF(t)
	show := readPassB(t, p.dir, "show.out")
	if !strings.Contains(show, "agent_argv_1=--model") {
		t.Fatal("the live harness holds no --model")
	}
	canaryWrite(t, p.dir, "show.out", strings.Replace(strings.Replace(show, "agent_argv_1=--model\n", "", 1), "agent_argv_2=sample-model\n", "", 1))
	out, code := p.run()
	if code == 0 || !strings.Contains(out, "FIX:  agent.argv has no --model") || !strings.Contains(out, "NOT READY (1 to fix)") || strings.Contains(out, "READY\n") && !strings.Contains(out, "NOT READY") {
		t.Errorf("exit status %d, want one FIX for the missing --model:\n%s", code, out)
	}
	if !strings.Contains(out, `add "--model", "NAME" (not in the pin)`) {
		t.Errorf("the FIX does not name the hand edit:\n%s", out)
	}

	p, _ = passBPF(t)
	os.Remove(filepath.Join(p.tools, "ss"))
	out, code = p.run()
	if code == 0 || !strings.Contains(out, "FIX:  not on PATH (steps 3 and 6 run them): ss") {
		t.Errorf("a live root without ss: exit status %d:\n%s", code, out)
	}

	pa := newCanaryPF(t)
	os.Remove(filepath.Join(pa.tools, "ss"))
	out, code = pa.run()
	if code != 0 || !strings.Contains(out, "ok:   systemctl, loginctl, git, bwrap and pasta are on PATH") || strings.Contains(out, "ss") && strings.Contains(out, "not on PATH") {
		t.Errorf("a pass A root without ss: exit status %d:\n%s", code, out)
	}
}

// F5: the note for no model does not tell the owner to prepare the same root
// again.
func TestCanaryRunKitModelNote(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run/canary-prepare.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "Prepare again with --model") || !strings.Contains(s, "Prepare a NEW root with --model NAME") || !strings.Contains(s, "by hand (not pinned) and run preflight again") {
		t.Error("the model note is not the new one")
	}
}

// F6: netcheck.sh uses resolved's upstream file only for a stub host file, as
// the holder does; the agent instructions name it as owner-only.
func TestCanaryRunKitNetcheckResolvAndOwnerOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run/netcheck.sh.in"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"/run/systemd/resolve/stub-resolv.conf", "127.0.0.53", `[ "$_stub" -eq 1 ] && [ -r /run/systemd/resolve/resolv.conf ]`} {
		if !strings.Contains(s, want) {
			t.Errorf("netcheck.sh.in does not hold %q", want)
		}
	}
	if strings.Contains(s, "[ -r \"$RESOLV\" ] || RESOLV=/etc/resolv.conf") {
		t.Error("netcheck.sh.in still binds the upstream file whenever it is readable")
	}
	for _, f := range []string{"AGENTS.md", "CLAUDE.md"} {
		d, err := os.ReadFile(filepath.Join(root(t), f))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(d), "`ROOT/netcheck.sh` (written by `canary-prepare.sh --pass B`) starts pasta, bubblewrap and a throwaway user unit and makes four unauthenticated requests, and is run only by the owner's hand, never by an agent") {
			t.Errorf("%s does not name netcheck.sh as owner-only", f)
		}
	}
}

// F7: the README and the slice guide do not say that show leaves out the
// environment.
func TestCanaryRunKitShowEnvNamesDocs(t *testing.T) {
	for _, f := range []string{"qualification/canary/run/README.md", "docs/canary-slice.md"} {
		d, err := os.ReadFile(filepath.Join(root(t), f))
		if err != nil {
			t.Fatal(err)
		}
		s := string(d)
		if strings.Contains(s, "leaves out the agent's environment") || strings.Contains(s, "never prints the agent's environment") {
			t.Errorf("%s says that show leaves out the agent's environment", f)
		}
		if !strings.Contains(s, "agent_env_names") {
			t.Errorf("%s does not name agent_env_names", f)
		}
	}
}
