//go:build linux

package safefile

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/sys/unix"
)

// realTemp is t.TempDir() with its links evaluated: OpenRootNoLinks refuses a
// link above the folder, and a temporary folder may lie behind one.
func realTemp(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The duplicate of a handle is the caller's own descriptor on the same
// object: O_PATH, close-on-exec, above the standard streams, and neither end
// closes the other.
func TestHandleDup(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "d/f"), "x")
	for name, o := range map[string]struct {
		open func(*Root, string) (*Handle, error)
		rel  string
	}{"folder": {openDir, "d"}, "file": {openFile, "d/f"}, "the root itself": {openDir, "."}} {
		t.Run(name, func(t *testing.T) {
			h, err := o.open(r, o.rel)
			if err != nil {
				t.Fatal(err)
			}
			own := handleFd(t, h)
			dup, err := h.Dup()
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(dup)
			if dup < 3 || dup == own {
				t.Errorf("the duplicate is descriptor %d (the handle's is %d), want another number from 3 up", dup, own)
			}
			wantPathDescriptor(t, "duplicate", dup)
			wantDev, wantIno := devIno(t, filepath.Join(dir, o.rel))
			if dev, ino := fdDevIno(t, dup); dev != wantDev || ino != wantIno {
				t.Errorf("the duplicate is %d:%d, the entry is %d:%d", dev, ino, wantDev, wantIno)
			}
			// A second duplicate is another number on the same object.
			dup2, err := h.Dup()
			if err != nil {
				t.Fatal(err)
			}
			if dup2 == dup {
				t.Errorf("two duplicates share the number %d", dup)
			}
			unix.Close(dup2)
			// Closing the handle leaves the duplicate, on the same object.
			if err := h.Close(); err != nil {
				t.Fatal(err)
			}
			if dev, ino := fdDevIno(t, dup); dev != wantDev || ino != wantIno {
				t.Errorf("after the handle closed the duplicate is %d:%d, want %d:%d", dev, ino, wantDev, wantIno)
			}
			if got, err := h.Dup(); got != -1 || !errors.Is(err, ErrClosed) {
				t.Errorf("Dup of a closed handle = %d, %v; want -1, ErrClosed", got, err)
			}
		})
	}

	t.Run("closing the duplicate leaves the handle", func(t *testing.T) {
		h, err := r.OpenDirHandle("d")
		if err != nil {
			t.Fatal(err)
		}
		defer h.Close()
		dup, err := h.Dup()
		if err != nil {
			t.Fatal(err)
		}
		unix.Close(dup)
		if info, err := h.Info(); err != nil || !info.IsDir() {
			t.Errorf("the handle after its duplicate closed: %+v, %v", info, err)
		}
	})

	t.Run("a zero handle", func(t *testing.T) {
		var zero Handle
		if got, err := zero.Dup(); got != -1 || !errors.Is(err, ErrClosed) {
			t.Errorf("Dup of a zero Handle = %d, %v; want -1, ErrClosed", got, err)
		}
	})
}

// The duplicate names the object that was opened, whatever happens to the
// name afterwards.
func TestHandleDupNamesTheOpenedObject(t *testing.T) {
	r, dir, outside := newRoot(t)
	write(t, filepath.Join(dir, "d/f"), "x")
	wantDev, wantIno := devIno(t, filepath.Join(dir, "d"))
	h, err := r.OpenDirHandle("d")
	if err != nil {
		t.Fatal(err)
	}
	swapForLink(t, filepath.Join(dir, "d"), outside)
	dup, err := h.Dup()
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(dup)
	h.Close()
	if dev, ino := fdDevIno(t, dup); dev != wantDev || ino != wantIno {
		t.Errorf("the duplicate is %d:%d, the folder that was opened is %d:%d", dev, ino, wantDev, wantIno)
	}
	if dev, ino := devIno(t, outside); dev == wantDev && ino == wantIno {
		t.Fatal("the fixture's outside folder is the folder itself")
	}
}

// A Close in another goroutine cannot hand a caller of Dup a number that
// names something else: Dup either fails with ErrClosed or returns a
// descriptor on the opened object.
func TestHandleDupAgainstAConcurrentClose(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "d/f"), "x")
	wantDev, wantIno := devIno(t, filepath.Join(dir, "d"))
	for round := 0; round < 200; round++ {
		h, err := r.OpenDirHandle("d")
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				fd, err := h.Dup()
				if err != nil {
					if !errors.Is(err, ErrClosed) {
						t.Errorf("Dup: %v", err)
					}
					return
				}
				defer unix.Close(fd)
				var st unix.Stat_t
				if err := unix.Fstat(fd, &st); err != nil || uint64(st.Dev) != wantDev || uint64(st.Ino) != wantIno {
					t.Errorf("a duplicate names %d:%d (%v), want %d:%d", st.Dev, st.Ino, err, wantDev, wantIno)
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			h.Close()
		}()
		close(start)
		wg.Wait()
	}
}

