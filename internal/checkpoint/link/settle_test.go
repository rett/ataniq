package link

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"ataniq/internal/seatgate"

	cp "ataniq/internal/checkpoint"
)

type continuingWitness struct {
	memoryWitness
	delayed atomic.Bool
	resume  chan struct{}
	result  chan error
}

func (w *continuingWitness) RawSubmit(ctx context.Context, s cp.Slot, h ed25519.PublicKey, op string, b, hs, ss []byte) (cp.Receipt, error) {
	if w.delayed.CompareAndSwap(false, true) {
		go func() {
			<-w.resume
			_, err := w.Memory.RawSubmit(context.WithoutCancel(ctx), s, h, op, b, hs, ss)
			w.result <- err
		}()
		return cp.Receipt{}, cp.ErrUnknownOutcome
	}
	return w.Memory.RawSubmit(ctx, s, h, op, b, hs, ss)
}

func TestSettleFencesContinuingWork(t *testing.T) {
	for _, network := range []string{"pipe", "tcp"} {
		t.Run(network, func(t *testing.T) {
			f := newFixture(t, "witness")
			id := identity(t, "home")
			e := endpointFor(t, id, f.id, f)
			r := serveNetwork(t, network, e.Serve)
			r.ServerPin = id.Fingerprint()
			w := &continuingWitness{memoryWitness: memoryWitness{f.w}, resume: make(chan struct{}), result: make(chan error, 1)}
			var once sync.Once
			resume := func() { once.Do(func() { close(w.resume) }) }
			defer resume()
			stop := startPolling(t, f.s, r, w)
			s := f.enroll(t, f.w.Administration())
			c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
			must(t, err)
			first := cp.Claim{Mutation: cp.Mutation{ID: cp.ID{2}, Expected: s}, NewSessionPublicKey: public(f.session)}
			_, err = c.Claim(t.Context(), first)
			if !errors.Is(err, cp.ErrUnknownOutcome) {
				t.Fatal(err)
			}
			q := cp.ReadRequest{Slot: f.slot, ReceiptID: first.ID, Challenge: cp.ID{3}}
			v, err := c.Read(t.Context(), q)
			must(t, err)
			if v.Prior != nil {
				t.Fatal("unexpected receipt before commit")
			}
			saved, ok := e.PendingMutation()
			body, err := first.Body(e.home.wid)
			must(t, err)
			if !ok || saved.ID != first.ID || saved.Digest != cp.RequestDigest(body) {
				t.Fatal("wrong journal record")
			}
			stop()
			cert := seatgate.ServerTLS(f.id, nil).Certificates[0].Certificate[0]
			e, err = NewEndpointWithPending(id, cert, f.pin, 1, f.slot, loopbackTrust(), saved)
			must(t, err)
			r = serveNetwork(t, network, e.Serve)
			r.ServerPin = id.Fingerprint()
			startPolling(t, f.s, r, w)
			replacement, err := e.Connect(t.Context(), f.slot, f.home, f.session)
			must(t, err)
			second := first
			second.ID = cp.ID{4}
			changed := first
			changed.Expected.Revision++
			for _, candidate := range []cp.Claim{second, changed} {
				_, err = replacement.Claim(t.Context(), candidate)
				if !errors.Is(err, ErrNotSent) {
					t.Fatalf("admitted conflicting mutation: %v", err)
				}
			}
			_, err = replacement.Settle(t.Context(), 1, f.slot, first.ID)
			if !errors.Is(err, cp.ErrNotApplied) {
				t.Fatal(err)
			}
			if _, ok := e.PendingMutation(); ok {
				t.Fatal("fenced intent still pending")
			}
			resume()
			if err := <-w.result; !errors.Is(err, cp.ErrNotApplied) {
				t.Fatalf("fenced work returned %v", err)
			}
			receipt, err := replacement.Claim(t.Context(), second)
			must(t, err)
			settled, err := replacement.Settle(t.Context(), 1, f.slot, second.ID)
			must(t, err)
			if cp.ReceiptDigest(receipt) != cp.ReceiptDigest(settled) {
				t.Fatal("settlement changed receipt")
			}
		})
	}
}

