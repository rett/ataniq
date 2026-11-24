package seatlink

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatgate"
)

// waitCard is the n-th card of the fake desk, of the wire's form.
func waitCard(n int) ownerwire.SeatWaitCard {
	return ownerwire.SeatWaitCard{
		ID:            fmt.Sprintf("ap-%d", n),
		Kind:          ownerwire.CardKindApproval,
		BindingSHA256: strings.Repeat(fmt.Sprintf("%x", n%16), 64),
	}
}

// waitDesk is the daemon's side of seat.wait for the tests: a pending set the
// test changes, and the gate's hold. A script, when set, answers a wait
// itself.
type waitDesk struct {
	mu    sync.Mutex
	cards []ownerwire.SeatWaitCard
	wake  chan struct{}
	// hold is how long a wait is held with no change (real time).
	hold time.Duration
	// sinces is the since of each wait, in order.
	sinces []string
	// script, if not nil, is asked first with the 1-based number of the wait
	// and its since; handled true means its result or error is the answer.
	script func(n int, since string) (handled bool, err error)
	// held gets a value (without blocking) each time a wait begins to hold.
	held chan struct{}
	// clk, if not nil, is the clock the simulation moves; its readings are
	// recorded in at, one for each wait.
	clk *testClock
	at  []time.Time
	// afterHold, if not nil, runs when a wait's hold ends with no change,
	// under no lock, and may change the set.
	afterHold func(d *waitDesk)
}

func newWaitDesk() *waitDesk {
	return &waitDesk{wake: make(chan struct{}), hold: 10 * time.Second, held: make(chan struct{}, 64)}
}

// set replaces the pending set and wakes the held waits.
func (d *waitDesk) set(cards ...ownerwire.SeatWaitCard) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.cards = slices.Clone(cards)
	close(d.wake)
	d.wake = make(chan struct{})
}

func (d *waitDesk) result() json.RawMessage {
	cards := slices.Clone(d.cards)
	if cards == nil {
		cards = []ownerwire.SeatWaitCard{}
	}
	out, err := ownerwire.EncodeSeatWaitResult(ownerwire.SeatWaitResult{
		Version: ownerwire.PendingVersion(d.cards), Waiting: len(cards), Cards: cards,
	})
	if err != nil {
		panic(err)
	}
	return out
}

func (d *waitDesk) waits() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.sinces)
}

func (d *waitDesk) sinceLog() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.sinces)
}

func (d *waitDesk) handle(ctx context.Context, _ seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
	if op != ownerwire.OpSeatWait {
		return nil, seatgate.NewCodedError("unknown-op")
	}
	p, err := ownerwire.DecodeSeatWaitParams(params)
	if err != nil {
		return nil, seatgate.NewCodedError("bad-params")
	}
	d.mu.Lock()
	d.sinces = append(d.sinces, p.Since)
	n := len(d.sinces)
	if d.clk != nil {
		d.at = append(d.at, d.clk.now())
	}
	if d.script != nil {
		d.mu.Unlock()
		if handled, err := d.script(n, p.Since); handled {
			if err != nil {
				return nil, err
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.result(), nil
		}
		d.mu.Lock()
	}
	for {
		if ownerwire.PendingVersion(d.cards) != p.Since {
			defer d.mu.Unlock()
			return d.result(), nil
		}
		wake := d.wake
		d.mu.Unlock()
		select {
		case d.held <- struct{}{}:
		default:
		}
		timer := time.NewTimer(d.hold)
		select {
		case <-wake:
			timer.Stop()
			d.mu.Lock()
		case <-timer.C:
			if d.afterHold != nil {
				d.afterHold(d)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			return d.result(), nil
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		}
	}
}

// recorder records what the watcher sleeps for and sleeps a moment instead,
// so that a schedule of seconds runs at once. Its events, with the calls the
// desk saw, are in order.
type recorder struct {
	mu     sync.Mutex
	sleeps []time.Duration
	events []string
	// onSleep, if not nil, runs with each requested sleep.
	onSleep func(d time.Duration)
}

func (r *recorder) sleep(ctx context.Context, d time.Duration) error {
	r.mu.Lock()
	r.sleeps = append(r.sleeps, d)
	r.events = append(r.events, "sleep:"+d.String())
	f := r.onSleep
	r.mu.Unlock()
	if f != nil {
		f(d)
	}
	return sleepFor(ctx, time.Millisecond)
}

func (r *recorder) event(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()
}

func (r *recorder) log() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.events)
}

