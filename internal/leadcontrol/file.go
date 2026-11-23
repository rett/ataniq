//go:build linux && ataniq_canary

package leadcontrol

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func closeFD(fd int) { _ = unix.Close(fd) }

func syncFD(fd int) error {
	for {
		err := unix.Fsync(fd)
		if err != unix.EINTR {
			return err
		}
	}
}

// openDir opens the folder of the table without following a symbolic link at
// its last element. The folder must exist: it is the daemon's state root.
func openDir(dir string) (int, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	switch {
	case err == unix.ELOOP || err == unix.ENOTDIR:
		return -1, fmt.Errorf("%w: %s is a symbolic link or not a folder", ErrInsecure, dir)
	case err != nil:
		return -1, fmt.Errorf("leadcontrol: cannot open the folder %s: %w", dir, err)
	}
	return fd, nil
}

// checkHandle judges an open file by its handle: a regular file of the caller
// with mode 0600 and one link.
func checkHandle(fd int, path string) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fmt.Errorf("leadcontrol: cannot examine %s: %w", path, err)
	}
	switch {
	case st.Mode&unix.S_IFMT != unix.S_IFREG:
		return fmt.Errorf("%w: %s is not a regular file", ErrInsecure, path)
	case int(st.Uid) != unix.Geteuid():
		return fmt.Errorf("%w: %s is not owned by the caller", ErrInsecure, path)
	case st.Mode&07777 != 0o600:
		return fmt.Errorf("%w: %s has mode %04o, want 0600", ErrInsecure, path, st.Mode&07777)
	case st.Nlink != 1:
		return fmt.Errorf("%w: %s has more than one link", ErrInsecure, path)
	}
	return nil
}

func lockFD(fd int, path string) error {
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		switch err {
		case unix.EINTR:
			continue
		case nil:
			return nil
		case unix.EWOULDBLOCK:
			return fmt.Errorf("%w: %s", ErrLocked, path)
		}
		return fmt.Errorf("leadcontrol: cannot lock %s: %w", path, err)
	}
}

const fileFlags = unix.O_RDWR | unix.O_APPEND | unix.O_NOFOLLOW | unix.O_CLOEXEC

// openFile opens the table's file in the folder, creating it with mode 0600
// when it is not there, locks it and judges it. A new file is made durable in
// its folder before it is used.
func openFile(dirFD int, name, path string) (*os.File, error) {
	created := true
	fd, err := unix.Openat(dirFD, name, fileFlags|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err == unix.EEXIST {
		created = false
		fd, err = unix.Openat(dirFD, name, fileFlags, 0)
	}
	switch {
	case err == unix.ELOOP:
		return nil, fmt.Errorf("%w: %s is a symbolic link", ErrInsecure, path)
	case err != nil:
		return nil, fmt.Errorf("leadcontrol: cannot open %s: %w", path, err)
	}
	f := os.NewFile(uintptr(fd), path)
	fail := func(err error) (*os.File, error) {
		_ = f.Close()
		return nil, err
	}
	if err := lockFD(fd, path); err != nil {
		return fail(err)
	}
	if created {
		// The umask may have cleared bits of the mode; the file is 0600 exactly.
		if err := unix.Fchmod(fd, 0o600); err != nil {
			return fail(fmt.Errorf("leadcontrol: cannot set the mode of %s: %w", path, err))
		}
		if err := syncFD(dirFD); err != nil {
			return fail(fmt.Errorf("leadcontrol: cannot sync the folder of %s: %w", path, err))
		}
	}
	if err := checkHandle(fd, path); err != nil {
		return fail(err)
	}
	return f, nil
}

// compact writes the lines that rebuild the state to a temporary file, syncs
// it, renames it over the table and syncs the folder. The new file is locked
// before the rename, so no other process can hold the path at any moment. On a
// failure the table's file is left as it was.
func (t *Table) compact(dirFD int, path string) error {
	data, err := t.snapshot()
	if err != nil {
		return err
	}
	name := filepath.Base(path)
	tmp := name + ".tmp"
	// A temporary file that an earlier run left is of no use.
	if err := unix.Unlinkat(dirFD, tmp, 0); err != nil && err != unix.ENOENT {
		return fmt.Errorf("leadcontrol: cannot remove the old temporary file: %w", err)
	}
	fd, err := unix.Openat(dirFD, tmp, fileFlags|unix.O_CREAT|unix.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("leadcontrol: cannot create the temporary file: %w", err)
	}
	nf := os.NewFile(uintptr(fd), path)
	fail := func(err error) error {
		_ = nf.Close()
		_ = unix.Unlinkat(dirFD, tmp, 0)
		return err
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		return fail(fmt.Errorf("leadcontrol: cannot set the mode of the temporary file: %w", err))
	}
	if err := lockFD(fd, path); err != nil {
		return fail(err)
	}
	if n, err := nf.Write(data); err != nil || n != len(data) {
		if err == nil {
			err = errors.New("short write")
		}
		return fail(fmt.Errorf("leadcontrol: cannot write the temporary file: %w", err))
	}
	if err := nf.Sync(); err != nil {
		return fail(fmt.Errorf("leadcontrol: cannot sync the temporary file: %w", err))
	}
	if err := unix.Renameat(dirFD, tmp, dirFD, name); err != nil {
		return fail(fmt.Errorf("leadcontrol: cannot rename the temporary file: %w", err))
	}
	old := t.f
	t.f = nf
	_ = old.Close()
	if err := syncFD(dirFD); err != nil {
		return fmt.Errorf("leadcontrol: cannot sync the folder: %w", err)
	}
	return nil
}
