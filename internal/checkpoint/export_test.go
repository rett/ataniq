package checkpoint

// KeepLastClaim models a witness that remembers only the current writer.
func (w *Memory) KeepLastClaim(slot Slot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	e := w.slots[slot]
	e.claimed = map[string]bool{e.state.SessionKeyID: true}
}
