//go:build linux

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/assert"
)

// The end-to-end tests run the test binary again as the probe (TestMain, with
// modeEnv set), with a spec under t.TempDir(), a pipe as the report
// descriptor and the test binary once more as the agent. No namespace is made.

const guard = 20 * time.Second

func testExe(t *testing.T) string {
	t.Helper()
	p, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// runOpts says how a test starts the probe.
type runOpts struct {
	mode    string   // modeFake or modeReal
	tty     *os.File // the slave side of a PTY as descriptors 0 to 2; nil: /dev/null and buffers
	setpgid bool     // the probe leads its process group
	leak    int      // inherited extra descriptors from 4 up (descriptor 3 is the report pipe)
	exit    int      // the agent's exit status
	hold    bool     // the agent waits for a byte on descriptor 0 after READY
}

// probeRun is a started probe.
type probeRun struct {
	t      *testing.T
	cmd    *exec.Cmd
	env    []string
	stderr *bytes.Buffer
	report *os.File
	out    string // the file the agent writes
	waited bool
}

func baseEnv(mode string) []string { return []string{modeEnv + "=" + mode, "FOO=bar", "EMPTY="} }

// startInside starts the probe in the inside mode and returns at once.
func startInside(t *testing.T, o runOpts, specPath string, reportFD string) *probeRun {
	t.Helper()
	exe := testExe(t)
	out := filepath.Join(t.TempDir(), "agent.json")
	hold := "0"
	if o.hold {
		hold = "1"
	}
	args := []string{"--spec", specPath, "--report-fd", reportFD, "--", exe, "agent", out, strconv.Itoa(o.exit), hold}
	return start(t, o, args, out, true)
}

func start(t *testing.T, o runOpts, args []string, out string, withReport bool) *probeRun {
	t.Helper()
	r := &probeRun{t: t, env: baseEnv(o.mode), stderr: &bytes.Buffer{}, out: out}
	cmd := exec.Command(testExe(t), args...)
	cmd.Env = r.env
	var closeAfter []*os.File
	if withReport {
		rd, wr, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		r.report = rd
		cmd.ExtraFiles = append(cmd.ExtraFiles, wr)
		closeAfter = append(closeAfter, wr)
		t.Cleanup(func() { rd.Close() })
	}
	for i := 0; i < o.leak; i++ {
		f, err := os.Open("/dev/null")
		if err != nil {
			t.Fatal(err)
		}
		cmd.ExtraFiles = append(cmd.ExtraFiles, f)
		closeAfter = append(closeAfter, f)
	}
	if o.tty != nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = o.tty, o.tty, o.tty
	} else {
		cmd.Stdout, cmd.Stderr = r.stderr, r.stderr
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: o.setpgid}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for _, f := range closeAfter {
		f.Close()
	}
	r.cmd = cmd
	t.Cleanup(func() {
		if !r.waited {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return r
}

// readReport reads the report descriptor to its end.
func (r *probeRun) readReport() []byte {
	r.t.Helper()
	r.report.SetReadDeadline(time.Now().Add(guard))
	data, err := io.ReadAll(r.report)
	if err != nil {
		r.t.Fatalf("reading the report descriptor: %v", err)
	}
	return data
}

// wait waits for the probe and returns its exit status, which is -1 when a
// signal ended it.
func (r *probeRun) wait() int {
	r.t.Helper()
	done := make(chan error, 1)
	go func() { done <- r.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(guard):
		r.cmd.Process.Kill()
		<-done
		r.t.Fatal("the probe did not exit")
	}
	r.waited = true
	return r.cmd.ProcessState.ExitCode()
}

// agent reads what the agent wrote, or fails.
func (r *probeRun) agent() agentReport {
	r.t.Helper()
	data, err := os.ReadFile(r.out)
	if err != nil {
		r.t.Fatalf("the agent left nothing: %v (stderr: %s)", err, r.stderr)
	}
	var a agentReport
	if err := json.Unmarshal(data, &a); err != nil {
		r.t.Fatalf("agent output: %v", err)
	}
	return a
}

func (r *probeRun) noAgent() {
	r.t.Helper()
	if _, err := os.Stat(r.out); !errors.Is(err, os.ErrNotExist) {
		r.t.Errorf("the agent ran: %v", err)
	}
}

func sorted(s []string) []string {
	c := slices.Clone(s)
	slices.Sort(c)
	return c
}

// oneLine says the text is a single line that starts with the command name.
func oneLine(t *testing.T, text string) {
	t.Helper()
	if !strings.HasPrefix(text, "ataniq-probe: ") || strings.Count(text, "\n") != 1 || !strings.HasSuffix(text, "\n") {
		t.Errorf("want one line starting with the command name, got %q", text)
	}
}

func TestInsideRealSystemFailsAndStartsNothing(t *testing.T) {
	spec := testSpec(t)
	r := startInside(t, runOpts{mode: modeReal, leak: 1}, writeSpec(t, spec), "3")
	line := r.readReport()
	if code := r.wait(); code != exitFailed {
		t.Fatalf("exit %d, want %d (stderr: %s)", code, exitFailed, r.stderr)
	}
	rep, err := assert.ParseProbeLine(line)
	if err != nil {
		t.Fatalf("the report line is refused: %v\n%q", err, line)
	}
	if rep.Digest != spec.Digest || len(rep.Results) != 8 {
		t.Errorf("report %+v", rep)
	}
	if !assert.Failed(rep, spec) {
		t.Errorf("a developer host cannot hold the assertions, yet the report passes: %+v", rep.Results)
	}
	r.noAgent()
	lines := strings.Split(strings.TrimSuffix(r.stderr.String(), "\n"), "\n")
	if len(lines) < 2 || !strings.Contains(lines[0], "the inside checks did not pass") || !strings.Contains(lines[0], "the agent is not started") {
		t.Fatalf("stderr %q", r.stderr)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l, "ataniq-probe: ") {
			t.Errorf("stderr line %q", l)
		}
	}
	if !strings.Contains(r.stderr.String(), "assertion 7 (") {
		t.Errorf("assertion 7 is not named: %s", r.stderr)
	}
}

func TestInsideFakePassStartsTheAgent(t *testing.T) {
	spec := testSpec(t)
	r := startInside(t, runOpts{mode: modeFake, leak: 2, exit: 7}, writeSpec(t, spec), "3")
	line := r.readReport() // the end of the report is the agent's exec
	if code := r.wait(); code != 7 {
		t.Fatalf("exit %d, want the agent's 7 (stderr: %s)", code, r.stderr)
	}
	rep, err := assert.ParseProbeLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if assert.Failed(rep, spec) {
		t.Fatalf("the fake does not pass: %v", assert.Problems(rep, spec, assert.RequireInside))
	}
	if bytes.Count(line, []byte("\n")) != 1 || !bytes.HasPrefix(line, []byte(assert.ReportPrefix)) {
		t.Errorf("not exactly one line: %q", line)
	}
	a := r.agent()
	if len(a.FDs) != 0 {
		t.Errorf("the agent has descriptors above 2: %+v", a.FDs)
	}
	if !slices.Equal(sorted(a.Env), sorted(r.env)) {
		t.Errorf("environment %q, want %q", a.Env, r.env)
	}
	if a.PID != r.cmd.Process.Pid {
		t.Errorf("the agent is pid %d, not the probe's %d: it was not executed in place", a.PID, r.cmd.Process.Pid)
	}
	if a.SID == a.PID {
		t.Errorf("a session was made without a terminal")
	}
	if !strings.Contains(r.stderr.String(), "descriptor 0 is not a terminal") || strings.Count(r.stderr.String(), "\n") != 1 {
		t.Errorf("stderr %q", r.stderr)
	}
}

func TestInsideSpecFailures(t *testing.T) {
	dir := t.TempDir()
	good, _ := assert.EncodeSpec(testSpec(t))
	write := func(name string, data []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(write("real.json", good), link); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(dir, "spec.fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, path, want string }{
		{"garbage", write("garbage.json", []byte("not json")), "assert: spec:"},
		{"unknown field", write("unknown.json", bytes.Replace(good, []byte(`"version"`), []byte(`"extra": 1, "version"`), 1)), "assert: spec:"},
		{"too large", write("big.json", bytes.Repeat([]byte(" "), assert.MaxSpecBytes+10)), "larger than"},
		{"empty", write("empty.json", nil), "assert: spec:"},
		{"missing", filepath.Join(dir, "none.json"), "no such file"},
		{"a link", link, "symbolic link"},
		{"a folder", dir, "not a regular file"},
		{"a FIFO", fifo, "not a regular file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := startInside(t, runOpts{mode: modeFake}, c.path, "3")
			if line := r.readReport(); len(line) != 0 {
				t.Errorf("a report was written: %q", line)
			}
			if code := r.wait(); code != exitSetup {
				t.Errorf("exit %d, want %d", code, exitSetup)
			}
			oneLine(t, r.stderr.String())
			if !strings.Contains(r.stderr.String(), c.want) {
				t.Errorf("stderr %q does not contain %q", r.stderr, c.want)
			}
			r.noAgent()
		})
	}
}

func TestInsideReportDescriptorFailures(t *testing.T) {
	spec := writeSpec(t, testSpec(t))
	t.Run("not open", func(t *testing.T) {
		r := startInside(t, runOpts{mode: modeFake}, spec, "9")
		r.readReport()
		if code := r.wait(); code != exitSetup {
			t.Errorf("exit %d", code)
		}
		oneLine(t, r.stderr.String())
		if !strings.Contains(r.stderr.String(), "report descriptor 9") {
			t.Errorf("stderr %q", r.stderr)
		}
		r.noAgent()
	})
	t.Run("the reader is gone", func(t *testing.T) {
		exe := testExe(t)
		out := filepath.Join(t.TempDir(), "agent.json")
		rd, wr, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		rd.Close()
		var stderr bytes.Buffer
		cmd := exec.Command(exe, "--spec", spec, "--report-fd", "3", "--", exe, "agent", out, "0", "0")
		cmd.Env = baseEnv(modeFake)
		cmd.ExtraFiles = []*os.File{wr}
		cmd.Stderr = &stderr
		err = cmd.Run()
		wr.Close()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != exitSetup {
			t.Fatalf("got %v, want exit %d (stderr %s)", err, exitSetup, &stderr)
		}
		oneLine(t, stderr.String())
		if !strings.Contains(stderr.String(), "cannot write the report") {
			t.Errorf("stderr %q", &stderr)
		}
		if _, err := os.Stat(out); err == nil {
			t.Error("the agent ran")
		}
	})
	t.Run("not writable", func(t *testing.T) {
		exe := testExe(t)
		out := filepath.Join(t.TempDir(), "agent.json")
		rd, wr, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer rd.Close()
		defer wr.Close()
		var stderr bytes.Buffer
		cmd := exec.Command(exe, "--spec", spec, "--report-fd", "3", "--", exe, "agent", out, "0", "0")
		cmd.Env = baseEnv(modeFake)
		cmd.ExtraFiles = []*os.File{rd} // the reading end: a write is refused
		cmd.Stderr = &stderr
		err = cmd.Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != exitSetup || !strings.Contains(stderr.String(), "cannot write the report") {
			t.Fatalf("got %v, stderr %q", err, &stderr)
		}
		if _, err := os.Stat(out); err == nil {
			t.Error("the agent ran")
		}
	})
}

func TestInsideUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--spec", "/x", "--report-fd", "3"},
		{"--spec", "/x", "--report-fd", "2", "--", "/bin/true"},
		{"--spec", "/x", "--report-fd", "3", "--", "true"},
		{"--spec", "/x", "--report-fd", "3", "--"},
		{"--spec", "/x", "--report-fd", "3", "--bogus", "--", "/bin/true"},
		{"pass-fds"},
		{"pass-fds", "--read", "2=/x", "--", "/bin/true"},
	} {
		var stderr, stdout bytes.Buffer
		cmd := exec.Command(testExe(t), args...)
		cmd.Env = baseEnv(modeFake)
		cmd.Stderr, cmd.Stdout = &stderr, &stdout
		err := cmd.Run()
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != exitSetup {
			t.Errorf("%q: got %v", args, err)
			continue
		}
		oneLine(t, stderr.String())
		if !strings.HasPrefix(stderr.String(), "ataniq-probe: usage: ") || stdout.Len() != 0 {
			t.Errorf("%q: stderr %q stdout %q", args, &stderr, &stdout)
		}
	}
}

