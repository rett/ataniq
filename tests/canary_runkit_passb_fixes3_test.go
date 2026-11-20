package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func canaryReadFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root(t), rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// R1: with no manifest of step 15, the canary's own folder read at step 25
// still proves a credential to be the canary's. The real folder's credential
// changed to the canary's hash: a finding, not a question the owner's yes clears.
func TestCanaryRunKitStep25NowManifestWithoutAfterManifest(t *testing.T) {
	o, ans, _ := step25Real(t, "", "a1 3 3 ./.credentials.json\n", "yes\nyes\nyes\n")
	if !strings.Contains(o, "MEANINGFUL: changed ./.credentials.json") || !strings.Contains(o, "findings=1") {
		t.Errorf("a canary credential is cleared without a manifest of step 15:\n%s\n%q", o, ans)
	}
	if strings.Contains(o, "explained by the owner as his own sessions: ./.credentials.json") {
		t.Errorf("the owner's yes explained the canary's credential:\n%s", o)
	}
}

// R4: netcheck says it only approximates the holder's resolv.conf rule.
func TestCanaryRunKitNetcheckNamesItsApproximation(t *testing.T) {
	text := canaryReadFile(t, "qualification/canary/run/netcheck.sh.in")
	if !strings.Contains(text, "approximation of the holder's rule") {
		t.Error("netcheck.sh.in does not say its stub test is an approximation")
	}
}

// R5: the checklist names the qualify-mirror build and run commands.
func TestCanaryRunKitPassBChecklistHasMirrorCommands(t *testing.T) {
	text := canaryReadFile(t, "qualification/canary/run/checklist-b.txt.in")
	for _, want := range []string{"go-tool.sh build -o build/qualify-mirror", "build/qualify-mirror --config @ROOT@/mirror-qual/config.json", "--out @ROOT@/mirror-qual/report.json"} {
		if !strings.Contains(text, want) {
			t.Errorf("the checklist does not hold %q", want)
		}
	}
}
