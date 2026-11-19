package ownerjournal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ataniq/internal/approval/record"
)

// The errors of the decision entries, found with errors.Is.
var (
	// ErrDecided says that the pending id already has a decision entry. A
	// pending record is decided once; only a held entry may be followed
	// by another entry for the same id.
	ErrDecided = errors.New("ownerjournal: the pending id already has a decision")
	// ErrExpired says that the pending id's held entry was closed as
	// expired, so nothing more can be decided for it.
	ErrExpired = errors.New("ownerjournal: the pending id has expired")
	// ErrDelivered says that the decision already has its delivered
	// entry.
	ErrDelivered = errors.New("ownerjournal: the decision already has its delivery")
	// ErrConsumed says that the approval already has a use entry that
	// says the door consumed it. An approval is used once.
	ErrConsumed = errors.New("ownerjournal: the approval was already consumed")
	// ErrSettled says that the approval already has its outcome.
	ErrSettled = errors.New("ownerjournal: the approval already has its outcome")
	// ErrNotConsumed says that an outcome that follows a consumption
	// (applied, not-applied or unknown) was journaled for an approval that
	// has no use entry saying the door consumed it.
	ErrNotConsumed = errors.New("ownerjournal: the approval was not consumed")
)

// Seen is what the journal keeps beside a decision about the moment it was
// made. Where it came from (the surface and the session) is read from the
// decision record's console block.
type Seen struct {
	// LastActivity is the owner's last activity before the decision, and
	// the zero time when there was none.
	LastActivity time.Time
	// ShownSHA256 is the digest of what the card showed when he decided
	// (64 lower-case hexadecimal digits), or empty when the caller has
	// none.
	ShownSHA256 string
}

func (v Seen) check() error {
	if !v.LastActivity.IsZero() && !validTime(v.LastActivity) {
		return errors.New("ownerjournal: the last activity is not a time an entry can hold")
	}
	if v.ShownSHA256 != "" && !validDigest(v.ShownSHA256) {
		return errors.New("ownerjournal: the digest of what was shown is not 64 lower-case hexadecimal digits")
	}
	return nil
}

// validDigest reports whether s is a SHA-256 digest as the records write
// it: 64 lower-case hexadecimal digits.
func validDigest(s string) bool { return validHex(s, 64) }

// validHex reports whether s is n lower-case hexadecimal digits.
func validHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// validCode reports whether c has the form of a refusal code: 1 to 64
// lower-case letters and dashes.
func validCode(c record.Code) bool {
	if len(c) == 0 || len(c) > 64 {
		return false
	}
	for i := 0; i < len(c); i++ {
		if (c[i] < 'a' || c[i] > 'z') && c[i] != '-' {
			return false
		}
	}
	return true
}

// Decision is the handle of one decision entry: a decision of the owner
// that is in the journal and goes to the door. It is returned by
// [Store.AppendDecision] when the entry is durable, and by
// [Store.Undelivered] after a restart. Only through it can the decision be
// marked as delivered, so the delivery cannot be journaled before the
// decision is.
type Decision struct {
	s     *Store
	seq   int64
	rec   record.Decision
	at    time.Time
	bytes []byte
}

// Seq returns the sequence number of the decision entry.
func (d *Decision) Seq() int64 { return d.seq }

// ID returns the pending id the decision answers.
func (d *Decision) ID() string { return d.rec.ID }

// Project returns the project of the decision.
func (d *Decision) Project() string { return d.rec.Project }

// Approve reports whether the decision is an approve; otherwise it is a
// deny.
func (d *Decision) Approve() bool { return d.rec.Decision == record.DecisionApprove }

// BindingSHA256 returns the digest of the binding the decision names.
func (d *Decision) BindingSHA256() string { return d.rec.BindingSHA256 }

// DecidedAt returns the record's decided_at, which is also the time of
// the entry.
func (d *Decision) DecidedAt() time.Time { return d.at }

