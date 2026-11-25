//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/clientapi"
	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadholder/buildid"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadunit"
	"ataniq/internal/leadwire"
)

// The tests of the observer that shows a starting lead ready (lead_ready_linux.go),
// over the fake manager and the fake holder of the other tests of scratch mode.

// readyHolder is a fake holder's answers for the status question. It is up for
// the epoch in epoch (a request for another epoch is refused wrong-epoch, as the
// holder does); with answerEpoch set it answers ok with a status of that epoch
// whatever the request's; with gate set it waits for the gate to be closed
// (or the connection to end) before it answers. Every other request is
// answered as the tests' usual holder does.
type readyHolder struct {
	role string

	epoch       atomic.Uint64
	answerEpoch atomic.Uint64
	answerLead  atomic.Value // string; empty is the role
	noStatus    atomic.Bool  // answer ok with no status
	gate        atomic.Value // chan struct{}
}

func newReadyHolder(role string) *readyHolder {
	return &readyHolder{role: role}
}

func (h *readyHolder) answer(req wire.Request, conn net.Conn) {
	if req.Op != wire.OpStatus {
		submitted(req, conn)
		return
	}
	if g, _ := h.gate.Load().(chan struct{}); g != nil {
		ended := make(chan struct{})
		go func() {
			defer close(ended)
			var b [1]byte
			conn.Read(b[:]) // returns when the daemon closes the connection
		}()
		select {
		case <-g:
		case <-ended:
			return
		}
	}
	if h.noStatus.Load() {
		writeResponse(conn, wire.Response{OK: true})
		return
	}
	epoch := h.epoch.Load()
	if ae := h.answerEpoch.Load(); ae != 0 {
		epoch = ae
	} else if req.Epoch != epoch {
		writeResponse(conn, wire.Response{Code: wire.CodeWrongEpoch, Text: "the request is for another epoch"})
		return
	}
	lead := h.role
	if s, _ := h.answerLead.Load().(string); s != "" {
		lead = s
	}
	writeResponse(conn, wire.Response{OK: true, Status: &wire.Status{
		Lead: lead, Epoch: epoch, Runtime: "fake", Idle: wire.IdleUnknown,
		Size: wire.Size{Columns: 80, Rows: 24}, Caps: wire.DefaultCaps(),
		Facts: wire.Facts{StreamEnded: wire.FactNo, TUIExited: wire.FactNo, TreeGone: wire.FactNo},
	}})
}

// readyBounds sets the observer's bounds for a test: it asks again every 10 ms
// at first and gives up after the time.
func (l *leadEnv) readyBounds(within time.Duration) {
	l.scratch.ReadyWithin = within
	l.scratch.ReadyBackoff = 10 * time.Millisecond
}

// upWith defines the lead, gives it the ready holder and starts it.
func (l *leadEnv) upWith(key string, h *readyHolder) leadwire.StartResult {
	l.t.Helper()
	want(l.t, l.define(key), leadwire.StatusDone, "")
	l.holder(key, h.answer)
	s := l.startLead(key)
	want(l.t, s, leadwire.StatusDone, "")
	var res leadwire.StartResult
	result(l.t, s, &res)
	return res
}

// shown returns the shown state, its reason code and its evidence kind and
// detail, from the daemon's status.
func (l *leadEnv) shown(key string) (state, code, evidence, detail string) {
	l.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cl, err := clientapi.Dial(ctx, l.running.socket)
	if err != nil {
		l.t.Fatal(err)
	}
	defer cl.Close()
	st, err := cl.Status(ctx)
	if err != nil {
		l.t.Fatal(err)
	}
	for _, s := range st.Leads {
		if s.Role == roleOf(key) {
			return s.State, s.ReasonCode, s.EvidenceKind, s.ReasonDetail
		}
	}
	l.t.Fatalf("no entry for %q in the status", key)
	return
}

