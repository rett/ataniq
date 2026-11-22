//go:build linux && ataniq_ownerchannel

package main

import (
	"flag"
	"fmt"

	"ataniq/internal/leaddaemon"
)

// This file is the owner channel of the command: a build with the
// ataniq_ownerchannel tag. owner_off_linux.go has the same names for every
// other build.

// ownerUsage is the part of the usage text that only a tagged build has. The
// text above it is the release build's, byte for byte.
const ownerUsage = `
In this build (tag ataniq_ownerchannel) the text above holds only without
--owner-channel. With run --owner-channel the daemon also serves the owner
connection, the operations owner.* of the console's approvals, on the same
socket and the same protocol (ataniq.client/1). It is provisional and
unqualified.

       ataniqd run --owner-channel

  --owner-channel   register the owner.* operations; without it this build
                    registers none. The place of the one connection that may
                    decide goes to a console of the owner that has a
                    controlling terminal. The daemon refuses to start when
                    this build has no way to build the host's side of a card.

The host's side of a card is built for the scratch pass only, from the file
owner/scratch.json in the state folder (one project, its mirror, the folder
requests are dropped in), over the real journal in owner/ and the stand-in for
the planner's door, which holds nothing across a restart. The settings must say
"door": "stand-in" and name "roots", the scratch folders, one of which holds the
mirror; the daemon refuses (the configuration's exit code) a state folder, a
root or a mirror that is, lies inside or contains the account's default state
or runtime folder, and prints "owner channel: STAND-IN door, scratch state: no
real approval" at start. A request is a file
<request id>.request in the drop folder, and its reply is a file beside it.
The suite of a request's merge runs in the verification sandbox, as the
intake's verify does: the settings name "sandbox", the sandbox template file,
and "work", the folder the runs work in, both under a scratch root and outside
the state folder, and the daemon refuses to start without them. Starting
starts no sandbox; one starts when a request is verified. It is provisional and
unqualified.

The owner connection also pairs a phone: the console starts a pairing, the
seat's call presents the credential, and the owner confirms at the desk. The
credentials of the paired phones, the owner's deciding keys, are kept in the
file owner/keys.log of the state folder (private, append-only, checked when it
is read). A file that cannot be opened (damaged, not private, held elsewhere)
is never repaired or started over: the daemon says so once at start, serves the
approvals, and answers the pairing and key operations "keys-unavailable". On
the socket the seat's call, seat.pair, is refused "unavailable": no connection
of the socket is a seat.

       ataniqd run --owner-channel --seat-gate ADDR
       ataniqd seat-gate init [--state-dir DIR]
       ataniqd seat-gate pins [--state-dir DIR]

  --seat-gate ADDR  also serve the seat gate, the link of the seat app on the
                    authority host, on ADDR: one IP address of this host and
                    a port, such as 192.0.2.10:7443 or [2001:db8::10]:7443
                    (a host name, a wildcard, multicast or broadcast address
                    and a zero port are refused). Only with --owner-channel.
                    The gate is TLS 1.3 with pinned keys: its own key is in
                    owner/gate/ of the state folder, and the seat keys it lets
                    in are in owner/gate/seat-pins (a regular file of the
                    daemon's user, mode 0600, one fingerprint per line, # for a
                    comment, at most 4). Both are read at start, so a change of
                    the pins takes effect at the next start; the daemon refuses
                    to start without a key or a pin. It serves the pairing call
                    and the reads of the cards, and decides nothing. At start
                    it prints one line with the address and the gate's key
                    fingerprint, and no pin; it stops the gate before it closes
                    the service.
  seat-gate init    make the gate's key in owner/gate/ of the state folder and
                    print its fingerprint, the pin the seat is given. A key
                    already there is never overwritten.
  seat-gate pins    print the seat pins as the daemon reads them at start.
`

func init() { usage += ownerUsage }

// ownerFlags are the flags of run that only a build with the tag has.
type ownerFlags struct {
	set bool
	// seatGate is the address of --seat-gate, when gateSet says it was
	// given.
	seatGate string
	gateSet  bool
}

func (o *ownerFlags) define(fs *flag.FlagSet) {
	fs.BoolVar(&o.set, "owner-channel", false, "serve the owner connection (owner.* operations)")
	fs.Func("seat-gate", "serve the seat gate on ADDR, one IP address and port (with --owner-channel)", func(v string) error {
		o.seatGate, o.gateSet = v, true
		return nil
	})
}

// ownerServiceBuilder builds the host's side of a card in the daemon's state
// folder. The daemon calls it once, when it holds its locks and listens, and
// calls the returned function after the server has ended. The host adapter
// sets it; it is nil until then, and run --owner-channel is refused while it
// is.
var ownerServiceBuilder func(stateDir string) (leaddaemon.OwnerService, func() error, error)

// ownerGuardSet is what the builder of a build brings with it: the checks of
// the state folder and the settings that run before the daemon starts, and the
// line the daemon prints once the service is built.
type ownerGuardSet struct {
	check  func(stateDir string, getenv func(string) string) error
	banner string
}

// ownerGuard is the host adapter's guard, set with the builder. A test that
// puts its own builder in place sets it to nil.
var ownerGuard *ownerGuardSet

// ownerInspector looks at the peer of a connection that asks to be the
// decider. A test replaces it, so that none needs a real terminal.
var ownerInspector leaddaemon.PeerInspector = leaddaemon.ProcInspector{}

// ownerRun sets the daemon's serve hook when --owner-channel was given. It
// returns a code to end with, or -1 to go on. The roots of cfg are final.
func (a *app) ownerRun(o *ownerFlags, cfg *leaddaemon.Config) (code int) {
	if !o.set {
		if o.gateSet {
			fmt.Fprint(a.stderr, "ataniqd run: --seat-gate needs --owner-channel\n")
			return leaddaemon.ExitUsage
		}
		return -1
	}
	build := ownerServiceBuilder
	if build == nil {
		fmt.Fprint(a.stderr, "ataniqd run: --owner-channel: this build cannot build the host's side of a card yet\n")
		return leaddaemon.ExitConfig
	}
	stateDir := cfg.StateDir
	// The guards of the builder's own door come before the daemon takes a
	// lock or makes a socket: a refusal is a configuration the daemon refuses.
	guard := ownerGuard
	if guard != nil {
		if err := guard.check(stateDir, a.getenv); err != nil {
			fmt.Fprintf(a.stderr, "ataniqd run: --owner-channel: %s\n", leaddaemon.SafeText(err.Error()))
			return leaddaemon.ExitConfig
		}
	}
	// The seat gate's address, key and pins are read before the daemon takes
	// a lock too; the gate listens once the service is built.
	var gate *leaddaemon.SeatGate
	if o.gateSet {
		g, code := a.seatGatePrepare(o.seatGate, stateDir)
		if code >= 0 {
			return code
		}
		gate = g
	}
	oc := &leaddaemon.OwnerChannel{
		Build: withSeatGate(gate, func() (leaddaemon.OwnerService, func() error, error) {
			svc, closer, err := build(stateDir)
			if err == nil && guard != nil {
				// The one line at start that says what the door is.
				fmt.Fprintf(a.stderr, "ataniqd: %s\n", guard.banner)
			}
			return svc, closer, err
		}),
		Inspector: ownerInspector,
		Log:       func(format string, args ...any) { fmt.Fprintf(a.stderr, "ataniqd: "+format+"\n", args...) },
	}
	cfg.ServeHook = oc.Serve
	return -1
}
