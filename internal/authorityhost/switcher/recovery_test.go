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
	if code := e.switchTo(rel1); code != ExitOK {
		t.Fatalf("switch to %s: exit %d", rel1, code)
	}
	e.release(rel2, "s")
	e.withLists(rel2, "reload-old", "health-old", "stop-old")
	if code := e.switchTo(rel2); code != ExitOK {
		t.Fatalf("switch to %s: exit %d", rel2, code)
	}
	e.release(rel3, "s")
	e.runner.reset()
	return e
}

// TestGoingBackUsesTheOldReleasesLists: after rel3 fails its health check,
// the switch goes back to rel2 and runs rel2's reload and health commands,
// not rel3's, and stops nothing.
func TestGoingBackUsesTheOldReleasesLists(t *testing.T) {
	e := activeOwn(t)
	e.runner.fail["health-probe"] = -1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.ran(map[string]int{"reload-units": 1, "reload-old": 1, "health-old": 1, "stop-all": 0, "stop-app": 0, "stop-old": 0})
	rec := e.record()
	if rec.Result != ResultRolledBack || rec.RestoredHealth != "ok" || rec.CurrentID != rel2 || rec.ServicesStopped {
		t.Errorf("record: %+v", rec)
	}
	e.links(rel2, rel1)
	e.noJournal()
}

// TestFailedRecovery: when going back cannot be completed (the old release's
// reload or health fails), the host's stop list and the stop lists of both
// releases stop the decision services, the result is failed-recovery and
// restored_health failed.
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
			if rec.Result != ResultFailedRecovery || rec.RestoredHealth != "failed" || rec.FailedStep != "health" || !rec.ServicesStopped {
				t.Errorf("record: %+v", rec)
			}
			e.ran(map[string]int{"stop-all": 1, "stop-app": 1, "stop-old": 1})
			if !strings.Contains(e.last, "the decision services are stopped") {
				t.Errorf("output:\n%s", e.last)
			}
			e.links(rel2, rel1)
			e.noJournal()
		})
	}
}

// TestFirstSwitchFailureStops: with no release to go back to, a failing
// first switch stops the decision services with the host's list and its
// own.
func TestFirstSwitchFailureStops(t *testing.T) {
	e := newEnv(t)
	e.release(rel1, "s")
	e.runner.fail["health-probe"] = -1
	if code := e.switchTo(rel1); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.ran(map[string]int{"stop-all": 1, "stop-app": 1})
	if rec := e.record(); rec.Result != ResultFailed || rec.CurrentID != rel1 || !rec.ServicesStopped {
		t.Errorf("record: %+v", rec)
	}
}

// TestUnverifiableCandidateStillStopped: a first switch interrupted while
// activating, whose release no longer verifies when it is resumed, is
// stopped with the host's stop list: the release's own list is not run, as
// its code cannot be trusted.
func TestUnverifiableCandidateStillStopped(t *testing.T) {
	e := newEnv(t)
	dir := e.release(rel1, "s")
	e.crashAt(CrashActivating, false, rel1)
	makeWritable(dir)
	os.WriteFile(filepath.Join(dir, "bin/app"), []byte("changed\n"), 0o755)
	e.runner.reset()
	if code := e.recover(resumeOnly); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.ran(map[string]int{"stop-all": 1, "stop-app": 0, "reload-units": 0})
	if !strings.Contains(e.last, "is not run: it cannot be verified") {
		t.Errorf("output:\n%s", e.last)
	}
	rec := e.record()
	if rec.Result != ResultFailed || !rec.ServicesStopped || rec.Error != CodeInterrupted {
		t.Errorf("record: %+v", rec)
	}
	e.noJournal()
}

// TestUnusableFallback: a current release that no longer verifies, or is
// gone, is no fallback: a failing switch stays on the candidate and stops
// the decision services; the stop list of the unusable release is not run.
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
			if rec.Result != ResultFailedRecovery || rec.CurrentID != rel3 || !strings.Contains(rec.Detail, "earlier release cannot be used") || !rec.ServicesStopped {
				t.Errorf("record: %+v", rec)
			}
			if got := e.link("current"); got != "releases/"+rel3 {
				t.Errorf("current -> %q", got)
			}
			e.ran(map[string]int{"stop-all": 1, "stop-app": 1, "stop-old": 0, "reload-old": 0})
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

