package seatlink

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand/v2"
	"net"
	"strconv"
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
// moves. A challenge is drawn only with room for its decision too, a
// credential's new challenge takes the place of its last, a challenge not
// drawn lets its room go, a hold ends with its challenge's life, and a
// decision is counted whatever the room.
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
	// challenges, each with room kept for its decision.
	for i := 0; i < decideRoom/2; i++ {
		c := "c" + strconv.Itoa(i)
		if !b.reserve(c) {
			t.Fatalf("challenge %d refused", i+1)
		}
		b.bind(c, "id"+strconv.Itoa(i), clock.now().Add(time.Hour))
	}
	if b.reserve("another") || b.reserve("c0") {
		t.Error("a challenge with no room left for its decision")
	}
	if len(b.holds) != decideRoom/2 || b.holds["c0"].id != "id0" {
		t.Errorf("a refused challenge changed the holds: %v", b.holds)
	}
	// Every decision is sent, its room kept or not.
	for i := 1; i < decideRoom/2; i++ {
		b.spend("id" + strconv.Itoa(i))
	}
	b.spend("id-not-held")
	if len(b.holds) != 1 || len(b.sent[kindDecide]) != decideRoom {
		t.Errorf("holds %v, %d decision calls", b.holds, len(b.sent[kindDecide]))
	}
	// The window moves on; c0's hold stays for its challenge's life.
	clock.advance(BudgetWindow)
	for _, k := range []kind{kindPublic, kindRead} {
		if !b.take(k) {
			t.Errorf("kind %d: a call after the window", k)
		}
	}
	// A credential's new challenge takes the place of its last.
	if !b.reserve("c0") || len(b.holds) != 1 || b.holds["c0"].id != "" {
		t.Fatalf("a new challenge of c0: %v", b.holds)
	}
	// A challenge not drawn lets its room go.
	b.release("c0")
	if len(b.holds) != 0 {
		t.Errorf("a challenge not drawn keeps its room: %v", b.holds)
	}
	// A hold ends with its challenge's life, or HoldLife.
	b.reserve("x")
	b.bind("x", "idx", clock.now().Add(time.Minute))
	b.reserve("y")
	b.bind("y", "idy", clock.now().Add(time.Hour))
	clock.advance(time.Minute)
	b.prune(clock.now())
	if _, ok := b.holds["x"]; ok || len(b.holds) != 1 {
		t.Errorf("a hold past its challenge's life: %v", b.holds)
	}
	clock.advance(HoldLife)
	b.prune(clock.now())
	if len(b.holds) != 0 {
		t.Errorf("a hold past HoldLife: %v", b.holds)
	}
}

// TestSeatLinkBudgetNeverOverTheGate: a long run of every kind of call, at
// random, as the link's callers make them: of each challenge drawn, a third
// are answered at once, a third later (unless a new tap of the same
// credential replaces it first, as the app keeps one decision a session),
// and a third never. In every BudgetWindow, which holds every minute of the
// gate's, the calls of each kind stay within its room, decisions included,
// which are never refused; the rooms and the pings are the gate's rate.
func TestSeatLinkBudgetNeverOverTheGate(t *testing.T) {
	clock := &testClock{t: decideNow}
	b := &budget{now: clock.now}
	rnd := rand.New(rand.NewPCG(1, 2))
	var sent [numKinds][]time.Time
	type later struct {
		cred, id string
		at       time.Time
	}
	var pending []later
	for step := 0; step < 20000; step++ {
		clock.advance(time.Duration(rnd.IntN(2000)) * time.Millisecond)
		now := clock.now()
		for i := 0; i < len(pending); {
			if !now.Before(pending[i].at) {
				b.spend(pending[i].id)
				sent[kindDecide] = append(sent[kindDecide], now)
				pending = append(pending[:i], pending[i+1:]...)
				continue
			}
			i++
		}
		switch k := kind(rnd.IntN(int(numKinds))); k {
		case kindPublic, kindRead:
			if b.take(k) {
				sent[k] = append(sent[k], now)
			}
		case kindDecide:
			cred := "c" + strconv.Itoa(rnd.IntN(5))
			if !b.reserve(cred) {
				continue
			}
			sent[kindDecide] = append(sent[kindDecide], now)
			id := "id" + strconv.Itoa(step)
			b.bind(cred, id, now.Add(HoldLife))
			// The credential's last challenge is replaced: its decision
			// never comes.
			kept := pending[:0]
			for _, p := range pending {
				if p.cred != cred {
					kept = append(kept, p)
				}
			}
			pending = kept
			switch rnd.IntN(3) {
			case 0:
				b.spend(id)
				sent[kindDecide] = append(sent[kindDecide], now)
			case 1:
				pending = append(pending, later{cred: cred, id: id, at: now.Add(time.Duration(rnd.IntN(int(HoldLife/time.Second))) * time.Second)})
			}
		}
	}
	for k, ts := range sent {
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
// The decision of the challenge drawn last is sent even so. One credential
// draws them, each in place of its last: 15 challenges and the room of the
// last one's decision fill the 16.
func TestSeatLinkDecideRoom(t *testing.T) {
	r := newDecideRig(t)
	clock := &testClock{t: decideNow}
	r.link.budget.now = clock.now
	r.show(t)
	var last seatapp.DecideChallenge
	for i := 0; i < decideRoom-1; i++ {
		ch, err := r.link.DecideChallenge(context.Background(), r.approve())
		if err != nil {
			t.Fatalf("challenge %d: %v", i+1, err)
		}
		last = ch
	}
	before := r.gate.calls.Load()
	if _, err := r.link.DecideChallenge(context.Background(), r.approve()); err != seatapp.ErrBusy {
		t.Errorf("a challenge past the room: %v, want %v", err, seatapp.ErrBusy)
	}
	if n := r.gate.calls.Load() - before; n != 0 {
		t.Errorf("%d calls reached the gate", n)
	}
	if _, err := r.link.Decide(context.Background(), r.sign(t, last)); err != nil {
		t.Errorf("the last challenge's decision: %v", err)
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
