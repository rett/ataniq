//go:build linux

package safefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// openFDs counts the descriptors this process has open. The tests of this
// package do not run in parallel, so two counts around a block compare.
func openFDs(t *testing.T) int {
	t.Helper()
	ents, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(ents)
}

// devIno reports the device and inode of the entry at p, not following it.
func devIno(t *testing.T, p string) (dev, ino uint64) {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Lstat(p, &st); err != nil {
		t.Fatal(err)
	}
	return uint64(st.Dev), uint64(st.Ino)
}

// fdDevIno reports the device and inode behind a descriptor.
func fdDevIno(t *testing.T, fd int) (dev, ino uint64) {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		t.Fatal(err)
	}
	return uint64(st.Dev), uint64(st.Ino)
}

// wantPathDescriptor checks that fd is an O_PATH descriptor with
// close-on-exec set.
func wantPathDescriptor(t *testing.T, what string, fd int) {
	t.Helper()
	fl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil || fl&unix.O_PATH == 0 {
		t.Errorf("%s: status flags %#x, %v; want O_PATH", what, fl, err)
	}
	fdfl, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil || fdfl&unix.FD_CLOEXEC == 0 {
		t.Errorf("%s: descriptor flags %#x, %v; want close-on-exec", what, fdfl, err)
	}
}

// mkfifo makes a FIFO at p.
func mkfifo(t *testing.T, p string) {
	t.Helper()
	if err := unix.Mkfifo(p, 0o600); err != nil {
		t.Fatal(err)
	}
}

// swapForLink moves the entry at p aside (to p+".old") and puts a symbolic
// link to target at its name: what a lead that can write the folder does
// between two owner-side calls.
func swapForLink(t *testing.T, p, target string) {
	t.Helper()
	if err := os.Rename(p, p+".old"); err != nil {
		t.Fatal(err)
	}
	symlink(t, target, p)
}

// A sub-root is a Root like one from OpenRoot: every method works on it, and
// a path given to it is beneath its own folder.
func TestSubRoot(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "a/b/f.txt"), "deep")
	write(t, filepath.Join(dir, "top"), "top")

	sub, err := r.SubRoot("a/b")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	wantPathDescriptor(t, "sub-root", sub.fd)

	got, err := sub.ReadFile("f.txt", 100)
	if err != nil || string(got) != "deep" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	info, err := sub.Lstat(".")
	if err != nil || !info.IsDir() {
		t.Fatalf("Lstat(.) = %+v, %v", info, err)
	}
	if dev, ino := devIno(t, filepath.Join(dir, "a/b")); info.Dev != dev || info.Ino != ino {
		t.Errorf("the sub-root is %d:%d, the folder asked for is %d:%d", info.Dev, info.Ino, dev, ino)
	}
	if info, err := sub.Lstat("f.txt"); err != nil || !info.IsRegular() {
		t.Errorf("Lstat(f.txt) = %+v, %v", info, err)
	}
	if names, err := sub.ReadDirNames(".", 10); err != nil || !reflect.DeepEqual(names, []string{"f.txt"}) {
		t.Errorf("ReadDirNames = %v, %v", names, err)
	}
	if err := sub.MkdirAll("m/n", 0o755); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "a/b/m/n")); err != nil || !fi.IsDir() {
		t.Errorf("MkdirAll did not make the folder below the sub-root: %v", err)
	}
	if err := sub.WriteFileExclusive("m/n/w", []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := sub.AppendFile("m/n/w", []byte("+more")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "a/b/m/n/w")); err != nil || string(b) != "new+more" {
		t.Errorf("file written through the sub-root: %q, %v", b, err)
	}
	if err := sub.Sync("m/n/w"); err != nil {
		t.Errorf("Sync of a file: %v", err)
	}
	if err := sub.Sync("."); err != nil {
		t.Errorf("Sync of the folder: %v", err)
	}
	if err := sub.RenameNoReplace("f.txt", "g.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a/b/g.txt")); err != nil {
		t.Errorf("RenameNoReplace did not rename in the sub-root's folder: %v", err)
	}

	// A sub-root of a sub-root.
	nested, err := sub.SubRoot("m")
	if err != nil {
		t.Fatal(err)
	}
	defer nested.Close()
	if got, err := nested.ReadFile("n/w", 100); err != nil || string(got) != "new+more" {
		t.Errorf("nested sub-root: %q, %v", got, err)
	}

	// Beneath means beneath the sub-root's folder, not the parent's: the
	// parent's file is not reachable, by "..", by a link, or in the kernel.
	if _, err := sub.ReadFile("../top", 100); !errors.Is(err, ErrEscapes) {
		t.Errorf("ReadFile(../top) through the sub-root: %v, want ErrEscapes", err)
	}
	symlink(t, "../../top", filepath.Join(dir, "a/b/up"))
	if got, err := sub.ReadFile("up", 100); !errors.Is(err, ErrSymlink) {
		t.Errorf("a link to the parent's file: %q, %v; want ErrSymlink", got, err)
	}
	if fd, err := sub.open(sub.fd, "../top", unix.O_PATH, 0); err != unix.EXDEV {
		if err == nil {
			unix.Close(fd)
		}
		t.Errorf("openat2 of ../top from the sub-root's descriptor: %v, want EXDEV", err)
	}

	// "." is a second handle on the same folder.
	same, err := r.SubRoot(".")
	if err != nil {
		t.Fatal(err)
	}
	defer same.Close()
	if got, err := same.ReadFile("top", 100); err != nil || string(got) != "top" {
		t.Errorf("SubRoot(.): %q, %v", got, err)
	}
	if same.fd == r.fd {
		t.Error("SubRoot(.) shares the parent's descriptor")
	}
}

