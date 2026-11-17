// Package leadlifecycle is the lead daemon's logic for starting, stopping and
// reconciling lead roles. It sits on three other packages: leadregistry, the
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
// # What it keeps in memory
//
// Nothing that would be lost in a crash. The only state besides the registry
// is a lock per role, so that two operations on one role do not interleave
// (operations on different roles run in parallel), and a flag that says the
// registry's recovery report was already acted on in this process. Every
// state change goes through the registry before the next unit call that
// depends on it, and the launch intent is journaled before the first unit
// call.
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
//     abandoned only once the late-start window has passed with the unit still
//     not loaded and its control group empty; a unit that lands afterwards is
//     stale, is reported by Reconcile, and Reconcile never signals it. An
//     explicit Stop does: it ends a stale unit of ours that has the
//     record's epoch and intent or an older epoch, which is the owner's way
//     out when the window did not hold.
//   - A launch intent carries no boot identity: the registry records the boot
//     only with the unit evidence of an outcome. A pending intent with no unit
//     evidence therefore cannot be told apart by boot, and only the window
//     resolves it. A change to leadregistry that records the boot identity at
//     BeginLaunch would let a reboot settle such an intent at once.
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
// # Status
//
// The package is provisional and unqualified: its names and behaviour may
// change until a qualification run on the real manager and the real holder
// has covered it.
package leadlifecycle