func (r *recorder) slept() []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.sleeps)
}

// watchRig is a link to a fake gate that serves seat.wait, with a watcher
// not yet started.
type watchRig struct {
	desk *waitDesk
	g    *gate
	c    *seatgate.Client
	d    *Daemon
	rec  *recorder
}

// fastTimes are the watcher's timings for a test that runs in real time, and
// the recorder's sleep.
func fastTimes(r *recorder) watchTimes {
	return watchTimes{
		call: 5 * time.Second, coalesce: 30 * time.Millisecond,
		backMin: 10 * time.Millisecond, backMax: 80 * time.Millisecond,
		full: 40 * time.Millisecond, idle: 5 * time.Millisecond,
		sleep: sleepFor,
	}
}

// newWatchRig serves desk with the capabilities given, connects a link with
// a clock that never fills the room, and waits for the connection. The
// watcher is not started.
func newWatchRig(t *testing.T, desk *waitDesk, caps []string) *watchRig {
	t.Helper()
	seat := newIdentity(t, "seat")
	g := serveGateCaps(t, []string{seat.Fingerprint()}, caps, desk.handle)
	d, c := dial(t, g, seat)
	r := &watchRig{desk: desk, g: g, c: c, d: d, rec: &recorder{}}
	t.Cleanup(func() { d.Close() })
	return r
}

// eventually polls cond until it holds or the time is out.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// next waits for the state to satisfy ok, looking each time the state
// changes, as the app does (the channel first, the state after).
func next(t *testing.T, d *Daemon, what string, ok func(WatchState) bool) WatchState {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		ch := d.WatchChanged()
		if s := d.Watch(); ok(s) {
			return s
		}
		select {
		case <-ch:
		case <-deadline:
			t.Fatalf("timed out: %s (state %+v)", what, d.Watch())
		}
	}
}

// goroutinesIn counts the goroutines whose stack holds name.
func goroutinesIn(name string) int {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	return strings.Count(string(buf), name)
}

// TestSeatLinkWaitRoomArithmetic: the waits' room is taken from the reads',
// the interview's from the reads' and the decisions', and the
// rooms and the pings are the gate's rate. (The compile-time checks in
// budget.go say the same; this keeps the numbers of the plan in view.)
func TestSeatLinkWaitRoomArithmetic(t *testing.T) {
	got := []int{pingRoom, decideRoom, publicRoom, readRoom, waitRoom, genesisRoom, chatRoom}
	if want := []int{6, 8, 10, 12, 6, 12, 6}; !slices.Equal(got, want) {
		t.Errorf("the rooms (pings, decisions, anyone's, reads, waits, genesis, chat) are %v, want %v", got, want)
	}
	if sum := pingRoom + decideRoom + publicRoom + readRoom + waitRoom + genesisRoom + chatRoom; sum != GateRate {
		t.Errorf("the rooms add up to %d, the gate's rate is %d", sum, GateRate)
	}
	// An idle watcher: one wait each WatchHold, the most in any window.
	if n := int(BudgetWindow/WatchHold) + 1; n != 4 || waitRoom-n != 2 {
		t.Errorf("an idle watcher sends %d waits a window, leaving %d", n, waitRoom-n)
	}
	if !slices.Contains(Caps(), CapWatch) {
		t.Errorf("the seat's hello lacks %q: %v", CapWatch, Caps())
	}
}

// TestSeatLinkBudgetTakeOrWait: with room the call is counted; without, it
// is not, and the wait is until the call that frees room ages out.
func TestSeatLinkBudgetTakeOrWait(t *testing.T) {
	clock := &testClock{t: decideNow}
	b := &budget{now: clock.now}
	for i := 0; i < waitRoom; i++ {
		if ok, _ := b.takeOrWait(kindWait); !ok {
			t.Fatalf("wait %d refused", i)
		}
		clock.advance(10 * time.Second)
	}
	// The first call was 60 s ago: it ages out in 20 s.
	ok, wait := b.takeOrWait(kindWait)
	if ok || wait != 20*time.Second {
		t.Fatalf("without room: %v, %v; want false, 20s", ok, wait)
	}
	if len(b.sent[kindWait]) != waitRoom {
		t.Errorf("a refused call was counted: %d", len(b.sent[kindWait]))
	}
	clock.advance(wait)
	if ok, _ := b.takeOrWait(kindWait); !ok {
		t.Error("no room after the wait")
	}
	// Reads have room of their own, which the waits did not take.
	if !b.take(kindRead) {
		t.Error("a read refused for the waits' room")
	}
}