// A sub-root and its parent have separate descriptors: closing one does not
// close the other.
func TestSubRootLifetime(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "d/f"), "inside")
	write(t, filepath.Join(dir, "top"), "top")

	sub, err := r.SubRoot("d")
	if err != nil {
		t.Fatal(err)
	}
	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := sub.ReadFile("f", 10); !errors.Is(err, ErrClosed) {
		t.Errorf("read through a closed sub-root: %v, want ErrClosed", err)
	}
	if _, err := sub.SubRoot("."); !errors.Is(err, ErrClosed) {
		t.Errorf("SubRoot of a closed sub-root: %v, want ErrClosed", err)
	}
	if err := sub.Close(); !errors.Is(err, ErrClosed) {
		t.Errorf("second Close of a sub-root: %v, want ErrClosed", err)
	}
	if got, err := r.ReadFile("top", 10); err != nil || string(got) != "top" {
		t.Errorf("the parent after its sub-root closed: %q, %v", got, err)
	}

	sub, err = r.SubRoot("d")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := sub.ReadFile("f", 10); err != nil || string(got) != "inside" {
		t.Errorf("the sub-root after its parent closed: %q, %v", got, err)
	}

	// A closed root and a zero Root open nothing.
	if s, err := r.SubRoot("d"); s != nil || !errors.Is(err, ErrClosed) {
		t.Errorf("SubRoot of a closed root: %v, %v; want ErrClosed", s, err)
	}
	var zero Root
	if s, err := zero.SubRoot("d"); s != nil || !errors.Is(err, ErrClosed) {
		t.Errorf("SubRoot of a zero Root: %v, %v; want ErrClosed", s, err)
	}
}

// What SubRoot refuses: a link (last or middle component, wherever it
// points), anything that is not a folder, a missing entry, and a path that
// leaves the folder or is not clean.
func TestSubRootRefuses(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "real/inner/f"), "x")
	write(t, filepath.Join(dir, "file"), "x")
	mkfifo(t, filepath.Join(dir, "fifo"))
	symlink(t, "real", filepath.Join(dir, "link-in"))                           // a folder inside the root
	symlink(t, outside, filepath.Join(dir, "link-out"))                         // a folder outside
	symlink(t, filepath.Join(dir, "gone"), filepath.Join(dir, "link-dangling")) // nowhere
	symlink(t, "link-loop", filepath.Join(dir, "link-loop"))                    // itself
	symlink(t, "file", filepath.Join(dir, "link-file"))                         // a file
	symlink(t, "/proc/self/root", filepath.Join(dir, "link-magic"))             // a magic link's target
	symlink(t, "inner", filepath.Join(dir, "real/link-inner"))                  // a link below a real folder

	before := snapshot(t, dir)
	for _, tc := range []struct {
		rel  string
		want error
	}{
		// A link as the last component.
		{"link-in", ErrSymlink},
		{"link-out", ErrSymlink},
		{"link-dangling", ErrSymlink},
		{"link-loop", ErrSymlink},
		{"link-file", ErrSymlink},
		{"link-magic", ErrSymlink},
		{"real/link-inner", ErrSymlink},
		// A link as a middle component.
		{"link-in/inner", ErrSymlink},
		{"link-out/anything", ErrSymlink},
		{"link-magic/etc", ErrSymlink},
		// Not a folder.
		{"file", ErrNotDirectory},
		{"fifo", ErrNotDirectory},
		{"real/inner/f", ErrNotDirectory},
		{"file/x", ErrNotDirectory},
		// Not there.
		{"missing", fs.ErrNotExist},
		{"real/missing", fs.ErrNotExist},
		{"missing/inner", fs.ErrNotExist},
		// Out of the folder, before any system call.
		{"..", ErrEscapes},
		{"../outside", ErrEscapes},
		{"real/..", ErrEscapes},
		{"real/../real", ErrEscapes},
		{"/", ErrEscapes},
		{outside, ErrEscapes},
		{dir, ErrEscapes},
		// Not a clean relative path.
		{"", ErrInvalidArgument},
		{"real/", ErrInvalidArgument},
		{"./real", ErrInvalidArgument},
		{"real//inner", ErrInvalidArgument},
		{"real\x00", ErrInvalidArgument},
	} {
		sub, err := r.SubRoot(tc.rel)
		if sub != nil {
			sub.Close()
		}
		if sub != nil || !errors.Is(err, tc.want) {
			t.Errorf("SubRoot(%q) = %v, %v; want %v", tc.rel, sub, err, tc.want)
		}
		var pe *Error
		if !errors.As(err, &pe) || pe.Op != "subroot" || pe.Path != tc.rel {
			t.Errorf("SubRoot(%q): error %#v, want an *Error of subroot on the path", tc.rel, err)
		}
	}
	if after := snapshot(t, dir); !reflect.DeepEqual(before, after) {
		t.Error("a refused SubRoot changed the folder")
	}
}

// A path that leaves the folder or is not clean is refused before any system
// call.
func TestSubRootRefusesBadPathsWithoutACall(t *testing.T) {
	r, _, outside := newRoot(t)
	calls := 0
	r.openat2 = func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		calls++
		return unix.Openat2(dirfd, p, how)
	}
	for _, rel := range []string{"..", "a/../b", "/", outside, "", "a/", "./a", "a//b", "a\x00"} {
		if sub, err := r.SubRoot(rel); sub != nil || err == nil {
			t.Errorf("SubRoot(%q) = %v, %v; want a refusal", rel, sub, err)
		}
	}
	if calls != 0 {
		t.Errorf("%d openat2 calls for paths that are refused before any", calls)
	}
}

// Each call is a fresh walk from the handle: an entry a lead swaps for a link
// after one call is refused by the next, as the last component and as a
// middle one.
func TestSubRootSwappedBetweenCalls(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "d/inner/f"), "inside")
	write(t, filepath.Join(outside, "inner/f"), "outside")

	for _, rel := range []string{"d", "d/inner"} {
		sub, err := r.SubRoot(rel)
		if err != nil {
			t.Fatalf("first SubRoot(%q): %v", rel, err)
		}
		sub.Close()
	}
	swapForLink(t, filepath.Join(dir, "d"), outside)
	// The name resolves by path, to the outside folder; the handle refuses.
	if _, err := os.Stat(filepath.Join(dir, "d/inner/f")); err != nil {
		t.Fatalf("the swapped path does not resolve by name: %v", err)
	}
	for _, rel := range []string{"d", "d/inner"} {
		sub, err := r.SubRoot(rel)
		if sub != nil {
			sub.Close()
		}
		if sub != nil || !errors.Is(err, ErrSymlink) {
			t.Errorf("second SubRoot(%q) = %v, %v; want ErrSymlink", rel, sub, err)
		}
	}
}

