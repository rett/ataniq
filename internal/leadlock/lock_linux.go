package leadlock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

// maxHintBytes bounds how much of the lock file is read for the pid text.
const maxHintBytes = 64

// ErrUnsafeFile is wrapped by the error [Acquire] returns for a lock path
// that is a symbolic link, not a regular file, not owned by the caller, or
// has permission bits wider than 0600.
var ErrUnsafeFile = errors.New("leadlock: unsafe lock file")

// ErrNoHolder is returned by [ReadHolder] when the file holds no decimal pid
// text.
var ErrNoHolder = errors.New("leadlock: no pid text in lock file")

// ErrReplaced is returned by [Acquire] when the path no longer names the file
// that was locked, because it was unlinked or replaced during the call.
var ErrReplaced = errors.New("leadlock: lock file replaced while locking")

// ErrHeld is returned by [Acquire] when another open file description holds
// the lock. The lock file is not changed.
//
// PIDText is the decimal text read from the file, empty when the file holds
// no decimal pid. It is a hint for messages and not proof of who holds the
// lock: the text can be stale, empty or written by a process that has since
// exited.
type ErrHeld struct {
	// Path is the lock file path given to Acquire.
	Path string
	// PIDText is the unverified pid hint, or empty.
	PIDText string
}

// Error describes the held lock and labels the pid as a hint.
func (e ErrHeld) Error() string {
	if e.PIDText == "" {
		return "leadlock: " + e.Path + " is locked by another holder (no pid hint)"
	}
	return "leadlock: " + e.Path + " is locked by another holder (pid hint " + e.PIDText + ", not proof)"
}

// Is reports whether target is an ErrHeld, so errors.Is(err, ErrHeld{})
// matches any held-lock error.
func (e ErrHeld) Is(target error) bool {
	_, ok := target.(ErrHeld)
	return ok
}

// DaemonPath returns the daemon singleton lock path, <stateDir>/daemon.lock.
func DaemonPath(stateDir string) string {
	return filepath.Join(stateDir, "daemon.lock")
}

// Lock is a held lock. It stays held until Release is called or the
// descriptor is otherwise closed. Keep the Lock reachable for as long as the
// lock must hold: the underlying os.File closes itself when collected.
type Lock struct {
	path string
	mu   sync.Mutex
	file *os.File
}

// Acquire creates the parent folders (mode 0700) and the file (mode 0600)
// when missing, opens the file without following a final symbolic link and
// with close-on-exec, refuses a file that is not regular, not owned by the
// effective user, or has permission bits wider than 0600, and takes
// flock(LOCK_EX|LOCK_NB). Only after the lock is held does it truncate the
// file and write the caller's pid as decimal text and a newline, then fsync.
//
// When the lock is held elsewhere it returns an [ErrHeld] and leaves the
// file untouched. Two Acquire calls on one path in one process conflict,
// because each opens its own file description.
func Acquire(path string) (*Lock, error) {
	if path == "" {
		return nil, errors.New("leadlock: empty lock path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("leadlock: create folder: %w", err)
	}
	fd, err := openNoFollow(path, unix.O_RDWR|unix.O_CREAT, 0o600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
		}
	}()

	var st unix.Stat_t
	if err := retry(func() error { return unix.Fstat(fd, &st) }); err != nil {
		return nil, fmt.Errorf("leadlock: stat %s: %w", path, err)
	}
	if err := checkFile(path, &st, uint32(os.Geteuid())); err != nil {
		return nil, err
	}

	err = retry(func() error { return unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) })
	if errors.Is(err, unix.EWOULDBLOCK) {
		text, _ := hintAt(f)
		return nil, ErrHeld{Path: path, PIDText: text}
	}
	if err != nil {
		return nil, fmt.Errorf("leadlock: lock %s: %w", path, err)
	}

	// The lock is held. Refuse if the path no longer names this file: a lock
	// on an unlinked inode excludes nobody.
	var now unix.Stat_t
	if err := unix.Lstat(path, &now); err != nil || now.Dev != st.Dev || now.Ino != st.Ino {
		return nil, fmt.Errorf("%w: %s", ErrReplaced, path)
	}

	if err := f.Truncate(0); err != nil {
		return nil, fmt.Errorf("leadlock: truncate %s: %w", path, err)
	}
	if _, err := f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0); err != nil {
		return nil, fmt.Errorf("leadlock: write pid %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("leadlock: sync %s: %w", path, err)
	}
	ok = true
	return &Lock{path: path, file: f}, nil
}

