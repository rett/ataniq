package switcher

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var errCrash = errors.New("crash")

// crashAt runs a switch (or a rollback) that dies at the n-th time it
// reaches point (the first without n).
func (e *env) crashAt(point string, rollback bool, id string, n ...int) {
	e.t.Helper()
	s := e.switcher()
	want := 1
	if len(n) > 0 {
		want = n[0]
	}
	seen := 0
	reached := false
	s.crashAt = func(p string) error {
		if p == point {
			seen++
			if seen == want {
				reached = true
				return errCrash
			}
		}
		return nil
	}
	var code int
	if rollback {
		code = s.RollBack(context.Background(), id)
	} else {
		code = s.Switch(context.Background(), id)
	}
	e.t.Logf("crash at %s: exit %d\n%s", point, code, e.out.String())
	e.last = e.out.String()
	e.out.Reset()
	if !reached {
		e.t.Fatalf("the switch never reached %s", point)
	}
	if code != ExitFailed {
		e.t.Fatalf("crash at %s: exit %d", point, code)
	}
	if strings.Contains(e.last, "result:") {
		e.t.Fatalf("a crash printed a result:\n%s", e.last)
	}
}

func (e *env) journal() (journal, bool) {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.root, StateDir, JournalFile))
	if errors.Is(err, fs.ErrNotExist) {
		return journal{}, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		e.t.Fatal(err)
	}
	return j, true
}

// links checks where current and previous point.
func (e *env) links(current, previous string) {
	e.t.Helper()
	want := func(id string) string {
		if id == "" {
			return ""
		}
		return "releases/" + id
	}
	if got := e.link("current"); got != want(current) {
		e.t.Errorf("current -> %q, want %q", got, want(current))
	}
	if got := e.link("previous"); got != want(previous) {
		e.t.Errorf("previous -> %q, want %q", got, want(previous))
	}
}

// ran checks how many times each named program ran since the last reset.
func (e *env) ran(want map[string]int) {
	e.t.Helper()
	for name, n := range want {
		if got := e.runner.called(name); got != n {
			e.t.Errorf("%s ran %d times, want %d; commands: %v", name, got, n, e.runner.calls)
		}
	}
}

// noJournal checks that the journal is gone.
func (e *env) noJournal() {
	e.t.Helper()
	if j, ok := e.journal(); ok {
		e.t.Errorf("the journal stays in phase %s", j.Phase)
	}
}

