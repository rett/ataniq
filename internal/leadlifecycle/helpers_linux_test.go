package leadlifecycle

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

var t0 = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func mkRole(t testing.TB, project string) leadrole.Role {
	t.Helper()
	r, err := leadrole.Lead(project)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func profile() leadregistry.Requested {
	return leadregistry.Requested{
		Provider: leadregistry.ProviderClaude, Version: "2.1.0", Model: "model-a",
		Effort: "high", PermissionMode: "default", AccessMode: "workspace",
	}
}

// crashSignal is the panic that stands for the daemon dying.
type crashSignal struct{ label string }

// crashPlan numbers the points at which a daemon could die (before each
// registry write, before and after each unit call) and dies at one of them.
type crashPlan struct {
	mu     sync.Mutex
	armed  bool
	n      int
	at     int
	labels []string
}

func (p *crashPlan) point(label string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if !p.armed {
		p.mu.Unlock()
		return
	}
	i := p.n
	p.n++
	p.labels = append(p.labels, label)
	hit := p.at == i
	p.mu.Unlock()
	if hit {
		panic(crashSignal{label: label})
	}
}

// audit wraps a Manager. It forwards every call, offers the crash points,
// runs test hooks and records the violations of the contract's invariants.
type audit struct {
	inner leadunit.Manager
	plan  *crashPlan

	mu         sync.Mutex
	starts     map[string]int  // intent id -> Start calls
	proven     map[string]bool // unit -> an Empty true was seen since the last Start or Signal
	lastBoot   map[string]string
	violations []string

	beforeSignal func(st leadunit.State, sig syscall.Signal)
	afterSignal  func(st leadunit.State, sig syscall.Signal)
	beforeStart  func(spec leadunit.Spec)
	afterStart   func(spec leadunit.Spec)
}

func newAudit(inner leadunit.Manager, plan *crashPlan) *audit {
	return &audit{inner: inner, plan: plan, starts: map[string]int{}, proven: map[string]bool{}, lastBoot: map[string]string{}}
}

func (a *audit) violate(format string, args ...any) {
	a.mu.Lock()
	a.violations = append(a.violations, fmt.Sprintf(format, args...))
	a.mu.Unlock()
}

func (a *audit) Start(ctx context.Context, spec leadunit.Spec) (leadunit.State, error) {
	a.plan.point("before Start")
	a.mu.Lock()
	a.starts[spec.Intent]++
	if a.starts[spec.Intent] > 1 {
		a.violations = append(a.violations, "second Start for intent "+spec.Intent)
	}
	if !a.proven[spec.Unit] {
		a.violations = append(a.violations, "Start of "+spec.Unit+" without a proof that the previous control group is empty")
	}
	a.proven[spec.Unit] = false
	hook := a.beforeStart
	a.mu.Unlock()
	if hook != nil {
		hook(spec)
	}
	st, err := a.inner.Start(ctx, spec)
	if a.afterStart != nil {
		a.afterStart(spec)
	}
	a.plan.point("after Start")
	return st, err
}

func (a *audit) Inspect(ctx context.Context, unit string) (leadunit.State, error) {
	a.plan.point("before Inspect")
	st, err := a.inner.Inspect(ctx, unit)
	if err == nil {
		a.mu.Lock()
		if prev, ok := a.lastBoot[unit]; ok && prev != st.BootID && !st.Loaded {
			a.proven[unit] = true // another boot identity is a proof of its own
		}
		a.lastBoot[unit] = st.BootID
		a.mu.Unlock()
	}
	return st, err
}

func (a *audit) Signal(ctx context.Context, st leadunit.State, sig syscall.Signal) error {
	a.plan.point("before Signal")
	if !st.Ours {
		a.violate("Signal %v to a unit that is not ours: %+v", sig, st)
	}
	a.mu.Lock()
	a.proven[st.Unit] = false
	hook := a.beforeSignal
	a.mu.Unlock()
	if hook != nil {
		hook(st, sig)
	}
	err := a.inner.Signal(ctx, st, sig)
	if a.afterSignal != nil {
		a.afterSignal(st, sig)
	}
	a.plan.point("after Signal")
	return err
}

func (a *audit) Empty(ctx context.Context, st leadunit.State) (bool, error) {
	a.plan.point("before Empty")
	empty, err := a.inner.Empty(ctx, st)
	if empty && err == nil {
		a.mu.Lock()
		a.proven[st.Unit] = true
		a.mu.Unlock()
	}
	return empty, err
}

type fakePreflight struct {
	mu    sync.Mutex
	err   error
	calls int
}

func (p *fakePreflight) Check(ctx context.Context, role leadrole.Role, e leadregistry.Entry) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	return p.err
}

