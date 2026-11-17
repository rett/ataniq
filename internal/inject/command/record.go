package command

import (
	"errors"
	"fmt"
	"time"
)

// State is where a command is in its life.
type State string

// The states of a command. A state of "" in a Record's From field means the
// record creates the command, or records a refusal at admission.
const (
	// StateAccepted: the message passed admission and its text is
	// journaled; it is about to be queued.
	StateAccepted State = "accepted"
	// StateQueued: waiting in the target's first-in first-out queue.
	StateQueued State = "queued"
	// StateDispatching: the intent to send is journaled. Once Enter is
	// written the command stays dispatching, waiting for confirmation.
	StateDispatching State = "dispatching"
	// StateDelivered: the message reached the agent (by evidence, or by an
	// operator's decision).
	StateDelivered State = "delivered"
	// StateRefused: the message will not be sent. A refusal at admission
	// has no command; this is the end state of an admitted command that
	// was refused later.
	StateRefused State = "refused"
	// StateUncertain: nobody knows whether the message reached the agent.
	// It freezes the queue until an operator decides.
	StateUncertain State = "uncertain"
	// StateResolved: an operator decided that an uncertain command was not
	// delivered, and either dropped it or sent it again as a new command.
	StateResolved State = "resolved"
)

// EvidenceKind keeps the three sorts of evidence of the design (1.7) apart.
type EvidenceKind string

const (
	// EvidenceProviderReport is what the agent's own side shows, such as a
	// position in its conversation file.
	EvidenceProviderReport EvidenceKind = "provider-report"
	// EvidenceObservation is what Ataniq saw itself, such as what the
	// fence wrote.
	EvidenceObservation EvidenceKind = "ataniq-observation"
	// EvidenceOwnerDecision is a decision an operator recorded.
	EvidenceOwnerDecision EvidenceKind = "owner-decision"
)

// Record is one journal entry: one transition of one command, or one refusal
// at admission. It is plain data and encodes to JSON as it is. Text is set
// only on the record that creates a command.
type Record struct {
	// Seq is the sequence number, 1 for the first record and one more for
	// each next record.
	Seq uint64 `json:"seq"`
	// At is the time given to the call that made the record.
	At time.Time `json:"at"`
	// ID is the command id. On a refusal at admission it is the id the
	// sender gave, cleaned and cut short, and may not be a valid id.
	ID string `json:"id"`
	// Target and Sender are the rendered target and sender. On a refusal at
	// admission they are cleaned and cut short.
	Target string `json:"target"`
	Sender string `json:"sender"`
	// Kind is the message kind.
	Kind string `json:"kind"`
	// From and To are the states the command moves between. From is empty
	// for a new command and for a refusal at admission.
	From State `json:"from"`
	To   State `json:"to"`
	// Reason and Detail say why. Detail is free text for a log and is
	// bounded.
	Reason Reason `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
	// EvidenceKind and Evidence say what the transition rests on.
	EvidenceKind EvidenceKind `json:"evidence_kind,omitempty"`
	Evidence     string       `json:"evidence,omitempty"`
	// Operator is the operator who decided, on a resolution.
	Operator string `json:"operator,omitempty"`
	// Of is, on the record that creates a command sent again, the id of
	// the uncertain command it replaces.
	Of string `json:"of,omitempty"`
	// Replacement is, on the resolution of an uncertain command sent again,
	// the id of the new command.
	Replacement string `json:"replacement,omitempty"`
	// Text is the whole text to deliver, header included. It is only on the
	// record that creates a command.
	Text string `json:"text,omitempty"`
}

// Journal is where the state machine writes its records. Append must have
// made the record durable, in the order given, when it returns nil. When it
// returns an error the record must not be counted as written.
type Journal interface {
	Append(Record) error
}

// Refusal is the error of a call that was refused. It carries a reason code
// that is stable and fit for a log, and a detail that is bounded and safe to
// print. A message refused at admission is journaled before Refusal is
// returned; JournalErr is set when that journaling failed (the refusal
// stands, and nothing was queued).
type Refusal struct {
	Reason Reason
	Detail string
	// CommandID is the command the refusal is about, when there is one: the
	// uncertain command that freezes a queue, or the command a call named.
	CommandID string
	// Sender and Target are the rendered, cleaned parties of a refused
	// message. They are empty for refusals that are not about a message.
	Sender, Target string
	// JournalErr is not nil when the refusal could not be journaled.
	JournalErr *JournalError
}

// Error implements error.
func (r *Refusal) Error() string {
	s := fmt.Sprintf("command: refused (%s): %s", r.Reason, r.Detail)
	if r.JournalErr != nil {
		s += "; " + r.JournalErr.Error()
	}
	return s
}

// Unwrap returns the journal error, if any.
func (r *Refusal) Unwrap() error {
	if r.JournalErr == nil {
		return nil
	}
	return r.JournalErr
}

// JournalError is returned when the journal refused a record. The state is
// as it was before the call, except where the method documents otherwise.
type JournalError struct {
	// Seq is the sequence number of the record that failed.
	Seq uint64
	Err error
}

// Error implements error.
func (e *JournalError) Error() string {
	return fmt.Sprintf("command: journal append of record %d failed: %v", e.Seq, e.Err)
}

// Unwrap returns the journal's error.
func (e *JournalError) Unwrap() error { return e.Err }

// RestoreError is returned by Restore for a record that does not follow the
// state machine.
type RestoreError struct {
	// Seq is the sequence number of the record, as it was read.
	Seq uint64
	Err error
}

// Error implements error.
func (e *RestoreError) Error() string {
	return fmt.Sprintf("command: restore: record %d: %v", e.Seq, e.Err)
}

// Unwrap returns the cause.
func (e *RestoreError) Unwrap() error { return e.Err }

var errInternal = errors.New("command: internal inconsistency")
