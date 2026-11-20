//go:build linux && ataniq_canary

package main

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// asBinary makes the test binary run main when it is started with this
// variable, so that the tests run the command itself, with a terminal, and
// need no build.
const asBinary = "ATANIQ_STANDIN_TEST_AS_BINARY"

func TestMain(m *testing.M) {
	if os.Getenv(asBinary) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=/usr/bin:/bin", "HOME=" + dir, "LC_ALL=C",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Sample", "GIT_AUTHOR_EMAIL=sample@example.invalid",
		"GIT_COMMITTER_NAME=Sample", "GIT_COMMITTER_EMAIL=sample@example.invalid",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimRight(string(out), "\n")
}

// tree is a repository on develop with one commit, and a home beside it.
func tree(t testing.TB) (dir, home string) {
	t.Helper()
	base := t.TempDir()
	dir, home = filepath.Join(base, "tree"), filepath.Join(base, "home")
	for _, d := range []string{dir, home} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	git(t, dir, "init", "--quiet", "--initial-branch=develop")
	git(t, dir, "config", "user.name", "Sample")
	git(t, dir, "config", "user.email", "sample@example.invalid")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "README")
	git(t, dir, "commit", "--quiet", "--message", "sample: first")
	return dir, home
}

// terminal is a pty the test holds the master of.
type terminal struct {
	master, slave *os.File
	mu            sync.Mutex
	cond          *sync.Cond
	out           []byte
}

func openTerminal(t testing.TB) *terminal {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	master := os.NewFile(uintptr(fd), "ptmx")
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile("/dev/pts/"+strconv.Itoa(n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := &terminal{master: master, slave: slave}
	p.cond = sync.NewCond(&p.mu)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			p.mu.Lock()
			p.out = append(p.out, buf[:n]...)
			p.cond.Broadcast()
			p.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { master.Close(); slave.Close() })
	return p
}

func (p *terminal) waitFor(t testing.TB, from int, s string) int {
	t.Helper()
	stop := time.AfterFunc(10*time.Second, func() {
		p.mu.Lock()
		p.cond.Broadcast()
		p.mu.Unlock()
	})
	defer stop.Stop()
	deadline := time.Now().Add(10 * time.Second)
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		if from <= len(p.out) {
			if i := bytes.Index(p.out[from:], []byte(s)); i >= 0 {
				return from + i + len(s)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in %q", s, p.out)
		}
		p.cond.Wait()
	}
}

// start runs the command on the terminal, as a session of its own with the
// terminal for its controlling terminal.
func start(t testing.TB, tm *terminal, dir, home string, args ...string) (*exec.Cmd, <-chan error) {
	t.Helper()
	return startEnv(t, tm, dir, []string{"HOME=" + home}, args...)
}

// startEnv is start with the environment given: only that, and the switch
// that makes the test binary run the command.
func startEnv(t testing.TB, tm *terminal, dir string, env []string, args ...string) (*exec.Cmd, <-chan error) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Dir = dir
	cmd.Env = append([]string{asBinary + "=1"}, env...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tm.slave, tm.slave, tm.slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })
	return cmd, done
}

func wait(t testing.TB, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("the command did not exit")
		return nil
	}
}

const sampleToken = "0123456789abcdef-sample"

