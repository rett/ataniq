package switcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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

// owed reads owed.json: the outcomes owed, none when it is missing.
func (e *env) owed() []owedOutcome {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.root, StateDir, OwedFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var f owedFile
	if err := json.Unmarshal(data, &f); err != nil {
		e.t.Fatal(err)
	}
	return f.Outcomes
}

// owes checks that the outcome of the transaction txn, of request
// requestID, with result, is the one outcome owed.
func (e *env) owes(txn, requestID, result string) {
	e.t.Helper()
	owed := e.owed()
	if len(owed) != 1 || owed[0].Txn != txn || owed[0].RequestID != requestID || owed[0].Result != result {
		e.t.Errorf("owed: %+v, want %s %s %s", owed, txn, requestID, result)
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
// a links phase puts the links back and goes back (the new release's stop
// list, the old release's reload and health); an activating phase runs
// the new release's reload and health again; a restoring phase stops the
// new release's services, puts the links back and runs the old release's
// reload and health again; a stopping phase runs every stop list again; a
// done phase records, after the reload list and health check of the
// release current points at ran again when the transaction ended healthy
// (a reboot may have come between its health check and its settling).
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
	// A links phase puts the links back and then goes back as a failed
	// health check does: the new release's services stopped, the old
	// release reloaded and checked.
	putBack := want{rel2, rel1, ResultRolledBack, "interrupted", "ok", false, with(map[string]int{"stop-all": 1, "stop-app": 1, "reload-old": 1, "health-old": 1})}
	completed := want{rel3, rel2, ResultOK, "", "", false, with(map[string]int{"stop-all": 1, "stop-old": 1, "reload-units": 1, "health-probe": 1})}
	recorded := want{rel3, rel2, ResultOK, "", "", false, with(map[string]int{"stop-all": 1, "stop-old": 1, "reload-units": 1, "health-probe": 1})}
	restored := want{rel2, rel1, ResultRolledBack, "health", "ok", false, with(map[string]int{"stop-all": 1, "stop-app": 1, "reload-old": 1, "health-old": 1})}
	restoredRecorded := restored
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
			if rec := e.record(); !reflect.DeepEqual(rec, before) && tc.point != CrashRecord {
				t.Errorf("deploy.json changed by a crash: %+v", rec)
			}
			if got := e.status(); !strings.Contains(got, "interrupted: switch to "+rel3+" in phase "+tc.phase) {
				t.Errorf("status:\n%s", got)
			}

			// The next command resumes it.
			e.keepOwed = true
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
				rec.ServicesStopped != tc.want.stopped || rec.CurrentID != tc.want.current || rec.Txn != j.Txn || rec.Requested != rel3 {
				t.Errorf("record: %+v", rec)
			}
			// Settled by another command: the outcome is owed.
			e.owes(j.Txn, "forward:"+rel3, tc.want.result)
			e.ran(tc.want.ran)
		})
	}
}

// TestCrashDoneRunsThePostCheckAgain: a switch killed right after its
// phase done was saved (CrashDone), resumed after a reboot, runs the
// post-activation no-AI check of the release current points at again, on
// the host as it now is, after its reload and health check and before
// decisions are no longer inhibited: the verdict from before the reboot is
// not reused. When that check now fails (the firewall changed while the
// service still answers its health check), decisions stay inhibited, the
// record says the check failed, the outcome is owed with the failed
// requalification, and the host's stop list runs.
func TestCrashDoneRunsThePostCheckAgain(t *testing.T) {
	for _, tc := range []struct {
		name string
		pass bool // the post-activation check passes after the reboot
	}{{"passes again", true}, {"fails after the reboot", false}} {
		t.Run(tc.name, func(t *testing.T) {
			e := activeOwn(t)
			e.keepOwed = true
			e.crashAt(CrashDone, false, rel3)
			if j, ok := e.journal(); !ok || j.Phase != PhaseDone || j.Check != CheckPassed {
				t.Fatalf("journal after the crash: %+v, %v", j, ok)
			}
			e.runner.reset()
			if !tc.pass {
				e.runner.failPost = e.relDir(rel3)
			}
			if code := e.recover(resumeOnly); code != ExitFailed {
				t.Fatalf("recover: exit %d\n%s", code, e.last)
			}
			// The checker is the one of the verified release current
			// points at, by its full path.
			post := postCall(filepath.Join(e.relDir(rel3), NoAICheckProgram), e.root, e.relDir(rel3))
			var order []string
			for _, c := range e.runner.calls {
				if c == post {
					order = append(order, "post-check")
					continue
				}
				order = append(order, strings.Fields(c)[0])
			}
			e.noJournal()
			rec := e.record()
			if rec.Result != ResultOK || rec.CurrentID != rel3 {
				t.Errorf("record: %+v", rec)
			}
			if tc.pass {
				if got := strings.Join(order, " "); got != "stop-all stop-old systemctl reload-units health-probe nft post-check" {
					t.Errorf("the resume ran %s, want the reload list, the health check and the post-activation check of %s", got, rel3)
				}
				if _, ok := e.inhibited(); ok {
					t.Errorf("still inhibited after the post-activation check passed again")
				}
				if rec.Check != CheckPassed {
					t.Errorf("record check %q, want %q", rec.Check, CheckPassed)
				}
				return
			}
			if got := strings.Join(order, " "); got != "stop-all stop-old systemctl reload-units health-probe nft post-check stop-all" {
				t.Errorf("the resume ran %s", got)
			}
			in, ok := e.inhibited()
			if !ok {
				t.Fatalf("decisions are no longer inhibited though the post-activation check failed after the reboot\n%s", e.last)
			}
			if !strings.Contains(in.Reason, "no-AI check") {
				t.Errorf("inhibit reason: %q", in.Reason)
			}
			if rec.Check != CheckFailed {
				t.Errorf("record check %q, want %q", rec.Check, CheckFailed)
			}
			if !strings.Contains(rec.Detail, "phase post") {
				t.Errorf("record detail: %q", rec.Detail)
			}
			if owed := e.owed(); len(owed) != 1 || !owed[0].RequalifyFailed {
				t.Errorf("owed: %+v", owed)
			}
			if !strings.Contains(e.last, "decisions stay inhibited") {
				t.Errorf("output:\n%s", e.last)
			}
		})
	}
}

