// Package leadlifecycle is the lead daemon's logic for starting, stopping and
// reconciling lead roles and the coordinators of their features. It sits on three other packages: leadregistry, the
// durable record; leadunit, the manager of one transient unit per role; and
// leadrole, the role names.
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
// needs observations of the holder that this package does not have.
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
// start that the manager refuses, or takes without a unit ever being loaded,
// because the parent unit is no longer live ends without a unit: the outcome
// failed is recorded and the coordinator is stopped with the proof of the
// absent unit, or unknown without it. A lead is not started while a
// coordinator recorded under an earlier epoch of it is not proven stopped
// (ErrCoordinatorNotStopped). Stop of a lead first stops each of its
// coordinators that is not proven stopped, one after the other, by the same
// sequence as for a lead (SIGTERM, 2 s, SIGKILL, the proof), each recorded in
// the registry, and then the lead itself. A coordinator that cannot be proven
// stopped is shown unknown and named in the error (a *[CoordinatorsError]); the
// lead is stopped all the same, and a later Start of the lead is refused until
// the coordinator is stopped with proof or the owner decides. Stop of a
// coordinator stops only that coordinator. Because the units are bound, a
// coordinator may be stopping already when it is signalled; the manager's
// not-loaded and invocation-changed answers lead to the proof as for a lead.
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
//     manager is settled by the window only.
//
// # Coordinators
//
// The package does not handle coordinator roles yet. The registry, the unit
// specification ([leadunit.Spec], which can name a parent unit) and the status
// protocol do have coordinators, but this package does not use them:
//
//   - Start builds a unit with no parent: it never sets the parent of a
//     [leadunit.Spec]. It has no parent check of its own; the registry's
//     launch gate refuses a coordinator whose parent lead is not live, and
//     Start returns that refusal.
//   - Stop of a lead does not stop the coordinators of that lead. It handles
//     the one role it is given.
//   - Reconcile treats a coordinator record like any other role: it compares
//     the record with the unit of that role and applies the same rules, with no
//     regard for the parent lead.
//   - There is no handling of the stalled state: this package never sets it
//     and never returns a stalled role to a live state. Reconcile and Stop
//     treat it like any other live state and may move it to stopped, ended or
//     unknown.
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
// # Status
//
// The package is provisional and unqualified: its names and behaviour may
// change until a qualification run on the real manager and the real holder
// has covered it.
package leadlifecycle
