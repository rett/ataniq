package assignment

import (
	"math"
)

// reduce takes a private copy of the transfer. The caller installs both
// projections together only after checking all evidence and the event.
func reduce(r Row, t Transfer, c Command, digest string, prior Evidence) (Row, Transfer, error) {
	old := r.Head
	if old != c.Expected {
		return r, t, ErrStale
	}
	if old.Phase == PhaseQuarantined {
		return r, t, ErrPhase
	}
	if old.Revision == math.MaxUint64 {
		return r, t, ErrOverflow
	}
	if c.RequestID == "" || c.TransferID == "" || c.Actor == "" || c.Authorization == "" ||
		old.Key.Home == "" || old.Key.Project == "" || old.Key.Role == "" {
		return r, t, ErrInvalid
	}
	if c.Kind != HoldStarted && (c.HoldStart != 0 || c.EarliestRetirement != 0) {
		return r, t, ErrInvalid
	}
	switch c.Kind {
	case Seed, Freeze, RollbackFreeze:
		if !c.Candidate.valid() {
			return r, t, ErrInvalid
		}
	case Reserve, Commit:
		if c.Candidate != t.Candidate {
			return r, t, ErrConflict
		}
	default:
		if c.Candidate != (Binding{}) {
			return r, t, ErrInvalid
		}
	}

	scope := old
	var targetID string
	switch c.Kind {
	case FenceReceipt:
		scope, targetID = t.Reservation, t.ReservationEventID
	case ConfirmReceipt:
		scope, targetID = t.Committed, t.CommittedEventID
	}
	if prior.Kind != "" {
		if c.TransferID != prior.TransferID || old.Key != prior.Scope.Key {
			return r, t, ErrEvidence
		}
		scope, targetID = prior.Scope, prior.ScopeEventID
	}
	if (c.Kind == FenceReceipt || c.Kind == ConfirmReceipt) && !validDigest(targetID) {
		return r, t, ErrEvidence
	}
	p, err := proofs(c, scope, targetID)
	if err != nil {
		return r, t, err
	}
	r.Head.Revision++
	if c.Kind == HoldReviewed {
		if t.ID != c.TransferID {
			return r, t, ErrStale
		}
		if t.Hold.Preservation == (Evidence{}) || t.Hold.Review != (Evidence{}) {
			return r, t, ErrPhase
		}
		review, err := p.take("fallback", t.Hold.Preservation.Digest, "reviewed")
		if err != nil || len(p) != 0 {
			return r, t, ErrEvidence
		}
		t.Hold.Review = review
		return r, t, nil
	}
	begin := c.Kind == Seed || c.Kind == Freeze || c.Kind == RollbackFreeze
	if begin {
		required := t.Routes
		t = Transfer{ID: c.TransferID, Original: old, Predecessor: r.Holder,
			Candidate: c.Candidate, BeginDigest: digest, Authorization: c.Authorization, Disposition: "pending"}
		r.LastTransferID = t.ID
		r.Head.TransferID = t.ID
		switch c.Kind {
		case Seed:
			if old.Revision != 0 {
				return r, t, ErrPhase
			}
			if _, err := p.take("enrollment", c.Candidate.ID, "verified"); err != nil {
				return r, t, err
			}
			r.Holder = c.Candidate
			r.Head.Generation, r.Head.BindingID = 1, r.Holder.ID
			r.Head.Phase = PhaseCommittedClosed
			t.Committed = r.Head
		case Freeze:
			if !old.Phase.open() && old.Phase != PhaseAbortedClosed {
				return r, t, ErrPhase
			}
			r.Head.Phase = PhaseFrozen
		case RollbackFreeze:
			if (!old.Phase.open() && old.Phase != PhaseCommittedClosed) || c.Candidate != r.Previous {
				return r, t, ErrPhase
			}
			r.Head.Phase = PhaseFrozen
		}
		if c.Kind != Seed && c.Candidate.ID == r.Holder.ID {
			return r, t, ErrInvalid
		}
		if c.Kind != Seed {
			if _, err := p.take("barrier", old.BindingID, "captured"); err != nil {
				return r, t, err
			}
		}
		err = t.capture(&p, required)
		if c.Kind == Seed && len(t.Outstanding) != 0 {
			return r, t, ErrEvidence
		}
	} else if prior.Kind != "" {
		if err = correction(&p, prior); err == nil {
			r.Head.Phase, r.Head.TransferID, t.Disposition = PhaseQuarantined, t.ID, "quarantined"
		}
	} else {
		if c.TransferID != t.ID || c.TransferID != r.LastTransferID {
			return r, t, ErrStale
		}
		err = advance(&r, &t, c, &p)
	}
	if err != nil {
		return r, t, err
	}
	if len(p) != 0 {
		return r, t, ErrEvidence
	}
	if begin || r.Head.Phase != old.Phase {
		t.PhaseRevision = r.Head.Revision
	}
	t.Phase = r.Head.Phase
	return r, t, nil
}

