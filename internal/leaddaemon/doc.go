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
//  4. socket lock: make the runtime folder (mode 0700, its parent must exist)
//     and take the socket lock in it, <runtime>/daemon.sock.lock (leadlock),
//     before the registry is opened. A socket path that does not fit in a Unix
//     socket address is refused first, before the folder or the file is made.
//     The daemon lock is per state folder, so it cannot tell a live socket
//     from a stale one when two daemons with different state folders share a
//     runtime folder; the socket lock is per runtime folder. A daemon that
//     finds it held exits with the socket code ([ExitSocket]), says that
//     another daemon serves the runtime folder and shows the pid in the lock
//     as a hint. It has opened no registry and asked no manager by then, so a
//     refused daemon writes nothing to its journal. A lock file that an
//     earlier daemon left is no obstacle: the lock is the flock on it, not the
//     file.
//  5. registry: open <state>/registry/. A corrupt registry stops the daemon;
//     nothing is repaired.
//  6. manager: read the boot identity, build the unit manager (injected) and
//     the lifecycle controller.
//  7. reconcile: call [leadlifecycle.Controller.Reconcile] once. It starts
//     nothing and signals nothing. A manager that does not answer is not
//     fatal: the daemon serves the roles as the registry has them and shows
//     the reconcile as not completed. It does so whenever any call to the
//     manager failed during the reconcile (the daemon wraps the manager and
//     counts them; an answer such as "not loaded" is not a failure, see the
//     counter's comment for the exact set), when the reconcile ran out of
//     time (also in its last step), and for a manager that is known to be
//     unavailable ([UnavailableManager]) when the registry holds nothing to
//     ask it about, an empty one or one of tmux-managed roles only.
//  8. stale socket: with both locks held, remove a socket file an earlier
//     daemon left (clientapi.RemoveStale).
//  9. socket: listen on <runtime>/daemon.sock (clientapi.Listen).
//  10. serve until the signal source delivers a signal (SIGTERM or SIGINT in
//     the binary) or the context ends.
//  11. shutdown: stop accepting and wait for the connections for a bounded
//     time (the listener is closed, which unlinks the socket, under the
//     socket lock), then close the registry, release the socket lock, and
//     release the daemon lock last. When the server has not ended within that
//     time, [Run] returns a [StartupError] of [StepShutdown] ([ExitShutdown])
//     that says what is left, and closes the registry and releases both locks
//     for no one: a goroutine that may still run could use them, and all end
//     with the process.
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
// themselves are shown unknown with the reason registry-recovered. The one
// acknowledgement is the reconcile's: once it has acted on every role it
// acknowledges the cut ([leadlifecycle.Controller.Reconcile]), and then the
// reconcile is shown as completed unless something else made it incomplete. A
// cut stays unacknowledged, and the reconcile not completed, when the manager
// did not answer for a role, when a failure of the registry or the end of the
// context stopped the reconcile first, or when the acknowledgement could not be
// written. The next daemon start acts on the cut again and acknowledges it
// when it can.
//
// The wire type has no place either for the proof text of a stopped or ended
// state, for the unit evidence (invocation, control group and boot identity)
// or for the launch intent's boot identity and times. They stay in the
// registry and are not shown in this protocol version, so a client sees
// stopped or ended without the proof it rests on.
//
// # Not in this build
//
// The lifecycle controller needs a Preflight and a HolderCommand. This
// feature has no qualification record and no holder command line, so the
// daemon passes implementations that always refuse with [ErrNotAvailable]
// ("not available in this build"); they are never reached, because nothing
// calls Start. Reconcile runs once at start and is not repeated on a timer:
// a periodic reconcile is not part of this feature. A launch intent that is
// still pending inside its late-start window at that moment leaves the role
// unknown (launch-unsettled) with the reconcile shown as completed, and
// nothing settles the intent until the daemon is restarted. Leads,
// coordinators, the holder, the sandbox and the console are not part of it
// either, and no operation of the protocol or flag of the binary starts,
// stops or defines a lead.
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
// user's processes is the peer. The locks and the registry are as qualified as
// their packages, which is not at all yet. Every test runs against the
// in-memory leadunit.Fake and private sockets; nothing here has run against
// systemd.
//
// The package is Linux only, provisional and unqualified: its names and
// behaviour may change until a qualification run has covered it. It does not
// build for any other system: paths.go uses clientapi.SocketPath, which
// exists only in a Linux file, and exit.go and doc.go carry no file name
// suffix only because nothing in them is system specific. The command still
// builds elsewhere as a stub that imports nothing of this package.
package leaddaemon
