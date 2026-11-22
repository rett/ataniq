package switcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Record is the content of deploy.json: the outcome of the last switch.
type Record struct {
	ID              string `json:"id"`               // the release the switch was asked for
	PreviousID      string `json:"previous_id"`      // what current pointed at before it
	CurrentID       string `json:"current_id"`       // what current points at after it
	Counter         uint64 `json:"counter"`          // the counter of id
	Mode            string `json:"mode"`             // manual or signed
	Rollback        bool   `json:"rollback"`         // asked as --rollback-to previous
	Time            string `json:"time"`             // when it ended, RFC 3339, UTC
	Result          string `json:"result"`           // ok, failed or rolled-back
	Check           string `json:"check"`            // the no-AI check: passed, failed or not run
	FailedStep      string `json:"failed_step"`      // empty when ok
	Error           string `json:"error"`            // empty when ok
	PackageSnapshot string `json:"package_snapshot"` // the snapshot the package step last brought the host to
	RestoredHealth  string `json:"restored_health"`  // after going back: ok or failed; empty otherwise
}

// Results.
const (
	ResultOK         = "ok"
	ResultFailed     = "failed"
	ResultRolledBack = "rolled-back"
)

// Check results.
const (
	CheckPassed = "passed"
	CheckFailed = "failed"
	CheckNotRun = "not run"
)

// readRecord reads deploy.json; a missing file is an empty record.
func readRecord(path string) (Record, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return rec, true, nil
}

// readHighest reads the highest-counter file; a missing file is 0. Anything
// else that is not one decimal number is an error, never a guess.
func readHighest(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s does not hold a counter: %q", path, strings.TrimSpace(string(data)))
	}
	return n, nil
}

// writeAtomic writes a file by writing a temporary file in the same folder,
// syncing it and renaming it over the name, then syncing the folder.
func writeAtomic(path string, data []byte, perm fs.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmp)
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
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// readLink reads a symbolic link; a missing name is not an error, a name that
// is something other than a symbolic link is.
func readLink(path string) (target string, exists bool, err error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return "", false, fmt.Errorf("%s is not a symbolic link", path)
	}
	target, err = os.Readlink(path)
	if err != nil {
		return "", false, err
	}
	return target, true, nil
}

// replaceLink points name at target without a moment where name is missing:
// it makes a new link next to it and renames that over it.
func replaceLink(name, target string) error {
	tmp := name + ".new"
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(name))
}

// lock is the switch lock, a flock on a file in the state folder.
type lock struct{ f *os.File }

var errLocked = errors.New("another ah-switch holds the lock")

// takeLock takes the lock without waiting. With create false (a dry run) a
// missing lock file means nobody holds it, and nothing is created.
func takeLock(path string, create bool) (*lock, error) {
	flags := os.O_RDONLY | syscall.O_NOFOLLOW
	if create {
		flags = os.O_RDWR | os.O_CREATE | syscall.O_NOFOLLOW
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		if !create && errors.Is(err, fs.ErrNotExist) {
			return &lock{}, nil
		}
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}
		return nil, err
	}
	return &lock{f: f}, nil
}

func (l *lock) release() {
	if l != nil && l.f != nil {
		l.f.Close()
	}
}
