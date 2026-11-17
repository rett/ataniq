//go:build linux

package safefs

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openRoot(t *testing.T, dir string) *Root {
	t.Helper()
	r, err := OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// withDeadline fails the test when fn does not return in time, which is how a
// blocking open of a FIFO shows itself.
func withDeadline(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("call blocked")
	}
}

// refusedSymlink checks the error of a symlink the kernel refused.
func refusedSymlink(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("symlink was followed")
	}
	if !errors.Is(err, unix.ELOOP) || !errors.Is(err, ErrEscape) {
		t.Fatalf("want ELOOP and ErrEscape, got %v", err)
	}
}

func TestOpenReadsRegularFile(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "sub", "a.txt"), "hello")
	r := openRoot(t, dir)
	f, err := r.Open("sub/a.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	buf := make([]byte, 16)
	n, _ := f.Read(buf)
	if string(buf[:n]) != "hello" {
		t.Fatalf("got %q", buf[:n])
	}
	// Lexical forms of the same name work too.
	for _, name := range []string{"./sub/a.txt", "sub//a.txt", "sub/./a.txt"} {
		got, err := r.ReadFile(name, 100)
		if err != nil || string(got) != "hello" {
			t.Fatalf("%q: %q, %v", name, got, err)
		}
	}
}

func TestOpenReturnsBlockingFile(t *testing.T) {
	// Open uses O_NONBLOCK so that a FIFO does not block it. The file it
	// returns must not keep the flag, which a child would inherit.
	dir := t.TempDir()
	write(t, filepath.Join(dir, "f"), "x")
	r := openRoot(t, dir)
	f, err := r.Open("f")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rc, err := f.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags int
	var ferr error
	// Control, not Fd: Fd itself would switch the descriptor to blocking.
	if err := rc.Control(func(fd uintptr) { flags, ferr = unix.FcntlInt(fd, unix.F_GETFL, 0) }); err != nil {
		t.Fatal(err)
	}
	if ferr != nil {
		t.Fatal(ferr)
	}
	if flags&unix.O_NONBLOCK != 0 {
		t.Fatalf("O_NONBLOCK is set on the returned file (flags %#x)", flags)
	}
}

func TestOpenMissingIsNotExist(t *testing.T) {
	r := openRoot(t, t.TempDir())
	_, err := r.Open("nope")
	if !errors.Is(err, fs.ErrNotExist) || !errors.Is(err, unix.ENOENT) {
		t.Fatalf("got %v", err)
	}
	_, err = r.Lstat("a/b")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
}

func TestReadFileLimit(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "f"), "0123456789")
	write(t, filepath.Join(dir, "empty"), "")
	r := openRoot(t, dir)

	got, err := r.ReadFile("f", 10) // exactly at the cap
	if err != nil || string(got) != "0123456789" {
		t.Fatalf("at cap: %q, %v", got, err)
	}
	got, err = r.ReadFile("f", 100)
	if err != nil || len(got) != 10 {
		t.Fatalf("under cap: %q, %v", got, err)
	}
	if _, err = r.ReadFile("f", 9); !errors.Is(err, ErrLimit) {
		t.Fatalf("over cap: %v", err)
	}
	if _, err = r.ReadFile("f", 0); !errors.Is(err, ErrLimit) {
		t.Fatalf("cap 0, nonempty file: %v", err)
	}
	if got, err = r.ReadFile("empty", 0); err != nil || len(got) != 0 {
		t.Fatalf("cap 0, empty file: %q, %v", got, err)
	}
	if _, err = r.ReadFile("f", -1); err == nil {
		t.Fatal("negative cap accepted")
	}
	if _, err = r.ReadFile("f", 1<<63-1); err != nil {
		t.Fatalf("largest cap: %v", err)
	}
}

