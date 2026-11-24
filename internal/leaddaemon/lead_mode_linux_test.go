//go:build linux && ataniq_canary

package leaddaemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadcanary/provision"
	"ataniq/internal/leadcontrol"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadwire"
)

// notThere fails the test when any of the things a daemon makes after the
// config step is there: the registry, the command table, the evidence folder and
// the runtime root.
func (l *leadEnv) notMade() {
	l.t.Helper()
	for _, p := range []string{
		filepath.Join(l.state, RegistryDirName), filepath.Join(l.state, LeadCommandsName),
		filepath.Join(l.state, leadcanary.EvidenceDirName), l.runtime,
	} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			l.t.Fatalf("%s was made: %v", p, err)
		}
	}
}

// hello lists the base operations, the six lead operations and the two chat
// operations of the desk, with mutations true, and not one of the canary's.
// Canary mode lists the canary's five and none of the lead's.
func TestLeadHelloListsTheSixWithMutations(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	ctx := t.Context()
	cl, err := clientapi.Dial(ctx, l.running.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	h, err := cl.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantOps := append(append(clientapi.Operations(), leadwire.Operations()...), leadwire.ChatOperations()...)
	if strings.Join(h.Operations, " ") != strings.Join(wantOps, " ") || !h.Mutations {
		t.Fatalf("hello: operations %v mutations %v, want %v and true", h.Operations, h.Mutations, wantOps)
	}
	if got := strings.Join(leadwire.Operations(), " "); got != "lead.list lead.define lead.start lead.stop lead.ticket lead.inject" {
		t.Fatalf("the operations are %q", got)
	}
	// The canary's operations are not served in scratch mode: a role could be
	// started past the table, the cap and the epoch checks.
	err = cl.Call(ctx, OpCanaryStart, map[string]string{"role": roleOf("alpha")}, nil)
	wantCode(t, err, clientapi.CodeUnsupportedOperation)

	c := newCanaryEnv(t)
	c.start()
	cl2, err := clientapi.Dial(ctx, c.running.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer cl2.Close()
	h2, err := cl2.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range h2.Operations {
		if strings.HasPrefix(op, "lead.") {
			t.Fatalf("canary mode lists %s", op)
		}
	}
}

// Every refusal of the scratch config is made at the config step, before
// anything is made: no registry, no table, no evidence, no runtime root.
func TestScratchRefusalsAtTheConfigStep(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(l *leadEnv, cfg *Config)
		text   string
	}{
		"canary mode too": {func(l *leadEnv, cfg *Config) { cfg.Canary = &Canary{} }, "exclude each other"},
		"state root is not the config's": {func(l *leadEnv, cfg *Config) {
			cfg.StateDir = filepath.Join(l.base, "other-state")
		}, "the scratch config's roots"},
		"runtime root is not the config's": {func(l *leadEnv, cfg *Config) {
			cfg.RuntimeDir = filepath.Join(l.base, "other-run")
		}, "the scratch config's roots"},
		"config that is not valid": {func(l *leadEnv, cfg *Config) {
			sc := *cfg.Scratch
			sc.Config.MaxRunning = 0
			cfg.Scratch = &sc
		}, "max_running"},
		"no root marker": {func(l *leadEnv, cfg *Config) {
			if err := os.Remove(filepath.Join(l.state, canaryroot.ScratchMarkerName)); err != nil {
				l.t.Fatal(err)
			}
		}, "marker"},
		"the other kind's marker": {func(l *leadEnv, cfg *Config) {
			if err := os.Rename(filepath.Join(l.state, canaryroot.ScratchMarkerName), filepath.Join(l.state, canaryroot.MarkerName)); err != nil {
				l.t.Fatal(err)
			}
		}, "marker"},
	} {
		t.Run(name, func(t *testing.T) {
			l := newLeadEnv(t)
			cfg := l.daemonConfig()
			tc.change(l, &cfg)
			err := l.runToEnd(cfg)
			se := mustFail(t, err, StepConfig, ExitConfig)
			if !strings.Contains(se.Error(), tc.text) {
				t.Fatalf("the refusal %q does not say %q", se, tc.text)
			}
			l.notMade()
			l.lockFree()
		})
	}
}

// Each mode refuses the other's prefix: the canary's config cannot name a lead
// of scratch mode, and a scratch config derives scratch leads only.
func TestEachModeRefusesTheOthersPrefix(t *testing.T) {
	c := newCanaryEnv(t)
	c.cfg.Role = mustRole(t, "scratch-alpha/lead")
	err := c.runToEnd(c.daemonConfig())
	mustFail(t, err, StepConfig, ExitConfig)

	l := newLeadEnv(t)
	for _, cfg := range l.w.Leads(t) {
		if !leadrole.IsReserved(cfg.Role.Project(), leadrole.ReservedScratch) || leadrole.IsReserved(cfg.Role.Project(), leadrole.ReservedThrowaway) {
			t.Fatalf("a lead of scratch mode is %s", cfg.Role)
		}
	}
	// The scratch place's root check refuses a throwaway project.
	if err := canaryroot.Check(l.state, l.runtime, "throwaway-one", l.w.Getenv()); err == nil {
		t.Fatal("the scratch root passed the check for a throwaway project")
	}
}

// The start-up of scratch mode: the evidence of every lead, then the table in the
// state root, mode 0600, with the daemon-start line of each lead; the table is
// closed after the server ended.
func TestScratchStartUpMakesTheEvidenceAndTheTable(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	if got := l.order.get(); len(got) < 5 || got[0] != StepConfig || got[1] != StepState {
		t.Fatalf("steps %v", got)
	}
	fi, err := os.Lstat(filepath.Join(l.state, LeadCommandsName))
	if err != nil || fi.Mode() != 0o600 {
		t.Fatalf("the table: %v %v", fi, err)
	}
	for _, key := range []string{"alpha", "beta"} {
		if got := strings.Join(l.evidenceOf(key), " "); got != "daemon-start" {
			t.Fatalf("evidence of %s: %s", key, got)
		}
	}
	// A second daemon on the same state is refused by the lock before it opens
	// the table; the first holds the table.
	if _, err := leadcontrol.Open(filepath.Join(l.state, LeadCommandsName), leadcontrol.Options{}); !errors.Is(err, leadcontrol.ErrLocked) {
		t.Fatalf("the running daemon does not hold the table: %v", err)
	}
	if err := l.running.stop(); err != nil {
		t.Fatal(err)
	}
	tbl, err := leadcontrol.Open(filepath.Join(l.state, LeadCommandsName), leadcontrol.Options{})
	if err != nil {
		t.Fatalf("the table is not closed after a clean shutdown: %v", err)
	}
	tbl.Close()
}

// A command table that is corrupt, or locked by another process, refuses the
// start at the state step with a text that says to stop every scratch unit
// first, after the locks and before the registry.
func TestScratchRefusesACorruptOrLockedTable(t *testing.T) {
	for name, setup := range map[string]func(l *leadEnv){
		"corrupt": func(l *leadEnv) {
			if err := os.WriteFile(filepath.Join(l.state, LeadCommandsName), []byte("garbage\n"), 0o600); err != nil {
				l.t.Fatal(err)
			}
		},
		"locked": func(l *leadEnv) {
			tbl, err := leadcontrol.Open(filepath.Join(l.state, LeadCommandsName), leadcontrol.Options{})
			if err != nil {
				l.t.Fatal(err)
			}
			l.t.Cleanup(func() { tbl.Close() })
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := newLeadEnv(t)
			setup(l)
			err := l.runToEnd(l.daemonConfig())
			se := mustFail(t, err, StepState, ExitState)
			if !strings.Contains(se.Error(), "every scratch unit is stopped") {
				t.Fatalf("the refusal does not say to stop the units first: %q", se)
			}
			if _, err := os.Lstat(filepath.Join(l.state, RegistryDirName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("the registry was opened: %v", err)
			}
			l.lockFree()
			l.noSocketMade()
		})
	}
}

// A record that is not what provisioning writes, or whose digest is not the
// lead's pin, refuses the start at the state step, and nothing is opened after
// it. A lead with a record gets its holder command from it; a lead without one
// has none.
func TestScratchReadsTheRecordsAtStart(t *testing.T) {
	recordPath := func(l *leadEnv, key string) string {
		cfg := l.cfgOf(key)
		layout, _, err := leadcanary.NamesFor(leadrole.ReservedScratch, cfg.Role)
		if err != nil {
			t.Fatal(err)
		}
		return filepath.Join(l.state, leadcanary.ScratchDirName, layout, provision.LeadRecordName)
	}
	t.Run("a record that is not valid", func(t *testing.T) {
		l := newLeadEnv(t)
		l.provision("alpha")
		if err := os.WriteFile(recordPath(l, "alpha"), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		se := mustFail(t, l.runToEnd(l.daemonConfig()), StepState, ExitState)
		if !strings.Contains(se.Error(), "alpha") {
			t.Fatalf("the refusal does not name the lead: %q", se)
		}
		if _, err := os.Lstat(filepath.Join(l.state, LeadCommandsName)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the table was opened: %v", err)
		}
		l.lockFree()
	})
	t.Run("a digest that is not the pin", func(t *testing.T) {
		l := newLeadEnv(t)
		l.provision("alpha")
		l.w.Config.Entries[0].ProfileDigest = strings.Repeat("ab", 32)
		l.resetScratch()
		se := mustFail(t, l.runToEnd(l.daemonConfig()), StepState, ExitState)
		if !strings.Contains(se.Error(), "pinned") {
			t.Fatalf("the refusal does not say why: %q", se)
		}
		l.lockFree()
	})
	t.Run("the pin that matches", func(t *testing.T) {
		l := newLeadEnv(t)
		res := l.provision("alpha")
		l.w.Config.Entries[0].ProfileDigest = res.ProfileDigest
		l.resetScratch()
		l.start()
		l.define("alpha")
		l.holder("alpha", submitted)
		want(t, l.startLead("alpha"), leadwire.StatusDone, "")
	})
	t.Run("a lead without a record has no holder command", func(t *testing.T) {
		l := newLeadEnv(t)
		l.provision("alpha")
		l.start()
		l.holder("alpha", submitted)
		want(t, l.define("alpha"), leadwire.StatusDone, "")
		want(t, l.startLead("beta"), leadwire.StatusRefused, "not-provisioned")
		if got := l.gate.starts.Load(); got != 0 {
			t.Fatalf("%d Start calls for a lead that is not provisioned", got)
		}
	})
}

// A server that does not end within the grace leaves the daemon at exit 19: the
// command table is then left open, with the registry and the locks, and is not
// closed under the handler that may still use it.
func TestScratchLeavesTheTableOpenAfterExit19(t *testing.T) {
	l := newLeadEnv(t)
	cfg := l.daemonConfig()
	cfg.ShutdownGrace = 200 * time.Millisecond
	l.running = l.startDaemon(cfg)
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	l.holder("alpha", submitted)

	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { leadActHook = nil })
	leadActHook = func(op string) {
		if op == "start" {
			close(entered)
			<-release // a command that does not end on the context
		}
	}
	t.Cleanup(func() { close(release) })
	go func() {
		var r leadwire.Receipt
		_ = l.call(leadwire.OpStart, params("alpha", "stuck-1", nil), &r)
	}()
	<-entered
	mustFail(t, l.running.stop(), StepShutdown, ExitShutdown)
	if _, err := leadcontrol.Open(filepath.Join(l.state, LeadCommandsName), leadcontrol.Options{}); !errors.Is(err, leadcontrol.ErrLocked) {
		t.Fatalf("the table was closed after exit 19: %v", err)
	}
}
