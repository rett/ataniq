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

	"ataniq/internal/authorityhost/decisionguard"
)

// boot runs ah-switch boot and returns its exit code; its output is in
// last. Nothing owed is acknowledged after it: boot never acknowledges
// anything, and the tests look at what it leaves owed.
func (e *env) boot(mod ...func(*Config)) int {
	e.t.Helper()
	code := e.switcher(mod...).Boot(context.Background())
	e.t.Logf("boot: exit %d\n%s", code, e.out.String())
	e.last = e.out.String()
	e.out.Reset()
	return code
}

// bootCrashAt runs ah-switch boot, which dies the first time it reaches
// point.
func (e *env) bootCrashAt(point string) {
	e.t.Helper()
	s := e.switcher()
	reached := false
	s.crashAt = func(p string) error {
		if p == point && !reached {
			reached = true
			return errCrash
		}
		return nil
	}
	code := s.Boot(context.Background())
	e.t.Logf("boot, crash at %s: exit %d\n%s", point, code, e.out.String())
	e.last = e.out.String()
	e.out.Reset()
	if !reached {
		e.t.Fatalf("boot never reached %s", point)
	}
	if code != ExitFailed {
		e.t.Fatalf("crash at %s: exit %d", point, code)
	}
	if strings.Contains(e.last, "result:") {
		e.t.Fatalf("a crash printed a result:\n%s", e.last)
	}
}

// stateFile is the content of the state file name, "" when it is missing.
func (e *env) stateFile(name string) string {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.root, StateDir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return string(data)
}

// writeInhibitFile writes decisions.inhibit holding in, as the switch
// writes it.
func (e *env) writeInhibitFile(in inhibit) {
	e.t.Helper()
	data, err := json.Marshal(in)
	if err != nil {
		e.t.Fatal(err)
	}
	e.write(e.inhibitPath(), string(data), 0o644)
}

// guardCheck is what the decision guard of a decision-capable service
// answers now: "" allows, otherwise the code of its refusal.
func (e *env) guardCheck() string {
	err := decisionguard.Check(filepath.Join(e.root, StateDir))
	if err == nil {
		return ""
	}
	var ge *decisionguard.Error
	if !errors.As(err, &ge) {
		e.t.Fatalf("the guard returned %v", err)
	}
	return ge.Code
}

// guarded counts the commands that run while decisions.inhibit is not
// there (the services would then take decisions), and asks the guard at
// each of them.
func (e *env) guarded() *int {
	unguarded := new(int)
	e.runner.hook = func([]string) {
		if _, ok := e.inhibited(); !ok || e.guardCheck() == "" {
			*unguarded++
		}
	}
	return unguarded
}

// names is the program of every command run since the last reset, by its
// first word.
func (e *env) names() string {
	var ran []string
	for _, c := range e.runner.calls {
		ran = append(ran, strings.Fields(c)[0])
	}
	return strings.Join(ran, " ")
}

// rebooted is a host on which rel2 (with its own lists) was switched to
// healthy, then rebooted: its service does not run, and nothing is owed or
// inhibited.
func rebooted(t *testing.T) (*env, *guardedApp) {
	t.Helper()
	e := activeOwn(t)
	e.keepOwed = true
	if _, ok := e.inhibited(); ok {
		t.Fatal("inhibited after a healthy switch")
	}
	if len(e.owed()) != 0 {
		t.Fatal("an outcome is owed after the setup")
	}
	g := guard(e)
	return e, g
}

// TestBootRequalifies: after a reboot, ah-switch boot writes
// decisions.inhibit (its own record, reason boot) before it runs
// anything, runs the reload list, the health check and the
// post-activation no-AI check of the release current points at, as a
// settling transaction does, and removes the inhibit only once all of it
// passed. A service that follows the inhibit contract is started by the
// reload, passes its readiness probe while decisions are refused, and
// takes decisions only afterwards. Nothing is recorded in deploy.json,
// and nothing becomes owed.
func TestBootRequalifies(t *testing.T) {
	e, g := rebooted(t)
	deploy := e.stateFile(DeployFile)
	var seen []inhibit
	unguarded := e.guarded()
	hook := e.runner.hook
	e.runner.hook = func(argv []string) {
		hook(argv)
		if in, ok := e.inhibited(); ok {
			seen = append(seen, in)
		}
	}
	if code := e.boot(); code != ExitOK {
		t.Fatalf("boot: exit %d\n%s", code, e.last)
	}
	checker := filepath.Join(e.relDir(rel2), NoAICheckProgram)
	if got := e.names(); got != "reload-old health-old nft "+checker {
		t.Errorf("ran %q, want the reload list, health check and post-activation no-AI check of %s", got, rel2)
	}
	if e.runner.calls[3] != postCall(checker, e.root, e.relDir(rel2)) {
		t.Errorf("the no-AI check ran as %q", e.runner.calls[3])
	}
	if *unguarded != 0 {
		t.Errorf("%d command(s) ran while decisions were not inhibited", *unguarded)
	}
	if len(seen) == 0 || seen[0].Reason != bootReason || !txnPattern.MatchString(seen[0].Txn) {
		t.Errorf("the inhibit while boot ran: %+v", seen)
	}
	if strings.Join(g.asked, " ") != "refused" {
		t.Errorf("decisions while the probe ran: %v", g.asked)
	}
	if _, ok := e.inhibited(); ok {
		t.Error("decisions are still inhibited after a boot that qualified current")
	}
	if got := g.decide(); got != "accepted" || !g.started {
		t.Errorf("after boot: %s, running %v", got, g.started)
	}
	if got := e.guardCheck(); got != "" {
		t.Errorf("the guard refuses after boot: %s", got)
	}
	if e.stateFile(DeployFile) != deploy {
		t.Error("boot changed deploy.json")
	}
	if len(e.owed()) != 0 {
		t.Errorf("owed after boot: %+v", e.owed())
	}
	if e.runner.called("stop-all") != 0 {
		t.Error("the host's stop list ran")
	}
	if !strings.Contains(e.last, "result: ok; current -> "+rel2) {
		t.Errorf("output:\n%s", e.last)
	}
	// A second boot does it all again: it is the qualification of this
	// boot, never one remembered from an earlier run.
	e.runner.reset()
	if code := e.boot(); code != ExitOK || e.names() != "reload-old health-old nft "+checker {
		t.Errorf("a second boot: exit %d, ran %q", code, e.names())
	}
}

