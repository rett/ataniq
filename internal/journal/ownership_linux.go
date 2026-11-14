package journal

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrOwned means another handle holds the database inode's advisory lock.
var ErrOwned = errors.New("journal: database already owned")

type ownership struct {
	path      string
	file, dir *os.File
	identity  os.FileInfo
}

func private(info os.FileInfo, directory bool) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
		return false
	}
	if directory {
		return info.IsDir()
	}
	return info.Mode().IsRegular()
}

func acquire(path string, create bool) (_ *ownership, err error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsRune(path, 0) {
		return nil, errors.New("journal: expected clean absolute file path")
	}
	// Walk directory descriptors without following any symlink component.
	dir, err := os.Open("/")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Dir(path), "/"), "/") {
		if part == "" {
			continue
		}
		fd, e := syscall.Openat(int(dir.Fd()), part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		dir.Close()
		if e != nil {
			return nil, e
		}
		dir = os.NewFile(uintptr(fd), part)
	}
	o := &ownership{path: path, dir: dir}
	defer func() {
		if err != nil {
			err = errors.Join(err, o.close())
		}
	}()
	info, err := dir.Stat()
	if err != nil {
		return nil, err
	}
	if !private(info, true) || info.Mode().Perm()&0700 != 0700 {
		return nil, errors.New("journal: directory must be private, owned and accessible")
	}
	if create {
		if _, e := os.Lstat(path); e == nil {
			return nil, os.ErrExist
		} else if !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
	}
	// Refuse unsafe SQLite sidecars before SQLite can follow or create them.
	if err = o.checkSidecars(create); err != nil {
		return nil, err
	}
	flags := syscall.O_RDWR | syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_NONBLOCK
	if create {
		flags |= syscall.O_CREAT | syscall.O_EXCL
	}
	fd, err := syscall.Openat(int(dir.Fd()), filepath.Base(path), flags, 0600)
	if err != nil {
		return nil, err
	}
	o.file = os.NewFile(uintptr(fd), path)
	info, err = o.file.Stat()
	if err != nil {
		return nil, err
	}
	if !private(info, false) || info.Mode().Perm() != 0600 {
		return nil, errors.New("journal: database must be an owned regular 0600 file")
	}
	o.identity = info
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrOwned
		}
		return nil, err
	}
	// SQLite sidecars are pathname-based. Even after the live-owner check,
	// do not reopen a multiply-linked inode through a different WAL basename.
	if info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return nil, errors.New("journal: multiply-linked database")
	}
	if err = o.check(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *ownership) checkSidecars(absent bool) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		info, err := os.Lstat(o.path + suffix)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if absent || !private(info, false) || info.Mode().Perm() != 0600 || info.Sys().(*syscall.Stat_t).Nlink != 1 {
			return fmt.Errorf("journal: unsafe or unexpected %s sidecar", suffix)
		}
	}
	return nil
}

func (o *ownership) check() error {
	info, err := os.Lstat(o.path)
	if err != nil {
		return err
	}
	if !private(info, false) || info.Mode().Perm() != 0600 || !os.SameFile(o.identity, info) || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return errors.New("journal: database path changed")
	}
	dir, err := os.Lstat(filepath.Dir(o.path))
	if err != nil {
		return err
	}
	pinned, err := o.dir.Stat()
	if err != nil {
		return err
	}
	if !private(dir, true) || !os.SameFile(dir, pinned) {
		return errors.New("journal: directory changed")
	}
	return o.checkSidecars(false)
}

func (o *ownership) close() error {
	var err error
	// Closing the locked descriptor releases flock only after SQL has closed.
	if o.file != nil {
		err = o.file.Close()
	}
	return errors.Join(err, o.dir.Close())
}
