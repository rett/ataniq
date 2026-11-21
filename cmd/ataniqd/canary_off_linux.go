//go:build linux && !ataniq_canary

package main

import (
	"flag"

	"ataniq/internal/leaddaemon"
)

// This build has no canary mode and no scratch mode: no --canary or --scratch
// flag, no command, nothing to register.
// canary_linux.go has the same names for a build with the ataniq_canary tag.

// canaryUsage is the part of the usage text that only a tagged build has.
const canaryUsage = ""

// canaryFlags are the flags of run that only a tagged build has.
type canaryFlags struct{}

func (*canaryFlags) define(*flag.FlagSet) {}

// canaryRun is the part of run that reads the canary config. It does nothing
// here: the roots are the flags' or the defaults'.
func (a *app) canaryRun(*canaryFlags, string, string, *leaddaemon.Config) (applied bool, code int) {
	return false, -1
}

// canaryCommand handles the commands and options only a tagged build has.
func (a *app) canaryCommand([]string) (code int, ok bool) { return 0, false }
