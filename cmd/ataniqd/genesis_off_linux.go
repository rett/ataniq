//go:build linux && !ataniq_ownerchannel

package main

// This build has no guided interview (it has no seat gate to serve one) and no
// genesis command. genesis_linux.go has the same names for a build with the
// ataniq_ownerchannel tag.

// genesisCommand handles the genesis command, which only a build with the tag
// has.
func (a *app) genesisCommand([]string) (code int, ok bool) { return 0, false }