// TestFailedRequalificationSurvivesASecondCrash: a switch killed in phase
// done (CrashDone) is resumed, its post-activation check fails after the
// reboot, and that resume is itself killed once the outcome is recorded
// and owed (CrashRecord), its journal still there. The next resume, whose
// check would pass now, does not turn the recorded failure into a
// success: the failed requalification is in the journal, the outcome stays
// owed with it, decisions stay inhibited and the command exits 1.
func TestFailedRequalificationSurvivesASecondCrash(t *testing.T) {
	e := activeOwn(t)
	e.keepOwed = true
	e.crashAt(CrashDone, false, rel3)
	e.runner.failPost = e.relDir(rel3)
	s := e.switcher()
	s.crashAt = func(p string) error {
		if p == CrashRecord {
			return errCrash
		}
		return nil
	}
	if code := s.Recover(context.Background(), resumeOnly); code != ExitFailed {
		t.Fatalf("the crashing resume: exit %d", code)
	}
	e.t.Logf("the crashing resume:\n%s", e.out.String())
	e.out.Reset()
	if owed := e.owed(); len(owed) != 1 || !owed[0].RequalifyFailed {
		t.Fatalf("owed after the crash: %+v", owed)
	}
	if j, ok := e.journal(); !ok || !j.RequalifyFailed {
		t.Fatalf("after the crashed resume: journal %+v, %v", j, ok)
	}
	if got := e.status(); !strings.Contains(got, "did not pass its reload list, health check and no-AI check again; the next command records that") {
		t.Errorf("status:\n%s", got)
	}
	// The post-activation check passes now.
	e.runner.failPost = ""
	e.runner.reset()
	if code := e.recover(resumeOnly); code != ExitFailed {
		t.Fatalf("resume: exit %d\n%s", code, e.last)
	}
	e.noJournal()
	if owed := e.owed(); len(owed) != 1 || owed[0].Result != ResultOK || !owed[0].RequalifyFailed {
		t.Errorf("the recorded failure was replaced: owed %+v", owed)
	}
	if _, ok := e.inhibited(); !ok {
		t.Errorf("decisions are no longer inhibited after a requalification that failed\n%s", e.last)
	}
	if rec := e.record(); rec.Check != CheckFailed || !strings.Contains(rec.Detail, "did not pass its reload, health check and no-AI check again") {
		t.Errorf("record: %+v", rec)
	}
	if !strings.Contains(e.last, "decisions stay inhibited") {
		t.Errorf("output:\n%s", e.last)
	}
	// The failure is carried, not qualified away by a second attempt.
	if n := e.runner.called("reload-units"); n != 0 {
		t.Errorf("the settling of a recorded failure ran the reload list %d times", n)
	}
}

// TestStatusWithholdsTheAckWhileSettling: a switch killed once its
// healthy outcome is recorded and owed (CrashRecord), its journal still
// there, has an outcome that settling can still change under the same
// transaction (the requalification may fail). status names that outcome
// as not final and prints no ack command for it, since ack is refused
// while the journal is open and the words would be given for an outcome
// that is not the one owed after settling. Once settled, status prints
// the ack command, and the outcome it acknowledges is the settled one.
func TestStatusWithholdsTheAckWhileSettling(t *testing.T) {
	e := activeOwn(t)
	e.keepOwed = true
	e.crashAt(CrashRecord, false, rel3)
	j, ok := e.journal()
	if !ok {
		t.Fatalf("no journal after the crash at %s", CrashRecord)
	}
	got := e.status()
	if strings.Contains(got, "ack --txn") {
		t.Errorf("status prints an ack command while the transaction is still open:\n%s", got)
	}
	if !strings.Contains(got, "outcome owed, not final (request forward:"+rel3+", transaction "+j.Txn+")") || !strings.Contains(got, "settling it may change this outcome") {
		t.Errorf("status does not say the outcome is not final:\n%s", got)
	}
	// The settling changes the outcome: current fails its health check
	// again.
	e.runner.fail["health-probe"] = -1
	if code := e.recover(resumeOnly); code != ExitFailed {
		t.Fatalf("recover: exit %d\n%s", code, e.last)
	}
	e.noJournal()
	owed := e.owed()
	if len(owed) != 1 || owed[0].Txn != j.Txn || !owed[0].RequalifyFailed {
		t.Fatalf("owed after settling: %+v", owed)
	}
	got = e.status()
	if !strings.Contains(got, "ah-switch ack --txn "+j.Txn+" --asked WORDS acknowledges it") || strings.Contains(got, "not final") {
		t.Errorf("status after settling:\n%s", got)
	}
}

