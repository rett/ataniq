package link

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync/atomic"
	"testing"

	cp "ataniq/internal/checkpoint"
)

type dispositionFixture struct {
	memoryWitness
	fault atomic.Int32
}

func (w *dispositionFixture) SignDisposition(ctx context.Context, digest [32]byte, result string) ([]byte, error) {
	switch w.fault.Load() {
	case 1:
		digest[0] ^= 1
	case 2:
		return nil, nil
	}
	return w.Memory.SignDisposition(ctx, digest, result)
}
func (w *dispositionFixture) ReadHome(ctx context.Context, q cp.ReadRequest, key ed25519.PublicKey) (cp.View, error) {
	v, err := w.Memory.ReadHome(ctx, q, key)
	if w.fault.Load() == 3 && err == nil {
		v.Fresh.Signature = nil
	}
	return v, err
}
func TestDispositionEvidence(t *testing.T) {
	for _, network := range []string{"pipe", "tcp"} {
		t.Run(network, func(t *testing.T) {
			f := newFixture(t, "witness")
			id := identity(t, "home")
			e := endpointFor(t, id, f.id, f)
			r := serveNetwork(t, network, e.Serve)
			r.ServerPin = id.Fingerprint()
			other, err := cp.NewMemory("witness", 1, key(t))
			must(t, err)
			must(t, other.Grant(f.slot, public(f.home), true))
			stop := startPolling(t, f.s, r, memoryWitness{other})
			if c, err := e.Connect(t.Context(), f.slot, f.home, f.session); err == nil || c != nil {
				t.Fatal("grant from wrong backend signer accepted")
			}
			stop()
			w := &dispositionFixture{memoryWitness: memoryWitness{f.w}}
			startPolling(t, f.s, r, w)
			state := f.enroll(t, f.w.Administration())
			c, err := e.Connect(t.Context(), f.slot, f.home, f.session)
			must(t, err)
			w.fault.Store(3)
			if _, err = c.Read(t.Context(), cp.ReadRequest{Slot: f.slot, Challenge: cp.ID{4}}); !errors.Is(err, cp.ErrUnavailable) || errors.Is(err, cp.ErrRefused) {
				t.Fatalf("invalid read evidence: %v", err)
			}
			state.Revision++
			claim := cp.Claim{Mutation: cp.Mutation{ID: cp.ID{2}, Expected: state}, NewSessionPublicKey: public(f.session)}
			for _, fault := range []int32{0, 1, 2} {
				w.fault.Store(fault)
				_, err = c.Claim(t.Context(), claim)
				want := cp.ErrUnknownOutcome
				if fault == 0 {
					want = cp.ErrStale
				}
				if !errors.Is(err, want) {
					t.Fatalf("fault %d: %v", fault, err)
				}
			}
		})
	}
}
