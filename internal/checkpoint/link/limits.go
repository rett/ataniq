package link

import (
	"sync"
	"time"
)

type allowance byte

const (
	ordinary allowance = iota
	recoveryReads
)

// Each authenticated signing key has a rolling minute of 60 calls in each
// of the ordinary and recovery classes, shared across its connections. Only a key with a live grant and a
// valid signature can consume a home budget.
type minute struct {
	times [60]time.Time
	next  int
}

func (m *minute) take(now time.Time) bool {
	if now.Sub(m.times[m.next]) < time.Minute {
		return false
	}
	m.times[m.next] = now
	m.next = (m.next + 1) % len(m.times)
	return true
}

type peerBudget struct {
	windows [2]minute
	last    time.Time
}
type limiter struct {
	mu    sync.Mutex
	peers map[string]*peerBudget
	owner peerBudget
}

func (l *limiter) allow(key string, pool allowance) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if key == "owner" {
		return l.owner.windows[pool].take(now)
	}
	if l.peers == nil {
		l.peers = make(map[string]*peerBudget)
	}
	p := l.peers[key]
	if p == nil {
		if len(l.peers) >= 256 {
			for k, b := range l.peers {
				if now.Sub(b.last) >= time.Minute {
					delete(l.peers, k)
				}
			}
			if len(l.peers) >= 256 {
				return false
			}
		}
		p = &peerBudget{}
		l.peers[key] = p
	}
	if !p.windows[pool].take(now) {
		return false
	}
	p.last = now
	return true
}
