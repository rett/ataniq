//go:build linux && !(ataniq_ownerchannel && ataniq_canary)

package main

import (
	"strings"
	"testing"

	"ataniq/internal/leaddaemon"
)

// Only a build with both tags has --owner-settings and --check: in the
// release build, a build with the canary tag alone and one with the owner
// channel's tag alone, run refuses them as flags it does not know, before
// anything is read, and the usage does not name them.
func TestOwnerSettingsIsOnlyInTheBuildWithBothTags(t *testing.T) {
	for _, args := range [][]string{
		{"run", "--owner-settings", "/srv/sample/settings"},
		{"run", "--owner-channel", "--owner-settings", "/srv/sample/settings"},
		{"run", "--check"},
	} {
		ta := newTestApp(t, nil)
		if code := ta.run(args); code != leaddaemon.ExitUsage || !strings.Contains(ta.err.String(), "flag provided but not defined") {
			t.Errorf("%v: exit %d: %s", args, code, ta.err.String())
		}
	}
	ta := newTestApp(t, nil)
	if code := ta.run([]string{"--help"}); code != 0 || strings.Contains(ta.out.String(), "--owner-settings") {
		t.Errorf("the usage of this build names --owner-settings: %d", code)
	}
}
