package seatlink

import (
	"sync"
	"time"
)

// The seat's calls on its one connection to the gate, and the room each kind
// has (docs/design/seat-gate.md, "Login", the rate paragraph). The gate takes
// GateRate requests a minute on the connection, pings included, and answers
// any more rate-limited; every browser's traffic shares the connection. So
// the link counts every call it sends, by kind, and sends none past the room
// of its kind: it answers [seatapp.ErrBusy] instead, before anything is
// sent. The rooms and the pings add up to GateRate, so the gate's rate never
// refuses a call the link sent, and a decision has room of its own that
// strangers' logins and pairings, and reads, cannot take.
const (
	// GateRate is the gate's rate on one connection, in requests a minute
	// (seatgate.ServerConfig's RateMax and RatePer).
	GateRate = 60
	// PingInterval is the interval of the seat's pings (the command sets it
	// on its client); the gate counts a ping as any request.
	PingInterval = 15 * time.Second
	// BudgetWindow is the window over which the link counts its calls: the
	// gate's minute and 20 seconds more, the longest a call is taken to wait
	// between the link's count and the gate's (the app gives a call 15
	// seconds, and the network 5). Calls the link counted more than
	// BudgetWindow apart are never in one minute of the gate's.
	BudgetWindow = 80 * time.Second
	// HoldLife is the longest the room for a decision is kept for a
	// challenge that is not answered: the app keeps a challenge 5 minutes
	// at most, and the daemon's expiry is used when it is sooner.
	HoldLife = 5 * time.Minute
)

// The room of each kind of call in BudgetWindow. The six add up to GateRate:
//
//	pings 6 + decisions 12 + anyone's 12 + reads 14 + waits 6 + genesis 10 = 60
//
// The room of seat.genesis (10) was made when the interview's pages came:
// before it the rooms were pings 6, decisions 16, anyone's 12, reads 20 and
// waits 6. A room of 10 lets the owner answer a round about every 8 seconds
// without meeting "busy" (each answer is one call; the page it redirects to
// is answered from the link's memory, see genesis.go), and holds the list of
// open projects the card list shows, which the link asks at most once in 20
// seconds. The 10 were taken from reads (20 to 14: three card lists that
// reload every 20 seconds take 12 of the 14) and decisions (16 to 12: 6
// decisions, each a passkey tap, in 80 seconds). Anyone's room stays 12:
// strangers can hold it with login challenges they never answer, and a
// smaller one would let fewer of them keep the owner from logging in.
const (
	// pingRoom is the pings: one every PingInterval is 6 at most in
	// BudgetWindow, and 5 in a minute of the gate's.
	pingRoom = 6
	// decideRoom is seat.challenge and seat.decide: 6 decisions. A challenge
	// is drawn only with room for its seat.decide as well, kept for that
	// challenge alone until the decision is sent, the app drops the
	// challenge, or the challenge's life ends.
	decideRoom = 12
	// publicRoom is seat.pair, seat.login-challenge and seat.login, which the
	// seat relays for anyone who reaches its pages: 6 logins. A login
	// challenge is drawn only with room for its seat.login as well, kept
	// for that challenge until the login is sent or the challenge's life
	// ends, so a login is never refused for room once the phone has
	// answered. Strangers who draw login challenges and never answer them
	// keep that room until their challenges' life ends.
	publicRoom = 12
	// readRoom is owner.list, owner.show and owner.paths, the reads of every
	// session together.
	readRoom = 14
	// waitRoom is seat.wait, the watcher's one long wait (watch.go). The
	// watcher keeps one wait in flight, and the gate holds it up to 25
	// seconds, so an idle seat sends one about every 25 seconds: 4 in
	// BudgetWindow (at 0, 25, 50 and 75 seconds), which leaves 2 for the
	// waits that a change cut short. With no room the watcher sleeps until
	// some frees rather than sending; the page lags, and nothing is
	// refused. The room is taken from readRoom (26 before the watcher), so
	// the rooms still add up to GateRate.
	waitRoom = 6
	// genesisRoom is seat.genesis, every verb of the interview: the pages'
	// starts, lists, rounds, answers, changes, summaries, finishes and
	// discards, of every session together.
	genesisRoom = 10
)

// The rooms and the pings are the gate's rate: either difference below is
// a negative constant, which does not convert to uint, if they are not.
const (
	_ = uint(GateRate - pingRoom - decideRoom - publicRoom - readRoom - waitRoom - genesisRoom)
	_ = uint(pingRoom + decideRoom + publicRoom + readRoom + waitRoom + genesisRoom - GateRate)
	// A decision and a login each take two calls of their room, so each
	// room is even.
	_ = uint(0 - decideRoom%2)
	_ = uint(0 - publicRoom%2)
	// The pings of BudgetWindow fit their room.
	_ = uint(pingRoom - int(BudgetWindow/PingInterval) - 1)
	// An idle watcher's waits of BudgetWindow (one every WatchHold, the
	// first at the window's start) fit their room with 2 to spare.
	_ = uint(waitRoom - 2 - (int(BudgetWindow/WatchHold) + 1))
)

// kind is a kind of call the link sends.
type kind int

const (
	kindPublic kind = iota
	kindRead
	kindDecide
	kindWait
	kindGenesis
	numKinds
)

