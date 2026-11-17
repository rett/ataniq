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
	switch r.Type {
	case "":
		if r.Held != 0 || r.Piece != "" {
			return errors.New("only a hold or a release record carries a hold counter or a piece")
		}
	case RecordHold:
		return m.checkHold(r)
	case RecordRelease:
		return m.checkRelease(r)
	default:
		return fmt.Errorf("unknown record type %q", r.Type)
	}
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
		case m.pendingResend(old) != nil:
			return fmt.Errorf("command %q was already sent again", r.Of)
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
			return fmt.Errorf("queue is frozen by command %q", f.id)
		}
		if h := m.holds[r.Target]; h != nil {
			return fmt.Errorf("queue is held for command %q", h.about[0])
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
		return checkWritten(r, WrittenNothing)
	case r.From == StateDispatching && r.To == StateDispatching:
		if c.hasEnter || r.Reason != ReasonEnterWritten {
			return errors.New("Enter is recorded once, as enter-written")
		}
		return checkWritten(r, WrittenPasteAndEnter)
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
		return checkWritten(r, WrittenNothing)
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
		case ReasonNoFenceResult:
			if c.hasEnter {
				return errors.New("no-fence-result after Enter")
			}
		default:
			return fmt.Errorf("uncertain with reason %q", r.Reason)
		}
	case r.From == StateQueued && r.To == StateResolved:
		// An operator withdraws a queued command of a held queue.
		if r.Reason != ReasonOperatorWithdrawn || !validOperator(r.Operator) || r.EvidenceKind != EvidenceOwnerDecision || r.Evidence != withdrawnEvidence || r.Replacement != "" {
			return errors.New("an operator withdraws a command with a named operator and an owner decision")
		}
		if m.holds[r.Target] == nil {
			return errors.New("a command is withdrawn only from a held queue")
		}
	case r.From == r.To && (r.From == StateUncertain || r.From == StateResolved):
		// An annotation: a late fence result or late evidence, on an uncertain
		// command or on one an operator resolved as "send again" or "drop". It
		// changes no state, so it never unfreezes a queue.
		if !c.takesLate() {
			return fmt.Errorf("a late annotation for command %q, which is %s and was not decided as sent again or dropped", c.id, c.state)
		}
		if r.Operator != "" || r.Replacement != "" {
			return errors.New("a late annotation names no operator and no replacement")
		}
		// The hold is journaled before the note, so a note for a decided
		// command always finds its queue held for that command.
		if m.holdReasonFor(c) != "" && !m.holds[r.Target].has(c.id) {
			return fmt.Errorf("a late annotation for command %q needs the queue of %s to be held for it", c.id, r.Target)
		}
		if len(c.late) >= maxLateNotes {
			return fmt.Errorf("command %q already has %d late annotations", c.id, len(c.late))
		}
		switch r.Reason {
		case ReasonLateFenceResult:
			if r.EvidenceKind != EvidenceObservation || !knownWritten(r.Evidence) {
				return errors.New("a late fence result rests on the observation of what was written")
			}
		case ReasonLateEvidence:
			if r.EvidenceKind != EvidenceProviderReport || strings.TrimSpace(r.Evidence) == "" || len(r.Evidence) > maxEvidenceBytes {
				return errors.New("late evidence is a provider report, not empty and bounded")
			}
		default:
			return fmt.Errorf("uncertain to uncertain with reason %q", r.Reason)
		}
	case r.From == StateUncertain && r.To == StateDelivered:
		if r.Reason != ReasonOperatorDelivered || !validOperator(r.Operator) || r.EvidenceKind != EvidenceOwnerDecision {
			return errors.New("an operator resolves delivered with a named operator and an owner decision")
		}
		if p := m.pendingResend(c); p != nil {
			return fmt.Errorf("command %q was already sent again as %q, which can only be completed", c.id, p.id)
		}
	case r.From == StateUncertain && r.To == StateResolved:
		if !in(r.Reason, ReasonOperatorDrop, ReasonOperatorResend) || !validOperator(r.Operator) || r.EvidenceKind != EvidenceOwnerDecision {
			return errors.New("an operator resolves with a named operator and an owner decision")
		}
		// A resend is written as the new command first and the resolution
		// second, so a resolution is a resend only for the command queued to
		// replace this one, and a drop only when none is.
		p := m.pendingResend(c)
		if r.Reason == ReasonOperatorResend {
			if p == nil || r.Replacement != p.id {
				return fmt.Errorf("resend names replacement %q, which is not the command queued to replace %q", r.Replacement, c.id)
			}
		} else {
			if r.Replacement != "" {
				return errors.New("a dropped command has no replacement")
			}
			if p != nil {
				return fmt.Errorf("command %q was already sent again as %q, which can only be completed", c.id, p.id)
			}
		}
	default:
		return fmt.Errorf("no transition from %s to %s", r.From, r.To)
	}
	return nil
}

// knownWritten reports whether s is one of the four things a fence can have
// written.
func knownWritten(s string) bool {
	switch Written(s) {
	case WrittenNothing, WrittenPaste, WrittenPasteAndEnter, WrittenUnknown:
		return true
	}
	return false
}

