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
`

func init() { usage += ownerUsage }

// ownerFlags are the flags of run that only a build with the tag has.
type ownerFlags struct{ set bool }

func (o *ownerFlags) define(fs *flag.FlagSet) {
	fs.BoolVar(&o.set, "owner-channel", false, "serve the owner connection (owner.* operations)")
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
	oc := &leaddaemon.OwnerChannel{
		Build: func() (leaddaemon.OwnerService, func() error, error) {
			svc, closer, err := build(stateDir)
			if err == nil && guard != nil {
				// The one line at start that says what the door is.
				fmt.Fprintf(a.stderr, "ataniqd: %s\n", guard.banner)
			}
			return svc, closer, err
		},
		Inspector: ownerInspector,
		Log:       func(format string, args ...any) { fmt.Fprintf(a.stderr, "ataniqd: "+format+"\n", args...) },
	}
	cfg.ServeHook = oc.Serve
	return -1
}