// The sub-root is the folder that was opened, not what its name holds later:
// renamed away and replaced by a link to another folder, it still reads its
// own files, and device and inode are those of the folder that was opened.
func TestSubRootNamesTheOpenedObject(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "d/f"), "inside")
	write(t, filepath.Join(outside, "f"), "outside")
	dev, ino := devIno(t, filepath.Join(dir, "d"))
	outDev, outIno := devIno(t, outside)

	check := func(t *testing.T, sub *Root) {
		t.Helper()
		if got, err := sub.ReadFile("f", 100); err != nil || string(got) != "inside" {
			t.Errorf("read through the sub-root: %q, %v; want the opened folder's file", got, err)
		}
		info, err := sub.Lstat(".")
		if err != nil {
			t.Fatal(err)
		}
		if info.Dev != dev || info.Ino != ino {
			t.Errorf("the sub-root is %d:%d, the folder that was opened is %d:%d", info.Dev, info.Ino, dev, ino)
		}
		if info.Dev == outDev && info.Ino == outIno {
			t.Error("the sub-root is the folder the link points to")
		}
		if d, i := fdDevIno(t, sub.fd); d != dev || i != ino {
			t.Errorf("the sub-root's descriptor is %d:%d, want %d:%d", d, i, dev, ino)
		}
		if d, i := devIno(t, filepath.Join(dir, "d.old")); d != dev || i != ino {
			t.Errorf("the folder moved aside is %d:%d, want %d:%d", d, i, dev, ino)
		}
	}

	t.Run("swapped during the call", func(t *testing.T) {
		// The lead acts right after the lookup, before the type is checked:
		// the check is made on the descriptor, so it is made on the folder
		// that was opened, and that folder is what the call returns.
		afterOpenat2Call(r, 1, func() { swapForLink(t, filepath.Join(dir, "d"), outside) })
		sub, err := r.SubRoot("d")
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		check(t, sub)
	})
	// Put the folder back for the second case.
	if err := os.Remove(filepath.Join(dir, "d")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "d.old"), filepath.Join(dir, "d")); err != nil {
		t.Fatal(err)
	}
	t.Run("swapped after the call", func(t *testing.T) {
		sub, err := r.SubRoot("d")
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Close()
		swapForLink(t, filepath.Join(dir, "d"), outside)
		check(t, sub)
	})
}

// The type is checked on the descriptor with fstat, not left to the open: a
// lookup that hands back something that is not a folder is refused, and the
// descriptor is closed.
func TestSubRootChecksTheDescriptor(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "file"), "x")
	mkfifo(t, filepath.Join(dir, "fifo"))
	symlink(t, "file", filepath.Join(dir, "link"))
	// Stand in for an open that does not enforce O_DIRECTORY: the same
	// lookup, with the flag dropped, and the last component not followed.
	var handed []int
	r.openat2 = func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		loose := *how
		loose.Flags = loose.Flags&^unix.O_DIRECTORY | unix.O_NOFOLLOW
		fd, err := unix.Openat2(dirfd, p, &loose)
		if err == nil {
			handed = append(handed, fd)
		}
		return fd, err
	}
	before := openFDs(t)
	for rel, want := range map[string]error{"file": ErrNotDirectory, "fifo": ErrNotDirectory, "link": ErrSymlink} {
		sub, err := r.SubRoot(rel)
		if sub != nil {
			sub.Close()
		}
		if sub != nil || !errors.Is(err, want) {
			t.Errorf("SubRoot(%q) on a descriptor that is not a folder = %v, %v; want %v", rel, sub, err, want)
		}
	}
	if len(handed) != 3 {
		t.Fatalf("the lookup handed back %d descriptors, want 3", len(handed))
	}
	if after := openFDs(t); after != before {
		t.Errorf("descriptors: %d before, %d after refused SubRoot calls", before, after)
	}
}

// SubRoot makes one lookup, with the resolve flags, O_PATH and close-on-exec,
// when it succeeds and when it is refused.
func TestSubRootIsOneLookup(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "d/e/f"), "x")
	symlink(t, outside, filepath.Join(dir, "l"))
	type call struct {
		dirfd int
		path  string
		how   unix.OpenHow
	}
	var calls []call
	r.openat2 = func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		calls = append(calls, call{dirfd, p, *how})
		return unix.Openat2(dirfd, p, how)
	}
	for _, rel := range []string{"d", "d/e", ".", "l", "l/x", "d/e/f", "missing"} {
		calls = nil
		sub, err := r.SubRoot(rel)
		if err == nil {
			sub.Close()
		}
		if len(calls) != 1 {
			t.Errorf("SubRoot(%q) made %d openat2 calls, want 1", rel, len(calls))
			continue
		}
		c := calls[0]
		if c.dirfd != r.fd || c.path != rel {
			t.Errorf("SubRoot(%q) looked up %q from descriptor %d, want the whole path from the handle (%d)", rel, c.path, c.dirfd, r.fd)
		}
		if c.how.Resolve != resolveFlags {
			t.Errorf("SubRoot(%q): resolve flags %#x, want %#x", rel, c.how.Resolve, uint64(resolveFlags))
		}
		for name, f := range map[string]uint64{"O_PATH": unix.O_PATH, "O_DIRECTORY": unix.O_DIRECTORY, "O_CLOEXEC": unix.O_CLOEXEC} {
			if c.how.Flags&f == 0 {
				t.Errorf("SubRoot(%q): open flags %#x lack %s", rel, c.how.Flags, name)
			}
		}
	}
}

// The no-fallback rule holds: when openat2 is refused, SubRoot is refused
// with ErrUnsupported after that one call, and a sub-root made earlier
// refuses too. The folder and file exist and are reachable by path; nothing
// is opened by path.
func TestSubRootUnsupported(t *testing.T) {
	for _, errno := range []unix.Errno{unix.ENOSYS, unix.EPERM} {
		t.Run(errno.Error(), func(t *testing.T) {
			r, dir, _ := newRoot(t)
			write(t, filepath.Join(dir, "d/f"), "readable")
			denied := false
			calls := 0
			r.openat2 = func(dirfd int, p string, how *unix.OpenHow) (int, error) {
				calls++
				if denied {
					return -1, errno
				}
				return unix.Openat2(dirfd, p, how)
			}
			earlier, err := r.SubRoot("d")
			if err != nil {
				t.Fatal(err)
			}
			defer earlier.Close()

			denied = true
			before := openFDs(t)
			calls = 0
			sub, err := r.SubRoot("d")
			if sub != nil || !errors.Is(err, ErrUnsupported) || !errors.Is(err, errno) {
				t.Errorf("SubRoot = %v, %v; want ErrUnsupported wrapping %v", sub, err, errno)
			}
			if calls != 1 {
				t.Errorf("SubRoot made %d openat2 calls, want 1", calls)
			}
			// The sub-root made earlier calls openat2 the way its parent does.
			calls = 0
			got, err := earlier.ReadFile("f", 100)
			if got != nil || !errors.Is(err, ErrUnsupported) || !errors.Is(err, errno) {
				t.Errorf("read through the earlier sub-root = %q, %v; want ErrUnsupported wrapping %v", got, err, errno)
			}
			if calls != 1 {
				t.Errorf("the earlier sub-root made %d openat2 calls through its parent's call, want 1", calls)
			}
			if after := openFDs(t); after != before {
				t.Errorf("descriptors: %d before, %d after the refused calls", before, after)
			}
		})
	}
}

