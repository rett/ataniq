package leadlifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"ataniq/internal/leadregistry"
)

// journalBytes returns the size of the registry journal of the rig.
func (r *rig) journalBytes() int64 {
	r.t.Helper()
	st, err := os.Stat(filepath.Join(r.dir, leadregistry.FileName))
	if err != nil {
		r.t.Fatal(err)
	}
	return st.Size()
}

// TestExpectedStateCallers checks the callers that read an entry, decide and
// write: each passes what it read, so a newer same-epoch decision stands and
// the stale write appends nothing.
func TestExpectedStateCallers(t *testing.T) {
	t.Run("MarkStalled loses a race to a newer state", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		observed := r.get(coord) // what the caller read: starting
		r.setShown(coord, leadregistry.StateReady)
		newer := r.get(coord)
		size := r.journalBytes()
		_, err := r.ctl.markStalledFrom(observed, "no output")
		isErr(t, err, leadregistry.ErrStaleState)
		if got := r.journalBytes(); got != size {
			t.Fatalf("the refused write appended %d bytes", got-size)
		}
		if got := r.get(coord); got.Shown != newer.Shown {
			t.Fatalf("shown %+v, want the newer decision %+v", got.Shown, newer.Shown)
		}
	})
	t.Run("MarkStalled with an unchanged state records it", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		e, err := r.markStalled(coord, "no output")
		noErr(t, err)
		if e.Shown.State != leadregistry.StateStalled {
			t.Fatalf("shown %+v", e.Shown)
		}
	})
	t.Run("ClearStalled loses a race during the inspection", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		_, err := r.markStalled(coord, "x")
		noErr(t, err)
		var newer leadregistry.Entry
		var size int64
		r.aud.afterInspect = func(string) {
			r.aud.afterInspect = nil
			r.setShown(coord, leadregistry.StateUnknown)
			newer, size = r.get(coord), r.journalBytes()
		}
		_, err = r.clearStalled(coord)
		isErr(t, err, leadregistry.ErrStaleState)
		if got := r.journalBytes(); got != size {
			t.Fatalf("the refused write appended %d bytes", got-size)
		}
		if got := r.get(coord); got.Shown != newer.Shown {
			t.Fatalf("shown %+v, want the newer decision %+v", got.Shown, newer.Shown)
		}
	})
	t.Run("setUnknown refuses an entry that is no longer current", func(t *testing.T) {
		r := newRig(t)
		lead := r.define("alpha")
		r.mustStart(lead)
		r.setShown(lead, leadregistry.StateReady)
		observed := r.get(lead)
		r.setShown(lead, leadregistry.StateDegraded)
		newer := r.get(lead)
		size := r.journalBytes()
		_, err := r.ctl.setUnknown(observed, "test-unknown", "from an old observation")
		isErr(t, err, leadregistry.ErrStaleState)
		if got := r.journalBytes(); got != size {
			t.Fatalf("the refused write appended %d bytes", got-size)
		}
		if got := r.get(lead); got.Shown != newer.Shown {
			t.Fatalf("shown %+v, want the newer decision %+v", got.Shown, newer.Shown)
		}
		// From the current entry it is written.
		e, err := r.ctl.setUnknown(newer, "test-unknown", "from the current entry")
		noErr(t, err)
		if e.Shown.State != leadregistry.StateUnknown {
			t.Fatalf("shown %+v", e.Shown)
		}
	})
	t.Run("ClearStalled shows unknown from what it read and not over a newer state", func(t *testing.T) {
		r, _, coord := coordRig(t)
		r.mustStart(coord)
		_, err := r.markStalled(coord, "x")
		noErr(t, err)
		r.fake.Crash(unitOf(coord))
		var newer leadregistry.Entry
		var size int64
		r.aud.afterInspect = func(string) {
			r.aud.afterInspect = nil
			r.setShown(coord, leadregistry.StateDegraded)
			newer, size = r.get(coord), r.journalBytes()
		}
		_, err = r.clearStalled(coord)
		isErr(t, err, ErrInstanceChanged)
		isErr(t, err, leadregistry.ErrStaleState)
		if got := r.journalBytes(); got != size {
			t.Fatalf("the refused write appended %d bytes", got-size)
		}
		if got := r.get(coord); got.Shown != newer.Shown {
			t.Fatalf("shown %+v, want the newer decision %+v", got.Shown, newer.Shown)
		}
	})
}
