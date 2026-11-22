package switcher

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
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
	Mode            string `json:"mode"`             // manual or signed: how the release was verified
	Trust           string `json:"trust"`            // the switch policy's minimum mode; manual without a policy
	Rollback        bool   `json:"rollback"`         // asked as --rollback-to previous
	Time            string `json:"time"`             // when it ended, RFC 3339, UTC
	Result          string `json:"result"`           // ok, failed or rolled-back
	Check           string `json:"check"`            // the no-AI check: passed, failed or not run
	FailedStep      string `json:"failed_step"`      // empty when ok
	Error           string `json:"error"`            // a failure code; empty when ok
	Detail          string `json:"detail"`           // fixed words and quoted identifiers; empty when ok
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

// maxStateFile bounds the state files the switch reads.
const maxStateFile = 1 << 20

// readRecord reads deploy.json from the state folder; a missing file is an
// empty record.
func readRecord(state *os.Root) (Record, bool, error) {
	data, err := readFileIn(state, DeployFile, maxStateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return Record{}, false, failf(CodeStateInvalid, "%s does not parse", DeployFile)
	}
	return rec, true, nil
}

// readHighest reads the highest-counter file of the state folder; a missing
// file is 0. Anything else that is not one decimal number is an error, never
// a guess, and its text is not repeated.
func readHighest(state *os.Root) (uint64, error) {
	data, err := readFileIn(state, HighestFile, 64)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, failf(CodeCounterInvalid, "%s cannot be read", HighestFile)
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, failf(CodeCounterInvalid, "%s does not hold a counter", HighestFile)
	}
	return n, nil
}

// readLink reads the symbolic link name of the folder r; a missing name is
// not an error, a name that is something other than a symbolic link is.
func readLink(r *os.Root, name string) (target string, exists bool, err error) {
	info, err := r.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		return "", false, failf(CodeStateInvalid, "%s is not a symbolic link", name)
	}
	target, err = r.Readlink(name)
	if err != nil {
		return "", false, err
	}
	return target, true, nil
}

// replaceLink points name of the folder r at target without a moment where
// name is missing: it makes a new link next to it and renames that over it,
// then syncs the folder.
func replaceLink(r *os.Root, name, target string) error {
	tmp := name + ".new"
	if err := r.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := r.Symlink(target, tmp); err != nil {
		return err
	}
	if err := r.Rename(tmp, name); err != nil {
		r.Remove(tmp)
		return err
	}
	return syncRoot(r)
}

// lock is the switch lock, a flock on a file in the state folder.
type lock struct{ f *os.File }

var errLocked = errors.New("another ah-switch holds the lock")

// takeLock takes the lock of the state folder without waiting: exclusive for
// a switch, shared for status. With create false a missing lock file means
// nobody holds it, and nothing is created.
func takeLock(state *os.Root, create, shared bool) (*lock, error) {
	flags := os.O_RDONLY | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	if create {
		flags = os.O_RDWR | os.O_CREATE | syscall.O_NOFOLLOW | syscall.O_NONBLOCK
	}
	f, err := state.OpenFile(LockFile, flags, 0o600)
	if err != nil {
		if !create && errors.Is(err, fs.ErrNotExist) {
			return &lock{}, nil
		}
		return nil, err
	}
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, failf(CodeStateInvalid, "%s is not a regular file", LockFile)
	}
	how := syscall.LOCK_EX
	if shared {
		how = syscall.LOCK_SH
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
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
