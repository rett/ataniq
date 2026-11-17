package leadlifecycle

import (
	"context"
	"errors"
	"fmt"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
)

// RoleReport is what Reconcile found for one role and did about it.
type RoleReport struct {
	// Role is the role.
	Role leadrole.Role
	// Kind is the kind of the role: a project lead, the supervisor or a
	// coordinator.
	Kind leadrole.Kind
	// Feature is the feature of a coordinator, and empty for every other role.
	Feature string
	// Parent is the lead a coordinator lives under, and the zero role for
	// every other role.
	Parent leadrole.Role
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
	// Acknowledged is true when this call, having acted on a cut for every
	// role, journaled the registry's acknowledgement of it
	// ([leadregistry.Registry.AcknowledgeRecovery]), so that a later daemon
	// does not treat the cut again. It is false when there was no cut to act
	// on, when a role could not be acted on (the manager did not answer),
	// when the acknowledgement could not be written, and when a call at the
	// same time acknowledged the cut first (that call reports it; this one
	// returns no error for it).
	Acknowledged bool
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
// when its unit is running, abandoned at once when the boot changed or once
// the late-start window and its margin have passed), so that the owner's
// decision is not refused, and the owner's ResolveUnknown
// moves it on. Stop also moves it on. When the pass has acted on every role
// (the manager answered for each) Reconcile then acknowledges the cut to the
// registry, with a note saying what it did, and the report says so
// (Acknowledged); otherwise the cut stays unacknowledged and the next daemon
// treats it again. A crash between acting and acknowledging does the same.
//
// For every role run by ataniq, by its recorded state:
//
//   - A pending launch intent (a lost start reply, or a daemon that stopped
//     between the intent and the outcome): the unit is inspected. A unit with
//     exactly this epoch and intent is adopted. With no unit and its control
//     group proven empty the role is unknown (reason launch-unsettled) until
//     the late-start window and its margin since the intent have passed, then
//     the intent is abandoned and the role stopped, with a proof text naming
//     the facts used.
//     With no unit loaded on another boot than the one journaled with the
//     intent, the intent is abandoned and the role stopped at once: the boot
//     changed, so no start request of the intent can land.
//     A loaded unit that is not this launch's makes the role unknown, and the
//     intent is abandoned once the window and margin have passed.
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
//   - A coordinator is judged as a lead is, and also against its parent lead,
//     which is settled first in the same call. When the parent's epoch is no
//     longer the one the coordinator recorded at launch, or the parent is not
//     shown starting, ready or degraded, the coordinator has ended with its
//     parent: with its unit proven absent it is ended with the reason
//     parent-epoch-ended and the proof; with its unit still loaded (the
//     manager has not finished stopping a bound unit) it is shown unknown with
//     the reason orphan-coordinator, and Reconcile signals nothing: an explicit
//     Stop ends it, or a later Reconcile finds it gone. After a reboot the
//     coordinator is stopped with the reason host-rebooted, as a lead is. A
//     stalled coordinator whose unit is live and ours stays stalled.
//
// When the manager does not answer, nothing changes for the role and the report
// says unavailable: the registry does not require a live role to become
// unknown for that, and showing unknown on a mere failure to ask would make a
// role that is running look lost. A role in a proven state is likewise left.
//
// Reconcile returns an error only for a failure of the registry or of the
// context; the report then holds the roles done so far. Each line of the report
// carries the kind of the role and, for a coordinator, its feature and parent.
func (c *Controller) Reconcile(ctx context.Context) (Report, error) {
	rec := c.reg.Recovery()
	c.mu.Lock()
	cut := rec.Cut() && !c.recovered
	ackDue := rec.Cut() && c.recovered && c.ackDue
	c.mu.Unlock()
	entries, err := c.reg.List()
	if err != nil {
		return Report{}, err
	}
	rep := Report{Recovered: cut}
	undone := 0
	// Leads are settled before coordinators, so that a coordinator is judged
	// against what became of its parent in this very pass; the report keeps
	// the registry's order.
	order := make([]int, 0, len(entries))
	for pass := 0; pass < 2; pass++ {
		for i, e := range entries {
			if (e.Role.Kind() == leadrole.KindCoordinator) == (pass == 1) {
				order = append(order, i)
			}
		}
	}
	done := make([]*RoleReport, len(entries))
	var failed error
	for _, i := range order {
		rr, left, err := c.reconcileRole(ctx, entries[i].Role, cut)
		done[i] = &rr
		if err != nil {
			failed = err
			break
		}
		if left {
			undone++
		}
	}
	for _, rr := range done {
		if rr != nil {
			rep.Roles = append(rep.Roles, *rr)
		}
	}
	if failed != nil {
		return rep, failed
	}
	if cut {
		c.mu.Lock()
		c.recovered = true
		c.ackDue = undone == 0
		c.mu.Unlock()
		ackDue = undone == 0
	}
	if ackDue {
		_, err := c.reg.AcknowledgeRecovery(cutNote(rep))
		switch {
		case err == nil:
			rep.Acknowledged = true
		case errors.Is(err, leadregistry.ErrNoRecovery):
			// A call at the same time acted on the cut and acknowledged it
			// first. Each call that acknowledges has acted on every role
			// itself, so the cut is done; this call did not write it.
		default:
			return rep, err
		}
		c.mu.Lock()
		c.ackDue = false
		c.mu.Unlock()
	}
	return rep, nil
}

// cutNote is the note of the acknowledgement of a cut: what Reconcile did to
// the roles it acted on.
func cutNote(rep Report) string {
	var unknown, left, other int
	for _, r := range rep.Roles {
		switch {
		case r.Action == ActionNotManaged:
		case r.Reason == "recovered-proven-absent":
			left++
		case r.Reason == codeRegistryRecov:
			unknown++
		default:
			other++
		}
	}
	return clip(fmt.Sprintf("lead reconcile acted on the cut journal for every role run by ataniq: %d shown unknown (registry-recovered), %d left as recorded with the unit proven absent, %d other",
		unknown, left, other), leadregistry.MaxProofBytes)
}

// reconcileRole settles one role. left is true when the role could not be
// acted on because the manager did not answer.
func (c *Controller) reconcileRole(ctx context.Context, role leadrole.Role, cut bool) (rr RoleReport, left bool, err error) {
	rr = RoleReport{Role: role, Kind: role.Kind(), Feature: role.Feature(), Parent: role.Parent(), Action: ActionUnchanged}
	unlock, err := c.lock(ctx, role)
	if err != nil {
		return rr, false, err
	}
	defer unlock()
	e, err := c.reg.Get(role)
	if err != nil {
		return rr, false, err
	}
	rr.Manager, rr.Epoch, rr.Before, rr.After = e.Manager, e.Epoch, e.Shown.State, e.Shown.State
	if e.Manager != leadregistry.ManagerAtaniq {
		rr.Action, rr.Reason, rr.Detail = ActionNotManaged, "not-managed", "the role is run by "+string(e.Manager)+" and was not touched"
		return rr, false, nil
	}
	var n note
	switch {
	case cut:
		e, n, left, err = c.reconcileCut(ctx, e)
	case e.Shown.State == leadregistry.StateUnknown && e.Shown.Reason.Code == codeRegistryRecov:
		e, n, err = c.reconcileRecovered(ctx, e)
	case e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomePending:
		e, n, err = c.settlePending(ctx, e, false)
	default:
		e, n, err = c.reconcileResolved(ctx, e)
	}
	rr.Action, rr.Reason, rr.Detail = n.action, n.reason, n.detail
	if err != nil {
		return rr, false, err
	}
	rr.After = e.Shown.State
	return rr, left, nil
}

// reconcileCut handles a role after the registry cut its journal. left is true
// when the manager did not answer for the role: the role is shown unknown all
// the same, but what happened to its unit was not looked at.
func (c *Controller) reconcileCut(ctx context.Context, e leadregistry.Entry) (leadregistry.Entry, note, bool, error) {
	const lost = "the registry journal was cut when it was opened, so what happened to the role after the cut is not known"
	left := false
	switch e.Shown.State {
	case leadregistry.StateDesired, leadregistry.StateStopped, leadregistry.StateEnded:
		detail := lost
		st, err := c.mgr.Inspect(ctx, e.Role.UnitName())
		switch {
		case err != nil:
			left = true
			detail += "; the unit could not be inspected: " + err.Error()
		case st.Loaded:
			detail += "; " + describeLoaded(st)
		default:
			ab := c.proveGone(ctx, st, e.Unit)
			if ab.kind == proofDone {
				return e, note{ActionUnchanged, "recovered-proven-absent", ab.text}, false, nil
			}
			left = ab.kind == proofUnavailable
			detail += "; " + ab.why
		}
		e, err = c.setUnknown(e, codeRegistryRecov, detail)
		return e, note{ActionUnknown, codeRegistryRecov, detail}, left, err
	}
	e, err := c.setUnknown(e, codeRegistryRecov, lost)
	if err != nil {
		return e, note{ActionUnknown, codeRegistryRecov, lost}, false, err
	}
	n := note{ActionUnknown, codeRegistryRecov, lost}
	if e.Intent != nil && e.Intent.Outcome == leadregistry.OutcomePending {
		var sn note
		e, sn, err = c.settlePending(ctx, e, true)
		if sn.action == ActionAdopted || sn.action == ActionAbandoned {
			n.detail += "; " + sn.detail
		}
		left = sn.action == ActionUnavailable
	}
	return e, n, left, err
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
	orphan := ""
	if !resting && e.Role.Kind() == leadrole.KindCoordinator {
		orphan = c.parentEnded(e)
	}
	if st.Loaded {
		var n note
		switch {
		case orphan != "" && matchesEntry(st, e):
			n = note{ActionUnknown, codeOrphanCoordinator, fmt.Sprintf(
				"%s, but the unit of epoch %d intent %s is still loaded (%s, invocation %s); Reconcile never signals it, an explicit Stop ends it",
				orphan, e.Epoch, st.Intent, st.Active, st.InvocationID)}
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
	if orphan != "" {
		proof := orphan + "; " + ab.text
		e, err = c.setProven(e, leadregistry.StateEnded, codeParentEpochEnded,
			"the coordinator ended with its parent lead", proof)
		return e, note{ActionEnded, codeParentEpochEnded, proof}, err
	}
	e, err = c.setProven(e, leadregistry.StateEnded, codeHolderGone,
		"the holder went away on its own", ab.text)
	return e, note{ActionEnded, codeHolderGone, ab.text}, err
}

// parentEnded says why the coordinator e has ended with its parent lead, or
// returns the empty string when its parent still runs the epoch the
// coordinator recorded. The parent has ended when its epoch is no longer the
// one the coordinator's launch recorded, or when it is not shown starting,
// ready or degraded. The registry is read as it is now: leads are settled
// before coordinators in one Reconcile.
func (c *Controller) parentEnded(e leadregistry.Entry) string {
	if e.Intent == nil {
		return ""
	}
	pr := e.Role.Parent()
	p, err := c.reg.Get(pr)
	switch {
	case errors.Is(err, leadregistry.ErrNoRole):
		return fmt.Sprintf("the parent lead %s is not defined", pr)
	case err != nil:
		return ""
	case p.Epoch != e.Intent.ParentEpoch:
		return fmt.Sprintf("the parent lead %s is at epoch %d, not the epoch %d the coordinator recorded", pr, p.Epoch, e.Intent.ParentEpoch)
	}
	switch p.Shown.State {
	case leadregistry.StateStarting, leadregistry.StateReady, leadregistry.StateDegraded:
		return ""
	}
	return fmt.Sprintf("the parent lead %s epoch %d is shown %s", pr, p.Epoch, p.Shown.State)
}
