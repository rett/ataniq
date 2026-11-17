//go:build linux

package safefile

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// newRoot makes a folder under t.TempDir() with a sibling "outside" folder
// that holds a secret, and returns a handle on the folder. The tests use the
// real system calls; they create no namespace.
func newRoot(t *testing.T) (r *Root, dir, outside string) {
	t.Helper()
	base := t.TempDir()
	dir = filepath.Join(base, "root")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{dir, outside} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// OpenRoot probes openat2. Where it does not work the tests fail, loudly:
	// a green run on such a system would cover nothing.
	r, err := OpenRoot(dir)
	if err != nil {
		t.Fatalf("these tests need a working openat2 (OpenRoot: %v)", err)
	}
	t.Cleanup(func() { r.Close() })
	return r, dir, outside
}

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// snapshot lists every entry under dir with its mode, size and mtime, to
// assert that a refused call changed nothing.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := os.Lstat(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		m[rel] = fi.Mode().String() + " " + fi.ModTime().String() + " " + strconv.FormatInt(fi.Size(), 10)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestReadFile(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "a.txt"), "hello")
	write(t, filepath.Join(dir, "d/e/f.txt"), "deep")
	write(t, filepath.Join(dir, "empty"), "")

	got, err := r.ReadFile("a.txt", 5)
	if err != nil || string(got) != "hello" {
		t.Fatalf("ReadFile a.txt = %q, %v", got, err)
	}
	got, err = r.ReadFile("d/e/f.txt", 100)
	if err != nil || string(got) != "deep" {
		t.Fatalf("ReadFile d/e/f.txt = %q, %v", got, err)
	}
	got, err = r.ReadFile("empty", 0)
	if err != nil || len(got) != 0 {
		t.Fatalf("ReadFile empty with cap 0 = %q, %v", got, err)
	}
	if _, err := r.ReadFile("missing", 10); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v, want fs.ErrNotExist", err)
	}
	if _, err := r.ReadFile("a.txt", -1); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("negative cap: %v", err)
	}
	// A middle component that is a file is not a folder.
	if _, err := r.ReadFile("a.txt/x", 10); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("file as a folder: %v, want ErrNotDirectory", err)
	}
}

func TestReadFileRefusesSymlinks(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "real/inner"), "inner")
	write(t, filepath.Join(dir, "plain"), "plain")

	symlink(t, filepath.Join(outside, "secret"), filepath.Join(dir, "link-out")) // absolute, outside
	symlink(t, "../outside/secret", filepath.Join(dir, "link-rel-out"))          // relative, outside
	symlink(t, "plain", filepath.Join(dir, "link-in"))                           // inside the root
	symlink(t, "real", filepath.Join(dir, "link-dir"))                           // folder inside the root
	symlink(t, outside, filepath.Join(dir, "link-outdir"))                       // folder outside
	symlink(t, filepath.Join(dir, "gone"), filepath.Join(dir, "link-dangling"))  // dangling
	symlink(t, "link-loop", filepath.Join(dir, "link-loop"))                     // loop
	symlink(t, "/proc/self/exe", filepath.Join(dir, "link-magic"))               // magic link target
	symlink(t, "link-in", filepath.Join(dir, "link-chain"))                      // chain
	symlink(t, "/proc/self/root", filepath.Join(dir, "link-root"))               // magic folder
	write(t, filepath.Join(dir, "real/nested/x"), "x")
	symlink(t, "../plain", filepath.Join(dir, "real/nested/up"))

	for _, rel := range []string{
		"link-out", "link-rel-out", "link-in", "link-dangling", "link-loop", "link-magic", "link-chain",
		"link-dir/inner", "link-outdir/secret", "link-root/etc", "real/nested/up",
	} {
		got, err := r.ReadFile(rel, 1<<20)
		if !errors.Is(err, ErrSymlink) {
			t.Errorf("ReadFile(%q) = %q, %v; want ErrSymlink", rel, got, err)
		}
		if len(got) != 0 {
			t.Errorf("ReadFile(%q) returned data", rel)
		}
	}
	// The same refusals hold for the other readers.
	if _, err := r.ReadDirNames("link-dir", 10); !errors.Is(err, ErrSymlink) {
		t.Errorf("ReadDirNames through a link: %v", err)
	}
	if _, err := r.ReadDirNames("link-outdir", 10); !errors.Is(err, ErrSymlink) {
		t.Errorf("ReadDirNames through a link outside: %v", err)
	}
}

func TestReadFileRefusesBadPaths(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "a"), "a")
	write(t, filepath.Join(dir, "d/b"), "b")

	escapes := []string{"/etc/passwd", filepath.Join(dir, "a"), "..", "../outside/secret", "d/../a", "d/..", "a/../..", "/"}
	for _, rel := range escapes {
		if _, err := r.ReadFile(rel, 10); !errors.Is(err, ErrEscapes) {
			t.Errorf("ReadFile(%q): %v, want ErrEscapes", rel, err)
		}
	}
	invalid := []string{"", ".", "./a", "d//b", "d/", "d/.", "a\x00b", strings.Repeat("a/", 3000)}
	for _, rel := range invalid {
		if _, err := r.ReadFile(rel, 10); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("ReadFile(%q): %v, want ErrInvalidArgument", rel, err)
		}
	}
}

