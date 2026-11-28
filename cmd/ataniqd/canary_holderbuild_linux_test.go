//go:build linux && ataniq_canary

package main

import (
	"strings"
	"testing"

	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadholder/buildid"
)

// testHolderBuild is the build the daemon of these tests expects, and the one
// it finds in every holder unless a test says otherwise: the tests' holders
// are fakes whose executable does not exist, and a test binary has no VCS
// revision. The init runs in the test binary only, also where it stands in for
// the command (asBinaryEnv).
var testHolderBuild = buildid.Identity{Module: "ataniq", Version: "v0.0.14-0.20261124010203-222222222222",
	Revision: "2222222222222222222222222222222222222222"}

func sameHolderBuild() *buildid.Checker {
	return &buildid.Checker{
		Expected:   testHolderBuild,
		Executable: func(string) (buildid.Identity, error) { return testHolderBuild, nil },
		Record: func(_ string, epoch uint64) (buildid.Record, error) {
			b := testHolderBuild
			return buildid.Record{Epoch: epoch, Build: &b}, nil
		},
	}
}

func init() { holderBuild = sameHolderBuild() }

// TestCanaryStartRefusesAHolderOfAnotherBuild: through the command, a start
// whose holder executable is another build is refused by name, naming both
// builds, and the role stays stopped.
func TestCanaryStartRefusesAHolderOfAnotherBuild(t *testing.T) {
	saved := holderBuild
	t.Cleanup(func() { holderBuild = saved })
	other := buildid.Identity{Module: "ataniq", Version: "v0.0.13-0.20261120010203-111111111111",
		Revision: "1111111111111111111111111111111111111111"}
	holderBuild = sameHolderBuild()
	holderBuild.Executable = func(string) (buildid.Identity, error) { return other, nil }

	f := newCanaryFixture(t)
	ta := canaryApp(t)
	startApp(t, ta, []string{"--canary", f.cfgPath}, f.socket(), f.cfg.Role.Project())
	cfgArg := []string{"--config", f.cfgPath}
	if code, _, errOut := canary(t, append([]string{"define"}, cfgArg...)...); code != 0 {
		t.Fatalf("define: %d %q", code, errOut)
	}
	code, out, errOut := canary(t, append([]string{"start"}, cfgArg...)...)
	if code != leaddaemon.ExitRefused || out != "" || !strings.Contains(errOut, "holder-version-skew: the holder is another build") ||
		!strings.Contains(errOut, other.Short()) || !strings.Contains(errOut, testHolderBuild.Short()) {
		t.Fatalf("start: %d %q %q", code, out, errOut)
	}
}
