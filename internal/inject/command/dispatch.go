package command

import (
	"fmt"
	"strings"
	"time"
)

// Written says what the fence wrote to the agent. The spellings are those of
// the fence's result, so the daemon can pass them through.
type Written string

const (
	WrittenNothing       Written = "nothing"
	WrittenPaste         Written = "paste"
	WrittenPasteAndEnter Written = "paste and Enter"
	WrittenUnknown       Written = "unknown"
)

// FenceOutcome is how the fence ended.
type FenceOutcome string

const (
	// OutcomeNotIdle: the guard said the lead is not idle; nothing written.
	OutcomeNotIdle FenceOutcome = "not idle"
	// OutcomeBlocked: the lead is waiting for an approval; nothing written.
	OutcomeBlocked FenceOutcome = "blocked"
	// OutcomeUntestedVersion: the runtime version is not in the tested
	// table; nothing written.
	OutcomeUntestedVersion FenceOutcome = "untested version"
	// OutcomeRefusedText: the fence refused the text; nothing written.
	OutcomeRefusedText FenceOutcome = "refused text"
	// OutcomeNotSubmitted: the paste was written and the check after it
	// failed, so Enter was not written.
	OutcomeNotSubmitted FenceOutcome = "not submitted"
	// OutcomeSubmitted: the paste and Enter were written.
	OutcomeSubmitted FenceOutcome = "submitted"
	// OutcomeDied: the holder or the agent died during the fence.
	OutcomeDied FenceOutcome = "died"
)

// FenceResult is what the fence reports, as plain values. This package does
// not import the fence.
type FenceResult struct {
	Written Written
	Outcome FenceOutcome
	// Readings is the text of the guard's readings, for the log. It is
	// bounded here. It is kept as the command's last reason when the command
	// goes back to the queue.
	Readings string
}

// NoticeBlocked is the notice for the owner when a lead is blocked.
const NoticeBlocked = "this lead is waiting for an approval"

// Dispatch is what the daemon needs to run the fence: the command and the
// text to send. It is returned only after the dispatching record is
// journaled.
type Dispatch struct {
	ID     string
	Target Target
	// Header is the header line the composer must hold before Enter.
	Header string
	// Text is the whole text, header and body.
	Text string
}

// FenceUpdate is the effect of RecordFence.
type FenceUpdate struct {
	ID     string
	Target Target
	// State and Reason are the command's state and reason after the call.
	State  State
	Reason Reason
	// Notice is not empty when the owner is to be told something: it is
	// NoticeBlocked on the first blocked result of a blocked spell.
	Notice string
}

func refusal(reason Reason, id, format string, args ...any) *Refusal {
	return &Refusal{Reason: reason, CommandID: id, Detail: safeField(fmt.Sprintf(format, args...), maxDetailBytes)}
}

// BeginDispatch starts the dispatch of the head of a target's queue. It
// refuses with a *Refusal when the queue is held (ReasonQueueHeld, naming the
// command the hold is about; checked first, so a held queue is refused even when
// it is also frozen or empty), when the queue is empty (ReasonQueueEmpty),
// frozen by an uncertain command (ReasonQueueFrozen, naming that command), when
// the head is already dispatching (ReasonAlreadyDispatching) or when the head is
// not yet queued (ReasonHeadNotQueued). Otherwise it appends the dispatching
// record, which is the journaled intent, and only then returns the text. On a
// journal error nothing changed and no text is returned: no byte may be
// written without the intent.
func (m *Machine) BeginDispatch(target Target, now time.Time) (Dispatch, error) {
	if h := m.holds[target.String()]; h != nil {
		return Dispatch{}, refusal(ReasonQueueHeld, h.about[0], "the queue of %s is held since %s: late information arrived for command %q; an operator releases the hold", target, h.since.UTC().Format(time.RFC3339), h.about[0])
	}
	q := m.queues[target.String()]
	if len(q) == 0 {
		return Dispatch{}, refusal(ReasonQueueEmpty, "", "the queue of %s is empty", target)
	}
	if f := frozenBy(q); f != nil {
		return Dispatch{}, refusal(ReasonQueueFrozen, f.id, "the queue of %s is frozen by uncertain command %q", target, f.id)
	}
	head := q[0]
	switch head.state {
	case StateDispatching:
		return Dispatch{}, refusal(ReasonAlreadyDispatching, head.id, "command %q is already dispatching", head.id)
	case StateQueued:
	default:
		return Dispatch{}, refusal(ReasonHeadNotQueued, head.id, "the head %q is %s, not queued", head.id, head.state)
	}
	r := base(head, now)
	r.From, r.To = StateQueued, StateDispatching
	if err := m.commit(r); err != nil {
		return Dispatch{}, err
	}
	return Dispatch{
		ID:     head.id,
		Target: head.target,
		Header: HeaderLine(head.id, head.sender, head.kind),
		Text:   head.text,
	}, nil
}

