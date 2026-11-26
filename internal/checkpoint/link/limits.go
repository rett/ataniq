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

// Budgets belong to configured endpoints and survive connection and key changes.
type limiter struct {
	now         func() time.Time
	mu          sync.Mutex
	home, owner [2]minute
}

func (l *limiter) allow(owner bool, pool allowance) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	if owner {
		return l.owner[pool].take(now)
	}
	return l.home[pool].take(now)
}
