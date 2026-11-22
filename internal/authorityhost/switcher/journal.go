package switcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
)

// JournalFile is the journal of a switch in the state folder: written, and
// synced with its folder, before any link is touched, and removed once the
// outcome is recorded. A journal that is still there when a command starts
// is a switch that was interrupted.
const JournalFile = "txn.json"

// Journal phases.
const (
	PhaseLinks = "links" // the links may be changing: the counter is written
)

// journal is the content of txn.json.
type journal struct {
	ID          string `json:"id"`           // the release switched to
	Counter     uint64 `json:"counter"`      // its counter
	Rollback    bool   `json:"rollback"`     // a --rollback-to
	OldCurrent  string `json:"old_current"`  // what current pointed at, as written; empty: none
	OldPrevious string `json:"old_previous"` // what previous pointed at; empty: none
	Phase       string `json:"phase"`
}

// readJournal reads txn.json; a missing file is no journal. A journal that
// does not parse, or whose names are not what a switch writes, is an error.
func (s *Switcher) readJournal() (journal, bool, error) {
	data, err := readFileIn(s.stateRoot, JournalFile, maxStateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return journal{}, false, nil
	}
	if err != nil {
		return journal{}, false, err
	}
	var j journal
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&j); err != nil {
		return journal{}, false, failf(CodeStateInvalid, "%s does not parse", JournalFile)
	}
	id, err := ParseID(j.ID)
	if err != nil || id.Counter != j.Counter || j.Phase != PhaseLinks {
		return journal{}, false, failf(CodeStateInvalid, "%s does not describe a switch", JournalFile)
	}
	for _, t := range []string{j.OldCurrent, j.OldPrevious} {
		if t != "" {
			if _, err := s.targetID(t); err != nil {
				return journal{}, false, failf(CodeStateInvalid, "%s names a link target that is not a release", JournalFile)
			}
		}
	}
	return j, true, nil
}

// writeJournal writes txn.json whole and syncs the state folder.
func (s *Switcher) writeJournal(j journal) error {
	data, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	return writeFileIn(s.stateRoot, JournalFile, append(data, '\n'), 0o600)
}

func (s *Switcher) dropJournal() error {
	return removeFileIn(s.stateRoot, JournalFile)
}

// crash is called at each point a switch could be interrupted; a test makes
// it return an error, and the switch then stops there as if the process had
// died: nothing is put back and nothing is recorded.
func (s *Switcher) crash(point string) bool {
	return s.crashAt != nil && s.crashAt(point) != nil
}

// The points crash is called at, in their order.
const (
	CrashJournal  = "journal"  // txn.json written
	CrashCounter  = "counter"  // highest-counter written
	CrashPrevious = "previous" // previous written
	CrashCurrent  = "current"  // current written
	CrashHealth   = "health"   // the health check passed
)

// highestAtLeast writes highest-counter as n when it holds less: a counter
// that a switch attempted stays used.
func (s *Switcher) highestAtLeast(n uint64) error {
	cur, present, err := readHighest(s.stateRoot)
	if err == nil && present && cur >= n {
		return nil
	}
	return writeFileIn(s.stateRoot, HighestFile, []byte(fmt.Sprintf("%d\n", n)), 0o644)
}

