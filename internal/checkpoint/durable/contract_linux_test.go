package durable

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"os"
	"path/filepath"
	"sync"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/checkpointtest"
)

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func testKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte(label))
	return ed25519.NewKeyFromSeed(seed[:])
}
func public(k ed25519.PrivateKey) ed25519.PublicKey { return k.Public().(ed25519.PublicKey) }
func config(t *testing.T, k ed25519.PrivateKey) Config {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	must(t, os.Mkdir(dir, 0700))
	keyPath := filepath.Join(root, "key")
	must(t, os.WriteFile(keyPath, k.Seed(), 0600))
	return Config{Directory: dir, KeyPath: keyPath, Pin: public(k), Identity: Identity{"witness", "store", "incarnation", 1}}
}

// Delivery faults are outside the backend, between its durable commit and
// verification by the home. No fixture mutation changes the stored bytes.
type delivery struct {
	mu        sync.Mutex
	lose      bool
	transform func(cp.Receipt) cp.Receipt
}

func (d *delivery) deliver(r cp.Receipt, err error) (cp.Receipt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		return r, err
	}
	if d.lose {
		d.lose = false
		return cp.Receipt{}, cp.ErrUnknownOutcome
	}
	if d.transform != nil {
		f := d.transform
		d.transform = nil
		r = f(r)
	}
	return r, nil
}

type deliveredAdmin struct {
	cp.Administration
	d *delivery
}

func (a deliveredAdmin) Enroll(ctx context.Context, q cp.Enrollment) (cp.Receipt, error) {
	return a.d.deliver(a.Administration.Enroll(ctx, q))
}
func (a deliveredAdmin) Release(ctx context.Context, q cp.Mutation) (cp.Receipt, error) {
	return a.d.deliver(a.Administration.Release(ctx, q))
}
func (a deliveredAdmin) Retire(ctx context.Context, q cp.Retirement) (cp.Receipt, error) {
	return a.d.deliver(a.Administration.Retire(ctx, q))
}

func TestWitnessContract(t *testing.T) {
	checkpointtest.Run(t, func(t *testing.T, k ed25519.PrivateKey) checkpointtest.Harness {
		c := config(t, k)
		w, err := Create(c)
		must(t, err)
		return harness(t, c, w)
	})
}
func harness(t *testing.T, c Config, w *Witness) checkpointtest.Harness {
	t.Helper()
	t.Cleanup(func() { must(t, w.Close()) })
	w.Inhibit(false)
	d := new(delivery)
	return checkpointtest.Harness{
		Restart: func() checkpointtest.Harness {
			grants := w.grants // independently supplied service configuration
			must(t, w.Close())
			next, err := Open(c)
			must(t, err)
			for s, g := range grants {
				must(t, next.Grant(s, g.pin, g.write))
			}
			return harness(t, c, next)
		},
		Admin: deliveredAdmin{w.Administration(), d}, Grant: w.Grant, Revoke: w.Revoke, Inhibit: w.Inhibit,
		LoseNextReply:      func() { d.mu.Lock(); defer d.mu.Unlock(); d.lose = true },
		TransformNextReply: func(f func(cp.Receipt) cp.Receipt) { d.mu.Lock(); defer d.mu.Unlock(); d.transform = f },
		SetReceiptLimit: func(limit uint64) {
			must(t, w.enter(t.Context()))
			defer w.unlock()
			if limit == 0 {
				limit = maxReceipts
			}
			w.limit = limit
		},
		RawSubmit: w.RawSubmit,
		Connect: func(slot cp.Slot, h, s ed25519.PrivateKey, p ed25519.PublicKey) (cp.Client, error) {
			handle, err := w.Connect(slot, h, s, p)
			if err != nil {
				return nil, err
			}
			c := handle.(*client)
			c.submit = func(ctx context.Context, slot cp.Slot, k ed25519.PublicKey, op string, b, hs, ss []byte) (cp.Receipt, error) {
				return d.deliver(w.RawSubmit(ctx, slot, k, op, b, hs, ss))
			}
			return c, nil
		}}
}
