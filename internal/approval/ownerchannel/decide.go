package ownerchannel

import (
	"context"
	"errors"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/presence"
	"ataniq/internal/approval/record"
)

// Decide takes the owner's decision frame from a connection. The frame is
// checked, in this order, for: the decider (the caller's word in src), the
// frame's own form, presence (owner-away), the pending record's state, the
// binding digest, and, for an approve, that the sign-offs equal the
// flagged list. A decision that fails one of the checks the journal has a
// kind for (not-decider, owner-away, decision-invalid, binding-mismatch)
// is journaled as refused with its code and returned as a [*record.Error].
//
// An approve is then judged against the day's limit. Past it, the approve
// is journaled as held, is not handed to the door, and comes back as a
// pending card with the held mark. A deny is never held. Otherwise the
// service fills the rest of the decision record, journals it, hands the
// same bytes to the door, marks it delivered, and for an approve goes on:
// it reads the repository again, uses the approval at the door, applies the
// ref update, settles and journals the outcome. The result says where the
// card stands. An outcome that is unknown stays and waits for the owner
// ([Service.Dispose]).
//
// When the door refuses the decision and the record stays pending (the
// decision's own time ran out before it arrived, say), the card is
// undecided again: the result is the state pending, with a line that says
// the door refused it and gives the door's code, and a new decision for
// the card is accepted. [Service.Show] gives the same line until one is. A
// decision for a card that has one on its way to the door, journaled and
// not yet handed over, is refused with a line that says so; it does not
// hand the other over, which is the work of a restart and of the tick.
//
// An error that is not a refusal means the step stopped part way (the
// door out of reach, the journal reopened, the service stopped). What the
// journal holds is then taken up again by [Service.Tick] or at the next
// start, and nothing is applied twice.
func (s *Service) Decide(ctx context.Context, src Source, f ownerwire.DecisionFrame) (ownerwire.DecideResult, error) {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return ownerwire.DecideResult{}, err
	}
	defer done()
	if !presence.ValidSurface(src.Surface) || !presence.ValidSession(src.Session) {
		return ownerwire.DecideResult{}, refuse(record.CodeDecisionInvalid, "the surface or the session of the frame is not well formed")
	}
	s.dmu.Lock()
	defer s.dmu.Unlock()
	now := s.now()

	r := &refusal{s: s, src: src, id: f.ID, now: now}
	if !src.Decider {
		return ownerwire.DecideResult{}, r.journal(ctx, record.CodeNotDecider, "this connection is not the one that may decide")
	}
	if _, err := ownerwire.EncodeDecisionFrame(f); err != nil {
		return ownerwire.DecideResult{}, r.journal(ctx, record.CodeDecisionInvalid, detailOf(err))
	}

	shown, pending, perr := s.lookup(ctx, f.ID)
	switch {
	case perr == nil:
		r.project = pending.Binding.Project
	case isVoid(perr):
	default:
		return ownerwire.DecideResult{}, wrapDoor(perr)
	}

	// The action that carries the decision counts as the owner's activity
	// when he was present (see mayDecide).
	stamp, err := s.mayDecide(src, now, true)
	if err != nil {
		var away *presence.Away
		if errors.As(err, &away) {
			r.last = away.LastActivity
		}
		return ownerwire.DecideResult{}, r.journal(ctx, record.CodeOwnerAway, "no activity of the owner inside the presence window")
	}
	r.last = stamp.LastActivity

	if perr != nil {
		return ownerwire.DecideResult{}, r.journal(ctx, record.CodeDecisionInvalid, "no pending record has this id")
	}
	if shown.Entry.State != record.StatePending {
		return ownerwire.DecideResult{}, endedRefusal(shown.Entry)
	}
	if f.BindingSHA256 != pending.BindingSHA256 {
		return ownerwire.DecideResult{}, r.journal(ctx, record.CodeBindingMismatch, "the digest is not the digest of the binding the record has")
	}
	approve := f.Decision == record.DecisionApprove
	if approve {
		var flagged []record.Flag
		if sum := pending.Binding.Summary; sum != nil {
			flagged = sum.Flagged
		}
		if !record.SignOffsEqual(flagged, f.SignedOff) {
			return ownerwire.DecideResult{}, r.journal(ctx, record.CodeDecisionInvalid, "the sign-offs are not the record's flagged list")
		}
	}

	rec := record.Decision{
		Schema:        record.SchemaDecision,
		ID:            pending.ID,
		Planner:       pending.Planner,
		Decision:      f.Decision,
		BindingSHA256: pending.BindingSHA256,
		Project:       pending.Binding.Project,
		Subject: record.Subject{
			Kind:     pending.Binding.Subject.Kind,
			RoleName: pending.Binding.Subject.RoleName,
			Epoch:    pending.Binding.Subject.Epoch,
			SeatRev:  pending.Binding.Subject.SeatRev,
		},
		PlanToken: pending.Binding.PlanToken,
		SignedOff: append([]record.SignOff{}, f.SignedOff...),
		DecidedAt: record.FormatTime(now),
		ExpiresAt: record.FormatTime(now.Add(s.life)),
		Confirmed: record.ConfirmedCode,
		Reason:    f.Reason,
		Console: record.Console{
			PID: src.PID, TTY: src.TTY, Client: src.Client, Decider: true,
			Surface: src.Surface, Session: src.Session,
		},
	}
	if approve {
		rec.Uses = 1
	}
	b, err := record.EncodeDecision(rec)
	if err != nil {
		// A frame that cannot become a record (the flagged list is too
		// long for the record's cap, say) is refused with the code the
		// record gives.
		c, _ := code(err)
		if jerr := r.journal(ctx, record.CodeDecisionInvalid, detailOf(err)); jerr != nil && !isRefusal(jerr) {
			return ownerwire.DecideResult{}, jerr
		}
		return ownerwire.DecideResult{}, refuse(c, detailOf(err))
	}
	seen := ownerjournal.Seen{LastActivity: stamp.LastActivity}

	if approve {
		held, uses, limit, err := s.judge(pending.Binding.Project, now)
		if err != nil {
			return ownerwire.DecideResult{}, s.jerr(ctx, err)
		}
		if held {
			return s.hold(ctx, b, shown.Pending.Bytes, pending, seen, uses, limit, now)
		}
	}

	d, err := s.journal().AppendDecision(b, seen)
	switch {
	case errors.Is(err, ownerjournal.ErrDecided):
		// The card has a decision that stands: one the door took, or one
		// that is journaled and has not reached the door. This one is
		// refused. It does not hand the other over: that is the work of a
		// restart and of the tick.
		return ownerwire.DecideResult{}, s.decidedRefusal(ctx, pending.ID)
	case errors.Is(err, ownerjournal.ErrExpired):
		return ownerwire.DecideResult{}, refuse(record.CodeApprovalExpired, "the card expired while it was held")
	case err != nil:
		return ownerwire.DecideResult{}, s.jerr(ctx, err)
	}
	// The day's alert is written before the point where the service may
	// stop, so that a stop does not lose it.
	if approve {
		if err := s.markReached(ctx, pending.Binding.Project, pending.ID, now); err != nil {
			return ownerwire.DecideResult{}, err
		}
	}
	if err := s.fault("decision-journaled"); err != nil {
		return ownerwire.DecideResult{}, err
	}
	return s.deliver(ctx, d, pending)
}

