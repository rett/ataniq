package doortest

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/record"
)

// The numbers of the design that the stand-in holds.
const (
	// RequestsPerMinute is how many requests a seat may have admitted
	// within one minute. A further one is refused request-rate.
	RequestsPerMinute = 6
	// MaxNotFinal is how many requests of a seat may be not yet final. A
	// further one is refused request-pending-cap.
	MaxNotFinal = 8
	// RepeatLimit is how many requests for one verb and target are taken
	// within RepeatWindow. The next is refused request-throttled, and so
	// is every one for that verb and target for ThrottleFor after it.
	// When that time is over, requests are judged against the hour just
	// past again: the throttle starts anew while three are still in it.
	RepeatLimit  = 3
	RepeatWindow = time.Hour
	ThrottleFor  = 10 * time.Minute
	// PendingLife is how long a pending record waits for a decision.
	PendingLife = 24 * time.Hour
	// ApprovalLife is the longest an approval may last from the moment it
	// was decided: a decision whose expires_at is later than that after its
	// decided_at is refused decision-invalid.
	ApprovalLife = 30 * time.Minute
)

// Seat is the one seat the stand-in knows: what the real door reads from
// the assignment and the seat record.
type Seat struct {
	// Project and RoleName name the seat.
	Project  string
	RoleName string
	// Epoch is the seat's present epoch, and SeatRev the revision of its
	// record.
	Epoch   int64
	SeatRev int64
	// Holder is who holds the seat.
	Holder record.Holder
	// Unassigned says the role has no assignment: every request is then
	// refused not-assigned.
	Unassigned bool
}

// Config is what a stand-in is made from.
type Config struct {
	// Now is the clock. It is required; [Clock] gives one a test can move.
	Now func() time.Time
	// Seat is the seat. The zero Seat stands for the sample seat of
	// [Sample].
	Seat Seat
	// Planner is the planner's name in every pending record, PlanToken the
	// plan token, and Command the word a plan text starts with. Each has
	// the value of [Sample] when it is empty.
	Planner   string
	PlanToken string
	Command   string
	// FirstNumber is the number in the first pending id; the ids are
	// ap-YYYYMMDD-NNNN, with the clock's date and a number that counts
	// up. Zero stands for 1.
	FirstNumber int
}

// Sample returns a configuration with neutral sample names: a seat of
// project-a held by a lead, at epoch 7 and revision 12, as in the vectors
// of the package record.
func Sample(now func() time.Time) Config {
	return Config{
		Now: now,
		Seat: Seat{
			Project:  "project-a",
			RoleName: "role-a",
			Epoch:    7,
			SeatRev:  12,
			Holder:   record.Holder{Kind: "lead", DisplayID: "role-a@e7", Runtime: "runtime-a"},
		},
		Planner:     "planner-a",
		PlanToken:   "h-3fa9c2e1",
		Command:     "planner",
		FirstNumber: 1,
	}
}

// Door is the stand-in. It keeps everything in memory under one lock, and
// is safe for use by several goroutines.
type Door struct {
	mu  sync.Mutex
	now func() time.Time

	seat      Seat
	planner   string
	planToken string
	command   string
	number    int

	// requests holds every admitted request by its id, and entries every
	// pending record by its id; order lists the pending ids as written.
	requests map[string]*request
	entries  map[string]*entry
	order    []string

	// admitted holds the times of the requests admitted in the last
	// minute. asked holds, for a verb and target, the times of the
	// requests counted towards the repeat throttle, and throttled the
	// time until which that verb and target is refused.
	admitted  []time.Time
	asked     map[string][]time.Time
	throttled map[string]time.Time
}

// request is one admitted request.
type request struct {
	req    record.Request
	sha256 string
	// target is the verb and target the request is for, the key of the
	// duplicate rule and the throttle.
	target string
	// pendingID is the id of the pending record written for the request,
	// or empty while there is none.
	pendingID string
	// ended says a request with no pending record is over: it was
	// withdrawn or voided, its plan was refused, or its epoch ended.
	ended bool
}

// entry is one pending record and where it stands. Its state is always one
// of the closed list, and changes only through move.
type entry struct {
	pending    record.Pending
	pendingRec door.Record
	// decision is the decision the door took, with its bytes and the
	// answer it was given; nil while there is none.
	decision    *record.Decision
	decisionRec door.Record
	decided     door.Decided
	state       record.State
	// reason is the refusal code that ended the record, if one did.
	reason record.Code
}

// move puts e in the state to. A move the table does not allow is a fault
// of this package, not of a caller, so it panics.
func (e *entry) move(to record.State, reason record.Code) {
	if !mayMove(e.state, to) {
		panic(fmt.Sprintf("doortest: no move from %s to %s", e.state, to))
	}
	e.state = to
	e.reason = reason
}

var _ door.Door = (*Door)(nil)

// New returns a stand-in with no request. It panics when cfg.Now is nil.
func New(cfg Config) *Door {
	if cfg.Now == nil {
		panic("doortest: Config.Now is nil")
	}
	sample := Sample(cfg.Now)
	if cfg.Seat == (Seat{}) {
		cfg.Seat = sample.Seat
	}
	if cfg.Planner == "" {
		cfg.Planner = sample.Planner
	}
	if cfg.PlanToken == "" {
		cfg.PlanToken = sample.PlanToken
	}
	if cfg.Command == "" {
		cfg.Command = sample.Command
	}
	if cfg.FirstNumber == 0 {
		cfg.FirstNumber = sample.FirstNumber
	}
	return &Door{
		now:       cfg.Now,
		seat:      cfg.Seat,
		planner:   cfg.Planner,
		planToken: cfg.PlanToken,
		command:   cfg.Command,
		number:    cfg.FirstNumber,
		requests:  make(map[string]*request),
		entries:   make(map[string]*entry),
		asked:     make(map[string][]time.Time),
		throttled: make(map[string]time.Time),
	}
}

