package ownerchannel

import (
	"context"
	"errors"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

// Lines the service writes about an approval. They are host-written and
// hold nothing a lead or a surface brought.
const (
	lineApplied    = "applied: the ref was moved"
	lineNotApplied = "not applied: the ref had moved, and nothing changed"
	lineUnknown    = "outcome unknown: the owner has to look at the mirror and say what became of it"
	lineWaiting    = "approved, and waiting to be applied"
	lineClockBack  = "approved, not applied: the clock moved back, and uses are refused until the daemon restarts"
)

// resume carries a delivered approval as far as it can go, from what the
// door says its state is. It is the one path both a decision that was just
// delivered and a restart take, so that each step is taken once:
//
//   - approved at the door: the approval was not used. The service reads the
//     repository again, uses it at the door, journals the use, applies the
//     ref update, settles and journals the outcome.
//   - consumed: the door used it and the outcome is not settled. The mirror
//     says what became of it (applied, not-applied, else unknown); the
//     service never applies again.
//   - applied, not-applied or unknown: the door has its outcome and the
//     journal does not; the journal takes it.
//   - expired, void or withdrawn: the approval ended unused.
//
// The caller holds dmu.
func (s *Service) resume(ctx context.Context, dl *ownerjournal.Delivery, p record.Pending) (ownerwire.DecideResult, error) {
	shown, err := s.cfg.Door.Show(ctx, p.ID)
	if err != nil {
		return ownerwire.DecideResult{}, wrapDoor(err)
	}
	switch st := shown.Entry.State; st {
	case record.StateApproved, record.StateApprovedByRule, record.StateApprovedByGrant:
		return s.use(ctx, dl, p)
	case record.StateConsumed:
		if err := s.ensureUse(ctx, dl); err != nil {
			return ownerwire.DecideResult{}, err
		}
		state, err := s.mirrorOutcome(ctx, p)
		if err != nil {
			return ownerwire.DecideResult{}, err
		}
		return s.finish(ctx, dl, p, state)
	case record.StateApplied, record.StateNotApplied, record.StateUnknown:
		if err := s.ensureUse(ctx, dl); err != nil {
			return ownerwire.DecideResult{}, err
		}
		// The door has the outcome. An unknown one is not settled again:
		// only the owner moves a record out of it.
		return s.journalOutcome(ctx, dl, p, door.Outcome(st), false)
	case record.StateExpired, record.StateVoid, record.StateWithdrawn:
		if err := s.handles().AppendOutcome(dl, s.now(), st, endLine(st)); err != nil && !errors.Is(err, ownerjournal.ErrSettled) {
			return ownerwire.DecideResult{}, s.jerr(ctx, err)
		}
		s.closeOut(ctx, p, st, endLine(st))
		return ownerwire.DecideResult{State: st, Line: endLine(st)}, nil
	}
	return ownerwire.DecideResult{}, errors.New("ownerchannel: a delivered approval is in the state " + string(shown.Entry.State) + " at the door")
}

// endLine says how a card that ended unused ended.
func endLine(st record.State) string {
	switch st {
	case record.StateExpired:
		return "expired: the approval's time ran out before it was used"
	case record.StateVoid:
		return "void: something the approval was bound to changed"
	case record.StateWithdrawn:
		return "withdrawn by the lead"
	case record.StateDenied:
		return "denied by the owner"
	case record.StateApplied:
		return lineApplied
	case record.StateNotApplied:
		return lineNotApplied
	}
	return string(st)
}

// use takes an approval that the door holds as approved through its use and
// its application. The caller holds dmu.
func (s *Service) use(ctx context.Context, dl *ownerjournal.Delivery, p record.Pending) (ownerwire.DecideResult, error) {
	if s.isBlocked(p.ID) {
		return ownerwire.DecideResult{State: record.StateApproved, Line: "refused: the door holds a decision that the journal does not"}, nil
	}
	if s.clockMovedBack() {
		return ownerwire.DecideResult{State: record.StateApproved, Line: lineClockBack}, nil
	}
	fresh, err := s.cfg.Intake.Fresh(ctx, FreshInput{Project: p.Binding.Project, Pending: p})
	if err != nil {
		if ctx.Err() != nil {
			return ownerwire.DecideResult{}, ctx.Err()
		}
		// Nothing was used. The approval stays open and the next tick
		// tries again while it lasts.
		s.notice("the repository could not be read again for a use: " + line(err.Error()))
		return ownerwire.DecideResult{State: record.StateApproved, Line: lineWaiting}, nil
	}
	if err := s.fault("fresh-read"); err != nil {
		return ownerwire.DecideResult{}, err
	}
	used, err := s.cfg.Door.Use(ctx, door.UseInput{ID: p.ID, Fresh: fresh})
	if err != nil {
		e, ok := doorRefusal(err)
		if !ok {
			// Whether the door consumed the approval is not known. The
			// next look at the door's state says.
			return ownerwire.DecideResult{}, wrapDoor(err)
		}
		// A refusal is journaled once for each code: the tick tries the
		// use again while the approval lasts, and the journal does not
		// grow with each try.
		if serr := s.syncIndex(ctx); serr != nil {
			return ownerwire.DecideResult{}, s.jerr(ctx, serr)
		}
		if !s.useRefusedBefore(p.ID, e.Code) {
			if aerr := s.handles().AppendUse(dl, s.now(), e.Code); aerr != nil {
				return ownerwire.DecideResult{}, s.jerr(ctx, aerr)
			}
		}
		shown, serr := s.cfg.Door.Show(ctx, p.ID)
		if serr != nil {
			return ownerwire.DecideResult{}, wrapDoor(serr)
		}
		if st := shown.Entry.State; st == record.StateApproved || st == record.StateApprovedByRule || st == record.StateApprovedByGrant {
			return ownerwire.DecideResult{}, e
		}
		res, rerr := s.resume(ctx, dl, p)
		if rerr != nil {
			return ownerwire.DecideResult{}, rerr
		}
		return res, nil
	}
	// From here the approval is spent. The rest runs to its end whatever the
	// caller's context says.
	ctx = context.WithoutCancel(ctx)
	if err := s.fault("door-used"); err != nil {
		return ownerwire.DecideResult{}, err
	}
	if err := s.handles().AppendUse(dl, s.now(), ""); err != nil && !errors.Is(err, ownerjournal.ErrConsumed) {
		return ownerwire.DecideResult{}, s.jerr(ctx, err)
	}
	if err := s.fault("use-journaled"); err != nil {
		return ownerwire.DecideResult{}, err
	}
	state, err := s.cfg.Intake.Apply(ctx, ApplyInput{Project: p.Binding.Project, Effects: used.Effects})
	if err != nil || !state.Valid() {
		state = door.OutcomeUnknown
	}
	if err := s.fault("applied"); err != nil {
		return ownerwire.DecideResult{}, err
	}
	return s.finish(ctx, dl, p, state)
}

// ensureUse journals the door's consumption of an approval when the journal
// lacks it: the service may have stopped between the door's answer and the
// entry.
func (s *Service) ensureUse(ctx context.Context, dl *ownerjournal.Delivery) error {
	err := s.handles().AppendUse(dl, s.now(), "")
	if err != nil && !errors.Is(err, ownerjournal.ErrConsumed) {
		return s.jerr(ctx, err)
	}
	return nil
}

// mirrorOutcome asks the intake what the mirror shows for the approval's
// effects. A failure to tell is unknown.
func (s *Service) mirrorOutcome(ctx context.Context, p record.Pending) (door.Outcome, error) {
	ctx = context.WithoutCancel(ctx)
	out, err := s.cfg.Intake.Mirror(ctx, ApplyInput{Project: p.Binding.Project, Effects: p.Binding.Effects})
	if err != nil || !out.Valid() {
		return door.OutcomeUnknown, nil
	}
	return out, nil
}

// finish settles an applied, not-applied or unknown outcome at the door and
// journals it. The caller holds dmu.
func (s *Service) finish(ctx context.Context, dl *ownerjournal.Delivery, p record.Pending, state door.Outcome) (ownerwire.DecideResult, error) {
	return s.journalOutcome(ctx, dl, p, state, true)
}

// journalOutcome journals the outcome of an approval. With settle set it
// first tells the door; a failure to reach the door leaves the approval open
// and the next tick finishes it. An unknown outcome is settled once, when it
// is first found, and then waits for the owner.
func (s *Service) journalOutcome(ctx context.Context, dl *ownerjournal.Delivery, p record.Pending, state door.Outcome, settle bool) (ownerwire.DecideResult, error) {
	ctx = context.WithoutCancel(ctx)
	if settle {
		_, err := s.cfg.Door.Settle(ctx, door.SettleInput{ID: p.ID, BindingSHA256: p.BindingSHA256, Outcome: state})
		if err != nil {
			if _, ok := doorRefusal(err); !ok {
				return ownerwire.DecideResult{State: record.StateConsumed, Line: lineWaiting}, wrapDoor(err)
			}
			// The door answered with a refusal: it holds another state
			// than the one the mirror shows. Leave it for the owner.
			s.notice("the door refused to settle an approval: " + line(err.Error()))
			state = door.OutcomeUnknown
		}
	}
	if err := s.fault("settled"); err != nil {
		return ownerwire.DecideResult{}, err
	}
	lineOf := outcomeLine(state)
	if err := s.handles().AppendOutcome(dl, s.now(), state.State(), lineOf); err != nil && !errors.Is(err, ownerjournal.ErrSettled) {
		return ownerwire.DecideResult{}, s.jerr(ctx, err)
	}
	if err := s.fault("outcome-journaled"); err != nil {
		return ownerwire.DecideResult{}, err
	}
	if state != door.OutcomeUnknown {
		s.closeOut(ctx, p, state.State(), lineOf)
	}
	return ownerwire.DecideResult{State: state.State(), Line: lineOf}, nil
}

// outcomeLine is the host's line for an outcome.
func outcomeLine(o door.Outcome) string {
	switch o {
	case door.OutcomeApplied:
		return lineApplied
	case door.OutcomeNotApplied:
		return lineNotApplied
	}
	return lineUnknown
}

// closeOut answers the lead for a card that ended, and takes the merge's pin
// away. The reply comes first: a stop between the two leaves the pin, and
// the next tick answers again (the mailbox keeps the first) and removes it.
// A card with no pin was closed out already.
func (s *Service) closeOut(ctx context.Context, p record.Pending, state record.State, text string) {
	ctx = context.WithoutCancel(ctx)
	s.mu.Lock()
	done := p.ID == "" || s.closedOut[p.ID]
	s.mu.Unlock()
	if done {
		return
	}
	key := pinKey(p.RequestSHA256)
	at, err := s.cfg.Merger.Pinned(ctx, key)
	if err != nil {
		s.notice("the pin could not be read: " + line(err.Error()))
		return
	}
	if at != "" {
		if !s.reply(ctx, Reply{Project: p.Binding.Project, RequestID: p.RequestID, PendingID: p.ID, State: state, Line: text}) {
			return
		}
		if err := s.cfg.Merger.Unpin(ctx, key, at); err != nil {
			s.notice("the pin could not be removed: " + line(err.Error()))
			return
		}
	}
	s.mu.Lock()
	s.closedOut[p.ID] = true
	s.mu.Unlock()
}

// isBlocked reports whether the door holds a decision for the id that the
// journal lacks.
func (s *Service) isBlocked(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blocked[id]
}
