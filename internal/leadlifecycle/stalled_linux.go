package leadlifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// Reason codes of the stalled state and of a failed clearing.
const (
	codeStalled         = "stalled"
	codeStalledNotOurs  = "stalled-instance-changed"
	stalledWasPrefix    = "was "
	stalledReasonJoiner = "; "
)

// ErrNotStalled means ClearStalled was called for a role that is not shown
// stalled; nothing was changed.
var ErrNotStalled = errors.New("leadlifecycle: the role is not shown stalled")

// ErrInstanceChanged means ClearStalled could not prove that the stalled
// coordinator's own instance is still running: the unit is not loaded, is not
// ours, or has another epoch, intent or invocation than the record, or is not
// running. The role is shown unknown.
var ErrInstanceChanged = errors.New("leadlifecycle: the unit is not the stalled instance")

// MarkStalled records that a coordinator has stopped making progress. The
// observation is made by a detector that is not part of this package; this is
// only the record of it. The role must be a coordinator shown starting, ready
// or degraded; for any other shown state, or a role that is not a coordinator,
// the registry's refusal is returned (an error that wraps
// leadregistry.ErrStateTransition) and nothing is changed. A coordinator that
// is already stalled is left as it is, and no error is returned. reason is the
// detector's text; it is recorded as the detail of the reason, together with
// the state the coordinator was in, and is clipped to fit the registry.
//
// A stalled coordinator is not replaced by this package on its own, and no
// launch is admitted from stalled: it is replaced only by Stop, which proves
// the instance stopped, and then Start, which launches instance n+1.
//
// The state is recorded from the entry read under the role's lock and is
// refused, appending nothing, with an error that wraps
// leadregistry.ErrStaleState when the registry shows the coordinator
// differently by then: the newer decision stands and the caller judges again.
func (c *Controller) MarkStalled(ctx context.Context, role leadrole.Role, reason string) (leadregistry.Entry, error) {
	unlock, err := c.lock(ctx, role)
	if err != nil {
		return leadregistry.Entry{}, err
	}
	defer unlock()
	e, err := c.load(role)
	if err != nil {
		return e, err
	}
	return c.markStalledFrom(e, reason)
}

// markStalledFrom records the stall from e, the entry the caller read. The
// caller holds the role's lock.
func (c *Controller) markStalledFrom(e leadregistry.Entry, reason string) (leadregistry.Entry, error) {
	role := e.Role
	if e.Shown.State == leadregistry.StateStalled {
		return e, nil
	}
	if reason == "" {
		reason = "no progress observed"
	}
	detail := clip(stalledWasPrefix+string(e.Shown.State)+stalledReasonJoiner+reason, leadregistry.MaxReasonDetailBytes)
	// The detail records the state the coordinator was in, read from e: the
	// write is made only if that is still what the registry shows.
	out, err := c.reg.SetState(role, leadregistry.StateChange{
		Epoch: e.Epoch, Expected: leadregistry.ExpectShown(e.Shown), State: leadregistry.StateStalled, Evidence: leadregistry.EvidenceObservation,
		Reason: leadregistry.Reason{Code: codeStalled, Detail: detail},
	})
	if err != nil {
		return e, fmt.Errorf("leadlifecycle: mark %s stalled: %w", role, err)
	}
	return out, nil
}

