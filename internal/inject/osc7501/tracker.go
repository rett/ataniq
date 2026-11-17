package osc7501

import (
	"sync"
	"time"
)

// Event is what Tracker.Observe made of one payload.
type Event int

// The three kinds of payload.
const (
	// EventInvalid is a payload that is neither a query nor a readable report.
	EventInvalid Event = iota
	// EventQuery is the support query.
	EventQuery
	// EventReport is a valid report; it is now the last one.
	EventReport
)

// String names the event.
func (e Event) String() string {
	switch e {
	case EventQuery:
		return "query"
	case EventReport:
		return "report"
	}
	return "invalid"
}

// Counts are the totals a Tracker has seen.
type Counts struct {
	Queries int
	Reports int
	Invalid int
}

// Tracker remembers the last valid report of one agent and counts what it was
// shown. It is safe for concurrent use. The zero Tracker is ready.
type Tracker struct {
	mu          sync.Mutex
	counts      Counts
	last        Report
	lastAt      time.Time
	hasLast     bool
	lastInvalid time.Time
	hasInvalid  bool
}

// Observe classifies one OSC 7501 payload received at the given time and
// counts it. A valid report replaces the last report (state=clear included).
// An invalid payload leaves the last report alone but records its time, see
// LastInvalid. A query changes only its count.
func (t *Tracker) Observe(payload []byte, at time.Time) Event {
	if IsQuery(payload) {
		t.mu.Lock()
		t.counts.Queries++
		t.mu.Unlock()
		return EventQuery
	}
	r, err := Parse(payload)
	t.mu.Lock()
	defer t.mu.Unlock()
	if err != nil {
		t.counts.Invalid++
		t.lastInvalid, t.hasInvalid = at, true
		return EventInvalid
	}
	t.counts.Reports++
	t.last, t.lastAt, t.hasLast = r, at, true
	return EventReport
}

// Last returns the last valid report and the time it was observed. The bool is
// false when none has arrived.
func (t *Tracker) Last() (Report, time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last, t.lastAt, t.hasLast
}

// LastInvalid returns the time of the last unreadable payload. The bool is
// false when there was none. A caller that finds this time later than the
// last report's should treat the agent's state as unknown: something arrived
// that may have changed it and could not be read.
func (t *Tracker) LastInvalid() (time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastInvalid, t.hasInvalid
}

// Counts returns the totals so far.
func (t *Tracker) Counts() Counts {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts
}
