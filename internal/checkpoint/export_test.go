package checkpoint

import (
	"crypto/ed25519"
	"maps"
)

// KeepLastClaim models a witness that remembers only the current writer.
func (w *Memory) KeepLastClaim(slot Slot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e := w.slots[slot]
	e.claimed = map[string]bool{e.state.SessionKeyID: true}
}

func (w *Memory) ForgetRetiredSlot(slot Slot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if e := w.slots[slot]; e != nil && e.state.Status == Retired {
		delete(w.slots, slot)
	}
}

// Used after Connect's only refusal was a key-role conflict.
func (w *Memory) ClientWithOverlappingRoles(slot Slot, home, session ed25519.PrivateKey, pin ed25519.PublicKey) Client {
	return &client{w, slot, newMemoryKey(home, homeProfile), newMemoryKey(session, sessionProfile), pin}
}

func (w *Memory) ForgetFence(s Slot, id ID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.fenced[s], id)
}

// ReopenForTest models a fresh adapter around a snapshot of authoritative
// state. Stored receipts, requests and keys are immutable.
func (w *Memory) ReopenForTest() *Memory {
	w.mu.Lock()
	defer w.mu.Unlock()
	next := &Memory{witness: w.witness, epoch: w.epoch, key: w.key, keyID: w.keyID,
		grants: maps.Clone(w.grants), slots: maps.Clone(w.slots), fenced: maps.Clone(w.fenced),
		inhibited: w.inhibited, receiptCount: w.receiptCount, receiptLimit: w.receiptLimit}
	for slot, e := range w.slots {
		entry := *e
		entry.history = maps.Clone(e.history)
		entry.claimed = maps.Clone(e.claimed)
		next.slots[slot] = &entry
	}
	for slot, ids := range w.fenced {
		next.fenced[slot] = maps.Clone(ids)
	}
	return next
}