// TestCrashAndResume: a transaction killed between any two of its durable
// steps is resumed by the next command from the phase its journal names,
// and ends where that phase leads, running exactly what that phase runs:
// a links phase puts the links back and runs nothing; an activating phase
// runs the new release's reload and health again; a restoring phase puts
// the links back and runs the old release's reload and health again; a
// stopping phase runs every stop list again; a done phase only records.
// The next command here is a recover, which then has nothing to do: the
// outcome is the transaction's own.
func TestCrashAndResume(t *testing.T) {
	type want struct {
		current, previous string
		result, step      string
		restored          string
		stopped           bool
		ran               map[string]int // in the resume
	}
	none := map[string]int{"reload-units": 0, "health-probe": 0, "reload-old": 0, "health-old": 0, "stop-all": 0, "stop-app": 0, "stop-old": 0}
	with := func(m map[string]int) map[string]int {
		out := map[string]int{}
		for k, v := range none {
			out[k] = v
		}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	putBack := want{rel2, rel1, ResultRolledBack, "interrupted", "", false, none}
	completed := want{rel3, rel2, ResultOK, "", "", false, with(map[string]int{"reload-units": 1, "health-probe": 1})}
	recorded := want{rel3, rel2, ResultOK, "", "", false, none}
	restored := want{rel2, rel1, ResultRolledBack, "health", "ok", false, with(map[string]int{"reload-old": 1, "health-old": 1})}
	restoredRecorded := want{rel2, rel1, ResultRolledBack, "health", "ok", false, none}
	stopped := want{rel2, rel1, ResultFailedRecovery, "health", "failed", true, with(map[string]int{"stop-all": 1, "stop-app": 1, "stop-old": 1})}
	stoppedRecorded := want{rel2, rel1, ResultFailedRecovery, "health", "failed", true, none}

	// Where a crash at each point leaves the links and the counter, before
	// anything resumes it.
	type crashed struct{ current, previous, highest string }
	before := crashed{rel2, rel1, "2"}   // nothing moved yet
	counted := crashed{rel2, rel1, "3"}  // the counter is written
	half := crashed{rel2, rel2, "3"}     // previous moved, current not yet
	moved := crashed{rel3, rel2, "3"}    // both links moved
	putBackC := crashed{rel2, rel1, "3"} // both links put back

	for _, tc := range []struct {
		name    string
		fail    map[string]int // what fails, in the run that crashes and in the resume
		point   string
		phase   string // the journal's phase after the crash
		crashed crashed
		want    want
	}{
		{"links", nil, CrashLinks, PhaseLinks, before, putBack},
		{"counter", nil, CrashCounter, PhaseLinks, counted, putBack},
		{"previous", nil, CrashPrevious, PhaseLinks, half, putBack},
		{"current", nil, CrashCurrent, PhaseLinks, moved, putBack},
		{"activating", nil, CrashActivating, PhaseActivating, moved, completed},
		{"reload", nil, CrashReload, PhaseActivating, moved, completed},
		{"health", nil, CrashHealth, PhaseActivating, moved, completed},
		{"postcheck", nil, CrashPostCheck, PhaseActivating, moved, completed},
		{"done", nil, CrashDone, PhaseDone, moved, recorded},
		{"record", nil, CrashRecord, PhaseDone, moved, recorded},
		{"restoring", map[string]int{"health-probe": -1}, CrashRestoring, PhaseRestoring, moved, restored},
		{"restore-links", map[string]int{"health-probe": -1}, CrashRestoreLinks, PhaseRestoring, putBackC, restored},
		{"restore-reload", map[string]int{"health-probe": -1}, CrashRestoreReload, PhaseRestoring, putBackC, restored},
		{"restored done", map[string]int{"health-probe": -1}, CrashDone, PhaseDone, putBackC, restoredRecorded},
		{"stopping", map[string]int{"health-probe": -1, "health-old": -1}, CrashStopping, PhaseStopping, putBackC, stopped},
		{"stopped", map[string]int{"health-probe": -1, "health-old": -1}, CrashStopped, PhaseStopping, putBackC, stopped},
		{"stopped done", map[string]int{"health-probe": -1, "health-old": -1}, CrashDone, PhaseDone, putBackC, stoppedRecorded},
		{"stopped record", map[string]int{"health-probe": -1, "health-old": -1}, CrashRecord, PhaseDone, putBackC, stoppedRecorded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := activeOwn(t)
			before := e.record()
			for k, v := range tc.fail {
				e.runner.fail[k] = v
			}
			e.crashAt(tc.point, false, rel3)
			j, ok := e.journal()
			if !ok || j.Phase != tc.phase || j.ID != rel3 || j.Counter != 3 || j.OldCurrent != "releases/"+rel2 || j.OldPrevious != "releases/"+rel1 || !j.CounterUsed {
				t.Fatalf("journal after the crash: %+v, %v", j, ok)
			}
			// Right after the crash, before any resume: a wrong state
			// here could be hidden by a resume that repairs it.
			e.links(tc.crashed.current, tc.crashed.previous)
			if got := e.highest(); got != tc.crashed.highest {
				t.Errorf("highest %q right after the crash, want %s", got, tc.crashed.highest)
			}
			if rec := e.record(); rec != before && tc.point != CrashRecord {
				t.Errorf("deploy.json changed by a crash: %+v", rec)
			}
			if got := e.status(); !strings.Contains(got, "interrupted: switch to "+rel3+" in phase "+tc.phase) {
				t.Errorf("status:\n%s", got)
			}

			// The next command resumes it.
			e.runner.reset()
			e.runner.fail = map[string]int{}
			for k, v := range tc.fail {
				e.runner.fail[k] = v
			}
			if code := e.recover(resumeOnly); code != ExitFailed {
				t.Fatalf("recover over a present counter: exit %d", code)
			}
			if !strings.Contains(e.last, "reconcile: an interrupted switch to "+rel3+" was found in phase "+tc.phase) {
				t.Errorf("output:\n%s", e.last)
			}
			e.noJournal()
			e.links(tc.want.current, tc.want.previous)
			if got := e.highest(); got != "3" {
				t.Errorf("highest %q, want 3", got)
			}
			rec := e.record()
			if rec.ID != rel3 || rec.Result != tc.want.result || rec.FailedStep != tc.want.step || rec.RestoredHealth != tc.want.restored ||
				rec.ServicesStopped != tc.want.stopped || rec.CurrentID != tc.want.current || rec.Txn != j.Txn || rec.Requested != rel3 || !rec.PendingReport {
				t.Errorf("record: %+v", rec)
			}
			e.ran(tc.want.ran)
		})
	}
}

