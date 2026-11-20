package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests of the fixes to the run script's pass B found by the pre-mortem: the
// not-now continuation of step 17 and the owner's pre-checks of step 16, the
// wording of the message, the login of step 15, the lows (listeners, the empty
// manifest, the search pattern, the revocation text) and step 25's judgement of
// the real config folder. The steps run for real under the script's own
// helpers with stubs for what the owner or the host does; nothing starts a
// unit, a sandbox or a console.

// A not-now with nothing written leads to the owner-supplies path in pass B,
// as in pass A's first epoch, whatever the reason; a not-now with something
// written, or unknown, does not.
func TestCanaryRunKitPassBNotNowSuppliesTheMessage(t *testing.T) {
	for _, r := range canaryReasons {
		got, code := canarySelfTest(t, "inject-action", "B", "not-now", r, "nothing")
		if code != 0 || got != "owner-supplies" {
			t.Errorf("pass B, not-now/%s/nothing: the action is %q (exit %d), want owner-supplies", r, got, code)
		}
		for _, w := range []string{"partial", "whole", "unknown"} {
			if got, _ := canarySelfTest(t, "inject-action", "B", "not-now", r, w); got != "decide" {
				t.Errorf("pass B, not-now/%s/%s: the action is %q, want decide", r, w, got)
			}
		}
	}
	for _, w := range canaryWritten {
		if got, _ := canarySelfTest(t, "inject-action", "B", "refused", "untested", w); got != "owner-supplies" {
			t.Errorf("pass B, refused/untested/%s: the action is %q, want owner-supplies", w, got)
		}
	}
	// Pass A keeps its own table.
	if got, _ := canarySelfTest(t, "inject-action", "A", "not-now", "not-ready", "nothing"); got != "owner-supplies" {
		t.Errorf("pass A, not-now/not-ready/nothing: %q", got)
	}
	if got, _ := canarySelfTest(t, "inject-action", "A", "not-now", "blocked", "nothing"); got != "unexpected" {
		t.Errorf("pass A, not-now/blocked/nothing: %q, want unexpected", got)
	}

	// The arm says what the fence did and what to clear first.
	body := canaryFuncs(t, canaryScriptText(t))["step_17"]
	i := strings.Index(body, "\n    owner-supplies)\n")
	if i < 0 {
		t.Fatal("step 17 has no owner-supplies arm")
	}
	arm := body[i:]
	arm = arm[:strings.Index(arm, "\n      ;;\n")]
	for _, want := range []string{"The fence wrote nothing ($_o,", "On not-now", "first clear what the pane shows", "a dialog", "update or install notice", "text in the composer", "G3 fallback"} {
		if !strings.Contains(arm, want) {
			t.Errorf("the owner-supplies arm does not say %q:\n%s", want, arm)
		}
	}
}

