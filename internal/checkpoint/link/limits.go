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
	receiptIndex
)

var capacities = [...]int{ordinary: 60, recoveryReads: 60, reconcile: 10, receiptIndex: 120}

type minute struct {
	times []time.Time
	next  int
}

func (m *minute) take(now time.Time, capacity int) bool {
	if !m.available(now) {
		return false
	}
	if m.times == nil {
		m.times = make([]time.Time, capacity)
	}
	m.times[m.next] = now
	m.next = (m.next + 1) % capacity
	return true
}

func (m *minute) available(now time.Time) bool {
	return m.times == nil || now.Sub(m.times[m.next]) >= time.Minute
}

// Budgets belong to configured endpoints and survive connection and key changes.
type limiter struct {
	now         func() time.Time
	mu          sync.Mutex
	home, owner [len(capacities)]minute
}

func (l *limiter) allow(owner bool, pool allowance) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	windows := &l.home
	if owner {
		windows = &l.owner
	}
	if pool == receiptIndex && !windows[ordinary].available(now) && !windows[recoveryReads].available(now) {
		return false
	}
	return windows[pool].take(now, capacities[pool])
}