type fakeHolder struct {
	mu    sync.Mutex
	err   error
	calls int
	hook  func(role leadrole.Role, e leadregistry.Entry, in leadregistry.Intent)
	seen  []leadregistry.Intent
}

func (h *fakeHolder) Build(role leadrole.Role, e leadregistry.Entry, in leadregistry.Intent) ([]string, string, []string, error) {
	h.mu.Lock()
	h.calls++
	h.seen = append(h.seen, in)
	hook, err := h.hook, h.err
	h.mu.Unlock()
	if hook != nil {
		hook(role, e, in)
	}
	if err != nil {
		return nil, "", nil, err
	}
	return []string{"/usr/libexec/holder", "--role", role.String()}, "/work", []string{"HOLDER_EPOCH=" + fmt.Sprint(in.Epoch)}, nil
}

type fakeQuiesce struct {
	mu     sync.Mutex
	err    error
	epochs []uint64
	// hook, when set, runs for every call, after it is recorded and without
	// the mutex held, and its error is the call's result when q.err is nil.
	hook func(role leadrole.Role, epoch uint64) error
}

func (q *fakeQuiesce) Stop(ctx context.Context, role leadrole.Role, epoch uint64) error {
	q.mu.Lock()
	q.epochs = append(q.epochs, epoch)
	err, hook := q.err, q.hook
	q.mu.Unlock()
	if err == nil && hook != nil {
		err = hook(role, epoch)
	}
	return err
}

// rig is one registry, one fake manager on a fake clock and a controller.
type rig struct {
	t      *testing.T
	dir    string
	clock  *leadunit.FakeClock
	fake   *leadunit.Fake
	aud    *audit
	plan   *crashPlan
	reg    *leadregistry.Registry
	pre    *fakePreflight
	holder *fakeHolder
	quiet  *fakeQuiesce
	ctl    *Controller
	gen    int64
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, dir: filepath.Join(t.TempDir(), "registry"), plan: &crashPlan{at: -1},
		pre: &fakePreflight{}, holder: &fakeHolder{}, quiet: &fakeQuiesce{}}
	r.clock = leadunit.NewFakeClock(t0)
	r.fake = leadunit.NewFake(r.clock)
	r.aud = newAudit(r.fake, r.plan)
	r.open()
	t.Cleanup(func() { r.reg.Close() })
	return r
}

// open opens the registry and builds a controller on it. Called again after
// close, it is the daemon coming back.
func (r *rig) open() {
	r.t.Helper()
	r.gen++
	opts := leadregistry.Options{
		Now: func() time.Time {
			r.plan.point("registry write")
			return r.clock.Now()
		},
		Rand: rand.New(rand.NewSource(r.gen)),
	}
	reg, err := leadregistry.Open(r.dir, opts)
	if err != nil {
		r.t.Fatalf("open registry: %v", err)
	}
	r.reg = reg
	r.ctl, err = New(Options{
		Registry: reg, Manager: r.aud, Clock: r.clock, Preflight: r.pre, Holder: r.holder, Quiesce: r.quiet,
	})
	if err != nil {
		r.t.Fatal(err)
	}
}

// restart closes the registry and opens it again with a new controller, as a
// daemon that died and came back.
func (r *rig) restart() {
	r.t.Helper()
	if err := r.reg.Close(); err != nil {
		r.t.Fatal(err)
	}
	r.open()
}

func (r *rig) define(project string) leadrole.Role {
	r.t.Helper()
	role := mkRole(r.t, project)
	if _, err := r.reg.Define(role, leadregistry.ManagerAtaniq, profile()); err != nil {
		r.t.Fatal(err)
	}
	return role
}