// TestSeatLinkWatchNoCapability: a gate whose hello lacks "watch" gets no
// wait, and the state says nothing is watched, however long the watcher
// runs.
func TestSeatLinkWatchNoCapability(t *testing.T) {
	desk := newWaitDesk()
	r := newWatchRig(t, desk, []string{"pair", "read"})
	r.d.wt = fastTimes(r.rec)
	r.d.wt.sleep = r.rec.sleep
	eventually(t, "connected", func() bool { _, ok := r.c.Connected(); return ok })
	r.d.StartWatch()
	// Wait until the watcher has looked a few times.
	eventually(t, "idle looks", func() bool { return len(r.rec.slept()) >= 5 })
	if n := r.g.calls.Load(); n != 0 || desk.waits() != 0 {
		t.Errorf("the gate saw %d calls with no capability", n)
	}
	if s := r.d.Watch(); s.Watching || s.Offline || s.Version != "" || len(s.Cards) != 0 {
		t.Errorf("state %+v, want one that does not watch", s)
	}
	if n := len(r.d.budget.sent[kindWait]); n != 0 {
		t.Errorf("%d waits counted", n)
	}
}

// TestSeatLinkWatchNotStarted: a link whose watcher was never started says
// it does not watch, and its channel is never closed.
func TestSeatLinkWatchNotStarted(t *testing.T) {
	var d Daemon
	if s := d.Watch(); s.Watching || s.Offline || s.Version != "" || !s.At.IsZero() {
		t.Errorf("state %+v", s)
	}
	select {
	case <-d.WatchChanged():
		t.Error("the channel of a link that does not watch is closed")
	default:
	}
	if d.WatchChanged() != d.WatchChanged() {
		t.Error("two calls, two channels, with nothing published")
	}
	d.Close()
	d.StartWatch()
	if d.w != nil {
		t.Error("a watcher started after Close")
	}
}

// TestSeatLinkWatchPublishesChange: the first answer is published with the
// cards; a change while a wait is held is published within that wait's
// answer, with no other wait sent; a held wait that ends with no change
// publishes nothing.
func TestSeatLinkWatchPublishesChange(t *testing.T) {
	desk := newWaitDesk()
	desk.set(waitCard(1))
	r := newWatchRig(t, desk, []string{"pair", "read", "watch"})
	r.d.wt = fastTimes(r.rec)
	eventually(t, "connected", func() bool { _, ok := r.c.Connected(); return ok })
	r.d.StartWatch()
	s := next(t, r.d, "first answer", func(s WatchState) bool { return s.Version != "" })
	want := ownerwire.PendingVersion([]ownerwire.SeatWaitCard{waitCard(1)})
	if !s.Watching || s.Offline || s.Version != want || s.Waiting != 1 || s.More || s.At.IsZero() {
		t.Fatalf("state %+v", s)
	}
	c1 := waitCard(1)
	if len(s.Cards) != 1 || s.Cards[0] != (WatchCard{ID: c1.ID, Kind: c1.Kind, BindingSHA256: c1.BindingSHA256}) {
		t.Fatalf("cards %+v", s.Cards)
	}
	// The snapshot is the caller's: changing it changes nothing.
	s.Cards[0].ID = "x"
	if r.d.Watch().Cards[0].ID != c1.ID {
		t.Error("a snapshot shares the cards")
	}
	// A wait is now held, with the version as its since.
	<-desk.held
	waits := desk.waits()
	if got := desk.sinceLog()[waits-1]; got != want {
		t.Fatalf("since %q, want %q", got, want)
	}
	ch := r.d.WatchChanged()
	desk.set(waitCard(1), waitCard(2))
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatal("a change was not published")
	}
	s = next(t, r.d, "second card", func(s WatchState) bool { return s.Waiting == 2 })
	if len(s.Cards) != 2 || s.Cards[1].ID != "ap-2" {
		t.Errorf("cards %+v", s.Cards)
	}
	if got := desk.waits(); got != waits+1 && got != waits+2 {
		// The change was answered by the held wait (waits+0), and the
		// next wait may have begun.
		if got != waits {
			t.Errorf("%d waits, was %d", got, waits)
		}
	}
	// A hold that ends with no change publishes nothing.
	desk.mu.Lock()
	desk.hold = 20 * time.Millisecond
	desk.mu.Unlock()
	before := r.d.Watch()
	eventually(t, "two holds ended", func() bool { return desk.waits() >= waits+3 })
	after := r.d.Watch()
	if !watchSame(before, after) || !before.At.Equal(after.At) {
		t.Errorf("a wait with no change republished: %+v then %+v", before, after)
	}
}