// Neither a duplicate that is made and closed nor a refused Dup leaves a
// descriptor open.
func TestHandleDupNoDescriptorLeak(t *testing.T) {
	r, dir, _ := newRoot(t)
	write(t, filepath.Join(dir, "d/f"), "x")
	before := openFDs(t)
	for i := 0; i < 200; i++ {
		h, err := r.OpenFileHandle("d/f")
		if err != nil {
			t.Fatal(err)
		}
		fd, err := h.Dup()
		if err != nil {
			t.Fatal(err)
		}
		h.Close()
		if _, err := h.Dup(); err == nil {
			t.Fatal("Dup of a closed handle succeeded")
		}
		unix.Close(fd)
	}
	if after := openFDs(t); after != before {
		t.Errorf("descriptors: %d before, %d after 200 duplicates", before, after)
	}
}

// OpenRootNoLinks gives a Root like OpenRoot's, on the folder its path names.
func TestOpenRootNoLinks(t *testing.T) {
	base := realTemp(t)
	write(t, filepath.Join(base, "a/b/f.txt"), "deep")
	r, err := OpenRootNoLinks(filepath.Join(base, "a"))
	if err != nil {
		t.Fatalf("these tests need a working openat2 (OpenRootNoLinks: %v)", err)
	}
	defer r.Close()
	wantPathDescriptor(t, "root", r.fd)
	if dev, ino := devIno(t, filepath.Join(base, "a")); func() bool { d, i := fdDevIno(t, r.fd); return d != dev || i != ino }() {
		t.Errorf("the root is not the folder asked for (%d:%d)", dev, ino)
	}
	if got, err := r.ReadFile("b/f.txt", 100); err != nil || string(got) != "deep" {
		t.Errorf("ReadFile = %q, %v", got, err)
	}
	h, err := r.OpenDirHandle("b")
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	sub, err := r.SubRoot("b")
	if err != nil {
		t.Fatal(err)
	}
	sub.Close()

	// The root of the file system is a folder like any other, and a folder on
	// another mount is reached: mount points above the folder are crossed.
	for _, dir := range []string{"/", "/proc"} {
		fsr, err := OpenRootNoLinks(dir)
		if err != nil {
			t.Errorf("OpenRootNoLinks(%q): %v", dir, err)
			continue
		}
		if info, err := fsr.Lstat("."); err != nil || !info.IsDir() {
			t.Errorf("Lstat(.) of %q = %+v, %v", dir, info, err)
		}
		fsr.Close()
	}
}

// A link on any component of the path is refused, the last included; so are a
// path that is not a folder, a missing one and a path that is not absolute
// and clean. None leaves a descriptor open.
func TestOpenRootNoLinksRefuses(t *testing.T) {
	base := realTemp(t)
	write(t, filepath.Join(base, "real/sub/f"), "x")
	symlink(t, filepath.Join(base, "real"), filepath.Join(base, "abs-link"))
	symlink(t, "real", filepath.Join(base, "rel-link"))
	symlink(t, "nowhere", filepath.Join(base, "dangling"))
	mkfifo(t, filepath.Join(base, "fifo"))
	before := openFDs(t)
	for _, c := range []struct {
		name string
		dir  string
		want error
	}{
		{"the last component is a link to a folder", filepath.Join(base, "abs-link"), ErrSymlink},
		{"the last component is a relative link", filepath.Join(base, "rel-link"), ErrSymlink},
		{"the last component is a dangling link", filepath.Join(base, "dangling"), ErrSymlink},
		{"a middle component is a link", filepath.Join(base, "abs-link", "sub"), ErrSymlink},
		{"a magic link on the way", "/proc/self/root", ErrSymlink},
		{"a regular file", filepath.Join(base, "real/sub/f"), ErrNotDirectory},
		{"below a regular file", filepath.Join(base, "real/sub/f/x"), ErrNotDirectory},
		{"a FIFO", filepath.Join(base, "fifo"), ErrNotDirectory},
		{"missing", filepath.Join(base, "none"), fs.ErrNotExist},
		{"below something missing", filepath.Join(base, "none/sub"), fs.ErrNotExist},
	} {
		r, err := OpenRootNoLinks(c.dir)
		if r != nil {
			r.Close()
		}
		if r != nil || !errors.Is(err, c.want) {
			t.Errorf("%s: %v, %v; want %v", c.name, r, err, c.want)
		}
	}
	if after := openFDs(t); after != before {
		t.Errorf("descriptors: %d before, %d after refused opens", before, after)
	}

	// A path that is not absolute and clean is refused before any call.
	for _, dir := range []string{"", "relative", "./x", base + "/", base + "/real/../real", base + "//real", base + "/./real", "/a\x00b"} {
		calls := 0
		r, err := openRootNoLinks(dir, func(int, string, *unix.OpenHow) (int, error) { calls++; return -1, unix.ENOSYS })
		if r != nil || !errors.Is(err, ErrInvalidArgument) || calls != 0 {
			t.Errorf("%q: %v, %v after %d calls; want ErrInvalidArgument and no call", dir, r, err, calls)
		}
	}
}