func TestReadFileLimitIsDecidedByReading(t *testing.T) {
	// A file in /proc reports size 0 but has content, so a check on the
	// reported size passes it. Only reading finds that it is too large.
	// /proc/self is a symlink, which OpenRoot refuses: use the pid.
	r, err := OpenRoot("/proc/" + strconv.Itoa(os.Getpid()))
	if err != nil {
		t.Skip("no /proc:", err)
	}
	defer r.Close()
	fi, err := r.Lstat("status")
	if err != nil {
		t.Skip("no status file:", err)
	}
	if fi.Size() != 0 {
		t.Skipf("status reports size %d, not 0", fi.Size())
	}
	got, err := r.ReadFile("status", 16)
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("got %d bytes, %v", len(got), err)
	}
	if got, err = r.ReadFile("status", 1<<20); err != nil || len(got) <= 16 {
		t.Fatalf("large cap: %d bytes, %v", len(got), err)
	}
}

func TestSymlinksRefused(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret"), "secret")
	write(t, filepath.Join(outside, "dir", "secret"), "secret")
	dir := t.TempDir()
	write(t, filepath.Join(dir, "real", "in.txt"), "inside")
	mustSymlink(t, "real/in.txt", filepath.Join(dir, "last-inside"))
	mustSymlink(t, filepath.Join(outside, "secret"), filepath.Join(dir, "last-outside"))
	mustSymlink(t, "../../../../../../../../etc/passwd", filepath.Join(dir, "last-relative"))
	mustSymlink(t, "real", filepath.Join(dir, "mid-inside"))
	mustSymlink(t, filepath.Join(outside, "dir"), filepath.Join(dir, "mid-outside"))
	mustSymlink(t, "nowhere", filepath.Join(dir, "dangling"))
	r := openRoot(t, dir)

	for _, name := range []string{
		"last-inside", "last-outside", "last-relative", "dangling",
		"mid-inside/in.txt", "mid-outside/secret",
	} {
		_, err := r.Open(name)
		refusedSymlink(t, err)
		_, err = r.ReadFile(name, 100)
		refusedSymlink(t, err)
	}
	// Lstat follows no symlink in the parent, but describes a last one.
	for _, name := range []string{"mid-inside/in.txt", "mid-outside/secret"} {
		_, err := r.Lstat(name)
		refusedSymlink(t, err)
	}
	_, err := r.TreeSize("mid-inside/in.txt", SizeLimits{1 << 30, 1000})
	refusedSymlink(t, err)
	for _, name := range []string{"mid-inside", "mid-outside"} {
		// A symlink as the start counts as one entry of size 0, unopened.
		sz, err := r.TreeSize(name, SizeLimits{1 << 30, 1000})
		if err != nil || sz != (Size{0, 1}) {
			t.Fatalf("%s: %+v, %v", name, sz, err)
		}
	}
	if ok, err := r.IsDir("mid-outside"); ok || err != nil {
		t.Fatalf("symlink to a folder: %v, %v", ok, err)
	}
}

func TestEscapingNamesRefusedBeforeAnySyscall(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a", "f"), "x")
	r := openRoot(t, dir)
	// Close the root first: a refusal that still shows ErrEscape, not
	// os.ErrClosed, was decided before any use of the descriptor.
	r.Close()
	for _, name := range []string{"", "/", "/etc/passwd", "..", "../x", "a/../f", "a/..", "a/../../x", "./.."} {
		if _, err := r.Open(name); !errors.Is(err, ErrEscape) {
			t.Errorf("Open(%q): %v", name, err)
		}
		if _, err := r.ReadFile(name, 1); !errors.Is(err, ErrEscape) {
			t.Errorf("ReadFile(%q): %v", name, err)
		}
		if _, err := r.Lstat(name); !errors.Is(err, ErrEscape) {
			t.Errorf("Lstat(%q): %v", name, err)
		}
		if _, err := r.IsDir(name); !errors.Is(err, ErrEscape) {
			t.Errorf("IsDir(%q): %v", name, err)
		}
		if _, err := r.TreeSize(name, SizeLimits{1, 1}); !errors.Is(err, ErrEscape) {
			t.Errorf("TreeSize(%q): %v", name, err)
		}
	}
	// A name with a dot-dot inside a file name is fine.
	r2 := openRoot(t, dir)
	write(t, filepath.Join(dir, "a..b"), "x")
	if _, err := r2.ReadFile("a..b", 10); err != nil {
		t.Fatal(err)
	}
}