// TestSeatLinkWatchCoalesces: after an answer that changed the set the next
// wait waits for the coalescing time; after one that did not (a hold that
// ended), it does not.
func TestSeatLinkWatchCoalesces(t *testing.T) {
	desk := newWaitDesk()
	r := newWatchRig(t, desk, []string{"pair", "read", "watch"})
	r.d.wt = fastTimes(r.rec)
	r.d.wt.coalesce = 2 * time.Second
	r.d.wt.sleep = r.rec.sleep
	// Waits 1 and 2 see the empty set; 3 sees a card; the rest hold.
	desk.hold = time.Minute
	desk.script = func(n int, since string) (bool, error) {
		r.rec.event(fmt.Sprintf("wait%d", n))
		switch n {
		case 1:
			return true, nil
		case 2:
			return true, nil
		case 3:
			desk.mu.Lock()
			desk.cards = []ownerwire.SeatWaitCard{waitCard(1)}
			desk.mu.Unlock()
			return true, nil
		}
		return false, nil
	}
	eventually(t, "connected", func() bool { _, ok := r.c.Connected(); return ok })
	r.d.StartWatch()
	eventually(t, "four waits", func() bool { return len(r.rec.log()) >= 6 })
	want := []string{"wait1", "sleep:2s", "wait2", "wait3", "sleep:2s", "wait4"}
	if got := r.rec.log()[:6]; !slices.Equal(got, want) {
		t.Errorf("events %v, want %v", got, want)
	}
}

// TestSeatLinkWatchBackoff: after a gate error (a code the gate answers with)
// the watcher waits 1, 2, 4, 8, 16, 30, 30 seconds between tries, and says
// nothing of the link: the gate answered, so the state is not offline (see
// TestSeatLinkWatchAnyAnswerLiftsOffline for the failures that are). An
// answer ends the run of failures, and the next failure starts again at 1
// second.
func TestSeatLinkWatchBackoff(t *testing.T) {
	desk := newWaitDesk()
	r := newWatchRig(t, desk, []string{"pair", "read", "watch"})
	r.d.wt = watchTimes{sleep: r.rec.sleep}
	var offlineSeen, watchingSeen atomic.Int64
	desk.script = func(n int, since string) (bool, error) {
		if n > 1 && n <= 8 && r.d.Watch().Offline {
			offlineSeen.Add(1)
		}
		if n > 1 && n <= 8 && r.d.Watch().Watching {
			watchingSeen.Add(1)
		}
		switch {
		case n <= 7:
			return true, seatgate.NewCodedError("failed")
		case n == 9:
			return true, seatgate.NewCodedError("failed")
		}
		return false, nil
	}
	eventually(t, "connected", func() bool { _, ok := r.c.Connected(); return ok })
	// A budget clock that never fills.
	r.d.budget.now = roomy()
	r.d.StartWatch()
	eventually(t, "ten waits", func() bool { return desk.waits() >= 10 })
	r.d.Close()
	s := time.Second
	want := []time.Duration{s, 2 * s, 4 * s, 8 * s, 16 * s, 30 * s, 30 * s,
		// wait 8 answered (the set is empty: changed from "", so the
		// coalescing wait); 9 failed, and starts again at 1 second.
		watchCoalesce, s}
	got := r.rec.slept()
	if len(got) < len(want) || !slices.Equal(got[:len(want)], want) {
		t.Errorf("sleeps %v, want %v first", got, want)
	}
	if n := offlineSeen.Load(); n != 0 {
		t.Errorf("the state said offline at %d of the 7 retries: the gate answered each", n)
	}
	if n := watchingSeen.Load(); n != 7 {
		t.Errorf("the state said it watches at %d of the 7 retries", n)
	}
}

