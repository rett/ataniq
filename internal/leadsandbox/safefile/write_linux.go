//go:build linux

package safefile

import (
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
			if err := unix.Mkdirat(cur, comp, m); err != nil && err != unix.EEXIST {
				return classify(op, rel, err)
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
	return nil
}

// WriteFileExclusive creates the regular file at rel with data and mode
// (before the umask) and fails if anything exists there, a symbolic link
// included: it never replaces and never follows (O_CREAT|O_EXCL). The parent
// folder must exist; use [Root.MkdirAll] first. A failure part-way through
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
	// O_NONBLOCK keeps a write open of a FIFO from blocking: with no reader
	// it fails with ENXIO, which is reported as not regular.
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
