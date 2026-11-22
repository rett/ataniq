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