// TestBootFailureKeepsTheInhibit: when current does not pass its reload,
// its health check or its post-activation no-AI check, or does not verify
// again, or the switch policy cannot be used to verify it, boot runs the
// host's stop list (a service may run from the boot or the reload), keeps
// decisions.inhibit (rewritten with the failure), adds the failure to
// owed.json as requalify_failed, and exits 1. status shows the outcome
// owed with the ack that takes it back; any switch or rollback is refused
// while it is owed; a later boot keeps the inhibit as it finds it and runs
// nothing; ack takes back the outcome and leaves the inhibit.
func TestBootFailureKeepsTheInhibit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		fail  func(e *env)
		ran   string // the commands boot runs, by their first word
		check string // a word of the recorded failure
	}{
		{"reload", func(e *env) { e.runner.fail["reload-old"] = -1 }, "reload-old stop-all", CodeReloadFailed},
		{"health", func(e *env) { e.runner.fail["health-old"] = -1 }, "", CodeHealthFailed},
		{"no-AI check", func(e *env) { e.runner.failPost = e.relDir(rel2) }, "reload-old health-old nft CHECK stop-all", CodeNoAICheckFailed},
		{"release changed", func(e *env) {
			e.write(filepath.Join(e.relDir(rel2), "bin/app"), "changed\n", 0o755)
		}, "stop-all", CodeSumsMismatch},
		{"policy unusable", func(e *env) {
			e.write(filepath.Join(e.root, PolicyDir, PolicyName), "{", 0o644)
		}, "stop-all", CodePolicyInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, g := rebooted(t)
			deploy := e.stateFile(DeployFile)
			tc.fail(e)
			unguarded := e.guarded()
			if code := e.boot(); code != ExitFailed {
				t.Fatalf("boot: exit %d\n%s", code, e.last)
			}
			if *unguarded != 0 {
				t.Errorf("%d command(s) ran while decisions were not inhibited", *unguarded)
			}
			checker := filepath.Join(e.relDir(rel2), NoAICheckProgram)
			if want := strings.ReplaceAll(tc.ran, "CHECK", checker); want != "" && e.names() != want {
				t.Errorf("ran %q, want %q", e.names(), want)
			}
			if n := e.runner.called("stop-all"); n != 1 {
				t.Errorf("the host's stop list ran %d times, want 1", n)
			}
			in, ok := e.inhibited()
			if !ok || !strings.Contains(in.Reason, "did not pass its reload, health check and no-AI check again") {
				t.Errorf("inhibit: %+v, %v", in, ok)
			}
			owed := e.owed()
			if len(owed) != 1 {
				t.Fatalf("owed: %+v", owed)
			}
			o := owed[0]
			if o.Direction != DirBoot || !o.RequalifyFailed || o.Result != ResultFailed || o.ID != rel2 || o.CurrentID != rel2 || o.Txn != in.Txn || o.RequestID != requestID(DirBoot, rel2) {
				t.Errorf("owed: %+v (inhibit %+v)", o, in)
			}
			if !strings.Contains(o.Detail, "after the boot") || !strings.Contains(o.Detail, tc.check) || !strings.Contains(o.Detail, "the host's stop list ran") {
				t.Errorf("owed detail: %q", o.Detail)
			}
			if e.stateFile(DeployFile) != deploy {
				t.Error("boot changed deploy.json")
			}
			if _, ok := e.journal(); ok {
				t.Error("boot left a journal")
			}
			if g.decide() != "refused" || e.guardCheck() != decisionguard.CodeInhibited {
				t.Errorf("a decision after the failed boot: %s, guard %q", g.decide(), e.guardCheck())
			}
			ack := "ah-switch ack --txn " + o.Txn + " --asked WORDS"
			if !strings.Contains(e.last, "decisions stay inhibited") || !strings.Contains(e.last, ack) {
				t.Errorf("output:\n%s", e.last)
			}
			if tc.name != "policy unusable" {
				if got := e.status(); !strings.Contains(got, "outcome owed (boot requalification of "+rel2+", transaction "+o.Txn+")") || !strings.Contains(got, ack) || !strings.Contains(got, "after the boot") {
					t.Errorf("status:\n%s", got)
				}
			}
			// A later boot finds the inhibit and keeps it as it is,
			// running nothing and acknowledging nothing.
			inhibitText, owedText := e.stateFile(InhibitFile), e.stateFile(OwedFile)
			e.runner.reset()
			delete(e.runner.fail, "reload-old")
			delete(e.runner.fail, "health-old")
			e.runner.failPost = ""
			if code := e.boot(); code != ExitFailed {
				t.Errorf("a later boot: exit %d\n%s", code, e.last)
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("a later boot ran %v", e.runner.calls)
			}
			if e.stateFile(InhibitFile) != inhibitText || e.stateFile(OwedFile) != owedText {
				t.Error("a later boot changed the inhibit or the outcome owed")
			}
			if !strings.Contains(e.last, "outcome owed (boot requalification of "+rel2) {
				t.Errorf("a later boot did not print what is owed:\n%s", e.last)
			}
			if tc.name == "policy unusable" {
				return
			}
			// A switch is refused while the outcome is owed.
			if code := e.switchTo(rel3); code != ExitFailed || !strings.Contains(e.last, "refused: an outcome is owed") {
				t.Errorf("a switch while the boot's outcome is owed: exit %d\n%s", code, e.last)
			}
			if code := e.ack("saw the failed boot"); code != ExitOK {
				t.Fatalf("ack: exit %d\n%s", code, e.last)
			}
			if !strings.Contains(e.last, "ack: the boot requalification of "+rel2) {
				t.Errorf("ack output:\n%s", e.last)
			}
			if len(e.owed()) != 0 || e.stateFile(InhibitFile) != inhibitText {
				t.Errorf("after the ack: owed %+v, inhibit changed %v", e.owed(), e.stateFile(InhibitFile) != inhibitText)
			}
		})
	}
}