// A mount point on the way is refused: SubRoot does not relax
// RESOLVE_NO_XDEV, so a sub-root is always on the mount of its parent.
func TestSubRootRefusesMountPoint(t *testing.T) {
	t.Run("EXDEV from the lookup", func(t *testing.T) {
		r, dir, _ := newRoot(t)
		if err := os.Mkdir(filepath.Join(dir, "d"), 0o755); err != nil {
			t.Fatal(err)
		}
		r.openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.EXDEV }
		if sub, err := r.SubRoot("d"); sub != nil || !errors.Is(err, ErrEscapes) || !errors.Is(err, unix.EXDEV) {
			t.Errorf("SubRoot = %v, %v; want ErrEscapes wrapping EXDEV", sub, err)
		}
	})
	t.Run("a real mount point", func(t *testing.T) {
		parent, name := mountPointAbove(t)
		r, err := OpenRoot(parent)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if sub, err := r.SubRoot(name); sub != nil || !errors.Is(err, ErrEscapes) || !errors.Is(err, unix.EXDEV) {
			if sub != nil {
				sub.Close()
			}
			t.Errorf("SubRoot of a mount point = %v, %v; want ErrEscapes wrapping EXDEV", sub, err)
		}
		// The mount's root is opened by name, and is then a root like any other.
		m, err := OpenRoot(filepath.Join(parent, name))
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		same, err := m.SubRoot(".")
		if err != nil {
			t.Fatalf("SubRoot(.) of a root that is a mount's root: %v", err)
		}
		same.Close()
	})
}

// mountPointAbove finds the mount point the temporary folder lies on: the
// first folder, going up from it, whose parent is on another mount. It
// returns that parent and the folder's name in it. It only looks (statx);
// the test that uses it opens both with O_PATH and creates nothing there. It
// skips when the temporary folder is on the root mount or the kernel does not
// report mount identifiers.
func mountPointAbove(t *testing.T) (parent, name string) {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mountID := func(p string) uint64 {
		var stx unix.Statx_t
		if err := unix.Statx(unix.AT_FDCWD, p, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_MNT_ID, &stx); err != nil {
			t.Fatal(err)
		}
		if stx.Mask&unix.STATX_MNT_ID == 0 {
			t.Skip("statx does not report mount identifiers here")
		}
		return stx.Mnt_id
	}
	for {
		up := filepath.Dir(p)
		if up == p {
			t.Skip("the temporary folder is on the root mount: no mount point to cross")
		}
		if mountID(up) != mountID(p) {
			return up, filepath.Base(p)
		}
		p = up
	}
}

// Neither a sub-root that is opened and closed nor a refused SubRoot leaves a
// descriptor open.
func TestSubRootNoDescriptorLeak(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "d/e/f"), "x")
	mkfifo(t, filepath.Join(dir, "fifo"))
	symlink(t, outside, filepath.Join(dir, "l"))
	before := openFDs(t)
	for i := 0; i < 200; i++ {
		for _, rel := range []string{"d", "d/e", "."} {
			sub, err := r.SubRoot(rel)
			if err != nil {
				t.Fatalf("SubRoot(%q): %v", rel, err)
			}
			nested, err := sub.SubRoot(".")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := nested.Lstat("."); err != nil {
				t.Fatal(err)
			}
			if err := nested.Close(); err != nil {
				t.Fatal(err)
			}
			if err := sub.Close(); err != nil {
				t.Fatal(err)
			}
		}
		for _, rel := range []string{"l", "l/x", "d/e/f", "fifo", "missing", "d/missing", "..", "/", ""} {
			if sub, err := r.SubRoot(rel); err == nil {
				sub.Close()
				t.Fatalf("SubRoot(%q) was not refused", rel)
			}
		}
	}
	if after := openFDs(t); after != before {
		t.Errorf("descriptors: %d before, %d after 200 rounds of SubRoot", before, after)
	}
}

// openDir and openFile name the two handle methods for tests that cover both.
func openDir(r *Root, rel string) (*Handle, error)  { return r.OpenDirHandle(rel) }
func openFile(r *Root, rel string) (*Handle, error) { return r.OpenFileHandle(rel) }

// closeHandle closes a handle a test did not expect to get.
func closeHandle(h *Handle) {
	if h != nil {
		h.Close()
	}
}