// TestSeatLinkWatchAnyAnswerLiftsOffline: a call that fails without an answer
// of the gate's (here, one that outlasts the call's time) publishes offline,
// and the next answer of any kind lifts it at once, a coded refusal such as
// wait-full or bad-params as much as a result: a code proves the link is up,
// and the state must not say the desk is out of reach until the next wait is
// held and answered, which is up to 25 seconds later. A coded refusal alone
// never says offline.
func TestSeatLinkWatchAnyAnswerLiftsOffline(t *testing.T) {
	desk := newWaitDesk()
	desk.hold = time.Minute
	r := newWatchRig(t, desk, []string{"pair", "read", "watch"})
	times := fastTimes(nil)
	times.call = 60 * time.Millisecond
	r.d.wt = times
	// offlineAt is the state's offline mark as each wait arrived.
	var mu sync.Mutex
	offlineAt := map[int]bool{}
	desk.script = func(n int, since string) (bool, error) {
		mu.Lock()
		offlineAt[n] = r.d.Watch().Offline
		mu.Unlock()
		switch n {
		case 1, 3, 5:
			// No answer within the call's time: the caller's call fails
			// with its own timeout.
			time.Sleep(400 * time.Millisecond)
			return true, seatgate.NewCodedError("late")
		case 2:
			return true, seatgate.NewCodedError("bad-params")
		case 4:
			return true, seatgate.NewCodedError(ownerwire.CodeWaitFull)
		case 6:
			return true, seatgate.NewCodedError("unavailable")
		}
		return false, nil
	}
	eventually(t, "connected", func() bool { _, ok := r.c.Connected(); return ok })
	r.d.budget.now = roomy()
	r.d.StartWatch()
	eventually(t, "the seventh wait", func() bool { return desk.waits() >= 7 })
	<-desk.held
	mu.Lock()
	defer mu.Unlock()
	// Wait 1 found the state online; 1 timed out (offline); 2 arrived with
	// it offline and its code lifted it; 3 timed out; 4 arrived offline and
	// wait-full lifted it; 5 timed out; 6 arrived offline and a coded refusal
	// lifted it, and said nothing of offline itself; 7 arrived online.
	want := map[int]bool{1: false, 2: true, 3: false, 4: true, 5: false, 6: true, 7: false}
	for n, w := range want {
		if offlineAt[n] != w {
			t.Errorf("the state was offline=%v as wait %d arrived, want %v (%v)", offlineAt[n], n, w, offlineAt)
		}
	}
	if s := r.d.Watch(); s.Offline || !s.Watching {
		t.Errorf("state %+v", s)
	}
}

// TestSeatLinkWatchFullBacksOffFiveSeconds: wait-full is a wait of 5 seconds
// (not the doubling), and publishes nothing: the gate is up.
func TestSeatLinkWatchFullBacksOffFiveSeconds(t *testing.T) {
	desk := newWaitDesk()
	r := newWatchRig(t, desk, []string{"pair", "read", "watch"})
	r.d.wt = watchTimes{sleep: r.rec.sleep}
	var offline atomic.Bool
	desk.script = func(n int, since string) (bool, error) {
		if n == 3 && r.d.Watch().Offline {
			offline.Store(true)
		}
		if n <= 2 {
			return true, seatgate.NewCodedError(ownerwire.CodeWaitFull)
		}
		return false, nil
	}
	eventually(t, "connected", func() bool { _, ok := r.c.Connected(); return ok })
	r.d.budget.now = roomy()
	r.d.StartWatch()
	eventually(t, "an answer", func() bool { return r.d.Watch().Version != "" })
	got := r.rec.slept()
	if len(got) < 2 || got[0] != 5*time.Second || got[1] != 5*time.Second {
		t.Errorf("sleeps %v, want 5s, 5s first", got)
	}
	if offline.Load() {
		t.Error("wait-full published offline")
	}
	if s := r.d.Watch(); s.Offline || !s.Watching {
		t.Errorf("state %+v", s)
	}
}

