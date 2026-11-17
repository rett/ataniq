//go:build linux

package safefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

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