func TestCommandPlainModeCommitsAndExitsOnSIGTERM(t *testing.T) {
	dir, home := tree(t)
	first := git(t, dir, "rev-parse", "HEAD")
	tm := openTerminal(t)
	before, err := unix.IoctlGetTermios(int(tm.slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	cmd, done := start(t, tm, dir, home)
	i := tm.waitFor(t, 0, "> ")
	tm.waitFor(t, i-2, "\x1b[?2004h")

	msg := "[ataniq-msg id=3 from=ataniq kind=canary]\ncanary-token: " + sampleToken + "\n$(touch pwned)\n"
	if _, err := tm.master.WriteString("\x1b[200~" + msg + "\x1b[201~\r"); err != nil {
		t.Fatal(err)
	}
	tm.waitFor(t, i, "committed canary/one")
	if got := git(t, dir, "rev-parse", "--abbrev-ref", "HEAD"); got != "canary/one" {
		t.Fatalf("branch %q", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "canary-token.txt")); string(b) != sampleToken+"\n" {
		t.Fatalf("file %q", b)
	}
	if got := git(t, dir, "log", "-1", "--format=%s"); got != "canary: token" {
		t.Fatalf("subject %q", got)
	}
	if got := git(t, dir, "rev-parse", "HEAD^"); got != first {
		t.Fatalf("parent %s, want %s", got, first)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("text of the message ran")
	}

	raw, err := unix.IoctlGetTermios(int(tm.slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Lflag&(unix.ICANON|unix.ECHO) != 0 {
		t.Fatalf("the terminal is not raw while the command runs: %+v", raw)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, done); err != nil {
		t.Fatalf("exit after SIGTERM: %v, want status 0", err)
	}
	after, err := unix.IoctlGetTermios(int(tm.slave.Fd()), unix.TCGETS)
	if err != nil {
		t.Fatal(err)
	}
	if *after != *before {
		t.Fatalf("the terminal was not restored:\nbefore %+v\nafter  %+v", before, after)
	}
}

// The sandbox names the identity file with GIT_CONFIG_GLOBAL and the tree has
// no identity of its own: the command hands the variable to git.
func TestCommandCommitsWithTheIdentityGitConfigGlobalNames(t *testing.T) {
	dir, home := tree(t)
	git(t, dir, "config", "--unset", "user.name")
	git(t, dir, "config", "--unset", "user.email")
	git(t, dir, "config", "user.useConfigOnly", "true") // no guess from the host's account
	ident := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(ident, []byte("[user]\n\tname = Provisioned\n\temail = provisioned@example.invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tm := openTerminal(t)
	cmd, done := startEnv(t, tm, dir, []string{"HOME=" + home, "GIT_CONFIG_GLOBAL=" + ident})
	i := tm.waitFor(t, 0, "> ")
	tm.waitFor(t, i-2, "\x1b[?2004h")
	msg := "[ataniq-msg id=3 from=ataniq kind=canary]\ncanary-token: " + sampleToken + "\n"
	if _, err := tm.master.WriteString("\x1b[200~" + msg + "\x1b[201~\r"); err != nil {
		t.Fatal(err)
	}
	tm.waitFor(t, i, "committed canary/one")
	if got := git(t, dir, "log", "-1", "--format=%an <%ae>"); got != "Provisioned <provisioned@example.invalid>" {
		t.Fatalf("identity %q", got)
	}
	cmd.Process.Signal(syscall.SIGTERM)
	wait(t, done)
}

func TestCommandChoosesReplayModeFromTheSwitchFile(t *testing.T) {
	dir, home := tree(t)
	rd := filepath.Join(home, "canary-replay")
	if err := os.Mkdir(rd, 0o700); err != nil {
		t.Fatal(err)
	}
	screen := `{"cols": 40, "rows": 10, "cursor_x": 2, "cursor_y": 1, "alt_screen": true, "title": "sample", "text": "sample screen\n> "}`
	for name, content := range map[string]string{
		"1.json": screen, "2.json": strings.Replace(screen, "sample screen", "second screen", 1),
		"report.txt": "state=idle:app=sample-app\n", "replay.on": "",
	} {
		if err := os.WriteFile(filepath.Join(rd, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tm := openTerminal(t)
	cmd, done := start(t, tm, dir, home)
	i := tm.waitFor(t, 0, "\x1b]7501;?\x1b\\\x1b[c")
	tm.waitFor(t, i, "sample screen")
	if _, err := tm.master.WriteString("\x1b]7501;?\x1b\\\x1b[?62c"); err != nil {
		t.Fatal(err)
	}
	tm.waitFor(t, i, "\x1b]7501;state=idle:app=sample-app\x1b\\")
	if _, err := tm.master.WriteString("\x1b[200~canary-token: " + sampleToken + "\x1b[201~"); err != nil {
		t.Fatal(err)
	}
	tm.waitFor(t, i, "second screen")
	if _, err := tm.master.WriteString("\r"); err != nil {
		t.Fatal(err)
	}
	// The branch is switched to before the file is written and committed, so
	// the commit itself is what is waited for.
	for end := time.Now().Add(10 * time.Second); git(t, dir, "rev-parse", "--abbrev-ref", "HEAD") != "canary/one" ||
		git(t, dir, "log", "-1", "--format=%s") != "canary: token"; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("no commit")
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "canary-token.txt")); string(b) != sampleToken+"\n" {
		t.Fatalf("file %q", b)
	}
	if b, err := os.ReadFile(filepath.Join(rd, "order.txt")); err != nil || string(b) != "7501-first\n" {
		t.Fatalf("order.txt %q, %v", b, err)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := wait(t, done); err != nil {
		t.Fatalf("exit after SIGTERM: %v", err)
	}
}

func TestCommandExitsZeroAtEndOfInput(t *testing.T) {
	var out, errOut bytes.Buffer
	if status := run(nil, strings.NewReader("abc"), &out, &errOut, "", "", -1); status != 0 {
		t.Fatalf("status %d, stderr %q", status, errOut.String())
	}
}

func TestCommandNeedsATerminal(t *testing.T) {
	var out, errOut bytes.Buffer
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if status := run(nil, f, &out, &errOut, "", "", int(f.Fd())); status != 1 {
		t.Fatalf("status %d, want 1", status)
	}
	if out.Len() != 0 || !strings.Contains(errOut.String(), "terminal") {
		t.Fatalf("stdout %q, stderr %q", out.String(), errOut.String())
	}
}

func TestCommandVersionAndUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if status := run([]string{"--version"}, strings.NewReader(""), &out, &errOut, "", "", -1); status != 0 || !strings.Contains(out.String(), "ataniq_canary") {
		t.Fatalf("--version: status %d, output %q", status, out.String())
	}
	out.Reset()
	for _, args := range [][]string{{"--help"}, {"x"}, {"--version", "x"}} {
		if status := run(args, strings.NewReader(""), &out, &errOut, "", "", -1); status != 2 || out.Len() != 0 {
			t.Errorf("%v: status %d, output %q", args, status, out.String())
		}
	}
}

// The command is a static program: nothing in it, or in the package it wires,
// imports cgo, the network or the account database, which would link a C
// library.
func TestCommandImportsNothingThatNeedsCgo(t *testing.T) {
	for _, dir := range []string{".", "../../internal/leadcanary/standin"} {
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("%s: %v", dir, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			af, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range af.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				switch {
				case p == "C", p == "plugin", p == "os/user", p == "net", strings.HasPrefix(p, "net/"):
					t.Errorf("%s imports %s", f, p)
				case strings.Contains(p, "."):
					if p != "golang.org/x/sys/unix" {
						t.Errorf("%s imports %s", f, p)
					}
				case strings.HasPrefix(p, "ataniq/"):
					if p != "ataniq/internal/leadcanary/standin" && p != "ataniq/internal/inject/osc7501" {
						t.Errorf("%s imports %s", f, p)
					}
				}
			}
		}
	}
}
