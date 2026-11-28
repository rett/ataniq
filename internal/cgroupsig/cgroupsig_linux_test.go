package cgroupsig

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const groupPath = "/user.slice/app.slice/unit.service"

// fakeKernel is the pidfd part of the host. A pid in failOpen fails to open
// with the error given; onOpen runs while a pidfd is being opened.
type fakeKernel struct {
	mu       sync.Mutex
	next     int
	pids     map[int]int
	opened   []int
	sent     []int
	closed   int
	failOpen map[int]error
	failSend map[int]error
	onOpen   func(pid int)
}

func newFakeKernel() *fakeKernel {
	return &fakeKernel{next: 100, pids: map[int]int{}, failOpen: map[int]error{}, failSend: map[int]error{}}
}

func (k *fakeKernel) PidfdOpen(pid int) (int, error) {
	k.mu.Lock()
	hook := k.onOpen
	k.mu.Unlock()
	if hook != nil {
		hook(pid)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := k.failOpen[pid]; err != nil {
		return -1, err
	}
	k.next++
	k.pids[k.next] = pid
	k.opened = append(k.opened, pid)
	return k.next, nil
}

func (k *fakeKernel) PidfdSendSignal(fd int, sig syscall.Signal) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	pid := k.pids[fd]
	if err := k.failSend[pid]; err != nil {
		return err
	}
	k.sent = append(k.sent, pid)
	return nil
}

func (k *fakeKernel) Close(fd int) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.closed++
	delete(k.pids, fd)
	return nil
}

type tree struct {
	root string
	dir  string
	id   Identity
}

// newTree makes a temporary directory tree with the files of a control group
// at groupPath: a cgroup.kill that starts empty and a cgroup.procs.
func newTree(t *testing.T, procs string) tree {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, groupPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, dir, procs)
	return tree{root: root, dir: dir, id: identityOf(t, dir)}
}