// TestRetryReportsSettled: a request retried after its transaction was
// interrupted reports the outcome the resume reached, once, and does not do
// the request again; a rollback retried after it was completed does not go
// back again to the release it left (it would, resolving previous anew).
func TestRetryReportsSettled(t *testing.T) {
	for _, tc := range []struct {
		point             string
		current, previous string
		code              int
	}{
		// Interrupted before activation: put back, and reported as such.
		{CrashCurrent, rel2, rel1, ExitFailed},
		// Interrupted after the health check passed: completed.
		{CrashHealth, rel1, rel2, ExitOK},
	} {
		t.Run("rollback/"+tc.point, func(t *testing.T) {
			e := active(t)
			e.crashAt(tc.point, true, "previous")
			if code := e.rollBack("previous"); code != tc.code {
				t.Fatalf("retry: exit %d", code)
			}
			if !strings.Contains(e.last, "settled: this rollback to previous was interrupted and has been settled since; it is not done again") {
				t.Errorf("output:\n%s", e.last)
			}
			e.links(tc.current, tc.previous)
			e.noJournal()
			if rec := e.record(); rec.PendingReport || !rec.Rollback {
				t.Errorf("record: %+v", rec)
			}
			// Asked again, it is a new rollback.
			if code := e.rollBack("previous"); code != ExitOK {
				t.Fatalf("a new rollback: exit %d", code)
			}
			e.links(tc.previous, tc.current)
		})
	}
	t.Run("settled by another command", func(t *testing.T) {
		e := active(t)
		e.crashAt(CrashHealth, true, "previous")
		if code := e.recover(resumeOnly); code != ExitFailed {
			t.Fatalf("recover: exit %d", code)
		}
		e.links(rel1, rel2)
		if got := e.status(); !strings.Contains(got, "outcome not reported yet (request rollback:"+rel1+")") {
			t.Errorf("status:\n%s", got)
		}
		if code := e.rollBack("previous"); code != ExitOK {
			t.Fatalf("retry: exit %d", code)
		}
		e.links(rel1, rel2)
	})
	t.Run("forward", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashReload, false, rel3)
		e.runner.reset()
		if code := e.switchTo(rel3); code != ExitOK {
			t.Fatalf("retry: exit %d", code)
		}
		if !strings.Contains(e.last, "reconcile: an interrupted switch to "+rel3) || !strings.Contains(e.last, "settled: this switch to "+rel3) {
			t.Errorf("output:\n%s", e.last)
		}
		// The resume of activating runs the reload, the health and the
		// post-activation check again; the pre-activation check is not.
		e.ran(map[string]int{"reload-units": 1, "health-probe": 1, "ah-noaicheck": 1})
		// Asked again, it is a new switch, refused by its used counter.
		if code := e.switchTo(rel3); code != ExitFailed || e.record().Error != CodeCounterNotAbove {
			t.Fatalf("a new switch: exit %d, %+v", code, e.record())
		}
	})
	t.Run("another request is not a retry", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashCurrent, false, rel3)
		e.release("0004-ddddddd", "2026-11-01")
		if code := e.switchTo("0004-ddddddd"); code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		e.links("0004-ddddddd", rel2)
	})
}

