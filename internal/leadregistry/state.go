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
		if e.Intent != nil && e.Intent.Outcome == OutcomePending {
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
		e.Shown = Shown{State: p.State, Reason: p.Reason, At: rec.at, Evidence: p.Evidence, Proof: p.Proof}
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
