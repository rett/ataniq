package switcher

import (
	"encoding/json"
	"errors"
	"fmt"
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
	Result          string `json:"result"`           // ok, failed, rolled-back or failed-recovery
	Check           string `json:"check"`            // the no-AI check: passed, failed or not run
	FailedStep      string `json:"failed_step"`      // empty when ok
	Error           string `json:"error"`            // a failure code; empty when ok
	Detail          string `json:"detail"`           // fixed words and quoted identifiers; empty when ok
	PackageSnapshot string `json:"package_snapshot"` // the snapshot the package step last brought the host to
	RestoredHealth  string `json:"restored_health"`  // after going back: ok only when the links, the reload and the health all succeeded; failed; empty otherwise
}

// Results.
const (
	ResultOK         = "ok"
	ResultFailed     = "failed"
	ResultRolledBack = "rolled-back"
	// ResultFailedRecovery: the switch failed and no healthy release could be
	// gone back to; the decision services were stopped.
	ResultFailedRecovery = "failed-recovery"
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

// writeJSONIn writes v as JSON to the state file name of r, whole (see
// writeFileIn). What does not fit maxStateFile, the bound its reader takes,
// is refused before anything is written: a state file the switch wrote can
// always be read back by it.
func writeJSONIn(r *os.Root, name string, v any, perm fs.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maxStateFile {
		return fmt.Errorf("%s would be %d bytes, more than the %d its reader takes", name, len(data), maxStateFile)
	}
	return writeFileIn(r, name, data, perm)
}

// readHighest reads the highest-counter file of the state folder: the
// highest counter a switch ever attempted. A missing file is 0, not present.
// Anything else that is not one decimal number is an error, never a guess,
// and its text is not repeated.
func readHighest(state *os.Root) (n uint64, present bool, err error) {
	data, err := readFileIn(state, HighestFile, 64)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, failf(CodeCounterInvalid, "%s cannot be read; run ah-switch recover --counter N", HighestFile)
	}
	n, err = strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0, false, failf(CodeCounterInvalid, "%s does not hold a counter; run ah-switch recover --counter N", HighestFile)
	}
	return n, true, nil
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
	return syncLinkDir(r)
}

// syncLinkDir syncs the folder of a link after its rename; a variable so that
// a test can make it fail after the rename succeeded.
var syncLinkDir = syncRoot

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
