//go:build linux && amd64

package harness_test

import (
	"os"
	"regexp"
	"testing"

	"ataniq/qualification/leadsandbox/internal/harness"
)

// holderMinBwrapSource is the file of the holder's start sequence that
// declares its own MinBwrapBindFD. The harness may not import that package
// (it is built only with the ataniq_canary tag), so the value is read from
// the source as text, the way the holder's steps_test.go reads the harness's
// steps.go.
const holderMinBwrapSource = "../../../../internal/leadholder/sandboxstart/run.go"

// TestMinBwrapBindFDMatchesTheHolder pins the harness's minimum bubblewrap
// release to the holder's. Both copies are the same unconfirmed assumption
// (the first release taken to have --bind-fd and --ro-bind-fd); whoever
// confirms it must change both together, and this test fails when only one
// of them has changed.
func TestMinBwrapBindFDMatchesTheHolder(t *testing.T) {
	data, err := os.ReadFile(holderMinBwrapSource)
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^const MinBwrapBindFD = "([^"\n]*)"\s*$`)
	found := re.FindAllStringSubmatch(string(data), -1)
	if len(found) != 1 {
		t.Fatalf("found %d declarations of the form `const MinBwrapBindFD = \"...\"` in %s, want exactly one: the test reads the holder's value from that line", len(found), holderMinBwrapSource)
	}
	holder := found[0][1]
	if harness.MinBwrapBindFD != holder {
		t.Errorf("the harness's MinBwrapBindFD is %q and the holder's (%s) is %q: they are one assumption and change together", harness.MinBwrapBindFD, holderMinBwrapSource, holder)
	}
}
