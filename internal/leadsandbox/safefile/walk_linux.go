//go:build linux

package safefile

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// openPath is the one lookup behind [Root.SubRoot]: a single openat2 from the
// handle with O_PATH and flags, then fstat on the descriptor it returned and
// check on what fstat reported. The type is decided on that descriptor and
// never on the name, so the object checked is the object the caller gets. The
// caller owns the descriptor returned with a nil error; on any error none is
// left open.
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
