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
	scratchSeat = &scratchSeatSet{on: leadModeOn, chat: scratchChat}
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
