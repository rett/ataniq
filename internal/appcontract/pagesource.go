package appcontract

import (
	"sync"
	"time"

	"ataniq/internal/appmodel"
)

// Retention bounds the entries one segment keeps: the newest MaxEntries, none
// older than MaxAge. A zero field is no bound on that axis. Pruning an entry
// moves the segment's floor, which readers meet as a gap; it never touches a
// command's receipt or status.
type Retention struct {
	MaxEntries int
	MaxAge     time.Duration
}

// MemorySource is an in-memory EntrySource for one lead's conversation,
// for fixtures and for adapters that hold entries in memory. A viewer reads
// only after Grant; its disclosure is the function given there. Every change
// of grant or revocation moves the disclosure projection generation, so cursors
// minted before it stop decoding. All methods are safe for concurrent use.
type MemorySource struct {
	mu           sync.Mutex
	clock        func() time.Time
	lead         string
	conversation appmodel.ConversationRef
	retention    Retention
	segment      uint64
	projection   uint64
	start, floor uint64
	next         uint64
	entries      []Entry
	statuses     map[appmodel.CommandKey]appmodel.CurrentStatus
	viewers      map[string]func(Entry) Disclosure
}

// NewMemorySource returns an empty source whose first segment has generation
// 1 and whose first entry will have sequence 1. The clock stamps entries and
// ages them out; it is never read otherwise.
func NewMemorySource(lead string, conversation appmodel.ConversationRef, clock func() time.Time, retention Retention) *MemorySource {
	return &MemorySource{clock: clock, lead: lead, conversation: conversation, retention: retention,
		segment: 1, projection: 1, start: 1, floor: 1, next: 1,
		statuses: map[appmodel.CommandKey]appmodel.CurrentStatus{}, viewers: map[string]func(Entry) Disclosure{}}
}

// Append adds an entry to the segment, stamping its sequence and time, and
// returns the sequence. The caller's Seq, At and Disclosure are ignored.
func (m *MemorySource) Append(e Entry) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	e.Seq, e.At, e.Disclosure = m.next, m.clock(), Disclosed
	if e.Receipt != nil {
		rc := *e.Receipt
		e.Receipt = &rc
	}
	m.next++
	m.entries = append(m.entries, e)
	m.prune()
	return e.Seq
}

// NewSegment starts a fresh readable segment: a new generation, sequences from
// 1 again, and no entries. Statuses survive, as receipts outlive display entries.
func (m *MemorySource) NewSegment() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.segment++
	m.start, m.floor, m.next, m.entries = 1, 1, 1, nil
}

// SetStatus records a command's current status.
func (m *MemorySource) SetStatus(st appmodel.CurrentStatus) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[st.Command] = st
}

// Grant lets viewer read, with disclose deciding each entry (nil discloses
// all), and moves the projection generation.
func (m *MemorySource) Grant(viewer string, disclose func(Entry) Disclosure) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if disclose == nil {
		disclose = func(Entry) Disclosure { return Disclosed }
	}
	m.viewers[viewer] = disclose
	m.projection++
}

// Revoke ends viewer's right to read and moves the projection generation.
func (m *MemorySource) Revoke(viewer string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.viewers, viewer)
	m.projection++
}

// prune drops entries beyond the retention bounds and moves the floor. The
// caller holds the lock.
func (m *MemorySource) prune() {
	drop := 0
	if n := m.retention.MaxEntries; n > 0 && len(m.entries) > n {
		drop = len(m.entries) - n
	}
	if age := m.retention.MaxAge; age > 0 {
		cutoff := m.clock().Add(-age)
		for drop < len(m.entries) && m.entries[drop].At.Before(cutoff) {
			drop++
		}
	}
	if drop > 0 {
		m.entries = append([]Entry(nil), m.entries[drop:]...)
	}
	m.floor = m.next
	if len(m.entries) > 0 {
		m.floor = m.entries[0].Seq
	}
}

// Snapshot implements EntrySource. A viewer without a grant, and a
// conversation or lead the source does not hold, both answer hidden_or_absent,
// so a probe learns nothing from the difference. The snapshot is taken under
// one lock, so entries, head, floor and generations agree.
func (m *MemorySource) Snapshot(viewer string, conversation appmodel.ConversationRef, lead string) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	disclose, ok := m.viewers[viewer]
	if !ok || conversation != m.conversation || lead != m.lead {
		return Snapshot{}, appmodel.DomainError{Code: appmodel.HiddenOrAbsent}
	}
	m.prune()
	s := Snapshot{Segment: m.segment, Projection: m.projection, Start: m.start, Floor: m.floor, Head: m.next - 1,
		Entries: make([]Entry, len(m.entries)), Statuses: map[appmodel.CommandKey]appmodel.CurrentStatus{}}
	for i, e := range m.entries {
		e.Disclosure = disclose(e)
		if e.Receipt != nil {
			rc := *e.Receipt
			e.Receipt = &rc
		}
		s.Entries[i] = e
		if st, ok := m.statuses[e.Command]; ok && e.Disclosure == Disclosed {
			s.Statuses[e.Command] = st
		}
	}
	return s, nil
}
