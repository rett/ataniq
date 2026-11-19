//go:build linux && !ataniq_canary

package main

import (
	"strings"
	"testing"

	"ataniq/internal/leaddaemon"
)

// TestNoCanaryWithoutTheTag: a build without the ataniq_canary tag has no
// --canary flag, no canary command and no --version, and its usage text does not
// mention them.
func TestNoCanaryWithoutTheTag(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--canary", "x"},
		{"run", "--canary=x"},
		{"canary", "define", "--config", "x"},
		{"canary"},
		{"--version"},
	} {
		ta := newTestApp(t, nil)
		if code := ta.run(args); code != leaddaemon.ExitUsage || ta.out.Len() != 0 {
			t.Errorf("%v: exit %d, stdout %q", args, code, ta.out.String())
		}
	}
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"run", "--canary", "x"}); code != leaddaemon.ExitUsage || !strings.Contains(ta.err.String(), "flag provided but not defined: -canary") {
		t.Errorf("run --canary: %d %q", code, ta.err.String())
	}
	for _, word := range []string{"canary", "--version"} {
		if strings.Contains(usage, word) {
			t.Errorf("the usage mentions %s", word)
		}
	}
}
