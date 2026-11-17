package command

import (
	"errors"
	"fmt"
	"strings"
)

// admissionReasons are the reasons a message can be refused at admission.
var admissionReasons = map[Reason]bool{
	ReasonBadSender: true, ReasonBadProject: true, ReasonEdge: true, ReasonBadID: true,
	ReasonBadKind: true, ReasonDuplicateID: true, ReasonEmptyBody: true,
	ReasonInvalidUTF8: true, ReasonBadCharacter: true, ReasonForgedHeader: true,
	ReasonTooLong: true, ReasonQueueFull: true, ReasonQueueBytes: true, ReasonRate: true,
}

func in(r Reason, set ...Reason) bool {
	for _, s := range set {
		if r == s {
			return true
		}
	}
	return false
}

// check reports whether a record follows the state machine, given the
// machine's current state. It is used for every record, live and restored,
// and changes nothing.
func (m *Machine) check(r Record) error {
	switch {
	case r.From == "" && r.To == StateRefused:
		if !admissionReasons[r.Reason] {
			return fmt.Errorf("a refusal at admission has reason %q", r.Reason)
		}
		return nil
	case r.From == "" && r.To == StateAccepted:
		if r.Reason != "" || r.Of != "" {
			return errors.New("an accepted record has no reason and no predecessor")
		}
		return m.checkNew(r)
	case r.From == "" && r.To == StateQueued:
		if err := m.checkNew(r); err != nil {
			return err
		}
		old := m.cmds[r.Of]
		switch {
		case r.Reason != ReasonOperatorResend || r.EvidenceKind != EvidenceOwnerDecision:
			return errors.New("a command sent again carries the operator's decision")
		case old == nil:
			return fmt.Errorf("sent again from unknown command %q", r.Of)
		case old.state != StateUncertain:
			return fmt.Errorf("sent again from command %q, which is %s", r.Of, old.state)
		case old.target.String() != r.Target || old.sender.String() != r.Sender || old.kind != r.Kind:
			return fmt.Errorf("a command sent again must keep the sender, target and kind of %q", r.Of)
		case !validOperator(r.Operator):
			return errors.New("a command sent again names its operator")
		}
		return nil
	case r.From == "":
		return fmt.Errorf("a record from no state to %q", r.To)
	}

	c := m.cmds[r.ID]
	if c == nil {
		return fmt.Errorf("unknown command %q", r.ID)
	}
	if c.state != r.From {
		return fmt.Errorf("command %q is %s, the record says %s", r.ID, c.state, r.From)
	}
	if r.Target != c.target.String() || r.Sender != c.sender.String() || r.Kind != c.kind {
		return fmt.Errorf("record names a different sender, target or kind than command %q", r.ID)
	}
	if r.Text != "" || r.Of != "" {
		return errors.New("only a record that creates a command carries text or a predecessor")
	}
	q := m.queues[r.Target]
	head := len(q) > 0 && q[0] == c

	switch {
	case r.From == StateAccepted && r.To == StateQueued:
		if r.Reason != "" {
			return errors.New("accepted to queued has no reason")
		}
	case r.From == StateAccepted && r.To == StateRefused:
		if !admissionReasons[r.Reason] {
			return fmt.Errorf("accepted to refused has reason %q", r.Reason)
		}
	case r.From == StateQueued && r.To == StateDispatching:
		if !head {
			return errors.New("only the head of a queue may dispatch")
		}
		if f := frozenBy(q); f != nil {
			return fmt.Errorf("queue is frozen by uncertain command %q", f.id)
		}
		if r.Reason != "" {
			return errors.New("dispatching has no reason")
		}
	case r.From == StateQueued && r.To == StateRefused:
		if !head || !in(r.Reason, ReasonNotIdle, ReasonUntestedVersion) {
			return errors.New("only the head of a queue may be refused, as not idle or untested")
		}
	case r.From == StateDispatching && r.To == StateQueued:
		if c.hasEnter || !in(r.Reason, ReasonNotIdle, ReasonBlocked) {
			return errors.New("dispatching returns to queued only before Enter, as not idle or blocked")
		}
	case r.From == StateDispatching && r.To == StateDispatching:
		if c.hasEnter || r.Reason != ReasonEnterWritten {
			return errors.New("Enter is recorded once, as enter-written")
		}
	case r.From == StateDispatching && r.To == StateDelivered:
		if !c.hasEnter {
			return errors.New("delivery needs Enter to have been written")
		}
		if r.Reason != ReasonConfirmed || r.EvidenceKind != EvidenceProviderReport || strings.TrimSpace(r.Evidence) == "" {
			return errors.New("delivery needs a provider report as evidence")
		}
	case r.From == StateDispatching && r.To == StateRefused:
		if c.hasEnter || !in(r.Reason, ReasonUntestedVersion, ReasonNotIdle, ReasonTextRefused) {
			return errors.New("dispatching is refused only before Enter, as untested, not idle or text refused")
		}
	case r.From == StateDispatching && r.To == StateUncertain:
		switch r.Reason {
		case ReasonNotSubmitted:
			if c.hasEnter {
				return errors.New("not-submitted after Enter")
			}
		case ReasonSubmittedNotConfirmed:
			if !c.hasEnter {
				return errors.New("submitted-not-confirmed before Enter")
			}
		case ReasonHolderDied, ReasonInconsistentResult:
		default:
			return fmt.Errorf("uncertain with reason %q", r.Reason)
		}
	case r.From == StateUncertain && r.To == StateDelivered:
		if r.Reason != ReasonOperatorDelivered || !validOperator(r.Operator) || r.EvidenceKind != EvidenceOwnerDecision {
			return errors.New("an operator resolves delivered with a named operator and an owner decision")
		}
	case r.From == StateUncertain && r.To == StateResolved:
		if !in(r.Reason, ReasonOperatorDrop, ReasonOperatorResend) || !validOperator(r.Operator) || r.EvidenceKind != EvidenceOwnerDecision {
			return errors.New("an operator resolves with a named operator and an owner decision")
		}
		if r.Reason == ReasonOperatorResend {
			n := m.cmds[r.Replacement]
			if n == nil || n.of != c.id || n.target != c.target {
				return fmt.Errorf("resend names replacement %q, which does not replace %q", r.Replacement, c.id)
			}
		} else if r.Replacement != "" {
			return errors.New("a dropped command has no replacement")
		}
	default:
		return fmt.Errorf("no transition from %s to %s", r.From, r.To)
	}
	return nil
}