func TestFIFORefusedWithoutBlocking(t *testing.T) {
	dir := t.TempDir()
	if err := unix.Mkfifo(filepath.Join(dir, "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "pipe2"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := openRoot(t, dir)
	withDeadline(t, func() {
		if _, err := r.Open("pipe"); !errors.Is(err, ErrNotRegular) {
			t.Errorf("Open: %v", err)
		}
		if _, err := r.ReadFile("pipe2", 10); !errors.Is(err, ErrNotRegular) {
			t.Errorf("ReadFile: %v", err)
		}
	})
}

func TestSocketRefused(t *testing.T) {
	// A lead can bind a socket in its folder without privilege. open(2) of a
	// socket fails with ENXIO, which must still read as "not a regular file".
	dir := t.TempDir()
	l, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Skip("no unix sockets:", err)
	}
	defer l.Close()
	r := openRoot(t, dir)
	withDeadline(t, func() {
		_, err := r.Open("s")
		if !errors.Is(err, ErrNotRegular) || !errors.Is(err, unix.ENXIO) {
			t.Errorf("Open: want ErrNotRegular and ENXIO, got %v", err)
		}
		if _, err := r.ReadFile("s", 10); !errors.Is(err, ErrNotRegular) {
			t.Errorf("ReadFile: %v", err)
		}
	})
}

func TestDeviceRefused(t *testing.T) {
	// A symlink-free path to a device needs root to create, so this uses the
	// real /dev as the root: the null device is world readable.
	r, err := OpenRoot("/dev")
	if err != nil {
		t.Skip("no /dev:", err)
	}
	defer r.Close()
	withDeadline(t, func() {
		if _, err := r.Open("null"); !errors.Is(err, ErrNotRegular) {
			t.Errorf("Open(null): %v", err)
		}
	})
}

func TestOpenRefusesFolder(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "d", "f"), "x")
	r := openRoot(t, dir)
	for _, name := range []string{"d", "."} {
		if _, err := r.Open(name); !errors.Is(err, ErrNotRegular) {
			t.Errorf("Open(%q): %v", name, err)
		}
	}
}

