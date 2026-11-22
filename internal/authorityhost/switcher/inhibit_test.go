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

func (e *env) inhibitPath() string { return filepath.Join(e.root, StateDir, InhibitFile) }

// inhibited reads decisions.inhibit: its content, and whether it is there.
func (e *env) inhibited() (inhibit, bool) {
	e.t.Helper()
	data, err := os.ReadFile(e.inhibitPath())
	if errors.Is(err, fs.ErrNotExist) {
		return inhibit{}, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	var in inhibit
	if err := json.Unmarshal(data, &in); err != nil {
		e.t.Fatal(err)
	}
	return in, true
}

// TestDecisionInhibit: decisions.inhibit is written (mode 0644) at the
// start of every transaction, before its package step and before its
// links, and removed only when the transaction ends healthy: the release
// passed its health check and its post-activation check, or the release
// gone back to passed its reload, health check and post-activation check.
// Any other end leaves it, with its reason, for as long as nothing else
// removes it (a reboot included: it is a file).
func TestDecisionInhibit(t *testing.T) {
	t.Run("a healthy switch", func(t *testing.T) {
		e := active(t)
		e.release(rel3, "2026-11-15")
		seen := map[string]inhibit{}
		e.runner.hook = func(argv []string) {
			name := filepath.Base(argv[0])
			if name == "ah-pkgstep" || name == "reload-units" {
				if in, ok := e.inhibited(); ok {
					seen[name] = in
				}
			}
			if name == "ah-noaicheck" && len(seen) == 0 {
				if _, ok := e.inhibited(); ok {
					t.Errorf("inhibited before the first check, which begins nothing")
				}
			}
		}
		if code := e.switchTo(rel3); code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		txn := e.record().Txn
		for _, name := range []string{"ah-pkgstep", "reload-units"} {
			if in, ok := seen[name]; !ok || in.Txn != txn || !strings.Contains(in.Reason, "under way") {
				t.Errorf("while %s ran: %+v, %v", name, in, ok)
			}
		}
		if _, ok := e.inhibited(); ok {
			t.Errorf("still inhibited after a healthy switch")
		}
		if !strings.Contains(e.last, "decisions no longer inhibited") {
			t.Errorf("output:\n%s", e.last)
		}
	})
	t.Run("a first switch that fails, across a reboot", func(t *testing.T) {
		e := newEnv(t)
		e.release(rel1, "s")
		e.runner.fail["health-probe"] = -1
		if code := e.switchTo(rel1); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		in, ok := e.inhibited()
		if !ok || in.Txn != e.record().Txn || !strings.Contains(in.Reason, "ended failed at health") || !strings.Contains(in.Reason, "stopped") {
			t.Fatalf("inhibit: %+v, %v", in, ok)
		}
		if got := mode(t, e.inhibitPath()); got != 0o644 {
			t.Errorf("mode %v", got)
		}
		// A new process (as after a reboot) sees it as it was.
		if got := e.status(); !strings.Contains(got, "decisions: inhibited (decisions.inhibit)") || !strings.Contains(got, "ended failed at health") {
			t.Errorf("status:\n%s", got)
		}
		delete(e.runner.fail, "health-probe")
		e.release(rel2, "s")
		if code := e.switchTo(rel2); code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		if _, ok := e.inhibited(); ok {
			t.Errorf("still inhibited after a healthy switch")
		}
		if got := e.status(); !strings.Contains(got, "decisions: not inhibited") {
			t.Errorf("status:\n%s", got)
		}
	})
	t.Run("going back, healthy", func(t *testing.T) {
		e := activeOwn(t)
		e.runner.fail["health-probe"] = -1
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if rec := e.record(); rec.Result != ResultRolledBack || rec.RestoredHealth != "ok" {
			t.Errorf("record: %+v", rec)
		}
		if _, ok := e.inhibited(); ok {
			t.Errorf("still inhibited after going back to a release that passed")
		}
	})
	t.Run("going back to a release that fails its check", func(t *testing.T) {
		e := activeOwn(t)
		e.runner.fail["health-probe"] = -1
		e.runner.failPost = e.relDir(rel2)
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		rec := e.record()
		if rec.Result != ResultFailedRecovery || rec.RestoredHealth != "failed" || !rec.ServicesStopped || !strings.Contains(rec.Detail, "no-AI check of "+rel2) {
			t.Errorf("record: %+v", rec)
		}
		if _, ok := e.inhibited(); !ok {
			t.Errorf("not inhibited after a failed recovery")
		}
	})
	t.Run("the new release unverifiable when going back", func(t *testing.T) {
		e := activeOwn(t)
		e.runner.fail["health-probe"] = -1
		e.crashAt(CrashRestoring, false, rel3)
		makeWritable(e.relDir(rel3))
		os.WriteFile(filepath.Join(e.relDir(rel3), "bin/app"), []byte("changed\n"), 0o755)
		e.runner.reset()
		if code := e.recover(resumeOnly); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		// Its own stop list cannot be trusted: the host's runs, before
		// the earlier release is reloaded.
		var order []string
		for _, c := range e.runner.calls {
			if f := strings.Fields(c)[0]; f == "stop-all" || f == "stop-app" || f == "reload-old" {
				order = append(order, f)
			}
		}
		if strings.Join(order, " ") != "stop-all reload-old" {
			t.Errorf("order: %v", order)
		}
		if rec := e.record(); rec.Result != ResultRolledBack || rec.RestoredHealth != "ok" {
			t.Errorf("record: %+v", rec)
		}
		if _, ok := e.inhibited(); ok {
			t.Errorf("still inhibited")
		}
	})
	t.Run("removed by hand during a transaction", func(t *testing.T) {
		e := activeOwn(t)
		e.crashAt(CrashActivating, false, rel3)
		os.Remove(e.inhibitPath())
		there := false
		e.runner.hook = func(argv []string) {
			if argv[0] == "reload-units" {
				_, there = e.inhibited()
			}
		}
		if code := e.recover(resumeOnly); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if !there {
			t.Errorf("a resume ran without the inhibit")
		}
		if _, ok := e.inhibited(); ok {
			t.Errorf("still inhibited after the resume completed")
		}
	})
}

// TestInhibitClear: ah-switch inhibit-clear --asked WORDS removes the
// inhibit on the owner's word and records the time and the words in
// deploy.json (kept by later switches); it needs the words, is refused
// while a transaction is open or when there is nothing to clear, runs no
// program, and a dry run changes nothing. recover --services-stopped
// leaves the inhibit.
func TestInhibitClear(t *testing.T) {
	clear := func(e *env, words string, mod ...func(*Config)) int {
		e.t.Helper()
		code := e.switcher(mod...).InhibitClear(context.Background(), words)
		e.last = e.out.String()
		e.out.Reset()
		return code
	}
	t.Run("cleared", func(t *testing.T) {
		e := newEnv(t)
		e.release(rel1, "s")
		e.runner.fail["health-probe"] = -1
		if code := e.switchTo(rel1); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		e.runner.reset()
		if code := clear(e, " "); code != ExitUsage {
			t.Errorf("without words: exit %d", code)
		}
		before := digest(t, e.root)
		if code := clear(e, "seat checked by hand", func(c *Config) { c.Dry = true }); code != ExitOK || digest(t, e.root) != before {
			t.Errorf("dry: exit %d\n%s", code, e.last)
		}
		was := e.record()
		e.clock.sleep(3600e9)
		if code := clear(e, "seat checked by hand"); code != ExitOK {
			t.Fatalf("exit %d\n%s", code, e.last)
		}
		if _, ok := e.inhibited(); ok {
			t.Errorf("still inhibited")
		}
		// The record of the switch stays as it was, its time included;
		// the clearing has its own time.
		rec := e.record()
		cleared := e.clock.now().UTC().Format("2006-01-02T15:04:05Z07:00")
		if rec.InhibitClearedAt != cleared || rec.InhibitClearedAsked != "seat checked by hand" || rec.ID != rel1 || rec.Time != was.Time || rec.Txn != was.Txn {
			t.Errorf("record: %+v", rec)
		}
		if got := e.status(); !strings.Contains(got, "inhibit last cleared by the owner at "+cleared+", asked: \"seat checked by hand\"") {
			t.Errorf("status:\n%s", got)
		}
		if len(e.runner.calls) != 0 {
			t.Errorf("commands run: %v", e.runner.calls)
		}
		if code := clear(e, "again"); code != ExitFailed || !strings.Contains(e.last, "nothing to clear") {
			t.Errorf("again: exit %d\n%s", code, e.last)
		}
		// Kept by the next switch's record.
		delete(e.runner.fail, "health-probe")
		e.release(rel2, "s")
		if code := e.switchTo(rel2); code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		if rec := e.record(); rec.InhibitClearedAsked != "seat checked by hand" {
			t.Errorf("record: %+v", rec)
		}
	})
	t.Run("a transaction open", func(t *testing.T) {
		e := newEnv(t)
		e.release(rel1, "s")
		e.runner.fail["health-probe"] = -1
		e.runner.fail["stop-app"] = -1
		if code := e.switchTo(rel1); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if code := clear(e, "looked"); code != ExitFailed || !strings.Contains(e.last, "still open in phase stopping") {
			t.Errorf("exit %d\n%s", code, e.last)
		}
		if _, ok := e.inhibited(); !ok {
			t.Errorf("the inhibit was removed under an open transaction")
		}
		// The owner's word that the services are stopped settles the
		// transaction, and leaves decisions inhibited.
		if code := e.recover(RecoverOptions{ServicesStopped: true}); code != ExitOK {
			t.Fatalf("recover: exit %d", code)
		}
		if _, ok := e.inhibited(); !ok {
			t.Errorf("recover --services-stopped removed the inhibit")
		}
	})
	t.Run("nothing to clear", func(t *testing.T) {
		e := active(t)
		if code := clear(e, "looked"); code != ExitFailed {
			t.Errorf("exit %d", code)
		}
	})
}

// guardedApp models a decision-capable service under the inhibit
// contract: its unit carries no start condition, so it starts (on a
// reload, at boot) whether decisions.inhibit is there or not, and its
// readiness probe passes only while it runs; it refuses every decision
// while the file is there, and when the file cannot be read.
type guardedApp struct {
	e       *env
	started bool
	// What a decision asked while the readiness probe ran got, in order.
	asked []string
}

func (g *guardedApp) decide() string {
	_, err := os.Stat(g.e.inhibitPath())
	if errors.Is(err, fs.ErrNotExist) {
		return "accepted"
	}
	return "refused"
}

func (g *guardedApp) gate(argv []string) error {
	switch filepath.Base(argv[0]) {
	case "reload-units", "reload-old":
		g.started = true
	case "stop-app", "stop-old", "stop-all":
		g.started = false
	case "health-probe", "health-old":
		if !g.started {
			return errors.New("not ready: the service is not running")
		}
		g.asked = append(g.asked, g.decide())
	}
	return nil
}

func guard(e *env) *guardedApp {
	g := &guardedApp{e: e}
	e.runner.gate = g.gate
	return g
}

// TestGuardedServiceQualifies: a service that obeys the inhibit contract
// (it may start while decisions.inhibit is there, and refuses every
// decision while it is) is started by the reload list, passes its
// readiness probe while decisions are still refused, and takes decisions
// only once the transaction removed the inhibit: on a first switch, on a
// later one, and when the switch goes back to the earlier release.
func TestGuardedServiceQualifies(t *testing.T) {
	t.Run("first and later switch", func(t *testing.T) {
		e := newEnv(t)
		g := guard(e)
		e.release(rel1, "s")
		if code := e.switchTo(rel1); code != ExitOK {
			t.Fatalf("first switch: exit %d", code)
		}
		e.release(rel2, "s")
		if code := e.switchTo(rel2); code != ExitOK {
			t.Fatalf("second switch: exit %d", code)
		}
		if strings.Join(g.asked, " ") != "refused refused" {
			t.Errorf("decisions while the probes ran: %v", g.asked)
		}
		if got := g.decide(); got != "accepted" || !g.started {
			t.Errorf("after the switch: %s, running %v", got, g.started)
		}
	})
	t.Run("going back", func(t *testing.T) {
		e := activeOwn(t)
		g := guard(e)
		g.started = true
		e.runner.failPost = e.relDir(rel3)
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if rec := e.record(); rec.Result != ResultRolledBack || rec.RestoredHealth != "ok" {
			t.Fatalf("record: %+v", rec)
		}
		if strings.Join(g.asked, " ") != "refused refused" {
			t.Errorf("decisions while the probes ran: %v", g.asked)
		}
		if got := g.decide(); got != "accepted" || !g.started {
			t.Errorf("after going back: %s, running %v", got, g.started)
		}
	})
}

// TestRebootInDoneRequalifies: a transaction that reached done (its
// release healthy and checked) and was interrupted before it was settled,
// as by a reboot that stopped its service, is settled by the next command
// only after the reload list and health check of the release current
// points at pass again; decisions are refused until then. When they do not
// pass, the outcome is recorded and decisions stay inhibited. status says
// what the next command does.
func TestRebootInDoneRequalifies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reload bool // the reload after the reboot passes
	}{{"healthy again", true}, {"not healthy after the reboot", false}} {
		t.Run(tc.name, func(t *testing.T) {
			e := activeOwn(t)
			g := guard(e)
			g.started = true
			e.crashAt(CrashDone, false, rel3)
			// The reboot: the service is not running, and the inhibit
			// is still there.
			g.started, g.asked = false, nil
			if _, ok := e.inhibited(); !ok {
				t.Fatalf("not inhibited after the crash in done")
			}
			if got := e.status(); !strings.Contains(got, "interrupted: switch to "+rel3+" in phase done") || !strings.Contains(got, "runs the reload list and the health check of "+rel3+" again before decisions are no longer inhibited") {
				t.Errorf("status:\n%s", got)
			}
			if !tc.reload {
				e.runner.fail["reload-units"] = 1
			}
			e.runner.reset()
			// resumeOnly is refused once the journal is settled, so its
			// exit code is the failure of that refusal when the
			// requalification passed, and the failure of the
			// requalification itself when it did not.
			if code := e.recover(resumeOnly); code != ExitFailed {
				t.Errorf("recover: exit %d, want %d\n%s", code, ExitFailed, e.last)
			}
			if !tc.reload && !strings.Contains(e.last, "decisions stay inhibited") {
				t.Errorf("recover did not report the failed requalification:\n%s", e.last)
			}
			var ran []string
			for _, c := range e.runner.calls {
				ran = append(ran, strings.Fields(c)[0])
			}
			rec := e.record()
			if rec.Result != ResultOK || rec.ID != rel3 || rec.CurrentID != rel3 {
				t.Errorf("record: %+v", rec)
			}
			if tc.reload {
				if strings.Join(ran, " ") != "reload-units health-probe" {
					t.Errorf("ran %v, want the reload list and health check of %s", ran, rel3)
				}
				if strings.Join(g.asked, " ") != "refused" {
					t.Errorf("decisions while the probe ran: %v", g.asked)
				}
				if got := g.decide(); got != "accepted" || !g.started {
					t.Errorf("after the resume: %s, running %v", got, g.started)
				}
				return
			}
			// The reload may have started a service: the host's stop list
			// runs, and the health check does not.
			if strings.Join(ran, " ") != "reload-units stop-all" {
				t.Errorf("ran %v", ran)
			}
			in, ok := e.inhibited()
			if !ok || !strings.Contains(in.Reason, "did not pass its reload and health check again") {
				t.Errorf("inhibit: %+v, %v", in, ok)
			}
			if !strings.Contains(rec.Detail, "did not pass its reload and health check again") {
				t.Errorf("record detail: %q", rec.Detail)
			}
			if _, err := os.Stat(filepath.Join(e.root, StateDir, JournalFile)); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the journal stays: %v", err)
			}
		})
	}
}

