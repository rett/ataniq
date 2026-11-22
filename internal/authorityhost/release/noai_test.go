package release

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// withNoAI builds a static release whose bin/ah-noaicheck is the script.
func withNoAI(t *testing.T, script string) string {
	t.Helper()
	o := testOptions(t, staticSrc(t))
	bin := filepath.Join(t.TempDir(), "ah-noaicheck")
	writeTestFile(t, bin, script, 0o755)
	o.Bins = []string{bin, o.Bins[1]}
	return built(t, o)
}

func TestCheckRunsTheShippedNoAICheck(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script string
		opts   CheckOptions
		want   []string
	}{
		{"clean", "#!/bin/sh\necho 'RUN complete'\necho ok\nexit 0\n", CheckOptions{}, nil},
		{"a hit and a failure", "#!/bin/sh\necho 'HIT binary /usr/bin/claude'\necho 'FAILED 1 hit(s)'\nexit 1\n", CheckOptions{}, []string{"noaicheck-exit", "noaicheck-hit"}},
		{"a hit and exit 0", "#!/bin/sh\necho 'HIT env /x'\nexit 0\n", CheckOptions{}, []string{"noaicheck-hit"}},
		{"exit 2, no hit", "#!/bin/sh\necho 'ah-noaicheck: a list is missing' >&2\nexit 2\n", CheckOptions{}, []string{"noaicheck-exit"}},
		{"a NOTE is not a hit", "#!/bin/sh\necho 'NOTE packages not enforced'\nexit 0\n", CheckOptions{}, nil},
		{"HIT in the middle of a line is not a hit line", "#!/bin/sh\necho 'a HIT b'\nexit 0\n", CheckOptions{}, nil},
		{"killed by a signal", "#!/bin/sh\nkill -9 $$\n", CheckOptions{}, []string{"noaicheck-exit"}},
		{"does not start", "#!/nonexistent/interpreter\n", CheckOptions{}, []string{"noaicheck-not-started"}},
		{"does not finish", "#!/bin/sh\nsleep 30\n", CheckOptions{Timeout: 300 * time.Millisecond}, []string{"noaicheck-timeout"}},
		{"a very long line is not a pass", "#!/bin/sh\nhead -c 3000000 /dev/zero | tr '\\0' a\necho\nexit 0\n", CheckOptions{}, []string{"noaicheck-exit"}},
		{"skipped", "#!/bin/sh\necho 'HIT x'\nexit 1\n", CheckOptions{NoNoAICheck: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			got := codes(Check(withNoAI(t, tc.script), tc.opts))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("findings\n got: %q\nwant: %q", got, tc.want)
			}
			if time.Since(start) > 10*time.Second {
				t.Errorf("took %s", time.Since(start))
			}
		})
	}
}

