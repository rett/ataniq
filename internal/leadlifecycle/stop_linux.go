package leadlifecycle

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// Stop stops the role and returns the registry's record of it afterwards.
//
// It sets the desired state to stopped and asks the [Quiesce] (when there is
// one) to stop delivering; a failure of that is recorded in the reason detail
// and does not prevent the stop. A pending launch intent is settled first as
// far as it can be: a unit with its epoch and intent is adopted and then
// stopped, a role whose intent is closed with no unit is stopped by that, and
// otherwise Stop returns ErrIntentPending and signals nothing.
//
// The unit is then inspected. A unit that is not loaded goes straight to the
// proof. A unit that is loaded and is not the role's recorded one is never
// signalled: the role is shown unknown and the error wraps ErrForeignUnit. Our
// unit gets the shown state stopping (an unknown role stays unknown, which the
// registry's table requires), SIGTERM to the control group of its invocation,
// and a wait of TermWait for the group to be proven empty, polled on the
// injected clock; if it is still populated, SIGKILL. Then the proof is awaited
// for the proof bound: the unit not loaded (or loaded with another invocation)
// and the control group of the stopped invocation proven empty. With it the
// role is stopped, with a proof text naming the invocation and the control
// group. Without it by the bound, or with a manager error that lasts to the
// bound, the role is unknown (case L4) and the error wraps ErrStopUnproven.
// ErrNotLoaded and ErrInvocationChanged from a signal are not failures: the
// proof decides. A context that ends first leaves the role shown stopping, and
// Stop can be called again.
//
// A unit with exactly the role's current epoch and intent counts as the role's
// own even when the intent was abandoned: that is a start that landed late.
// Reconcile never signals it, but Stop is the owner's order, and signals it.
func (c *Controller) Stop(ctx context.Context, role leadrole.Role) (leadregistry.Entry, error) {
	unlock, err := c.lock(ctx, role)
	if err != nil {
		return leadregistry.Entry{}, err
	}
	defer unlock()

	e, err := c.load(role)
	if err != nil {
		return e, err
	}
	if e.Desired != leadregistry.DesiredStopped {
		if e, err = c.reg.SetDesired(role, leadregistry.DesiredStopped); err != nil {
			return e, err
		}
	}
	var notes []string
	switch e.Shown.State {
	case leadregistry.StateStopped, leadregistry.StateEnded, leadregistry.StateDesired:
	default:
		if c.quiet != nil {
			if qerr := c.quiet.Stop(ctx, role, e.Epoch); qerr != nil {
				notes = append(notes, "quiesce failed: "+qerr.Error())
			}
		}
	}
	if e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomePending {
		var n note
		if e, n, err = c.settlePending(ctx, e, false); err != nil {
			return e, err
		}
		switch n.action {
		case ActionAdopted:
		case ActionStopped:
			return e, nil
		default:
			return e, fmt.Errorf("%w: %s: %s", ErrIntentPending, n.reason, n.detail)
		}
	}

	unit := role.UnitName()
	st, err := c.inspectRetry(ctx, unit)
	if err != nil {
		if ctx.Err() != nil {
			return e, err
		}
		return c.unproven(e, notes, "the unit could not be inspected: "+err.Error(), err)
	}
	if !st.Loaded {
		return c.stopAbsent(ctx, e, st, notes)
	}
	if !stopsAsOurs(st, e) {
		out := fmt.Errorf("%w: %w: %s", ErrUnitLoaded, ErrForeignUnit, describeLoaded(st))
		e, serr := c.setUnknown(e, foreignCode(st), describeLoaded(st))
		if serr != nil {
			return e, errors.Join(out, serr)
		}
		return e, out
	}
	if e.Shown.State != leadregistry.StateStopping && e.Shown.State != leadregistry.StateUnknown {
		if e, err = c.reg.SetState(role, leadregistry.StateChange{
			Epoch: e.Epoch, State: leadregistry.StateStopping, Evidence: leadregistry.EvidenceObservation,
			Reason: leadregistry.Reason{Code: "stop-requested"},
		}); err != nil {
			return e, err
		}
	}
	return c.terminate(ctx, e, st, notes)
}

// stopsAsOurs reports whether a loaded unit is the unit the role's record
// names: ours, with the epoch and intent of the current launch, and the
// recorded invocation if one is recorded. A pending intent has been settled by
// now, so this needs no outcome.
func stopsAsOurs(st leadunit.State, e leadregistry.Entry) bool {
	if !matchesIntent(st, e.Intent) {
		return false
	}
	return e.Unit.InvocationID == "" || e.Unit.InvocationID == st.InvocationID
}

