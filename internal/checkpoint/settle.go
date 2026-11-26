package checkpoint

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"math"
)

func (a admin) Settle(ctx context.Context, epoch uint64, slot Slot, id ID) (Receipt, error) {
	a.w.mu.Lock()
	defer a.w.mu.Unlock()
	return a.w.settle(ctx, epoch, slot, id)
}

func (c *client) Settle(ctx context.Context, epoch uint64, slot Slot, id ID) (Receipt, error) {
	if slot != c.slot {
		return Receipt{}, &Reason{Kind: Unauthorized}
	}
	return c.w.SettleHome(ctx, epoch, slot, id, c.home.public)
}

// SettleHome requires a live write grant because fencing an ID changes what
// may commit. Authentication and disposition share the commit lock.
func (w *Memory) SettleHome(ctx context.Context, epoch uint64, slot Slot, id ID, home ed25519.PublicKey) (Receipt, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	g, ok := w.grants[slot]
	if !ok || !g.write || !bytes.Equal(g.key, home) {
		return Receipt{}, &Reason{Kind: Unauthorized}
	}
	if e := w.slots[slot]; e != nil && !bytes.Equal(e.home, home) {
		return Receipt{}, &Reason{Kind: Unauthorized}
	}
	return w.settle(ctx, epoch, slot, id)
}

func (w *Memory) settle(ctx context.Context, epoch uint64, slot Slot, id ID) (Receipt, error) {
	if ctx.Err() != nil {
		return Receipt{}, ErrUnavailable
	}
	if epoch != w.epoch {
		return Receipt{}, ErrStale
	}
	if !slot.valid() || slot.Witness != w.witness || id == (ID{}) {
		return Receipt{}, &Reason{Kind: InvalidRequest}
	}
	if e := w.slots[slot]; e != nil {
		if saved, ok := e.history[id]; ok {
			return cloneReceipt(saved.receipt), nil
		}
	}
	if w.fenced[slot][id] {
		return Receipt{}, ErrNotApplied
	}
	if w.inhibited || w.receiptLimit != 0 && w.receiptCount >= w.receiptLimit {
		return Receipt{}, ErrUnavailable
	}
	if w.receiptCount >= math.MaxUint64-1 {
		return Receipt{}, &Reason{Kind: CounterExhausted}
	}
	if w.fenced[slot] == nil {
		w.fenced[slot] = make(map[ID]bool)
	}
	w.fenced[slot][id] = true
	w.receiptCount++
	return Receipt{}, ErrNotApplied
}
