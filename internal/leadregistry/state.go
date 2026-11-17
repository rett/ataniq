package leadregistry

import (
	"math"

	"ataniq/internal/leadrole"
)

// reduce applies one validated record to the entry of its role and returns
// the new entry. It is the only place that changes state, for a live call and
// for replay alike, and it never mutates cur or anything it points to.
// all holds every role before the record; it is only read.
func reduce(cur Entry, exists bool, all map[leadrole.Role]Entry, rec record) (Entry, error) {
	if rec.kind == kindDefine {
		p := rec.payload.(*definePayload)
		if exists {
			cur = cur.clone()
			cur.Manager = p.Manager
			cur.Requested = p.Requested
			return cur, nil
		}
		if len(all) >= MaxRoles {
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
		if err := launchGate(e, all); err != nil {
			return Entry{}, err
		}
		if e.Role.Kind() == leadrole.KindCoordinator && p.ParentEpoch != all[e.Role.Parent()].Epoch {
			return Entry{}, ErrStaleEpoch
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
			ParentEpoch:   p.ParentEpoch,
			Identity:      p.Identity,
			BootIdentity:  p.BootID,
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

// launchGate applies the gate of BeginLaunch to the entry e, which is looked
// at with every role of the registry in all: no pending intent, run by
// ataniq, shown desired, stopped or ended, and an epoch that can grow. A
// coordinator also needs its parent lead defined, run by ataniq and shown
// starting, ready or degraded. A lead also needs every coordinator recorded
// under one of its earlier epochs stopped, ended or desired (never launched).
func launchGate(e Entry, all map[leadrole.Role]Entry) error {
	if e.pending() {
		return ErrLaunchPending
	}
	if e.Manager != ManagerAtaniq {
		return ErrLaunchManager
	}
	switch e.Shown.State {
	case StateDesired, StateStopped, StateEnded:
	default:
		return ErrLaunchState
	}
	if e.Epoch == math.MaxUint64 {
		return ErrEpochExhausted
	}
	switch e.Role.Kind() {
	case leadrole.KindCoordinator:
		parent, ok := all[e.Role.Parent()]
		switch {
		case !ok:
			return ErrLaunchNoParent
		case parent.Manager != ManagerAtaniq:
			return ErrLaunchParentManager
		}
		// A parent that was never launched (epoch 0) is not live, whatever it
		// is shown as: there is no epoch to record.
		if parent.Epoch == 0 {
			return ErrLaunchParentState
		}
		switch parent.Shown.State {
		case StateStarting, StateReady, StateDegraded:
		default:
			return ErrLaunchParentState
		}
	case leadrole.KindLead:
		for _, c := range all {
			if c.Role.Kind() != leadrole.KindCoordinator || c.Role.Parent() != e.Role || c.Intent == nil {
				continue
			}
			// A coordinator launches under the lead's current epoch, so every
			// launched one was recorded under an epoch before the next.
			if c.Intent.ParentEpoch > e.Epoch {
				continue
			}
			switch c.Shown.State {
			case StateStopped, StateEnded, StateDesired:
			default:
				return ErrLaunchCoordinator
			}
		}
	}
	return nil
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
	case StateStalled:
		if e.Role.Kind() != leadrole.KindCoordinator {
			return refuseTransition(from, p.State, "stalled is a state of coordinators only")
		}
		switch from {
		case StateStarting, StateReady, StateDegraded:
		default:
			return refuseTransition(from, p.State, "stalled is entered only from starting, ready or degraded")
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
