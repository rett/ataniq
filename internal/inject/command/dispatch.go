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
// refuses with a *Refusal when the queue is empty (ReasonQueueEmpty), frozen by
// an uncertain command (ReasonQueueFrozen, naming that command), when the head
// is already dispatching (ReasonAlreadyDispatching) or when the head is not
// yet queued (ReasonHeadNotQueued). Otherwise it appends the dispatching
// record, which is the journaled intent, and only then returns the text. On a
// journal error nothing changed and no text is returned: no byte may be
// written without the intent.
func (m *Machine) BeginDispatch(target Target, now time.Time) (Dispatch, error) {
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
// It refuses with ReasonWrongState unless the command is dispatching and Enter
// has not been recorded. On a journal error nothing changed.
func (m *Machine) RecordFence(id string, res FenceResult, now time.Time) (FenceUpdate, error) {
	c := m.cmds[id]
	switch {
	case c == nil:
		return FenceUpdate{}, refusal(ReasonNoCommand, id, "no command %q", safeField(id, 70))
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

// ConfirmDelivery records evidence that the message reached the agent, such as
// a position in the conversation file, and makes the command delivered. The
// evidence is recorded as a provider report. It is allowed only for a
// dispatching command whose Enter was written (ReasonWrongState, ReasonNoEnter
// otherwise), and the evidence must be non-empty and at most 512 bytes
// (ReasonBadEvidence). An uncertain command cannot be confirmed: only an
// operator leaves uncertain.
func (m *Machine) ConfirmDelivery(id, evidence string, now time.Time) error {
	c := m.cmds[id]
	switch {
	case c == nil:
		return refusal(ReasonNoCommand, id, "no command %q", safeField(id, 70))
	case c.state != StateDispatching:
		return refusal(ReasonWrongState, id, "command %q is %s, not dispatching", id, c.state)
	case !c.hasEnter:
		return refusal(ReasonNoEnter, id, "Enter was not written for command %q", id)
	case strings.TrimSpace(evidence) == "":
		return refusal(ReasonBadEvidence, id, "delivery needs evidence")
	case len(evidence) > maxEvidenceBytes:
		return refusal(ReasonBadEvidence, id, "evidence is %d bytes, the limit is %d", len(evidence), maxEvidenceBytes)
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
//   - a submitted command with no evidence ConfirmTimeout after its Enter becomes
//     uncertain (submitted, not confirmed);
//   - a queued head that is not behind an uncertain command, and is past
//     QueueTimeLimit since its admission, becomes refused (not idle). At most
//     one head per target is refused by a call, so a command behind it gets a
//     chance to be dispatched before the next tick.
//
// Tick never dispatches and never resends, and it never moves an uncertain
// command. It stops at the first journal error and returns it with the records
// written so far.
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
		case head.state == StateDispatching && head.hasEnter && now.Sub(head.enterAt) >= m.limits.ConfirmTimeout:
			r := base(head, now)
			r.From, r.To, r.Reason = StateDispatching, StateUncertain, ReasonSubmittedNotConfirmed
			r.Detail = fmt.Sprintf("no evidence %s after Enter", now.Sub(head.enterAt).Round(time.Second))
			r.EvidenceKind, r.Evidence = EvidenceObservation, string(WrittenPasteAndEnter)
			if err := step(r); err != nil {
				return done, err
			}
		case head.state == StateQueued && frozenBy(q) == nil && now.Sub(head.admitted) > m.limits.QueueTimeLimit:
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
