//go:build linux && !ataniq_ownerchannel

package main

// This build has no phone trial: no --trial flag (there is no
// --owner-channel either) and no trial command. owner_trial_linux.go has the
// same names for a build with the ataniq_ownerchannel tag.

// trialCommand handles the trial command, which only a build with the tag
// has.
func (a *app) trialCommand([]string) (code int, ok bool) { return 0, false }
