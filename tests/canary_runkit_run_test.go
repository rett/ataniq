package tests

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests of the run script's second-epoch viewer ticket (step 14), the wait of
// step 12 for the holder's second write of holder.json, the text printed to
// compare with a paste, and step 9's hash comparison. The steps run for real
// (outside --dry) under the script's own helpers, with stubs for what the
// owner or the host does: the commands are recorded stubs in a temporary
// folder, and nothing starts a unit, a sandbox or a console.

// canaryStepHarness runs one step function of the script with the real
// helpers named and the stubs of pre, in a temporary folder with a tools
// folder as the only PATH (bin holds the stubs written by the caller). It
// returns the output and the exit status.
func canaryStepHarness(t *testing.T, dir string, real []string, pre, call string) (string, int) {
	t.Helper()
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	script := canaryScriptText(t)
	var b strings.Builder
	b.WriteString("DRY=0 PASS=A CUR=1 EPOCH_NO=1 IN_CHAIN=1 FINDINGS=0 LOG= RC=0 LASTOUT= LASTERR= EV=" + filepath.Join(dir, "ev") + "\n")
	for _, name := range real {
		b.WriteString(canaryDef(t, script, name))
	}
	b.WriteString(pre + "\n" + call + "\necho findings=$FINDINGS\n")
	cmd := exec.Command(sh, "-c", b.String())
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + filepath.Join(dir, "bin") + ":/usr/bin:/bin", "HOME=" + dir}
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

func canaryStub(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o700); err != nil {
		t.Fatal(err)
	}
}

// Step 14: the first epoch's ticket is a writer's, the second epoch's a
// viewer's (a writer's attach resizes the session, a viewer's does not).
func TestCanaryRunKitStep14MintsWriterThenViewer(t *testing.T) {
	// The dry run names the role of each epoch, and the heading says so.
	out, code := canaryDry(t, "--pass", "A", "--config", "/nonexistent/sample/canary.json")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, out)
	}
	second := strings.Index(out, "== step 14 [pass A, second epoch]")
	first := strings.Index(out, "== step 14 [pass A]")
	if first < 0 || second < first {
		t.Fatalf("the dry run lacks step 14 in both epochs:\n%s", out)
	}
	e1, e2 := out[first:second], out[second:]
	if i := strings.Index(e2, "== step"); i >= 0 {
		if j := strings.Index(e2[i+1:], "== "); j >= 0 {
			e2 = e2[:i+1+j]
		}
	}
	if !strings.Contains(e1, "canary ticket --config /nonexistent/sample/canary.json --attach-role writer") || strings.Contains(e1, "--attach-role viewer") {
		t.Errorf("the first epoch does not mint a writer ticket:\n%s", e1)
	}
	if !strings.Contains(e2, "canary ticket --config /nonexistent/sample/canary.json --attach-role viewer") || strings.Contains(e2, "--attach-role writer") {
		t.Errorf("the second epoch does not mint a viewer ticket:\n%s", e2)
	}
	for _, want := range []string{"mint a writer ticket", "read-only VIEWER"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not say %q", want)
		}
	}
	for _, want := range []string{"mint a viewer ticket", "you only WATCH"} {
		if !strings.Contains(out, want) {
			t.Errorf("the dry run does not say %q", want)
		}
	}
	// The first epoch tells the owner about the window, pasta's two lines.
	for _, want := range []string{"at least 100 columns by 30 rows", "pasta may write two lines", "AVX2", "nameserver"} {
		if !strings.Contains(e1, want) {
			t.Errorf("the first epoch's step 14 does not say %q:\n%s", want, e1)
		}
	}
	if strings.Contains(e2, "at least 100 columns") {
		t.Errorf("the second epoch tells a viewer about the window's limits:\n%s", e2)
	}

	// A real run of the step with a stub ataniqd that records its arguments.
	for _, c := range []struct {
		epoch int
		role  string
		two   bool
	}{{1, "writer", true}, {2, "viewer", false}} {
		t.Run(c.role, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.MkdirAll(filepath.Join(dir, "ev", "out"), 0o700); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(dir, "calls")
			canaryStub(t, filepath.Join(dir, "bin"), "ataniqd", "echo \"$@\" >>"+calls+"\n")
			pre := `BIN=` + filepath.Join(dir, "bin") + ` CONFIG=/x/canary.json HRUN=` + dir + `/run HSTATE=` + dir + `/state EPOCH_NO=` + fmt.Sprint(c.epoch) + `
step_head() { echo "HEAD: $2"; }
wait_key() { :; }
affirm() { return 0; }
poll_until() { return 0; }
ticket_taken() { return 0; }
count_lines() { echo 3; }
fld() { printf '%s' "$HRUN/ticket"; }
result() { :; }
done_step() { :; }`
			o, code := canaryStepHarness(t, dir, []string{"out", "note", "dry", "fail", "runcap", "check", "is_eq", "is_rc", "step_14"}, pre, "step_14; echo STEP-PASSED")
			if code != 0 || !strings.Contains(o, "STEP-PASSED") || !strings.Contains(o, "findings=0") {
				t.Fatalf("exit status %d:\n%s", code, o)
			}
			got, _ := os.ReadFile(calls)
			if want := "canary ticket --config /x/canary.json --attach-role " + c.role + "\n"; string(got) != want {
				t.Errorf("ataniqd was run with %q, want %q", got, want)
			}
			if !strings.Contains(o, "HEAD: Attach the console, then mint a "+c.role+" ticket") {
				t.Errorf("the heading does not name the %s ticket:\n%s", c.role, o)
			}
			if c.two != strings.Contains(o, "the event log shows no second attach") {
				t.Errorf("the second console's check is not as the epoch %d wants (tried in the first epoch only):\n%s", c.epoch, o)
			}
		})
	}

	// The count of the attach events does not depend on the role.
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	canaryStub(t, filepath.Join(work, "build", "canary"), "ataniq-canary", "cat >/dev/null\nprintf 'event=attach\\nreason=viewer\\n'\n")
	events := filepath.Join(dir, "events.jsonl")
	if err := os.WriteFile(events, []byte("{}\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := canaryCount(t, work, dir, events, "event=attach"); got != "2" {
		t.Errorf("two viewer attaches count %q, want 2", got)
	}
}