// waitState waits for the lead to be shown in state.
func (l *leadEnv) waitState(key, state string) {
	l.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if got, _, _, _ := l.shown(key); got == state {
			return
		}
		if time.Now().After(deadline) {
			got, code, _, _ := l.shown(key)
			l.t.Fatalf("the lead %q is shown %q (%s), want %q\nlog:\n%s", key, got, code, state, l.log)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// stays checks, over a short time, that the lead is shown in state throughout.
func (l *leadEnv) stays(key, state string, d time.Duration) {
	l.t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if got, code, _, _ := l.shown(key); got != state {
			l.t.Fatalf("the lead %q is shown %q (%s), want %q throughout", key, got, code, state)
		}
	}
}

// A lead whose holder answers for the epoch of the start is shown ready, with
// the reason code, the evidence kind and the detail the observer gives; the
// start's own result still says starting.
func TestLeadReadyPromotedAfterStart(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	sr := l.upWith("alpha", h)
	if sr.State != "starting" || sr.Epoch != 1 {
		t.Fatalf("the start result: %+v", sr)
	}
	l.waitState("alpha", "ready")
	state, code, evidence, detail := l.shown("alpha")
	if state != "ready" || code != "holder-ready" || evidence != "ataniq-observation" ||
		detail != "the holder is up and the agent was started; the agent's login is not observed" {
		t.Fatalf("shown: %q %q %q %q", state, code, evidence, detail)
	}
	if row := l.row("alpha"); row.State != "ready" || row.Epoch != 1 {
		t.Fatalf("the row: %+v", row)
	}
	reqs := h.holderRequests(t, l, "alpha")
	if len(reqs) == 0 || reqs[0].Epoch != 1 || reqs[0].CommandID == "" || !strings.HasPrefix(reqs[0].CommandID, "lead-status-1-") {
		t.Fatalf("the status requests: %+v", reqs)
	}
	// Ready stays: no question after it changes the state, and a stop works.
	l.stays("alpha", "ready", 150*time.Millisecond)
	want(t, l.stopLead("alpha", 1), leadwire.StatusDone, "")
	if state, _, _, _ := l.shown("alpha"); state != "stopped" {
		t.Fatalf("after the stop: %q", state)
	}
}

// holderRequests returns the status requests the lead's fake holder has seen.
func (h *readyHolder) holderRequests(t *testing.T, l *leadEnv, key string) []wire.Request {
	t.Helper()
	return l.holders[key].requests(wire.OpStatus)
}

// A daemon that is started over a lead that it adopts as starting (its unit
// runs, nothing set ready) shows it ready once the holder answers, with no
// start in this daemon.
func TestLeadReadyPromotedAtStartupForAnAdoptedLead(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	h.noStatus.Store(true) // up, but no status: the first daemon cannot show it ready
	l.upWith("alpha", h)
	l.stays("alpha", "starting", 150*time.Millisecond)
	if err := l.running.stop(); err != nil {
		t.Fatalf("the daemon did not stop clean: %v", err)
	}
	h.noStatus.Store(false)
	l.start()
	// The reconcile adopted the unit and kept the state; the observer of the
	// new daemon finds the holder answering.
	l.waitState("alpha", "ready")
	if _, code, evidence, _ := l.shown("alpha"); code != "holder-ready" || evidence != "ataniq-observation" {
		t.Fatalf("shown: %q %q", code, evidence)
	}
	if n := len(l.fake.StartedSpecs()); n != 1 {
		t.Fatalf("%d starts: the restart must start nothing", n)
	}
}

// A lead that is ready before a restart stays ready after it, and a lead that
// is stopped is never observed.
func TestLeadReadyNotObservedWhenNotStarting(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	l.upWith("alpha", h)
	l.waitState("alpha", "ready")
	want(t, l.define("beta"), leadwire.StatusDone, "")
	hb := newReadyHolder(roleOf("beta"))
	hb.epoch.Store(1)
	l.holder("beta", hb.answer)
	want(t, l.startLead("beta"), leadwire.StatusDone, "")
	l.waitState("beta", "ready")
	want(t, l.stopLead("beta", 1), leadwire.StatusDone, "")
	before := len(l.holders["alpha"].requests(wire.OpStatus))
	bBefore := len(l.holders["beta"].requests(wire.OpStatus))
	l.restart()
	l.stays("alpha", "ready", 200*time.Millisecond)
	if state, _, _, _ := l.shown("beta"); state != "stopped" {
		t.Fatalf("beta: %q", state)
	}
	if n := len(l.holders["alpha"].requests(wire.OpStatus)); n != before {
		t.Fatalf("a ready lead was asked again: %d requests, had %d", n, before)
	}
	if n := len(l.holders["beta"].requests(wire.OpStatus)); n != bBefore {
		t.Fatalf("a stopped lead was asked: %d requests, had %d", n, bBefore)
	}
}

// A holder that is slow to answer does not hold the start up: the start is
// answered while the observer waits, the lead stays starting, and a stop works
// and ends the observer.
func TestLeadReadySlowHolderDoesNotBlockStartOrStop(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(30 * time.Second)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	gate := make(chan struct{})
	h.gate.Store(gate)
	t0 := time.Now()
	l.upWith("alpha", h)
	if d := time.Since(t0); d > 5*time.Second {
		t.Fatalf("the start took %s", d)
	}
	// The observer has asked and waits.
	deadline := time.Now().Add(5 * time.Second)
	for len(l.holders["alpha"].requests(wire.OpStatus)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the observer never asked the holder")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if state, _, _, _ := l.shown("alpha"); state != "starting" {
		t.Fatalf("shown %q while the holder is slow", state)
	}
	if row := l.row("alpha"); row.State != "starting" {
		t.Fatalf("the row: %+v", row)
	}
	// Other commands are answered meanwhile, and the stop is not held up.
	want(t, l.define("beta"), leadwire.StatusDone, "")
	t0 = time.Now()
	want(t, l.stopLead("alpha", 1), leadwire.StatusDone, "")
	if d := time.Since(t0); d > 5*time.Second {
		t.Fatalf("the stop took %s", d)
	}
	close(gate)
	l.stays("alpha", "stopped", 150*time.Millisecond)
}

// A holder that never answers leaves the lead starting once the time is up, and
// the daemon logs it; nothing else changes.
func TestLeadReadySilentHolderLeavesStarting(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(300 * time.Millisecond)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	h.gate.Store(make(chan struct{}))
	l.upWith("alpha", h)
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(l.log.String(), "the lead stays starting") {
		if time.Now().After(deadline) {
			t.Fatalf("no give-up in the log:\n%s", l.log)
		}
		time.Sleep(10 * time.Millisecond)
	}
	l.stays("alpha", "starting", 100*time.Millisecond)
	if row := l.row("alpha"); row.State != "starting" || row.Epoch != 1 {
		t.Fatalf("the row: %+v", row)
	}
}

// A holder that is not up yet (nothing listens) is asked again, and the lead
// is shown ready once it is.
func TestLeadReadyRetriesUntilTheHolderIsUp(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	want(t, l.startLead("alpha"), leadwire.StatusDone, "") // no holder listens
	time.Sleep(100 * time.Millisecond)
	l.stays("alpha", "starting", 50*time.Millisecond)
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	l.holder("alpha", h.answer)
	l.waitState("alpha", "ready")
}

// A holder that answers for another epoch, for another lead, with a refusal or
// with no status is ignored: the lead stays starting.
func TestLeadReadyIgnoresAnAnswerThatIsNotTheLeads(t *testing.T) {
	cases := []struct {
		name string
		set  func(h *readyHolder)
	}{
		{"another-epoch-answered", func(h *readyHolder) { h.answerEpoch.Store(2) }},
		{"another-lead-answered", func(h *readyHolder) { h.answerLead.Store(roleOf("beta")) }},
		{"wrong-epoch-refused", func(h *readyHolder) { h.epoch.Store(7) }},
		{"no-status", func(h *readyHolder) { h.epoch.Store(1); h.noStatus.Store(true) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := newLeadEnv(t)
			l.readyBounds(10 * time.Second)
			l.start()
			h := newReadyHolder(roleOf("alpha"))
			h.epoch.Store(1)
			c.set(h)
			l.upWith("alpha", h)
			deadline := time.Now().Add(5 * time.Second)
			for len(l.holders["alpha"].requests(wire.OpStatus)) < 3 {
				if time.Now().After(deadline) {
					t.Fatal("the observer did not ask again")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if state, code, _, _ := l.shown("alpha"); state != "starting" {
				t.Fatalf("shown %q (%s)", state, code)
			}
			for _, r := range l.holders["alpha"].requests(wire.OpStatus) {
				if r.Epoch != 1 {
					t.Fatalf("a question for epoch %d", r.Epoch)
				}
			}
		})
	}
}

// A lead that is stopped after the holder has answered and before the state is
// set is not shown ready: the compare-and-set sees the stopped state.
func TestLeadReadyNotPromotedWhenStoppedMeanwhile(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	var once sync.Once
	stopped := make(chan struct{})
	leadReadyHook = func(stage string) {
		once.Do(func() {
			want(t, l.stopLead("alpha", 1), leadwire.StatusDone, "")
			close(stopped)
		})
	}
	t.Cleanup(func() { leadReadyHook = nil })
	l.upWith("alpha", h)
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the hook never ran")
	}
	l.stays("alpha", "stopped", 200*time.Millisecond)
	if _, code, _, _ := l.shown("alpha"); code == "holder-ready" {
		t.Fatalf("the stopped lead carries the ready reason %q", code)
	}
}

// A lead that is stopped and started again (a newer epoch) after the holder has
// answered for the old epoch is not promoted by the old observer: the state it
// finds is the newer epoch's, which only a holder of that epoch can show ready.
func TestLeadReadyNotPromotedWhenTheEpochChanged(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1) // refuses epoch 2: the new holder is not up
	var once sync.Once
	restarted := make(chan struct{})
	leadReadyHook = func(stage string) {
		once.Do(func() {
			want(t, l.stopLead("alpha", 1), leadwire.StatusDone, "")
			want(t, l.startLead("alpha"), leadwire.StatusDone, "")
			close(restarted)
		})
	}
	t.Cleanup(func() { leadReadyHook = nil })
	l.upWith("alpha", h)
	select {
	case <-restarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the hook never ran")
	}
	l.stays("alpha", "starting", 300*time.Millisecond)
	if row := l.row("alpha"); row.State != "starting" || row.Epoch != 2 {
		t.Fatalf("the row: %+v", row)
	}
	// The holder of epoch 2 comes up: the lead of that epoch is ready.
	h.epoch.Store(2)
	l.waitState("alpha", "ready")
	if row := l.row("alpha"); row.Epoch != 2 {
		t.Fatalf("the row: %+v", row)
	}
}

// The operation lock is not held across the holder call: while the observer
// waits for a slow holder, a second start of the lead is refused by the state
// (not role-busy), and a stop is not refused role-busy either. The promotion
// itself tries the lock and tries again.
func TestLeadReadyDoesNotHoldTheRoleLockAcrossTheHolder(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(30 * time.Second)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	h.gate.Store(make(chan struct{}))
	l.upWith("alpha", h)
	deadline := time.Now().Add(5 * time.Second)
	for len(l.holders["alpha"].requests(wire.OpStatus)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the observer never asked the holder")
		}
		time.Sleep(5 * time.Millisecond)
	}
	r := l.startLead("alpha")
	if r.Code == "role-busy" {
		t.Fatalf("a start was refused role-busy while the observer waits: %+v", r)
	}
	want(t, l.stopLead("alpha", 0), leadwire.StatusDone, "")
}

