//go:build linux

package safefile

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// resolveFlags is what every lookup below the handle uses. NO_SYMLINKS
// implies NO_MAGICLINKS; both are named so the intent reads from the code.
const resolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS

// maxRelPath bounds a relative path (the kernel's PATH_MAX less the NUL).
const maxRelPath = 4095

// openat2Func is the system call; a test replaces it on its own Root to
// stand in for a kernel without openat2 or a filter that denies it.
type openat2Func func(dirfd int, path string, how *unix.OpenHow) (int, error)

// mkdiratFunc is the mkdirat system call; a test replaces it on its own Root.
type mkdiratFunc func(dirfd int, path string, mode uint32) error

// Root is a folder handle: an O_PATH descriptor opened once by [OpenRoot].
// All access goes through it. A Root is safe for concurrent use; Close waits
// for calls in progress. The caller closes it.
type Root struct {
	mu      sync.RWMutex
	fd      int
	closed  bool
	openat2 openat2Func
	mkdirat mkdiratFunc
}

// OpenRoot opens the folder at path as a handle, with O_PATH, O_DIRECTORY,
// O_NOFOLLOW and O_CLOEXEC. Only the last component is protected by
// O_NOFOLLOW: path must be one the caller owns and trusts, with parents no
// lead can change. A symbolic link at the path is refused with [ErrSymlink]
// and a non-folder with [ErrNotDirectory].
func OpenRoot(dir string) (*Root, error) {
	if dir == "" || strings.IndexByte(dir, 0) >= 0 {
		return nil, &Error{Op: "open", Path: dir, Err: ErrInvalidArgument}
	}
	var fd int
	var err error
	for {
		fd, err = unix.Open(dir, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		if err == unix.ELOOP || err == unix.ENOTDIR {
			// Tell a link from a file for the message only; this lstat
			// decides nothing (the open already refused).
			var st unix.Stat_t
			if unix.Lstat(dir, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK {
				return nil, &Error{Op: "open", Path: dir, Err: refuse(ErrSymlink, err)}
			}
			return nil, &Error{Op: "open", Path: dir, Err: refuse(ErrNotDirectory, err)}
		}
		return nil, &Error{Op: "open", Path: dir, Err: err}
	}
	return &Root{fd: fd, openat2: unix.Openat2, mkdirat: unix.Mkdirat}, nil
}

// Close releases the handle. A second Close returns [ErrClosed].
func (r *Root) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return &Error{Op: "close", Path: "", Err: ErrClosed}
	}
	r.closed = true
	return unix.Close(r.fd)
}

// begin takes the read lock when the handle is open.
func (r *Root) begin(op, rel string) error {
	r.mu.RLock()
	if r.closed {
		r.mu.RUnlock()
		return &Error{Op: op, Path: rel, Err: ErrClosed}
	}
	return nil
}

func (r *Root) end() { r.mu.RUnlock() }

// refuse joins a sentinel with the errno behind it, so both match errors.Is.
func refuse(sentinel error, errno error) error {
	return fmt.Errorf("%w: %w", sentinel, errno)
}

// validate refuses a path before any system call. allowDot lets "." name the
// folder itself.
func validate(rel string, allowDot bool) error {
	if rel == "" {
		return fmt.Errorf("%w: empty path", ErrInvalidArgument)
	}
	if len(rel) > maxRelPath {
		return fmt.Errorf("%w: path longer than %d bytes", ErrInvalidArgument, maxRelPath)
	}
	if strings.IndexByte(rel, 0) >= 0 {
		return fmt.Errorf("%w: path has a NUL byte", ErrInvalidArgument)
	}
	if rel[0] == '/' {
		return fmt.Errorf("%w: absolute path", ErrEscapes)
	}
	for _, c := range strings.Split(rel, "/") {
		if c == ".." {
			return fmt.Errorf("%w: \"..\" component", ErrEscapes)
		}
	}
	if rel == "." {
		if allowDot {
			return nil
		}
		return fmt.Errorf("%w: \".\" names the folder, not an entry", ErrInvalidArgument)
	}
	if path.Clean(rel) != rel {
		return fmt.Errorf("%w: path is not clean", ErrInvalidArgument)
	}
	return nil
}

// permMode converts a mode to creation bits, refusing anything but
// permission bits.
func permMode(mode fs.FileMode) (uint32, error) {
	if mode&^fs.ModePerm != 0 {
		return 0, fmt.Errorf("%w: mode has bits other than permission bits", ErrInvalidArgument)
	}
	return uint32(mode.Perm()), nil
}

