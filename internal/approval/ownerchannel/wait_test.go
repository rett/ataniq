package ownerchannel

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

// fakeWaitClock is the wait's clock, on the clock of the stand-in door, so
// that the floor, the hold and the expiry all run on the time a test moves.
// A timer fires when Advance passes it.
type fakeWaitClock struct {
	clock *doortest.Clock

	mu     sync.Mutex
	timers []*fakeWaitTimer
	asked  []time.Duration
}

type fakeWaitTimer struct {
	at      time.Time
	ch      chan time.Time
	stopped bool
	fired   bool
	owner   *fakeWaitClock
}

func (f *fakeWaitClock) Now() time.Time { return f.clock.Now() }

func (f *fakeWaitClock) NewTimer(d time.Duration) waitTimer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeWaitTimer{at: f.clock.Now().Add(d), ch: make(chan time.Time, 1), owner: f}
	f.asked = append(f.asked, d)
	if d <= 0 {
		t.fired = true
		t.ch <- t.at
	} else {
		f.timers = append(f.timers, t)
	}
	return t
}

func (t *fakeWaitTimer) C() <-chan time.Time { return t.ch }

func (t *fakeWaitTimer) Stop() bool {
	t.owner.mu.Lock()
	defer t.owner.mu.Unlock()
	was := !t.stopped && !t.fired
	t.stopped = true
	return was
}

// Advance moves the clock and fires the timers that are due.
func (f *fakeWaitClock) Advance(d time.Duration) {
	now := f.clock.Advance(d)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.timers {
		if !t.stopped && !t.fired && !t.at.After(now) {
			t.fired = true
			t.ch <- now
		}
	}
}

// active is how many timers are set and neither fired nor stopped.
func (f *fakeWaitClock) active() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, t := range f.timers {
		if !t.stopped && !t.fired {
			n++
		}
	}
	return n
}

// asks returns the durations the timers were asked for, in order.
func (f *fakeWaitClock) asks() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Duration(nil), f.asked...)
}

// waitEnv is a service on the fake wait clock.
type waitEnv struct {
	*env
	wc *fakeWaitClock
}

func newWaitEnv(t *testing.T) *waitEnv {
	t.Helper()
	e := newEnv(t)
	e.start()
	wc := &fakeWaitClock{clock: e.clock}
	e.svc.wait.clk = wc
	return &waitEnv{env: e, wc: wc}
}

// waitOut is what a WaitPending call answered.
type waitOut struct {
	res ownerwire.SeatWaitResult
	err error
}

// startWait runs a wait in the background.
func (w *waitEnv) startWait(ctx context.Context, since string) <-chan waitOut {
	out := make(chan waitOut, 1)
	go func() {
		r, err := w.svc.WaitPending(ctx, since)
		out <- waitOut{r, err}
	}()
	return out
}

