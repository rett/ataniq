//go:build linux

package safefile

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"golang.org/x/sys/unix"
)

// MkdirAll creates the folder at rel and any missing parents, walking one
// component at a time: each component is opened with openat2 (a symbolic link
// is refused with [ErrSymlink], a file with [ErrNotDirectory]) and a missing
// one is made with mkdirat and then opened again the same way, so a link
// planted between the two is caught. Only a folder this call creates gets
// mode (before the umask); an existing folder is left as it is. "." is the
// handle's own folder and does nothing.
//
// Each step is beneath the folder before it, not beneath the handle, so a
// folder renamed while the call runs would carry the rest of the walk with
// it. After the walk the call opens rel again from the handle and compares
// device and inode with the folder the walk ended on: a difference, or a path
// that is gone, returns [ErrEscapes]. What remains: folders the call made
// after the move are left where the moved folder went (outside the handle,
// but only in a place the lead could already write, since it moved the
// folder), and the call cannot undo them (there is no remove helper).
//
// A nil result holds only at the moment of that end check: a folder moved
// afterwards is not seen. A caller that uses the path later (a bind by path)
// checks it again right before it does.
func (r *Root) MkdirAll(rel string, mode fs.FileMode) error {
	const op = "mkdir"
	if err := validate(rel, true); err != nil {
		return invalid(op, rel, err)
	}
	m, err := permMode(mode)
	if err != nil {
		return invalid(op, rel, err)
	}
	if err := r.begin(op, rel); err != nil {
		return err
	}
	defer r.end()
	if rel == "." {
		return nil
	}
	cur, owned := r.fd, false
	defer func() {
		if owned {
			unix.Close(cur)
		}
	}()
	for _, comp := range strings.Split(rel, "/") {
		next, err := r.open(cur, comp, unix.O_PATH|unix.O_DIRECTORY, 0)
		if err == unix.ENOENT {
			if err := r.mkdirat(cur, comp, m); err != nil && err != unix.EEXIST {
				return mkdirError(op, rel, err)
			}
			next, err = r.open(cur, comp, unix.O_PATH|unix.O_DIRECTORY, 0)
		}
		if err != nil {
			return classify(op, rel, err)
		}
		if owned {
			unix.Close(cur)
		}
		cur, owned = next, true
	}
	// The walk is beneath each component, not beneath the handle: a folder
	// renamed while the call runs carries the rest of the walk with it. Open
	// rel again from the handle and compare it with the folder the walk ended
	// on; a different folder, or none, means a folder on the path moved.
	whole, err := r.open(r.fd, rel, unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		if err == unix.ENOENT {
			return movedError(op, rel)
		}
		return classify(op, rel, err)
	}
	defer unix.Close(whole)
	walked, err1 := fstat(cur)
	found, err2 := fstat(whole)
	if err1 != nil || err2 != nil {
		return &Error{Op: op, Path: rel, Err: errors.Join(err1, err2)}
	}
	if walked.Dev != found.Dev || walked.Ino != found.Ino {
		return movedError(op, rel)
	}
	return nil
}

// mkdirError reports a mkdirat failure. It is not openat2's: EPERM here is a
// refusal to create (a filter, an attribute, a read-only file system) and is
// reported as the errno, never as [ErrUnsupported].
func mkdirError(op, rel string, err error) error {
	if err == unix.ENOTDIR {
		return &Error{Op: op, Path: rel, Err: refuse(ErrNotDirectory, err)}
	}
	return &Error{Op: op, Path: rel, Err: err}
}

func movedError(op, rel string) error {
	return &Error{Op: op, Path: rel, Err: fmt.Errorf("%w: a folder on the path moved while the call ran", ErrEscapes)}
}

// WriteFileExclusive creates the regular file at rel with data and mode
// (before the umask) and fails if anything exists there, a symbolic link
// included: it never replaces and never follows (O_CREAT|O_EXCL). When what
// exists is a symbolic link the error is [ErrSymlink] (and still matches
// fs.ErrExist, as the errno is wrapped too); anything else that exists is
// only fs.ErrExist, and a link in a parent component is [ErrSymlink]. The
// parent folder must exist; use [Root.MkdirAll] first. A failure part-way through
// the write leaves the partial file: there is no remove helper by design.
func (r *Root) WriteFileExclusive(rel string, data []byte, mode fs.FileMode) error {
	const op = "create"
	if err := validate(rel, false); err != nil {
		return invalid(op, rel, err)
	}
	m, err := permMode(mode)
	if err != nil {
		return invalid(op, rel, err)
	}
	if err := r.begin(op, rel); err != nil {
		return err
	}
	defer r.end()
	fd, err := r.open(r.fd, rel, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_NOCTTY, m)
	if err != nil {
		if err == unix.EEXIST && r.lastIsSymlink(rel) {
			// The create was refused; the lookup only names the reason. The
			// error still matches fs.ErrExist.
			return &Error{Op: op, Path: rel, Err: refuse(ErrSymlink, err)}
		}
		return classify(op, rel, err)
	}
	werr := writeAll(fd, data)
	cerr := unix.Close(fd)
	if werr != nil {
		return &Error{Op: op, Path: rel, Err: werr}
	}
	if cerr != nil {
		return &Error{Op: op, Path: rel, Err: cerr}
	}
	return nil
}

// lastIsSymlink reports whether the entry at rel is a symbolic link, looked
// up without following it (the no-follow stat of [Root.Lstat]). It decides
// nothing but the wording of a refusal that already happened.
func (r *Root) lastIsSymlink(rel string) bool {
	fd, err := r.open(r.fd, rel, unix.O_PATH|unix.O_NOFOLLOW, 0)
	if err != nil {
		return false
	}
	defer unix.Close(fd)
	info, err := fstat(fd)
	return err == nil && info.IsSymlink()
}

// AppendFile appends data to the existing regular file at rel (O_APPEND; it
// creates nothing and truncates nothing). The file type is checked on the
// descriptor before anything is written, and a hard-linked file is always
// refused with [ErrHardLink]: a write through the lead's name would change
// the other name's data too.
func (r *Root) AppendFile(rel string, data []byte) error {
	const op = "append"
	if err := validate(rel, false); err != nil {
		return invalid(op, rel, err)
	}
	if err := r.begin(op, rel); err != nil {
		return err
	}
	defer r.end()
	// O_NONBLOCK keeps a write open of a FIFO from blocking (with no reader
	// it fails with ENXIO, reported as not regular), and keeps a lease a lead
	// holds on the file from stalling the owner for the lease-break time: the
	// open fails at once with EAGAIN. Do not drop it because fstat refuses
	// FIFOs anyway. A device's open has already run when fstat sees it (see
	// the package documentation).
	fd, err := r.open(r.fd, rel, unix.O_WRONLY|unix.O_APPEND|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return classify(op, rel, err)
	}
	defer unix.Close(fd)
	info, err := fstat(fd)
	if err != nil {
		return &Error{Op: op, Path: rel, Err: err}
	}
	if !info.IsRegular() {
		return &Error{Op: op, Path: rel, Err: fmt.Errorf("%w: %s", ErrNotRegular, info.Mode.Type())}
	}
	if info.HardLinked() {
		return &Error{Op: op, Path: rel, Err: fmt.Errorf("%w: link count %d", ErrHardLink, info.Nlink)}
	}
	if err := writeAll(fd, data); err != nil {
		return &Error{Op: op, Path: rel, Err: err}
	}
	return nil
}

func writeAll(fd int, data []byte) error {
	for len(data) > 0 {
		n, err := unix.Write(fd, data)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}
