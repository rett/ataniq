//go:build linux && ataniq_canary

package main

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

// A live process reads as alive with its state, a reaped one as gone, and a
// zombie is polled until it is reaped.
func TestWaitProcessGone(t *testing.T) {
	if s := procState(os.Getpid()); s != "R" && s != "S" {
		t.Errorf("the state of this process: %q", s)
	}
	if state, gone := waitProcessGone(os.Getpid(), 50*time.Millisecond); gone || state == "" {
		t.Errorf("this process: gone %v, state %q", gone, state)
	}

	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Skip("cannot start a sample process:", err)
	}
	pid := cmd.Process.Pid
	cmd.Process.Kill()
	// Not reaped yet: a zombie once the kill has taken effect, and gone after
	// Wait.
	for end := time.Now().Add(5 * time.Second); procState(pid) != "Z"; time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("the killed process never became a zombie: %q", procState(pid))
		}
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		cmd.Wait()
	}()
	if state, gone := waitProcessGone(pid, 10*time.Second); !gone {
		t.Errorf("a zombie was not polled until it was reaped: state %q", state)
	}

	live := exec.Command("sleep", "60")
	if err := live.Start(); err != nil {
		t.Skip("cannot start a sample process:", err)
	}
	defer func() { live.Process.Kill(); live.Wait() }()
	if state, gone := waitProcessGone(live.Process.Pid, 50*time.Millisecond); gone || state == "Z" || state == "" {
		t.Errorf("a live process: gone %v, state %q", gone, state)
	}
}