// TestRequalifyUnverifiableStillStops: a switch killed in phase done is
// resumed after its release no longer verifies. Its services may run from
// the activation before the interruption (or from the boot), so the
// host's stop list runs, as when the reload, health or no-AI check fails
// again, and decisions stay inhibited.
func TestRequalifyUnverifiableStillStops(t *testing.T) {
	e := activeOwn(t)
	e.keepOwed = true
	e.crashAt(CrashDone, false, rel3)
	e.write(filepath.Join(e.relDir(rel3), "bin/app"), "not what SHA256SUMS says\n", 0o755)
	e.runner.reset()
	if code := e.recover(resumeOnly); code != ExitFailed {
		t.Fatalf("recover: exit %d\n%s", code, e.last)
	}
	e.noJournal()
	if n := e.runner.called("stop-all"); n != 2 {
		t.Errorf("the host's stop list ran %d times, want 2; commands: %v", n, e.runner.calls)
	}
	if n := e.runner.called("reload-units"); n != 0 {
		t.Errorf("a release that does not verify was reloaded")
	}
	if _, ok := e.inhibited(); !ok {
		t.Errorf("not inhibited")
	}
	if owed := e.owed(); len(owed) != 1 || !owed[0].RequalifyFailed {
		t.Errorf("owed: %+v", owed)
	}
	if rec := e.record(); !strings.Contains(rec.Detail, "the host's stop list ran") {
		t.Errorf("record detail: %q", rec.Detail)
	}
}

// TestRetryLeavesTheOutcomeOwed: the same request run again reports its
// owed outcome but does not take it back: only the owner's ack does. A
// caller allowed only the switch form (the deploying user) therefore cannot
// consume a failed outcome by asking again and then switch to another
// release, which, healthy, would remove the decision inhibit without the
// owner having looked at the host.
func TestRetryLeavesTheOutcomeOwed(t *testing.T) {
	e := active(t)
	e.keepOwed = true
	e.release(rel3, "2026-11-01")
	e.runner.fail["health-probe"] = -1
	e.runner.fail["health-old"] = -1
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("the failing switch: exit %d\n%s", code, e.last)
	}
	delete(e.runner.fail, "health-probe")
	delete(e.runner.fail, "health-old")
	rec := e.record()
	if rec.Result != ResultFailedRecovery || !rec.ServicesStopped {
		t.Fatalf("record: %+v", rec)
	}
	if _, ok := e.inhibited(); !ok {
		t.Fatalf("not inhibited after a failed recovery")
	}
	e.owes(rec.Txn, "forward:"+rel3, ResultFailedRecovery)
	// The same request, twice: reported each time, still owed.
	for i := 0; i < 2; i++ {
		if code := e.switchTo(rel3); code != ExitFailed || !strings.Contains(e.last, "has been settled already; it is not done again") {
			t.Fatalf("retry %d: exit %d\n%s", i+1, code, e.last)
		}
		e.owes(rec.Txn, "forward:"+rel3, ResultFailedRecovery)
	}
	// Another release, healthy: refused while the outcome is owed, and
	// the inhibit stays.
	e.release("0004-ddddddd", "2026-11-01")
	e.runner.reset()
	if code := e.switchTo("0004-ddddddd"); code != ExitFailed || !strings.Contains(e.last, "refused: an outcome is owed") {
		t.Errorf("another switch after the retries: exit %d\n%s", code, e.last)
	}
	if len(e.runner.calls) != 0 {
		t.Errorf("the refused switch ran %v", e.runner.calls)
	}
	if _, ok := e.inhibited(); !ok {
		t.Fatalf("the inhibit was removed without the owner's ack")
	}
	// The owner's ack takes it back; the next switch goes on.
	if code := e.ack("looked at the host"); code != ExitOK {
		t.Fatalf("ack: exit %d\n%s", code, e.last)
	}
	if code := e.switchTo("0004-ddddddd"); code != ExitOK {
		t.Errorf("the switch after the ack: exit %d\n%s", code, e.last)
	}
}