// Step 12 waits for the holder's second write of holder.json (the epoch and
// sandbox_started true), or the end of the unit, and prints the journal when
// it gives up.
func TestCanaryRunKitStepTwelveWaitsForTheSecondWrite(t *testing.T) {
	text := canaryScriptText(t)
	for _, want := range []string{"\nWAIT_TRIES=60\n", "\nWAIT_INTERVAL=1\n", "_hwmax=$((WAIT_TRIES * 3 / 2))", `"$HSTATE/holder.json" sandbox_started`} {
		if !strings.Contains(text, want) {
			t.Errorf("the script does not hold %q (90 asks, one second apart: at least 70 s)", want)
		}
	}
	// Step 12 waits through holder_started, which shows the journal.
	funcs := canaryFuncs(t, text)
	if !strings.Contains(funcs["step_12"], "holder_started || return 0") || strings.Count(funcs["holder_started"], "show_unit_journal") != 2 {
		t.Error("step 12 does not wait through holder_started, which shows the journal before each failure")
	}

	const (
		started  = "epoch=2\nsandbox_started=true\nwaiver=x\n"
		firstOne = "epoch=2\n"
	)
	run := func(t *testing.T, state, holder string, later string) (string, string) {
		t.Helper()
		dir := t.TempDir()
		hs := filepath.Join(dir, "hs")
		if err := os.MkdirAll(hs, 0o700); err != nil {
			t.Fatal(err)
		}
		if holder != "" {
			if err := os.WriteFile(filepath.Join(hs, "holder.json"), []byte(holder), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		tools := filepath.Join(dir, "tools")
		calls := filepath.Join(dir, "calls")
		canaryStub(t, tools, "systemctl", "echo "+state+"\n")
		canaryStub(t, tools, "journalctl", "echo \"$*\" >>"+calls+"\necho journal-line\n")
		canaryStub(t, filepath.Join(dir, "build", "canary"), "ataniq-canary", `[ "$1" = fields ] && cat "$3"`+"\n")
		for _, tool := range []string{"awk", "date", "sed", "cat", "printf", "sleep"} {
			if real, err := exec.LookPath(tool); err == nil {
				os.Symlink(real, filepath.Join(tools, tool))
			}
		}
		if later != "" { // the holder's second write, after a moment
			go func() {
				time.Sleep(400 * time.Millisecond)
				os.WriteFile(filepath.Join(hs, "holder.json.new"), []byte(later), 0o600)
				os.Rename(filepath.Join(hs, "holder.json.new"), filepath.Join(hs, "holder.json"))
			}()
		}
		cmd := exec.Command("/bin/sh", filepath.Join(root(t), canaryRunScript), "--self-test", "holder-wait", hs, "2", "ataniq-lead-throwaway-one.service")
		cmd.Dir = dir
		cmd.Env = []string{"PATH=" + tools, "HOME=" + dir, "CANARY_RUN_WAIT_TRIES=10", "CANARY_RUN_WAIT_INTERVAL=0.1"}
		o, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v\n%s", err, o)
		}
		asked, _ := os.ReadFile(calls)
		return string(o), string(asked)
	}
	for _, c := range []struct {
		name, state, holder, later string
		want                       string
		journal                    bool
	}{
		{"the second write is already there", "active", started, "", "result=ok end=started", false},
		{"the file names the epoch first and the second write comes later", "active", firstOne, started, "result=ok end=started", false},
		{"an activating unit is waited for", "activating", firstOne, started, "result=ok end=started", false},
		{"the file is never written", "active", "", "", "result=failed end=timeout", true},
		{"the file names the epoch and never says started", "active", firstOne, "", "result=failed end=timeout", true},
		{"the file is the last epoch's", "active", "epoch=1\nsandbox_started=true\n", "", "result=failed end=timeout", true},
		{"the unit has ended and the file never came", "failed", "", "", "result=failed end=ended", true},
		{"the unit has ended after the first write", "inactive", firstOne, "", "result=failed end=ended", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			o, asked := run(t, c.state, c.holder, c.later)
			if !strings.Contains(o, c.want) {
				t.Errorf("want %q:\n%s", c.want, o)
			}
			if c.journal {
				if !strings.Contains(o, "FAILED at step 12:") || !strings.Contains(asked, "--unit ataniq-lead-throwaway-one.service") {
					t.Errorf("the step does not fail with the journal asked for first (journalctl was asked %q):\n%s", asked, o)
				}
				if i, j := strings.Index(o, "the journal of the unit since"), strings.Index(o, "FAILED at step 12:"); i < 0 || j < i {
					t.Errorf("the journal is not shown before the failure:\n%s", o)
				}
			} else if asked != "" || strings.Contains(o, "FAILED") {
				t.Errorf("a wait that succeeded asked for the journal or failed (%q):\n%s", asked, o)
			}
		})
	}
}

