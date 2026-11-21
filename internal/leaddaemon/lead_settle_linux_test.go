//go:build linux && ataniq_canary

package leaddaemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
	"ataniq/internal/leadwire"
)

// The tests of the timer that settles a launch intent when its late-start
// window ends (lead_settle_linux.go), over the fake manager, on a clock that
// runs in real time and that a test moves forward by a step.

// stepClock is real time plus the sum of the steps of Advance. A sleep ends
// when the clock has moved by its duration, in real time or by a step, so the
// short waits of the lifecycle run as they do and a window of 40 s is crossed
// by one call.
type stepClock struct {
	mu     sync.Mutex
	skew   time.Duration
	moved  chan struct{} // closed and replaced on each step
	sleeps []time.Duration
}

func newStepClock() *stepClock { return &stepClock{moved: make(chan struct{})} }

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return time.Now().Add(c.skew)
}

// Advance moves the clock forward by d.
func (c *stepClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.skew += d
	close(c.moved)
	c.moved = make(chan struct{})
	c.mu.Unlock()
}

func (c *stepClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	go func() {
		if c.Sleep(context.Background(), d) == nil {
			ch <- c.Now()
		}
	}()
	return ch
}

func (c *stepClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.mu.Unlock()
	start := c.Now()
	for {
		remaining := d - c.Now().Sub(start)
		if remaining <= 0 {
			return nil
		}
		c.mu.Lock()
		moved := c.moved
		c.mu.Unlock()
		t := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-moved:
			t.Stop()
		case <-t.C:
		}
	}
}

// longSleeps is how many sleeps of a second or more were asked for so far: the
// timers of the daemon, and nothing else, wait that long.
func (c *stepClock) longSleeps() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, d := range c.sleeps {
		if d >= time.Second {
			n++
		}
	}
	return n
}

