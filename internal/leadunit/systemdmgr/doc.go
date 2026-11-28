// Package systemdmgr is the [leadunit.Manager] for the systemd user manager.
// It has no D-Bus library: it runs "busctl --user" for the one call that
// starts a unit and "systemctl --user" for everything else, through a small
// [Runner]. The default runner ([ExecRunner]) runs the two tools by absolute
// path, resolved once from a fixed list of directories and never from PATH,
// with the user-manager environment of internal/systemd, no shell, a bounded
// output and a deadline. Tests use a fake runner and run nothing.
//
// # What it does
//
// Start sends one StartTransientUnit call (mode "fail") that carries exactly
// [leadunit.FixedProperties] (TimeoutStopSec as the TimeoutStopUSec the bus
// knows), the description of [leadunit.Spec.Description], the working
// directory, the explicit environment and the command as ExecStartEx with the
// "no-env-expand" flag, so that systemd does not expand "$VAR" in the
// arguments. A spec with a Parent adds BindsTo, PartOf and After for it
// ([leadunit.BoundProperties]), each as a list of that one unit name ("as"),
// right after the fixed ones; a unit with no Parent gets none of them. A spec
// with Limits adds MemoryMax, MemorySwapMax and TasksMax
// ([leadunit.LimitProperties]), each an unsigned 64-bit number ("t"), after
// those; a spec without them gets none, and its call is exactly the call
// without limits. A reply
// that names a job is success, and Start then inspects the unit once; the job
// completes later, so that state can be partial (see [Manager.Start]). Inspect
// is one "systemctl --user show --all" with a fixed property list, which
// includes BindsTo: State.Parent is that unit when the list names exactly one
// service unit, and empty in any other case. Signal checks the invocation
// and the recorded control group identity, then signals the control group
// itself, pinned by an open directory (SIGKILL through its cgroup.kill, SIGTERM
// through pidfds of the processes it lists); it runs no "systemctl kill". Empty
// proves a control group empty from the control group file system and the
// manager, in the cases listed at [Manager.Empty].
//
// # Failure classes of Start
//
// A manager that cannot be reached before the request is sent
// ([leadunit.ErrUnavailable]), a bus refusal because the unit name is taken
// ([leadunit.ErrUnitExists]) and a context that is done before the call mean
// nothing was started. Everything else that goes wrong once the request may
// have been sent (a runner error, a timeout, a cancelled context, output that
// does not parse as a job, an inspection that fails after an accepted job)
// wraps [leadunit.ErrReplyLost], and the caller inspects and never starts
// again. The two refusals are recognised from the tool's text (busctl prints
// the bus error message, not its name), so a changed message degrades to
// [leadunit.ErrReplyLost], which is the safe direction. Start never retries.
//
// Start returns only after its tool has ended, even when the caller's context
// ends first (the wait is bounded by the per-call deadline plus one second),
// so busctl never sends the request after ErrReplyLost was returned. A message
// that busctl had already written to the bus can still be processed by the
// manager after that, a window of broker latency that this package cannot
// close: after a lost reply the caller inspects, and treats "not loaded" as
// final only after a short settle time and a second inspection.
//
// # What it does not prove
//
// The parts that need systemd (the argument rendering, the busctl and
// systemctl output formats, the error texts above, ExecStartEx and its flag,
// which need systemd 243 or newer, the cgroup placement of a unit) are written
// from the documentation and from the existing qualification host code, not
// from a run (that includes the "--" that ends busctl's options before the
// verb, so that no argument of the command is read as an option: whether
// busctl accepts it as written is for the qualification run to show; and what
// the three bound properties do, in particular what a start does when the
// parent is not active, and that BindsTo is printed as one space-separated
// list); tests check them against canned output. The process of the unit
// gets, besides the explicit environment, what systemd adds (the environment
// block of the user manager, HOME, LOGNAME, USER, SHELL, INVOCATION_ID and
// the like); the explicit entries win on the same name. Signal checks the
// invocation and then signals the control group the State recorded, by
// identity, so a unit that restarts in between is not hit (see
// [Manager.Signal]); its SIGKILL needs cgroup.kill (Linux 5.14), which was not
// checked on a live host. An Empty answer is a fact about the instant it was
// read. The control group file system is read at the fixed mount
// "/sys/fs/cgroup" through internal/systemd; nothing proves that the mount is
// the manager's own beyond the identity checks those helpers make.
//
// Limit of Empty. Empty proves that the control group of the unit, the one
// the State names by path and identity, has no process. It does not prove that
// every process the unit ever ran is gone: a process of the unit can move
// itself into another control group of the user's delegated subtree (the
// subtree the user manager owns and the user may write to) before its own
// group is removed, and an empty unit group says nothing of it. No check here
// sees such a process and none is attempted; it is a limit of what the user
// manager can prove for processes of the same user, to be known by whoever
// relies on "ended".
//
// # Status
//
// The package is provisional and unqualified: it has had no run against a real
// systemd user manager. The qualification harness in
// qualification/leadunit/cmd/leadunit-qualify is the explicit, opt-in way to
// make one; no test starts a unit.
package systemdmgr
