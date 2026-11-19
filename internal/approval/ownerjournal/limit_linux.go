package ownerjournal

import (
	"errors"
	"time"

	"ataniq/internal/approval/record"
)

// ErrLimitMoved says that a change of the limit does not start from the
// limit the journal has for the project, so another change came between.
var ErrLimitMoved = errors.New("ownerjournal: the limit is not the one the change starts from")

// ViewDays is how many calendar days before today [Store.Days] returns:
// the days the limit's screen shows and makes its suggestion from.
const ViewDays = 14

// dateLayout is the form of Day.Date.
const dateLayout = "2006-01-02"

// LimitChange is one change of a project's daily card limit.
type LimitChange struct {
	// Seq is the sequence number of the entry. It is set when a change is
	// read and ignored when one is appended.
	Seq int64
	// Project is the project whose limit it is.
	Project string
	// At is when the limit changed.
	At time.Time
	// From is the limit before the change and To the limit after it. The
	// first change of a project starts from the limit that holds when
	// none was ever set, which the caller knows and the journal does not.
	From int
	To   int
	// Typed says that the owner typed the number.
	Typed bool
}

// Limit is the present limit of a project as the journal has it.
type Limit struct {
	// Set is false when the project's limit was never changed; the limit
	// is then the one that holds from the start, and the other fields
	// are zero.
	Set bool
	// Limit is the number, and ChangedAt when it was set.
	Limit     int
	ChangedAt time.Time
	// LastRaise is the time of the last change that raised the limit,
	// and the zero time when there was none.
	LastRaise time.Time
}

// Day is one calendar day of one project.
type Day struct {
	// Date is the day as YYYY-MM-DD, in the location of the time
	// [Store.Days] was given.
	Date string
	// Uses counts the card uses of the day: the approves journaled as a
	// decision. A held approve and a deny are not uses.
	Uses int
	// ReachedLimit says that an alert entry of the day marks the limit as
	// reached, and Alert that the day had an alert of another kind.
	ReachedLimit bool
	Alert        bool
}

// Alert marks a moment a project had an alert, or reached its limit.
type Alert struct {
	// Seq is the sequence number of the entry. It is set when an alert is
	// read and ignored when one is appended.
	Seq int64
	// Project is the project, and At the time.
	Project string
	At      time.Time
	// PendingID names the pending record the alert is about, when there
	// is one.
	PendingID string
	// LimitReached says that this marks the day as one that reached the
	// limit; otherwise it is an alert of another kind, such as a decision
	// the door holds and the journal lacks.
	LimitReached bool
	// Note is a host-written line of at most 500 bytes. It may be empty.
	Note string
}

// Count says, for one request, whether it would have met the terms of the
// first standing approval.
type Count struct {
	// Seq is the sequence number of the entry. It is set when a count is
	// read and ignored when one is appended.
	Seq int64
	// Project is the project, and At when the request was counted.
	Project string
	At      time.Time
	// RequestID is the lead's label for the request. It says which
	// request was meant and is not what requests are told apart by.
	RequestID string
	// PendingID is the host's id of the pending record made for the
	// request, or empty when none was made.
	PendingID string
	// Met says whether the terms would have been met, and Reason is the
	// host-written line that says why not, or what was left out. It is
	// at most 500 bytes and may be empty.
	Met    bool
	Reason string
}

// The bodies of the kinds of this file.
type (
	limitBody struct {
		From  int  `json:"from"`
		To    int  `json:"to"`
		Typed bool `json:"typed"`
	}
	alertBody struct {
		LimitReached bool   `json:"limit_reached"`
		Note         string `json:"note"`
	}
	countBody struct {
		Met       bool   `json:"met"`
		Reason    string `json:"reason"`
		RequestID string `json:"request_id"`
	}
)

var errNoProject = errors.New("ownerjournal: the project is not a name of at most 256 bytes without control characters")

// limitChanges returns the changes of a project's limit in order.
func limitChanges(q querier, project string) ([]LimitChange, error) {
	entries, err := queryEntries(q, "SELECT "+entryColumns+" FROM entries WHERE kind = 'limit' AND project = ? ORDER BY seq", project)
	if err != nil {
		return nil, err
	}
	var out []LimitChange
	for _, e := range entries {
		var b limitBody
		if err := parseBody(e, &b); err != nil {
			return nil, err
		}
		out = append(out, LimitChange{Seq: e.Seq, Project: e.Project, At: e.At, From: b.From, To: b.To, Typed: b.Typed})
	}
	return out, nil
}