// TestRetryReportsSettled: a request retried after its transaction was
// interrupted reports the outcome the resume reached, and does not do the
// request again, for as long as the outcome is owed (until the owner's
// ack); a rollback retried after it was completed does not go back again
// to the release it left (it would, resolving previous anew).
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
			e.keepOwed = true
			e.crashAt(tc.point, true, "previous")
			if code := e.rollBack("previous"); code != tc.code {
				t.Fatalf("retry: exit %d", code)
			}
			if !strings.Contains(e.last, "settled: this rollback to previous has been settled already; it is not done again") {
				t.Errorf("output:\n%s", e.last)
			}
			e.links(tc.current, tc.previous)
			e.noJournal()
			if rec := e.record(); !rec.Rollback || len(e.owed()) != 1 {
				t.Errorf("record: %+v, owed %+v", rec, e.owed())
			}
			// Asked again before the owner's ack, it is reported again
			// and not done.
			if code := e.rollBack("previous"); code != tc.code || !strings.Contains(e.last, "has been settled already; it is not done again") {
				t.Fatalf("a second retry: exit %d\n%s", code, e.last)
			}
			e.links(tc.current, tc.previous)
			if code := e.ack("seen"); code != ExitOK {
				t.Fatalf("ack: exit %d\n%s", code, e.last)
			}
			// Asked again after it, it is a new rollback.
			if code := e.rollBack("previous"); code != ExitOK {
				t.Fatalf("a new rollback: exit %d", code)
			}
			e.links(tc.previous, tc.current)
		})
	}
	t.Run("settled by another command", func(t *testing.T) {
		e := active(t)
		e.keepOwed = true
		e.crashAt(CrashHealth, true, "previous")
		if code := e.recover(resumeOnly); code != ExitFailed {
			t.Fatalf("recover: exit %d", code)
		}
		e.links(rel1, rel2)
		if got := e.status(); !strings.Contains(got, "outcome owed (request rollback:"+rel1+", transaction ") {
			t.Errorf("status:\n%s", got)
		}
		if code := e.rollBack("previous"); code != ExitOK {
			t.Fatalf("retry: exit %d", code)
		}
		e.links(rel1, rel2)
	})
	t.Run("forward", func(t *testing.T) {
		e := active(t)
		// The retry leaves its outcome owed: the next request is a retry
		// again until the owner's ack, and only then the counter refusal
		// asserted below.
		e.keepOwed = true
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
		if code := e.switchTo(rel3); code != ExitOK || !strings.Contains(e.last, "settled: this switch to "+rel3) {
			t.Fatalf("a second retry: exit %d\n%s", code, e.last)
		}
		if code := e.ack("seen"); code != ExitOK {
			t.Fatalf("ack: exit %d\n%s", code, e.last)
		}
		// Asked again after the ack, it is a new switch, refused by its
		// used counter.
		if code := e.switchTo(rel3); code != ExitFailed || e.record().Error != CodeCounterNotAbove {
			t.Fatalf("a new switch: exit %d, %+v", code, e.record())
		}
	})
	t.Run("another request is not a retry", func(t *testing.T) {
		e := active(t)
		e.keepOwed = true
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashCurrent, false, rel3)
		e.release("0004-ddddddd", "2026-11-01")
		// It settles the interrupted switch, whose outcome is then owed:
		// it reports it and is refused until the owner acknowledges it.
		if code := e.switchTo("0004-ddddddd"); code != ExitFailed || !strings.Contains(e.last, "owed: the switch to "+rel3) {
			t.Fatalf("exit %d\n%s", code, e.last)
		}
		e.links(rel2, rel1)
		if code := e.ack("the put-back switch was seen"); code != ExitOK {
			t.Fatalf("ack: exit %d\n%s", code, e.last)
		}
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
		// Found in phase done: current is reloaded and checked again
		// before decisions are no longer inhibited.
		e.ran(map[string]int{"reload-units": 1, "health-probe": 1})
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
	// stop-app: going back first stops rel3's services, then the
	// stopping phase runs every list.
	e.ran(map[string]int{"reload-old": 0, "stop-all": 3, "stop-app": 2, "stop-old": 2})
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
		e.keepOwed = true
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashActivating, false, rel3)
		// The resume uses up the failing probes and goes back to rel2, as a
		// failed health check does; its outcome is owed, so the rollback is
		// refused until it is acknowledged, and then goes to rel1.
		e.runner.fail["health-probe"] = 30
		if code := e.rollBack("previous"); code != ExitFailed {
			t.Fatalf("rollback: exit %d", code)
		}
		if !strings.Contains(e.last, "back: current -> releases/"+rel2) || !strings.Contains(e.last, "owed: the switch to "+rel3) {
			t.Errorf("output:\n%s", e.last)
		}
		e.links(rel2, rel1)
		if code := e.ack("looked"); code != ExitOK {
			t.Fatalf("ack: exit %d", code)
		}
		if code := e.rollBack("previous"); code != ExitOK {
			t.Fatalf("rollback: exit %d", code)
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
			e.keepOwed = true
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
			// The retry reports the settled outcome; once the owner
			// acknowledges it, the request runs anew.
			if code := e.switchTo(rel3); code != ExitFailed || !strings.Contains(e.last, "settled: this switch to "+rel3) {
				t.Fatalf("retry: exit %d\n%s", code, e.last)
			}
			if code := e.ack("seen"); code != ExitOK {
				t.Fatalf("ack: exit %d\n%s", code, e.last)
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
// reports it and does not go back again, the outcome stays owed, and only
// the request after the owner's ack is a new one.
func TestDryRetryChangesNothing(t *testing.T) {
	e := active(t)
	e.keepOwed = true
	e.crashAt(CrashHealth, true, "previous")
	if code := e.recover(resumeOnly); code != ExitFailed {
		t.Fatalf("recover: exit %d", code)
	}
	e.links(rel1, rel2)
	if owed := e.owed(); len(owed) != 1 || owed[0].RequestID != "rollback:"+rel1 {
		t.Fatalf("owed: %+v", owed)
	}
	e.clock.sleep(time.Hour)
	e.runner.reset()
	before := digest(t, e.root)
	dry := e.switcher(func(c *Config) { c.Dry = true })
	code := dry.RollBack(context.Background(), "previous")
	out := e.out.String()
	e.out.Reset()
	if code != ExitOK || !strings.Contains(out, "settled: this rollback to previous has been settled already") || !strings.Contains(out, "dry run") {
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
	if owed := e.owed(); len(owed) != 1 || owed[0].RequestID != "rollback:"+rel1 {
		t.Errorf("owed: %+v", owed)
	}
	if code := e.ack("seen"); code != ExitOK {
		t.Fatalf("ack: exit %d\n%s", code, e.last)
	}
	if code := e.rollBack("previous"); code != ExitOK {
		t.Fatalf("a new rollback: exit %d", code)
	}
	e.links(rel2, rel1)
}

// TestResponseLostAfterJournalRemoved: a command that dies after its
// journal was removed and before its result line was printed leaves the
// outcome owed: owed.json holds it before the journal is removed. The
// retry reports it and does nothing else; a retried rollback is bound to
// the release it went to (its request id), never to previous resolved
// anew, whether it is asked as previous or by that id, and the outcome
// stays owed. A command that printed its result leaves it owed too: a line
// written is no proof that anyone read it; only the owner's ack clears it.
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
			e.keepOwed = true
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
			if rec.RequestID != want || rec.Result != ResultOK {
				t.Fatalf("record: %+v", rec)
			}
			e.owes(rec.Txn, want, ResultOK)
			if got := e.status(); !strings.Contains(got, "outcome owed (request "+want) {
				t.Errorf("status:\n%s", got)
			}
			e.runner.reset()
			var code int
			if tc.rollback {
				code = e.rollBack(tc.arg)
			} else {
				code = e.switchTo(tc.arg)
			}
			if code != ExitOK || !strings.Contains(e.last, "has been settled already; it is not done again") {
				t.Fatalf("retry: exit %d\n%s", code, e.last)
			}
			e.links(tc.cur, tc.pre)
			if len(e.runner.calls) != 0 {
				t.Errorf("the retry ran %v", e.runner.calls)
			}
			// Reported is not acknowledged: still owed.
			e.owes(rec.Txn, want, ResultOK)
		})
	}
	// A command that printed its result leaves it owed; the owner's ack
	// clears it, recorded with the words and the time.
	e := newEnv(t)
	e.keepOwed = true
	e.release(rel1, "2026-11-01")
	if code := e.switchTo(rel1); code != ExitOK || !strings.Contains(e.last, "result: ok") {
		t.Fatalf("switch: exit %d\n%s", code, e.last)
	}
	rec := e.record()
	e.owes(rec.Txn, "forward:"+rel1, ResultOK)
	if code := e.ack("result ok seen on the console"); code != ExitOK {
		t.Fatalf("ack: exit %d\n%s", code, e.last)
	}
	if owed := e.owed(); len(owed) != 0 {
		t.Errorf("owed after the ack: %+v", owed)
	}
	if rec := e.record(); rec.OutcomeAckedAsked != "result ok seen on the console" || rec.OutcomeAckedTxn != rec.Txn || rec.OutcomeAckedAt == "" {
		t.Errorf("record of the ack: %+v", rec)
	}
}