// Bytes returns the decision record exactly as it was given to
// [Store.AppendDecision]. These are the bytes to hand to the door.
func (d *Decision) Bytes() []byte { return append([]byte(nil), d.bytes...) }

// Delivery is the handle of a decision the door took. It is returned by
// [Decision.MarkDelivered], and after a restart by [Store.OpenUses] and
// [Store.UnknownOutcomes]. What the door's Use verb answered and what
// became of the approval are journaled through it.
type Delivery struct {
	d  *Decision
	at time.Time
}

// Decision returns the decision that was delivered.
func (u *Delivery) Decision() *Decision { return u.d }

// DeliveredAt returns the time of the delivered entry.
func (u *Delivery) DeliveredAt() time.Time { return u.at }

// HeldCard is one held entry that is still open: an approve the owner made
// past the day's limit. The card it belongs to is shown again as pending,
// with the mark "decided past the limit".
type HeldCard struct {
	// Seq is the sequence number of the held entry.
	Seq int64
	// ID and Project name the pending record.
	ID      string
	Project string
	// BindingSHA256 is the digest the held decision named.
	BindingSHA256 string
	// DecidedAt is when the owner decided.
	DecidedAt time.Time
	// PendingExpiresAt is when the pending record ends unanswered.
	PendingExpiresAt time.Time
	// Decision holds the bytes of the decision that was held.
	Decision []byte
}

// Refused is a decision the host refused before it became one: the owner
// was away, the surface was not the decider, the frame was malformed, or
// it named another binding. It is journaled and never handed to the door.
type Refused struct {
	// At is when it was refused.
	At time.Time
	// PendingID is the id the frame named. It is left empty when the
	// frame named none that has the form of a pending id.
	PendingID string
	// Project is the pending record's project, or empty when the id is
	// not known.
	Project string
	// Code is why: owner-away, not-decider, decision-invalid or
	// binding-mismatch.
	Code record.Code
	// Surface and Session say where the frame came from: the kind of
	// surface (1 to 32 bytes) and the id of its run (16 lower-case
	// hexadecimal digits).
	Surface string
	Session string
	// LastActivity is the owner's last activity before it, and the zero
	// time when there was none.
	LastActivity time.Time
}

// Activity is what an activity entry holds: where a decision, a held or a
// refused entry came from, and the owner's last activity before it.
type Activity struct {
	// Ref is the sequence number of the entry it belongs to, and At that
	// entry's time.
	Ref int64
	At  time.Time
	// Surface and Session say where it came from.
	Surface string
	Session string
	// LastActivity is the last activity before it, and the zero time
	// when there was none.
	LastActivity time.Time
	// ShownSHA256 is the digest of what the card showed, when the caller
	// gave one.
	ShownSHA256 string
}

// DoorDecision names one pending record that the door holds as decided by
// the owner.
type DoorDecision struct {
	// Project and ID name the pending record.
	Project string
	ID      string
	// SHA256 is the digest of the decision's bytes as the door holds
	// them ([record.Digest]). It may be empty; then only the id is
	// compared.
	SHA256 string
}

// The bodies of the kinds of this file.
type (
	decisionBody struct {
		Approve       bool   `json:"approve"`
		BindingSHA256 string `json:"binding_sha256"`
	}
	heldBody struct {
		BindingSHA256    string `json:"binding_sha256"`
		PendingExpiresAt string `json:"pending_expires_at"`
	}
	activityBody struct {
		LastActivity string `json:"last_activity"`
		Session      string `json:"session"`
		ShownSHA256  string `json:"shown_sha256"`
		Surface      string `json:"surface"`
	}
	refusedBody struct {
		Code    string `json:"code"`
		Session string `json:"session"`
		Surface string `json:"surface"`
	}
	// codeBody is the body of a delivered and of a use entry: the code
	// the door refused with, or empty when it did not refuse.
	codeBody struct {
		Code string `json:"code"`
	}
	outcomeBody struct {
		Line  string `json:"line"`
		State string `json:"state"`
	}
	expiredBody struct {
		PendingExpiresAt string `json:"pending_expires_at"`
	}
)

