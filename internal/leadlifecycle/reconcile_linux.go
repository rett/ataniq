package leadlifecycle

import (
	"context"
	"fmt"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// RoleReport is what Reconcile found for one role and did about it.
type RoleReport struct {
	// Role is the role.
	Role leadrole.Role
	// Manager is the manager that runs the role.
	Manager leadregistry.Manager
	// Epoch is the role's epoch.
	Epoch uint64
	// Before is the shown state when Reconcile looked.
	Before leadregistry.State
	// After is the shown state when it was done.
	After leadregistry.State
	// Action says what was done.
	Action Action
	// Reason is a short machine code for the finding.
	Reason string
	// Detail is the finding in words, with the facts it rests on.
	Detail string
}

// Report is the result of one [Controller.Reconcile], one line per role in the
// registry's order.
type Report struct {
	// Roles has one entry per role.
	Roles []RoleReport
	// Recovered is true when this call acted on a cut of the registry
	// journal (the registry's Recovery said so and no earlier call had).
	Recovered bool
}

// Find returns the report of one role.
func (r Report) Find(role leadrole.Role) (RoleReport, bool) {
	for _, x := range r.Roles {
		if x.Role == role {
			return x, true
		}
	}
	return RoleReport{}, false
}

// Reconcile compares the registry with the unit manager for every role and
// returns what it found. It is called when the daemon starts and may be called
// at any time. It starts nothing and sends no signal, ever.
//
// If the registry's Recovery reports that the journal was cut, the first call
// shows every role run by ataniq as unknown with the reason registry-recovered,
// except a role that was recorded desired, stopped or ended and whose unit is
// proven absent (not loaded, with its control group proven empty or the boot
// identity changed): that role is left as it is. A role shown unknown for that
// reason is then never resolved by Reconcile, since the registry may have lost
// what happened to it; only a pending launch intent of it is closed (adopted
// when its unit is running, abandoned when the late-start window has passed),
// so that the owner's decision is not refused, and the owner's ResolveUnknown
// moves it on. Stop also moves it on.
//
// For every role run by ataniq, by its recorded state:
//
//   - A pending launch intent (a lost start reply, or a daemon that stopped
//     between the intent and the outcome): the unit is inspected. A unit with
//     exactly this epoch and intent is adopted. With no unit and its control
//     group proven empty the role is unknown (reason launch-unsettled) until
//     the late-start window since the intent has passed, then the intent is
//     abandoned and the role stopped, with a proof text naming the facts used.
//     A loaded unit that is not this launch's makes the role unknown, and the
//     intent is abandoned once the window has passed. The registry does not
//     record the boot identity of an intent, so a reboot cannot settle one
//     earlier than the window does.
//   - Live (starting, ready, degraded, stopping) or unknown, with the intent
//     resolved: the unit is inspected. A loaded unit of ours whose epoch and
//     intent equal the journal's, launched or adopted, is adopted: the shown
//     state is kept, never raised to ready. A role shown unknown stays unknown
//     (the registry cannot leave unknown for a live state). A unit with
//     another epoch, another invocation, an intent that was abandoned or
//     failed, or not ours is reported (stale-unit, foreign-unit) and the role
//     is unknown; it is never signalled. With the unit not loaded: a boot
//     identity that differs from the evidence's is stopped with the reason
//     host-rebooted; on the same boot, the control group of the evidence proven
//     empty is ended with the reason holder-gone (stopped, reason
//     stop-completed, for a role that was stopping or whose desired state is
//     stopped), and a role with no unit
//     evidence is stopped when the unit name and its control group are proven
//     free. Without the proof the role is unknown. The turn is never recorded
//     as finished.
//   - desired, stopped, ended: a loaded unit is a competing or leftover unit,
//     and the role is unknown (competing-unit; stale-unit for a unit of ours
//     that landed late, after its intent was abandoned).
//   - A role run by the tmux-manager is not touched and is reported as such.
//
// When the manager does not answer, nothing changes for the role and the report
// says unavailable: the registry does not require a live role to become
// unknown for that, and showing unknown on a mere failure to ask would make a
// role that is running look lost. A role in a proven state is likewise left.
//
// Reconcile returns an error only for a failure of the registry or of the
// context; the report then holds the roles done so far.
func (c *Controller) Reconcile(ctx context.Context) (Report, error) {
	rec := c.reg.Recovery()
	c.mu.Lock()
	cut := rec.Cut() && !c.recovered
	c.mu.Unlock()
	entries, err := c.reg.List()
	if err != nil {
		return Report{}, err
	}
	rep := Report{Recovered: cut}
	for _, e := range entries {
		rr, err := c.reconcileRole(ctx, e.Role, cut)
		rep.Roles = append(rep.Roles, rr)
		if err != nil {
			return rep, err
		}
	}
	if cut {
		c.mu.Lock()
		c.recovered = true
		c.mu.Unlock()
	}
	return rep, nil
}

func (c *Controller) reconcileRole(ctx context.Context, role leadrole.Role, cut bool) (RoleReport, error) {
	rr := RoleReport{Role: role, Action: ActionUnchanged}
	unlock, err := c.lock(ctx, role)
	if err != nil {
		return rr, err
	}
	defer unlock()
	e, err := c.reg.Get(role)
	if err != nil {
		return rr, err
	}
	rr.Manager, rr.Epoch, rr.Before, rr.After = e.Manager, e.Epoch, e.Shown.State, e.Shown.State
	if e.Manager != leadregistry.ManagerAtaniq {
		rr.Action, rr.Reason, rr.Detail = ActionNotManaged, "not-managed", "the role is run by "+string(e.Manager)+" and was not touched"
		return rr, nil
	}
	var n note
	switch {
	case cut:
		e, n, err = c.reconcileCut(ctx, e)
	case e.Shown.State == leadregistry.StateUnknown && e.Shown.Reason.Code == codeRegistryRecov:
		e, n, err = c.reconcileRecovered(ctx, e)
	case e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomePending:
		e, n, err = c.settlePending(ctx, e, false)
	default:
		e, n, err = c.reconcileResolved(ctx, e)
	}
	rr.Action, rr.Reason, rr.Detail = n.action, n.reason, n.detail
	if err != nil {
		return rr, err
	}
	rr.After = e.Shown.State
	return rr, nil
}

// reconcileCut handles a role after the registry cut its journal.
func (c *Controller) reconcileCut(ctx context.Context, e leadregistry.Entry) (leadregistry.Entry, note, error) {
	const lost = "the registry journal was cut when it was opened, so what happened to the role after the cut is not known"
	switch e.Shown.State {
	case leadregistry.StateDesired, leadregistry.StateStopped, leadregistry.StateEnded:
		detail := lost
		st, err := c.mgr.Inspect(ctx, e.Role.UnitName())
		switch {
		case err != nil:
			detail += "; the unit could not be inspected: " + err.Error()
		case st.Loaded:
			detail += "; " + describeLoaded(st)
		default:
			ab := c.proveGone(ctx, st, e.Unit)
			if ab.kind == proofDone {
				return e, note{ActionUnchanged, "recovered-proven-absent", ab.text}, nil
			}
			detail += "; " + ab.why
		}
		e, err = c.setUnknown(e, codeRegistryRecov, detail)
		return e, note{ActionUnknown, codeRegistryRecov, detail}, err
	}
	e, err := c.setUnknown(e, codeRegistryRecov, lost)
	if err != nil {
		return e, note{ActionUnknown, codeRegistryRecov, lost}, err
	}
	n := note{ActionUnknown, codeRegistryRecov, lost}
	if e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomePending {
		var sn note
		e, sn, err = c.settlePending(ctx, e, true)
		if sn.action == ActionAdopted || sn.action == ActionAbandoned {
			n.detail += "; " + sn.detail
		}
	}
	return e, n, err
}

// reconcileRecovered keeps a role that an earlier Reconcile showed unknown
// because of a cut: its pending intent is settled, its state is left to the
// owner.
func (c *Controller) reconcileRecovered(ctx context.Context, e leadregistry.Entry) (leadregistry.Entry, note, error) {
	n := note{ActionUnchanged, codeRegistryRecov, "shown unknown since the registry journal was cut; the owner decides, or Stop"}
	if e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomePending {
		var sn note
		var err error
		e, sn, err = c.settlePending(ctx, e, true)
		if sn.action == ActionAdopted || sn.action == ActionAbandoned {
			n.action = sn.action
			n.detail += "; " + sn.detail
		}
		return e, n, err
	}
	return e, n, nil
}

// reconcileResolved handles a role whose launch intent has an outcome (or that
// never had one).
func (c *Controller) reconcileResolved(ctx context.Context, e leadregistry.Entry) (leadregistry.Entry, note, error) {
	st, err := c.mgr.Inspect(ctx, e.Role.UnitName())
	if err != nil {
		return e, note{ActionUnavailable, "manager-unavailable", "inspect: " + err.Error()}, nil
	}
	resting := false
	switch e.Shown.State {
	case leadregistry.StateDesired, leadregistry.StateStopped, leadregistry.StateEnded:
		resting = true
	}
	if st.Loaded {
		var n note
		switch {
		case resting:
			code := codeCompetingUnit
			if st.Ours {
				code = "stale-unit"
			}
			n = note{ActionUnknown, code, fmt.Sprintf("the record says %s but %s", e.Shown.State, describeLoaded(st))}
		case matchesEntry(st, e) && !st.Live():
			n = note{ActionUnknown, codeUnitNotRunning, fmt.Sprintf("the unit of epoch %d is loaded but %s", e.Epoch, st.Active)}
		case matchesEntry(st, e):
			detail := fmt.Sprintf("the unit of epoch %d intent %s is running (invocation %s); shown state %s kept, never raised to ready",
				e.Epoch, st.Intent, st.InvocationID, e.Shown.State)
			if e.Shown.State == leadregistry.StateUnknown {
				detail = fmt.Sprintf("the unit of epoch %d intent %s is running (invocation %s); the role stays unknown, which the registry cannot leave for a live state: stop it or decide",
					e.Epoch, st.Intent, st.InvocationID)
			}
			return e, note{ActionAdopted, "adopted", detail}, nil
		default:
			n = note{ActionUnknown, foreignCode(st), describeLoaded(st)}
		}
		e, err = c.setUnknown(e, n.reason, n.detail)
		return e, n, err
	}
	if resting {
		return e, note{ActionUnchanged, "", ""}, nil
	}
	ab := c.proveGone(ctx, st, e.Unit)
	switch ab.kind {
	case proofUnavailable:
		return e, note{ActionUnavailable, "manager-unavailable", ab.why}, nil
	case proofLacking:
		e, err = c.setUnknown(e, codeNotEmpty, ab.why)
		return e, note{ActionUnknown, codeNotEmpty, ab.why}, err
	}
	switch {
	case ab.basis == basisReboot:
		e, err = c.setProven(e, leadregistry.StateStopped, codeHostRebooted,
			"the host rebooted; no lead starts by itself", ab.text)
		return e, note{ActionStopped, codeHostRebooted, ab.text}, err
	case e.Unit.InvocationID == "":
		e, err = c.setProven(e, leadregistry.StateStopped, codeUnitAbsent,
			"no unit evidence is recorded and the unit is absent", ab.text)
		return e, note{ActionStopped, codeUnitAbsent, ab.text}, err
	case e.Shown.State == leadregistry.StateStopping || e.Desired == leadregistry.DesiredStopped:
		e, err = c.setProven(e, leadregistry.StateStopped, codeStopCompleted,
			"the unit is gone and the control group empty", ab.text)
		return e, note{ActionStopped, codeStopCompleted, ab.text}, err
	}
	e, err = c.setProven(e, leadregistry.StateEnded, codeHolderGone,
		"the holder went away on its own", ab.text)
	return e, note{ActionEnded, codeHolderGone, ab.text}, err
}