// Path returns the lock file path given to Acquire.
func (l *Lock) Path() string { return l.path }

// Held reports whether this Lock still owns its descriptor, that is, Release
// has not been called. It does not touch the file and is no probe of other
// holders.
func (l *Lock) Held() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file != nil
}

// File returns the open lock file so the daemon can pass the held descriptor
// on, for example through exec.Cmd.ExtraFiles. It returns nil after Release.
// The descriptor is close-on-exec; a process that receives a duplicate keeps
// the lock until it too closes it, even after Release here. Do not write to
// or truncate the file.
func (l *Lock) File() *os.File {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file
}

// Release closes the descriptor, which drops the lock unless a duplicate of
// the descriptor is still open elsewhere. It is idempotent. The file is never
// unlinked.
func (l *Lock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	return f.Close()
}

// ReadHolder reads the pid text of the lock file at path without taking the
// lock and without following a final symbolic link. It is for display only.
// The text is a hint, not proof: the lock is the flock, the text can be stale,
// partial or empty, and a peer is authenticated by other means (a pidfd from
// the socket), never by this value. It returns [ErrNoHolder] when the file
// holds no decimal pid.
func ReadHolder(path string) (pid int, err error) {
	fd, err := openNoFollow(path, unix.O_RDONLY, 0)
	if err != nil {
		return 0, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var st unix.Stat_t
	if err := retry(func() error { return unix.Fstat(fd, &st) }); err != nil {
		return 0, fmt.Errorf("leadlock: stat %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return 0, fmt.Errorf("%w: %s is not a regular file", ErrUnsafeFile, path)
	}
	text, _ := hintAt(f)
	if text == "" {
		return 0, ErrNoHolder
	}
	n, err := strconv.Atoi(text)
	if err != nil || n <= 0 {
		return 0, ErrNoHolder
	}
	return n, nil
}

// openNoFollow opens path with O_NOFOLLOW, O_CLOEXEC and O_NOCTTY and maps
// the errors that mean an unsafe target to ErrUnsafeFile.
func openNoFollow(path string, flags int, mode uint32) (int, error) {
	var fd int
	err := retry(func() (e error) {
		fd, e = unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NOCTTY, mode)
		return e
	})
	switch {
	case err == nil:
		return fd, nil
	case errors.Is(err, unix.ELOOP):
		return -1, fmt.Errorf("%w: %s is a symbolic link", ErrUnsafeFile, path)
	case errors.Is(err, unix.EISDIR):
		return -1, fmt.Errorf("%w: %s is a directory", ErrUnsafeFile, path)
	}
	return -1, fmt.Errorf("leadlock: open %s: %w", path, err)
}

// checkFile enforces: regular file, owned by euid, no permission bits beyond
// 0600 (setuid, setgid, sticky and every group or other bit count).
func checkFile(path string, st *unix.Stat_t, euid uint32) error {
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%w: %s is not a regular file", ErrUnsafeFile, path)
	}
	if st.Uid != euid {
		return fmt.Errorf("%w: %s is not owned by the caller", ErrUnsafeFile, path)
	}
	if extra := st.Mode & 0o7777 &^ 0o600; extra != 0 {
		return fmt.Errorf("%w: %s has mode %04o, wider than 0600", ErrUnsafeFile, path, st.Mode&0o7777)
	}
	return nil
}

// hintAt reads the pid hint through the descriptor without moving its offset.
// It returns "" unless the text is a short run of decimal digits, so a hint
// that reaches a message cannot carry control characters.
func hintAt(f *os.File) (string, error) {
	buf := make([]byte, maxHintBytes)
	n, err := f.ReadAt(buf, 0)
	if n == 0 {
		return "", err
	}
	text := strings.TrimSpace(string(buf[:n]))
	if text == "" || len(text) > 10 {
		return "", nil
	}
	for _, c := range text {
		if c < '0' || c > '9' {
			return "", nil
		}
	}
	return text, nil
}

// retry repeats fn while it fails with EINTR.
func retry(fn func() error) error {
	for {
		err := fn()
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
