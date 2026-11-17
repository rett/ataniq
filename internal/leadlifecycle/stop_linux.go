package leadlifecycle

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
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
// Stop of a lead first stops each coordinator of the lead that is not proven
// stopped (a coordinator never launched, run by the tmux-manager, or recorded
// stopped or ended with proof is left alone), one after the other, each by the
// sequence above: its own quiesce, SIGTERM, 2 s, SIGKILL, the proof. Then the
// lead is stopped, whatever became of them. A coordinator that cannot be proven
// stopped is shown unknown by its own stop, and the result's error is a
// *CoordinatorsError naming it (errors.Is(err, ErrCoordinatorNotStopped)),
// joined with the lead's own error if there is one; the lead's own stop is
// still carried out, and its entry is returned. A later Start of the lead is
// refused with ErrCoordinatorNotStopped until the coordinator is stopped with
// proof, or the owner decides. Stop of a coordinator stops only that
// coordinator. Because a coordinator's unit is bound to its lead's, it may
// already be stopping when it is signalled: ErrNotLoaded and
// ErrInvocationChanged from a signal lead to the proof as they do for a lead.
// The lead's coordinators are listed again after the lead's own stop, and each
// one found that is not proven stopped is stopped by the same sequence: a
// coordinator started between the first listing and the lead's stop is thereby
// stopped, or named in the result. This holds because once the lead has left
// its live states (it is stopping, unknown or stopped) the registry admits no
// new coordinator launch, and a Start of a coordinator that began earlier holds the
// coordinator's lock until it returns, so that the second listing either sees
// its intent or waits for it. A coordinator named by the first pass is not
// stopped a second time. The unit is bound to the lead's, so it is also stopped by the lead's stop,
// but only the proof that Stop collects says that it is gone.
//
// Stop of a lead never returns nil while the second listing was not done. When
// the lead is proven stopped the second listing runs on a context of its own
// that the caller's cannot end (it is bounded by the stop sequences, which are
// bounded by TermWait and the proof bound), so a caller whose context ended
// after the proof still gets the coordinator named, shown unknown, in the
// result. When the stop of the lead returned an error, the second listing runs
// on the caller's context, and a coordinator it could not stop for that
// context makes the result an error that says the coordinators were not all
// listed and stopped; the error of the lead's stop is part of the result
// anyway, and a lead whose own stop failed before it left its live states may
// still admit a coordinator, which the next Stop finds. A context that has ended on entry returns its error before anything
// is read or changed.
//
// A unit with exactly the role's current epoch and intent counts as the role's
// own even when the intent was abandoned: that is a start that landed late.
// So does a unit of ours with an older epoch, which is a start that landed
// after a newer epoch had run (epochs only grow, so it cannot be newer work).
// Reconcile never signals either, but Stop is the owner's order, and signals
// them. A unit of a newer epoch, of the same epoch with another intent, or
// that is not ours is never signalled.
func (c *Controller) Stop(ctx context.Context, role leadrole.Role) (leadregistry.Entry, error) {
	unlock, err := c.lock(ctx, role)
	if err != nil {
		return leadregistry.Entry{}, err
	}
	defer unlock()

	var first *CoordinatorsError
	if role.Kind() == leadrole.KindLead {
		if _, err := c.load(role); err != nil {
			e, _ := c.reg.Get(role)
			return e, err
		}
		if first, err = c.stopCoordinators(ctx, role, nil); err != nil {
			e, _ := c.reg.Get(role)
			return e, err
		}
	}
	e, err := c.stopOne(ctx, role)
	var cerr error
	if first != nil {
		cerr = first
	}
	if role.Kind() == leadrole.KindLead {
		// The lead is no longer live (or no coordinator is admitted under it
		// any more): list the coordinators again, see Stop. Once the lead
		// is proven stopped this is owed whatever became of the caller's
		// context, so it runs on a context of its own, which only the
		// bounds of the stop sequences end; after a failed stop of the lead
		// it runs on the caller's context.
		lctx := ctx
		if err == nil {
			lctx = settleCtx(ctx)
		}
		all, lerr := c.stopCoordinators(lctx, role, first)
		switch {
		case lerr != nil:
			lerr = fmt.Errorf("leadlifecycle: stop %s: the coordinators were not all listed and stopped after the lead's stop: %w", role, lerr)
			return e, errors.Join(err, cerr, lerr)
		case all != nil:
			cerr = all
		}
	}
	switch {
	case cerr == nil:
		return e, err
	case err == nil:
		return e, cerr
	}
	return e, errors.Join(err, cerr)
}

