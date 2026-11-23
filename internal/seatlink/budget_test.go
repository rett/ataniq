package seatlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/webauthntest"
	"ataniq/internal/seatapp"
	"ataniq/internal/seatgate"
)

// clientHeader is the header the clock rig's app reads a client's address
// from.
const clientHeader = "X-Test-Client"

// TestSeatLinkBudgetRooms: each kind of call has its room in BudgetWindow,
// and a call past it is not counted; the room comes back as the window
// moves. A challenge is drawn only with room for its answer too, kept for
// that challenge alone: challenges asked at the same moment, or by several
// sessions of one credential, each keep their own. A challenge not drawn
// lets its room go, a dropped challenge lets it go, a hold ends with its
// challenge's life, and an answer with its room kept is counted whatever
// the room; one without is counted only with room.
func TestSeatLinkBudgetRooms(t *testing.T) {
	clock := &testClock{t: decideNow}
	b := &budget{now: clock.now}
	for _, k := range []kind{kindPublic, kindRead} {
		for i := 0; i < k.room(); i++ {
			if !b.take(k) {
				t.Fatalf("kind %d: call %d refused", k, i+1)
			}
		}
		if b.take(k) {
			t.Errorf("kind %d: a call past its room of %d", k, k.room())
		}
	}
	// The decisions have their own room, which the others did not take: 8
	// challenges, each with room kept for its decision. They are all
	// reserved before any is bound, as calls asked at the same moment are.
	var tokens []uint64
	for i := 0; i < decideRoom/2; i++ {
		tok, ok := b.reserve(kindDecide)
		if !ok {
			t.Fatalf("challenge %d refused", i+1)
		}
		tokens = append(tokens, tok)
	}
	for i := len(tokens) - 1; i >= 0; i-- {
		b.bind(tokens[i], "id"+strconv.Itoa(i), clock.now().Add(time.Hour))
	}
	if _, ok := b.reserve(kindDecide); ok {
		t.Error("a challenge with no room left for its decision")
	}
	if len(b.holds) != decideRoom/2 {
		t.Errorf("a refused challenge changed the holds: %v", b.holds)
	}
	for i, tok := range tokens {
		if h := b.holds[tok]; h.id != "id"+strconv.Itoa(i) {
			t.Errorf("challenge %d's hold names %q", i, h.id)
		}
	}
	// Every decision of a challenge drawn is sent, though the room is full.
	for i := 1; i < decideRoom/2; i++ {
		if !b.answer(kindDecide, "id"+strconv.Itoa(i)) {
			t.Errorf("the decision of challenge %d refused", i)
		}
	}
	// A decision of no challenge whose room is kept is refused for room.
	if b.answer(kindDecide, "id-not-held") {
		t.Error("a decision with no room kept, and none left, was sent")
	}
	if len(b.holds) != 1 || len(b.sent[kindDecide]) != decideRoom-1 {
		t.Errorf("holds %v, %d decision calls", b.holds, len(b.sent[kindDecide]))
	}
	// The window moves on; id0's hold stays for its challenge's life.
	clock.advance(BudgetWindow)
	for _, k := range []kind{kindPublic, kindRead} {
		if !b.take(k) {
			t.Errorf("kind %d: a call after the window", k)
		}
	}
	if len(b.holds) != 1 || b.holds[tokens[0]].id != "id0" {
		t.Fatalf("id0's hold: %v", b.holds)
	}
	// A challenge not drawn lets its room go, and no other.
	tok, _ := b.reserve(kindDecide)
	b.release(tok)
	if len(b.holds) != 1 {
		t.Errorf("a challenge not drawn keeps its room: %v", b.holds)
	}
	// A challenge the app dropped lets its room go.
	b.drop(kindDecide, "id0")
	if len(b.holds) != 0 {
		t.Errorf("a dropped challenge keeps its room: %v", b.holds)
	}
	// A hold ends with its challenge's life, or HoldLife.
	x, _ := b.reserve(kindDecide)
	b.bind(x, "idx", clock.now().Add(time.Minute))
	y, _ := b.reserve(kindDecide)
	b.bind(y, "idy", clock.now().Add(time.Hour))
	clock.advance(time.Minute)
	b.prune(clock.now())
	if _, ok := b.holds[x]; ok || len(b.holds) != 1 {
		t.Errorf("a hold past its challenge's life: %v", b.holds)
	}
	clock.advance(HoldLife)
	b.prune(clock.now())
	if len(b.holds) != 0 {
		t.Errorf("a hold past HoldLife: %v", b.holds)
	}
}

