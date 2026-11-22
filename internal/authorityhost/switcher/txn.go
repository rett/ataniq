package switcher

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

// txn is one transaction being driven: its journal, and the releases it
// needs, opened and verified once, with the trust the journal names, when
// they are first needed.
type txn struct {
	j journal
	// resumed: driven by a later command than the one that began it; the
	// outcome is then kept for a retry of the same request to report.
	resumed bool
	cand    *release // the release switched to
	candErr error
	candSet bool
	fb      *release // the release current pointed at before, to go back to
	fbErr   error
	fbSet   bool
	owned   []*release // opened by the txn, closed by close
}

func (t *txn) close() {
	for _, r := range t.owned {
		r.Close()
	}
	t.owned = nil
}

func (t *txn) forward() bool { return t.j.Direction == DirForward }

// kind is how the transaction is named in a line.
func (t *txn) kind() string {
	if t.forward() {
		return "switch to"
	}
	return "rollback to"
}

// errNoFallback: the transaction has no earlier release to go back to.
var errNoFallback = errors.New("no earlier release")

// openVerified opens releases/<id> and verifies it as the switch policy and
// the transaction's trust say.
func (s *Switcher) openVerified(ctx context.Context, id ReleaseID) (*release, error) {
	b := &budget{lim: s.cfg.Limits, deadline: s.cfg.Now().Add(s.cfg.Limits.Time), now: s.cfg.Now}
	rel, err := openRelease(id, s.relRoot, id.Name, filepath.Join(s.base(), "releases", id.Name), s.entryTrusted, b)
	if err != nil {
		return nil, err
	}
	if err := s.checkRelease(ctx, rel); err != nil {
		rel.Close()
		return nil, err
	}
	if err := s.checkProvenance(rel); err != nil {
		rel.Close()
		return nil, err
	}
	return rel, nil
}

// candidate is the transaction's release, verified.
func (s *Switcher) candidate(ctx context.Context, t *txn) (*release, error) {
	if !t.candSet {
		t.candSet = true
		id, _ := ParseID(t.j.ID)
		t.cand, t.candErr = s.openVerified(ctx, id)
		if t.cand != nil {
			t.owned = append(t.owned, t.cand)
		}
	}
	return t.cand, t.candErr
}

// fallback is the release current pointed at before the transaction,
// verified; errNoFallback when there was none.
func (s *Switcher) fallback(ctx context.Context, t *txn) (*release, error) {
	if !t.fbSet {
		t.fbSet = true
		if t.j.OldCurrent == "" {
			t.fbErr = errNoFallback
		} else {
			id, _ := s.targetID(t.j.OldCurrent)
			t.fb, t.fbErr = s.openVerified(ctx, id)
			if t.fb != nil {
				t.owned = append(t.owned, t.fb)
			}
		}
	}
	return t.fb, t.fbErr
}

// save writes the journal: every change of phase is durable before
// anything of the next phase is done.
func (s *Switcher) save(t *txn) error {
	if err := s.writeJournal(t.j); err != nil {
		return failf(CodeRecordFailed, "%s not written: %s", JournalFile, q(err.Error()))
	}
	return nil
}

// ensureLink makes the link name of opt/authority-host point at target, or
// removes it when target is empty, and syncs the folder either way: a link
// that already points there is only synced, so that a change a crash left
// visible but not durable becomes durable. The rename or the sync failing is
// an error: a link is only taken as moved once its folder is synced.
func (s *Switcher) ensureLink(name, target string) error {
	cur, ok, err := readLink(s.baseRoot, name)
	if err != nil {
		return err
	}
	switch {
	case target == "" && !ok, ok && cur == target:
		return syncLinkDir(s.baseRoot)
	case target == "":
		if err := s.baseRoot.Remove(name); err != nil {
			return err
		}
		return syncLinkDir(s.baseRoot)
	}
	return replaceLink(s.baseRoot, name, target)
}