// handleInfo is Handle.Info that fails the test on an error.
func handleInfo(t *testing.T, h *Handle) Info {
	t.Helper()
	info, err := h.Info()
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// handleFd is Handle.Fd that fails the test on an error.
func handleFd(t *testing.T, h *Handle) int {
	t.Helper()
	fd, err := h.Fd()
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

// A handle is an O_PATH, close-on-exec descriptor on the entry asked for,
// checked to be a folder or a regular file, and its Info is taken on the
// descriptor each time.
func TestHandle(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "d/e/f"), "data")

	d, err := r.OpenDirHandle("d/e")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	info := handleInfo(t, d)
	if !info.IsDir() {
		t.Errorf("folder handle: %+v, want a folder", info)
	}
	if dev, ino := devIno(t, filepath.Join(dir, "d/e")); info.Dev != dev || info.Ino != ino {
		t.Errorf("folder handle is %d:%d, the folder asked for is %d:%d", info.Dev, info.Ino, dev, ino)
	}
	wantPathDescriptor(t, "folder handle", handleFd(t, d))
	if dev, ino := fdDevIno(t, handleFd(t, d)); dev != info.Dev || ino != info.Ino {
		t.Errorf("Fd is %d:%d, Info says %d:%d", dev, ino, info.Dev, info.Ino)
	}

	f, err := r.OpenFileHandle("d/e/f")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info = handleInfo(t, f)
	if !info.IsRegular() || info.Size != 4 || info.Nlink != 1 || info.HardLinked() {
		t.Errorf("file handle: %+v, want a regular file of 4 bytes with one link", info)
	}
	if dev, ino := devIno(t, filepath.Join(dir, "d/e/f")); info.Dev != dev || info.Ino != ino {
		t.Errorf("file handle is %d:%d, the file asked for is %d:%d", info.Dev, info.Ino, dev, ino)
	}
	wantPathDescriptor(t, "file handle", handleFd(t, f))
	// An O_PATH descriptor reads nothing.
	if _, err := unix.Read(handleFd(t, f), make([]byte, 1)); err != unix.EBADF {
		t.Errorf("read on the file handle's descriptor: %v, want EBADF", err)
	}

	// Info is fstat now, not a copy from the open: a later change of mode,
	// size or link count is seen, on the same object.
	p := filepath.Join(dir, "d/e/f")
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("longer data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(p, filepath.Join(dir, "second-name")); err != nil {
		t.Fatal(err)
	}
	later := handleInfo(t, f)
	if later.Mode.Perm() != 0o600 || later.Size != 11 || later.Nlink != 2 || !later.HardLinked() {
		t.Errorf("Info after the file changed: %+v, want mode 0600, 11 bytes, two links", later)
	}
	if later.Dev != info.Dev || later.Ino != info.Ino {
		t.Errorf("Info names %d:%d after the change, %d:%d before", later.Dev, later.Ino, info.Dev, info.Ino)
	}

	// "." hands the root's own folder on as it is; for a file it names
	// nothing a file handle could be.
	self, err := r.OpenDirHandle(".")
	if err != nil {
		t.Fatal(err)
	}
	defer self.Close()
	root, err := r.Lstat(".")
	if err != nil {
		t.Fatal(err)
	}
	if got := handleInfo(t, self); !got.IsDir() || got.Dev != root.Dev || got.Ino != root.Ino {
		t.Errorf("OpenDirHandle(.) is %+v, the root's folder is %+v", got, root)
	}
	if dev, ino := devIno(t, dir); root.Dev != dev || root.Ino != ino {
		t.Errorf("the root's folder is %d:%d, want %d:%d", root.Dev, root.Ino, dev, ino)
	}
	if fd := handleFd(t, self); fd == r.fd {
		t.Error("OpenDirHandle(.) shares the root's descriptor")
	}
	if h, err := r.OpenFileHandle("."); h != nil || !errors.Is(err, ErrInvalidArgument) {
		closeHandle(h)
		t.Errorf("OpenFileHandle(.) = %v, %v; want ErrInvalidArgument", h, err)
	}

	// The handle of a sub-root works the same.
	sub, err := r.SubRoot("d")
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	sf, err := sub.OpenFileHandle("e/f")
	if err != nil {
		t.Fatal(err)
	}
	defer sf.Close()
	if got := handleInfo(t, sf); got.Dev != info.Dev || got.Ino != info.Ino {
		t.Errorf("the file through a sub-root is %d:%d, want %d:%d", got.Dev, got.Ino, info.Dev, info.Ino)
	}
}

// What the handle methods refuse: a link (last or middle component, wherever
// it points), an entry of another type than the one asked for, a missing
// entry, and a path that leaves the folder or is not clean.
func TestHandleRefuses(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "real/inner/f"), "x")
	write(t, filepath.Join(dir, "file"), "x")
	mkfifo(t, filepath.Join(dir, "fifo"))
	symlink(t, "real", filepath.Join(dir, "link-in"))                            // a folder inside the root
	symlink(t, outside, filepath.Join(dir, "link-out"))                          // a folder outside
	symlink(t, "file", filepath.Join(dir, "link-file"))                          // a file inside the root
	symlink(t, filepath.Join(outside, "secret"), filepath.Join(dir, "link-sec")) // a file outside
	symlink(t, filepath.Join(dir, "gone"), filepath.Join(dir, "link-dangling"))  // nowhere
	symlink(t, "link-loop", filepath.Join(dir, "link-loop"))                     // itself
	symlink(t, "/proc/self/exe", filepath.Join(dir, "link-magic"))               // a magic link's target
	symlink(t, "f", filepath.Join(dir, "real/inner/link-f"))                     // a link below real folders

	type refusal struct {
		rel  string
		want error
	}
	both := []refusal{
		// A link as the last component.
		{"link-in", ErrSymlink},
		{"link-out", ErrSymlink},
		{"link-file", ErrSymlink},
		{"link-sec", ErrSymlink},
		{"link-dangling", ErrSymlink},
		{"link-loop", ErrSymlink},
		{"link-magic", ErrSymlink},
		{"real/inner/link-f", ErrSymlink},
		// A link as a middle component.
		{"link-in/inner", ErrSymlink},
		{"link-in/inner/f", ErrSymlink},
		{"link-out/secret", ErrSymlink},
		// A file as a middle component.
		{"file/x", ErrNotDirectory},
		// Not there.
		{"missing", fs.ErrNotExist},
		{"real/missing", fs.ErrNotExist},
		{"missing/inner", fs.ErrNotExist},
		// Out of the folder, before any system call.
		{"..", ErrEscapes},
		{"../outside", ErrEscapes},
		{"../outside/secret", ErrEscapes},
		{"real/..", ErrEscapes},
		{"real/../file", ErrEscapes},
		{"/", ErrEscapes},
		{outside, ErrEscapes},
		{filepath.Join(dir, "file"), ErrEscapes},
		// Not a clean relative path.
		{"", ErrInvalidArgument},
		{"real/", ErrInvalidArgument},
		{"./file", ErrInvalidArgument},
		{"real//inner", ErrInvalidArgument},
		{"file\x00", ErrInvalidArgument},
	}
	before := snapshot(t, dir)
	for _, k := range []struct {
		name  string
		open  func(*Root, string) (*Handle, error)
		wrong []refusal
	}{
		{"OpenDirHandle", openDir, []refusal{
			{"file", ErrNotDirectory}, {"fifo", ErrNotDirectory}, {"real/inner/f", ErrNotDirectory},
		}},
		{"OpenFileHandle", openFile, []refusal{
			{"real", ErrNotRegular}, {"real/inner", ErrNotRegular}, {"fifo", ErrNotRegular},
			{".", ErrInvalidArgument},
		}},
	} {
		for _, tc := range append(append([]refusal{}, both...), k.wrong...) {
			h, err := k.open(r, tc.rel)
			closeHandle(h)
			if h != nil || !errors.Is(err, tc.want) {
				t.Errorf("%s(%q) = %v, %v; want %v", k.name, tc.rel, h, err, tc.want)
			}
			var pe *Error
			if !errors.As(err, &pe) || pe.Op != "handle" || pe.Path != tc.rel {
				t.Errorf("%s(%q): error %#v, want an *Error of handle on the path", k.name, tc.rel, err)
			}
		}
	}
	if after := snapshot(t, dir); !reflect.DeepEqual(before, after) {
		t.Error("a refused handle changed the folder")
	}
}

