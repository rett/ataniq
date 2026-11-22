package switcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withLists gives the release id its own reload, health and stop commands.
func (e *env) withLists(id, reload, health, stop string) {
	e.t.Helper()
	dir := e.relDir(id)
	makeWritable(dir)
	e.write(filepath.Join(dir, ReloadList), reload+"\n", 0o644)
	e.write(filepath.Join(dir, HealthCmd), health+"\n", 0o644)
	e.write(filepath.Join(dir, StopList), stop+"\n", 0o644)
	e.sums(dir)
}

// activeOwn is active, with rel2 holding its own reload, health and stop
// commands, and rel3 ready with the fixture's.
func activeOwn(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.release(rel1, "s")
	e.release(rel2, "s")
	e.withLists(rel2, "reload-old", "health-old", "stop-old")
	for _, id := range []string{rel1, rel2} {
		if code := e.switchTo(id); code != ExitOK {
			t.Fatalf("switch to %s: exit %d", id, code)
		}
	}
	e.release(rel3, "s")
	e.runner.reset()
	return e
}

// TestGoingBackUsesTheOldReleasesLists: after rel3 fails its health check,
// the switch goes back to rel2 and runs rel2's reload and health commands,
// not rel3's.
func TestGoingBackUsesTheOldReleasesLists(t *testing.T) {
	e := activeOwn(t)
	e.runner.fail["health-probe"] = -1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if e.runner.called("reload-units") != 1 || e.runner.called("reload-old") != 1 || e.runner.called("health-old") != 1 {
		t.Errorf("commands: %v", e.runner.calls)
	}
	if e.runner.called("stop-app") != 0 || e.runner.called("stop-old") != 0 {
		t.Errorf("stopped although going back worked: %v", e.runner.calls)
	}
	rec := e.record()
	if rec.Result != ResultRolledBack || rec.RestoredHealth != "ok" || rec.CurrentID != rel2 {
		t.Errorf("record: %+v", rec)
	}
	if _, ok := e.journal(); ok {
		t.Errorf("journal left")
	}
}

// TestFailedRecovery: when going back cannot be completed (the old release's
// reload or health fails), the candidate's stop list stops the decision
// services, the result is failed-recovery and restored_health failed.
func TestFailedRecovery(t *testing.T) {
	for _, failing := range []string{"reload-old", "health-old"} {
		t.Run(failing, func(t *testing.T) {
			e := activeOwn(t)
			e.runner.fail["health-probe"] = -1
			e.runner.fail[failing] = -1
			if code := e.switchTo(rel3); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			rec := e.record()
			if rec.Result != ResultFailedRecovery || rec.RestoredHealth != "failed" || rec.FailedStep != "health" {
				t.Errorf("record: %+v", rec)
			}
			if e.runner.called("stop-app") != 1 || e.runner.called("stop-old") != 0 {
				t.Errorf("the candidate's stop list did not run: %v", e.runner.calls)
			}
			if got := e.link("current"); got != "releases/"+rel2 {
				t.Errorf("current -> %q", got)
			}
			if _, ok := e.journal(); ok {
				t.Errorf("journal left")
			}
		})
	}
}

// TestFirstSwitchFailureStops: with no release to go back to, a failing
// first switch stops the decision services too.
func TestFirstSwitchFailureStops(t *testing.T) {
	e := newEnv(t)
	e.release(rel1, "s")
	e.runner.fail["health-probe"] = -1
	if code := e.switchTo(rel1); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if e.runner.called("stop-app") != 1 {
		t.Errorf("not stopped: %v", e.runner.calls)
	}
	if rec := e.record(); rec.Result != ResultFailed || rec.CurrentID != rel1 {
		t.Errorf("record: %+v", rec)
	}
}

// TestUnusableFallback: a current release that no longer verifies, or is
// gone, is no fallback: a failing switch stays on the candidate and stops
// the decision services.
func TestUnusableFallback(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(e *env)
	}{
		{"changed", func(e *env) {
			dir := e.relDir(rel2)
			makeWritable(dir)
			os.WriteFile(filepath.Join(dir, "bin/app"), []byte("changed\n"), 0o755)
		}},
		{"missing", func(e *env) {
			makeWritable(e.relDir(rel2))
			os.RemoveAll(e.relDir(rel2))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := activeOwn(t)
			tc.spoil(e)
			e.runner.fail["health-probe"] = -1
			if code := e.switchTo(rel3); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			if !strings.Contains(e.last, "fallback: none") {
				t.Errorf("output:\n%s", e.last)
			}
			rec := e.record()
			if rec.Result != ResultFailedRecovery || rec.CurrentID != rel3 || !strings.Contains(rec.Detail, "earlier release cannot be used") {
				t.Errorf("record: %+v", rec)
			}
			if got := e.link("current"); got != "releases/"+rel3 {
				t.Errorf("current -> %q", got)
			}
			if e.runner.called("stop-app") != 1 || e.runner.called("reload-old") != 0 {
				t.Errorf("commands: %v", e.runner.calls)
			}
		})
	}
}

// TestRollbackToMissingRelease: previous pointing at a release folder that
// is gone is refused before anything runs.
func TestRollbackToMissingRelease(t *testing.T) {
	e := active(t)
	makeWritable(e.relDir(rel1))
	os.RemoveAll(e.relDir(rel1))
	if code := e.rollBack("previous"); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.unchanged("releases/"+rel2, "counter")
	if rec := e.record(); rec.Error != CodeNoPrevious {
		t.Errorf("record: %+v", rec)
	}
}

// TestStopFailureNoted: a stop command that fails is in the record, and the
// other stop commands still run.
func TestStopFailureNoted(t *testing.T) {
	e := newEnv(t)
	dir := e.release(rel1, "s")
	e.write(filepath.Join(dir, StopList), "stop-seat\nstop-notify\n", 0o644)
	e.sums(dir)
	e.runner.fail["health-probe"] = -1
	e.runner.fail["stop-seat"] = -1
	if code := e.switchTo(rel1); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if e.runner.called("stop-notify") != 1 {
		t.Errorf("commands: %v", e.runner.calls)
	}
	if rec := e.record(); !strings.Contains(rec.Detail, "stop failed: stop-seat: failed") {
		t.Errorf("record: %+v", rec)
	}
}

// TestReconcileFailedRecovery: an interrupted switch whose current moved,
// whose release is unhealthy and whose old release is unhealthy too ends
// with the decision services stopped.
func TestReconcileFailedRecovery(t *testing.T) {
	e := activeOwn(t)
	e.crashAt(CrashCurrent, false, rel3)
	e.runner.reset()
	e.runner.fail["health-probe"] = -1
	e.runner.fail["health-old"] = -1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if e.runner.called("stop-app") != 1 {
		t.Errorf("not stopped: %v", e.runner.calls)
	}
	if !strings.Contains(e.last, "result: failed-recovery") {
		t.Errorf("output:\n%s", e.last)
	}
	if _, ok := e.journal(); ok {
		t.Errorf("journal left")
	}
}
