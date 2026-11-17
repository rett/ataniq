//go:build linux

package safefile

import (
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

// Sync flushes the regular file or folder at rel to stable storage with fsync,
// so that what was written through the handle survives a crash. "." names the
// handle's own folder. The entry is opened read-only with openat2 like every
// other lookup (a link is refused with [ErrSymlink], also as the last
// component) and its type is checked on the descriptor: only a regular file
// or a folder is synced, anything else is refused with [ErrNotRegular]. Syncing
// a file does not make its entry in its folder durable: sync the folder too
// (see [Root.RenameNoReplace]).
func (r *Root) Sync(rel string) error {
	const op = "sync"
	if err := validate(rel, true); err != nil {
		return invalid(op, rel, err)
	}
	if err := r.begin(op, rel); err != nil {
		return err
	}
	defer r.end()
	// O_NONBLOCK keeps the open of a FIFO from blocking and of a leased file
	// from waiting; the type check below refuses a FIFO anyway. A read-only
	// descriptor is enough for fsync, on a folder too.
	fd, err := r.open(r.fd, rel, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return classify(op, rel, err)
	}
	defer unix.Close(fd)
	info, err := fstat(fd)
	if err != nil {
		return &Error{Op: op, Path: rel, Err: err}
	}
	if !info.IsRegular() && !info.IsDir() {
		return &Error{Op: op, Path: rel, Err: fmt.Errorf("%w: %s", ErrNotRegular, info.Mode.Type())}
	}
	for {
		err := unix.Fsync(fd)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return &Error{Op: op, Path: rel, Err: err}
		}
		return nil
	}
}

// RenameNoReplace renames the entry oldName of the handle's folder to newName
// in the same folder with renameat2 and RENAME_NOREPLACE: it never replaces,
// and fails with an error that matches fs.ErrExist when anything, a link
// included, is at newName. Both are single names, not paths (no "/", not "."
// or ".."), so the call looks nothing up below the handle and follows no link:
// a link at oldName is renamed as a link, not followed. It is the one rename
// the package has, for the step that puts a folder built beside its place
// where it belongs, in one step that cannot be seen half done. A file system
// that does not support RENAME_NOREPLACE answers EINVAL, which is returned as
// it is: there is no fallback to a rename that replaces. The folder is not
// synced: call [Root.Sync] on "." afterwards to make the new name durable.
func (r *Root) RenameNoReplace(oldName, newName string) error {
	const op = "rename"
	for _, n := range []string{oldName, newName} {
		if err := validate(n, false); err != nil {
			return invalid(op, oldName+" "+newName, err)
		}
		if strings.Contains(n, "/") {
			return invalid(op, oldName+" "+newName, fmt.Errorf("%w: %q is not a single name", ErrInvalidArgument, n))
		}
	}
	if err := r.begin(op, oldName); err != nil {
		return err
	}
	defer r.end()
	for {
		err := unix.Renameat2(r.fd, oldName, r.fd, newName, unix.RENAME_NOREPLACE)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return &Error{Op: op, Path: oldName + " -> " + newName, Err: err}
		}
		return nil
	}
}