// Step 16 ends with the owner's pre-checks, a yes, and names the mode the
// config expects; step 14 of pass B says what the window size means.
func TestCanaryRunKitPassBStep16PreChecks(t *testing.T) {
	out, code := canaryDry(t, "--pass", "B", "--config", "/nonexistent/sample/canary.json")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	var sixteen, fourteen string
	for _, b := range canaryBlocks(out) {
		switch {
		case strings.HasPrefix(b.heading, "== step 16 [pass B]: "):
			sixteen = b.heading + "\n" + strings.Join(b.lines, "\n")
		case strings.HasPrefix(b.heading, "== step 14 [pass B]: "):
			fourteen = b.heading + "\n" + strings.Join(b.lines, "\n")
		}
	}
	for _, want := range []string{
		"the pane must show the EMPTY composer and nothing else",
		"no dialog",
		"the LAST row reads exactly the mode the config expects (manual)",
		"an update or install notice there means not-now",
		"Shift+Tab",
		"leave the session pane (Ctrl+\\) and touch neither key nor mouse there for 3 seconds",
		"does the pane show the empty composer, the expected mode and no notice (yes; or go-on:",
	} {
		if !strings.Contains(sixteen, want) {
			t.Errorf("step 16 of pass B does not say %q:\n%s", want, sixteen)
		}
	}
	for _, want := range []string{"120 columns by 36 rows", "composer", "footer", "A resize redraws"} {
		if !strings.Contains(fourteen, want) {
			t.Errorf("step 14 of pass B does not say %q:\n%s", want, fourteen)
		}
	}
	if strings.Contains(fourteen, "stand-in") {
		t.Errorf("step 14 of pass B talks of the stand-in's prompt:\n%s", fourteen)
	}
	// The pre-check is the last thing step 16 asks: the owner's yes is nearest to step 17.
	f := funcsOf(t)["step_16"]
	if a, l := strings.Index(f, "ask 'Owner: does the pane show the empty composer"), strings.LastIndex(f, "ask 'Lead:"); a < 0 || a < l {
		t.Error("step 16 does not end with the owner's yes to the pre-checks")
	}
	// Pass A does not run step 16 and its step 14 keeps its own text.
	outA, _ := canaryDry(t, "--pass", "A", "--config", "/nonexistent/sample/canary.json")
	if strings.Contains(outA, "EMPTY composer") {
		t.Error("pass A prints the pass B pre-checks")
	}
	if !strings.Contains(outA, "above the stand-in's prompt") {
		t.Error("pass A's step 14 lost its text")
	}
}

func funcsOf(t *testing.T) map[string]string {
	t.Helper()
	return canaryFuncs(t, canaryScriptText(t))
}

// The message tells a live agent to write the value only, keeps the label line
// the stand-in and the daemon's admission use, and stays within the admission's
// limits (2048 bytes, printable text or a newline).
func TestCanaryRunKitMessageWording(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "ev"), 0o700); err != nil {
		t.Fatal(err)
	}
	pre := "PASS=B STAMP=20260101T000000Z EPOCH_NO=1 BRANCH=canary/one"
	o, code := canaryStepHarness(t, dir, []string{"make_message"}, pre, "make_message")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, o)
	}
	data, err := os.ReadFile(filepath.Join(dir, "ev", "message-1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if len(text) > 2048 {
		t.Errorf("the body is %d bytes, the admission's limit is 2048", len(text))
	}
	for _, r := range text {
		if r != '\n' && (r < 0x20 || r > 0x7e) {
			t.Errorf("the body holds a character that is not printable ASCII: %q", r)
		}
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 2 || lines[1] != "canary-token: canary-20260101t000000z-1" {
		t.Errorf("the body is not the instruction and the label line:\n%s", text)
	}
	for _, want := range []string{
		"Create the file canary-token.txt in the top folder of this repository.",
		"exactly one line",
		"the value after the label on the next line of this message",
		"the value only, without the label",
		"then one line feed",
		"Create the branch canary/one, commit that one file on it, and do nothing else.",
	} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("the instruction does not say %q:\n%s", want, lines[0])
		}
	}
	if strings.Contains(text, "the token below") {
		t.Errorf("the body still uses the ambiguous wording:\n%s", text)
	}
	if strings.Count(text, "canary-token: ") != 1 {
		t.Errorf("the label appears other than once:\n%s", text)
	}
}

