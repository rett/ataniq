package ownerchannel

import (
	"context"
	"errors"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/record"
)

// Tick takes up whatever the journal and the door say is unfinished. [New]
// runs it once, which is the restart of owner-channel 6.3; a daemon may run
// it from time to time after that, since each step is safe to repeat.
//
//   - A journaled decision with no delivered entry is handed to the door,
//     byte for byte, once. The door answers the same for the same bytes, so
//     a decision that did reach it before the stop is not applied twice. A
//     decision the door refuses is closed with its code.
//   - An approval that was delivered and has no outcome is carried on from
//     what the door says: not used yet, it is used now; used, its outcome
//     is read from what the mirror shows (applied, not-applied, else
//     unknown) and the approval is never applied again.
//   - An outcome the journal holds as unknown, for an approval the door now
//     holds as applied or not-applied, is the owner's word that reached the
//     door before the service stopped (see [Service.Dispose]): the journal
//     takes it. Nothing is settled.
//   - A held decision delivers nothing.
//   - A project that has a card use today and has reached its limit gets
//     the day's limit alert, once, when a stop lost it between the decision
//     entry and the alert entry that follows it.
//   - A decision the door holds and the journal lacks, or holds with other
//     bytes than the journal's, did not come from the owner's console. That
//     approval is refused and an alert is journaled; the others go on. This
//     is looked at first, before any use is tried.
//   - A request that has a pending record and no count is counted.
//   - A held card past its expiry is closed, and a card that ended at the
//     door gets its reply and loses its pin.
//
// An approval whose outcome is unknown is left for the owner.
func (s *Service) Tick(ctx context.Context) error {
	ctx, done, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	s.dmu.Lock()
	defer s.dmu.Unlock()
	return s.reconcile(ctx)
}

// reconcile is Tick with dmu held. A failure to reach the door is
// returned wrapped in [ErrDoorUnavailable] after the other steps ran.
func (s *Service) reconcile(ctx context.Context) error {
	var doorErrs []error
	note := func(err error) error {
		if err == nil {
			return nil
		}
		if errors.Is(err, ErrDoorUnavailable) || ctx.Err() != nil {
			doorErrs = append(doorErrs, err)
			return nil
		}
		return err
	}

	// What the door holds comes first: a decision it holds that the journal
	// lacks blocks that approval before any use is tried.
	if err := note(s.checkDoor(ctx)); err != nil {
		return err
	}

	st := s.journal()
	undelivered, err := st.Undelivered()
	if err != nil {
		return s.jerr(ctx, err)
	}
	for _, d := range undelivered {
		if err := note(s.redeliver(ctx, d)); err != nil {
			return err
		}
	}

	open, err := st.OpenUses()
	if err != nil {
		return s.jerr(ctx, err)
	}
	for _, u := range open {
		_, p, perr := s.lookup(ctx, u.Decision().ID())
		if perr != nil {
			if err := note(wrapDoor(perr)); err != nil {
				return err
			}
			continue
		}
		if _, err := s.resume(ctx, u, p); note(err) != nil {
			return err
		}
	}

	if err := note(s.takeUpDisposals(ctx)); err != nil {
		return err
	}

	if _, err := st.ExpireHeld(s.now()); err != nil {
		return s.jerr(ctx, err)
	}
	if err := s.syncIndex(ctx); err != nil {
		return s.jerr(ctx, err)
	}
	if err := s.markReachedAgain(ctx); err != nil {
		return err
	}
	return errors.Join(doorErrs...)
}

// markReachedAgain writes the limit alert of today for each project that has
// a card use today and has no such alert. A stop between a decision entry
// and the alert entry that follows it (they are two commits) would lose the
// alert otherwise; markReached writes it once for the day.
func (s *Service) markReachedAgain(ctx context.Context) error {
	now := s.now()
	s.mu.Lock()
	projects, ids := s.idx.usedSince(startOfDay(now))
	s.mu.Unlock()
	for i, project := range projects {
		if err := s.markReached(ctx, project, ids[i], now); err != nil {
			return err
		}
	}
	return nil
}

// takeUpDisposals finishes what a [Service.Dispose] left half done: the
// owner's word reached the door (applied or not-applied) and the service
// stopped before the journal had it. It copies what the door holds into the
// journal and settles nothing. An outcome the door holds as unknown, or
// still as consumed, is left for the owner.
func (s *Service) takeUpDisposals(ctx context.Context) error {
	unknown, err := s.journal().UnknownOutcomes()
	if err != nil {
		return s.jerr(ctx, err)
	}
	var doorErrs []error
	for _, u := range unknown {
		shown, p, perr := s.lookup(ctx, u.Decision().ID())
		if perr != nil {
			doorErrs = append(doorErrs, wrapDoor(perr))
			continue
		}
		st := shown.Entry.State
		if st != record.StateApplied && st != record.StateNotApplied {
			continue
		}
		if _, err := s.journalOutcome(ctx, u, p, door.Outcome(st), false); err != nil {
			if errors.Is(err, ErrDoorUnavailable) || ctx.Err() != nil {
				doorErrs = append(doorErrs, err)
				continue
			}
			return err
		}
	}
	return errors.Join(doorErrs...)
}

