//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadholder/session"
)

func TestStartTicks(t *testing.T) {
	// The command name may hold spaces and parentheses.
	stat := "1234 (a b) c) S 1 2 3 4 5 6 7 8 9 10 11 12 13 14 15 16 17 18 987654 23 24\n"
	got, err := startTicks([]byte(stat))
	if err != nil || got != 987654 {
		t.Errorf("startTicks = %d, %v", got, err)
	}
	for _, bad := range []string{"", "no parens 1 2 3", "1 (x) S 1 2", "1 (x) S " + strings.Repeat("1 ", 18) + "notanumber 1"} {
		if _, err := startTicks([]byte(bad)); err == nil {
			t.Errorf("startTicks(%q) did not fail", bad)
		}
	}
	if n, err := selfStartTicks(); err != nil || n == 0 {
		t.Errorf("selfStartTicks = %d, %v", n, err)
	}
}

// holder.json is 0600 whatever the umask, replaces an older file whole, and a
// leftover temporary name is no obstacle.
func TestWriteStateFile(t *testing.T) {
	dir := t.TempDir()
	old := syscall.Umask(0o277)
	defer syscall.Umask(old)
	st := stateFile{Version: 1, Role: "r", Epoch: 2, Pid: 3, Started: time.Unix(1, 0).UTC(), Scratch: true}
	if err := os.WriteFile(filepath.Join(dir, StateFileName), []byte("old content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".holder.json.tmp-"+itoaPID()), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeStateFile(dir, st); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, StateFileName))
	if err != nil || !strings.HasPrefix(string(b), `{"version":1,"role":"r","epoch":2`) || strings.Contains(string(b), "old content") {
		t.Errorf("state file %q, %v", b, err)
	}
	if got := mode(t, filepath.Join(dir, StateFileName)); got != 0o600 {
		t.Errorf("mode %v", got)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("files left in the folder: %v", ents)
	}
	// A folder that is not there is an error, not a panic.
	if err := writeStateFile(filepath.Join(dir, "absent"), st); err == nil {
		t.Error("a missing folder was accepted")
	}
}

func itoaPID() string { return strconv.Itoa(os.Getpid()) }

// The agent cannot be started: the holder exits 1, says so, logs a refusal and
// lets the role go; the state file was written first, as for every start.
func TestAgentThatCannotStartExits1(t *testing.T) {
	r := newRig(t)
	a := r.spec("")
	a.agent = []string{filepath.Join(r.root, "no-such-agent")}
	a.env = nil
	if code := r.runNow(a.build()); code != exitFail {
		t.Fatalf("exit %d, want %d; stderr: %s", code, exitFail, r.errbuf.String())
	}
	if !strings.Contains(r.errbuf.String(), "cannot start the agent") {
		t.Errorf("stderr %q", r.errbuf.String())
	}
	// The diagnostic says that the state file names a holder that is gone.
	if !strings.Contains(r.errbuf.String(), "holder.json names a holder that is gone") {
		t.Errorf("the diagnostic does not say that holder.json is stale: %q", r.errbuf.String())
	}
	if st := r.readState(); st.Agent != a.agent[0] {
		t.Errorf("state file %+v", st)
	}
	if !strings.Contains(string(r.logBytes()), `"reason":"start-failed"`) {
		t.Errorf("event log %q", r.logBytes())
	}
	// The role is free again: another holder takes it.
	r2 := r
	r2.errbuf = &syncBuf{}
	r2.start(r2.spec("raw; say AGAIN; exit 0").build())
	r2.waitReady()
	if code := r2.wait(); code != exitOK {
		t.Fatalf("the next holder exited %d; stderr: %s", code, r2.errbuf.String())
	}
	if rec := r2.readFinal(); rec.Facts.Status == nil || rec.Facts.Status.Status != 0 || rec.Facts.TUIExited != session.Yes {
		t.Errorf("final %+v", rec.Facts)
	}
}

func TestCheckPrivateDir(t *testing.T) {
	root := t.TempDir()
	mk := func(name string, m os.FileMode) string {
		p := filepath.Join(root, name)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, m); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := checkPrivateDir(mk("ok", 0o700)); err != nil {
		t.Errorf("0700: %v", err)
	}
	for i, m := range []os.FileMode{0o500, 0o750, 0o755, os.ModeSticky | 0o700, os.ModeSetgid | 0o700, 0o770, 0o777} {
		if err := checkPrivateDir(mk("m"+strconv.Itoa(i), m)); err == nil {
			t.Errorf("mode %v was accepted", m)
		}
	}
}

// A final record that an earlier epoch left is moved to final.json.previous
// before the new epoch writes its state file, replacing an older one: while an
// agent runs, final.json is absent, and after it ends it is that epoch's own.
func TestFinalRecordOfAnEarlierEpochIsRenamed(t *testing.T) {
	r := newRig(t)
	runEpoch := func(epoch string, script string) {
		t.Helper()
		r.errbuf = &syncBuf{}
		r.start(r.spec(script).set("epoch", epoch).build())
		r.waitReady()
	}
	finalPath := filepath.Join(r.state, session.FinalName)
	previous := filepath.Join(r.state, PreviousFinalName)
	if _, err := os.Lstat(previous); err == nil {
		t.Fatal("a previous record before any epoch")
	}

	// The first epoch has nothing to move aside.
	runEpoch("7", "raw; say FIRST; wait")
	r.sigc <- syscall.SIGTERM
	if code := r.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
	}
	if _, err := os.Lstat(previous); err == nil {
		t.Error("final.json.previous exists after the first epoch")
	}
	first, err := os.ReadFile(finalPath)
	if err != nil || r.readFinal().Epoch != "7" {
		t.Fatalf("final.json of epoch 7: %v", err)
	}

	// The second epoch moves it aside before its agent runs.
	runEpoch("8", "raw; say SECOND; wait")
	if _, err := os.Lstat(finalPath); err == nil {
		t.Error("final.json of epoch 7 is still there while epoch 8 runs")
	}
	got, err := os.ReadFile(previous)
	if err != nil || string(got) != string(first) {
		t.Errorf("final.json.previous is %q (%v), want epoch 7's record", got, err)
	}
	if mode(t, previous) != 0o600 {
		t.Errorf("final.json.previous has mode %v", mode(t, previous))
	}
	if st := r.readState(); st.Epoch != 8 {
		t.Errorf("state file %+v", st)
	}
	r.sigc <- syscall.SIGTERM
	if code := r.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
	}
	if r.readFinal().Epoch != "8" {
		t.Errorf("final.json is %+v, want epoch 8's", r.readFinal())
	}
	if got, _ := os.ReadFile(previous); string(got) != string(first) {
		t.Errorf("final.json.previous changed when epoch 8 ended: %q", got)
	}

	// The third epoch replaces the older previous record with epoch 8's.
	second, _ := os.ReadFile(finalPath)
	runEpoch("9", "raw; say THIRD; wait")
	if got, _ := os.ReadFile(previous); string(got) != string(second) {
		t.Errorf("final.json.previous is %q, want epoch 8's record", got)
	}
	r.sigc <- syscall.SIGTERM
	if code := r.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
	}
}

