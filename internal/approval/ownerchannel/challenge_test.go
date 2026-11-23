package ownerchannel

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// decisionEntry is a decision challenge of a session for a card.
func decisionEntry(session, credential, pending string) *challengeEntry {
	return &challengeEntry{kind: challengeDecision, session: session, credential: credential, pendingID: pending,
		nonce: make([]byte, 32), challenge: make([]byte, 32), ends: start.Add(ChallengeLife)}
}

// loginEntry is a login challenge of a session.
func loginEntry(session string) *challengeEntry {
	return &challengeEntry{kind: challengeLogin, session: session, nonce: make([]byte, 32), challenge: make([]byte, 32), ends: start.Add(ChallengeLife)}
}

const (
	sessA = "aaaaaaaaaaaaaaaa"
	sessB = "bbbbbbbbbbbbbbbb"
)

// lines collects a store's log.
type lines struct {
	mu sync.Mutex
	l  []string
}

func (l *lines) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.l = append(l.l, s)
}

func (l *lines) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.l...)
}

// The 33rd decision challenge is refused while the other 32 live, and is
// taken once one of them expired: the expired ones are dropped first.
func TestTheDecisionStoreHolds32AndDropsTheExpiredFirst(t *testing.T) {
	log := &lines{}
	c := newChallengeStore(nil, log.add)
	for i := 0; i < MaxDecisionChallenges; i++ {
		e := decisionEntry(sessA, "0123456789abcdef", fmt.Sprintf("ap-20261120-%04d", i))
		if i == 0 {
			e.ends = start.Add(time.Minute)
		}
		if err := c.add(e, start); err != nil {
			t.Fatalf("entry %d: %v", i, err)
		}
		if len(e.id) != 16 || strings.Trim(e.id, "0123456789abcdef") != "" {
			t.Fatalf("the id %q is not 16 lower-case hex digits", e.id)
		}
	}
	extra := decisionEntry(sessA, "0123456789abcdef", "ap-20261120-0099")
	if err := c.add(extra, start); !errors.Is(err, ErrChallengesFull) {
		t.Fatalf("the 33rd: %v", err)
	}
	// Login entries never take a decision's room, and a full login store
	// does not refuse a decision.
	for i := 0; i < MaxLoginChallenges+3; i++ {
		if err := c.add(loginEntry(sessA), start); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.add(extra, start); !errors.Is(err, ErrChallengesFull) {
		t.Fatalf("the 33rd after logins: %v", err)
	}
	// At its expiry the first one is expired, and dropped before the 33rd is
	// judged.
	if err := c.add(extra, start.Add(time.Minute)); err != nil {
		t.Fatalf("the 33rd after one expired: %v", err)
	}
	if n := c.size(challengeDecision); n != MaxDecisionChallenges {
		t.Fatalf("%d decision entries", n)
	}
	found := false
	for _, l := range log.all() {
		found = found || strings.Contains(l, "decision challenge of gate session "+sessA+" expired unused")
	}
	if !found {
		t.Fatalf("the drop was not logged: %q", log.all())
	}
}

// An entry of the same session, credential and pending id is replaced and
// burnt (the owner tapped again), so it takes no room; another session,
// credential or card is not replaced.
func TestADecisionChallengeReplacesTheSameTap(t *testing.T) {
	c := newChallengeStore(nil, nil)
	first := decisionEntry(sessA, "0123456789abcdef", "ap-20261120-0007")
	if err := c.add(first, start); err != nil {
		t.Fatal(err)
	}
	for _, other := range []*challengeEntry{
		decisionEntry(sessB, "0123456789abcdef", "ap-20261120-0007"),
		decisionEntry(sessA, "fedcba9876543210", "ap-20261120-0007"),
		decisionEntry(sessA, "0123456789abcdef", "ap-20261120-0008"),
	} {
		if err := c.add(other, start); err != nil {
			t.Fatal(err)
		}
	}
	again := decisionEntry(sessA, "0123456789abcdef", "ap-20261120-0007")
	if err := c.add(again, start); err != nil {
		t.Fatal(err)
	}
	if n := c.size(challengeDecision); n != 4 {
		t.Fatalf("%d entries, want 4", n)
	}
	if _, err := c.take(first.id, challengeDecision, sessA, start); !errors.Is(err, ErrChallengeStale) {
		t.Fatalf("the replaced entry was found: %v", err)
	}
	if e, err := c.take(again.id, challengeDecision, sessA, start); err != nil || e != again {
		t.Fatalf("the new entry: %v", err)
	}
	// With every room taken by the same tap of one session, a new tap of
	// that tap replaces rather than being refused.
	c = newChallengeStore(nil, nil)
	for i := 0; i < MaxDecisionChallenges; i++ {
		if err := c.add(decisionEntry(sessA, "0123456789abcdef", fmt.Sprintf("ap-20261120-%04d", i)), start); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.add(decisionEntry(sessA, "0123456789abcdef", "ap-20261120-0005"), start); err != nil {
		t.Fatalf("a tap again in a full store: %v", err)
	}
}

// A 17th login challenge drops the oldest login entry, never a decision's.
func TestAFullLoginStoreDropsItsOldest(t *testing.T) {
	log := &lines{}
	c := newChallengeStore(nil, log.add)
	d := decisionEntry(sessA, "0123456789abcdef", "ap-20261120-0007")
	if err := c.add(d, start); err != nil {
		t.Fatal(err)
	}
	var logins []*challengeEntry
	for i := 0; i < MaxLoginChallenges+1; i++ {
		e := loginEntry(sessB)
		if err := c.add(e, start.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
		logins = append(logins, e)
	}
	if n := c.size(challengeLogin); n != MaxLoginChallenges {
		t.Fatalf("%d login entries", n)
	}
	if _, err := c.take(logins[0].id, challengeLogin, sessB, start); !errors.Is(err, ErrChallengeStale) {
		t.Fatal("the oldest login entry is still there")
	}
	for _, e := range logins[1:] {
		if got, err := c.take(e.id, challengeLogin, sessB, start); err != nil || got != e {
			t.Fatalf("a newer login entry was dropped: %v", err)
		}
	}
	if got, err := c.take(d.id, challengeDecision, sessA, start); err != nil || got != d {
		t.Fatalf("the decision entry was dropped: %v", err)
	}
	if l := log.all(); len(l) != 1 || !strings.Contains(l[0], "oldest login challenge") {
		t.Fatalf("log %q", l)
	}
}

// take burns the entry it finds, whatever comes next: a second take of the
// id is stale. An entry of another session or kind is not found and not
// burnt. An expired one is stale and burnt.
func TestTakeBurnsOnlyWhatItFinds(t *testing.T) {
	c := newChallengeStore(nil, nil)
	d := decisionEntry(sessA, "0123456789abcdef", "ap-20261120-0007")
	l := loginEntry(sessA)
	for _, e := range []*challengeEntry{d, l} {
		if err := c.add(e, start); err != nil {
			t.Fatal(err)
		}
	}
	for name, try := range map[string]func() error{
		"another session":     func() error { _, err := c.take(d.id, challengeDecision, sessB, start); return err },
		"the login kind":      func() error { _, err := c.take(d.id, challengeLogin, sessA, start); return err },
		"a decision for l":    func() error { _, err := c.take(l.id, challengeDecision, sessA, start); return err },
		"an id no entry has":  func() error { _, err := c.take("0000000000000000", challengeDecision, sessA, start); return err },
		"a login of sessionB": func() error { _, err := c.take(l.id, challengeLogin, sessB, start); return err },
	} {
		if err := try(); !errors.Is(err, ErrChallengeStale) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if c.size(challengeDecision) != 1 || c.size(challengeLogin) != 1 {
		t.Fatal("a take that found nothing burnt an entry")
	}
	if got, err := c.take(d.id, challengeDecision, sessA, start); err != nil || got != d {
		t.Fatalf("the owner's take: %v", err)
	}
	if _, err := c.take(d.id, challengeDecision, sessA, start); !errors.Is(err, ErrChallengeStale) {
		t.Fatalf("a replay: %v", err)
	}
	// At its expiry, and after, the entry is stale and gone.
	if _, err := c.take(l.id, challengeLogin, sessA, l.ends); !errors.Is(err, ErrChallengeStale) {
		t.Fatalf("an expired login: %v", err)
	}
	if c.size(challengeLogin) != 0 {
		t.Fatal("the expired entry was not burnt")
	}
}

// Of many calls that take one id at once, exactly one has it.
func TestOneTakeOfManyWins(t *testing.T) {
	for round := 0; round < 20; round++ {
		c := newChallengeStore(nil, nil)
		d := decisionEntry(sessA, "0123456789abcdef", "ap-20261120-0007")
		if err := c.add(d, start); err != nil {
			t.Fatal(err)
		}
		var wins, stale atomic.Int64
		var wg sync.WaitGroup
		gate := make(chan struct{})
		for i := 0; i < 16; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-gate
				_, err := c.take(d.id, challengeDecision, sessA, start)
				switch {
				case err == nil:
					wins.Add(1)
				case errors.Is(err, ErrChallengeStale):
					stale.Add(1)
				}
			}()
		}
		close(gate)
		wg.Wait()
		if wins.Load() != 1 || stale.Load() != 15 {
			t.Fatalf("round %d: %d won, %d stale", round, wins.Load(), stale.Load())
		}
	}
}

// repeating gives the same bytes for its first draws, then the system's.
type repeating struct {
	left int
	b    []byte
}

func (r *repeating) Read(p []byte) (int, error) {
	if r.left > 0 {
		r.left--
		return copy(p, bytes.Repeat(r.b, len(p))), nil
	}
	return rand.Read(p)
}

// An id that the store holds already is drawn again; a source that keeps
// giving one id, or fails, refuses the challenge rather than reuse an id.
func TestAnIDTakenIsDrawnAgain(t *testing.T) {
	r := &repeating{left: 2, b: []byte{7}}
	c := newChallengeStore(r, nil)
	a := decisionEntry(sessA, "0123456789abcdef", "ap-20261120-0007")
	b := decisionEntry(sessA, "0123456789abcdef", "ap-20261120-0008")
	if err := c.add(a, start); err != nil {
		t.Fatal(err)
	}
	if err := c.add(b, start); err != nil {
		t.Fatal(err)
	}
	if a.id != "0707070707070707" || b.id == a.id {
		t.Fatalf("ids %s and %s", a.id, b.id)
	}
	stuck := newChallengeStore(&repeating{left: 1 << 30, b: []byte{9}}, nil)
	if err := stuck.add(decisionEntry(sessA, "0123456789abcdef", "ap-20261120-0007"), start); err != nil {
		t.Fatal(err)
	}
	if err := stuck.add(decisionEntry(sessA, "0123456789abcdef", "ap-20261120-0008"), start); !errors.Is(err, errRandom) {
		t.Fatalf("a stuck source: %v", err)
	}
	broken := newChallengeStore(io.LimitReader(rand.Reader, 0), nil)
	if _, err := broken.drawNonce(32); !errors.Is(err, errRandom) {
		t.Fatalf("a failed source: %v", err)
	}
}
