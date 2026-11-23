package ownerchannel

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"
)

// The bounds of the challenges of the seat gate (docs/design/seat-gate.md,
// "The store").
const (
	// ChallengeLife is how long a challenge may be used, measured with the
	// clock's reading as [Config].Now gives it, monotonic part included, as
	// the pairing's times are.
	ChallengeLife = 5 * time.Minute
	// MaxDecisionChallenges is the most decision challenges outstanding at
	// once. A 33rd is refused [ErrChallengesFull], unless one expired or is
	// replaced.
	MaxDecisionChallenges = 32
	// MaxLoginChallenges is the most login challenges outstanding at once.
	// A login challenge is never refused for room: the oldest is dropped.
	MaxLoginChallenges = 16
)

// The errors of the seat gate's challenges, found with errors.Is. Each has a
// fixed text that holds nothing a caller sent; the gate answers each with
// the code of package ownerwire that names it.
var (
	// ErrChallengeStale says no outstanding challenge of this kind and gate
	// session has the id given (it is unknown, used, expired, of another
	// session, or was dropped by a restart), or the frame is not the one
	// the challenge was drawn for (ownerwire.CodeChallengeStale).
	ErrChallengeStale = errors.New("ownerchannel: no outstanding challenge has this id, or the frame is not the one it was drawn for")
	// ErrChallengesFull says every room for a decision challenge is taken
	// and none can be replaced (ownerwire.CodeChallengesFull).
	ErrChallengesFull = errors.New("ownerchannel: every decision challenge is taken")
)

// challengeKind is what a challenge is for.
type challengeKind uint8

const (
	challengeDecision challengeKind = iota + 1
	challengeLogin
)

func (k challengeKind) String() string {
	if k == challengeLogin {
		return "login"
	}
	return "decision"
}

// challengeEntry is one outstanding challenge, in memory only.
type challengeEntry struct {
	// id is 16 lower-case hexadecimal digits, 8 bytes drawn at random.
	id   string
	kind challengeKind
	// session is the gate session that asked for it: only a call of that
	// session finds it.
	session string
	// nonce is the daemon's 32 random bytes, and challenge the digest the
	// phone signs.
	nonce     []byte
	challenge []byte
	// ends is when it expires, as the clock read it (monotonic part
	// included when the clock has one); endsWall is the wall reading the
	// result shows.
	ends     time.Time
	endsWall time.Time
	// credential (the display id), pendingID and project are a decision
	// challenge's: the credential it may be answered with, the card and the
	// card's project.
	credential string
	pendingID  string
	project    string
	// order counts the entries in the order they were added.
	order uint64
}

// expired reports whether the entry is expired at now: at its expiry it is.
func (e *challengeEntry) expired(now time.Time) bool { return !now.Before(e.ends) }

// challengeStore holds the outstanding challenges of the seat gate. It is
// the service's, so a restart, which makes a new service, drops every one:
// the owner taps again. It is safe for concurrent use.
type challengeStore struct {
	mu      sync.Mutex
	rand    io.Reader
	entries map[string]*challengeEntry
	next    uint64
	// log receives one line for each entry that expires or is dropped,
	// without anything of the entry but its kind and session.
	log func(line string)
}

func newChallengeStore(r io.Reader, log func(string)) *challengeStore {
	if r == nil {
		r = rand.Reader
	}
	if log == nil {
		log = func(string) {}
	}
	return &challengeStore{rand: r, entries: make(map[string]*challengeEntry), log: log}
}

// maxIDDraws bounds how often an id is drawn again because the store holds
// it already. With 8 random bytes and at most 48 entries a second draw is
// already unlikely; a source that keeps giving the same bytes is broken.
const maxIDDraws = 8

// errRandom says the random source failed or kept repeating itself.
var errRandom = errors.New("ownerchannel: the random source of the challenges failed")

// drawNonce draws a nonce of n bytes from the store's source.
func (c *challengeStore) drawNonce(n int) ([]byte, error) {
	b := make([]byte, n)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := io.ReadFull(c.rand, b); err != nil {
		return nil, errRandom
	}
	return b, nil
}

// drawID draws an id the store does not hold; c.mu is held.
func (c *challengeStore) drawID() (string, error) {
	var b [8]byte
	for i := 0; i < maxIDDraws; i++ {
		if _, err := io.ReadFull(c.rand, b[:]); err != nil {
			return "", errRandom
		}
		id := hex.EncodeToString(b[:])
		if _, taken := c.entries[id]; !taken {
			return id, nil
		}
	}
	return "", errRandom
}

// dropExpired drops the entries that are expired at now; c.mu is held.
func (c *challengeStore) dropExpired(now time.Time) {
	for id, e := range c.entries {
		if e.expired(now) {
			delete(c.entries, id)
			c.log("a " + e.kind.String() + " challenge of gate session " + e.session + " expired unused")
		}
	}
}

// count returns how many entries of a kind the store holds; c.mu is held.
func (c *challengeStore) count(k challengeKind) int {
	n := 0
	for _, e := range c.entries {
		if e.kind == k {
			n++
		}
	}
	return n
}

// add puts e in the store under a fresh id, which it sets, after the expired
// entries are dropped. A decision entry replaces, and burns, an entry of the
// same session, credential and pending id, and is refused
// [ErrChallengesFull] when [MaxDecisionChallenges] remain; a login entry
// drops the oldest login entry when [MaxLoginChallenges] remain.
func (c *challengeStore) add(e *challengeEntry, now time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dropExpired(now)
	switch e.kind {
	case challengeDecision:
		for id, o := range c.entries {
			if o.kind == challengeDecision && o.session == e.session && o.credential == e.credential && o.pendingID == e.pendingID {
				delete(c.entries, id)
				c.log("a decision challenge of gate session " + o.session + " was replaced by a new one for the same card and credential")
			}
		}
		if c.count(challengeDecision) >= MaxDecisionChallenges {
			return ErrChallengesFull
		}
	case challengeLogin:
		for c.count(challengeLogin) >= MaxLoginChallenges {
			var oldest *challengeEntry
			for _, o := range c.entries {
				if o.kind == challengeLogin && (oldest == nil || o.order < oldest.order) {
					oldest = o
				}
			}
			delete(c.entries, oldest.id)
			c.log("the oldest login challenge, of gate session " + oldest.session + ", was dropped to make room")
		}
	default:
		return errors.New("ownerchannel: a challenge of no kind")
	}
	id, err := c.drawID()
	if err != nil {
		return err
	}
	c.next++
	e.id, e.order = id, c.next
	c.entries[id] = e
	return nil
}

// take takes the entry of this id, kind and session out of the store, in one
// step under the store's lock, so that of two calls with one id only one has
// it. An entry of another kind or session is not found and stays where it is.
// The entry found is burnt whatever comes next; one that is expired is burnt
// and answered [ErrChallengeStale], as is an id no entry has.
func (c *challengeStore) take(id string, k challengeKind, session string, now time.Time) (*challengeEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[id]
	if !ok || e.kind != k || e.session != session {
		return nil, ErrChallengeStale
	}
	delete(c.entries, id)
	if e.expired(now) {
		c.log("a " + e.kind.String() + " challenge of gate session " + e.session + " was used after it expired")
		return nil, ErrChallengeStale
	}
	return e, nil
}

// size returns how many entries of a kind the store holds now, the expired
// ones included until something drops them. It is for the tests.
func (c *challengeStore) size(k challengeKind) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count(k)
}
