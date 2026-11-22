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
