//go:build linux

package mirror

import (
	"errors"
	"io/fs"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// oNoFollow makes an open refuse a symlink as the last element.
const oNoFollow = unix.O_NOFOLLOW

// ownerUID returns the owning user of a file as the system reports it.
// It accepts the result of os.Lstat and of the safefs calls.
func ownerUID(fi fs.FileInfo) (uint32, bool) {
	switch st := fi.Sys().(type) {
	case *syscall.Stat_t:
		return st.Uid, true
	case *unix.Stat_t:
		return st.Uid, true
	}
	return 0, false
}

// removeEmptyDir removes the folder at path when it is empty. It is rmdir and
// nothing else: it removes no file, no symlink and no folder with content.
func removeEmptyDir(path string) error { return unix.Rmdir(path) }

// lockFile takes an exclusive, non-blocking lock (flock) on the file at path
// and returns the function that releases it. The file is created with mode
// 0600 when it is missing, and opened without following a symlink. It must be
// a regular file that belongs to the calling user. The lock lives as long as
// the open file, so a crash releases it. The file is never removed: removing
// it would let two callers lock two different files.
//
// It returns [ErrBusy] when another open file holds the lock.
func lockFile(path string) (release func(), err error) {
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, &fs.PathError{Op: "fstat", Path: path, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || int(st.Uid) != os.Geteuid() {
		unix.Close(fd)
		return nil, errors.New("mirror: the lock file is not a regular file of the calling user")
	}
	for {
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err != unix.EINTR {
			break
		}
	}
	if err != nil {
		unix.Close(fd)
		if err == unix.EWOULDBLOCK {
			return nil, ErrBusy
		}
		return nil, &fs.PathError{Op: "flock", Path: path, Err: err}
	}
	return func() { unix.Close(fd) }, nil
}