// L1: the text printed to compare with a paste has no prefix, so what the
// owner copies from the screen is the file's text; the lines that mark it are
// not part of it.
func TestCanaryRunKitPasteMessageHasNoPrefix(t *testing.T) {
	dir := t.TempDir()
	body := filepath.Join(dir, "message-1.txt")
	const text = "Write the token below.\ncanary-token: canary-x-1\n"
	if err := os.WriteFile(body, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	o, code := canaryStepHarness(t, dir, []string{"out", "paste_message"}, "BODY="+body, "paste_message")
	if code != 0 {
		t.Fatalf("exit status %d:\n%s", code, o)
	}
	if strings.Contains(o, "     | ") {
		t.Errorf("the text is printed with a prefix:\n%s", o)
	}
	begin, end := "----- begin of message-1.txt -----\n", "----- end -----\n"
	i, j := strings.Index(o, begin), strings.Index(o, end)
	if i < 0 || j < i || o[i+len(begin):j] != text {
		t.Errorf("the file's text is not between the marker lines, unchanged:\n%s", o)
	}
	if !strings.Contains(o, "\ncanary-token: canary-x-1\n") {
		t.Errorf("no line of the screen starts with the token prefix:\n%s", o)
	}
	for _, want := range []string{"Copy the file's text (cat the file), not the lines of this screen", `"no token" in the pane means the paste was not`, "paste again"} {
		if !strings.Contains(o, want) {
			t.Errorf("the paste text does not say %q:\n%s", want, o)
		}
	}
}

// F3: step 9 of pass A compares the hashes of the stand-in and of the probe
// with the built ones, outside --dry.
func TestCanaryRunKitStepNineComparesTheHashes(t *testing.T) {
	for _, c := range []struct {
		name                        string
		agent, built, probe, pbuilt string
		pass                        bool
		failure                     string
	}{
		{"all the same", "a", "a", "p", "p", true, ""},
		{"another stand-in", "a", "b", "p", "p", false, "the agent is the stand-in that make canary-build produced"},
		{"another probe", "a", "a", "p", "q", false, "the probe is the one that make canary-build produced"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.MkdirAll(filepath.Join(dir, "ev", "out"), 0o700); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"agent": c.agent, "ataniq-canary-standin": c.built, "probe": c.probe, "ataniq-probe": c.pbuilt}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			for _, tool := range []string{"sha256sum", "awk", "sed"} {
				if real, err := exec.LookPath(tool); err == nil {
					os.MkdirAll(bin, 0o700)
					os.Symlink(real, filepath.Join(bin, tool))
				}
			}
			pre := "BIN=" + dir + ` INPUTS=/x/inputs.json
agent_host() { echo ` + filepath.Join(dir, "agent") + `; }
fld() { echo ` + filepath.Join(dir, "probe") + `; }
step_head() { :; }
wait_key() { :; }
result() { :; }
done_step() { :; }`
			o, code := canaryStepHarness(t, dir, []string{"out", "note", "dry", "fail", "check", "is_eq", "step_9"}, pre, "step_9; echo STEP-PASSED")
			if code != 0 {
				t.Fatalf("exit status %d:\n%s", code, o)
			}
			if c.pass {
				if !strings.Contains(o, "STEP-PASSED") || !strings.Contains(o, "findings=0") {
					t.Errorf("the step did not pass:\n%s", o)
				}
			} else if !strings.Contains(o, "findings=1") || !strings.Contains(o, "FAILED at step") || !strings.Contains(o, c.failure) {
				t.Errorf("the step did not fail with %q:\n%s", c.failure, o)
			}
			hashes, _ := os.ReadFile(filepath.Join(dir, "ev", "standin-hash.txt"))
			if !strings.Contains(string(hashes), "probe-built ") {
				t.Errorf("the hashes are not recorded: %q", hashes)
			}
		})
	}
}
