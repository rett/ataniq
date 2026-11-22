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
		if want := "decisions stay inhibited (decisions.inhibit): the switch to " + rel1 + " did not end healthy"; !strings.Contains(e.last, want) {
			t.Errorf("output lacks %q:\n%s", want, e.last)
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

// TestEveryCommandRestoresTheInhibitBesideAJournal: decisions.inhibit is
// there whenever txn.json is, and a command that finds a journal without
// it (removed by hand) writes it back first, before it checks the journal
// against the host: a journal that does not match the host is neither
// resumed nor settled, and it is still never the only guard. A dry run
// writes nothing.
func TestEveryCommandRestoresTheInhibitBesideAJournal(t *testing.T) {
	e := activeOwn(t)
	e.crashAt(CrashActivating, false, rel3)
	j, _ := e.journal()
	os.Remove(e.inhibitPath())
	// The links no longer match the journal: it is not resumed.
	if err := os.Remove(filepath.Join(e.root, BaseDir, "previous")); err != nil {
		t.Fatal(err)
	}
	if got := e.status(); !strings.Contains(got, "decisions: not inhibited, although "+JournalFile+" is there") {
		t.Errorf("status:\n%s", got)
	}
	e.runner.reset()
	if code := e.switchTo(rel3, func(c *Config) { c.Dry = true }); code != ExitFailed {
		t.Fatalf("dry run: exit %d\n%s", code, e.last)
	}
	if _, ok := e.inhibited(); ok {
		t.Errorf("a dry run wrote the inhibit")
	}
	if code := e.recover(resumeOnly); code != ExitFailed || !strings.Contains(e.last, "does not match the host") {
		t.Fatalf("recover: exit %d\n%s", code, e.last)
	}
	in, ok := e.inhibited()
	if !ok || in.Txn != j.Txn {
		t.Errorf("a command that found the journal left it without the inhibit: %+v, %v", in, ok)
	}
	if len(e.runner.calls) != 0 {
		t.Errorf("commands run: %v", e.runner.calls)
	}
	if got, ok := e.journal(); !ok || got.Phase != PhaseActivating {
		t.Errorf("journal: %+v, %v", got, ok)
	}
}

// TestARefusedCommandRestoresTheInhibitBesideAJournal: a command that
// finds a journal without decisions.inhibit (removed by hand) writes it
// back before it does anything else, also when it then refuses: a
// recover --discard-journal of a journal that matches the host or of
// another transaction, an ack and an inhibit-clear, which are refused
// while a transaction is open, and a boot whose policy cannot be used.
// The journal stays as it was, and nothing is run.
func TestARefusedCommandRestoresTheInhibitBesideAJournal(t *testing.T) {
	clear := func(e *env) int {
		code := e.switcher().InhibitClear(context.Background(), "looked")
		e.last = e.out.String()
		e.out.Reset()
		return code
	}
	for _, tc := range []struct {
		name string
		run  func(e *env, j journal) int
		want string
	}{
		{"a discard of a journal that matches the host", func(e *env, j journal) int {
			return e.recover(RecoverOptions{DiscardJournal: j.Txn})
		}, "matches the host"},
		{"a discard of another transaction", func(e *env, j journal) int {
			return e.recover(RecoverOptions{DiscardJournal: neverTxn})
		}, "no journal of the transaction"},
		{"an ack", func(e *env, j journal) int {
			return e.ackTxns([]string{neverTxn}, "looked")
		}, "still open"},
		{"an inhibit-clear", func(e *env, j journal) int {
			return clear(e)
		}, "still open"},
		{"a boot whose policy cannot be used", func(e *env, j journal) int {
			e.write(filepath.Join(e.root, PolicyDir, PolicyName), "{", 0o644)
			return e.boot()
		}, "switch policy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := activeOwn(t)
			g := guard(e)
			e.crashAt(CrashActivating, false, rel3)
			j, _ := e.journal()
			if err := os.Remove(e.inhibitPath()); err != nil {
				t.Fatal(err)
			}
			e.runner.reset()
			if code := tc.run(e, j); code != ExitFailed || !strings.Contains(e.last, tc.want) {
				t.Fatalf("exit %d, want %d and %q\n%s", code, ExitFailed, tc.want, e.last)
			}
			in, ok := e.inhibited()
			if !ok || in.Txn != j.Txn || in.Reason == bootReason {
				t.Errorf("the command left the journal without the transaction's inhibit: %+v, %v", in, ok)
			}
			if got := g.decide(); got != "refused" {
				t.Errorf("a decision is %s beside the open journal", got)
			}
			if len(e.runner.calls) != 0 {
				t.Errorf("commands run: %v", e.runner.calls)
			}
			if got, ok := e.journal(); !ok || got.Phase != PhaseActivating {
				t.Errorf("journal: %+v, %v", got, ok)
			}
		})
	}
}

