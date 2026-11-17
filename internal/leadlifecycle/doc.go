// Package leadlifecycle is the lead daemon's logic for starting, stopping and
// reconciling lead roles and the coordinators of their features. It sits on
// three other packages: leadregistry, the durable record; leadunit, the
// manager of one transient unit per role; and leadrole, the role names.
//
// # What it does
//
// A [Controller] has three operations.
//
// Start launches a role. It refuses unless the role is defined, run by
// ataniq and admitted by the registry (shown state desired, stopped or ended,
// no pending launch intent), and unless the [Preflight] passes. Before
// anything is journaled it applies the replacement rule: the unit name must
// not be loaded, and the control group of the previous epoch must be proven
// empty (the boot identity having changed is proof in itself). Only then does
// it journal the launch intent, build the command with a [HolderCommand], and
// call the manager's Start once. It never retries a Start and never waits for
// readiness. A start that succeeds records the outcome launched; the shown
// state stays starting. The shown state ready is never set by this package: it
// needs observations of the holder that this package does not have (the one
// exception is ClearStalled, below, which gives back a ready that an observer
// had set).
//
// Stop sets the desired state to stopped, asks the delivery side to quiesce
// through [Quiesce], settles a pending launch intent as far as it can, sends
// SIGTERM to the control group of the invocation it recorded, waits 2 s for
// the group to empty, sends SIGKILL if it did not, and then waits up to a
// bound for the proof: the unit not loaded (or loaded with another
// invocation) and the control group of the stopped invocation proven empty.
// With the proof the role is stopped, with a proof text naming the invocation
// and the control group; without it by the bound the role is unknown. Stop
// never signals a unit that is not ours, and never types into a session. A
// unit of ours that the record does not name is signalled only when its epoch
// is older than the registry's: that is a start request that landed late, and
// epochs only grow, so it cannot be newer work.
//
// Reconcile compares the registry with the manager for every role the daemon
// runs. It starts nothing and signals nothing, ever. It adopts a running unit
// of ours that the journal says is ours, resolves a role whose unit is gone
// when it can prove it (stopped after a reboot, ended after the holder went
// away) and shows unknown where it cannot. It returns a [Report] with one line
// per role: what it was, what it is, and why.
//
// # Coordinators
//
// A coordinator is a role of its own, one per feature of a project, with its
// own unit, epoch (the instance number) and evidence. Start launches it only
// under a live parent lead, which the registry enforces and Start checks first
// so that a refusal changes nothing (ErrParentNotLive wraps the registry's
// refusal), and passes the lead's unit name as [leadunit.Spec].Parent, so that
// the unit is bound to the lead's. The replacement rule applies to the
// coordinator's own unit and evidence exactly as to a lead's, and every reply
// lost, unit exists and failure path of a lead is the coordinator's too. The
// [HolderCommand] is the same interface; the role tells the kinds apart. A
// start that the manager takes without a unit ever being loaded because the
// parent unit is no longer live ends without a unit: the outcome failed is
// recorded and the coordinator is stopped with the proof of the absent unit, or
// unknown without it. A start that the manager refuses with a lost reply naming
// the missing parent is a lost reply like any other: the real manager cannot
// tell a refusal from a request that may still land, so nothing is assumed, the
// intent stays pending and the coordinator is unknown (ErrLaunchUncertain),
// until Reconcile or Stop proves the absence after the late-start window. A
// lead is not started while a coordinator recorded under an earlier epoch of
// it is not proven stopped (ErrCoordinatorNotStopped). Stop of a lead first stops each of its
// coordinators that is not proven stopped, one after the other, by the same
// sequence as for a lead (SIGTERM, 2 s, SIGKILL, the proof), each recorded in
// the registry, and then the lead itself. A coordinator that cannot be proven
// stopped is shown unknown, or left as recorded when the manager or the
// registry did not answer its stop, and is named in the error (a
// *[CoordinatorsError]); the
// lead is stopped all the same, and a later Start of the lead is refused until
// the coordinator is stopped with proof or the owner decides. The coordinators
// are listed once more after the lead's own stop, because one may have been
// started in between (the lock of the lead does not serialise a Start of its
// coordinator, which takes only its own); once the lead has left its live
// states the registry admits no new one, so the second listing is complete,
// and a coordinator found in it is stopped, or named, like the others (a
// coordinator instance the first pass named is not stopped again, but a later
// instance of the same role is). Stop of
// a lead returns nil only when it did the second listing: after the lead's
// proven stop the listing runs on a context of its own, which the caller's
// cannot end, so a caller that gave up in the meantime still has a coordinator
// of that gap named. When the lead's own stop failed (so that it may still be
// live and a coordinator may still be admitted under it) the result is an
// error in any case, and the listing is done on the caller's context if that
// still runs. A listing that fails partway (the caller's context ended, or the
// registry failed) still returns, in the error, the coordinators it had named. Stop of a
// coordinator stops only that coordinator. Because the units are bound, a
// coordinator may be stopping already when it is signalled; the manager's
// not-loaded and invocation-changed answers lead to the proof as for a lead.
// MarkStalled records that a coordinator has stopped making progress, an
// observation that a detector outside this package supplies; the method is the
// whole interface. ClearStalled returns the coordinator to its live state when
// the same instance is still running (the unit inspected has the recorded
// epoch, intent and invocation, and is running), and shows it unknown
// otherwise. The live state is the one it was stalled from (ready or
// degraded; starting, which only a launch can show, comes back as degraded),
// as MarkStalled recorded it in the reason; this package shows ready only by
// giving back what an observer had set. A stalled coordinator is never replaced
// by Reconcile or by Start: it is stopped (Stop, with proof, which is what
// [leadregistry.Registry.CoordinatorStopped] asks for the instance) and then
// started again as instance n+1. After a daemon restart Reconcile keeps a
// stalled coordinator stalled while its unit is live and ours.
//
// Reconcile treats a coordinator as it treats a lead, and also against its
// parent, which it settles first in the same call. A coordinator whose parent's
// epoch is no longer the one it recorded, or whose parent is not live (a
// parent shown starting whose own launch failed or was given up is not live,
// as for a start), has ended with its parent: with its unit proven absent it is ended (reason
// parent-epoch-ended, with the proof); with the unit still loaded it is
// reported orphan-coordinator and shown unknown, because Reconcile never
// signals, and an explicit Stop ends it. A parent that cannot be read from the
// registry (an error, not a parent that is not defined) is "cannot tell", not
// "live": the coordinator is reported unchanged (reason parent-unreadable),
// neither adopted nor ended, and Reconcile returns the registry's error.
// After a reboot it is stopped, as a
// lead is, and nothing is started. The lines of the report carry the kind of
// the role and, for a coordinator, its feature and parent.
//
// A coordinator has no seat, no approval and is no
// handoff target: nothing here gives it authority.
//
// # What it keeps in memory
//
// Nothing that would be lost in a crash. The only state besides the registry
// is a lock per role, so that two operations on one role do not interleave
// (operations on different roles run in parallel), and two flags: one says
// the registry's recovery report was already acted on in this process, and
// the other that the acknowledgement of it is still due, because the pass
// acted on every role but the acknowledgement was not written, so that a
// later pass in the same process writes it again without acting on the cut
// again. Every state change goes through the registry before the next unit
// call that depends on it, and the launch intent is journaled before the first
// unit call.
//
// The flags are a convenience of one process. What outlives a crash is the
// registry's own record: once Reconcile has acted on a cut of the journal for
// every role it journals the acknowledgement of the cut
// ([leadregistry.Registry.AcknowledgeRecovery], with a note saying what was
// done), so that a later daemon does not show healthy roles unknown again. A
// daemon that dies between acting and acknowledging, or that could not act on
// every role because the manager did not answer, leaves the cut
// unacknowledged, and the next daemon treats it again, conservatively. The
// registry reports an unacknowledged cut also after it is reopened: the report
// goes away only with the acknowledgement.
//
// # Where the registry's rules win
//
// The registry's transition table is stricter than the lifecycle text in a few
// places. Where it is, the package does what the table allows and stays
// conservative:
//
//   - A role shown unknown cannot return to starting, ready, degraded or
//     stopping. A reply-lost launch that is adopted later (by Reconcile, or by
//     Stop) is therefore recorded as adopted, but the role stays shown unknown
//     until it is stopped with proof or the owner decides. Stop does not show
//     stopping for an unknown role; it goes from unknown straight to stopped
//     or stays unknown.
//   - While a launch intent is pending, the registry refuses stopped, ended and
//     an owner decision. The pending intent of a lost reply (the unit is not
//     loaded) is therefore left pending, with the role shown unknown and the
//     reason launch-reply-lost. Reconcile (or a later Stop) closes it as
//     abandoned only once the late-start window and its margin have passed
//     with the unit still not loaded and its control group empty, or at once
//     when the boot changed (the next bullet); a unit that lands afterwards is
//     stale, is reported by Reconcile, and Reconcile never signals it. An
//     explicit Stop does: it ends a stale unit of ours that has the
//     record's epoch and intent or an older epoch, which is the owner's way
//     out when the window did not hold.
//   - A launch intent carries the boot identity. Every launch journals the boot
//     identity the manager reported before the intent
//     ([leadregistry.Registry.BeginLaunchWith]), and the intent uses it for
//     this: to tell whether the machine booted again since the intent. A pending
//     intent (a lost reply, or a daemon that died between the intent and the
//     outcome) whose unit is not loaded on another boot than that one is
//     closed as abandoned and the role stopped at once, without waiting for
//     the window: the boot changed, which is the proof. (For a role shown
//     unknown for a cut of the journal only the intent is closed, and the role
//     stays unknown.) An intent with no boot identity (one journaled by a
//     registry caller that gave none) or with no boot identity from the
//     manager is settled by the window only. The boot identity is read by the
//     replacement-rule Inspect, before the intent is journaled and before the
//     unit call; that is sound because one daemon process cannot outlive a
//     reboot, so the boot cannot change between the read and the call within
//     one process.
//
// # What it does not prove
//
// The package proves nothing about systemd, processes or control groups: it
// acts on what the [leadunit.Manager] answers, and the tests run against
// [leadunit.Fake], which is a model written from the contract. It does not
// read the lock files, does not look at the holder, and has no view of
// readiness or of a turn in progress. The late-start window is a heuristic, not
// a bound. The manager's call timeout limits how long the caller waits for a
// reply, not when systemd executes a request that is already on the bus, so a
// request can land after the window, even after a newer epoch has run. The
// window only decides when Reconcile stops waiting for a start that may still
// land; the stale-unit path (Reconcile reports it, Stop ends it) is what
// handles a request that lands later.
//
// The window is counted from the time the registry stamped on the launch
// intent and compared with the controller's clock, so the registry's Now and
// [Options].Clock must be one time source; the package cannot check it, and
// two sources that differ make the window shorter or longer than configured.
// The registry stamps the intent before the holder command is built and the
// request is sent, so the window counted from the send is shorter than
// configured; [Options].LateStartMargin (default 10 s) is added to cover that
// and a step of the wall clock. Across a restart only the wall clock exists,
// so a step of it (a time adjustment, a suspend and resume) can still close
// the window early or hold it open; the margin covers a small step only.
// A margin of zero means the default; [Options].NoLateStartMargin asks for none.
// The window and the margin are added to the intent's time one after the other
// and the end of the window is cut at 100 years, so that a large option cannot
// wrap around into the past and close the window at once.
//
// A unit is taken as the role's own from its description alone: the epoch and
// the intent that [leadunit.State] reads back from it. Any process of the same
// user can create a unit with such a description, so the description is not
// proof of origin, and this package has no other. Unit names are derived from
// the project (and feature) only, not from the Ataniq state folder, so two
// state folders of one user would share unit names and each would see the
// other's units as its own, with epochs that mean nothing to it. The daemon is
// one per user by assumption; nothing in this package enforces that (the
// daemon's own lock is another package's). An explicit Stop of a unit of ours
// with an older epoch than the registry's, which is how a start request that
// landed late is ended, therefore assumes one installation: with a second one a
// running unit of the other installation can look like such a stale unit, and
// Stop would signal it. The behaviour is as described; it is a limit, not a
// guard.
//
// # Status
//
// The package is provisional and unqualified: its names and behaviour may
// change until a qualification run on the real manager and the real holder
// has covered it.
package leadlifecycle