// TestStaleJournalUnresolved: a journal that does not match the host (its
// links are neither the journal's old state nor its new one, or a forward
// journal whose counter is below the counters the host attempted since) is
// stale, such as one restored from a backup: it is never resumed, nothing
// is run or moved, and only recover --discard-journal TXN, naming it,
// sets it aside.
func TestStaleJournalUnresolved(t *testing.T) {
	journalPath := func(e *env) string { return filepath.Join(e.root, StateDir, JournalFile) }
	t.Run("links moved on", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashActivating, false, rel3)
		old, _ := os.ReadFile(journalPath(e))
		j, _ := e.journal()
		if code := e.recover(resumeOnly); code != ExitFailed {
			t.Fatalf("resume: exit %d", code)
		}
		e.release("0004-ddddddd", "2026-11-01")
		if code := e.switchTo("0004-ddddddd"); code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		// The old journal comes back.
		e.write(journalPath(e), string(old), 0o600)
		e.release("0005-eeeeeee", "2026-11-01")
		e.runner.reset()
		if code := e.switchTo("0005-eeeeeee"); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(e.last, "does not match the host") || !strings.Contains(e.last, "recover --discard-journal "+j.Txn) {
			t.Errorf("output:\n%s", e.last)
		}
		e.links("0004-ddddddd", rel3)
		if len(e.runner.calls) != 0 {
			t.Errorf("commands run: %v", e.runner.calls)
		}
		if _, ok := e.journal(); !ok {
			t.Fatalf("the stale journal was removed")
		}
		if code := e.recover(RecoverOptions{DiscardJournal: strings.Repeat("0", 32)}); code != ExitFailed {
			t.Errorf("discard of another txn: exit %d", code)
		}
		if code := e.recover(RecoverOptions{DiscardJournal: j.Txn}); code != ExitOK {
			t.Fatalf("discard: exit %d\n%s", code, e.last)
		}
		e.noJournal()
		if kept, err := os.ReadFile(filepath.Join(e.root, StateDir, "txn-"+j.Txn+".discarded")); err != nil || string(kept) != string(old) {
			t.Errorf("the discarded journal is not kept: %v", err)
		}
		if code := e.switchTo("0005-eeeeeee"); code != ExitOK {
			t.Fatalf("after the discard: exit %d", code)
		}
	})
	t.Run("counter below the floor", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashLinks, false, rel3)
		// Counters attempted since, as far as the host shows.
		e.write(filepath.Join(e.root, StateDir, HighestFile), "5\n", 0o644)
		f, _ := os.OpenFile(filepath.Join(e.root, StateDir, CountersLog), os.O_APPEND|os.O_WRONLY, 0)
		f.WriteString("5 0005-eeeeeee 2026-11-21T12:00:00Z\n")
		f.Close()
		e.runner.reset()
		if code := e.recover(resumeOnly); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if !strings.Contains(e.last, "below 5") {
			t.Errorf("output:\n%s", e.last)
		}
		if _, ok := e.journal(); !ok {
			t.Fatalf("the stale journal was settled")
		}
		e.links(rel2, rel1)
	})
	t.Run("a matching journal is not discarded", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-01")
		e.crashAt(CrashCurrent, false, rel3)
		j, _ := e.journal()
		if code := e.recover(RecoverOptions{DiscardJournal: j.Txn}); code != ExitFailed || !strings.Contains(e.last, "matches the host") {
			t.Errorf("exit %d\n%s", code, e.last)
		}
		if _, ok := e.journal(); !ok {
			t.Errorf("a matching journal was discarded")
		}
	})
}

// owedPath is owed.json of the root of e.
func (e *env) owedPath() string { return filepath.Join(e.root, StateDir, OwedFile) }