func TestReadFileRefusesNonRegular(t *testing.T) {
	r, dir, _ := newRoot(t)
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, rel := range []string{"folder", "fifo"} {
		// A FIFO with no writer must not block the open: bound the call by
		// a deadline, not by waiting.
		done := make(chan error, 1)
		go func() {
			_, err := r.ReadFile(rel, 10)
			done <- err
		}()
		select {
		case err := <-done:
			if !errors.Is(err, ErrNotRegular) {
				t.Errorf("ReadFile(%q): %v, want ErrNotRegular", rel, err)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("ReadFile(%q) blocked", rel)
		}
	}
}

func TestReadFileRefusesSocket(t *testing.T) {
	r, dir, _ := newRoot(t)
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Skip(err)
	}
	defer unix.Close(fd)
	// Bind through a descriptor path to stay under the socket path limit.
	dfd, err := unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dfd)
	sockPath := "/proc/self/fd/" + strconv.Itoa(dfd) + "/sock"
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: sockPath}); err != nil {
		t.Skipf("cannot bind a socket here: %v", err)
	}
	if _, err := r.ReadFile("sock", 10); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("socket: %v, want ErrNotRegular", err)
	}
}

func TestInfoModes(t *testing.T) {
	for _, tc := range []struct {
		mode    uint32
		regular bool
		typ     fs.FileMode
	}{
		{unix.S_IFREG | 0o644, true, 0},
		{unix.S_IFDIR | 0o755, false, fs.ModeDir},
		{unix.S_IFIFO | 0o600, false, fs.ModeNamedPipe},
		{unix.S_IFSOCK | 0o600, false, fs.ModeSocket},
		{unix.S_IFCHR | 0o600, false, fs.ModeDevice | fs.ModeCharDevice},
		{unix.S_IFBLK | 0o600, false, fs.ModeDevice},
		{unix.S_IFLNK | 0o777, false, fs.ModeSymlink},
	} {
		info := infoFromStat(&unix.Stat_t{Mode: tc.mode})
		if info.IsRegular() != tc.regular || info.Type() != tc.typ {
			t.Errorf("mode %#o: regular %v type %v; want %v %v", tc.mode, info.IsRegular(), info.Type(), tc.regular, tc.typ)
		}
	}
	if m := infoFromStat(&unix.Stat_t{Mode: unix.S_IFREG | unix.S_ISUID | 0o755}).Mode; m != fs.ModeSetuid|0o755 {
		t.Errorf("setuid mode = %v", m)
	}
}

func TestReadFileCaps(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "ten"), "0123456789")

	if got, err := r.ReadFile("ten", 10); err != nil || len(got) != 10 {
		t.Fatalf("exact cap: %q, %v", got, err)
	}
	got, err := r.ReadFile("ten", 9)
	if !errors.Is(err, ErrTooLarge) || got != nil {
		t.Fatalf("over the cap: %q, %v; want ErrTooLarge and no data", got, err)
	}
	if _, err := r.ReadFile("ten", 0); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("cap 0: %v", err)
	}
}

// A file that grows after fstat is caught by the capped read, not truncated.
func TestReadCappedGrowth(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "grow"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(bytes.Repeat([]byte("x"), 100)); err != nil {
		t.Fatal(err)
	}
	fd := int(f.Fd())
	// The hint says 10 bytes (what fstat once reported), the file has 100.
	if _, err := unix.Seek(fd, 0, 0); err != nil {
		t.Fatal(err)
	}
	if got, err := readCapped(fd, 10, 50); !errors.Is(err, ErrTooLarge) || got != nil {
		t.Fatalf("grown file over the cap: %d bytes, %v", len(got), err)
	}
	if _, err := unix.Seek(fd, 0, 0); err != nil {
		t.Fatal(err)
	}
	got, err := readCapped(fd, 10, 100)
	if err != nil || len(got) != 100 {
		t.Fatalf("grown file within the cap: %d bytes, %v", len(got), err)
	}
	// A hint far above the cap does not allocate past the cap.
	if _, err := unix.Seek(fd, 0, 0); err != nil {
		t.Fatal(err)
	}
	if got, err := readCapped(fd, 1<<40, 1000); err != nil || len(got) != 100 {
		t.Fatalf("large hint: %d bytes, %v", len(got), err)
	}
}

func TestReplacedBetweenReads(t *testing.T) {
	r, dir, outside := newRoot(t)
	p := filepath.Join(dir, "state")
	write(t, p, "first")
	if got, err := r.ReadFile("state", 100); err != nil || string(got) != "first" {
		t.Fatalf("first read: %q, %v", got, err)
	}
	// The lead swaps the file for a link between the reads.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(outside, "secret"), p)
	if got, err := r.ReadFile("state", 100); !errors.Is(err, ErrSymlink) || got != nil {
		t.Fatalf("second read: %q, %v; want ErrSymlink", got, err)
	}
	// And a middle component swapped for a link to an outside folder.
	write(t, filepath.Join(dir, "sub/state"), "sub")
	if _, err := r.ReadFile("sub/state", 100); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "sub"), filepath.Join(dir, "sub.old")); err != nil {
		t.Fatal(err)
	}
	symlink(t, outside, filepath.Join(dir, "sub"))
	write(t, filepath.Join(outside, "state"), "outside")
	if got, err := r.ReadFile("sub/state", 100); !errors.Is(err, ErrSymlink) || got != nil {
		t.Fatalf("swapped folder: %q, %v; want ErrSymlink", got, err)
	}
}