// TestSyncFailureKeepsTheJournal: a link whose rename is visible but whose
// folder could not be synced is not taken as moved: the journal and its
// phase stay, the command fails without a result of ok, and the next
// command syncs and settles.
func TestSyncFailureKeepsTheJournal(t *testing.T) {
	saved := syncLinkDir
	t.Cleanup(func() { syncLinkDir = saved })

	t.Run("links", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		syncLinkDir = func(*os.Root) error { return errors.New("sync failed") }
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		syncLinkDir = saved
		if strings.Contains(e.last, "result: ok") || !strings.Contains(e.last, "result: unresolved") {
			t.Errorf("output:\n%s", e.last)
		}
		if j, ok := e.journal(); !ok || j.Phase != PhaseLinks {
			t.Fatalf("journal: %+v, %v", j, ok)
		}
		if rec := e.record(); rec.ID != rel2 || rec.Result != ResultOK {
			t.Errorf("deploy.json recorded the unsynced switch: %+v", rec)
		}
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("retry: exit %d", code)
		}
		e.noJournal()
		e.links(rel2, rel1)
		if rec := e.record(); rec.Result != ResultRolledBack || rec.Error != CodeInterrupted {
			t.Errorf("record: %+v", rec)
		}
	})
	t.Run("settle", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		// The two renames and the two syncs of activation succeed; the sync
		// before the outcome is recorded fails.
		n := 0
		syncLinkDir = func(r *os.Root) error {
			n++
			if n > 4 {
				return errors.New("sync failed")
			}
			return saved(r)
		}
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		syncLinkDir = saved
		if strings.Contains(e.last, "result: ok") {
			t.Errorf("ok before the link folder was synced:\n%s", e.last)
		}
		if j, ok := e.journal(); !ok || j.Phase != PhaseDone || j.Result != ResultOK {
			t.Fatalf("journal: %+v, %v", j, ok)
		}
		if rec := e.record(); rec.ID != rel2 {
			t.Errorf("deploy.json recorded before the sync: %+v", rec)
		}
		e.runner.reset()
		if code := e.switchTo(rel3); code != ExitOK {
			t.Fatalf("retry: exit %d", code)
		}
		e.noJournal()
		e.links(rel3, rel2)
		e.ran(map[string]int{"reload-units": 0, "health-probe": 0})
	})
}

// TestRestorePreviousFailing: going back with previous that cannot be put
// back is a failed recovery, not restored_health ok.
func TestRestorePreviousFailing(t *testing.T) {
	e := activeOwn(t)
	e.runner.fail["health-probe"] = -1
	// Once the switch moved its links, previous can no longer be renamed
	// over: its temporary name is taken by a folder that is not empty.
	e.runner.hook = func(argv []string) {
		if argv[0] == "health-probe" {
			os.MkdirAll(filepath.Join(e.root, BaseDir, "previous.new", "x"), 0o755)
		}
	}
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	rec := e.record()
	if rec.Result != ResultFailedRecovery || rec.RestoredHealth != "failed" || !rec.ServicesStopped || !strings.Contains(rec.Detail, "going back failed: previous") {
		t.Errorf("record: %+v", rec)
	}
	e.ran(map[string]int{"reload-old": 0, "stop-all": 1, "stop-app": 1, "stop-old": 1})
	e.noJournal()
}

