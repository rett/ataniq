package command

import (
	"errors"
	"fmt"
	"time"
)

// Bounds on text that comes from outside and is written to the journal.
const (
	maxFieldBytes    = 96   // an id, sender or target in a refusal record
	maxDetailBytes   = 1024 // a detail or a fence reading
	maxEvidenceBytes = 512  // a piece of evidence
	maxOperatorBytes = 128  // an operator name
)

// command is the machine's memory of one command. A command that has ended
// keeps its fields and drops its text.
type command struct {
	id, kind string
	sender   Sender
	target   Target
	text     string

	state  State
	reason Reason // the last reason, kept across transitions that give none
	detail string

	evidenceKind EvidenceKind
	evidence     string
	operator     string

	admitted time.Time
	updated  time.Time
	enterAt  time.Time
	hasEnter bool

	of          string
	replacement string
}

// Machine is the durable command state machine of one daemon: the queues of
// all targets, the rate windows of all senders and the journal they are
// written to. It is not safe for concurrent use.
//
// Every method that changes a command first appends a Record to the journal
// and applies the change only when the append succeeded, so a failed append
// leaves the state as it was. Two methods write more than one record and say
// so: Submit (accepted, then queued) and Resolve with a resend.
type Machine struct {
	limits  Limits
	journal Journal
	seq     uint64

	cmds    map[string]*command
	queues  map[string][]*command // by rendered target, first in first out
	rates   map[string][]time.Time
	blocked map[string]bool // a blocked spell has been announced for the target
}

// New returns an empty Machine that writes to journal. It returns an error
// when the limits do not validate (the queue time limit has no default and
// must be set) or the journal is nil.
func New(limits Limits, journal Journal) (*Machine, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	if journal == nil {
		return nil, errors.New("command: a journal is required")
	}
	return &Machine{
		limits:  limits,
		journal: journal,
		cmds:    map[string]*command{},
		queues:  map[string][]*command{},
		rates:   map[string][]time.Time{},
		blocked: map[string]bool{},
	}, nil
}

// Limits returns the limits the machine was made with.
func (m *Machine) Limits() Limits { return m.limits }

// commit stamps the record with the next sequence number, checks that it
// follows the state machine, appends it and, only when the append succeeded,
// applies it.
func (m *Machine) commit(r Record) error {
	r.Seq = m.seq + 1
	if err := m.check(r); err != nil {
		return fmt.Errorf("%w: %v", errInternal, err)
	}
	if err := m.journal.Append(r); err != nil {
		return &JournalError{Seq: r.Seq, Err: err}
	}
	m.seq = r.Seq
	m.apply(r)
	return nil
}

// Message is a message to admit.
type Message struct {
	// ID is the command id, chosen by the caller and unique.
	ID string
	// From is the authenticated sender.
	From Sender
	// To is the target.
	To Target
	// Kind is the message kind, for the header.
	Kind string
	// Body is the text after the header.
	Body string
}

// Admission is the result of an admitted message.
type Admission struct {
	ID     string
	Target Target
	// Header and Text are what will be delivered.
	Header string
	Text   string
}

// Submit admits a message, or refuses it. A refusal is a *Refusal error with
// a reason code; it is journaled as a record with no command, and nothing is
// queued. The checks, in order, are: the sender, target and project names, the
// edge policy, the id and kind, a duplicate id, the whole text size, the body
// rules of CheckBody, the target's queue (messages, then bytes with this
// text) and the sender's rate (more than MaxPerWindow admitted messages in the
// RateWindow ending at now). Refused messages are not counted against the
// rate.
//
// An admitted message is journaled as accepted (with its text) and then as
// queued, in the same call. If the first append fails nothing changed. If the
// second fails the command stays accepted, at the end of its queue, and
// Submit returns the Admission together with the *JournalError; Tick (or
// Restore) completes it, and until then it holds its place in the queue and
// the queue cannot dispatch past it.
func (m *Machine) Submit(msg Message, now time.Time) (Admission, error) {
	if ref := m.admit(msg, now); ref != nil {
		rec := Record{
			At:     now,
			ID:     safeField(msg.ID, maxFieldBytes),
			Target: ref.Target,
			Sender: ref.Sender,
			Kind:   safeField(msg.Kind, maxFieldBytes),
			To:     StateRefused,
			Reason: ref.Reason,
			Detail: ref.Detail,
		}
		if err := m.commit(rec); err != nil {
			var je *JournalError
			if errors.As(err, &je) {
				ref.JournalErr = je
			}
		}
		return Admission{}, ref
	}

	header := HeaderLine(msg.ID, msg.From, msg.Kind)
	text := header + "\n" + msg.Body
	rec := Record{
		At:     now,
		ID:     msg.ID,
		Target: msg.To.String(),
		Sender: msg.From.String(),
		Kind:   msg.Kind,
		To:     StateAccepted,
		Text:   text,
	}
	if err := m.commit(rec); err != nil {
		return Admission{}, err
	}
	adm := Admission{ID: msg.ID, Target: msg.To, Header: header, Text: text}
	if err := m.commit(queuedRecord(m.cmds[msg.ID], now)); err != nil {
		return adm, err
	}
	return adm, nil
}

