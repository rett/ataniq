//go:build linux && !ataniq_ownerchannel

package main

// This build has no seat gate: no --seat-gate flag (there is no
// --owner-channel either) and no seat-gate command. seatgate_linux.go has the
// same names for a build with the ataniq_ownerchannel tag.

// seatGateCommand handles the seat-gate command, which only a build with the
// tag has.
func (a *app) seatGateCommand([]string) (code int, ok bool) { return 0, false }
