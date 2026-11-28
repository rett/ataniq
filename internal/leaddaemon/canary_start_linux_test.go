//go:build linux && ataniq_canary

package leaddaemon

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/clientapi"
	"ataniq/internal/instanceid"
	"ataniq/internal/leadcanary"
	"ataniq/internal/leadholder/canaryroot"
	"ataniq/internal/leadholder/sandboxstart"
	"ataniq/internal/leadlifecycle"
	"ataniq/internal/leadlock"
	"ataniq/internal/leadregistry"
	"ataniq/internal/leadrole"
	"ataniq/internal/leadunit"
)

// refusedBeforeListening runs the daemon with cfg and checks that it ends at
// the config step with the config exit code, and that it made nothing: no
// socket, no lock, no registry, no evidence.
func (c *canaryEnv) refusedBeforeListening(cfg Config, wantText string) {
	c.t.Helper()
	err := c.runToEnd(cfg)
	se := mustFail(c.t, err, StepConfig, ExitConfig)
	if !strings.Contains(se.Error(), wantText) {
		c.t.Fatalf("the error is %q, want %q in it", se.Error(), wantText)
	}
	for _, name := range []string{RegistryDirName, leadcanary.EvidenceName, "daemon.lock"} {
		if _, err := os.Lstat(filepath.Join(c.cfg.StateRoot, name)); err == nil {
			c.t.Fatalf("%s was made", name)
		}
	}
	if len(c.fake.Calls()) != 0 {
		c.t.Fatal("the manager was called")
	}
	// Checked last: taking the free lock makes its file.
	c.noSocketMade()
	c.lockFree()
}

