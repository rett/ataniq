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
	r, err := OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	// Skip, loudly, where the kernel or a filter has no openat2: the
	// package refuses there and there is nothing to test against.
	if err := os.WriteFile(filepath.Join(dir, ".probe"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadFile(".probe", 1); errors.Is(err, ErrUnsupported) {
		t.Skipf("openat2 unavailable here: %v", err)
	}
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
	if err != nil || res.HardLinked || res.Info.Nlink != 1 || string(res.Data) != "data" {
		t.Fatalf("single link: %+v, %v", res, err)
	}
	if err := os.Link(filepath.Join(dir, "one"), filepath.Join(dir, "two")); err != nil {
		t.Skipf("hard links not available here: %v", err)
	}
	res, err = r.ReadFileInfo("two", 100)
	if err != nil || !res.HardLinked || res.Info.Nlink != 2 || string(res.Data) != "data" {
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
		if err == nil || !(errors.Is(err, ErrSymlink) || errors.Is(err, fs.ErrExist)) {
			t.Errorf("WriteFileExclusive(%q): %v, want a refusal", rel, err)
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
	if got, err := r.ReadDirNames(".", 100); err != nil || !reflect.DeepEqual(got, []string{".probe", "d", "empty"}) {
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