// TestNoAICheckIsRunLikeTheSwitchRunsIt: the flags, the clean environment,
// the folder / and no standard input.
func TestNoAICheckIsRunLikeTheSwitchRunsIt(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	script := "#!/bin/sh\n{ echo \"args: $*\"; echo \"cwd: $(pwd)\"; env | sort; echo \"stdin: $(cat)\"; } > " + record + "\nexit 0\n"
	dir := withNoAI(t, script)
	t.Setenv("ANTHROPIC_API_KEY", "must-not-reach-the-check")
	if f := Check(dir, CheckOptions{}); len(f) != 0 {
		t.Fatal(lines(f))
	}
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for _, want := range []string{"args: --phase pre --release " + dir + "\n", "cwd: /\n", "LC_ALL=C\n", "PATH=/usr/sbin:/usr/bin:/sbin:/bin\n", "stdin: \n"} {
		if !strings.Contains(got, want) {
			t.Errorf("record lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ANTHROPIC") || strings.Contains(got, "HOME=") {
		t.Errorf("the environment is not clean:\n%s", got)
	}

	os.Remove(record)
	if f := Check(dir, CheckOptions{Root: "/some/root", RequirePolicy: true}); len(f) != 0 {
		t.Fatal(lines(f))
	}
	data, _ = os.ReadFile(record)
	if want := "args: --phase pre --release " + dir + " --root /some/root --require-policy\n"; !strings.Contains(string(data), want) {
		t.Errorf("record lacks %q:\n%s", want, data)
	}
}

// TestNoAICheckTimeoutKillsTheGroup: a program that left a child behind does
// not outlive the check.
func TestNoAICheckTimeoutKillsTheGroup(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "alive")
	script := "#!/bin/sh\n(sleep 2; echo child > " + marker + ") &\nsleep 30\n"
	dir := withNoAI(t, script)
	got := codes(Check(dir, CheckOptions{Timeout: 300 * time.Millisecond}))
	if !reflect.DeepEqual(got, []string{"noaicheck-timeout"}) {
		t.Fatalf("findings %q", got)
	}
	time.Sleep(2500 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Error("a child of the check outlived it")
	}
}

// TestNoAICheckNotRunOnABrokenFolder: no bin/ah-noaicheck, nothing runs, and
// the missing binary is the finding.
func TestNoAICheckNotRunOnABrokenFolder(t *testing.T) {
	dir := built(t, testOptions(t, staticSrc(t)))
	os.Remove(filepath.Join(dir, "bin/ah-noaicheck"))
	resum(t, dir)
	if got := codes(Check(dir, CheckOptions{})); !reflect.DeepEqual(got, []string{"bin-missing: bin/ah-noaicheck"}) {
		t.Errorf("findings %q", got)
	}
}

// TestNoAICheckNotRunWhenItsHashIsWrong: a bin/ah-noaicheck that is there
// but differs from its SHA256SUMS entry is already found corrupt, so it is
// not run; the mismatch is the only finding.
func TestNoAICheckNotRunWhenItsHashIsWrong(t *testing.T) {
	record := filepath.Join(t.TempDir(), "ran")
	dir := withNoAI(t, "#!/bin/sh\nexit 0\n")
	writeTestFile(t, filepath.Join(dir, "bin/ah-noaicheck"), "#!/bin/sh\necho ran > "+record+"\nexit 0\n", 0o755)
	got := codes(Check(dir, CheckOptions{}))
	if want := []string{"sums-mismatch: bin/ah-noaicheck"}; !reflect.DeepEqual(got, want) {
		t.Errorf("findings\n got: %q\nwant: %q", got, want)
	}
	if _, err := os.Stat(record); err == nil {
		t.Error("the check ran a bin/ah-noaicheck whose hash is wrong")
	}
}

// TestNoAICheckWithARelativeFolder: a release named by a path relative to
// the caller's folder is checked, and its no-AI check run, as the folder
// that path names: the check is started from the folder / with the
// release's absolute path.
func TestNoAICheckWithARelativeFolder(t *testing.T) {
	record := filepath.Join(t.TempDir(), "record")
	dir := withNoAI(t, "#!/bin/sh\necho \"args: $*\" > "+record+"\nexit 0\n")
	t.Chdir(filepath.Dir(dir))
	for _, rel := range []string{filepath.Base(dir), "./" + filepath.Base(dir)} {
		os.Remove(record)
		if f := Check(rel, CheckOptions{}); len(f) != 0 {
			t.Fatalf("%s: %s", rel, lines(f))
		}
		data, err := os.ReadFile(record)
		if err != nil {
			t.Fatalf("%s: the check did not run: %v", rel, err)
		}
		if want := "args: --phase pre --release " + dir + "\n"; string(data) != want {
			t.Errorf("%s: record %q, want %q", rel, data, want)
		}
	}
}

// privateTemp points the temporary folder at a new empty folder and checks,
// at the end of the test, that nothing was left in it.
func privateTemp(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Cleanup(func() {
		if ents, err := os.ReadDir(tmp); err != nil || len(ents) != 0 {
			t.Errorf("the temporary folder holds %d entries, %v", len(ents), err)
		}
	})
}

// onNoAICheck sets the hook Check calls after the folder was verified and
// before the no-AI check is run.
func onNoAICheck(t *testing.T, f func()) {
	t.Helper()
	beforeNoAICheck = f
	t.Cleanup(func() { beforeNoAICheck = nil })
}

// TestNoAICheckRunsTheBytesItVerified: what runs is the bin/ah-noaicheck
// Check verified. A checker replaced in the folder after the verification
// is not run (noaicheck-changed); a folder renamed away and replaced by
// another after it still runs the verified checker, not the other's.
func TestNoAICheckRunsTheBytesItVerified(t *testing.T) {
	script := func(record, word string) string {
		return "#!/bin/sh\necho " + word + " >> " + record + "\nexit 0\n"
	}
	t.Run("the checker replaced", func(t *testing.T) {
		record := filepath.Join(t.TempDir(), "record")
		dir := withNoAI(t, script(record, "verified"))
		privateTemp(t)
		onNoAICheck(t, func() {
			p := filepath.Join(dir, "bin/ah-noaicheck")
			writeTestFile(t, p+".new", script(record, "other"), 0o755)
			if err := os.Rename(p+".new", p); err != nil {
				t.Fatal(err)
			}
		})
		got := codes(Check(dir, CheckOptions{}))
		if want := []string{"noaicheck-changed"}; !reflect.DeepEqual(got, want) {
			t.Errorf("findings\n got: %q\nwant: %q", got, want)
		}
		if data, err := os.ReadFile(record); err == nil {
			t.Errorf("a checker ran: %q", data)
		}
	})
	t.Run("the folder replaced", func(t *testing.T) {
		record := filepath.Join(t.TempDir(), "record")
		dir := withNoAI(t, script(record, "verified"))
		other := withNoAI(t, script(record, "other"))
		privateTemp(t)
		onNoAICheck(t, func() {
			if err := os.Rename(dir, dir+".old"); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(other, dir); err != nil {
				t.Fatal(err)
			}
		})
		if f := Check(dir, CheckOptions{}); len(f) != 0 {
			t.Fatal(lines(f))
		}
		if data, err := os.ReadFile(record); err != nil || string(data) != "verified\n" {
			t.Errorf("record %q, %v; want the verified checker only", data, err)
		}
	})
}