// TestTheStopsOwedRunWithTheInhibit: a journal left in its stopping phase
// whose inhibit was removed by hand: every command writes the inhibit back
// before it runs the host's stop list, a boot and a command that cannot use
// the switch policy included, so that no stop command runs while decisions
// are allowed.
func TestTheStopsOwedRunWithTheInhibit(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(e *env) int
	}{
		{"boot", func(e *env) int { return e.boot() }},
		{"a switch", func(e *env) int { return e.switchTo(rel3) }},
		{"a recover", func(e *env) int { return e.recover(resumeOnly) }},
		{"a switch without a usable policy", func(e *env) int {
			e.write(filepath.Join(e.root, PolicyDir, PolicyName), "{", 0o644)
			return e.switchTo(rel3)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := activeOwn(t)
			e.keepOwed = true
			e.runner.fail["health-probe"] = -1
			e.runner.fail["health-old"] = -1
			e.crashAt(CrashStopping, false, rel3)
			j, ok := e.journal()
			if !ok || j.Phase != PhaseStopping {
				t.Fatalf("journal: %+v, %v", j, ok)
			}
			if err := os.Remove(e.inhibitPath()); err != nil {
				t.Fatal(err)
			}
			e.runner.reset()
			unguarded := e.guarded()
			tc.run(e)
			if e.runner.called("stop-all") == 0 {
				t.Fatalf("the host's stop list did not run: %v", e.runner.calls)
			}
			if *unguarded != 0 {
				t.Errorf("%d command(s) ran while decisions were allowed beside the stopping journal: %v", *unguarded, e.runner.calls)
			}
			if in, ok := e.inhibited(); !ok || in.Txn != j.Txn {
				t.Errorf("inhibit: %+v, %v", in, ok)
			}
		})
	}
}