// drive runs the transaction from its phase until it is settled (its
// outcome recorded and its journal removed) or cannot go on: a crash, or a
// step that left it unresolved, which keeps the journal for the next
// command. It returns the exit code and whether the transaction settled.
func (s *Switcher) drive(ctx context.Context, t *txn) (int, bool) {
	for {
		var err error
		switch t.j.Phase {
		case PhasePackages:
			err = s.resumePackages(t)
		case PhaseLinks:
			err = s.putBack(t, "interrupted", failf(CodeInterrupted, "the %s %s was interrupted before it was activated", t.kind(), t.j.ID))
		case PhaseActivating:
			err = s.activate(ctx, t)
		case PhaseRestoring:
			err = s.restore(ctx, t)
		case PhaseStopping:
			err = s.stop(ctx, t)
		case PhaseDone:
			return s.settle(t)
		}
		if err != nil {
			if !errors.Is(err, errCrashed) {
				s.printf("result: unresolved: %s; %s stays in phase %s, and the next switch, rollback or recover resumes it", describe(err), JournalFile, t.j.Phase)
			}
			return ExitFailed, false
		}
	}
}

// fail sets the outcome of a failure at step.
func (t *txn) fail(step string, cause error) {
	t.j.FailedStep = step
	t.j.Error, t.j.Detail = codeOf(cause)
}

// note adds words to the outcome's detail.
func (t *txn) note(format string, args ...any) {
	if t.j.Detail != "" {
		t.j.Detail += "; "
	}
	t.j.Detail += fmt.Sprintf(format, args...)
}

// done ends the transaction with result: it is recorded next.
func (s *Switcher) done(t *txn, result string) error {
	t.j.Result = result
	t.j.Phase = PhaseDone
	if err := s.save(t); err != nil {
		return err
	}
	return s.crash(CrashDone)
}

// putBack is the resume of a links phase, and what a link that cannot be
// written in it leads to: current and previous are put back as the journal
// says they were (or removed where there was none), and synced. Nothing was
// reloaded in that phase, so nothing else is to undo; the counter stays
// used.
func (s *Switcher) putBack(t *txn, step string, cause error) error {
	if err := s.ensureLink("current", t.j.OldCurrent); err != nil {
		return failf(CodeLinkFailed, "current not put back: %s", q(err.Error()))
	}
	if err := s.ensureLink("previous", t.j.OldPrevious); err != nil {
		return failf(CodeLinkFailed, "previous not put back: %s", q(err.Error()))
	}
	if err := s.crash(CrashPutBack); err != nil {
		return err
	}
	t.fail(step, cause)
	t.note("current and previous were put back")
	s.printf("back: current -> %s, previous -> %s", linkText(t.j.OldCurrent), linkText(t.j.OldPrevious))
	result := ResultRolledBack
	if t.j.OldCurrent == "" {
		result = ResultFailed
	}
	return s.done(t, result)
}

func linkText(target string) string {
	if target == "" {
		return "none"
	}
	return q(target)
}

// activate is the activating phase: the links point at the new release
// (made so again, and synced, when resumed), its reload list runs and it
// must be healthy. A failure goes back (restoring), or stops the decision
// services when there is nothing to go back to.
func (s *Switcher) activate(ctx context.Context, t *txn) error {
	cand, err := s.candidate(ctx, t)
	if err != nil {
		// Only a resumed transaction gets here: the release was verified
		// before its links moved.
		return s.toRecovery(ctx, t, "interrupted", failf(CodeInterrupted, "the %s %s was interrupted and its release cannot be verified again: %s", t.kind(), t.j.ID, describe(err)))
	}
	prev := t.j.OldPrevious
	if t.j.OldCurrent != "" {
		prev = t.j.OldCurrent
	}
	if err := s.ensureLink("previous", prev); err != nil {
		return failf(CodeLinkFailed, "previous: %s", q(err.Error()))
	}
	if err := s.ensureLink("current", "releases/"+t.j.ID); err != nil {
		return failf(CodeLinkFailed, "current: %s", q(err.Error()))
	}
	if err := s.reload(ctx, cand.reloads); err != nil {
		return s.toRecovery(ctx, t, "reload", err)
	}
	if err := s.crash(CrashReload); err != nil {
		return err
	}
	if err := s.checkHealth(ctx, cand.health); err != nil {
		return s.toRecovery(ctx, t, stepNames[7], err)
	}
	if err := s.crash(CrashHealth); err != nil {
		return err
	}
	if err := s.postCheck(ctx, t.j.CheckProgram, cand.dir); err != nil {
		t.j.Check = CheckFailed
		return s.toRecovery(ctx, t, stepNames[8], err)
	}
	t.j.Check = CheckPassed
	if err := s.crash(CrashPostCheck); err != nil {
		return err
	}
	if t.resumed {
		t.note("completed after an interruption")
	}
	return s.done(t, ResultOK)
}

