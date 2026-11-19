package ownerchannel

import (
	"encoding/json"
	"time"

	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/record"
)

// index is what the service keeps in memory of the journal, so that a
// listing does not read the whole journal each time. It is made by reading
// the entries after the last one it saw, and holds nothing the journal
// lacks: a new service builds it again from the file.
type index struct {
	// seq is the sequence number of the last entry read.
	seq int64
	// byID holds what the journal says of each pending id.
	byID map[string]*idRecord
	// alerted holds the pending ids that have an alert that is not a
	// limit mark.
	alerted map[string]bool
	// counted holds, for each pending id that has a count, whether it met
	// the terms. A count with no pending id adds to the two totals below.
	counted       map[string]bool
	bareRequests  int64
	bareWouldMeet int64
}

// idRecord is what the journal says of one pending id, the latest of each.
type idRecord struct {
	// decidedAt is the time of the last decision or held entry.
	decidedAt time.Time
	// approve says whether the last decision entry was an approve, and
	// decided whether there is a decision entry at all. delivered says
	// that the last decision entry has its delivered entry, whatever the
	// door answered.
	decided   bool
	approve   bool
	delivered bool
	// useRefused holds the codes the door refused a use of the approval
	// with, each of which has a use entry.
	useRefused map[record.Code]bool
	// refusedByDoor is the code the door refused the last decision with,
	// while no decision or held entry came after it, or empty. The card is
	// undecided again then.
	refusedByDoor record.Code
	// outcome is the last outcome entry.
	outcomeState record.State
	outcomeLine  string
	outcomeAt    time.Time
}

func newIndex() index {
	return index{
		byID:    make(map[string]*idRecord),
		alerted: make(map[string]bool),
		counted: make(map[string]bool),
	}
}

func (x *index) rec(id string) *idRecord {
	r := x.byID[id]
	if r == nil {
		r = &idRecord{}
		x.byID[id] = r
	}
	return r
}

// bodies of the entries the index reads.
type (
	indexDecision struct {
		Approve bool `json:"approve"`
	}
	indexCode struct {
		Code string `json:"code"`
	}
	indexOutcome struct {
		Line  string `json:"line"`
		State string `json:"state"`
	}
	indexAlert struct {
		LimitReached bool `json:"limit_reached"`
	}
	indexCount struct {
		Met bool `json:"met"`
	}
)

// add takes one entry into the index. An entry whose body does not read is
// skipped: the journal checked its chain, and a body this version does not
// know says nothing the index needs.
func (x *index) add(e ownerjournal.Entry) {
	x.seq = e.Seq
	switch e.Kind {
	case ownerjournal.KindDecision:
		var b indexDecision
		if json.Unmarshal(e.Body, &b) != nil {
			return
		}
		r := x.rec(e.PendingID)
		r.decided, r.approve, r.decidedAt = true, b.Approve, e.At
		r.delivered, r.refusedByDoor = false, ""
	case ownerjournal.KindHeld:
		r := x.rec(e.PendingID)
		r.decidedAt, r.refusedByDoor = e.At, ""
	case ownerjournal.KindDelivered:
		var b indexCode
		if json.Unmarshal(e.Body, &b) != nil {
			return
		}
		r := x.rec(e.PendingID)
		r.delivered = true
		if b.Code != "" {
			r.refusedByDoor = record.Code(b.Code)
		}
	case ownerjournal.KindUse:
		var b indexCode
		if json.Unmarshal(e.Body, &b) != nil || b.Code == "" {
			return
		}
		r := x.rec(e.PendingID)
		if r.useRefused == nil {
			r.useRefused = make(map[record.Code]bool)
		}
		r.useRefused[record.Code(b.Code)] = true
	case ownerjournal.KindOutcome:
		var b indexOutcome
		if json.Unmarshal(e.Body, &b) != nil {
			return
		}
		r := x.rec(e.PendingID)
		r.outcomeState, r.outcomeLine, r.outcomeAt = record.State(b.State), b.Line, e.At
	case ownerjournal.KindAlert:
		var b indexAlert
		if json.Unmarshal(e.Body, &b) == nil && !b.LimitReached && e.PendingID != "" {
			x.alerted[e.PendingID] = true
		}
	case ownerjournal.KindCount:
		var b indexCount
		if json.Unmarshal(e.Body, &b) != nil {
			return
		}
		if e.PendingID != "" {
			x.counted[e.PendingID] = b.Met
			return
		}
		x.bareRequests++
		if b.Met {
			x.bareWouldMeet++
		}
	}
}

// totals returns how many requests were counted and how many would have met
// the terms of the first standing approval.
func (x *index) totals() (requests, wouldMeet int64) {
	requests, wouldMeet = x.bareRequests, x.bareWouldMeet
	for _, met := range x.counted {
		requests++
		if met {
			wouldMeet++
		}
	}
	return requests, wouldMeet
}

// syncIndex reads the journal entries the index has not seen.
func (s *Service) syncIndex() error {
	for {
		s.mu.Lock()
		after := s.idx.seq
		st := s.store
		s.mu.Unlock()
		entries, err := st.Entries(after, 500)
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			return nil
		}
		s.mu.Lock()
		if s.idx.seq == after {
			for _, e := range entries {
				s.idx.add(e)
			}
		}
		s.mu.Unlock()
	}
}

// useRefusedBefore reports whether the journal has a use entry that says the
// door refused a use of the approval with the code.
func (s *Service) useRefusedBefore(id string, c record.Code) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.idx.byID[id]
	return r != nil && r.useRefused[c]
}

// indexed returns a copy of what the index holds for id.
func (s *Service) indexed(id string) (idRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.idx.byID[id]
	if r == nil {
		return idRecord{}, false
	}
	return *r, true
}
