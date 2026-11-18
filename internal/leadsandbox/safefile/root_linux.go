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
// NO_XDEV refuses to cross a mount point, bind mounts included, on any
// component: a mount below the handle's folder is refused (EXDEV, reported as
// [ErrEscapes]) and not read across. The handle's own folder may be a mount
// root or on any file system; only a crossing below it is refused.
const resolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_XDEV

// maxRelPath bounds a relative path (the kernel's PATH_MAX less the NUL).
const maxRelPath = 4095

// openat2Func is the system call; a test replaces it on its own Root to
// stand in for a kernel without openat2 or a filter that denies it.
type openat2Func func(dirfd int, path string, how *unix.OpenHow) (int, error)

// mkdiratFunc is the mkdirat system call; a test replaces it on its own Root.
type mkdiratFunc func(dirfd int, path string, mode uint32) error

// Root is a folder handle: an O_PATH descriptor opened once by [OpenRoot] or
// [OpenRootNoLinks], or beneath another Root by [Root.SubRoot]. All access
// goes through it, and only a Root from one of them works: the zero value
// refuses every call (and Close) with [ErrClosed]. A Root is safe for
// concurrent use. The caller closes it; Close does not wait for calls in
// progress (see [Root.Close]).
type Root struct {
	mu      sync.Mutex
	fd      int
	opened  bool // set by OpenRoot, OpenRootNoLinks and SubRoot; false for the zero value
	closed  bool
	active  int // calls in progress; the descriptor stays open until they end
	openat2 openat2Func
	mkdirat mkdiratFunc
}

// OpenRoot opens the folder at path as a handle, with O_PATH, O_DIRECTORY,
// O_NOFOLLOW and O_CLOEXEC. Only the last component is protected by
// O_NOFOLLOW: path must be one the caller owns and trusts, with parents no
// lead can change. A symbolic link at the path is refused with [ErrSymlink]
// and a non-folder with [ErrNotDirectory].
//
// It then probes openat2 once, on the handle, with the flags every later call
// uses. When the call does not work (the kernel lacks it, a filter denies it,
// or the flags are not accepted) OpenRoot returns [ErrUnsupported] wrapping
// the errno, so a start fails before any work is done. There is no fallback to
// a path-based open.
func OpenRoot(dir string) (*Root, error) {
	return openRoot(dir, unix.Openat2)
}

// openRoot is OpenRoot with the openat2 call given, so a test can stand in
// for a kernel without it.
func openRoot(dir string, call openat2Func) (*Root, error) {
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
	r := &Root{fd: fd, opened: true, openat2: call, mkdirat: unix.Mkdirat}
	probe, err := r.open(fd, ".", unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		unix.Close(fd)
		return nil, &Error{Op: "open", Path: dir, Err: refuse(ErrUnsupported, err)}
	}
	unix.Close(probe)
	return r, nil
}

// rootResolveFlags is what the one lookup of [OpenRootNoLinks] uses, from the
// root of the file system: no symbolic link and no magic link on any
// component. Mount points are crossed on purpose: the folder may be on any
// mount, and the folders above it are the caller's.
const rootResolveFlags = unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS

// OpenRootNoLinks opens the folder at dir as a handle, like [OpenRoot], and
// follows no symbolic link in any component of dir, the last included: it
// opens the root of the file system and looks dir up from there in one
// openat2 with RESOLVE_NO_SYMLINKS and RESOLVE_NO_MAGICLINKS, O_PATH and
// O_DIRECTORY, and the handle is the descriptor that lookup returned. So the
// folder that was found without following a link is the folder every later
// call is beneath; no second open by name lies in between. It is for a caller
// that is given a path it expects to be a real path (a folder the walk of a
// bind source starts from) and must not be sent elsewhere by a link above it.
//
// dir must be absolute and clean ("/" is accepted); anything else is refused
// with [ErrInvalidArgument] before any system call. A link on the way is
// refused with [ErrSymlink], a component that is not a folder (dir itself
// included) with [ErrNotDirectory], and a missing one with an error that
// matches fs.ErrNotExist. Mount points on the way are crossed, unlike in a
// lookup below a handle: dir may lie on any mount. When openat2 does not work
// the error is [ErrUnsupported], as for OpenRoot; there is no fallback to a
// path-based open. The folders above dir can still be renamed by whoever can
// write them: the call refuses a link, it does not make a path trusted.
func OpenRootNoLinks(dir string) (*Root, error) {
	return openRootNoLinks(dir, unix.Openat2)
}

// openRootNoLinks is OpenRootNoLinks with the openat2 call given.
func openRootNoLinks(dir string, call openat2Func) (*Root, error) {
	const op = "open"
	if dir == "" || dir[0] != '/' || strings.IndexByte(dir, 0) >= 0 || len(dir) > maxRelPath || path.Clean(dir) != dir {
		return nil, &Error{Op: op, Path: dir, Err: fmt.Errorf("%w: the folder must be an absolute, clean path", ErrInvalidArgument)}
	}
	var fsRoot int
	var err error
	for {
		fsRoot, err = unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		return nil, &Error{Op: op, Path: dir, Err: fmt.Errorf("the root of the file system: %w", err)}
	}
	defer unix.Close(fsRoot)
	rel := strings.TrimPrefix(dir, "/")
	if rel == "" {
		rel = "."
	}
	how := unix.OpenHow{Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC, Resolve: rootResolveFlags}
	var fd int
	for {
		fd, err = call(fsRoot, rel, &how)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		return nil, classify(op, dir, err)
	}
	// The open asked for a folder (O_DIRECTORY); the type is read from the
	// descriptor as well, so the check does not rest on the open alone.
	info, err := fstat(fd)
	if err == nil {
		err = checkDir(info)
	}
	if err != nil {
		unix.Close(fd)
		return nil, &Error{Op: op, Path: dir, Err: err}
	}
	r := &Root{fd: fd, opened: true, openat2: call, mkdirat: unix.Mkdirat}
	probe, err := r.open(fd, ".", unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		unix.Close(fd)
		return nil, &Error{Op: op, Path: dir, Err: refuse(ErrUnsupported, err)}
	}
	unix.Close(probe)
	return r, nil
}

// Close releases the handle and does not wait for a call in progress, so a
// call that is stuck cannot hold Close (and its caller) with it. Every call
// that starts after Close returns [ErrClosed]; a call already running
// finishes with the descriptor it has, and the descriptor is closed when the
// last of them ends (at once when none is running). A second Close, and
// Close on a zero Root, return [ErrClosed]. The error of the underlying
// close is returned only when the descriptor is closed by this call.
func (r *Root) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.opened || r.closed {
		return &Error{Op: "close", Path: "", Err: ErrClosed}
	}
	r.closed = true
	if r.active == 0 {
		return unix.Close(r.fd)
	}
	return nil
}

// begin registers a call when the handle is open.
func (r *Root) begin(op, rel string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.opened || r.closed {
		return &Error{Op: op, Path: rel, Err: ErrClosed}
	}
	r.active++
	return nil
}

// end unregisters a call; the last call out of a closed handle closes the
// descriptor.
func (r *Root) end() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active--
	if r.closed && r.active == 0 {
		unix.Close(r.fd)
	}
}

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
