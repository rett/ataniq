//go:build linux && !ataniq_ownerchannel

package main

import (
	"strings"
	"testing"

	"ataniq/internal/leaddaemon"
)

// TestNoOwnerChannelWithoutTheTag: a build without the ataniq_ownerchannel tag
// has no --owner-channel flag, and its usage text does not mention the owner
// channel; the text is the release text and nothing else is appended to it
// (with the canary tag, only the canary's part).
func TestNoOwnerChannelWithoutTheTag(t *testing.T) {
	for _, args := range [][]string{{"run", "--owner-channel"}, {"run", "--owner-channel=true"}, {"run", "-owner-channel"}} {
		ta := newTestApp(t, nil)
		if code := ta.run(args); code != leaddaemon.ExitUsage || ta.out.Len() != 0 ||
			!strings.Contains(ta.err.String(), "flag provided but not defined: -owner-channel") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, ta.out.String(), ta.err.String())
		}
	}
	if strings.Contains(usage, "owner") {
		t.Errorf("the usage mentions the owner channel:\n%s", usage)
	}
	if usage != baseUsage+canaryUsage {
		t.Errorf("the usage is not the release text and the canary's part")
	}
}