// toRecovery ends the activation that failed at step: the transaction goes
// back to the old release (restoring), or, on a first switch, stops the
// decision services.
func (s *Switcher) toRecovery(ctx context.Context, t *txn, step string, cause error) error {
	n := 7
	switch step {
	case "reload":
		n = 6
	case stepNames[8]:
		n = 8
	}
	s.step(n, "FAILED: %s", describe(cause))
	t.fail(step, cause)
	if t.j.OldCurrent == "" {
		t.j.Result = ResultFailed
		t.note("there is no earlier release to go back to, so current stays at %s", t.j.ID)
		t.j.Phase = PhaseStopping
	} else {
		t.j.Phase = PhaseRestoring
	}
	if err := s.save(t); err != nil {
		return err
	}
	if t.j.Phase == PhaseRestoring {
		return s.crash(CrashRestoring)
	}
	return s.crash(CrashStopping)
}

// restore is the restoring phase: the old release, verified as the
// transaction's trust says, is made current again with previous as it was,
// then its own reload list runs and it must be healthy. Anything of that
// failing, putting previous back included, is a failed recovery: the
// decision services are stopped.
func (s *Switcher) restore(ctx context.Context, t *txn) error {
	failed := func(format string, args ...any) error {
		t.j.Result = ResultFailedRecovery
		t.note(format, args...)
		t.j.Phase = PhaseStopping
		if err := s.save(t); err != nil {
			return err
		}
		return s.crash(CrashStopping)
	}
	fb, err := s.fallback(ctx, t)
	if err != nil {
		s.printf("back: FAILED: the earlier release cannot be used: %s", describe(err))
		return failed("the earlier release cannot be used (%s)", describe(err))
	}
	if err := s.ensureLink("current", t.j.OldCurrent); err != nil {
		t.j.RestoredHealth = "failed"
		return failed("going back failed: current: %s", q(err.Error()))
	}
	if err := s.ensureLink("previous", t.j.OldPrevious); err != nil {
		t.j.RestoredHealth = "failed"
		return failed("going back failed: previous: %s", q(err.Error()))
	}
	s.printf("back: current -> %s, previous -> %s", linkText(t.j.OldCurrent), linkText(t.j.OldPrevious))
	if err := s.crash(CrashRestoreLinks); err != nil {
		return err
	}
	if err := s.reload(ctx, fb.reloads); err != nil {
		t.j.RestoredHealth = "failed"
		return failed("going back failed: %s", describe(err))
	}
	if err := s.crash(CrashRestoreReload); err != nil {
		return err
	}
	if err := s.checkHealth(ctx, fb.health); err != nil {
		t.j.RestoredHealth = "failed"
		return failed("going back failed: %s", describe(err))
	}
	t.j.RestoredHealth = "ok"
	return s.done(t, ResultRolledBack)
}