// TestReconcileRecord: what the settling of a journal records.
func TestReconcileRecord(t *testing.T) {
	t.Run("put back", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashPrevious, false, rel3)
		if code := e.recover(resumeOnly); code != ExitFailed {
			t.Fatalf("recover with a counter present: exit %d", code)
		}
		rec := e.record()
		if rec.Result != ResultRolledBack || rec.Error != CodeInterrupted || rec.ID != rel3 || rec.CurrentID != rel2 || rec.PreviousID != rel2 {
			t.Errorf("record: %+v", rec)
		}
	})
	t.Run("completed", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashActivating, false, rel3)
		e.runner.reset()
		if code := e.recover(resumeOnly); code != ExitFailed {
			t.Fatalf("recover with a counter present: exit %d", code)
		}
		rec := e.record()
		if rec.Result != ResultOK || rec.ID != rel3 || rec.CurrentID != rel3 || rec.Detail != "completed after an interruption" {
			t.Errorf("record: %+v", rec)
		}
		e.ran(map[string]int{"reload-units": 1, "health-probe": 1})
	})
	t.Run("activating but unhealthy", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashActivating, false, rel3)
		// The resume uses up the failing probes and goes back to rel2, as a
		// failed health check does; the rollback then goes to rel1.
		e.runner.fail["health-probe"] = 30
		if code := e.rollBack("previous"); code != ExitOK {
			t.Fatalf("rollback: exit %d", code)
		}
		if !strings.Contains(e.last, "back: current -> releases/"+rel2) {
			t.Errorf("output:\n%s", e.last)
		}
		e.links(rel1, rel2)
		if e.highest() != "3" {
			t.Errorf("highest %q", e.highest())
		}
	})
	t.Run("dry run", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashPrevious, false, rel3)
		before := digest(t, e.root)
		if code := e.switchTo(rel3, func(c *Config) { c.Dry = true }); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if digest(t, e.root) != before {
			t.Errorf("a dry run settled the journal")
		}
	})
	t.Run("unreadable journal", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashPrevious, false, rel3)
		p := filepath.Join(e.root, StateDir, JournalFile)
		data, _ := os.ReadFile(p)
		e.write(p, strings.Replace(string(data), `"phase": "links"`, `"phase": "elsewhere"`, 1), 0o600)
		before := digest(t, e.root)
		if code := e.switchTo(rel3); code != ExitFailed || !strings.Contains(e.last, "look at") {
			t.Fatalf("exit %d\n%s", code, e.last)
		}
		if digest(t, e.root) != before {
			t.Errorf("an unreadable journal was acted on")
		}
	})
}

// TestCrashDuringRollback: a rollback that dies after writing previous is put
// back, and a rollback asked after the retry reported it goes through.
func TestCrashDuringRollback(t *testing.T) {
	e := active(t)
	e.crashAt(CrashPrevious, true, "previous")
	if got := e.link("previous"); got != "releases/"+rel2 {
		t.Fatalf("previous -> %q after the crash", got)
	}
	if code := e.rollBack("previous"); code != ExitFailed {
		t.Fatalf("retry: exit %d", code)
	}
	e.links(rel2, rel1)
	if code := e.rollBack("previous"); code != ExitOK {
		t.Fatalf("rollback again: exit %d", code)
	}
	e.links(rel1, rel2)
	if got := e.highest(); got != "2" {
		t.Errorf("a rollback moved the counter: %q", got)
	}
}

// TestMissingCounterIsRecovery: a host with a current release and no
// highest-counter is refused, never taken as 0, until recover writes it.
func TestMissingCounterIsRecovery(t *testing.T) {
	e := active(t)
	os.Remove(filepath.Join(e.root, StateDir, HighestFile))
	before := e.deployJSON()
	if code := e.switchTo(rel1); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(e.last, "counter-missing") || !strings.Contains(e.last, "ah-switch recover --counter N") {
		t.Errorf("output:\n%s", e.last)
	}
	if e.deployJSON() != before {
		t.Errorf("deploy.json was recorded over")
	}
	if code := e.rollBack("previous"); code != ExitFailed {
		t.Fatalf("rollback: exit %d", code)
	}
	if got := e.status(); !strings.Contains(got, "highest counter: missing; run ah-switch recover --counter N") {
		t.Errorf("status:\n%s", got)
	}
	counter := func(n uint64) int { return e.recover(RecoverOptions{SetCounter: true, Counter: n}) }
	if code := counter(1); code != ExitFailed || !strings.Contains(e.last, "below 2") {
		t.Fatalf("recover below the used counter: exit %d\n%s", code, e.last)
	}
	if code := counter(2); code != ExitOK || e.highest() != "2" {
		t.Fatalf("recover: exit %d, highest %q", code, e.highest())
	}
	if code := counter(5); code != ExitFailed {
		t.Fatalf("recover over a present counter: exit %d", code)
	}
	e.release(rel3, "2026-11-01")
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatalf("switch after recover: exit %d", code)
	}

	// A broken counter is recovered the same way.
	e.write(filepath.Join(e.root, StateDir, HighestFile), "x\n", 0o644)
	if code := counter(3); code != ExitOK || e.highest() != "3" {
		t.Fatalf("recover a broken counter: exit %d, highest %q", code, e.highest())
	}
}