func TestHandleSurvivesRootRename(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "f"), "inside")
	write(t, filepath.Join(outside, "f"), "outside")
	moved := dir + ".moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	symlink(t, outside, dir)
	// The handle names the original folder, not the path it had.
	got, err := r.ReadFile("f", 100)
	if err != nil || string(got) != "inside" {
		t.Fatalf("read after the root path was replaced by a link: %q, %v", got, err)
	}
}

func TestHardLinks(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "one"), "data")
	res, err := r.ReadFileInfo("one", 100)
	if err != nil || res.Info.HardLinked() || res.Info.Nlink != 1 || string(res.Data) != "data" {
		t.Fatalf("single link: %+v, %v", res, err)
	}
	if err := os.Link(filepath.Join(dir, "one"), filepath.Join(dir, "two")); err != nil {
		t.Skipf("hard links not available here: %v", err)
	}
	res, err = r.ReadFileInfo("two", 100)
	if err != nil || !res.Info.HardLinked() || res.Info.Nlink != 2 || string(res.Data) != "data" {
		t.Fatalf("two links reported: %+v, %v", res, err)
	}
	if got, err := r.ReadFile("two", 100); err != nil || string(got) != "data" {
		t.Fatalf("ReadFile accepts a hard link by default: %q, %v", got, err)
	}
	if got, err := r.ReadFile("two", 100, RefuseHardLinks()); !errors.Is(err, ErrHardLink) || got != nil {
		t.Fatalf("RefuseHardLinks: %q, %v", got, err)
	}
	if _, err := r.ReadFile("one", 100, RefuseHardLinks()); !errors.Is(err, ErrHardLink) {
		t.Fatalf("RefuseHardLinks on the other name: %v", err)
	}
	// Append never goes through a hard link and changes nothing.
	if err := r.AppendFile("two", []byte("more")); !errors.Is(err, ErrHardLink) {
		t.Fatalf("AppendFile on a hard link: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "one")); string(b) != "data" {
		t.Fatalf("file changed through a refused append: %q", b)
	}
	// A link made to a file outside the folder is the case the option exists for.
	if err := os.Link(filepath.Join(outside, "secret"), filepath.Join(dir, "stolen")); err == nil {
		if _, err := r.ReadFile("stolen", 100, RefuseHardLinks()); !errors.Is(err, ErrHardLink) {
			t.Fatalf("hard link to an outside file: %v", err)
		}
	}
}

func TestLstat(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "f"), "abc")
	if err := os.Mkdir(filepath.Join(dir, "d"), 0o750); err != nil {
		t.Fatal(err)
	}
	symlink(t, outside, filepath.Join(dir, "d/link"))
	symlink(t, "nowhere", filepath.Join(dir, "dangling"))
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}

	info, err := r.Lstat("f")
	if err != nil || !info.IsRegular() || info.Size != 3 || info.Mode.Perm() != 0o644 || info.Nlink != 1 {
		t.Fatalf("Lstat f: %+v, %v", info, err)
	}
	if info, err = r.Lstat("d"); err != nil || !info.IsDir() || info.Mode.Perm() != 0o750 {
		t.Fatalf("Lstat d: %+v, %v", info, err)
	}
	if info, err = r.Lstat("."); err != nil || !info.IsDir() {
		t.Fatalf("Lstat .: %+v, %v", info, err)
	}
	// A link as the last component is reported, not followed.
	if info, err = r.Lstat("d/link"); err != nil || !info.IsSymlink() || info.IsDir() {
		t.Fatalf("Lstat d/link: %+v, %v", info, err)
	}
	if info, err = r.Lstat("dangling"); err != nil || !info.IsSymlink() {
		t.Fatalf("Lstat dangling: %+v, %v", info, err)
	}
	if info, err = r.Lstat("fifo"); err != nil || info.Type() != fs.ModeNamedPipe {
		t.Fatalf("Lstat fifo: %+v, %v", info, err)
	}
	// A link in a parent component is refused.
	if _, err = r.Lstat("d/link/secret"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Lstat through a link: %v", err)
	}
	if _, err = r.Lstat("missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat missing: %v", err)
	}
	for rel, want := range map[string]error{"..": ErrEscapes, "/": ErrEscapes, "": ErrInvalidArgument, "a//b": ErrInvalidArgument} {
		if _, err = r.Lstat(rel); !errors.Is(err, want) {
			t.Errorf("Lstat(%q): %v, want %v", rel, err, want)
		}
	}
}

