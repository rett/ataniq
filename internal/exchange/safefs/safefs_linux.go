//go:build linux

package safefs

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// resolveFlags are the openat2 rules for every name below the root: stay
// beneath the root, follow no symlink and no /proc magic link.
const resolveFlags = unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS

// dirBatch is how many names one directory read returns.
const dirBatch = 256

// Root is a folder opened once and kept open. Every name is resolved from
// the held descriptor, so a swap of the folder's path after [OpenRoot] has no
// effect. A Root is safe for concurrent use; [Root.Close] waits for calls in
// progress.
type Root struct{ f *os.File }

// OpenRoot opens the folder at path. The final component must be a real
// folder: a symlink there is refused with an error that matches [ErrEscape]
// and unix.ELOOP. Trailing slashes are ignored, because a trailing slash
// would make the kernel follow a symlink. The folders above the final
// component belong to the owner and are resolved the ordinary way.
//
// OpenRoot returns [ErrUnsupported] when the kernel has no openat2.
func OpenRoot(path string) (*Root, error) {
	if path == "" {
		return nil, pathError("open", path, unix.ENOENT)
	}
	p := path
	for len(p) > 1 && p[len(p)-1] == '/' {
		p = p[:len(p)-1]
	}
	var fd int
	var err error
	for {
		fd, err = unix.Open(p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		if err == unix.ENOTDIR || err == unix.ELOOP {
			// Only the error label depends on this look: what is opened does not.
			var st unix.Stat_t
			if unix.Lstat(p, &st) == nil && st.Mode&unix.S_IFMT == unix.S_IFLNK {
				err = escapeError{unix.ELOOP}
			}
		}
		return nil, pathError("open", path, err)
	}
	// Probe openat2 now, so that a kernel without it fails here.
	pfd, err := openat2(fd, ".", unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		unix.Close(fd)
		return nil, wrapErrno("open", path, err)
	}
	unix.Close(pfd)
	return &Root{f: os.NewFile(uintptr(fd), path)}, nil
}

// Close releases the folder. It waits for calls in progress. Calls made
// after Close fail with [os.ErrClosed].
func (r *Root) Close() error {
	if r == nil || r.f == nil {
		return os.ErrClosed
	}
	return r.f.Close()
}

// withFd calls fn with the root descriptor, which stays open until fn returns.
func (r *Root) withFd(fn func(fd int) error) error {
	if r == nil || r.f == nil {
		return os.ErrClosed
	}
	rc, err := r.f.SyscallConn()
	if err != nil {
		return err
	}
	var ferr error
	if err := rc.Control(func(fd uintptr) { ferr = fn(int(fd)) }); err != nil {
		// Control fails only when the file is closed.
		return os.ErrClosed
	}
	return ferr
}

// openat2 opens rel below dirfd with the resolve rules above.
func openat2(dirfd int, rel string, flags int) (int, error) {
	how := unix.OpenHow{Flags: uint64(flags | unix.O_CLOEXEC), Resolve: resolveFlags}
	for {
		fd, err := unix.Openat2(dirfd, rel, &how)
		if err != unix.EINTR {
			return fd, err
		}
	}
}

// wrapErrno turns an errno from the kernel into the error of this package.
func wrapErrno(op, rel string, err error) error { return pathError(op, rel, mapErrno(err)) }

// mapErrno maps an openat2 errno: ENOSYS is ErrUnsupported, and ELOOP and
// EXDEV (a symlink, or a name that leaves the root) also match ErrEscape.
func mapErrno(err error) error {
	switch {
	case errors.Is(err, unix.ENOSYS):
		return ErrUnsupported
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.EXDEV):
		return escapeError{err}
	}
	return err
}

