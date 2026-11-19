//go:build linux && ataniq_canary

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The refusals that write nothing come before the role lock is taken: a
// refused canary command line does not even create the lock file.
func TestCanaryRefusalsComeBeforeTheRoleLock(t *testing.T) {
	c := newCanaryRig(t)
	c.in.Canary = false
	c.writeInputs()
	if code := c.runNow(c.spec("").build()); code != exitRefuse {
		t.Fatalf("exit %d; stderr: %s", code, c.errbuf.String())
	}
	if _, err := os.Lstat(c.roleLock); err == nil {
		t.Error("a refused command line created the role lock file")
	}
	if _, err := os.Lstat(filepath.Join(c.state, leadHasRunMark)); err == nil {
		t.Error("a refused command line left the mark")
	}
	untouched(t, c.state)
}

// The holder's flags must agree with the sandbox inputs and the layout: each
// disagreement is refused, with exit 2, before anything is written.
func TestCanaryRefusesFlagsThatDisagreeWithTheInputs(t *testing.T) {
	// Folders that exist and are real, so that only the comparison can refuse.
	exact := func(c *canaryRig, name string) string {
		d := filepath.Join(c.stateRoot, "leads", name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		return d
	}
	tests := []struct {
		name  string
		setup func(c *canaryRig, a *argSpec)
		want  string
	}{
		{"a --runtime that is not the inputs'", func(c *canaryRig, a *argSpec) { a.set("runtime", c.in.Runtime+"x") }, "--runtime and --runtime-version"},
		{"a --runtime-version that is not the inputs'", func(c *canaryRig, a *argSpec) { a.set("runtime-version", c.in.RuntimeVersion+".1") }, "--runtime and --runtime-version"},
		{"a --tree that is not the layout's tree", func(c *canaryRig, a *argSpec) { a.set("tree", exact(c, "other-tree")) }, "--tree is not the lead's tree"},
		{"a --state-dir under the state root that is not the holder folder", func(c *canaryRig, a *argSpec) {
			a.set("state-dir", exact(c, "other-holder"))
		}, "--state-dir is not the holder folder"},
		{"a --runtime-dir under the runtime root that is not the lead's", func(c *canaryRig, a *argSpec) {
			d := filepath.Join(c.runRoot, "leads", "other")
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
			a.set("runtime-dir", d)
		}, "--runtime-dir is not the lead's runtime folder"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanaryRig(t)
			a := c.spec("")
			tc.setup(c, a)
			if code := c.runNow(a.build()); code != exitRefuse {
				t.Errorf("exit %d, want %d; stderr: %s", code, exitRefuse, c.errbuf.String())
			}
			if !strings.Contains(c.errbuf.String(), "refused: ") || !strings.Contains(c.errbuf.String(), tc.want) {
				t.Errorf("stderr %q does not say %q", c.errbuf.String(), tc.want)
			}
			untouched(t, c.state)
			if _, err := os.Lstat(c.roleLock); err == nil {
				t.Error("the role lock file was created")
			}
			if c.systemsMade() != 0 || len(c.launches()) != 0 {
				t.Error("a refused start reached the start sequence")
			}
		})
	}
}