// reconcile settles a journal left by an interrupted switch, under the lock,
// before the command does anything else. A dry run reconciles nothing and
// stops.
//
//   - current still points where it pointed before the switch: no service
//     was reloaded yet, so previous is put back exactly and the outcome is
//     recorded as rolled-back with the code interrupted;
//   - current already points at the journal's release: when that release
//     verifies, reloads and is healthy, the switch is completed and recorded
//     as ok; otherwise the switch goes back as a failed health check does
//     (to the old release, verified, with its own reload and health lists,
//     or, failing that, stopping the decision services);
//   - current points anywhere else: nothing is touched and the command
//     fails, leaving the journal for a person to look at.
//
// Afterwards highest-counter is at least the journal's counter and, unless
// current could not be put back, the journal is removed.
func (s *Switcher) reconcile(ctx context.Context) (int, bool) {
	if s.stateRoot == nil {
		return 0, false
	}
	j, ok, err := s.readJournal()
	if err != nil {
		s.printf("reconcile: FAILED: %s; look at %s by hand", describe(err), q(filepath.Join(s.stateDir(), JournalFile)))
		return ExitFailed, true
	}
	if !ok {
		return 0, false
	}
	kind := "switch to"
	if j.Rollback {
		kind = "rollback to"
	}
	s.printf("reconcile: an interrupted %s %s was found", kind, j.ID)
	if s.cfg.Dry {
		s.printf("reconcile: a dry run reconciles nothing; run the command without --dry first")
		return ExitFailed, true
	}
	last, _, _ := readRecord(s.stateRoot)
	a := &attempt{
		rec: Record{
			ID:              j.ID,
			Counter:         j.Counter,
			Mode:            s.mode,
			Trust:           s.policy.Trust(),
			Rollback:        j.Rollback,
			Check:           CheckNotRun,
			PackageSnapshot: last.PackageSnapshot,
		},
		oldTarget:  j.OldCurrent,
		oldExists:  j.OldCurrent != "",
		prevTarget: j.OldPrevious,
		prevExists: j.OldPrevious != "",
	}
	if a.oldExists {
		id, _ := s.targetID(j.OldCurrent)
		a.rec.PreviousID = id.Name
	}
	target := filepath.Join("releases", j.ID)
	cur, curOK, err := readLink(s.baseRoot, "current")
	if err != nil {
		s.printf("reconcile: FAILED: current: %s", describe(err))
		return ExitFailed, true
	}
	if !j.Rollback {
		if err := s.highestAtLeast(j.Counter); err != nil {
			s.printf("reconcile: FAILED: %s not written: %s; the journal stays", HighestFile, q(err.Error()))
			return ExitFailed, true
		}
	}
	switch {
	case curOK && filepath.Clean(cur) == target:
		a.rec.CurrentID = j.ID
		err := s.completeInterrupted(ctx, a, j)
		if err == nil {
			return 0, false
		}
		s.printf("reconcile: %s is current but cannot be completed: %s", j.ID, describe(err))
		s.openFallback(ctx, a)
		defer func() {
			if a.fallback != nil {
				a.fallback.Close()
			}
		}()
		s.goBack(ctx, a, "interrupted", failf(CodeInterrupted, "the switch was interrupted after current was moved and could not be completed: %s", describe(err)))
		if _, still, _ := s.readJournal(); still {
			return ExitFailed, true
		}
		return 0, false
	case curOK != a.oldExists || (curOK && filepath.Clean(cur) != filepath.Clean(j.OldCurrent)):
		s.printf("reconcile: FAILED: current points at neither the old nor the new release; the journal stays")
		return ExitFailed, true
	}
	// current never moved: no service was touched.
	if err := s.putPrevious(a); err != nil {
		s.printf("reconcile: FAILED: previous could not be put back: %s; the journal stays", describe(err))
		return ExitFailed, true
	}
	a.rec.FailedStep = "interrupted"
	a.rec.Error = CodeInterrupted
	a.rec.Detail = "the switch was interrupted before current was moved; previous was put back"
	a.rec.CurrentID = a.rec.PreviousID
	a.rec.Result = ResultRolledBack
	if !a.oldExists {
		a.rec.Result = ResultFailed
		a.rec.Detail = "the first switch was interrupted before current was made"
	}
	if err := s.record(a); err != nil {
		return ExitFailed, true
	}
	if err := s.dropJournal(); err != nil {
		s.printf("reconcile: FAILED: %s not removed: %s", JournalFile, q(err.Error()))
		return ExitFailed, true
	}
	s.printf("reconcile: put back: current -> %s, previous -> %s", linkText(j.OldCurrent), linkText(j.OldPrevious))
	return 0, false
}

