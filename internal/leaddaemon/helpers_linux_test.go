package leaddaemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadlock"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

var t0 = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

// syncBuffer is a log writer that tests read while the daemon writes.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Reset forgets what was written.
func (s *syncBuffer) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.b.Reset()
}

// env is the folders, the fake and the config of one test daemon.
type env struct {
	t       *testing.T
	base    string
	state   string // not created: Run makes it
	runtime string // not created: Listen makes it
	clock   *leadunit.FakeClock
	fake    *leadunit.Fake
	signals chan os.Signal
	log     *syncBuffer
	order   *orderLog
}

type orderLog struct {
	mu    sync.Mutex
	steps []Step
}

func (o *orderLog) add(s Step) {
	o.mu.Lock()
	o.steps = append(o.steps, s)
	o.mu.Unlock()
}

func (o *orderLog) get() []Step {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]Step(nil), o.steps...)
}

// newEnv makes the folders under a short path (a Unix socket address is
// short) and a fake manager on a fake clock.
func newEnv(t *testing.T) *env {
	t.Helper()
	base, err := os.MkdirTemp("", "ad")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	clock := leadunit.NewFakeClock(t0)
	return &env{
		t: t, base: base,
		state:   filepath.Join(base, "state"),
		runtime: filepath.Join(base, "run"),
		clock:   clock, fake: leadunit.NewFake(clock),
		signals: make(chan os.Signal, 4), log: &syncBuffer{}, order: &orderLog{},
	}
}

// config is the config the tests start from.
func (e *env) config() Config {
	return Config{
		StateDir: e.state, RuntimeDir: e.runtime,
		NewManager: func(context.Context) (leadunit.Manager, error) { return e.fake, nil },
		Clock:      e.clock,
		Signals:    e.signals,
		EUID:       func() int { return 1000 },
		BootID:     func() (string, error) { return e.fake.BootID(), nil },
		Version:    "9.9.9-test",
		Log:        e.log,
		Observe:    e.order.add,
	}
}

// socket is the socket path of the runtime folder.
func (e *env) socket() string { return clientapi.SocketPath(e.runtime) }

// prepare opens the registry of the state folder, creating the folder as the
// daemon would, runs fn and closes it again.
func (e *env) prepare(fn func(reg *leadregistry.Registry)) {
	e.t.Helper()
	if err := os.MkdirAll(e.state, 0o700); err != nil {
		e.t.Fatal(err)
	}
	reg, err := leadregistry.Open(filepath.Join(e.state, RegistryDirName), leadregistry.Options{Now: e.clock.Now})
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() {
		if err := reg.Close(); err != nil {
			e.t.Fatal(err)
		}
	}()
	fn(reg)
}

// running is a daemon started in this process.
type running struct {
	e      *env
	cancel context.CancelFunc
	ended  chan struct{}
	err    error
	socket string
}

// startDaemon runs the daemon with cfg and waits until it listens.
func (e *env) startDaemon(cfg Config) *running {
	e.t.Helper()
	ready := make(chan string, 1)
	cfg.Ready = func(s string) { ready <- s }
	ctx, cancel := context.WithCancel(context.Background())
	r := &running{e: e, cancel: cancel, ended: make(chan struct{})}
	go func() {
		r.err = Run(ctx, cfg)
		close(r.ended)
	}()
	select {
	case r.socket = <-ready:
	case <-r.ended:
		cancel()
		e.t.Fatalf("the daemon ended before it listened: %v\nlog:\n%s", r.err, e.log)
	case <-time.After(20 * time.Second):
		cancel()
		e.t.Fatalf("the daemon did not listen in 20 s\nlog:\n%s", e.log)
	}
	e.t.Cleanup(func() {
		cancel()
		select {
		case <-r.ended:
		case <-time.After(20 * time.Second):
		}
	})
	return r
}

// stop sends SIGTERM through the signal source and returns Run's result.
func (r *running) stop() error {
	r.e.t.Helper()
	r.e.signals <- syscall.SIGTERM
	return r.wait()
}

func (r *running) wait() error {
	r.e.t.Helper()
	select {
	case <-r.ended:
		return r.err
	case <-time.After(20 * time.Second):
		r.e.t.Fatal("the daemon did not stop in 20 s")
		return nil
	}
}