// checkNew checks the parts of a record that creates a command.
func (m *Machine) checkNew(r Record) error {
	if !ValidName(r.ID) {
		return fmt.Errorf("bad id %q", safeField(r.ID, 70))
	}
	if _, dup := m.cmds[r.ID]; dup {
		return fmt.Errorf("duplicate id %q", r.ID)
	}
	from, err := ParseSender(r.Sender)
	if err != nil {
		return err
	}
	to, err := ParseTarget(r.Target)
	if err != nil {
		return err
	}
	if !EdgeAllowed(from, to) {
		return fmt.Errorf("%s may not send to %s", from, to)
	}
	if !ValidName(r.Kind) {
		return fmt.Errorf("bad kind %q", safeField(r.Kind, 70))
	}
	header := HeaderLine(r.ID, from, r.Kind) + "\n"
	if len(r.Text) > maxRestoreText || !strings.HasPrefix(r.Text, header) {
		return errors.New("text does not start with the header of the command")
	}
	if reason, detail := CheckBody(r.Text[len(header):]); reason != "" {
		return fmt.Errorf("text is refused by the body rules (%s): %s", reason, detail)
	}
	return nil
}

// validOperator reports whether an operator name is non-empty, bounded and
// made of printable characters.
func validOperator(s string) bool {
	return strings.TrimSpace(s) != "" && len(s) <= maxOperatorBytes && safeField(s, maxOperatorBytes) == s && !strings.Contains(s, "\n")
}

// apply changes the state as the record says. The record must have passed
// check.
func (m *Machine) apply(r Record) {
	switch {
	case r.From == "" && r.To == StateRefused:
		return
	case r.From == "" && (r.To == StateAccepted || r.To == StateQueued):
		from, _ := ParseSender(r.Sender)
		to, _ := ParseTarget(r.Target)
		c := &command{
			id: r.ID, kind: r.Kind, sender: from, target: to, text: r.Text,
			state: r.To, admitted: r.At, updated: r.At, of: r.Of,
		}
		m.cmds[c.id] = c
		key := r.Target
		if r.To == StateAccepted {
			m.queues[key] = append(m.queues[key], c)
			rate := m.rates[r.Sender]
			cut := r.At.Add(-m.limits.RateWindow)
			kept := rate[:0:0]
			for _, t := range rate {
				if t.After(cut) {
					kept = append(kept, t)
				}
			}
			m.rates[r.Sender] = append(kept, r.At)
		} else {
			c.evidenceKind, c.evidence, c.operator = r.EvidenceKind, r.Evidence, r.Operator
			c.reason, c.detail = ReasonOperatorResend, r.Detail
			m.queues[key] = append([]*command{c}, m.queues[key]...)
		}
		return
	}

	c := m.cmds[r.ID]
	key := c.target.String()
	c.state = r.To
	c.updated = r.At
	if r.Reason != "" {
		c.reason, c.detail = r.Reason, r.Detail
	}
	if r.EvidenceKind != "" {
		c.evidenceKind, c.evidence = r.EvidenceKind, r.Evidence
	}
	if r.Operator != "" {
		c.operator = r.Operator
	}
	if r.Replacement != "" {
		c.replacement = r.Replacement
	}
	if r.From == StateDispatching && r.To == StateDispatching {
		c.hasEnter, c.enterAt = true, r.At
	}
	// A blocked spell lasts until the next fence result that is not blocked.
	if r.From == StateDispatching {
		m.blocked[key] = r.To == StateQueued && r.Reason == ReasonBlocked
	}
	switch r.To {
	case StateDelivered, StateRefused, StateResolved:
		c.text = ""
		q := m.queues[key]
		for i, x := range q {
			if x == c {
				m.queues[key] = append(q[:i:i], q[i+1:]...)
				break
			}
		}
		if len(m.queues[key]) == 0 {
			delete(m.queues, key)
		}
	}
}
