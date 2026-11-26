// Package checkpointtest exercises the checkpoint contract through the home
// and owner capabilities. Adapters supply isolated provisioning and fault hooks.
package checkpointtest

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"testing"

	"ataniq/internal/assignment"
	"ataniq/internal/assignment/wire"
	cp "ataniq/internal/checkpoint"
)

// Harness is a newly empty witness named "witness" at epoch one. Connect
// establishes a new handle and verifies the independently supplied witness pin.
// LoseNextReply drops a mutation acknowledgement only after its commit.
type Harness struct {
	Admin           cp.Administration
	Grant           func(cp.Slot, ed25519.PublicKey, bool) error
	Connect         func(cp.Slot, ed25519.PrivateKey, ed25519.PrivateKey, ed25519.PublicKey) (cp.Client, error)
	Inhibit         func(bool)
	Revoke          func(cp.Slot)
	LoseNextReply   func()
	SetReceiptLimit func(uint64)
}

type Factory func(*testing.T, ed25519.PrivateKey) Harness

func id(label string) cp.ID                      { return sha256.Sum256([]byte(label)) }
func key(label string) ed25519.PrivateKey        { seed := id(label); return ed25519.NewKeyFromSeed(seed[:]) }
func pub(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }
func digest(label string) string                 { d := id(label); return hex.EncodeToString(d[:]) }

type fixture struct {
	Harness
	ctx context.Context
	pin ed25519.PublicKey
}

func start(t *testing.T, factory Factory) fixture {
	k := key("test witness")
	return fixture{factory(t, k), t.Context(), pub(k)}
}

type home struct {
	slot             cp.Slot
	private, session ed25519.PrivateKey
	e                cp.Enrollment
	c                cp.Client
}

func (f fixture) home(t *testing.T, authority, store, generation string, write bool) home {
	t.Helper()
	slot := cp.Slot{Witness: "witness", Authority: authority, Store: store}
	h := home{slot: slot, private: key(authority + generation), session: key("session " + authority + generation)}
	keyID, err := cp.KeyID(pub(h.private))
	check(t, err)
	header := assignment.Header{Schema: 1, Authority: authority, Store: store, LedgerID: "ledger" + generation, Incarnation: "incarnation" + generation, KeyID: keyID}
	raw, err := header.Bytes()
	check(t, err)
	h.e = cp.Enrollment{ID: id("enroll " + authority + generation), Slot: slot, HeaderBytes: raw, HomePublicKey: pub(h.private)}
	h.e.ProofOfPossession, err = cp.ProveEnrollment(h.private, h.e)
	check(t, err)
	check(t, f.Grant(slot, pub(h.private), write))
	h.c, err = f.Connect(slot, h.private, h.session, f.pin)
	check(t, err)
	return h
}
func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func want(t *testing.T, err, errorValue error) {
	t.Helper()
	if !errors.Is(err, errorValue) {
		t.Fatalf("got %v, want %v", err, errorValue)
	}
}
func same(a, b cp.Receipt) bool {
	return bytes.Equal(a.Body, b.Body) && bytes.Equal(a.Signature, b.Signature)
}
func (f fixture) state(t *testing.T, h home, label string) cp.State {
	t.Helper()
	q := cp.ReadRequest{Slot: h.slot, Challenge: id(label)}
	v, err := h.c.Read(f.ctx, q)
	check(t, err)
	check(t, cp.VerifyView(f.pin, q, v))
	return v.Current
}
func (f fixture) enroll(t *testing.T, h home) cp.State {
	t.Helper()
	r, err := f.Admin.Enroll(f.ctx, h.e)
	check(t, err)
	a, err := cp.VerifyReceipt(f.pin, r)
	check(t, err)
	l, err := assignment.NewLedgerFromHeader(h.e.HeaderBytes)
	check(t, err)
	if a.Operation != "enroll" || a.ID != h.e.ID || a.State.Head != l.Checkpoint() || a.State.Fence != 0 || a.State.SessionKeyID != "" ||
		a.State.Status != cp.Active || a.State.Revision != 1 || a.State.Epoch != 1 || a.State != f.state(t, h, "after enroll") {
		t.Fatalf("genesis: %+v", a)
	}
	return a.State
}
func mutation(s cp.State, label string) cp.Mutation { return cp.Mutation{ID: id(label), Expected: s} }
func advance(s cp.State, label string) cp.Advance {
	next := s.Head
	next.Sequence++
	next.Digest = digest(label)
	return cp.Advance{Mutation: mutation(s, label), Next: next}
}
func (f fixture) claim(t *testing.T, h home, s cp.State, label string) cp.State {
	t.Helper()
	r, err := h.c.Claim(f.ctx, cp.Claim{Mutation: mutation(s, label), NewSessionPublicKey: pub(h.session)})
	check(t, err)
	a, err := cp.VerifyReceipt(f.pin, r)
	check(t, err)
	if a.State.Head != s.Head || a.State.Fence != s.Fence+1 || a.State.Revision != s.Revision+1 || a.PredecessorDigest != s.LastReceiptDigest {
		t.Fatal("claim did not fence and link", a)
	}
	return a.State
}
func (f fixture) reconnect(t *testing.T, h home, session ed25519.PrivateKey) home {
	t.Helper()
	var err error
	h.c, err = f.Connect(h.slot, h.private, session, f.pin)
	check(t, err)
	h.session = session
	return h
}