// A path that leaves the folder or is not clean is refused before any system
// call.
func TestHandleRefusesBadPathsWithoutACall(t *testing.T) {
	r, _, outside := newRoot(t)
	calls := 0
	r.openat2 = func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		calls++
		return unix.Openat2(dirfd, p, how)
	}
	for _, rel := range []string{"..", "a/../b", "/", outside, "", "a/", "./a", "a//b", "a\x00"} {
		for name, open := range map[string]func(*Root, string) (*Handle, error){"OpenDirHandle": openDir, "OpenFileHandle": openFile} {
			if h, err := open(r, rel); h != nil || err == nil {
				closeHandle(h)
				t.Errorf("%s(%q) = %v, %v; want a refusal", name, rel, h, err)
			}
		}
	}
	if h, err := r.OpenFileHandle("."); h != nil || err == nil {
		closeHandle(h)
		t.Errorf("OpenFileHandle(.) = %v, %v; want a refusal", h, err)
	}
	if calls != 0 {
		t.Errorf("%d openat2 calls for paths that are refused before any", calls)
	}
}

// Each call is a fresh walk from the root: an entry a lead swaps for a link
// after one call is refused by the next, as the last component and as a
// middle one.
func TestHandleSwappedBetweenCalls(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "d/inner/f"), "inside")
	write(t, filepath.Join(dir, "state"), "inside")
	write(t, filepath.Join(outside, "inner/f"), "outside")

	opens := []struct {
		name string
		open func(*Root, string) (*Handle, error)
		rel  string
	}{
		{"OpenDirHandle", openDir, "d"},
		{"OpenDirHandle", openDir, "d/inner"},
		{"OpenFileHandle", openFile, "d/inner/f"},
		{"OpenFileHandle", openFile, "state"},
	}
	for _, o := range opens {
		h, err := o.open(r, o.rel)
		if err != nil {
			t.Fatalf("first %s(%q): %v", o.name, o.rel, err)
		}
		h.Close()
	}
	swapForLink(t, filepath.Join(dir, "d"), outside)
	swapForLink(t, filepath.Join(dir, "state"), filepath.Join(outside, "secret"))
	// The names resolve by path, to what the links point at; the root refuses.
	for _, p := range []string{"d/inner/f", "state"} {
		if _, err := os.Stat(filepath.Join(dir, p)); err != nil {
			t.Fatalf("the swapped path %q does not resolve by name: %v", p, err)
		}
	}
	for _, o := range opens {
		h, err := o.open(r, o.rel)
		closeHandle(h)
		if h != nil || !errors.Is(err, ErrSymlink) {
			t.Errorf("second %s(%q) = %v, %v; want ErrSymlink", o.name, o.rel, h, err)
		}
	}
}

// The handle is the object that was opened, not what its name holds later:
// with the entry renamed away and a link to another object put at its name,
// during the call or after it, device and inode stay those of the object that
// was opened, and the descriptor reaches that object.
func TestHandleNamesTheOpenedObject(t *testing.T) {
	for _, tc := range []struct {
		name   string
		open   func(*Root, string) (*Handle, error)
		isDir  bool
		during bool // the swap happens right after the lookup, inside the call
	}{
		{"folder, swapped during the call", openDir, true, true},
		{"folder, swapped after the call", openDir, true, false},
		{"file, swapped during the call", openFile, false, true},
		{"file, swapped after the call", openFile, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, dir, outside := newRoot(t)
			p, target := filepath.Join(dir, "src"), filepath.Join(outside, "secret")
			if tc.isDir {
				write(t, filepath.Join(p, "f"), "inside")
				write(t, filepath.Join(outside, "f"), "outside")
				target = outside
			} else {
				write(t, p, "inside")
			}
			dev, ino := devIno(t, p)
			otherDev, otherIno := devIno(t, target)
			if dev == otherDev && ino == otherIno {
				t.Fatal("the two objects of the test are the same")
			}
			if tc.during {
				// The lead acts right after the lookup, before the type is
				// checked. The check is fstat on the descriptor, so it sees
				// the object that was opened.
				afterOpenat2Call(r, 1, func() { swapForLink(t, p, target) })
			}
			h, err := tc.open(r, "src")
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			if !tc.during {
				swapForLink(t, p, target)
			}
			// By name, "src" now is the other object.
			var st unix.Stat_t
			if err := unix.Stat(p, &st); err != nil || uint64(st.Dev) != otherDev || uint64(st.Ino) != otherIno {
				t.Fatalf("the name does not lead to the other object after the swap: %v", err)
			}
			info := handleInfo(t, h)
			if info.Dev != dev || info.Ino != ino {
				t.Errorf("the handle is %d:%d, the object that was opened is %d:%d", info.Dev, info.Ino, dev, ino)
			}
			if info.IsDir() != tc.isDir || info.IsSymlink() {
				t.Errorf("the handle's type changed: %+v", info)
			}
			fd := handleFd(t, h)
			if d, i := fdDevIno(t, fd); d != dev || i != ino {
				t.Errorf("the descriptor is %d:%d, want %d:%d", d, i, dev, ino)
			}
			if d, i := devIno(t, p+".old"); d != dev || i != ino {
				t.Errorf("the object moved aside is %d:%d, want %d:%d", d, i, dev, ino)
			}
			if tc.isDir {
				// Used as a folder descriptor it reaches the opened folder's file.
				ffd, err := unix.Openat(fd, "f", unix.O_RDONLY|unix.O_CLOEXEC, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer unix.Close(ffd)
				buf := make([]byte, 16)
				n, err := unix.Read(ffd, buf)
				if err != nil || string(buf[:n]) != "inside" {
					t.Errorf("read below the handle's descriptor: %q, %v; want the opened folder's file", buf[:n], err)
				}
			}
		})
	}
}