// TestDiscardLeavesDecisionsInhibited: recover --discard-journal sets a
// journal aside on the owner's word that it does not match the host; that
// qualifies nothing, so it writes the inhibit first when it is not there
// (removed by hand) and never leaves the host with neither guard. Only a
// healthy end or inhibit-clear removes the inhibit.
func TestDiscardLeavesDecisionsInhibited(t *testing.T) {
	e := activeOwn(t)
	g := guard(e)
	e.crashAt(CrashActivating, false, rel3)
	j, _ := e.journal()
	os.Remove(e.inhibitPath())
	if err := os.Remove(filepath.Join(e.root, BaseDir, "previous")); err != nil {
		t.Fatal(err)
	}
	e.runner.reset()
	if code := e.recover(RecoverOptions{DiscardJournal: j.Txn}); code != ExitOK {
		t.Fatalf("discard: exit %d\n%s", code, e.last)
	}
	e.noJournal()
	in, ok := e.inhibited()
	if !ok || in.Txn != j.Txn || !strings.Contains(in.Reason, "set aside by the owner") {
		t.Errorf("inhibit after the discard: %+v, %v", in, ok)
	}
	if got := g.decide(); got != "refused" {
		t.Errorf("after the discard a decision is %s", got)
	}
	if len(e.runner.calls) != 0 {
		t.Errorf("commands run: %v", e.runner.calls)
	}
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
// readiness probe passes only while it runs; before each decision it asks
// decisionguard.Check, as the contract says, and refuses unless it allows
// (decisions.inhibit does not exist in a real state folder).
type guardedApp struct {
	e       *env
	started bool
	// What a decision asked while the readiness probe ran got, in order.
	asked []string
}

func (g *guardedApp) decide() string {
	if decisionguard.Check(filepath.Join(g.e.root, StateDir)) != nil {
		return "refused"
	}
	return "accepted"
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

// TestTheReferenceServiceAsksTheGuard: the model service decides by
// decisionguard.Check, the check the contract names, not by a lookup of
// its own: it refuses while the state folder is missing or is a link,
// and while decisions.inhibit is there, and decides only on a real state
// folder without it.
func TestTheReferenceServiceAsksTheGuard(t *testing.T) {
	e := newEnv(t)
	g := guard(e)
	state := filepath.Join(e.root, StateDir)
	if _, err := os.Lstat(state); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the fixture has a state folder: %v", err)
	}
	if got := g.decide(); got != "refused" {
		t.Errorf("without a state folder a decision is %s", got)
	}
	other := filepath.Join(e.root, "elsewhere")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(state), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, state); err != nil {
		t.Fatal(err)
	}
	if got := g.decide(); got != "refused" {
		t.Errorf("with a link for the state folder a decision is %s", got)
	}
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(state, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := g.decide(); got != "accepted" {
		t.Errorf("on a state folder without the inhibit a decision is %s", got)
	}
	e.write(e.inhibitPath(), "", 0o644)
	if got := g.decide(); got != "refused" {
		t.Errorf("with the inhibit a decision is %s", got)
	}
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
// only after the reload list, health check and post-activation no-AI check
// of the release current points at pass again; decisions are refused until
// then. When they do not
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
			if got := e.status(); !strings.Contains(got, "interrupted: switch to "+rel3+" in phase done") || !strings.Contains(got, "runs the reload list, the health check and the no-AI check of "+rel3+" again before decisions are no longer inhibited") {
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
				// The checker is the one of the verified release current
				// points at, by its full path.
				if strings.Join(ran, " ") != "reload-units health-probe nft "+filepath.Join(e.relDir(rel3), NoAICheckProgram) {
					t.Errorf("ran %v, want the reload list, health check and post-activation check of %s", ran, rel3)
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
			if !ok || !strings.Contains(in.Reason, "did not pass its reload, health check and no-AI check again") {
				t.Errorf("inhibit: %+v, %v", in, ok)
			}
			if !strings.Contains(rec.Detail, "did not pass its reload, health check and no-AI check again") {
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
	if got := e.status(); !strings.Contains(got, "did not pass its reload, health check and no-AI check again") {
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

// TestTheInhibitIsTheOneGuard: decisions.inhibit is the one file a
// decision-capable service looks up. At every crash point of a switch (a
// package step, a healthy end, going back, stopping the services), the
// journal is never there without it, and it is absent only once a
// transaction ended healthy and qualified; a service following the
// one-lookup contract refuses exactly while it is there. The next command
// keeps it there while it resumes (at every command it runs), and removes
// it only for an outcome that is healthy.
func TestTheInhibitIsTheOneGuard(t *testing.T) {
	healthy := func(rec Record) bool {
		return rec.Result == ResultOK || rec.Result == ResultRolledBack && rec.RestoredHealth == "ok"
	}
	back := map[string]int{"health-probe": -1}
	stopped := map[string]int{"health-probe": -1, "health-old": -1}
	for _, tc := range []struct {
		name     string
		point    string
		snapshot string
		fail     map[string]int
	}{
		{"package-start", CrashPackageStart, "t", nil},
		{"package-ran", CrashPackageRan, "t", nil},
		{"package-done", CrashPackageDone, "t", nil},
		{"links", CrashLinks, "s", nil},
		{"counter", CrashCounter, "s", nil},
		{"previous", CrashPrevious, "s", nil},
		{"current", CrashCurrent, "s", nil},
		{"activating", CrashActivating, "s", nil},
		{"reload", CrashReload, "s", nil},
		{"health", CrashHealth, "s", nil},
		{"postcheck", CrashPostCheck, "s", nil},
		{"done", CrashDone, "s", nil},
		{"record", CrashRecord, "s", nil},
		{"healthy end", CrashJournalMoved, "s", nil},
		{"unreported", CrashUnreported, "s", nil},
		{"restoring", CrashRestoring, "s", back},
		{"candidate-stopped", CrashCandidateStopped, "s", back},
		{"restore-links", CrashRestoreLinks, "s", back},
		{"restore-reload", CrashRestoreReload, "s", back},
		{"restore-health", CrashRestoreHealth, "s", back},
		{"restored done", CrashDone, "s", back},
		{"restored record", CrashRecord, "s", back},
		{"restored healthy end", CrashJournalMoved, "s", back},
		{"restored unreported", CrashUnreported, "s", back},
		{"stopping", CrashStopping, "s", stopped},
		{"stopped", CrashStopped, "s", stopped},
		{"stopped done", CrashDone, "s", stopped},
		{"stopped record", CrashRecord, "s", stopped},
		{"stopped end", CrashInhibitKept, "s", stopped},
		{"stopped unreported", CrashUnreported, "s", stopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := activeOwn(t)
			e.keepOwed = true
			if tc.snapshot != "s" {
				e.release(rel3, tc.snapshot)
			}
			for k, v := range tc.fail {
				e.runner.fail[k] = v
			}
			g := guard(e)
			g.started = true
			e.crashAt(tc.point, false, rel3)
			_, open := e.journal()
			_, inhibited := e.inhibited()
			if open && !inhibited {
				t.Fatalf("after a crash at %s the journal is there without the inhibit", tc.point)
			}
			if !inhibited && (open || !healthy(e.record())) {
				t.Fatalf("after a crash at %s the inhibit is gone, but no transaction ended healthy: journal %v, record %+v", tc.point, open, e.record())
			}
			if want := map[bool]string{true: "refused", false: "accepted"}[inhibited]; g.decide() != want {
				t.Errorf("after a crash at %s, inhibited %v: a decision is %s", tc.point, inhibited, g.decide())
			}
			// The reboot, then the next command: the inhibit is there at
			// every command the resume runs, and gone only after an
			// outcome that is healthy.
			g.started, g.asked = false, nil
			unguarded := 0
			e.runner.hook = func([]string) {
				if _, ok := e.inhibited(); !ok {
					unguarded++
				}
			}
			e.runner.reset()
			e.recover(resumeOnly)
			e.runner.hook = nil
			if unguarded > 0 {
				t.Errorf("the resume ran %d command(s) without the inhibit: %v", unguarded, e.runner.calls)
			}
			e.noJournal()
			_, inhibited = e.inhibited()
			rec := e.record()
			owed := e.owed()
			qualified := healthy(rec) && (len(owed) == 0 || !owed[len(owed)-1].RequalifyFailed)
			if inhibited == qualified {
				t.Errorf("after the resume: inhibited %v, record %+v, owed %+v", inhibited, rec, owed)
			}
			if want := map[bool]string{true: "refused", false: "accepted"}[inhibited]; g.decide() != want {
				t.Errorf("after the resume, inhibited %v: a decision is %s", inhibited, g.decide())
			}
		})
	}
}

// TestCrashBetweenTheTwoStepsOfAHealthyEnd: a healthy end first renames
// the journal over decisions.inhibit, then removes that file. A crash (a
// power loss) between the two leaves the journal in the inhibit: services
// refuse, status says what is pending, and the next command writes the
// journal back, qualifies current again and settles it, the inhibit there
// all the while; when the qualification fails, the inhibit stays with the
// failure, and a decision is never accepted on the way.
func TestCrashBetweenTheTwoStepsOfAHealthyEnd(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reload bool // the reload after the reboot passes
	}{{"qualified again", true}, {"not qualified after the reboot", false}} {
		t.Run(tc.name, func(t *testing.T) {
			e := activeOwn(t)
			e.keepOwed = true
			g := guard(e)
			g.started = true
			e.crashAt(CrashJournalMoved, false, rel3)
			e.noJournal()
			if _, ok := e.inhibited(); !ok {
				t.Fatalf("no inhibit after a crash between the two steps of a healthy end")
			}
			if got := e.status(); !strings.Contains(got, "interrupted: switch to "+rel3+" in phase done") || !strings.Contains(got, "runs the reload list, the health check and the no-AI check of "+rel3+" again") || strings.Contains(got, "decisions: not inhibited") || strings.Contains(got, "ack --txn") {
				t.Errorf("status:\n%s", got)
			}
			g.started, g.asked = false, nil
			if !tc.reload {
				e.runner.fail["reload-units"] = 1
			}
			unguarded := 0
			e.runner.hook = func([]string) {
				if _, ok := e.inhibited(); !ok {
					unguarded++
				}
			}
			e.runner.reset()
			if code := e.recover(resumeOnly); code != ExitFailed {
				t.Fatalf("recover: exit %d\n%s", code, e.last)
			}
			e.runner.hook = nil
			if unguarded > 0 {
				t.Errorf("the resume ran %d command(s) without the inhibit", unguarded)
			}
			e.noJournal()
			if e.runner.called("reload-units") != 1 {
				t.Errorf("the resume did not qualify current again: %v", e.runner.calls)
			}
			if tc.reload {
				if strings.Join(g.asked, " ") != "refused" {
					t.Errorf("decisions while the probe ran: %v", g.asked)
				}
				if got := g.decide(); got != "accepted" || !g.started {
					t.Errorf("after the resume: %s, running %v", got, g.started)
				}
				return
			}
			if got := g.decide(); got != "refused" {
				t.Errorf("after a failed requalification a decision is %s", got)
			}
			if in, ok := e.inhibited(); !ok || !strings.Contains(in.Reason, "did not pass its reload, health check and no-AI check again") {
				t.Errorf("inhibit: %+v, %v", in, ok)
			}
		})
	}
}

// TestTheHeldJournalIsNoOutcome: after a crash between the two steps of a
// healthy end, decisions.inhibit holds the journal of a transaction still
// to settle, not an outcome the owner can take back: inhibit-clear and ack
// refuse it (the host is qualified again first), a dry run reconciles
// nothing and changes nothing, and status shows the transaction, the
// requalification still to run and no ack command. A held journal that
// does not match the host is written back and stays unresolved; setting
// it aside leaves decisions inhibited with a plain record, and no later
// command writes it back again.
func TestTheHeldJournalIsNoOutcome(t *testing.T) {
	clear := func(e *env, words string) int {
		e.t.Helper()
		code := e.switcher().InhibitClear(context.Background(), words)
		e.last = e.out.String()
		e.out.Reset()
		return code
	}
	held := func(t *testing.T) (*env, string) {
		e := activeOwn(t)
		e.keepOwed = true
		e.crashAt(CrashJournalMoved, false, rel3)
		e.noJournal()
		data, err := os.ReadFile(e.inhibitPath())
		if err != nil {
			t.Fatal(err)
		}
		var j journal
		if err := json.Unmarshal(data, &j); err != nil || j.Phase != PhaseDone || j.Result != ResultOK || j.ID != rel3 {
			t.Fatalf("the inhibit does not hold the journal: %v %+v", err, j)
		}
		e.runner.reset()
		return e, j.Txn
	}
	t.Run("refused by the owner's verbs", func(t *testing.T) {
		e, txn := held(t)
		before := digest(t, e.root)
		if code := clear(e, "looked at the host"); code != ExitFailed || !strings.Contains(e.last, "must be qualified again first") {
			t.Errorf("inhibit-clear: exit %d\n%s", code, e.last)
		}
		if code := e.ackTxns([]string{txn}, "saw it"); code != ExitFailed || !strings.Contains(e.last, "still to be settled") {
			t.Errorf("ack: exit %d\n%s", code, e.last)
		}
		if code := e.switchTo(rel3, func(c *Config) { c.Dry = true }); code != ExitFailed || !strings.Contains(e.last, "a dry run reconciles nothing") {
			t.Errorf("dry run: exit %d\n%s", code, e.last)
		}
		if digest(t, e.root) != before {
			t.Errorf("a refused verb or a dry run changed the state")
		}
		got := e.status()
		for _, want := range []string{"which holds its journal", "interrupted: switch to " + rel3 + " in phase done (transaction " + txn + ", its journal held by " + InhibitFile + ")", "again before decisions are no longer inhibited", "outcome owed, not final"} {
			if !strings.Contains(got, want) {
				t.Errorf("status lacks %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "ack --txn") {
			t.Errorf("status prints an ack command:\n%s", got)
		}
		if len(e.runner.calls) != 0 {
			t.Errorf("commands run: %v", e.runner.calls)
		}
		// Settled by the next command, the owner's verbs work again.
		if code := e.recover(resumeOnly); code != ExitFailed {
			t.Fatalf("recover: exit %d\n%s", code, e.last)
		}
		if _, ok := e.inhibited(); ok {
			t.Errorf("still inhibited after the settling")
		}
		if code := e.ackTxns([]string{txn}, "saw it"); code != ExitOK {
			t.Errorf("ack after the settling: exit %d\n%s", code, e.last)
		}
	})
	t.Run("stale, set aside", func(t *testing.T) {
		e, txn := held(t)
		g := guard(e)
		if err := os.Remove(filepath.Join(e.root, BaseDir, "previous")); err != nil {
			t.Fatal(err)
		}
		e.runner.reset()
		if code := e.recover(resumeOnly); code != ExitFailed || !strings.Contains(e.last, "does not match the host") {
			t.Fatalf("recover: exit %d\n%s", code, e.last)
		}
		if j, ok := e.journal(); !ok || j.Txn != txn {
			t.Fatalf("the held journal was not written back: %+v, %v", j, ok)
		}
		if code := e.recover(RecoverOptions{DiscardJournal: txn}); code != ExitOK {
			t.Fatalf("discard: exit %d\n%s", code, e.last)
		}
		e.noJournal()
		data, _ := os.ReadFile(e.inhibitPath())
		var in inhibit
		if err := decodeStrict(data, &in); err != nil || !strings.Contains(in.Reason, "set aside by the owner") {
			t.Errorf("the inhibit after the discard: %v, %s", err, data)
		}
		if g.decide() != "refused" {
			t.Errorf("a decision is accepted after the discard")
		}
		// The next command finds nothing to write back.
		if code := e.recover(resumeOnly); code != ExitFailed || strings.Contains(e.last, "reconcile:") {
			t.Errorf("recover after the discard: exit %d\n%s", code, e.last)
		}
		e.noJournal()
		if len(e.runner.calls) != 0 {
			t.Errorf("commands run: %v", e.runner.calls)
		}
	})
}