func writeFiles(t *testing.T, dir, procs string) {
	t.Helper()
	for name, body := range map[string]string{"cgroup.kill": "", "cgroup.procs": procs} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func identityOf(t *testing.T, dir string) Identity {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Stat(dir, &st); err != nil {
		t.Fatal(err)
	}
	return Identity{uint64(st.Dev), uint64(st.Ino), st.Uid}
}

func killContent(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "cgroup.kill"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCgroupSignal(t *testing.T) {
	t.Run("KillWritesOne", func(t *testing.T) {
		tr := newTree(t, "")
		g, err := Open(newFakeKernel(), tr.root, groupPath, tr.id)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		if err := g.Kill(); err != nil {
			t.Fatal(err)
		}
		if got := killContent(t, tr.dir); got != "1" {
			t.Fatalf("cgroup.kill holds %q", got)
		}
	})

	t.Run("IdentityReplacedZeroWrites", func(t *testing.T) {
		tr := newTree(t, "321\n")
		// The old directory is kept, so that its inode number cannot be
		// reused for the new one.
		if err := os.Rename(tr.dir, tr.dir+".old"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(tr.dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFiles(t, tr.dir, "321\n")
		k := newFakeKernel()
		g, err := Open(k, tr.root, groupPath, tr.id)
		if !errors.Is(err, ErrIdentity) || g != nil {
			t.Fatalf("Open: %v", err)
		}
		if got := killContent(t, tr.dir); got != "" {
			t.Fatalf("the replacing group got %q", got)
		}
		if got := killContent(t, tr.dir+".old"); got != "" {
			t.Fatalf("the old group got %q", got)
		}
		if len(k.opened) != 0 {
			t.Fatalf("pidfds opened: %v", k.opened)
		}
	})

	t.Run("GroupRemovedNotLoaded", func(t *testing.T) {
		tr := newTree(t, "321\n")
		g, err := Open(newFakeKernel(), tr.root, groupPath, tr.id)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		if err := os.RemoveAll(tr.dir); err != nil {
			t.Fatal(err)
		}
		if err := g.Kill(); !errors.Is(err, ErrGone) {
			t.Fatalf("Kill: %v", err)
		}
		if _, err := g.Signal(syscall.SIGTERM, 321); !errors.Is(err, ErrGone) {
			t.Fatalf("Signal: %v", err)
		}
		// A group gone before Open is the same answer.
		if _, err := Open(nil, tr.root, groupPath, tr.id); !errors.Is(err, ErrGone) {
			t.Fatalf("Open: %v", err)
		}
	})

	t.Run("EmptyRecordedIDRefused", func(t *testing.T) {
		tr := newTree(t, "321\n")
		if _, err := Open(newFakeKernel(), tr.root, groupPath, Identity{}); !errors.Is(err, ErrIdentity) {
			t.Fatalf("zero identity: %v", err)
		}
		for _, p := range []string{"", "/", "rel/path", groupPath + "/", "/a/../b"} {
			if _, err := Open(newFakeKernel(), tr.root, p, tr.id); err == nil {
				t.Errorf("path %q was opened", p)
			}
		}
		if got := killContent(t, tr.dir); got != "" {
			t.Fatalf("wrote %q", got)
		}
	})

	t.Run("PidReusedOutsideGroupSkipped", func(t *testing.T) {
		tr := newTree(t, "400\n321\n500\n")
		k := newFakeKernel()
		// While the pidfd of 500 opens, 500 is no longer in the group: its
		// number is held by a process outside it.
		k.onOpen = func(pid int) {
			if pid == 500 {
				if err := os.WriteFile(filepath.Join(tr.dir, "cgroup.procs"), []byte("400\n321\n"), 0o644); err != nil {
					t.Error(err)
				}
			}
		}
		g, err := Open(k, tr.root, groupPath, tr.id)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		o, err := g.Signal(syscall.SIGTERM, 321)
		if err != nil {
			t.Fatal(err)
		}
		if want := []int{321, 400}; !reflect.DeepEqual(k.sent, want) {
			t.Fatalf("signalled %v, want %v (the main process first, 500 skipped)", k.sent, want)
		}
		if o.Signalled != 2 || o.PidfdFailures != 0 || o.Unconfirmed != 0 {
			t.Fatalf("outcome %+v", o)
		}
		if k.closed != 3 {
			t.Fatalf("%d pidfds closed, want 3", k.closed)
		}
		if got := killContent(t, tr.dir); got != "" {
			t.Fatalf("a term wrote %q to cgroup.kill", got)
		}
	})

	t.Run("MainPidNotListedSkipped", func(t *testing.T) {
		tr := newTree(t, "400\n")
		k := newFakeKernel()
		g, err := Open(k, tr.root, groupPath, tr.id)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		if _, err := g.Signal(syscall.SIGTERM, 321); err != nil {
			t.Fatal(err)
		}
		if want := []int{400}; !reflect.DeepEqual(k.sent, want) {
			t.Fatalf("signalled %v, want %v", k.sent, want)
		}
	})

	t.Run("NoKillFileIsNotGone", func(t *testing.T) {
		tr := newTree(t, "")
		if err := os.Remove(filepath.Join(tr.dir, "cgroup.kill")); err != nil {
			t.Fatal(err)
		}
		g, err := Open(newFakeKernel(), tr.root, groupPath, tr.id)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		if err := g.Kill(); !errors.Is(err, ErrNoKill) || errors.Is(err, ErrGone) {
			t.Fatalf("Kill: %v", err)
		}
	})

	t.Run("PidfdFailuresAreCounted", func(t *testing.T) {
		tr := newTree(t, "321\n400\n500\n")
		k := newFakeKernel()
		k.failOpen[321] = unix.ESRCH // gone: not a failure
		k.failOpen[400] = unix.EPERM // a failure
		k.failSend[500] = unix.EPERM // a failure
		g, err := Open(k, tr.root, groupPath, tr.id)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		o, err := g.Signal(syscall.SIGTERM, 321)
		if err == nil || o.PidfdFailures != 2 || o.Signalled != 0 {
			t.Fatalf("%+v %v", o, err)
		}
		// Nothing was sent: not a partial signal, so a caller may try again.
		if errors.Is(err, ErrPartial) {
			t.Fatalf("a signal that sent nothing is partial: %v", err)
		}
	})

	t.Run("PartialSignalIsDistinct", func(t *testing.T) {
		tr := newTree(t, "321\n400\n500\n")
		k := newFakeKernel()
		k.failOpen[400] = unix.EPERM // a process the caller cannot signal
		g, err := Open(k, tr.root, groupPath, tr.id)
		if err != nil {
			t.Fatal(err)
		}
		defer g.Close()
		o, err := g.Signal(syscall.SIGTERM, 321)
		if !errors.Is(err, ErrPartial) || errors.Is(err, ErrGone) || o.Signalled != 2 || o.PidfdFailures != 1 {
			t.Fatalf("%+v %v, want the others signalled and an error that wraps ErrPartial", o, err)
		}
		if want := []int{321, 500}; !reflect.DeepEqual(k.sent, want) {
			t.Fatalf("signalled %v, want %v", k.sent, want)
		}
		// The same with a failure of the send.
		k = newFakeKernel()
		k.failSend[500] = unix.EPERM
		g2, err := Open(k, tr.root, groupPath, tr.id)
		if err != nil {
			t.Fatal(err)
		}
		defer g2.Close()
		if o, err := g2.Signal(syscall.SIGTERM, 321); !errors.Is(err, ErrPartial) || o.Signalled != 2 {
			t.Fatalf("a failed send: %+v %v", o, err)
		}
	})
}

// SignalListed is the loop ptyhost shares: a member whose list cannot be read
// again is counted and not signalled.
func TestSignalListedUnconfirmed(t *testing.T) {
	k := newFakeKernel()
	relist := func(procs string) map[int]bool {
		if procs == "bad" {
			return nil
		}
		return map[int]bool{1: true}
	}
	o := SignalListed(k, []Member{{1, "ok"}, {2, "ok"}, {3, "bad"}}, syscall.SIGKILL, relist)
	if o.Signalled != 1 || o.Unconfirmed != 1 || o.PidfdFailures != 0 {
		t.Fatalf("%+v", o)
	}
	if !reflect.DeepEqual(k.sent, []int{1}) {
		t.Fatalf("signalled %v", k.sent)
	}
}

func TestReadProcsBounds(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "p")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if pids, trunc, err := ReadProcs(write("1\n22\n")); err != nil || trunc || !reflect.DeepEqual(pids, []int{1, 22}) {
		t.Fatal(pids, trunc, err)
	}
	if _, trunc, err := ReadProcs(write(strings.Repeat("1\n", MaxProcsBytes/2+1))); err != nil || !trunc {
		t.Fatal(trunc, err)
	}
	if _, _, err := ReadProcs(write("1\nx\n")); err == nil {
		t.Fatal("a bad line was accepted")
	}
}