// startable is a gate on a fixed address that can be stopped and started
// again, so that the link reconnects to it.
type restartable struct {
	t    *testing.T
	id   seatgate.Identity
	pins []string
	caps []string
	h    seatgate.HandlerFunc
	// rate is the gate's requests a minute, the default when 0.
	rate  int
	addr  string
	mu    sync.Mutex
	stopF func()
}

func (g *restartable) start() {
	g.t.Helper()
	g.mu.Lock()
	defer g.mu.Unlock()
	addr := g.addr
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	var ln net.Listener
	var err error
	for i := 0; i < 200; i++ {
		if ln, err = net.Listen("tcp", addr); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil {
		g.t.Fatal(err)
	}
	g.addr = ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		seatgate.Serve(ctx, ln, seatgate.ServerConfig{
			TLS: seatgate.ServerTLS(g.id, g.pins), Caps: g.caps, ShutdownGrace: 50 * time.Millisecond, RateMax: g.rate,
		}, g.h)
	}()
	var once sync.Once
	g.stopF = func() { once.Do(func() { cancel(); <-done }) }
}

func (g *restartable) stop() {
	g.mu.Lock()
	f := g.stopF
	g.mu.Unlock()
	if f != nil {
		f()
	}
}

// link connects a link to g that reconnects when the gate is back, with a
// budget clock that never fills; both are closed when the test ends.
func (g *restartable) link(t *testing.T, seat seatgate.Identity) (*Daemon, *seatgate.Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c, err := seatgate.NewClient(g.addr, seatgate.ClientConfig{
		TLS: seatgate.ClientTLS(seat, g.id.Fingerprint(), "gate"), Caps: Caps(),
		BackoffMin: 5 * time.Millisecond, BackoffMax: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	ran := make(chan struct{})
	go func() { defer close(ran); c.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-ran; c.Close() })
	d, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	d.budget.now = roomy()
	t.Cleanup(func() { d.Close() })
	eventually(t, "connected", func() bool { _, ok := c.Connected(); return ok })
	return d, c
}

// TestSeatLinkWatchOfflineAndBack: when the gate goes away the state says
// offline, keeping the last version and cards; when it is back (the link
// reconnected) the state says online without waiting for an answer, and the
// next wait carries the version kept as its since. A change made while it
// was away is then published.
func TestSeatLinkWatchOfflineAndBack(t *testing.T) {
	desk := newWaitDesk()
	desk.set(waitCard(1))
	seat := newIdentity(t, "seat")
	g := &restartable{t: t, id: newIdentity(t, "gate"), pins: []string{seat.Fingerprint()}, caps: []string{"pair", "read", "watch"}, h: desk.handle}
	g.start()
	t.Cleanup(g.stop)
	d, _ := g.link(t, seat)
	d.wt = fastTimes(nil)
	d.StartWatch()
	v1 := ownerwire.PendingVersion([]ownerwire.SeatWaitCard{waitCard(1)})
	next(t, d, "first answer", func(s WatchState) bool { return s.Version == v1 && !s.Offline })
	<-desk.held

	g.stop()
	s := next(t, d, "offline", func(s WatchState) bool { return s.Offline })
	if !s.Watching || s.Version != v1 || s.Waiting != 1 || len(s.Cards) != 1 {
		t.Errorf("offline state %+v lost what it knew", s)
	}
	// While away, the set changes.
	desk.set(waitCard(1), waitCard(2))
	g.start()
	next(t, d, "online again", func(s WatchState) bool { return !s.Offline })
	v2 := ownerwire.PendingVersion([]ownerwire.SeatWaitCard{waitCard(1), waitCard(2)})
	s = next(t, d, "the change made while away", func(s WatchState) bool { return s.Version == v2 })
	if s.Offline || s.Waiting != 2 {
		t.Errorf("state %+v", s)
	}
	sinces := desk.sinceLog()
	if len(sinces) < 2 || sinces[len(sinces)-1] != v1 && sinces[len(sinces)-2] != v1 {
		t.Errorf("the waits' since values %v: the version kept (%s) was not sent after the reconnect", sinces, v1)
	}
	// The wait after the one that returned the change is v2.
	<-desk.held
	if got := desk.sinceLog(); got[len(got)-1] != v2 {
		t.Errorf("last since %q, want %q", got[len(got)-1], v2)
	}
}

// TestSeatLinkWatchRoomSimulation: a long run on a simulated clock, with
// holds of any length (up to the gate's 25 seconds, or none) and changes at
// random, and a watcher whose coalescing wait, backoffs and sleeps for room
// move the same clock. In no BudgetWindow does it send more waits than its
// room, it never sees an error for room (it sleeps), and the state is never
// offline.
func TestSeatLinkWatchRoomSimulation(t *testing.T) {
	desk := newWaitDesk()
	clk := &testClock{t: decideNow}
	desk.clk = clk
	rnd := rand.New(rand.NewPCG(3, 4))
	const total = 600
	var seq atomic.Int64
	desk.script = func(n int, since string) (bool, error) {
		// The hold: it took a random time, up to the gate's.
		switch rnd.IntN(3) {
		case 0:
			// A change at once.
			desk.mu.Lock()
			desk.cards = []ownerwire.SeatWaitCard{waitCard(int(seq.Add(1)))}
			desk.mu.Unlock()
		case 1:
			clk.advance(time.Duration(rnd.IntN(int(WatchHold/time.Millisecond))) * time.Millisecond)
			desk.mu.Lock()
			desk.cards = []ownerwire.SeatWaitCard{waitCard(int(seq.Add(1)))}
			desk.mu.Unlock()
		default:
			clk.advance(WatchHold)
		}
		return true, nil
	}
	// The gate's own rate limit runs on the real clock, which the
	// simulation outruns: the gate here takes any rate.
	seat := newIdentity(t, "seat")
	g := &restartable{t: t, id: newIdentity(t, "gate"), pins: []string{seat.Fingerprint()}, caps: []string{"pair", "read", "watch"}, h: desk.handle, rate: 1 << 30}
	g.start()
	t.Cleanup(g.stop)
	d, _ := g.link(t, seat)
	r := &watchRig{desk: desk, d: d}
	r.d.budget.now = clk.now
	var roomSleeps atomic.Int64
	var offline atomic.Bool
	r.d.wt = watchTimes{
		sleep: func(ctx context.Context, d time.Duration) error {
			clk.advance(d)
			if d != watchCoalesce {
				roomSleeps.Add(1)
			}
			if r.d.Watch().Offline {
				offline.Store(true)
			}
			return sleepFor(ctx, 50*time.Microsecond)
		},
	}
	r.d.StartWatch()
	eventually(t, "a long run", func() bool { return desk.waits() >= total })
	r.d.Close()
	desk.mu.Lock()
	at := slices.Clone(desk.at)
	desk.mu.Unlock()
	if len(at) < total {
		t.Fatalf("%d waits", len(at))
	}
	for i := range at {
		j := i
		for j < len(at) && at[j].Sub(at[i]) < BudgetWindow {
			j++
		}
		if n := j - i; n > waitRoom {
			t.Fatalf("%d waits in %v from %v, room %d", n, BudgetWindow, at[i], waitRoom)
		}
	}
	if offline.Load() {
		t.Error("a wait refused for room was shown as the gate being offline")
	}
	if roomSleeps.Load() == 0 {
		t.Error("the watcher never slept for room: the run did not reach it")
	}
	if n := len(r.d.budget.sent[kindWait]); n > waitRoom {
		t.Errorf("%d waits counted in the window", n)
	}
}

// TestSeatLinkWatchRoomExhaustedSleeps: with the room spent the watcher
// sleeps until room frees, sends nothing meanwhile, publishes nothing wrong
// and then sends.
func TestSeatLinkWatchRoomExhaustedSleeps(t *testing.T) {
	desk := newWaitDesk()
	clk := &testClock{t: decideNow}
	r := newWatchRig(t, desk, []string{"pair", "read", "watch"})
	r.d.budget.now = clk.now
	// The room is spent: 6 waits, 10 seconds apart; the first is 50 s old.
	for i := 0; i < waitRoom; i++ {
		clk.advance(10 * time.Second)
		if ok, _ := r.d.budget.takeOrWait(kindWait); !ok {
			t.Fatal("setup")
		}
	}
	slept := make(chan time.Duration, 4)
	r.d.wt = watchTimes{sleep: func(ctx context.Context, d time.Duration) error {
		select {
		case slept <- d:
		default:
		}
		clk.advance(d)
		return sleepFor(ctx, time.Millisecond)
	}}
	eventually(t, "connected", func() bool { _, ok := r.c.Connected(); return ok })
	r.d.StartWatch()
	// The oldest call of the 6 was made at +10 s, now is +60 s: it leaves
	// the window 30 s from now.
	select {
	case d := <-slept:
		if d != BudgetWindow-50*time.Second {
			t.Errorf("slept %v for room, want %v", d, BudgetWindow-50*time.Second)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watcher did not sleep for room")
	}
	next(t, r.d, "an answer after the room freed", func(s WatchState) bool { return s.Version != "" })
	if s := r.d.Watch(); s.Offline {
		t.Errorf("state %+v", s)
	}
	if desk.waits() != 1 {
		t.Errorf("%d waits", desk.waits())
	}
}

// TestSeatLinkWatchCloseLeavesNothing: Close ends the goroutine, even with a
// wait in flight at the gate, at once and every time; the state then says it
// does not watch and wakes whoever waits on it; no watcher starts after.
func TestSeatLinkWatchCloseLeavesNothing(t *testing.T) {
	if n := goroutinesIn("seatlink.(*watcher).run"); n != 0 {
		t.Fatalf("%d watcher goroutines before the test", n)
	}
	desk := newWaitDesk()
	r := newWatchRig(t, desk, []string{"pair", "read", "watch"})
	r.d.wt = fastTimes(r.rec)
	eventually(t, "connected", func() bool { _, ok := r.c.Connected(); return ok })
	r.d.StartWatch()
	r.d.StartWatch()
	eventually(t, "the watcher goroutine", func() bool { return goroutinesIn("seatlink.(*watcher).run") >= 1 })
	if n := goroutinesIn("seatlink.(*watcher).run"); n != 1 {
		t.Fatalf("%d watcher goroutines, want 1", n)
	}
	next(t, r.d, "first answer", func(s WatchState) bool { return s.Watching && s.Version != "" })
	<-desk.held
	ch := r.d.WatchChanged()
	done := make(chan struct{})
	go func() { r.d.Close(); r.d.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not return with a wait in flight")
	}
	if n := goroutinesIn("seatlink.(*watcher).run"); n != 0 {
		t.Errorf("%d watcher goroutines after Close", n)
	}
	select {
	case <-ch:
	default:
		t.Error("Close did not wake the subscribers")
	}
	if s := r.d.Watch(); s.Watching || s.Version != "" || len(s.Cards) != 0 {
		t.Errorf("state %+v after Close", s)
	}
	r.d.StartWatch()
	if n := goroutinesIn("seatlink.(*watcher).run"); n != 0 {
		t.Errorf("a watcher started after Close")
	}
}

// TestSeatLinkWatchBroadcastNeverBlocks: a change wakes any number of
// subscribers with no goroutine of the link's for each, publishing with
// nobody listening does not block, and the same state is not published
// twice.
func TestSeatLinkWatchBroadcastNeverBlocks(t *testing.T) {
	var h watchHub
	at := decideNow
	if !h.update(at, func(s *WatchState) { s.Watching = true }) {
		t.Fatal("a change was not published")
	}
	// Nobody listens: any number of publishes return.
	for i := 0; i < 1000; i++ {
		h.update(at, func(s *WatchState) { s.Waiting = i })
	}
	if h.update(at, func(s *WatchState) { s.Waiting = 999 }) {
		t.Error("the same state was published again")
	}
	before := runtime.NumGoroutine()
	chans := make([]<-chan struct{}, 200)
	for i := range chans {
		chans[i] = h.changed()
	}
	if !slices.Equal(chans[:2], []<-chan struct{}{chans[0], chans[0]}) {
		t.Error("two subscribers got two channels")
	}
	h.update(at, func(s *WatchState) { s.Version = "x" })
	for i, ch := range chans {
		select {
		case <-ch:
		default:
			t.Fatalf("subscriber %d was not woken", i)
		}
	}
	if h.changed() == chans[0] {
		t.Error("the closed channel was not replaced")
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Errorf("%d goroutines more after a broadcast", n-before)
	}
}
