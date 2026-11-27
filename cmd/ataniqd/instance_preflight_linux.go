package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ataniq/internal/leaddaemon"
	"ataniq/internal/leadregistry"
	"golang.org/x/sys/unix"
)

// Before publishing a first key, replay the existing journal. Registry.Open
// can repair a torn tail, so replay a private copy and leave the original for
// the registry's locked migration. A root without a journal needs no copy.
func validateBeforeKey(root *os.File) error {
	dir, err := unix.Openat(int(root.Fd()), leaddaemon.RegistryDirName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return nil
	}
	if err == unix.ENOTDIR || err == unix.ELOOP {
		return fmt.Errorf("%s is a symlink or not a directory", filepath.Join(root.Name(), leaddaemon.RegistryDirName))
	}
	if err != nil {
		return err
	}
	defer unix.Close(dir)
	var st unix.Stat_t
	if err := unix.Fstat(dir, &st); err != nil {
		return err
	}
	if int(st.Uid) != os.Geteuid() || st.Mode&0077 != 0 {
		return fmt.Errorf("%w: expected private user-owned registry directory", leadregistry.ErrInsecure)
	}
	fd, err := unix.Openat(dir, leadregistry.FileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return nil
	}
	if err == unix.ELOOP {
		return errors.New("journal is a symlink")
	}
	if err != nil {
		return err
	}
	source := os.NewFile(uintptr(fd), journalPath(root.Name()))
	defer source.Close()
	if err := unix.Flock(fd, unix.LOCK_SH|unix.LOCK_NB); err != nil {
		if err == unix.EWOULDBLOCK {
			return leadregistry.ErrLocked
		}
		return err
	}
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || int(st.Uid) != os.Geteuid() || st.Nlink != 1 || st.Mode&0177 != 0 {
		return fmt.Errorf("%w: expected a singly linked private user-owned journal", leadregistry.ErrInsecure)
	}
	return replayCopy(root, source)
}

func replayCopy(root, source *os.File) (err error) {
	dir, err := os.MkdirTemp(fmt.Sprintf("/proc/self/fd/%d", root.Fd()), ".registry-check-")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	snapshot, err := os.OpenFile(filepath.Join(dir, leadregistry.FileName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(snapshot, source)
	if err := errors.Join(err, snapshot.Close()); err != nil {
		return err
	}
	reg, err := leadregistry.Open(dir, leadregistry.Options{})
	if err != nil {
		return err
	}
	return reg.Close()
}
