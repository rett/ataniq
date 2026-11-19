//go:build linux && ataniq_canary

package provision

import (
	"context"
	"errors"
	"os/user"
	"path/filepath"
	"testing"

	"ataniq/internal/leadcanary/provision/provisiontest"
	"ataniq/internal/leadholder/canaryroot"
)

// withRoots points the world's config at other roots, as the layout would
// have them, and writes it.
func withRoots(t *testing.T, w *provisiontest.World, state, runtime string) {
	t.Helper()
	w.Cfg.StateRoot, w.Cfg.RuntimeRoot = state, runtime
	w.Cfg.Provision.Mirror = filepath.Join(state, "mirrors", "throwaway-one.git")
	// Files that are not there: if the roots were not checked first, the run
	// would fail on these instead.
	w.Cfg.Provision.ManagedSettings = filepath.Join(w.Dir, "no-such-settings")
	w.Cfg.Provision.GuardHook = filepath.Join(w.Dir, "no-such-hook")
}

func refusedCode(t *testing.T, err error) canaryroot.Code {
	t.Helper()
	var r *canaryroot.RefusedError
	if !errors.As(err, &r) {
		t.Fatalf("error %v is not a canaryroot.RefusedError", err)
	}
	return r.Code
}

func TestTheDefaultRootsAreRefusedBeforeAnythingIsLookedAt(t *testing.T) {
	w := provisiontest.New(t)
	env := w.Getenv()
	xdgState, xdgRun, home := env("XDG_STATE_HOME"), env("XDG_RUNTIME_DIR"), env("HOME")
	other := filepath.Join(w.Dir, "elsewhere")
	for name, c := range map[string]struct {
		state, runtime string
		code           canaryroot.Code
	}{
		"state root is the XDG default":    {filepath.Join(xdgState, "ataniq"), other, canaryroot.CodeDefaultRoot},
		"state root is the HOME default":   {filepath.Join(home, ".local", "state", "ataniq"), other, canaryroot.CodeDefaultRoot},
		"state root inside the default":    {filepath.Join(xdgState, "ataniq", "sub"), other, canaryroot.CodeDefaultRoot},
		"state root holds the default":     {xdgState, other, canaryroot.CodeDefaultRoot},
		"runtime root is the XDG default":  {other, filepath.Join(xdgRun, "ataniq"), canaryroot.CodeDefaultRoot},
		"runtime root inside the default":  {other, filepath.Join(xdgRun, "ataniq", "x", "y"), canaryroot.CodeDefaultRoot},
		"runtime root holds the default":   {other, xdgRun, canaryroot.CodeDefaultRoot},
		"the roots are one folder":         {other, other, canaryroot.CodeNestedRoot},
		"the runtime root is in the state": {other, filepath.Join(other, "run"), canaryroot.CodeNestedRoot},
		"the state root is in the runtime": {filepath.Join(other, "state"), other, canaryroot.CodeNestedRoot},
	} {
		t.Run(name, func(t *testing.T) {
			w2 := provisiontest.New(t)
			withRoots(t, w2, c.state, c.runtime)
			err := Provision(context.Background(), w2.Cfg, Options{Getenv: env})
			if err == nil {
				t.Fatal("the roots were accepted")
			}
			if got := refusedCode(t, err); got != c.code {
				t.Fatalf("code %q, want %q (%v)", got, c.code, err)
			}
			for _, d := range []string{xdgState, xdgRun, home, other} {
				if exists(d) {
					t.Errorf("%s was made", d)
				}
			}
		})
	}
}

func TestTheAccountsOwnDefaultRootsAreRefusedWhateverTheEnvironmentSays(t *testing.T) {
	u, err := user.Current()
	if err != nil || !filepath.IsAbs(u.HomeDir) {
		t.Skip("no absolute home folder in the account")
	}
	w := provisiontest.New(t)
	empty := func(string) string { return "" }
	for name, roots := range map[string][2]string{
		"state":   {filepath.Join(u.HomeDir, ".local", "state", "ataniq"), filepath.Join(w.Dir, "run")},
		"runtime": {w.State, filepath.Join("/run/user", u.Uid, "ataniq")},
	} {
		t.Run(name, func(t *testing.T) {
			withRoots(t, w, roots[0], roots[1])
			err := Provision(context.Background(), w.Cfg, Options{Getenv: empty})
			if err == nil || refusedCode(t, err) != canaryroot.CodeDefaultRoot {
				t.Fatalf("Provision: %v", err)
			}
		})
	}
	// Nothing in the account's folders was touched by the refusals.
	if !exists(w.Dir) || exists(filepath.Join(w.Dir, "run")) {
		t.Fatal("the world changed")
	}
}

func TestANilGetenvReadsTheRealEnvironment(t *testing.T) {
	w := provisiontest.New(t)
	t.Setenv("XDG_STATE_HOME", filepath.Join(w.Dir, "xs"))
	withRoots(t, w, filepath.Join(w.Dir, "xs", "ataniq"), filepath.Join(w.Dir, "run"))
	err := Provision(context.Background(), w.Cfg, Options{})
	if err == nil || refusedCode(t, err) != canaryroot.CodeDefaultRoot {
		t.Fatalf("Provision: %v", err)
	}
}

func TestReplayOnRefusesTheDefaultRootsToo(t *testing.T) {
	w := provisiontest.New(t)
	env := w.Getenv()
	withRoots(t, w, filepath.Join(env("XDG_STATE_HOME"), "ataniq"), filepath.Join(w.Dir, "run"))
	err := ReplayOn(w.Cfg, Options{Getenv: env})
	if err == nil || refusedCode(t, err) != canaryroot.CodeDefaultRoot {
		t.Fatalf("ReplayOn: %v", err)
	}
}

func TestARootThatIsNotCleanOrAbsoluteIsRefused(t *testing.T) {
	for name, state := range map[string]string{"relative": "state", "unclean": "/tmp/a/../b"} {
		t.Run(name, func(t *testing.T) {
			w := provisiontest.New(t)
			w.Cfg.StateRoot = state
			if err := Provision(context.Background(), w.Cfg, Options{Getenv: w.Getenv()}); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}