// AppendLimit journals a change of a project's limit. Whether the change
// is allowed (how high, how often, typed or not) is decided by the limit's
// own rules before this is called; the journal keeps what was decided. It
// checks only that both numbers are at least 1 and that the change starts
// from the limit the journal has: when the project has an earlier change,
// From must be that change's To, else the append is refused with
// [ErrLimitMoved] and the caller reads the limit again.
func (s *Store) AppendLimit(c LimitChange) error {
	if !validName(c.Project, 256) {
		return errNoProject
	}
	if c.From < 1 || c.To < 1 {
		return errors.New("ownerjournal: a limit is at least 1")
	}
	b, err := body(map[string]any{"from": c.From, "to": c.To, "typed": c.Typed})
	if err != nil {
		return err
	}
	_, err = s.write(func(t *txn) error {
		changes, err := limitChanges(t.tx, c.Project)
		if err != nil {
			return err
		}
		if n := len(changes); n != 0 && changes[n-1].To != c.From {
			return ErrLimitMoved
		}
		_, err = t.add(Entry{Kind: KindLimit, At: c.At, Project: c.Project, Body: b})
		return err
	})
	return err
}

// LimitChanges returns every change of a project's limit, in the order
// they were made. From them a caller can say which limit held on a day.
func (s *Store) LimitChanges(project string) ([]LimitChange, error) {
	var out []LimitChange
	err := s.read(func(q querier) (err error) {
		out, err = limitChanges(q, project)
		return err
	})
	return out, err
}

// Limit returns the present limit of a project and the time of its last
// raise.
func (s *Store) Limit(project string) (Limit, error) {
	changes, err := s.LimitChanges(project)
	if err != nil || len(changes) == 0 {
		return Limit{}, err
	}
	last := changes[len(changes)-1]
	out := Limit{Set: true, Limit: last.To, ChangedAt: last.At}
	for _, c := range changes {
		if c.To > c.From {
			out.LastRaise = c.At
		}
	}
	return out, nil
}

// AppendAlert journals an alert of a project, or that the project reached
// its limit at that moment.
func (s *Store) AppendAlert(a Alert) error {
	if !validName(a.Project, 256) {
		return errNoProject
	}
	if !validLine(a.Note) {
		return errors.New("ownerjournal: the alert's note is not at most 500 bytes without control characters")
	}
	b, err := body(map[string]any{"limit_reached": a.LimitReached, "note": a.Note})
	if err != nil {
		return err
	}
	_, err = s.write(func(t *txn) error {
		_, err := t.add(Entry{Kind: KindAlert, At: a.At, PendingID: a.PendingID, Project: a.Project, Body: b})
		return err
	})
	return err
}

// alertsSince returns the alerts of a project at or after from, in order.
func alertsSince(q querier, project string, from time.Time) ([]Alert, error) {
	entries, err := queryEntries(q, "SELECT "+entryColumns+" FROM entries WHERE kind = 'alert' AND project = ? AND at >= ? ORDER BY seq", project, from.UnixNano())
	if err != nil {
		return nil, err
	}
	var out []Alert
	for _, e := range entries {
		var b alertBody
		if err := parseBody(e, &b); err != nil {
			return nil, err
		}
		out = append(out, Alert{Seq: e.Seq, Project: e.Project, At: e.At, PendingID: e.PendingID, LimitReached: b.LimitReached, Note: b.Note})
	}
	return out, nil
}

// cardUses returns the times of a project's card uses at or after from.
func cardUses(q querier, project string, from time.Time) ([]time.Time, error) {
	entries, err := queryEntries(q, "SELECT "+entryColumns+" FROM entries WHERE kind = 'decision' AND project = ? AND at >= ? ORDER BY at, seq", project, from.UnixNano())
	if err != nil {
		return nil, err
	}
	var out []time.Time
	for _, e := range entries {
		var b decisionBody
		if err := parseBody(e, &b); err != nil {
			return nil, err
		}
		if b.Approve {
			out = append(out, e.At)
		}
	}
	return out, nil
}

