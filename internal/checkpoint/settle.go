package checkpoint

import (
	"bytes"
	"context"
	"crypto/ed25519"
)

func (a admin) Settle(ctx context.Context, slot Slot, id ID) (Receipt, error) {
	a.w.mu.Lock()
	defer a.w.mu.Unlock()
	return a.w.settle(ctx, slot, id)
}

func (c *client) Settle(ctx context.Context, slot Slot, id ID) (Receipt, error) {
	if slot != c.slot {
		return Receipt{}, &Reason{Kind: Unauthorized}
	}
	return c.w.SettleHome(ctx, slot, id, c.home.Public().(ed25519.PublicKey))
}

// SettleHome requires a live write grant because fencing an ID changes what
// may commit. Authentication and disposition share the commit lock.
func (w *Memory) SettleHome(ctx context.Context, slot Slot, id ID, home ed25519.PublicKey) (Receipt, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	g, ok := w.grants[slot]
	if !ok || !g.write || !bytes.Equal(g.key, home) {
		return Receipt{}, &Reason{Kind: Unauthorized}
	}
	if e := w.slots[slot]; e != nil && !bytes.Equal(e.home, home) {
		return Receipt{}, &Reason{Kind: Unauthorized}
	}
	return w.settle(ctx, slot, id)
}

func (w *Memory) settle(ctx context.Context, slot Slot, id ID) (Receipt, error) {
	if ctx.Err() != nil {
		return Receipt{}, ErrUnavailable
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
	if w.fenced[slot] == nil {
		w.fenced[slot] = make(map[ID]bool)
	}
	w.fenced[slot][id] = true
	w.receiptCount++
	return Receipt{}, ErrNotApplied
}
