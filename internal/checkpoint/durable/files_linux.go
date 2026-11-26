package durable

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
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
	if flags&(syscall.O_CREAT|syscall.O_EXCL) == syscall.O_CREAT|syscall.O_EXCL {
		if err := f.Chmod(0600); err != nil {
			f.Close()
			return nil, err
		}
	}
	if err := inspect(f, false); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func openKey(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnsafe
	}
	dir, err := directory(filepath.Dir(path), false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	return openFile(dir, filepath.Base(path), syscall.O_RDONLY)
}

func readKey(key *os.File) (ed25519.PrivateKey, error) {
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

func exclusive(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return ErrLocked
	}
	return err
}

// Scan a fresh directory descriptor so an optimistic empty-store check and
// the authoritative check under flock each start at the first entry.
type publication struct {
	index, journal, pending bool
	private                 []string
}

func inspectPublication(dir *os.File) (publication, error) {
	var p publication
	fd, err := unix.Openat(int(dir.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return p, err
	}
	f := os.NewFile(uintptr(fd), ".")
	defer f.Close()
	names, err := f.Readdirnames(-1)
	for _, name := range names {
		switch name {
		case lockName:
			p.index = true
		case journalName:
			p.journal = true
		case recoveryName, poisonName:
			p.pending = true
		default:
			if strings.HasPrefix(name, ".create-") {
				p.private = append(p.private, name)
			}
		}
	}
	return p, err
}

func (p publication) state() error {
	switch {
	case p.index && p.journal:
		return nil
	case len(p.private) != 0:
		return ErrPublication
	case p.index:
		return ErrMissingJournal
	case p.journal:
		return ErrMissingIndex
	case p.pending:
		return ErrPublication
	default:
		return ErrNotInitialized
	}
}

// Accept one staged journal and its corresponding staged or published index.
// Ambiguous remnants and a lone public file must be preserved for inspection.
func (p publication) stagedPrefix() (string, error) {
	if p.journal {
		return "", ErrPublication
	}
	var prefix string
	for _, name := range p.private {
		if strings.HasSuffix(name, "-"+journalName) {
			prefix = strings.TrimSuffix(name, journalName)
		}
	}
	if prefix != "" {
		if p.index && len(p.private) == 1 {
			return prefix, nil
		}
		if !p.index && len(p.private) == 2 && slices.Contains(p.private, prefix+lockName) {
			return prefix, nil
		}
	}
	return "", ErrPublication
}

func (w *Witness) openPair(mode startMode, key *os.File) error {
	if mode == openStore {
		p, err := inspectPublication(w.dir)
		if err != nil {
			return err
		}
		if p.state() == ErrNotInitialized {
			return errors.Join(ErrNotInitialized, os.ErrNotExist)
		}
	}
	if err := exclusive(w.dir); err != nil {
		return err
	}
	p, err := inspectPublication(w.dir)
	if err != nil {
		return err
	}
	state := p.state()
	switch mode {
	case createStore:
		if state == nil {
			return os.ErrExist
		}
		if state != ErrNotInitialized {
			return state
		}
	case openStore:
		if state != nil {
			return state
		}
	case completeStore:
		if state != ErrPublication {
			if state == nil {
				return os.ErrExist
			}
			return state
		}
		w.stagingPrefix, err = p.stagedPrefix()
		if err != nil {
			return err
		}
	}
	if err := exclusive(key); err != nil {
		return err
	}
	if err := w.checkPoison(); err != nil {
		return err
	}
	flags := syscall.O_RDWR | syscall.O_APPEND
	if mode == createStore {
		w.stagingPrefix = ".create-" + rand.Text() + "-"
		flags |= syscall.O_CREAT | syscall.O_EXCL
	}
	index := w.stagingPrefix + lockName
	if p.index {
		index = lockName
	}
	w.lockFile, err = openFile(w.dir, index, flags)
	if err != nil {
		return err
	}
	// Keep the index flock for compatibility with existing directory users.
	if err = exclusive(w.lockFile); err != nil {
		return err
	}
	w.journal, err = openFile(w.dir, w.stagingPrefix+journalName, flags)
	return err
}

// Genesis and its index entry have both passed their sync barriers. Publish
// the already locked index first. The journal rename makes the pair visible.
// RENAME_NOREPLACE also protects against an existing name at publication.
func (w *Witness) publishPair() error {
	fd := int(w.dir.Fd())
	for _, name := range []string{lockName, journalName} {
		if name == lockName && w.lockFile.Name() == lockName {
			continue
		}
		if err := unix.Renameat2(fd, w.stagingPrefix+name, fd, name, unix.RENAME_NOREPLACE); err != nil {
			return err
		}
		if err := w.after("published-" + name); err != nil {
			return err
		}
	}
	w.stagingPrefix = ""
	if err := w.dir.Sync(); err != nil {
		return w.poison(err)
	}
	return nil
}
