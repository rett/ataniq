package switcher

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// PackageState is unknown when a package step did not finish (it
	// failed or was interrupted): the host is between PackageFrom and
	// PackageTo, and package_snapshot is not to be relied on. Empty when
	// known. PackageStatedAt is when the owner last stated the snapshot
	// (recover --package-state).
	PackageState    string `json:"package_state"`
	PackageFrom     string `json:"package_from"`
	PackageTo       string `json:"package_to"`
	PackageStatedAt string `json:"package_stated_at"`
	RestoredHealth  string `json:"restored_health"`  // after going back: ok only when the links, the reload and the health all succeeded; failed; empty otherwise
	ServicesStopped bool   `json:"services_stopped"` // the decision services were stopped (every stop command passed, or the owner said so)
	// The transaction this record ends: its id, what was asked (an id, or
	// previous) and, when a later command settled it, that nobody was shown
	// its outcome yet: the same request run again reports it once.
	Txn           string `json:"txn"`
	Requested     string `json:"requested"`
	PendingReport bool   `json:"pending_report"`
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

// maxLockFile bounds the lock file, which the switch creates empty.
const maxLockFile = 64

// stateFileTrusted reports why info, a file of the state folder, cannot be
// trusted: it must be a regular file with one link, not writable by its
// group or by others, of an owner the owner check accepts. The state folder
// is checked, but a file that was already in it when it was made safe (a
// migrated folder) is checked here.
func (s *Switcher) stateFileTrusted(name string, info fs.FileInfo) error {
	if err := s.trusted(filepath.Join(s.stateDir(), name), info, false); err != nil {
		return err
	}
	if links(info) != 1 {
		return failf(CodeUntrusted, "%s has more than one link", q(name))
	}
	return nil
}

// readStateFile reads the file name of the state folder, of at most max
// bytes, after checking it with stateFileTrusted. A missing file is
// fs.ErrNotExist.
func (s *Switcher) readStateFile(name string, max int64) ([]byte, error) {
	f, info, err := openFileIn(s.stateRoot, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := s.stateFileTrusted(name, info); err != nil {
		return nil, err
	}
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

// readRecord reads deploy.json from the state folder; a missing file is an
// empty record.
func (s *Switcher) readRecord() (Record, bool, error) {
	data, err := s.readStateFile(DeployFile, maxStateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	var rec Record
	if err := decodeStrict(data, &rec); err != nil {
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
func (s *Switcher) readHighest() (n uint64, present bool, err error) {
	data, err := s.readStateFile(HighestFile, 64)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		if code, _ := codeOf(err); code == CodeUntrusted {
			return 0, false, err
		}
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
// nobody holds it, and nothing is created. check, when given, is asked
// about the opened lock file before it is locked: a lock file another
// account can write or hold is no lock.
func takeLock(state *os.Root, create, shared bool, check func(fs.FileInfo) error) (*lock, error) {
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
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, failf(CodeStateInvalid, "%s is not a regular file", LockFile)
	}
	if check != nil {
		if err := check(info); err != nil {
			f.Close()
			return nil, err
		}
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
