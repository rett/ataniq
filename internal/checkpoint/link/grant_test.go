package link

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link/internal/frame"
)

type blockedGrant struct {
	Witness
	entered chan struct{}
}

func (w blockedGrant) CheckHome(ctx context.Context, _ cp.Slot, _ ed25519.PublicKey) error {
	close(w.entered)
	<-ctx.Done()
	return ctx.Err()
}
func TestGrantLookupCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w := blockedGrant{entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- authenticate(ctx, w, cp.Slot{}, nil, nil, nil) }()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("grant lookup did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("grant lookup did not stop")
	}
}

type countingWitness struct {
	memoryWitness
	lookups atomic.Int32
}

func (w *countingWitness) CheckHome(ctx context.Context, s cp.Slot, k ed25519.PublicKey) error {
	w.lookups.Add(1)
	return w.memoryWitness.CheckHome(ctx, s, k)
}

func TestUngrantedKeyPacingAndBudget(t *testing.T) {
	for _, network := range []string{"pipe", "tcp"} {
		t.Run(network, func(t *testing.T) {
			f := newFixture(t, "witness")
			id := identity(t, "home")
			e := endpointFor(t, id, f.id, f)
			r := serveNetwork(t, network, e.Serve)
			r.ServerPin = id.Fingerprint()
			var polls atomic.Int32
			dial := r.DialContext
			r.DialContext = func(ctx context.Context, n, a string) (net.Conn, error) { polls.Add(1); return dial(ctx, n, a) }
			for range 58 {
				if !f.s.limits.allow(false, ordinary) {
					t.Fatal("initial budget")
				}
			}
			w := &countingWitness{memoryWitness: memoryWitness{f.w}}
			startPolling(t, f.s, r, w)
			wrong := key(t)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			for i := 0; i < 16 && ctx.Err() == nil; i++ {
				if _, err := e.Connect(ctx, f.slot, wrong, f.session); err == nil {
					t.Fatal("granted foreign key")
				}
			}
			if n := polls.Load(); n > 10 || n < 3 {
				t.Fatalf("%d polls in one second", n)
			}
			if n := w.lookups.Load(); n != 2 {
				t.Fatalf("%d lookups with two credits", n)
			}
		})
	}
}

func TestOnlyCommittedReceiptsUseRecoveryBudget(t *testing.T) {
	for _, network := range []string{"pipe", "tcp"} {
		t.Run(network, func(t *testing.T) {
			f := newFixture(t, "witness")
			id := identity(t, "home")
			e := endpointFor(t, id, f.id, f)
			r := serveNetwork(t, network, e.Serve)
			r.ServerPin = id.Fingerprint()
			startPolling(t, f.s, r, memoryWitness{f.w})
			f.enroll(t, f.w.Administration())
			c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
			must(t, err)
			absent := cp.ReadRequest{Slot: f.slot, Challenge: cp.ID{1}, ReceiptID: cp.ID{99}}
			v, err := c.Read(t.Context(), absent)
			must(t, err)
			if v.Prior != nil {
				t.Fatal("unexpected receipt")
			}
			for range 58 {
				if !f.s.limits.allow(false, ordinary) {
					t.Fatal("ordinary window exhausted early")
				}
			}
			absent.Challenge[0]++
			if _, err = c.Read(t.Context(), absent); !errors.Is(err, cp.ErrUnavailable) {
				t.Fatalf("missing receipt spent recovery credit: %v", err)
			}
			found := absent
			found.ReceiptID = cp.ID{1}
			found.Challenge[0]++
			v, err = c.Read(t.Context(), found)
			must(t, err)
			if v.Prior == nil {
				t.Fatal("history lookup lost reserved credit")
			}
		})
	}
}

type lookupCounter struct {
	memoryWitness
	indexCalls int
}

func (w *lookupCounter) HasReceipt(ctx context.Context, s cp.Slot, id cp.ID) (bool, error) {
	w.indexCalls++
	return w.Memory.HasReceipt(ctx, s, id)
}
func TestReceiptIndexAfterChargeAndAuth(t *testing.T) {
	f := newFixture(t, "witness")
	w := &lookupCounter{memoryWitness: memoryWitness{f.w}}
	q := cp.ReadRequest{Slot: f.slot, Challenge: cp.ID{1}, ReceiptID: cp.ID{2}}
	body, err := readBody(q, f.s.witnessKeyID, "read", 0)
	must(t, err)
	sig, err := cp.Sign(cp.HomeNamespace, f.home, body)
	must(t, err)
	msg := frame.New(frame.Read, body, public(f.home), nil)
	_, err = f.s.dispatch(t.Context(), w, nil, w, msg)
	if !errors.Is(err, cp.ErrRefused) || w.indexCalls != 0 {
		t.Fatalf("bad signature: %d index calls, %v", w.indexCalls, err)
	}
	msg.Fields[2] = sig
	for range 60 {
		f.s.limits.allow(false, ordinary)
		f.s.limits.allow(false, recoveryReads)
	}
	_, err = f.s.dispatch(t.Context(), w, nil, w, msg)
	if !errors.Is(err, cp.ErrUnavailable) || w.indexCalls != 0 {
		t.Fatalf("exhausted budgets: %d index calls, %v", w.indexCalls, err)
	}
}