func TestOpenRoot(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "real", "f"), "x")
	write(t, filepath.Join(base, "file"), "x")
	mustSymlink(t, "real", filepath.Join(base, "link"))

	for _, p := range []string{filepath.Join(base, "link"), filepath.Join(base, "link") + "/", filepath.Join(base, "link") + "//"} {
		_, err := OpenRoot(p)
		refusedSymlink(t, err)
	}
	if _, err := OpenRoot(filepath.Join(base, "file")); !errors.Is(err, unix.ENOTDIR) {
		t.Errorf("file: %v", err)
	}
	if _, err := OpenRoot(filepath.Join(base, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
	if _, err := OpenRoot(""); err == nil {
		t.Error("empty path accepted")
	}
	for _, p := range []string{filepath.Join(base, "real"), filepath.Join(base, "real") + "/"} {
		r, err := OpenRoot(p)
		if err != nil {
			t.Fatalf("%q: %v", p, err)
		}
		if got, err := r.ReadFile("f", 5); err != nil || string(got) != "x" {
			t.Errorf("%q: %q, %v", p, got, err)
		}
		r.Close()
	}
}

func TestOpenRootRefusesUncleanPath(t *testing.T) {
	// With a last element "." or "..", the kernel follows a symlink before it
	// (O_NOFOLLOW sees only the last element). Such a path, and any path that
	// is not clean, is refused before the open.
	base := t.TempDir()
	write(t, filepath.Join(base, "real", "f"), "x")
	mustSymlink(t, "real", filepath.Join(base, "link"))
	link := filepath.Join(base, "link")
	real := filepath.Join(base, "real")
	for _, p := range []string{
		link + "/.", link + "/./", link + "/..", link + "/../real",
		real + "/.", real + "/..", real + "/../real",
		base + "/./real", base + "//real", base + "/real/./",
		".", "..", "./real",
	} {
		r, err := OpenRoot(p)
		if r != nil {
			r.Close()
		}
		if !errors.Is(err, ErrEscape) {
			t.Errorf("%q: want ErrEscape, got %v", p, err)
		}
	}
	for _, p := range []string{"/", "//", real + "/", real + "//"} {
		r, err := OpenRoot(p)
		if err != nil {
			t.Errorf("%q: %v", p, err)
			continue
		}
		r.Close()
	}
}

func TestOpenRootSymlinkInParentIsOwners(t *testing.T) {
	// The folders above the root belong to the owner, so a symlink there is
	// resolved the ordinary way.
	base := t.TempDir()
	write(t, filepath.Join(base, "real", "sub", "f"), "x")
	mustSymlink(t, "real", filepath.Join(base, "via"))
	r, err := OpenRoot(filepath.Join(base, "via", "sub"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.ReadFile("f", 5); err != nil {
		t.Fatal(err)
	}
}

func TestSwapAfterOpenRoot(t *testing.T) {
	base := t.TempDir()
	orig := filepath.Join(base, "shared")
	write(t, filepath.Join(orig, "f"), "original")
	write(t, filepath.Join(orig, "d", "g"), "g")
	outside := t.TempDir()
	write(t, filepath.Join(outside, "f"), "stolen")
	write(t, filepath.Join(outside, "d", "g"), "stolen")

	r := openRoot(t, orig)
	moved := filepath.Join(base, "moved")
	if err := os.Rename(orig, moved); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, outside, orig)

	got, err := r.ReadFile("f", 100)
	if err != nil || string(got) != "original" {
		t.Fatalf("read %q, %v", got, err)
	}
	if got, err = r.ReadFile("d/g", 100); err != nil || string(got) != "g" {
		t.Fatalf("read %q, %v", got, err)
	}
	if ok, err := r.IsDir("d"); !ok || err != nil {
		t.Fatalf("IsDir: %v, %v", ok, err)
	}
	sz, err := r.TreeSize(".", SizeLimits{1 << 30, 100})
	if err != nil || sz.Entries != 4 {
		t.Fatalf("TreeSize: %+v, %v", sz, err)
	}
	// A new root at the old path is refused.
	_, err = OpenRoot(orig)
	refusedSymlink(t, err)
}

func TestLstat(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "d", "f"), "12345")
	mustSymlink(t, "d/f", filepath.Join(dir, "link"))
	mustSymlink(t, "d", filepath.Join(dir, "d", "dlink"))
	if err := unix.Mkfifo(filepath.Join(dir, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := openRoot(t, dir)

	fi, err := r.Lstat("link")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&fs.ModeSymlink == 0 || fi.IsDir() || fi.Name() != "link" {
		t.Fatalf("link: %v %q", fi.Mode(), fi.Name())
	}
	if fi, err = r.Lstat("d/dlink"); err != nil || fi.Mode()&fs.ModeSymlink == 0 || fi.Name() != "dlink" {
		t.Fatalf("dlink: %v, %v", fi, err)
	}
	if fi, err = r.Lstat("d/dlink/"); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("dlink/: %v, %v", fi, err)
	}
	if fi, err = r.Lstat("d/f"); err != nil || !fi.Mode().IsRegular() || fi.Size() != 5 || fi.Mode().Perm() != 0o644 {
		t.Fatalf("file: %v, %v", fi, err)
	}
	if fi, err = r.Lstat("d"); err != nil || !fi.IsDir() {
		t.Fatalf("dir: %v, %v", fi, err)
	}
	if fi, err = r.Lstat("."); err != nil || !fi.IsDir() || fi.Name() != "." {
		t.Fatalf("root: %v, %v", fi, err)
	}
	if fi, err = r.Lstat("pipe"); err != nil || fi.Mode()&fs.ModeNamedPipe == 0 {
		t.Fatalf("pipe: %v, %v", fi, err)
	}
	if _, ok := fi.Sys().(*unix.Stat_t); !ok {
		t.Fatalf("Sys: %T", fi.Sys())
	}
	if _, err = r.Lstat("d/dlink/f"); !errors.Is(err, ErrEscape) {
		t.Fatalf("through symlink: %v", err)
	}
}

func TestIsDir(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "d", "f"), "x")
	mustSymlink(t, "d", filepath.Join(dir, "link"))
	r := openRoot(t, dir)
	for name, want := range map[string]bool{"d": true, "d/f": false, "link": false, ".": true} {
		got, err := r.IsDir(name)
		if err != nil || got != want {
			t.Errorf("IsDir(%q) = %v, %v", name, got, err)
		}
	}
	if _, err := r.IsDir("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("missing: %v", err)
	}
}