func TestMkdirAll(t *testing.T) {
	r, dir, _ := newRoot(t)
	if err := r.MkdirAll("a/b/c", 0o750); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, "a/b/c"))
	if err != nil || !fi.IsDir() {
		t.Fatalf("a/b/c: %v, %v", fi, err)
	}
	if fi.Mode().Perm() != 0o750&^currentUmask() {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	if err := r.MkdirAll("a/b/c", 0o700); err != nil {
		t.Fatalf("again: %v", err)
	}
	if err := r.MkdirAll("a/b/c/d/e", 0o700); err != nil {
		t.Fatalf("extend: %v", err)
	}
	if err := r.MkdirAll(".", 0o700); err != nil {
		t.Fatalf("dot: %v", err)
	}
	// An existing folder keeps its mode.
	fi, _ = os.Stat(filepath.Join(dir, "a"))
	if fi.Mode().Perm() != 0o750&^currentUmask() {
		t.Errorf("existing folder mode changed: %v", fi.Mode().Perm())
	}
	// A file in the way.
	write(t, filepath.Join(dir, "file"), "x")
	if err := r.MkdirAll("file/sub", 0o700); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("file in the way: %v", err)
	}
	if err := r.MkdirAll("file", 0o700); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("file as the last component: %v", err)
	}
	for rel, want := range map[string]error{"../x": ErrEscapes, "/x": ErrEscapes, "": ErrInvalidArgument, "a//b": ErrInvalidArgument} {
		if err := r.MkdirAll(rel, 0o700); !errors.Is(err, want) {
			t.Errorf("MkdirAll(%q): %v, want %v", rel, err, want)
		}
	}
	if err := r.MkdirAll("x", fs.ModeDir|0o700); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("mode with a type bit: %v", err)
	}
	if err := r.MkdirAll("x", fs.ModeSetuid|0o700); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("mode with setuid: %v", err)
	}
}

func currentUmask() fs.FileMode {
	old := unix.Umask(0)
	unix.Umask(old)
	return fs.FileMode(old)
}

func TestMkdirAllThroughPlantedSymlink(t *testing.T) {
	r, dir, outside := newRoot(t)
	symlink(t, outside, filepath.Join(dir, "link"))
	symlink(t, "../outside", filepath.Join(dir, "rel"))
	if err := os.Mkdir(filepath.Join(dir, "mid"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, outside, filepath.Join(dir, "mid/link"))
	symlink(t, "mid", filepath.Join(dir, "inside"))
	before := snapshot(t, outside)
	rootBefore := snapshot(t, dir)

	for _, rel := range []string{"link", "link/new", "link/new/deeper", "rel/new", "mid/link/new/deeper", "inside/new"} {
		if err := r.MkdirAll(rel, 0o755); !errors.Is(err, ErrSymlink) {
			t.Errorf("MkdirAll(%q): %v, want ErrSymlink", rel, err)
		}
	}
	if after := snapshot(t, outside); !reflect.DeepEqual(before, after) {
		t.Fatalf("outside folder changed:\nbefore %v\nafter  %v", before, after)
	}
	if after := snapshot(t, dir); !reflect.DeepEqual(rootBefore, after) {
		t.Fatalf("root changed by refused calls:\nbefore %v\nafter  %v", rootBefore, after)
	}
}

// afterOpenat2Call makes r run fn once, right after its n-th openat2 call
// returns (n counts from 1), which stands in for a lead acting between two
// steps of the owner's call.
func afterOpenat2Call(r *Root, n int, fn func()) {
	calls := 0
	r.openat2 = func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		fd, err := unix.Openat2(dirfd, p, how)
		calls++
		if calls == n {
			fn()
		}
		return fd, err
	}
}

// A folder renamed out of the root while MkdirAll walks carries the rest of
// the walk with it: the call must say so and not report success.
func TestMkdirAllFolderMovedDuringWalk(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace bool // the lead puts a folder with the same names back
	}{
		{"moved away", false},
		{"moved away and replaced", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, dir, outside := newRoot(t)
			if err := os.Mkdir(filepath.Join(dir, "a"), 0o755); err != nil {
				t.Fatal(err)
			}
			afterOpenat2Call(r, 1, func() {
				if err := os.Rename(filepath.Join(dir, "a"), filepath.Join(outside, "a")); err != nil {
					t.Error(err)
				}
				if tc.replace {
					if err := os.MkdirAll(filepath.Join(dir, "a/b/c"), 0o755); err != nil {
						t.Error(err)
					}
				}
			})
			err := r.MkdirAll("a/b/c", 0o755)
			if !errors.Is(err, ErrEscapes) {
				t.Fatalf("MkdirAll after a folder moved: %v, want ErrEscapes", err)
			}
			// What the doc admits: the folders made after the move are left
			// where the moved folder went.
			if _, err := os.Stat(filepath.Join(outside, "a/b/c")); err != nil {
				t.Errorf("expected the walk to have continued in the moved folder: %v", err)
			}
		})
	}
}

// A mkdirat failure is reported as itself: EPERM from mkdirat is not "openat2
// is unavailable".
func TestMkdirAllMkdiratErrors(t *testing.T) {
	for _, tc := range []struct {
		errno unix.Errno
		want  error // nil: only that the error is not a package sentinel
	}{
		{unix.EPERM, nil},
		{unix.EACCES, fs.ErrPermission},
		{unix.EROFS, nil},
		{unix.ENOTDIR, ErrNotDirectory},
	} {
		t.Run(tc.errno.Error(), func(t *testing.T) {
			r, dir, _ := newRoot(t)
			r.mkdirat = func(int, string, uint32) error { return tc.errno }
			err := r.MkdirAll("d/e", 0o755)
			if !errors.Is(err, tc.errno) {
				t.Fatalf("MkdirAll: %v, want it to wrap %v", err, tc.errno)
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Errorf("MkdirAll: %v, want %v", err, tc.want)
			}
			for _, s := range []error{ErrUnsupported, ErrSymlink, ErrEscapes} {
				if errors.Is(err, s) {
					t.Errorf("MkdirAll: %v wrongly matches %v", err, s)
				}
			}
			if _, err := os.Lstat(filepath.Join(dir, "d")); err == nil {
				t.Error("a folder was made")
			}
		})
	}
}