// A start that fails after the state file was written still moved the earlier
// final record aside, and leaves holder.json naming the holder that is gone.
func TestFailedStartAfterAnEarlierEpochKeepsItsRecordAside(t *testing.T) {
	r := newRig(t)
	r.start(r.spec("raw; wait").build())
	r.waitReady()
	r.sigc <- syscall.SIGTERM
	if code := r.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
	}
	first, _ := os.ReadFile(filepath.Join(r.state, session.FinalName))

	r.errbuf = &syncBuf{}
	a := r.spec("").set("epoch", "8")
	a.agent = []string{filepath.Join(r.root, "no-such-agent")}
	a.env = nil
	if code := r.runNow(a.build()); code != exitFail {
		t.Fatalf("exit %d, want %d; stderr: %s", code, exitFail, r.errbuf.String())
	}
	if _, err := os.Lstat(filepath.Join(r.state, session.FinalName)); err == nil {
		t.Error("final.json of the earlier epoch is still in place after the failed start")
	}
	if got, _ := os.ReadFile(filepath.Join(r.state, PreviousFinalName)); string(got) != string(first) {
		t.Errorf("final.json.previous is %q", got)
	}
	if st := r.readState(); st.Epoch != 8 {
		t.Errorf("state file %+v", st)
	}
}