// TestBootLeavesAFoundInhibit: an inhibit boot did not write itself in this
// run stays exactly as boot finds it, whoever wrote it and whatever it
// says (one it cannot read strictly included), and boot then runs nothing
// and acknowledges nothing: it prints what is owed and exits 1. A journal
// still open (in txn.json, or held by the inhibit after an interrupted
// healthy end) is settled through the normal reconcile path, whose
// transaction alone may lift the inhibit, by its own rules.
func TestBootLeavesAFoundInhibit(t *testing.T) {
	kept := func(t *testing.T, e *env, owedWant int) {
		t.Helper()
		before, owedBefore := e.stateFile(InhibitFile), e.stateFile(OwedFile)
		info, err := os.Lstat(e.inhibitPath())
		if err != nil {
			t.Fatal(err)
		}
		e.runner.reset()
		if code := e.boot(); code != ExitFailed {
			t.Errorf("boot: exit %d\n%s", code, e.last)
		}
		if len(e.runner.calls) != 0 {
			t.Errorf("boot ran %v", e.runner.calls)
		}
		after, err := os.Lstat(e.inhibitPath())
		if err != nil || e.stateFile(InhibitFile) != before || after.Mode() != info.Mode() || !os.SameFile(info, after) {
			t.Errorf("boot changed the inhibit it found (%v)", err)
		}
		if e.stateFile(OwedFile) != owedBefore || len(e.owed()) != owedWant {
			t.Errorf("boot changed what is owed: %+v", e.owed())
		}
		if !strings.Contains(e.last, "stays as found") || !strings.Contains(e.last, "result: decisions stay inhibited") {
			t.Errorf("output:\n%s", e.last)
		}
		if owedWant > 0 && !strings.Contains(e.last, "outcome owed") {
			t.Errorf("boot did not print what is owed:\n%s", e.last)
		}
	}
	t.Run("a failed transaction's", func(t *testing.T) {
		e := activeOwn(t)
		e.runner.fail["health-probe"] = -1
		e.runner.fail["health-old"] = -1
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		if rec := e.record(); rec.Result != ResultFailedRecovery {
			t.Fatalf("record: %+v", rec)
		}
		delete(e.runner.fail, "health-probe")
		delete(e.runner.fail, "health-old")
		kept(t, e, 0)
	})
	t.Run("a failed requalification's, still owed", func(t *testing.T) {
		e := activeOwn(t)
		e.keepOwed = true
		e.crashAt(CrashDone, false, rel3)
		e.runner.fail["health-probe"] = -1
		if code := e.switchTo(rel3); code != ExitFailed {
			t.Fatalf("exit %d", code)
		}
		delete(e.runner.fail, "health-probe")
		kept(t, e, 1)
	})
	t.Run("one that does not parse", func(t *testing.T) {
		e := activeOwn(t)
		e.write(e.inhibitPath(), "the owner's note\n", 0o644)
		kept(t, e, 0)
	})
	t.Run("a boot record that is not trusted", func(t *testing.T) {
		e := activeOwn(t)
		e.write(e.inhibitPath(), `{"txn": "`+neverTxn+`", "reason": "`+bootReason+`", "time": "2026-11-21T12:00:00Z"}`, 0o644)
		if err := os.Chmod(e.inhibitPath(), 0o666); err != nil {
			t.Fatal(err)
		}
		kept(t, e, 0)
	})
	t.Run("an empty folder", func(t *testing.T) {
		e := activeOwn(t)
		if err := os.Mkdir(e.inhibitPath(), 0o755); err != nil {
			t.Fatal(err)
		}
		e.runner.reset()
		if code := e.boot(); code != ExitFailed || len(e.runner.calls) != 0 {
			t.Errorf("boot: exit %d, ran %v", code, e.runner.calls)
		}
		if info, err := os.Lstat(e.inhibitPath()); err != nil || !info.IsDir() {
			t.Errorf("boot changed what it found: %v", err)
		}
	})
	t.Run("an open journal", func(t *testing.T) {
		e := activeOwn(t)
		e.keepOwed = true
		g := guard(e)
		e.crashAt(CrashActivating, false, rel3)
		g.started = false
		unguarded := e.guarded()
		if code := e.boot(); code != ExitOK {
			t.Fatalf("boot: exit %d\n%s", code, e.last)
		}
		if *unguarded != 0 {
			t.Errorf("%d command(s) ran while decisions were not inhibited", *unguarded)
		}
		e.noJournal()
		if _, ok := e.inhibited(); ok || g.decide() != "accepted" {
			t.Errorf("after the transaction ended healthy: inhibited %v, a decision %s", ok, g.decide())
		}
		e.owes(e.owed()[0].Txn, requestID(DirForward, rel3), ResultOK)
		if !strings.Contains(e.last, "outcome owed") {
			t.Errorf("boot did not print what is owed:\n%s", e.last)
		}
	})
	t.Run("an open journal whose inhibit was removed by hand", func(t *testing.T) {
		e := activeOwn(t)
		e.keepOwed = true
		e.crashAt(CrashActivating, false, rel3)
		if err := os.Remove(e.inhibitPath()); err != nil {
			t.Fatal(err)
		}
		unguarded := e.guarded()
		if code := e.boot(); code != ExitOK {
			t.Fatalf("boot: exit %d\n%s", code, e.last)
		}
		if *unguarded != 0 {
			t.Errorf("%d command(s) ran while decisions were not inhibited", *unguarded)
		}
		e.noJournal()
	})
	for _, healthy := range []bool{true, false} {
		name := map[bool]string{true: "a held journal, qualified again", false: "a held journal that does not qualify again"}[healthy]
		t.Run(name, func(t *testing.T) {
			e := activeOwn(t)
			e.keepOwed = true
			g := guard(e)
			e.crashAt(CrashJournalMoved, false, rel3)
			g.started = false
			if !healthy {
				e.runner.fail["health-probe"] = -1
			}
			e.runner.reset()
			unguarded := e.guarded()
			code := e.boot()
			if *unguarded != 0 {
				t.Errorf("%d command(s) ran while decisions were not inhibited", *unguarded)
			}
			e.noJournal()
			in, inhibited := e.inhibited()
			owed := e.owed()
			if len(owed) != 1 || owed[0].Direction != DirForward || owed[0].RequalifyFailed == healthy {
				t.Errorf("owed: %+v", owed)
			}
			if healthy {
				if code != ExitOK || inhibited || g.decide() != "accepted" {
					t.Errorf("exit %d, inhibited %v, a decision %s", code, inhibited, g.decide())
				}
				if !strings.HasPrefix(e.names(), "reload-units health-probe nft") {
					t.Errorf("ran %q", e.names())
				}
				return
			}
			if code != ExitFailed || !inhibited || !strings.Contains(in.Reason, "did not pass") || g.decide() != "refused" {
				t.Errorf("exit %d, inhibit %+v %v, a decision %s", code, in, inhibited, g.decide())
			}
		})
	}
	t.Run("an outcome owed, no inhibit", func(t *testing.T) {
		// Nothing guards the outcome: boot writes its own inhibit,
		// qualifies current and lifts it, and the outcome stays owed.
		e := activeOwn(t)
		e.keepOwed = true
		if code := e.switchTo(rel3); code != ExitOK {
			t.Fatalf("exit %d", code)
		}
		owedText := e.stateFile(OwedFile)
		e.runner.reset()
		unguarded := e.guarded()
		if code := e.boot(); code != ExitOK {
			t.Fatalf("boot: exit %d\n%s", code, e.last)
		}
		if *unguarded != 0 || !strings.HasPrefix(e.names(), "reload-units health-probe nft") {
			t.Errorf("unguarded %d, ran %q", *unguarded, e.names())
		}
		if e.stateFile(OwedFile) != owedText {
			t.Error("boot changed what is owed")
		}
		if !strings.Contains(e.last, "outcome owed (request forward:"+rel3) {
			t.Errorf("boot did not print what is owed:\n%s", e.last)
		}
	})
}