// redeliver hands one journaled, undelivered decision to the door. An
// approve then goes on through its use, in the order of the journal.
func (s *Service) redeliver(ctx context.Context, d *ownerjournal.Decision) error {
	_, p, err := s.lookup(ctx, d.ID())
	if err != nil {
		if isVoid(err) {
			// The door does not know the card. Hand over anyway: the door
			// answers with its own code, and the decision is closed.
			p = record.Pending{}
		} else {
			return wrapDoor(err)
		}
	}
	res, err := s.handOver(ctx, d)
	if err != nil || res.refused != nil || res.delivery == nil {
		return err
	}
	if !d.Approve() {
		s.closeOut(ctx, p, record.StateDenied, "denied by the owner")
		return nil
	}
	_, err = s.resume(ctx, res.delivery, p)
	return err
}

// checkDoor reads the door's list once and does what depends on it: it finds
// decisions the journal lacks, counts requests that have no count, closes
// out cards that ended, and notes when each ended.
func (s *Service) checkDoor(ctx context.Context) error {
	entries, err := s.cfg.Door.List(ctx)
	if err != nil {
		return wrapDoor(err)
	}
	if err := s.syncIndex(ctx); err != nil {
		return s.jerr(ctx, err)
	}
	var decided []ownerjournal.DoorDecision
	pendings := make(map[string]record.Pending, len(entries))
	for _, e := range entries {
		p, err := s.pendingFor(ctx, e)
		if err != nil {
			return wrapDoor(err)
		}
		pendings[e.ID] = p
		if e.DecisionSHA256 != "" {
			decided = append(decided, ownerjournal.DoorDecision{Project: p.Binding.Project, ID: e.ID, SHA256: e.DecisionSHA256})
		}
	}
	lacking, err := s.journal().Unjournaled(decided)
	if err != nil {
		return s.jerr(ctx, err)
	}
	for _, d := range lacking {
		if err := s.block(ctx, d); err != nil {
			return err
		}
	}

	for _, e := range entries {
		p := pendings[e.ID]
		if err := s.countOne(ctx, p); err != nil {
			return err
		}
		switch e.State {
		case record.StateDenied, record.StateExpired, record.StateVoid, record.StateWithdrawn,
			record.StateApplied, record.StateNotApplied:
			s.noteEnded(e)
			s.closeOut(ctx, p, e.State, endLine(e.State))
		}
	}
	return nil
}

// block marks an approval whose decision the journal lacks, and journals an
// alert for it once.
func (s *Service) block(ctx context.Context, d ownerjournal.DoorDecision) error {
	s.mu.Lock()
	s.blocked[d.ID] = true
	known := s.idx.alerted[d.ID]
	s.mu.Unlock()
	if known {
		return nil
	}
	err := s.journal().AppendAlert(ownerjournal.Alert{
		Project: d.Project, At: s.now(), PendingID: d.ID,
		Note: "the door holds a decision for this approval that the journal does not: it came by no console, and the approval is refused",
	})
	if err != nil {
		return s.jerr(ctx, err)
	}
	s.mu.Lock()
	s.idx.alerted[d.ID] = true
	s.mu.Unlock()
	s.notice("an approval was decided at the door with no decision in the journal: " + d.ID)
	return nil
}

// countOne journals the count of a pending record that has none.
func (s *Service) countOne(ctx context.Context, p record.Pending) error {
	s.mu.Lock()
	_, counted := s.idx.counted[p.ID]
	s.mu.Unlock()
	if counted || p.Binding.Summary == nil {
		return nil
	}
	if err := s.countPending(ctx, p.Binding.Project, p.RequestID, p, true); err != nil {
		return err
	}
	return s.syncIndex(ctx)
}

// noteEnded remembers when a card was first seen in a final state, by the
// service's clock (see [Service.now]).
func (s *Service) noteEnded(e door.Entry) {
	at := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.ended[e.ID]; !ok {
		s.ended[e.ID] = at
	}
}

// pendingFor returns the pending record of a listed entry, from the cache
// when the cache holds the very bytes the entry names.
func (s *Service) pendingFor(ctx context.Context, e door.Entry) (record.Pending, error) {
	s.mu.Lock()
	c, ok := s.pending[e.ID]
	s.mu.Unlock()
	if ok && c.sha == e.PendingSHA256 {
		return c.pending, nil
	}
	_, p, err := s.lookup(ctx, e.ID)
	return p, err
}
