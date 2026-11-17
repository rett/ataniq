// Command leadunit-qualify is the explicit, opt-in harness that qualifies the
// real lead unit manager (internal/leadunit/systemdmgr) on a host with a
// systemd user manager. The lead runs it by hand; no test and no make target
// calls it.
//
// Without --run it prints what it would do and exits 0 without touching
// systemd. With --run --state-dir DIR it starts one transient user unit with
// a unique name, ataniq-qualify-<random>.service (never the name of a role),
// that re-executes this program as "leadunit-qualify child --ignore-term", a
// helper that ignores SIGTERM, and checks the manager against it: the start
// and the description read back; the child's arguments, among them one with
// $HOME and one with %h, reach it unchanged (the child records what it was
// started with before it says it is ready); a second start with the same name
// is refused; Inspect matches the start; a second unit, bound to the first
// (Spec.Parent: BindsTo, PartOf and After), starts and Inspect reads the first
// unit back as its parent; SIGTERM leaves the control group populated; after 2
// s SIGKILL empties it; Empty is then true and the unit is not loaded; the
// bound unit stops with it, with no signal sent to it, and Empty is true for
// its control group; and Empty is true for a fresh, never-used unit name, which
// is the one case that needs the user manager's own control group to be
// derived.
// It prints one JSON report with a line per check and an overall verdict, and
// exits non-zero when any check fails. It always cleans up its own unit and
// says what it did: it sends SIGKILL only to a unit whose description is this
// run's own epoch and intent (a unit of that name with another description, or
// one that is not transient, is left alone and reported), the same for the
// bound unit, waits for the unit's invocation id before it
// signals, reports "not sent" when nothing was sent, and reports when it could
// not prove the group empty.
//
// What it does not prove. A passing run says that these checks held once, on
// this host, for this unit, at this systemd version; it is not a
// qualification of the manager in general. It does not simulate a lost start
// reply (the report lists it as not attempted), a crash of the daemon, a
// reboot, a unit that is replaced under the same name, or a populated group
// that SIGKILL cannot empty. An empty control group of the unit proves that
// the unit's group is empty, not that every process the unit ever ran is gone:
// a process can move itself into another control group of the user's
// delegated subtree before its group is removed, and no check here sees that.
// It does not look at the unit's environment beyond what the start call sent.
// It needs the user manager of the calling user and reaches the system
// manager nowhere.
//
// The program is provisional and unqualified.
package main