// TestStopFailureStaysStopping: a stop command that fails leaves the
// transaction in its stopping phase: the command fails without saying the
// services are stopped, the other stop commands still run, every later
// command runs the stops again before anything else and does nothing else,
// and once they pass the transaction settles with the services stopped,
// and still nothing else is done.
func TestStopFailureStaysStopping(t *testing.T) {
	e := newEnv(t)
	dir := e.release(rel1, "s")
	e.write(filepath.Join(dir, StopList), "stop-seat\nstop-notify\n", 0o644)
	e.sums(dir)
	e.runner.fail["health-probe"] = -1
	e.runner.fail["stop-seat"] = -1
	if code := e.switchTo(rel1); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.release(rel2, "s")
	if strings.Contains(e.last, "decision services are stopped") {
		t.Errorf("said stopped although a stop failed:\n%s", e.last)
	}
	if !strings.Contains(e.last, "result: unresolved: stop-failed") {
		t.Errorf("output:\n%s", e.last)
	}
	e.ran(map[string]int{"stop-all": 1, "stop-seat": 1, "stop-notify": 1})
	j, ok := e.journal()
	if !ok || j.Phase != PhaseStopping || !strings.Contains(j.StopFailures, "stop-seat") {
		t.Fatalf("journal: %+v, %v", j, ok)
	}
	if got := e.status(); !strings.Contains(got, "in phase stopping") || !strings.Contains(got, "stop failures: 1 failed") {
		t.Errorf("status:\n%s", got)
	}

	// Still failing: the next switch runs the stops and goes no further.
	e.runner.reset()
	if code := e.switchTo(rel2); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.ran(map[string]int{"stop-all": 1, "stop-seat": 1, "ah-noaicheck": 0, "reload-units": 0})
	if _, ok := e.journal(); !ok {
		t.Fatalf("the journal was removed")
	}

	// Passing: the transaction settles, and the switch still does not run.
	delete(e.runner.fail, "stop-seat")
	e.runner.reset()
	if code := e.switchTo(rel2); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(e.last, "nothing else is done") {
		t.Errorf("output:\n%s", e.last)
	}
	e.ran(map[string]int{"stop-seat": 1, "ah-noaicheck": 0})
	e.noJournal()
	if rec := e.record(); rec.ID != rel1 || !rec.ServicesStopped || rec.Result != ResultFailed {
		t.Errorf("record: %+v", rec)
	}
	// Now the switch is asked again and runs.
	delete(e.runner.fail, "health-probe")
	if code := e.switchTo(rel2); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
}

// TestRecoverServicesStopped: the owner's word settles a stopping
// transaction whose stop commands keep failing; it is recorded with the
// time, and nothing is run.
func TestRecoverServicesStopped(t *testing.T) {
	e := newEnv(t)
	e.release(rel1, "s")
	if code := e.recover(RecoverOptions{ServicesStopped: true}); code != ExitFailed {
		t.Fatalf("with no state: exit %d", code)
	}
	e.runner.fail["health-probe"] = -1
	e.runner.fail["stop-app"] = -1
	if code := e.switchTo(rel1); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.runner.reset()
	if code := e.recover(RecoverOptions{ServicesStopped: true}); code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if len(e.runner.calls) != 0 {
		t.Errorf("commands run: %v", e.runner.calls)
	}
	e.noJournal()
	rec := e.record()
	if !rec.ServicesStopped || !strings.Contains(rec.Detail, "owner's word") || !strings.Contains(rec.Detail, "2026-11-21T12:") {
		t.Errorf("record: %+v", rec)
	}
	if code := e.recover(RecoverOptions{ServicesStopped: true}); code != ExitFailed {
		t.Errorf("again: exit %d", code)
	}
}

// TestReconcileFailedRecovery: an interrupted switch, resumed while
// activating, whose release is unhealthy and whose old release is unhealthy
// too, ends with the decision services stopped, and the command that
// resumed it does nothing else.
func TestReconcileFailedRecovery(t *testing.T) {
	e := activeOwn(t)
	e.crashAt(CrashActivating, false, rel3)
	e.release("0004-ddddddd", "s")
	e.runner.reset()
	e.runner.fail["health-probe"] = -1
	e.runner.fail["health-old"] = -1
	if code := e.switchTo("0004-ddddddd"); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.ran(map[string]int{"stop-all": 1, "stop-app": 1, "stop-old": 1, "ah-noaicheck": 0})
	if !strings.Contains(e.last, "result: failed-recovery") || !strings.Contains(e.last, "nothing else is done") {
		t.Errorf("output:\n%s", e.last)
	}
	e.noJournal()
	if rec := e.record(); rec.ID != rel3 {
		t.Errorf("record: %+v", rec)
	}
}