// decidedRefusal is the refusal of a decision for a card that has a decision
// already, in plain words about what the earlier one is: an approve or a deny
// that is journaled and on its way to the door gets that said.
func (s *Service) decidedRefusal(ctx context.Context, id string) error {
	if err := s.syncIndex(ctx); err == nil {
		if r, ok := s.indexed(id); ok && r.decided && !r.delivered {
			if r.approve {
				return refuse(record.CodeDecisionInvalid, "an approve for this card is already on its way to the door")
			}
			return refuse(record.CodeDecisionInvalid, "a deny for this card is already on its way to the door")
		}
	}
	return refuse(record.CodeDecisionInvalid, "the card has a decision already")
}

// lineRefusedByDoor is the host's line for a card whose last decision the
// door refused while the record stayed pending. The code is the door's.
func lineRefusedByDoor(c record.Code) string {
	return "the earlier decision was refused by the door (" + string(c) + "): the card is pending, and can be decided again"
}

// refusal journals a refused decision.
type refusal struct {
	s       *Service
	src     Source
	id      string
	project string
	last    time.Time
	now     time.Time
}

// journal writes the refused entry and returns the refusal as an error. A
// failure of the journal itself is returned instead, since a refusal that
// was not journaled must not be told as one.
func (r *refusal) journal(ctx context.Context, c record.Code, detail string) error {
	e := ownerjournal.Refused{
		At: r.now, Code: c, Surface: r.src.Surface, Session: r.src.Session, LastActivity: r.last,
	}
	if record.ValidPendingID(r.id) {
		e.PendingID = r.id
		e.Project = r.project
	}
	if err := r.s.journal().AppendRefused(e); err != nil {
		return r.s.jerr(ctx, err)
	}
	return refuse(c, detail)
}