// Run exercises the same state machine and recovery cases for every adapter.
// The factory must provision a separate directory for each call, including
// calls with identical keys used to model independent restores.
func Run(t *testing.T, newWitness Factory) {
	for _, op := range []string{"release", "retire"} {
		t.Run("stale owner "+op, func(t *testing.T) { ownerCAS(t, newWitness, op) })
	}
	for _, field := range []string{"release", "retire", "ledger", "incarnation", "key"} {
		t.Run("tombstone "+field, func(t *testing.T) { tombstone(t, newWitness, field) })
	}
	for _, op := range []string{"enroll", "release", "retire"} {
		t.Run("conflicting "+op, func(t *testing.T) { conflictingOwnerID(t, newWitness, op) })
	}
	t.Run("enrollment", func(t *testing.T) {
		f := start(t, newWitness)
		h := f.home(t, "west", "shared", "one", true)
		for name, alter := range map[string]func(*cp.Enrollment){
			"torn": func(e *cp.Enrollment) {
				e.HeaderBytes = e.HeaderBytes[:len(e.HeaderBytes)-1]
				var err error
				e.ProofOfPossession, err = cp.ProveEnrollment(h.private, *e)
				check(t, err)
			},
			"substituted header": func(e *cp.Enrollment) {
				e.HeaderBytes = bytes.Replace(e.HeaderBytes, []byte("ledgerone"), []byte("ledgertwo"), 1)
			},
			"wrong pin": func(e *cp.Enrollment) {
				e.HomePublicKey = pub(key("other"))
				var err error
				e.ProofOfPossession, err = cp.ProveEnrollment(key("other"), *e)
				check(t, err)
			},
			"wrong proof": func(e *cp.Enrollment) { e.ProofOfPossession = []byte("bad") },
		} {
			t.Run(name, func(t *testing.T) {
				e := h.e
				alter(&e)
				_, err := f.Admin.Enroll(f.ctx, e)
				want(t, err, cp.ErrRefused)
			})
		}
		f.LoseNextReply()
		_, err := f.Admin.Enroll(f.ctx, h.e)
		want(t, err, cp.ErrUnknownOutcome)
		q := cp.ReadRequest{Slot: h.slot, Challenge: id("fresh"), ReceiptID: h.e.ID}
		v, err := h.c.Read(f.ctx, q)
		check(t, err)
		check(t, cp.VerifyView(f.pin, q, v))
		r, err := f.Admin.Enroll(f.ctx, h.e)
		check(t, err)
		if v.Prior == nil || !same(*v.Prior, r) || v.Current.Revision != 1 {
			t.Fatal("lost enrollment")
		}
		e := h.e
		e.ID = id("another enrollment")
		e.ProofOfPossession, err = cp.ProveEnrollment(h.private, e)
		check(t, err)
		_, err = f.Admin.Enroll(f.ctx, e)
		want(t, err, cp.ErrStale)
		replacement := f.home(t, "west", "fresh", "two", true)
		_, err = f.Admin.Enroll(f.ctx, replacement.e)
		want(t, err, cp.ErrStale)
		_, err = f.Admin.Retire(f.ctx, cp.Retirement{Mutation: mutation(v.Current, "retire"), DispositionDigest: digest("disposed")})
		check(t, err)
		_, err = f.Admin.Enroll(f.ctx, h.e)
		want(t, err, cp.ErrStale)
		f.enroll(t, replacement)
	})
	t.Run("two homes and live grants", func(t *testing.T) {
		f := start(t, newWitness)
		a, b := f.home(t, "west", "shared", "one", true), f.home(t, "east", "shared", "one", false)
		sa, sb := f.enroll(t, a), f.enroll(t, b)
		if _, ok := a.c.(cp.Administration); ok {
			t.Fatal("home client exposes owner capability")
		}
		_, err := a.c.Read(f.ctx, cp.ReadRequest{Slot: b.slot, Challenge: id("foreign")})
		want(t, err, cp.ErrRefused)
		_, err = f.Connect(a.slot, b.private, b.session, f.pin)
		want(t, err, cp.ErrRefused)
		_, err = f.Connect(a.slot, a.private, a.session, pub(key("wrong witness")))
		want(t, err, cp.ErrRefused)
		sa = f.claim(t, a, sa, "claim west")
		_, err = b.c.Claim(f.ctx, cp.Claim{Mutation: mutation(sb, "claim east"), NewSessionPublicKey: pub(b.session)})
		want(t, err, cp.ErrRefused)
		check(t, f.Grant(b.slot, pub(b.private), true))
		sb = f.claim(t, b, sb, "claim east")
		for _, p := range []struct {
			h home
			s cp.State
		}{{a, sa}, {b, sb}} {
			_, err = p.h.c.Advance(f.ctx, advance(p.s, p.h.slot.Authority))
			check(t, err)
		}
		f.Inhibit(true)
		_, err = a.c.Advance(f.ctx, advance(sa, "inhibited"))
		want(t, err, cp.ErrUnavailable)
		_, err = a.c.Read(f.ctx, cp.ReadRequest{Slot: a.slot, Challenge: id("inhibited")})
		want(t, err, cp.ErrUnavailable)
		f.Inhibit(false)
		sa = f.state(t, a, "online")
		f.Revoke(a.slot)
		_, err = a.c.Advance(f.ctx, advance(sa, "revoked"))
		want(t, err, cp.ErrRefused)
		_, err = a.c.Read(f.ctx, cp.ReadRequest{Slot: a.slot, Challenge: id("revoked")})
		want(t, err, cp.ErrRefused)
		_, err = f.Admin.Retire(f.ctx, cp.Retirement{Mutation: mutation(sa, "lost key"), DispositionDigest: digest("fenced")})
		check(t, err)
		if f.state(t, b, "still online").Head.Sequence != 1 {
			t.Fatal("foreign grant affected east")
		}
	})
	for _, op := range []string{"release", "retire"} {
		t.Run("capacity owner "+op, func(t *testing.T) {
			f := start(t, newWitness)
			h := f.home(t, "west", "s", "one", true)
			s := f.claim(t, h, f.enroll(t, h), "claim")
			f.SetReceiptLimit(2)
			_, err := h.c.Advance(f.ctx, advance(s, "full"))
			want(t, err, cp.ErrUnavailable)
			m := mutation(s, "owner")
			var r cp.Receipt
			status := cp.Active
			if op == "release" {
				r, err = f.Admin.Release(f.ctx, m)
			} else {
				status = cp.Retired
				r, err = f.Admin.Retire(f.ctx, cp.Retirement{Mutation: m, DispositionDigest: digest("dispose")})
			}
			check(t, err)
			a, err := cp.VerifyReceipt(f.pin, r)
			check(t, err)
			if a.State.Fence != s.Fence+1 || a.State.SessionKeyID != "" || a.State.Status != status || a.State != f.state(t, h, "fenced") {
				t.Fatal("wrong fencing receipt")
			}
		})
	}
	t.Run("capacity preserves recovery", func(t *testing.T) {
		f := start(t, newWitness)
		h := f.home(t, "west", "s", "one", true)
		s := f.claim(t, h, f.enroll(t, h), "claim")
		q := advance(s, "last free receipt")
		f.SetReceiptLimit(3)
		f.LoseNextReply()
		_, err := h.c.Advance(f.ctx, q)
		want(t, err, cp.ErrUnknownOutcome)
		h = f.reconnect(t, h, h.session)
		read := cp.ReadRequest{Slot: h.slot, Challenge: id("at capacity"), ReceiptID: q.ID}
		v, err := h.c.Read(f.ctx, read)
		check(t, err)
		check(t, cp.VerifyView(f.pin, read, v))
		r, err := h.c.Advance(f.ctx, q)
		check(t, err)
		if v.Prior == nil || !same(*v.Prior, r) {
			t.Fatal("receipt lost at capacity")
		}
		_, err = h.c.Advance(f.ctx, advance(v.Current, "over limit"))
		want(t, err, cp.ErrUnavailable)
		if f.state(t, h, "still recoverable") != v.Current {
			t.Fatal("capacity refusal changed state")
		}
	})
	t.Run("inhibited retry", func(t *testing.T) {
		f := start(t, newWitness)
		h := f.home(t, "west", "s", "one", true)
		s := f.claim(t, h, f.enroll(t, h), "claim")
		q := advance(s, "first")
		f.LoseNextReply()
		_, err := h.c.Advance(f.ctx, q)
		want(t, err, cp.ErrUnknownOutcome)
		f.Inhibit(true)
		r, err := h.c.Advance(f.ctx, q)
		check(t, err)
		a, err := cp.VerifyReceipt(f.pin, r)
		check(t, err)
		if a.ID != q.ID || a.State.Head != q.Next {
			t.Fatal("retry lost committed advance")
		}
		next := advance(a.State, "second")
		_, err = h.c.Advance(f.ctx, next)
		want(t, err, cp.ErrUnavailable)
		f.Inhibit(false)
		_, err = h.c.Advance(f.ctx, next)
		check(t, err)
		f.Inhibit(true)
		_, err = h.c.Advance(f.ctx, q)
		want(t, err, cp.ErrStale)
	})
	t.Run("historical head", func(t *testing.T) {
		f := start(t, newWitness)
		h := f.home(t, "west", "s", "one", true)
		s := f.claim(t, h, f.enroll(t, h), "claim")
		first := advance(s, "first")
		r, err := h.c.Advance(f.ctx, first)
		check(t, err)
		a, err := cp.VerifyReceipt(f.pin, r)
		check(t, err)
		later, err := h.c.Advance(f.ctx, advance(a.State, "second"))
		check(t, err)
		b, err := cp.VerifyReceipt(f.pin, later)
		check(t, err)
		_, err = h.c.Advance(f.ctx, first)
		want(t, err, cp.ErrStale)
		first.Next.Digest = digest("different")
		_, err = h.c.Advance(f.ctx, first)
		want(t, err, cp.ErrStale)
		q := cp.ReadRequest{Slot: h.slot, Challenge: id("historical challenge"), ReceiptID: first.ID}
		v, err := h.c.Read(f.ctx, q)
		check(t, err)
		if v.Current != b.State {
			t.Fatal("historical lookup changed the current head")
		}
		check(t, cp.VerifyView(f.pin, q, v))
		if v.Prior == nil || !same(*v.Prior, r) {
			t.Fatal("missing historical receipt")
		}
	})
	t.Run("replacement retry", func(t *testing.T) {
		f := start(t, newWitness)
		h := f.home(t, "west", "s", "one", true)
		s := f.claim(t, h, f.enroll(t, h), "claim")
		q := advance(s, "saved advance")
		_, err := h.c.Advance(f.ctx, q)
		check(t, err)
		_, err = f.Admin.Release(f.ctx, mutation(f.state(t, h, "before release"), "release"))
		check(t, err)
		replacement := f.reconnect(t, h, key("replacement"))
		current := f.claim(t, replacement, f.state(t, h, "released"), "replace")
		_, err = replacement.c.Advance(f.ctx, q)
		want(t, err, cp.ErrStale)
		if f.state(t, replacement, "after retry") != current {
			t.Fatal("retry changed replacement state")
		}
	})
	t.Run("exact predecessor", func(t *testing.T) { exactPredecessor(t, start(t, newWitness)) })
	t.Run("partition and session replacement", func(t *testing.T) { recovery(t, start(t, newWitness)) })
	t.Run("independent restore", func(t *testing.T) { restore(t, newWitness) })
	t.Run("concurrent successors", func(t *testing.T) {
		f := start(t, newWitness)
		h := f.home(t, "west", "s", "one", true)
		s := f.claim(t, h, f.enroll(t, h), "claim")
		type result struct {
			q   cp.Advance
			r   cp.Receipt
			err error
		}
		results := make(chan result, 8)
		for i := 0; i < cap(results); i++ {
			go func() {
				q := advance(s, fmt.Sprint(i))
				r, err := h.c.Advance(f.ctx, q)
				results <- result{q, r, err}
			}()
		}
		winners := 0
		for range cap(results) {
			v := <-results
			if v.err != nil {
				want(t, v.err, cp.ErrStale)
				continue
			}
			winners++
			a, err := cp.VerifyReceipt(f.pin, v.r)
			check(t, err)
			if a.State.Head != v.q.Next {
				t.Fatal("wrong winner")
			}
		}
		if winners != 1 || f.state(t, h, "winner").Revision != s.Revision+1 {
			t.Fatal("winners", winners)
		}
	})
	t.Run("two session property", func(t *testing.T) { interleave(t, newWitness) })
}