// waitLongSleeps waits until n sleeps of a second or more were asked for.
func (c *stepClock) waitLongSleeps(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for c.longSleeps() < n {
		if time.Now().After(deadline) {
			t.Fatalf("%d long sleeps, want %d", c.longSleeps(), n)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// settleEnv is a scratch world on a step clock.
type settleEnv struct {
	*leadEnv
	clk *stepClock
}

func newSettleEnv(t *testing.T) *settleEnv {
	t.Helper()
	l := newLeadEnv(t)
	clk := newStepClock()
	l.fake = leadunit.NewFake(clk)
	l.gate = &gateManager{Manager: l.fake}
	return &settleEnv{leadEnv: l, clk: clk}
}

// startDaemonOn runs the daemon on the step clock.
func (s *settleEnv) startDaemonOn() {
	s.t.Helper()
	cfg := s.daemonConfig()
	cfg.Clock = s.clk
	s.running = s.env.startDaemon(cfg)
}

func (s *settleEnv) roleOf(key string) leadrole.Role {
	s.t.Helper()
	r, err := leadrole.Parse(roleOf(key))
	if err != nil {
		s.t.Fatal(err)
	}
	return r
}

// withRegistry opens the registry of the stopped daemon on the step clock and
// runs fn.
func (s *settleEnv) withRegistry(fn func(reg *leadregistry.Registry)) {
	s.t.Helper()
	reg, err := leadregistry.Open(filepath.Join(s.state, RegistryDirName), leadregistry.Options{Now: s.clk.Now})
	if err != nil {
		s.t.Fatal(err)
	}
	defer func() {
		if err := reg.Close(); err != nil {
			s.t.Fatal(err)
		}
	}()
	fn(reg)
}

// killedMidStart brings the world to what a daemon killed in the middle of a
// start leaves: the leads are defined, the launch intent of the lead key is
// journaled and pending, no unit was asked for. The daemon is then started
// again, and it shows the lead unknown (launch-unsettled). It returns the
// intent.
func (s *settleEnv) killedMidStart(key string, defines ...string) leadregistry.Intent {
	s.t.Helper()
	s.startDaemonOn()
	for _, k := range append([]string{key}, defines...) {
		want(s.t, s.define(k), leadwire.StatusDone, "")
	}
	if err := s.running.stop(); err != nil {
		s.t.Fatalf("the daemon did not stop clean: %v", err)
	}
	var in leadregistry.Intent
	s.withRegistry(func(reg *leadregistry.Registry) {
		var err error
		in, err = reg.BeginLaunchWith(s.roleOf(key), leadregistry.LaunchOptions{BootIdentity: s.fake.BootID()})
		if err != nil {
			s.t.Fatal(err)
		}
	})
	s.startDaemonOn()
	if state, code, _, _ := s.shown(key); state != "unknown" || code != "launch-unsettled" {
		s.t.Fatalf("the lead is shown %q (%s)\nlog:\n%s", state, code, s.log)
	}
	return in
}

// finalEntry stops the daemon and returns the registry's entry of the lead.
func (s *settleEnv) finalEntry(key string) leadregistry.Entry {
	s.t.Helper()
	if err := s.running.stop(); err != nil {
		s.t.Fatalf("the daemon did not stop clean: %v\nlog:\n%s", err, s.log)
	}
	var e leadregistry.Entry
	s.withRegistry(func(reg *leadregistry.Registry) {
		var err error
		if e, err = reg.Get(s.roleOf(key)); err != nil {
			s.t.Fatal(err)
		}
	})
	return e
}

// startsAndSignals returns how many Start and Signal calls the fake saw.
func (s *settleEnv) startsAndSignals() (starts, signals int) {
	return len(s.fake.CallsOf(leadunit.MethodStart)), len(s.fake.CallsOf(leadunit.MethodSignal))
}

// A daemon killed in the middle of a start leaves the lead unknown; the daemon
// that comes back settles it itself when the window ends, with no restart: the
// lead is stopped, with the proof, and can be started again, with the next
// epoch.
func TestSettleKilledMidStartEndsStoppedWithoutARestart(t *testing.T) {
	s := newSettleEnv(t)
	in := s.killedMidStart("alpha")
	s.clk.waitLongSleeps(t, 1)

	// The role is unknown, so nothing starts, and a stop is not settled inside
	// the window.
	s.holder("alpha", submitted)
	want(t, s.startLead("alpha"), leadwire.StatusRefused, codeLaunchRefused)
	want(t, s.stopLead("alpha", 0), leadwire.StatusUnavailable, codeIntentPending)
	s.clk.Advance(leadlifecycle.DefaultLateStartWindow)
	s.leadEnv.stays("alpha", "unknown", 200*time.Millisecond)

	// The window and its margin have passed.
	s.clk.Advance(leadlifecycle.DefaultLateStartMargin + time.Second)
	s.waitState("alpha", "stopped")
	if _, code, _, _ := s.shown("alpha"); code != "launch-abandoned" {
		t.Fatalf("reason %q", code)
	}
	if starts, signals := s.startsAndSignals(); starts != 0 || signals != 0 {
		t.Fatalf("%d starts and %d signals", starts, signals)
	}
	if !strings.Contains(s.log.String(), "scratch settle \"alpha\"") {
		t.Fatalf("the settle was not logged:\n%s", s.log)
	}

	// The lead starts again, in the next epoch.
	r := s.startLead("alpha")
	want(t, r, leadwire.StatusDone, "")
	var res leadwire.StartResult
	result(t, r, &res)
	if res.Epoch != in.Epoch+1 {
		t.Fatalf("epoch %d, want %d", res.Epoch, in.Epoch+1)
	}
	e := s.finalEntry("alpha")
	if e.Epoch != in.Epoch+1 {
		t.Fatalf("entry %+v", e)
	}
}

// A start of the running daemon whose reply is lost is settled the same way.
func TestSettleALostReplyOfALiveStartEndsStopped(t *testing.T) {
	s := newSettleEnv(t)
	s.startDaemonOn()
	want(t, s.define("alpha"), leadwire.StatusDone, "")
	s.holder("alpha", submitted)
	s.fake.DropNextStart(s.roleOf("alpha").UnitName())
	want(t, s.startLead("alpha"), leadwire.StatusUncertain, codeLaunchUncertain)
	if state, code, _, _ := s.shown("alpha"); state != "unknown" || code != "launch-reply-lost" {
		t.Fatalf("shown %q (%s)", state, code)
	}
	s.clk.waitLongSleeps(t, 1)
	s.clk.Advance(leadlifecycle.DefaultLateStartWindow + leadlifecycle.DefaultLateStartMargin + time.Second)
	s.waitState("alpha", "stopped")
	if starts, signals := s.startsAndSignals(); starts != 1 || signals != 0 {
		t.Fatalf("%d starts and %d signals, want the lost start and no signal", starts, signals)
	}
	want(t, s.startLead("alpha"), leadwire.StatusDone, "")
}

// A unit that is loaded at the deadline is never a reason to settle the lead to
// stopped, and is never signalled: one that is not the launch's is reported and
// the intent abandoned, the lead staying unknown; the launch's own is adopted,
// the lead staying unknown too.
func TestSettleNeverSettlesALeadWhoseUnitIsLoaded(t *testing.T) {
	t.Run("another unit of the name", func(t *testing.T) {
		s := newSettleEnv(t)
		s.killedMidStart("alpha")
		s.clk.waitLongSleeps(t, 1)
		if err := s.fake.AddForeign(s.roleOf("alpha").UnitName(), "someone else's unit"); err != nil {
			t.Fatal(err)
		}
		s.clk.Advance(leadlifecycle.DefaultLateStartWindow + leadlifecycle.DefaultLateStartMargin + time.Second)
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, code, _, _ := s.shown("alpha"); code == "foreign-unit" {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("the lead is not reported:\n%s", s.log)
			}
			time.Sleep(5 * time.Millisecond)
		}
		s.stays("alpha", "unknown", 200*time.Millisecond)
		want(t, s.startLead("alpha"), leadwire.StatusRefused, codeLaunchRefused)
		if starts, signals := s.startsAndSignals(); starts != 0 || signals != 0 {
			t.Fatalf("%d starts and %d signals", starts, signals)
		}
		if e := s.finalEntry("alpha"); e.Intent.Outcome != leadregistry.OutcomeAbandoned || e.Shown.State != leadregistry.StateUnknown {
			t.Fatalf("entry %+v", e)
		}
	})
	t.Run("the unit of the launch", func(t *testing.T) {
		s := newSettleEnv(t)
		in := s.killedMidStart("alpha")
		s.clk.waitLongSleeps(t, 1)
		// The request that the killed daemon sent lands, late.
		if _, err := s.fake.Start(context.Background(), leadunit.Spec{
			Unit: in.UnitName, Epoch: in.Epoch, Intent: in.ID, Argv: []string{"/bin/true"}, WorkDir: "/",
		}); err != nil {
			t.Fatal(err)
		}
		s.clk.Advance(leadlifecycle.DefaultLateStartWindow + leadlifecycle.DefaultLateStartMargin + time.Second)
		deadline := time.Now().Add(10 * time.Second)
		for !strings.Contains(s.log.String(), "adopted") {
			if time.Now().After(deadline) {
				t.Fatalf("the unit was not adopted:\n%s", s.log)
			}
			time.Sleep(5 * time.Millisecond)
		}
		s.stays("alpha", "unknown", 200*time.Millisecond)
		if _, signals := s.startsAndSignals(); signals != 0 {
			t.Fatalf("%d signals", signals)
		}
		if e := s.finalEntry("alpha"); e.Intent.Outcome != leadregistry.OutcomeAdopted || e.Shown.State != leadregistry.StateUnknown {
			t.Fatalf("entry %+v", e)
		}
	})
}

// A lead shown unknown for a cut of the registry is the owner's: no timer is
// armed for it and it is not settled.
func TestSettleDoesNotTouchARegistryRecoveredLead(t *testing.T) {
	s := newSettleEnv(t)
	s.startDaemonOn()
	want(t, s.define("alpha"), leadwire.StatusDone, "")
	if err := s.running.stop(); err != nil {
		t.Fatal(err)
	}
	s.withRegistry(func(reg *leadregistry.Registry) {
		if _, err := reg.BeginLaunchWith(s.roleOf("alpha"), leadregistry.LaunchOptions{BootIdentity: s.fake.BootID()}); err != nil {
			t.Fatal(err)
		}
	})
	f, err := os.OpenFile(filepath.Join(s.state, RegistryDirName, leadregistry.FileName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":99,"time":"2026`); err != nil {
		t.Fatal(err)
	}
	f.Close()
	s.startDaemonOn()
	if state, code, _, _ := s.shown("alpha"); state != "unknown" || code != "registry-recovered" {
		t.Fatalf("shown %q (%s)\nlog:\n%s", state, code, s.log)
	}
	s.clk.Advance(time.Hour)
	time.Sleep(300 * time.Millisecond)
	if n := s.clk.longSleeps(); n != 0 {
		t.Fatalf("%d timers waited for a lead that is the owner's", n)
	}
	if state, code, _, _ := s.shown("alpha"); state != "unknown" || code != "registry-recovered" {
		t.Fatalf("shown %q (%s)", state, code)
	}
	if strings.Contains(s.log.String(), "scratch settle") {
		t.Fatalf("a timer was armed:\n%s", s.log)
	}
}

// The settled lead leaves max_running's count: a start that was refused at the
// cap is admitted once the timer has settled the unknown lead.
func TestSettleFreesTheLeadsPlaceUnderMaxRunning(t *testing.T) {
	s := newSettleEnv(t)
	s.w.Config.MaxRunning = 1
	s.resetScratch()
	s.killedMidStart("alpha", "beta")
	s.clk.waitLongSleeps(t, 1)
	s.holder("alpha", submitted)
	s.holder("beta", submitted)
	if res := s.list(); res.Running != 1 {
		t.Fatalf("running %d with one unknown", res.Running)
	}
	want(t, s.startLead("beta"), leadwire.StatusRefused, leadwire.CodeMaxRunning)
	s.clk.Advance(leadlifecycle.DefaultLateStartWindow + leadlifecycle.DefaultLateStartMargin + time.Second)
	s.waitState("alpha", "stopped")
	if res := s.list(); res.Running != 0 {
		t.Fatalf("running %d after the settle", res.Running)
	}
	want(t, s.startLead("beta"), leadwire.StatusDone, "")
}

// An owner's stop that lands after the window but before the timer's settle
// (the hook holds the timer there) settles the intent itself, and the timer
// finds nothing to settle.
func TestSettleStopRacingTheTimer(t *testing.T) {
	s := newSettleEnv(t)
	s.killedMidStart("alpha")
	s.clk.waitLongSleeps(t, 1)

	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	settleHook = func(stage string) {
		if stage != "settling" {
			return
		}
		once.Do(func() {
			close(reached)
			<-release
		})
	}
	t.Cleanup(func() { settleHook = nil })

	s.clk.Advance(leadlifecycle.DefaultLateStartWindow + leadlifecycle.DefaultLateStartMargin + time.Second)
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatalf("the timer did not reach the settle:\n%s", s.log)
	}
	r := s.stopLead("alpha", 0)
	want(t, r, leadwire.StatusDone, "")
	var res leadwire.StopResult
	result(t, r, &res)
	if res.State != "stopped" || res.Proof == "" {
		t.Fatalf("stop result %+v", res)
	}
	close(release)
	// The timer settles nothing more and ends.
	time.Sleep(200 * time.Millisecond)
	if state, _, _, _ := s.shown("alpha"); state != "stopped" {
		t.Fatalf("shown %q", state)
	}
	if starts, signals := s.startsAndSignals(); starts != 0 || signals != 0 {
		t.Fatalf("%d starts and %d signals", starts, signals)
	}
	if e := s.finalEntry("alpha"); e.Shown.State != leadregistry.StateStopped || e.Intent.Outcome != leadregistry.OutcomeAbandoned {
		t.Fatalf("entry %+v", e)
	}
}

// A start that lands after the window but before the timer's settle is refused
// (the lead is unknown); once the timer has settled the lead, the next start is
// admitted, and the two never run together.
func TestSettleStartRacingTheTimer(t *testing.T) {
	s := newSettleEnv(t)
	in := s.killedMidStart("alpha")
	s.holder("alpha", submitted)
	s.clk.waitLongSleeps(t, 1)

	reached := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	settleHook = func(stage string) {
		once.Do(func() {
			close(reached)
			<-release
		})
	}
	t.Cleanup(func() { settleHook = nil })

	s.clk.Advance(leadlifecycle.DefaultLateStartWindow + leadlifecycle.DefaultLateStartMargin + time.Second)
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatalf("the timer did not reach the settle:\n%s", s.log)
	}
	want(t, s.startLead("alpha"), leadwire.StatusRefused, codeLaunchRefused)
	if starts, _ := s.startsAndSignals(); starts != 0 {
		t.Fatalf("%d starts while the lead was unknown", starts)
	}
	close(release)
	s.waitState("alpha", "stopped")
	r := s.startLead("alpha")
	want(t, r, leadwire.StatusDone, "")
	var res leadwire.StartResult
	result(t, r, &res)
	if res.Epoch != in.Epoch+1 {
		t.Fatalf("epoch %d", res.Epoch)
	}
	if starts, signals := s.startsAndSignals(); starts != 1 || signals != 0 {
		t.Fatalf("%d starts and %d signals", starts, signals)
	}
}

// A role whose operation lock is held at the deadline (here a start in flight)
// is not waited for: the timer tries again later, and settles once the lock is
// free.
func TestSettleTriesAgainWhileTheRoleIsBusy(t *testing.T) {
	s := newSettleEnv(t)
	s.killedMidStart("alpha")
	s.holder("alpha", submitted)
	s.clk.waitLongSleeps(t, 1)

	held := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	leadActHook = func(op string) {
		if op == "start" {
			once.Do(func() {
				close(held)
				<-release
			})
		}
	}
	t.Cleanup(func() { leadActHook = nil })
	done := make(chan leadwire.Receipt, 1)
	go func() {
		var r leadwire.Receipt
		s.call(leadwire.OpStart, params("alpha", s.id(), nil), &r)
		done <- r
	}()
	<-held
	s.clk.Advance(leadlifecycle.DefaultLateStartWindow + leadlifecycle.DefaultLateStartMargin + time.Second)
	// The timer found the lock held and sleeps for its retry.
	s.clk.waitLongSleeps(t, 2)
	if state, _, _, _ := s.shown("alpha"); state != "unknown" {
		t.Fatalf("shown %q while the role is busy", state)
	}
	close(release)
	r := <-done
	want(t, r, leadwire.StatusRefused, codeLaunchRefused)
	s.clk.Advance(settleRetryFirst)
	s.waitState("alpha", "stopped")
}

// A manager that does not answer at the deadline leaves the lead as it is, and
// the timer tries again.
func TestSettleTriesAgainWhenTheManagerDoesNotAnswer(t *testing.T) {
	s := newSettleEnv(t)
	s.killedMidStart("alpha")
	s.clk.waitLongSleeps(t, 1)
	s.fake.MakeUnavailable(leadunit.MethodInspect, 1)
	s.clk.Advance(leadlifecycle.DefaultLateStartWindow + leadlifecycle.DefaultLateStartMargin + time.Second)
	s.clk.waitLongSleeps(t, 2)
	if state, _, _, _ := s.shown("alpha"); state != "unknown" {
		t.Fatalf("shown %q", state)
	}
	s.clk.Advance(settleRetryFirst)
	s.waitState("alpha", "stopped")
}

// The rule that stops a second unit of a role holds after the timer settled the
// lead: the start request of the abandoned launch lands late, the next start
// finds the unit loaded and is refused (nothing is started or signalled by it),
// and an explicit stop ends the unit and records the lead stopped. Until a
// start is refused the list shows the lead stopped, as docs/lead-daemon.md says.
func TestSettleALateLandingAfterTheSettleRefusesTheNextStart(t *testing.T) {
	s := newSettleEnv(t)
	in := s.killedMidStart("alpha")
	s.holder("alpha", submitted)
	s.clk.waitLongSleeps(t, 1)
	s.clk.Advance(leadlifecycle.DefaultLateStartWindow + leadlifecycle.DefaultLateStartMargin + time.Second)
	s.waitState("alpha", "stopped")

	// The request that the killed daemon sent lands, after the settle.
	if _, err := s.fake.Start(context.Background(), leadunit.Spec{
		Unit: in.UnitName, Epoch: in.Epoch, Intent: in.ID, Argv: []string{"/bin/true"}, WorkDir: "/",
	}); err != nil {
		t.Fatal(err)
	}
	startsBefore, signalsBefore := s.startsAndSignals()
	if startsBefore != 1 || signalsBefore != 0 {
		t.Fatalf("%d starts and %d signals before the owner's start, want the landing only", startsBefore, signalsBefore)
	}

	// The list still shows the lead stopped: nothing has looked at the unit.
	if state, _, _, _ := s.shown("alpha"); state != "stopped" {
		t.Fatalf("shown %q", state)
	}

	// The next start finds the unit and is refused; it starts and signals nothing.
	want(t, s.startLead("alpha"), leadwire.StatusRefused, codeUnitLoaded)
	if starts, signals := s.startsAndSignals(); starts != startsBefore || signals != signalsBefore {
		t.Fatalf("%d starts and %d signals after the refused start, want %d and %d", starts, signals, startsBefore, signalsBefore)
	}

	// An explicit stop ends the unit.
	want(t, s.stopLead("alpha", 0), leadwire.StatusDone, "")
	if state, _, _, _ := s.shown("alpha"); state != "stopped" {
		t.Fatalf("shown %q after the stop", state)
	}
	if st, err := s.fake.Inspect(context.Background(), in.UnitName); err != nil || st.Loaded {
		t.Fatalf("the unit after the stop: %+v (%v)", st, err)
	}
	if starts, _ := s.startsAndSignals(); starts != startsBefore {
		t.Fatalf("%d starts", starts)
	}
}

// journalLines is how many records the registry's journal holds now.
func (s *settleEnv) journalLines() int {
	s.t.Helper()
	b, err := os.ReadFile(filepath.Join(s.state, RegistryDirName, leadregistry.FileName))
	if err != nil {
		s.t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// A control group that is not proven empty at the deadline leaves the lead
// unknown with the reason control-group-not-proven-empty, and the timer, which
// that reason keeps armed, tries again at growing waits: once the group is
// empty the lead ends stopped, with no restart. The tries add no line to the
// registry's journal after the first that changes the reason.
func TestSettleTriesAgainWhileTheControlGroupIsNotEmpty(t *testing.T) {
	s := newSettleEnv(t)
	s.killedMidStart("alpha")
	s.clk.waitLongSleeps(t, 1)
	unit := s.roleOf("alpha").UnitName()
	s.fake.LeaveControlGroup(unit, true)

	s.clk.Advance(leadlifecycle.DefaultLateStartWindow + leadlifecycle.DefaultLateStartMargin + time.Second)
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, code, _, _ := s.shown("alpha"); code == codeGroupNotEmpty {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the lead is not shown with the group's reason:\n%s", s.log)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The timer is waiting for its retry.
	s.clk.waitLongSleeps(t, 2)
	lines := s.journalLines()

	// Several tries with the group still left: the lead stays unknown, nothing
	// is journaled, and the timer is still there.
	for i := 0; i < 4; i++ {
		n := s.clk.longSleeps()
		s.clk.Advance(settleRetryLongest)
		s.clk.waitLongSleeps(t, n+1)
	}
	if state, code, _, _ := s.shown("alpha"); state != "unknown" || code != codeGroupNotEmpty {
		t.Fatalf("shown %q (%s)", state, code)
	}
	if got := s.journalLines(); got != lines {
		t.Fatalf("the journal has %d records after the tries, %d before", got, lines)
	}
	if got := strings.Count(s.log.String(), "epoch 1: unknown ("+codeGroupNotEmpty+")"); got < 3 {
		t.Fatalf("%d tries logged, want each try:\n%s", got, s.log)
	}

	// The group is empty: the next try settles the lead.
	s.fake.LeaveControlGroup(unit, false)
	for i := 0; i < 20; i++ {
		if state, _, _, _ := s.shown("alpha"); state == "stopped" {
			break
		}
		s.clk.Advance(settleRetryLongest)
		time.Sleep(20 * time.Millisecond)
	}
	s.waitState("alpha", "stopped")
	if _, code, _, _ := s.shown("alpha"); code != "launch-abandoned" {
		t.Fatalf("reason %q", code)
	}
	if starts, signals := s.startsAndSignals(); starts != 0 || signals != 0 {
		t.Fatalf("%d starts and %d signals", starts, signals)
	}
	if e := s.finalEntry("alpha"); e.Shown.State != leadregistry.StateStopped || e.Intent.Outcome != leadregistry.OutcomeAbandoned {
		t.Fatalf("entry %+v", e)
	}
}

// A shutdown ends a timer that waits for its deadline, within the grace, and
// the log has no word of an observer left behind.
func TestSettleShutdownJoinsTheTimer(t *testing.T) {
	s := newSettleEnv(t)
	s.killedMidStart("alpha")
	s.clk.waitLongSleeps(t, 1)
	t0 := time.Now()
	if err := s.running.stop(); err != nil {
		t.Fatalf("the daemon did not stop clean: %v\nlog:\n%s", err, s.log)
	}
	if d := time.Since(t0); d > 10*time.Second {
		t.Fatalf("the stop took %s", d)
	}
	if strings.Contains(s.log.String(), "did not stop") {
		t.Fatalf("a timer was left:\n%s", s.log)
	}
	// The lead is as it was: nothing settled after the end.
	s.clk.Advance(time.Hour)
	time.Sleep(100 * time.Millisecond)
	s.withRegistry(func(reg *leadregistry.Registry) {
		e, err := reg.Get(s.roleOf("alpha"))
		if err != nil || e.Intent.Outcome != leadregistry.OutcomePending {
			t.Fatalf("entry %+v (%v)", e, err)
		}
	})
}

// The unit tests of the timers on their own: which lead is armed, one timer per
// lead and intent, replaced by another intent, cancelled, joined; none starts
// after the join.
func bareSettle(t *testing.T) (*leadMode, *leadRole) {
	t.Helper()
	m, lr, _ := barePromote(t)
	// barePromote launched the lead: the outcome is recorded, the lead starts
	// from there. The tests of the timers make their own intents.
	fake := leadunit.NewFake(leadunit.SystemClock{})
	ctl, err := leadlifecycle.New(leadlifecycle.Options{
		Registry: m.reg, Manager: fake, Preflight: refusePreflight{}, Holder: refuseHolder{},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.ctl = ctl
	return m, lr
}

// unknownPending records the lead as stopped (proven) and journals a new
// launch intent that stays pending, with the lead shown unknown for the code.
func unknownPending(t *testing.T, m *leadMode, lr *leadRole, code string) leadregistry.Intent {
	t.Helper()
	e, err := m.reg.Get(lr.role)
	if err != nil {
		t.Fatal(err)
	}
	if e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomePending {
		if _, err := m.reg.RecordOutcome(lr.role, e.Intent.ID, e.Epoch, leadregistry.OutcomeAbandoned, leadregistry.UnitEvidence{}); err != nil {
			t.Fatal(err)
		}
	}
	if e.Shown.State != leadregistry.StateStopped {
		if _, err := m.reg.SetState(lr.role, leadregistry.StateChange{
			Epoch: e.Epoch, State: leadregistry.StateStopped, Evidence: leadregistry.EvidenceObservation,
			Reason: leadregistry.Reason{Code: "test"}, Proof: "the test says so",
		}); err != nil {
			t.Fatal(err)
		}
	}
	in, err := m.reg.BeginLaunchWith(lr.role, leadregistry.LaunchOptions{BootIdentity: "boot"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.reg.SetState(lr.role, leadregistry.StateChange{
		Epoch: in.Epoch, State: leadregistry.StateUnknown, Evidence: leadregistry.EvidenceObservation,
		Reason: leadregistry.Reason{Code: code, Detail: "the test"},
	}); err != nil {
		t.Fatal(err)
	}
	return in
}

func timersOf(m *leadMode) map[leadrole.Role]*settler {
	m.settle.mu.Lock()
	defer m.settle.mu.Unlock()
	out := map[leadrole.Role]*settler{}
	for k, v := range m.settle.byRole {
		out[k] = v
	}
	return out
}

func TestSettleTimersAreArmedPerIntentAndCancelled(t *testing.T) {
	m, lr := bareSettle(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.startSettlers(ctx) // the lead is shown starting: no timer
	if n := len(timersOf(m)); n != 0 {
		t.Fatalf("%d timers for a lead that is starting", n)
	}

	// A code that is not a launch's: none.
	unknownPending(t, m, lr, "registry-recovered")
	m.armSettle(lr)
	if n := len(timersOf(m)); n != 0 {
		t.Fatalf("%d timers for registry-recovered", n)
	}

	for _, code := range []string{"launch-unsettled", "launch-reply-lost"} {
		in := unknownPending(t, m, lr, code)
		m.armSettle(lr)
		first := timersOf(m)[lr.role]
		if first == nil || first.intent != in.ID || first.epoch != in.Epoch {
			t.Fatalf("%s: timer %+v for intent %+v", code, first, in)
		}
		// The same intent keeps its timer.
		m.armSettle(lr)
		if again := timersOf(m)[lr.role]; again != first {
			t.Fatalf("%s: the timer was replaced for the same intent", code)
		}
	}
	// A new intent replaces the timer of the old one.
	old := timersOf(m)[lr.role]
	in := unknownPending(t, m, lr, "launch-unsettled")
	m.armSettle(lr)
	cur := timersOf(m)[lr.role]
	if cur == nil || cur == old || cur.intent != in.ID {
		t.Fatalf("timer %+v after a new intent %+v", cur, in)
	}
	if old.cancel == nil {
		t.Fatal("no cancel")
	}

	m.cancelSettle(lr)
	if n := len(timersOf(m)); n != 0 {
		t.Fatalf("%d timers after the cancel", n)
	}
	m.settle.wg.Wait() // the cancelled timers end

	// A lead whose intent is resolved is not armed.
	if _, err := m.reg.RecordOutcome(lr.role, in.ID, in.Epoch, leadregistry.OutcomeAbandoned, leadregistry.UnitEvidence{}); err != nil {
		t.Fatal(err)
	}
	m.armSettle(lr)
	if n := len(timersOf(m)); n != 0 {
		t.Fatalf("%d timers for a resolved intent", n)
	}
}

// A call that arms a timer reads the entry under the timers' lock: one that
// waits for the lock while the role is stopped and started again (a newer epoch
// and intent, unknown and pending) arms the timer of the newer intent, and does
// not replace it with one for the epoch it first saw. The test holds the lock,
// lets the call reach it, changes the lead and lets the call go.
func TestSettleArmReadsTheEntryUnderTheTimersLock(t *testing.T) {
	m, lr := bareSettle(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.startSettlers(ctx)
	first := unknownPending(t, m, lr, "launch-unsettled")

	m.settle.mu.Lock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.armSettle(lr)
	}()
	// The call is at the lock (or about to be: a call that reads late sees the
	// newer entry anyway, which is what the test checks).
	time.Sleep(100 * time.Millisecond)
	second := unknownPending(t, m, lr, "launch-unsettled")
	if second.Epoch == first.Epoch || second.ID == first.ID {
		t.Fatalf("the intent did not change: %+v %+v", first, second)
	}
	m.settle.mu.Unlock()
	<-done

	cur := timersOf(m)[lr.role]
	if cur == nil || cur.epoch != second.Epoch || cur.intent != second.ID {
		t.Fatalf("timer %+v, want epoch %d intent %s", cur, second.Epoch, second.ID)
	}
	m.cancelSettle(lr)
	m.settle.wg.Wait()
}

func TestSettleTimersAreJoinedAndNoneStartsAfter(t *testing.T) {
	m, lr := bareSettle(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.startSettlers(ctx)
	unknownPending(t, m, lr, "launch-unsettled")
	m.armSettle(lr)
	if n := len(timersOf(m)); n != 1 {
		t.Fatalf("%d timers", n)
	}
	if !m.joinObservers(5 * time.Second) {
		t.Fatal("the timer did not end")
	}
	if n := len(timersOf(m)); n != 0 {
		// The goroutine removes itself as it ends.
		t.Fatalf("%d timers after the join", n)
	}
	m.armSettle(lr)
	if n := len(timersOf(m)); n != 0 {
		t.Fatalf("a timer started after the join")
	}
}

// A timer settles nothing when the role is not what it was armed for: here the
// role is stopped and started again before the deadline.
func TestSettleTimerOfAnOldEpochSettlesNothing(t *testing.T) {
	m, lr := bareSettle(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.startSettlers(ctx)
	in := unknownPending(t, m, lr, "launch-unsettled")
	me := &settler{epoch: in.Epoch, intent: in.ID, cancel: func() {}}
	if _, ok := m.settleOf(lr, me); !ok {
		t.Fatal("the timer's lead is not found")
	}
	unknownPending(t, m, lr, "launch-reply-lost") // another epoch and intent
	if _, ok := m.settleOf(lr, me); ok {
		t.Fatal("the timer found a lead of another epoch")
	}
	if m.settleStep(ctx, lr, me) {
		t.Fatal("the timer asked to be tried again for a lead it is not for")
	}
}
