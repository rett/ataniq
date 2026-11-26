package switcher

import (
	"errors"
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
// the switch stops rel3's services with rel3's own stop list, goes back to
// rel2 and runs rel2's reload and health commands, not rel3's, and its
// no-AI check. The host stop list runs first.
func TestGoingBackUsesTheOldReleasesLists(t *testing.T) {
	e := activeOwn(t)
	e.runner.fail["health-probe"] = -1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	e.ran(map[string]int{"reload-units": 1, "reload-old": 1, "health-old": 1, "stop-all": 1, "stop-app": 1, "stop-old": 0})
	var order []string
	for _, c := range e.runner.calls {
		if f := strings.Fields(c); f[0] == "stop-app" || f[0] == "reload-old" || strings.Contains(c, "--phase post --root "+e.root+" --release "+e.relDir(rel2)+" ") {
			order = append(order, filepath.Base(f[0]))
		}
	}
	if strings.Join(order, " ") != "stop-app reload-old ah-noaicheck" {
		t.Errorf("going back ran %v", order)
	}
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
			e.ran(map[string]int{"stop-all": 2, "stop-app": 2, "stop-old": 1})
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
	e.ran(map[string]int{"stop-all": 2, "stop-app": 2, "stop-old": 1, "ah-noaicheck": 0})
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

// stoppingFirst is a first switch to rel1 left in its stopping phase: its
// health check failed and its own stop command keeps failing.
func stoppingFirst(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.release(rel1, "s")
	e.runner.fail["health-probe"] = -1
	e.runner.fail["stop-app"] = -1
	if code := e.switchTo(rel1); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if j, ok := e.journal(); !ok || j.Phase != PhaseStopping {
		t.Fatalf("journal: %+v, %v", j, ok)
	}
	e.runner.reset()
	return e
}

// TestHostStopsComeFirst: a transaction in its stopping phase gets the
// host's stop list run, under the lock, before anything that can fail on
// the way to it: releases/ that cannot be used, a counter or a record
// that cannot be read, a journal that cannot be checked against the host;
// and a transaction that must stop the decision services runs the host's
// stop list before it saves that phase, so that a journal that cannot be
// written does not keep the stops from running. The journal stays.
func TestHostStopsComeFirst(t *testing.T) {
	commands := []struct {
		name string
		run  func(e *env) int
	}{
		{"switch", func(e *env) int { return e.switchTo(rel2) }},
		{"rollback", func(e *env) int { return e.rollBack("previous") }},
		{"recover --counter", func(e *env) int { return e.recover(RecoverOptions{SetCounter: true, Counter: 1}) }},
		{"recover --discard-journal", func(e *env) int {
			j, _ := e.journal()
			return e.recover(RecoverOptions{DiscardJournal: j.Txn})
		}},
	}
	for _, broken := range []struct {
		name   string
		break_ func(e *env)
	}{
		{"releases untrusted", func(e *env) { os.Chmod(filepath.Join(e.root, BaseDir, "releases"), 0o777) }},
		{"releases missing", func(e *env) {
			makeWritable(filepath.Join(e.root, BaseDir, "releases"))
			os.Rename(filepath.Join(e.root, BaseDir, "releases"), filepath.Join(e.root, BaseDir, "elsewhere"))
		}},
		{"counter unreadable", func(e *env) { e.write(filepath.Join(e.root, StateDir, HighestFile), "x\n", 0o644) }},
		{"record unreadable", func(e *env) { e.write(filepath.Join(e.root, StateDir, DeployFile), "{", 0o644) }},
	} {
		for _, c := range commands {
			t.Run(broken.name+"/"+c.name, func(t *testing.T) {
				e := stoppingFirst(t)
				broken.break_(e)
				if code := c.run(e); code != ExitFailed {
					t.Fatalf("exit %d", code)
				}
				e.ran(map[string]int{"stop-all": 1, "ah-noaicheck": 0, "reload-units": 0})
				if j, ok := e.journal(); !ok || j.Phase != PhaseStopping {
					t.Errorf("journal: %+v, %v", j, ok)
				}
			})
		}
	}
	t.Run("saving the stopping phase fails", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-15")
		checks := 0
		e.runner.gate = func(argv []string) error {
			if filepath.Base(argv[0]) != "ah-noaicheck" {
				return nil
			}
			if checks++; checks == 2 {
				// The check after the package step fails, and the journal
				// cannot be written any more.
				os.MkdirAll(filepath.Join(e.root, StateDir, "."+JournalFile+".new", "x"), 0o755)
				return errors.New("exit status 1")
			}
			return nil
		}
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		e.ran(map[string]int{"ah-pkgstep": 1, "stop-all": 1})
		if !strings.Contains(e.last, "result: unresolved") {
			t.Errorf("output:\n%s", e.last)
		}
	})
}

// TestDiscardNeedsProof: recover --discard-journal sets a journal aside
// only when the host proves it stale; a journal that cannot be checked
// (a counter or record that cannot be read) is unresolved, never
// discarded, and is not resumed either.
func TestDiscardNeedsProof(t *testing.T) {
	for _, tc := range []struct {
		name   string
		break_ func(e *env)
	}{
		{"counter unreadable", func(e *env) { e.write(filepath.Join(e.root, StateDir, HighestFile), "x\n", 0o644) }},
		{"counters.log unreadable", func(e *env) { e.write(filepath.Join(e.root, StateDir, CountersLog), "not a line\n", 0o644) }},
		{"record unreadable", func(e *env) { e.write(filepath.Join(e.root, StateDir, DeployFile), "{", 0o644) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			e.release(rel3, "2026-11-01")
			e.crashAt(CrashActivating, false, rel3)
			j, _ := e.journal()
			tc.break_(e)
			e.runner.reset()
			if code := e.recover(RecoverOptions{DiscardJournal: j.Txn}); code != ExitFailed {
				t.Fatalf("exit %d\n%s", code, e.last)
			}
			if !strings.Contains(e.last, "cannot be checked against the host") {
				t.Errorf("output:\n%s", e.last)
			}
			if got, ok := e.journal(); !ok || got.Txn != j.Txn {
				t.Errorf("the journal was set aside: %+v, %v", got, ok)
			}
			if code := e.switchTo(rel3); code != ExitFailed || !strings.Contains(e.last, "cannot be checked against the host") || strings.Contains(e.last, "discard-journal") {
				t.Errorf("resume: exit %d\n%s", code, e.last)
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("commands run: %v", e.runner.calls)
			}
		})
	}
}

// TestRestoringNotSavedStillStops: a candidate whose reload started its
// services and whose health check failed must have those services stopped
// even when the journal cannot be moved to restoring: the candidate's stop
// list and the host's run first, under the lock, then the command reports
// the failure. The transaction stays unresolved in activating, and the
// next command, with the state folder writable again, goes back.
func TestRestoringNotSavedStillStops(t *testing.T) {
	e := activeOwn(t)
	e.runner.fail["health-probe"] = -1
	// The health check of the candidate is where the journal stops being
	// writable: its temporary file cannot be made again.
	block := filepath.Join(e.root, StateDir, "."+JournalFile+".new")
	e.runner.hook = func(argv []string) {
		if argv[0] == "health-probe" {
			os.MkdirAll(block, 0o755)
			os.WriteFile(filepath.Join(block, "x"), []byte("x"), 0o644)
		}
	}
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d\n%s", code, e.last)
	}
	if !strings.Contains(e.last, "result: unresolved") || !strings.Contains(e.last, "txn.json stays in phase activating") {
		t.Errorf("output:\n%s", e.last)
	}
	e.ran(map[string]int{"stop-app": 1, "stop-all": 1, "reload-old": 0})
	if j, ok := e.journal(); !ok || j.Phase != PhaseActivating {
		t.Errorf("journal: %+v, %v", j, ok)
	}
	if _, ok := e.inhibited(); !ok {
		t.Errorf("not inhibited")
	}
	// Writable again: the next command resumes and goes back.
	e.runner.hook = nil
	if err := os.RemoveAll(block); err != nil {
		t.Fatal(err)
	}
	e.runner.reset()
	e.recover(resumeOnly)
	if rec := e.record(); rec.Result != ResultRolledBack || rec.RestoredHealth != "ok" {
		t.Errorf("record: %+v", rec)
	}
	e.links(rel2, rel1)
}