// TestBootLeavesNoRecordToTakeOverBesideAJournal: while a journal is
// pending boot never writes its own record, which a later boot would take
// over and lift: the inhibit of a readable journal is the transaction's
// (written back first when it was removed by hand), and a journal that
// cannot be read gets a plain record. A journal then set aside by recover
// --discard-journal, or removed by the owner, leaves decisions inhibited
// at the next boot, which runs nothing and lifts nothing, as discard says:
// until a transaction ends healthy or inhibit-clear. A discard also
// replaces a boot record it finds by its own.
func TestBootLeavesNoRecordToTakeOverBesideAJournal(t *testing.T) {
	// stale is a journal left in its activating phase whose links no
	// longer match the host, with its inhibit removed by hand.
	stale := func(t *testing.T) (*env, journal) {
		e := activeOwn(t)
		e.keepOwed = true
		e.crashAt(CrashActivating, false, rel3)
		j, _ := e.journal()
		if err := os.Remove(e.inhibitPath()); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(e.root, BaseDir, "previous")); err != nil {
			t.Fatal(err)
		}
		e.runner.reset()
		return e, j
	}
	// kept checks that a later boot keeps the inhibit as it is and runs
	// nothing.
	kept := func(t *testing.T, e *env, g *guardedApp) {
		t.Helper()
		before := e.stateFile(InhibitFile)
		e.runner.reset()
		if code := e.boot(); code != ExitFailed {
			t.Errorf("the next boot: exit %d\n%s", code, e.last)
		}
		if _, ok := e.inhibited(); !ok || e.stateFile(InhibitFile) != before || g.decide() != "refused" {
			t.Errorf("the next boot lifted or changed the inhibit: a decision %s", g.decide())
		}
		if len(e.runner.calls) != 0 {
			t.Errorf("the next boot ran %v", e.runner.calls)
		}
	}
	t.Run("a stale journal, then discarded", func(t *testing.T) {
		e, j := stale(t)
		g := guard(e)
		if code := e.boot(); code != ExitFailed {
			t.Fatalf("boot: exit %d\n%s", code, e.last)
		}
		in, ok := e.inhibited()
		if !ok || in.Txn != j.Txn || in.Reason == bootReason {
			t.Errorf("boot beside the stale journal left %+v, %v", in, ok)
		}
		if code := e.recover(RecoverOptions{DiscardJournal: j.Txn}); code != ExitOK {
			t.Fatalf("discard: exit %d\n%s", code, e.last)
		}
		if in, ok := e.inhibited(); !ok || in.Txn != j.Txn || !strings.Contains(in.Reason, "set aside by the owner") {
			t.Errorf("after the discard: %+v, %v", in, ok)
		}
		kept(t, e, g)
	})
	t.Run("a boot record beside a stale journal, then discarded", func(t *testing.T) {
		e, j := stale(t)
		g := guard(e)
		e.write(e.inhibitPath(), `{"txn": "`+neverTxn+`", "reason": "`+bootReason+`", "time": "2026-11-21T12:00:00Z"}`, 0o644)
		if code := e.recover(RecoverOptions{DiscardJournal: j.Txn}); code != ExitOK {
			t.Fatalf("discard: exit %d\n%s", code, e.last)
		}
		if in, ok := e.inhibited(); !ok || in.Txn != j.Txn || in.Reason == bootReason || !strings.Contains(in.Reason, "set aside by the owner") {
			t.Errorf("the discard kept the boot record: %+v, %v", in, ok)
		}
		kept(t, e, g)
	})
	t.Run("a journal that cannot be read, then removed by the owner", func(t *testing.T) {
		e := activeOwn(t)
		g := guard(e)
		e.write(filepath.Join(e.root, StateDir, JournalFile), "{", 0o644)
		e.runner.reset()
		if code := e.boot(); code != ExitFailed {
			t.Fatalf("boot: exit %d\n%s", code, e.last)
		}
		in, ok := e.inhibited()
		if !ok || in.Reason != unreadableJournalReason || !txnPattern.MatchString(in.Txn) {
			t.Errorf("boot beside the unreadable journal left %+v, %v", in, ok)
		}
		if len(e.runner.calls) != 0 {
			t.Errorf("boot ran %v", e.runner.calls)
		}
		if err := os.Remove(filepath.Join(e.root, StateDir, JournalFile)); err != nil {
			t.Fatal(err)
		}
		if got := e.status(); !strings.Contains(got, "a later boot keeps it") {
			t.Errorf("status:\n%s", got)
		}
		kept(t, e, g)
	})
}

// TestNoBootRecordStaysBesideAJournalItCannotRead: the whole record of a
// boot that did not finish is there beside a txn.json that cannot be read
// (an earlier boot, then a journal that went bad). The first command that
// takes the lock (a boot, or any other) rewrites it as the plain record of
// a journal that cannot be read, keeping its run id, so that a boot after
// the owner set the journal aside by hand keeps decisions inhibited
// instead of taking the record over and lifting it.
func TestNoBootRecordStaysBesideAJournalItCannotRead(t *testing.T) {
	for _, tc := range lockedCommands() {
		t.Run(tc.name, func(t *testing.T) {
			e := activeOwn(t)
			e.keepOwed = true
			g := guard(e)
			e.write(filepath.Join(e.root, StateDir, JournalFile), "{", 0o600)
			e.writeInhibitFile(inhibit{Txn: neverTxn, Reason: bootReason, Time: "2026-11-21T12:00:00Z"})
			e.runner.reset()
			if code := tc.run(e, journal{}); code == ExitOK {
				t.Fatalf("exit 0 beside a journal that cannot be read\n%s", e.last)
			}
			in, ok := e.inhibited()
			if !ok || in.Reason != unreadableJournalReason || in.Txn != neverTxn {
				t.Fatalf("the boot record beside the journal is now %+v, %v\n%s", in, ok, e.last)
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("commands run: %v", e.runner.calls)
			}
			if err := os.Remove(filepath.Join(e.root, StateDir, JournalFile)); err != nil {
				t.Fatal(err)
			}
			before := e.stateFile(InhibitFile)
			e.runner.reset()
			if code := e.boot(); code != ExitFailed || e.stateFile(InhibitFile) != before || len(e.runner.calls) != 0 {
				t.Errorf("the next boot: exit %d, ran %v, inhibit changed %v\n%s", code, e.runner.calls, e.stateFile(InhibitFile) != before, e.last)
			}
			if got := g.decide(); got != "refused" {
				t.Errorf("a decision is %s after the next boot", got)
			}
		})
	}
}