func TestClosedRoot(t *testing.T) {
	r := openRoot(t, t.TempDir())
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Open("x"); !errors.Is(err, os.ErrClosed) {
		t.Errorf("Open: %v", err)
	}
	if _, err := r.Lstat("x"); !errors.Is(err, os.ErrClosed) {
		t.Errorf("Lstat: %v", err)
	}
	if _, err := r.TreeSize(".", SizeLimits{1, 1}); !errors.Is(err, os.ErrClosed) {
		t.Errorf("TreeSize: %v", err)
	}
}

// blocks returns st_blocks * 512 of a path, read independently of the package.
func blocks(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Blocks * 512
}

func TestTreeSizeKnownTree(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a"), strings.Repeat("a", 10000))
	write(t, filepath.Join(dir, "d1", "b"), "b")
	write(t, filepath.Join(dir, "d1", "d2", "c"), strings.Repeat("c", 5000))
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	r := openRoot(t, dir)

	want := Size{Entries: 7}
	for _, p := range []string{"", "a", "d1", "d1/b", "d1/d2", "d1/d2/c", "empty"} {
		want.Bytes += blocks(t, filepath.Join(dir, p))
	}
	got, err := r.TreeSize(".", SizeLimits{1 << 30, 1000})
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}

	// A subtree: its own folder and what is below it.
	want = Size{Entries: 4}
	for _, p := range []string{"d1", "d1/b", "d1/d2", "d1/d2/c"} {
		want.Bytes += blocks(t, filepath.Join(dir, p))
	}
	for _, rel := range []string{"d1", "d1/", "./d1"} {
		got, err = r.TreeSize(rel, SizeLimits{1 << 30, 1000})
		if err != nil || got != want {
			t.Fatalf("%s: got %+v, %v; want %+v", rel, got, err, want)
		}
	}

	// A regular file is a tree of one.
	got, err = r.TreeSize("a", SizeLimits{1 << 30, 1000})
	if err != nil || got != (Size{blocks(t, filepath.Join(dir, "a")), 1}) {
		t.Fatalf("file: %+v, %v", got, err)
	}
	if _, err = r.TreeSize("missing", SizeLimits{1 << 30, 1000}); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
}

func TestTreeSizeLimitsRequired(t *testing.T) {
	r := openRoot(t, t.TempDir())
	for _, lim := range []SizeLimits{{}, {MaxBytes: 1}, {MaxEntries: 1}, {-1, 1}, {1, -1}} {
		if _, err := r.TreeSize(".", lim); err == nil {
			t.Errorf("%+v accepted", lim)
		}
	}
}

func TestTreeSizeDoesNotFollowSymlinks(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "big"), strings.Repeat("x", 1<<20))
	write(t, filepath.Join(outside, "d", "more"), strings.Repeat("x", 1<<20))
	dir := t.TempDir()
	write(t, filepath.Join(dir, "f"), "x")
	mustSymlink(t, outside, filepath.Join(dir, "out-dir"))
	mustSymlink(t, filepath.Join(outside, "big"), filepath.Join(dir, "out-file"))
	mustSymlink(t, ".", filepath.Join(dir, "loop"))
	r := openRoot(t, dir)

	want := Size{Bytes: blocks(t, dir) + blocks(t, filepath.Join(dir, "f")), Entries: 5}
	got, err := r.TreeSize(".", SizeLimits{1 << 20, 100})
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}

func TestTreeSizeEntryLimit(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		write(t, filepath.Join(dir, n), "x")
	}
	r := openRoot(t, dir)
	// Root and five files are six entries: at the limit it passes.
	if _, err := r.TreeSize(".", SizeLimits{1 << 30, 6}); err != nil {
		t.Fatalf("at limit: %v", err)
	}
	got, err := r.TreeSize(".", SizeLimits{1 << 30, 3})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got.Entries != 4 || got.Bytes == 0 {
		t.Fatalf("size at stop: %+v", got)
	}
}