// CardUses returns the decision times of a project's card uses at or
// after from, oldest first. A card use is an approve journaled as a
// decision, whatever became of it later; a held approve and a deny are
// not uses. The times are in UTC; the caller places them on days in its
// clock's location. With the start of today it gives what the limit
// judges the next approve against.
func (s *Store) CardUses(project string, from time.Time) ([]time.Time, error) {
	if !validTime(from) {
		return nil, errNoTime
	}
	var out []time.Time
	err := s.read(func(q querier) (err error) {
		out, err = cardUses(q, project, from)
		return err
	})
	return out, err
}

// Days returns the days of a project for the limit's view: the [ViewDays]
// calendar days before today and then today, oldest first, each with its
// card uses and its marks. Today is the calendar day of now, and every
// time is placed on its day, in the location of now: a decision at 23:59
// and one at 00:00 there are on two days, whatever their UTC dates are.
func (s *Store) Days(project string, now time.Time) ([]Day, error) {
	if !validTime(now) {
		return nil, errNoTime
	}
	loc := now.Location()
	y, m, d := now.Date()
	out := make([]Day, ViewDays+1)
	place := make(map[string]*Day, len(out))
	for i := range out {
		// The dates are counted on the calendar alone, so that a day that
		// is an hour shorter or longer than the others still gets its own.
		out[i].Date = time.Date(y, m, d-ViewDays+i, 0, 0, 0, 0, time.UTC).Format(dateLayout)
		place[out[i].Date] = &out[i]
	}
	from := time.Date(y, m, d-ViewDays, 0, 0, 0, 0, loc)
	err := s.read(func(q querier) error {
		uses, err := cardUses(q, project, from)
		if err != nil {
			return err
		}
		for _, at := range uses {
			if day := place[at.In(loc).Format(dateLayout)]; day != nil {
				day.Uses++
			}
		}
		alerts, err := alertsSince(q, project, from)
		if err != nil {
			return err
		}
		for _, a := range alerts {
			day := place[a.At.In(loc).Format(dateLayout)]
			switch {
			case day == nil:
			case a.LimitReached:
				day.ReachedLimit = true
			default:
				day.Alert = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AppendCount journals, for one request, whether it would have met the
// terms of the first standing approval. The request id must have the form
// of one ([record.ValidRequestID]).
func (s *Store) AppendCount(c Count) error {
	if !validName(c.Project, 256) {
		return errNoProject
	}
	if !record.ValidRequestID(c.RequestID) {
		return errors.New("ownerjournal: the request id is not rq- and 1 to 48 lower-case letters, digits and dashes")
	}
	if !validLine(c.Reason) {
		return errors.New("ownerjournal: the count's reason is not at most 500 bytes without control characters")
	}
	b, err := body(map[string]any{"met": c.Met, "reason": c.Reason, "request_id": c.RequestID})
	if err != nil {
		return err
	}
	_, err = s.write(func(t *txn) error {
		_, err := t.add(Entry{Kind: KindCount, At: c.At, PendingID: c.PendingID, Project: c.Project, Body: b})
		return err
	})
	return err
}

// Counts returns the count of each request of a project, in the order the
// requests were counted. Requests are told apart by the host's pending id:
// when a pending id was counted more than once (a restart may count a
// request again), only its last count is returned, at the place of the
// last. A count with no pending id is a request of its own, since the
// request id is the lead's to choose and may repeat.
func (s *Store) Counts(project string) ([]Count, error) {
	var out []Count
	err := s.read(func(q querier) error {
		entries, err := queryEntries(q, "SELECT "+entryColumns+" FROM entries WHERE kind = 'count' AND project = ? ORDER BY seq", project)
		if err != nil {
			return err
		}
		last := make(map[string]int64)
		for _, e := range entries {
			if e.PendingID != "" {
				last[e.PendingID] = e.Seq
			}
		}
		for _, e := range entries {
			if e.PendingID != "" && last[e.PendingID] != e.Seq {
				continue
			}
			var b countBody
			if err := parseBody(e, &b); err != nil {
				return err
			}
			out = append(out, Count{Seq: e.Seq, Project: e.Project, At: e.At, RequestID: b.RequestID, PendingID: e.PendingID, Met: b.Met, Reason: b.Reason})
		}
		return nil
	})
	return out, err
}

// CountTotals returns how many requests of a project were counted and how
// many of them would have met the terms. It counts what [Store.Counts]
// returns.
func (s *Store) CountTotals(project string) (requests, met int, err error) {
	counts, err := s.Counts(project)
	if err != nil {
		return 0, 0, err
	}
	for _, c := range counts {
		if c.Met {
			met++
		}
	}
	return len(counts), met, nil
}