// While a command of the role holds the operation lock the promotion is tried
// again, and made once the lock is free. A second start of the role, made from
// the hook (after the holder answered) and held at the act hook, stands for a
// command that holds the lock; it is refused by the registry afterwards.
func TestLeadReadyPromotionTriesAgainWhileTheRoleIsBusy(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	held := make(chan struct{})
	release := make(chan struct{})
	var hookOnce sync.Once
	// The first start (the one of upWith) passes the act hook, which is not
	// set; the second is made from the ready hook, with the act hook set to
	// hold the role's lock.
	t.Cleanup(func() { leadActHook = nil; leadReadyHook = nil })
	secondDone := make(chan struct{})
	leadReadyHook = func(stage string) {
		hookOnce.Do(func() {
			leadActHook = func(op string) {
				if op == "start" {
					close(held)
					<-release
				}
			}
			go func() {
				defer close(secondDone)
				var r leadwire.Receipt
				l.call(leadwire.OpStart, params("alpha", "second-start", nil), &r)
			}()
			select {
			case <-held:
			case <-time.After(10 * time.Second):
			}
		})
	}
	l.upWith("alpha", h)
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the second start never took the role's lock")
	}
	// The observer tried the lock, found it held, and tries again.
	time.Sleep(100 * time.Millisecond)
	if state, _, _, _ := l.shown("alpha"); state != "starting" {
		t.Fatalf("shown %q while the role is busy", state)
	}
	close(release)
	<-secondDone
	l.waitState("alpha", "ready")
}

