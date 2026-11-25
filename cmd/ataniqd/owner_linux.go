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
                    and the reads of the cards, and the guided new-project
                    interview, whose threads it keeps one file each in
                    owner/genesis/ of the state folder (a folder it makes,
                    mode 0700; it refuses a link, a folder that is not the
                    daemon user's and one open to writing). In the phone
                    trial alone (--trial, below) it also serves the seat's
                    lead pages from STAND-IN samples; a daemon with real
                    leads (--scratch, a build with both tags) serves their
                    replies instead, read only. With the seat
                    origin in the
                    owner-local file ataniq/owner-local/seat-origin of the
                    configuration folder (https:// and a lower-case domain
                    name, read once at start) it also serves a login and a
                    decision from the phone, each a passkey assertion with
                    user verification, on scratch state only; without one it
                    decides nothing. At start it prints one line with the
                    address, the gate's key fingerprint, the origin and what
                    it serves (or why it serves no login and decision), and
                    no pin; it stops the gate before it closes the service.
  seat-gate init    make the gate's key in owner/gate/ of the state folder and
                    print its fingerprint, the pin the seat is given. A key
                    already there is never overwritten.
  seat-gate pins    print the seat pins as the daemon reads them at start.

The desk reads the finished interviews of owner/genesis/ of the state folder,
whether or not the daemon runs, and writes nothing there:

       ataniqd genesis list [--state-dir DIR]
       ataniqd genesis handoff TAG [--state-dir DIR]

  genesis list      print one line per finished interview that holds a plan:
                    its tag (the first 6 digits of its id), its kind, the
                    plan's digest (12 digits) and the day it was finished. A
                    file that is not served is named on standard error.
  genesis handoff   print the hand-off of the plan of the interview whose id
                    starts with TAG (6 to 16 digits): comment lines and one
                    command line, "qargi project new NAME --methodology
                    gitflow", for you to run yourself in the folder where you
                    keep projects; it prints Qargi's plan and token, and
                    creating the project needs your own words. Nothing is run.
                    Only a software plan has a hand-off yet. Exit codes: 0
                    printed, 1 a failure, 2 usage, 3 refused (no interview or
                    more than one has the tag, or its kind has no hand-off).

The phone trial is a STAND-IN rig for scratch state only, in which the owner
tries the phone end to end without a lead, a mirror or a sandbox:

       ataniqd trial init --state-dir DIR
       ataniqd run --owner-channel --trial --state-dir DIR [--seat-gate ADDR]
       ataniqd trial submit --kind finish|branch-prune [--count N] [--socket PATH]

  trial init        write the trial's settings, owner/scratch.json of DIR
                    ("trial": true: a sample project, role and target, the
                    stand-in door, the three kinds of passkey, and no mirror,
                    drop folder or sandbox). DIR must not be, lie inside or
                    contain the account's default state or runtime folder;
                    settings already there are never overwritten.
  --trial           serve the trial's settings: sample cards only, made on
                    demand by trial submit. A decision goes through the real
                    path (the challenge, the passkey assertion and its
                    verification, the decision record, the door's checks and
                    the journal's evidence) and applies nothing: an approved
                    card ends not-applied, "applied: nothing (trial)". Only
                    with --owner-channel; the daemon refuses trial settings
                    without --trial and other settings with it, and a state or
                    runtime folder that is, lies inside or contains a default
                    folder of the account (so give --runtime-dir), and prints a
                    TRIAL line after the STAND-IN banner at start. With
                    --seat-gate the trial's gate also serves the seat's lead
                    pages (the capability chat, read only) from STAND-IN
                    sample leads and conversations held in memory: no lead is
                    read or reached, and no other gate serves these samples.
                    --trial is refused with every flag that gives the
                    daemon leads (--scratch, in a build that has it).
  trial submit      ask the trial daemon on the socket (default the runtime
                    folder's daemon.sock) for N sample cards (1 to 10, default
                    1) of a kind: finish, a sample feature finish, or branch-prune,
                    a sample branch prune, one at a time. It prints one line per
                    card, or per refusal. Exit codes: 0 every card was made, 1
                    a refusal or a failure, 2 usage, 20 no daemon, 21
                    unauthorized, 22 a protocol error (a daemon that is not a
                    trial's answers unsupported-operation).
`

func init() { usage += ownerUsage }

// ownerFlags are the flags of run that only a build with the tag has.
type ownerFlags struct {
	set bool
	// seatGate is the address of --seat-gate, when gateSet says it was
	// given.
	seatGate string
	gateSet  bool
	// trial is --trial: the phone trial (owner_trial_linux.go).
	trial bool
	// settings are --owner-settings and --check, which only a build with both
	// tags registers (scratch_settings_linux.go).
	settings settingsFlags
}

// settingsFlags are the flags of run that only a build with both tags has:
// --owner-settings DIR, the folder the owner channel reads its settings from
// in place of owner/ of the state folder, and --check, which checks and prints
// what the daemon would read and exits; with --check, --keys-prefix and
// --keys-since PREFIX check the state folder's key list alone
// (scratch_keys_linux.go).
type settingsFlags struct {
	dir        string
	dirSet     bool
	check      bool
	keysPrefix bool
	keysSince  string
	sinceSet   bool
}

// given reports whether any of the flags was given.
func (s settingsFlags) given() bool { return s.dirSet || s.check || s.keysPrefix || s.sinceSet }

// ownerSettingsNote is the line the daemon prints after the banner when the
// owner channel's settings come from the folder of --owner-settings: the file
// scratch.json in it (hostadapter.ScratchName) is read, and owner/scratch.json
// of the state folder is not.
func ownerSettingsNote(dir string) string {
	return "owner channel: settings read from " + leaddaemon.SafeText(dir) + "/scratch.json (--owner-settings); owner/scratch.json of the state folder is not read"
}

func (o *ownerFlags) define(fs *flag.FlagSet) {
	fs.BoolVar(&o.set, "owner-channel", false, "serve the owner connection (owner.* operations)")
	fs.Func("seat-gate", "serve the seat gate on ADDR, one IP address and port (with --owner-channel)", func(v string) error {
		o.seatGate, o.gateSet = v, true
		return nil
	})
	fs.BoolVar(&o.trial, "trial", false, "serve the phone trial's sample cards (with --owner-channel)")
	if scratchSeat != nil && scratchSeat.flags != nil {
		scratchSeat.flags(fs, &o.settings)
	}
}

// ownerServiceBuilder builds the host's side of a card in the daemon's state
// folder. The daemon calls it once, when it holds its locks and listens, and
// calls the returned function after the server has ended. The host adapter
// sets it; it is nil until then, and run --owner-channel is refused while it
// is.
var ownerServiceBuilder func(stateDir string) (leaddaemon.OwnerService, func() error, error)

// ownerBuildFrom is the host adapter's builder on settings read by load in
// place of owner/scratch.json of the state folder, which run --owner-settings
// uses (a build with both tags). The host adapter sets it with the builder.
var ownerBuildFrom func(stateDir string, load scratchLoad) (leaddaemon.OwnerService, func() error, error)

// ownerGuardSet is what the builder of a build brings with it: the checks of
// the state folder and the settings that run before the daemon starts, and the
// line the daemon prints once the service is built.
type ownerGuardSet struct {
	check func(stateDir string, getenv func(string) string) error
	// checkFrom, when set, is check on the settings load reads, for run
	// --owner-settings; a guard without it cannot take that flag.
	checkFrom func(stateDir string, load scratchLoad, getenv func(string) string) error
	// checkRuntime, when set, checks the runtime folder (the daemon's socket
	// is in it) before the daemon takes a lock.
	checkRuntime func(runtimeDir string, getenv func(string) string) error
	banner       string
	// note, when set, is a second line printed after the banner.
	note string
}

// ownerGuard is the host adapter's guard, set with the builder. A test that
// puts its own builder in place sets it to nil.
var ownerGuard *ownerGuardSet

// ownerInspector looks at the peer of a connection that asks to be the
// decider. A test replaces it, so that none needs a real terminal.
var ownerInspector leaddaemon.PeerInspector = leaddaemon.ProcInspector{}

// scratchSeatSet is what a build with both tags, ataniq_ownerchannel and
// ataniq_canary, adds to the owner channel (chat_scratch_linux.go): scratch
// mode with the seat gate.
type scratchSeatSet struct {
	// on reports whether the daemon runs a mode with leads: scratch mode
	// (run --scratch) or canary mode (run --canary).
	on func(cfg *leaddaemon.Config) bool
	// chat gives the gate the scratch leads' chat port when the daemon runs
	// scratch mode.
	chat func(cfg *leaddaemon.Config, gate *leaddaemon.SeatGate)
	// flags registers --owner-settings and --check.
	flags func(fs *flag.FlagSet, s *settingsFlags)
	// settings takes --owner-settings and --check, after the scratch config
	// is read: it returns the reader of the folder's settings, or a code to
	// end with (a refusal, or the end of --check).
	settings func(a *app, o *ownerFlags, cfg *leaddaemon.Config) (scratchLoad, int)
}

// scratchSeat is set by a build with both tags. It is nil in a build with the
// owner channel's tag alone, which has no scratch mode: its gate serves no
// lead's chat but the phone trial's samples.
var scratchSeat *scratchSeatSet

// leadsOn reports whether the daemon runs a mode with leads, scratch or
// canary mode.
func leadsOn(cfg *leaddaemon.Config) bool {
	return scratchSeat != nil && scratchSeat.on(cfg)
}

// trialLeadRefusal is what run says when --trial is given with a mode that
// has leads, --scratch or --canary.
const trialLeadRefusal = "ataniqd run: --trial excludes --scratch and --canary: a daemon with leads never serves the phone trial's samples\n"

// trialCheck refuses --trial with a mode that has leads (--scratch, or
// --canary, whose agent may be live), from the flags alone: before any
// config or setting is read, at the usage exit code. It returns a code to
// end with, or -1 to go on.
func (a *app) trialCheck(o *ownerFlags, c *canaryFlags) (code int) {
	if o.trial && c.leadMode() {
		fmt.Fprint(a.stderr, trialLeadRefusal)
		return leaddaemon.ExitUsage
	}
	return -1
}

// ownerRun sets the daemon's serve hook when --owner-channel was given. It
// returns a code to end with, or -1 to go on. The roots of cfg are final.
func (a *app) ownerRun(o *ownerFlags, cfg *leaddaemon.Config) (code int) {
	// The phone trial serves sample cards and, with a gate, STAND-IN sample
	// leads; a daemon with leads never does. trialCheck refused the flags
	// already; this holds it for the config too.
	if o.trial && leadsOn(cfg) {
		fmt.Fprint(a.stderr, trialLeadRefusal)
		return leaddaemon.ExitUsage
	}
	if !o.set {
		if o.settings.given() {
			fmt.Fprint(a.stderr, "ataniqd run: --owner-settings and --check need --owner-channel\n")
			return leaddaemon.ExitUsage
		}
		if o.gateSet {
			fmt.Fprint(a.stderr, "ataniqd run: --seat-gate needs --owner-channel\n")
			return leaddaemon.ExitUsage
		}
		if o.trial {
			fmt.Fprint(a.stderr, "ataniqd run: --trial needs --owner-channel\n")
			return leaddaemon.ExitUsage
		}
		return -1
	}
	build := ownerServiceBuilder
	guard := ownerGuard
	// The phone trial has its own builder and guard; the extension that
	// makes its sample cards is added once the service is built.
	var trial *trialRig
	if o.trial {
		trial = &trialRig{}
		build, guard = trial.build, trialGuard
	}
	if build == nil {
		fmt.Fprint(a.stderr, "ataniqd run: --owner-channel: this build cannot build the host's side of a card yet\n")
		return leaddaemon.ExitConfig
	}
	// --owner-settings (a build with both tags): the settings are read from
	// the folder it names, which is checked first, and owner/scratch.json of
	// the state folder is not read. --check ends here.
	if o.settings.given() {
		if scratchSeat == nil || scratchSeat.settings == nil {
			fmt.Fprint(a.stderr, "ataniqd run: --owner-settings: this build has no scratch mode\n")
			return leaddaemon.ExitUsage
		}
		load, code := scratchSeat.settings(a, o, cfg)
		if code >= 0 {
			return code
		}
		if guard == nil || guard.checkFrom == nil || ownerBuildFrom == nil {
			fmt.Fprint(a.stderr, "ataniqd run: --owner-settings: this build cannot read the owner channel's settings from another folder\n")
			return leaddaemon.ExitConfig
		}
		from, checkFrom := ownerBuildFrom, guard.checkFrom
		build = func(stateDir string) (leaddaemon.OwnerService, func() error, error) { return from(stateDir, load) }
		g := *guard
		g.check = func(stateDir string, getenv func(string) string) error { return checkFrom(stateDir, load, getenv) }
		g.note = ownerSettingsNote(o.settings.dir)
		guard = &g
	}
	stateDir := cfg.StateDir
	// The guards of the builder's own door come before the daemon takes a
	// lock or makes a socket: a refusal is a configuration the daemon refuses.
	if guard != nil {
		if err := guard.check(stateDir, a.getenv); err != nil {
			fmt.Fprintf(a.stderr, "ataniqd run: --owner-channel: %s\n", leaddaemon.SafeText(err.Error()))
			return leaddaemon.ExitConfig
		}
	}
	if guard != nil && guard.checkRuntime != nil {
		if err := guard.checkRuntime(cfg.RuntimeDir, a.getenv); err != nil {
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
		// The phone trial's gate, and no other, serves the STAND-IN chat; a
		// scratch daemon's gate serves the real leads' chat (a build with
		// both tags), and the two exclude each other (above).
		if trial != nil {
			trialChat(gate)
		} else if scratchSeat != nil {
			scratchSeat.chat(cfg, gate)
		}
	}
	oc := &leaddaemon.OwnerChannel{
		Build: withSeatGate(gate, func() (leaddaemon.OwnerService, func() error, error) {
			svc, closer, err := build(stateDir)
			if err == nil && guard != nil {
				// The one line at start that says what the door is.
				fmt.Fprintf(a.stderr, "ataniqd: %s\n", guard.banner)
				if guard.note != "" {
					fmt.Fprintf(a.stderr, "ataniqd: %s\n", guard.note)
				}
			}
			return svc, closer, err
		}),
		Inspector: ownerInspector,
		Log:       func(format string, args ...any) { fmt.Fprintf(a.stderr, "ataniqd: "+format+"\n", args...) },
	}
	cfg.ServeHook = oc.Serve
	if trial != nil {
		cfg.ServeHook = trial.serve(oc.Serve)
	}
	return -1
}