// room is the room of a kind in BudgetWindow.
func (k kind) room() int {
	switch k {
	case kindPublic:
		return publicRoom
	case kindRead:
		return readRoom
	case kindWait:
		return waitRoom
	case kindGenesis:
		return genesisRoom
	}
	return decideRoom
}

// hold is the room kept for the answer to one challenge the link drew (the
// seat.decide of a decision's challenge, the seat.login of a login
// challenge): its kind, the challenge's id (empty while it is being asked)
// and when the room is let go if no answer came.
type hold struct {
	kind  kind
	id    string
	until time.Time
}

// budget counts the calls the link sent, by kind, over BudgetWindow, and
// keeps the room for the answers to the challenges it drew. It is safe for
// concurrent use.
type budget struct {
	now func() time.Time

	mu   sync.Mutex
	sent [numKinds][]time.Time
	// holds are the room kept for answers, one for each challenge drawn
	// and not answered, by the token reserve gave it before the challenge
	// was asked. A challenge's room is its own: another challenge, of the
	// same session, of another session of the same credential (a synced
	// passkey in two browsers), or one asked at the same moment, never
	// takes it or lets it go.
	holds map[uint64]hold
	// last is the last token reserve gave.
	last uint64
}

// prune drops the calls older than BudgetWindow and the holds past their
// life. It runs under mu.
func (b *budget) prune(now time.Time) {
	cut := now.Add(-BudgetWindow)
	for k := range b.sent {
		ts := b.sent[k]
		i := 0
		for i < len(ts) && !ts[i].After(cut) {
			i++
		}
		b.sent[k] = ts[i:]
	}
	for t, h := range b.holds {
		if !now.Before(h.until) {
			delete(b.holds, t)
		}
	}
}

// used is the room of a kind the calls sent and the holds kept take. It
// runs under mu.
func (b *budget) used(k kind) int {
	n := len(b.sent[k])
	for _, h := range b.holds {
		if h.kind == k {
			n++
		}
	}
	return n
}

// take counts a call that is not a challenge's answer, and reports whether
// it has room. A call without room is not counted, and not sent.
func (b *budget) take(k kind) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.prune(now)
	return b.takeLocked(k, now)
}

// takeLocked is take under mu.
func (b *budget) takeLocked(k kind, now time.Time) bool {
	if b.used(k) >= k.room() {
		return false
	}
	b.sent[k] = append(b.sent[k], now)
	return true
}

// takeOrWait is take for a caller that sleeps rather than refuses: it counts
// the call and returns (true, 0) when the kind has room, and otherwise
// counts nothing and returns how long until room frees, as the calls
// counted age out of BudgetWindow (never less than a millisecond).
func (b *budget) takeOrWait(k kind) (bool, time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.prune(now)
	if b.takeLocked(k, now) {
		return true, 0
	}
	// The call counted index need-1 of the kind is the one whose ageing
	// leaves room. Holds, which have no life known here, are not counted
	// as ageing; a kind with fewer calls than that waits a second.
	need := b.used(k) - k.room() + 1
	if need > len(b.sent[k]) {
		return false, time.Second
	}
	wait := b.sent[k][need-1].Add(BudgetWindow).Sub(now)
	if wait < time.Millisecond {
		wait = time.Millisecond
	}
	return false, wait
}

// reserve counts a challenge of a kind and keeps the room of its answer,
// and returns the hold's token, which bind, release and nothing else name.
// It reports whether there was room for both; without it nothing is
// counted or kept, and the challenge is not asked. So every answer to a
// challenge the link drew has its room, whatever else is sent meanwhile.
// The token is given here, before anything is sent, so calls asked at the
// same moment each have a hold of their own.
func (b *budget) reserve(k kind) (uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.prune(now)
	if b.used(k)+2 > k.room() {
		return 0, false
	}
	if b.holds == nil {
		b.holds = map[uint64]hold{}
	}
	b.last++
	b.sent[k] = append(b.sent[k], now)
	b.holds[b.last] = hold{kind: k, until: now.Add(HoldLife)}
	return b.last, true
}

// bind names the challenge a hold is for, and lets the room go at the
// challenge's expiry when that is sooner than HoldLife.
func (b *budget) bind(token uint64, id string, expires time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h, ok := b.holds[token]
	if !ok || h.id != "" || id == "" {
		return
	}
	h.id = id
	if expires.Before(h.until) {
		h.until = expires
	}
	b.holds[token] = h
}

// release lets a hold go when its challenge was not drawn.
func (b *budget) release(token uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if h, ok := b.holds[token]; ok && h.id == "" {
		delete(b.holds, token)
	}
}

// drop lets the room of a challenge's answer go when the answer will never
// be sent: the app dropped the challenge.
func (b *budget) drop(k kind, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for t, h := range b.holds {
		if h.kind == k && h.id != "" && h.id == id {
			delete(b.holds, t)
			return
		}
	}
}

// answer counts the answer to a challenge of a kind, and reports whether it
// may be sent. An answer whose challenge's room is kept is counted in it and
// is never refused for room: the phone has answered. An answer with no room
// kept (its challenge's life ended, or the link did not draw it) is counted
// as take counts a call.
func (b *budget) answer(k kind, id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.prune(now)
	for t, h := range b.holds {
		if h.kind == k && h.id != "" && h.id == id {
			delete(b.holds, t)
			b.sent[k] = append(b.sent[k], now)
			return true
		}
	}
	return b.takeLocked(k, now)
}
