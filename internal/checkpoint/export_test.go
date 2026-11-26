package checkpoint

import "crypto/ed25519"

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
	return &client{w, slot, home, session, pin}
}