// TestHostStopListRequired: a switch or rollback is refused, with nothing
// done, without the host's own stop list, or with one that is not trusted
// or holds no command.
func TestHostStopListRequired(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(path string)
	}{
		{"missing", func(p string) { os.Remove(p) }},
		{"empty", func(p string) { os.WriteFile(p, []byte("# nothing\n"), 0o644) }},
		{"writable by others", func(p string) { os.Chmod(p, 0o666) }},
		{"a link", func(p string) { os.Rename(p, p+".real"); os.Symlink(p+".real", p) }},
		{"shell", func(p string) { os.WriteFile(p, []byte("systemctl stop a; rm -rf /\n"), 0o644) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			e.release(rel3, "2026-11-01")
			tc.setup(filepath.Join(e.root, PolicyDir, HostStopList))
			before := digest(t, e.root)
			if code := e.switchTo(rel3); code != ExitFailed {
				t.Fatalf("exit %d", code)
			}
			if code := e.rollBack("previous"); code != ExitFailed {
				t.Fatalf("rollback: exit %d", code)
			}
			if digest(t, e.root) != before || len(e.runner.calls) != 0 {
				t.Errorf("a refused switch changed the tree or ran %v", e.runner.calls)
			}
		})
	}
}

// stoppingSigned leaves a signed host with a first switch whose health
// failed and whose stop-app keeps failing: its journal stays in stopping.
func stoppingSigned(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.policy(signedPolicy)
	e.signRelease(e.release(rel1, "s"))
	e.runner.fail["health-probe"] = -1
	e.runner.fail["stop-app"] = -1
	if code := e.switchTo(rel1, verifier(&fakeVerifier{})); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if j, ok := e.journal(); !ok || j.Phase != PhaseStopping {
		t.Fatalf("journal: %+v, %v", j, ok)
	}
	delete(e.runner.fail, "stop-app")
	e.runner.reset()
	return e
}

// TestStopsBeforePolicyErrors: under the lock, a transaction stopping the
// decision services has its stops run before the command requires the
// switch policy, its signers file or the host's stop list: a signers file
// gone missing, or a host stop list gone, does not keep the host's own
// stop list (or the verifiable releases' lists) from running. The error is
// reported afterwards, and nothing else is done.
func TestStopsBeforePolicyErrors(t *testing.T) {
	signers := func(e *env) string { return filepath.Join(e.root, "etc/authority-host/allowed_signers") }
	t.Run("signers file missing, switch", func(t *testing.T) {
		e := stoppingSigned(t)
		os.Remove(signers(e))
		e.signRelease(e.release(rel2, "s"))
		if code := e.switchTo(rel2, verifier(&fakeVerifier{})); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		e.ran(map[string]int{"stop-all": 1, "stop-app": 0, "ah-noaicheck": 0, "reload-units": 0})
		if !strings.Contains(e.last, "allowed signers file") || !strings.Contains(e.last, "is not run: it cannot be verified") {
			t.Errorf("output:\n%s", e.last)
		}
		e.noJournal()
		if rec := e.record(); !rec.ServicesStopped || rec.ID != rel1 {
			t.Errorf("record: %+v", rec)
		}
		e.links(rel1, "")
	})
	t.Run("signers file missing, recover", func(t *testing.T) {
		e := stoppingSigned(t)
		os.Remove(signers(e))
		if code := e.recover(RecoverOptions{SetCounter: true, Counter: 1}); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		e.ran(map[string]int{"stop-all": 1})
		e.noJournal()
		if !strings.Contains(e.last, "allowed signers file") {
			t.Errorf("output:\n%s", e.last)
		}
	})
	t.Run("host stop list missing", func(t *testing.T) {
		e := newEnv(t)
		e.release(rel1, "s")
		e.runner.fail["health-probe"] = -1
		e.runner.fail["stop-app"] = 1
		if code := e.switchTo(rel1); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		os.Remove(filepath.Join(e.root, PolicyDir, HostStopList))
		e.runner.reset()
		if code := e.rollBack("previous"); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		// The release's own list runs; the host's cannot, so the journal
		// stays in stopping.
		e.ran(map[string]int{"stop-app": 1, "stop-all": 0})
		if j, ok := e.journal(); !ok || j.Phase != PhaseStopping || !strings.Contains(j.StopFailures, HostStopList) {
			t.Errorf("journal: %+v, %v", j, ok)
		}
		if !strings.Contains(e.last, "stop-list-missing") {
			t.Errorf("output:\n%s", e.last)
		}
	})
	t.Run("policy broken, nothing to stop", func(t *testing.T) {
		e := active(t)
		e.write(filepath.Join(e.root, PolicyDir, PolicyName), "{", 0o644)
		e.release(rel3, "2026-11-01")
		before := digest(t, e.root)
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if digest(t, e.root) != before || len(e.runner.calls) != 0 {
			t.Errorf("changed the tree or ran %v", e.runner.calls)
		}
	})
}