func isRefusal(err error) bool {
	_, ok := doorRefusal(err)
	return ok
}

func isVoid(err error) bool {
	e, ok := doorRefusal(err)
	return ok && e.Code == record.CodeApprovalVoid
}

// endedRefusal names why a card that is not pending cannot be decided.
func endedRefusal(e door.Entry) error {
	switch e.State {
	case record.StateExpired:
		return refuse(record.CodeApprovalExpired, "the card's time ran out")
	case record.StateConsumed, record.StateApplied, record.StateNotApplied, record.StateUnknown:
		return refuse(record.CodeApprovalConsumed, "the approval was used already")
	case record.StateApproved, record.StateApprovedByRule, record.StateApprovedByGrant, record.StateDenied:
		return refuse(record.CodeDecisionInvalid, "the card was decided already")
	case record.StateVoid:
		if e.ReasonCode == record.CodePreviousEpoch {
			return refuse(record.CodePreviousEpoch, "the card is void: the seat's epoch ended")
		}
	}
	return refuse(record.CodeApprovalVoid, "the card has ended: it is "+string(e.State))
}

// lookup reads a pending record from the door, through the cache.
func (s *Service) lookup(ctx context.Context, id string) (door.Shown, record.Pending, error) {
	if !record.ValidPendingID(id) {
		return door.Shown{}, record.Pending{}, refuse(record.CodeApprovalVoid, "no pending record has this id")
	}
	shown, err := s.cfg.Door.Show(ctx, id)
	if err != nil {
		return door.Shown{}, record.Pending{}, err
	}
	p, err := s.pendingOf(shown)
	return shown, p, err
}

// pendingOf decodes the pending record of a shown entry, keeping each
// record that was decoded once.
func (s *Service) pendingOf(shown door.Shown) (record.Pending, error) {
	s.mu.Lock()
	c, ok := s.pending[shown.Entry.ID]
	s.mu.Unlock()
	if ok && c.sha == shown.Pending.SHA256 {
		return c.pending, nil
	}
	return s.remember(shown.Pending)
}

// judge reads the day's card uses and the project's limit and says whether
// an approve now is held.
func (s *Service) judge(project string, now time.Time) (held bool, uses, limit int, err error) {
	limit, err = s.limitOf(project)
	if err != nil {
		return false, 0, 0, err
	}
	times, err := s.journal().CardUses(project, startOfDay(now))
	if err != nil {
		return false, 0, 0, err
	}
	uses = limits.UsesToday(times, now)
	return limits.Judge(true, uses, limit) == limits.Held, uses, limit, nil
}

// limitOf returns the limit in force for a project: the journal's, or the
// default when it was never changed.
func (s *Service) limitOf(project string) (int, error) {
	return limitIn(s.journal(), project)
}

// limitIn is limitOf for the store st.
func limitIn(st Journal, project string) (int, error) {
	l, err := st.Limit(project)
	if err != nil {
		return 0, err
	}
	if !l.Set {
		return limits.Default, nil
	}
	return l.Limit, nil
}

// startOfDay is the start of now's calendar day in its own location.
func startOfDay(now time.Time) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
}