// TestOwedSurvivesUnrelatedRefusal: a rollback whose reply was lost owes
// its outcome in owed.json, which no other record replaces. An unrelated
// request (here a release that is not there) reports that outcome, leaves
// it owed and is refused, changing nothing; the retried rollback then
// reports it, does not go back again to the release it left, and leaves it
// owed for the owner's ack.
func TestOwedSurvivesUnrelatedRefusal(t *testing.T) {
	e := active(t)
	e.keepOwed = true
	e.crashAt(CrashUnreported, true, "previous")
	e.links(rel1, rel2)
	if _, err := os.Stat(e.owedPath()); err != nil {
		t.Fatalf("owed.json: %v", err)
	}
	before := e.deployJSON()
	e.runner.reset()
	if code := e.switchTo("0009-fffffff"); code != ExitFailed {
		t.Fatalf("unrelated request: exit %d", code)
	}
	if !strings.Contains(e.last, "owed: the rollback to "+rel1) || !strings.Contains(e.last, "ah-switch ack --txn "+e.owed()[0].Txn+" --asked WORDS") {
		t.Errorf("output:\n%s", e.last)
	}
	if got := e.deployJSON(); got != before {
		t.Errorf("deploy.json changed by the refused request:\n%s", got)
	}
	if _, err := os.Stat(e.owedPath()); err != nil {
		t.Fatalf("owed.json after the unrelated request: %v", err)
	}
	if code := e.rollBack("previous"); code != ExitOK || !strings.Contains(e.last, "it is not done again") {
		t.Fatalf("retry: exit %d\n%s", code, e.last)
	}
	e.links(rel1, rel2)
	if len(e.runner.calls) != 0 {
		t.Errorf("commands run: %v", e.runner.calls)
	}
	if owed := e.owed(); len(owed) != 1 || owed[0].Direction != DirRollback || owed[0].ID != rel1 {
		t.Errorf("owed after the matching retry: %+v", owed)
	}
}

// brokenOut is an output whose writes (or, buffered, whose flush) fail,
// as a full filesystem or a dropped session does.
type brokenOut struct {
	flush bool // writes are accepted and held; Flush fails
}

func (b brokenOut) Write(p []byte) (int, error) {
	if b.flush {
		return len(p), nil
	}
	return 0, errors.New("no space left on device")
}

func (b brokenOut) Flush() error {
	if b.flush {
		return errors.New("no space left on device")
	}
	return nil
}

// TestOwedKeptWhenTheResultCannotBeWritten: a retry that cannot write or
// flush the outcome it reports has not delivered it: the outcome stays
// owed and the command fails, so that the next retry reports it and the
// owner's next --rollback-to previous cannot resolve the rollback's
// release again.
func TestOwedKeptWhenTheResultCannotBeWritten(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  brokenOut
	}{{"write fails", brokenOut{}}, {"flush fails", brokenOut{flush: true}}} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			e.keepOwed = true
			e.crashAt(CrashUnreported, true, "previous")
			e.links(rel1, rel2)
			e.runner.reset()
			s := e.switcher(func(c *Config) { c.Out, c.Err = tc.out, &e.out })
			if code := s.RollBack(context.Background(), "previous"); code != ExitFailed {
				t.Fatalf("retry with a broken output: exit %d\n%s", code, e.out.String())
			}
			e.out.Reset()
			if owed := e.owed(); len(owed) != 1 || owed[0].Direction != DirRollback || owed[0].ID != rel1 {
				t.Fatalf("owed after the failed write: %+v", owed)
			}
			e.links(rel1, rel2)
			if len(e.runner.calls) != 0 {
				t.Errorf("commands run: %v", e.runner.calls)
			}
			// The next retry, with a working output, reports it; it
			// stays owed for the owner's ack.
			if code := e.rollBack("previous"); code != ExitOK || !strings.Contains(e.last, "it is not done again") {
				t.Fatalf("next retry: exit %d\n%s", code, e.last)
			}
			e.links(rel1, rel2)
			if owed := e.owed(); len(owed) != 1 || owed[0].Direction != DirRollback || owed[0].ID != rel1 {
				t.Errorf("owed after it was reported: %+v", owed)
			}
		})
	}
}

