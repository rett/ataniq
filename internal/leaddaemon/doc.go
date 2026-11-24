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
// unknown (launch-unsettled) with the reconcile shown as completed. Without
// scratch mode (below) nothing settles the intent until the daemon is
// restarted; in scratch mode a timer settles it when the window ends. Leads,
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
// canary.start looks, before it changes anything, at the folders that
// provisioning makes and the holder's unit needs before it has written
// anything: the role's runtime folder, the holder's state folder (the unit's
// working directory) and the folder of the role lock
// (<state root>/roles/<project>, where the holder makes the lock file but not
// the folder). Each must be a folder of the caller with mode 0700, and one
// that is not is a [CodeRefused] with a fixed text that names it; a start that
// went on would answer "starting" for a unit that ends at once, with the
// reason in the journal alone. A path that cannot be worked out from the config
// is refused like a missing folder, never skipped; the detail goes to the
// daemon's log and no path is in the client's message.
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
// # Scratch mode and a launch that is not settled
//
// In scratch mode ([Config].Scratch, ataniqd run --scratch) the
// daemon also settles a launch intent itself when its late-start window ends
// (lead_settle_linux.go). A lead that the registry shows unknown with the
// reason launch-unsettled or launch-reply-lost and its intent pending (a
// daemon killed in the middle of a start, or a start whose reply was lost) has
// a timer, armed when the daemon starts serving and after a start that ended
// with its reply lost. At the intent's deadline on the daemon's clock, the
// timer asks [leadlifecycle.Controller.SettleLaunch] to settle the one role,
// which applies the rules of Reconcile and no other: with no unit loaded, the
// control group empty and the window and its margin passed, the intent is
// abandoned and the lead is stopped with the proof (and leaves the
// max_running count); a unit that is loaded at the deadline leaves the lead
// unknown (the launch's own unit is adopted, another is reported and the
// intent abandoned) and is never signalled; a manager that does not answer or
// a control group that is not proven empty is tried again later, at growing
// waits (the lead's reason then reads control-group-not-proven-empty, which
// keeps the timer armed while the intent is pending). The timer never starts a unit. It takes the role's operation lock as
// a try-lock for one read of the registry, never across a call to the manager
// or the lifecycle, and the lifecycle's own lock for the role keeps its settle
// from overlapping a start or a stop. A lead shown unknown for a cut of the
// registry (registry-recovered) has no timer. A timer ends when the intent is
// no longer pending, on a proven stop, on a new start, at shutdown, where the
// daemon joins it like the ready observers, and when the lifecycle answers
// that the reconcile at start did not act on the registry's cut journal (one
// log line names the restart that settles the lead).
//
// # Scratch mode: the leads' replies
//
// In scratch mode the daemon also keeps each lead's written replies to the
// owner (its chat), read only, through package leadchat
// (chat_desk_linux.go). At start, with both locks held, it opens one chat
// store per lead in <state>/chat; a lead whose store cannot be opened has no
// chat and every other lead is untouched. Each lead's switch is the desk's,
// in memory: off or read, off for every lead whenever the daemon starts,
// effective at once, and checked on every list, read and scan. Three
// operations of their own, chat.list, chat.set and chat.purge
// (leadwire.ChatOperations, listed after the six), are the desk's on the
// Unix socket; chat.set refuses send (chat-send-not-built), a key longer
// than a seat's lead identifier holds (chat-key-unrepresentable) and a
// turn-on whose lead's quarantine has no free name (chat-quarantine-full),
// which chat.purge empties. While a lead's switch is read
// one scanner of its own takes its replies every [DefaultChatScanEvery]; a
// scanner ends when the switch is off, the scan stops the lead's chat (the
// reply folder was replaced, opened to others or held too much), the lead's
// store fails, or the daemon stops, where it is joined with the observers.
// Its log lines hold counts and fixed words only. Every define and every
// start of a lead first writes the text of its instruction file while the
// lead is proven stopped (provision.EnsureChat); an instruction file that is
// not private, or that changed while it was replaced, refuses the command
// (chat-folder). Neither looks at the reply folder, which the turn-on of the
// lead's chat makes.
//
// [LeadChat] (chat_port_linux.go) is the seat's view of the same chats, the
// chat port of the gate's seat.chat route: it fits gatehost.ChatPort by its
// method set, imports nothing of the approval packages but ownerwire, and
// reads only a view the scratch mode binds to it ([Scratch].Chat) once the
// stores are open; it answers chat-busy before that, after shutdown, for a
// lead whose chat the scan stopped or whose store failed, past
// [ChatCallsPerMinute] calls a minute, and on a panic. A lead whose switch is
// off is a bare row and chat-off. Nothing of it reaches the lifecycle, a
// holder, the command table, or the owner channel's service.
//
// # The owner connection
//
// A build with the ataniq_ownerchannel tag has one more thing, and no other
// build has any of it (the files owner_*_linux.go): [OwnerChannel], whose
// Serve method is the [Config].ServeHook of the daemon. The hook is the one
// place outside those files where the package knows of it: it is called once,
// with both locks held and the socket bound, and nothing in a release build
// sets it. With it set, the daemon builds the host's side of an approval card
// ([OwnerService], package ownerchannel's service in a real build), registers
// the operations owner.list, owner.show, owner.paths, owner.patch,
// owner.decider, owner.decide, owner.beat, owner.limit, owner.limit-set,
// owner.looked, the pairing and key operations (owner.pair-start,
// owner.pair-status, owner.pair-confirm, owner.pair-reject, owner.keys,
// owner.key-revoke and owner.keys-off) and the seat's seat.pair
// ([OwnerOperations]) through the client protocol's extension seam, on the same socket and the same protocol, "ataniq.client/1", and
// closes the service after the server has ended. A daemon without the hook
// registers none of them, whatever the tag.
//
// The extension seam does not tell an operation which connection it came from,
// and the owner channel needs to know (the decider is one connection, and the
// session of owner.decider belongs to it). The daemon therefore wraps the
// listener: every connection is known, and the goroutine that reads it, which
// is the one that serves its requests, is recorded against it. A request that
// cannot be tied to a connection is refused for every operation that needs one
// (owner_conn_linux.go).
//
// The decider's place goes to one connection at a time: the peer's user is the
// owner's (the server's own check, which the wrapper leaves as it is), its
// client string names the console (first word "ataniq-console"), and its
// process has a controlling terminal, which an injected [PeerInspector]
// finds ([ProcInspector], the real one, holds a pidfd for the peer, reads its
// state, start time and terminal from /proc, and refuses a process that
// started after the connection was accepted). A second console is read-only;
// a client with no terminal, one that does not name the console, and a peer
// that cannot be judged get no place. The session named by the first
// owner.decider is kept for the connection, a second one that names another is
// refused decision-invalid, and the service is told the connection's session,
// pid, terminal and client for every decision, which it writes into the
// decision record and its journal. A decision frame from a connection that is
// not the decider is refused not-decider and counted ([OwnerCounts]); the
// service is told too, so that its journal keeps the refusal. The beats of the
// decider reach the service. [UnescapingIntake] turns the lines of the intake's
// patch back to raw bytes before the service pages them.
//
// The pairing writes (start, confirm, reject, key-revoke, keys-off) are for the
// decider's connection only: from another they are refused not-decider before
// their params are read, and count against the same per-connection bound as a
// decision frame, past which the connection is closed (OwnerCounts). The
// service decides presence. Confirm and reject take the pending id of the
// credential the owner compared ([ownerwire.PairPendingParams], required), and
// the service refuses one that is not the credential pending now with
// pairing-changed, changing nothing. Each error of the service has a code of its own and
// a fixed text (keys-unavailable, keys-full, key-unknown, key-ambiguous,
// pairing-open, pairing-none, pairing-expired, pairing-changed,
// pairing-refused, credential-invalid); a cause that names a path is in the log and not in the
// answer, and the pairing code, which is in the result of owner.pair-start
// and nowhere else, is never logged. seat.pair is the seat app's call: it is
// answered only to a connection a seat source says is a seat, and no build has
// one, so it is refused "unavailable" to every connection before its params are
// read. The refused calls of a connection count against the same bound as its
// refused writes: the first [DecisionRefusalBound] are logged, the next closes
// the connection and is logged once, and no line is written for any after.
//
// The seat gate (seatgate_linux.go, in the same tagged build) is a second
// listener of the daemon: a TCP listener with pinned-key TLS for the seat app,
// opened only by run --owner-channel --seat-gate ADDR. It serves the pairing
// call and the reads of the cards through package gatehost and decides
// nothing; docs/lead-daemon.md describes it.
//
// The same-user limit of the socket holds here as everywhere: any process of
// the owner's user can say it is the console, and one with a terminal of its
// own is granted the place when it is free (owner-channel case OC20). Nothing
// here is qualified.
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