// until polls cond for a few real seconds.
func until(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// held waits until a wait has set its hold timer, so that a later advance
// does not come before it.
func (w *waitEnv) held(t *testing.T, timers int) {
	t.Helper()
	until(t, "the wait to be held", func() bool { return w.wc.active() >= timers })
}

// drive advances the fake clock a step at a time until the wait answers, and
// returns the answer and how far the clock moved.
func (w *waitEnv) drive(t *testing.T, out <-chan waitOut, step time.Duration, most int) (waitOut, time.Duration) {
	t.Helper()
	var moved time.Duration
	for i := 0; i < most; i++ {
		select {
		case r := <-out:
			return r, moved
		case <-time.After(15 * time.Millisecond):
		}
		w.wc.Advance(step)
		moved += step
	}
	select {
	case r := <-out:
		return r, moved
	case <-time.After(100 * time.Millisecond):
	}
	t.Fatalf("the wait did not answer after %s of the fake clock", moved)
	return waitOut{}, moved
}

// answered returns the answer of a wait that must come with no time passing.
func answered(t *testing.T, out <-chan waitOut) waitOut {
	t.Helper()
	select {
	case r := <-out:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not answer at once")
	}
	return waitOut{}
}

// now asks for the current set. A look is not made within a second of the
// last, so the clock moves on first.
func (w *waitEnv) now(t *testing.T) ownerwire.SeatWaitResult {
	t.Helper()
	w.wc.Advance(waitFloor)
	r, err := w.svc.WaitPending(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerwire.EncodeSeatWaitResult(r); err != nil {
		t.Fatalf("the result is not one the wire takes: %v", err)
	}
	return r
}

func ids(r ownerwire.SeatWaitResult) []string {
	out := []string{}
	for _, c := range r.Cards {
		out = append(out, c.ID)
	}
	return out
}

func TestWaitAnswersTheCurrentSetAtOnceWithNoSince(t *testing.T) {
	w := newWaitEnv(t)
	r := w.now(t)
	if r.Waiting != 0 || r.More || len(r.Cards) != 0 || r.Cards == nil {
		t.Fatalf("an empty door answered %+v", r)
	}
	if r.Version != ownerwire.PendingVersion(nil) {
		t.Fatalf("the empty set's version = %s", r.Version)
	}
	a := w.card("feature-a")
	r = w.now(t)
	if r.Waiting != 1 || r.More || len(r.Cards) != 1 {
		t.Fatalf("one card answered %+v", r)
	}
	c := r.Cards[0]
	if c.ID != a.ID || c.Kind != ownerwire.CardKindApproval || c.BindingSHA256 != a.BindingSHA256 || c.Held {
		t.Fatalf("the card is %+v, want %s %s", c, a.ID, a.BindingSHA256)
	}
	if r.Version != ownerwire.PendingVersion(r.Cards) {
		t.Fatal("the version is not the whole set's")
	}
}

func TestWaitAnswersAtOnceWhenSinceIsNotTheVersion(t *testing.T) {
	w := newWaitEnv(t)
	w.card("feature-a")
	stale := strings.Repeat("0", ownerwire.SeatWaitVersionLength)
	got := answered(t, w.startWait(context.Background(), stale))
	if got.err != nil || got.res.Waiting != 1 || got.res.Version == stale {
		t.Fatalf("answered %+v", got)
	}
	if w.wc.active() != 0 {
		t.Fatalf("%d timers are left", w.wc.active())
	}
}

func TestWaitHoldsUntilTheHoldEndsAndAnswersTheSameVersion(t *testing.T) {
	w := newWaitEnv(t)
	w.card("feature-a")
	first := w.now(t)
	out := w.startWait(context.Background(), first.Version)
	w.held(t, 1)
	got, moved := w.drive(t, out, time.Second, 40)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.res.Version != first.Version || got.res.Waiting != 1 {
		t.Fatalf("a hold with no change answered %+v, want the version %s", got.res, first.Version)
	}
	if moved < defaultWaitHold {
		t.Fatalf("answered after %s, before the hold of %s", moved, defaultWaitHold)
	}
	if w.wc.active() != 0 {
		t.Fatalf("%d timers are left", w.wc.active())
	}
}

func TestWaitHoldIsTheConfiguredOne(t *testing.T) {
	w := newWaitEnv(t)
	w.svc.cfg.WaitHold = 7 * time.Second
	first := w.now(t)
	out := w.startWait(context.Background(), first.Version)
	w.held(t, 1)
	got, moved := w.drive(t, out, time.Second, 40)
	if got.err != nil || got.res.Version != first.Version {
		t.Fatalf("answered %+v", got)
	}
	if moved < 7*time.Second || moved > 9*time.Second {
		t.Fatalf("answered after %s, want about 7s", moved)
	}
}

func TestWaitIsWokenByANewCard(t *testing.T) {
	w := newWaitEnv(t)
	a := w.card("feature-a")
	first := w.now(t)
	out := w.startWait(context.Background(), first.Version)
	w.held(t, 1)
	b := w.card("feature-b")
	got, moved := w.drive(t, out, time.Second, 40)
	if got.err != nil {
		t.Fatal(got.err)
	}
	if moved >= defaultWaitHold {
		t.Fatalf("woken after %s, which is the hold", moved)
	}
	if got.res.Version == first.Version || got.res.Waiting != 2 {
		t.Fatalf("answered %+v", got.res)
	}
	want := []string{a.ID, b.ID}
	sort.Strings(want)
	if fmt.Sprint(ids(got.res)) != fmt.Sprint(want) {
		t.Fatalf("cards %v, want %v", ids(got.res), want)
	}
}

func TestWaitIsWokenByADecision(t *testing.T) {
	w := newWaitEnv(t)
	a := w.card("feature-a")
	first := w.now(t)
	out := w.startWait(context.Background(), first.Version)
	w.held(t, 1)
	if _, err := w.decide(a, false); err != nil {
		t.Fatal(err)
	}
	got, _ := w.drive(t, out, time.Second, 40)
	if got.err != nil || got.res.Waiting != 0 || got.res.Version == first.Version {
		t.Fatalf("answered %+v", got)
	}
	if got.res.Version != ownerwire.PendingVersion(nil) {
		t.Fatal("an empty set did not answer the empty version")
	}
}

// A change made at the door, which the daemon did not write, is noticed at
// the next tick.
func TestWaitNoticesADoorChangeAtTheNextTick(t *testing.T) {
	w := newWaitEnv(t)
	a := w.card("feature-a")
	first := w.now(t)
	out := w.startWait(context.Background(), first.Version)
	w.held(t, 1)
	if err := w.stand.Void(a.ID); err != nil {
		t.Fatal(err)
	}
	// Nothing the service wrote: the wait is still held.
	w.wc.Advance(3 * time.Second)
	select {
	case r := <-out:
		t.Fatalf("answered before any poke: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	if err := w.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, _ := w.drive(t, out, time.Second, 40)
	if got.err != nil || got.res.Waiting != 0 {
		t.Fatalf("answered %+v", got)
	}
}

func TestWaitIsWokenByTheExpiryOfACard(t *testing.T) {
	w := newWaitEnv(t)
	w.svc.cfg.WaitHold = 48 * time.Hour
	a := w.card("feature-a")
	first := w.now(t)
	out := w.startWait(context.Background(), first.Version)
	// The hold timer and the expiry timer.
	w.held(t, 2)
	select {
	case r := <-out:
		t.Fatalf("answered early: %+v", r)
	case <-time.After(30 * time.Millisecond):
	}
	expires, err := record.ParseTime(a.Binding.PendingExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	w.wc.Advance(expires.Sub(w.clock.Now()) + time.Second)
	got, _ := w.drive(t, out, time.Second, 10)
	if got.err != nil || got.res.Waiting != 0 || got.res.Version == first.Version {
		t.Fatalf("answered %+v", got)
	}
	if n := w.wc.active(); n != 0 {
		t.Fatalf("%d timers are left", n)
	}
}

func TestWaitIsWokenByAHeldDecisionAndItsExpiry(t *testing.T) {
	// A held card stays pending; the mark changes the version.
	w := newWaitEnv(t)
	w.setLimit(1)
	w.useUp(1, "feature-u")
	a := w.card("feature-a")
	first := w.now(t)
	if first.Cards[0].Held {
		t.Fatal("the card is held before a decision")
	}
	out := w.startWait(context.Background(), first.Version)
	w.held(t, 1)
	if _, err := w.decide(a, true); err != nil {
		t.Fatal(err)
	}
	got, _ := w.drive(t, out, time.Second, 40)
	if got.err != nil || got.res.Waiting != 1 || !got.res.Cards[0].Held || got.res.Version == first.Version {
		t.Fatalf("answered %+v", got)
	}
}

func TestWaitCancelReturnsAtOnceAndLeavesNothing(t *testing.T) {
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	w := newWaitEnv(t)
	w.svc.cfg.WaitHold = 48 * time.Hour
	w.card("feature-a")
	first := w.now(t)
	ctx, cancel := context.WithCancel(context.Background())
	out := w.startWait(ctx, first.Version)
	w.held(t, 2)
	cancel()
	got := answered(t, out)
	if !errors.Is(got.err, context.Canceled) || got.res.Version != "" {
		t.Fatalf("a cancelled wait answered %+v", got)
	}
	if n := w.wc.active(); n != 0 {
		t.Fatalf("%d timers are left", n)
	}
	w.stop()
	settleGoroutines(t, before)
}

func TestWaitCancelledWhileWaitingOutTheFloor(t *testing.T) {
	w := newWaitEnv(t)
	first := w.now(t)
	ctx, cancel := context.WithCancel(context.Background())
	out := w.startWait(ctx, first.Version)
	w.held(t, 1)
	w.card("feature-a")
	// The poke came within the second of the last look: the wait is on the
	// floor's timer now, besides the hold's.
	w.held(t, 2)
	cancel()
	got := answered(t, out)
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("answered %+v", got)
	}
	if n := w.wc.active(); n != 0 {
		t.Fatalf("%d timers are left", n)
	}
}

func TestWaitEndsWithTheService(t *testing.T) {
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	w := newWaitEnv(t)
	first := w.now(t)
	out := w.startWait(context.Background(), first.Version)
	w.held(t, 1)
	w.stop()
	got := answered(t, out)
	if !errors.Is(got.err, ErrClosed) {
		t.Fatalf("answered %+v, want ErrClosed", got)
	}
	settleGoroutines(t, before)
	if _, err := w.svc.WaitPending(context.Background(), ""); !errors.Is(err, ErrClosed) {
		t.Fatalf("a closed service answered %v", err)
	}
}

func TestWaitHoldStaysInsideTheCallersDeadline(t *testing.T) {
	w := newWaitEnv(t)
	first := w.now(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out := w.startWait(ctx, first.Version)
	w.held(t, 1)
	asks := w.wc.asks()
	hold := asks[len(asks)-1]
	if hold <= 0 || hold > 600*time.Millisecond {
		t.Fatalf("the hold is %s inside a deadline of 2s", hold)
	}
	w.wc.Advance(time.Second)
	got := answered(t, out)
	if got.err != nil || got.res.Version != first.Version {
		t.Fatalf("answered %+v", got)
	}

	// Too close to the deadline for a hold: answered at once.
	tight, cancelTight := context.WithTimeout(context.Background(), time.Second)
	defer cancelTight()
	got = answered(t, w.startWait(tight, first.Version))
	if got.err != nil || got.res.Version != first.Version {
		t.Fatalf("answered %+v", got)
	}
}

func TestWaitVersionSurvivesARestart(t *testing.T) {
	w := newWaitEnv(t)
	w.card("feature-a")
	w.card("feature-b")
	before := w.now(t)
	w.restart()
	w.svc.wait.clk = w.wc
	after := w.now(t)
	if before.Version != after.Version || fmt.Sprint(ids(before)) != fmt.Sprint(ids(after)) {
		t.Fatalf("the version is %s after a restart, was %s", after.Version, before.Version)
	}
	// A wait that held the old version is not woken by the restart.
	out := w.startWait(context.Background(), before.Version)
	w.held(t, 1)
	select {
	case r := <-out:
		t.Fatalf("woken with nothing changed: %+v", r)
	case <-time.After(50 * time.Millisecond):
	}
	got, moved := w.drive(t, out, time.Second, 40)
	if got.err != nil || got.res.Version != before.Version || moved < defaultWaitHold {
		t.Fatalf("answered %+v after %s", got, moved)
	}
}

// Pokes within a second cause one look at the end of it.
func TestWaitLooksAtTheDoorAtMostOnceASecond(t *testing.T) {
	w := newWaitEnv(t)
	w.card("feature-a")
	first := w.now(t)
	var outs []<-chan waitOut
	for i := 0; i < 3; i++ {
		outs = append(outs, w.startWait(context.Background(), first.Version))
	}
	w.held(t, 3)
	lists := func() int {
		w.door.mu.Lock()
		defer w.door.mu.Unlock()
		return w.door.lists
	}
	quiet := func() {
		time.Sleep(60 * time.Millisecond)
	}
	quiet()
	base := lists()
	for round := 1; round <= 3; round++ {
		for i := 0; i < 50; i++ {
			w.svc.poke()
		}
		quiet()
		if n := lists() - base; n != 0 {
			t.Fatalf("round %d: %d looks within the second of the last", round, n)
		}
		w.wc.Advance(time.Second)
		quiet()
		if n := lists() - base; n != 1 {
			t.Fatalf("round %d: %d looks in the second, want 1", round, n)
		}
		base = lists()
		w.wc.Advance(100 * time.Millisecond)
		for i := 0; i < 50; i++ {
			w.svc.poke()
		}
		// Within the second of that look again.
		quiet()
		if n := lists() - base; n != 0 {
			t.Fatalf("round %d: %d looks inside the next second", round, n)
		}
		w.wc.Advance(time.Second)
		quiet()
		if n := lists() - base; n != 1 {
			t.Fatalf("round %d: %d looks after the next second, want 1", round, n)
		}
		base = lists()
	}
	select {
	case r := <-outs[0]:
		t.Fatalf("answered with nothing changed: %+v", r)
	default:
	}
	// A look that found nothing pokes nothing: the waits are quiet.
	w.wc.Advance(5 * time.Second)
	quiet()
	if n := lists() - base; n != 0 {
		t.Fatalf("%d looks with no poke", n)
	}
	w.stop()
	for _, o := range outs {
		<-o
	}
}

// The look's own ExpireHeld writes (when it closes a held card) and pokes: a
// wait does not spin on it.
func TestWaitDoesNotSpinOnItsOwnLook(t *testing.T) {
	w := newWaitEnv(t)
	w.svc.cfg.WaitHold = 48 * time.Hour
	w.setLimit(1)
	w.useUp(1, "feature-u")
	a := w.card("feature-a")
	w.resetRef()
	if _, err := w.decide(a, true); err != nil {
		t.Fatal(err)
	}
	first := w.now(t)
	if len(first.Cards) != 1 || !first.Cards[0].Held {
		t.Fatalf("the card is not held: %+v", first)
	}
	out := w.startWait(context.Background(), first.Version)
	w.held(t, 2)
	w.door.mu.Lock()
	base := w.door.lists
	w.door.mu.Unlock()
	// The pending record ends: ExpireHeld closes the held card at the look.
	expires, err := record.ParseTime(a.Binding.PendingExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	w.wc.Advance(expires.Sub(w.clock.Now()) + time.Second)
	got, _ := w.drive(t, out, time.Second, 10)
	if got.err != nil || got.res.Waiting != 0 {
		t.Fatalf("answered %+v", got)
	}
	w.door.mu.Lock()
	n := w.door.lists - base
	w.door.mu.Unlock()
	if n > 3 {
		t.Fatalf("%d looks for one expiry", n)
	}
}

func TestWaitIsRefusedPastTheLimitAndFreesASlotOnCancel(t *testing.T) {
	w := newWaitEnv(t)
	w.svc.cfg.WaitMax = 2
	first := w.now(t)
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	outA := w.startWait(ctxA, first.Version)
	outB := w.startWait(ctxB, first.Version)
	w.held(t, 2)
	for _, since := range []string{first.Version, "", strings.Repeat("1", 32)} {
		if _, err := w.svc.WaitPending(context.Background(), since); !errors.Is(err, ErrWaitFull) {
			t.Fatalf("a third wait (since %q) answered %v, want ErrWaitFull", since, err)
		}
	}
	cancelA()
	if got := answered(t, outA); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("answered %+v", got)
	}
	if _, err := w.svc.WaitPending(context.Background(), ""); err != nil {
		t.Fatalf("after a slot was freed: %v", err)
	}
	// And the slot is taken again by a held one.
	outC := w.startWait(context.Background(), first.Version)
	w.held(t, 2)
	if _, err := w.svc.WaitPending(context.Background(), ""); !errors.Is(err, ErrWaitFull) {
		t.Fatalf("answered %v, want ErrWaitFull", err)
	}
	cancelB()
	<-outB
	w.stop()
	<-outC
}

func TestWaitConfigRefusesNegativeValues(t *testing.T) {
	for _, edit := range []func(*Config){
		func(c *Config) { c.WaitHold = -time.Second },
		func(c *Config) { c.WaitMax = -1 },
	} {
		e := newEnv(t)
		st, err := ownerjournal.Open(e.path)
		if err != nil {
			t.Fatal(err)
		}
		cfg := e.config(st)
		edit(&cfg)
		if svc, err := New(context.Background(), cfg); err == nil {
			_ = svc.Close()
			t.Fatal("New took a negative wait setting")
		}
		_ = st.Close()
	}
}

func TestWaitResultCapsTheCardsAndKeepsTheWholeSetInTheVersion(t *testing.T) {
	var all []ownerwire.SeatWaitCard
	for i := 0; i < 300; i++ {
		all = append(all, ownerwire.SeatWaitCard{
			ID:            fmt.Sprintf("ap-%04d", 300-i),
			Kind:          ownerwire.CardKindApproval,
			BindingSHA256: strings.Repeat("ab", 32),
			Held:          i%7 == 0,
		})
	}
	res := waitResultOf(append([]ownerwire.SeatWaitCard(nil), all...))
	if len(res.Cards) != ownerwire.MaxSeatWaitCards || res.Waiting != 300 || !res.More {
		t.Fatalf("cards %d, waiting %d, more %v", len(res.Cards), res.Waiting, res.More)
	}
	if res.Version != ownerwire.PendingVersion(all) {
		t.Fatal("the version is not the whole set's")
	}
	for i := 1; i < len(res.Cards); i++ {
		if !waitCardLess(res.Cards[i-1], res.Cards[i]) {
			t.Fatalf("cards %d and %d are out of order", i-1, i)
		}
	}
	// The listed ones are the first of the whole set in the version's order.
	if res.Cards[0].ID != "ap-0001" || res.Cards[127].ID != "ap-0128" {
		t.Fatalf("listed %s to %s", res.Cards[0].ID, res.Cards[127].ID)
	}
	if _, err := ownerwire.EncodeSeatWaitResult(res); err != nil {
		t.Fatalf("a capped result is refused by the wire: %v", err)
	}

	// A change past the list changes the version and not the cards.
	changed := append([]ownerwire.SeatWaitCard(nil), all...)
	for i := range changed {
		if changed[i].ID == "ap-0300" {
			changed[i].Held = !changed[i].Held
		}
	}
	other := waitResultOf(changed)
	if other.Version == res.Version {
		t.Fatal("a change past the cards did not change the version")
	}
	if fmt.Sprint(other.Cards) != fmt.Sprint(res.Cards) {
		t.Fatal("a change past the cards changed the cards")
	}

	exact := waitResultOf(append([]ownerwire.SeatWaitCard(nil), all[:128]...))
	if exact.More || exact.Waiting != 128 || len(exact.Cards) != 128 {
		t.Fatalf("128 cards: waiting %d, more %v", exact.Waiting, exact.More)
	}
	if empty := waitResultOf(nil); empty.Cards == nil || empty.Waiting != 0 || empty.More {
		t.Fatalf("no cards: %+v", empty)
	}
}

func TestWaitCardsAreInTheOrderTheVersionSorts(t *testing.T) {
	w := newWaitEnv(t)
	for _, n := range []string{"feature-c", "feature-a", "feature-b"} {
		w.card(n)
	}
	r := w.now(t)
	if len(r.Cards) != 3 || !sort.SliceIsSorted(r.Cards, func(i, j int) bool { return r.Cards[i].ID < r.Cards[j].ID }) {
		t.Fatalf("cards %v", ids(r))
	}
	reversed := []ownerwire.SeatWaitCard{r.Cards[2], r.Cards[1], r.Cards[0]}
	if ownerwire.PendingVersion(reversed) != r.Version {
		t.Fatal("the version depends on the order")
	}
}

// Waits and the service's writes at once, on the real clock: the poke is
// taken from under the journal's and the service's locks, and nothing is left
// behind.
func TestWaitsAndWritesTogether(t *testing.T) {
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()
	e := newEnv(t)
	e.start()
	e.svc.cfg.WaitHold = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var woken sync.Map
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			since := ""
			for ctx.Err() == nil {
				r, err := e.svc.WaitPending(ctx, since)
				if err != nil {
					if !errors.Is(err, ErrWaitFull) && ctx.Err() == nil {
						t.Errorf("wait %d: %v", i, err)
					}
					return
				}
				if since != "" && r.Version != since {
					woken.Store(r.Version, true)
				}
				since = r.Version
			}
		}(i)
	}
	time.Sleep(50 * time.Millisecond)
	woke := func() int {
		n := 0
		woken.Range(func(_, _ any) bool { n++; return true })
		return n
	}
	var cards []record.Pending
	for _, n := range []string{"feature-a", "feature-b", "feature-c"} {
		cards = append(cards, e.card(n))
	}
	until(t, "a wait to be woken by the cards", func() bool { return woke() > 0 })
	seen := woke()
	for _, p := range cards {
		if _, err := e.decide(p, false); err != nil {
			t.Fatal(err)
		}
	}
	until(t, "a wait to be woken by the decisions", func() bool { return woke() > seen })
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	cancel()
	wg.Wait()
	e.stop()
	settleGoroutines(t, before)
}
