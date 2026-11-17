package command

import (
	"time"
)

// Decision is what an operator decides about an uncertain command.
type Decision string

const (
	// DecisionDelivered: the message did reach the agent.
	DecisionDelivered Decision = "delivered"
	// DecisionDrop: the message was not delivered, and is dropped.
	DecisionDrop Decision = "not delivered, drop"
	// DecisionResend: the message was not delivered, and is sent again as
	// a new command.
	DecisionResend Decision = "not delivered, send again"
)

// Resolution is an operator's decision about an uncertain command.
type Resolution struct {
	// ID is the uncertain command.
	ID string
	// Decision is one of the three decisions.
	Decision Decision
	// Operator names who decided. It must be non-empty, printable and at
	// most 128 bytes.
	Operator string
	// NewID is, for DecisionResend, the id of the new command. The caller
	// supplies it; it must match ValidName and be unused.
	NewID string
	// Note is optional free text for the log.
	Note string
	// SeenLate is the number of late notes the operator was shown for the
	// command (len(CommandView.Late)). The decision is refused when more, or
	// fewer, are there now, so that it never covers a note nobody read.
	SeenLate int
}

// Resolved is the effect of Resolve.
type Resolved struct {
	ID    string
	State State
	// NewID is the new command of a resend.
	NewID string
}

// Resolve records an operator's decision about an uncertain command. It is the
// only way out of uncertain, and any decision unfreezes the queue.
//
//   - DecisionDelivered: the command becomes delivered, on the evidence of the
//     owner's decision.
//   - DecisionDrop: the command becomes resolved (reason operator-drop).
//   - DecisionResend: a new command with NewID, the same sender, target and kind,
//     and the same body under a header with the new id, is created in state
//     queued at the head of the queue, naming the old id; the old command
//     becomes resolved (reason operator-resend) naming the new one. The new text
//     must still fit the text cap. The caps of admission do not apply, because
//     the new command takes the place of the old.
//
// Late evidence for the old command after this call is journaled on it and
// holds the queue (see ConfirmDelivery), whatever state the new command is in.
// The same evidence while a resend is half written, before the second record,
// holds the queue too. Resolve does not end a hold: ReleaseHold does.
//
// A resend writes two records: the new command first, then the resolution of
// the old one. If the second append fails the new command is queued behind
// nothing but the still uncertain old one, so the queue stays frozen; calling
// Resolve again with the same arguments writes only the second record.
//
// A decision covers the late notes the operator saw: res.SeenLate is the number
// of them (len(CommandView.Late), or the same from Machine.Command), and the
// call is refused (ReasonNotesChanged) when the command has another number now.
// The operator reads the command again and decides again. A repeat that
// finishes a half-written resend names the current number too, so late
// information that arrived in between is seen first.
//
// It refuses with a *Refusal, and journals nothing, when the command is not
// uncertain, the decision or operator is bad, the number of late notes seen is
// not the current one, or the new id is bad, used, or makes the text too long.
func (m *Machine) Resolve(res Resolution, now time.Time) (Resolved, error) {
	c := m.cmds[res.ID]
	switch {
	case c == nil:
		return Resolved{}, refusal(ReasonNoCommand, res.ID, "no command %q", safeField(res.ID, 70))
	case c.state != StateUncertain:
		return Resolved{}, refusal(ReasonWrongState, c.id, "command %q is %s, not uncertain", c.id, c.state)
	case !validOperator(res.Operator):
		return Resolved{}, refusal(ReasonBadOperator, c.id, "a resolution names its operator, printable and at most %d bytes", maxOperatorBytes)
	}
	// A resend whose second record failed leaves its new command queued. Only
	// the same resend may go on, or the message would be sent twice.
	if p := m.pendingResend(c); p != nil && (res.Decision != DecisionResend || res.NewID != p.id) {
		return Resolved{}, refusal(ReasonWrongState, c.id, "command %q was already sent again as %q; repeat that resolution to finish it", c.id, p.id)
	}
	// The operator decides about the notes they saw. This holds for every
	// decision, and for the repeat that finishes a half-written resend.
	if len(c.late) != res.SeenLate {
		return Resolved{}, refusal(ReasonNotesChanged, c.id, "command %q has %d late notes, the decision saw %d: late information arrived since", c.id, len(c.late), res.SeenLate)
	}
	r := base(c, now)
	r.From = StateUncertain
	r.Operator = res.Operator
	r.EvidenceKind = EvidenceOwnerDecision
	r.Detail = safeField(res.Note, maxDetailBytes)

	switch res.Decision {
	case DecisionDelivered:
		r.To, r.Reason, r.Evidence = StateDelivered, ReasonOperatorDelivered, string(DecisionDelivered)
		if err := m.commit(r); err != nil {
			return Resolved{}, err
		}
		return Resolved{ID: c.id, State: c.state}, nil
	case DecisionDrop:
		r.To, r.Reason, r.Evidence = StateResolved, ReasonOperatorDrop, string(DecisionDrop)
		if err := m.commit(r); err != nil {
			return Resolved{}, err
		}
		return Resolved{ID: c.id, State: c.state}, nil
	case DecisionResend:
	default:
		return Resolved{}, refusal(ReasonBadDecision, c.id, "unknown decision %q", safeField(string(res.Decision), 70))
	}

	if !ValidName(res.NewID) {
		return Resolved{}, refusal(ReasonBadID, c.id, "new id %q does not match the name grammar", safeField(res.NewID, 70))
	}
	next := m.cmds[res.NewID]
	if next != nil && (next.of != c.id || next.state != StateQueued) {
		return Resolved{}, refusal(ReasonDuplicateID, c.id, "a command with id %q exists", res.NewID)
	}
	if next == nil {
		body := c.text[len(HeaderLine(c.id, c.sender, c.kind))+1:]
		text := RenderText(res.NewID, c.sender, c.kind, body)
		if len(text) > m.limits.MaxTextBytes {
			return Resolved{}, refusal(ReasonTooLong, c.id, "the text with the new id is %d bytes, the limit is %d", len(text), m.limits.MaxTextBytes)
		}
		n := base(c, now)
		n.ID = res.NewID
		n.To = StateQueued
		n.Reason, n.Detail = ReasonOperatorResend, safeField("sent again from "+c.id+": "+res.Note, maxDetailBytes)
		n.EvidenceKind, n.Evidence = EvidenceOwnerDecision, string(DecisionResend)
		n.Operator, n.Of, n.Text = res.Operator, c.id, text
		if err := m.commit(n); err != nil {
			return Resolved{}, err
		}
	}
	r.To, r.Reason, r.Evidence = StateResolved, ReasonOperatorResend, string(DecisionResend)
	r.Replacement = res.NewID
	if err := m.commit(r); err != nil {
		return Resolved{NewID: res.NewID}, err
	}
	return Resolved{ID: c.id, State: c.state, NewID: res.NewID}, nil
}

// pendingResend returns the command queued to replace c, if there is one.
func (m *Machine) pendingResend(c *command) *command {
	for _, x := range m.queues[c.target.String()] {
		if x.of == c.id {
			return x
		}
	}
	return nil
}
