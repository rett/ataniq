//go:build linux && ataniq_canary

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadholder/session"
)

// leadHasRunAbsent asks by the folder's handle and follows no link.
func TestLeadHasRunAbsent(t *testing.T) {
	dir := t.TempDir()
	if absent, err := leadHasRunAbsent(dir); err != nil || !absent {
		t.Fatalf("an empty folder: %v %v", absent, err)
	}
	if _, err := markLeadHasRun(dir); err != nil {
		t.Fatal(err)
	}
	if absent, err := leadHasRunAbsent(dir); err != nil || absent {
		t.Errorf("a folder with the mark: %v %v", absent, err)
	}
	linked := t.TempDir()
	if err := os.Symlink(filepath.Join(t.TempDir(), "gone"), filepath.Join(linked, leadHasRunMark)); err != nil {
		t.Fatal(err)
	}
	if absent, err := leadHasRunAbsent(linked); err != nil || absent {
		t.Errorf("a dangling link at the mark's name: %v %v", absent, err)
	}
	if _, err := leadHasRunAbsent(filepath.Join(dir, "absent")); err == nil {
		t.Error("a missing folder is not an error")
	}
}

// The launcher makes the mark before it starts the session, and a launch whose
// mark cannot be made starts nothing.
func TestLauncherMakesTheMarkFirst(t *testing.T) {
	var order []string
	l := &sessionLauncher{
		mark: func() error { order = append(order, "mark"); return nil },
		start: func(session.Config) (*session.Session, error) {
			order = append(order, "start")
			return nil, syscall.ENOENT
		},
	}
	if _, err := l.Launch([]string{"x"}); err == nil {
		t.Fatal("a failed session start was not an error")
	}
	if strings.Join(order, " ") != "mark start" {
		t.Errorf("the order was %q", order)
	}
	started := false
	l = &sessionLauncher{
		mark:  func() error { return errors.New("sample failure") },
		start: func(session.Config) (*session.Session, error) { started = true; return nil, nil },
	}
	if _, err := l.Launch([]string{"x"}); err == nil || !strings.Contains(err.Error(), "cannot be made") {
		t.Errorf("a launch with no mark: %v", err)
	}
	if started || l.called {
		t.Error("the session was started with no mark")
	}
}

// A start that fails before the launch leaves no mark, and the next start is a
// first start again: it prepares the mount points. A start that fails after the
// launch leaves the mark, and the next start does not prepare.
func TestCanaryMarkFollowsTheLaunch(t *testing.T) {
	c := newCanaryRig(t)
	c.stopGrace = 200 * time.Millisecond
	digest := c.digest()
	mark := filepath.Join(c.state, leadHasRunMark)

	c.sys.fail["CheckOwnClone"] = errors.New("sample failure")
	if code := c.runNow(c.spec(digest).build()); code != exitSandbox {
		t.Fatalf("exit %d; stderr: %s", code, c.errbuf.String())
	}
	if len(c.launches()) != 0 {
		t.Fatal("the start launched")
	}
	if _, err := os.Lstat(mark); err == nil {
		t.Fatal("a start that failed before the launch left the mark")
	}
	c.noAgentNoClaim()

	// The next start prepares again, then fails after the launch.
	delete(c.sys.fail, "CheckOwnClone")
	c.sys.fail["ReadLine"] = errors.New("sample failure")
	c.sys.reset()
	c.errbuf = &syncBuf{}
	if code := c.runNow(c.spec(digest).build()); code != exitSandbox {
		t.Fatalf("exit %d; stderr: %s", code, c.errbuf.String())
	}
	if seen := c.sys.inputs(); len(seen) != 1 || !seen[0].NoLeadHasRun {
		t.Errorf("the start after a failure before the launch: %+v", seen)
	}
	if c.sys.called("PrepareMountPoints") != 1 {
		t.Error("the start after a failure before the launch did not prepare")
	}
	if len(c.launches()) != 1 {
		t.Fatalf("%d launches", len(c.launches()))
	}
	if got := mode(t, mark); got != 0o600 {
		t.Errorf("a start that failed after the launch left a mark of mode %v", got)
	}
	c.noAgentNoClaim()

	// And the start after that is not a first start.
	delete(c.sys.fail, "ReadLine")
	c.sys.reset()
	c.errbuf = &syncBuf{}
	c.fakeCgroup(true)
	c.start(c.spec(digest).build())
	c.waitReady()
	if seen := c.sys.inputs(); len(seen) != 1 || seen[0].NoLeadHasRun {
		t.Errorf("the start after a launch: %+v", seen)
	}
	if c.sys.called("PrepareMountPoints") != 0 {
		t.Error("the start after a launch prepared the mount points")
	}
	c.stopAndWait()
}

// The mark is made inside the launcher, before the plan's first process: it is
// there when the session starts. A mark that cannot be made (here one that
// appeared after the decision) refuses the launch: nothing is started.
func TestCanaryMarkBeforeTheProcessAndRefusal(t *testing.T) {
	c := newCanaryRig(t)
	c.stopGrace = 200 * time.Millisecond
	digest := c.digest()
	mark := filepath.Join(c.state, leadHasRunMark)

	d := c.deps()
	inner := d.canary.startSession
	var atStart bool
	d.canary.startSession = func(cfg session.Config) (*session.Session, error) {
		_, err := os.Lstat(mark)
		atStart = err == nil
		return inner(cfg)
	}
	c.done = make(chan int, 1)
	go func() { c.done <- run(c.spec(digest).build(), d) }()
	c.waitReady()
	if !atStart {
		t.Error("the mark was not there when the session started")
	}
	c.stopAndWait()

	// A mark that appears between the decision and the launch.
	os.Remove(mark)
	c.sys.reset()
	c.errbuf = &syncBuf{}
	c.sys.hook = map[string]func(){"OpenReport": func() { os.WriteFile(mark, nil, 0o600) }}
	before := len(c.launches())
	c.start(c.spec(digest).build())
	select {
	case code := <-c.done:
		if code != exitSandbox {
			t.Fatalf("exit %d; stderr: %s", code, c.errbuf.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a mark that appeared during the start did not refuse the launch: the holder is serving")
	}
	if !strings.Contains(c.errbuf.String(), "at the step launch") || !strings.Contains(c.errbuf.String(), "cannot be made") {
		t.Errorf("stderr: %s", c.errbuf.String())
	}
	if len(c.launches()) != before {
		t.Error("a launch with no mark started the session")
	}
	c.noAgentNoClaim()
}