// For a folder the type is checked on the descriptor with fstat and not left
// to the open: a lookup that hands back something that is not a folder is
// refused, and the descriptor is closed. (For a regular file the open checks
// no type at all, so the refusals of TestHandleRefuses are fstat's.)
func TestHandleChecksTheDescriptor(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "file"), "x")
	mkfifo(t, filepath.Join(dir, "fifo"))
	symlink(t, "file", filepath.Join(dir, "link"))
	handed := 0
	r.openat2 = func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		loose := *how
		loose.Flags = loose.Flags&^unix.O_DIRECTORY | unix.O_NOFOLLOW
		fd, err := unix.Openat2(dirfd, p, &loose)
		if err == nil {
			handed++
		}
		return fd, err
	}
	before := openFDs(t)
	for rel, want := range map[string]error{"file": ErrNotDirectory, "fifo": ErrNotDirectory, "link": ErrSymlink} {
		h, err := r.OpenDirHandle(rel)
		closeHandle(h)
		if h != nil || !errors.Is(err, want) {
			t.Errorf("OpenDirHandle(%q) on a descriptor that is not a folder = %v, %v; want %v", rel, h, err, want)
		}
	}
	if handed != 3 {
		t.Fatalf("the lookup handed back %d descriptors, want 3", handed)
	}
	if after := openFDs(t); after != before {
		t.Errorf("descriptors: %d before, %d after refused OpenDirHandle calls", before, after)
	}
}

// A handle is one lookup of the whole path from the root, with the resolve
// flags, O_PATH and close-on-exec, when it succeeds and when it is refused. A
// folder is opened with O_DIRECTORY, a file with O_NOFOLLOW.
func TestHandleIsOneLookup(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "d/e/f"), "x")
	symlink(t, outside, filepath.Join(dir, "l"))
	type call struct {
		dirfd int
		path  string
		how   unix.OpenHow
	}
	var calls []call
	r.openat2 = func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		calls = append(calls, call{dirfd, p, *how})
		return unix.Openat2(dirfd, p, how)
	}
	for _, k := range []struct {
		name string
		open func(*Root, string) (*Handle, error)
		kind uint64
		rels []string
	}{
		{"OpenDirHandle", openDir, unix.O_DIRECTORY, []string{"d", "d/e", ".", "l", "l/x", "d/e/f", "missing"}},
		{"OpenFileHandle", openFile, unix.O_NOFOLLOW, []string{"d/e/f", "d", "l", "l/secret", "missing"}},
	} {
		for _, rel := range k.rels {
			calls = nil
			h, err := k.open(r, rel)
			if err == nil {
				h.Close()
			}
			if len(calls) != 1 {
				t.Errorf("%s(%q) made %d openat2 calls, want 1", k.name, rel, len(calls))
				continue
			}
			c := calls[0]
			if c.dirfd != r.fd || c.path != rel {
				t.Errorf("%s(%q) looked up %q from descriptor %d, want the whole path from the root (%d)", k.name, rel, c.path, c.dirfd, r.fd)
			}
			if c.how.Resolve != resolveFlags {
				t.Errorf("%s(%q): resolve flags %#x, want %#x", k.name, rel, c.how.Resolve, uint64(resolveFlags))
			}
			if want := uint64(unix.O_PATH|unix.O_CLOEXEC) | k.kind; c.how.Flags != want {
				t.Errorf("%s(%q): open flags %#x, want %#x", k.name, rel, c.how.Flags, want)
			}
		}
	}
}

// The no-fallback rule holds: when openat2 is refused, a handle is refused
// with ErrUnsupported after that one call. The entries exist and are
// reachable by path; nothing is opened by path.
func TestHandleUnsupported(t *testing.T) {
	for _, errno := range []unix.Errno{unix.ENOSYS, unix.EPERM} {
		t.Run(errno.Error(), func(t *testing.T) {
			r, dir, _ := newRoot(t)
			write(t, filepath.Join(dir, "d/f"), "readable")
			calls := 0
			r.openat2 = func(int, string, *unix.OpenHow) (int, error) {
				calls++
				return -1, errno
			}
			before := openFDs(t)
			for _, k := range []struct {
				name string
				open func(*Root, string) (*Handle, error)
				rel  string
			}{
				{"OpenDirHandle", openDir, "d"},
				{"OpenDirHandle", openDir, "."},
				{"OpenFileHandle", openFile, "d/f"},
			} {
				calls = 0
				h, err := k.open(r, k.rel)
				closeHandle(h)
				if h != nil || !errors.Is(err, ErrUnsupported) || !errors.Is(err, errno) {
					t.Errorf("%s(%q) = %v, %v; want ErrUnsupported wrapping %v", k.name, k.rel, h, err, errno)
				}
				if calls != 1 {
					t.Errorf("%s(%q) made %d openat2 calls, want 1", k.name, k.rel, calls)
				}
			}
			if after := openFDs(t); after != before {
				t.Errorf("descriptors: %d before, %d after the refused calls", before, after)
			}
		})
	}
}