// TestAck: ah-switch ack --asked WORDS acknowledges every outcome owed:
// it needs the words, is refused while a transaction is open and when
// nothing is owed, runs no program, and a dry run changes nothing. An
// owed.json that is not what a switch writes, or not trusted, refuses
// every switch and rollback (and ack) rather than being taken as nothing
// owed.
func TestAck(t *testing.T) {
	t.Run("refusals and dry run", func(t *testing.T) {
		e := active(t)
		e.keepOwed = true
		if code := e.ack("looked"); code != ExitFailed || !strings.Contains(e.last, "nothing to acknowledge") {
			t.Errorf("nothing owed: exit %d\n%s", code, e.last)
		}
		e.release(rel3, "2026-11-01")
		e.runner.fail["stop-app"] = -1
		e.runner.fail["health-probe"] = -1
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if _, ok := e.journal(); !ok {
			t.Fatalf("no journal open")
		}
		if code := e.ack("looked"); code != ExitFailed || !strings.Contains(e.last, "still open") {
			t.Errorf("journal open: exit %d\n%s", code, e.last)
		}
		delete(e.runner.fail, "stop-app")
		if code := e.recover(resumeOnly); code != ExitFailed {
			t.Fatalf("recover: exit %d", code)
		}
		if len(e.owed()) != 1 {
			t.Fatalf("owed: %+v", e.owed())
		}
		if code := e.ack(" "); code != ExitUsage {
			t.Errorf("no words: exit %d", code)
		}
		e.runner.reset()
		before := digest(t, e.root)
		if code := e.ack("looked", func(c *Config) { c.Dry = true }); code != ExitOK || digest(t, e.root) != before {
			t.Errorf("dry: exit %d\n%s", code, e.last)
		}
		if code := e.ack("looked"); code != ExitOK || len(e.owed()) != 0 {
			t.Errorf("ack: exit %d, owed %+v\n%s", code, e.owed(), e.last)
		}
		if len(e.runner.calls) != 0 {
			t.Errorf("commands run: %v", e.runner.calls)
		}
		if got := e.status(); !strings.Contains(got, "outcomes last acknowledged by the owner at") || strings.Contains(got, "outcome owed") {
			t.Errorf("status:\n%s", got)
		}
	})
	for _, tc := range []struct {
		name   string
		break_ func(path string)
	}{
		{"writable by others", func(path string) { os.Chmod(path, 0o666) }},
		{"not parsing", func(path string) { os.WriteFile(path, []byte("{\"outcomes\": []}"), 0o644) }},
		{"an unknown direction", func(path string) {
			data, _ := os.ReadFile(path)
			os.WriteFile(path, []byte(strings.Replace(string(data), `"direction": "forward"`, `"direction": "sideways"`, 1)), 0o644)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			e.keepOwed = true
			e.release(rel3, "2026-11-01")
			if code := e.switchTo(rel3); code != ExitOK {
				t.Fatalf("exit %d", code)
			}
			tc.break_(e.owedPath())
			before := e.deployJSON()
			e.runner.reset()
			if code := e.rollBack("previous"); code != ExitFailed || !strings.Contains(e.last, "state: FAILED") {
				t.Errorf("rollback: exit %d\n%s", code, e.last)
			}
			if code := e.switchTo(rel3); code != ExitFailed {
				t.Errorf("the same request: exit %d", code)
			}
			if code := e.ack("looked"); code != ExitFailed {
				t.Errorf("ack: exit %d", code)
			}
			if e.deployJSON() != before || len(e.runner.calls) != 0 {
				t.Errorf("changed: %v", e.runner.calls)
			}
			e.links(rel3, rel2)
		})
	}
}

// TestAckKeptWhenTheOutcomesCannotBeWritten: ack shows the outcomes it
// acknowledges first; when writing or flushing them fails the owner has not
// seen them, so nothing is recorded and owed.json stays, as with a retry
// that cannot report: the command exits 1, and the next ack with a working
// output acknowledges them.
func TestAckKeptWhenTheOutcomesCannotBeWritten(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  brokenOut
	}{{"write fails", brokenOut{}}, {"flush fails", brokenOut{flush: true}}} {
		t.Run(tc.name, func(t *testing.T) {
			e := active(t)
			e.keepOwed = true
			e.crashAt(CrashUnreported, true, "previous")
			e.links(rel1, rel2)
			e.runner.reset()
			owed := e.owed()
			if len(owed) != 1 {
				t.Fatalf("owed: %+v", owed)
			}
			before, recBefore := digest(t, e.root), e.record()
			var errs bytes.Buffer
			s := e.switcher(func(c *Config) { c.Out, c.Err = tc.out, &errs })
			if code := s.Ack(context.Background(), owedTxns(owed), "looked"); code != ExitFailed {
				t.Fatalf("ack with a broken output: exit %d", code)
			}
			if !strings.Contains(errs.String(), "stay owed") {
				t.Errorf("the failure is not said: %q", errs.String())
			}
			if after := digest(t, e.root); after != before {
				t.Error("the host's state changed")
			}
			if _, err := os.Stat(e.owedPath()); err != nil {
				t.Errorf("owed.json: %v", err)
			}
			if rec := e.record(); !reflect.DeepEqual(rec, recBefore) {
				t.Errorf("an acknowledgement was recorded: %+v", rec)
			}
			if got := e.owed(); !reflect.DeepEqual(got, owed) {
				t.Errorf("owed changed: %+v", got)
			}
			// With a working output the same ack goes through.
			if code := e.ack("looked"); code != ExitOK || len(e.owed()) != 0 {
				t.Errorf("next ack: exit %d, owed %+v\n%s", code, e.owed(), e.last)
			}
			if rec := e.record(); rec.OutcomeAckedAsked != "looked" {
				t.Errorf("record: %+v", rec)
			}
		})
	}
}

