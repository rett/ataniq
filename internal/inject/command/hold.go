package command

import "time"

// The evidence text of the two decisions an operator takes about a held queue.
const (
	releaseEvidence   = "hold released"
	withdrawnEvidence = "withdrawn, not sent"
)

// takesLate reports whether a late fence result or late delivery evidence for
// c is kept as a note: c is uncertain, or an operator resolved it as "not
// delivered, send again" or "not delivered, drop".
func (c *command) takesLate() bool {
	return c.state == StateUncertain || c.resolvedAs(ReasonOperatorResend) || c.resolvedAs(ReasonOperatorDrop)
}

// resolvedAs reports whether an operator resolved c with the reason.
func (c *command) resolvedAs(reason Reason) bool {
	return c.state == StateResolved && c.reason == reason
}

// holdReasonFor returns the reason for which late information about c holds
// its queue, or "" when it does not: c is resolved as sent again or dropped,
// or it is uncertain and its resend is half written (its replacement is
// queued). An uncertain command with no resend pending has its queue frozen
// already, and a late note for it holds nothing.
func (m *Machine) holdReasonFor(c *command) Reason {
	switch {
	case c.state == StateUncertain && m.pendingResend(c) != nil:
		return ReasonHoldDuringResend
	case c.resolvedAs(ReasonOperatorResend):
		return ReasonHoldAfterResend
	case c.resolvedAs(ReasonOperatorDrop):
		return ReasonHoldAfterDrop
	}
	return ""
}

// holdRecord is the record that holds the queue of c's target because late
// information arrived about c.
func holdRecord(c *command, reason Reason, now time.Time) Record {
	return Record{
		At: now, Type: RecordHold, ID: c.id, Target: c.target.String(), Reason: reason,
		Detail: safeField("late information arrived for "+c.id+"; an operator looks at its late notes and releases the hold", maxDetailBytes),
	}
}

// ReleaseHold ends the hold of a target's queue. operator names who decides:
// it must be non-empty, printable and at most 128 bytes. The release is
// journaled as an owner decision. It does nothing else: it does not decide
// about an uncertain command (Resolve does), it does not withdraw a queued
// command (Withdraw does), and the queue dispatches again unless an uncertain
// command still freezes it. No timer, Tick or Restore ends a hold, and
// releasing ends the hold for every command it was about. Late information that
// arrives again after a release holds the queue again.
//
// It refuses with a *Refusal, and journals nothing, when the queue is not held
// (ReasonNotHeld) or the operator is bad (ReasonBadOperator). On a journal error
// nothing changed and the hold stands.
func (m *Machine) ReleaseHold(target Target, operator string, now time.Time) error {
	key := target.String()
	switch {
	case m.holds[key] == nil:
		return refusal(ReasonNotHeld, "", "the queue of %s is not held", target)
	case !validOperator(operator):
		return refusal(ReasonBadOperator, "", "a release names its operator, printable and at most %d bytes", maxOperatorBytes)
	}
	return m.commit(Record{
		At: now, Type: RecordRelease, Target: key, Reason: ReasonHoldReleased,
		EvidenceKind: EvidenceOwnerDecision, Evidence: releaseEvidence, Operator: operator,
	})
}

// Withdraw ends a queued command of a held queue without sending it: the
// command becomes resolved (reason operator-withdrawn) and leaves the queue.
// operator names who decides, as for Resolve. It is for a replacement or a
// later command that the operator does not want sent now that late information
// arrived; it never ends a hold, and the next command of the queue is still
// held back until ReleaseHold.
//
// It refuses with a *Refusal, and journals nothing, when there is no such
// command (ReasonNoCommand), the command is not queued (ReasonWrongState; a
// dispatching command is not interrupted), its queue is not held
// (ReasonNotHeld) or the operator is bad (ReasonBadOperator). On a journal error
// nothing changed.
func (m *Machine) Withdraw(id, operator string, now time.Time) error {
	c := m.cmds[id]
	switch {
	case c == nil:
		return refusal(ReasonNoCommand, id, "no command %q", safeField(id, 70))
	case c.state != StateQueued:
		return refusal(ReasonWrongState, c.id, "command %q is %s, not queued", c.id, c.state)
	case m.holds[c.target.String()] == nil:
		return refusal(ReasonNotHeld, c.id, "the queue of %s is not held", c.target)
	case !validOperator(operator):
		return refusal(ReasonBadOperator, c.id, "a withdrawal names its operator, printable and at most %d bytes", maxOperatorBytes)
	}
	r := base(c, now)
	r.From, r.To, r.Reason = StateQueued, StateResolved, ReasonOperatorWithdrawn
	r.Operator, r.EvidenceKind, r.Evidence = operator, EvidenceOwnerDecision, withdrawnEvidence
	r.Detail = "withdrawn from a held queue"
	return m.commit(r)
}
