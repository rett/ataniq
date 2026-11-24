// Command ataniqd is the lead daemon of Ataniq, in the form this feature has
// it: a read-only skeleton.
//
//	ataniqd run [--state-dir DIR] [--runtime-dir DIR]
//	ataniqd status [--socket PATH] [--json]
//	ataniqd version
//
// run takes the daemon lock in the state folder and the socket lock in the
// runtime folder, opens the registry in the state folder, reconciles it once
// against the systemd user manager, and answers the client protocol's hello
// and status on a private socket until it gets SIGTERM or SIGINT. The state folder defaults to $XDG_STATE_HOME/ataniq,
// else $HOME/.local/state/ataniq; the runtime folder, which holds
// daemon.sock, to $XDG_RUNTIME_DIR/ataniq, with no default (and never a
// shared folder) when XDG_RUNTIME_DIR is not set. It refuses to run as root.
// Every start-up step that fails stops the daemon with its own exit code and
// one line on standard error; the codes are listed in package leaddaemon.
//
// status asks a running daemon for its status and prints one line per role
// and one for the daemon, or the status result as JSON with --json. The
// socket defaults to the runtime folder's, by the same rule. It exits 0 when
// the daemon answered, 20 when no daemon answers at the socket, 21 when the
// daemon refuses this user, and 22 for an answer that is not valid.
//
// A build with the ataniq_canary tag adds canary mode (canary_linux.go; every
// other build has canary_off_linux.go and none of it): "ataniqd run --canary
// CONFIG", whose roots are the config's (--state-dir and --runtime-dir are
// refused), "ataniqd canary define|start|ticket|inject|stop --config CONFIG",
// which calls the operation of the running daemon and prints its result as one
// JSON line, and "ataniqd --version", which names the tag. It also adds
// scratch mode, "ataniqd run --scratch CONFIG", and the desk's switch of the
// scratch leads' written replies, "ataniqd chat list --config CONFIG",
// "ataniqd chat set KEY off|read --config CONFIG" and "ataniqd chat purge KEY
// --config CONFIG", which call chat.list, chat.set and chat.purge of the
// running daemon and print the result as one JSON line.
// Package leaddaemon says what the daemon then does.
//
// A build with the ataniq_ownerchannel tag adds one flag to run
// (owner_linux.go; every other build has owner_off_linux.go and none of it):
// "ataniqd run --owner-channel", which serves the owner connection, the
// operations owner.* of the console's approvals, on the same socket and
// protocol. Without the flag the tagged build registers nothing. The host's
// side of a card is built in the daemon's state folder through
// ownerServiceBuilder, which the host adapter sets; until it does the flag is
// refused with the config exit code before anything is made. It combines with
// --canary. Package leaddaemon says what the daemon then does. With
// --seat-gate ADDR as well (seatgate_linux.go; seatgate_off_linux.go has the
// same names for every other build) the daemon also serves the seat gate, the
// pinned-key TCP link of the seat app, on ADDR once the service is built, and
// stops it before the service is closed; "ataniqd seat-gate init" makes the
// gate's key and "ataniqd seat-gate pins" lists the seat keys it lets in.
// With --trial as well (owner_trial_linux.go; owner_off_trial_linux.go has the
// same names for every other build) the daemon serves the phone trial, a
// STAND-IN rig whose settings "ataniqd trial init" writes: sample cards only,
// made on demand by "ataniqd trial submit" through the operation trial.submit,
// decided through the real decision path and applied to nothing; with
// --seat-gate its gate also serves seat.chat (the capability chat) from the
// STAND-IN sample leads of package chat/standin, held in memory. In a build
// with both tags, run --scratch CONFIG --owner-channel --seat-gate ADDR
// (chat_scratch_linux.go) gives the gate the scratch leads' read-only chat
// port, leaddaemon.LeadChat, instead: the replies the leads wrote to their
// outboxes, of each lead the desk turned on with "ataniqd chat set KEY read".
// --trial is refused with --scratch, so no other gate serves chat.
// "ataniqd genesis list" and "ataniqd genesis handoff TAG" (genesis_linux.go;
// genesis_off_linux.go has the same names for every other build) read the
// finished interviews in owner/genesis of the state folder through the thread
// store's read-only reader, whether or not the daemon runs, and print them or
// the hand-off of one software plan (package genesis/handoff): a command line
// for the owner to run himself. Nothing is written, run or sent.
//
// A build without the tag is read-only. It starts, stops and manages no unit
// and defines no lead; no flag does. Reconcile runs at start only, not on a timer. Leads,
// coordinators, the holder, the sandbox and the console are not part of it.
// It is provisional and unqualified: nothing in it has run against systemd.
// The package documentation of internal/leaddaemon says what is and is not
// shown and proved.
package main
