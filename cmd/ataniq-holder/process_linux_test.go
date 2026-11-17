//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/leadholder/ptyhost"
	"ataniq/internal/leadholder/wire"
)

// sigIgn reads the SigIgn mask of a process.
func sigIgn(t *testing.T, pid int) uint64 {
	t.Helper()
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "SigIgn:"); ok {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 16, 64)
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	t.Fatal("no SigIgn line")
	return 0
}

// startHolderProcess runs the test binary as the real holder command with the
// arguments of a. The returned channel is closed when it has ended, and
// *exitErr is then its Wait result.
func startHolderProcess(t *testing.T, r *rig, a *argSpec) (*exec.Cmd, <-chan struct{}, *error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], a.build()...)
	cmd.Env = []string{holderMainEnv + "=1", "ATANIQ_HOLDER_TEST_MARKER=x"}
	cmd.Stderr = r.errbuf
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var exitErr error
	exited := make(chan struct{})
	go func() {
		exitErr = cmd.Wait()
		close(exited)
	}()
	t.Cleanup(func() {
		cmd.Process.Kill() // an error when it has ended already
		<-exited
	})
	return cmd, exited, &exitErr
}

// skipUnlessHolderCanRun skips a test that starts the real holder on a host
// that the holder refuses.
func skipUnlessHolderCanRun(t *testing.T) {
	t.Helper()
	if enabled, _ := ptyhost.LegacyTIOCSTI(""); enabled {
		t.Skip("dev.tty.legacy_tiocsti is not 0 on this host: the holder refuses to start")
	}
	if ign, err := ptyhost.IgnoredSignals(); err != nil || len(ign) != 0 {
		for _, s := range ign {
			for _, need := range signalsNeeded {
				if s == need {
					t.Skipf("the test process ignores %s, which the holder refuses", signalName(s))
				}
			}
		}
	}
}

// The holder as a process of its own, with the real signals and the real
// defaults: SIGHUP changes nothing, the agent does not inherit an ignored
// signal, SIGTERM ends the agent and the holder exits 0 with the final record.
func TestHolderProcessSignals(t *testing.T) {
	skipUnlessHolderCanRun(t)
	r := newRig(t)
	report := filepath.Join(r.agentFiles, "report")
	a := r.spec("report " + report + "; raw; say UP; wait")
	cmd, exited, exitErr := startHolderProcess(t, r, a)
	poll(t, func() string { return "the holder to answer; stderr: " + r.errbuf.String() }, func() bool {
		select {
		case <-exited:
			t.Fatalf("the holder ended: %v; stderr: %s", exitErr, r.errbuf.String())
		default:
		}
		resp, err := r.control(wire.Request{Op: wire.OpStatus, Epoch: testEpoch})
		return err == nil && resp.OK
	})
	c := r.attach(wire.RoleViewer)
	c.waitText("UP")

	rep, err := fakeagent.ReadReport(report)
	if err != nil {
		t.Fatal(err)
	}
	// The holder's own environment (the marker, the test's variables) stays
	// with the holder.
	if strings.Join(rep.Env, "\x00") != strings.Join(a.env, "\x00") {
		t.Errorf("the agent's environment is %q, want exactly %q", rep.Env, a.env)
	}
	// The holder itself ignores nothing; neither does the agent.
	for _, who := range []struct {
		name string
		pid  int
	}{{"holder", cmd.Process.Pid}, {"agent", rep.Pid}} {
		if m := sigIgn(t, who.pid); m != 0 {
			t.Errorf("the %s ignores signals (mask %x)", who.name, m)
		}
	}
	if rep.Ppid != cmd.Process.Pid {
		t.Errorf("the agent's parent is %d, want the holder %d", rep.Ppid, cmd.Process.Pid)
	}

	if err := cmd.Process.Signal(syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
		t.Fatalf("SIGHUP ended the holder: %v", *exitErr)
	case <-time.After(300 * time.Millisecond):
	}
	if st := r.status(); st.Facts.TUIExited != wire.FactNo {
		t.Fatalf("SIGHUP ended the agent: %+v", st.Facts)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
		if *exitErr != nil {
			t.Fatalf("the holder ended with %v; stderr: %s", exitErr, r.errbuf.String())
		}
	case <-time.After(patience):
		t.Fatalf("SIGTERM did not end the holder; stderr: %s", r.errbuf.String())
	}
	rec := r.readFinal()
	if rec.Facts.TUIExited != "yes" || rec.Facts.TreeGone != "yes" || rec.Facts.Status == nil || rec.Facts.Status.Signal != int(syscall.SIGTERM) {
		t.Errorf("final facts %+v status %+v", rec.Facts, rec.Facts.Status)
	}
	if !strings.Contains(r.errbuf.String(), "ready:") {
		t.Errorf("no ready line: %q", r.errbuf.String())
	}
}

// SIGQUIT, SIGUSR1 and SIGUSR2 sent to the holder process are handled like
// SIGTERM: the agent is stopped, the final record is written and the holder
// exits 0. Left at their defaults they would end the holder at once, with no
// final record (SIGQUIT with a goroutine dump).
func TestHolderProcessStopSignals(t *testing.T) {
	skipUnlessHolderCanRun(t)
	for _, sig := range []syscall.Signal{syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2} {
		t.Run(signalName(sig), func(t *testing.T) {
			r := newRig(t)
			cmd, exited, exitErr := startHolderProcess(t, r, r.spec("raw; say UP; wait"))
			poll(t, func() string { return "the holder to answer; stderr: " + r.errbuf.String() }, func() bool {
				select {
				case <-exited:
					t.Fatalf("the holder ended: %v; stderr: %s", *exitErr, r.errbuf.String())
				default:
				}
				resp, err := r.control(wire.Request{Op: wire.OpStatus, Epoch: testEpoch})
				return err == nil && resp.OK
			})
			c := r.attach(wire.RoleViewer)
			c.waitText("UP")
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			select {
			case <-exited:
				if *exitErr != nil {
					t.Fatalf("%s ended the holder with %v; stderr: %s", signalName(sig), *exitErr, r.errbuf.String())
				}
			case <-time.After(patience):
				t.Fatalf("%s did not end the holder; stderr: %s", signalName(sig), r.errbuf.String())
			}
			rec := r.readFinal()
			if rec.Facts.TUIExited != "yes" || rec.Facts.TreeGone != "yes" {
				t.Errorf("final facts %+v", rec.Facts)
			}
			if !strings.Contains(r.errbuf.String(), "stopping the agent ("+signalName(sig)+")") {
				t.Errorf("stderr %q", r.errbuf.String())
			}
			if strings.Contains(r.errbuf.String(), "goroutine ") {
				t.Errorf("the Go runtime dumped its goroutines: %q", r.errbuf.String())
			}
		})
	}
}
