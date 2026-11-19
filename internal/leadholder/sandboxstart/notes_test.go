//go:build linux && ataniq_canary

package sandboxstart

import (
	"strings"
	"testing"

	"ataniq/internal/leadholder/ptyhost"
)

// A change of ptyhost that stops *ptyhost.Host from being a Child breaks this
// package's tests, not the holder.
var _ Child = (*ptyhost.Host)(nil)

// TestStartedKeepsTheBwrapNote shows the note of a bubblewrap version line that
// gives no release is kept in Started and not dropped.
func TestStartedKeepsTheBwrapNote(t *testing.T) {
	w := newWorld(t)
	w.sys.versions.Bwrap = "bubblewrap (no release here)"
	st, err := w.run_()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Notes) != 1 || !strings.Contains(st.Notes[0], "bubblewrap") {
		t.Errorf("notes = %q", st.Notes)
	}
	w2 := newWorld(t)
	st2, err := w2.run_()
	if err != nil {
		t.Fatal(err)
	}
	if len(st2.Notes) != 0 {
		t.Errorf("notes = %q on a readable release", st2.Notes)
	}
}
