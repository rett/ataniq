//go:build linux

package ptyhost

import (
	"path/filepath"
	"testing"

	"ataniq/internal/leadholder/fakeagent"
)

// reportOf starts the fake agent with the given option and returns what the
// child reported about its own session and terminal.
func reportOf(t *testing.T, noCtty bool) (fakeagent.Report, *Host) {
	t.Helper()
	rep := filepath.Join(t.TempDir(), "report")
	h, _ := start(t, "report "+rep+"; exit 0", opts{mod: func(c *Config) { c.NoControllingTerminal = noCtty }})
	if e := waitExit(t, h); e.How != "exit" || e.Code != 0 {
		t.Fatalf("exit %v", e)
	}
	r, err := fakeagent.ReadReport(rep)
	if err != nil {
		t.Fatal(err)
	}
	return r, h
}

func TestNoControllingTerminalLeadsASessionWithoutOne(t *testing.T) {
	r, h := reportOf(t, true)
	if r.Pid != h.Pid() || r.Sid != r.Pid || r.Pgid != r.Pid {
		t.Errorf("pid %d sid %d pgid %d, host pid %d: want a session leader", r.Pid, r.Sid, r.Pgid, h.Pid())
	}
	if r.Ctty {
		t.Error("the child could open /dev/tty: it has a controlling terminal")
	}
	if r.Tpgid > 0 {
		t.Errorf("foreground group %d, want none without a controlling terminal", r.Tpgid)
	}
	// The slave is still the child's standard streams.
	if !r.StdinTTY || !r.StdoutTTY || !r.StderrTTY {
		t.Errorf("stdio terminals %v %v %v, want all three", r.StdinTTY, r.StdoutTTY, r.StderrTTY)
	}
}

func TestDefaultStartKeepsTheControllingTerminal(t *testing.T) {
	r, h := reportOf(t, false)
	if r.Pid != h.Pid() || r.Sid != r.Pid || r.Pgid != r.Pid {
		t.Errorf("pid %d sid %d pgid %d, host pid %d: want a session leader", r.Pid, r.Sid, r.Pgid, h.Pid())
	}
	if !r.Ctty || r.Tpgid != r.Pid {
		t.Errorf("controlling terminal %v, foreground group %d, want true and %d", r.Ctty, r.Tpgid, r.Pid)
	}
}
