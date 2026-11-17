// Package leaddaemon assembles the lead daemon of this feature: a read-only
// skeleton that opens the registry, reconciles it once against the unit
// manager, and answers the client protocol's hello and status operations on a
// private Unix socket. cmd/ataniqd is the binary around it.
//
// # What it does
//
// [Run] takes the steps below in this order and stops at the first that
// fails, with an error that names the step ([StartupError]) and maps to its
// own exit code ([ExitCode]):
//
//  1. config: refuse to run as root, and check the two folders are clean
//     absolute paths.
//  2. state: create the state folder with mode 0700, or accept an existing
//     one only if it is a real folder, owned by the caller, and not
//     accessible to group or others.
//  3. lock: take the daemon lock, <state>/daemon.lock (leadlock). A second
//     daemon fails here, with the holder's pid shown as a hint and not as
//     proof.
//  4. registry: open <state>/registry/. A corrupt registry stops the daemon;
//     nothing is repaired.
//  5. manager: read the boot identity, build the unit manager (injected) and
//     the lifecycle controller.
//  6. reconcile: call [leadlifecycle.Controller.Reconcile] once. It starts
//     nothing and signals nothing. A manager that does not answer is not
//     fatal: the daemon serves the roles as the registry has them and shows
//     the reconcile as not completed. It does so also for a manager that is
//     known to be unavailable ([UnavailableManager]) when the registry holds
//     nothing to ask it about, an empty one or one of tmux-managed roles only.
//  7. stale socket: only now, with the lock held, remove a socket file an
//     earlier daemon left (clientapi.RemoveStale).
//  8. socket: listen on <runtime>/daemon.sock (clientapi.Listen).
//  9. serve until the signal source delivers a signal (SIGTERM or SIGINT in
//     the binary) or the context ends.
//  10. shutdown: stop accepting, wait for the connections for a bounded time,
//     close the registry, and release the lock last. When the server has not
//     ended within that time, [Run] returns a [StartupError] of [StepShutdown]
//     ([ExitShutdown]) that says what is left, and closes the registry and
//     releases the lock for no one: a goroutine that may still run could use
//     them, and both end with the process.
//
// The daemon never starts, signals or stops a unit, at start-up, while it
// serves or at shutdown: it does not call Start or Stop of the controller,
// and nothing in it can reach a unit manager's Start or Signal. A lead that
// runs when the daemon goes away keeps running.
//
// # The status
//
// The handler maps the registry's entries to the wire's: leads, the
// supervisor and coordinators with their feature, parent, parent epoch,
// instance and identity. The requested and the observed profile stay apart:
// a field that nobody observed is shown as not observed and never takes the
// requested value. The daemon block carries the pid, the start time, the
// registry revision, the boot identity, and the time and completeness of the
// reconcile.
//
// The wire type has no place for an unacknowledged registry recovery (a cut
// of the journal that Open made). The daemon therefore shows the reconcile
// as not completed while one is unacknowledged, which is the only place the
// wire leaves, and writes the reason to its log. A client cannot tell that
// reason from an unavailable manager by the daemon block; the roles
// themselves are shown unknown with the reason registry-recovered. Nothing in
// this build acknowledges a recovery.
//
// # Not in this build
//
// The lifecycle controller needs a Preflight and a HolderCommand. This
// feature has no qualification record and no holder command line, so the
// daemon passes implementations that always refuse with [ErrNotAvailable]
// ("not available in this build"); they are never reached, because nothing
// calls Start. Reconcile runs once at start and is not repeated on a timer:
// a periodic reconcile is not part of this feature. Leads, coordinators, the
// holder, the sandbox and the console are not part of it either, and no
// operation of the protocol or flag of the binary starts, stops or defines a
// lead.
//
// The version the hello operation reports is [DefaultVersion]. The
// repository's VERSION file cannot be embedded from the command's folder
// (go:embed does not reach a parent folder, and moving the file is outside
// this feature), and the daemon reads no file at run time to learn it, so a
// build that wants the real number sets it with a linker flag on the
// command's variable.
//
// # What it does not prove
//
// The same-user check of the socket is the protocol's: the peer's UID from
// SO_PEERCRED must equal the daemon's, which says nothing about which of the
// user's processes is the peer. The lock and the registry are as qualified as
// their packages, which is not at all yet. Every test runs against the
// in-memory leadunit.Fake and private sockets; nothing here has run against
// systemd.
//
// The package is Linux only, provisional and unqualified: its names and
// behaviour may change until a qualification run has covered it.
package leaddaemon