func TestInsideAgentCannotBeExecuted(t *testing.T) {
	spec := testSpec(t)
	exe := testExe(t)
	rd, wr, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer rd.Close()
	var stderr bytes.Buffer
	cmd := exec.Command(exe, "--spec", writeSpec(t, spec), "--report-fd", "3", "--", "/nonexistent/agent")
	cmd.Env = baseEnv(modeFake)
	cmd.ExtraFiles = []*os.File{wr}
	cmd.Stderr = &stderr
	err = cmd.Run()
	wr.Close()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != exitSetup {
		t.Fatalf("got %v", err)
	}
	// The report was written first and passes: the holder reads the status.
	data, _ := io.ReadAll(rd)
	if rep, err := assert.ParseProbeLine(data); err != nil || assert.Failed(rep, spec) {
		t.Errorf("report %q: %v", data, err)
	}
	// stderr holds the not-a-terminal note, then the failure.
	lines := strings.Split(strings.TrimSuffix(stderr.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "cannot execute the agent") {
		t.Errorf("stderr %q", &stderr)
	}
}

// openPTY opens a pseudo terminal pair. The slave is opened with O_NOCTTY so
// that the test process does not take it as its terminal.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx: %v", err)
	}
	t.Cleanup(func() { master.Close() })
	sc, err := master.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var n int
	var ioerr error
	err = sc.Control(func(fd uintptr) {
		if ioerr = unix.IoctlSetPointerInt(int(fd), unix.TIOCSPTLCK, 0); ioerr != nil {
			return
		}
		n, ioerr = unix.IoctlGetInt(int(fd), unix.TIOCGPTN)
	})
	if err != nil || ioerr != nil {
		t.Skipf("cannot unlock the pseudo terminal: %v %v", err, ioerr)
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("cannot open the pseudo terminal slave: %v", err)
	}
	t.Cleanup(func() { slave.Close() })
	return master, slave
}