// TestCrashInPackageStep: a switch that dies while its package step runs, or
// after it ran but before the journal says so, is recorded as
// package-step-interrupted with the package state unknown, and a switch to
// a release of the snapshot it went to runs the step again from the same
// snapshot; one that dies after the journal says done records the new
// snapshot, and the step is not run again. The links never moved.
func TestCrashInPackageStep(t *testing.T) {
	for _, tc := range []struct {
		point, snapshot, code string
		again                 int
	}{
		{CrashPackageStart, "2026-11-01", CodePackageInterrupted, 1},
		{CrashPackageRan, "2026-11-01", CodePackageInterrupted, 1},
		{CrashPackageDone, "2026-11-15", CodeInterrupted, 0},
	} {
		t.Run(tc.point, func(t *testing.T) {
			e := active(t)
			e.release(rel3, "2026-11-15")
			e.crashAt(tc.point, false, rel3)
			if j, ok := e.journal(); !ok || j.Phase != PhasePackages || j.PackageFrom != "2026-11-01" || j.PackageTo != "2026-11-15" || j.CounterUsed {
				t.Fatalf("journal: %+v, %v", j, ok)
			}
			if got := e.highest(); got != "2" {
				t.Errorf("highest %q: the counter is written with the links", got)
			}
			if code := e.recover(resumeOnly); code != ExitFailed {
				t.Fatalf("recover: exit %d", code)
			}
			rec := e.record()
			if rec.Result != ResultFailed || rec.Error != tc.code || rec.PackageSnapshot != tc.snapshot || rec.CurrentID != rel2 {
				t.Errorf("record: %+v", rec)
			}
			// A step not known to be done leaves the package state unknown.
			if unknown := tc.again == 1; (rec.PackageState == PackageUnknown) != unknown ||
				unknown && (rec.PackageFrom != "2026-11-01" || rec.PackageTo != "2026-11-15") {
				t.Errorf("package state: %+v", rec)
			}
			e.links(rel2, rel1)
			if got := e.highest(); got != "2" {
				t.Errorf("highest %q", got)
			}
			// The retry reports the settled outcome once, then runs anew.
			if code := e.switchTo(rel3); code != ExitFailed || !strings.Contains(e.last, "settled: this switch to "+rel3) {
				t.Fatalf("retry: exit %d\n%s", code, e.last)
			}
			e.runner.reset()
			if code := e.switchTo(rel3); code != ExitOK {
				t.Fatalf("switch again: exit %d", code)
			}
			if e.runner.called("ah-pkgstep") != tc.again {
				t.Errorf("package step runs: %v", e.runner.calls)
			}
			if tc.again == 1 && !strings.Contains(strings.Join(e.runner.calls, "\n"), "--from 2026-11-01 --to 2026-11-15") {
				t.Errorf("package step not run from the old snapshot: %v", e.runner.calls)
			}
		})
	}
}

// TestRecordFailureIsNotOK: a switch whose deploy.json cannot be written is
// not reported ok; its journal stays, and the next command records it.
func TestRecordFailureIsNotOK(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	deploy := filepath.Join(e.root, StateDir, DeployFile)
	// Once the switch has read deploy.json, a folder takes its name.
	e.runner.hook = func(argv []string) {
		if argv[0] == "health-probe" {
			os.Remove(deploy)
			os.Mkdir(deploy, 0o755)
		}
	}
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if strings.Contains(e.last, "result: ok") || !strings.Contains(e.last, "result: unresolved: the outcome (ok) is not recorded yet") {
		t.Errorf("output:\n%s", e.last)
	}
	if j, ok := e.journal(); !ok || j.Phase != PhaseDone {
		t.Fatalf("the journal was removed although the outcome is not recorded")
	}
	e.runner.hook = nil
	os.Remove(deploy)
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatalf("retry: exit %d", code)
	}
	if !strings.Contains(e.last, "settled: this switch to "+rel3) {
		t.Errorf("output:\n%s", e.last)
	}
	e.noJournal()
}

