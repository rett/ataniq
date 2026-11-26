package link

import (
	"sync"
	"time"
)

type allowance byte

const (
	ordinary allowance = iota
	recoveryReads
	reconcile
)

type minute struct {
	times [60]time.Time
	next  int
}

func (m *minute) take(now time.Time, capacity int) bool {
	if now.Sub(m.times[m.next]) < time.Minute {
		return false
	}
	m.times[m.next] = now
	m.next = (m.next + 1) % capacity
	return true
}

// Budgets belong to configured endpoints and survive connection and key changes.
type limiter struct {
	now         func() time.Time
	mu          sync.Mutex
	home, owner [3]minute
}

func (l *limiter) allow(owner bool, pool allowance) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	capacity := 60
	if pool == reconcile {
		capacity = 10
	}
	if owner {
		return l.owner[pool].take(now, capacity)
	}
	return l.home[pool].take(now, capacity)
}