// The folder is looked up once, from the root of the file system, with the
// flags that follow no link; the only other call is the probe.
func TestOpenRootNoLinksIsOneLookup(t *testing.T) {
	base := realTemp(t)
	write(t, filepath.Join(base, "a/f"), "x")
	type call struct {
		path string
		how  unix.OpenHow
	}
	var calls []call
	r, err := openRootNoLinks(filepath.Join(base, "a"), func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		calls = append(calls, call{p, *how})
		return unix.Openat2(dirfd, p, how)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(calls) != 2 {
		t.Fatalf("%d openat2 calls, want the lookup and the probe: %+v", len(calls), calls)
	}
	if want := filepath.Join(base, "a")[1:]; calls[0].path != want {
		t.Errorf("the lookup was of %q, want the whole path %q from the root of the file system", calls[0].path, want)
	}
	if calls[0].how.Resolve != rootResolveFlags || calls[0].how.Resolve&unix.RESOLVE_NO_SYMLINKS == 0 {
		t.Errorf("resolve flags of the lookup %#x, want %#x", calls[0].how.Resolve, uint64(rootResolveFlags))
	}
	if want := uint64(unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC); calls[0].how.Flags != want {
		t.Errorf("open flags of the lookup %#x, want %#x", calls[0].how.Flags, want)
	}
	if calls[1].path != "." || calls[1].how.Resolve != resolveFlags {
		t.Errorf("the probe was %q with resolve %#x", calls[1].path, calls[1].how.Resolve)
	}
}

// The handle is the folder the lookup found: putting a link at its name
// afterwards changes nothing for it, and a fresh open then refuses the link.
func TestOpenRootNoLinksNamesTheOpenedObject(t *testing.T) {
	base := realTemp(t)
	outside := filepath.Join(base, "outside")
	write(t, filepath.Join(base, "state/f"), "inside")
	write(t, filepath.Join(outside, "f"), "outside")
	r, err := OpenRootNoLinks(filepath.Join(base, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	swapForLink(t, filepath.Join(base, "state"), outside)
	if got, err := r.ReadFile("f", 100); err != nil || string(got) != "inside" {
		t.Errorf("after the swap the handle reads %q, %v; want the folder that was opened", got, err)
	}
	if again, err := OpenRootNoLinks(filepath.Join(base, "state")); again != nil || !errors.Is(err, ErrSymlink) {
		if again != nil {
			again.Close()
		}
		t.Errorf("a fresh open of the name that is now a link: %v, %v; want ErrSymlink", again, err)
	}
	// OpenRoot protects the last component only: a link above it is followed.
	// That is the difference the function is for.
	if err := os.Mkdir(filepath.Join(outside, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if by, err := OpenRoot(filepath.Join(base, "state", "sub")); err != nil {
		t.Errorf("OpenRoot through a link above the folder: %v (it follows one)", err)
	} else {
		by.Close()
	}
	if strict, err := OpenRootNoLinks(filepath.Join(base, "state", "sub")); strict != nil || !errors.Is(err, ErrSymlink) {
		if strict != nil {
			strict.Close()
		}
		t.Errorf("OpenRootNoLinks through a link above the folder: %v, %v; want ErrSymlink", strict, err)
	}
}

// The no-fallback rule holds: when openat2 is refused the open fails with
// ErrUnsupported, in the lookup or in the probe, and leaves nothing open.
func TestOpenRootNoLinksUnsupported(t *testing.T) {
	base := realTemp(t)
	before := openFDs(t)
	for _, errno := range []unix.Errno{unix.ENOSYS, unix.EPERM} {
		calls := 0
		r, err := openRootNoLinks(base, func(int, string, *unix.OpenHow) (int, error) { calls++; return -1, errno })
		if r != nil || !errors.Is(err, ErrUnsupported) || !errors.Is(err, errno) || calls != 1 {
			t.Errorf("the lookup failing with %v: %v, %v after %d calls; want ErrUnsupported after one", errno, r, err, calls)
		}
		// The lookup works and the probe does not (flags that are refused).
		calls = 0
		r, err = openRootNoLinks(base, func(dirfd int, p string, how *unix.OpenHow) (int, error) {
			calls++
			if calls == 1 {
				return unix.Openat2(dirfd, p, how)
			}
			return -1, errno
		})
		if r != nil || !errors.Is(err, ErrUnsupported) || calls != 2 {
			t.Errorf("the probe failing with %v: %v, %v after %d calls; want ErrUnsupported", errno, r, err, calls)
		}
	}
	if after := openFDs(t); after != before {
		t.Errorf("descriptors: %d before, %d after refused opens", before, after)
	}
}
