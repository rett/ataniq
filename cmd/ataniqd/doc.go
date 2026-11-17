// Command ataniqd is the lead daemon of Ataniq, in the form this feature has
// it: a read-only skeleton.
//
//	ataniqd run [--state-dir DIR] [--runtime-dir DIR]
//	ataniqd status [--socket PATH] [--json]
//	ataniqd version
//
// run takes the daemon lock in the state folder, opens the registry there,
// reconciles it once against the systemd user manager, and answers the
// client protocol's hello and status on a private socket until it gets
// SIGTERM or SIGINT. The state folder defaults to $XDG_STATE_HOME/ataniq,
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
// This build is read-only. It starts, stops and manages no unit and defines
// no lead; no flag does. Reconcile runs at start only, not on a timer. Leads,
// coordinators, the holder, the sandbox and the console are not part of it.
// It is provisional and unqualified: nothing in it has run against systemd.
// The package documentation of internal/leaddaemon says what is and is not
// shown and proved.
package main
