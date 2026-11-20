package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The kit tests above give prepare a stub ataniq-canary and a --bin outside
// the checkout, which hid two refusals of the real binaries: the probe named
// below the checkout (a bind source below never_mounted) and the read of the
// lead's provisioned managed settings before anything is provisioned. The
// tests here run the scripts with the binaries of the tagged build and the
// default --bin, the build/canary of the checkout the scripts lie in.

// canaryRealBuild builds the tagged binaries the prepare and the preflight
// really run into dir: ataniq-canary (profile-digest, check-inputs), the
// stand-in and the probe (copied), and ataniq-intake (init). The builds are
// cached by the Go build cache after the first.
func canaryRealBuild(t *testing.T, dir string) {
	t.Helper()
	for name, static := range map[string]bool{"ataniq-canary": false, "ataniq-canary-standin": true, "ataniq-probe": true, "ataniq-intake": false} {
		os.Remove(filepath.Join(dir, name)) // the harness's stub
		cmd := exec.Command(canaryGoTool(t), "build", "-tags", canaryTag, "-trimpath", "-buildvcs=false", "-o", filepath.Join(dir, name), "./cmd/"+name)
		cmd.Dir = root(t)
		cmd.Env = canaryGoEnv()
		if static {
			cmd.Env = append(cmd.Env, "CGO_ENABLED=0")
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building %s: %v\n%s", name, err, out)
		}
	}
}

// canaryRealKit makes the checkout of the preflight's harness complete for a
// prepare: the prepare script, the checklist template and the recorded
// screens it copies, and the real binaries in build/canary of that checkout.
func canaryRealKit(t *testing.T) *canaryPF {
	t.Helper()
	p := newCanaryPF(t)
	for _, name := range []string{"canary-prepare.sh", "checklist.txt.in"} {
		data, err := os.ReadFile(filepath.Join(root(t), "qualification/canary/run", name))
		if err != nil {
			t.Fatal(err)
		}
		canaryWrite(t, p.runDir, name, string(data))
	}
	screens := "internal/inject/screen/testdata/claude-2.1.325"
	entries, err := os.ReadDir(filepath.Join(root(t), screens))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root(t), screens, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		canaryWrite(t, filepath.Join(p.repo, screens), e.Name(), string(data))
	}
	canaryRealBuild(t, p.bin)
	// The tools prepare needs besides the preflight's.
	for _, name := range []string{"chmod", "dirname", "printf", "ln", "touch", "uname"} {
		if _, err := os.Lstat(filepath.Join(p.tools, name)); err == nil {
			continue
		}
		if real, err := exec.LookPath(name); err == nil {
			if err := os.Symlink(real, filepath.Join(p.tools, name)); err != nil {
				t.Fatal(err)
			}
		}
	}
	p.age(time.Now())
	return p
}

