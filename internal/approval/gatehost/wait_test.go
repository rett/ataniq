package gatehost

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/seatgate"
)

// waitingPort is a fakePort that can wait, as the service does: WaitPending
// answers at once when since is empty or is not the version of the pending
// set it holds, and otherwise holds the call until the set changes (set), the
// hold ends, or ctx is done. It keeps what it was asked.
type waitingPort struct {
	fakePort

	wmu   sync.Mutex
	cards []ownerwire.SeatWaitCard
	wake  chan struct{}
	// hold is how long a wait is held; zero is a second.
	hold time.Duration
	// err, when set, is what WaitPending answers at once.
	werr error

	active    int
	sinces    []string
	deadlines []bool
	ctxDeadAt []time.Time
}

func (w *waitingPort) result() ownerwire.SeatWaitResult {
	cards := append([]ownerwire.SeatWaitCard{}, w.cards...)
	return ownerwire.SeatWaitResult{Version: ownerwire.PendingVersion(cards), Waiting: len(cards), Cards: cards}
}

// set replaces the pending set and wakes the held waits.
func (w *waitingPort) set(cards ...ownerwire.SeatWaitCard) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	w.cards = cards
	if w.wake != nil {
		close(w.wake)
		w.wake = nil
	}
}

// version is the version of the pending set now.
func (w *waitingPort) version() string {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	return w.result().Version
}

// heldNow is how many calls are inside WaitPending.
func (w *waitingPort) heldNow() int {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	return w.active
}

func (w *waitingPort) calls() (sinces []string, deadlines []time.Time, all bool) {
	w.wmu.Lock()
	defer w.wmu.Unlock()
	all = true
	for _, d := range w.deadlines {
		all = all && d
	}
	return append([]string(nil), w.sinces...), append([]time.Time(nil), w.ctxDeadAt...), all
}

func (w *waitingPort) WaitPending(ctx context.Context, since string) (ownerwire.SeatWaitResult, error) {
	dl, hasDl := ctx.Deadline()
	w.wmu.Lock()
	w.sinces = append(w.sinces, since)
	w.deadlines = append(w.deadlines, hasDl)
	w.ctxDeadAt = append(w.ctxDeadAt, dl)
	if w.werr != nil {
		err := w.werr
		w.wmu.Unlock()
		return ownerwire.SeatWaitResult{}, err
	}
	w.active++
	w.wmu.Unlock()
	defer func() {
		w.wmu.Lock()
		w.active--
		w.wmu.Unlock()
	}()
	hold := w.hold
	if hold == 0 {
		hold = time.Second
	}
	end := time.NewTimer(hold)
	defer end.Stop()
	for {
		w.wmu.Lock()
		res := w.result()
		if since == "" || since != res.Version {
			w.wmu.Unlock()
			return res, nil
		}
		if w.wake == nil {
			w.wake = make(chan struct{})
		}
		ch := w.wake
		w.wmu.Unlock()
		select {
		case <-ch:
		case <-end.C:
			w.wmu.Lock()
			defer w.wmu.Unlock()
			return w.result(), nil
		case <-ctx.Done():
			return ownerwire.SeatWaitResult{}, ctx.Err()
		}
	}
}

var _ WaitPort = (*waitingPort)(nil)

func aCard(id string) ownerwire.SeatWaitCard {
	return ownerwire.SeatWaitCard{ID: id, Kind: ownerwire.CardKindApproval, BindingSHA256: strings.Repeat("ab", 32)}
}

// waitGate is a handler behind the real gate on the loopback address, with
// the seat's key pinned.
type waitGate struct {
	t      *testing.T
	h      *Handler
	lb     *logBook
	addr   string
	gateID seatgate.Identity
	seatID seatgate.Identity
}

// serveWait serves a handler made over f (with newHandler's log) through
// seatgate.Serve with the given request deadline.
func serveWait(t *testing.T, f Port, requestTimeout time.Duration) *waitGate {
	t.Helper()
	g := &waitGate{t: t, gateID: newIdentity(t, "gate"), seatID: newIdentity(t, "seat"), lb: &logBook{}}
	var err error
	g.h, err = New(f, func(fp string) bool { return fp == g.seatID.Fingerprint() }, g.lb.logf)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g.addr = ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	cfg := seatgate.ServerConfig{TLS: seatgate.ServerTLS(g.gateID, []string{g.seatID.Fingerprint()}), Caps: g.h.Caps(), RequestTimeout: requestTimeout}
	go func() { served <- seatgate.Serve(ctx, ln, cfg, g.h) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-served:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("Serve did not end")
		}
	})
	return g
}