// Open opens the regular file rel for reading. The open uses openat2 from the
// root with no symlink followed anywhere in the name, and with O_NONBLOCK, so
// it does not block on a FIFO. The file is then checked with fstat: anything
// but a regular file is closed and refused with [ErrNotRegular].
//
// rel must be relative with no ".." element, else [ErrEscape].
func (r *Root) Open(rel string) (*os.File, error) {
	rel, err := cleanRel("open", rel)
	if err != nil {
		return nil, err
	}
	var file *os.File
	err = r.withFd(func(fd int) error {
		nfd, err := openat2(fd, rel, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY)
		if err != nil {
			return wrapErrno("open", rel, err)
		}
		var st unix.Stat_t
		if err := unix.Fstat(nfd, &st); err != nil {
			unix.Close(nfd)
			return pathError("open", rel, err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFREG {
			unix.Close(nfd)
			return pathError("open", rel, ErrNotRegular)
		}
		file = os.NewFile(uintptr(nfd), rel)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return file, nil
}

// Lstat describes rel without following a symlink, the last component
// included. The parent folder is resolved with the same rules as [Root.Open]
// and the last component is read with fstatat and AT_SYMLINK_NOFOLLOW. The
// name "." describes the root itself. The result's Sys method returns a
// *unix.Stat_t.
func (r *Root) Lstat(rel string) (fs.FileInfo, error) {
	rel, err := cleanRel("lstat", rel)
	if err != nil {
		return nil, err
	}
	var fi fs.FileInfo
	err = r.withFd(func(fd int) error {
		var st unix.Stat_t
		if rel == "." {
			if err := unix.Fstat(fd, &st); err != nil {
				return pathError("lstat", rel, err)
			}
			fi = newFileInfo(".", &st)
			return nil
		}
		dir, base := path.Split(rel)
		parent := fd
		if dir != "" {
			pfd, err := openat2(fd, path.Clean(dir), unix.O_PATH|unix.O_DIRECTORY)
			if err != nil {
				return wrapErrno("lstat", rel, err)
			}
			defer unix.Close(pfd)
			parent = pfd
		}
		if err := fstatat(parent, base, &st); err != nil {
			return pathError("lstat", rel, err)
		}
		fi = newFileInfo(base, &st)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return fi, nil
}

func fstatat(dirfd int, name string, st *unix.Stat_t) error {
	for {
		err := unix.Fstatat(dirfd, name, st, unix.AT_SYMLINK_NOFOLLOW)
		if err != unix.EINTR {
			return err
		}
	}
}

// TreeSize counts the allocated size and the number of names of the tree at
// rel; the name "." is the root itself. The walk goes by descriptors: each
// folder is opened with openat2 from its parent's descriptor, never by path
// and never through a symlink. It counts st_blocks * 512 of every regular
// file and folder. A symlink, FIFO, socket or device counts as one entry of
// size 0 and is never opened. The starting name counts as an entry too.
//
// A file with several hard links is counted once for each name that reaches
// it: the walk does not remember inodes.
//
// The walk does not cross to another filesystem: a name whose st_dev differs
// from the root's is [ErrCrossDevice]. As soon as a limit is passed it stops
// with [ErrLimit]; the returned Size holds what was counted up to then. Folders
// nested deeper than [MaxDepth] are [ErrLimit] too. Both limits must be
// greater than zero. The walk returns the first error it meets, so a tree
// that the lead changes during the walk can fail with ENOENT or ENOTDIR.
func (r *Root) TreeSize(rel string, lim SizeLimits) (Size, error) {
	if lim.MaxBytes <= 0 || lim.MaxEntries <= 0 {
		return Size{}, pathError("treesize", rel, errors.New("safefs: both size limits must be greater than zero"))
	}
	rel, err := cleanRel("treesize", rel)
	if err != nil {
		return Size{}, err
	}
	w := &walker{lim: lim, rel: rel}
	err = r.withFd(func(fd int) error {
		var root unix.Stat_t
		if err := unix.Fstat(fd, &root); err != nil {
			return pathError("treesize", rel, err)
		}
		w.dev = uint64(root.Dev)
		if rel == "." {
			return w.visit(fd, ".", &root, 0)
		}
		dir, base := path.Split(rel)
		parent := fd
		if dir != "" {
			pfd, err := openat2(fd, path.Clean(dir), unix.O_PATH|unix.O_DIRECTORY)
			if err != nil {
				return wrapErrno("treesize", rel, err)
			}
			defer unix.Close(pfd)
			parent = pfd
		}
		var st unix.Stat_t
		if err := fstatat(parent, base, &st); err != nil {
			return pathError("treesize", rel, err)
		}
		return w.visit(parent, base, &st, 0)
	})
	return w.size, err
}

// walker holds the state of one TreeSize walk.
type walker struct {
	lim   SizeLimits
	size  Size
	dev   uint64
	rel   string   // the name the walk started at
	names []string // folders from the start to the current one
}

// errAt wraps err with the name of the entry being visited: the start name
// and the folders below it.
func (w *walker) errAt(err error) error { return w.errChild("", err) }

// errChild wraps err with the name of the entry child inside the folder
// being visited.
func (w *walker) errChild(child string, err error) error {
	var parts []string
	if w.rel != "." {
		parts = append(parts, w.rel)
	}
	parts = append(parts, w.names...)
	if child != "" {
		parts = append(parts, child)
	}
	p := "."
	if len(parts) > 0 {
		p = strings.Join(parts, "/")
	}
	return pathError("treesize", p, err)
}

// visit counts the entry name below parent (st is its fstatat result) and,
// for a folder, walks into it. depth is 0 for the starting entry.
func (w *walker) visit(parent int, name string, st *unix.Stat_t, depth int) error {
	if depth > 0 {
		w.names = append(w.names, name)
		defer func() { w.names = w.names[:len(w.names)-1] }()
	}
	if uint64(st.Dev) != w.dev {
		return w.errAt(ErrCrossDevice)
	}
	kind := st.Mode & unix.S_IFMT
	w.size.Entries++
	if kind == unix.S_IFREG || kind == unix.S_IFDIR {
		w.size.Bytes += int64(st.Blocks) * 512
	}
	if w.size.Entries > w.lim.MaxEntries {
		return w.errAt(fmt.Errorf("%w: more than %d entries", ErrLimit, w.lim.MaxEntries))
	}
	if w.size.Bytes > w.lim.MaxBytes {
		return w.errAt(fmt.Errorf("%w: more than %d bytes", ErrLimit, w.lim.MaxBytes))
	}
	if kind != unix.S_IFDIR {
		return nil
	}
	if depth >= MaxDepth {
		return w.errAt(fmt.Errorf("%w: folders nested deeper than %d", ErrLimit, MaxDepth))
	}
	fd, err := openat2(parent, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return w.errAt(mapErrno(err))
	}
	dir := os.NewFile(uintptr(fd), name)
	defer dir.Close()
	var dst unix.Stat_t
	if err := unix.Fstat(fd, &dst); err != nil {
		return w.errAt(err)
	}
	if uint64(dst.Dev) != w.dev {
		return w.errAt(ErrCrossDevice)
	}
	for {
		names, rerr := dir.Readdirnames(dirBatch)
		for _, n := range names {
			var cst unix.Stat_t
			if err := fstatat(fd, n, &cst); err != nil {
				return w.errChild(n, err)
			}
			if err := w.visit(fd, n, &cst, depth+1); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return w.errAt(rerr)
		}
	}
}

// fileInfo is the fs.FileInfo of an Lstat result.
type fileInfo struct {
	name string
	st   unix.Stat_t
}

func newFileInfo(name string, st *unix.Stat_t) *fileInfo { return &fileInfo{name: name, st: *st} }

func (fi *fileInfo) Name() string { return fi.name }
func (fi *fileInfo) Size() int64  { return int64(fi.st.Size) }
func (fi *fileInfo) ModTime() time.Time {
	return time.Unix(int64(fi.st.Mtim.Sec), int64(fi.st.Mtim.Nsec))
}
func (fi *fileInfo) IsDir() bool { return fi.Mode().IsDir() }
func (fi *fileInfo) Sys() any    { return &fi.st }

func (fi *fileInfo) Mode() fs.FileMode {
	m := fs.FileMode(fi.st.Mode & 0o777)
	switch fi.st.Mode & unix.S_IFMT {
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
	if fi.st.Mode&unix.S_ISUID != 0 {
		m |= fs.ModeSetuid
	}
	if fi.st.Mode&unix.S_ISGID != 0 {
		m |= fs.ModeSetgid
	}
	if fi.st.Mode&unix.S_ISVTX != 0 {
		m |= fs.ModeSticky
	}
	return m
}
