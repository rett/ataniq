//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
)

// A SIGTERM or SIGINT that arrives while the holder starts is seen just before
// the agent would start: the holder exits 2 without starting it, removes the
// state file it had written, logs a refusal, and the role is free again. The
// signal is sent at two points of the start-up: before the role lock is taken,
// and after the state file is written (the hook of the session's settings).
func TestStopSignalDuringStartUpStopsBeforeTheAgent(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		for _, point := range []string{"before the role lock", "after the state file"} {
			t.Run(signalName(sig)+" "+point, func(t *testing.T) {
				r := newRig(t)
				ran := filepath.Join(r.agentFiles, "agent-ran")
				d := r.deps()
				switch point {
				case "before the role lock":
					d.beforeRoleLock = func() { r.sigc <- sig }
				default:
					inner := d.tweakSession
					d.tweakSession = func(c *session.Config) {
						if _, err := os.Stat(filepath.Join(r.state, StateFileName)); err != nil {
							t.Errorf("the state file is not written at this point: %v", err)
						}
						r.sigc <- sig
						inner(c)
					}
				}
				code := run(r.spec("report "+ran+"; raw; say STARTED; wait").build(), d)
				if code != exitRefuse {
					t.Fatalf("exit %d, want %d; stderr: %s", code, exitRefuse, r.errbuf.String())
				}
				if !strings.Contains(r.errbuf.String(), "before the agent was started") || !strings.Contains(r.errbuf.String(), signalName(sig)) {
					t.Errorf("stderr %q", r.errbuf.String())
				}
				if strings.Contains(r.errbuf.String(), "ready:") {
					t.Errorf("the holder says it is ready: %q", r.errbuf.String())
				}
				if _, err := os.Stat(ran); err == nil {
					t.Error("the agent was started")
				}
				// The state file is removed, and no final record was written.
				for _, n := range []string{StateFileName, session.FinalName} {
					if _, err := os.Lstat(filepath.Join(r.state, n)); err == nil {
						t.Errorf("%s is left in the state folder", n)
					}
				}
				if !strings.Contains(string(r.logBytes()), `"reason":"signal-before-start"`) {
					t.Errorf("event log %q", r.logBytes())
				}
				if strings.Contains(string(r.logBytes()), `"event":"start"`) {
					t.Errorf("a start was logged: %q", r.logBytes())
				}
				// The role lock is free: another holder takes it and runs.
				r.errbuf = &syncBuf{}
				r.start(r.spec("raw; say NEXT; exit 0").build())
				r.waitReady()
				if code := r.wait(); code != exitOK {
					t.Fatalf("the next holder exited %d; stderr: %s", code, r.errbuf.String())
				}
			})
		}
	}
}

// SIGHUP and SIGPIPE that arrive during start-up change nothing: the agent
// starts, and a stop signal that comes later still stops it.
func TestOtherSignalsDuringStartUpDoNotStopIt(t *testing.T) {
	r := newRig(t)
	d := r.deps()
	inner := d.tweakSession
	d.tweakSession = func(c *session.Config) {
		r.sigc <- syscall.SIGHUP
		r.sigc <- syscall.SIGPIPE
		inner(c)
	}
	go func() { r.done <- run(r.spec("raw; say UP; wait").build(), d) }()
	r.waitReady()
	c := r.attach(wire.RoleViewer)
	c.waitText("UP")
	r.sigc <- syscall.SIGTERM
	c.waitExit()
	if code := r.wait(); code != exitOK {
		t.Fatalf("exit %d; stderr: %s", code, r.errbuf.String())
	}
}
