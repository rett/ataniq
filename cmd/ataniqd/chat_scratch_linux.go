//go:build linux && ataniq_ownerchannel && ataniq_canary

package main

import "ataniq/internal/leaddaemon"

// This file composes the scratch leads' chat with the seat gate, in a build
// with both tags (make scratch-seat-build): run --scratch CONFIG
// --owner-channel --seat-gate ADDR gives the gate the daemon's chat port,
// leaddaemon.LeadChat, which the scratch mode binds to its leads' chat once
// their stores are open. The port is a read-only view of the replies the
// leads wrote to their outboxes, each lead off until the desk turns it on
// (ataniqd chat set KEY read). The phone trial's STAND-IN port is never given
// to such a gate: run refuses --trial with --scratch and with --canary, from
// the flags, before any config is read (owner_linux.go).
//
// A build with the owner channel's tag alone has no scratch mode and leaves
// scratchSeat nil. The file sets it in its init, as owner_host_linux.go sets
// the builder, so that no file has to forbid the canary tag beside the owner
// channel's.

func init() {
	// --owner-settings and --check (scratch_settings_linux.go) belong to the
	// same build.
	scratchSeat = &scratchSeatSet{on: leadModeOn, chat: scratchChat, flags: defineSettingsFlags, settings: ownerSettingsRun}
	usage += scratchSeatUsage
}

// scratchSeatUsage is the part of the usage text that only a build with both
// tags has.
const scratchSeatUsage = `
In a build with both tags (ataniq_ownerchannel and ataniq_canary, make
scratch-seat-build) one daemon serves the scratch leads and the owner channel
with the seat gate together:

       ataniqd run --scratch CONFIG --owner-channel --seat-gate ADDR

  The roots are the scratch config's: the owner channel reads the non-trial
  settings owner/scratch.json of the config's state root, and the gate its key
  and pins in owner/gate/ of it. The gate then also serves the capability
  chat, read only: the replies the scratch leads wrote to their outboxes, of
  each lead whose chat the desk turned on (ataniqd chat set KEY read); every
  lead is off whenever the daemon starts. The gate's start line is followed by
  "seat gate: chat: replies the scratch leads wrote to their outboxes; read
  only". --trial is refused with --scratch and with --canary (exit 2, before
  any config is read), so a daemon with leads never serves the phone trial's
  samples.

       ataniqd run --scratch CONFIG --owner-channel --seat-gate ADDR
               --owner-settings DIR [--check]

  --owner-settings DIR
                    read the owner channel's settings from DIR/scratch.json
                    in place of owner/scratch.json of the state root, which
                    is then not read (a phone trial's settings stay where
                    they are); the journal, the list of keys and the gate's
                    key and pins are the state root's all the same, read and
                    never moved. Only with --scratch and --owner-channel. DIR
                    sits beside the state root and the runtime root, not
                    inside either. It is resolved through every link and
                    opened by its handle with no link followed, and the
                    daemon refuses to start (exit 10), naming why, when DIR
                    is not a private folder of the daemon's user or when it
                    is, lies in or holds: owner/ of the state root (the
                    trial's settings folder); the state root or the runtime
                    root; a path a lead's sandbox reaches (each lead's
                    folder, mirror, mailbox, published folder and holder
                    runtime folder, the agent install, the probe, the host
                    paths every sandbox binds); or owner/gate (the gate's
                    identity). Each is decided on the resolved path and on
                    the identities of the folders, so a bind mount of a
                    guarded folder itself, given as DIR, is caught. The
                    checks guard against mistakes, not attackers: a mount
                    layout the owner or root makes (a bind mount of a folder
                    within a guard, nested mounts, a mount made after the
                    check, another namespace) is not looked at; a lead
                    cannot make a mount on the host. Once read, the
                    settings must not name a drop folder, mirror, work
                    folder or sandbox template that DIR is, lies in or
                    holds. scratch.json is opened beneath the folder's
                    handle without following a link and must be a private
                    regular file with one name that is not the key list,
                    the journal, a file of the gate or the trial's settings,
                    and not a phone trial's; its bytes are read once, from
                    the descriptor that was checked, and decoded from those
                    bytes. The start prints
                    "owner channel: settings read from DIR/scratch.json
                    (--owner-settings)".
  --check           with --owner-settings: make every check the daemon makes
                    before it takes a lock (the folder's refusals, the
                    settings, the roots, the root marker, the gate's address,
                    key and pins, the socket paths), print one line each,
                    "pass" or "FAIL" with the reason, then what the daemon
                    would read, and exit: 0 when nothing is refused, 10
                    otherwise. It binds, locks, makes and starts nothing.
`

// scratchChatNote is the line the gate writes after its start line when it
// serves the scratch leads' chat.
const scratchChatNote = "chat: replies the scratch leads wrote to their outboxes; read only"

// leadModeOn reports whether the daemon runs a mode with leads: scratch mode
// (run --scratch CONFIG) or canary mode (run --canary CONFIG), whose agent
// may be live.
func leadModeOn(cfg *leaddaemon.Config) bool { return cfg.Scratch != nil || cfg.Canary != nil }

// scratchChat gives the gate the scratch leads' chat port when the daemon runs
// scratch mode: a new [leaddaemon.LeadChat] in cfg.Scratch.Chat, which the
// scratch mode binds once its leads' stores are open and unbinds when it
// stops (it answers chat-busy until then and after), and the same port to the
// gate, with its note. It does nothing without scratch mode or without a gate.
func scratchChat(cfg *leaddaemon.Config, gate *leaddaemon.SeatGate) {
	if cfg.Scratch == nil || gate == nil {
		return
	}
	if cfg.Scratch.Chat == nil {
		cfg.Scratch.Chat = leaddaemon.NewLeadChat()
	}
	gate.Chat = cfg.Scratch.Chat
	gate.ChatNote = scratchChatNote
}
