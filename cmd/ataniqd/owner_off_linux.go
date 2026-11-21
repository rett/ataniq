//go:build linux && !ataniq_ownerchannel

package main

import (
	"flag"

	"ataniq/internal/leaddaemon"
)

// This build has no owner channel: no flag, nothing to register.
// owner_linux.go has the same names for a build with the ataniq_ownerchannel
// tag.

// ownerFlags are the flags of run that only a build with the tag has.
type ownerFlags struct{}

func (*ownerFlags) define(*flag.FlagSet) {}

// ownerRun is the part of run that registers the owner connection. It does
// nothing here.
func (a *app) ownerRun(*ownerFlags, *leaddaemon.Config) (code int) { return -1 }