// TestBootWithholdsTheAckWhileSettling: an outcome owed while its
// transaction is still open (in txn.json, or held by the inhibit) is not
// final: settling it may change it (its requalification may fail). boot
// prints it as status does then, without the ack command, so that none is
// copied for an outcome that may change under the same transaction.
func TestBootWithholdsTheAckWhileSettling(t *testing.T) {
	for _, point := range []string{CrashRecord, CrashJournalMoved} {
		t.Run(point, func(t *testing.T) {
			e := activeOwn(t)
			e.keepOwed = true
			e.crashAt(point, false, rel3)
			owed := e.owed()
			if len(owed) != 1 {
				t.Fatalf("owed: %+v", owed)
			}
			// The policy cannot be used: the transaction stays open.
			e.write(filepath.Join(e.root, PolicyDir, PolicyName), "{", 0o644)
			if code := e.boot(); code != ExitFailed {
				t.Fatalf("boot: exit %d\n%s", code, e.last)
			}
			_, open := e.journal()
			held := strings.Contains(e.stateFile(InhibitFile), `"phase"`)
			if !open && !held {
				t.Fatal("the transaction was settled")
			}
			if strings.Contains(e.last, "ack --txn") {
				t.Errorf("boot printed an ack command while the transaction is open:\n%s", e.last)
			}
			if !strings.Contains(e.last, "outcome owed, not final (request "+q(owed[0].RequestID)+", transaction "+owed[0].Txn+")") {
				t.Errorf("boot did not print the outcome owed as not final:\n%s", e.last)
			}
		})
	}
}

// TestBootWithoutARelease: on a host where nothing is deployed (no current
// release, no release folder, no deploy.json, no journal) boot refuses
// with its own exit code, qualifies nothing, runs
// nothing and creates nothing: the inhibit is left as found. The decision
// guard then answers by the state folder alone. It refuses while the state
// folder is missing (a host the bootstrap did not prepare) or holds
// decisions.inhibit, and allows on a prepared state folder without it;
// no decision-capable service runs there, since every one comes with a
// release (its unit, its reload list), and none was ever switched to.
func TestBootWithoutARelease(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(e *env)
		guard string
	}{
		{"no state folder", func(e *env) {}, decisionguard.CodeStateDir},
		{"no opt/authority-host either", func(e *env) {
			if err := os.RemoveAll(filepath.Join(e.root, BaseDir)); err != nil {
				e.t.Fatal(err)
			}
		}, decisionguard.CodeStateDir},
		{"an empty state folder", func(e *env) {
			if err := os.MkdirAll(filepath.Join(e.root, StateDir), 0o755); err != nil {
				e.t.Fatal(err)
			}
		}, ""},
		{"an inhibit and a lock, no current", func(e *env) {
			e.write(filepath.Join(e.root, StateDir, LockFile), "", 0o600)
			e.write(e.inhibitPath(), `{"txn": "`+neverTxn+`", "reason": "`+bootReason+`", "time": "2026-11-21T12:00:00Z"}`, 0o644)
		}, decisionguard.CodeInhibited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(e)
			before := digest(t, e.root)
			if code := e.boot(); code != ExitNoRelease {
				t.Errorf("boot: exit %d, want %d\n%s", code, ExitNoRelease, e.last)
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("boot ran %v", e.runner.calls)
			}
			if digest(t, e.root) != before {
				t.Error("boot changed the root")
			}
			if !strings.Contains(e.last, "no current release") {
				t.Errorf("output:\n%s", e.last)
			}
			if got := e.guardCheck(); got != tc.guard {
				t.Errorf("the guard answers %q, want %q", got, tc.guard)
			}
		})
	}
}

// TestBootOnADeployedHostWithoutCurrent: a host that shows a deployment
// (deploy.json, a release folder, a journal open or held) is not one where
// nothing is deployed, even when current is missing: boot inhibits
// decisions and fails as a failed qualification does ("current is
// missing": the host's stop list runs, the failure is owed), exit 1, never
// exit 4; a journal is settled as any command settles it, the lock file
// made when it is missing.
func TestBootOnADeployedHostWithoutCurrent(t *testing.T) {
	noCurrent := func(e *env) {
		if err := os.Remove(filepath.Join(e.root, BaseDir, "current")); err != nil {
			e.t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T) *env
		owed  bool // the failure is boot's, owed; otherwise a journal is settled
	}{
		{"current removed", func(t *testing.T) *env {
			e := activeOwn(t)
			noCurrent(e)
			return e
		}, true},
		{"current and the lock file removed", func(t *testing.T) *env {
			e := activeOwn(t)
			noCurrent(e)
			if err := os.Remove(filepath.Join(e.root, StateDir, LockFile)); err != nil {
				t.Fatal(err)
			}
			return e
		}, true},
		{"only deploy.json", func(t *testing.T) *env {
			e := activeOwn(t)
			if err := os.RemoveAll(filepath.Join(e.root, BaseDir)); err != nil {
				t.Fatal(err)
			}
			return e
		}, true},
		{"a release folder never switched to", func(t *testing.T) *env {
			e := newEnv(t)
			e.release(rel1, "s")
			e.write(filepath.Join(e.root, StateDir, LockFile), "", 0o600)
			return e
		}, true},
		{"a first switch interrupted, its lock file removed", func(t *testing.T) *env {
			e := newEnv(t)
			e.release(rel1, "s")
			e.crashAt(CrashLinks, false, rel1)
			if err := os.Remove(filepath.Join(e.root, StateDir, LockFile)); err != nil {
				t.Fatal(err)
			}
			return e
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.setup(t)
			e.keepOwed = true
			g := guard(e)
			e.runner.reset()
			unguarded := e.guarded()
			if code := e.boot(); code != ExitFailed {
				t.Fatalf("boot: exit %d, want %d\n%s", code, ExitFailed, e.last)
			}
			if *unguarded != 0 {
				t.Errorf("%d command(s) ran while decisions were not inhibited", *unguarded)
			}
			in, ok := e.inhibited()
			if !ok || g.decide() != "refused" || e.guardCheck() != decisionguard.CodeInhibited {
				t.Errorf("after boot: inhibit %+v %v, a decision %s", in, ok, g.decide())
			}
			if e.runner.called("stop-all") == 0 {
				t.Errorf("the host's stop list did not run: %v", e.runner.calls)
			}
			if tc.owed {
				owed := e.owed()
				if len(owed) != 1 || owed[0].Direction != DirBoot || owed[0].Txn != in.Txn || !strings.Contains(owed[0].Detail, "current is missing") {
					t.Errorf("owed: %+v (inhibit %+v)", owed, in)
				}
				if !strings.Contains(in.Reason, "current is missing") {
					t.Errorf("inhibit: %+v", in)
				}
				// A later boot keeps it as found.
				e.runner.reset()
				if code := e.boot(); code != ExitFailed || len(e.runner.calls) != 0 {
					t.Errorf("a later boot: exit %d, ran %v", code, e.runner.calls)
				}
				return
			}
			e.noJournal()
			if rec := e.record(); rec.Result == ResultOK {
				t.Errorf("record: %+v", rec)
			}
		})
	}
}

