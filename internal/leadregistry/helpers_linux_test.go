package leadregistry

import (
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"ataniq/internal/leadrole"
)

// fakeClock returns strictly increasing UTC times with nanosecond parts.
type fakeClock struct{ n int64 }

func (c *fakeClock) Now() time.Time {
	c.n++
	return time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC).Add(time.Duration(c.n) * 1500 * time.Millisecond)
}

func testOpts(seed int64) Options {
	c := &fakeClock{}
	return Options{Now: c.Now, Rand: rand.New(rand.NewSource(seed))}
}

func lead(t testing.TB, project string) leadrole.Role {
	t.Helper()
	r, err := leadrole.Lead(project)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func coord(t testing.TB, project, feature string) leadrole.Role {
	t.Helper()
	r, err := leadrole.Coordinator(project, feature)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// liveLead defines the lead as run by ataniq, launches it and shows it ready,
// and returns its epoch.
func liveLead(t testing.TB, r *Registry, role leadrole.Role) uint64 {
	t.Helper()
	if _, err := r.Define(role, ManagerAtaniq, goodProfile()); err != nil {
		t.Fatal(err)
	}
	in, err := r.BeginLaunch(role)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.RecordOutcome(role, in.ID, in.Epoch, OutcomeLaunched, UnitEvidence{InvocationID: "i"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.SetState(role, setState(StateReady, in.Epoch)); err != nil {
		t.Fatal(err)
	}
	return in.Epoch
}

func goodProfile() Requested {
	return Requested{
		Provider:       ProviderClaude,
		Version:        "2.1.0",
		Model:          "model-a",
		Effort:         "high",
		PermissionMode: "default",
		AccessMode:     "workspace",
	}
}

// regDir returns a folder path under t.TempDir that does not exist yet.
func regDir(t testing.TB) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "reg")
}

func openReg(t testing.TB, dir string) *Registry {
	t.Helper()
	r, err := Open(dir, testOpts(1))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// okf returns a function that fails the test unless a mutation succeeded.
func okf(t testing.TB) func(Entry, error) Entry {
	return func(e Entry, err error) Entry {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return e
	}
}

func stopProof(epoch uint64) StateChange {
	return StateChange{Epoch: epoch, State: StateStopped, Evidence: EvidenceObservation,
		Reason: Reason{Code: "unit-gone"}, Proof: "unit not loaded and control group empty"}
}

func setState(st State, epoch uint64) StateChange {
	return StateChange{Epoch: epoch, State: st, Evidence: EvidenceObservation, Reason: Reason{Code: "test", Detail: "detail"}}
}

// copyJournal copies the journal of dir into a fresh folder and returns it.
func copyJournal(t testing.TB, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	return writeJournal(t, b)
}

// writeJournal writes b as the journal of a fresh folder (mode 0700, file 0600).
func writeJournal(t testing.TB, b []byte) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "copy")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, FileName), b, 0o600); err != nil {
		t.Fatal(err)
	}
	return d
}

func journalSize(t testing.TB, dir string) int64 {
	t.Helper()
	st, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	return st.Size()
}

// snapshot returns the list of all roles of r.
func snapshot(t testing.TB, r *Registry) []Entry {
	t.Helper()
	l, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func sameEntries(a, b []Entry) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}
