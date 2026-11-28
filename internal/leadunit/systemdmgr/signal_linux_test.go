package systemdmgr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"testing"

	"ataniq/internal/cgroupsig"
	"ataniq/internal/leadunit"
	"ataniq/internal/systemd"
)

// fakePidfds is the pidfd part of the host: every pid opens, nothing is
// signalled for real. onOpen runs while a pidfd is being opened.
type fakePidfds struct {
	mu     sync.Mutex
	next   int
	pids   map[int]int // descriptor to pid
	opened []int
	sent   []int // pids signalled, in order
	sigs   []syscall.Signal
	onOpen func(pid int)
	// failSend maps a pid to the error its pidfd_send_signal returns.
	failSend map[int]error
}

func newFakePidfds() *fakePidfds { return &fakePidfds{next: 100, pids: map[int]int{}} }

func (f *fakePidfds) PidfdOpen(pid int) (int, error) {
	f.mu.Lock()
	hook := f.onOpen
	f.mu.Unlock()
	if hook != nil {
		hook(pid)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.next++
	f.pids[f.next] = pid
	f.opened = append(f.opened, pid)
	return f.next, nil
}

func (f *fakePidfds) PidfdSendSignal(fd int, sig syscall.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failSend[f.pids[fd]]; err != nil {
		return err
	}
	f.sent = append(f.sent, f.pids[fd])
	f.sigs = append(f.sigs, sig)
	return nil
}

func (f *fakePidfds) Close(fd int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pids, fd)
	return nil
}

var _ cgroupsig.Kernel = (*fakePidfds)(nil)

// signalRig is a rig whose unit has a control group directory in a temporary
// tree standing for the cgroup file system, and a fake for the pidfds. The
// fake kernel reports the real identity of that directory.
type signalRig struct {
	*rig
	root string
	dir  string
	pf   *fakePidfds
	st   leadunit.State
}

func newSignalRig(t *testing.T, procs string) *signalRig {
	t.Helper()
	root := t.TempDir()
	old := cgroupMount
	cgroupMount = root
	t.Cleanup(func() { cgroupMount = old })
	dir := filepath.Join(root, cgUnit)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeGroup(t, dir, procs)
	r := newRig(t)
	r.k.groups[cgUnit] = dirIdentity(t, dir)
	s := &signalRig{rig: r, root: root, dir: dir, pf: newFakePidfds()}
	r.m.pk = s.pf
	st, err := r.m.Inspect(context.Background(), unitName)
	if err != nil {
		t.Fatal(err)
	}
	s.st = st
	return s
}

