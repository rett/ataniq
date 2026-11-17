//go:build linux

package intake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// failSync makes every folder sync fail until the test ends.
func failSync(t *testing.T) {
	t.Helper()
	old := syncFolder
	syncFolder = func(int) error { return unix.EIO }
	t.Cleanup(func() { syncFolder = old })
}

// TestStoreFolderSyncFailureIsAWarning: the file has its name, the record can
// be read, and the error says that the folder was not synced.
func TestStoreFolderSyncFailureIsAWarning(t *testing.T) {
	st, dir := newTestStore(t)
	failSync(t)
	rec, err := st.PutVerification(testProject, passResult(idOf('a')), testGates)
	if !errors.Is(err, ErrNotSynced) || errors.Is(err, ErrStore) {
		t.Fatalf("error = %v", err)
	}
	if rec.Tip != idOf('a') || !rec.passing() {
		t.Errorf("record: %+v", rec)
	}
	if !strings.Contains(err.Error(), "written") || !strings.Contains(err.Error(), "-verification-") {
		t.Errorf("the warning does not say what happened: %v", err)
	}
	if got := mustReadDir(t, dir); len(got) != 2 {
		t.Errorf("files: %v", got)
	}
	syncFolder = func(fd int) error { return unix.Fsync(fd) }
	all, err := st.Verifications(idOf('a'))
	if err != nil || len(all) != 1 {
		t.Errorf("%v, %d records", err, len(all))
	}
}

// TestIntegrateFolderSyncFailureKeepsTheRecord: the branch moved and the
// record was written, but not synced. The call returns the record with the
// warning, and not a *RecordError.
func TestIntegrateFolderSyncFailureKeepsTheRecord(t *testing.T) {
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	req := g.req(s.inspect())
	req.afterChecks = func() { failSync(t) }
	rec, err := Integrate(context.Background(), s.source(), req)
	var re *RecordError
	if !errors.Is(err, ErrNotSynced) || errors.As(err, &re) || errors.Is(err, ErrRecordMissing) {
		t.Fatalf("error = %v", err)
	}
	syncFolder = func(fd int) error { return unix.Fsync(fd) }
	g.requireMovedAndRecorded(rec)
}

// TestStoreWithTooManyNamesFailsClosed: when the folder has more than
// maxStoreEntries names, every listing fails, so every read and write fails,
// and Integrate fails before it moves a branch.
func TestStoreWithTooManyNamesFailsClosed(t *testing.T) {
	if testing.Short() {
		t.Skip("creates 100000 files")
	}
	s := flaggedScene(t)
	g := newRig(t, s)
	g.record(nil)
	req := g.req(s.inspect())
	for i := 0; i <= maxStoreEntries; i++ {
		f, err := os.Create(filepath.Join(g.dir, "x"+strconv.Itoa(i)))
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if _, err := g.store.Verifications(s.tip); !errors.Is(err, ErrStore) {
		t.Errorf("Verifications = %v", err)
	}
	if _, err := g.store.PutVerification(testProject, passResult(s.tip), testGates); !errors.Is(err, ErrStore) {
		t.Errorf("PutVerification = %v", err)
	}
	_, err := Integrate(context.Background(), s.source(), req)
	if !errors.Is(err, ErrStore) {
		t.Errorf("Integrate = %v", err)
	}
	if g.id(upstreamRef) != s.base {
		t.Error("the target moved")
	}
}