func TestCanaryRunKitPrepareAndPreflightWithTheRealBinaries(t *testing.T) {
	p := canaryRealKit(t)
	r := filepath.Join(p.dir, "real-root")
	// No --bin: the default is build/canary of the checkout the script lies
	// in, which is the first entry of never_mounted.
	cmd := exec.Command(filepath.Join(p.tools, "sh"), filepath.Join(p.runDir, "canary-prepare.sh"),
		"--root", r, "--pasta", filepath.Join(p.tools, "pasta"), "--bwrap", filepath.Join(p.tools, "bwrap"))
	cmd.Dir = p.dir
	cmd.Env = []string{"PATH=" + p.tools, "HOME=" + filepath.Join(p.dir, "home"), "TMPDIR=" + p.tmp}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("prepare with the real binaries: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "check-inputs: the inputs match the config") {
		t.Errorf("prepare did not say the inputs match:\n%s", out)
	}
	// The probe the inputs name is the copy in the root, not the built one.
	inputs, err := os.ReadFile(filepath.Join(r, "conf/sandbox-inputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inputs), `"probe": "`+r+`/bin/ataniq-probe"`) {
		t.Errorf("the inputs do not name the probe copy in the root:\n%s", inputs)
	}
	built, _ := os.ReadFile(filepath.Join(p.bin, "ataniq-probe"))
	if copied, err := os.ReadFile(filepath.Join(r, "bin/ataniq-probe")); err != nil || string(copied) != string(built) || canaryMode(t, filepath.Join(r, "bin/ataniq-probe")) != 0o700 {
		t.Errorf("bin/ataniq-probe is not a 0700 copy of the built probe: %v", err)
	}

	// The preflight, on that root, gets past the pin and the inputs.
	pre, _ := p.runRaw("--root", r)
	for _, want := range []string{
		"the holder is the built one and the profile digest pin is the one computed now",
		"ataniq-canary check-inputs accepts the sandbox inputs, which name the marker guard-hook that the managed settings hold",
	} {
		if !strings.Contains(pre, want) {
			t.Errorf("the preflight lacks %q:\n%s", want, pre)
		}
	}
	for _, bad := range []string{"cannot compute the profile digest", "refuses the sandbox inputs", "skip: the sandbox inputs", "skip: the holder and the profile digest pin"} {
		if strings.Contains(pre, bad) {
			t.Errorf("the preflight says %q:\n%s", bad, pre)
		}
	}
}

// A prepare that fails after it made the root leaves it, deletes nothing, and
// says as its last line how to remove it by hand. The root has no mark, which
// canary-reset.sh requires.
func TestCanaryRunKitFailedPrepareSaysHowToRemoveTheHalfMadeRoot(t *testing.T) {
	p := canaryRealKit(t)
	// A digest that cannot be computed: the one program of the run that fails.
	canaryWrite(t, p.bin, "ataniq-canary", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(p.bin, "ataniq-canary"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := filepath.Join(p.dir, "failed-root")
	cmd := exec.Command(filepath.Join(p.tools, "sh"), filepath.Join(p.runDir, "canary-prepare.sh"),
		"--root", r, "--pasta", filepath.Join(p.tools, "pasta"), "--bwrap", filepath.Join(p.tools, "bwrap"))
	cmd.Dir = p.dir
	cmd.Env = []string{"PATH=" + p.tools, "HOME=" + filepath.Join(p.dir, "home"), "TMPDIR=" + p.tmp}
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("prepare succeeded with a failing ataniq-canary:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "half-made root "+r) || !strings.Contains(last, "rm -r "+r) {
		t.Errorf("the last line does not say how to remove the root by hand: %q", last)
	}
	if _, err := os.Stat(filepath.Join(r, "config.json")); err != nil {
		t.Errorf("prepare removed what it had made: %v", err)
	}
	if _, err := os.Stat(filepath.Join(r, ".canary-prepared")); err == nil {
		t.Error("a failed prepare left the mark that canary-reset.sh requires")
	}
}

// The two replay screens the kit copies are the two the holder's accepting
// path takes (cmd/ataniq-holder/canary_fixture_path_test.go): the third
// recording of a collapsed paste, because the other two carry a damaged title
// that reads as a busy agent. The names are read out of the script and of that
// test, so that neither can change alone.
func TestCanaryRunKitReplayScreensAreTheHoldersAcceptingPathScreens(t *testing.T) {
	holder, err := os.ReadFile(filepath.Join(root(t), "cmd/ataniq-holder/canary_fixture_path_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	constant := func(name string) string {
		m := regexp.MustCompile(`(?m)^\s*` + name + `\s*=\s*"([^"]+)"\s*$`).FindSubmatch(holder)
		if m == nil {
			t.Fatalf("the holder's accepting-path test no longer holds the constant %s", name)
		}
		return string(m[1])
	}
	wantIdle, wantPasted := constant("idleScreenName"), constant("pastedScreenName")

	script, err := os.ReadFile(filepath.Join(root(t), canaryPrepareScript))
	if err != nil {
		t.Fatal(err)
	}
	copies := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^\s*cp -- "\$SCREENS/([a-z0-9-]+)\.\$_x" "\$ROOT/conf/replay-screens/([0-9]+)\.\$_x"$`).FindAllStringSubmatch(string(script), -1) {
		copies[m[2]] = m[1]
	}
	if len(copies) != 2 || copies["1"] != wantIdle || copies["2"] != wantPasted {
		t.Errorf("prepare copies screens %v; the holder's accepting-path test uses 1=%s and 2=%s", copies, wantIdle, wantPasted)
	}
	dry := regexp.MustCompile(`replay-screens \(([a-z0-9-]+) as 1, ([a-z0-9-]+) as 2,`).FindStringSubmatch(string(script))
	if dry == nil || dry[1] != wantIdle || dry[2] != wantPasted {
		t.Errorf("the dry text of prepare names %v; the holder's accepting-path test uses %s and %s", dry, wantIdle, wantPasted)
	}
	// The kit test that pins what was copied names the same screens.
	pin, err := os.ReadFile(filepath.Join(root(t), "tests/canary_runkit_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for dst, name := range map[string]string{"1": wantIdle, "2": wantPasted} {
		for _, ext := range []string{"json", "txt"} {
			want := regexp.MustCompile(`"conf/replay-screens/` + dst + `\.` + ext + `":\s+filepath\.Join\(screens, "` + name + `\.` + ext + `"\)`)
			if !want.Match(pin) {
				t.Errorf("the kit test that pins conf/replay-screens/%s.%s does not name %s.%s", dst, ext, name, ext)
			}
		}
	}
}