// stop is the stopping phase: the host's own stop list runs, then the stop
// lists of the transaction's release and of the old release, each one
// that can be verified (the host's list is what does not depend on that).
// Only when every command exits 0 are the services taken as stopped and
// the transaction done; otherwise it stays in this phase, the command
// fails, and the next one runs the stops again.
func (s *Switcher) stop(ctx context.Context, t *txn) error {
	var failures []string
	run := func(list string, cmds [][]string) {
		for _, c := range cmds {
			if _, err := s.cfg.Runner.Run(ctx, Command{Argv: c, Timeout: ReloadTimeout}); err != nil {
				s.printf("stop: FAILED: %s: %s", qargv(c), summary(err))
				failures = append(failures, list+": "+q(c[0])+": "+summary(err))
				continue
			}
			s.printf("stop: ran %s", qargv(c))
		}
	}
	if stops, err := s.hostStopList(); err != nil {
		s.printf("stop: FAILED: the host's %s: %s", HostStopList, describe(err))
		failures = append(failures, "the host's "+HostStopList+": "+describe(err))
	} else {
		run("host", stops)
	}
	if cand, err := s.candidate(ctx, t); err == nil {
		run(t.j.ID, cand.stops)
	} else {
		s.printf("stop: the stop list of %s is not run: it cannot be verified (%s)", t.j.ID, describe(err))
	}
	if fb, err := s.fallback(ctx, t); err == nil {
		run(fb.id.Name, fb.stops)
	} else if !errors.Is(err, errNoFallback) {
		s.printf("stop: the stop list of the earlier release is not run: it cannot be verified (%s)", describe(err))
	}
	if err := s.crash(CrashStopped); err != nil {
		return err
	}
	if len(failures) > 0 {
		t.j.StopFailures = boundDetail(fmt.Sprint(len(failures), " failed: ", joinFailures(failures)))
		if err := s.save(t); err != nil {
			return err
		}
		return failf(CodeStopFailed, "%d stop command(s) failed, so the decision services may still run (%s); run the command again once the cause is fixed, or ah-switch recover --services-stopped once they are stopped by hand", len(failures), joinFailures(failures))
	}
	t.j.Stopped = true
	t.j.StopFailures = ""
	return s.done(t, t.j.Result)
}

