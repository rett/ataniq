package durable

import (
	"crypto/ed25519"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const journalName = "journal.v1"
const lockName = "witness.lock"
const poisonName = "poisoned"
const recoveryName = "recovery.pending"

// A device reporting a writeback error may also fail to persist this marker.
// Open therefore proves the tail again even when there is no marker.
func (w *Witness) poison(cause error) error {
	w.stopped = true
	marker, err := openFile(w.dir, poisonName, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL)
	if err == nil {
		err = errors.Join(marker.Sync(), marker.Close(), w.dir.Sync())
	}
	return errors.Join(cause, err)
}

func (w *Witness) checkPoison() error {
	marker, err := openFile(w.dir, poisonName, syscall.O_RDONLY)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return errors.Join(ErrPoisoned, marker.Close())
}

// Persist the marker before any journal write or replay sync. A failed device
// may be unable to record that it failed. The lock serializes marker ownership.
func (w *Witness) arm() error {
	if w.armed {
		return nil
	}
	marker, err := openFile(w.dir, recoveryName, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL)
	if errors.Is(err, os.ErrExist) {
		w.recoveryRequired = true
		marker, err = openFile(w.dir, recoveryName, syscall.O_RDONLY)
	}
	if err != nil {
		return err
	}
	if err := errors.Join(marker.Sync(), marker.Close()); err != nil {
		return w.poison(err)
	}
	if err := w.dir.Sync(); err != nil {
		return w.poison(err)
	}
	w.armed = true
	return nil
}

// Require an absolute, clean path. Hold the opened parent while looking up
// each child. O_PATH permits traversal of execute-only ancestors.
func directory(path string, private bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnsafe
	}
	const flags = syscall.O_NOFOLLOW | syscall.O_CLOEXEC | syscall.O_DIRECTORY
	const oPath = 0x200000 // Linux O_PATH (missing from syscall on amd64)
	fd, err := syscall.Open("/", oPath|flags, 0)
	if err != nil {
		return nil, err
	}
	for _, name := range strings.FieldsFunc(path, func(r rune) bool { return r == '/' }) {
		next, e := syscall.Openat(fd, name, flags|oPath, 0)
		syscall.Close(fd)
		if e != nil {
			return nil, errors.Join(ErrUnsafe, e)
		}
		fd = next
		var st syscall.Stat_t
		if err = syscall.Fstat(fd, &st); err != nil || (st.Uid != 0 && int(st.Uid) != os.Geteuid()) || (st.Mode&0022 != 0 && st.Mode&syscall.S_ISVTX == 0) {
			syscall.Close(fd)
			return nil, errors.Join(ErrUnsafe, err)
		}
	}
	if private {
		next, e := syscall.Openat(fd, ".", flags, 0)
		syscall.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), path)
	if private {
		if err := inspect(f, true); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

func inspect(f *os.File, dir bool) error {
	var st syscall.Stat_t
	if err := syscall.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	return checkStat(st, dir, uint32(os.Geteuid()))
}

func checkStat(st syscall.Stat_t, dir bool, uid uint32) error {
	want := uint32(syscall.S_IFREG | 0600)
	if dir {
		want = syscall.S_IFDIR | 0700
	}
	if st.Mode != want || st.Uid != uid || (!dir && st.Nlink != 1) {
		return ErrUnsafe
	}
	return nil
}

func openFile(dir *os.File, name string, flags int) (*os.File, error) {
	fd, err := syscall.Openat(int(dir.Fd()), name, flags|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	if err := inspect(f, false); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func loadKey(path string) (ed25519.PrivateKey, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnsafe
	}
	dir, err := directory(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	key, err := openFile(dir, filepath.Base(path), syscall.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer key.Close()
	seed, err := io.ReadAll(io.LimitReader(key, ed25519.SeedSize+1))
	defer clear(seed)
	if err != nil {
		return nil, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, ErrIdentity
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
