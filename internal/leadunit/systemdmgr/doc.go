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
// arguments. A reply that names a job is success, and Start then inspects the
// unit once; the job completes later, so that state can be partial (see
// [Manager.Start]). Inspect is one "systemctl --user show --all" with a fixed
// property list. Signal checks the invocation, then runs "systemctl --user
// kill --kill-whom=all". Empty proves a control group empty from the control
// group file system and the manager, in the cases listed at [Manager.Empty].
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
// from a run; tests check them against canned output. The process of the unit
// gets, besides the explicit environment, what systemd adds (the environment
// block of the user manager, HOME, LOGNAME, USER, SHELL, INVOCATION_ID and
// the like); the explicit entries win on the same name. Signal checks the
// invocation and then kills by name, so a unit that restarts in between is hit
// (see [Manager.Signal]). An Empty answer is a fact about the instant it was
// read. The control group file system is read at the fixed mount
// "/sys/fs/cgroup" through internal/systemd; nothing proves that the mount is
// the manager's own beyond the identity checks those helpers make.
//
// # Status
//
// The package is provisional and unqualified: it has had no run against a real
// systemd user manager. The qualification harness in
// qualification/leadunit/cmd/leadunit-qualify is the explicit, opt-in way to
// make one; no test starts a unit.
package systemdmgr