func joinFailures(f []string) string {
	out := ""
	for i, s := range f {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return boundDetail(out)
}

// settle is the done phase: the link folder is synced (a link is not taken
// as moved before that), the counter is made at least the transaction's
// when it was used, deploy.json records the outcome and the journal is
// removed. Any of that failing keeps the journal, and the next command
// settles it again; the result is printed only once it is recorded.
func (s *Switcher) settle(t *txn) (int, bool) {
	unresolved := func(what string, err error) (int, bool) {
		s.step(9, "FAILED: %s: %s", what, q(err.Error()))
		s.printf("result: unresolved: the outcome (%s) is not recorded yet; %s stays, and the next switch, rollback or recover records it", t.j.Result, JournalFile)
		return ExitFailed, false
	}
	if err := syncLinkDir(s.baseRoot); err != nil {
		return unresolved("the link folder was not synced", err)
	}
	if t.j.CounterUsed {
		if err := s.highestAtLeast(t.j.Counter); err != nil {
			return unresolved(HighestFile+" not written", err)
		}
	}
	cur, curOK, err := readLink(s.baseRoot, "current")
	if err != nil {
		return unresolved("current cannot be read", err)
	}
	rec, err := s.txnRecord(t, cur, curOK)
	if err != nil {
		return unresolved(DeployFile+" not written", err)
	}
	if err := s.writeRecord(&rec); err != nil {
		return unresolved(DeployFile+" not written", err)
	}
	if err := s.crash(CrashRecord); err != nil {
		return ExitFailed, false
	}
	if err := s.dropJournal(); err != nil {
		return unresolved(JournalFile+" not removed", err)
	}
	if err := s.crash(CrashUnreported); err != nil {
		return ExitFailed, false
	}
	s.step(9, "recorded in %s", q(filepath.Join(s.stateDir(), DeployFile)))
	// The outcome stays owed until its result line is written, and is
	// owed still when the command that settled it was not the one asked
	// for it (a resume).
	if err := s.printResult(rec); err == nil && !t.resumed {
		s.reported(rec)
	}
	if rec.Result == ResultOK {
		return ExitOK, true
	}
	return ExitFailed, true
}

// txnRecord is the record of the settled transaction t, current pointing
// at cur.
func (s *Switcher) txnRecord(t *txn, cur string, curOK bool) (Record, error) {
	last, _, err := s.readRecord()
	if err != nil {
		return Record{}, err
	}
	rec := Record{
		ID:              t.j.ID,
		Counter:         t.j.Counter,
		Mode:            t.j.TrustMode,
		Trust:           s.policy.Trust(),
		Rollback:        !t.forward(),
		Result:          t.j.Result,
		Check:           t.j.Check,
		FailedStep:      t.j.FailedStep,
		Error:           t.j.Error,
		Detail:          t.j.Detail,
		PackageSnapshot: last.PackageSnapshot,
		RestoredHealth:  t.j.RestoredHealth,
		Txn:             t.j.Txn,
		Requested:       t.j.Requested,
		RequestID:       requestID(t.j.Direction, t.j.ID),
		PendingReport:   true,
		ServicesStopped: t.j.Stopped,
		CounterUsed:     t.j.CounterUsed,
	}
	if t.j.OldCurrent != "" {
		id, _ := s.targetID(t.j.OldCurrent)
		rec.PreviousID = id.Name
	}
	if curOK {
		id, err := s.targetID(cur)
		if err != nil {
			return Record{}, err
		}
		rec.CurrentID = id.Name
	}
	switch t.j.PackageState {
	case PackageDone:
		rec.PackageSnapshot = t.j.PackageTo
		rec.PackageState, rec.PackageFrom, rec.PackageTo, rec.PackageStatedAt = "", "", "", ""
	case PackageStarted, PackageUnknown:
		// Never the old snapshot nor the new: unknown until settled.
		rec.PackageState, rec.PackageFrom, rec.PackageTo = PackageUnknown, t.j.PackageFrom, t.j.PackageTo
	default:
		rec.PackageState, rec.PackageFrom, rec.PackageTo, rec.PackageStatedAt = last.PackageState, last.PackageFrom, last.PackageTo, last.PackageStatedAt
	}
	return rec, nil
}

// printResult prints the last line of a settled transaction, and says
// whether it was written.
func (s *Switcher) printResult(rec Record) error {
	cur := rec.CurrentID
	if cur == "" {
		cur = "none"
	}
	switch rec.Result {
	case ResultOK:
		return s.emit("result: ok; current -> %s", cur)
	}
	line := fmt.Sprintf("result: %s at %s; current -> %s", rec.Result, rec.FailedStep, cur)
	if rec.RestoredHealth != "" {
		line += "; health after going back: " + rec.RestoredHealth
	}
	if rec.ServicesStopped {
		line += "; the decision services are stopped"
	}
	return s.emit("%s", line)
}

// highestAtLeast writes highest-counter as n when it holds less: a counter
// that a switch attempted stays used.
func (s *Switcher) highestAtLeast(n uint64) error {
	cur, present, err := s.readHighest()
	if err == nil && present && cur >= n {
		return nil
	}
	return writeFileIn(s.stateRoot, HighestFile, []byte(fmt.Sprintf("%d\n", n)), 0o644)
}

// resumePackages is the resume of a packages phase: the transaction stopped
// before its links, so nothing but the package step was done. A package
// step that was started and is not known to be done leaves the host's
// package state unknown, once its process is known to have ended (see
// helperEnded); it is never taken as done or as not run.
func (s *Switcher) resumePackages(t *txn) error {
	switch t.j.PackageState {
	case PackageDone:
		t.fail("interrupted", failf(CodeInterrupted, "the %s %s was interrupted before its links; the package step had brought the host to snapshot %s", t.kind(), t.j.ID, q(t.j.PackageTo)))
	default:
		if err := s.helperEnded(t.j.HelperPID, t.j.HelperStart); err != nil {
			return err
		}
		t.j.PackageState = PackageUnknown
		t.fail("interrupted", failf(CodePackageInterrupted, "the package step from %s to %s was interrupted", q(t.j.PackageFrom), q(t.j.PackageTo)))
		t.note("%s", describe(packageUnknownError(t.j.PackageFrom, t.j.PackageTo)))
	}
	return s.done(t, ResultFailed)
}

// now is the time as deploy.json records it.
func (s *Switcher) now() string { return s.cfg.Now().UTC().Format(time.RFC3339) }