// checkWritten requires the evidence of a fence record to be the observation
// of what the fence wrote, which is fixed by the transition: nothing for a
// command that goes back to queued or is refused, paste and Enter for the
// Enter record. Evidence that says more was written than the record's own
// transition allows would let a pasted command be queued and dispatched again.
func checkWritten(r Record, want Written) error {
	if r.EvidenceKind != EvidenceObservation || r.Evidence != string(want) {
		return fmt.Errorf("a fence record from dispatching to %s rests on the observation %q, not on %s %q", r.To, want, r.EvidenceKind, safeField(r.Evidence, 40))
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
	if len(r.Text) > m.limits.MaxTextBytes {
		return fmt.Errorf("text is %d bytes, the limit is %d", len(r.Text), m.limits.MaxTextBytes)
	}
	if !strings.HasPrefix(r.Text, header) {
		return errors.New("text does not start with the header of the command")
	}
	if reason, detail := CheckBody(r.Text[len(header):]); reason != "" {
		return fmt.Errorf("text is refused by the body rules (%s): %s", reason, detail)
	}
	return nil
}

// checkHold checks a hold record: a piece of late information about a command
// an operator decided was not delivered, or is deciding. It sets the target's
// hold counter to one more than it was, carries the digest and a rendering of
// the late call, and is not a repeat of the newest piece of a queue that is
// held. A record the live machine could not have written is an error.
func (m *Machine) checkHold(r Record) error {
	if r.From != "" || r.To != "" || r.Sender != "" || r.Kind != "" || r.Text != "" || r.Of != "" || r.Replacement != "" ||
		r.Operator != "" || r.EvidenceKind != "" || r.Evidence != "" {
		return errors.New("a hold record names only the target, the command, the reason, the counter, the piece, its rendering and the time")
	}
	c := m.cmds[r.ID]
	if c == nil {
		return fmt.Errorf("a hold about unknown command %q", safeField(r.ID, 70))
	}
	if r.Target != c.target.String() {
		return fmt.Errorf("a hold names a different target than command %q", c.id)
	}
	want := m.holdReasonFor(c)
	if want == "" {
		return fmt.Errorf("a hold about command %q, which is %s and is neither uncertain with a resend pending nor resolved as sent again or dropped", c.id, c.state)
	}
	if r.Reason != want {
		return fmt.Errorf("a hold about command %q has reason %q, want %q", c.id, r.Reason, want)
	}
	if want := m.heldSeq[r.Target] + 1; r.Held != want {
		return fmt.Errorf("a hold of %s sets the counter to %d, want %d", r.Target, r.Held, want)
	}
	if !validPiece(r.Piece) {
		return errors.New("a hold record carries the digest of the late call")
	}
	if r.Detail == "" || safeField(r.Detail, maxDetailBytes) != r.Detail {
		return errors.New("a hold record carries a bounded, safe rendering of the late call")
	}
	if h := m.holds[r.Target]; h != nil && h.last == r.Piece {
		return fmt.Errorf("a hold of %s repeats the last piece of late information, which the machine does not journal", r.Target)
	}
	return nil
}

// checkRelease checks a release record: an operator ends the hold of a queue
// that is held, having seen its current counter.
func (m *Machine) checkRelease(r Record) error {
	if r.From != "" || r.To != "" || r.ID != "" || r.Sender != "" || r.Kind != "" || r.Text != "" || r.Of != "" || r.Replacement != "" {
		return errors.New("a release record names only the target and the operator")
	}
	if r.Reason != ReasonHoldReleased || r.EvidenceKind != EvidenceOwnerDecision || r.Evidence != releaseEvidence || !validOperator(r.Operator) {
		return errors.New("an operator releases a hold with a named operator and an owner decision")
	}
	if m.holds[r.Target] == nil {
		return fmt.Errorf("a release of the queue of %q, which is not held", safeField(r.Target, 70))
	}
	if r.Held != m.heldSeq[r.Target] {
		return fmt.Errorf("a release of the queue of %q saw the hold counter at %d, it is at %d", safeField(r.Target, 70), r.Held, m.heldSeq[r.Target])
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
	switch r.Type {
	case RecordHold:
		h := m.holds[r.Target]
		if h == nil {
			h = &hold{reason: r.Reason, since: r.At}
			m.holds[r.Target] = h
		}
		if !h.has(r.ID) {
			h.about = append(h.about, r.ID)
		}
		h.last = r.Piece
		h.pieces = append(h.pieces, HeldPiece{Held: r.Held, ID: r.ID, At: r.At, Detail: r.Detail})
		if len(h.pieces) > maxHeldPieces {
			h.pieces = append([]HeldPiece(nil), h.pieces[len(h.pieces)-maxHeldPieces:]...)
		}
		m.heldSeq[r.Target] = r.Held
		return
	case RecordRelease:
		delete(m.holds, r.Target)
		return
	}
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
	if r.From == r.To && (r.From == StateUncertain || r.From == StateResolved) {
		// An annotation keeps the state, the last reason, the evidence and the
		// update time of the command; it is only added to the notes.
		c.late = append(c.late, LateNote{
			Seq: r.Seq, At: r.At, Reason: r.Reason, Detail: r.Detail,
			EvidenceKind: r.EvidenceKind, Evidence: r.Evidence,
		})
		return
	}
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