// count answers a query that counts.
func count(q querier, query string, args ...any) (int, error) {
	var n int
	err := q.QueryRowContext(context.Background(), query, args...).Scan(&n)
	return n, err
}

// related returns the entries of one kind that point at the entry seq, in
// order.
func related(q querier, seq int64, kind Kind) ([]Entry, error) {
	return queryEntries(q, "SELECT "+entryColumns+" FROM entries WHERE ref = ? AND kind = ? ORDER BY seq", seq, string(kind))
}

// undecided checks that a pending id can still be decided: it has no
// decision entry and was not closed as expired.
func (t *txn) undecided(project, id string) error {
	n, err := count(t.tx, "SELECT count(*) FROM entries WHERE project = ? AND pending_id = ? AND kind = 'decision'", project, id)
	if err != nil {
		return err
	}
	if n != 0 {
		return ErrDecided
	}
	n, err = count(t.tx, "SELECT count(*) FROM entries WHERE project = ? AND pending_id = ? AND kind = 'expired'", project, id)
	if err != nil {
		return err
	}
	if n != 0 {
		return ErrExpired
	}
	return nil
}

// addActivity appends the activity entry that goes with the entry e.
func (t *txn) addActivity(e Entry, surface, session string, v Seen) error {
	b, err := body(map[string]any{
		"last_activity": stamp(v.LastActivity),
		"session":       session,
		"shown_sha256":  v.ShownSHA256,
		"surface":       surface,
	})
	if err != nil {
		return err
	}
	_, err = t.add(Entry{Kind: KindActivity, At: e.At, PendingID: e.PendingID, Project: e.Project, Ref: e.Seq, Body: b})
	return err
}

// AppendDecision journals a decision of the owner that goes to the door:
// an approve inside the day's limit, or a deny. decision is the decision
// record as the door will get it; it is checked with
// [record.DecodeDecision] and kept byte for byte. With the decision, in the
// same write, goes its activity entry.
//
// When it returns without an error the decision is durable, and only then
// is it handed to the door, with [Decision.Bytes]. After the door answered,
// the caller says so with [Decision.MarkDelivered] or
// [Decision.MarkRefusedByDoor]. A decision that has neither is listed by
// [Store.Undelivered].
//
// It refuses with [ErrDecided] a second decision for a pending id, and
// with [ErrExpired] a decision for an id whose held entry was closed as
// expired.
func (s *Store) AppendDecision(decision []byte, seen Seen) (*Decision, error) {
	rec, at, err := readDecision(decision)
	if err != nil {
		return nil, err
	}
	if err := seen.check(); err != nil {
		return nil, err
	}
	b, err := body(map[string]any{"approve": rec.Decision == record.DecisionApprove, "binding_sha256": rec.BindingSHA256})
	if err != nil {
		return nil, err
	}
	kept := append([]byte(nil), decision...)
	added, err := s.write(func(t *txn) error {
		if err := t.undecided(rec.Project, rec.ID); err != nil {
			return err
		}
		e, err := t.add(Entry{Kind: KindDecision, At: at, PendingID: rec.ID, Project: rec.Project, Body: b, Record: kept})
		if err != nil {
			return err
		}
		return t.addActivity(e, rec.Console.Surface, rec.Console.Session, seen)
	})
	if err != nil {
		return nil, err
	}
	return &Decision{s: s, seq: added[0].Seq, rec: rec, at: added[0].At, bytes: kept}, nil
}

// readDecision checks the bytes of a decision record and returns the
// record and its decided_at.
func readDecision(decision []byte) (record.Decision, time.Time, error) {
	rec, err := record.DecodeDecision(decision)
	if err != nil {
		return record.Decision{}, time.Time{}, fmt.Errorf("ownerjournal: the decision record: %w", err)
	}
	at, err := record.ParseTime(rec.DecidedAt)
	if err != nil {
		return record.Decision{}, time.Time{}, fmt.Errorf("ownerjournal: the decision record: %w", err)
	}
	return rec, at, nil
}

