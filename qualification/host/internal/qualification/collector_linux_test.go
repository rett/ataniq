//go:build linux && amd64

package qualification

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func ownedRoot(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, dir
}
func put(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCollectFlat(t *testing.T) {
	root, dir := ownedRoot(t)
	put(t, filepath.Join(dir, "good"), []byte("synthetic"))
	for _, size := range []int{0, 1, MaxCollectBytes} {
		data := bytes.Repeat([]byte("x"), size)
		put(t, filepath.Join(dir, "bounded"), data)
		limit := int64(size)
		if limit == 0 {
			limit = 1
		}
		got, err := CollectFlat(root, "bounded", limit, true)
		if err != nil || !bytes.Equal(got, data) {
			t.Fatal("positive/exact cap", len(got), err)
		}
	}
	put(t, filepath.Join(dir, "over"), bytes.Repeat([]byte("x"), MaxCollectBytes+1))
	if _, err := CollectFlat(root, "over", MaxCollectBytes, true); err == nil {
		t.Fatal("over cap accepted")
	}
	if _, err := CollectFlat(root, "good", 8, true); err == nil {
		t.Fatal("over caller cap accepted")
	}
	if _, err := CollectFlat(root, "good", 10, false); err == nil {
		t.Fatal("missing external stop proof accepted")
	}
	for _, name := range []string{"missing", "../good", "a/b", ".hidden", ".."} {
		if _, err := CollectFlat(root, name, 10, true); err == nil {
			t.Fatal("invalid/absent name", name)
		}
	}
	for _, limit := range []int64{0, -1, MaxCollectBytes + 1} {
		if _, err := CollectFlat(root, "good", limit, true); err == nil {
			t.Fatal("invalid cap")
		}
	}
	if _, err := CollectFlat(nil, "good", 10, true); err == nil {
		t.Fatal("nil root")
	}
	if err := os.Mkdir(filepath.Join(dir, "directory"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(dir, "good"), filepath.Join(dir, "hard")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"directory", "hard", "good"} {
		if _, err := CollectFlat(root, name, 10, true); err == nil {
			t.Fatal("wrong type/link count", name)
		}
	}
}

func TestCollectorSelectedGoFlags(t *testing.T) {
	root, dir := ownedRoot(t)
	put(t, filepath.Join(dir, "regular"), []byte("fake"))
	outside := t.TempDir()
	put(t, filepath.Join(outside, "fake"), []byte("fake"))
	for name, target := range map[string]string{"inside": "regular", "escape": filepath.Join(outside, "fake")} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
		fi, err := root.Lstat(name)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Fatal("Lstat followed final symlink", err)
		}
		f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if f != nil {
			f.Close()
		}
		if name == "escape" && err == nil {
			t.Fatal("Root.OpenFile escaped root")
		}
		if name == "inside" {
			t.Logf("selected Go Root.OpenFile in-root final symlink with O_NOFOLLOW: %v", err)
		}
		f, err = openFlat(root, name)
		if f != nil {
			f.Close()
		}
		if !errors.Is(err, syscall.ELOOP) {
			t.Fatalf("flat open must reject final symlink %s: %v", name, err)
		}
		if _, err := CollectFlat(root, name, 10, true); err == nil {
			t.Fatal("collector accepted symlink")
		}
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	// This directly establishes O_NONBLOCK behavior in the selected Go root API.
	done := make(chan error, 1)
	go func() {
		f, err := openFlat(root, "fifo")
		if f != nil {
			f.Close()
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		// Release a hypothetical blocking open before reporting the failed flag.
		fd, _ := syscall.Open(filepath.Join(dir, "fifo"), syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if fd >= 0 {
			syscall.Close(fd)
		}
		t.Fatal("FIFO open blocked")
	}
	if _, err := CollectFlat(root, "fifo", 10, true); err == nil {
		t.Fatal("collector opened FIFO")
	}
	t.Log("selected Go: flat open rejects final symlinks; FIFO open is nonblocking")
	t.Run("owned-socket", func(t *testing.T) {
		listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "socket"), Net: "unix"})
		if err != nil {
			t.Skipf("owned Unix socket fixture unavailable: %v", err)
		}
		defer listener.Close()
		if _, err := CollectFlat(root, "socket", 10, true); err == nil {
			t.Fatal("socket accepted")
		}
		f, err := openFlat(root, "socket")
		if f != nil {
			f.Close()
		}
		if err == nil {
			t.Fatal("socket opened as regular evidence")
		}
	})
}

func TestCollectorIdentityChecks(t *testing.T) {
	_, dir := ownedRoot(t)
	path := filepath.Join(dir, "fake")
	put(t, path, []byte("before"))
	a, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	put(t, path, []byte("after is larger"))
	b, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if same(a, b) {
		t.Fatal("size/identity change missed")
	}
	if err := checkRegular(b, 1); err == nil {
		t.Fatal("opened oversize not rejected")
	}
	// Synthetic device metadata exercises the pre-open rejection without mknod
	// privileges or touching any host device.
	if err := checkRegular(fakeInfo{mode: os.ModeDevice}, 10); err == nil {
		t.Fatal("device metadata accepted")
	}
}

type fakeInfo struct{ mode os.FileMode }

func (fakeInfo) Name() string        { return "fake" }
func (fakeInfo) Size() int64         { return 0 }
func (f fakeInfo) Mode() os.FileMode { return f.mode }
func (fakeInfo) ModTime() time.Time  { return time.Time{} }
func (fakeInfo) IsDir() bool         { return false }
func (fakeInfo) Sys() any            { return &syscall.Stat_t{Nlink: 1} }
