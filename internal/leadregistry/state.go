package leadregistry

import (
	"math"
)

// reduce applies one validated record to the entry of its role and returns
// the new entry. It is the only place that changes state, for a live call and
// for replay alike, and it never mutates cur or anything it points to.
// nroles is the number of roles before the record.
func reduce(cur Entry, exists bool, nroles int, rec record) (Entry, error) {
	if rec.kind == kindDefine {
		p := rec.payload.(*definePayload)
		if exists {
			cur = cur.clone()
			cur.Manager = p.Manager
			cur.Requested = p.Requested
			return cur, nil
		}
		if nroles >= MaxRoles {
			return Entry{}, ErrTooManyRoles
		}
		return Entry{
			Role:      rec.role,
			Manager:   p.Manager,
			Requested: p.Requested,
			Desired:   DesiredStopped,
			Shown: Shown{
				State:    StateStopped,
				Reason:   Reason{Code: "defined"},
				At:       rec.at,
				Evidence: EvidenceOwner,
			},
		}, nil
	}
	if !exists {
		return Entry{}, ErrNoRole
	}
	e := cur.clone()
	switch p := rec.payload.(type) {
	case *desiredPayload:
		e.Desired = p.Desired
	case *beginPayload:
		if e.pending() {
			return Entry{}, ErrLaunchPending
		}
		if e.Manager != ManagerAtaniq {
			return Entry{}, ErrLaunchManager
		}
		switch e.Shown.State {
		case StateDesired, StateStopped, StateEnded:
		default:
			return Entry{}, ErrLaunchState
		}
		if e.Epoch == math.MaxUint64 {
			return Entry{}, ErrEpochExhausted
		}
		if p.Epoch != e.Epoch+1 {
			return Entry{}, ErrStaleEpoch
		}
		if p.UnitName != e.Role.UnitName() {
			return Entry{}, invalid("launch unit name is not the role's unit")
		}
		if p.ProfileDigest != e.Requested.Digest() {
			return Entry{}, invalid("launch profile digest is not the requested profile's")
		}
		e.Epoch = p.Epoch
		e.Intent = &Intent{
			ID:            p.IntentID,
			Epoch:         p.Epoch,
			UnitName:      p.UnitName,
			ProfileDigest: p.ProfileDigest,
			At:            rec.at,
			Outcome:       OutcomePending,
		}
		e.Unit = UnitEvidence{}
		e.Observed = Observed{}
		e.Shown = Shown{
			State:    StateStarting,
			Reason:   Reason{Code: "launch-intent"},
			At:       rec.at,
			Evidence: EvidenceObservation,
		}
	case *outcomePayload:
		if p.Epoch != e.Epoch {
			return Entry{}, ErrStaleEpoch
		}
		if e.Intent == nil || e.Intent.ID != p.IntentID {
			return Entry{}, ErrIntentMismatch
		}
		if e.Intent.Outcome != OutcomePending {
			return Entry{}, ErrIntentResolved
		}
		e.Intent.Outcome = p.Outcome
		e.Intent.OutcomeAt = rec.at
		e.Unit = p.Unit
	case *statePayload:
		if p.Epoch != e.Epoch {
			return Entry{}, ErrStaleEpoch
		}
		if err := checkTransition(e, p); err != nil {
			return Entry{}, err
		}
		e.Shown = Shown{State: p.State, Reason: p.Reason, At: rec.at, Evidence: p.Evidence, Proof: p.Proof}
	case *decisionPayload:
		if p.Epoch != e.Epoch {
			return Entry{}, ErrStaleEpoch
		}
		if e.Shown.State != StateUnknown {
			return Entry{}, refuseTransition(e.Shown.State, p.State, "an owner decision resolves unknown only")
		}
		if e.pending() {
			return Entry{}, ErrOutcomePending
		}
		e.Shown = Shown{State: p.State, Reason: p.Reason, At: rec.at, Evidence: EvidenceOwner, Proof: p.Decision}
	case *observedPayload:
		if p.Epoch != e.Epoch {
			return Entry{}, ErrStaleEpoch
		}
		e.Observed = e.Observed.merge(p.Observed)
	default:
		return Entry{}, invalid("record kind %q", rec.kind)
	}
	return e, nil
}

// checkTransition applies the transition table of the package documentation
// to a state record. The epoch has been checked already.
func checkTransition(e Entry, p *statePayload) error {
	from := e.Shown.State
	switch p.State {
	case StateDesired:
		switch from {
		case StateDesired, StateStopped, StateEnded:
		default:
			return refuseTransition(from, p.State, "desired is set only from desired, stopped or ended")
		}
	case StateReady, StateDegraded, StateStopping:
		if from == StateUnknown {
			return refuseTransition(from, p.State, "unknown is left only for stopped or ended, with proof")
		}
	case StateStopped, StateEnded:
		if e.pending() {
			return ErrOutcomePending
		}
		if from == StateUnknown && p.Evidence == EvidenceOwner {
			return refuseTransition(from, p.State, "an owner decision resolves unknown through ResolveUnknown")
		}
	}
	return nil
}
