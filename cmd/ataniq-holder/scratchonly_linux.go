//go:build linux && !ataniq_canary

package main

// This file is the counterpart, in a build without the ataniq_canary tag, of
// canary_linux.go: the command knows --scratch only. The flags of canary mode
// are not registered, so they are unknown flags, and nothing here can waive an
// assertion or give the service a deliverer.

// versionSuffix follows the version in the line of --version: nothing.
const versionSuffix = ""

// modeUsageText follows the usage text: nothing.
const modeUsageText = ""

// notScratchText is the refusal of a command line without --scratch.
const notScratchText = "this build can host only a scratch agent (--scratch): the sandbox is not built"

// modeFlags, modeConfig and canaryDeps are empty: there is no other mode.
type (
	modeFlags  struct{}
	modeConfig struct{}
	canaryDeps struct{}
)

// addModeFlags adds no flag.
func addModeFlags(*flagParser) {}

// modeOf refuses a command line without --scratch. The guard that keeps a real
// lead off an unsandboxed holder comes before any other check of the line.
func (p *flagParser) modeOf() (modeConfig, error) {
	if !p.scratch {
		return modeConfig{}, notScratch{}
	}
	return modeConfig{}, nil
}

// selectMode returns scratch mode, the only one.
func selectMode(config, deps, holderOut, int) (holderMode, int) { return scratchMode{}, exitOK }
