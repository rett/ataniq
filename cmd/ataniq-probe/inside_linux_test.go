//go:build linux

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
	"ataniq/internal/leadsandbox/profile"
)

// insideHarness runs runInside in this process, with a fake system and a fake
// launch, so that nothing is executed.
type insideHarness struct {
	t       *testing.T
	spec    assert.Spec
	system  *fakeSystem
	launch  func(agent, env []string, reportFD int, stderr io.Writer) int
	launchN int
	agent   []string
	env     []string
}

func (h *insideHarness) run(specPath string, reportFD int) (code int, stderr string) {
	var buf bytes.Buffer
	d := insideDeps{
		system:  func(assert.Spec) assert.System { return h.system },
		environ: func() []string { return []string{"A=1", "B=2"} },
		launch: func(agent, env []string, fd int, w io.Writer) int {
			h.launchN++
			h.agent, h.env = agent, env
			if h.launch != nil {
				return h.launch(agent, env, fd, w)
			}
			return 0
		},
	}
	// The agent must be an executable regular file: runInside looks.
	code = runInside([]string{"--spec", specPath, "--report-fd", fmt.Sprint(reportFD), "--", testExe(h.t), "-x"}, &buf, d)
	return code, buf.String()
}

func newHarness(t *testing.T) *insideHarness {
	spec := testSpec(t)
	return &insideHarness{t: t, spec: spec, system: passSystem(spec)}
}

// pipe returns a report pipe whose write end is a plain descriptor.
func pipe(t *testing.T) (r, w int) {
	t.Helper()
	var p [2]int
	if err := unix.Pipe2(p[:], unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unix.Close(p[0]); unix.Close(p[1]) })
	return p[0], p[1]
}

func drain(t *testing.T, r, w int) []byte {
	t.Helper()
	unix.Close(w)
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := unix.Read(r, buf)
		if err != nil || n == 0 {
			return out
		}
		out = append(out, buf[:n]...)
	}
}

func TestRunInsidePassLaunchesTheAgentAsIs(t *testing.T) {
	h := newHarness(t)
	r, w := pipe(t)
	h.launch = func(agent, env []string, fd int, _ io.Writer) int {
		if fd != w {
			t.Errorf("launch got report descriptor %d, want %d", fd, w)
		}
		return 0
	}
	code, stderr := h.run(writeSpec(t, h.spec), w)
	if code != 0 || stderr != "" || h.launchN != 1 {
		t.Fatalf("code %d launched %d stderr %q", code, h.launchN, stderr)
	}
	if strings.Join(h.agent, " ") != testExe(t)+" -x" || strings.Join(h.env, ",") != "A=1,B=2" {
		t.Errorf("agent %q env %q", h.agent, h.env)
	}
	if fl, _ := unix.FcntlInt(uintptr(w), unix.F_GETFD, 0); fl&unix.FD_CLOEXEC == 0 {
		t.Error("the report descriptor is not close-on-exec")
	}
	line := drain(t, r, w)
	rep, err := assert.ParseProbeLine(line)
	if err != nil || assert.Failed(rep, h.spec) || bytes.Count(line, []byte("\n")) != 1 {
		t.Errorf("report %q: %v", line, err)
	}
}