func writeGroup(t *testing.T, dir, procs string) {
	t.Helper()
	for name, body := range map[string]string{"cgroup.kill": "", "cgroup.procs": procs} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func dirIdentity(t *testing.T, dir string) systemd.FileIdentity {
	t.Helper()
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	return systemd.IdentityOf(fi)
}

func readKillFile(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "cgroup.kill"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// noSystemctlKill fails when a "systemctl kill" was run.
func (s *signalRig) noSystemctlKill() {
	s.t.Helper()
	for _, c := range s.run.recorded() {
		if len(c) > 2 && c[2] == "kill" {
			s.t.Fatalf("systemctl kill was run: %q", c)
		}
	}
}

func TestCgroupSignal(t *testing.T) {
	ctx := context.Background()

	t.Run("KillWritesOneAndNoSystemctlKill", func(t *testing.T) {
		s := newSignalRig(t, "321\n")
		if err := s.m.Signal(ctx, s.st, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		if got := readKillFile(t, s.dir); got != "1" {
			t.Fatalf("cgroup.kill holds %q, want 1", got)
		}
		s.noSystemctlKill()
		if len(s.pf.opened) != 0 {
			t.Fatalf("pidfds opened for a kill: %v", s.pf.opened)
		}
	})

	t.Run("TermMainPidFirst", func(t *testing.T) {
		s := newSignalRig(t, "400\n321\n500\n")
		if err := s.m.Signal(ctx, s.st, syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if want := []int{321, 400, 500}; !reflect.DeepEqual(s.pf.sent, want) {
			t.Fatalf("signalled %v, want %v", s.pf.sent, want)
		}
		if got := readKillFile(t, s.dir); got != "" {
			t.Fatalf("a term wrote %q to cgroup.kill", got)
		}
		s.noSystemctlKill()
	})

	// A term that reached some processes and not others is a signal that is
	// out: the caller must not read it as a call that sent nothing.
	t.Run("TermPartialIsErrPartialSignal", func(t *testing.T) {
		s := newSignalRig(t, "400\n321\n500\n")
		s.pf.failSend = map[int]error{400: syscall.EPERM}
		err := s.m.Signal(ctx, s.st, syscall.SIGTERM)
		if !errors.Is(err, leadunit.ErrPartialSignal) || errors.Is(err, leadunit.ErrNotLoaded) {
			t.Fatalf("Signal: %v, want ErrPartialSignal", err)
		}
		if want := []int{321, 500}; !reflect.DeepEqual(s.pf.sent, want) {
			t.Fatalf("signalled %v, want %v", s.pf.sent, want)
		}
		// Nothing signalled is a plain failure, which a caller may retry.
		s = newSignalRig(t, "400\n")
		s.pf.failSend = map[int]error{400: syscall.EPERM}
		err = s.m.Signal(ctx, s.st, syscall.SIGTERM)
		if err == nil || errors.Is(err, leadunit.ErrPartialSignal) {
			t.Fatalf("a term that sent nothing: %v", err)
		}
	})

	t.Run("EmptyRecordedIDRefused", func(t *testing.T) {
		s := newSignalRig(t, "321\n")
		for name, id := range map[string]string{"empty": "", "unreadable": "7:100"} {
			st := s.st
			st.ControlGroupID = id
			for _, sig := range []syscall.Signal{syscall.SIGKILL, syscall.SIGTERM} {
				if err := s.m.Signal(ctx, st, sig); !errors.Is(err, leadunit.ErrNoControlGroupID) {
					t.Errorf("%s id, signal %d: %v", name, sig, err)
				}
			}
		}
		st := s.st
		st.ControlGroup = ""
		if err := s.m.Signal(ctx, st, syscall.SIGKILL); !errors.Is(err, leadunit.ErrNoControlGroupID) {
			t.Errorf("no path: %v", err)
		}
		if got := readKillFile(t, s.dir); got != "" || len(s.pf.opened) != 0 {
			t.Fatalf("a refused signal wrote %q and opened %v", got, s.pf.opened)
		}
		s.noSystemctlKill()
	})

	t.Run("IdentityReplacedZeroWrites", func(t *testing.T) {
		s := newSignalRig(t, "321\n")
		// The group is replaced by another directory of the same path after
		// the inspection: the old one is kept (so that its inode number is
		// not reused) and a new one with a cgroup.kill takes its place.
		if err := os.Rename(s.dir, s.dir+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(s.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeGroup(t, s.dir, "321\n")
		for _, sig := range []syscall.Signal{syscall.SIGKILL, syscall.SIGTERM} {
			if err := s.m.Signal(ctx, s.st, sig); !errors.Is(err, leadunit.ErrControlGroupMismatch) {
				t.Errorf("signal %d: %v", sig, err)
			}
		}
		if got := readKillFile(t, s.dir); got != "" {
			t.Fatalf("the replacing group got %q", got)
		}
		if got := readKillFile(t, s.dir+".old"); got != "" {
			t.Fatalf("the old group got %q", got)
		}
		if len(s.pf.opened) != 0 || len(s.pf.sent) != 0 {
			t.Fatalf("pidfds opened %v, signalled %v", s.pf.opened, s.pf.sent)
		}
		s.noSystemctlKill()
	})

	t.Run("GroupRemovedNotLoaded", func(t *testing.T) {
		s := newSignalRig(t, "321\n")
		if err := os.RemoveAll(s.dir); err != nil {
			t.Fatal(err)
		}
		for _, sig := range []syscall.Signal{syscall.SIGKILL, syscall.SIGTERM} {
			if err := s.m.Signal(ctx, s.st, sig); !errors.Is(err, leadunit.ErrNotLoaded) {
				t.Errorf("signal %d: %v", sig, err)
			}
		}
		if len(s.pf.opened) != 0 {
			t.Fatalf("pidfds opened %v", s.pf.opened)
		}
		s.noSystemctlKill() // no fall back to a kill by name
	})

	t.Run("PidReusedOutsideGroupSkipped", func(t *testing.T) {
		s := newSignalRig(t, "321\n400\n500\n")
		// While the pidfd of 500 opens, 500 leaves the group (its number is
		// held by another process now): it is no longer listed when the list
		// is read again.
		s.pf.onOpen = func(pid int) {
			if pid == 500 {
				if err := os.WriteFile(filepath.Join(s.dir, "cgroup.procs"), []byte("321\n400\n"), 0o644); err != nil {
					t.Error(err)
				}
			}
		}
		if err := s.m.Signal(ctx, s.st, syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if want := []int{321, 400}; !reflect.DeepEqual(s.pf.sent, want) {
			t.Fatalf("signalled %v, want %v", s.pf.sent, want)
		}
		s.noSystemctlKill()
	})

	t.Run("MismatchedPathRefused", func(t *testing.T) {
		s := newSignalRig(t, "321\n")
		s.setUnit(with(loadedProps(), "ControlGroup", cgBase+"/app.slice/other.service"))
		s.k.groups[cgBase+"/app.slice/other.service"] = idB
		if err := s.m.Signal(ctx, s.st, syscall.SIGKILL); !errors.Is(err, leadunit.ErrControlGroupMismatch) {
			t.Fatal(err)
		}
		if got := readKillFile(t, s.dir); got != "" {
			t.Fatalf("wrote %q", got)
		}
	})
}