func linkText(target string) string {
	if target == "" {
		return "none"
	}
	return q(target)
}

// completeInterrupted completes an interrupted switch whose current link is
// already moved: the release must verify as the policy says, its reload list
// must pass and it must be healthy. Then the outcome is recorded as ok and
// the journal is removed. When the release verifies, its stop list is kept
// in a, whatever happens next.
func (s *Switcher) completeInterrupted(ctx context.Context, a *attempt, j journal) error {
	id, _ := ParseID(j.ID)
	s.budget = &budget{lim: s.cfg.Limits, deadline: s.cfg.Now().Add(s.cfg.Limits.Time), now: s.cfg.Now}
	rel, err := openRelease(id, s.relRoot, id.Name, filepath.Join(s.base(), "releases", id.Name), s.entryTrusted, s.budget)
	if err != nil {
		return err
	}
	defer rel.Close()
	if err := s.checkRelease(ctx, rel); err != nil {
		return err
	}
	a.reloads, a.health, a.stops = rel.reloads, rel.health, rel.stops
	if err := s.reload(ctx, a.reloads); err != nil {
		return err
	}
	if err := s.checkHealth(ctx, a.health); err != nil {
		return err
	}
	a.rec.Result = ResultOK
	a.rec.Detail = "completed after an interruption"
	if err := s.record(a); err != nil {
		return failf(CodeRecordFailed, "%s not written", DeployFile)
	}
	if err := s.dropJournal(); err != nil {
		return failf(CodeRecordFailed, "%s not removed: %s", JournalFile, q(err.Error()))
	}
	s.printf("reconcile: completed: current -> %s", j.ID)
	return nil
}

// Recover writes highest-counter as counter when it is missing or does not
// hold a counter, on a host whose current link shows that releases were
// switched to: the switch refuses to guess it. counter must be at least the
// counter of the releases current and previous point at (a journal is
// settled first, which writes the counter itself).
func (s *Switcher) Recover(ctx context.Context, counter uint64) int {
	if err := s.checkConfig(); err != nil {
		s.errorf("%v", err)
		return ExitUsage
	}
	if s.cfg.From != "" {
		s.errorf("recover takes no --from")
		return ExitUsage
	}
	if code, done := s.applyPolicy(); done {
		return code
	}
	s.printf("ah-switch: recover --counter %d (root %s)", counter, q(s.cfg.Root))
	lk, code := s.lock(false)
	if lk == nil {
		return code
	}
	defer lk.release()
	defer s.closeRoots()
	if s.stateRoot == nil {
		s.printf("recover: no state folder; nothing to recover")
		return ExitFailed
	}
	a := &attempt{}
	if code, done := s.openBase(a); done {
		return code
	}
	if code, done := s.reconcile(ctx); done {
		return code
	}
	if n, present, err := readHighest(s.stateRoot); err == nil && present {
		s.printf("recover: %s holds %d; nothing to recover", HighestFile, n)
		return ExitFailed
	}
	least := uint64(0)
	for _, name := range []string{"current", "previous"} {
		if t, ok, err := readLink(s.baseRoot, name); err == nil && ok {
			if id, err := s.targetID(t); err == nil {
				least = max(least, id.Counter)
			}
		}
	}
	if counter < least {
		s.printf("recover: FAILED: --counter %d is below %d, the counter of a release this host already used", counter, least)
		return ExitFailed
	}
	if s.cfg.Dry {
		s.printf("recover: would write %s as %d", HighestFile, counter)
		return ExitOK
	}
	if err := writeFileIn(s.stateRoot, HighestFile, []byte(fmt.Sprintf("%d\n", counter)), 0o644); err != nil {
		s.printf("recover: FAILED: %s not written: %s", HighestFile, q(err.Error()))
		return ExitFailed
	}
	s.printf("recover: %s is now %d", HighestFile, counter)
	return ExitOK
}