// dial connects the seat to the gate. The connection is closed at the end of
// the test, if the test has not closed it.
func (g *waitGate) dial() *seatgate.Client {
	g.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := seatgate.Dial(ctx, g.addr, seatgate.ClientConfig{TLS: seatgate.ClientTLS(g.seatID, g.gateID.Fingerprint(), "gate"), Caps: []string{"pair", "read", "watch"}})
	if err != nil {
		g.t.Fatal(err)
	}
	g.t.Cleanup(func() { c.Close() })
	return c
}

// waitCall is the answer of one seat.wait, delivered on a channel.
type waitCall struct {
	res ownerwire.SeatWaitResult
	err error
}

func sinceParams(t testing.TB, since string) json.RawMessage {
	t.Helper()
	b, err := ownerwire.EncodeSeatWaitParams(ownerwire.SeatWaitParams{Since: since})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// startWait sends a seat.wait on c in a goroutine of its own.
func startWait(c *seatgate.Client, since string) <-chan waitCall {
	out := make(chan waitCall, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var res ownerwire.SeatWaitResult
		params, err := ownerwire.EncodeSeatWaitParams(ownerwire.SeatWaitParams{Since: since})
		if err == nil {
			err = c.Call(ctx, ownerwire.OpSeatWait, json.RawMessage(params), &res)
		}
		out <- waitCall{res, err}
	}()
	return out
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func remoteCode(err error) string {
	var re *seatgate.RemoteError
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

func (g *waitGate) heldFor(fp string) int {
	g.h.waits.mu.Lock()
	defer g.h.waits.mu.Unlock()
	return g.h.waits.held[fp]
}

// refusalsOf is how many counted refusals the bound holds for the seat.
func (g *waitGate) refusalsOf() int {
	g.h.bound.mu.Lock()
	defer g.h.bound.mu.Unlock()
	if p := g.h.bound.peers[g.seatID.Fingerprint()]; p != nil {
		return len(p.times)
	}
	return 0
}

// A service that can wait gives the handler watch, in the hello of the gate.
func TestWatchIsListedForAServiceThatCanWait(t *testing.T) {
	g := serveWait(t, &waitingPort{}, 0)
	c := g.dial()
	if !slices.Equal(c.Caps(), []string{"pair", "read", "watch"}) {
		t.Fatalf("the hello listed %q", c.Caps())
	}
}

// A wait on the current version is held, and a new card wakes it with the new
// version and the card.
func TestAWaitWakesOnANewCard(t *testing.T) {
	f := &waitingPort{hold: 15 * time.Second}
	g := serveWait(t, f, 0)
	c := g.dial()
	v0 := f.version()
	got := startWait(c, v0)
	eventually(t, "the wait is held", func() bool { return f.heldNow() == 1 })
	select {
	case r := <-got:
		t.Fatalf("answered before a change: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	f.set(aCard("ap-20261122-0001"))
	r := <-got
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.res.Version == v0 || r.res.Waiting != 1 || len(r.res.Cards) != 1 || r.res.Cards[0].ID != "ap-20261122-0001" || r.res.Version != f.version() {
		t.Fatalf("result %+v", r.res)
	}
	eventually(t, "the slot is given back", func() bool { return g.heldFor(g.seatID.Fingerprint()) == 0 })
	if g.refusalsOf() != 0 || len(g.lb.all()) != 0 {
		t.Fatalf("a served wait counted or logged: %d %q", g.refusalsOf(), g.lb.all())
	}
}

// A hold that ends with no change answers the same version, which the seat
// takes as "same".
func TestAWaitReturnsTheSameVersionAtTheEndOfTheHold(t *testing.T) {
	f := &waitingPort{hold: 250 * time.Millisecond}
	f.set(aCard("ap-20261122-0001"))
	g := serveWait(t, f, 0)
	c := g.dial()
	v0 := f.version()
	start := time.Now()
	r := <-startWait(c, v0)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if r.res.Version != v0 || r.res.Waiting != 1 {
		t.Fatalf("result %+v, want version %s", r.res, v0)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatalf("answered after %v, before the hold ended", time.Since(start))
	}
}

// A since that is not the current version, or none, is answered at once, and
// the params reach the service as sent.
func TestAWaitOnAnotherVersionReturnsAtOnce(t *testing.T) {
	f := &waitingPort{hold: 20 * time.Second}
	f.set(aCard("ap-20261122-0001"))
	g := serveWait(t, f, 0)
	c := g.dial()
	other := strings.Repeat("0", ownerwire.SeatWaitVersionLength)
	for _, since := range []string{other, ""} {
		start := time.Now()
		r := <-startWait(c, since)
		if r.err != nil || r.res.Version != f.version() || r.res.Waiting != 1 {
			t.Fatalf("since %q: %+v %v", since, r.res, r.err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("since %q answered after %v", since, d)
		}
	}
	sinces, _, _ := f.calls()
	if !slices.Equal(sinces, []string{other, ""}) {
		t.Fatalf("the service was asked since %q", sinces)
	}
}

// The service is given the request's own context: it carries the gate's
// deadline for the request, and a wait that is still held at it ends with the
// request, frees its slot, and leaves nothing running.
func TestTheWaitCarriesTheRequestsDeadline(t *testing.T) {
	f := &waitingPort{hold: time.Minute}
	g := serveWait(t, f, 600*time.Millisecond)
	c := g.dial()
	start := time.Now()
	r := <-startWait(c, f.version())
	if remoteCode(r.err) != CodeDeadline {
		t.Fatalf("got %+v %v, want %s", r.res, r.err, CodeDeadline)
	}
	_, dls, all := f.calls()
	if !all || len(dls) != 1 || dls[0].After(start.Add(2*time.Second)) || dls[0].Before(start) {
		t.Fatalf("the service's context deadline %v (start %v)", dls, start)
	}
	eventually(t, "the call returned and gave its slot back", func() bool {
		return f.heldNow() == 0 && g.heldFor(g.seatID.Fingerprint()) == 0
	})
	// A deadline is the daemon's own state: it is not counted.
	if g.refusalsOf() != 0 {
		t.Fatalf("the deadline counted %d", g.refusalsOf())
	}
}

// At most MaxWaitsPerPeer waits are held for a peer; the third is wait-full,
// answered before the service is asked, and however often it is answered it
// does not count against the refusal bound and does not shut the seat out.
func TestTheThirdWaitIsWaitFullAndIsNotCounted(t *testing.T) {
	if MaxWaitsPerPeer != 2 {
		t.Fatalf("MaxWaitsPerPeer is %d", MaxWaitsPerPeer)
	}
	f := &waitingPort{hold: 30 * time.Second}
	g := serveWait(t, f, 0)
	c := g.dial()
	v := f.version()
	first, second := startWait(c, v), startWait(c, v)
	eventually(t, "two waits are held", func() bool { return f.heldNow() == 2 })

	var wrong int
	for i := 0; i < RefusalBound+8; i++ {
		r := <-startWait(c, v)
		if remoteCode(r.err) != ownerwire.CodeWaitFull {
			wrong++
			t.Errorf("wait %d: %+v %v", i, r.res, r.err)
		}
	}
	if wrong > 0 {
		t.FailNow()
	}
	if f.heldNow() != 2 {
		t.Errorf("%d waits held in the service", f.heldNow())
	}
	sinces, _, _ := f.calls()
	if len(sinces) != 2 {
		t.Errorf("the service was asked %d times, the refused waits reached it", len(sinces))
	}
	if g.refusalsOf() != 0 {
		t.Errorf("wait-full moved the refusal count to %d", g.refusalsOf())
	}
	if n := g.h.Counts()[ownerwire.CodeWaitFull]; n != RefusalBound+8 {
		t.Errorf("wait-full answered %d times", n)
	}
	if n := g.h.Counts()[CodeTooManyRefusals]; n != 0 {
		t.Errorf("too-many-refusals answered %d times", n)
	}
	// The log is limited: one line for the peer, not one for each answer.
	var lines int
	for _, l := range g.lb.all() {
		if strings.Contains(l, "refused wait-full") {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("%d wait-full lines in the log %q", lines, g.lb.all())
	}
	// The seat is not shut out: a read is served, and a wait is held again
	// once a slot is free.
	var l ownerwire.Listing
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := c.Call(ctx, ownerwire.OpList, nil, &l); err != nil {
		t.Fatalf("owner.list after the wait-fulls: %v", err)
	}
	f.set(aCard("ap-20261122-0001"))
	for _, ch := range []<-chan waitCall{first, second} {
		if r := <-ch; r.err != nil || r.res.Waiting != 1 {
			t.Fatalf("held wait: %+v %v", r.res, r.err)
		}
	}
	eventually(t, "the slots are given back", func() bool { return g.heldFor(g.seatID.Fingerprint()) == 0 })
	next := startWait(c, f.version())
	eventually(t, "a new wait is held", func() bool { return f.heldNow() == 1 })
	f.set()
	if r := <-next; r.err != nil {
		t.Fatal(r.err)
	}
}

// The service's own limit is wait-full too, uncounted, and the peer's limit
// is the handler's, kept apart for each peer.
func TestTheServicesWaitFullIsWaitFullAndIsNotCounted(t *testing.T) {
	f := &waitingPort{werr: ownerchannel.ErrWaitFull}
	h, lb := newHandler(t, f)
	clk := &testClock{t: time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)}
	h.now = clk.now
	wait := func() {
		t.Helper()
		out, err := h.Handle(context.Background(), seat, ownerwire.OpSeatWait, sinceParams(t, ""))
		if out != nil || codeOfErr(err) != ownerwire.CodeWaitFull {
			t.Fatalf("%s %v", out, err)
		}
	}
	for i := 0; i < RefusalBound*2; i++ {
		wait()
	}
	if len(h.bound.peers) != 0 {
		t.Fatalf("the bound holds %v", h.bound.peers)
	}
	if n := h.Counts()[ownerwire.CodeWaitFull]; n != RefusalBound*2 || h.Counts()[CodeTooManyRefusals] != 0 {
		t.Fatalf("counts %v", h.Counts())
	}
	if l := lb.all(); len(l) != 1 || !strings.Contains(l[0], "seat.wait refused wait-full (peer "+seatFP[:16]+")") {
		t.Fatalf("log %q", l)
	}
	// The slot of each of those calls was given back.
	if len(h.waits.held) != 0 {
		t.Fatalf("slots held: %v", h.waits.held)
	}
	// The log line is limited by the handler's clock: a minute on, one more.
	clk.t = clk.t.Add(waitFullLogEvery)
	wait()
	wait()
	if l := lb.all(); len(l) != 2 {
		t.Fatalf("log %q", l)
	}
}

// The slots are kept for each peer.
func TestTheWaitSlotsAreForEachPeer(t *testing.T) {
	var w waitSlots
	for i := 0; i < MaxWaitsPerPeer; i++ {
		if !w.take("a") {
			t.Fatalf("slot %d of a", i)
		}
	}
	if w.take("a") {
		t.Fatal("a third slot of a")
	}
	if !w.take("b") {
		t.Fatal("b has no slot because a is full")
	}
	w.give("a")
	if !w.take("a") {
		t.Fatal("a slot given back was not free")
	}
	for _, k := range []string{"a", "a", "b"} {
		w.give(k)
	}
	if len(w.held) != 0 {
		t.Fatalf("slots kept: %v", w.held)
	}
}

// Without the capability (a service that cannot wait) seat.wait is
// unknown-op before its params are read and counts as any unknown-op does;
// with it, params that are wrong are bad-params, count the same, and never
// reach the service.
func TestWatchRefusalsCountAsForAnyOperation(t *testing.T) {
	// No capability: over the gate, and the hello does not list it.
	g := serveWait(t, &fakePort{}, 0)
	c := g.dial()
	if slices.Contains(c.Caps(), CapWatch) {
		t.Fatalf("the hello listed %q", c.Caps())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for i, params := range []json.RawMessage{sinceParams(t, ""), json.RawMessage(`{"since":"x"}`), json.RawMessage(`{}`)} {
		err := c.Call(ctx, ownerwire.OpSeatWait, params, nil)
		if remoteCode(err) != CodeUnknownOp {
			t.Fatalf("call %d: %v", i, err)
		}
		if n := g.refusalsOf(); n != i+1 {
			t.Fatalf("after %d unknown-ops the bound holds %d", i+1, n)
		}
	}

	// With it: bad params.
	f := &waitingPort{}
	g = serveWait(t, f, 0)
	c = g.dial()
	good := strings.Repeat("a", ownerwire.SeatWaitVersionLength)
	bad := []string{
		`{"since":"` + good[:31] + `"}`,
		`{"since":"` + good + `0"}`,
		`{"since":"` + strings.ToUpper(good) + `"}`,
		`{"since":"` + strings.Repeat("g", 32) + `"}`,
		`{}`,
		`{"since":null}`,
		`{"since":""` + `,"extra":1}`,
	}
	for i, p := range bad {
		err := c.Call(ctx, ownerwire.OpSeatWait, json.RawMessage(p), nil)
		if remoteCode(err) != CodeBadParams {
			t.Fatalf("params %s: %v", p, err)
		}
		if n := g.refusalsOf(); n != i+1 {
			t.Fatalf("after %d bad-params the bound holds %d", i+1, n)
		}
	}
	// The client does not send params that are not one object with no key
	// twice, so these reach the handler directly.
	n := g.refusalsOf()
	for _, p := range []string{`{"since":"","since":""}`, `[]`, `null`, `"since"`} {
		if _, err := g.h.Handle(context.Background(), seatgate.Peer{Fingerprint: g.seatID.Fingerprint()}, ownerwire.OpSeatWait, json.RawMessage(p)); codeOfErr(err) != CodeBadParams {
			t.Fatalf("params %s: %v", p, err)
		}
		n++
		if got := g.refusalsOf(); got != n {
			t.Fatalf("the bound holds %d, want %d", got, n)
		}
	}
	if sinces, _, _ := f.calls(); len(sinces) != 0 {
		t.Fatalf("the service was asked %q", sinces)
	}
	if g.heldFor(g.seatID.Fingerprint()) != 0 {
		t.Fatal("a slot is held")
	}
}

// A seat whose key is not pinned is refused not-pinned before the wait, with
// no slot taken.
func TestAWaitOfAPeerThatIsNotPinnedTakesNoSlot(t *testing.T) {
	f := &waitingPort{}
	h, _ := newHandler(t, f)
	for i := 0; i < 3; i++ {
		_, err := h.Handle(context.Background(), seatgate.Peer{Fingerprint: otherFP}, ownerwire.OpSeatWait, sinceParams(t, ""))
		if codeOfErr(err) != CodeNotPinned {
			t.Fatalf("%v", err)
		}
	}
	if len(h.waits.held) != 0 {
		t.Fatalf("slots held: %v", h.waits.held)
	}
	if sinces, _, _ := f.calls(); len(sinces) != 0 {
		t.Fatalf("the service was asked %q", sinces)
	}
}

// A connection that drops, with its waits held, frees their slots: the seat
// that reconnects is not wait-full, and nothing is left running in the
// service.
func TestADroppedConnectionFreesItsWaitSlots(t *testing.T) {
	f := &waitingPort{hold: 30 * time.Second}
	g := serveWait(t, f, 0)
	fp := g.seatID.Fingerprint()
	old := g.dial()
	v := f.version()
	a, b := startWait(old, v), startWait(old, v)
	eventually(t, "two waits are held", func() bool { return f.heldNow() == 2 && g.heldFor(fp) == 2 })

	old.Close()
	for _, ch := range []<-chan waitCall{a, b} {
		if r := <-ch; r.err == nil {
			t.Fatalf("a wait of a closed connection answered %+v", r.res)
		}
	}
	eventually(t, "the service's waits and the slots are gone", func() bool {
		return f.heldNow() == 0 && g.heldFor(fp) == 0
	})

	fresh := g.dial()
	c, d := startWait(fresh, v), startWait(fresh, v)
	eventually(t, "two new waits are held", func() bool { return f.heldNow() == 2 })
	third := <-startWait(fresh, v)
	if remoteCode(third.err) != ownerwire.CodeWaitFull {
		t.Fatalf("third wait: %+v %v", third.res, third.err)
	}
	f.set(aCard("ap-20261122-0001"))
	for _, ch := range []<-chan waitCall{c, d} {
		if r := <-ch; r.err != nil {
			t.Fatal(r.err)
		}
	}
	if g.refusalsOf() != 0 {
		t.Fatalf("the refusal count is %d", g.refusalsOf())
	}
}

// A result the encoder refuses is failed, with the slot given back.
func TestAWaitResultTheEncoderRefusesIsFailed(t *testing.T) {
	f := &waitingPort{}
	h, _ := newHandler(t, f)
	f.cards = []ownerwire.SeatWaitCard{{ID: "not an id", Kind: "approval"}}
	out, err := h.Handle(context.Background(), seat, ownerwire.OpSeatWait, sinceParams(t, ""))
	if out != nil || codeOfErr(err) != CodeFailed {
		t.Fatalf("%s %v", out, err)
	}
	if len(h.waits.held) != 0 {
		t.Fatalf("slots held: %v", h.waits.held)
	}
}
