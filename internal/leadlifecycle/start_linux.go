package leadlifecycle

import (
	"context"
	"errors"
	"fmt"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// Start launches the role and returns the registry's record of it afterwards.
// It does not wait for readiness and never calls the manager's Start twice for
// one intent.
//
// It refuses, changing nothing, when the role is not defined (an error that
// wraps leadregistry.ErrNoRole), not run by ataniq (ErrNotManaged), not
// admitted by the registry (an error that wraps leadregistry.ErrLaunchRefused:
// the shown state is not desired, stopped or ended, or an intent is pending),
// refused by the preflight (ErrPreflight), or when the previous unit is not
// proven gone: a loaded unit gives ErrUnitLoaded (and ErrForeignUnit when it
// is not the role's own), and a manager error or a control group that is not
// proven empty gives ErrNoProof. A role that claims stopped, ended or desired
// stays as it is in all of these; Reconcile is what shows a competing unit as
// unknown.
//
// A coordinator is started only under its parent lead: the lead must be
// defined, run by ataniq, shown starting, ready or degraded and not have a launch
// outcome of failed or abandoned (a lead whose own launch failed or was given
// up never ran as that epoch, whatever it is shown as), else nothing is
// changed and the error wraps ErrParentNotLive and the registry's refusal
// (leadregistry.ErrLaunchNoParent, ErrLaunchParentManager or
// ErrLaunchParentState). The unit is started with [leadunit.Spec].Parent set to
// the lead's unit, so that it is bound to it, and everything else is as for a
// lead: the replacement rule on the coordinator's own unit and evidence, the
// intent (which records the parent's epoch and the identity of the instance),
// the holder command, and the outcomes below. A start that the manager accepts
// without a unit because the parent unit is not live (it died after the
// registry was looked at) started nothing: the outcome failed is recorded and
// the coordinator is stopped with the proof of the absent unit, or unknown
// without it. A start that the manager refuses with a lost reply that names the
// missing parent is a lost reply (below), not a start that failed: the real
// manager cannot tell the two apart. A lead is not started while a coordinator of it is not proven
// stopped: the error wraps ErrCoordinatorNotStopped and
// leadregistry.ErrLaunchCoordinator, and nothing is changed.
//
// The registry applies those rules again when the launch intent is journaled,
// and the lead or the coordinators may have changed since the pre-check (their
// locks are not held): the registry's refusal is mapped to the same errors,
// ErrParentNotLive and ErrCoordinatorNotStopped, and the desired state that
// the call set is put back, so that the refusal leaves the role as it was. The
// two journal lines of the desired state (set, then restored) remain in the
// registry's journal; the role's record is as before.
//
// Once the proof is in hand it sets the desired state to running, journals the
// launch intent (new epoch, intent id, unit name, and the boot identity the
// manager reported, so that a later reboot settles an intent that never got a
// unit) and only then builds the
// command and calls the manager once:
//
//   - A State whose epoch and intent match the intent: the outcome launched is
//     recorded with the unit evidence, and the shown state stays starting.
//     Start returns nil.
//   - leadunit.ErrUnitExists: nothing of ours was started. Unless an Inspect
//     shows a unit with this epoch and intent, which is adopted and returns
//     nil, the outcome failed is recorded and the role is unknown (someone
//     holds the name). The error wraps ErrStartFailed and, for a unit that is
//     not ours, ErrForeignUnit.
//   - leadunit.ErrReplyLost: Start is not called again. An Inspect that shows
//     a unit with exactly this epoch and intent adopts it (case L1) and Start
//     returns nil. Otherwise the intent stays pending, which is the only
//     outcome the registry's table leaves open for a start that may still
//     land, and the role is unknown with the reason launch-reply-lost; the
//     error wraps ErrLaunchUncertain and Reconcile resolves it.
//   - Any other error from the manager, or a command that cannot be built:
//     nothing was started and nothing will be. The outcome failed (abandoned
//     for a command that cannot be built) is recorded, then the absence of the
//     unit is proved again and the role is stopped with that proof, or unknown
//     without it. The error wraps ErrStartFailed.
func (c *Controller) Start(ctx context.Context, role leadrole.Role) (leadregistry.Entry, error) {
	unlock, err := c.lock(ctx, role)
	if err != nil {
		return leadregistry.Entry{}, err
	}
	defer unlock()

	e, err := c.load(role)
	if err != nil {
		return e, err
	}
	// The registry's own admission rules, checked before any unit call so that
	// a refusal costs nothing. BeginLaunch checks them again.
	if e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomePending {
		return e, fmt.Errorf("leadlifecycle: %w", leadregistry.ErrLaunchPending)
	}
	switch e.Shown.State {
	case leadregistry.StateDesired, leadregistry.StateStopped, leadregistry.StateEnded:
	default:
		return e, fmt.Errorf("leadlifecycle: %w (shown state is %s)", leadregistry.ErrLaunchState, e.Shown.State)
	}
	parent, err := c.admitUnderParent(e)
	if err != nil {
		return e, err
	}
	if err := c.pre.Check(ctx, role, e); err != nil {
		return e, fmt.Errorf("%w: %w", ErrPreflight, err)
	}

	// The replacement rule, before anything is journaled.
	unit := role.UnitName()
	st, err := c.mgr.Inspect(ctx, unit)
	if err != nil {
		return e, fmt.Errorf("%w: inspect %s: %w", ErrNoProof, unit, err)
	}
	if st.Loaded {
		if st.Ours {
			return e, fmt.Errorf("%w: %s", ErrUnitLoaded, describeLoaded(st))
		}
		return e, fmt.Errorf("%w: %w: %s", ErrUnitLoaded, ErrForeignUnit, describeLoaded(st))
	}
	if ab := c.proveGone(ctx, st, e.Unit); ab.kind != proofDone {
		if ab.err != nil {
			return e, fmt.Errorf("%w: %s: %w", ErrNoProof, ab.why, ab.err)
		}
		return e, fmt.Errorf("%w: %s", ErrNoProof, ab.why)
	}
	boot := st.BootID

	// From here the registry changes. The desired state is recorded before the
	// launch intent, as every intent is recorded before its action, and is put
	// back when the registry refuses the launch (see refuseLaunch).
	wasDesired := e.Desired
	if e.Desired != leadregistry.DesiredRunning {
		if _, err := c.reg.SetDesired(role, leadregistry.DesiredRunning); err != nil {
			return e, err
		}
	}
	intent, err := c.reg.BeginLaunchWith(role, leadregistry.LaunchOptions{BootIdentity: boot})
	if err != nil {
		return c.refuseLaunch(e, wasDesired, err)
	}
	e, err = c.reg.Get(role)
	if err != nil {
		return e, err
	}
	argv, workdir, env, err := c.holder.Build(role, e, intent)
	if err != nil {
		return c.nothingStarted(ctx, e, intent, boot, leadregistry.OutcomeAbandoned, "holder-command-failed",
			fmt.Errorf("build the holder command: %w", err))
	}
	spec := leadunit.Spec{
		Unit: intent.UnitName, Epoch: intent.Epoch, Intent: intent.ID,
		Argv: argv, WorkDir: workdir, Env: env,
	}
	if !parent.IsZero() {
		spec.Parent = parent.UnitName()
	}
	got, err := c.mgr.Start(ctx, spec)
	switch {
	case err == nil:
		if !matchesIntent(got, &intent) {
			if !parent.IsZero() && !got.Loaded {
				// The manager took the request and the unit never became
				// active: with its parent not live, a bound unit does not
				// start. Nothing was started; prove it.
				return c.nothingStarted(ctx, e, intent, boot, leadregistry.OutcomeFailed, "parent-not-live",
					fmt.Errorf("the manager accepted the start of %s but the unit is not loaded, as when its parent unit %s is not live", intent.UnitName, spec.Parent))
			}
			return c.launchMismatch(e, intent, got)
		}
		return c.reg.RecordOutcome(role, intent.ID, intent.Epoch, leadregistry.OutcomeLaunched, evidenceOf(got))
	case errors.Is(err, leadunit.ErrReplyLost):
		return c.replyLost(ctx, e, intent, boot, err)
	case errors.Is(err, leadunit.ErrUnitExists):
		return c.unitExists(ctx, e, intent, boot, err)
	default:
		return c.nothingStarted(ctx, e, intent, boot, leadregistry.OutcomeFailed, "launch-failed", err)
	}
}

// refuseLaunch handles the registry's refusal of the launch after the
// pre-checks passed: the parent lead stopped being live, or a coordinator of the
// lead stopped being proven stopped, between the pre-check and the launch (the
// lock of the role does not cover the other roles). The registry's named
// errors are mapped to the lifecycle's, as admitUnderParent maps them, and the
// desired state that this Start set is put back, so that a refused Start leaves
// the role as it was.
//
// Restoring and not setting the desired state only after the launch is
// admitted keeps the order of every intent: what is wanted is journaled before
// the action, and a launch intent never exists with a desired state of stopped
// beside a unit that may run, which a crash between the two lines would leave.
// It is crash-safe because the restoring line is the only write after the
// refusal and it adds no state that was not possible already: a crash before
// it leaves the desired state running with no launch intent, exactly what a
// crash between the setting and the launch leaves, and nothing in this package
// starts or signals a unit on the desired state alone.
func (c *Controller) refuseLaunch(e leadregistry.Entry, was leadregistry.Desired, err error) (leadregistry.Entry, error) {
	switch {
	case errors.Is(err, leadregistry.ErrLaunchNoParent), errors.Is(err, leadregistry.ErrLaunchParentManager), errors.Is(err, leadregistry.ErrLaunchParentState):
		err = fmt.Errorf("%w: %s: %w", ErrParentNotLive, e.Role.Parent(), err)
	case errors.Is(err, leadregistry.ErrLaunchCoordinator):
		err = fmt.Errorf("%w: %w", ErrCoordinatorNotStopped, err)
	}
	if was != leadregistry.DesiredRunning {
		cur, derr := c.reg.SetDesired(e.Role, was)
		if derr != nil {
			return e, errors.Join(err, fmt.Errorf("restore the desired state %s: %w", was, derr))
		}
		return cur, err
	}
	return e, err
}

// settleCtx is the context for the calls that follow a failed Start. The
// caller's context may be the very reason the start failed, and the follow-up
// must still be able to look at the unit.
func settleCtx(ctx context.Context) context.Context { return context.WithoutCancel(ctx) }

// nothingStarted records an outcome for a launch that started nothing and then
// proves the unit absent again: stopped with that proof, unknown without it.
func (c *Controller) nothingStarted(ctx context.Context, e leadregistry.Entry, in leadregistry.Intent, boot string, outcome leadregistry.Outcome, code string, cause error) (leadregistry.Entry, error) {
	out := fmt.Errorf("%w: %w", ErrStartFailed, cause)
	if _, err := c.reg.RecordOutcome(e.Role, in.ID, in.Epoch, outcome, leadregistry.UnitEvidence{BootID: boot}); err != nil {
		return e, errors.Join(out, err)
	}
	e, err := c.reg.Get(e.Role)
	if err != nil {
		return e, errors.Join(out, err)
	}
	sctx := settleCtx(ctx)
	st, ierr := c.mgr.Inspect(sctx, in.UnitName)
	switch {
	case ierr != nil:
		e, err = c.setUnknown(e, code+"-unproven", "the unit could not be inspected after the failed launch: "+ierr.Error())
	case st.Loaded:
		e, err = c.setUnknown(e, code+"-unproven", describeLoaded(st))
	default:
		ab := c.proveGone(sctx, st, leadregistry.UnitEvidence{BootID: boot})
		if ab.kind == proofDone {
			e, err = c.setProven(e, leadregistry.StateStopped, code,
				fmt.Sprintf("intent %s epoch %d: %s", in.ID, in.Epoch, cause),
				"launch of intent "+in.ID+" started nothing; "+ab.text)
		} else {
			e, err = c.setUnknown(e, code+"-unproven", ab.why)
		}
	}
	if err != nil {
		return e, errors.Join(out, err)
	}
	return e, out
}

// unitExists handles ErrUnitExists: adopt a unit with this very epoch and
// intent, else record failed and show unknown.
func (c *Controller) unitExists(ctx context.Context, e leadregistry.Entry, in leadregistry.Intent, boot string, cause error) (leadregistry.Entry, error) {
	st, ierr := c.mgr.Inspect(settleCtx(ctx), in.UnitName)
	if ierr == nil && matchesIntent(st, &in) {
		return c.reg.RecordOutcome(e.Role, in.ID, in.Epoch, leadregistry.OutcomeAdopted, evidenceOf(st))
	}
	out := fmt.Errorf("%w: %w", ErrStartFailed, cause)
	detail := "the manager refused the start because the unit name is taken"
	switch {
	case ierr != nil:
		detail += "; inspect failed: " + ierr.Error()
	case st.Loaded:
		detail += "; " + describeLoaded(st)
		out = fmt.Errorf("%w: %w", out, ErrForeignUnit)
	}
	if _, err := c.reg.RecordOutcome(e.Role, in.ID, in.Epoch, leadregistry.OutcomeFailed, leadregistry.UnitEvidence{BootID: boot}); err != nil {
		return e, errors.Join(out, err)
	}
	e, err := c.reg.Get(e.Role)
	if err == nil {
		e, err = c.setUnknown(e, "unit-exists", detail)
	}
	if err != nil {
		return e, errors.Join(out, err)
	}
	return e, out
}

// replyLost handles ErrReplyLost: inspect once, adopt only a unit with exactly
// this epoch and intent, and otherwise leave the intent pending.
func (c *Controller) replyLost(ctx context.Context, e leadregistry.Entry, in leadregistry.Intent, boot string, cause error) (leadregistry.Entry, error) {
	st, ierr := c.mgr.Inspect(settleCtx(ctx), in.UnitName)
	if ierr == nil && matchesIntent(st, &in) {
		return c.reg.RecordOutcome(e.Role, in.ID, in.Epoch, leadregistry.OutcomeAdopted, evidenceOf(st))
	}
	detail := "the start reply was lost"
	switch {
	case ierr != nil:
		detail += "; the unit could not be inspected: " + ierr.Error()
	case st.Loaded:
		detail += "; " + describeLoaded(st)
	default:
		detail += "; the unit is not loaded but the request may still land"
	}
	out := fmt.Errorf("%w: %w", ErrLaunchUncertain, cause)
	e, err := c.reg.Get(e.Role)
	if err == nil {
		e, err = c.setUnknown(e, "launch-reply-lost", detail)
	}
	if err != nil {
		return e, errors.Join(out, err)
	}
	return e, out
}

// launchMismatch handles a start that reported success with a state that is
// not this launch's. The manager contract excludes it; the role is shown
// unknown rather than trusting the answer.
func (c *Controller) launchMismatch(e leadregistry.Entry, in leadregistry.Intent, got leadunit.State) (leadregistry.Entry, error) {
	out := fmt.Errorf("%w: the manager's answer does not carry epoch %d intent %s", ErrLaunchUncertain, in.Epoch, in.ID)
	if _, err := c.reg.RecordOutcome(e.Role, in.ID, in.Epoch, leadregistry.OutcomeAbandoned, leadregistry.UnitEvidence{BootID: got.BootID}); err != nil {
		return e, errors.Join(out, err)
	}
	e, err := c.reg.Get(e.Role)
	if err == nil {
		detail := "the manager reported success but the unit is not loaded"
		if got.Loaded {
			detail = "the manager's answer is not this launch's: " + describeLoaded(got)
		}
		e, err = c.setUnknown(e, "launch-state-mismatch", detail)
	}
	if err != nil {
		return e, errors.Join(out, err)
	}
	return e, out
}

// admitUnderParent applies, before anything is changed, the rules of the
// registry that tie a coordinator to its parent lead and a lead to its
// coordinators, so that a refusal costs no journal line. The registry applies
// them again in BeginLaunch and is what decides. It returns the parent of a
// coordinator, and the zero role for every other role.
func (c *Controller) admitUnderParent(e leadregistry.Entry) (leadrole.Role, error) {
	switch e.Role.Kind() {
	case leadrole.KindCoordinator:
		parent := e.Role.Parent()
		pe, err := c.reg.Get(parent)
		var why error
		switch {
		case errors.Is(err, leadregistry.ErrNoRole):
			why = leadregistry.ErrLaunchNoParent
		case err != nil:
			return leadrole.Role{}, err
		case pe.Manager != leadregistry.ManagerAtaniq:
			why = leadregistry.ErrLaunchParentManager
		case pe.Epoch == 0:
			why = leadregistry.ErrLaunchParentState
		default:
			why = leadregistry.ErrLaunchParentState
			switch pe.Shown.State {
			case leadregistry.StateStarting, leadregistry.StateReady, leadregistry.StateDegraded:
				// A parent whose own launch failed or was given up never ran
				// as that epoch, even while it is still shown starting: it is
				// not live. The registry applies the same rule.
				if in := pe.Intent; in == nil || (in.Outcome != leadregistry.OutcomeFailed && in.Outcome != leadregistry.OutcomeAbandoned) {
					return parent, nil
				}
			}
		}
		return leadrole.Role{}, fmt.Errorf("%w: %s: %w", ErrParentNotLive, parent, why)
	case leadrole.KindLead:
		coords, err := c.reg.Coordinators(e.Role)
		if err != nil {
			return leadrole.Role{}, err
		}
		for _, co := range coords {
			if co.Intent == nil || co.Intent.ParentEpoch > e.Epoch {
				continue
			}
			switch co.Shown.State {
			case leadregistry.StateStopped, leadregistry.StateEnded, leadregistry.StateDesired:
			default:
				return leadrole.Role{}, fmt.Errorf("%w: %s is %s since epoch %d of the lead: %w", ErrCoordinatorNotStopped,
					co.Role, co.Shown.State, co.Intent.ParentEpoch, leadregistry.ErrLaunchCoordinator)
			}
		}
	}
	return leadrole.Role{}, nil
}