// RecordFence records the result of the fence for the dispatching command id.
// What happens depends on what was written and how the fence ended:
//
//   - nothing written, not idle: the command goes back to queued with the
//     readings kept as its last reason; when now is past the queue time limit
//     since admission it becomes refused (not idle) instead;
//   - nothing written, blocked: back to queued; the update carries NoticeBlocked
//     the first time in a blocked spell, which ends with the next result that is
//     not blocked;
//   - nothing written, untested version: refused (untested version);
//   - nothing written, text refused: refused (text refused);
//   - paste written, not submitted: uncertain (not submitted);
//   - paste and Enter written, submitted: the command stays dispatching,
//     submitted at now, waiting for confirmation;
//   - died, or what was written is unknown: uncertain (holder or agent died);
//   - any other combination contradicts itself: uncertain (inconsistent
//     result), which is safe because it freezes the queue.
//
// A fence result for a command that is already uncertain, as one that a Tick
// made uncertain after FenceTimeout, is late. It is journaled as an annotation
// (uncertain to uncertain, ReasonLateFenceResult, with what was written as the
// evidence and the outcome and readings as the detail) and shown in the view
// (CommandView.Late). It never moves the command out of uncertain and never
// unfreezes the queue: only an operator does. The update carries the state and
// reason the command has. What was written is normalised to one of the four
// values of Written, and anything else is recorded as unknown.
//
// The same holds for a command that an operator resolved as "not delivered,
// send again" or "not delivered, drop" (state resolved): the late result is
// journaled as an annotation on it (resolved to resolved), and the queue of the
// target is held first. See ConfirmDelivery for the hold and for the rules that
// all late paths share (repeats, the cap, ended commands).
//
// Otherwise it refuses with ReasonWrongState unless the command is dispatching
// and Enter has not been recorded. On a journal error nothing changed: the
// daemon retries RecordFence with the same result, and never calls BeginDispatch again for
// the command, which stays dispatching and is refused as already dispatching.
func (m *Machine) RecordFence(id string, res FenceResult, now time.Time) (FenceUpdate, error) {
	c := m.cmds[id]
	switch {
	case c == nil:
		return FenceUpdate{}, refusal(ReasonNoCommand, id, "no command %q", safeField(id, 70))
	case c.takesLate():
		return m.lateFence(c, res, now)
	case c.state != StateDispatching || c.hasEnter:
		return FenceUpdate{}, refusal(ReasonWrongState, id, "command %q is %s and cannot take a fence result", id, c.state)
	}
	r := base(c, now)
	r.From = StateDispatching
	r.Detail = safeField(res.Readings, maxDetailBytes)
	r.EvidenceKind = EvidenceObservation
	r.Evidence = safeField(string(res.Written), maxFieldBytes)

	uncertain := func(reason Reason) {
		r.To, r.Reason = StateUncertain, reason
	}
	switch {
	case res.Outcome == OutcomeDied || res.Written == WrittenUnknown:
		uncertain(ReasonHolderDied)
	case res.Written == WrittenNothing:
		switch res.Outcome {
		case OutcomeNotIdle:
			if now.Sub(c.admitted) > m.limits.QueueTimeLimit {
				r.To, r.Reason = StateRefused, ReasonNotIdle
			} else {
				r.To, r.Reason = StateQueued, ReasonNotIdle
			}
		case OutcomeBlocked:
			r.To, r.Reason = StateQueued, ReasonBlocked
		case OutcomeUntestedVersion:
			r.To, r.Reason = StateRefused, ReasonUntestedVersion
		case OutcomeRefusedText:
			r.To, r.Reason = StateRefused, ReasonTextRefused
		default:
			uncertain(ReasonInconsistentResult)
		}
	case res.Written == WrittenPaste && res.Outcome == OutcomeNotSubmitted:
		uncertain(ReasonNotSubmitted)
	case res.Written == WrittenPasteAndEnter && res.Outcome == OutcomeSubmitted:
		r.To, r.Reason = StateDispatching, ReasonEnterWritten
	default:
		uncertain(ReasonInconsistentResult)
	}

	wasBlocked := m.blocked[c.target.String()]
	if err := m.commit(r); err != nil {
		return FenceUpdate{}, err
	}
	up := FenceUpdate{ID: id, Target: c.target, State: c.state, Reason: r.Reason}
	if r.Reason == ReasonBlocked && !wasBlocked {
		up.Notice = NoticeBlocked
	}
	return up, nil
}