func queuedRecord(c *command, now time.Time) Record {
	r := base(c, now)
	r.From, r.To = StateAccepted, StateQueued
	return r
}

// base fills the identity fields of a record for an existing command.
func base(c *command, now time.Time) Record {
	return Record{At: now, ID: c.id, Target: c.target.String(), Sender: c.sender.String(), Kind: c.kind}
}

// admit applies the admission rules. It reads the machine and changes
// nothing.
func (m *Machine) admit(msg Message, now time.Time) *Refusal {
	ref := func(reason Reason, format string, args ...any) *Refusal {
		return &Refusal{
			Reason:    reason,
			Detail:    safeField(fmt.Sprintf(format, args...), maxDetailBytes),
			CommandID: safeField(msg.ID, maxFieldBytes),
			Sender:    safeField(msg.From.String(), maxFieldBytes),
			Target:    safeField(msg.To.String(), maxFieldBytes),
		}
	}
	if msg.From.kind == principalNone || msg.To.kind == principalNone {
		return ref(ReasonBadSender, "sender or target is not set")
	}
	if !msg.From.Valid() {
		return ref(ReasonBadProject, "sender project %q does not match the name grammar", safeField(msg.From.project, 70))
	}
	if !msg.To.Valid() {
		return ref(ReasonBadProject, "target project %q does not match the name grammar", safeField(msg.To.project, 70))
	}
	if !EdgeAllowed(msg.From, msg.To) {
		return ref(ReasonEdge, "%s may not send to %s", msg.From, msg.To)
	}
	if !ValidName(msg.ID) {
		return ref(ReasonBadID, "id %q does not match the name grammar", safeField(msg.ID, 70))
	}
	if !ValidName(msg.Kind) {
		return ref(ReasonBadKind, "kind %q does not match the name grammar", safeField(msg.Kind, 70))
	}
	if _, dup := m.cmds[msg.ID]; dup {
		return ref(ReasonDuplicateID, "a command with id %q exists", msg.ID)
	}
	size := len(HeaderLine(msg.ID, msg.From, msg.Kind)) + 1 + len(msg.Body)
	if size > m.limits.MaxTextBytes {
		return ref(ReasonTooLong, "text is %d bytes, the limit is %d", size, m.limits.MaxTextBytes)
	}
	if reason, detail := CheckBody(msg.Body); reason != "" {
		return ref(reason, "%s", detail)
	}
	q := m.queues[msg.To.String()]
	if len(q) >= m.limits.MaxQueueMessages {
		return ref(ReasonQueueFull, "the queue of %s holds %d commands, the limit is %d", msg.To, len(q), m.limits.MaxQueueMessages)
	}
	bytes := size
	for _, c := range q {
		bytes += len(c.text)
	}
	if bytes > m.limits.MaxQueueBytes {
		return ref(ReasonQueueBytes, "the queue of %s would hold %d bytes, the limit is %d", msg.To, bytes, m.limits.MaxQueueBytes)
	}
	if n := countSince(m.rates[msg.From.String()], now.Add(-m.limits.RateWindow)); n >= m.limits.MaxPerWindow {
		return ref(ReasonRate, "%s sent %d messages in the last %s, the limit is %d", msg.From, n, m.limits.RateWindow, m.limits.MaxPerWindow)
	}
	return nil
}

// countSince counts the times strictly after the cut.
func countSince(times []time.Time, cut time.Time) int {
	n := 0
	for _, t := range times {
		if t.After(cut) {
			n++
		}
	}
	return n
}

// frozenBy returns the uncertain command that freezes a queue, or nil.
func frozenBy(q []*command) *command {
	for _, c := range q {
		if c.state == StateUncertain {
			return c
		}
	}
	return nil
}