// Without a move the end check passes, also for a path that already exists.
func TestMkdirAllEndCheckAcceptsStablePath(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "x/y/f"), "")
	for _, rel := range []string{"x", "x/y", "x/y/z", "p/q"} {
		if err := r.MkdirAll(rel, 0o755); err != nil {
			t.Errorf("MkdirAll(%q): %v", rel, err)
		}
	}
}

func TestWriteFileExclusive(t *testing.T) {
	r, dir, outside := newRoot(t)
	if err := r.MkdirAll("d", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := r.WriteFileExclusive("d/new", []byte("one"), 0o640); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "d/new"))
	if err != nil || string(b) != "one" {
		t.Fatalf("content: %q, %v", b, err)
	}
	if fi, _ := os.Stat(filepath.Join(dir, "d/new")); fi.Mode().Perm() != 0o640&^currentUmask() {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	// Never replaces.
	if err := r.WriteFileExclusive("d/new", []byte("two"), 0o640); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("second create: %v, want fs.ErrExist", err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "d/new")); string(b) != "one" {
		t.Fatalf("file replaced: %q", b)
	}
	// Missing parent is not created.
	if err := r.WriteFileExclusive("nope/new", nil, 0o600); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing parent: %v", err)
	}

	// A planted link as the target, dangling and to an existing file.
	symlink(t, filepath.Join(outside, "created"), filepath.Join(dir, "dangling"))
	symlink(t, filepath.Join(outside, "secret"), filepath.Join(dir, "tosecret"))
	symlink(t, outside, filepath.Join(dir, "linkdir"))
	before := snapshot(t, outside)
	for _, rel := range []string{"dangling", "tosecret", "linkdir/created", "linkdir/secret"} {
		err := r.WriteFileExclusive(rel, []byte("x"), 0o600)
		if !errors.Is(err, ErrSymlink) {
			t.Errorf("WriteFileExclusive(%q): %v, want ErrSymlink", rel, err)
		}
		// A link as the last component also still reads as "exists".
		if !strings.Contains(rel, "/") && !errors.Is(err, fs.ErrExist) {
			t.Errorf("WriteFileExclusive(%q): %v, want it to match fs.ErrExist too", rel, err)
		}
	}
	// A plain entry that exists is only "exists", not a link.
	for _, rel := range []string{"d", "d/new"} {
		if err := r.WriteFileExclusive(rel, nil, 0o600); !errors.Is(err, fs.ErrExist) || errors.Is(err, ErrSymlink) {
			t.Errorf("WriteFileExclusive(%q) over a non-link: %v, want fs.ErrExist and not ErrSymlink", rel, err)
		}
	}
	if err := r.WriteFileExclusive("linkdir/created", nil, 0o600); !errors.Is(err, ErrSymlink) {
		t.Errorf("through a linked folder: %v, want ErrSymlink", err)
	}
	if after := snapshot(t, outside); !reflect.DeepEqual(before, after) {
		t.Fatalf("outside changed:\nbefore %v\nafter  %v", before, after)
	}
	for rel, want := range map[string]error{"../x": ErrEscapes, "/x": ErrEscapes, "": ErrInvalidArgument, ".": ErrInvalidArgument} {
		if err := r.WriteFileExclusive(rel, nil, 0o600); !errors.Is(err, want) {
			t.Errorf("WriteFileExclusive(%q): %v, want %v", rel, err, want)
		}
	}
	if err := r.WriteFileExclusive("m", nil, fs.ModeSetuid|0o600); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("setuid mode: %v", err)
	}
}