func TestRunInsideFailureWritesTheReportAndLaunchesNothing(t *testing.T) {
	h := newHarness(t)
	h.system.env = []string{"HOME=/home/lead", "EXTRA=1"} // assertion 2 fails
	r, w := pipe(t)
	code, stderr := h.run(writeSpec(t, h.spec), w)
	if code != exitFailed || h.launchN != 0 {
		t.Fatalf("code %d launched %d", code, h.launchN)
	}
	rep, err := assert.ParseProbeLine(drain(t, r, w))
	if err != nil || !assert.Failed(rep, h.spec) {
		t.Fatalf("report: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(stderr, "\n"), "\n")
	if len(lines) < 2 || !strings.HasPrefix(lines[0], "ataniq-probe: the inside checks did not pass: ") {
		t.Fatalf("stderr %q", stderr)
	}
	found := false
	for _, l := range lines[1:] {
		found = found || strings.HasPrefix(l, "ataniq-probe:   assertion 2 (") && strings.Contains(l, "failed")
	}
	if !found {
		t.Errorf("assertion 2 is not listed: %q", stderr)
	}
}

func TestRunInsideEmptyMountTableFails(t *testing.T) {
	h := newHarness(t)
	h.system.mounts = nil // the mount table is empty: assertion 7 cannot find the mounts
	r, w := pipe(t)
	if code, _ := h.run(writeSpec(t, h.spec), w); code != exitFailed {
		t.Errorf("code %d", code)
	}
	drain(t, r, w)
}

// Assertion 11 is one of the inside checks: a parent folder that is missing,
// or a coordinator's tree under the published folder, stops the start.
func TestRunInsideParentFoldersAreChecked(t *testing.T) {
	for name, mutate := range map[string]func(f *fakeSystem){
		"the mailbox is missing":     func(f *fakeSystem) { delete(f.nodes, profile.MailboxInside) },
		"the mail folder is refused": func(f *fakeSystem) { f.writable[profile.MailInside] = false },
		"the published folder can be written": func(f *fakeSystem) {
			f.writable[profile.CoordInside] = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			mutate(h.system)
			r, w := pipe(t)
			code, stderr := h.run(writeSpec(t, h.spec), w)
			if code != exitFailed || h.launchN != 0 {
				t.Fatalf("code %d launched %d", code, h.launchN)
			}
			rep, err := assert.ParseProbeLine(drain(t, r, w))
			if err != nil || !assert.Failed(rep, h.spec) {
				t.Fatalf("report: %v", err)
			}
			if !strings.Contains(stderr, "ataniq-probe:   assertion 11 ("+assert.IDParentFolders+"): failed") {
				t.Errorf("assertion 11 is not listed as failed: %q", stderr)
			}
		})
	}
	// With no coordinator identity in the spec the check cannot be made, and
	// that stops the start too.
	h := newHarness(t)
	h.spec.CoordinatorIdentities = nil
	r, w := pipe(t)
	code, stderr := h.run(writeSpec(t, h.spec), w)
	drain(t, r, w)
	if code != exitFailed || h.launchN != 0 || !strings.Contains(stderr, "assertion 11 ("+assert.IDParentFolders+"): not checked") {
		t.Errorf("code %d launched %d stderr %q", code, h.launchN, stderr)
	}
}

func TestRunInsideSetupFailures(t *testing.T) {
	h := newHarness(t)
	spec := writeSpec(t, h.spec)
	t.Run("the descriptor is not open", func(t *testing.T) {
		code, stderr := h.run(spec, 777)
		if code != exitSetup || h.launchN != 0 || !strings.Contains(stderr, "report descriptor 777") {
			t.Errorf("code %d stderr %q", code, stderr)
		}
	})
	t.Run("the spec is refused and nothing is written", func(t *testing.T) {
		r, w := pipe(t)
		bad := writeSpec(t, h.spec)
		if err := os.WriteFile(bad, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, _ := h.run(bad, w)
		if line := drain(t, r, w); code != exitSetup || len(line) != 0 || h.launchN != 0 {
			t.Errorf("code %d report %q launched %d", code, line, h.launchN)
		}
	})
	t.Run("a write that fails", func(t *testing.T) {
		r, w := pipe(t)
		unix.Close(r)
		code, stderr := h.run(spec, w)
		if code != exitSetup || h.launchN != 0 || !strings.Contains(stderr, "cannot write the report") {
			t.Errorf("code %d launched %d stderr %q", code, h.launchN, stderr)
		}
	})
}

func TestRunInsideLaunchStatusIsReturned(t *testing.T) {
	h := newHarness(t)
	h.launch = func([]string, []string, int, io.Writer) int { return 42 }
	r, w := pipe(t)
	if code, _ := h.run(writeSpec(t, h.spec), w); code != 42 {
		t.Errorf("code %d", code)
	}
	drain(t, r, w)
}

func TestAfterReportListsAtMostTheCap(t *testing.T) {
	spec := testSpec(t)
	rep := assert.RunInside(spec, passSystem(spec))
	var out bytes.Buffer
	if code := afterReport(rep, spec, &out); code != 0 || out.Len() != 0 {
		t.Fatalf("a passing report: %d %q", code, &out)
	}
	for i := range rep.Results {
		rep.Results[i].Status = assert.StatusFailed
		rep.Results[i].Detail = "bad\x1b[31m detail"
	}
	code := afterReport(rep, spec, &out)
	if code != exitFailed {
		t.Fatalf("code %d", code)
	}
	if strings.ContainsRune(out.String(), 0x1b) {
		t.Errorf("an escape reached standard error: %q", &out)
	}
	if n := strings.Count(out.String(), "\n"); n != 10 {
		t.Errorf("%d lines, want a header and 9 problems: %q", n, &out)
	}
	// A report of another digest is a failure too.
	rep2 := assert.RunInside(spec, passSystem(spec))
	rep2.Digest = strings.Repeat("cd", 32)
	out.Reset()
	if afterReport(rep2, spec, &out) != exitFailed || !strings.Contains(out.String(), "another profile digest") {
		t.Errorf("stderr %q", &out)
	}
}

func TestExecProgram(t *testing.T) {
	var gotPath string
	var gotArgv, gotEnv []string
	var out bytes.Buffer
	ok := func(path string, argv, env []string) error { gotPath, gotArgv, gotEnv = path, argv, env; return nil }
	closed := 0
	if code := execProgram("agent", []string{"/a", "b"}, []string{"E=1"}, &out, ok, func() error { closed++; return nil }); code != 0 ||
		gotPath != "/a" || strings.Join(gotArgv, " ") != "/a b" || strings.Join(gotEnv, " ") != "E=1" || closed != 1 {
		t.Errorf("code %d path %q argv %q env %q closed %d", code, gotPath, gotArgv, gotEnv, closed)
	}
	// A failure to close prevents the exec.
	gotPath = ""
	code := execProgram("agent", []string{"/a"}, nil, &out, ok, func() error { return io.ErrClosedPipe })
	if code != exitSetup || gotPath != "" || !strings.Contains(out.String(), "before the agent") {
		t.Errorf("code %d path %q stderr %q", code, gotPath, &out)
	}
	out.Reset()
	code = execProgram("agent", []string{"/a"}, nil, &out, func(string, []string, []string) error { return unix.ENOENT }, func() error { return nil })
	oneLine(t, out.String())
	if code != exitSetup || !strings.Contains(out.String(), "cannot execute the agent") {
		t.Errorf("code %d stderr %q", code, &out)
	}
}
