package link

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/durable"
	"ataniq/internal/checkpoint/link/internal/frame"
)

func TestOwnerRecoveryEndpoint(t *testing.T) {
	f := newFixture(t, "witness")
	k := key(t)
	f.pin = public(k)
	var err error
	f.s, err = NewServer(f.id, f.pin, f.s.home, f.slot)
	must(t, err)
	dir := t.TempDir()
	c := durable.Config{Identity: durable.Identity{Witness: "witness", Store: "store", Incarnation: "one", Epoch: 1}, Directory: filepath.Join(dir, "state"), KeyPath: filepath.Join(dir, "key"), Pin: f.pin, HomePin: public(f.home)}
	must(t, os.Mkdir(c.Directory, 0700))
	must(t, os.WriteFile(c.KeyPath, k.Seed(), 0600))
	w, err := durable.Create(c)
	must(t, err)
	must(t, w.Close())
	must(t, os.WriteFile(filepath.Join(c.Directory, "poisoned"), nil, 0600))
	w, err = durable.OpenForRecovery(c)
	must(t, err)
	t.Cleanup(func() { must(t, w.Close()) })
	id := identity(t, "owner")
	e := f.endpoint(testOwner(t, f.s, w, w.Administration(), id.Fingerprint()))
	owner, err := e.Administration(id)
	must(t, err)
	check := func(ch cp.ID, reason cp.Inhibition) {
		t.Helper()
		r, err := owner.Status(t.Context(), ch)
		must(t, err)
		s, err := cp.VerifyDirectoryStatus(c.Pin, 1, ch, r)
		must(t, err)
		if s.Sequence != 1 || s.Inhibition != reason {
			t.Fatal(s)
		}
	}
	check(cp.ID{1}, cp.StorageFailure)
	e.Epoch++
	stale, err := e.Administration(id)
	must(t, err)
	// The refusal is signed under the real epoch. A stale caller can't
	// authenticate it, but the mutation must not reach the backend.
	if err := stale.AcknowledgeStorageError(t.Context()); err == nil {
		t.Fatal("accepted stale epoch")
	}
	for _, kind := range []frame.Kind{frame.OwnerStatus, frame.OwnerAcknowledge} {
		if _, err := f.s.dispatch(t.Context(), w, nil, w, frame.New(kind, nil)); !errors.Is(err, cp.ErrRefused) {
			t.Fatal("home path accepted recovery", err)
		}
	}
	check(cp.ID{2}, cp.StorageFailure)
	must(t, owner.AcknowledgeStorageError(t.Context()))
	check(cp.ID{3}, cp.ServiceInhibited)
	if _, err := os.Stat(filepath.Join(c.Directory, "poisoned")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}