// TestSeatLinkBudgetSessionsOfOneCredential: several sessions of one
// credential (a synced passkey in two browsers) each draw a challenge and
// then each send its decision. Each decision has the room its challenge
// kept: the decide kind never goes past its room, and no decision is
// refused for room. On the base the holds were one per credential, so
// every challenge but the last lost its room, and the 15 decisions sent
// made 30 calls in a room of 16.
func TestSeatLinkBudgetSessionsOfOneCredential(t *testing.T) {
	clock := &testClock{t: decideNow}
	b := &budget{now: clock.now}
	var ids []string
	for i := 0; ; i++ {
		tok, ok := b.reserve(kindDecide)
		if !ok {
			break
		}
		id := "session" + strconv.Itoa(i)
		b.bind(tok, id, clock.now().Add(HoldLife))
		ids = append(ids, id)
	}
	if len(ids) != decideRoom/2 {
		t.Fatalf("%d challenges drawn, want %d", len(ids), decideRoom/2)
	}
	for _, id := range ids {
		if !b.answer(kindDecide, id) {
			t.Errorf("the decision of %s refused for room", id)
		}
	}
	if n := len(b.sent[kindDecide]); n > decideRoom {
		t.Errorf("%d decision calls in the window, room %d", n, decideRoom)
	}
}

// TestSeatLinkBudgetConcurrentChallenges: challenges asked at the same
// moment, whose answers come back in any order, each keep their room; none
// is lost to another's bind or release.
func TestSeatLinkBudgetConcurrentChallenges(t *testing.T) {
	clock := &testClock{t: decideNow}
	b := &budget{now: clock.now}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var drawn []string
	for i := 0; i < decideRoom/2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, ok := b.reserve(kindDecide)
			if !ok {
				t.Error("a challenge refused")
				return
			}
			if i%2 == 1 {
				// This challenge was not drawn.
				b.release(tok)
				return
			}
			id := "c" + strconv.Itoa(i)
			b.bind(tok, id, clock.now().Add(HoldLife))
			mu.Lock()
			drawn = append(drawn, id)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if len(b.holds) != len(drawn) {
		t.Fatalf("%d holds for %d challenges drawn: %v", len(b.holds), len(drawn), b.holds)
	}
	for _, id := range drawn {
		found := false
		for _, h := range b.holds {
			found = found || h.id == id
		}
		if !found {
			t.Errorf("the room of %s was lost", id)
		}
	}
}

// TestSeatLinkBudgetNeverOverTheGate: a long run of every kind of call, at
// random, as the link's callers make them. Sessions draw challenges, in
// several sessions of each credential: of each challenge drawn, a third
// are answered at once, a third later (unless a new tap of the same
// session replaces it first, as the app keeps one decision a session and
// drops the one replaced), and a third never. In every BudgetWindow, which
// holds every minute of the gate's, the calls of each kind stay within its
// room, and a decision whose challenge is in its life is never refused; the
// rooms and the pings are the gate's rate.
func TestSeatLinkBudgetNeverOverTheGate(t *testing.T) {
	clock := &testClock{t: decideNow}
	b := &budget{now: clock.now}
	rnd := rand.New(rand.NewPCG(1, 2))
	var sent [numKinds][]time.Time
	type later struct {
		id string
		at time.Time
	}
	// pending is each session's one decision to come: sessions 0 to 7,
	// two of each of 4 credentials (the credential does not matter to the
	// room, which is each challenge's own).
	pending := map[int]later{}
	for step := 0; step < 20000; step++ {
		clock.advance(time.Duration(rnd.IntN(2000)) * time.Millisecond)
		now := clock.now()
		for s, p := range pending {
			if !now.Before(p.at) {
				if !b.answer(kindDecide, p.id) {
					t.Fatalf("session %d: a decision in its challenge's life refused for room", s)
				}
				sent[kindDecide] = append(sent[kindDecide], now)
				delete(pending, s)
			}
		}
		switch k := kind(rnd.IntN(int(numKinds))); k {
		case kindPublic, kindRead:
			if b.take(k) {
				sent[k] = append(sent[k], now)
			}
		case kindDecide:
			s := rnd.IntN(8)
			tok, ok := b.reserve(kindDecide)
			if !ok {
				continue
			}
			sent[kindDecide] = append(sent[kindDecide], now)
			if rnd.IntN(10) == 0 {
				// The challenge was not drawn.
				b.release(tok)
				continue
			}
			id := "id" + strconv.Itoa(step)
			b.bind(tok, id, now.Add(HoldLife))
			// The session's last challenge is replaced: its decision
			// never comes, and the app drops it.
			if p, ok := pending[s]; ok {
				b.drop(kindDecide, p.id)
				delete(pending, s)
			}
			switch rnd.IntN(3) {
			case 0:
				if !b.answer(kindDecide, id) {
					t.Fatalf("a decision answered at once refused for room")
				}
				sent[kindDecide] = append(sent[kindDecide], now)
			case 1:
				// Within the challenge's life, as the app sends it.
				pending[s] = later{id: id, at: now.Add(time.Duration(rnd.IntN(int(HoldLife/time.Second)-10)) * time.Second)}
			}
		}
	}
	for k, ts := range sent {
		slices.SortFunc(ts, func(a, b time.Time) int { return a.Compare(b) })
		for i := range ts {
			j := i
			for j < len(ts) && ts[j].Sub(ts[i]) < BudgetWindow {
				j++
			}
			if n := j - i; n > kind(k).room() {
				t.Fatalf("kind %d: %d calls in %v from %v, room %d", k, n, BudgetWindow, ts[i], kind(k).room())
			}
		}
	}
}