// TestBootCrashKeepsDecisionsRefused: boot dies at every one of its
// steps, on the way to a qualification that passes and to each kind of
// failure. At every instant decisions are refused (decisions.inhibit is
// there) unless boot qualified current in this boot. After the crash a
// reboot and the next boot keep the inhibit at every command they run,
// and lift it only by boot's rules: an unfinished boot's own inhibit is
// taken over and lifted once current qualifies; a failure is written into
// the inhibit before anything else of it is done (the stop list, the
// outcome owed), so from the first step of a failure on the inhibit holds
// it, and no later boot lifts it: not after the crash, and not after the
// owner acknowledged the outcome owed.
func TestBootCrashKeepsDecisionsRefused(t *testing.T) {
	reloadFails := func(e *env) { e.runner.fail["reload-old"] = -1 }
	healthFails := func(e *env) { e.runner.fail["health-old"] = -1 }
	postFails := func(e *env) { e.runner.failPost = e.relDir(rel2) }
	for _, tc := range []struct {
		name  string
		point string
		fail  func(e *env)
	}{
		{"inhibit", CrashBootInhibit, nil},
		{"reload", CrashRequalifyReload, nil},
		{"health", CrashRequalifyHealth, nil},
		{"qualified", CrashBootQualified, nil},
		{"lifted", CrashBootLifted, nil},
		{"reload fails: inhibit", CrashBootInhibit, reloadFails},
		{"reload fails: kept", CrashBootKept, reloadFails},
		{"reload fails: failed", CrashBootFailed, reloadFails},
		{"reload fails: owed", CrashBootOwed, reloadFails},
		{"health fails: inhibit", CrashBootInhibit, healthFails},
		{"health fails: reload", CrashRequalifyReload, healthFails},
		{"health fails: kept", CrashBootKept, healthFails},
		{"health fails: failed", CrashBootFailed, healthFails},
		{"health fails: owed", CrashBootOwed, healthFails},
		{"postcheck fails: inhibit", CrashBootInhibit, postFails},
		{"postcheck fails: reload", CrashRequalifyReload, postFails},
		{"postcheck fails: health", CrashRequalifyHealth, postFails},
		{"postcheck fails: kept", CrashBootKept, postFails},
		{"postcheck fails: failed", CrashBootFailed, postFails},
		{"postcheck fails: owed", CrashBootOwed, postFails},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, g := rebooted(t)
			if tc.fail != nil {
				tc.fail(e)
			}
			unguarded := e.guarded()
			e.bootCrashAt(tc.point)
			if *unguarded != 0 {
				t.Errorf("%d command(s) ran while decisions were not inhibited", *unguarded)
			}
			if _, ok := e.journal(); ok {
				t.Error("boot left a journal")
			}
			in, inhibited := e.inhibited()
			qualified := tc.point == CrashBootLifted
			if inhibited == qualified {
				t.Fatalf("after a crash at %s: inhibited %v", tc.point, inhibited)
			}
			// The failure is known from its first step on: the inhibit
			// holds it; it is owed only once that step is done.
			known := tc.point == CrashBootKept || tc.point == CrashBootFailed || tc.point == CrashBootOwed
			if inhibited && known != strings.HasPrefix(in.Reason, "boot: after the boot, ") {
				t.Errorf("after a crash at %s: the inhibit %+v", tc.point, in)
			}
			if inhibited && !known && in.Reason != bootReason {
				t.Errorf("after a crash at %s: the inhibit %+v is not boot's record", tc.point, in)
			}
			if stops := e.runner.called("stop-all"); stops != map[bool]int{true: 1, false: 0}[tc.point == CrashBootFailed || tc.point == CrashBootOwed] {
				t.Errorf("after a crash at %s: the host's stop list ran %d times", tc.point, stops)
			}
			if want := map[bool]string{true: "refused", false: "accepted"}[inhibited]; g.decide() != want {
				t.Errorf("after a crash at %s, inhibited %v: a decision is %s", tc.point, inhibited, g.decide())
			}
			if got := e.status(); inhibited && !strings.Contains(got, "by ah-switch boot (run ") {
				t.Errorf("status after a crash at %s:\n%s", tc.point, got)
			}
			owedOne := tc.point == CrashBootOwed
			if owed := e.owed(); owedOne != (len(owed) == 1 && owed[0].Direction == DirBoot && owed[0].RequalifyFailed && owed[0].Txn == in.Txn) {
				t.Errorf("after a crash at %s: owed %+v", tc.point, owed)
			}
			// The reboot, with a host that would now qualify, then the
			// next boot.
			g.started, g.asked = false, nil
			delete(e.runner.fail, "reload-old")
			delete(e.runner.fail, "health-old")
			e.runner.failPost = ""
			inhibitText, owedText := e.stateFile(InhibitFile), e.stateFile(OwedFile)
			e.runner.reset()
			unguarded = e.guarded()
			code := e.boot()
			if *unguarded != 0 {
				t.Errorf("the next boot ran %d command(s) while decisions were not inhibited", *unguarded)
			}
			_, inhibited = e.inhibited()
			if known {
				if code != ExitFailed || len(e.runner.calls) != 0 || e.stateFile(InhibitFile) != inhibitText || e.stateFile(OwedFile) != owedText {
					t.Errorf("the next boot after a known failure: exit %d, ran %v, inhibit or owed changed", code, e.runner.calls)
				}
				if g.decide() != "refused" {
					t.Error("a decision is accepted after a known failure")
				}
				// The owner acknowledges what is owed; a later boot
				// still keeps the failure as found.
				if owedOne {
					if code := e.ack("saw the failed boot"); code != ExitOK {
						t.Fatalf("ack: exit %d\n%s", code, e.last)
					}
				}
				e.runner.reset()
				if code := e.boot(); code != ExitFailed || len(e.runner.calls) != 0 || e.stateFile(InhibitFile) != inhibitText || g.decide() != "refused" {
					t.Errorf("a boot after the ack: exit %d, ran %v, a decision %s", code, e.runner.calls, g.decide())
				}
				return
			}
			if code != ExitOK || inhibited || g.decide() != "accepted" || !strings.HasPrefix(e.names(), "reload-old health-old nft") {
				t.Errorf("the next boot: exit %d, inhibited %v, a decision %s, ran %q", code, inhibited, g.decide(), e.names())
			}
		})
	}
}