// The open of a handle is O_PATH and opens nothing itself: a FIFO with no
// writer does not block it, and the whiteout device, whose ordinary open
// fails with a raw errno, is seen by fstat and refused as not regular.
func TestHandleDoesNotOpenTheFile(t *testing.T) {
	r, dir, _ := newRoot(t)
	mkfifo(t, filepath.Join(dir, "fifo"))
	done := make(chan error, 1)
	go func() {
		h, err := r.OpenFileHandle("fifo")
		closeHandle(h)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Errorf("OpenFileHandle on a FIFO: %v, want ErrNotRegular", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("OpenFileHandle on a FIFO waited for a writer")
	}

	t.Run("whiteout device", func(t *testing.T) {
		p := filepath.Join(dir, "whiteout")
		if err := unix.Mknod(p, unix.S_IFCHR|0o600, 0); err != nil {
			t.Skipf("cannot make the 0:0 whiteout device here: %v", err)
		}
		// An ordinary open of it fails in the open itself, with a raw errno.
		if _, err := r.ReadFile("whiteout", 10); err == nil || errors.Is(err, ErrNotRegular) {
			t.Skipf("the ordinary open of the whiteout device did not fail in the open here: %v", err)
		}
		h, err := r.OpenFileHandle("whiteout")
		closeHandle(h)
		if h != nil || !errors.Is(err, ErrNotRegular) {
			t.Errorf("OpenFileHandle on a device = %v, %v; want ErrNotRegular", h, err)
		}
		h, err = r.OpenDirHandle("whiteout")
		closeHandle(h)
		if h != nil || !errors.Is(err, ErrNotDirectory) {
			t.Errorf("OpenDirHandle on a device = %v, %v; want ErrNotDirectory", h, err)
		}
	})
}

// A closed root and a zero Root open no handle; a closed handle and a zero
// Handle refuse every call; a handle outlives the root it came from.
func TestHandleClosedAndZero(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "d/f"), "x")

	h, err := r.OpenFileHandle("d/f")
	if err != nil {
		t.Fatal(err)
	}
	d, err := r.OpenDirHandle("d")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	fd := handleFd(t, h)
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != unix.EBADF {
		t.Errorf("the descriptor after Close: %v, want EBADF", err)
	}
	if got, err := h.Fd(); got != -1 || !errors.Is(err, ErrClosed) {
		t.Errorf("Fd of a closed handle = %d, %v; want -1, ErrClosed", got, err)
	}
	if _, err := h.Info(); !errors.Is(err, ErrClosed) {
		t.Errorf("Info of a closed handle: %v, want ErrClosed", err)
	}
	if err := h.Close(); !errors.Is(err, ErrClosed) {
		t.Errorf("second Close: %v, want ErrClosed", err)
	}
	// Closing one handle closed nothing else.
	if _, err := d.Info(); err != nil {
		t.Errorf("another handle after the first closed: %v", err)
	}
	if _, err := r.Lstat("d"); err != nil {
		t.Errorf("the root after a handle closed: %v", err)
	}

	// The handle outlives its root.
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if info, err := d.Info(); err != nil || !info.IsDir() {
		t.Errorf("a handle after its root closed: %+v, %v", info, err)
	}
	for name, open := range map[string]func(*Root, string) (*Handle, error){"OpenDirHandle": openDir, "OpenFileHandle": openFile} {
		if h, err := open(r, "d"); h != nil || !errors.Is(err, ErrClosed) {
			closeHandle(h)
			t.Errorf("%s of a closed root = %v, %v; want ErrClosed", name, h, err)
		}
		var zero Root
		if h, err := open(&zero, "d"); h != nil || !errors.Is(err, ErrClosed) {
			closeHandle(h)
			t.Errorf("%s of a zero Root = %v, %v; want ErrClosed", name, h, err)
		}
	}

	// A zero Handle is not a handle, and does not touch descriptor 0.
	var zero Handle
	if got, err := zero.Fd(); got != -1 || !errors.Is(err, ErrClosed) {
		t.Errorf("Fd of a zero Handle = %d, %v; want -1, ErrClosed", got, err)
	}
	if _, err := zero.Info(); !errors.Is(err, ErrClosed) {
		t.Errorf("Info of a zero Handle: %v, want ErrClosed", err)
	}
	if err := zero.Close(); !errors.Is(err, ErrClosed) {
		t.Errorf("Close of a zero Handle: %v, want ErrClosed", err)
	}
	if _, err := unix.FcntlInt(0, unix.F_GETFD, 0); err != nil {
		t.Errorf("descriptor 0 was closed: %v", err)
	}
}

// A mount point on the way is refused for a handle as for every lookup:
// RESOLVE_NO_XDEV is not relaxed.
func TestHandleRefusesMountPoint(t *testing.T) {
	t.Run("EXDEV from the lookup", func(t *testing.T) {
		r, dir, _ := newRoot(t)
		write(t, filepath.Join(dir, "d/f"), "x")
		r.openat2 = func(int, string, *unix.OpenHow) (int, error) { return -1, unix.EXDEV }
		for name, o := range map[string]struct {
			open func(*Root, string) (*Handle, error)
			rel  string
		}{"OpenDirHandle": {openDir, "d"}, "OpenFileHandle": {openFile, "d/f"}} {
			if h, err := o.open(r, o.rel); h != nil || !errors.Is(err, ErrEscapes) || !errors.Is(err, unix.EXDEV) {
				closeHandle(h)
				t.Errorf("%s = %v, %v; want ErrEscapes wrapping EXDEV", name, h, err)
			}
		}
	})
	t.Run("a real mount point", func(t *testing.T) {
		parent, name := mountPointAbove(t)
		r, err := OpenRoot(parent)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		if h, err := r.OpenDirHandle(name); h != nil || !errors.Is(err, ErrEscapes) || !errors.Is(err, unix.EXDEV) {
			closeHandle(h)
			t.Errorf("OpenDirHandle of a mount point = %v, %v; want ErrEscapes wrapping EXDEV", h, err)
		}
		// The mount's root, opened by name, is handed on with ".".
		m, err := OpenRoot(filepath.Join(parent, name))
		if err != nil {
			t.Fatal(err)
		}
		defer m.Close()
		h, err := m.OpenDirHandle(".")
		if err != nil {
			t.Fatalf("OpenDirHandle(.) of a root that is a mount's root: %v", err)
		}
		h.Close()
	})
}

// Neither a handle that is opened and closed nor a refused one leaves a
// descriptor open.
func TestHandleNoDescriptorLeak(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "d/e/f"), "x")
	mkfifo(t, filepath.Join(dir, "fifo"))
	symlink(t, outside, filepath.Join(dir, "l"))
	symlink(t, "d/e/f", filepath.Join(dir, "lf"))
	before := openFDs(t)
	for i := 0; i < 200; i++ {
		for _, o := range []struct {
			open func(*Root, string) (*Handle, error)
			rel  string
		}{{openDir, "d"}, {openDir, "d/e"}, {openDir, "."}, {openFile, "d/e/f"}} {
			h, err := o.open(r, o.rel)
			if err != nil {
				t.Fatalf("handle on %q: %v", o.rel, err)
			}
			if _, err := h.Info(); err != nil {
				t.Fatal(err)
			}
			if _, err := h.Fd(); err != nil {
				t.Fatal(err)
			}
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
		}
		for _, o := range []struct {
			open func(*Root, string) (*Handle, error)
			rel  string
		}{
			{openDir, "l"}, {openDir, "l/x"}, {openDir, "d/e/f"}, {openDir, "fifo"}, {openDir, "missing"},
			{openDir, ".."}, {openDir, "/"}, {openDir, ""},
			{openFile, "lf"}, {openFile, "l/secret"}, {openFile, "d"}, {openFile, "fifo"}, {openFile, "d/missing"},
			{openFile, ".."}, {openFile, "/"}, {openFile, "."},
		} {
			if h, err := o.open(r, o.rel); err == nil {
				h.Close()
				t.Fatalf("handle on %q was not refused", o.rel)
			}
		}
	}
	if after := openFDs(t); after != before {
		t.Errorf("descriptors: %d before, %d after 200 rounds of handles", before, after)
	}
}