func advance(r *Row, t *Transfer, c Command, p *evidenceSet) error {
	h := &r.Head
	switch c.Kind {
	case Inventory:
		if !h.Phase.precommit() {
			return ErrPhase
		}
		if t.PreviewMismatch {
			if _, err := p.take("preview", t.Candidate.ID, "stopped"); err != nil {
				return err
			}
		}
		// A changed snapshot invalidates every dependent comparison and seal.
		t.Belief, t.Decision, t.Reservation, t.Fences = Evidence{}, Evidence{}, Head{}, nil
		t.ReservationEventID = ""
		t.PreviewMismatch = false
		t.Disposition = "pending"
		subject := h.Key.Project
		if e, err := p.take("inventory", subject, "uncertain"); err == nil {
			t.Inventory, t.Note = e, Evidence{}
			h.Phase, t.Disposition = PhaseFrozen, "uncertain"
			return nil
		}
		var err error
		t.Inventory, err = p.take("inventory", subject, "complete")
		if err != nil {
			return err
		}
		t.Note, err = p.take("handoff", subject, "verified")
		if err != nil {
			return err
		}
		acks := subjects(p.group("route", "frozen"))
		if !covers(acks, t.Routes) {
			return ErrEvidence
		}
		t.Routes = acks
		if err := t.inventoryEffects(p); err != nil {
			return err
		}
		h.Phase = PhaseInventoried
	case BeliefAccepted:
		if h.Phase != PhaseInventoried || t.PreviewMismatch {
			return ErrPhase
		}
		if e, err := p.take("belief", t.Inventory.Digest, "mismatch"); err == nil {
			t.Belief, t.PreviewMismatch, t.Disposition = e, true, "rejected"
			return nil
		}
		e, err := p.take("belief", t.Inventory.Digest, "matched")
		if err != nil {
			return err
		}
		t.Belief = e
		h.Phase = PhaseBeliefVerified
	case Reserve:
		if h.Phase != PhaseBeliefVerified {
			return ErrPhase
		}
		e, err := p.take("decision", t.Candidate.ID, "authorized")
		if err != nil {
			return err
		}
		t.Decision, t.Authorization = e, c.Authorization
		h.Phase = PhaseReserved
		t.Reservation = *h
	case FenceReceipt:
		if h.Phase != PhaseReserved && h.Phase != PhasePartiallyFenced {
			return ErrPhase
		}
		var err error
		t.Fences, err = receipt(p, t.Routes, t.Fences, "sealed-and-drained")
		if err != nil {
			return err
		}
		if err := t.settle(p, t.Fences[len(t.Fences)-1].Subject); err != nil {
			return err
		}
		h.Phase = PhasePartiallyFenced
	case Fenced:
		if h.Phase != PhasePartiallyFenced {
			return ErrPhase
		}
		if !covers(subjects(t.Fences), t.Routes) || len(t.Outstanding) != 0 {
			return ErrEvidence
		}
		if _, err := p.take("revalidation", t.Inventory.Digest, "current"); err != nil {
			return err
		}
		h.Phase = PhaseFenced
	case Commit:
		if h.Phase != PhaseFenced {
			return ErrPhase
		}
		if h.Generation == math.MaxUint64 {
			return ErrOverflow
		}
		if c.Authorization != t.Authorization {
			return ErrEvidence
		}
		if _, err := p.take("revalidation", t.Decision.Digest, "current"); err != nil {
			return err
		}
		r.Previous, r.Holder = r.Holder, t.Candidate
		h.BindingID = r.Holder.ID
		h.Generation++
		h.Phase = PhaseCommittedClosed
		t.Committed, t.Disposition = *h, "committed"
	case ConfirmReceipt:
		if h.Phase != PhaseCommittedClosed {
			return ErrPhase
		}
		var err error
		t.Confirmations, err = receipt(p, t.Routes, t.Confirmations, "installed-stale-refused")
		return err
	case Open:
		if h.Phase != PhaseCommittedClosed {
			return ErrPhase
		}
		if t.ReservationEventID != "" && !covers(subjects(t.Fences), t.Routes) {
			return ErrEvidence
		}
		if !covers(subjects(t.Confirmations), t.Routes) || len(t.Outstanding) != 0 ||
			c.Authorization != t.Authorization {
			return ErrEvidence
		}
		if _, err := p.take("enrollment", r.Holder.ID, "current"); err != nil {
			return err
		}
		h.Phase, h.TransferID, t.Disposition = PhaseConfirmed, "", "confirmed"
	case AbortPrepared:
		if !h.Phase.precommit() {
			return ErrPhase
		}
		if t.PreviewMismatch {
			if _, err := p.take("preview", t.Candidate.ID, "stopped"); err != nil {
				return err
			}
		}
		if _, err := p.take("candidate", t.Candidate.ID, "never-executed"); err != nil {
			return err
		}
		h.Phase, t.Disposition = PhaseAborting, "pending"
	case Aborted, AbortedClosed:
		if h.Phase != PhaseAborting {
			return ErrPhase
		}
		if err := t.settle(p, ""); err != nil {
			return err
		}
		if c.Kind == Aborted {
			if !t.Original.Phase.open() {
				return ErrPhase
			}
			if !coversExact(subjects(p.group("route", "safe-to-reopen")), t.Routes) {
				return ErrEvidence
			}
			h.Phase, t.Disposition = PhaseAborted, "aborted"
		} else {
			if _, err := p.take("incumbent", r.Holder.ID, "irreversibly-fenced"); err != nil {
				return err
			}
			h.Phase, t.Disposition = PhaseAbortedClosed, "aborted-closed"
		}
		h.TransferID = ""
	case HoldStarted:
		if h.Phase != PhaseConfirmed || t.Predecessor.Kind != "legacy" || t.Hold.Preservation != (Evidence{}) {
			return ErrPhase
		}
		const fortnight = 14 * 24 * 60 * 60
		if c.EarliestRetirement < c.HoldStart || c.EarliestRetirement-c.HoldStart < fortnight {
			return ErrEvidence
		}
		e, err := p.take("fallback", t.Predecessor.ID, "stopped-disabled-preserved")
		if err != nil {
			return err
		}
		t.Hold = Hold{Start: c.HoldStart, EarliestRetirement: c.EarliestRetirement, Preservation: e}
	default:
		return ErrInvalid
	}
	return nil
}

func coversExact(names, required []string) bool {
	return len(names) == len(required) && covers(names, required)
}
