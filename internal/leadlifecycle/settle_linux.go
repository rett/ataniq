package leadlifecycle

import (
	"context"
	"fmt"
	"time"

	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// Action says what Reconcile did to a role.
type Action string

// The actions of a [RoleReport].
const (
	// ActionUnchanged means the record was left as it was.
	ActionUnchanged Action = "unchanged"
	// ActionAdopted means a running unit of ours was matched to the journal's
	// epoch and intent; the shown state was kept (never raised to ready).
	ActionAdopted Action = "adopted"
	// ActionStopped means the role was recorded stopped, with proof.
	ActionStopped Action = "stopped"
	// ActionEnded means the role was recorded ended, with proof.
	ActionEnded Action = "ended"
	// ActionUnknown means the role is shown unknown.
	ActionUnknown Action = "unknown"
	// ActionAbandoned means a pending launch intent was closed as abandoned
	// without changing the shown state.
	ActionAbandoned Action = "abandoned"
	// ActionPending means a launch intent is still unsettled: the late-start
	// window is open.
	ActionPending Action = "pending"
	// ActionUnavailable means the manager did not answer and nothing changed.
	ActionUnavailable Action = "unavailable"
	// ActionNotManaged means the role belongs to the tmux-manager and was not
	// touched.
	ActionNotManaged Action = "not-managed"
)

// note is what settling a role found, for a report or an error.
type note struct {
	action Action
	reason string
	detail string
}

// reply-lost reason codes.
const (
	codeReplyLost      = "launch-reply-lost"
	codeUnsettled      = "launch-unsettled"
	codeNotEmpty       = "control-group-not-proven-empty"
	codeRegistryRecov  = "registry-recovered"
	codeHostRebooted   = "host-rebooted"
	codeHolderGone     = "holder-gone"
	codeStopCompleted  = "stop-completed"
	codeUnitAbsent     = "unit-absent"
	codeCompetingUnit  = "competing-unit"
	codeUnitNotRunning = "unit-not-running"

	codeOrphanCoordinator = "orphan-coordinator"
	codeParentEpochEnded  = "parent-epoch-ended"
	codeParentUnreadable  = "parent-unreadable"
)

// reasonNoPendingIntent is the reason of a role that SettleLaunch left alone.
const reasonNoPendingIntent = "no-pending-intent"

// SettleLaunch settles the pending launch intent of one role, by the rules
// Reconcile applies to a role with a pending intent and no other: the same
// code runs, under the role's lock, so it never overlaps a Start, a Stop or a
// Reconcile of the role. A unit with this epoch and intent is adopted; with no
// unit loaded and its control group proven empty the intent is abandoned and
// the role stopped once the late-start window and its margin have passed on
// the controller's clock (at once when the boot identity changed), and the
// role is left unknown, the intent pending, before; a loaded unit that is not
// this launch's is reported and, once the window has passed, the intent is
// abandoned, the role staying unknown. A role shown unknown with the reason
// registry-recovered keeps its state, as in Reconcile. A role with no pending
// intent, or run by the tmux-manager, is left as it is and reported so
// (reason no-pending-intent, action unchanged, or not-managed). The returned
// report says what was found, and Action is [ActionPending] while the window
// is open and [ActionUnavailable] when the manager did not answer. It starts
// nothing and sends no signal, ever.
//
// It returns [ErrNotReconciled] while the registry's cut journal has not been
// acted on by Reconcile, and an error only for a failure of the registry or of
// the context.
func (c *Controller) SettleLaunch(ctx context.Context, role leadrole.Role) (RoleReport, error) {
	rec := c.reg.Recovery()
	c.mu.Lock()
	cut := rec.Cut() && !c.recovered
	c.mu.Unlock()
	if cut {
		return RoleReport{Role: role, Kind: role.Kind(), Action: ActionUnchanged}, ErrNotReconciled
	}
	rr, _, err := c.reconcileRole(ctx, role, false, true)
	return rr, err
}

// LaunchDeadline is the time at which the late-start window and its margin
// since the launch intent have passed: the earliest time SettleLaunch can
// close an intent whose unit is not loaded (unless the boot changed).
func (c *Controller) LaunchDeadline(in leadregistry.Intent) time.Time {
	return c.windowClosesAt(in)
}

// settlePending settles the pending launch intent of e as far as the unit
// manager lets it: a unit with this epoch and intent is adopted; with the unit
// not loaded and its control group empty, the intent is abandoned and the role
// stopped once the late-start window has passed, and left unknown before; a
// loaded unit that is not this launch's is reported and, once the window has
// passed, the intent is abandoned. With keep set the shown state is never
// changed (a role whose record the registry lost a tail of is left to the
// owner). It never signals and never starts.
func (c *Controller) settlePending(ctx context.Context, e leadregistry.Entry, keep bool) (leadregistry.Entry, note, error) {
	in := *e.Intent
	st, err := c.mgr.Inspect(ctx, in.UnitName)
	if err != nil {
		return e, note{ActionUnavailable, "manager-unavailable", "inspect: " + err.Error()}, nil
	}
	closes := c.windowClosesAt(in)
	late := !c.clock.Now().Before(closes)
	if st.Loaded {
		if matchesIntent(st, &in) {
			e, err = c.reg.RecordOutcome(e.Role, in.ID, in.Epoch, leadregistry.OutcomeAdopted, evidenceOf(st))
			return e, note{ActionAdopted, "adopted", describeLoaded(st)}, err
		}
		n := note{ActionUnknown, foreignCode(st), describeLoaded(st)}
		if late {
			if e, err = c.reg.RecordOutcome(e.Role, in.ID, in.Epoch, leadregistry.OutcomeAbandoned, leadregistry.UnitEvidence{BootID: st.BootID}); err != nil {
				return e, n, err
			}
		}
		if !keep {
			e, err = c.setUnknown(e, n.reason, n.detail)
		}
		return e, n, err
	}
	if basis, ok := rebootedSince(in, st); ok {
		// The boot identity changed since the intent was journaled and the unit
		// is not loaded: a start request of the earlier boot is gone with it,
		// so there is nothing to wait for.
		if e, err = c.reg.RecordOutcome(e.Role, in.ID, in.Epoch, leadregistry.OutcomeAbandoned, leadregistry.UnitEvidence{BootID: st.BootID}); err != nil {
			return e, note{}, err
		}
		if keep {
			return e, note{ActionAbandoned, "launch-abandoned", basis}, nil
		}
		e, err = c.setProven(e, leadregistry.StateStopped, "launch-abandoned", "the launch intent was abandoned without a unit; the host rebooted", basis)
		return e, note{ActionStopped, "launch-abandoned", basis}, err
	}
	ab := c.proveGone(ctx, st, leadregistry.UnitEvidence{})
	switch ab.kind {
	case proofUnavailable:
		return e, note{ActionUnavailable, "manager-unavailable", ab.why}, nil
	case proofLacking:
		n := note{ActionUnknown, codeNotEmpty, ab.why}
		if !keep {
			e, err = c.setUnknown(e, n.reason, n.detail)
		}
		return e, n, err
	}
	if !late {
		n := note{ActionPending, codeUnsettled, fmt.Sprintf(
			"no unit is loaded for intent %s epoch %d, but its start request could still land until %s",
			in.ID, in.Epoch, closes.UTC().Format(time.RFC3339))}
		replyLost := e.Shown.State == leadregistry.StateUnknown && e.Shown.Reason.Code == codeReplyLost
		if !keep && !replyLost {
			e, err = c.setUnknown(e, n.reason, n.detail)
		}
		return e, n, err
	}
	basis := fmt.Sprintf("intent %s epoch %d journaled at %s, no unit loaded and the control group empty after the %s late-start window and its %s margin; %s",
		in.ID, in.Epoch, in.At.UTC().Format(time.RFC3339), c.window, c.margin, ab.text)
	if e, err = c.reg.RecordOutcome(e.Role, in.ID, in.Epoch, leadregistry.OutcomeAbandoned, leadregistry.UnitEvidence{BootID: st.BootID}); err != nil {
		return e, note{}, err
	}
	if keep {
		return e, note{ActionAbandoned, "launch-abandoned", basis}, nil
	}
	e, err = c.setProven(e, leadregistry.StateStopped, "launch-abandoned", "the launch intent was abandoned without a unit", basis)
	return e, note{ActionStopped, "launch-abandoned", basis}, err
}

// rebootedSince reports whether the intent was journaled on another boot than
// the one the manager reports for a unit that is not loaded, and gives the
// proof text. Without a boot identity on the intent, or one from the manager,
// there is no such proof and only the window settles the intent.
func rebootedSince(in leadregistry.Intent, st leadunit.State) (string, bool) {
	if st.Loaded || in.BootIdentity == "" || st.BootID == "" || in.BootIdentity == st.BootID {
		return "", false
	}
	return fmt.Sprintf("intent %s epoch %d journaled at %s on boot %s; the host now runs boot %s and unit %s is not loaded, so no start request of the intent can land",
		in.ID, in.Epoch, in.At.UTC().Format(time.RFC3339), in.BootIdentity, st.BootID, st.Unit), true
}
