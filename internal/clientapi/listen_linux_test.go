package clientapi

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func mode(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode()
}

func TestListenCreatesFolderAndSocketWithModes(t *testing.T) {
	dir := socketDir(t)
	l, path, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if path != filepath.Join(dir, "daemon.sock") || path != SocketPath(dir) {
		t.Fatalf("path = %q", path)
	}
	if m := mode(t, dir); !m.IsDir() || m.Perm() != 0o700 {
		t.Fatalf("folder mode = %v", m)
	}
	if m := mode(t, path); m.Type() != os.ModeSocket || m.Perm() != 0o600 {
		t.Fatalf("socket mode = %v", m)
	}
}

func TestListenFolderModeIsExactUnderAnyUmask(t *testing.T) {
	dir := socketDir(t)
	old := syscall.Umask(0o277)
	defer syscall.Umask(old)
	l, path, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if m := mode(t, dir); m.Perm() != 0o700 {
		t.Fatalf("folder mode = %v", m)
	}
	if m := mode(t, path); m.Perm() != 0o600 {
		t.Fatalf("socket mode = %v", m)
	}
}

func TestListenSocketIsNeverWiderThanFolderAllows(t *testing.T) {
	// With a permissive umask the socket is created wide and narrowed after;
	// the folder keeps everyone else out meanwhile.
	dir := socketDir(t)
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	l, path, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if m := mode(t, path); m.Perm() != 0o600 {
		t.Fatalf("socket mode = %v", m)
	}
	if m := mode(t, dir); m.Perm() != 0o700 {
		t.Fatalf("folder mode = %v", m)
	}
}

func TestListenAcceptsExistingPrivateFolder(t *testing.T) {
	dir := socketDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	l, _, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	// Stricter than 0700 for others, such as 0500, is refused as unusable;
	// 0700 is fine and unchanged.
	if m := mode(t, dir); m.Perm() != 0o700 {
		t.Fatalf("mode = %v", m)
	}
}

func TestListenRefusesFolderOthersCanReach(t *testing.T) {
	for _, perm := range []os.FileMode{0o770, 0o750, 0o710, 0o707, 0o705, 0o701, 0o755, 0o777, 0o500, 0o000} {
		dir := socketDir(t)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, perm); err != nil {
			t.Fatal(err)
		}
		l, _, err := Listen(dir)
		if err == nil {
			l.Close()
			t.Fatalf("mode %04o accepted", perm)
		}
		if _, statErr := os.Lstat(filepath.Join(dir, SocketName)); statErr == nil {
			t.Fatalf("mode %04o: a socket was bound", perm)
		}
		os.Chmod(dir, 0o700)
	}
}

func TestListenDoesNotNarrowARefusedFolder(t *testing.T) {
	dir := socketDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Listen(dir); err == nil {
		t.Fatal("accepted")
	}
	if m := mode(t, dir); m.Perm() != 0o750 {
		t.Fatalf("Listen changed a folder it refused: %v", m)
	}
}

func TestListenRefusesOtherKindsAtTheFolder(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "f")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	for name, dir := range map[string]string{"file": file, "symlink": link, "relative": "run", "unclean": base + "/a/../real", "missing parent": filepath.Join(base, "no", "run")} {
		if l, _, err := Listen(dir); err == nil {
			l.Close()
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestListenRefusesPathTooLongForASocketAddress(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, strings.Repeat("d", 120))
	_, _, err := Listen(dir)
	if err == nil || !strings.Contains(err.Error(), "Unix socket address") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Lstat(dir); statErr == nil {
		t.Fatal("the folder was created for a path that cannot be bound")
	}
	// The edge: 107 bytes is the longest that fits. It is built under a short
	// folder of its own, as t.TempDir's name grows with the test's name and
	// TMPDIR and may leave no room.
	short, err := os.MkdirTemp("", "ca")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(short) })
	sock := "/" + SocketName
	pad := maxSocketPath - len(short) - 1 - len(sock)
	if pad < 1 {
		t.Fatalf("TMPDIR leaves no room for the edge test: %q is %d bytes", short, len(short))
	}
	fit := short + "/" + strings.Repeat("e", pad)
	if len(fit+sock) != maxSocketPath {
		t.Fatalf("test arithmetic: %d", len(fit+sock))
	}
	l, _, err := Listen(fit)
	if err != nil {
		t.Fatalf("the longest path that fits did not bind: %v", err)
	}
	l.Close()
	_, _, err = Listen(fit + "e")
	if err == nil {
		t.Fatal("108-byte path accepted")
	}
}