func TestAppendFile(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "log"), "a")
	if err := r.AppendFile("log", []byte("b")); err != nil {
		t.Fatal(err)
	}
	if err := r.AppendFile("log", []byte("c")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "log")); string(b) != "abc" {
		t.Fatalf("content %q", b)
	}
	// Existing files only.
	if err := r.AppendFile("absent", []byte("x")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("absent: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "absent")); err == nil {
		t.Fatal("AppendFile created a file")
	}

	symlink(t, filepath.Join(outside, "secret"), filepath.Join(dir, "link"))
	symlink(t, "log", filepath.Join(dir, "inlink"))
	symlink(t, outside, filepath.Join(dir, "linkdir"))
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]error{
		"link": ErrSymlink, "inlink": ErrSymlink, "linkdir/secret": ErrSymlink,
		"folder": ErrNotRegular, "fifo": ErrNotRegular,
		"../x": ErrEscapes, "/x": ErrEscapes, "": ErrInvalidArgument,
	} {
		done := make(chan error, 1)
		go func() { done <- r.AppendFile(rel, []byte("x")) }()
		select {
		case err := <-done:
			if !errors.Is(err, want) {
				t.Errorf("AppendFile(%q): %v, want %v", rel, err, want)
			}
		case <-time.After(20 * time.Second):
			t.Fatalf("AppendFile(%q) blocked", rel)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret")); string(b) != "secret" {
		t.Fatalf("outside file changed: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "log")); string(b) != "abc" {
		t.Fatalf("log changed through a refused call: %q", b)
	}
}

func TestReadDirNames(t *testing.T) {
	r, dir, outside := newRoot(t)
	for _, n := range []string{"b", "a", "c", ".hidden"} {
		write(t, filepath.Join(dir, "d", n), "")
	}
	symlink(t, outside, filepath.Join(dir, "d", "link"))
	got, err := r.ReadDirNames("d", 10)
	want := []string{".hidden", "a", "b", "c", "link"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadDirNames d = %v, %v; want %v", got, err, want)
	}
	if _, err := r.ReadDirNames("d", 5); err != nil {
		t.Fatalf("exactly the cap: %v", err)
	}
	if got, err := r.ReadDirNames("d", 4); !errors.Is(err, ErrTooLarge) || got != nil {
		t.Fatalf("over the cap: %v, %v", got, err)
	}
	if got, err := r.ReadDirNames("d", 0); !errors.Is(err, ErrTooLarge) || got != nil {
		t.Fatalf("cap 0: %v, %v", got, err)
	}
	if err := os.Mkdir(filepath.Join(dir, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := r.ReadDirNames("empty", 0); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty folder: %#v, %v", got, err)
	}
	if got, err := r.ReadDirNames(".", 100); err != nil || !reflect.DeepEqual(got, []string{"d", "empty"}) {
		t.Fatalf("root: %v, %v", got, err)
	}
	if _, err := r.ReadDirNames("d/a", 10); !errors.Is(err, ErrNotDirectory) {
		t.Fatalf("file: %v", err)
	}
	if _, err := r.ReadDirNames("d/link", 10); !errors.Is(err, ErrSymlink) {
		t.Fatalf("link: %v", err)
	}
	if _, err := r.ReadDirNames("d", -1); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("negative: %v", err)
	}
	if _, err := r.ReadDirNames("..", 10); !errors.Is(err, ErrEscapes) {
		t.Fatalf("..: %v", err)
	}
	// More entries than one getdents buffer holds.
	for i := 0; i < 600; i++ {
		write(t, filepath.Join(dir, "many", strings.Repeat("n", 40)+strconv.Itoa(i)), "")
	}
	if got, err := r.ReadDirNames("many", 600); err != nil || len(got) != 600 {
		t.Fatalf("many: %d, %v", len(got), err)
	}
	if _, err := r.ReadDirNames("many", 599); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("many over cap: %v", err)
	}
}

// With openat2 unavailable or denied every operation is refused as
// unsupported and nothing is tried by path: the files below exist and are
// readable by path, and none is read, created or changed.
func TestUnsupportedRefuses(t *testing.T) {
	for _, errno := range []unix.Errno{unix.ENOSYS, unix.EPERM} {
		t.Run(errno.Error(), func(t *testing.T) {
			r, dir, _ := newRoot(t)
			write(t, filepath.Join(dir, "f"), "readable")
			if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
				t.Fatal(err)
			}
			calls := 0
			r.openat2 = func(int, string, *unix.OpenHow) (int, error) {
				calls++
				return -1, errno
			}
			ops := map[string]func() error{
				"ReadFile":           func() error { _, err := r.ReadFile("f", 100); return err },
				"ReadFileInfo":       func() error { _, err := r.ReadFileInfo("f", 100); return err },
				"Lstat":              func() error { _, err := r.Lstat("f"); return err },
				"MkdirAll":           func() error { return r.MkdirAll("d/x", 0o755) },
				"WriteFileExclusive": func() error { return r.WriteFileExclusive("new", []byte("x"), 0o600) },
				"AppendFile":         func() error { return r.AppendFile("f", []byte("x")) },
				"ReadDirNames":       func() error { _, err := r.ReadDirNames("d", 10); return err },
			}
			for name, op := range ops {
				before := calls
				err := op()
				if !errors.Is(err, ErrUnsupported) || !errors.Is(err, errno) {
					t.Errorf("%s: %v, want ErrUnsupported wrapping %v", name, err, errno)
				}
				if calls == before {
					t.Errorf("%s did not reach openat2", name)
				}
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "f")); string(b) != "readable" {
				t.Errorf("file changed: %q", b)
			}
			for _, n := range []string{"new", "d/x"} {
				if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
					t.Errorf("%s was created", n)
				}
			}
		})
	}
}