// hold journals an approve past the limit and answers that the card is
// pending again.
func (s *Service) hold(ctx context.Context, decision, pendingBytes []byte, p record.Pending, seen ownerjournal.Seen, uses, limit int, now time.Time) (ownerwire.DecideResult, error) {
	if _, err := s.journal().AppendHeld(decision, pendingBytes, seen); err != nil {
		switch {
		case errors.Is(err, ownerjournal.ErrExpired):
			return ownerwire.DecideResult{}, refuse(record.CodeApprovalExpired, "the card expired while it was held")
		case errors.Is(err, ownerjournal.ErrDecided):
			return ownerwire.DecideResult{}, s.decidedRefusal(ctx, p.ID)
		}
		return ownerwire.DecideResult{}, s.jerr(ctx, err)
	}
	if err := s.markReached(ctx, p.Binding.Project, p.ID, now); err != nil {
		return ownerwire.DecideResult{}, err
	}
	if err := s.fault("held-journaled"); err != nil {
		return ownerwire.DecideResult{}, err
	}
	return ownerwire.DecideResult{State: record.StatePending, Line: limits.HeldReason(uses, limit), Held: true}, nil
}

// markReached raises the alert of a day that reached its limit, once for
// the day, when the day's uses are at or above the limit.
func (s *Service) markReached(ctx context.Context, project, id string, now time.Time) error {
	held, _, _, err := s.judge(project, now)
	if err != nil {
		return s.jerr(ctx, err)
	}
	if !held {
		return nil
	}
	days, err := s.journal().Days(project, now)
	if err != nil {
		return s.jerr(ctx, err)
	}
	if n := len(days); n > 0 && days[n-1].ReachedLimit {
		return nil
	}
	err = s.journal().AppendAlert(ownerjournal.Alert{
		Project: project, At: now, PendingID: id, LimitReached: true,
		Note: "the project reached its daily card limit",
	})
	if err != nil {
		return s.jerr(ctx, err)
	}
	return nil
}

// deliver hands a journaled decision to the door and marks it delivered. For
// an approve it goes on to use and apply. The caller holds dmu.
func (s *Service) deliver(ctx context.Context, d *ownerjournal.Decision, p record.Pending) (ownerwire.DecideResult, error) {
	res, err := s.handOver(ctx, d)
	if err != nil {
		return ownerwire.DecideResult{}, err
	}
	if res.refused != nil {
		// The door refused the decision. When the record is still pending
		// the card is undecided again: it comes back as pending, with the
		// door's code, and may be decided again.
		if shown, serr := s.cfg.Door.Show(ctx, p.ID); serr == nil && shown.Entry.State == record.StatePending {
			return ownerwire.DecideResult{State: record.StatePending, Line: lineRefusedByDoor(res.refused.Code)}, nil
		}
		return ownerwire.DecideResult{}, res.refused
	}
	if !d.Approve() {
		s.closeOut(ctx, p, record.StateDenied, "denied by the owner")
		return ownerwire.DecideResult{State: record.StateDenied, Line: "denied by the owner"}, nil
	}
	return s.resume(ctx, res.delivery, p)
}

// handedOver is the result of handing a decision to the door.
type handedOver struct {
	delivery *ownerjournal.Delivery
	// refused is set when the door refused the decision; the journal has it
	// closed.
	refused *record.Error
}

// handOver hands the journaled bytes of a decision to the door and journals
// the answer. A failure to reach the door leaves the decision undelivered,
// to be handed over again.
func (s *Service) handOver(ctx context.Context, d *ownerjournal.Decision) (handedOver, error) {
	_, err := s.cfg.Door.Decide(ctx, door.RecordOf(d.Bytes()))
	if err != nil {
		e, ok := doorRefusal(err)
		if !ok {
			return handedOver{}, wrapDoor(err)
		}
		if merr := s.handles().MarkRefusedByDoor(d, s.now(), e.Code); merr != nil {
			return handedOver{}, s.jerr(ctx, merr)
		}
		return handedOver{refused: e}, nil
	}
	if err := s.fault("door-decided"); err != nil {
		return handedOver{}, err
	}
	dl, err := s.handles().MarkDelivered(d, s.now())
	if err != nil {
		return handedOver{}, s.jerr(ctx, err)
	}
	if err := s.fault("delivered"); err != nil {
		return handedOver{}, err
	}
	return handedOver{delivery: dl}, nil
}