func exactPredecessor(t *testing.T, f fixture) {
	h := f.home(t, "west", "s", "one", true)
	s := f.claim(t, h, f.enroll(t, h), "claim")
	for name, alter := range map[string]func(*cp.State){
		"witness": func(s *cp.State) { s.Slot.Witness += "x" }, "authority": func(s *cp.State) { s.Slot.Authority += "x" }, "store": func(s *cp.State) { s.Slot.Store += "x" },
		"header authority": func(s *cp.State) { s.Header.Authority += "x" }, "header store": func(s *cp.State) { s.Header.Store += "x" },
		"header incarnation": func(s *cp.State) { s.Header.Incarnation += "x" }, "header ledger": func(s *cp.State) { s.Header.LedgerID += "x" },
		"header key": func(s *cp.State) { s.Header.KeyID += "x" }, "header schema": func(s *cp.State) { s.Header.Schema++ },
		"head authority": func(s *cp.State) { s.Head.Authority += "x" }, "head store": func(s *cp.State) { s.Head.Store += "x" },
		"head incarnation": func(s *cp.State) { s.Head.Incarnation += "x" }, "head ledger": func(s *cp.State) { s.Head.LedgerID += "x" },
		"sequence": func(s *cp.State) { s.Head.Sequence++ }, "digest": func(s *cp.State) { s.Head.Digest = digest("different") },
		"revision": func(s *cp.State) { s.Revision++ }, "epoch": func(s *cp.State) { s.Epoch++ }, "fence": func(s *cp.State) { s.Fence++ },
		"session key": func(s *cp.State) { s.SessionKeyID += "x" }, "home key": func(s *cp.State) { s.HomeKeyID += "x" }, "status": func(s *cp.State) { s.Status = cp.Retired },
		"chain": func(s *cp.State) { s.LastReceiptDigest = digest("different") },
	} {
		t.Run(name, func(t *testing.T) {
			old := s
			alter(&old)
			_, err := h.c.Advance(f.ctx, advance(old, "cas"))
			if !errors.Is(err, cp.ErrRefused) && !errors.Is(err, cp.ErrStale) {
				t.Fatal("wrong CAS result", err)
			}
		})
	}
	for _, edit := range []func(*assignment.Checkpoint){
		func(n *assignment.Checkpoint) { n.Sequence = 0 }, func(n *assignment.Checkpoint) { n.Sequence = 2 }, func(n *assignment.Checkpoint) { n.Sequence = math.MaxUint64 },
		func(n *assignment.Checkpoint) { n.Authority += "x" }, func(n *assignment.Checkpoint) { n.Store += "x" }, func(n *assignment.Checkpoint) { n.Incarnation += "x" }, func(n *assignment.Checkpoint) { n.LedgerID += "x" },
	} {
		q := advance(s, "next")
		edit(&q.Next)
		_, err := h.c.Advance(f.ctx, q)
		want(t, err, cp.ErrStale)
	}
	if f.state(t, h, "still same") != s {
		t.Fatal("refusal changed predecessor")
	}
}

