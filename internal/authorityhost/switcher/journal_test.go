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
)

var errCrash = errors.New("crash")

// crashAt runs a switch (or a rollback) that dies at point.
func (e *env) crashAt(point string, rollback bool, id string) {
	e.t.Helper()
	s := e.switcher()
	s.crashAt = func(p string) error {
		if p == point {
			return errCrash
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
	e.out.Reset()
	if code != ExitFailed {
		e.t.Fatalf("crash at %s: exit %d", point, code)
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

// TestCrashAtEachPoint: a switch from rel2 to rel3 dies after each of its
// durable steps. The journal is there with the old links; current points
// at a whole release; the next command settles it: a switch whose current
// already moved and is healthy is completed, any other is put back exactly,
// and previous never ends up pointing at the active release. The counter
// stays used either way.
func TestCrashAtEachPoint(t *testing.T) {
	for _, tc := range []struct {
		point             string
		current, previous string // after the crash
		done              bool   // the next command completes the switch
	}{
		{CrashJournal, rel2, rel1, false},
		{CrashCounter, rel2, rel1, false},
		{CrashPrevious, rel2, rel2, false},
		{CrashCurrent, rel3, rel2, true},
		{CrashHealth, rel3, rel2, true},
	} {
		t.Run(tc.point, func(t *testing.T) {
			e := active(t)
			e.release(rel3, "2026-11-01")
			before := e.record()
			e.crashAt(tc.point, false, rel3)

			j, ok := e.journal()
			if !ok || j.ID != rel3 || j.Counter != 3 || j.OldCurrent != "releases/"+rel2 || j.OldPrevious != "releases/"+rel1 || j.Phase != PhaseLinks {
				t.Fatalf("journal after the crash: %+v, %v", j, ok)
			}
			if got := e.link("current"); got != "releases/"+tc.current {
				t.Errorf("current -> %q after the crash, want %s", got, tc.current)
			}
			if got := e.link("previous"); got != "releases/"+tc.previous {
				t.Errorf("previous -> %q after the crash, want %s", got, tc.previous)
			}
			if rec := e.record(); rec != before {
				t.Errorf("deploy.json changed by a crash: %+v", rec)
			}
			want := "3"
			if tc.point == CrashJournal {
				want = "2"
			}
			if got := e.highest(); got != want {
				t.Errorf("highest %q after the crash, want %s", got, want)
			}
			if got := e.status(); !strings.Contains(got, "interrupted: switch to "+rel3) {
				t.Errorf("status does not show the interrupted switch:\n%s", got)
			}

			// The retry settles the journal first, then is refused: the
			// counter was attempted.
			if code := e.switchTo(rel3); code != ExitFailed {
				t.Fatalf("retry: exit %d", code)
			}
			if _, ok := e.journal(); ok {
				t.Errorf("the journal is still there")
			}
			wantCur, wantPrev := rel2, rel1
			if tc.done {
				wantCur, wantPrev = rel3, rel2
				if !strings.Contains(e.last, "reconcile: completed: current -> "+rel3) {
					t.Errorf("output:\n%s", e.last)
				}
			} else if !strings.Contains(e.last, "reconcile: put back") {
				t.Errorf("output:\n%s", e.last)
			}
			if got := e.link("current"); got != "releases/"+wantCur {
				t.Errorf("current -> %q, want %s", got, wantCur)
			}
			if got := e.link("previous"); got != "releases/"+wantPrev {
				t.Errorf("previous -> %q, want %s", got, wantPrev)
			}
			if got := e.highest(); got != "3" {
				t.Errorf("highest %q, want 3", got)
			}
			if rec := e.record(); rec.FailedStep != "counter" || rec.Error != CodeCounterNotAbove {
				t.Errorf("the retry: %+v", rec)
			}
		})
	}
}

// TestReconcileRecord: what the settling of a journal records.
func TestReconcileRecord(t *testing.T) {
	t.Run("put back", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashPrevious, false, rel3)
		if code := e.switcher().Recover(context.Background(), 9); code != ExitFailed {
			t.Fatalf("recover with a counter present: exit %d", code)
		}
		e.out.Reset()
		rec := e.record()
		if rec.Result != ResultRolledBack || rec.Error != CodeInterrupted || rec.ID != rel3 || rec.CurrentID != rel2 || rec.PreviousID != rel2 {
			t.Errorf("record: %+v", rec)
		}
	})
	t.Run("completed", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashCurrent, false, rel3)
		e.runner.reset()
		if code := e.switcher().Recover(context.Background(), 9); code != ExitFailed {
			t.Fatalf("recover with a counter present: exit %d", code)
		}
		e.out.Reset()
		rec := e.record()
		if rec.Result != ResultOK || rec.ID != rel3 || rec.CurrentID != rel3 || rec.Detail != "completed after an interruption" {
			t.Errorf("record: %+v", rec)
		}
		if e.runner.called("reload-units") != 1 || e.runner.called("health-probe") != 1 {
			t.Errorf("completing ran %v", e.runner.calls)
		}
	})
	t.Run("current moved but unhealthy", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashCurrent, false, rel3)
		// The settling uses up the failing probes and goes back to rel2
		// as a failed health check does; the rollback then goes to rel1.
		e.runner.fail["health-probe"] = 30
		if code := e.rollBack("previous"); code != ExitOK {
			t.Fatalf("rollback: exit %d", code)
		}
		if !strings.Contains(e.last, "cannot be completed") || !strings.Contains(e.last, "back: current -> releases/"+rel2) {
			t.Errorf("output:\n%s", e.last)
		}
		if e.link("current") != "releases/"+rel1 || e.link("previous") != "releases/"+rel2 || e.highest() != "3" {
			t.Errorf("current -> %q, previous -> %q, highest %q", e.link("current"), e.link("previous"), e.highest())
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
}

// TestCrashDuringRollback: a rollback that dies after writing previous is put
// back, and the rollback run again then goes through.
func TestCrashDuringRollback(t *testing.T) {
	e := active(t)
	e.crashAt(CrashPrevious, true, "previous")
	if got := e.link("previous"); got != "releases/"+rel2 {
		t.Fatalf("previous -> %q after the crash", got)
	}
	if code := e.rollBack("previous"); code != ExitOK {
		t.Fatalf("rollback again: exit %d", code)
	}
	if e.link("current") != "releases/"+rel1 || e.link("previous") != "releases/"+rel2 {
		t.Errorf("current -> %q, previous -> %q", e.link("current"), e.link("previous"))
	}
	if got := e.highest(); got != "2" {
		t.Errorf("a rollback moved the counter: %q", got)
	}
}

// TestMissingCounterIsRecovery: a host with a current release and no
// highest-counter is refused, never taken as 0, until recover writes it.
func TestMissingCounterIsRecovery(t *testing.T) {
	e := active(t)
	e.release(rel3, "2026-11-01")
	os.Remove(filepath.Join(e.root, StateDir, HighestFile))
	if code := e.switchTo(rel1); code != ExitFailed {
		t.Fatalf("exit %d", code)
	}
	if rec := e.record(); rec.Error != CodeCounterMissing || !strings.Contains(rec.Detail, "ah-switch recover --counter N") {
		t.Errorf("record: %+v", rec)
	}
	if code := e.rollBack("previous"); code != ExitFailed {
		t.Fatalf("rollback: exit %d", code)
	}
	if got := e.status(); !strings.Contains(got, "highest counter: missing; run ah-switch recover --counter N") {
		t.Errorf("status:\n%s", got)
	}
	recover := func(n uint64) int {
		code := e.switcher().Recover(context.Background(), n)
		e.last = e.out.String()
		e.out.Reset()
		return code
	}
	if code := recover(1); code != ExitFailed || !strings.Contains(e.last, "below 2") {
		t.Fatalf("recover below the used counter: exit %d\n%s", code, e.last)
	}
	if code := recover(2); code != ExitOK || e.highest() != "2" {
		t.Fatalf("recover: exit %d, highest %q", code, e.highest())
	}
	if code := recover(5); code != ExitFailed {
		t.Fatalf("recover over a present counter: exit %d", code)
	}
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatalf("switch after recover: exit %d", code)
	}

	// A broken counter is recovered the same way.
	e.write(filepath.Join(e.root, StateDir, HighestFile), "x\n", 0o644)
	if code := recover(3); code != ExitOK || e.highest() != "3" {
		t.Fatalf("recover a broken counter: exit %d, highest %q", code, e.highest())
	}
}