// When the gate fails, the owner and the lead see the first 3 lines the file
// holds, and nothing else; the gate itself stays an exact line check.
func TestCanaryRunKitStep20ShowsTheFileAfterAFailedGate(t *testing.T) {
	text := canaryScriptText(t)
	if !strings.Contains(text, `--gate "token=grep -qxF $TOKEN canary-token.txt"`) {
		t.Error("the gate is not the exact-line check any more")
	}
	if strings.Count(text, "--gate ") != 1 {
		t.Error("the script has another gate than the one of step 20")
	}
	dir := t.TempDir()
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(tree, 0o700); err != nil {
		t.Fatal(err)
	}
	canaryWrite(t, tree, "canary-token.txt", "canary-token: canary-x-1\n\nline three\nline four SECRET-FOURTH\n\x1b]0;title\x07")
	o, code := canaryStepHarness(t, dir, []string{"out", "show_token_file"}, "TREE="+tree, "show_token_file")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, o)
	}
	for _, want := range []string{"the gate failed", "first 3 lines", "     | canary-token: canary-x-1\n", "     | \n", "     | line three\n"} {
		if !strings.Contains(o, want) {
			t.Errorf("the output does not hold %q:\n%s", want, o)
		}
	}
	if strings.Contains(o, "SECRET-FOURTH") || strings.Contains(o, "\x1b") {
		t.Errorf("the output holds more than 3 lines or an escape byte:\n%q", o)
	}
	// No file: it says so.
	if err := os.Remove(filepath.Join(tree, "canary-token.txt")); err != nil {
		t.Fatal(err)
	}
	o, _ = canaryStepHarness(t, dir, []string{"out", "show_token_file"}, "TREE="+tree, "show_token_file")
	if !strings.Contains(o, "no canary-token.txt") {
		t.Errorf("a missing file is not said:\n%s", o)
	}
	body := funcsOf(t)["step_20"]
	if !strings.Contains(body, `if [ "$DRY" -eq 0 ] && [ "$RC" -ne 0 ]; then show_token_file; fi`) {
		t.Error("step 20 does not show the file after a failed gate")
	}
}

// Step 15: the login's way out of the pane, the failed answer, and the list of
// changed files when the folder was empty before.
func TestCanaryRunKitStep15Login(t *testing.T) {
	out, _ := canaryDry(t, "--pass", "B", "--config", "/nonexistent/sample/canary.json")
	var fifteen string
	for _, b := range canaryBlocks(out) {
		if strings.HasPrefix(b.heading, "== step 15 [pass B]: ") {
			fifteen = strings.Join(b.lines, "\n")
		}
	}
	for _, want := range []string{"logged-in", "failed", "clipboard requests are dropped", "hyperlinks are not drawn", "no selection", "Ctrl+\\", "two lines", "line break", "timer has started already"} {
		if !strings.Contains(fifteen, want) && !strings.Contains(funcsOf(t)["step_15"], want) {
			t.Errorf("step 15 does not say %q", want)
		}
	}

	run := func(t *testing.T, answer string) (string, int) {
		dir := t.TempDir()
		ev := filepath.Join(dir, "ev")
		if err := os.MkdirAll(ev, 0o700); err != nil {
			t.Fatal(err)
		}
		pre := `PASS=B LCONFIG=/x CFG_SHOW=agent_argv_0=a
step_head() { :; }
affirm() { return 0; }
wait_key() { :; }
result() { echo "RESULT: $*"; }
done_step() { echo DONE-STEP; }
ask() { ANSWER=` + answer + `; }
manifest_of() { case "$2" in *before*) : >"$2" ;; *) printf 'x 1 2 ./a\nx 1 2 ./b c\n' >"$2" ;; esac; }`
		return canaryStepHarness(t, dir, []string{"out", "note", "dry", "fail", "now", "step_15"}, pre, "step_15")
	}
	o, code := run(t, "logged-in")
	if code != 0 || !strings.Contains(o, "DONE-STEP") {
		t.Fatalf("exit status %d:\n%s", code, o)
	}
	// The awk lists the files although the "before" manifest is empty.
	if !strings.Contains(o, "     ./a\n") || !strings.Contains(o, "     ./b c\n") {
		t.Errorf("the changed files are not listed against an empty manifest:\n%s", o)
	}
	o, _ = run(t, "failed")
	if strings.Contains(o, "DONE-STEP") || !strings.Contains(o, "FAILED at step 1: the login did not succeed") {
		t.Errorf("the answer failed does not end the step:\n%s", o)
	}
}

