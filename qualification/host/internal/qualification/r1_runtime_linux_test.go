//go:build linux && amd64

package qualification

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestR1PinnedFixedControlReader(t *testing.T) {
	root := t.TempDir()
	c := &r1FakeClock{time.Unix(1, 0)}
	s, err := r1OpenControl(root, c, c.Now().Add(R1MainLifetime))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Read(R1ReleaseName, c.Now().Add(R1MainLifetime)); !errors.Is(err, errR1Absent) {
		t.Fatal(err)
	}
	for _, name := range []string{"../context.json", "other.json", ".temporary", "/context.json"} {
		if _, err := s.Read(name, c.Now().Add(R1MainLifetime)); err == nil || errors.Is(err, errR1Absent) {
			t.Fatal("nonfixed read", name, err)
		}
	}
	put := func(name string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, name), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(R1ContextName, []byte("context bytes\n"))
	f, err := s.Read(R1ContextName, c.Now().Add(R1MainLifetime))
	if err != nil || string(f.Data) != "context bytes\n" {
		t.Fatal(f, err)
	}
	// Publication of later fixed records does not invalidate directory identity.
	put(R1ReleaseName, bytes.Repeat([]byte{'x'}, R1MaxFrame))
	if _, err := s.Read(R1ReleaseName, c.Now().Add(R1MainLifetime)); err != nil {
		t.Fatal(err)
	}
	put(R1FinishName, bytes.Repeat([]byte{'x'}, R1MaxFrame+1))
	if _, err := s.Read(R1FinishName, c.Now().Add(R1MainLifetime)); err == nil {
		t.Fatal("oversize accepted")
	}
	if err := os.Remove(filepath.Join(root, R1ReleaseName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(R1ContextName, filepath.Join(root, R1ReleaseName)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(R1ReleaseName, c.Now().Add(R1MainLifetime)); err == nil {
		t.Fatal("symlink followed")
	}
	if err := os.Remove(filepath.Join(root, R1ReleaseName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, R1ContextName), filepath.Join(root, R1ReleaseName)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(R1ContextName, c.Now().Add(R1MainLifetime)); err == nil {
		t.Fatal("hardlink accepted")
	}
	if err := os.Remove(filepath.Join(root, R1ReleaseName)); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(root, R1ReleaseName), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(R1ReleaseName, c.Now().Add(R1MainLifetime)); err == nil {
		t.Fatal("fifo admitted")
	}
	if err := os.Remove(filepath.Join(root, R1ReleaseName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, R1ReleaseName), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(R1ReleaseName, c.Now().Add(R1MainLifetime)); err == nil {
		t.Fatal("directory admitted")
	}
}

func TestR1ReaderRejectsDirectoryReplacementAndSubjectDrift(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "control")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	c := &r1FakeClock{time.Unix(1, 0)}
	s, err := r1OpenControl(root, c, c.Now().Add(R1MainLifetime))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	path := filepath.Join(root, R1ContextName)
	if err := os.WriteFile(path, []byte("same"), 0600); err != nil {
		t.Fatal(err)
	}
	old, err := s.Read(R1ContextName, c.Now().Add(R1MainLifetime))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(root, "old-context")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("same"), 0600); err != nil {
		t.Fatal(err)
	}
	current, err := s.Read(R1ContextName, c.Now().Add(R1MainLifetime))
	if err != nil || current.Subject == old.Subject || !bytes.Equal(current.Data, old.Data) {
		t.Fatal("replacement identity lost", err)
	}
	if err := os.WriteFile(path, []byte("drift"), 0600); err != nil {
		t.Fatal(err)
	}
	changed, err := s.Read(R1ContextName, c.Now().Add(R1MainLifetime))
	if err != nil || changed.Subject == current.Subject {
		t.Fatal("content subject drift lost", err)
	}
	if err := os.Rename(root, filepath.Join(parent, "old-control")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(R1ReleaseName, c.Now().Add(R1MainLifetime)); err == nil || errors.Is(err, errR1Absent) {
		t.Fatal("lost directory treated as missing release", err)
	}
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(); err == nil {
		t.Fatal("replacement directory adopted")
	}
}

func TestR1PrivateOwnedChild(t *testing.T) {
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "r1-owned-child" {
		os.Exit(R1PrivateChild())
	}
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "r1-owned-stalled" {
		if syscall.SetNonblock(4, true) != nil {
			os.Exit(23)
		}
		// Deliberately keep the identity pipe open without a handshake. This
		// child has no pressure allocation and only accepts the private token.
		os.Exit(r1ChildTokens(r1WallClock{}, time.Now().Add(2*time.Second), r1RawRead(4)))
	}
	if len(os.Args) > 1 && os.Args[len(os.Args)-1] == "r1-owned-exit" {
		// Publish the real identity, then terminate before corroboration.
		hello := os.NewFile(3, "test-private-hello")
		os.Exit(runR1Child(r1WallClock{}, strings.NewReader(r1AbortToken).Read, r1RawWrite(3), func() (R1ActorIdentity, error) {
			return r1LocalIdentity(os.Getpid())
		}, hello.Close))
	}
}

type r1RecordedExecChild struct {
	*r1ExecChild
	readyID     R1ActorIdentity
	readyErr    error
	ends        int
	endStart    time.Time
	endDeadline time.Time
}

func (c *r1RecordedExecChild) Ready(d time.Time) (R1ActorIdentity, error) {
	c.readyID, c.readyErr = c.r1ExecChild.Ready(d)
	return c.readyID, c.readyErr
}
func (c *r1RecordedExecChild) End(token string, d time.Time) (R1ChildResult, error) {
	c.ends++
	c.endStart, c.endDeadline = time.Now(), d
	return c.r1ExecChild.End(token, d)
}

func TestR1UncorroboratedHandshakeKeepsCleanupHandle(t *testing.T) {
	for _, earlyDeath := range []bool{false, true} {
		t.Run(map[bool]string{false: "metadata-denied", true: "early-death"}[earlyDeath], func(t *testing.T) {
			var watchdog *time.Timer
			mode := "r1-owned-child"
			if earlyDeath {
				mode = "r1-owned-exit"
			}
			child, err := r1SpawnWith(r1WallClock{}, func(cmd *exec.Cmd) error {
				cmd.Args = []string{"probe", "-test.run=^TestR1PrivateOwnedChild$", "--", mode}
				if err := cmd.Start(); err != nil {
					return err
				}
				watchdog = time.AfterFunc(3*time.Second, func() { _ = cmd.Process.Kill() })
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			raw := child.(*r1ExecChild)
			t.Cleanup(func() { _, _ = raw.End("abort", time.Now().Add(R1ReapWait)); watchdog.Stop() })
			pid, process := raw.pid, raw.process
			lookups := 0
			raw.localIdentity = func(got int) (R1ActorIdentity, error) {
				lookups++
				if got != pid {
					t.Fatal("lost owned PID")
				}
				if earlyDeath {
					deadline := time.Now().Add(R1ReapWait)
					for {
						ended, err := raw.poll()
						if err != nil {
							t.Fatal(err)
						}
						if ended {
							break
						}
						if r1Pause(raw.clock, deadline) != nil {
							t.Fatal("owned early exit did not terminate within bound")
						}
					}
				}
				return R1ActorIdentity{}, syscall.EACCES
			}
			recorded := &r1RecordedExecChild{r1ExecChild: raw}
			f := newR1Fixture(t)
			f.d.clock = r1WallClock{}
			f.d.spawn = func() (r1Child, error) { f.spawned++; return recorded, nil }
			if runR1(f.d) == nil {
				t.Fatal("metadata failure became success")
			}
			if lookups == 0 && errors.Is(recorded.readyErr, errR1Metadata) {
				t.Skip("unsupported child metadata before handshake: real admission unqualified")
			}
			failure := f.last(t)
			if !errors.Is(recorded.readyErr, errR1Metadata) || recorded.readyID != (R1ActorIdentity{}) || raw.id != nil || failure.Child.Identity != nil {
				t.Fatal("uncorroborated identity escaped", recorded.readyID, recorded.readyErr, failure)
			}
			if f.allocated != 0 || f.spawned != 1 || len(f.frames) != 1 || failure.Stage != "child-ready" || failure.Error != "metadata-unsupported" {
				t.Fatal("effects after failed admission", failure)
			}
			if raw.pid != pid || raw.process != process || recorded.ends != 1 || recorded.endDeadline.After(recorded.endStart.Add(R1ReapWait)) {
				t.Fatal("cleanup handle/bound lost", recorded)
			}
			if failure.Child.Reap != "reaped" || failure.Child.ExitCode == nil || *failure.Child.ExitCode != 20 {
				t.Fatal("owned child's actual terminal evidence lost", failure)
			}
			wantToken := "abort"
			if earlyDeath {
				wantToken = "none"
			}
			if failure.Child.Token != wantToken {
				t.Fatal("invented cleanup token", failure.Child)
			}
		})
	}
}

func TestR1RealPrivateHandshakeOnly(t *testing.T) {
	// This runs only the fixed child, never the main actor or mmap helper.
	var watchdog *time.Timer
	child, err := r1SpawnWith(r1WallClock{}, func(cmd *exec.Cmd) error {
		cmd.Args = []string{"probe", "-test.run=^TestR1PrivateOwnedChild$", "--", "r1-owned-child"}
		if len(cmd.Env) != 0 || cmd.Path != "/proc/self/exe" || len(cmd.ExtraFiles) != 2 || cmd.Stdin != nil || cmd.Stdout != nil || cmd.Stderr != nil || cmd.SysProcAttr != nil {
			t.Fatal("nonfixed child startup")
		}
		if err := cmd.Start(); err != nil {
			return err
		}
		watchdog = time.AfterFunc(3*time.Second, func() { _ = cmd.Process.Kill() })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { watchdog.Stop(); _, _ = child.End("abort", time.Now().Add(R1ReapWait)) })
	id, err := child.Ready(time.Now().Add(R1ReadyWait))
	if errors.Is(err, errR1Metadata) {
		t.Skip("unsupported process metadata: real private child admission unqualified")
	}
	if err != nil || !id.valid() {
		t.Fatal(id, err)
	}
	if err := child.Alive(); err != nil {
		t.Fatal(err)
	}
	r, err := child.End("finish", time.Now().Add(R1ReapWait))
	if err != nil || !r.valid() || r.Identity == nil || *r.Identity != id || r.Reap != "reaped" || r.ExitCode == nil || *r.ExitCode != 0 {
		t.Fatal(r, err)
	}
	t.Log("private handshake and actual child reap observed; pressure actor unexecuted")
}

func TestR1StalledPrivateHandshakeRemainsNonblocking(t *testing.T) {
	var watchdog *time.Timer
	child, err := r1SpawnWith(r1WallClock{}, func(cmd *exec.Cmd) error {
		cmd.Args = []string{"probe", "-test.run=^TestR1PrivateOwnedChild$", "--", "r1-owned-stalled"}
		if err := cmd.Start(); err != nil {
			return err
		}
		watchdog = time.AfterFunc(3*time.Second, func() { _ = cmd.Process.Kill() })
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { watchdog.Stop(); _, _ = child.End("abort", time.Now().Add(R1ReapWait)) })
	raw := child.(*r1ExecChild)
	for _, fd := range []int{raw.helloFD, raw.tokenFD} {
		flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_GETFL, 0)
		if errno != 0 || flags&syscall.O_NONBLOCK == 0 {
			t.Fatal("private endpoint became blocking", errno)
		}
	}
	start := time.Now()
	_, err = child.Ready(start.Add(50 * time.Millisecond))
	if !errors.Is(err, errR1Deadline) || time.Since(start) >= time.Second {
		t.Fatal("stalled pipe escaped read deadline", err, time.Since(start))
	}
	flags, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(raw.tokenFD), syscall.F_GETFL, 0)
	if errno != 0 || flags&syscall.O_NONBLOCK == 0 {
		t.Fatal("cleanup pipe became blocking", errno)
	}
	r, err := child.End("abort", time.Now().Add(R1ReapWait))
	if err != nil || r.Reap != "reaped" || r.ExitCode == nil || *r.ExitCode != 20 {
		t.Fatal(r, err)
	}
}

func TestR1NonzeroPageStrideWithoutMapping(t *testing.T) {
	// Small test backing only; production obtains its fixed bytes from mmap.
	m := r1Mapping{data: make([]byte, 8192), page: 4096}
	if err := m.TouchPage(0); err != nil {
		t.Fatal(err)
	}
	if err := m.TouchPage(4096); err != nil {
		t.Fatal(err)
	}
	if m.data[0] == 0 || m.data[4096] == 0 || bytes.Count(m.data, []byte{0xa5}) != 2 {
		t.Fatal("wrong stride/value")
	}
	for _, off := range []int{-1, 1, 8192} {
		if m.TouchPage(off) == nil {
			t.Fatal("bad offset", off)
		}
	}
}

func TestR1ErrorsAreNotMissingRecords(t *testing.T) {
	for _, name := range []string{R1ReleaseName, R1FinishName} {
		f := newR1Fixture(t)
		f.store.read = func(n string) error {
			if n == name && len(f.frames) > 0 {
				return os.ErrNotExist
			}
			return nil
		}
		if runR1(f.d) == nil {
			t.Fatal("lost subject accepted")
		}
		if f.clock.Now() != time.Unix(1, 0) {
			t.Fatal("lost subject waited")
		}
		if strings.Contains(f.out.String(), `"stage":"completion"`) {
			t.Fatal("false completion")
		}
	}
}
