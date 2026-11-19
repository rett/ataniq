//go:build linux && ataniq_canary

package main

import (
	"strings"
	"testing"
)

// --inject with no --expected-mode, or an empty one, is refused: no delivery
// could pass the gate, which would look open and be shut.
func TestCanaryInjectNeedsAnExpectedMode(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(a *argSpec)
	}{
		{"none", func(a *argSpec) { a.drop("expected-mode") }},
		{"an empty one", func(a *argSpec) { a.set("expected-mode", "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := newCanaryRig(t)
			a := c.spec("", "--inject")
			tc.setup(a)
			if code := c.runNow(a.build()); code != exitRefuse {
				t.Errorf("exit %d, want %d; stderr: %s", code, exitRefuse, c.errbuf.String())
			}
			if !strings.Contains(c.errbuf.String(), "--inject needs a non-empty --expected-mode") {
				t.Errorf("stderr: %s", c.errbuf.String())
			}
			nothingStarted(t, c.rig)
			untouched(t, c.state)
		})
	}
}