// stopAbsent finishes a stop for a unit that was not loaded when Stop looked:
// there is nothing to signal, only the proof to establish.
func (c *Controller) stopAbsent(ctx context.Context, e leadregistry.Entry, st leadunit.State, notes []string) (leadregistry.Entry, error) {
	if e.Shown.State == leadregistry.StateStopped || e.Shown.State == leadregistry.StateEnded {
		return e, nil
	}
	var ab absence
	why, err := c.pollUntil(ctx, func(ctx context.Context) (bool, string) {
		cur, ierr := c.mgr.Inspect(ctx, st.Unit)
		if ierr != nil {
			return false, "the unit could not be inspected: " + ierr.Error()
		}
		if cur.Loaded {
			return false, "a unit appeared while the stop waited: " + describeLoaded(cur)
		}
		ab = c.proveGone(ctx, cur, e.Unit)
		return ab.kind == proofDone, ab.why
	})
	if err != nil {
		return e, err
	}
	if why != "" {
		return c.unproven(e, notes, why, nil)
	}
	return c.proven(e, notes, "stop-completed", ab.text)
}

// terminate signals the control group of the invocation st names and waits for
// the proof.
func (c *Controller) terminate(ctx context.Context, e leadregistry.Entry, st leadunit.State, notes []string) (leadregistry.Entry, error) {
	start := c.clock.Now()
	killAt := start.Add(TermWait)
	var last error
	termSent := false
	emptied := false
	for !emptied && c.clock.Now().Before(killAt) {
		if !termSent {
			err := c.mgr.Signal(ctx, st, syscall.SIGTERM)
			switch {
			case err == nil, errors.Is(err, leadunit.ErrNotLoaded), errors.Is(err, leadunit.ErrInvocationChanged):
				termSent = true
			case ctx.Err() != nil:
				return e, err
			default:
				last = err
			}
		}
		empty, err := c.mgr.Empty(ctx, st)
		switch {
		case empty:
			emptied = true
			continue
		case err != nil && ctx.Err() != nil:
			return e, err
		case err != nil:
			last = err
		}
		left := killAt.Sub(c.clock.Now())
		if err := c.clock.Sleep(ctx, min(c.poll, left)); err != nil {
			return e, err
		}
	}

	killSent := emptied
	var proof string
	why, err := c.pollUntil(ctx, func(ctx context.Context) (bool, string) {
		if !killSent {
			err := c.mgr.Signal(ctx, st, syscall.SIGKILL)
			switch {
			case err == nil, errors.Is(err, leadunit.ErrNotLoaded), errors.Is(err, leadunit.ErrInvocationChanged):
				killSent = true
			default:
				last = err
			}
		}
		text, ok, w := c.stoppedProof(ctx, st)
		proof = text
		return ok, w
	})
	if err != nil {
		return e, err
	}
	if why != "" {
		if last != nil {
			why += "; last manager error: " + last.Error()
		}
		return c.unproven(e, notes, why, nil)
	}
	return c.proven(e, notes, "stopped", proof)
}

// stoppedProof is the proof that the stopped invocation is gone: the unit is
// not loaded, or loaded with another invocation, and the control group of the
// stopped invocation is proven empty.
func (c *Controller) stoppedProof(ctx context.Context, was leadunit.State) (proof string, ok bool, why string) {
	cur, err := c.mgr.Inspect(ctx, was.Unit)
	if err != nil {
		return "", false, "the unit could not be inspected: " + err.Error()
	}
	if cur.Loaded && cur.InvocationID == was.InvocationID {
		return "", false, "the unit still holds the stopped invocation " + was.InvocationID
	}
	empty, err := c.mgr.Empty(ctx, was)
	if err != nil {
		return "", false, "the control group could not be proven empty: " + err.Error()
	}
	if !empty {
		return "", false, "the control group of invocation " + was.InvocationID + " is not proven empty"
	}
	where := "the unit is not loaded"
	if cur.Loaded {
		where = "the unit has another invocation " + cur.InvocationID + " in a control group of another identity"
	}
	return fmt.Sprintf("invocation %s control group %s (%s) of unit %s proven empty after the stop; %s (boot %s)",
		was.InvocationID, was.ControlGroup, was.ControlGroupID, was.Unit, where, cur.BootID), true, ""
}

func (c *Controller) proven(e leadregistry.Entry, notes []string, code, proof string) (leadregistry.Entry, error) {
	detail := "stopped on request"
	for _, n := range notes {
		detail += "; " + n
	}
	return c.setProven(e, leadregistry.StateStopped, code, detail, proof)
}

func (c *Controller) unproven(e leadregistry.Entry, notes []string, why string, cause error) (leadregistry.Entry, error) {
	detail := why
	for _, n := range notes {
		detail += "; " + n
	}
	out := fmt.Errorf("%w: %s", ErrStopUnproven, why)
	if cause != nil {
		out = fmt.Errorf("%w: %s: %w", ErrStopUnproven, why, cause)
	}
	e, err := c.setUnknown(e, "stop-unproven", detail)
	if err != nil {
		return e, errors.Join(out, err)
	}
	return e, out
}
