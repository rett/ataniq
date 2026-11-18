//go:build linux && ataniq_canary

package canaryroot

import (
	"path/filepath"
	"testing"
)

// A daemon started without XDG_STATE_HOME uses $HOME/.local/state/ataniq. When
// the caller's environment sets XDG_STATE_HOME and a HOME that differs from the
// account's, that folder is a candidate too, as are the environment's own and
// the account's.
func TestRootsHomeDiffersWithXDGStateHome(t *testing.T) {
	f := newFixture(t, true)
	otherHome := filepath.Join(f.base, "otherhome")
	xdg := filepath.Join(f.base, "xdg-state")
	mkdir(t, otherHome)
	mkdir(t, xdg)
	getenv := envOf(map[string]string{"HOME": otherHome, "XDG_STATE_HOME": xdg, "XDG_RUNTIME_DIR": f.runDir})

	homeDefault := filepath.Join(otherHome, ".local", "state", "ataniq")
	for name, root := range map[string]string{
		"HOME's default":            homeDefault,
		"inside HOME's default":     filepath.Join(homeDefault, "x"),
		"containing HOME's default": filepath.Join(otherHome, ".local"),
		"the environment's":         filepath.Join(xdg, "ataniq"),
		"the account's":             f.stateDef,
	} {
		t.Run(name, func(t *testing.T) {
			wantRefusal(t, f.env.checkRoots(root, f.good[1], getenv), CodeDefaultRoot)
		})
	}
	if err := f.env.checkRoots(f.good[0], f.good[1], getenv); err != nil {
		t.Fatal(err)
	}
	// A relative HOME gives no candidate and breaks nothing.
	getenv = envOf(map[string]string{"HOME": "h", "XDG_STATE_HOME": xdg})
	if err := f.env.checkRoots(f.good[0], f.good[1], getenv); err != nil {
		t.Fatal(err)
	}
}