func TestListenDoesNotRemoveAnExistingSocket(t *testing.T) {
	dir := socketDir(t)
	l1, path, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer l1.Close()
	l2, _, err := Listen(dir)
	if err == nil {
		l2.Close()
		t.Fatal("second Listen succeeded")
	}
	if !errors.Is(err, syscall.EADDRINUSE) {
		t.Fatalf("err = %v, want EADDRINUSE", err)
	}
	if mode(t, path).Type() != os.ModeSocket {
		t.Fatal("the first socket is gone")
	}
	// And the first one still serves.
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
}

// leaveStale binds a socket in dir and leaves its file behind, as a daemon
// that was killed does.
func leaveStale(t *testing.T, dir string) string {
	t.Helper()
	path := SocketPath(dir)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetUnlinkOnClose(false)
	l.Close()
	if mode(t, path).Type() != os.ModeSocket {
		t.Fatal("no stale socket")
	}
	return path
}

func TestRemoveStale(t *testing.T) {
	dir := socketDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveStale(dir); err != nil || removed {
		t.Fatalf("nothing there: %v %v", removed, err)
	}
	path := leaveStale(t, dir)
	l, _, err := Listen(dir)
	if err == nil {
		l.Close()
		t.Fatal("Listen removed a stale socket on its own")
	}
	if removed, err := RemoveStale(dir); err != nil || !removed {
		t.Fatalf("RemoveStale = %v %v", removed, err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("socket still there: %v", err)
	}
	l, _, err = Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}

func TestRemoveStaleRefusesWhatIsNotASocket(t *testing.T) {
	dir := socketDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := SocketPath(dir)
	if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveStale(dir); err == nil || removed {
		t.Fatalf("removed a regular file: %v %v", removed, err)
	}
	os.Remove(path)
	other := filepath.Join(dir, "other")
	os.WriteFile(other, nil, 0o600)
	if err := os.Symlink(other, path); err != nil {
		t.Fatal(err)
	}
	if removed, err := RemoveStale(dir); err == nil || removed {
		t.Fatalf("removed a symlink: %v %v", removed, err)
	}
	if _, err := os.Lstat(other); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveStaleChecksTheFolder(t *testing.T) {
	dir := socketDir(t)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	leaveStale(t, dir)
	os.Chmod(dir, 0o770)
	if removed, err := RemoveStale(dir); err == nil || removed {
		t.Fatalf("RemoveStale in a group-accessible folder: %v %v", removed, err)
	}
	if _, err := RemoveStale("relative"); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestPeerUIDDefaultSameUserOverRealSocket(t *testing.T) {
	l, path, err := Listen(socketDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := make(chan uint32, 1)
	errc := make(chan error, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			errc <- err
			return
		}
		defer c.Close()
		uid, err := PeerUID(c)
		if err != nil {
			errc <- err
			return
		}
		got <- uid
	}()
	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select {
	case uid := <-got:
		if uid != uint32(os.Geteuid()) {
			t.Fatalf("peer uid %d, want %d", uid, os.Geteuid())
		}
	case err := <-errc:
		t.Fatal(err)
	}
	// Not a Unix connection: refused, not guessed.
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if _, err := PeerUID(a); err == nil {
		t.Fatal("PeerUID accepted a non-Unix connection")
	}
}

func TestServeServesTheSameUserWithTheDefaultCheck(t *testing.T) {
	ts := startServer(t, &stubHandler{version: "v"}, Options{})
	c, err := Dial(context.Background(), ts.path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Hello(context.Background()); err != nil {
		t.Fatal(err)
	}
	if snap := ts.counters.Snapshot(); snap.RefusedUID != 0 || snap.PeerErrors != 0 {
		t.Fatalf("counters = %+v", snap)
	}
}