func TestStaleOwnerCannotFenceNewEpoch(t *testing.T) {
	f := newFixture(t, "witness")
	k := key(t)
	var err error
	f.w, err = cp.NewMemory(f.slot.Witness, 2, k)
	must(t, err)
	f.pin = public(k)
	f.s, err = NewServer(f.id, f.pin, f.s.home, f.slot)
	must(t, err)
	must(t, f.w.Grant(f.slot, public(f.home), true))
	state := f.enroll(t, f.w.Administration())
	ownerID := identity(t, "owner")
	stale := f.endpoint(testOwner(t, f.s, f.w, f.w.Administration(), ownerID.Fingerprint()))
	a, err := stale.Administration(ownerID)
	must(t, err)
	id := cp.ID{2}
	if _, err = a.Settle(t.Context(), 1, f.slot, id); !errors.Is(err, cp.ErrUnknownOutcome) {
		t.Fatalf("stale owner received %v", err)
	}
	c, err := f.w.Connect(f.slot, f.home, f.session, f.pin)
	must(t, err)
	_, err = c.Claim(t.Context(), cp.Claim{Mutation: cp.Mutation{ID: id, Expected: state}, NewSessionPublicKey: public(f.session)})
	if err != nil {
		t.Fatalf("epoch-2 claim after epoch-1 settlement: %v", err)
	}
}

func TestReconcileAfterOrdinaryWindowFull(t *testing.T) {
	f := newFixture(t, "witness")
	e, r := f.listener(t)
	w := &continuingWitness{memoryWitness: memoryWitness{f.w}, resume: make(chan struct{}), result: make(chan error, 1)}
	defer close(w.resume)
	startPolling(t, f.s, r, w)
	s := f.enroll(t, f.w.Administration())
	c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
	must(t, err)
	claim := cp.Claim{Mutation: cp.Mutation{ID: cp.ID{2}, Expected: s}, NewSessionPublicKey: public(f.session)}
	_, err = c.Claim(t.Context(), claim)
	if !errors.Is(err, cp.ErrUnknownOutcome) {
		t.Fatal(err)
	}
	q := cp.ReadRequest{Slot: f.slot}
	for i := 1; i <= 58; i++ {
		q.Challenge[0] = byte(i)
		_, err = c.Read(t.Context(), q)
		must(t, err)
	}
	q.Challenge[0]++
	if _, err = c.Read(t.Context(), q); !errors.Is(err, cp.ErrUnavailable) {
		t.Fatalf("ordinary budget: %v", err)
	}
	q.ReceiptID = claim.ID
	for range 9 {
		q.Challenge[0]++
		v, err := c.Read(t.Context(), q)
		if err != nil || v.Prior != nil {
			t.Fatalf("pending recovery read: prior=%v err=%v", v.Prior != nil, err)
		}
	}
	_, err = c.Settle(t.Context(), 1, f.slot, claim.ID)
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatalf("settle after 60 ordinary calls: %v", err)
	}
	if _, ok := e.PendingMutation(); ok {
		t.Fatal("fence did not resolve intent")
	}
	_, err = c.Settle(t.Context(), 1, f.slot, claim.ID)
	if !errors.Is(err, cp.ErrUnavailable) {
		t.Fatalf("reconcile limit: %v", err)
	}
}

type pausedSubmit struct {
	memoryWitness
	entered, resume chan struct{}
}

func (w pausedSubmit) RawSubmit(ctx context.Context, s cp.Slot, key ed25519.PublicKey, op string, b, hs, ss []byte) (cp.Receipt, error) {
	close(w.entered)
	select {
	case <-ctx.Done():
		return cp.Receipt{}, ctx.Err()
	case <-w.resume:
		return w.Memory.RawSubmit(ctx, s, key, op, b, hs, ss)
	}
}

// The independent owner route can fence a synchronous home submission
// that has reached the witness but has not entered its commit transaction.
func TestOwnerSettleWhileSubmitInFlight(t *testing.T) {
	f := newFixture(t, "witness")
	e, r := f.listener(t)
	w := pausedSubmit{memoryWitness{f.w}, make(chan struct{}), make(chan struct{})}
	startPolling(t, f.s, r, w)
	ownerID := identity(t, "owner")
	a, err := f.endpoint(testOwner(t, f.s, f.w, f.w.Administration(), ownerID.Fingerprint())).Administration(ownerID)
	must(t, err)
	s := f.enroll(t, a)
	c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
	must(t, err)
	q := cp.Claim{Mutation: cp.Mutation{ID: cp.ID{2}, Expected: s}, NewSessionPublicKey: public(f.session)}
	result := make(chan error, 1)
	go func() { _, err := c.Claim(t.Context(), q); result <- err }()
	select {
	case <-w.entered:
	case err := <-result:
		t.Fatalf("claim did not reach backend: %v", err)
	}
	_, settled := a.Settle(t.Context(), 1, f.slot, q.ID)
	close(w.resume)
	if !errors.Is(settled, cp.ErrNotApplied) {
		t.Fatal(settled)
	}
	if err := <-result; !errors.Is(err, cp.ErrNotApplied) {
		t.Fatalf("in-flight commit escaped fence: %v", err)
	}
}