// stagedHeads replays the same immutable header with a complete signed
// frame and with that frame missing after a clean truncation.
func stagedHeads(t *testing.T, h home, header assignment.Header) (full, truncated assignment.Checkpoint) {
	t.Helper()
	l, err := assignment.NewLedgerFromHeader(h.e.HeaderBytes)
	check(t, err)
	row := assignment.Head{Key: assignment.Key{Home: header.Authority, Project: "fixture", Role: "lead"}}
	c := assignment.Command{
		Expected: row, Kind: assignment.Seed, TransferID: "seed", RequestID: "begin", Actor: "owner", Authorization: "decision",
		Candidate: assignment.Binding{Authority: header.Authority, Store: header.Store, Incarnation: header.Incarnation,
			Conversation: "conversation", Kind: "legacy", ID: "holder"},
	}
	for _, proof := range []struct{ kind, subject, outcome string }{{"enrollment", "holder", "verified"}, {"route", "qargi", "enabled"}} {
		c.Evidence = append(c.Evidence, assignment.Evidence{Kind: proof.kind, Subject: proof.subject, Outcome: proof.outcome,
			Revision: "snapshot", Issuer: "source", TransferID: c.TransferID, Scope: row, Digest: digest(proof.kind)})
	}
	event, _, err := assignment.Decide(l, c)
	check(t, err)
	signer, err := wire.NewSigner(header, h.private)
	check(t, err)
	body, err := event.SigningBytes()
	check(t, err)
	event.Signature, err = signer.Sign(header, body)
	check(t, err)
	raw, err := event.RecordBytes()
	check(t, err)
	stored, err := assignment.DecodeRecord(raw)
	check(t, err)
	v, err := wire.NewVerifier(header, pub(h.private))
	check(t, err)
	complete, err := assignment.ReplayFromHeader(h.e.HeaderBytes, []assignment.Event{stored}, v)
	check(t, err)
	cut, err := assignment.ReplayFromHeader(h.e.HeaderBytes, nil, v)
	check(t, err)
	return complete.Checkpoint(), cut.Checkpoint()
}