// waitReady reads the master until the agent has said READY.
func waitReady(t *testing.T, master *os.File) {
	t.Helper()
	master.SetReadDeadline(time.Now().Add(guard))
	var seen []byte
	buf := make([]byte, 256)
	for !bytes.Contains(seen, []byte("READY")) {
		n, err := master.Read(buf)
		seen = append(seen, buf[:n]...)
		if err != nil {
			t.Fatalf("waiting for the agent: %v (read %q)", err, seen)
		}
	}
}

// release lets the held agent go.
func release(t *testing.T, master *os.File) {
	t.Helper()
	if _, err := master.Write([]byte("x\n")); err != nil {
		t.Fatal(err)
	}
}

func checkNewSession(t *testing.T, a agentReport) {
	t.Helper()
	ours, err := unix.Getsid(0)
	if err != nil {
		t.Fatal(err)
	}
	if a.SID == ours {
		t.Errorf("the agent is in the test's session %d", ours)
	}
	if a.SID != a.PID || a.PGID != a.PID {
		t.Errorf("the agent leads its session? pid %d sid %d pgid %d", a.PID, a.SID, a.PGID)
	}
	if a.TIOCGSID != a.SID {
		t.Errorf("descriptor 0 is the controlling terminal of session %d, the agent's is %d", a.TIOCGSID, a.SID)
	}
	if !a.DevTTY {
		t.Error("/dev/tty does not open: the agent has no controlling terminal")
	}
	if len(a.FDs) != 0 {
		t.Errorf("descriptors above 2: %+v", a.FDs)
	}
}

