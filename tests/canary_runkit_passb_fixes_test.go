package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Tests of the fixes to the run script's pass B after the review of the first
// fixes: step 16's way to go on, step 15's asking again, step 20's single
// open of the file, step 23's local time, step 25's judgement of a changed
// credential and of the sandbox's working folder, and step 26's search term.

const canaryEmptyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// Step 16: yes goes on, go-on goes on to the paste path, anything else ends
// the pass. The case statement of the step runs as it is written.
func TestCanaryRunKitPassBStep16Answers(t *testing.T) {
	f := funcsOf(t)["step_16"]
	i := strings.Index(f, "ask 'Owner: does the pane show")
	if i < 0 {
		t.Fatal("step 16 has no owner question")
	}
	tail := f[i:]
	if !strings.Contains(tail[:strings.Index(tail, "\n")], "go-on") {
		t.Error("the question does not name go-on")
	}
	j := strings.Index(tail, "case $ANSWER in")
	k := strings.Index(tail, "\n  esac\n")
	if j < 0 || k < 0 {
		t.Fatalf("step 16 has no case on the answer:\n%s", tail)
	}
	snippet := tail[j : k+len("\n  esac\n")]
	for _, c := range []struct {
		answer string
		cont   bool
	}{{"yes", true}, {"go-on", true}, {"y", false}, {"", false}, {"no", false}} {
		script := "DRY=0\nnote() { :; }\nfail() { echo \"FAILED: $*\"; }\nf() {\nANSWER='" + c.answer + "'\n" + snippet + "echo CONTINUED\n}\nf\n"
		o, err := exec.Command("sh", "-c", script).CombinedOutput()
		if err != nil {
			t.Fatalf("%q: %v\n%s", c.answer, err, o)
		}
		got := string(o)
		if strings.Contains(got, "CONTINUED") != c.cont || strings.Contains(got, "FAILED: the pane was not made ready") == c.cont {
			t.Errorf("answer %q: want continued=%v:\n%s", c.answer, c.cont, got)
		}
	}
}

// Step 15 asks again until the answer is logged-in or failed.
func TestCanaryRunKitStep15AsksAgain(t *testing.T) {
	dir := t.TempDir()
	ev := filepath.Join(dir, "ev")
	if err := os.MkdirAll(ev, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		answers string
		done    bool
		asks    int
	}{{"logged-in", true, 1}, {"done logged-in-x logged-in", true, 3}, {"oops failed", false, 2}, {"", false, 1}} {
		pre := `PASS=B LCONFIG=/x CFG_SHOW=agent_argv_0=a N=0 ANSWERS='` + c.answers + `'
step_head() { :; }
affirm() { return 0; }
wait_key() { :; }
result() { echo "RESULT: $*"; }
done_step() { echo DONE-STEP; }
manifest_of() { : >"$2"; }
ask() {
  N=$((N + 1)); echo "ASK$N"
  if [ -z "$ANSWERS" ]; then ANSWER=; _askok=0; return 0; fi
  set -- $ANSWERS; ANSWER=$1; shift; ANSWERS=$*; _askok=1
}`
		o, code := canaryStepHarness(t, dir, []string{"out", "note", "dry", "fail", "now", "step_15"}, pre, "step_15")
		if code != 0 {
			t.Fatalf("%q: exit status %d:\n%s", c.answers, code, o)
		}
		if got := strings.Contains(o, "DONE-STEP"); got != c.done {
			t.Errorf("%q: done=%v, want %v:\n%s", c.answers, got, c.done, o)
		}
		if n := strings.Count(o, "ASK"); n != c.asks {
			t.Errorf("%q: asked %d times, want %d:\n%s", c.answers, n, c.asks, o)
		}
	}
}

// show_token_file: the filter for bytes that are not printable is what the
// test exercises (the escape byte sits on the first line); a symlink and a
// FIFO are never read, and a FIFO does not hang the step.
func TestCanaryRunKitShowTokenFileFilterAndOpen(t *testing.T) {
	show := func(t *testing.T, tree string) string {
		dir := t.TempDir()
		o, code := canaryStepHarness(t, dir, []string{"out", "show_token_file"}, "TREE="+tree, "show_token_file")
		if code != 0 {
			t.Fatalf("exit status %d:\n%s", code, o)
		}
		return o
	}
	tree := t.TempDir()
	canaryWrite(t, tree, "canary-token.txt", "canary-token: canary-x-1\x1b[2J\nline two\n")
	o := show(t, tree)
	if !strings.Contains(o, "     | canary-token: canary-x-1?[2J\n") || strings.Contains(o, "\x1b") {
		t.Errorf("an escape byte on the first line is not turned into a question mark:\n%q", o)
	}
	if !strings.Contains(o, "working file") {
		t.Errorf("the text does not say it is the working file:\n%s", o)
	}

	// A symlink is not followed.
	link := t.TempDir()
	secret := canaryWrite(t, t.TempDir(), "secret.txt", "HOST-SECRET\n")
	if err := os.Symlink(secret, filepath.Join(link, "canary-token.txt")); err != nil {
		t.Fatal(err)
	}
	if o := show(t, link); strings.Contains(o, "HOST-SECRET") || !strings.Contains(o, "no canary-token.txt") {
		t.Errorf("a symlink is read or not refused:\n%s", o)
	}

	// A FIFO does not hang and is not read.
	fifo := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(fifo, "canary-token.txt"), 0o600); err != nil {
		t.Skip("no mkfifo")
	}
	if o := show(t, fifo); !strings.Contains(o, "no canary-token.txt") {
		t.Errorf("a FIFO is not refused:\n%s", o)
	}
}