// TestBootTakesOverOnlyAWholeRecord: the one inhibit boot takes over is
// exactly the record boot writes: its reason, a run id as boot makes it
// and the time as boot writes it. A record that holds boot's reason but
// lacks any of the rest, or holds another shape of it, was not written by
// a boot: it stays as found, and boot runs nothing.
func TestBootTakesOverOnlyAWholeRecord(t *testing.T) {
	for name, record := range map[string]string{
		"no time":        `{"txn": "` + neverTxn + `", "reason": "` + bootReason + `"}`,
		"an empty time":  `{"txn": "` + neverTxn + `", "reason": "` + bootReason + `", "time": ""}`,
		"another time":   `{"txn": "` + neverTxn + `", "reason": "` + bootReason + `", "time": "yesterday"}`,
		"a local time":   `{"txn": "` + neverTxn + `", "reason": "` + bootReason + `", "time": "2026-11-21T03:00:00-09:00"}`,
		"no run id":      `{"reason": "` + bootReason + `", "time": "2026-11-21T12:00:00Z"}`,
		"a bad run id":   `{"txn": "0123", "reason": "` + bootReason + `", "time": "2026-11-21T12:00:00Z"}`,
		"another reason": `{"txn": "` + neverTxn + `", "reason": "` + bootReason + `.", "time": "2026-11-21T12:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			e, g := rebooted(t)
			e.write(e.inhibitPath(), record, 0o644)
			e.runner.reset()
			if code := e.boot(); code != ExitFailed {
				t.Errorf("boot: exit %d\n%s", code, e.last)
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("boot ran %v", e.runner.calls)
			}
			if e.stateFile(InhibitFile) != record || g.decide() != "refused" {
				t.Errorf("boot changed the record it found: %q", e.stateFile(InhibitFile))
			}
			if got := e.status(); !strings.Contains(got, "a later boot keeps it") {
				t.Errorf("status:\n%s", got)
			}
		})
	}
	t.Run("a whole record", func(t *testing.T) {
		e, g := rebooted(t)
		e.write(e.inhibitPath(), `{"txn": "`+neverTxn+`", "reason": "`+bootReason+`", "time": "2026-11-21T12:00:00Z"}`, 0o644)
		if code := e.boot(); code != ExitOK || g.decide() != "accepted" {
			t.Errorf("boot: exit %d, a decision %s\n%s", code, g.decide(), e.last)
		}
	})
}

// TestBootLiftsOnlyItsOwnRecord: once current qualified again, boot reads
// the inhibit back and removes it only when it is still exactly the record
// this run wrote. Changed in that instant (another run id, another time,
// another reason, or removed), it is left as it is, nothing is lifted, and
// boot exits 1.
func TestBootLiftsOnlyItsOwnRecord(t *testing.T) {
	for name, change := range map[string]func(e *env, in inhibit){
		"another run id": func(e *env, in inhibit) {
			in.Txn = neverTxn
			e.writeInhibitFile(in)
		},
		"another time": func(e *env, in inhibit) {
			in.Time = "2026-11-21T13:00:00Z"
			e.writeInhibitFile(in)
		},
		"another reason": func(e *env, in inhibit) {
			in.Reason = "the owner's note"
			e.writeInhibitFile(in)
		},
		"removed": func(e *env, in inhibit) {
			if err := os.Remove(e.inhibitPath()); err != nil {
				e.t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e, _ := rebooted(t)
			s := e.switcher()
			var changed string
			s.crashAt = func(p string) error {
				if p == CrashBootQualified {
					in, ok := e.inhibited()
					if !ok || in.Reason != bootReason {
						t.Fatalf("at %s the inhibit is %+v, %v", p, in, ok)
					}
					change(e, in)
					changed = e.stateFile(InhibitFile)
				}
				return nil
			}
			code := s.Boot(context.Background())
			e.last = e.out.String()
			e.out.Reset()
			if code != ExitFailed || !strings.Contains(e.last, "no longer the record this boot wrote") {
				t.Errorf("boot: exit %d\n%s", code, e.last)
			}
			if got := e.stateFile(InhibitFile); got != changed {
				t.Errorf("boot changed the inhibit after its read-back: %q, want %q", got, changed)
			}
		})
	}
}

// TestASwitchLiftsWhatBootLeft: after boot died or failed, a switch lifts
// the inhibit by its own rules (a transaction that ends healthy), once
// what is owed is acknowledged.
func TestASwitchLiftsWhatBootLeft(t *testing.T) {
	for _, point := range []string{CrashBootInhibit, CrashBootKept} {
		t.Run(point, func(t *testing.T) {
			e, g := rebooted(t)
			if point == CrashBootKept {
				e.runner.fail["health-old"] = -1
			}
			e.bootCrashAt(point)
			delete(e.runner.fail, "health-old")
			if _, ok := e.inhibited(); !ok {
				t.Fatal("not inhibited after the crash")
			}
			if len(e.owed()) > 0 {
				if code := e.ack("saw the failed boot"); code != ExitOK {
					t.Fatalf("ack: exit %d\n%s", code, e.last)
				}
			}
			if code := e.switchTo(rel3); code != ExitOK {
				t.Fatalf("switch: exit %d\n%s", code, e.last)
			}
			if _, ok := e.inhibited(); ok || g.decide() != "accepted" {
				t.Errorf("after a healthy switch: inhibited %v, a decision %s", ok, g.decide())
			}
		})
	}
}

// TestBootOutcomeOwed: the outcome a failed boot owes is read back with the
// checks of owed.json, and no switch or rollback request is a retry of it.
func TestBootOutcomeOwed(t *testing.T) {
	good := owedOutcome{Txn: neverTxn, RequestID: requestID(DirBoot, rel2), Direction: DirBoot, Requested: DirBoot, ID: rel2, CurrentID: rel2, Result: ResultFailed, FailedStep: "boot", RequalifyFailed: true}
	if err := good.check(); err != nil {
		t.Fatalf("a boot's outcome: %v", err)
	}
	for _, arg := range []string{rel2, "previous"} {
		for _, rollback := range []bool{false, true} {
			if good.matches(arg, rollback) {
				t.Errorf("a request (%s, rollback %v) is taken as a retry of the boot's outcome", arg, rollback)
			}
		}
	}
	for name, mod := range map[string]func(o *owedOutcome){
		"not requalify_failed": func(o *owedOutcome) { o.RequalifyFailed = false },
		"result ok":            func(o *owedOutcome) { o.Result = ResultOK },
		"requested an id":      func(o *owedOutcome) { o.Requested = rel2 },
		"request id":           func(o *owedOutcome) { o.RequestID = requestID(DirForward, rel2) },
		"from":                 func(o *owedOutcome) { o.FromID = rel1 },
		"another current":      func(o *owedOutcome) { o.CurrentID = rel1 },
		"restored health":      func(o *owedOutcome) { o.RestoredHealth = "ok" },
		"services stopped":     func(o *owedOutcome) { o.ServicesStopped = true },
	} {
		o := good
		mod(&o)
		if o.check() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// bootRewriteFails runs ah-switch boot, whose rewrite of decisions.inhibit
// with its failure fails (a folder that is not empty stands where the
// new file is made), as a write that fails on the host would: boot's own
// record stays, the host's stop list runs and the failure is owed under
// its run. It returns that record.
func (e *env) bootRewriteFails() inhibit {
	e.t.Helper()
	blocker := filepath.Join(e.root, StateDir, "."+InhibitFile+".new")
	s := e.switcher()
	s.crashAt = func(p string) error {
		if p == CrashBootInhibit {
			if err := os.MkdirAll(filepath.Join(blocker, "x"), 0o755); err != nil {
				e.t.Fatal(err)
			}
		}
		return nil
	}
	code := s.Boot(context.Background())
	e.t.Logf("boot, its rewrite failing: exit %d\n%s", code, e.out.String())
	e.last = e.out.String()
	e.out.Reset()
	if code != ExitFailed || !strings.Contains(e.last, "not rewritten with the failure") {
		e.t.Fatalf("boot: exit %d\n%s", code, e.last)
	}
	in, ok := e.inhibited()
	owed := e.owed()
	if !ok || in.Reason != bootReason || len(owed) != 1 || owed[0].Txn != in.Txn || owed[0].Direction != DirBoot {
		e.t.Fatalf("after the boot: inhibit %+v %v, owed %+v", in, ok, owed)
	}
	return in
}

// TestAckWritesTheFailureOfABoot: a failed boot could not rewrite its
// inhibit with the failure, so its own record stays, with the failure owed
// under its run. The ack of that outcome first rewrites the record as the
// failure, as boot would have, and only then acknowledges it; when it
// cannot, it refuses and leaves the outcome owed. A later boot keeps the
// failure record, as it keeps every failure: only a healthy switch or
// inhibit-clear lifts it. An ack recorded already (deploy.json holds
// its transactions) and repeated does the same before it finishes.
func TestAckWritesTheFailureOfABoot(t *testing.T) {
	blocker := func(e *env) string { return filepath.Join(e.root, StateDir, "."+InhibitFile+".new") }
	for _, retry := range []bool{false, true} {
		t.Run(map[bool]string{false: "an ack", true: "an ack recorded already"}[retry], func(t *testing.T) {
			e, g := rebooted(t)
			e.runner.fail["health-old"] = -1
			run := e.bootRewriteFails()
			delete(e.runner.fail, "health-old")
			if retry {
				rec := e.record()
				rec.OutcomeAckedAt, rec.OutcomeAckedAsked, rec.OutcomeAckedTxn = "2026-11-21T12:00:00Z", "saw it before", run.Txn
				data, err := json.Marshal(rec)
				if err != nil {
					t.Fatal(err)
				}
				e.write(filepath.Join(e.root, StateDir, DeployFile), string(data), 0o644)
			}
			// The rewrite fails again: nothing is acknowledged.
			owedText := e.stateFile(OwedFile)
			if code := e.ack("saw the failed boot"); code != ExitFailed || !strings.Contains(e.last, "not rewritten") {
				t.Errorf("ack while the inhibit cannot be written: exit %d\n%s", code, e.last)
			}
			if e.stateFile(OwedFile) != owedText {
				t.Error("the outcome is no longer owed after a refused ack")
			}
			if in, _ := e.inhibited(); in != run {
				t.Errorf("inhibit after a refused ack: %+v", in)
			}
			if err := os.RemoveAll(blocker(e)); err != nil {
				t.Fatal(err)
			}
			if code := e.ack("saw the failed boot"); code != ExitOK {
				t.Fatalf("ack: exit %d\n%s", code, e.last)
			}
			in, ok := e.inhibited()
			if !ok || in.Txn != run.Txn || !strings.HasPrefix(in.Reason, "boot: after the boot, "+q("releases/"+rel2)+" did not pass") {
				t.Errorf("after the ack: inhibit %+v, %v", in, ok)
			}
			if len(e.owed()) != 0 {
				t.Errorf("owed after the ack: %+v", e.owed())
			}
			before := e.stateFile(InhibitFile)
			e.runner.reset()
			if code := e.boot(); code != ExitFailed || len(e.runner.calls) != 0 || e.stateFile(InhibitFile) != before {
				t.Errorf("the next boot: exit %d, ran %v, inhibit changed %v\n%s", code, e.runner.calls, e.stateFile(InhibitFile) != before, e.last)
			}
			if got := g.decide(); got != "refused" {
				t.Errorf("a decision is %s after the next boot", got)
			}
		})
	}
}

// TestBootTakesAnInhibitItCannotReadAsUnresolved: the journal held by the
// inhibit after an interrupted healthy end fails its trust check (its mode
// was changed), so it cannot be read. That may be a transaction still to
// settle: boot prints an outcome owed as not final, without the ack
// command, and never takes the host for one where nothing was deployed
// (exit 4), also when that journal is the only sign of a deployment.
func TestBootTakesAnInhibitItCannotReadAsUnresolved(t *testing.T) {
	untrusted := func(e *env) {
		if err := os.Chmod(e.inhibitPath(), 0o666); err != nil {
			e.t.Fatal(err)
		}
	}
	t.Run("an outcome owed", func(t *testing.T) {
		e := activeOwn(t)
		e.keepOwed = true
		e.crashAt(CrashJournalMoved, false, rel3)
		owed := e.owed()
		if len(owed) != 1 || !strings.Contains(e.stateFile(InhibitFile), `"phase"`) {
			t.Fatalf("owed %+v, inhibit %s", owed, e.stateFile(InhibitFile))
		}
		untrusted(e)
		e.runner.reset()
		if code := e.boot(); code != ExitFailed || len(e.runner.calls) != 0 {
			t.Fatalf("boot: exit %d, ran %v\n%s", code, e.runner.calls, e.last)
		}
		if strings.Contains(e.last, "ack --txn") {
			t.Errorf("boot printed an ack command beside an inhibit it cannot read:\n%s", e.last)
		}
		if !strings.Contains(e.last, "outcome owed, not final (request "+q(owed[0].RequestID)+", transaction "+owed[0].Txn+")") {
			t.Errorf("boot did not print the outcome owed as not final:\n%s", e.last)
		}
	})
	for _, lock := range []bool{true, false} {
		t.Run(map[bool]string{true: "the only sign of a deployment", false: "the only sign of a deployment, no lock file"}[lock], func(t *testing.T) {
			e := newEnv(t)
			e.release(rel1, "s")
			e.crashAt(CrashJournalMoved, false, rel1)
			for _, p := range []string{filepath.Join(e.root, BaseDir, "current"), filepath.Join(e.root, StateDir, DeployFile)} {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			}
			makeWritable(e.relDir(rel1))
			if err := os.RemoveAll(e.relDir(rel1)); err != nil {
				t.Fatal(err)
			}
			if !lock {
				if err := os.Remove(filepath.Join(e.root, StateDir, LockFile)); err != nil {
					t.Fatal(err)
				}
			}
			untrusted(e)
			before := e.stateFile(InhibitFile)
			e.runner.reset()
			if code := e.boot(); code != ExitFailed {
				t.Errorf("boot: exit %d, want %d\n%s", code, ExitFailed, e.last)
			}
			if len(e.runner.calls) != 0 || e.stateFile(InhibitFile) != before {
				t.Errorf("boot ran %v, changed the inhibit %v", e.runner.calls, e.stateFile(InhibitFile) != before)
			}
			if e.guardCheck() != decisionguard.CodeInhibited {
				t.Errorf("the guard answers %q", e.guardCheck())
			}
		})
	}
}