// Step 6 asks once more before it fails on a listener that came or went.
func TestCanaryRunKitStep6ListsAgainBeforeFailing(t *testing.T) {
	run := func(t *testing.T, third string) (string, int) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "ev"), 0o700); err != nil {
			t.Fatal(err)
		}
		pre := `PASS=B RESAMPLE_SECONDS=0 N=0
step_head() { :; }
wait_key() { :; }
poll_until() { return 0; }
result() { echo "RESULT: $*"; }
done_step() { echo DONE-STEP; }
ask() { ANSWER=none; }
listeners() {
  N=$((N + 1))
  case $N in
    1) printf 'tcp 0.0.0.0:22\n' >"$2" ;;
    2) printf 'tcp 0.0.0.0:22\nudp 0.0.0.0:5353\n' >"$2" ;;
    *) printf '` + third + `' >"$2" ;;
  esac
  echo "listed:$N"
}`
		return canaryStepHarness(t, dir, []string{"out", "note", "dry", "fail", "now", "check", "is_eq", "step_6"}, pre, "step_6")
	}
	o, code := run(t, "tcp 0.0.0.0:22\\n")
	if code != 0 || !strings.Contains(o, "DONE-STEP") || !strings.Contains(o, "listed:3") || strings.Contains(o, "FAILED") {
		t.Errorf("a socket that went away again fails the step (exit %d):\n%s", code, o)
	}
	o, _ = run(t, "tcp 0.0.0.0:22\\nudp 0.0.0.0:9999\\n")
	if strings.Contains(o, "DONE-STEP") || !strings.Contains(o, "FAILED at step 1") || !strings.Contains(o, "     + udp 0.0.0.0:9999") || !strings.Contains(o, "listed:3") {
		t.Errorf("a socket that stays does not fail the step, with its name:\n%s", o)
	}
}

// Step 26 also looks for this runtime's token and code shapes; step 23 names
// the login to revoke by its time and warns about the other rows.
func TestCanaryRunKitStep23And26(t *testing.T) {
	f := funcsOf(t)
	for _, want := range []string{"sk-ant-", "[?&]code=", "[A-Za-z0-9_-]{20,}#[A-Za-z0-9_-]{20,}", "https?://", "[A-Z0-9]{4}-[A-Z0-9]{4}"} {
		if !strings.Contains(f["step_26"], want) {
			t.Errorf("step 26's search does not hold %q", want)
		}
	}
	dir := t.TempDir()
	pre := `PASS=B STAMP=20260101T000000Z LOGIN_AT=2026-01-01T00:10:00Z IN_CHAIN=0
step_head() { :; }
was_done() { return 1; }
ask() { ANSWER=revoked; }
result() { echo "RESULT: $*"; }
done_step() { echo DONE-STEP; }`
	o, code := canaryStepHarness(t, dir, []string{"out", "note", "dry", "fail", "step_23"}, pre, "step_23")
	if code != 0 || !strings.Contains(o, "DONE-STEP") {
		t.Fatalf("exit status %d:\n%s", code, o)
	}
	for _, want := range []string{"started at 20260101T000000Z", "done at about 2026-01-01T00:10:00Z (UTC)", "In your local time that is about", "Revoke ONLY the login", "no other row", "log out everywhere", "a wrong row ends them"} {
		if !strings.Contains(o, want) {
			t.Errorf("step 23 does not say %q:\n%s", want, o)
		}
	}
}