// lateFence journals a fence result that arrived for an uncertain command, or
// for one an operator resolved as sent again or dropped.
func (m *Machine) lateFence(c *command, res FenceResult, now time.Time) (FenceUpdate, error) {
	written := res.Written
	if !knownWritten(string(written)) {
		written = WrittenUnknown
	}
	r := base(c, now)
	r.Reason = ReasonLateFenceResult
	r.Detail = safeField("outcome: "+safeField(string(res.Outcome), 40)+"; "+res.Readings, maxDetailBytes)
	r.EvidenceKind, r.Evidence = EvidenceObservation, string(written)
	piece := pieceDigest("late-fence-result", c.id, string(res.Written), string(res.Outcome), res.Readings)
	if err := m.late(c, r, piece, now); err != nil {
		return FenceUpdate{}, err
	}
	return FenceUpdate{ID: c.id, Target: c.target, State: c.state, Reason: c.reason}, nil
}

// ConfirmDelivery records evidence that the message reached the agent, such as
// a position in the conversation file, and makes the command delivered. The
// evidence is recorded as a provider report. It is allowed only for a
// dispatching command whose Enter was written (ReasonWrongState, ReasonNoEnter
// otherwise), and the evidence must be non-empty and at most 512 bytes
// (ReasonBadEvidence). An uncertain command cannot be confirmed: only an
// operator leaves uncertain.
//
// Evidence for a command that is already uncertain is late, and its Enter may
// never have been recorded (a fence the timeout cut off may still have
// written). It is journaled as an annotation (uncertain to uncertain,
// ReasonLateEvidence, as a provider report), shown in the view
// (CommandView.Late) for the operator, and the command stays uncertain with
// the queue frozen. The same bounds apply to the evidence.
//
// Late information about a command that an operator decided was not delivered,
// or is deciding, holds the target's queue. The cases are a command resolved as
// "not delivered, send again" (state resolved, ReasonHoldAfterResend), one
// resolved as "not delivered, drop" (ReasonHoldAfterDrop), and an uncertain
// command whose resend is half written, that is, its replacement is queued and
// the resolution that completes the resend is not journaled yet
// (ReasonHoldDuringResend). The call journals a hold record (RecordHold) and
// then the note (resolved to resolved, or uncertain to uncertain). While the
// queue is held BeginDispatch refuses with ReasonQueueHeld; the hold changes no
// command, whatever state a replacement is in: a replacement that is already
// dispatching is not interrupted, and when its fence wrote nothing it returns
// to queued and the hold keeps it there. Only ReleaseHold ends a hold; a queued
// replacement the operator does not want sent is ended with Withdraw. Late
// information for an uncertain command with no resend pending is a note only:
// that command already freezes the queue.
//
// Every such call is a piece of late information and is decided before the
// rules below, whatever else is wrong with it: evidence that is refused as bad
// (ReasonBadEvidence) holds the queue first and is refused after, and neither a
// repeated note nor a full note list suppresses it. The hold record is written
// each time, not only for the first piece: it advances the target's hold
// counter (TargetView.HeldSeq), which ReleaseHold must name, so that a release
// never covers a piece the operator has not seen. The one call that writes no
// hold record is a repeat, byte for byte, of the last piece of a queue that is
// still held. If the hold record fails to append, nothing changed and the error
// is returned: the daemon repeats the call. If the note then fails to append,
// the hold stands and the error is returned: repeating the call writes the note
// only. After a release, the same evidence for the same command holds the queue
// again.
//
// The rules for every late note, on an uncertain or on a decided command:
//
//   - A note equal to one the command already has (same reason, detail,
//     evidence kind and evidence) is skipped: no note is written and there is
//     no error. The daemon should still pass each piece of evidence once; the
//     skip is a safety net, not a way to poll.
//   - At most eight notes are kept for a command. A different note after that
//     is refused with ReasonLateNotesFull, and no note is written; the hold, if
//     the command calls for one, was set first and stands.
//   - Evidence for a command that was delivered, refused, or resolved by a
//     withdrawal is refused with ReasonWrongState.
//   - Notes stay with the command after it ends, for the audit (see the package
//     documentation).
func (m *Machine) ConfirmDelivery(id, evidence string, now time.Time) error {
	c := m.cmds[id]
	var bad *Refusal
	switch {
	case strings.TrimSpace(evidence) == "":
		bad = refusal(ReasonBadEvidence, id, "delivery needs evidence")
	case len(evidence) > maxEvidenceBytes:
		bad = refusal(ReasonBadEvidence, id, "evidence is %d bytes, the limit is %d", len(evidence), maxEvidenceBytes)
	}
	switch {
	case c == nil:
		return refusal(ReasonNoCommand, id, "no command %q", safeField(id, 70))
	case c.takesLate():
		piece := pieceDigest("late-evidence", c.id, evidence)
		if bad != nil {
			// Unusable evidence is still late information: the hold comes
			// first, then the refusal.
			if err := m.holdIfDecided(c, piece, now); err != nil {
				return err
			}
			return bad
		}
		r := base(c, now)
		r.Reason = ReasonLateEvidence
		r.EvidenceKind, r.Evidence = EvidenceProviderReport, safeField(evidence, maxEvidenceBytes)
		return m.late(c, r, piece, now)
	case c.state != StateDispatching:
		return refusal(ReasonWrongState, id, "command %q is %s, not dispatching", id, c.state)
	case !c.hasEnter:
		return refusal(ReasonNoEnter, id, "Enter was not written for command %q", id)
	case bad != nil:
		return bad
	}
	r := base(c, now)
	r.From, r.To, r.Reason = StateDispatching, StateDelivered, ReasonConfirmed
	r.EvidenceKind, r.Evidence = EvidenceProviderReport, safeField(evidence, maxEvidenceBytes)
	return m.commit(r)
}