// AppendHeld journals an approve that the owner made past the day's limit.
// decision is the decision record that would have gone to the door, and
// pending the pending record it answers, from which the journal takes the
// time the card ends. The two must name the same id, binding digest and
// project. A deny is never held and is refused here.
//
// A held entry is never handed to the door and never listed by
// [Store.Undelivered]. The card is decided again later: that writes
// another held entry or, when the day has room, a decision, and either
// closes this one. A held entry whose pending record ran out is closed by
// [Store.ExpireHeld].
func (s *Store) AppendHeld(decision, pending []byte, seen Seen) (HeldCard, error) {
	rec, at, err := readDecision(decision)
	if err != nil {
		return HeldCard{}, err
	}
	if rec.Decision != record.DecisionApprove {
		return HeldCard{}, errors.New("ownerjournal: a deny is never held")
	}
	p, err := record.DecodePending(pending)
	if err != nil {
		return HeldCard{}, fmt.Errorf("ownerjournal: the pending record: %w", err)
	}
	if p.ID != rec.ID || p.BindingSHA256 != rec.BindingSHA256 || p.Binding.Project != rec.Project {
		return HeldCard{}, errors.New("ownerjournal: the decision does not answer the pending record: the id, the binding digest or the project differs")
	}
	expires, err := record.ParseTime(p.Binding.PendingExpiresAt)
	if err != nil {
		return HeldCard{}, fmt.Errorf("ownerjournal: the pending record: %w", err)
	}
	if err := seen.check(); err != nil {
		return HeldCard{}, err
	}
	b, err := body(map[string]any{"binding_sha256": rec.BindingSHA256, "pending_expires_at": stamp(expires)})
	if err != nil {
		return HeldCard{}, err
	}
	kept := append([]byte(nil), decision...)
	added, err := s.write(func(t *txn) error {
		if err := t.undecided(rec.Project, rec.ID); err != nil {
			return err
		}
		e, err := t.add(Entry{Kind: KindHeld, At: at, PendingID: rec.ID, Project: rec.Project, Body: b, Record: kept})
		if err != nil {
			return err
		}
		return t.addActivity(e, rec.Console.Surface, rec.Console.Session, seen)
	})
	if err != nil {
		return HeldCard{}, err
	}
	return heldCard(added[0])
}

func heldCard(e Entry) (HeldCard, error) {
	var b heldBody
	if err := parseBody(e, &b); err != nil {
		return HeldCard{}, err
	}
	expires, err := unstamp(b.PendingExpiresAt)
	if err != nil {
		return HeldCard{}, err
	}
	return HeldCard{Seq: e.Seq, ID: e.PendingID, Project: e.Project, BindingSHA256: b.BindingSHA256,
		DecidedAt: e.At, PendingExpiresAt: expires, Decision: e.Record}, nil
}

// AppendRefused journals a decision the host refused, with its activity
// entry in the same write. It is never delivered and never listed.
func (s *Store) AppendRefused(r Refused) error {
	switch r.Code {
	case record.CodeOwnerAway, record.CodeNotDecider, record.CodeDecisionInvalid, record.CodeBindingMismatch:
	default:
		return errors.New("ownerjournal: a refused entry has the code owner-away, not-decider, decision-invalid or binding-mismatch")
	}
	if !validName(r.Surface, 32) {
		return errors.New("ownerjournal: the surface is not a name of at most 32 bytes")
	}
	if !validHex(r.Session, 16) {
		return errors.New("ownerjournal: the session is not 16 lower-case hexadecimal digits")
	}
	seen := Seen{LastActivity: r.LastActivity}
	if err := seen.check(); err != nil {
		return err
	}
	b, err := body(map[string]any{"code": string(r.Code), "session": r.Session, "surface": r.Surface})
	if err != nil {
		return err
	}
	_, err = s.write(func(t *txn) error {
		e, err := t.add(Entry{Kind: KindRefused, At: r.At, PendingID: r.PendingID, Project: r.Project, Body: b})
		if err != nil {
			return err
		}
		return t.addActivity(e, r.Surface, r.Session, seen)
	})
	return err
}

