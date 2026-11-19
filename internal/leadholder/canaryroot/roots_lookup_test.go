//go:build linux && ataniq_canary

package canaryroot

import (
	"os"
	"path/filepath"
	"testing"
)

// A lookup error other than "does not exist" refuses the root. Here a folder
// that cannot be searched hides the rest of the path: treating the error like
// a missing folder would anchor the root at the locked folder and skip the
// identity of what lies behind it.
func TestRootsRefusedWhenNotSearchable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root searches any folder")
	}
	f := newFixture(t, true)
	locked := filepath.Join(f.base, "locked")
	mkdir(t, filepath.Join(locked, "x"))
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	wantRefusal(t, f.state(filepath.Join(locked, "x", "y")), CodeDefaultRoot)
	wantRefusal(t, f.runtime(filepath.Join(locked, "x", "y")), CodeDefaultRoot)
}