// ClearStalled returns a stalled coordinator to its live state when its own
// instance is still running. The unit is inspected: it must be loaded, ours,
// running, with the record's epoch and intent and the recorded invocation. The
// role then returns to the state it was stalled from, as recorded by
// MarkStalled (ready or degraded), or to degraded when that state was starting
// (the registry shows starting only for a launch) or is not recorded; this
// package never shows a state it has not been told. With anything else the role
// is shown unknown (reason stalled-instance-changed), and the error wraps
// ErrInstanceChanged; with a manager that does not answer nothing is changed
// and the error says so. A role that is not stalled gives ErrNotStalled.
//
// The parent is judged first, as Reconcile judges it: when the parent lead's
// epoch is no longer the one the coordinator recorded, or the parent is not
// shown starting, ready or degraded, the coordinator has ended with its parent
// and is not returned to a live state. It is shown unknown (reason
// orphan-coordinator), the error wraps ErrParentNotLive, and the unit is not
// looked at: Reconcile proves it absent, or reports it still loaded, and an
// explicit Stop ends it. When the parent cannot be read from the registry the
// parent is neither live nor ended as far as is known: nothing is changed and
// the error says so.
func (c *Controller) ClearStalled(ctx context.Context, role leadrole.Role) (leadregistry.Entry, error) {
	unlock, err := c.lock(ctx, role)
	if err != nil {
		return leadregistry.Entry{}, err
	}
	defer unlock()
	e, err := c.load(role)
	if err != nil {
		return e, err
	}
	if e.Shown.State != leadregistry.StateStalled {
		return e, fmt.Errorf("%w: %s is shown %s", ErrNotStalled, role, e.Shown.State)
	}
	// A coordinator ends with its parent's epoch: under a parent that has
	// ended, or is not live, it is not returned to a live state, whatever its
	// unit is doing. Reconcile judges the same fact in the same way, and is
	// what proves the unit absent or reports it still loaded.
	orphan, perr := c.parentEnded(e)
	if perr != nil {
		// Cannot tell: nothing is changed, and the stall is not cleared.
		return e, fmt.Errorf("leadlifecycle: clear stalled %s: %w", role, perr)
	}
	if orphan != "" {
		out := fmt.Errorf("%w: %s", ErrParentNotLive, orphan)
		e, serr := c.setUnknown(e, codeOrphanCoordinator, orphan+"; the stall is not cleared, and Reconcile or an explicit Stop ends the coordinator")
		if serr != nil {
			return e, errors.Join(out, serr)
		}
		return e, out
	}
	st, err := c.mgr.Inspect(ctx, role.UnitName())
	if err != nil {
		return e, fmt.Errorf("leadlifecycle: clear stalled %s: inspect: %w", role, err)
	}
	if !st.Loaded || !matchesEntry(st, e) || !st.Live() {
		why := "the unit is not loaded"
		switch {
		case st.Loaded && !matchesEntry(st, e):
			why = describeLoaded(st)
		case st.Loaded:
			why = fmt.Sprintf("the unit of epoch %d is loaded but %s", e.Epoch, st.Active)
		}
		out := fmt.Errorf("%w: %s", ErrInstanceChanged, why)
		e, serr := c.setUnknown(e, codeStalledNotOurs, why)
		if serr != nil {
			return e, errors.Join(out, serr)
		}
		return e, out
	}
	to := leadregistry.StateDegraded
	if was := stalledFrom(e.Shown.Reason.Detail); was == leadregistry.StateReady {
		to = leadregistry.StateReady
	}
	// The unit was inspected after e was read, and the return state is read
	// from e: the write is made only if the stall is still what is shown.
	out, err := c.reg.SetState(role, leadregistry.StateChange{
		Epoch: e.Epoch, Expected: leadregistry.ExpectShown(e.Shown), State: to, Evidence: leadregistry.EvidenceObservation,
		Reason: leadregistry.Reason{Code: "stall-cleared", Detail: clip(fmt.Sprintf(
			"the unit of epoch %d intent %s still runs (invocation %s)", e.Epoch, st.Intent, st.InvocationID), leadregistry.MaxReasonDetailBytes)},
	})
	if err != nil {
		return e, fmt.Errorf("leadlifecycle: clear stalled %s: %w", role, err)
	}
	return out, nil
}

// stalledFrom reads the state MarkStalled recorded in a reason detail; it
// returns the empty state when there is none.
func stalledFrom(detail string) leadregistry.State {
	rest, ok := strings.CutPrefix(detail, stalledWasPrefix)
	if !ok {
		return ""
	}
	name, _, _ := strings.Cut(rest, stalledReasonJoiner)
	switch s := leadregistry.State(name); s {
	case leadregistry.StateStarting, leadregistry.StateReady, leadregistry.StateDegraded:
		return s
	}
	return ""
}
