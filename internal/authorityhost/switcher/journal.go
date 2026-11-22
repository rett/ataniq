package switcher

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"path"
	"regexp"
)

// JournalFile is the journal of a switch in the state folder: one
// transaction, written whole and synced with its folder before every change
// of its phase, and removed once its outcome is recorded. A journal that is
// still there when a command starts is a transaction that did not finish;
// the command resumes it from its phase before it does anything else.
const JournalFile = "txn.json"

// Journal phases. A transaction moves through them in this order, skipping
// what does not apply: packages (only when the package snapshot changes),
// links, activating, then done; or, after a failure, restoring and stopping
// before done. Each phase is resumed by doing what the phase says, from its
// start, which is safe to do twice; where the links point never decides what
// happens.
const (
	PhasePackages   = "packages"   // the package step runs or ran; no link was touched
	PhaseLinks      = "links"      // the counter is used and the links are being moved; nothing reloaded yet. Resumed by putting the links back
	PhaseActivating = "activating" // the links are to point at the new release; its reload and health run. Resumed by running them again
	PhaseRestoring  = "restoring"  // going back: the links are being put back and the old release reloaded and checked
	PhaseStopping   = "stopping"   // the decision services are being stopped
	PhaseDone       = "done"       // the outcome is decided and is being recorded
)

// Directions of a transaction.
const (
	DirForward  = "forward"  // a switch to a new release
	DirRollback = "rollback" // --rollback-to previous
)

// Package step progress in the journal.
const (
	PackageNone    = "none"    // the snapshot does not change
	PackageStarted = "started" // the package step was started
	PackageDone    = "done"    // the package step brought the host to PackageTo
)

// journal is the content of txn.json.
type journal struct {
	Txn         string `json:"txn"`       // the transaction's id, 32 hex digits
	Direction   string `json:"direction"` // forward or rollback
	Requested   string `json:"requested"` // what was asked: a release id, or previous
	ID          string `json:"id"`        // the release switched to
	Counter     uint64 `json:"counter"`   // its counter
	OldCurrent  string `json:"old_current"`
	OldPrevious string `json:"old_previous"`
	// The trust the transaction began with: what it is verified with
	// whenever it is resumed, together with the policy of that time.
	TrustMode      string `json:"trust_mode"`
	TrustSigners   string `json:"trust_signers"`
	TrustPrincipal string `json:"trust_principal"`
	Phase          string `json:"phase"`
	CounterUsed    bool   `json:"counter_used"` // highest-counter must be at least Counter (a forward switch that reached its links)
	// The package step of this transaction.
	PackageFrom  string `json:"package_from"`
	PackageTo    string `json:"package_to"`
	PackageState string `json:"package_state"`
	// The package step's process (the leader of its process group) and its
	// start time, once it runs: a resume makes sure it has ended.
	HelperPID   int    `json:"helper_pid"`
	HelperStart uint64 `json:"helper_start"`
	// The outcome, once a step failed or the release was activated.
	Result         string `json:"result"`
	FailedStep     string `json:"failed_step"`
	Error          string `json:"error"`
	Detail         string `json:"detail"`
	Check          string `json:"check"`
	CheckProgram   string `json:"check_program"` // the no-AI check the post-activation check runs
	RestoredHealth string `json:"restored_health"`
	Stopped        bool   `json:"stopped"`       // every stop command succeeded, or the owner stated the services are stopped
	StopFailures   string `json:"stop_failures"` // the stop commands that failed last time
}

var txnPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// newTxnID returns a new transaction id.
func newTxnID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func oneOf(s string, values ...string) bool {
	for _, v := range values {
		if s == v {
			return true
		}
	}
	return false
}

// readJournal reads txn.json; a missing file is no journal. A journal that
// is not exactly what a switch writes is an error: it is never guessed at.
func (s *Switcher) readJournal() (journal, bool, error) {
	data, err := s.readStateFile(JournalFile, maxStateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return journal{}, false, nil
	}
	if err != nil {
		return journal{}, false, err
	}
	var j journal
	if err := decodeStrict(data, &j); err != nil {
		return journal{}, false, failf(CodeStateInvalid, "%s does not parse", JournalFile)
	}
	if err := s.checkJournal(j); err != nil {
		return journal{}, false, failf(CodeStateInvalid, "%s does not describe a transaction: %s", JournalFile, err.Error())
	}
	return j, true, nil
}