// open is the one place a lookup happens: openat2 from dirfd with the
// resolve flags. flags always carry O_CLOEXEC.
func (r *Root) open(dirfd int, rel string, flags int, mode uint32) (int, error) {
	how := unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Mode:    uint64(mode),
		Resolve: resolveFlags,
	}
	for {
		fd, err := r.openat2(dirfd, rel, &how)
		if err == unix.EINTR {
			continue
		}
		return fd, err
	}
}

// classify turns a system error into the package's refusals. EPERM is
// reported as unsupported because a seccomp filter denies a call with it,
// but it is the same value a permission failure gives: the message carries
// the errno and says so, and nothing is retried another way.
func classify(op, rel string, err error) error {
	var sentinel error
	switch err {
	case unix.ENOSYS, unix.EPERM:
		sentinel = ErrUnsupported
	case unix.ELOOP:
		sentinel = ErrSymlink
	case unix.EXDEV:
		sentinel = ErrEscapes
	case unix.EISDIR, unix.ENXIO:
		sentinel = ErrNotRegular
	case unix.ENOTDIR:
		sentinel = ErrNotDirectory
	default:
		return &Error{Op: op, Path: rel, Err: err}
	}
	return &Error{Op: op, Path: rel, Err: refuse(sentinel, err)}
}

func invalid(op, rel string, err error) error {
	return &Error{Op: op, Path: rel, Err: err}
}

// Info is what fstat reports about an entry, taken on the descriptor.
type Info struct {
	Mode  fs.FileMode // type and permission bits, as io/fs defines them
	Size  int64
	Nlink uint64 // hard link count
	UID   uint32
	GID   uint32
	Dev   uint64
	Ino   uint64
}

// Type returns the type bits of the mode (zero for a regular file).
func (i Info) Type() fs.FileMode { return i.Mode & fs.ModeType }

// IsRegular reports a regular file.
func (i Info) IsRegular() bool { return i.Mode&fs.ModeType == 0 }

// IsDir reports a folder.
func (i Info) IsDir() bool { return i.Mode&fs.ModeDir != 0 }

// IsSymlink reports a symbolic link; Lstat reports one as the last
// component without following it.
func (i Info) IsSymlink() bool { return i.Mode&fs.ModeSymlink != 0 }

// HardLinked reports a regular file with more than one hard link.
func (i Info) HardLinked() bool { return i.IsRegular() && i.Nlink > 1 }

func infoFromStat(st *unix.Stat_t) Info {
	m := fs.FileMode(st.Mode & 0o777)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		m |= fs.ModeDir
	case unix.S_IFLNK:
		m |= fs.ModeSymlink
	case unix.S_IFIFO:
		m |= fs.ModeNamedPipe
	case unix.S_IFSOCK:
		m |= fs.ModeSocket
	case unix.S_IFCHR:
		m |= fs.ModeDevice | fs.ModeCharDevice
	case unix.S_IFBLK:
		m |= fs.ModeDevice
	}
	if st.Mode&unix.S_ISUID != 0 {
		m |= fs.ModeSetuid
	}
	if st.Mode&unix.S_ISGID != 0 {
		m |= fs.ModeSetgid
	}
	if st.Mode&unix.S_ISVTX != 0 {
		m |= fs.ModeSticky
	}
	return Info{
		Mode:  m,
		Size:  int64(st.Size),
		Nlink: uint64(st.Nlink),
		UID:   st.Uid,
		GID:   st.Gid,
		Dev:   uint64(st.Dev),
		Ino:   uint64(st.Ino),
	}
}

func fstat(fd int) (Info, error) {
	var st unix.Stat_t
	for {
		err := unix.Fstat(fd, &st)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return Info{}, err
		}
		return infoFromStat(&st), nil
	}
}

// Lstat reports the type and mode of the entry at rel without following it.
// A symbolic link in any parent component is refused with [ErrSymlink]; a
// link as the last component is reported as a link (Info.IsSymlink), so a
// caller checking a mount point can see it. "." names the folder itself.
func (r *Root) Lstat(rel string) (Info, error) {
	const op = "lstat"
	if err := validate(rel, true); err != nil {
		return Info{}, invalid(op, rel, err)
	}
	if err := r.begin(op, rel); err != nil {
		return Info{}, err
	}
	defer r.end()
	// RESOLVE_NO_SYMLINKS with O_PATH|O_NOFOLLOW refuses a link in a parent
	// and yields a descriptor on a link in the last component.
	fd, err := r.open(r.fd, rel, unix.O_PATH|unix.O_NOFOLLOW, 0)
	if err != nil {
		return Info{}, classify(op, rel, err)
	}
	defer unix.Close(fd)
	info, err := fstat(fd)
	if err != nil {
		return Info{}, &Error{Op: op, Path: rel, Err: err}
	}
	return info, nil
}
