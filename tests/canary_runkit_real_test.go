package tests

import (
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// The kit tests above give prepare a stub ataniq-canary and a --bin outside
// the checkout, which hid two refusals of the real binaries: the probe named
// below the checkout (a bind source below never_mounted) and the read of the
// lead's provisioned managed settings before anything is provisioned. The
// tests here run the scripts with the binaries of the tagged build and the
// default --bin, the build/canary of the checkout the scripts lie in.

// canaryRealBinaries are the tagged binaries the prepare and the preflight
// really run: ataniq-canary (profile-digest, check-inputs), the stand-in and
// the probe (copied), which are static, and ataniq-intake (init).
var canaryRealBinaries = []struct {
	name   string
	static bool
}{{"ataniq-canary", false}, {"ataniq-canary-standin", true}, {"ataniq-probe", true}, {"ataniq-intake", false}}

// canaryRealBuilt holds the originals of canaryRealBinaries: built once per
// test process, on first use, so that a test that needs none of them builds
// nothing, into a folder of their own that TestMain removes. A failed build is
// kept and fails every test that asks.
var canaryRealBuilt struct {
	sync.Mutex
	done bool
	dir  string
	err  error
}

// canaryRealOriginals returns the folder of the originals, building them on
// the first call.
func canaryRealOriginals(t *testing.T) string {
	t.Helper()
	canaryRealBuilt.Lock()
	defer canaryRealBuilt.Unlock()
	if !canaryRealBuilt.done {
		tool, repo := canaryGoTool(t), root(t)
		canaryRealBuilt.dir, canaryRealBuilt.err = canaryBuildReal(tool, repo)
		canaryRealBuilt.done = true
	}
	if canaryRealBuilt.err != nil {
		t.Fatal(canaryRealBuilt.err)
	}
	return canaryRealBuilt.dir
}

// canaryBuildReal builds canaryRealBinaries with the selected Go into a new
// temporary folder, with the tag, -trimpath and no VCS stamp, and the static
// ones with cgo off. It returns the folder even when a build fails, so that
// TestMain removes it.
func canaryBuildReal(tool, repo string) (string, error) {
	dir, err := os.MkdirTemp("", "ataniq-runkit-bin-")
	if err != nil {
		return "", err
	}
	for _, b := range canaryRealBinaries {
		cmd := exec.Command(tool, "build", "-tags", canaryTag, "-trimpath", "-buildvcs=false", "-o", filepath.Join(dir, b.name), "./cmd/"+b.name)
		cmd.Dir = repo
		cmd.Env = canaryGoEnv()
		if b.static {
			cmd.Env = append(cmd.Env, "CGO_ENABLED=0")
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			return dir, fmt.Errorf("building %s: %v\n%s", b.name, err, out)
		}
	}
	return dir, nil
}

// canaryRealCleanup removes the originals, if they were built. TestMain calls
// it after the tests.
func canaryRealCleanup() {
	canaryRealBuilt.Lock()
	defer canaryRealBuilt.Unlock()
	if canaryRealBuilt.dir != "" {
		os.RemoveAll(canaryRealBuilt.dir)
	}
}

// canaryRealBuild puts a copy of each of the originals into dir, in place of
// the harness's stub. A copy, never a link: a test overwrites its ataniq-canary
// with a failing stub, which must reach no other fixture. Each copy has the
// mode of the original and a time of its own.
func canaryRealBuild(t *testing.T, dir string) {
	t.Helper()
	originals := canaryRealOriginals(t)
	for _, b := range canaryRealBinaries {
		if err := canaryCopyBinary(filepath.Join(originals, b.name), filepath.Join(dir, b.name)); err != nil {
			t.Fatal(err)
		}
	}
}

// canaryCopyBinary copies src to a new file dst with the mode of src, after
// removing what dst was.
func canaryCopyBinary(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	fi, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.Remove(dst); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	// The umask may have taken bits off.
	return os.Chmod(dst, fi.Mode().Perm())
}

// Each fixture gets copies of the originals, not links: a change to one copy
// reaches neither the original nor another fixture. A copy has the mode and
// the bytes of its original and a time of its own, and the stand-in and the
// probe are static, with no program interpreter.
func TestCanaryRunKitRealBinariesAreCopies(t *testing.T) {
	originals := canaryRealOriginals(t)
	a, b := t.TempDir(), t.TempDir()
	// A stub in place, as the harness leaves it.
	if err := os.WriteFile(filepath.Join(a, "ataniq-canary"), []byte("#!/bin/sh\nexit 97\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	canaryRealBuild(t, a)
	past := time.Now().Add(-time.Hour)
	for _, bin := range canaryRealBinaries {
		if err := os.Chtimes(filepath.Join(a, bin.name), past, past); err != nil {
			t.Fatal(err)
		}
	}
	canaryRealBuild(t, b)
	for _, bin := range canaryRealBinaries {
		orig, ca, cb := filepath.Join(originals, bin.name), filepath.Join(a, bin.name), filepath.Join(b, bin.name)
		fo, err := os.Stat(orig)
		if err != nil {
			t.Fatal(err)
		}
		if fo.Mode().Perm()&0o100 == 0 {
			t.Errorf("%s: the original is not executable: %v", bin.name, fo.Mode())
		}
		want, err := os.ReadFile(orig)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []string{ca, cb} {
			fc, err := os.Stat(c)
			if err != nil {
				t.Fatal(err)
			}
			if os.SameFile(fo, fc) {
				t.Errorf("%s is the original, not a copy", c)
			}
			if fc.Mode() != fo.Mode() {
				t.Errorf("%s has mode %v, the original %v", c, fc.Mode(), fo.Mode())
			}
			if got, err := os.ReadFile(c); err != nil || string(got) != string(want) {
				t.Errorf("%s is not the bytes of the original: %v", c, err)
			}
		}
		fa, _ := os.Stat(ca)
		fb, _ := os.Stat(cb)
		if os.SameFile(fa, fb) || !fa.ModTime().Equal(past) || fb.ModTime().Equal(past) {
			t.Errorf("%s: the copies share a file or a time: %v %v", bin.name, fa.ModTime(), fb.ModTime())
		}
		if bin.static {
			f, err := elf.Open(orig)
			if err != nil {
				t.Fatal(err)
			}
			for _, prog := range f.Progs {
				if prog.Type == elf.PT_INTERP {
					t.Errorf("%s is not static: it names a program interpreter", bin.name)
				}
			}
			f.Close()
		}
	}
	// The failing stub one test writes over its copy stays in that copy.
	canaryWrite(t, a, "ataniq-canary", "#!/bin/sh\nexit 1\n")
	if got, _ := os.ReadFile(filepath.Join(b, "ataniq-canary")); strings.HasPrefix(string(got), "#!") {
		t.Error("a stub written over one copy reached another fixture")
	}
	if got, _ := os.ReadFile(filepath.Join(originals, "ataniq-canary")); strings.HasPrefix(string(got), "#!") {
		t.Error("a stub written over one copy reached the original")
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