// CoordinatorsError is the error of a Stop of a lead that could not prove
// every coordinator of the lead stopped. It wraps ErrCoordinatorNotStopped and
// the error of each coordinator's stop; the lead's own stop went ahead.
type CoordinatorsError struct {
	// Roles are the coordinators that are not proven stopped, each shown
	// unknown, in the registry's order.
	Roles []leadrole.Role
	// Errs holds the error of the stop of each of them, in the same order.
	Errs []error
}

// Error names the coordinators.
func (e *CoordinatorsError) Error() string {
	names := make([]string, len(e.Roles))
	for i, r := range e.Roles {
		names[i] = r.String()
	}
	return fmt.Sprintf("%s: %s", ErrCoordinatorNotStopped, strings.Join(names, ", "))
}

// Unwrap returns ErrCoordinatorNotStopped and the errors of the coordinators'
// stops.
func (e *CoordinatorsError) Unwrap() []error {
	return append([]error{ErrCoordinatorNotStopped}, e.Errs...)
}

// stopCoordinators stops each coordinator of the lead that is not proven
// stopped, one after the other in the registry's order, each by the whole
// sequence of Stop. A coordinator that cannot be proven stopped is shown
// unknown by its own stop and is named in the first result (nil when there is
// none); the second result is for a failure that ends the lead's stop too (the
// context). The coordinators that named holds (the result of an earlier pass
// over the same lead) are not stopped again and stay in the result. The lead's
// lock is held by the caller; a coordinator's stop takes only its own, and no
// operation on a coordinator takes the lead's.
func (c *Controller) stopCoordinators(ctx context.Context, lead leadrole.Role, named *CoordinatorsError) (unproven *CoordinatorsError, err error) {
	coords, err := c.reg.Coordinators(lead)
	if err != nil {
		return nil, err
	}
	var out *CoordinatorsError
	if named != nil {
		out = &CoordinatorsError{Roles: slices.Clone(named.Roles), Errs: slices.Clone(named.Errs)}
	}
	for _, co := range coords {
		if co.Manager != leadregistry.ManagerAtaniq || co.Epoch == 0 {
			continue
		}
		if out != nil && slices.Contains(out.Roles, co.Role) {
			continue
		}
		done, err := c.reg.CoordinatorStopped(co.Role, co.Epoch)
		if err != nil {
			return nil, err
		}
		if done {
			continue
		}
		if _, err := c.Stop(ctx, co.Role); err == nil {
			continue
		} else if ctx.Err() != nil {
			return nil, err
		} else {
			if out == nil {
				out = &CoordinatorsError{}
			}
			out.Roles = append(out.Roles, co.Role)
			out.Errs = append(out.Errs, err)
		}
	}
	return out, nil
}

// stopOne stops one role: the sequence of Stop for a role whose lock the
// caller holds.
func (c *Controller) stopOne(ctx context.Context, role leadrole.Role) (leadregistry.Entry, error) {
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

// stopsAsOurs reports whether Stop may signal a loaded unit: it is ours and
// either the unit the role's record names (the epoch and intent of the current
// launch, and the recorded invocation if one is recorded), or a unit of an
// older epoch. Epochs only grow, so a unit of ours with an older epoch is a
// start request that landed late, after its intent was closed, and cannot be
// newer work; Stop is the owner's order and ends it. A newer epoch, another
// intent of the current epoch (an epoch the registry reused after a cut) and
// another invocation of the current launch are not recognised. A pending
// intent has been settled by now, so this needs no outcome.
func stopsAsOurs(st leadunit.State, e leadregistry.Entry) bool {
	if st.Loaded && st.Ours && st.Epoch < e.Epoch {
		return true
	}
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
	ok, why, err := c.pollUntil(ctx, func(ctx context.Context) (bool, string) {
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
	if !ok {
		return c.unproven(e, notes, orNoReason(why), nil)
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
	ok, why, err := c.pollUntil(ctx, func(ctx context.Context) (bool, string) {
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
	if !ok {
		why = orNoReason(why)
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

// orNoReason is the reason text of an unproven stop whose last check gave
// none.
func orNoReason(why string) string {
	if why == "" {
		return "the proof was not established by the bound"
	}
	return why
}