// SetEpoch changes the seat's epoch, as a handover does. Every request and
// every record of another epoch that is not yet used becomes void, and is
// refused previous-epoch from then on.
func (d *Door) SetEpoch(epoch int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seat.Epoch = epoch
	d.sweep(d.now())
}

// SetSeatRev changes the revision of the seat record, as any write of that
// record does. Every record bound to another revision that is not yet used
// becomes void.
func (d *Door) SetSeatRev(rev int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seat.SeatRev = rev
	d.sweep(d.now())
}

// SetAssigned says whether the seat's role has an assignment. Without one,
// Admit and Plan refuse not-assigned.
func (d *Door) SetAssigned(assigned bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.seat.Unassigned = !assigned
}

// Withdraw ends a request as the lead's withdrawal does. id is a pending
// id or a request id. A record that is pending or approved becomes
// withdrawn; a request that has no pending record yet is ended without a
// state, since it has no record. Anything else is an error: a request that
// was used or has ended cannot be withdrawn.
func (d *Door) Withdraw(id string) error {
	return d.end(id, record.StateWithdrawn, "")
}

// Void ends a request as a change of something it is bound to does (a ref
// that moved, a seat that was written). id is a pending id or a request
// id, as for [Door.Withdraw]. The record becomes void, and a later use is
// refused approval-void.
func (d *Door) Void(id string) error {
	return d.end(id, record.StateVoid, record.CodeApprovalVoid)
}

// Expire ends a pending record whose time has run out, and reports an
// error when it has not: a test expires a record by moving the clock past
// the record's time first. The other verbs notice the time by themselves;
// this method is for a test that wants to say so at once and be told if
// its clock is wrong.
func (d *Door) Expire(id string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweep(d.now())
	e := d.entries[id]
	switch {
	case e == nil:
		return errors.New("doortest: no pending record has this id")
	case e.state == record.StateExpired:
		return nil
	case !mayMove(e.state, record.StateExpired):
		return fmt.Errorf("doortest: a record that is %s does not expire", e.state)
	}
	return errors.New("doortest: the record's time has not run out; move the clock first")
}

// end moves the record, or the request, named by id to a final state.
func (d *Door) end(id string, to record.State, reason record.Code) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweep(d.now())
	e := d.entries[id]
	if e == nil {
		r := d.requests[id]
		switch {
		case r == nil:
			return errors.New("doortest: no request and no pending record has this id")
		case r.pendingID == "" && r.ended:
			return errors.New("doortest: the request has ended already")
		case r.pendingID == "":
			r.ended = true
			return nil
		}
		e = d.entries[r.pendingID]
	}
	if !mayMove(e.state, to) {
		return fmt.Errorf("doortest: a record that is %s cannot become %s", e.state, to)
	}
	e.move(to, reason)
	return nil
}

// sweep applies what time and the seat have done since the last call: it
// ends what belongs to another epoch or another revision of the seat, and
// what has run out of time. Every verb calls it first, under the lock, so
// that each sees the states as they are now.
func (d *Door) sweep(now time.Time) {
	for _, r := range d.requests {
		if r.pendingID == "" && !r.ended && r.req.Epoch != d.seat.Epoch {
			r.ended = true
		}
	}
	for _, id := range d.order {
		e := d.entries[id]
		if e.state != record.StatePending && !usable(e.state) {
			continue
		}
		subject := e.pending.Binding.Subject
		switch {
		case subject.Epoch != d.seat.Epoch:
			e.move(record.StateVoid, record.CodePreviousEpoch)
		case subject.SeatRev != d.seat.SeatRev:
			e.move(record.StateVoid, record.CodeApprovalVoid)
		case !now.Before(e.expires()):
			e.move(record.StateExpired, record.CodeApprovalExpired)
		}
	}
}

// expires returns when e ends by itself: the end of the approval once
// there is one, the end of the pending record before.
func (e *entry) expires() time.Time {
	s := e.pending.Binding.PendingExpiresAt
	if e.decision != nil && usable(e.state) {
		s = e.decision.ExpiresAt
	}
	// Both strings come from records the decoders accepted, so they parse.
	t, err := record.ParseTime(s)
	if err != nil {
		panic("doortest: a decoded record holds a time that does not parse")
	}
	return t
}

// live reports whether r is not yet final: it waits for its plan, or its
// record is in a state something can still follow.
func (d *Door) live(r *request) bool {
	if r.pendingID == "" {
		return !r.ended
	}
	return !Final(d.entries[r.pendingID].state)
}

// refuse returns a refusal with a code and a detail of this package's own
// words.
func refuse(code record.Code, detail string) error {
	return &record.Error{Code: code, Detail: detail}
}

// ended returns the refusal for a record that is not in the state a verb
// needs, by the state it is in.
func ended(e *entry) error {
	switch e.state {
	case record.StateExpired:
		return refuse(record.CodeApprovalExpired, "the record's time ran out")
	case record.StateConsumed, record.StateApplied, record.StateNotApplied, record.StateUnknown:
		return refuse(record.CodeApprovalConsumed, "the approval was used; the record is "+string(e.state))
	case record.StateVoid:
		if e.reason == record.CodePreviousEpoch {
			return refuse(record.CodePreviousEpoch, "the record is void: the seat's epoch ended")
		}
		return refuse(record.CodeApprovalVoid, "the record is void")
	case record.StateApproved:
		return refuse(record.CodeApprovalVoid, "the approval was not used")
	}
	// Pending, denied or withdrawn: there is no approval to act on.
	return refuse(record.CodeApprovalVoid, "the record has no approval: it is "+string(e.state))
}