// deliver writes the delivered entry of a decision: code is empty when the
// door took the decision, and the door's code when it refused it.
func (d *Decision) deliver(at time.Time, code record.Code) error {
	b, err := body(map[string]any{"code": string(code)})
	if err != nil {
		return err
	}
	_, err = d.s.write(func(t *txn) error {
		n, err := count(t.tx, "SELECT count(*) FROM entries WHERE seq = ? AND kind = 'decision' AND project = ? AND pending_id = ?", d.seq, d.rec.Project, d.rec.ID)
		if err != nil {
			return err
		}
		if n != 1 {
			return errors.New("ownerjournal: the decision is not in this store")
		}
		if n, err = count(t.tx, "SELECT count(*) FROM entries WHERE ref = ? AND kind = 'delivered'", d.seq); err != nil {
			return err
		}
		if n != 0 {
			return ErrDelivered
		}
		_, err = t.add(Entry{Kind: KindDelivered, At: at, PendingID: d.rec.ID, Project: d.rec.Project, Ref: d.seq, Body: b})
		return err
	})
	return err
}

// MarkDelivered journals that the door took the decision. From then on the
// decision is not listed by [Store.Undelivered]. For an approve, the
// approval is now open: it is listed by [Store.OpenUses] until it has its
// outcome. It refuses with [ErrDelivered] a decision that has its delivered
// entry already.
//
// The door's Decide answers the same for the same id and digest, so a
// caller that crashed between the door's answer and this call hands the
// decision over again after the restart and then calls this.
func (d *Decision) MarkDelivered(at time.Time) (*Delivery, error) {
	if err := d.deliver(at, ""); err != nil {
		return nil, err
	}
	return &Delivery{d: d, at: time.Unix(0, at.UnixNano()).UTC()}, nil
}

// MarkRefusedByDoor journals that the door answered the decision with a
// refusal, for example because the pending record had ended meanwhile.
// The decision is then closed: it is not handed over again after a
// restart, and it has no use and no outcome. A failure to reach the door
// is not a refusal; such a decision stays undelivered.
func (d *Decision) MarkRefusedByDoor(at time.Time, code record.Code) error {
	if !validCode(code) {
		return errors.New("ownerjournal: the door's code is not 1 to 64 lower-case letters and dashes")
	}
	return d.deliver(at, code)
}

// usable checks, inside a write, that the approval may still get a use or
// an outcome entry, and returns its outcome entries so far.
func (u *Delivery) usable(t *txn) ([]Entry, error) {
	if !u.d.Approve() {
		return nil, errors.New("ownerjournal: a deny has no use and no outcome")
	}
	delivered, err := related(t.tx, u.d.seq, KindDelivered)
	if err != nil {
		return nil, err
	}
	if len(delivered) != 1 {
		return nil, errors.New("ownerjournal: the decision has no delivered entry in this store")
	}
	var b codeBody
	if err := parseBody(delivered[0], &b); err != nil {
		return nil, err
	}
	if b.Code != "" {
		return nil, errors.New("ownerjournal: the door refused the decision; it has no use and no outcome")
	}
	return related(t.tx, u.d.seq, KindOutcome)
}

// consumed reports, inside a write, whether the approval has a use entry
// with an empty code, which says the door consumed it.
func (u *Delivery) consumed(t *txn) (bool, error) {
	uses, err := related(t.tx, u.d.seq, KindUse)
	if err != nil {
		return false, err
	}
	for _, e := range uses {
		var b codeBody
		if err := parseBody(e, &b); err != nil {
			return false, err
		}
		if b.Code == "" {
			return true, nil
		}
	}
	return false, nil
}