// TestSeatLinkDecideRoom: through the link, a challenge is asked only with
// room for its decision; a tap past the room is ErrBusy and reaches no gate.
// The decision of every challenge drawn is sent even so: 8 challenges, each
// of them with the room of its decision, fill the 16, whichever sessions
// drew them. A challenge the app dropped lets its decision's room go.
func TestSeatLinkDecideRoom(t *testing.T) {
	r := newDecideRig(t)
	clock := &testClock{t: decideNow}
	r.link.budget.now = clock.now
	r.show(t)
	var drawn []seatapp.DecideChallenge
	for i := 0; i < decideRoom/2; i++ {
		ch, err := r.link.DecideChallenge(context.Background(), r.approve())
		if err != nil {
			t.Fatalf("challenge %d: %v", i+1, err)
		}
		drawn = append(drawn, ch)
	}
	before := r.gate.calls.Load()
	if _, err := r.link.DecideChallenge(context.Background(), r.approve()); err != seatapp.ErrBusy {
		t.Errorf("a challenge past the room: %v, want %v", err, seatapp.ErrBusy)
	}
	if n := r.gate.calls.Load() - before; n != 0 {
		t.Errorf("%d calls reached the gate", n)
	}
	// The app drops the first two; the room of their decisions comes back,
	// and one challenge more is drawn with it.
	r.link.DropChallenge(drawn[0].ID)
	r.link.DropChallenge(drawn[1].ID)
	ch, err := r.link.DecideChallenge(context.Background(), r.approve())
	if err != nil {
		t.Fatalf("a challenge in the room of two dropped: %v", err)
	}
	drawn = append(drawn[2:], ch)
	for i, ch := range drawn {
		if _, err := r.link.Decide(context.Background(), r.sign(t, ch)); err != nil && errors.Is(err, seatapp.ErrBusy) {
			t.Errorf("the decision of challenge %d: %v", i+1, err)
		}
	}
	// The room comes back with the window.
	clock.advance(BudgetWindow)
	if _, err := r.link.DecideChallenge(context.Background(), r.approve()); err != nil {
		t.Errorf("a challenge after the window: %v", err)
	}
}

// clockRig is the phone rig on a gate whose rate (60 a minute, the
// default) runs on the test's clock, as the app and the link's budget do,
// so that a test can spend the gate's minute without waiting for it. Its
// app reads a client's address from clientHeader.
type clockRig struct {
	*phoneRig
	strangers int
}