func recovery(t *testing.T, f fixture) {
	h := f.home(t, "west", "s", "one", true)
	genesis := f.enroll(t, h)
	s := f.claim(t, h, genesis, "claim")
	copyHome := f.reconnect(t, h, key("copied directory"))
	_, err := copyHome.c.Claim(f.ctx, cp.Claim{Mutation: mutation(s, "copy"), NewSessionPublicKey: pub(copyHome.session)})
	want(t, err, cp.ErrStale)
	full, cut := stagedHeads(t, h, s.Header)
	q := cp.Advance{Mutation: mutation(s, "staged intent"), Next: full}
	if cut != s.Head {
		t.Fatal("truncated replay does not match predecessor")
	}
	_, err = copyHome.c.Advance(f.ctx, q)
	want(t, err, cp.ErrRefused)
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	_, err = h.c.Advance(cancelled, q)
	want(t, err, cp.ErrUnavailable)
	if f.state(t, h, "before send") != s {
		t.Fatal("local staging advanced witness")
	}
	f.LoseNextReply()
	_, err = h.c.Advance(f.ctx, q)
	want(t, err, cp.ErrUnknownOutcome)
	h = f.reconnect(t, h, h.session)
	read := cp.ReadRequest{Slot: h.slot, Challenge: id("recover"), ReceiptID: q.ID}
	v, err := h.c.Read(f.ctx, read)
	check(t, err)
	check(t, cp.VerifyView(f.pin, read, v))
	other := read
	other.Challenge = id("new nonce")
	want(t, cp.VerifyView(f.pin, other, v), cp.ErrRefused)
	if v.Prior == nil || v.Current.Head != q.Next || assignment.VerifyCheckpoint(cut, v.Current.Head) == nil {
		t.Fatal("truncated ledger or missing receipt")
	}
	body, err := cp.AdvanceBody(q)
	check(t, err)
	a, err := cp.VerifyReceipt(f.pin, *v.Prior)
	check(t, err)
	if a.ID != q.ID || a.Operation != "advance" || a.RequestDigest != cp.RequestDigest(body) || a.PredecessorDigest != s.LastReceiptDigest || a.State != v.Current {
		t.Fatal("accepted wrong intent", a)
	}
	r, err := h.c.Advance(f.ctx, q)
	check(t, err)
	if !same(r, *v.Prior) || f.state(t, h, "once") != a.State {
		t.Fatal("retry applied twice")
	}
	conflict := q
	conflict.Next.Digest = digest("conflict")
	_, err = h.c.Advance(f.ctx, conflict)
	want(t, err, cp.ErrStale)
	conflict.ID = id("divergence")
	_, err = h.c.Advance(f.ctx, conflict)
	want(t, err, cp.ErrStale)
	r, err = f.Admin.Release(f.ctx, mutation(v.Current, "release"))
	check(t, err)
	released, err := cp.VerifyReceipt(f.pin, r)
	check(t, err)
	_, err = h.c.Claim(f.ctx, cp.Claim{Mutation: mutation(released.State, "reuse released key"), NewSessionPublicKey: pub(h.session)})
	want(t, err, cp.ErrStale)
	current := f.claim(t, copyHome, released.State, "replace")
	h = f.reconnect(t, h, h.session)
	_, err = h.c.Advance(f.ctx, q)
	want(t, err, cp.ErrRefused)
	_, err = h.c.Advance(f.ctx, advance(current, "wrong session"))
	want(t, err, cp.ErrRefused)
	read.Challenge = id("after replacement")
	v, err = h.c.Read(f.ctx, read)
	check(t, err)
	if v.Current != current || v.Prior == nil || a.State.Fence == current.Fence || cp.ReceiptDigest(*v.Prior) != a.State.LastReceiptDigest {
		t.Fatal("history reactivated old writer")
	}
	r, err = f.Admin.Retire(f.ctx, cp.Retirement{Mutation: mutation(current, "retire"), DispositionDigest: digest("fenced")})
	check(t, err)
	retired, err := cp.VerifyReceipt(f.pin, r)
	check(t, err)
	if retired.State.Status != cp.Retired || retired.State.Head != current.Head || retired.State.Fence <= current.Fence || retired.State.SessionKeyID != "" {
		t.Fatal("bad tombstone")
	}
	_, err = copyHome.c.Advance(f.ctx, advance(current, "after retire"))
	want(t, err, cp.ErrRetired)
}

