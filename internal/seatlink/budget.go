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

// The room of each kind of call in BudgetWindow.
const (
	// pingRoom is the pings: one every PingInterval is 6 at most in
	// BudgetWindow, and 5 in a minute of the gate's.
	pingRoom = 6
	// decideRoom is seat.challenge and seat.decide: 8 decisions. A challenge
	// is drawn only with room for its seat.decide as well, kept until the
	// decision is sent or the challenge's life ends.
	decideRoom = 16
	// publicRoom is seat.pair, seat.login-challenge and seat.login, which the
	// seat relays for anyone who reaches its pages: 6 logins.
	publicRoom = 12
	// readRoom is owner.list, owner.show and owner.paths, the reads of every
	// session together.
	readRoom = 26
)

// The rooms and the pings are the gate's rate: either difference below is
// a negative constant, which does not convert to uint, if they are not.
const (
	_ = uint(GateRate - pingRoom - decideRoom - publicRoom - readRoom)
	_ = uint(pingRoom + decideRoom + publicRoom + readRoom - GateRate)
	// The pings of BudgetWindow fit their room.
	_ = uint(pingRoom - int(BudgetWindow/PingInterval) - 1)
)

// kind is a kind of call the link sends.
type kind int

const (
	kindPublic kind = iota
	kindRead
	kindDecide
	numKinds
)

// room is the room of a kind in BudgetWindow.
func (k kind) room() int {
	switch k {
	case kindPublic:
		return publicRoom
	case kindRead:
		return readRoom
	}
	return decideRoom
}

// hold is the room kept for the seat.decide of a credential's last
// challenge: the challenge's id (empty while it is being asked) and when the
// room is let go if no decision came.
type hold struct {
	id    string
	until time.Time
}

// budget counts the calls the link sent, by kind, over BudgetWindow, and
// keeps the room for decisions. It is safe for concurrent use.
type budget struct {
	now func() time.Time

	mu   sync.Mutex
	sent [numKinds][]time.Time
	// holds are the room kept for a decision, one for each credential's
	// last challenge: the app keeps one decision for each session, and a
	// new tap replaces the last.
	holds map[string]hold
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
	for c, h := range b.holds {
		if !now.Before(h.until) {
			delete(b.holds, c)
		}
	}
}

// take counts a call of a kind that is not a decision's, and reports
// whether it has room. A call without room is not counted, and not sent.
func (b *budget) take(k kind) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.prune(now)
	if len(b.sent[k]) >= k.room() {
		return false
	}
	b.sent[k] = append(b.sent[k], now)
	return true
}

// reserve counts a credential's seat.challenge and keeps the room of its
// seat.decide, replacing the credential's last hold, and reports whether
// there was room for both. Without it nothing is counted or kept, and the
// challenge is not asked: so every decision sent has its room, whatever
// else is sent meanwhile.
func (b *budget) reserve(credential string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.prune(now)
	used := len(b.sent[kindDecide]) + len(b.holds)
	if _, ok := b.holds[credential]; ok {
		used--
	}
	if used+2 > decideRoom {
		return false
	}
	if b.holds == nil {
		b.holds = map[string]hold{}
	}
	b.sent[kindDecide] = append(b.sent[kindDecide], now)
	b.holds[credential] = hold{until: now.Add(HoldLife)}
	return true
}

// bind names the challenge a credential's hold is for, and lets the room go
// at its expiry when that is sooner than HoldLife.
func (b *budget) bind(credential, id string, expires time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h, ok := b.holds[credential]
	if !ok || h.id != "" {
		return
	}
	h.id = id
	if expires.Before(h.until) {
		h.until = expires
	}
	b.holds[credential] = h
}

// release lets a credential's hold go when its challenge was not drawn.
func (b *budget) release(credential string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if h, ok := b.holds[credential]; ok && h.id == "" {
		delete(b.holds, credential)
	}
}

// spend counts the seat.decide of a challenge, in the room its hold kept.
// A decision is never refused for room: the phone has answered.
func (b *budget) spend(challengeID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	b.prune(now)
	for c, h := range b.holds {
		if h.id != "" && h.id == challengeID {
			delete(b.holds, c)
			break
		}
	}
	b.sent[kindDecide] = append(b.sent[kindDecide], now)
}