// AppendUse journals one answer of the door's Use verb for the approval:
// code is empty when the door consumed it, and the door's code when it
// refused the use. This is not a card use; the card use that counts
// against the day's limit is the decision entry itself.
//
// An approval is consumed once: a second entry with an empty code is
// refused with [ErrConsumed]. No use is journaled after the outcome
// ([ErrSettled]).
func (u *Delivery) AppendUse(at time.Time, code record.Code) error {
	if code != "" && !validCode(code) {
		return errors.New("ownerjournal: the door's code is not 1 to 64 lower-case letters and dashes")
	}
	b, err := body(map[string]any{"code": string(code)})
	if err != nil {
		return err
	}
	_, err = u.d.s.write(func(t *txn) error {
		outcomes, err := u.usable(t)
		if err != nil {
			return err
		}
		if len(outcomes) != 0 {
			return ErrSettled
		}
		if code == "" {
			if done, err := u.consumed(t); err != nil {
				return err
			} else if done {
				return ErrConsumed
			}
		}
		_, err = t.add(Entry{Kind: KindUse, At: at, PendingID: u.d.rec.ID, Project: u.d.rec.Project, Ref: u.d.seq, Body: b})
		return err
	})
	return err
}

// AppendOutcome journals what became of the approval, and closes it: it
// is no longer listed by [Store.OpenUses]. state is where the pending
// record ended: applied, not-applied or unknown after it was consumed, or
// expired, void or withdrawn when it ended unused. line is a host-written
// line of at most 500 bytes and may be empty.
//
// Applied, not-applied and unknown follow a consumption at the door, so
// they are refused with [ErrNotConsumed] until [Delivery.AppendUse] has
// journaled a use whose code is empty. Expired, void and withdrawn are
// for an approval that ended unused and need no use.
//
// An approval has one outcome ([ErrSettled] for a second), with one
// exception: unknown waits for the owner, so after it one more outcome,
// applied or not-applied, may follow. Approvals whose last outcome is
// unknown are listed by [Store.UnknownOutcomes].
func (u *Delivery) AppendOutcome(at time.Time, state record.State, line string) error {
	switch state {
	case record.StateApplied, record.StateNotApplied, record.StateUnknown,
		record.StateExpired, record.StateVoid, record.StateWithdrawn:
	default:
		return errors.New("ownerjournal: an outcome is applied, not-applied, unknown, expired, void or withdrawn")
	}
	if !validLine(line) {
		return errors.New("ownerjournal: the outcome's line is not at most 500 bytes without control characters")
	}
	unused := state == record.StateExpired || state == record.StateVoid || state == record.StateWithdrawn
	b, err := body(map[string]any{"line": line, "state": string(state)})
	if err != nil {
		return err
	}
	_, err = u.d.s.write(func(t *txn) error {
		outcomes, err := u.usable(t)
		if err != nil {
			return err
		}
		if !unused {
			consumed, err := u.consumed(t)
			if err != nil {
				return err
			}
			if !consumed {
				return ErrNotConsumed
			}
		}
		if n := len(outcomes); n != 0 {
			var last outcomeBody
			if err := parseBody(outcomes[n-1], &last); err != nil {
				return err
			}
			resolves := state == record.StateApplied || state == record.StateNotApplied
			if n != 1 || last.State != string(record.StateUnknown) || !resolves {
				return ErrSettled
			}
		}
		_, err = t.add(Entry{Kind: KindOutcome, At: at, PendingID: u.d.rec.ID, Project: u.d.rec.Project, Ref: u.d.seq, Body: b})
		return err
	})
	return err
}

// decisionOf makes the handle of a decision entry read from the store.
func (s *Store) decisionOf(e Entry) (*Decision, error) {
	rec, at, err := readDecision(e.Record)
	if err != nil || rec.ID != e.PendingID || rec.Project != e.Project || !at.Equal(e.At) {
		return nil, fmt.Errorf("%w: the record of entry %d cannot be read", ErrDamaged, e.Seq)
	}
	return &Decision{s: s, seq: e.Seq, rec: rec, at: e.At, bytes: e.Record}, nil
}