func newClockRig(t *testing.T) *clockRig {
	t.Helper()
	clock := &testClock{t: decideNow}
	p, b := vector(t)
	d := &decideRig{desk: &decideDesk{reads: &fakeReads{pending: p, bytes: b}, entries: map[string]deskEntry{}}}
	seat := newIdentity(t, "seat")
	d.seat = seat
	g := &gate{id: newIdentity(t, "gate")}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	g.addr = ln.Addr().String()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		seatgate.Serve(ctx, ln, seatgate.ServerConfig{
			TLS: seatgate.ServerTLS(g.id, []string{seat.Fingerprint()}), Caps: []string{"pair", "read", "login", "decide"},
			ShutdownGrace: 100 * time.Millisecond, Now: clock.now,
		}, seatgate.HandlerFunc(func(ctx context.Context, p seatgate.Peer, op string, params json.RawMessage) (json.RawMessage, error) {
			g.calls.Add(1)
			return d.desk.handle(ctx, p, op, params)
		}))
	}()
	t.Cleanup(func() { cancel(); <-done })
	d.gate = g
	d.desk.login = newFakeDesk(g.id.Fingerprint())
	phone, err := webauthntest.New(seatRPID, seatOrigin)
	if err != nil {
		t.Fatal(err)
	}
	d.desk.login.pair(t, phone)
	d.phone, d.credential = phone, DisplayID(phone.CredentialID)
	d.link = dialPinned(t, g, seat)
	d.link.budget.now = clock.now
	r := &phoneRig{decideRig: d, clock: clock}
	h, err := seatapp.New(seatapp.Config{Origin: seatOrigin, RPID: seatRPID, Daemon: d.link, Now: clock.now,
		AllowNoFetchMetadataForTests: true, ClientAddrHeader: clientHeader})
	if err != nil {
		t.Fatal(err)
	}
	r.browser = newBrowser(h)
	return &clockRig{phoneRig: r}
}

// stranger asks the seat for a login challenge, as anyone who reaches the
// login page may, from an address of its own, and returns its browser.
func (r *clockRig) stranger() (*browser, int) {
	b := newBrowser(r.browser.h)
	r.strangers++
	b.source = "203.0.113." + strconv.Itoa(r.strangers)
	return b, b.do("POST", "/api/login-challenge", "{}").Code
}

// answer posts an assertion a stranger's authenticator made, which the desk
// does not know: a call of the gate all the same.
func (r *clockRig) answer(t *testing.T, b *browser, a *webauthntest.Authenticator) int {
	t.Helper()
	as, err := a.Get(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return b.do("POST", "/api/login", loginJSON(as)).Code
}

// TestSeatDecideNeverRefusedOnRate: within the documented allowances (8
// login challenges a minute from all sources, each answered within its
// life; 20 reads a minute for the owner's credential, each the first page
// of a card's path list, which asks the gate twice; a decision), on a gate
// of 60 requests a minute: the owner draws a challenge, strangers spend
// their logins and the owner reads, and the decision the phone then signs
// is decided, not refused on the gate's rate. On the base the gate refused
// it rate-limited after the passkey was asked. The calls the link has no
// room for are refused before they are sent, as "the desk is busy".
func TestSeatDecideNeverRefusedOnRate(t *testing.T) {
	r := newClockRig(t)
	a, err := webauthntest.New(seatRPID, seatOrigin)
	if err != nil {
		t.Fatal(err)
	}
	var held []*browser
	for i := 0; i < 8; i++ {
		b, _ := r.stranger()
		held = append(held, b)
	}
	r.clock.advance(61 * time.Second)
	if w := r.browser.loginWith(t, r.phone); w.Code != 200 {
		t.Fatalf("the owner's login: %d %s", w.Code, w.Body)
	}
	r.open(t)
	tap := r.tap("approve", []int{0, 1}, "")
	if tap.Code != 200 {
		t.Fatalf("tap: %d %s", tap.Code, tap.Body)
	}
	for i := 0; i < 7; i++ {
		b, _ := r.stranger()
		held = append(held, b)
	}
	busy := 0
	for _, b := range held {
		if r.answer(t, b, a) == 503 {
			busy++
		}
	}
	before := r.gate.calls.Load()
	reads := 0
	for i := 0; i < 19; i++ {
		// Each read is of a card whose review the link does not know.
		r.link.reviews.mu.Lock()
		r.link.reviews.m = nil
		r.link.reviews.mu.Unlock()
		switch w := r.browser.do("GET", "/cards/"+r.desk.reads.pending.ID+"/paths/0", ""); w.Code {
		case 200:
			reads++
		case 503:
		default:
			t.Fatalf("a read: %d", w.Code)
		}
	}
	if n := r.gate.calls.Load() - before; n > readRoom || reads == 19 {
		t.Errorf("%d page reads made %d calls of the gate, room %d", reads, n, readRoom)
	}
	w := r.confirm(t, tap)
	if m := said(t, w); w.Code != 200 || m["state"] != "approved" {
		t.Errorf("the decision after the passkey: %d %v", w.Code, m)
	}
	if busy == 0 {
		t.Error("no stranger's login was refused for room")
	}
}