// Step 25 counts what the owner's own running sessions write and judges only
// the files of the login's kind, at the top of the real config folder.
func TestCanaryRunKitStep25JudgesOnlyTheLoginsFiles(t *testing.T) {
	const before = "h1 10 100 ./.credentials.json\nh2 10 100 ./settings.json\nh3 10 100 ./projects/a/one.jsonl\nh4 10 100 ./projects/a/two.jsonl\nh5 10 100 ./history.jsonl\n"
	noise := "h1 10 100 ./.credentials.json\nh2 10 100 ./settings.json\nh3x 11 200 ./projects/a/one.jsonl\nh5x 12 200 ./history.jsonl\nh6 5 200 ./projects/b/new.jsonl\n"
	cases := []struct {
		name, after string
		findings    int
		want        []string
	}{
		{"nothing", before, 0, []string{"RESULT: no difference"}},
		{"only the owner's sessions", noise, 0, []string{"1 added, 1 removed, 2 changed", "no file of the login's kind differs", "     changed ./history.jsonl"}},
		{"the credential file, the canary's own", strings.Replace(noise, "h1 10 100", "hc 12 300", 1), 1, []string{"MEANINGFUL: changed ./.credentials.json", "of the login's kind"}},
		{"a new state file, the canary's own", noise + "hc 5 5 ./.claude.json\n", 1, []string{"MEANINGFUL: added ./.claude.json"}},
		{"a changed file that is not the canary's is a question, answered yes", strings.Replace(noise, "h1 10 100", "h1y 12 300", 1), 0, []string{"changed, not the canary's file", "ASKED:Owner and lead: is the change of ./.credentials.json"}},
		{"a token file removed is a question, answered no", strings.Replace(noise, "h2 10 100 ./settings.json\n", "", 1), 1, []string{"changed, not the canary's file", "ASKED:Owner and lead: is the change of ./settings.json"}},
		{"a new transcript of the sandbox's working folder", noise + "h7 5 5 ./projects/-work-throwaway/s.jsonl\n", 1, []string{"MEANINGFUL: added ./projects/-work-throwaway/s.jsonl"}},
		{"deep names are not judged", noise + "t1 5 5 ./projects/a/oauth-token-notes.txt\n", 0, []string{"2 added, 1 removed, 2 changed"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			ev := filepath.Join(dir, "ev")
			canaryWrite(t, ev, "manifest-before.txt", before)
			canaryWrite(t, ev, "live-config-folder.txt", "/x\n")
			after := canaryWrite(t, dir, "after.txt", c.after)
			canaryWrite(t, ev, "canary-config-after.txt", "hc 1 1 ./.credentials.json\n"+canaryEmptyHash+" 0 1 ./empty\n")
			answer := "yes"
			if strings.Contains(c.name, "answered no") {
				answer = "no"
			}
			pre := `PASS=B
ask() { ANSWER=` + answer + `; echo "ASKED:$1"; }
step_head() { :; }
wait_key() { :; }
manifest_of() { cp ` + after + ` "$2"; }
result() { echo "RESULT: $*"; }
done_step() { echo DONE-STEP; }`
			o, code := canaryStepHarness(t, dir, []string{"out", "note", "dry", "fail", "manifest_changes", "meaningful_path", "canary_owns", "step_25"}, pre, "step_25")
			if code != 0 || !strings.Contains(o, "DONE-STEP") {
				t.Fatalf("exit status %d:\n%s", code, o)
			}
			if !strings.Contains(o, "findings="+string(rune('0'+c.findings))) {
				t.Errorf("findings are not %d:\n%s", c.findings, o)
			}
			for _, want := range c.want {
				if !strings.Contains(o, want) {
					t.Errorf("the output does not hold %q:\n%s", want, o)
				}
			}
			if c.findings == 0 && strings.Contains(o, "MEANINGFUL") {
				t.Errorf("a file of no meaning is judged:\n%s", o)
			}
		})
	}
}

// The preflight names the pass the root was made for.
func TestCanaryRunKitPreflightNamesThePass(t *testing.T) {
	for _, c := range []struct{ show, pass string }{
		{"agent_kind=stand-in\n", "A"},
		{"project=throwaway-one\n", "<A or B>"},
	} {
		p := newCanaryPF(t)
		canaryWrite(t, p.dir, "show.out", "holder_runtime_dir="+p.root+"/runtime/leads/throwaway-one-lead\n"+c.show)
		out, code := p.run()
		if code != 0 {
			t.Fatalf("%q: exit status %d:\n%s", c.show, code, out)
		}
		if want := "canary-run.sh --pass " + c.pass + " --config "; !strings.Contains(out, want) {
			t.Errorf("%q: the output does not hold %q:\n%s", c.show, want, out)
		}
	}
	// A live root (the live checks of the preflight need what passBPF sets up).
	p, _ := passBPF(t)
	out, code := p.run()
	if want := "canary-run.sh --pass B --config "; code != 0 || !strings.Contains(out, want) {
		t.Errorf("live: exit status %d, the output does not hold %q:\n%s", code, want, out)
	}
}