func restore(t *testing.T, factory Factory) {
	// Two witnesses receive the identical prefix. One is later restored to
	// that prefix. A surviving receipt exposes it even when both home files
	// and the witness were restored together.
	f, backup := start(t, factory), start(t, factory)
	h, b := f.home(t, "west", "s", "one", true), backup.home(t, "west", "s", "one", true)
	s := f.claim(t, h, f.enroll(t, h), "claim")
	old := backup.claim(t, b, backup.enroll(t, b), "claim")
	if s != old {
		t.Fatal("fixture diverged before backup")
	}
	q := advance(s, "newer")
	r, err := h.c.Advance(f.ctx, q)
	check(t, err)
	archive, err := cp.VerifyReceipt(f.pin, r)
	check(t, err)
	restored := backup.state(t, b, "restore")
	if assignment.VerifyCheckpoint(q.Next, restored.Head) == nil || assignment.VerifyCheckpoint(old.Head, archive.State.Head) == nil ||
		archive.State.Revision <= restored.Revision || archive.State.LastReceiptDigest == restored.LastReceiptDigest {
		t.Fatal("independent rollback invisible")
	}
	if assignment.VerifyCheckpoint(old.Head, restored.Head) != nil || old != restored {
		t.Fatal("joint restore changed prefix")
	}
	_, err = b.c.Advance(backup.ctx, advance(archive.State, "restored witness"))
	want(t, err, cp.ErrStale)
}