// TestAckCrashAfterRecord: an ack killed after deploy.json records the
// acknowledgement (its words, time and transactions) and before owed.json
// is removed leaves both. The transactions recorded are the commit mark:
// the next ack of the same outcomes only finishes the removal and keeps the
// owner's original receipt, its words and time, whatever words it is given.
func TestAckCrashAfterRecord(t *testing.T) {
	e := active(t)
	e.keepOwed = true
	e.release(rel3, "2026-11-01")
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatalf("switch: exit %d", code)
	}
	owed := e.owed()
	if len(owed) != 1 {
		t.Fatalf("owed: %+v", owed)
	}
	s := e.switcher()
	reached := false
	s.crashAt = func(p string) error {
		if p == CrashAcked {
			reached = true
			return errCrash
		}
		return nil
	}
	if code := s.Ack(context.Background(), owedTxns(owed), "the first look"); code != ExitFailed {
		t.Fatalf("ack killed after its record: exit %d\n%s", code, e.out.String())
	}
	e.out.Reset()
	if !reached {
		t.Fatalf("ack never reached %s", CrashAcked)
	}
	first := e.record()
	if first.OutcomeAckedAsked != "the first look" || first.OutcomeAckedTxn != owed[0].Txn || first.OutcomeAckedAt == "" {
		t.Fatalf("record after the crash: %+v", first)
	}
	if got := e.owed(); !reflect.DeepEqual(got, owed) {
		t.Fatalf("owed after the crash: %+v", got)
	}
	// Later, the owner acknowledges again, in other words.
	e.clock.sleep(time.Hour)
	e.runner.reset()
	if code := e.ack("other words later"); code != ExitOK {
		t.Fatalf("the retried ack: exit %d\n%s", code, e.last)
	}
	if got := e.owed(); len(got) != 0 {
		t.Errorf("owed after the retried ack: %+v", got)
	}
	rec := e.record()
	if rec.OutcomeAckedAsked != first.OutcomeAckedAsked || rec.OutcomeAckedAt != first.OutcomeAckedAt || rec.OutcomeAckedTxn != first.OutcomeAckedTxn {
		t.Errorf("the original receipt (%q at %s, %s) was overwritten: %q at %s, %s",
			first.OutcomeAckedAsked, first.OutcomeAckedAt, first.OutcomeAckedTxn, rec.OutcomeAckedAsked, rec.OutcomeAckedAt, rec.OutcomeAckedTxn)
	}
	if !strings.Contains(e.last, "recorded already") {
		t.Errorf("the retried ack does not say the acknowledgement was recorded already:\n%s", e.last)
	}
	if len(e.runner.calls) != 0 {
		t.Errorf("commands run: %v", e.runner.calls)
	}
	// Nothing is owed now: a later ack has nothing to acknowledge.
	if code := e.ack("once more"); code != ExitFailed || !strings.Contains(e.last, "nothing to acknowledge") {
		t.Errorf("ack with nothing owed: exit %d\n%s", code, e.last)
	}
}

// TestAckBoundToTheOutcomesSeen: ack names the transactions of the
// outcomes the owner saw. The ack of outcome A removes owed.json but its
// answer is lost; a switch to B then settles and owes B. Repeating A's ack
// must not take B back with words given before B was seen: it keeps A's
// receipt, leaves B owed and says so. An ack naming transactions that are
// not owed, and not acknowledged before, is refused and changes nothing.
func TestAckBoundToTheOutcomesSeen(t *testing.T) {
	const rel4 = "0004-ddddddd"
	e := active(t)
	e.keepOwed = true
	e.release(rel3, "2026-11-01")
	if code := e.switchTo(rel3); code != ExitOK {
		t.Fatalf("switch to %s: exit %d", rel3, code)
	}
	a := e.owedTxns()
	if !strings.Contains(e.last, "ah-switch ack --txn "+a[0]+" --asked WORDS") {
		t.Errorf("the switch does not show the ack command:\n%s", e.last)
	}
	if got := e.status(); !strings.Contains(got, "ah-switch ack --txn "+a[0]+" --asked WORDS acknowledges it") {
		t.Errorf("status:\n%s", got)
	}
	before := e.deployJSON()
	if code := e.ackTxns([]string{neverTxn}, "not what is owed"); code != ExitFailed || !strings.Contains(e.last, "refused") || !strings.Contains(e.last, "ah-switch ack --txn "+a[0]+" --asked WORDS") {
		t.Errorf("an ack of other transactions: exit %d\n%s", code, e.last)
	}
	if e.deployJSON() != before || !reflect.DeepEqual(e.owedTxns(), a) {
		t.Fatalf("a refused ack changed the state")
	}
	if code := e.ackTxns(a, "words for A"); code != ExitOK || len(e.owed()) != 0 {
		t.Fatalf("ack of A: exit %d\n%s", code, e.last)
	}
	receipt := e.record()
	// The answer was lost; the owner repeats it: nothing owed, A's receipt
	// stands.
	e.clock.sleep(time.Hour)
	if code := e.ackTxns(a, "words for A, again"); code != ExitOK || !strings.Contains(e.last, "recorded already") {
		t.Errorf("the repeated ack of A: exit %d\n%s", code, e.last)
	}
	if !reflect.DeepEqual(e.record(), receipt) {
		t.Errorf("the repeated ack changed the receipt: %+v", e.record())
	}
	// B settles and is owed; A's ack, repeated once more, leaves it owed.
	e.release(rel4, "2026-11-01")
	if code := e.switchTo(rel4); code != ExitOK {
		t.Fatalf("switch to %s: exit %d", rel4, code)
	}
	b := e.owedTxns()
	before = e.deployJSON()
	if code := e.ackTxns(a, "words for A"); code != ExitOK {
		t.Errorf("the repeated ack of A after B: exit %d\n%s", code, e.last)
	}
	if !reflect.DeepEqual(e.owedTxns(), b) {
		t.Fatalf("the repeated ack of A took B back: owed %+v", e.owed())
	}
	if e.deployJSON() != before {
		t.Errorf("the repeated ack of A changed deploy.json:\n%s", e.deployJSON())
	}
	if !strings.Contains(e.last, "not acknowledged by this") || !strings.Contains(e.last, "ah-switch ack --txn "+b[0]+" --asked WORDS") {
		t.Errorf("output:\n%s", e.last)
	}
	if code := e.ackTxns(b, "words for B"); code != ExitOK || len(e.owed()) != 0 {
		t.Fatalf("ack of B: exit %d\n%s", code, e.last)
	}
	if rec := e.record(); rec.OutcomeAckedTxn != b[0] || rec.OutcomeAckedAsked != "words for B" {
		t.Errorf("record: %+v", rec)
	}
}
