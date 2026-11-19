//go:build linux && ataniq_canary

package main

import (
	"os"
	"path/filepath"
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
