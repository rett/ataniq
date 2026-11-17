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
	// stale is set by an unreadable payload and cleared by a valid report: it
	// records the order of arrival, which the times cannot (several payloads
	// of one read carry the same time).
	stale bool
}

// Status is everything a caller needs to know about the last report, read at
// once. Has is false until a valid report arrived. Stale is true when an
// unreadable payload arrived after the last valid report, or when unreadable
// payloads arrived and no valid report ever did: the agent's state is then
// unknown, and Report is the last state that could be read, not the current
// one. A stale report must never be treated as the agent's state.
type Status struct {
	Report Report
	At     time.Time
	Has    bool
	Stale  bool
}

// Observe classifies one OSC 7501 payload received at the given time and
// counts it. A valid report replaces the last report (state=clear included).
// An invalid payload leaves the last report alone but records its time, see
// LastInvalid, and marks the report stale until the next valid report, see
// Status. A query changes only its count.
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
		t.stale = true
		return EventInvalid
	}
	t.counts.Reports++
	t.last, t.lastAt, t.hasLast = r, at, true
	t.stale = false
	return EventReport
}

// Status returns the last valid report, its time, whether there is one and
// whether an unreadable payload arrived after it. It does not change the
// tracker.
func (t *Tracker) Status() Status {
	t.mu.Lock()
	defer t.mu.Unlock()
	return Status{Report: t.last, At: t.lastAt, Has: t.hasLast, Stale: t.stale}
}

// Last returns the last valid report and the time it was observed. The bool is
// false when none has arrived.
func (t *Tracker) Last() (Report, time.Time, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.last, t.lastAt, t.hasLast
}

// LastInvalid returns the time of the last unreadable payload. The bool is
// false when there was none. Do not compare it with the last report's time to
// find out whether the report is stale: payloads of one read share a time. Use
// Status.Stale.
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