func TestOpenRoot(t *testing.T) {
	base := t.TempDir()
	write(t, filepath.Join(base, "file"), "x")
	if err := os.Mkdir(filepath.Join(base, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(base, "dir"), filepath.Join(base, "linkdir"))
	symlink(t, filepath.Join(base, "file"), filepath.Join(base, "linkfile"))

	r, err := OpenRoot(filepath.Join(base, "dir"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]error{
		"linkdir": ErrSymlink, "linkfile": ErrSymlink, "file": ErrNotDirectory,
		"missing": fs.ErrNotExist, "": ErrInvalidArgument, "a\x00b": ErrInvalidArgument,
	} {
		r, err := OpenRoot(filepath.Join(base, rel))
		if rel == "" {
			r, err = OpenRoot("")
		}
		if err == nil {
			r.Close()
		}
		if !errors.Is(err, want) {
			t.Errorf("OpenRoot(%q): %v, want %v", rel, err, want)
		}
	}
}

// A lease a lead holds on a file does not stall an owner-side open: it fails
// at once with EAGAIN (the open is O_NONBLOCK). Skipped where leases cannot be
// taken (they need a file system and a setting that allow them).
func TestLeasedFileFailsAtOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		flags  int
		lease  int
		access func(r *Root) error
	}{
		{"append under a read lease", unix.O_RDONLY, unix.F_RDLCK, func(r *Root) error { return r.AppendFile("f", []byte("x")) }},
		{"read under a write lease", unix.O_RDWR, unix.F_WRLCK, func(r *Root) error { _, err := r.ReadFile("f", 10); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, dir, _ := newRoot(t)
			p := filepath.Join(dir, "f")
			write(t, p, "x")
			fd, err := unix.Open(p, tc.flags|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETLEASE, tc.lease); err != nil {
				t.Skipf("cannot take a lease here: %v", err)
			}
			defer unix.FcntlInt(uintptr(fd), unix.F_SETLEASE, unix.F_UNLCK)
			done := make(chan error, 1)
			go func() { done <- tc.access(r) }()
			select {
			case err := <-done:
				if !errors.Is(err, unix.EAGAIN) {
					t.Fatalf("access to a leased file: %v, want EAGAIN", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("access to a leased file waited for the lease to break")
			}
		})
	}
}

// OpenRoot probes openat2 once and refuses with ErrUnsupported when it does
// not work, closing what it opened.
func TestOpenRootProbe(t *testing.T) {
	base := t.TempDir()
	countFDs := func() int {
		ents, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(ents)
	}
	before := countFDs()
	for _, errno := range []unix.Errno{unix.ENOSYS, unix.EPERM, unix.EINVAL, unix.E2BIG} {
		calls := 0
		r, err := openRoot(base, func(int, string, *unix.OpenHow) (int, error) {
			calls++
			return -1, errno
		})
		if r != nil || !errors.Is(err, ErrUnsupported) || !errors.Is(err, errno) {
			t.Errorf("openat2 failing with %v: %v, %v; want ErrUnsupported wrapping it", errno, r, err)
		}
		if calls != 1 {
			t.Errorf("the probe made %d calls, want 1", calls)
		}
	}
	if after := countFDs(); after != before {
		t.Errorf("descriptors: %d before, %d after failed OpenRoot calls", before, after)
	}
	// A working openat2 passes the probe once and the handle is usable.
	calls := 0
	r, err := openRoot(base, func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		calls++
		if p != "." || how.Resolve != resolveFlags {
			t.Errorf("probe on %q with resolve %#x", p, how.Resolve)
		}
		return unix.Openat2(dirfd, p, how)
	})
	if err != nil || calls != 1 {
		t.Fatalf("openRoot: %v after %d calls", err, calls)
	}
	r.Close()
}

func TestClosedRoot(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "f"), "x")
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); !errors.Is(err, ErrClosed) {
		t.Errorf("second Close: %v", err)
	}
	if _, err := r.ReadFile("f", 10); !errors.Is(err, ErrClosed) {
		t.Errorf("ReadFile: %v", err)
	}
	if _, err := r.Lstat("f"); !errors.Is(err, ErrClosed) {
		t.Errorf("Lstat: %v", err)
	}
	if err := r.MkdirAll("x", 0o700); !errors.Is(err, ErrClosed) {
		t.Errorf("MkdirAll: %v", err)
	}
}

// Every openat2 call, whichever method makes it and on whichever path
// (success, failure, the lookups behind a refusal), carries the resolve flags
// and close-on-exec.
func TestEveryOpenCarriesFlags(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "f"), "x")
	write(t, filepath.Join(dir, "d/g"), "x")
	symlink(t, outside, filepath.Join(dir, "l"))
	var bad []string
	n := 0
	r.openat2 = func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		n++
		if how.Flags&unix.O_CLOEXEC == 0 || how.Resolve != resolveFlags {
			bad = append(bad, p)
		}
		return unix.Openat2(dirfd, p, how)
	}
	r.ReadFile("f", 10)
	r.ReadFile("missing", 10)
	r.ReadFileInfo("f", 10, RefuseHardLinks())
	r.Lstat("f")
	r.Lstat("l")
	r.MkdirAll("m/n", 0o755)
	r.MkdirAll("d", 0o755)
	r.WriteFileExclusive("m/n/w", nil, 0o600)
	r.WriteFileExclusive("l", nil, 0o600)
	r.AppendFile("f", []byte("y"))
	r.ReadDirNames("d", 10)
	if n < 12 || len(bad) != 0 {
		t.Fatalf("%d calls seen; without the flags: %v", n, bad)
	}
	for name, f := range map[string]uint64{
		"BENEATH": unix.RESOLVE_BENEATH, "NO_SYMLINKS": unix.RESOLVE_NO_SYMLINKS,
		"NO_MAGICLINKS": unix.RESOLVE_NO_MAGICLINKS, "NO_XDEV": unix.RESOLVE_NO_XDEV,
	} {
		if resolveFlags&f == 0 {
			t.Errorf("resolveFlags %#x lacks RESOLVE_%s", uint64(resolveFlags), name)
		}
	}
}