// Undelivered returns the decisions to hand to the door at a restart:
// every decision entry that has no delivered entry, in the order they were
// journaled, each once. A held and a refused entry are never among them. A
// decision leaves the list when [Decision.MarkDelivered] or
// [Decision.MarkRefusedByDoor] returned.
func (s *Store) Undelivered() ([]*Decision, error) {
	var out []*Decision
	err := s.read(func(q querier) error {
		entries, err := queryEntries(q, `SELECT `+columnsOf("d")+` FROM entries d WHERE d.kind = 'decision'
 AND NOT EXISTS (SELECT 1 FROM entries x WHERE x.ref = d.seq AND x.kind = 'delivered') ORDER BY d.seq`)
		if err != nil {
			return err
		}
		for _, e := range entries {
			d, err := s.decisionOf(e)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return nil
	})
	return out, err
}

// deliveryOf makes the handle of a delivered approval, and reports false
// for a deny and for a decision the door refused.
func (s *Store) deliveryOf(q querier, e Entry) (*Delivery, bool, error) {
	d, err := s.decisionOf(e)
	if err != nil {
		return nil, false, err
	}
	delivered, err := related(q, e.Seq, KindDelivered)
	if err != nil || len(delivered) != 1 {
		return nil, false, err
	}
	var b codeBody
	if err := parseBody(delivered[0], &b); err != nil {
		return nil, false, err
	}
	return &Delivery{d: d, at: delivered[0].At}, d.Approve() && b.Code == "", nil
}

// OpenUses returns the approvals that were delivered and have no outcome,
// in the order they were decided. At a restart the caller settles each
// from what the door and the mirror show: one the door never consumed is
// simply still open; one that was consumed gets its outcome with
// [Delivery.AppendOutcome]. A deny and a decision the door refused are
// never among them.
func (s *Store) OpenUses() ([]*Delivery, error) {
	var out []*Delivery
	err := s.read(func(q querier) error {
		entries, err := queryEntries(q, `SELECT `+columnsOf("d")+` FROM entries d WHERE d.kind = 'decision'
 AND EXISTS (SELECT 1 FROM entries x WHERE x.ref = d.seq AND x.kind = 'delivered')
 AND NOT EXISTS (SELECT 1 FROM entries o WHERE o.ref = d.seq AND o.kind = 'outcome') ORDER BY d.seq`)
		if err != nil {
			return err
		}
		for _, e := range entries {
			u, open, err := s.deliveryOf(q, e)
			if err != nil {
				return err
			}
			if open {
				out = append(out, u)
			}
		}
		return nil
	})
	return out, err
}

// UnknownOutcomes returns the approvals whose last outcome is unknown, in
// the order they were decided. Each waits for the owner; what he finds is
// journaled with [Delivery.AppendOutcome].
func (s *Store) UnknownOutcomes() ([]*Delivery, error) {
	var out []*Delivery
	err := s.read(func(q querier) error {
		entries, err := queryEntries(q, `SELECT `+columnsOf("d")+` FROM entries d JOIN entries o ON o.ref = d.seq AND o.kind = 'outcome'
 WHERE d.kind = 'decision' AND o.seq = (SELECT max(seq) FROM entries WHERE ref = d.seq AND kind = 'outcome') ORDER BY d.seq`)
		if err != nil {
			return err
		}
		for _, e := range entries {
			outcomes, err := related(q, e.Seq, KindOutcome)
			if err != nil {
				return err
			}
			var last outcomeBody
			if err := parseBody(outcomes[len(outcomes)-1], &last); err != nil {
				return err
			}
			if last.State != string(record.StateUnknown) {
				continue
			}
			u, _, err := s.deliveryOf(q, e)
			if err != nil {
				return err
			}
			out = append(out, u)
		}
		return nil
	})
	return out, err
}

// openHeld is the query for held entries that are still open: the last
// held or decision entry of their pending id, and not closed as expired.
const openHeld = ` FROM entries h WHERE h.kind = 'held'
 AND NOT EXISTS (SELECT 1 FROM entries l WHERE l.project = h.project AND l.pending_id = h.pending_id AND l.kind IN ('held', 'decision') AND l.seq > h.seq)
 AND NOT EXISTS (SELECT 1 FROM entries e WHERE e.ref = h.seq AND e.kind = 'expired')`

func heldCards(q querier, where string, args ...any) ([]HeldCard, error) {
	entries, err := queryEntries(q, "SELECT "+columnsOf("h")+openHeld+where+" ORDER BY h.seq", args...)
	if err != nil {
		return nil, err
	}
	var out []HeldCard
	for _, e := range entries {
		c, err := heldCard(e)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// HeldCards returns the held cards of a project: for each pending id whose
// last decision was held, that held entry, unless it was closed as
// expired. The journal does not know whether the pending record ended at
// the door in another way (withdrawn, void); the caller joins this list
// with the door's pending records.
func (s *Store) HeldCards(project string) ([]HeldCard, error) {
	var out []HeldCard
	err := s.read(func(q querier) (err error) {
		out, err = heldCards(q, " AND h.project = ?", project)
		return err
	})
	return out, err
}

// ExpireHeld closes, with an expired entry each, every open held entry of
// every project whose pending record's pending_expires_at is before now,
// and returns the cards it closed. The service calls it with the time.
// All of them are closed in one write. After it, nothing more can be
// decided for those ids ([ErrExpired]).
func (s *Store) ExpireHeld(now time.Time) ([]HeldCard, error) {
	if !validTime(now) {
		return nil, errNoTime
	}
	var closed []HeldCard
	_, err := s.write(func(t *txn) error {
		closed = nil
		open, err := heldCards(t.tx, "")
		if err != nil {
			return err
		}
		for _, c := range open {
			if !now.After(c.PendingExpiresAt) {
				continue
			}
			b, err := body(map[string]any{"pending_expires_at": stamp(c.PendingExpiresAt)})
			if err != nil {
				return err
			}
			if _, err := t.add(Entry{Kind: KindExpired, At: now, PendingID: c.ID, Project: c.Project, Ref: c.Seq, Body: b}); err != nil {
				return err
			}
			closed = append(closed, c)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return closed, nil
}

// ActivityOf returns the activity entry that goes with the decision, held
// or refused entry seq: where it came from and the owner's last activity
// before it.
func (s *Store) ActivityOf(seq int64) (Activity, error) {
	var out Activity
	err := s.read(func(q querier) error {
		entries, err := related(q, seq, KindActivity)
		if err != nil {
			return err
		}
		if len(entries) != 1 {
			return fmt.Errorf("ownerjournal: entry %d has no activity entry", seq)
		}
		var b activityBody
		if err := parseBody(entries[0], &b); err != nil {
			return err
		}
		last, err := unstamp(b.LastActivity)
		if err != nil {
			return err
		}
		out = Activity{Ref: seq, At: entries[0].At, Surface: b.Surface, Session: b.Session, LastActivity: last, ShownSHA256: b.ShownSHA256}
		return nil
	})
	return out, err
}

// Unjournaled takes the pending records the door holds as decided by the
// owner and returns those the journal lacks, in the order given: the ones
// with no decision entry, and, where a digest is given, the ones whose
// journaled decision has other bytes. A held entry does not count: a held
// decision was never handed to the door.
//
// Every decision reaches the door through the journal, so one the journal
// lacks did not come from the owner's console. The caller refuses that
// approval and raises an alert, and goes on with the others. The caller
// passes only records decided on a card; one approved by a rule or a
// grant has no decision here.
func (s *Store) Unjournaled(decided []DoorDecision) ([]DoorDecision, error) {
	var out []DoorDecision
	err := s.read(func(q querier) error {
		for _, d := range decided {
			entries, err := queryEntries(q, "SELECT "+entryColumns+" FROM entries WHERE project = ? AND pending_id = ? AND kind = 'decision'", d.Project, d.ID)
			if err != nil {
				return err
			}
			if len(entries) == 1 && (d.SHA256 == "" || d.SHA256 == record.Digest(entries[0].Record)) {
				continue
			}
			out = append(out, d)
		}
		return nil
	})
	return out, err
}
