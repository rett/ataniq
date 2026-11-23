package gatehost

import (
	"sync"
	"time"
)

// waitSlots counts the seat.wait calls held for each peer, by its key
// fingerprint (see boundKey), and when a wait-full was last logged for it.
// The zero value is ready; it is safe for concurrent use. A peer's entry is
// dropped when its last slot is given back and no log time is kept for it.
type waitSlots struct {
	mu    sync.Mutex
	held  map[string]int
	logAt map[string]time.Time
}

// take claims one of the peer's [MaxWaitsPerPeer] slots and reports whether
// there was one.
func (w *waitSlots) take(key string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.held == nil {
		w.held = map[string]int{}
	}
	if w.held[key] >= MaxWaitsPerPeer {
		return false
	}
	w.held[key]++
	return true
}

// give returns a slot taken with take.
func (w *waitSlots) give(key string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.held[key] <= 1 {
		delete(w.held, key)
		return
	}
	w.held[key]--
}

// mayLogFull reports whether a wait-full for the peer may be logged now, and
// if so notes it. The peers are the pinned keys, at most [maxPeers] of them
// are kept; past that the oldest notes are dropped by age.
func (w *waitSlots) mayLogFull(key string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.logAt == nil {
		w.logAt = map[string]time.Time{}
	}
	if last, ok := w.logAt[key]; ok && now.Sub(last) < waitFullLogEvery {
		return false
	}
	if len(w.logAt) >= maxPeers {
		for k, t := range w.logAt {
			if now.Sub(t) >= waitFullLogEvery {
				delete(w.logAt, k)
			}
		}
	}
	if len(w.logAt) < maxPeers {
		w.logAt[key] = now
	}
	return true
}