func TestCanaryRefusesBeforeListening(t *testing.T) {
	t.Run("default state root", func(t *testing.T) {
		c := newCanaryEnvWithoutHome(t)
		xdg := filepath.Join(c.base, "xdg-state")
		c.cfg.StateRoot = filepath.Join(xdg, "ataniq")
		if err := os.MkdirAll(c.cfg.StateRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := canaryroot.WriteMarker(c.cfg.StateRoot, c.cfg.Role.Project()); err != nil {
			t.Fatal(err)
		}
		cfg := c.daemonConfig()
		cfg.StateDir = c.cfg.StateRoot
		cfg.Canary.Getenv = func(k string) string {
			if k == "XDG_STATE_HOME" {
				return xdg
			}
			return ""
		}
		c.refusedBeforeListening(cfg, string(canaryroot.CodeDefaultRoot))
	})
	t.Run("default runtime root", func(t *testing.T) {
		c := newCanaryEnvWithoutHome(t)
		xdg := filepath.Join(c.base, "xdg-run")
		c.cfg.RuntimeRoot = filepath.Join(xdg, "ataniq")
		cfg := c.daemonConfig()
		cfg.RuntimeDir = c.cfg.RuntimeRoot
		cfg.Canary.Getenv = func(k string) string {
			if k == "XDG_RUNTIME_DIR" {
				return xdg
			}
			return ""
		}
		c.refusedBeforeListening(cfg, string(canaryroot.CodeDefaultRoot))
	})
	t.Run("no marker", func(t *testing.T) {
		c := newCanaryEnvWithoutHome(t)
		if err := os.Remove(filepath.Join(c.state, canaryroot.MarkerName)); err != nil {
			t.Fatal(err)
		}
		c.refusedBeforeListening(c.daemonConfig(), string(canaryroot.CodeNoMarker))
	})
	t.Run("marker of another project", func(t *testing.T) {
		c := newCanaryEnvWithoutHome(t)
		p := filepath.Join(c.state, canaryroot.MarkerName)
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := canaryroot.WriteMarker(c.state, "throwaway-two"); err != nil {
			t.Fatal(err)
		}
		c.refusedBeforeListening(c.daemonConfig(), string(canaryroot.CodeBadMarker))
	})
	t.Run("a project without the prefix", func(t *testing.T) {
		c := newCanaryEnvWithoutHome(t)
		role, err := leadrole.Lead("sample")
		if err != nil {
			t.Fatal(err)
		}
		c.cfg.Role = role
		c.refusedBeforeListening(c.daemonConfig(), "role")
	})
	t.Run("nested roots", func(t *testing.T) {
		c := newCanaryEnvWithoutHome(t)
		c.cfg.RuntimeRoot = filepath.Join(c.state, "run")
		cfg := c.daemonConfig()
		cfg.RuntimeDir = c.cfg.RuntimeRoot
		c.refusedBeforeListening(cfg, string(canaryroot.CodeNestedRoot))
	})
	t.Run("the daemon's folders are not the config's roots", func(t *testing.T) {
		c := newCanaryEnvWithoutHome(t)
		cfg := c.daemonConfig()
		cfg.StateDir = filepath.Join(c.base, "other-state")
		c.refusedBeforeListening(cfg, "roots")
	})
	t.Run("limits missing", func(t *testing.T) {
		c := newCanaryEnvWithoutHome(t)
		c.cfg.Limits = leadcanary.Limits{}
		c.refusedBeforeListening(c.daemonConfig(), "limits")
	})
	t.Run("one limit outside its bound", func(t *testing.T) {
		c := newCanaryEnvWithoutHome(t)
		c.cfg.Limits.TasksMax = leadcanary.MaxTasksMax + 1
		c.refusedBeforeListening(c.daemonConfig(), "limits.tasks_max")
	})
}

// TestCanaryWithoutTheConfigIsTheReadOnlySkeleton: a tagged daemon given no
// canary config registers no extension and never builds a canary holder
// command.
func TestCanaryWithoutTheConfigIsTheReadOnlySkeleton(t *testing.T) {
	orig := newHolderCommand
	t.Cleanup(func() { newHolderCommand = orig })
	newHolderCommand = func(leadcanary.Config, string) (leadlifecycle.HolderCommand, error) {
		t.Error("the canary holder command was built without --canary")
		return nil, errors.New("must not be built")
	}
	e := newEnv(t)
	r := e.startDaemon(e.config())
	ctx := t.Context()
	cl, err := clientapi.Dial(ctx, r.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	h, err := cl.Hello(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(h.Operations, " ") != "hello status" || h.Mutations {
		t.Fatalf("hello: %v mutations %v", h.Operations, h.Mutations)
	}
	var ce *clientapi.Error
	err = cl.Call(ctx, OpCanaryStart, CanaryParams{Role: canaryRoleText}, nil)
	if !errors.As(err, &ce) || ce.Code != clientapi.CodeUnsupportedOperation {
		t.Fatalf("canary.start: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(e.state, leadcanary.EvidenceName)); err == nil {
		t.Fatal("an evidence file was made")
	}
	// The lifecycle has the refusing preflight and holder of every build.
	if err := r.stop(); err != nil {
		t.Fatal(err)
	}
}

func TestCanaryBuildsTheHolderCommandOnceWithTheDaemonsLock(t *testing.T) {
	orig := newHolderCommand
	t.Cleanup(func() { newHolderCommand = orig })
	var calls []string
	newHolderCommand = func(cfg leadcanary.Config, lock string) (leadlifecycle.HolderCommand, error) {
		calls = append(calls, lock)
		return orig(cfg, lock)
	}
	c := newCanaryEnv(t)
	c.start()
	if len(calls) != 1 || calls[0] != leadlock.DaemonPath(c.state) {
		t.Fatalf("the builder was called with %v", calls)
	}
}

// TestCanaryBoundsAreLeadunits holds each of leadcanary's six unit bounds
// equal to leadunit's.
func TestCanaryBoundsAreLeadunits(t *testing.T) {
	for name, pair := range map[string][2]uint64{
		"MinMemoryMax": {leadcanary.MinMemoryMax, leadunit.MinMemoryMax},
		"MaxMemoryMax": {leadcanary.MaxMemoryMax, leadunit.MaxMemoryMax},
		"MinTasksMax":  {leadcanary.MinTasksMax, leadunit.MinTasksMax},
		"MaxTasksMax":  {leadcanary.MaxTasksMax, leadunit.MaxTasksMax},
		"MinSwapMax":   {leadcanary.MinSwapMax, leadunit.MinSwapMax},
		"MaxSwapMax":   {leadcanary.MaxSwapMax, leadunit.MaxSwapMax},
	} {
		if pair[0] != pair[1] {
			t.Errorf("%s: leadcanary %d, leadunit %d", name, pair[0], pair[1])
		}
	}
}

// Two daemon state roots launching the same role supply their own authority prefixes.
func TestManagedLaunchTagAcrossInstances(t *testing.T) {
	var first string
	for range 2 {
		c := newCanaryEnv(t)
		d := daemon{Config: c.daemonConfig()}
		m, err := d.canarySetup()
		if err != nil {
			t.Fatal(err)
		}
		if err := m.openState(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.closeEvidence() })
		id, err := instanceid.Load(c.state)
		if err != nil {
			t.Fatal(err)
		}
		_, unit, err := leadcanary.Names(c.cfg.Role)
		if err != nil {
			t.Fatal(err)
		}
		_, _, env, err := m.router.Build(c.cfg.Role, leadregistry.Entry{Role: c.cfg.Role, Epoch: 1},
			leadregistry.Intent{ID: "sample-intent", Epoch: 1, UnitName: unit})
		tag := id.Authority[:16]
		want := append(leadcanary.HolderEnvironment(), sandboxstart.AuthorityTagEnv+"="+tag)
		if err != nil || !slices.Equal(env, want) || tag == first {
			t.Fatalf("holder launch environment %q, %v; want %q with a distinct tag", env, err, want)
		}
		first = tag
	}
}

func TestManagedLaunchRequiresInstanceKey(t *testing.T) {
	for name, tc := range map[string]struct {
		file   string
		damage func(string) error
		want   instanceid.Refusal
	}{
		"missing":             {instanceid.FileName, os.Remove, instanceid.ErrMissing},
		"damaged":             {instanceid.FileName, func(p string) error { return os.WriteFile(p, []byte("damaged"), 0600) }, instanceid.ErrInvalid},
		"unsafe":              {instanceid.FileName, func(p string) error { return os.Chmod(p, 0644) }, instanceid.ErrUnsafe},
		"missing incarnation": {instanceid.IncarnationFile, os.Remove, instanceid.ErrInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCanaryEnv(t)
			if err := tc.damage(filepath.Join(c.state, tc.file)); err != nil {
				t.Fatal(err)
			}
			err := c.runToEnd(c.daemonConfig())
			mustFail(t, err, StepState, ExitState)
			if !errors.Is(err, tc.want) || len(c.fake.Calls()) != 0 {
				t.Fatalf("start refusal %v, manager calls %v", err, c.fake.Calls())
			}
		})
	}
}