func TestInsideNewSessionWithTerminal(t *testing.T) {
	master, slave := openPTY(t)
	spec := testSpec(t)
	r := startInside(t, runOpts{mode: modeFake, tty: slave, hold: true, leak: 1}, writeSpec(t, spec), "3")
	slave.Close()
	waitReady(t, master)
	a := r.agent()
	if a.PID != r.cmd.Process.Pid {
		t.Errorf("the agent is pid %d, the probe %d: not executed in place", a.PID, r.cmd.Process.Pid)
	}
	checkNewSession(t, a)
	if !slices.Equal(sorted(a.Env), sorted(r.env)) {
		t.Errorf("environment %q, want %q", a.Env, r.env)
	}
	// The end of the report is the exec, which happened.
	line := r.readReport()
	if rep, err := assert.ParseProbeLine(line); err != nil || assert.Failed(rep, spec) {
		t.Errorf("report %q: %v", line, err)
	}
	release(t, master)
	if code := r.wait(); code != 0 {
		t.Errorf("exit %d", code)
	}
}

func TestInsideNewSessionFromAGroupLeader(t *testing.T) {
	master, slave := openPTY(t)
	spec := testSpec(t)
	r := startInside(t, runOpts{mode: modeFake, tty: slave, hold: true, setpgid: true, exit: 5, leak: 1}, writeSpec(t, spec), "3")
	slave.Close()
	waitReady(t, master)
	a := r.agent()
	if a.PID == r.cmd.Process.Pid {
		t.Errorf("setsid is refused for a group leader, yet the agent is the probe")
	}
	checkNewSession(t, a)
	if !slices.Equal(sorted(a.Env), sorted(r.env)) {
		t.Errorf("environment %q, want %q", a.Env, r.env)
	}
	// The probe, which waits, holds no descriptor of the report: end of file
	// arrives while the agent is alive and held.
	line := r.readReport()
	if rep, err := assert.ParseProbeLine(line); err != nil || assert.Failed(rep, spec) {
		t.Errorf("report %q: %v", line, err)
	}
	release(t, master)
	if code := r.wait(); code != 5 {
		t.Errorf("exit %d, want the agent's 5", code)
	}
}

