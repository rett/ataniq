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