func TestTreeSizeByteLimit(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "big"), strings.Repeat("x", 1<<20))
	r := openRoot(t, dir)
	got, err := r.TreeSize(".", SizeLimits{MaxBytes: 64 << 10, MaxEntries: 100})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got.Bytes <= 64<<10 || got.Entries != 2 {
		t.Fatalf("size at stop: %+v", got)
	}
	if _, err = r.TreeSize(".", SizeLimits{MaxBytes: 2 << 20, MaxEntries: 100}); err != nil {
		t.Fatal(err)
	}
}

func TestTreeSizeFIFOAndOtherNames(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "d", "f"), "x")
	if err := unix.Mkfifo(filepath.Join(dir, "d", "pipe"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mknod(filepath.Join(dir, "sock-like"), unix.S_IFIFO|0o644, 0); err != nil {
		t.Fatal(err)
	}
	r := openRoot(t, dir)
	var got Size
	var err error
	withDeadline(t, func() { got, err = r.TreeSize(".", SizeLimits{1 << 30, 100}) })
	want := Size{Entries: 5}
	for _, p := range []string{"", "d", "d/f"} {
		want.Bytes += blocks(t, filepath.Join(dir, p))
	}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
	// A FIFO as the start is one entry of size 0.
	withDeadline(t, func() { got, err = r.TreeSize("d/pipe", SizeLimits{1, 1}) })
	if err != nil || got != (Size{0, 1}) {
		t.Fatalf("fifo start: %+v, %v", got, err)
	}
}

func TestTreeSizeHardLinksCountPerName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a"), strings.Repeat("x", 8192))
	if err := os.Link(filepath.Join(dir, "a"), filepath.Join(dir, "b")); err != nil {
		t.Skip("no hard links:", err)
	}
	r := openRoot(t, dir)
	got, err := r.TreeSize(".", SizeLimits{1 << 30, 100})
	want := Size{Bytes: blocks(t, dir) + 2*blocks(t, filepath.Join(dir, "a")), Entries: 3}
	if err != nil || got != want {
		t.Fatalf("got %+v, %v; want %+v", got, err, want)
	}
}

func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Skip("no /proc/self/fd:", err)
	}
	return len(ents)
}

func deepTree(t *testing.T, root string, levels int) {
	t.Helper()
	parts := make([]string, levels)
	for i := range parts {
		parts[i] = "d"
	}
	p := filepath.Join(append([]string{root}, parts...)...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(p, "leaf"), "x")
}

func TestTreeSizeDeepTree(t *testing.T) {
	dir := t.TempDir()
	deepTree(t, dir, 200)
	r := openRoot(t, dir)
	before := openFDs(t)
	got, err := r.TreeSize(".", SizeLimits{1 << 30, 10000})
	if err != nil {
		t.Fatal(err)
	}
	if got.Entries != 202 {
		t.Fatalf("got %+v", got)
	}
	if after := openFDs(t); after != before {
		t.Fatalf("descriptors: %d before, %d after", before, after)
	}
}

func TestTreeSizePastMaxDepth(t *testing.T) {
	dir := t.TempDir()
	deepTree(t, dir, MaxDepth+20)
	r := openRoot(t, dir)
	before := openFDs(t)
	_, err := r.TreeSize(".", SizeLimits{1 << 30, 10000})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("got %v", err)
	}
	if after := openFDs(t); after != before {
		t.Fatalf("descriptors: %d before, %d after", before, after)
	}
}

func TestTreeSizeCrossDevice(t *testing.T) {
	var root, proc unix.Stat_t
	if unix.Stat("/", &root) != nil || unix.Stat("/proc", &proc) != nil || root.Dev == proc.Dev {
		t.Skip("/proc is not a separate filesystem here")
	}
	r, err := OpenRoot("/")
	if err != nil {
		t.Skip(err)
	}
	defer r.Close()
	if _, err = r.TreeSize("proc", SizeLimits{1 << 30, 1 << 20}); !errors.Is(err, ErrCrossDevice) {
		t.Fatalf("got %v", err)
	}
}

func TestErrorNamesTheEntry(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a", "b", "f"), "x")
	r := openRoot(t, dir)
	_, err := r.TreeSize("a", SizeLimits{1 << 30, 2})
	var pe *fs.PathError
	if !errors.As(err, &pe) || !errors.Is(err, ErrLimit) || !strings.HasPrefix(pe.Path, "a/b") {
		t.Fatalf("got %v", err)
	}
}