// runToEnd runs the daemon, which must end by itself, and returns its error.
func (e *env) runToEnd(cfg Config) error {
	e.t.Helper()
	done := make(chan error, 1)
	go func() { done <- Run(context.Background(), cfg) }()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		e.t.Fatal("the daemon did not end in 20 s")
		return nil
	}
}

// noSocketMade fails the test when a socket was bound or removed in the
// runtime folder, or when the socket lock cannot be taken. The folder and the
// lock file may be there: the daemon makes them before it opens the registry.
func (e *env) noSocketMade() {
	e.t.Helper()
	if _, err := os.Lstat(e.socket()); err == nil || !errors.Is(err, os.ErrNotExist) {
		e.t.Fatalf("a socket was made: %v", err)
	}
	l, err := leadlock.Acquire(SocketLockPath(e.runtime))
	if err != nil {
		e.t.Fatalf("the socket lock is not free: %v", err)
	}
	if err := l.Release(); err != nil {
		e.t.Fatal(err)
	}
}

// lockFree fails the test unless the daemon lock can be taken, and releases it.
func (e *env) lockFree() {
	e.t.Helper()
	l, err := leadlock.Acquire(leadlock.DaemonPath(e.state))
	if err != nil {
		e.t.Fatalf("the daemon lock is not free: %v", err)
	}
	if err := l.Release(); err != nil {
		e.t.Fatal(err)
	}
}

// mustFail checks that err is a StartupError of step with the exit code.
func mustFail(t *testing.T, err error, step Step, code int) *StartupError {
	t.Helper()
	var se *StartupError
	if !errors.As(err, &se) {
		t.Fatalf("error %v is not a StartupError", err)
	}
	if se.Step != step || ExitCode(err) != code {
		t.Fatalf("failed at %q with exit code %d (%v), want %q and %d", se.Step, ExitCode(err), err, step, code)
	}
	return se
}

func lead(t *testing.T, project string) leadrole.Role {
	t.Helper()
	r, err := leadrole.Lead(project)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func coordinator(t *testing.T, project, feature string) leadrole.Role {
	t.Helper()
	r, err := leadrole.Coordinator(project, feature)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func profile() leadregistry.Requested {
	return leadregistry.Requested{
		Provider: leadregistry.ProviderClaude, Version: "2.1.0", Model: "model-requested",
		Effort: "high", PermissionMode: "default", AccessMode: "workspace",
	}
}

// launch brings a role up in the registry and in the fake as a daemon of an
// earlier life would have: an intent, a unit, the outcome and the shown state.
func (e *env) launch(reg *leadregistry.Registry, role leadrole.Role, shown leadregistry.State) leadregistry.Entry {
	e.t.Helper()
	in, err := reg.BeginLaunchWith(role, leadregistry.LaunchOptions{BootIdentity: e.fake.BootID()})
	if err != nil {
		e.t.Fatal(err)
	}
	spec := leadunit.Spec{Unit: in.UnitName, Epoch: in.Epoch, Intent: in.ID, Argv: []string{"/bin/true"}, WorkDir: "/"}
	if role.Kind() == leadrole.KindCoordinator {
		spec.Parent = role.Parent().UnitName()
	}
	st, err := e.fake.Start(context.Background(), spec)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := reg.RecordOutcome(role, in.ID, in.Epoch, leadregistry.OutcomeLaunched, leadregistry.UnitEvidence{
		InvocationID: st.InvocationID, ControlGroupPath: st.ControlGroup, ControlGroupID: st.ControlGroupID, BootID: st.BootID,
	}); err != nil {
		e.t.Fatal(err)
	}
	if shown != leadregistry.StateStarting {
		if _, err := reg.SetState(role, leadregistry.StateChange{Epoch: in.Epoch, State: shown,
			Evidence: leadregistry.EvidenceObservation, Reason: leadregistry.Reason{Code: "test-setup", Detail: "set by the test"}}); err != nil {
			e.t.Fatal(err)
		}
	}
	got, err := reg.Get(role)
	if err != nil {
		e.t.Fatal(err)
	}
	return got
}

// noStartNoSignal fails the test when the fake's call log, from index from on,
// holds a Start or a Signal.
func noStartNoSignal(t *testing.T, f *leadunit.Fake, from int) {
	t.Helper()
	calls := f.Calls()
	for _, c := range calls[from:] {
		if c.Method == leadunit.MethodStart || c.Method == leadunit.MethodSignal {
			t.Fatalf("the daemon made a %s call for %s", c.Method, c.Unit)
		}
	}
}
