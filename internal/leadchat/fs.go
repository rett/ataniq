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
	// ErrFull: the claim folder holds more entries than a turn-on reads, the
	// sequence numbers, claim ids or generations are spent, or the chat file
	// would pass its bound.
	ErrFull = errors.New("leadchat: too many entries")
	// ErrUnavailable: the outbox cannot be reached or claimed from safely:
	// the kernel has no openat2 with the resolve flags (or a filter denies
	// it), it cannot rename without replacing (the turn-on's probe failed),
	// the outbox, the claim folder and the quarantine are on two file
	// systems, the lead's home is not there, the folder the turn-on made was
	// replaced before it was opened or published, or the reply folder was
	// made by another while chat was turned on. A turn-on is refused.
	ErrUnavailable = errors.New("leadchat: the outbox is unavailable")
	// ErrQuarantined: the lead's quarantine holds [MaxQuarantine] entries
	// set aside, none of them an empty folder, and a turn-on that would set
	// aside one more is refused until the owner purges it
	// ([Store.PurgeQuarantine]).
	ErrQuarantined = errors.New("leadchat: the quarantine is full; purge it before chat is turned on again")
	// ErrSuspended: the lead's chat was stopped by a scan (the outbox was
	// replaced, removed or made open to others, held a folder, a link or a
	// special file, or held too many entries): only a fresh turn-on takes
	// replies again, and the seat reads nothing of it meanwhile
	// ([Store.ReadSeat]); the daemon's own [Store.Read] still answers what
	// it holds.
	ErrSuspended = errors.New("leadchat: chat is stopped for the lead until it is turned on again")
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
// are added) with openat2 beneath dirfd and the resolve flags. There is no
// fallback: where the kernel has no openat2 or a filter denies it, the open
// fails with [ErrUnavailable]. A name is never a path: it holds no slash and
// is not "." or "..", which the callers hold. openat2 may answer EAGAIN when
// a rename races the lookup; that is tried again a few times.
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
		return -1, fmt.Errorf("%w: the kernel cannot open a name beneath a folder without following links", ErrUnavailable)
	}
	return fd, err
}

// privateDirAt opens the folder name below dirfd, making it with mode 0700
// when it is not there, and checks its descriptor: a folder of the caller
// with mode 0700 exactly. A folder it made is made durable in dirfd.
func privateDirAt(call openat2Func, dirfd int, name string) (int, error) {
	created := false
	switch err := unix.Mkdirat(dirfd, name, 0o700); err {
	case nil:
		created = true
	case unix.EEXIST:
	default:
		return -1, fmt.Errorf("leadchat: cannot make a folder: %w", err)
	}
	fd, err := openAt(call, dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	switch {
	case err == unix.ELOOP || err == unix.ENOTDIR || err == unix.EXDEV:
		return -1, fmt.Errorf("%w: a symbolic link, a mount point or not a folder", ErrInsecure)
	case err != nil:
		return -1, err
	}
	if created {
		// The umask may have cleared bits; the folder is 0700 exactly.
		if err := unix.Fchmod(fd, 0o700); err != nil {
			closeFD(fd)
			return -1, fmt.Errorf("leadchat: cannot set the mode of a folder: %w", err)
		}
		if err := syncFD(dirfd); err != nil {
			closeFD(fd)
			return -1, fmt.Errorf("leadchat: cannot sync a folder: %w", err)
		}
	}
	if err := checkDirFD(fd, true); err != nil {
		closeFD(fd)
		return -1, err
	}
	return fd, nil
}

// renameFunc is renameat2; a test replaces it to stand in for a file system
// that cannot rename without replacing.
type renameFunc func(oldDir int, oldName string, newDir int, newName string, flags uint) error

// renameNoReplace renames the one name oldName below oldDir to newName below
// newDir, and fails rather than replace what newName holds. Neither name is
// followed. A kernel, file system or filter that cannot or will not rename
// so (EINVAL, ENOSYS, EOPNOTSUPP, EPERM, EACCES), and two folders on two
// file systems, answer [ErrUnavailable].
func renameNoReplace(call renameFunc, oldDir int, oldName string, newDir int, newName string) error {
	for {
		err := call(oldDir, oldName, newDir, newName, unix.RENAME_NOREPLACE)
		switch err {
		case unix.EINTR:
			continue
		case unix.EINVAL, unix.ENOSYS, unix.EOPNOTSUPP, unix.EPERM, unix.EACCES:
			return fmt.Errorf("%w: the kernel cannot rename without replacing (%v)", ErrUnavailable, err)
		case unix.EXDEV:
			return fmt.Errorf("%w: the outbox, the claim folder and the quarantine are not on one file system", ErrUnavailable)
		}
		return err
	}
}

// unlinkFunc is unlinkat; a test replaces it to make a removal fail.
type unlinkFunc func(dirfd int, name string, flags int) error

// removeTree removes the folder name below dirfd and what it holds, one name
// at a time below descriptors, without following a link or crossing a mount
// point, at most maxTreeDepth deep and *left entries in all: a deeper or
// larger tree, or one that does not stay empty, is left with [ErrFull]. Only
// [Store.PurgeQuarantine] uses it, on what the quarantine holds.
func removeTree(call openat2Func, unlink unlinkFunc, dirfd int, name string, left *int, depth int) error {
	if depth >= maxTreeDepth {
		return fmt.Errorf("%w: a folder set aside is too deep", ErrFull)
	}
	fd, err := openAt(call, dirfd, name, unix.O_RDONLY|unix.O_DIRECTORY, 0)
	switch {
	case err == unix.ENOENT:
		return nil
	case err == unix.ENOTDIR || err == unix.ELOOP:
		return unlink(dirfd, name, 0) // no folder any more
	case err != nil:
		return err
	}
	defer closeFD(fd)
	names, more, err := readNames(fd, *left)
	if err != nil {
		return err
	}
	if more {
		return fmt.Errorf("%w: a folder set aside holds too many entries", ErrFull)
	}
	*left -= len(names)
	for _, n := range names {
		st, err := lstatAt(fd, n)
		switch {
		case err == unix.ENOENT:
			continue
		case err != nil:
			return err
		case st.Mode&unix.S_IFMT == unix.S_IFDIR:
			err = removeTree(call, unlink, fd, n, left, depth+1)
		default:
			err = unlink(fd, n, 0)
		}
		if err != nil && err != unix.ENOENT {
			return err
		}
	}
	switch err := unlink(dirfd, name, unix.AT_REMOVEDIR); err {
	case nil, unix.ENOENT:
		return nil
	case unix.ENOTEMPTY, unix.EEXIST:
		return fmt.Errorf("%w: a folder set aside did not stay empty", ErrFull)
	default:
		return err
	}
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
