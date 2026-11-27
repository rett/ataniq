package leadregistry

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ataniq/internal/instanceid"
	"ataniq/internal/leadrole"
)

func TestInitBindsLegacyJournal(t *testing.T) {
	data, _ := readV1Base(t)
	dir := writeJournal(t, data)
	if err := os.Chmod(filepath.Dir(dir), 0700); err != nil {
		t.Fatal(err)
	}
	before := openReg(t, dir)
	want := snapshot(t, before)
	before.Close()

	id, err := InitInstance(filepath.Dir(dir), filepath.Base(dir), "sample desk")
	if err != nil {
		t.Fatal(err)
	}
	r := openReg(t, dir)
	if !sameEntries(snapshot(t, r), want) || r.Recovery() != (Recovery{}) {
		t.Fatal("migration changed journal state")
	}
	r.Close()
	path := filepath.Join(dir, FileName)
	bound, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	head, _, _ := bytes.Cut(bound, []byte("\n"))
	if !bytes.Contains(head, []byte(id.Authority)) || !bytes.Contains(head, []byte(id.Incarnation)) || bytes.Contains(bound, []byte(id.Label)) {
		t.Fatal("incorrect binding in migrated journal")
	}
	if again, err := InitInstance(filepath.Dir(dir), filepath.Base(dir), ""); err != nil || again != id {
		t.Fatalf("retry: %+v %v", again, err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(bound, after) {
		t.Fatal("retry changed the journal")
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir, Options{}); !errors.Is(err, ErrUnbound) {
		t.Fatalf("ordinary open adopted legacy file: %v", err)
	}
}

func TestForeignInstanceRefusedBeforeTailRepair(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	id, err := InitInstance(root, "registry", "")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "registry")
	path := filepath.Join(dir, FileName)
	other := filepath.Join(t.TempDir(), "other")
	foreign, err := InitInstance(other, "registry", "")
	if err != nil {
		t.Fatal(err)
	}
	for name, binding := range map[string]instanceid.Binding{
		"authority":   {Authority: foreign.Authority, Incarnation: id.Incarnation},
		"incarnation": {Authority: id.Authority, Incarnation: foreign.Incarnation},
		"copied":      foreign.Binding,
	} {
		t.Run(name, func(t *testing.T) {
			header, _, err := encodeLine(genesis, 0, (&fakeClock{}).Now(), kindHeader, leadrole.Role{}, journalHeader(binding))
			if err != nil {
				t.Fatal(err)
			}
			if name == "copied" {
				header, err = os.ReadFile(filepath.Join(other, "registry", FileName))
				if err != nil {
					t.Fatal(err)
				}
			}
			data := append(header, "partial"...)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir, Options{}); !errors.Is(err, ErrForeignInstance) {
				t.Fatalf("open: %v", err)
			}
			if _, err := InitInstance(root, "registry", ""); !errors.Is(err, ErrForeignInstance) {
				t.Fatalf("init: %v", err)
			}
			b, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(b, data) {
				t.Fatal("foreign journal changed")
			}
		})
	}
	if err := os.Remove(filepath.Join(root, instanceid.FileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := InitInstance(root, "registry", ""); !errors.Is(err, instanceid.ErrMissing) {
		t.Fatalf("init after key loss: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, instanceid.FileName)); !os.IsNotExist(err) {
		t.Fatal("lost key was replaced")
	}
}

func TestMigrationPreservesCorruptOriginal(t *testing.T) {
	data, _ := readV1Base(t)
	data = append(data, "damaged\n"...)
	dir := writeJournal(t, data)
	if err := os.Chmod(filepath.Dir(dir), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := InitInstance(filepath.Dir(dir), filepath.Base(dir), ""); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("init: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil || !bytes.Equal(data, got) {
		t.Fatal("original journal changed")
	}
}

func TestMigrationWaitsForClosedJournalAndKeepsTornTail(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	dir := filepath.Join(root, "registry")
	r, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InitInstance(root, "registry", ""); !errors.Is(err, ErrLocked) {
		t.Fatalf("init with writer: %v", err)
	}
	if _, err := instanceid.Load(root); !errors.Is(err, instanceid.ErrMissing) {
		t.Fatalf("init changed locked root: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, FileName), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("torn"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InitInstance(root, "registry", ""); err != nil {
		t.Fatal(err)
	}
	r = openReg(t, dir)
	if cut := r.Recovery(); cut.Lines != 1 || cut.Bytes != 4 {
		t.Fatalf("lost tail recovery: %+v", cut)
	}
}