func TestInsideForwardsSignalsToTheAgent(t *testing.T) {
	master, slave := openPTY(t)
	r := startInside(t, runOpts{mode: modeFake, tty: slave, hold: true, setpgid: true}, writeSpec(t, testSpec(t)), "3")
	slave.Close()
	waitReady(t, master)
	if a := r.agent(); a.PID == r.cmd.Process.Pid {
		t.Fatal("no waiting parent")
	}
	if err := r.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if code := r.wait(); code != 128+int(syscall.SIGTERM) {
		t.Errorf("exit %d, want %d (the agent was ended by SIGTERM)", code, 128+int(syscall.SIGTERM))
	}
}

func TestInsideFailureWithATerminalStartsNothing(t *testing.T) {
	master, slave := openPTY(t)
	spec := testSpec(t)
	r := startInside(t, runOpts{mode: modeReal, tty: slave}, writeSpec(t, spec), "3")
	slave.Close()
	line := r.readReport()
	if code := r.wait(); code != exitFailed {
		t.Fatalf("exit %d", code)
	}
	if rep, err := assert.ParseProbeLine(line); err != nil || !assert.Failed(rep, spec) {
		t.Errorf("report %q: %v", line, err)
	}
	r.noAgent()
	// The failed results go to the terminal too.
	master.SetReadDeadline(time.Now().Add(guard))
	buf := make([]byte, 4096)
	n, _ := master.Read(buf)
	if !bytes.Contains(buf[:n], []byte("the inside checks did not pass")) {
		t.Errorf("the terminal got %q", buf[:n])
	}
}
