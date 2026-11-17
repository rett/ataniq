package leaddaemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// status dials the daemon's socket and asks for the status.
func status(t *testing.T, socket string) clientapi.StatusResult {
	t.Helper()
	c, err := clientapi.Dial(context.Background(), socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	res, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func find(t *testing.T, res clientapi.StatusResult, role string) clientapi.LeadStatus {
	t.Helper()
	for _, l := range res.Leads {
		if l.Role == role {
			return l
		}
	}
	t.Fatalf("no %q in the status: %+v", role, res.Leads)
	return clientapi.LeadStatus{}
}

// registryOfEveryKind prepares a registry with a lead (ready, one observed
// field), the supervisor (never launched), a coordinator under the lead, a
// lead shown unknown and a lead the tmux-manager runs.
func registryOfEveryKind(t *testing.T, e *env) {
	t.Helper()
	e.prepare(func(reg *leadregistry.Registry) {
		alpha := lead(t, "alpha")
		if _, err := reg.Define(alpha, leadregistry.ManagerAtaniq, profile()); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.SetDesired(alpha, leadregistry.DesiredRunning); err != nil {
			t.Fatal(err)
		}
		e.launch(reg, alpha, leadregistry.StateReady)
		if _, err := reg.SetObserved(alpha, 1, leadregistry.Observed{Model: leadregistry.Seen("model-seen")}); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.Define(leadrole.Supervisor(), leadregistry.ManagerAtaniq, profile()); err != nil {
			t.Fatal(err)
		}
		feat := coordinator(t, "alpha", "feat")
		if _, err := reg.Define(feat, leadregistry.ManagerAtaniq, leadregistry.Requested{}); err != nil {
			t.Fatal(err)
		}
		e.launch(reg, feat, leadregistry.StateReady)
		gamma := lead(t, "gamma")
		if _, err := reg.Define(gamma, leadregistry.ManagerAtaniq, profile()); err != nil {
			t.Fatal(err)
		}
		e.launch(reg, gamma, leadregistry.StateUnknown)
		if _, err := reg.Define(lead(t, "delta"), leadregistry.ManagerTmux, profile()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestStatusOverTheSocket(t *testing.T) {
	e := newEnv(t)
	registryOfEveryKind(t, e)
	calls := len(e.fake.Calls())
	d := e.startDaemon(e.config())

	c, err := clientapi.Dial(context.Background(), d.socket)
	if err != nil {
		t.Fatal(err)
	}
	hello, err := c.Hello(context.Background())
	if err != nil || hello.Version != "9.9.9-test" || hello.Mutations {
		t.Fatalf("hello %+v, %v", hello, err)
	}
	c.Close()

	res := status(t, d.socket)
	if len(res.Leads) != 5 {
		t.Fatalf("%d entries: %+v", len(res.Leads), res.Leads)
	}

	// The daemon block.
	dm := res.Daemon
	if dm.PID != os.Getpid() || !dm.StartedAt.Equal(t0) || dm.BootID != e.fake.BootID() || !dm.LastReconcileCompleted ||
		dm.LastReconcileAt == nil || !dm.LastReconcileAt.Equal(t0) || dm.RegistryRevision == 0 {
		t.Fatalf("daemon block %+v", dm)
	}

	// A lead: requested and observed stay apart.
	a := find(t, res, "alpha/lead")
	if a.Kind != clientapi.KindLead || a.Project != "alpha" || a.Manager != "ataniq" || a.Epoch != 1 || a.Desired != "running" ||
		a.State != clientapi.StateReady || a.ReasonCode != "test-setup" || a.ReasonDetail != "set by the test" ||
		a.EvidenceKind != "ataniq-observation" {
		t.Fatalf("lead %+v", a)
	}
	if a.Requested.Model != "model-requested" || a.Requested.Provider != "claude" || a.Requested.Effort != "high" {
		t.Fatalf("requested %+v", a.Requested)
	}
	if a.Observed.Model != clientapi.Observe("model-seen") {
		t.Fatalf("observed model %+v", a.Observed.Model)
	}
	for name, o := range map[string]clientapi.Observed{"provider": a.Observed.Provider, "version": a.Observed.Version,
		"effort": a.Observed.Effort, "permission mode": a.Observed.PermissionMode, "access mode": a.Observed.AccessMode} {
		if o.Observed || o.Value != "" {
			t.Fatalf("observed %s is %+v: an unobserved field must not take the requested value", name, o)
		}
	}
	if a.LaunchIntent == nil || a.LaunchIntent.Epoch != 1 || a.LaunchIntent.Unit != "ataniq-lead-alpha.service" ||
		a.LaunchIntent.Outcome != "launched" || len(a.LaunchIntent.ID) != 32 {
		t.Fatalf("launch intent %+v", a.LaunchIntent)
	}
	if a.Feature != "" || a.Parent != "" || a.ParentEpoch != nil || a.Instance != nil || a.Identity != "" {
		t.Fatalf("a lead carries coordinator fields: %+v", a)
	}

	// The supervisor, never launched.
	s := find(t, res, "supervisor")
	if s.Kind != clientapi.KindSupervisor || s.Project != "" || s.State != clientapi.StateStopped || s.ReasonCode != "defined" || s.Epoch != 0 || s.LaunchIntent != nil {
		t.Fatalf("supervisor %+v", s)
	}

	// A coordinator.
	cr := find(t, res, "alpha/coord/feat")
	if cr.Kind != clientapi.KindCoordinator || cr.Feature != "feat" || cr.Parent != "alpha/lead" || cr.Project != "alpha" ||
		cr.Instance == nil || *cr.Instance != 1 || cr.ParentEpoch == nil || *cr.ParentEpoch != 1 ||
		cr.Identity != "alpha-lead@e1:coord:feat:1" || cr.State != clientapi.StateReady || cr.Epoch != 1 {
		t.Fatalf("coordinator %+v", cr)
	}
	if cr.Requested.Provider != "claude" || cr.Requested.Model != "fable" {
		t.Fatalf("coordinator profile %+v", cr.Requested)
	}

	// A role the registry shows unknown stays unknown, with its reason.
	g := find(t, res, "gamma/lead")
	if g.State != clientapi.StateUnknown || g.ReasonCode == "" {
		t.Fatalf("unknown role %+v", g)
	}
	// A role of the tmux-manager is shown as it is.
	if dl := find(t, res, "delta/lead"); dl.Manager != "tmux-manager" || dl.State != clientapi.StateStopped {
		t.Fatalf("tmux role %+v", dl)
	}

	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
	noStartNoSignal(t, e.fake, calls)
	e.prepare(func(reg *leadregistry.Registry) {
		if rev, _ := reg.Revision(); rev != dm.RegistryRevision {
			t.Fatalf("the daemon showed revision %d and the registry is at %d", dm.RegistryRevision, rev)
		}
	})
}

// TestUnavailableManagerWithNothingToInspect: a manager known to be
// unavailable shows the reconcile as not completed also when the registry
// holds nothing the reconcile asks the manager about.
func TestUnavailableManagerWithNothingToInspect(t *testing.T) {
	for _, tt := range []struct {
		name        string
		prep        func(t *testing.T, e *env)
		unavailable bool
		completed   bool
	}{
		{"empty registry", func(t *testing.T, e *env) {}, true, false},
		{"tmux roles only", func(t *testing.T, e *env) {
			e.prepare(func(reg *leadregistry.Registry) {
				if _, err := reg.Define(lead(t, "delta"), leadregistry.ManagerTmux, profile()); err != nil {
					t.Fatal(err)
				}
			})
		}, true, false},
		// The same registries with a manager that answers: completed.
		{"empty registry, manager answers", func(t *testing.T, e *env) {}, false, true},
		{"tmux roles only, manager answers", func(t *testing.T, e *env) {
			e.prepare(func(reg *leadregistry.Registry) {
				if _, err := reg.Define(lead(t, "delta"), leadregistry.ManagerTmux, profile()); err != nil {
					t.Fatal(err)
				}
			})
		}, false, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			tt.prep(t, e)
			cfg := e.config()
			if tt.unavailable {
				cfg.NewManager = func(context.Context) (leadunit.Manager, error) {
					return UnavailableManager(errors.New("no systemctl")), nil
				}
			}
			calls := len(e.fake.Calls())
			d := e.startDaemon(cfg)
			res := status(t, d.socket)
			if res.Daemon.LastReconcileCompleted != tt.completed {
				t.Fatalf("LastReconcileCompleted=%v, want %v", res.Daemon.LastReconcileCompleted, tt.completed)
			}
			if res.Daemon.LastReconcileAt == nil {
				t.Fatal("no reconcile time")
			}
			if got := strings.Contains(e.log.String(), "the unit manager is unavailable (no systemctl)"); got != tt.unavailable {
				t.Fatalf("log mentions the unavailable manager: %v\n%s", got, e.log)
			}
			if err := d.stop(); err != nil {
				t.Fatal(err)
			}
			noStartNoSignal(t, e.fake, calls)
		})
	}
}

func TestStatusWhileSystemdIsUnavailable(t *testing.T) {
	for _, tt := range []struct {
		name string
		prep func(e *env, cfg *Config)
	}{
		{"manager that answers nothing", func(e *env, cfg *Config) {
			e.fake.MakeUnavailable(leadunit.MethodInspect, 100)
		}},
		{"manager that could not be built", func(e *env, cfg *Config) {
			cfg.NewManager = func(context.Context) (leadunit.Manager, error) {
				return UnavailableManager(errors.New("no systemctl")), nil
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			registryOfEveryKind(t, e)
			cfg := e.config()
			tt.prep(e, &cfg)
			calls := len(e.fake.Calls())
			d := e.startDaemon(cfg)
			res := status(t, d.socket)
			if res.Daemon.LastReconcileCompleted {
				t.Fatal("the reconcile is shown as completed")
			}
			if res.Daemon.LastReconcileAt == nil {
				t.Fatal("no reconcile time")
			}
			// The roles are as the registry has them: nothing became unknown
			// for want of an answer.
			if a := find(t, res, "alpha/lead"); a.State != clientapi.StateReady || a.ReasonCode != "test-setup" {
				t.Fatalf("lead %+v", a)
			}
			if c := find(t, res, "alpha/coord/feat"); c.State != clientapi.StateReady {
				t.Fatalf("coordinator %+v", c)
			}
			if !strings.Contains(e.log.String(), "did not answer") {
				t.Fatalf("log:\n%s", e.log)
			}
			if err := d.stop(); err != nil {
				t.Fatal(err)
			}
			noStartNoSignal(t, e.fake, calls)
		})
	}
}

func TestRebootShowsStoppedAndStartsNothing(t *testing.T) {
	e := newEnv(t)
	registryOfEveryKind(t, e)
	e.fake.Reboot()
	calls := len(e.fake.Calls())
	d := e.startDaemon(e.config())
	res := status(t, d.socket)
	if !res.Daemon.LastReconcileCompleted || res.Daemon.BootID != e.fake.BootID() {
		t.Fatalf("daemon block %+v", res.Daemon)
	}
	for _, role := range []string{"alpha/lead", "alpha/coord/feat"} {
		l := find(t, res, role)
		if l.State != clientapi.StateStopped || l.ReasonCode != "host-rebooted" || l.Epoch != 1 {
			t.Fatalf("%s: %+v", role, l)
		}
	}
	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
	// Not a Start and not a Signal in the daemon's whole life, and nothing
	// loaded afterwards.
	noStartNoSignal(t, e.fake, calls)
	for _, u := range []string{"ataniq-lead-alpha.service", "ataniq-coord-alpha_feat.service", "ataniq-lead-gamma.service"} {
		if e.fake.Loaded(u) {
			t.Fatalf("%s is loaded", u)
		}
	}
}

// tearJournal appends an unfinished line to the registry's journal, as an
// append that never finished would leave it.
func tearJournal(t *testing.T, e *env) {
	t.Helper()
	path := filepath.Join(e.state, RegistryDirName, leadregistry.FileName)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":`); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// cutUnacknowledged reports whether the registry of the state folder, opened
// afresh, still holds a cut that nobody acknowledged.
func cutUnacknowledged(e *env) (cut bool) {
	e.prepare(func(reg *leadregistry.Registry) { cut = reg.Recovery().Cut() })
	return cut
}

// TestARecoveryActedOnIsAcknowledged: the reconcile acts on every role of a
// registry that was cut and acknowledges the cut, so the daemon shows the
// reconcile as completed, the roles stay unknown with the reason
// registry-recovered, and a later start finds no cut.
func TestARecoveryActedOnIsAcknowledged(t *testing.T) {
	e := newEnv(t)
	registryOfEveryKind(t, e)
	tearJournal(t, e)
	calls := len(e.fake.Calls())
	d := e.startDaemon(e.config())
	res := status(t, d.socket)
	if !res.Daemon.LastReconcileCompleted {
		t.Fatalf("a cut acted on for every role is shown as not completed\nlog:\n%s", e.log)
	}
	if a := find(t, res, "alpha/lead"); a.State != clientapi.StateUnknown || a.ReasonCode != "registry-recovered" {
		t.Fatalf("lead %+v", a)
	}
	if strings.Contains(e.log.String(), "not acknowledged") {
		t.Fatalf("log:\n%s", e.log)
	}
	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
	noStartNoSignal(t, e.fake, calls)
	if cutUnacknowledged(e) {
		t.Fatal("the cut is still unacknowledged after a reconcile that acted on every role")
	}
}

// TestACutIsNotAcknowledgedWhenTheManagerDoesNotAnswer: when the manager does
// not answer for a role, the reconcile has not acted on it. The cut stays
// unacknowledged and the reconcile is shown as not completed; the next daemon
// start that can act acknowledges it.
func TestACutIsNotAcknowledgedWhenTheManagerDoesNotAnswer(t *testing.T) {
	e := newEnv(t)
	registryOfEveryKind(t, e)
	tearJournal(t, e)
	e.fake.MakeUnavailable(leadunit.MethodInspect, 1000)
	calls := len(e.fake.Calls())
	d := e.startDaemon(e.config())
	res := status(t, d.socket)
	if res.Daemon.LastReconcileCompleted {
		t.Fatal("the reconcile is shown as completed although the manager did not answer for a role")
	}
	if !strings.Contains(e.log.String(), "not acknowledged") {
		t.Fatalf("log:\n%s", e.log)
	}
	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
	if !cutUnacknowledged(e) {
		t.Fatal("the cut was acknowledged without the manager having answered")
	}

	// The next start, with a manager that answers, acts and acknowledges.
	e.fake.MakeUnavailable(leadunit.MethodInspect, 0)
	e.log.Reset()
	d = e.startDaemon(e.config())
	res = status(t, d.socket)
	if !res.Daemon.LastReconcileCompleted {
		t.Fatalf("the reconcile of a manager that answers is shown as not completed\nlog:\n%s", e.log)
	}
	if a := find(t, res, "alpha/lead"); a.State != clientapi.StateUnknown || a.ReasonCode != "registry-recovered" {
		t.Fatalf("lead %+v", a)
	}
	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
	noStartNoSignal(t, e.fake, calls)
	if cutUnacknowledged(e) {
		t.Fatal("the cut is still unacknowledged after a daemon that could act")
	}
}

func TestCorruptRegistryIsLeftAsItIs(t *testing.T) {
	e := newEnv(t)
	registryOfEveryKind(t, e)
	path := filepath.Join(e.state, RegistryDirName, leadregistry.FileName)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0x55
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	mustFail(t, e.runToEnd(e.config()), StepRegistry, ExitRegistry)
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(b) {
		t.Fatalf("the corrupt registry was changed: %v", err)
	}
	if _, err := os.Lstat(e.runtime); err == nil {
		t.Fatal("a runtime folder was made")
	}
	e.lockFree()
}

func TestHandlerHonoursItsContext(t *testing.T) {
	e := newEnv(t)
	e.prepare(func(*leadregistry.Registry) {})
	reg, err := leadregistry.Open(filepath.Join(e.state, RegistryDirName), leadregistry.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	h := &handler{reg: reg, version: "v"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := h.Status(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Status with a cancelled context: %v", err)
	}
	if _, err := h.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentClientsAndShutdownWithAnOpenConnection(t *testing.T) {
	e := newEnv(t)
	registryOfEveryKind(t, e)
	cfg := e.config()
	cfg.ShutdownGrace = 10 * time.Second
	d := e.startDaemon(cfg)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if res := status(t, d.socket); len(res.Leads) != 5 {
					t.Errorf("%d entries", len(res.Leads))
					return
				}
			}
		}()
	}
	wg.Wait()
	// A client that stays connected does not hold the daemon up.
	idle, err := clientapi.Dial(context.Background(), d.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	if _, err := idle.Hello(context.Background()); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	if err := d.stop(); err != nil {
		t.Fatal(err)
	}
	if time.Since(began) > 5*time.Second {
		t.Fatalf("shutdown took %s with an idle client", time.Since(began))
	}
	e.lockFree()
}

// emptyOverride is a manager that answers like the fake except for Empty,
// which it hands to fn.
type emptyOverride struct {
	leadunit.Manager
	fn func(ctx context.Context, st leadunit.State) (bool, error)
}

func (m emptyOverride) Empty(ctx context.Context, st leadunit.State) (bool, error) {
	return m.fn(ctx, st)
}

// aReadyRoleWhoseUnitIsGone prepares a registry with one role shown ready
// whose unit crashed away, so that the reconcile has to prove its control
// group empty.
func aReadyRoleWhoseUnitIsGone(t *testing.T, e *env) {
	t.Helper()
	e.prepare(func(reg *leadregistry.Registry) {
		alpha := lead(t, "alpha")
		if _, err := reg.Define(alpha, leadregistry.ManagerAtaniq, profile()); err != nil {
			t.Fatal(err)
		}
		if _, err := reg.SetDesired(alpha, leadregistry.DesiredRunning); err != nil {
			t.Fatal(err)
		}
		e.launch(reg, alpha, leadregistry.StateReady)
	})
	if !e.fake.Crash("ataniq-lead-alpha.service") {
		t.Fatal("the unit was not running")
	}
}

func TestFailedEmptyShowsTheReconcileIncomplete(t *testing.T) {
	for _, tt := range []struct {
		name    string
		empty   func(context.Context, leadunit.State) (bool, error)
		timeout time.Duration
		log     string
		answers bool // the manager gave a defined answer, not a failure
	}{
		{name: "plain error", log: "1 unit manager call(s) failed",
			empty: func(context.Context, leadunit.State) (bool, error) {
				return false, errors.New("read cgroup.events: permission denied")
			}},
		{name: "deadline in the last role", timeout: 200 * time.Millisecond, log: "ran out of time",
			empty: func(ctx context.Context, _ leadunit.State) (bool, error) {
				<-ctx.Done()
				return false, ctx.Err()
			}},
		{name: "control group of another identity is an answer", answers: true,
			empty: func(context.Context, leadunit.State) (bool, error) {
				return false, leadunit.ErrControlGroupMismatch
			}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			aReadyRoleWhoseUnitIsGone(t, e)
			cfg := e.config()
			cfg.ReconcileTimeout = tt.timeout
			cfg.NewManager = func(context.Context) (leadunit.Manager, error) {
				return emptyOverride{Manager: e.fake, fn: tt.empty}, nil
			}
			calls := len(e.fake.Calls())
			d := e.startDaemon(cfg)
			res := status(t, d.socket)
			a := find(t, res, "alpha/lead")
			if a.State != clientapi.StateUnknown || a.ReasonCode != "control-group-not-proven-empty" {
				t.Fatalf("lead %+v", a)
			}
			if res.Daemon.LastReconcileCompleted == !tt.answers {
				t.Fatalf("LastReconcileCompleted = %v, the manager call %s", res.Daemon.LastReconcileCompleted,
					map[bool]string{true: "answered", false: "failed"}[tt.answers])
			}
			if tt.log != "" && !strings.Contains(e.log.String(), tt.log) {
				t.Fatalf("log has no %q:\n%s", tt.log, e.log)
			}
			if err := d.stop(); err != nil {
				t.Fatal(err)
			}
			noStartNoSignal(t, e.fake, calls)
		})
	}
}