func (r *rig) defineTmux(project string) leadrole.Role {
	r.t.Helper()
	role := mkRole(r.t, project)
	if _, err := r.reg.Define(role, leadregistry.ManagerTmux, profile()); err != nil {
		r.t.Fatal(err)
	}
	return role
}

// drive runs fn in a goroutine and moves the fake clock one poll interval at a
// time whenever fn sleeps on it, until fn returns. It reports the crash point
// when fn was killed by the crash plan.
func (r *rig) drive(fn func()) *crashSignal {
	r.t.Helper()
	done := make(chan *crashSignal, 1)
	go func() {
		defer func() {
			if x := recover(); x != nil {
				if cs, ok := x.(crashSignal); ok {
					done <- &cs
					return
				}
				panic(x)
			}
			done <- nil
		}()
		fn()
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
		err := r.clock.BlockUntil(ctx, 1)
		cancel()
		select {
		case cs := <-done:
			return cs
		default:
		}
		if err == nil {
			r.clock.Advance(DefaultPollInterval)
		}
		if time.Now().After(deadline) {
			r.t.Fatal("operation did not finish in 30 s of real time")
		}
	}
}

func (r *rig) start(role leadrole.Role) (leadregistry.Entry, error) {
	r.t.Helper()
	var e leadregistry.Entry
	var err error
	r.drive(func() { e, err = r.ctl.Start(context.Background(), role) })
	return e, err
}

func (r *rig) get(role leadrole.Role) leadregistry.Entry {
	r.t.Helper()
	e, err := r.reg.Get(role)
	if err != nil {
		r.t.Fatal(err)
	}
	return e
}

func (r *rig) revision() uint64 {
	r.t.Helper()
	n, err := r.reg.Revision()
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// mustStart starts a role and requires the launched outcome.
func (r *rig) mustStart(role leadrole.Role) leadregistry.Entry {
	r.t.Helper()
	e, err := r.start(role)
	if err != nil {
		r.t.Fatalf("Start %s: %v", role, err)
	}
	if e.Intent == nil || e.Intent.Outcome != leadregistry.OutcomeLaunched || e.Shown.State != leadregistry.StateStarting {
		r.t.Fatalf("Start %s left %+v", role, e)
	}
	return e
}

// advance moves the fake clock without anything waiting on it.
func (r *rig) advance(d time.Duration) { r.clock.Advance(d) }

func (r *rig) callsOf(m leadunit.Method) int { return len(r.fake.CallsOf(m)) }

// auditClean fails the test for a violation the audit wrapper saw.
func (r *rig) auditClean() {
	r.t.Helper()
	r.aud.mu.Lock()
	defer r.aud.mu.Unlock()
	for _, v := range r.aud.violations {
		r.t.Errorf("violation: %s", v)
	}
}

func (r *rig) expectState(role leadrole.Role, want leadregistry.State, code string) leadregistry.Entry {
	r.t.Helper()
	e := r.get(role)
	if e.Shown.State != want || (code != "" && e.Shown.Reason.Code != code) {
		r.t.Fatalf("%s: shown %s/%q (%s), want %s/%q", role, e.Shown.State, e.Shown.Reason.Code, e.Shown.Reason.Detail, want, code)
	}
	if (want == leadregistry.StateStopped || want == leadregistry.StateEnded) && e.Shown.Proof == "" {
		r.t.Fatalf("%s: %s without a proof", role, want)
	}
	return e
}

func isErr(t testing.TB, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error %v does not match %v", err, want)
	}
}

func noErr(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// signalCalls returns the Signal entries of the call log.
func (r *rig) signalCalls() []leadunit.Call { return r.fake.CallsOf(leadunit.MethodSignal) }

func (r *rig) noSignals() {
	r.t.Helper()
	if n := len(r.signalCalls()); n != 0 {
		r.t.Fatalf("%d Signal calls, want none: %+v", n, r.signalCalls())
	}
}

// pastTheWindow is a step of the fake clock that takes a launch intent just
// stamped to after the end of the late-start window and its margin.
const pastTheWindow = DefaultLateStartWindow + DefaultLateStartMargin + time.Second
