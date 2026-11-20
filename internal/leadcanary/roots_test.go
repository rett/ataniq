//go:build linux && ataniq_canary

package leadcanary_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/canaryroot"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func wantRefusal(t *testing.T, err error, code canaryroot.Code) {
	t.Helper()
	var re *canaryroot.RefusedError
	if !errors.As(err, &re) {
		t.Fatalf("got %v, want a *canaryroot.RefusedError (%s)", err, code)
	}
	if re.Code != code {
		t.Fatalf("code %q (%v), want %q", re.Code, err, code)
	}
}

// TestCheckRootsIsTheSharedCheck shows that Config.CheckRoots refuses through
// canaryroot: the default roots, nested roots and a missing marker.
func TestCheckRootsIsTheSharedCheck(t *testing.T) {
	f := newFixture(t)
	cfg := f.config(t)

	// Fine roots with no marker: refused for the marker.
	wantRefusal(t, cfg.CheckRoots(envOf(nil)), canaryroot.CodeNoMarker)
	wantRefusal(t, cfg.CheckRoots(nil), canaryroot.CodeNoMarker)

	// With the marker for the project, the check passes.
	if err := canaryroot.WriteMarker(f.state, cfg.Role.Project()); err != nil {
		t.Fatal(err)
	}
	if err := cfg.CheckRoots(envOf(nil)); err != nil {
		t.Fatalf("roots with a marker: %v", err)
	}

	// A marker of another project is not this one's.
	other := cfg
	other.Role = mustRole(t, "throwaway-two/lead")
	wantRefusal(t, other.CheckRoots(envOf(nil)), canaryroot.CodeBadMarker)

	// The default state root, by the environment, is refused even with a
	// marker in it.
	home := filepath.Join(f.dir, "home")
	defState := filepath.Join(home, ".local", "state", "ataniq")
	if err := os.MkdirAll(defState, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := canaryroot.WriteMarker(defState, cfg.Role.Project()); err != nil {
		t.Fatal(err)
	}
	def := cfg
	def.StateRoot = defState
	wantRefusal(t, def.CheckRoots(envOf(map[string]string{"HOME": home})), canaryroot.CodeDefaultRoot)

	// So is the default runtime root, and a root that holds one.
	run := filepath.Join(f.dir, "run")
	defRuntime := filepath.Join(run, "ataniq")
	if err := os.MkdirAll(defRuntime, 0o700); err != nil {
		t.Fatal(err)
	}
	def = cfg
	def.RuntimeRoot = defRuntime
	wantRefusal(t, def.CheckRoots(envOf(map[string]string{"XDG_RUNTIME_DIR": run})), canaryroot.CodeDefaultRoot)
	def.RuntimeRoot = run
	wantRefusal(t, def.CheckRoots(envOf(map[string]string{"XDG_RUNTIME_DIR": run})), canaryroot.CodeDefaultRoot)

	// Roots that nest.
	nest := cfg
	nest.RuntimeRoot = filepath.Join(f.state, "runtime")
	wantRefusal(t, nest.CheckRoots(envOf(nil)), canaryroot.CodeNestedRoot)
	nest.RuntimeRoot = f.state
	wantRefusal(t, nest.CheckRoots(envOf(nil)), canaryroot.CodeNestedRoot)
}

// TestCheckRootsRefusesTheAccountsDefaultsWithoutAnEnvironment shows that the
// check does not depend on the environment: with no variable set the
// account's own default folders are still refused.
func TestCheckRootsRefusesTheAccountsDefaultsWithoutAnEnvironment(t *testing.T) {
	f := newFixture(t)
	cfg := f.config(t)
	acct := filepath.Join("/run/user", itoa(os.Geteuid()), "ataniq")
	cfg.RuntimeRoot = acct
	wantRefusal(t, cfg.CheckRoots(envOf(nil)), canaryroot.CodeDefaultRoot)
	cfg.RuntimeRoot = filepath.Dir(acct)
	wantRefusal(t, cfg.CheckRoots(envOf(nil)), canaryroot.CodeDefaultRoot)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

func TestCheckRootsRefusesAProjectWithoutThePrefix(t *testing.T) {
	f := newFixture(t)
	cfg := f.config(t)
	cfg.Role = mustRole(t, "demo/lead")
	wantRefusal(t, cfg.CheckRoots(envOf(nil)), canaryroot.CodeNotThrowaway)
}

// TestReadConfigDoesNotCheckTheRoots shows the split: reading the file is
// separate from the root check the daemon calls before it listens.
func TestReadConfigDoesNotCheckTheRoots(t *testing.T) {
	f := newFixture(t)
	cfg := f.config(t)
	if _, err := leadcanary.ReadConfig(f.writeConfig(t, cfg)); err != nil {
		t.Fatalf("a config with no marker in its state root reads fine: %v", err)
	}
}
