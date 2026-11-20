//go:build linux

package safefile

import (
	"fmt"
	"sync"

	"golang.org/x/sys/unix"
)

// openPath is the one lookup behind [Root.SubRoot], [Root.OpenDirHandle] and
// [Root.OpenFileHandle]: a single openat2 from the handle with O_PATH and
// flags, then fstat on the descriptor it returned and check on what fstat
// reported. The type is decided on that descriptor and never on the name, so
// the object checked is the object the caller gets. The caller owns the
// descriptor returned with a nil error; on any error none is left open.
func (r *Root) openPath(op, rel string, flags int, check func(Info) error) (int, error) {
	fd, err := r.open(r.fd, rel, unix.O_PATH|flags, 0)
	if err != nil {
		return -1, classify(op, rel, err)
	}
	info, err := fstat(fd)
	if err == nil {
		err = check(info)
	}
	if err != nil {
		unix.Close(fd)
		return -1, &Error{Op: op, Path: rel, Err: err}
	}
	return fd, nil
}

// dirFlags open a folder to keep: O_DIRECTORY and no O_NOFOLLOW, so the
// kernel refuses a non-folder itself and the resolve flags refuse a link as
// the last component with ELOOP (with O_NOFOLLOW it would be ENOTDIR).
// [checkDir] decides too, so the check does not rest on the open alone.
const dirFlags = unix.O_DIRECTORY

// fileFlags open a regular file to keep: O_NOFOLLOW, which with O_PATH
// yields a descriptor on a link that is the last component, so that
// [checkRegular] sees the link on the descriptor and refuses it.
const fileFlags = unix.O_NOFOLLOW

// checkDir refuses what fstat reported when it is not a folder.
func checkDir(info Info) error {
	switch {
	case info.IsSymlink():
		return fmt.Errorf("%w: the last component", ErrSymlink)
	case info.IsRegular():
		return fmt.Errorf("%w: a regular file", ErrNotDirectory)
	case !info.IsDir():
		return fmt.Errorf("%w: %s", ErrNotDirectory, info.Mode.Type())
	}
	return nil
}

// checkRegular refuses what fstat reported when it is not a regular file.
func checkRegular(info Info) error {
	switch {
	case info.IsSymlink():
		return fmt.Errorf("%w: the last component", ErrSymlink)
	case !info.IsRegular():
		return fmt.Errorf("%w: %s", ErrNotRegular, info.Mode.Type())
	}
	return nil
}

// SubRoot opens the folder at rel as a new [Root], from this handle and not
// by name: one openat2 lookup with the resolve flags every call uses, then
// fstat on the descriptor it returned. A symbolic link, in a parent component
// or as the last one, is refused with [ErrSymlink], anything that is not a
// folder with [ErrNotDirectory], a mount point on the way with [ErrEscapes],
// and a missing entry with an error that matches fs.ErrNotExist. "." gives a
// second handle on the same folder.
//
// The new Root is on the folder that was opened, whatever happens to its name
// afterwards: a caller that looked at the folder through the new Root and
// then uses the new Root has checked the object it uses, which a
// [Root.Lstat] followed by [OpenRoot] on the name does not give. Each call is
// a fresh walk from the handle; nothing an earlier call found is reused.
//
// The new Root works like one from [OpenRoot] with every method, and a path
// given to it is beneath its own folder, not beneath this one. It has its own
// descriptor: the caller closes it, and closing either Root does not close
// the other. It makes its system calls the way this Root does, and is not
// probed again.
func (r *Root) SubRoot(rel string) (*Root, error) {
	const op = "subroot"
	if err := validate(rel, true); err != nil {
		return nil, invalid(op, rel, err)
	}
	if err := r.begin(op, rel); err != nil {
		return nil, err
	}
	defer r.end()
	fd, err := r.openPath(op, rel, dirFlags, checkDir)
	if err != nil {
		return nil, err
	}
	return &Root{fd: fd, opened: true, openat2: r.openat2, mkdirat: r.mkdirat}, nil
}

// Handle is one entry below a [Root], opened once and kept: an O_PATH
// descriptor on a folder or a regular file, for a caller that hands the entry
// on (a bind source) and needs the object it checked to be the object that is
// used. It comes from [Root.OpenDirHandle] or [Root.OpenFileHandle]; the zero
// value refuses every call with [ErrClosed]. A Handle is safe for concurrent
// use. The caller closes it: nothing closes it when it is no longer
// referenced, and closing the Root it came from does not close it.
//
// The descriptor names the object that was opened. Renaming the entry, or
// putting something else (a link included) at its name, does not change what
// the Handle names. It is an O_PATH descriptor: it cannot be read or written.
type Handle struct {
	mu     sync.Mutex
	fd     int
	rel    string // the path it was opened with, for error messages
	opened bool   // set by the Root; false for the zero value
	closed bool
}

