package link

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

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
			startPolling(t, f.s, r, w)
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
			_, err = replacement.Settle(t.Context(), f.slot, first.ID)
			if !errors.Is(err, cp.ErrNotApplied) {
				t.Fatal(err)
			}
			resume()
			if err := <-w.result; !errors.Is(err, cp.ErrNotApplied) {
				t.Fatalf("fenced work returned %v", err)
			}
			receipt, err := replacement.Claim(t.Context(), second)
			must(t, err)
			settled, err := c.Settle(t.Context(), f.slot, second.ID)
			must(t, err)
			if cp.ReceiptDigest(receipt) != cp.ReceiptDigest(settled) {
				t.Fatal("settlement changed receipt")
			}
		})
	}
}
