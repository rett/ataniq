//go:build linux && ataniq_canary

package leadchat

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// Errors. Each is wrapped, so test with errors.Is.
var (
	// ErrInsecure: a folder or a file is a link, is not a folder or a
	// regular file where one is needed, is not the caller's, has the wrong
	// mode or more than one link, or lies across a mount point.
	ErrInsecure = errors.New("leadchat: a folder or file is not private")
	// ErrLocked: another holder has the lead's chat file.
	ErrLocked = errors.New("leadchat: the chat file is locked by another holder")
	// ErrCorrupt: the lead's chat file is damaged; nothing was repaired.
	ErrCorrupt = errors.New("leadchat: the chat file is corrupt")
	// ErrBroken: a write or a sync of the lead's chat file failed, so what
	// reached the disk is unknown; the store takes nothing more.
	ErrBroken = errors.New("leadchat: the chat file cannot be written")
	// ErrClosed: the store or the root was closed.
	ErrClosed = errors.New("leadchat: closed")
	// ErrOff: the lead's chat switch is off; nothing is read.
	ErrOff = errors.New("leadchat: chat is off for the lead")
	// ErrInvalid: an argument is not valid (a role that is not a layout
	// name, a switch that is not off or read, a negative cursor).
	ErrInvalid = errors.New("leadchat: invalid argument")
	// ErrFull: the outbox holds more entries than one clear reads, or the
	// sequence numbers are spent.
	ErrFull = errors.New("leadchat: too many entries")
)

// resolveFlags is what every openat2 lookup below a folder descriptor uses:
// beneath the descriptor, no symbolic or magic link on any component, and no
// mount point crossed.
const resolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV

// openat2Func is the system call; a test replaces it to stand in for a kernel
// without openat2.
type openat2Func func(dirfd int, path string, how *unix.OpenHow) (int, error)

// Root is the daemon's state root, opened once by [OpenRoot] and held as a
// descriptor. Every outbox and chat file is reached from it by descriptor,
// one name at a time; no path below it is opened by name. The caller closes
// it after the stores that use it.
type Root struct {
	mu      sync.Mutex
	fd      int
	closed  bool
	openat2 openat2Func
}

// OpenRoot opens the state root dir as a folder descriptor, without following
// a symbolic link at its last element, and checks the descriptor: a folder of
// the caller that neither group nor other may write. dir is the daemon's own
// state root, which the caller has verified; the folders above it are trusted
// as they are by the rest of the daemon.
func OpenRoot(dir string) (*Root, error) {
	fd, err := openRetry(func() (int, error) {
		return unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	})
	switch {
	case err == unix.ELOOP || err == unix.ENOTDIR:
		return nil, fmt.Errorf("%w: the state root is a symbolic link or not a folder", ErrInsecure)
	case err != nil:
		return nil, fmt.Errorf("leadchat: cannot open the state root: %w", err)
	}
	if err := checkDirFD(fd, false); err != nil {
		closeFD(fd)
		return nil, fmt.Errorf("the state root: %w", err)
	}
	return &Root{fd: fd, openat2: unix.Openat2}, nil
}

// Close releases the descriptor. A second Close returns [ErrClosed].
func (r *Root) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrClosed
	}
	r.closed = true
	return unix.Close(r.fd)
}

// dup returns a descriptor of its own on the root, for one walk, so a Close
// during the walk cannot pull the descriptor from under it.
func (r *Root) dup() (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return -1, ErrClosed
	}
	fd, err := unix.FcntlInt(uintptr(r.fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return -1, fmt.Errorf("leadchat: cannot hold the state root: %w", err)
	}
	return fd, nil
}

func closeFD(fd int) { _ = unix.Close(fd) }

func syncFD(fd int) error {
	for {
		err := unix.Fsync(fd)
		if err != unix.EINTR {
			return err
		}
	}
}

// openRetry runs an open again on EINTR.
func openRetry(open func() (int, error)) (int, error) {
	for {
		fd, err := open()
		if err != unix.EINTR {
			return fd, err
		}
	}
}