// A FIFO a lead holds open for reading opens for append; the type check on
// the descriptor refuses it and nothing is written.
func TestAppendFIFOWithReader(t *testing.T) {
	r, dir, _ := newRoot(t)
	p := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
	rd, err := unix.Open(p, unix.O_RDONLY|unix.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(rd)
	if err := r.AppendFile("fifo", []byte("x")); !errors.Is(err, ErrNotRegular) {
		t.Fatalf("AppendFile on a FIFO with a reader: %v, want ErrNotRegular", err)
	}
	if n, _ := unix.Read(rd, make([]byte, 1)); n > 0 {
		t.Fatal("data was written to the FIFO")
	}
}

// A file over the cap is refused from its size, before any byte is read: the
// message names the size, where the read-time check names growth.
func TestReadFileSizeCheckBeforeRead(t *testing.T) {
	r, dir, _ := newRoot(t)
	f, err := os.Create(filepath.Join(dir, "big"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(1 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, err = r.ReadFile("big", 10)
	if !errors.Is(err, ErrTooLarge) || !strings.Contains(err.Error(), "1048576 bytes") {
		t.Fatalf("ReadFile over the cap: %v, want ErrTooLarge naming the size", err)
	}
}

// Close does not wait behind a call that is stuck: it returns, later calls
// are refused, and the descriptor is released when the stuck call ends.
func TestCloseDoesNotWaitForStuckCall(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "f"), "data")
	entered := make(chan struct{})
	release := make(chan struct{})
	r.openat2 = func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		close(entered)
		<-release
		return unix.Openat2(dirfd, p, how)
	}
	fd := r.fd
	type result struct {
		data []byte
		err  error
	}
	res := make(chan result, 1)
	go func() {
		data, err := r.ReadFile("f", 10)
		res <- result{data, err}
	}()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- r.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Errorf("Close with a call in progress: %v", err)
		}
	case <-time.After(20 * time.Second):
		close(release)
		t.Fatal("Close waited behind a stuck call")
	}
	if _, err := r.ReadFile("f", 10); !errors.Is(err, ErrClosed) {
		t.Errorf("call after Close: %v, want ErrClosed", err)
	}
	if err := r.Close(); !errors.Is(err, ErrClosed) {
		t.Errorf("second Close: %v, want ErrClosed", err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
		t.Errorf("descriptor released while a call still uses it: %v", err)
	}
	close(release)
	got := <-res
	if got.err != nil || string(got.data) != "data" {
		t.Errorf("the call in progress: %q, %v", got.data, got.err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != unix.EBADF {
		t.Errorf("descriptor still open after the last call ended: %v", err)
	}
}

// A zero Root is not a handle: every call and Close refuse it with ErrClosed,
// and nothing panics or touches descriptor 0.
func TestZeroRoot(t *testing.T) {
	var r Root
	if _, err := r.ReadFile("f", 1); !errors.Is(err, ErrClosed) {
		t.Errorf("ReadFile: %v", err)
	}
	if _, err := r.ReadFileInfo("f", 1); !errors.Is(err, ErrClosed) {
		t.Errorf("ReadFileInfo: %v", err)
	}
	if _, err := r.Lstat("f"); !errors.Is(err, ErrClosed) {
		t.Errorf("Lstat: %v", err)
	}
	if err := r.MkdirAll("d", 0o755); !errors.Is(err, ErrClosed) {
		t.Errorf("MkdirAll: %v", err)
	}
	if err := r.WriteFileExclusive("f", nil, 0o600); !errors.Is(err, ErrClosed) {
		t.Errorf("WriteFileExclusive: %v", err)
	}
	if err := r.AppendFile("f", nil); !errors.Is(err, ErrClosed) {
		t.Errorf("AppendFile: %v", err)
	}
	if _, err := r.ReadDirNames(".", 1); !errors.Is(err, ErrClosed) {
		t.Errorf("ReadDirNames: %v", err)
	}
	if err := r.Close(); !errors.Is(err, ErrClosed) {
		t.Errorf("Close: %v", err)
	}
	if _, err := unix.FcntlInt(0, unix.F_GETFD, 0); err != nil {
		t.Errorf("descriptor 0 was closed: %v", err)
	}
}

func TestErrorText(t *testing.T) {
	r, dir, _ := newRoot(t)
	symlink(t, "f", filepath.Join(dir, "l"))
	_, err := r.ReadFile("l", 1)
	var pe *Error
	if !errors.As(err, &pe) || pe.Op != "read" || pe.Path != "l" {
		t.Fatalf("error %#v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, `"l"`) || !strings.Contains(msg, "symbolic link") {
		t.Errorf("message %q does not name the path and the reason", msg)
	}
	long := &Error{Op: "read", Path: strings.Repeat("a", 5000), Err: ErrSymlink}
	if len(long.Error()) > 400 {
		t.Errorf("long path not bounded: %d bytes", len(long.Error()))
	}
}