// TestStatusTakesTheLock: status reads under the shared lock, so it is
// refused while a switch runs, and prints nothing when part of the state
// cannot be read.
func TestStatusTakesTheLock(t *testing.T) {
	e := active(t)
	state, err := os.OpenRoot(filepath.Join(e.root, StateDir))
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	held, err := takeLock(state, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code := e.switcher().Status(); code != ExitLocked {
		t.Errorf("status during a switch: exit %d", code)
	}
	e.out.Reset()
	held.release()

	// A shared lock held by another status does not stop status, but stops
	// a switch.
	shared, err := takeLock(state, false, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.status()
	e.release(rel3, "2026-11-01")
	if code := e.switchTo(rel3); code != ExitLocked {
		t.Errorf("switch during status: exit %d", code)
	}
	shared.release()

	e.write(filepath.Join(e.root, StateDir, DeployFile), "{", 0o644)
	if code := e.switcher().Status(); code != ExitFailed || strings.Contains(e.out.String(), "current:") {
		t.Errorf("status of a broken deploy.json: exit %d, printed %q", code, e.out.String())
	}
	e.out.Reset()
}

// TestResumeCrashesToo: a resume killed at a point of its own is resumed
// again by the next command, and ends where one resume would have: each
// phase is safe to redo from its start.
func TestResumeCrashesToo(t *testing.T) {
	for _, tc := range []struct {
		fail          map[string]int
		first, second string
		current       string
		result        string
		stopped       bool
	}{
		{nil, CrashCurrent, CrashPutBack, rel2, ResultRolledBack, false},
		{nil, CrashActivating, CrashReload, rel3, ResultOK, false},
		{nil, CrashActivating, CrashPostCheck, rel3, ResultOK, false},
		{map[string]int{"health-probe": -1}, CrashRestoring, CrashRestoreLinks, rel2, ResultRolledBack, false},
		{map[string]int{"health-probe": -1}, CrashRestoreReload, CrashRestoreReload, rel2, ResultRolledBack, false},
		{map[string]int{"health-probe": -1, "health-old": -1}, CrashStopping, CrashStopped, rel2, ResultFailedRecovery, true},
		{nil, CrashDone, CrashRecord, rel3, ResultOK, false},
	} {
		t.Run(tc.first+"/"+tc.second, func(t *testing.T) {
			e := activeOwn(t)
			for k, v := range tc.fail {
				e.runner.fail[k] = v
			}
			e.crashAt(tc.first, false, rel3)
			s := e.switcher()
			s.crashAt = func(p string) error {
				if p == tc.second {
					return errCrash
				}
				return nil
			}
			if code := s.Recover(context.Background(), resumeOnly); code != ExitFailed {
				t.Fatalf("the crashing resume: exit %d", code)
			}
			e.out.Reset()
			if _, ok := e.journal(); !ok {
				t.Fatalf("a crashed resume removed the journal")
			}
			if code := e.recover(resumeOnly); code != ExitFailed {
				t.Fatalf("resume: exit %d", code)
			}
			e.noJournal()
			if got := e.link("current"); got != "releases/"+tc.current {
				t.Errorf("current -> %q, want %s", got, tc.current)
			}
			if rec := e.record(); rec.Result != tc.result || rec.ServicesStopped != tc.stopped || e.highest() != "3" {
				t.Errorf("record: %+v, highest %q", rec, e.highest())
			}
		})
	}
}

// TestDryRetryChangesNothing: a dry run of a request whose settled outcome
// is still to be reported shows that outcome and changes nothing (not the
// record, not its time, not the report still owed); the real retry then
// reports it once and does not go back again, and only the request after
// that is a new one.
func TestDryRetryChangesNothing(t *testing.T) {
	e := active(t)
	e.crashAt(CrashHealth, true, "previous")
	if code := e.recover(resumeOnly); code != ExitFailed {
		t.Fatalf("recover: exit %d", code)
	}
	e.links(rel1, rel2)
	if rec := e.record(); !rec.PendingReport || rec.RequestID != "rollback:"+rel1 {
		t.Fatalf("record: %+v", rec)
	}
	e.clock.sleep(time.Hour)
	e.runner.reset()
	before := digest(t, e.root)
	dry := e.switcher(func(c *Config) { c.Dry = true })
	code := dry.RollBack(context.Background(), "previous")
	out := e.out.String()
	e.out.Reset()
	if code != ExitOK || !strings.Contains(out, "settled: this rollback to previous was interrupted and has been settled since") || !strings.Contains(out, "dry run") {
		t.Fatalf("dry retry: exit %d\n%s", code, out)
	}
	if digest(t, e.root) != before {
		t.Errorf("a dry run changed the tree")
	}
	if len(e.runner.calls) != 0 {
		t.Errorf("a dry run ran %v", e.runner.calls)
	}
	// The real retry still finds the outcome to report.
	if code := e.rollBack("previous"); code != ExitOK || !strings.Contains(e.last, "settled: this rollback to previous") {
		t.Fatalf("retry: exit %d\n%s", code, e.last)
	}
	e.links(rel1, rel2)
	if rec := e.record(); rec.PendingReport {
		t.Errorf("record: %+v", rec)
	}
	if code := e.rollBack("previous"); code != ExitOK {
		t.Fatalf("a new rollback: exit %d", code)
	}
	e.links(rel2, rel1)
}

// TestResponseLostAfterJournalRemoved: a command that dies after its
// journal was removed and before its result line was printed leaves the
// outcome to report: the record says pending_report before the journal is
// removed, and only the command that printed the result clears it. The
// retry reports it and does nothing else; a retried rollback is bound to
// the release it went to (its request id), never to previous resolved
// anew, whether it is asked as previous or by that id.
func TestResponseLostAfterJournalRemoved(t *testing.T) {
	for _, tc := range []struct {
		name     string
		rollback bool
		arg      string // the retry
		cur, pre string // the links after the first command
	}{
		{"switch", false, rel3, rel3, rel2},
		{"rollback, retried as previous", true, "previous", rel1, rel2},
		{"rollback, retried by its id", true, rel1, rel1, rel2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			e.release(rel3, "2026-11-01")
			first := rel3
			if tc.rollback {
				first = "previous"
			}
			e.crashAt(CrashUnreported, tc.rollback, first)
			e.noJournal()
			e.links(tc.cur, tc.pre)
			rec := e.record()
			want := "forward:" + rel3
			if tc.rollback {
				want = "rollback:" + rel1
			}
			if !rec.PendingReport || rec.RequestID != want || rec.Result != ResultOK {
				t.Fatalf("record: %+v", rec)
			}
			if got := e.status(); !strings.Contains(got, "not reported yet") {
				t.Errorf("status:\n%s", got)
			}
			e.runner.reset()
			var code int
			if tc.rollback {
				code = e.rollBack(tc.arg)
			} else {
				code = e.switchTo(tc.arg)
			}
			if code != ExitOK || !strings.Contains(e.last, "has been settled since; it is not done again") {
				t.Fatalf("retry: exit %d\n%s", code, e.last)
			}
			e.links(tc.cur, tc.pre)
			if len(e.runner.calls) != 0 {
				t.Errorf("the retry ran %v", e.runner.calls)
			}
			if rec := e.record(); rec.PendingReport {
				t.Errorf("record after the report: %+v", rec)
			}
		})
	}
	// A command that printed its result clears the report it owed.
	e := active(t)
	if rec := e.record(); rec.PendingReport || rec.RequestID != "forward:"+rel2 {
		t.Errorf("record of a reported switch: %+v", rec)
	}
}