// TestRetryAfterFailedRequalification: the request that was interrupted in
// phase done is asked again after a reboot, and the release it ended on
// does not pass its health check again. The command fails (it never
// reports the historical ok), decisions stay inhibited, the outcome stays
// owed with the failure kept apart from it, the host's stop list runs
// because the reload may have started services, and a later retry of the
// same request does not turn the failure into a success.
func TestRetryAfterFailedRequalification(t *testing.T) {
	e := activeOwn(t)
	e.keepOwed = true
	e.crashAt(CrashDone, false, rel3)
	e.runner.fail["health-probe"] = -1
	e.runner.reset()
	if code := e.switchTo(rel3); code != ExitFailed {
		t.Fatalf("the retry: exit %d, want %d\n%s", code, ExitFailed, e.last)
	}
	if e.runner.called("stop-all") != 1 {
		t.Errorf("the host's stop list ran %d times, want 1; commands: %v", e.runner.called("stop-all"), e.runner.calls)
	}
	if !strings.Contains(e.last, "decisions stay inhibited") {
		t.Errorf("output:\n%s", e.last)
	}
	check := func(when string) {
		t.Helper()
		owed := e.owed()
		if len(owed) != 1 || owed[0].Result != ResultOK || !owed[0].RequalifyFailed {
			t.Errorf("%s: owed: %+v", when, owed)
		}
		if _, ok := e.inhibited(); !ok {
			t.Errorf("%s: not inhibited", when)
		}
		if _, ok := e.journal(); ok {
			t.Errorf("%s: the journal stays", when)
		}
	}
	check("after the retry")
	if got := e.status(); !strings.Contains(got, "did not pass its reload and health check again") {
		t.Errorf("status:\n%s", got)
	}
	// The same request again, even with a health check that passes now:
	// still a failure, still owed.
	delete(e.runner.fail, "health-probe")
	for i := 0; i < 2; i++ {
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("retry %d: exit %d\n%s", i+2, code, e.last)
		}
		if strings.Contains(e.last, "result: ok;") && !strings.Contains(e.last, "decisions stay inhibited") {
			t.Errorf("retry %d reported success:\n%s", i+2, e.last)
		}
		check("later retry")
	}
	// The owner acknowledges it; the inhibit is the owner's to clear.
	if code := e.ack("saw the failed requalification"); code != ExitOK {
		t.Fatalf("ack: exit %d\n%s", code, e.last)
	}
	if _, ok := e.inhibited(); !ok {
		t.Errorf("acknowledging cleared the inhibit")
	}
}