func interleave(t *testing.T, factory Factory) {
	for seed := int64(0); seed < 6; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			random := rand.New(rand.NewSource(seed))
			f := start(t, factory)
			a := f.home(t, "west", "s", "one", true)
			b := f.reconnect(t, a, key("second session"))
			s := f.claim(t, a, f.enroll(t, a), "claim")
			homes := []home{a, b}
			cached := []cp.State{s, s}
			heads := map[uint64]string{s.Head.Sequence: s.Head.Digest}
			observe := func(s cp.State) {
				t.Helper()
				if d, ok := heads[s.Head.Sequence]; ok && d != s.Head.Digest {
					t.Fatalf("sequence %d accepted both %s and %s", s.Head.Sequence, d, s.Head.Digest)
				}
				heads[s.Head.Sequence] = s.Head.Digest
			}
			for step := 0; step < 70; step++ {
				i := random.Intn(2)
				h, old := homes[i], cached[i]
				label := fmt.Sprint(seed, "/", step)
				var r cp.Receipt
				var err error
				op := random.Intn(9)
				// Start with a committed successor so even an early release
				// races a home holding a valid but truncated prefix.
				if step == 0 {
					i = 0
					h, old = homes[i], cached[i]
					op = 5
				}
				switch op {
				case 0, 1:
					cached[i] = f.state(t, h, label)
					observe(cached[i])
					continue
				case 2:
					r, err = f.Admin.Release(f.ctx, mutation(old, label))
				case 3:
					r, err = h.c.Claim(f.ctx, cp.Claim{Mutation: mutation(old, label), NewSessionPublicKey: pub(h.session)})
				case 4:
					if step > 40 {
						r, err = f.Admin.Retire(f.ctx, cp.Retirement{Mutation: mutation(old, label), DispositionDigest: digest(label)})
					} else {
						continue
					}
				default:
					q := advance(old, label)
					r, err = h.c.Advance(f.ctx, q)
					if err == nil {
						observe(cp.State{Head: q.Next})
					}
				}
				if err != nil {
					if !errors.Is(err, cp.ErrStale) && !errors.Is(err, cp.ErrRefused) && !errors.Is(err, cp.ErrRetired) {
						t.Fatal(err)
					}
					continue
				}
				ack, err := cp.VerifyReceipt(f.pin, r)
				check(t, err)
				observe(ack.State)
				cached[i] = ack.State
			}
			if len(heads) < 2 {
				t.Fatal("no accepted successor")
			}
		})
	}
}