// The daemon stops clean, with exit 0, while an observer waits for a holder that
// does not answer, and the observer is gone when Run returns.
func TestLeadReadyShutdownJoinsTheObserver(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(time.Minute)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	h.gate.Store(make(chan struct{}))
	l.upWith("alpha", h)
	deadline := time.Now().Add(5 * time.Second)
	for len(l.holders["alpha"].requests(wire.OpStatus)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the observer never asked the holder")
		}
		time.Sleep(5 * time.Millisecond)
	}
	t0 := time.Now()
	if err := l.running.stop(); err != nil {
		t.Fatalf("the daemon did not stop clean: %v\nlog:\n%s", err, l.log)
	}
	if d := time.Since(t0); d > 10*time.Second {
		t.Fatalf("the stop took %s", d)
	}
	if strings.Contains(l.log.String(), "observer did not stop") {
		t.Fatalf("the observer was left:\n%s", l.log)
	}
}

// barePromote is a mode over a world, the lead alpha defined and launched (its
// outcome recorded), and the holder folder made; it returns the mode, the lead
// and the epoch.
func barePromote(t *testing.T) (*leadMode, *leadRole, uint64) {
	t.Helper()
	dir, err := os.MkdirTemp("", "ls") // short: a socket is made in it
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	m := bareMode(t, provisiontest.NewScratch(shortTB{TB: t, dir: dir}))
	lr := m.roles[mustRole(t, roleOf("alpha"))]
	if _, err := m.reg.Define(lr.role, leadregistry.ManagerAtaniq, profile()); err != nil {
		t.Fatal(err)
	}
	in, err := m.reg.BeginLaunchWith(lr.role, leadregistry.LaunchOptions{BootIdentity: "boot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.reg.RecordOutcome(lr.role, in.ID, in.Epoch, leadregistry.OutcomeLaunched,
		leadregistry.UnitEvidence{InvocationID: "inv", ControlGroupPath: "/cg/alpha", ControlGroupID: "1", BootID: "boot"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lr.in.ext.holderDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return m, lr, in.Epoch
}

// The promotion is a compare-and-set on the epoch and the shown state, made
// under the role's operation lock: it sets ready for the starting lead of the
// epoch, and changes nothing for another epoch, for a lead that is stopped, for
// one that is already ready, and while the lock is held.
func TestLeadReadyPromoteIsACompareAndSet(t *testing.T) {
	m, lr, epoch := barePromote(t)
	shownNow := func() (leadregistry.State, string) {
		e, err := m.reg.Get(lr.role)
		if err != nil {
			t.Fatal(err)
		}
		return e.Shown.State, e.Shown.Reason.Code
	}
	// Another epoch: nothing.
	if !m.promote(lr, epoch+1) {
		t.Fatal("a promotion of another epoch must be done")
	}
	if st, _ := shownNow(); st != leadregistry.StateStarting {
		t.Fatalf("another epoch: shown %q", st)
	}
	// The lock held: not done, nothing changed.
	if !lr.tryLock() {
		t.Fatal("the lock is not free")
	}
	if m.promote(lr, epoch) {
		t.Fatal("a promotion under a held lock must not be done")
	}
	lr.unlock()
	if st, _ := shownNow(); st != leadregistry.StateStarting {
		t.Fatalf("lock held: shown %q", st)
	}
	// The lead is starting in the epoch: ready, with the reason.
	if !m.promote(lr, epoch) {
		t.Fatal("the promotion is not done")
	}
	e, _ := m.reg.Get(lr.role)
	if e.Shown.State != leadregistry.StateReady || e.Shown.Reason.Code != "holder-ready" ||
		e.Shown.Evidence != leadregistry.EvidenceObservation || e.Epoch != epoch ||
		e.Shown.Reason.Detail != "the holder is up and the agent was started; the agent's login is not observed" {
		t.Fatalf("the entry: %+v", e.Shown)
	}
	// Ready stays ready, and the reason is not rewritten.
	at := e.Shown.At
	if !m.promote(lr, epoch) {
		t.Fatal("the second promotion is not done")
	}
	if e2, _ := m.reg.Get(lr.role); !e2.Shown.At.Equal(at) {
		t.Fatal("a ready lead was written again")
	}
	// Stopped: never ready.
	if _, err := m.reg.SetState(lr.role, leadregistry.StateChange{Epoch: epoch, State: leadregistry.StateStopped,
		Evidence: leadregistry.EvidenceObservation, Reason: leadregistry.Reason{Code: "test-setup"}, Proof: "the test"}); err != nil {
		t.Fatal(err)
	}
	if !m.promote(lr, epoch) {
		t.Fatal("a promotion of a stopped lead must be done")
	}
	if st, _ := shownNow(); st != leadregistry.StateStopped {
		t.Fatalf("stopped lead: shown %q", st)
	}
}

// The observers of a mode: none starts before they may run or after the join,
// a second one for a lead replaces the first, and the join ends the one that
// waits for a holder that does not answer.
func TestLeadReadyObserversAreJoinedAndNoneStartsAfter(t *testing.T) {
	m, lr, epoch := barePromote(t)
	count := func() int {
		m.ready.mu.Lock()
		defer m.ready.mu.Unlock()
		return len(m.ready.byRole)
	}
	m.readyWithin, m.readyBackoff = time.Minute, 10*time.Millisecond
	h := newFakeHolder(t, lr.in.ext.holderDir, silent)

	// Before the observers may run, nothing starts.
	m.observeReady(lr, epoch)
	if count() != 0 {
		t.Fatal("an observer started before the observers were let run")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.startObservers(ctx) // the lead is starting with a launched intent, but has no holder command
	if count() != 0 {
		t.Fatal("the lead has no holder command and was observed")
	}
	m.observeReady(lr, epoch)
	first := count()
	deadline := time.Now().Add(5 * time.Second)
	for len(h.requests(wire.OpStatus)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the observer never asked")
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.observeReady(lr, epoch) // replaces the first: still one
	if first != 1 || count() != 1 {
		t.Fatalf("observers: %d then %d, want 1", first, count())
	}
	t0 := time.Now()
	if !m.joinObservers(5 * time.Second) {
		t.Fatal("the join did not finish")
	}
	if d := time.Since(t0); d > 3*time.Second {
		t.Fatalf("the join took %s", d)
	}
	m.observeReady(lr, epoch)
	if count() != 0 {
		t.Fatalf("%d observers after the join", count())
	}
	// A second join is harmless.
	if !m.joinObservers(time.Second) {
		t.Fatal("the second join did not finish")
	}
}

// A stop that is refused because it names another epoch leaves the epoch that
// runs, and so its observer: the lead is shown ready once the holder answers
// for the epoch, though the stop came while it was still starting.
func TestLeadReadyKeptByAStopOfAnotherEpoch(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.answerEpoch.Store(99) // an answer for another epoch: the lead stays starting
	l.upWith("alpha", h)
	want(t, l.stopLead("alpha", 7), leadwire.StatusRefused, codeStaleEpoch)
	l.stays("alpha", "starting", 100*time.Millisecond)
	h.answerEpoch.Store(0)
	h.epoch.Store(1)
	l.waitState("alpha", "ready")
}

// A stop that is refused role-busy (a command of the role holds the lock) goes
// no further and leaves the observer: the lead is shown ready once the holder
// answers. A stop that fails after it began changes the registry's state (the
// lead is shown unknown), so there is no starting lead left for an observer
// to settle; the lifecycle's stop errors are covered by the failure table.
func TestLeadReadyKeptByAStopRefusedRoleBusy(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.answerEpoch.Store(99)
	l.upWith("alpha", h)
	held, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { leadActHook = nil })
	leadActHook = func(op string) {
		if op == "start" {
			close(held)
			<-release
		}
	}
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		var r leadwire.Receipt
		l.call(leadwire.OpStart, params("alpha", "second-start", nil), &r)
	}()
	select {
	case <-held:
	case <-time.After(10 * time.Second):
		t.Fatal("the second start never took the role's lock")
	}
	want(t, l.stopLead("alpha", 0), leadwire.StatusRefused, "role-busy")
	close(release)
	<-secondDone
	l.stays("alpha", "starting", 100*time.Millisecond)
	h.answerEpoch.Store(0)
	h.epoch.Store(1)
	l.waitState("alpha", "ready")
}

// A server that does not end within the grace is abandoned, and the observers
// are then not waited for a second grace: the daemon returns its exit 19 error
// after about one grace, though an observer does not end either (it is held at
// the ready hook, which the daemon's context does not reach).
func TestLeadReadyAbandonedServerDoesNotWaitASecondGrace(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(time.Minute)
	cfg := l.daemonConfig()
	const grace = time.Second
	cfg.ShutdownGrace = grace
	l.running = l.startDaemon(cfg)
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	want(t, l.define("beta"), leadwire.StatusDone, "")
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	l.holder("alpha", h.answer)
	l.holder("beta", submitted)

	observing, stuckStart, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once, onceStart sync.Once
	t.Cleanup(func() { leadActHook = nil; leadReadyHook = nil })
	t.Cleanup(func() { close(release) })
	leadReadyHook = func(string) {
		once.Do(func() { close(observing) })
		<-release // an observer that does not end on the context
	}
	want(t, l.startLead("alpha"), leadwire.StatusDone, "")
	select {
	case <-observing:
	case <-time.After(10 * time.Second):
		t.Fatal("the observer never reached the hook")
	}
	leadActHook = func(op string) {
		if op == "start" {
			onceStart.Do(func() { close(stuckStart); <-release })
		}
	}
	// A command that does not end on the context keeps the server from
	// stopping.
	go func() {
		var r leadwire.Receipt
		_ = l.call(leadwire.OpStart, params("beta", "stuck-1", nil), &r)
	}()
	<-stuckStart
	t0 := time.Now()
	mustFail(t, l.running.stop(), StepShutdown, ExitShutdown)
	if d := time.Since(t0); d >= 2*grace-grace/5 {
		t.Fatalf("the stop took %s: the observers were waited for a second grace of %s", d, grace)
	}
}

// otherBuildRecord is a check of the holder's build whose holders are the
// expected build by their executable (the check before the start passes) and
// record another build in holder.json for every epoch: a holder rebuilt
// between the check and the start.
func otherBuildRecord() *buildid.Checker {
	ch := sameHolderBuild()
	ch.Record = func(_ string, epoch uint64) (buildid.Record, error) {
		b := otherHolderBuild
		return buildid.Record{Epoch: epoch, Build: &b}, nil
	}
	return ch
}

// A holder that answers for the epoch of this daemon's start but recorded
// another build from its running executable is never shown ready: the lead is
// stopped, and the stop is in the evidence with the refusal.
func TestLeadReadyStopsAHolderThatStartedAsAnotherBuild(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.scratch.HolderBuild = otherBuildRecord()
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	sr := l.upWith("alpha", h)
	if sr.State != "starting" || sr.Epoch != 1 {
		t.Fatalf("the start result: %+v", sr)
	}
	l.waitState("alpha", "stopped")
	if len(h.holderRequests(t, l, "alpha")) == 0 {
		t.Fatal("the holder was never asked")
	}
	ev := l.evidenceOf("alpha")
	if len(ev) == 0 || ev[len(ev)-1] != "stop" {
		t.Fatalf("evidence %v", ev)
	}
	b, err := os.ReadFile(l.cfgOf("alpha").EvidencePath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"detail":"holder-version-skew: the holder is another build`) {
		t.Fatalf("evidence %s", b)
	}
	if !strings.Contains(l.log.String(), "is not the build checked before the start; the lead is stopped") {
		t.Fatalf("log:\n%s", l.log)
	}
	want(t, l.ticket("alpha", 1, "writer"), leadwire.StatusRefused, "not-running")
}

// A lead this daemon adopted as starting, whose holder recorded another build,
// is not shown ready and is not stopped: the daemon did not start it.
func TestLeadReadyLeavesAnAdoptedHolderOfAnotherBuild(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	h.noStatus.Store(true)
	l.upWith("alpha", h)
	l.stays("alpha", "starting", 100*time.Millisecond)
	if err := l.running.stop(); err != nil {
		t.Fatalf("the daemon did not stop clean: %v", err)
	}
	l.scratch.HolderBuild = otherBuildRecord()
	h.noStatus.Store(false)
	l.start()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(l.log.String(), "which this daemon adopted, is not its build") {
		if time.Now().After(deadline) {
			t.Fatalf("log:\n%s", l.log)
		}
		time.Sleep(10 * time.Millisecond)
	}
	l.stays("alpha", "starting", 100*time.Millisecond)
	if n := len(l.fake.StartedSpecs()); n != 1 {
		t.Fatalf("%d starts", n)
	}
	want(t, l.ticket("alpha", 1, "writer"), leadwire.StatusRefused, "holder-version-skew")
}

// The reviewer's case of an upgraded daemon: a lead an earlier daemon showed
// ready is adopted by a daemon of another build. Before the new daemon serves
// anything its holder.json is read again: the very first status and list
// show the lead degraded with the reason holder-version-skew and the refusal
// in the detail, never ready, so the row agrees with the refused ticket. Its
// unit is left running: no signal, no stop, no start, and the holder is asked
// nothing. A lead of the expected build that is ready stays ready.
func TestLeadReadyAdoptedReadyLeadOfAnotherBuildIsShownSkewed(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	for _, key := range []string{"alpha", "beta"} {
		h := newReadyHolder(roleOf(key))
		h.epoch.Store(1)
		l.upWith(key, h)
		l.waitState(key, "ready")
	}
	if err := l.running.stop(); err != nil {
		t.Fatalf("the daemon did not stop clean: %v", err)
	}
	// The new daemon's build: alpha's holder recorded another, beta's the
	// expected one.
	ch := sameHolderBuild()
	alphaRole := l.cfgOf("alpha").Role
	ch.Record = func(dir string, epoch uint64) (buildid.Record, error) {
		b := testHolderBuild
		alphaLayout, err := l.cfgOf("alpha").Layout()
		if err != nil {
			return buildid.Record{}, err
		}
		if dir == alphaLayout.Holder() {
			b = otherHolderBuild
		}
		return buildid.Record{Epoch: epoch, Build: &b}, nil
	}
	l.scratch.HolderBuild = ch
	statusBefore := len(l.holders["alpha"].requests(wire.OpStatus))
	signals := len(l.fake.CallsOf(leadunit.MethodSignal))
	l.start()
	state, code, evidence, detail := l.shown("alpha")
	if state != "degraded" || code != "holder-version-skew" || evidence != "ataniq-observation" ||
		!strings.HasPrefix(detail, "holder-version-skew: the holder is another build") ||
		!strings.Contains(detail, "expected build "+testHolderBuild.Short()) {
		t.Fatalf("alpha shown: %q %q %q %q", state, code, evidence, detail)
	}
	if row := l.row("alpha"); row.State != "degraded" || row.StateReason != "holder-version-skew" || row.Epoch != 1 {
		t.Fatalf("the row: %+v", row)
	}
	if state, code, _, _ := l.shown("beta"); state != "ready" || code != "holder-ready" {
		t.Fatalf("beta shown: %q %q", state, code)
	}
	want(t, l.ticket("alpha", 1, "writer"), leadwire.StatusRefused, "holder-version-skew")
	l.stays("alpha", "degraded", 150*time.Millisecond)
	if !l.fake.Loaded(alphaRole.UnitName()) {
		t.Fatal("the adopted unit was stopped")
	}
	if n := len(l.fake.CallsOf(leadunit.MethodSignal)); n != signals {
		t.Fatalf("%d signals were sent", n-signals)
	}
	if n := len(l.fake.StartedSpecs()); n != 2 {
		t.Fatalf("%d starts", n)
	}
	if n := len(l.holders["alpha"].requests(wire.OpStatus)); n != statusBefore {
		t.Fatalf("the holder was asked %d times", n-statusBefore)
	}
	// The owner's way out: a stop ends it.
	want(t, l.stopLead("alpha", 1), leadwire.StatusDone, "")
}

// An adopted ready lead whose holder.json is missing confirms no other build:
// it stays ready, and the daemon logs that its build cannot be confirmed.
func TestLeadReadyAdoptedReadyLeadWithoutARecordStaysReady(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	l.upWith("alpha", h)
	l.waitState("alpha", "ready")
	if err := l.running.stop(); err != nil {
		t.Fatalf("the daemon did not stop clean: %v", err)
	}
	l.scratch.HolderBuild.Record = func(string, uint64) (buildid.Record, error) { return buildid.Record{}, os.ErrNotExist }
	l.start()
	if state, code, _, _ := l.shown("alpha"); state != "ready" || code != "holder-ready" {
		t.Fatalf("shown %q %q", state, code)
	}
	l.waitLog("the adopted ready lead's build cannot be confirmed for epoch 1; it stays ready")
}

// waitLog waits for the daemon's log to hold text.
func (l *leadEnv) waitLog(text string) {
	l.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(l.log.String(), text) {
		if time.Now().After(deadline) {
			l.t.Fatalf("no %q in the log:\n%s", text, l.log)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The reviewer's case of a holder that never answers: the holder that started
// for this daemon's start wrote its holder.json of the epoch from its own
// running executable, which is another build, and gives no usable status
// (no status in its answers, or nothing listens at all). The observer reads
// the file after each question that got no answer, and the lead is stopped,
// with the stop in the evidence, as for one that answered; it does not stay
// starting after the observation ends.
func TestLeadReadyStopsAHolderOfAnotherBuildThatDoesNotAnswer(t *testing.T) {
	for name, listens := range map[string]bool{"no status in its answers": true, "nothing listens": false} {
		t.Run(name, func(t *testing.T) {
			l := newLeadEnv(t)
			l.readyBounds(2 * time.Second)
			l.scratch.HolderBuild = otherBuildRecord()
			l.start()
			want(t, l.define("alpha"), leadwire.StatusDone, "")
			if listens {
				h := newReadyHolder(roleOf("alpha"))
				h.epoch.Store(1)
				h.noStatus.Store(true)
				l.holder("alpha", h.answer)
			}
			s := l.startLead("alpha")
			want(t, s, leadwire.StatusDone, "")
			var sr leadwire.StartResult
			result(t, s, &sr)
			if sr.State != "starting" || sr.Epoch != 1 {
				t.Fatalf("the start result: %+v", sr)
			}
			l.waitState("alpha", "stopped")
			ev := l.evidenceOf("alpha")
			if len(ev) == 0 || ev[len(ev)-1] != "stop" {
				t.Fatalf("evidence %v", ev)
			}
			if !strings.Contains(l.log.String(), "is not the build checked before the start; the lead is stopped") {
				t.Fatalf("log:\n%s", l.log)
			}
		})
	}
}

// A holder of the expected build that never answers, and one whose
// holder.json cannot be read, confirm no other build: the lead is not stopped
// and stays starting once the observation ends.
func TestLeadReadyDoesNotStopASilentHolderOfNoConfirmedBuild(t *testing.T) {
	for name, rec := range map[string]func(string, uint64) (buildid.Record, error){
		"the expected build": sameHolderBuild().Record,
		"an unreadable file": func(string, uint64) (buildid.Record, error) {
			return buildid.Record{}, errors.New("the state file is not a JSON object of a holder")
		},
		"no file": func(string, uint64) (buildid.Record, error) {
			return buildid.Record{}, os.ErrNotExist
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := newLeadEnv(t)
			l.readyBounds(300 * time.Millisecond)
			l.scratch.HolderBuild.Record = rec
			l.start()
			h := newReadyHolder(roleOf("alpha"))
			h.epoch.Store(1)
			h.noStatus.Store(true)
			l.upWith("alpha", h)
			l.waitLog("the lead stays starting")
			l.stays("alpha", "starting", 100*time.Millisecond)
			if strings.Contains(l.log.String(), "the lead is stopped") {
				t.Fatalf("log:\n%s", l.log)
			}
		})
	}
}

// A holder of another build that answers at the very end of the observation
// still gets a whole stop: the stop has a deadline of its own, not what is
// left of the observation's, which has run out when it begins.
func TestLeadReadySkewStopHasItsOwnDeadline(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(200 * time.Millisecond)
	l.scratch.HolderBuild = otherBuildRecord()
	var once sync.Once
	leadReadyHook = func(stage string) {
		once.Do(func() { time.Sleep(400 * time.Millisecond) }) // past the observation's end
	}
	t.Cleanup(func() { leadReadyHook = nil })
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	l.upWith("alpha", h)
	l.waitState("alpha", "stopped")
	if ev := l.evidenceOf("alpha"); len(ev) == 0 || ev[len(ev)-1] != "stop" {
		t.Fatalf("evidence %v", ev)
	}
}

// A stop of a holder of another build that is not proven (here its own
// deadline is already over when it begins, so the lifecycle does nothing) is
// not left as a lead shown starting that nothing watches: the lead is shown
// degraded with the reason skew-stop-unproven and the refusal in the detail,
// stays so, and the owner's stop ends it.
func TestLeadReadyShowsAnUnprovenSkewStop(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.scratch.HolderBuild = otherBuildRecord()
	l.scratch.SkewStopWithin = time.Nanosecond
	l.start()
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	l.upWith("alpha", h)
	l.waitState("alpha", "degraded")
	state, code, evidence, detail := l.shown("alpha")
	if state != "degraded" || code != codeSkewStopUnproven || evidence != "ataniq-observation" ||
		!strings.HasPrefix(detail, detailSkewStopUnproven+"holder-version-skew: the holder is another build") {
		t.Fatalf("shown: %q %q %q %q", state, code, evidence, detail)
	}
	if row := l.row("alpha"); row.State != "degraded" || row.StateReason != codeSkewStopUnproven || row.Epoch != 1 {
		t.Fatalf("the row: %+v", row)
	}
	l.stays("alpha", "degraded", 150*time.Millisecond)
	want(t, l.stopLead("alpha", 1), leadwire.StatusDone, "")
	if state, _, _, _ := l.shown("alpha"); state != "stopped" {
		t.Fatalf("after the stop: %q", state)
	}
}
