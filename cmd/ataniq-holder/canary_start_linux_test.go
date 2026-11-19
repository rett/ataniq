//go:build linux && ataniq_canary

package main

import (
	"testing"

	"ataniq/internal/leadsandbox/assert"
)

// The session of the plan starts its first process in /, as the qualification
// harness does, and with the stop rule's grace set explicitly.
func TestCanarySessionStartsInRootWithTheStopRule(t *testing.T) {
	c := newCanaryRig(t)
	digest := c.digest()
	c.start(c.spec(digest).build())
	c.waitReady()
	ls := c.launches()
	if len(ls) != 1 {
		t.Fatalf("%d launches", len(ls))
	}
	if ls[0].Dir != "/" {
		t.Errorf("the first process starts in %q, want /", ls[0].Dir)
	}
	if ls[0].StopGrace != assert.StopTermWait {
		t.Errorf("the session's grace is %v, want the stop rule's %v", ls[0].StopGrace, assert.StopTermWait)
	}
	c.stopAndWait()
}