// The step opens once: no test of the name comes before the open.
func TestCanaryRunKitShowTokenFileOpensOnce(t *testing.T) {
	body := funcsOf(t)["show_token_file"]
	if strings.Contains(body, `[ -f "$_stf" ]`) || strings.Contains(body, `[ ! -L "$_stf" ]`) {
		t.Errorf("show_token_file checks the name before it opens:\n%s", body)
	}
	for _, want := range []string{"timeout 2", "exec 3<", "/proc/self/fd/3", "tr -c '[:print:]"} {
		if !strings.Contains(body, want) {
			t.Errorf("show_token_file does not hold %q", want)
		}
	}
}

// Step 23 names the time as about, in UTC and in the local time.
func TestCanaryRunKitStep23LocalTime(t *testing.T) {
	dir := t.TempDir()
	pre := `TZ=UTC
export TZ
PASS=B STAMP=20260101T000000Z LOGIN_AT=2026-01-01T00:10:00Z IN_CHAIN=0
step_head() { :; }
was_done() { return 1; }
ask() { ANSWER=revoked; }
result() { echo "RESULT: $*"; }
done_step() { echo DONE-STEP; }`
	o, code := canaryStepHarness(t, dir, []string{"out", "note", "dry", "fail", "step_23"}, pre, "step_23")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, o)
	}
	for _, want := range []string{"done at about 2026-01-01T00:10:00Z (UTC)", "In your local time that is about 2026-01-01 00:10:00 UTC"} {
		if !strings.Contains(o, want) {
			t.Errorf("step 23 does not say %q:\n%s", want, o)
		}
	}
}

// Step 25: the hash of an empty file is no proof, nor is a missing canary
// manifest, and a matching hash is proof.
func TestCanaryRunKitStep25EmptyAndMissingProof(t *testing.T) {
	for _, c := range []struct {
		name, after, canary string
		findings            int
	}{
		{"empty hash on both sides", canaryEmptyHash + " 0 2 ./.credentials.json\n", canaryEmptyHash + " 0 1 ./.credentials.json\n", 1},
		{"no canary manifest", "hc 1 1 ./.credentials.json\n", "", 1},
		{"a matching hash", "hc 1 1 ./.credentials.json\n", "hc 1 1 ./other\n", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			ev := filepath.Join(dir, "ev")
			canaryWrite(t, ev, "manifest-before.txt", "h0 1 1 ./.credentials.json\n")
			canaryWrite(t, ev, "live-config-folder.txt", "/x\n")
			if c.canary != "" {
				canaryWrite(t, ev, "canary-config-after.txt", c.canary)
			}
			a := canaryWrite(t, dir, "after.txt", c.after)
			pre := `PASS=B
step_head() { :; }
wait_key() { :; }
ask() { ANSWER=no; }
manifest_of() { cp ` + a + ` "$2"; }
result() { echo "RESULT: $*"; }
done_step() { echo DONE-STEP; }`
			o, code := canaryStepHarness(t, dir, []string{"out", "note", "dry", "fail", "manifest_changes", "meaningful_path", "canary_owns", "step_25"}, pre, "step_25")
			if code != 0 {
				t.Fatalf("exit status %d:\n%s", code, o)
			}
			// The owner answers no to the question, so every case is a finding;
			// what differs is whether the question is asked.
			asked := strings.Contains(o, "changed, not the canary's file")
			wantAsked := c.name != "a matching hash"
			if asked != wantAsked {
				t.Errorf("question asked=%v, want %v:\n%s", asked, wantAsked, o)
			}
			if !strings.Contains(o, "findings="+string(rune('0'+c.findings))) {
				t.Errorf("findings are not %d:\n%s", c.findings, o)
			}
		})
	}
}

// Step 26's term matches a login URL's query form and not the kit's own lines.
func TestCanaryRunKitStep26CodeTerm(t *testing.T) {
	body := funcsOf(t)["step_26"]
	if !strings.Contains(body, "[?&]code=") || strings.Contains(body, "|code=") {
		t.Fatalf("step 26 does not use the query form of the code term:\n%s", body)
	}
	i := strings.Index(body, "grep -rnIE '")
	if i < 0 {
		t.Fatal("step 26 has no search")
	}
	pat := body[i+len("grep -rnIE '"):]
	pat = pat[:strings.Index(pat, "'")]
	dir := t.TempDir()
	canaryWrite(t, dir, "hit.txt", "callback?state=1&code=zz\nnext?code=zz\n")
	canaryWrite(t, dir, "miss.txt", "exit.code=0\nUnit exited, code=exited, status=0\n")
	o, _ := exec.Command("grep", "-rnIE", pat, dir).CombinedOutput()
	got := string(o)
	if !strings.Contains(got, "hit.txt:1:") || !strings.Contains(got, "hit.txt:2:") {
		t.Errorf("the query forms are not matched:\n%s", got)
	}
	if strings.Contains(got, "miss.txt") {
		t.Errorf("the kit's own lines are matched:\n%s", got)
	}
}