// OpenDirHandle opens the folder at rel and keeps it as a [Handle]: one
// openat2 lookup with O_PATH and the resolve flags every call uses, then
// fstat on the descriptor. A symbolic link, in a parent component or as the
// last one, is refused with [ErrSymlink], anything that is not a folder with
// [ErrNotDirectory], a mount point on the way with [ErrEscapes], and a
// missing entry with an error that matches fs.ErrNotExist. "." names the
// Root's own folder, so a Root can be handed on as it is. Each call is a
// fresh walk from the Root; nothing an earlier call found is reused.
func (r *Root) OpenDirHandle(rel string) (*Handle, error) {
	return r.openHandle(rel, true, dirFlags, checkDir)
}

// OpenFileHandle opens the regular file at rel and keeps it as a [Handle],
// the way [Root.OpenDirHandle] does a folder. A symbolic link is refused with
// [ErrSymlink] and a folder, FIFO, device or socket with [ErrNotRegular]. The
// open is O_PATH: it does not open the file itself, so a FIFO cannot block it
// and a device's own open does not run. "." is refused with
// [ErrInvalidArgument]: it names a folder.
//
// Neither the size nor the link count is refused here, because nothing is
// read: [Handle.Info] reports both, and a caller that must not hand on a
// hard-linked file checks [Info.HardLinked].
func (r *Root) OpenFileHandle(rel string) (*Handle, error) {
	return r.openHandle(rel, false, fileFlags, checkRegular)
}

func (r *Root) openHandle(rel string, allowDot bool, flags int, check func(Info) error) (*Handle, error) {
	const op = "handle"
	if err := validate(rel, allowDot); err != nil {
		return nil, invalid(op, rel, err)
	}
	if err := r.begin(op, rel); err != nil {
		return nil, err
	}
	defer r.end()
	fd, err := r.openPath(op, rel, flags, check)
	if err != nil {
		return nil, err
	}
	return &Handle{fd: fd, rel: rel, opened: true}, nil
}

// Info is what fstat reports on the descriptor now: the object that was
// opened, not what its name holds today. It is taken again on every call, so
// a mode, owner, size or link count that changed since the open is seen; the
// type of an object does not change. After Close it returns [ErrClosed].
func (h *Handle) Info() (Info, error) {
	const op = "handle"
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.opened || h.closed {
		return Info{}, &Error{Op: op, Path: h.rel, Err: ErrClosed}
	}
	info, err := fstat(h.fd)
	if err != nil {
		return Info{}, &Error{Op: op, Path: h.rel, Err: err}
	}
	return info, nil
}

// Fd returns the number of the descriptor, for a caller that passes the
// object to a child process. After Close it returns [ErrClosed].
//
// The descriptor is close-on-exec, like every descriptor of the package: no
// child process inherits it unless the caller passes it on, which is the
// caller's explicit act (a duplicate without the flag made for that one
// child, as os/exec does for ExtraFiles). The Handle still owns the
// descriptor: the number is valid until Close, so the caller keeps the Handle
// open until the child has it, and neither closes the number itself nor wraps
// it in something that would (an os.File from os.NewFile closes it when the
// file is closed or collected; duplicate the descriptor first). A caller that
// wants a descriptor of its own, which no Close of the Handle can invalidate,
// takes [Handle.Dup].
func (h *Handle) Fd() (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.opened || h.closed {
		return -1, &Error{Op: "handle", Path: h.rel, Err: ErrClosed}
	}
	return h.fd, nil
}

// minDupFD is the lowest number [Handle.Dup] gives: above the standard
// streams, so that a duplicate never becomes descriptor 0, 1 or 2 of a
// process that has closed one of them.
const minDupFD = 3

// Dup returns a duplicate of the descriptor that the caller owns: a new
// number (the lowest free one from 3 up) that names the same opened object,
// is an O_PATH descriptor like the Handle's own and is close-on-exec. It is
// for a caller that hands the object on (a bind source placed at a number a
// child process inherits). The caller closes the duplicate, or moves it
// (dup3) and clears close-on-exec for the one program that is to inherit it;
// closing the Handle does not close it, and closing it does not close the
// Handle. After Close it returns [ErrClosed].
//
// Unlike the number [Handle.Fd] returns, the duplicate cannot be invalidated
// by a Close of the Handle in another goroutine: it is made under the
// Handle's lock (F_DUPFD_CLOEXEC, so no other thread's exec sees it without
// the flag) and from then on is the caller's alone.
func (h *Handle) Dup() (int, error) {
	const op = "handle"
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.opened || h.closed {
		return -1, &Error{Op: op, Path: h.rel, Err: ErrClosed}
	}
	for {
		fd, err := unix.FcntlInt(uintptr(h.fd), unix.F_DUPFD_CLOEXEC, minDupFD)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return -1, &Error{Op: op, Path: h.rel, Err: err}
		}
		return fd, nil
	}
}

// Close releases the descriptor. A second Close, and Close on a zero Handle,
// return [ErrClosed].
func (h *Handle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.opened || h.closed {
		return &Error{Op: "close", Path: h.rel, Err: ErrClosed}
	}
	h.closed = true
	return unix.Close(h.fd)
}
