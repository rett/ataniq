package switcher

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// OwnerCheck decides whether the owner of a file or folder the switch trusts
// (the state folder and its parents, the release folders, the policy and the
// allowed signers file) is acceptable. It is given what Lstat or Fstat
// returned. The modes are checked apart from it: nothing trusted may be
// writable by its group or by others.
type OwnerCheck func(info fs.FileInfo) error

// DefaultOwnerCheck accepts root as the owner, and the user that runs the
// switch when that is not root. On the host the switch runs as root, so only
// root is accepted there; a switch run by another user (the tests) can do
// nothing that user could not do anyway.
func DefaultOwnerCheck(info fs.FileInfo) error {
	uid, ok := ownerOf(info)
	if !ok {
		return errors.New("its owner cannot be read")
	}
	if uid == 0 || uid == uint32(os.Geteuid()) {
		return nil
	}
	return fmt.Errorf("owned by uid %d, not root", uid)
}

func ownerOf(info fs.FileInfo) (uint32, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return st.Uid, true
}

// trustedMode reports why info, which names path, is not a folder or a
// regular file (as isDir says) that the switch can trust: the owner, a mode
// writable by group or others.
func (s *Switcher) trusted(path string, info fs.FileInfo, isDir bool) error {
	switch {
	case info.Mode()&fs.ModeSymlink != 0:
		return failf(CodeUntrusted, "%s is a symbolic link", q(path))
	case isDir && !info.IsDir():
		return failf(CodeUntrusted, "%s is not a folder", q(path))
	case !isDir && !info.Mode().IsRegular():
		return failf(CodeUntrusted, "%s is not a regular file", q(path))
	case info.Mode().Perm()&0o022 != 0:
		return failf(CodeUntrusted, "%s is writable by its group or by others", q(path))
	}
	if err := s.cfg.Owner(info); err != nil {
		return failf(CodeUntrusted, "%s: %s", q(path), q(err.Error()))
	}
	return nil
}

// openTrusted opens the folder rel below the root, checking the root and each
// folder on the way: each must be a real folder (not a link), trusted by
// trusted, and the folder opened must be the one checked. The returned
// os.Root pins the last folder: what is opened through it stays below it,
// whatever is renamed above it. With create, missing folders are made with
// mode 0755; without it a missing folder is fs.ErrNotExist.
func (s *Switcher) openTrusted(rel string, create bool) (*os.Root, error) {
	r, err := os.OpenRoot(s.cfg.Root)
	if err != nil {
		return nil, err
	}
	info, err := r.Stat(".")
	if err == nil {
		err = s.trusted(s.cfg.Root, info, true)
	}
	if err != nil {
		r.Close()
		return nil, err
	}
	path := s.cfg.Root
	for _, c := range strings.Split(filepath.ToSlash(rel), "/") {
		path = filepath.Join(path, c)
		info, err := r.Lstat(c)
		if create && errors.Is(err, fs.ErrNotExist) {
			if err = r.Mkdir(c, 0o755); err == nil || errors.Is(err, fs.ErrExist) {
				info, err = r.Lstat(c)
			}
		}
		if err == nil {
			err = s.trusted(path, info, true)
		}
		var sub *os.Root
		if err == nil {
			sub, err = r.OpenRoot(c)
		}
		r.Close()
		if err != nil {
			return nil, err
		}
		opened, err := sub.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			sub.Close()
			return nil, failf(CodeUntrusted, "%s changed while it was opened", q(path))
		}
		r = sub
	}
	return r, nil
}

// openFileIn opens the regular file name of the folder r for reading, without
// following a link and without blocking on a fifo, and checks that what was
// opened is the regular file that was looked at.
func openFileIn(r *os.Root, name string) (*os.File, fs.FileInfo, error) {
	before, err := r.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, failf(CodeBadEntry, "%s is not a regular file", q(name))
	}
	f, err := r.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		f.Close()
		return nil, nil, failf(CodeReadFailed, "%s changed while it was opened", q(name))
	}
	return f, after, nil
}

// readFileIn reads the regular file name of the folder r, of at most max
// bytes. A missing file is fs.ErrNotExist.
func readFileIn(r *os.Root, name string, max int64) ([]byte, error) {
	f, info, err := openFileIn(r, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info.Size() > max {
		return nil, failf(CodeReadFailed, "%s is larger than %d bytes", q(name), max)
	}
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, failf(CodeReadFailed, "%s: %s", q(name), q(err.Error()))
	}
	if int64(len(data)) > max {
		return nil, failf(CodeReadFailed, "%s is larger than %d bytes", q(name), max)
	}
	return data, nil
}

// writeFileIn writes the file name of the folder r whole: it writes a new
// file beside it, syncs it, renames it over name and syncs the folder.
func writeFileIn(r *os.Root, name string, data []byte, perm fs.FileMode) error {
	tmp := "." + name + ".new"
	if err := r.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			r.Remove(tmp)
		}
	}()
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := r.Rename(tmp, name); err != nil {
		return err
	}
	ok = true
	return syncRoot(r)
}

// removeFileIn removes name from r and syncs the folder; a missing name is
// not an error.
func removeFileIn(r *os.Root, name string) error {
	if err := r.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncRoot(r)
}

func syncRoot(r *os.Root) error {
	d, err := r.Open(".")
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