// openAt opens the one name below dirfd with flags (O_CLOEXEC and O_NOFOLLOW
// are added): with openat2 beneath dirfd and the resolve flags, and, where
// the kernel has no openat2 or a filter denies it, with openat, which on a
// single name with O_NOFOLLOW refuses a link the same way. A name is never a
// path: it holds no slash and is not "." or "..", which the callers hold.
// openat2 may answer EAGAIN when a rename races the lookup; that is tried
// again a few times.
func openAt(call openat2Func, dirfd int, name string, flags int, mode uint32) (int, error) {
	flags |= unix.O_CLOEXEC | unix.O_NOFOLLOW
	how := unix.OpenHow{Flags: uint64(flags), Mode: uint64(mode), Resolve: resolveFlags}
	var fd int
	var err error
	for try := 0; try < 4; try++ {
		fd, err = openRetry(func() (int, error) { return call(dirfd, name, &how) })
		if err != unix.EAGAIN || flags&unix.O_NONBLOCK != 0 {
			break
		}
	}
	if err == unix.ENOSYS || err == unix.EPERM {
		return openRetry(func() (int, error) { return unix.Openat(dirfd, name, flags, mode) })
	}
	return fd, err
}

// openDirAt opens the folder name below dirfd and checks its descriptor: a
// folder of the caller. exact asks for mode 0700 exactly; otherwise group
// and other may not write it.
func openDirAt(call openat2Func, dirfd int, name string, exact bool) (int, error) {
	fd, err := openAt(call, dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	switch {
	case err == unix.ELOOP || err == unix.ENOTDIR || err == unix.EXDEV:
		return -1, fmt.Errorf("%w: a symbolic link, a mount point or not a folder", ErrInsecure)
	case err != nil:
		return -1, err
	}
	if err := checkDirFD(fd, exact); err != nil {
		closeFD(fd)
		return -1, err
	}
	return fd, nil
}

// checkDirFD judges a folder by its descriptor.
func checkDirFD(fd int, exact bool) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("leadchat: cannot examine a folder: %w", err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFDIR:
		return fmt.Errorf("%w: not a folder", ErrInsecure)
	case int(st.Uid) != unix.Geteuid():
		return fmt.Errorf("%w: a folder is not owned by the caller", ErrInsecure)
	case exact && st.Mode&07777 != 0o700:
		return fmt.Errorf("%w: a folder has mode %04o, want 0700", ErrInsecure, st.Mode&07777)
	case !exact && st.Mode&0o022 != 0:
		return fmt.Errorf("%w: a folder may be written by group or other (mode %04o)", ErrInsecure, st.Mode&07777)
	}
	return nil
}

// walk opens the folders names below the root, one name at a time, each
// checked by [openDirAt], and returns the descriptor of the last. Every
// descriptor on the way is closed. A missing step is returned as ENOENT.
func (r *Root) walk(names ...string) (int, error) {
	fd, err := r.dup()
	if err != nil {
		return -1, err
	}
	for _, name := range names {
		next, err := openDirAt(r.openat2, fd, name, false)
		closeFD(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// readNames lists at most max names of the folder (without "." and ".."), in
// the order the folder gives them, and reports whether it holds more. The
// names are the lead's: untrusted text, never shown.
func readNames(dirfd int, max int) ([]string, bool, error) {
	fd, err := openRetry(func() (int, error) {
		return unix.Openat(dirfd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	})
	if err != nil {
		return nil, false, fmt.Errorf("leadchat: cannot list a folder: %w", err)
	}
	defer closeFD(fd)
	names := []string{}
	buf := make([]byte, 8192)
	for {
		n, err := unix.ReadDirent(fd, buf)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return nil, false, fmt.Errorf("leadchat: cannot list a folder: %w", err)
		}
		if n <= 0 {
			return names, false, nil
		}
		_, _, names = unix.ParseDirent(buf[:n], max+1-len(names), names)
		if len(names) > max {
			return names[:max], true, nil
		}
	}
}

// lstatAt reports the entry name of dirfd without following it.
func lstatAt(dirfd int, name string) (unix.Stat_t, error) {
	var st unix.Stat_t
	for {
		err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW)
		if err != unix.EINTR {
			return st, err
		}
	}
}

func fstatFD(fd int) (unix.Stat_t, error) {
	var st unix.Stat_t
	for {
		err := unix.Fstat(fd, &st)
		if err != unix.EINTR {
			return st, err
		}
	}
}

func ctimeOf(st *unix.Stat_t) time.Time { return time.Unix(st.Ctim.Sec, st.Ctim.Nsec) }

// sameFile reports whether two stats are of one object in one state: the same
// device and inode, size, link count, change time and modification time.
func sameFile(a, b *unix.Stat_t) bool {
	return a.Dev == b.Dev && a.Ino == b.Ino && a.Size == b.Size && a.Nlink == b.Nlink &&
		a.Ctim == b.Ctim && a.Mtim == b.Mtim && a.Mode == b.Mode && a.Uid == b.Uid
}

func sortedCopy(names []string) []string {
	out := append([]string(nil), names...)
	sort.Strings(out)
	return out
}
