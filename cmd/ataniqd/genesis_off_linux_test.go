//go:build linux && !ataniq_ownerchannel

package main

import (
	"strings"
	"testing"

	"ataniq/internal/leaddaemon"
)

// A build without the ataniq_ownerchannel tag has no genesis command: it is
// usage, and the usage does not name it.
func TestNoGenesisCommandWithoutTheTag(t *testing.T) {
	for _, args := range [][]string{{"genesis", "list"}, {"genesis", "handoff", "a1b2c3"}} {
		ta := newTestApp(t, nil)
		if code := ta.run(args); code != leaddaemon.ExitUsage || ta.out.Len() != 0 {
			t.Errorf("%v: exit %d, stdout %q", args, code, ta.out.String())
		}
	}
	if strings.Contains(usage, "genesis") || strings.Contains(usage, "qargi") {
		t.Errorf("the usage names the genesis command:\n%s", usage)
	}
}
