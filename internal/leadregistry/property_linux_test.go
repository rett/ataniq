package leadregistry

import (
	"math/rand"
	"strings"
	"testing"

	"ataniq/internal/leadrole"
)

func newRNG(seed int64) *rand.Rand { return rand.New(rand.NewSource(seed * 7919)) }

var allStates = []State{StateDesired, StateStarting, StateReady, StateDegraded, StateStopping, StateStopped, StateEnded, StateUnknown}

func pick[T any](rng *rand.Rand, xs ...T) T { return xs[rng.Intn(len(xs))] }

// randomOp calls one random operation. Parameters are right most of the time
// and wrong (stale epoch, wrong intent id, missing proof, bad text) some of
// the time, so valid and refused calls are both exercised.
func randomOp(t *testing.T, r *Registry, rng *rand.Rand, role leadrole.Role) error {
	t.Helper()
	cur, _ := r.Get(role)
	epoch := cur.Epoch
	if rng.Intn(6) == 0 {
		epoch = uint64(rng.Intn(5))
	}
	switch rng.Intn(15) {
	case 0:
		p := goodProfile()
		p.Model = pick(rng, "m1", "m2", "m3")
		if rng.Intn(8) == 0 {
			p.Model = ""
		}
		_, err := r.Define(role, pick(rng, ManagerAtaniq, ManagerAtaniq, ManagerTmux), p)
		return err
	case 1:
		_, err := r.SetDesired(role, pick(rng, DesiredRunning, DesiredStopped))
		return err
	case 2, 3:
		_, err := r.BeginLaunch(role)
		return err
	case 4, 5:
		id := ""
		if cur.Intent != nil {
			id = cur.Intent.ID
		}
		if rng.Intn(6) == 0 {
			id = strings.Repeat("a", 32)
		}
		ev := UnitEvidence{}
		if rng.Intn(2) == 0 {
			ev = UnitEvidence{InvocationID: "inv-" + string(rune('a'+rng.Intn(26))), ControlGroupPath: "/system.slice/u", ControlGroupID: "12", BootID: "boot"}
		}
		_, err := r.RecordOutcome(role, id, epoch, pick(rng, OutcomeLaunched, OutcomeAdopted, OutcomeFailed, OutcomeAbandoned), ev)
		return err
	case 6, 7, 8:
		c := StateChange{
			Epoch:    epoch,
			State:    pick(rng, allStates...),
			Evidence: pick(rng, EvidenceObservation, EvidenceProvider, EvidenceOwner),
			Reason:   Reason{Code: pick(rng, "a", "unit-gone", "x-1"), Detail: pick(rng, "", "detail", "café \"q\"")},
		}
		if rng.Intn(3) != 0 {
			c.Proof = pick(rng, "p1", "proof two")
		}
		_, err := r.SetState(role, c)
		return err
	case 9:
		_, err := r.ResolveUnknown(role, OwnerDecision{Epoch: epoch, State: pick(rng, StateStopped, StateEnded),
			Reason: Reason{Code: "owner-decision"}, Decision: pick(rng, "decision one", "decision two")})
		return err
	case 11, 12, 13, 14:
		return guidedOp(r, rng, role, cur)
	default:
		f := func() Field {
			if rng.Intn(2) == 0 {
				return Field{}
			}
			return Seen(pick(rng, "a", "b", "c"))
		}
		_, err := r.SetObserved(role, epoch, Observed{Provider: f(), Version: f(), Model: f(), Effort: f(), PermissionMode: f(), AccessMode: f()})
		return err
	}
}

// guidedOp makes the move a well-behaved caller would make next, so that the
// random walk gets past a launch into the live states and the stops: begin a
// launch, record its outcome, and then change the shown state or stop with a
// proof. A state it cannot move from is left to the other operations.
func guidedOp(r *Registry, rng *rand.Rand, role leadrole.Role, cur Entry) error {
	switch {
	case cur.pending():
		_, err := r.RecordOutcome(role, cur.Intent.ID, cur.Epoch, OutcomeLaunched, UnitEvidence{InvocationID: "inv"})
		return err
	case cur.Epoch == 0 || cur.Shown.State == StateDesired || cur.Shown.State == StateStopped || cur.Shown.State == StateEnded:
		if rng.Intn(3) == 0 && cur.Epoch > 0 {
			_, err := r.SetDesired(role, DesiredRunning)
			return err
		}
		if cur.Epoch > 0 && rng.Intn(5) == 0 { // a role at rest may be set desired
			_, err := r.SetState(role, setState(StateDesired, cur.Epoch))
			return err
		}
		_, err := r.BeginLaunch(role)
		return err
	case cur.Shown.State == StateUnknown:
		if rng.Intn(2) == 0 {
			_, err := r.ResolveUnknown(role, OwnerDecision{Epoch: cur.Epoch, State: StateStopped, Reason: Reason{Code: "owner-decision"}, Decision: "checked"})
			return err
		}
		_, err := r.SetState(role, stopProof(cur.Epoch))
		return err
	}
	st := pick(rng, StateReady, StateDegraded, StateStalled, StateStopping, StateUnknown, StateStopped, StateStopped, StateEnded)
	c := setState(st, cur.Epoch)
	if st == StateStopped || st == StateEnded {
		c = stopProof(cur.Epoch)
		c.State = st
	}
	_, err := r.SetState(role, c)
	return err
}
