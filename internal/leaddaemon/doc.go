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
// Apart from canary mode (below), which exists only in a build with the
// ataniq_canary tag, the daemon never starts, signals or stops a unit, at
// start-up, while it serves or at shutdown: it does not call Start or Stop of the controller,
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
// stops or defines a lead (canary mode, in a build with the tag, aside).
//
// The version the hello operation reports is [DefaultVersion]. The
// repository's VERSION file cannot be embedded from the command's folder
// (go:embed does not reach a parent folder, and moving the file is outside
// this feature), and the daemon reads no file at run time to learn it, so a
// build that wants the real number sets it with a linker flag on the
// command's variable.
//
// # Canary mode
//
// A build with the ataniq_canary tag adds one thing to the daemon, and nothing
// to any other build: [Config].Canary (the files canary_*_linux.go; in a
// build without the tag [CanaryConfig] is an empty struct and
// canary_off_linux.go does nothing). With Canary nil the tagged daemon is the
// skeleton this document describes: no extension is registered and no holder
// command is built. With it set, the daemon is for one throwaway lead, the
// lead of the canary config:
//
//   - at the config step, before anything is made, it validates the config
//     (every limit is required), runs the shared root check
//     ([leadcanary.Config.CheckRoots]: the default roots, nesting, the marker,
//     the prefix) and requires its two folders to be the config's roots; a
//     failure is a [StartupError] of [StepConfig];
//   - it gives the lifecycle the canary preflight, the canary holder command
//     (with its own lock file as the holder's daemon lock) and a Limits seam
//     that answers the config's three limits for the config's role;
//   - it opens <state root>/canary-evidence.jsonl, writes daemon-start once it
//     listens, and registers the extension of five mutating operations,
//     canary.define, canary.start, canary.ticket, canary.inject and
//     canary.stop ([CanaryOperations]); hello lists them and says mutations
//     true.
//
// Every operation takes strict params with the role, which must be the
// config's. The errors add two codes to the protocol's, [CodeRefused] and
// [CodeUnavailable]. A message is sent once per epoch: once the request frame
// is written to the holder's control socket the message is spent, and an answer
// that is lost is the uncertain result, not an error. A spent message stays
// spent when the daemon is restarted inside the epoch: when canary mode starts
// it reads back the inject-sent lines of the evidence, and an evidence file
// that it cannot read back line by line refuses the start, at [StepState]. The
// inject-sent line is written as soon as the frame is, before the wait for the
// holder's answer, so a daemon killed while it waits leaves the epoch spent. A
// line that cannot be written then is tried again after the wait; the message
// counts as sent either way, and the log says so when the evidence does not
// hold it. The ticket is written to the role's runtime folder for the console
// and is in no answer and no evidence line. No file of the package imports the
// injection tree.
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
// The package is provisional and unqualified: its names and behaviour may
// change until a qualification run has covered it. The daemon is Linux only,
// the package is not: it builds on every system, as do the daemon's other
// packages (the whole tree builds on macOS, not yet on Windows). On another
// system it is only paths.go, exit.go and doc.go, which carry no file name
// suffix because nothing in them is system specific
// (the default folders, the socket and lock paths, the exit codes). The
// daemon, the client and the status text are in the files with the _linux
// suffix, which only a Linux build compiles. The command builds elsewhere as
// a stub that imports nothing of this package.
package leaddaemon
