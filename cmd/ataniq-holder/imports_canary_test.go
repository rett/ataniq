//go:build linux && ataniq_canary

package main

// guardListFlags are the flags of go list for this build: the tag.
var guardListFlags = []string{"-tags", canaryTag}

// canaryRepoDeps are the packages of this repository that canary mode adds to
// the six, each named: the start sequence, the bridge, the root check, the
// role names, the packages of the lead sandbox the start sequence drives, and
// the packages of the injection library the bridge needs.
var canaryRepoDeps = []string{
	"ataniq/internal/leadholder/sandboxstart",
	"ataniq/internal/leadholder/injectbridge",
	"ataniq/internal/leadholder/canaryroot",
	"ataniq/internal/leadrole",
	"ataniq/internal/leadsandbox/profile",
	"ataniq/internal/leadsandbox/plan",
	"ataniq/internal/leadsandbox/mountpoints",
	"ataniq/internal/leadsandbox/assert",
	"ataniq/internal/leadsandbox/safefile",
	"ataniq/internal/leadsandbox/sandboxnet",
	// The injection library: the fence (inject), what it reads of a screen
	// (screen, title, idle) and the status protocol (osc7501).
	"ataniq/internal/inject",
	"ataniq/internal/inject/idle",
	"ataniq/internal/inject/osc7501",
	"ataniq/internal/inject/screen",
	"ataniq/internal/inject/title",
}

// guardRepoDeps is the list of this build: the six and the written-out list of
// canary mode.
func guardRepoDeps() []string {
	return append(append([]string(nil), scratchRepoDeps...), canaryRepoDeps...)
}
