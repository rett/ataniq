package main

import (
	"context"
	"crypto/ed25519"

	"ataniq/internal/authorityhost/decisionguard"
	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
)

// Serialize each guard lookup and the operation using it across both links.
// The durable store also enforces its own storage inhibition and write lock.
type guardedWitness struct {
	*durable.Witness
	slot         cp.Slot
	dir          string
	recoveryOnly bool
	turn         chan struct{}
}

type ownerAdmin struct{ g *guardedWitness }

func guarded[T any](ctx context.Context, g *guardedWitness, op func() (T, error)) (T, error) {
	select {
	case g.turn <- struct{}{}:
		defer func() { <-g.turn }()
	case <-ctx.Done():
		var zero T
		return zero, cp.ErrUnavailable
	}
	blocked := decisionguard.Check(g.dir) != nil
	g.Inhibit(blocked || g.recoveryOnly)
	return op()
}

func (g *guardedWitness) ReadHome(ctx context.Context, q cp.ReadRequest, k ed25519.PublicKey) (cp.View, error) {
	return guarded(ctx, g, func() (cp.View, error) { return g.Witness.ReadHome(ctx, q, k) })
}
func (g *guardedWitness) RawSubmit(ctx context.Context, s cp.Slot, k ed25519.PublicKey, op string, b, hs, ss []byte) (cp.Receipt, error) {
	return guarded(ctx, g, func() (cp.Receipt, error) { return g.Witness.RawSubmit(ctx, s, k, op, b, hs, ss) })
}
func (g *guardedWitness) SettleHome(ctx context.Context, e uint64, s cp.Slot, id cp.ID, k ed25519.PublicKey) (cp.Receipt, error) {
	return guarded(ctx, g, func() (cp.Receipt, error) { return g.Witness.SettleHome(ctx, e, s, id, k) })
}
func (a ownerAdmin) Read(ctx context.Context, q cp.ReadRequest) (cp.View, error) {
	if q.Slot != a.g.slot {
		return cp.View{}, &cp.Reason{Kind: cp.Unauthorized}
	}
	return guarded(ctx, a.g, func() (cp.View, error) { return a.g.Administration().Read(ctx, q) })
}
func (a ownerAdmin) Enroll(ctx context.Context, q cp.Enrollment) (cp.Receipt, error) {
	return guarded(ctx, a.g, func() (cp.Receipt, error) { return a.g.Administration().Enroll(ctx, q) })
}
func (a ownerAdmin) Release(ctx context.Context, q cp.Mutation) (cp.Receipt, error) {
	if q.Expected.Slot != a.g.slot {
		return cp.Receipt{}, &cp.Reason{Kind: cp.Unauthorized}
	}
	return guarded(ctx, a.g, func() (cp.Receipt, error) { return a.g.Administration().Release(ctx, q) })
}
func (a ownerAdmin) Retire(ctx context.Context, q cp.Retirement) (cp.Receipt, error) {
	if q.Expected.Slot != a.g.slot {
		return cp.Receipt{}, &cp.Reason{Kind: cp.Unauthorized}
	}
	return guarded(ctx, a.g, func() (cp.Receipt, error) { return a.g.Administration().Retire(ctx, q) })
}
func (a ownerAdmin) Settle(ctx context.Context, e uint64, s cp.Slot, id cp.ID) (cp.Receipt, error) {
	return guarded(ctx, a.g, func() (cp.Receipt, error) { return a.g.Administration().Settle(ctx, e, s, id) })
}
func (a ownerAdmin) Status(ctx context.Context, id cp.ID) (cp.Receipt, error) {
	return guarded(ctx, a.g, func() (cp.Receipt, error) { return a.g.Administration().Status(ctx, id) })
}
func (a ownerAdmin) AcknowledgeStorageError(ctx context.Context) error {
	_, err := guarded(ctx, a.g, func() (struct{}, error) {
		if decisionguard.Check(a.g.dir) != nil {
			return struct{}{}, cp.ErrUnavailable
		}
		return struct{}{}, a.g.Administration().AcknowledgeStorageError(ctx)
	})
	return err
}