// Tick makes the changes that only time causes, and returns the records it
// wrote. For each target, in order of name:
//
//   - a command left accepted (its queued record failed) is queued;
//   - a dispatching command with no fence result (Enter not recorded) FenceTimeout
//     after its dispatching record becomes uncertain (no-fence-result, which is
//     not a reported death); the queue is then frozen and shows it. A fence
//     result or evidence that comes later is kept as an annotation and does not
//     change that (see RecordFence and ConfirmDelivery). Like ConfirmTimeout, it
//     is reached when the elapsed time equals it;
//   - a submitted command with no evidence ConfirmTimeout after its Enter becomes
//     uncertain (submitted, not confirmed);
//   - a queued head that is not behind an uncertain command, and is past
//     QueueTimeLimit since its admission, becomes refused (not idle). At most
//     one head per target is refused by a call, so a command behind it gets a
//     chance to be dispatched before the next tick.
//
// Tick never dispatches and never resends, and it never moves an uncertain
// command or ends a hold. A queue that is held keeps its queued head: the head
// is not tried, so it is not refused as not idle. It stops at the first journal
// error and returns it with the records written so far.
func (m *Machine) Tick(now time.Time) ([]Record, error) {
	var done []Record
	step := func(r Record) error {
		if err := m.commit(r); err != nil {
			return err
		}
		done = append(done, m.lastRecord(r))
		return nil
	}
	for _, key := range m.targetKeys() {
		for _, c := range append([]*command(nil), m.queues[key]...) {
			if c.state != StateAccepted {
				continue
			}
			if err := step(queuedRecord(c, now)); err != nil {
				return done, err
			}
		}
		q := m.queues[key]
		if len(q) == 0 {
			continue
		}
		head := q[0]
		switch {
		case head.state == StateDispatching && !head.hasEnter && now.Sub(head.updated) >= m.limits.FenceTimeout:
			// No fence result was recorded in time. Nobody saw the holder or the
			// agent die: the fence may still be running, or the result was
			// lost. Whatever was written is unknown.
			r := base(head, now)
			r.From, r.To, r.Reason = StateDispatching, StateUncertain, ReasonNoFenceResult
			r.Detail = fmt.Sprintf("no fence result %s after the dispatching record", now.Sub(head.updated).Round(time.Second))
			r.EvidenceKind, r.Evidence = EvidenceObservation, string(WrittenUnknown)
			if err := step(r); err != nil {
				return done, err
			}
		case head.state == StateDispatching && head.hasEnter && now.Sub(head.enterAt) >= m.limits.ConfirmTimeout:
			r := base(head, now)
			r.From, r.To, r.Reason = StateDispatching, StateUncertain, ReasonSubmittedNotConfirmed
			r.Detail = fmt.Sprintf("no evidence %s after Enter", now.Sub(head.enterAt).Round(time.Second))
			r.EvidenceKind, r.Evidence = EvidenceObservation, string(WrittenPasteAndEnter)
			if err := step(r); err != nil {
				return done, err
			}
		case head.state == StateQueued && frozenBy(q) == nil && m.holds[key] == nil && now.Sub(head.admitted) > m.limits.QueueTimeLimit:
			r := base(head, now)
			r.From, r.To, r.Reason = StateQueued, StateRefused, ReasonNotIdle
			r.Detail = safeField("queue time limit passed; last reason: "+string(head.reason)+": "+head.detail, maxDetailBytes)
			r.EvidenceKind, r.Evidence = EvidenceObservation, string(WrittenNothing)
			if err := step(r); err != nil {
				return done, err
			}
		}
	}
	return done, nil
}

// lastRecord is the record just committed; commit stamped its sequence number.
func (m *Machine) lastRecord(r Record) Record {
	r.Seq = m.seq
	return r
}

// hasNote reports whether c already has a late note equal to the one r says.
func (c *command) hasNote(r Record) bool {
	for _, n := range c.late {
		if n.Reason == r.Reason && n.Detail == r.Detail && n.EvidenceKind == r.EvidenceKind && n.Evidence == r.Evidence {
			return true
		}
	}
	return false
}

// late journals the late note r (its reason, detail and evidence are set) for
// c, which is uncertain or was resolved as sent again or dropped; piece is the
// digest of the call. First it records the information with holdIfDecided; then
// it skips a note that c already has, and refuses a different one when c holds
// the most it keeps. See ConfirmDelivery for the rules.
func (m *Machine) late(c *command, r Record, piece string, now time.Time) error {
	if err := m.holdIfDecided(c, piece, now); err != nil {
		return err
	}
	r.From, r.To = c.state, c.state
	if c.hasNote(r) {
		return nil
	}
	if len(c.late) >= maxLateNotes {
		return refusal(ReasonLateNotesFull, c.id, "command %q already has %d late notes", c.id, len(c.late))
	}
	return m.commit(r)
}