// checkJournal reports what in j is not what a switch writes; its text
// holds no value of j.
func (s *Switcher) checkJournal(j journal) error {
	id, err := ParseID(j.ID)
	switch {
	case !txnPattern.MatchString(j.Txn):
		return errors.New("txn")
	case !oneOf(j.Direction, DirForward, DirRollback):
		return errors.New("direction")
	case err != nil || id.Counter != j.Counter:
		return errors.New("id or counter")
	case j.Requested != "previous" && j.Requested != j.ID:
		return errors.New("requested")
	case j.Requested == "previous" && j.Direction != DirRollback:
		return errors.New("requested")
	case !oneOf(j.Phase, PhasePackages, PhaseLinks, PhaseActivating, PhaseRestoring, PhaseStopping, PhaseDone):
		return errors.New("phase")
	case !oneOf(j.PackageState, PackageNone, PackageStarted, PackageUnknown, PackageDone):
		return errors.New("package_state")
	case j.PackageState != PackageNone && (!ValidSnapshot(j.PackageTo) || (j.PackageFrom != "" && !ValidSnapshot(j.PackageFrom))):
		return errors.New("package snapshots")
	case !oneOf(j.Result, "", ResultOK, ResultFailed, ResultRolledBack, ResultFailedRecovery):
		return errors.New("result")
	case !oneOf(j.Check, CheckPassed, CheckFailed, CheckNotRun):
		return errors.New("check")
	case !oneOf(j.RestoredHealth, "", "ok", "failed"):
		return errors.New("restored_health")
	case j.Phase == PhaseDone && j.Result == "":
		return errors.New("a done phase without a result")
	case j.HelperPID < 0 || (j.HelperPID > 0 && j.PackageState == PackageNone):
		return errors.New("helper_pid")
	case j.CheckProgram != "" && (!path.IsAbs(j.CheckProgram) || path.Clean(j.CheckProgram) != j.CheckProgram):
		return errors.New("check_program")
	case j.CounterUsed && j.Direction != DirForward:
		return errors.New("counter_used")
	}
	switch j.TrustMode {
	case ModeManual:
		if j.TrustSigners != "" || j.TrustPrincipal != "" {
			return errors.New("trust")
		}
	case ModeSigned:
		if !principalPattern.MatchString(j.TrustPrincipal) || !path.IsAbs(j.TrustSigners) || path.Clean(j.TrustSigners) != j.TrustSigners || j.TrustSigners == "/" {
			return errors.New("trust")
		}
	default:
		return errors.New("trust")
	}
	for _, t := range []string{j.OldCurrent, j.OldPrevious} {
		if t != "" {
			if _, err := s.targetID(t); err != nil {
				return errors.New("an old link target")
			}
		}
	}
	return nil
}

// writeJournal writes txn.json whole and syncs the state folder.
func (s *Switcher) writeJournal(j journal) error {
	j.Detail = boundDetail(j.Detail)
	return writeJSONIn(s.stateRoot, JournalFile, j, 0o600)
}

func (s *Switcher) dropJournal() error {
	return removeFileIn(s.stateRoot, JournalFile)
}

// errCrashed stops a transaction where a test made it crash.
var errCrashed = errors.New("crashed (test)")

// crash is called at each point a switch could be interrupted; a test makes
// it return an error, and the switch then stops there as if the process had
// died: nothing is put back, recorded or removed.
func (s *Switcher) crash(point string) error {
	if s.crashAt != nil && s.crashAt(point) != nil {
		return errCrashed
	}
	return nil
}

// The points crash is called at: between every two durable steps of a
// transaction, in their order.
const (
	CrashPackageStart  = "package-start"  // txn.json says packages/started; the package step has not run
	CrashPackageRan    = "package-ran"    // the package step exited 0; txn.json still says started
	CrashPackageDone   = "package-done"   // txn.json says packages/done
	CrashLinks         = "links"          // txn.json says links
	CrashCounter       = "counter"        // highest-counter written
	CrashPrevious      = "previous"       // previous written
	CrashCurrent       = "current"        // current written
	CrashActivating    = "activating"     // txn.json says activating
	CrashReload        = "reload"         // the new release's reload list ran
	CrashHealth        = "health"         // the new release is healthy
	CrashPostCheck     = "postcheck"      // the post-activation no-AI check passed
	CrashPutBack       = "put-back"       // the links of a links phase are put back
	CrashRestoring     = "restoring"      // txn.json says restoring
	CrashRestoreLinks  = "restore-links"  // current and previous are back
	CrashRestoreReload = "restore-reload" // the old release's reload list ran
	CrashStopping      = "stopping"       // txn.json says stopping
	CrashStopped       = "stopped"        // every stop command ran; txn.json still says stopping
	CrashDone          = "done"           // txn.json says done
	CrashRecord        = "record"         // deploy.json written; txn.json still there
)