// hostList writes the host's stop list as lines and returns its path.
func (e *env) hostList(lines string) string {
	e.t.Helper()
	p := filepath.Join(e.root, PolicyDir, HostStopList)
	if err := os.WriteFile(p, []byte(lines), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

// TestHostStopListPrograms: the programs of the host's own stop list are
// checked as those of a release's lists are: a bare name, or an absolute
// path of a protected host program (root-owned as the switch checks it,
// not writable by group or others, one link, an execute bit), which then
// runs as that path below the root. Any other absolute path is refused
// when the list is loaded, with the code stop-list-refused, and a switch
// or rollback is refused with nothing done.
func TestHostStopListPrograms(t *testing.T) {
	setup := func(t *testing.T) (*env, string) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		dir := filepath.Join(e.root, "usr/local/bin")
		e.write(filepath.Join(dir, "stopper"), "#!/bin/sh\nexit 0\n", 0o755)
		return e, dir
	}
	t.Run("a protected program runs as that path below the root", func(t *testing.T) {
		// A first switch whose health check fails has nothing to go back
		// to: the host's stop list runs.
		e := newEnv(t)
		e.release(rel1, "s")
		stopper := filepath.Join(e.root, "usr/local/bin/stopper")
		e.write(stopper, "#!/bin/sh\nexit 0\n", 0o755)
		e.hostList("/usr/local/bin/stopper\n")
		e.runner.fail["health-probe"] = -1
		if code := e.switchTo(rel1); code != ExitFailed {
			t.Fatalf("exit %d\n%s", code, e.last)
		}
		found := false
		for _, c := range e.runner.calls {
			found = found || c == stopper
		}
		if !found {
			t.Errorf("the host's stopper did not run as its path below the root: %v", e.runner.calls)
		}
	})
	for _, tc := range []struct {
		name  string
		setup func(e *env, dir string)
	}{
		{"writable by others", func(e *env, dir string) { os.Chmod(filepath.Join(dir, "stopper"), 0o757) }},
		{"writable by its group", func(e *env, dir string) { os.Chmod(filepath.Join(dir, "stopper"), 0o775) }},
		{"no execute bit", func(e *env, dir string) { os.Chmod(filepath.Join(dir, "stopper"), 0o644) }},
		{"missing", func(e *env, dir string) { os.Remove(filepath.Join(dir, "stopper")) }},
		{"a link", func(e *env, dir string) {
			os.Rename(filepath.Join(dir, "stopper"), filepath.Join(dir, "real"))
			os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "stopper"))
		}},
		{"two links", func(e *env, dir string) { os.Link(filepath.Join(dir, "stopper"), filepath.Join(dir, "other")) }},
		{"in a folder writable by others", func(e *env, dir string) { os.Chmod(dir, 0o777) }},
		{"a workspace helper", func(e *env, dir string) {
			ws := t.TempDir()
			os.WriteFile(filepath.Join(ws, "helper"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
			e.hostList(filepath.Join(ws, "helper") + "\n")
		}},
		{"an unclean path", func(e *env, dir string) { e.hostList("/usr/local/bin/../bin/stopper\n") }},
	} {
		t.Run("refused: "+tc.name, func(t *testing.T) {
			e, dir := setup(t)
			e.hostList("/usr/local/bin/stopper\n")
			tc.setup(e, dir)
			t.Cleanup(func() { os.Chmod(dir, 0o755) })
			if got := e.status(); !strings.Contains(got, "host stop list: stop-list-refused") {
				t.Errorf("status:\n%s", got)
			}
			before := digest(t, e.root)
			e.runner.reset()
			if code := e.switchTo(rel3); code != ExitFailed || !strings.Contains(e.last, "stop-list-refused") {
				t.Fatalf("exit %d\n%s", code, e.last)
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

// TestRefusedHostStopListDoesNotBlockRecovery: a host stop list that is
// refused is reported loudly, but the other recovery steps still run. A
// journal in stopping runs the stop lists of the releases that verify; a
// journal in any other phase is resumed (here, going back after a failed
// health check); only new work is refused.
func TestRefusedHostStopListDoesNotBlockRecovery(t *testing.T) {
	refuse := func(e *env) {
		e.hostList("/usr/local/bin/stopper\n")
		stopper := filepath.Join(e.root, "usr/local/bin/stopper")
		e.write(stopper, "#!/bin/sh\nexit 0\n", 0o755)
		// Writable by others: not a protected program. (The mode is set
		// apart from the write, which the umask would narrow.)
		if err := os.Chmod(stopper, 0o757); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("stopping", func(t *testing.T) {
		e := newEnv(t)
		e.release(rel1, "s")
		e.runner.fail["health-probe"] = -1
		e.runner.fail["stop-app"] = 1
		if code := e.switchTo(rel1); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		refuse(e)
		e.runner.reset()
		if code := e.rollBack("previous"); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		e.ran(map[string]int{"stop-app": 1, "stop-all": 0, "stopper": 0})
		if j, ok := e.journal(); !ok || j.Phase != PhaseStopping || !strings.Contains(j.StopFailures, HostStopList) {
			t.Errorf("journal: %+v, %v", j, ok)
		}
		if !strings.Contains(e.last, "stop-list-refused") {
			t.Errorf("output:\n%s", e.last)
		}
	})
	t.Run("activating", func(t *testing.T) {
		// rel2 has lists of its own, so going back to it passes while
		// rel3's health check fails.
		e := activeOwn(t)
		e.crashAt(CrashActivating, false, rel3)
		refuse(e)
		e.runner.fail["health-probe"] = -1
		e.runner.reset()
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d\n%s", code, e.last)
		}
		// The verified release stops still run, but restoration waits for the
		// owner to repair the host stop list.
		if j, ok := e.journal(); !ok || j.Phase != PhaseRestoring {
			t.Fatalf("journal: %+v", j)
		}
		if _, held := e.inhibited(); !held {
			t.Fatal("cleared inhibit")
		}
		e.links(rel3, rel2)
		e.ran(map[string]int{"stop-app": 1, "stopper": 0, "stop-all": 0})
		if !strings.Contains(e.last, "stop-list-refused") || !strings.Contains(e.last, "nothing else done") {
			t.Errorf("output:\n%s", e.last)
		}
	})
}
