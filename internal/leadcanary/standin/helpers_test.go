//go:build linux && ataniq_canary

package standin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// fixtureRoot is the injection library's own screen folder. It holds one
// folder of recorded screens for each tested pair, and the replay tests copy
// two screens from the first folder that has them.
const fixtureRoot = "../../inject/screen/testdata"

// fixture returns the path, without its extension, of the recorded screen of
// that name in the first folder that has one.
func fixture(t testing.TB, name string) string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(fixtureRoot, "*", name+".json"))
	if err != nil || len(found) == 0 {
		t.Fatalf("no recorded screen %s under %s: %v", name, fixtureRoot, err)
	}
	return strings.TrimSuffix(found[0], ".json")
}

// The pieces of the canary message the tests paste.
const (
	testToken  = "0123456789abcdef-sample"
	pasteBegin = "\x1b[200~"
	pasteClose = "\x1b[201~"
)

func requireGit(t testing.TB) {
	t.Helper()
	if _, err := os.Stat(GitPath); err != nil {
		t.Fatalf("the tests need git at %s: %v", GitPath, err)
	}
}

// runGit runs git in dir for a test, with its own identity and no other
// configuration, and returns its output without the final line break.
func runGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(GitPath, args...)
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

// testRepo makes a temporary git repository on branch develop with one
// commit, and a home folder beside it that holds the identity git needs: the
// stand-in's git is given HOME and nothing else.
type testRepo struct {
	Dir, Home string
}

func newRepo(t testing.TB) testRepo {
	t.Helper()
	requireGit(t)
	base := t.TempDir()
	r := testRepo{Dir: filepath.Join(base, "tree"), Home: filepath.Join(base, "home")}
	for _, d := range []string{r.Dir, r.Home} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, r.Dir, "init", "--quiet", "--initial-branch=develop")
	runGit(t, r.Dir, "config", "user.name", "Sample")
	runGit(t, r.Dir, "config", "user.email", "sample@example.invalid")
	if err := os.WriteFile(filepath.Join(r.Dir, "README"), []byte("sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, r.Dir, "add", "README")
	runGit(t, r.Dir, "commit", "--quiet", "--message", "sample: first")
	return r
}

func (r testRepo) branch(t testing.TB) string {
	return runGit(t, r.Dir, "rev-parse", "--abbrev-ref", "HEAD")
}

func (r testRepo) head(t testing.TB) string { return runGit(t, r.Dir, "rev-parse", "HEAD") }

// ptyPair is a pseudo-terminal opened by the test: the stand-in gets the
// slave, the test is the terminal at the master.
type ptyPair struct {
	master, slave *os.File

	mu   sync.Mutex
	cond *sync.Cond
	out  []byte
}

func openPTY(t testing.TB) *ptyPair {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatalf("open /dev/ptmx: %v", err)
	}
	master := os.NewFile(uintptr(fd), "ptmx")
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPTLCK, 0); err != nil {
		t.Fatal(err)
	}
	n, err := unix.IoctlGetInt(fd, unix.TIOCGPTN)
	if err != nil {
		t.Fatal(err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := &ptyPair{master: master, slave: slave}
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

// send writes to the stand-in's input, as the terminal does.
func (p *ptyPair) send(t testing.TB, s string) {
	t.Helper()
	if _, err := p.master.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

// output returns everything the stand-in has written so far.
func (p *ptyPair) output() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.out...)
}

// waitFor waits until the output, from byte offset from, holds s, and returns
// the offset after it.
func (p *ptyPair) waitFor(t testing.TB, from int, s string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	timer := time.AfterFunc(10*time.Second, func() {
		p.mu.Lock()
		p.cond.Broadcast()
		p.mu.Unlock()
	})
	defer timer.Stop()
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		if from <= len(p.out) {
			if i := bytes.Index(p.out[from:], []byte(s)); i >= 0 {
				return from + i + len(s)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q in the output %q", s, p.out[min(from, len(p.out)):])
		}
		p.cond.Wait()
	}
}

// standinRun is a Run on a pty.
type standinRun struct {
	pty    *ptyPair
	cancel context.CancelFunc
	done   chan error
}

// startRun starts Run on a new pty with the repository as its working tree.
func startRun(t testing.TB, repo testRepo, wait time.Duration) *standinRun {
	t.Helper()
	p := openPTY(t)
	ctx, cancel := context.WithCancel(context.Background())
	r := &standinRun{pty: p, cancel: cancel, done: make(chan error, 1)}
	go func() {
		r.done <- Run(ctx, Config{
			Dir: repo.Dir, Home: repo.Home, In: p.slave, Out: p.slave,
			TermFD: int(p.slave.Fd()), ReplyWait: wait,
		})
	}()
	t.Cleanup(func() { cancel(); p.master.Close() })
	return r
}

// finish ends the run by closing the terminal's side, and returns Run's
// result.
func (r *standinRun) finish(t testing.TB) error {
	t.Helper()
	r.pty.master.Close()
	select {
	case err := <-r.done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

// pasteOf is a bracketed paste of text, as a terminal sends it.
func pasteOf(text string) string { return pasteBegin + text + pasteClose }

// waitUntil polls cond for up to 10 seconds.
func waitUntil(t testing.TB, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}
