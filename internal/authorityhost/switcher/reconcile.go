package switcher

import (
	"context"
	"fmt"
	"path/filepath"
)

// reconcile settles a journal left by a transaction that did not finish,
// under the lock, before the command does anything else: it resumes the
// journal's phase (see drive), with the trust the transaction began with
// as well as the policy's. A dry run reconciles nothing and stops.
//
// It returns halt when the command must not go on: the journal could not
// be read or still stays, or the transaction ended with the decision
// services stopped, after which no new switch starts on its own.
func (s *Switcher) reconcile(ctx context.Context, newWork bool) (code int, halt bool) {
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
	t := &txn{j: j, resumed: true}
	defer t.close()
	s.claim(t)
	s.printf("reconcile: an interrupted %s %s was found in phase %s", t.kind(), j.ID, j.Phase)
	mismatch, err := s.staleJournal(j)
	if err != nil {
		s.printf("reconcile: unresolved: %s; the journal cannot be checked against the host and is not resumed: nothing else is run or moved. Make it readable, then run the command again", describe(err))
		return ExitFailed, true
	}
	if mismatch != nil {
		s.printf("reconcile: unresolved: %s; the journal does not match the host and is not resumed: nothing is run or moved. Look at the host, then run ah-switch recover --discard-journal %s to set it aside", describe(mismatch), j.Txn)
		return ExitFailed, true
	}
	if s.cfg.Dry {
		s.printf("reconcile: a dry run reconciles nothing; run the command without --dry first")
		return ExitFailed, true
	}
	saved := s.trust
	s.trust = s.trust.with(journalTrust(j))
	defer func() { s.trust = saved }()
	code, settled := s.drive(ctx, t)
	if !settled {
		return ExitFailed, true
	}
	if t.requalifyFailed {
		// The settled outcome is owed with the failure, and decisions stay
		// inhibited: the command fails, whatever it was asked.
		return code, true
	}
	if t.j.Stopped && newWork {
		s.printf("reconcile: the interrupted %s %s ended with the decision services stopped; nothing else is done: look at the host, then run the command again", t.kind(), j.ID)
		return ExitFailed, true
	}
	return 0, false
}

// staleJournal reports why the journal j does not match the host, before
// anything of it is resumed: where current and previous point must be what
// the journal's phase allows (in its packages phase the old targets, in any
// later phase each link its old target or its new one), and a forward
// journal's counter must not be below the counters the host attempted
// (highest-counter, counters.log, the last forward switch deploy.json
// records, the releases the links name). A journal that fails this was not
// left by the last transaction (one restored from a backup, or written by
// hand): resuming it would move the links to its releases.
//
// mismatch is that proof; err is evidence that cannot be read, which
// proves nothing either way: the journal is then neither resumed nor
// discarded.
func (s *Switcher) staleJournal(j journal) (mismatch, err error) {
	cur, _, err := readLink(s.baseRoot, "current")
	if err != nil {
		return nil, err
	}
	prev, _, err := readLink(s.baseRoot, "previous")
	if err != nil {
		return nil, err
	}
	newT := "releases/" + j.ID
	prevAfter := j.OldPrevious
	if j.OldCurrent != "" {
		prevAfter = j.OldCurrent
	}
	curOK := cur == j.OldCurrent || j.Phase != PhasePackages && cur == newT
	prevOK := prev == j.OldPrevious || j.Phase != PhasePackages && prev == prevAfter
	if !curOK || !prevOK {
		return failf(CodeStateInvalid, "current -> %s and previous -> %s are neither the %s %s's old links nor its new ones", linkText(cur), linkText(prev), (&txn{j: j}).kind(), j.ID), nil
	}
	if j.Direction != DirForward {
		return nil, nil
	}
	highest, _, err := s.readHighest()
	if err != nil {
		return nil, err
	}
	ev, err := s.counterEvidence("", false)
	if err != nil {
		return nil, err
	}
	ev.see(highest, HighestFile)
	if j.Counter < ev.counter {
		return failf(CodeStateInvalid, "its counter %d is below %d, the counter of %s, attempted since", j.Counter, ev.counter, ev.source), nil
	}
	return nil, nil
}

// journalTrust is the trust a journal's transaction began with.
func journalTrust(j journal) trust {
	if j.TrustMode != ModeSigned {
		return trust{}
	}
	return trust{signed: []signer{{signers: j.TrustSigners, principal: j.TrustPrincipal}}}
}

// RecoverOptions is what ah-switch recover is asked to do: exactly one of
// them.
type RecoverOptions struct {
	// Counter, with SetCounter, is the highest counter this host ever
	// switched to, written to highest-counter when that is missing or does
	// not hold a counter.
	Counter    uint64
	SetCounter bool
	// ServicesStopped is the owner's statement that the decision services
	// are stopped, which settles a transaction whose stop commands failed.
	ServicesStopped bool
	// PackageState, with SetPackageState, is the owner's statement of the
	// snapshot the host's packages are at, which settles an unknown
	// package state: one of the two ends of the step that did not finish.
	PackageState    string
	SetPackageState bool
	// DiscardJournal, the id of a journal that does not match the host
	// (see staleJournal), sets that journal aside without resuming it.
	DiscardJournal string
}

// Recover runs ah-switch recover. Each form takes the lock and checks the
// state as a switch does.
//
//   - --counter N writes highest-counter as N when it is missing, does not
//     hold a counter, or holds less than the host shows it used (see
//     counterEvidence): the switch refuses to guess it. N must be at least
//     all of that evidence. A journal is settled first.
//   - --services-stopped settles a transaction that stays in its stopping
//     phase because a stop command failed: the owner states that the
//     decision services are stopped, and that statement is recorded, with
//     the time, instead of the stops being run again.
//   - --package-state SNAPSHOT settles an unknown package state: the owner
//     states which end of the package step that did not finish the host is
//     at, and that statement is recorded with the time. A journal is
//     settled first.
func (s *Switcher) Recover(ctx context.Context, o RecoverOptions) int {
	if err := s.checkConfig(); err != nil {
		s.errorf("%v", err)
		return ExitUsage
	}
	if s.cfg.From != "" {
		s.errorf("recover takes no --from")
		return ExitUsage
	}
	n := 0
	for _, set := range []bool{o.SetCounter, o.ServicesStopped, o.SetPackageState, o.DiscardJournal != ""} {
		if set {
			n++
		}
	}
	if n != 1 {
		s.errorf("recover takes one of --counter N, --package-state SNAPSHOT, --services-stopped and --discard-journal TXN")
		return ExitUsage
	}
	if o.DiscardJournal != "" && !txnPattern.MatchString(o.DiscardJournal) {
		s.errorf("--discard-journal takes the id of a transaction, 32 hex digits, as status shows it")
		return ExitUsage
	}
	// As for a switch: what the policy lacks is required only after the
	// stops a journal owes have run (see stopFirst).
	code, usage, blocked := s.applyPolicy()
	if usage {
		return code
	}
	what := fmt.Sprintf("--counter %d", o.Counter)
	switch {
	case o.ServicesStopped:
		what = "--services-stopped"
	case o.SetPackageState:
		what = "--package-state " + q(o.PackageState)
	case o.DiscardJournal != "":
		what = "--discard-journal " + o.DiscardJournal
	}
	s.printf("ah-switch: recover %s (root %s)", what, q(s.cfg.Root))
	lk, code := s.lockWith(false, blocked == nil && !s.cfg.Dry)
	if lk == nil {
		return code
	}
	defer lk.release()
	defer s.closeRoots()
	if !o.ServicesStopped {
		// The owner's word that the services are stopped runs nothing.
		s.stopsOwed(ctx)
	}
	if blocked != nil && s.lockFile == nil {
		// No state folder or no lock file: no journal to resume.
		s.errorf("%s; nothing done", describe(blocked))
		return ExitFailed
	}
	if s.stateRoot == nil {
		s.printf("recover: no state folder; nothing to recover")
		return ExitFailed
	}
	if o.ServicesStopped || o.DiscardJournal != "" {
		// The owner's word runs nothing and needs no policy.
		if code, done := s.openBase(); done {
			return code
		}
		if o.DiscardJournal != "" {
			return s.recoverDiscard(o.DiscardJournal)
		}
		return s.recoverStopped(ctx)
	}
	if blocked != nil {
		return s.stopFirst(ctx, blocked)
	}
	if code, done := s.openBase(); done {
		return code
	}
	if code, halt := s.reconcile(ctx, false); halt {
		return code
	}
	if o.SetPackageState {
		return s.recoverPackageState(o.PackageState)
	}
	ev, err := s.counterEvidence("", true)
	if err != nil {
		s.printf("recover: FAILED: %s", describe(evidenceError(err)))
		return ExitFailed
	}
	if n, present, err := s.readHighest(); err == nil && present && n >= ev.counter {
		s.printf("recover: %s holds %d; nothing to recover", HighestFile, n)
		return ExitFailed
	}
	if o.Counter < ev.counter {
		s.printf("recover: FAILED: --counter %d is below %d, the counter of %s", o.Counter, ev.counter, ev.source)
		return ExitFailed
	}
	if s.cfg.Dry {
		s.printf("recover: would write %s as %d", HighestFile, o.Counter)
		return ExitOK
	}
	if err := writeFileIn(s.stateRoot, HighestFile, []byte(fmt.Sprintf("%d\n", o.Counter)), 0o644); err != nil {
		s.printf("recover: FAILED: %s not written: %s", HighestFile, q(err.Error()))
		return ExitFailed
	}
	s.printf("recover: %s is now %d", HighestFile, o.Counter)
	return ExitOK
}

// recoverStopped settles a transaction in its stopping phase on the owner's
// word that the decision services are stopped.
func (s *Switcher) recoverStopped(ctx context.Context) int {
	j, ok, err := s.readJournal()
	if err != nil {
		s.printf("recover: FAILED: %s; look at %s by hand", describe(err), q(filepath.Join(s.stateDir(), JournalFile)))
		return ExitFailed
	}
	if !ok || j.Phase != PhaseStopping {
		s.printf("recover: FAILED: no transaction is stopping the decision services; nothing to recover")
		return ExitFailed
	}
	if s.cfg.Dry {
		s.printf("recover: would record that the decision services are stopped and settle the %s %s", (&txn{j: j}).kind(), j.ID)
		return ExitOK
	}
	t := &txn{j: j, resumed: true}
	defer t.close()
	s.claim(t)
	t.note("stop failures (%s) settled by the owner's word that the decision services are stopped (recover --services-stopped at %s)", j.StopFailures, s.now())
	t.j.Stopped = true
	t.j.StopFailures = ""
	if err := s.done(t, t.j.Result); err != nil {
		s.printf("recover: FAILED: %s", describe(err))
		return ExitFailed
	}
	code, settled := s.drive(ctx, t)
	if !settled {
		return ExitFailed
	}
	_ = code
	s.printf("recover: the decision services are recorded as stopped by the owner")
	return ExitOK
}

// recoverDiscard sets aside the journal of the transaction id, which must
// be there and be proven not to match the host (see staleJournal): it is
// renamed to txn-TXN.discarded in the state folder, kept as evidence, and
// nothing of it is resumed. A journal that matches the host is refused
// (any command resumes it), and so is one that cannot be checked against
// the host: evidence that cannot be read proves nothing.
func (s *Switcher) recoverDiscard(id string) int {
	j, ok, err := s.readJournal()
	if err != nil {
		s.printf("recover: FAILED: %s; look at %s by hand", describe(err), q(filepath.Join(s.stateDir(), JournalFile)))
		return ExitFailed
	}
	if !ok || j.Txn != id {
		s.printf("recover: FAILED: no journal of the transaction %s; nothing to discard", id)
		return ExitFailed
	}
	mismatch, err := s.staleJournal(j)
	if err != nil {
		s.printf("recover: FAILED: %s; the journal of the %s %s cannot be checked against the host, so it is unresolved and not discarded: make it readable, then run the command again", describe(err), (&txn{j: j}).kind(), j.ID)
		return ExitFailed
	}
	if mismatch == nil {
		s.printf("recover: FAILED: the journal of the %s %s matches the host; it is not discarded: any switch, rollback or recover resumes it", (&txn{j: j}).kind(), j.ID)
		return ExitFailed
	}
	kept := "txn-" + id + ".discarded"
	if s.cfg.Dry {
		s.printf("recover: would set the journal aside as %s", q(filepath.Join(s.stateDir(), kept)))
		return ExitOK
	}
	if err := s.stateRoot.Rename(JournalFile, kept); err != nil {
		s.printf("recover: FAILED: %s", q(err.Error()))
		return ExitFailed
	}
	if err := syncRoot(s.stateRoot); err != nil {
		s.printf("recover: FAILED: the state folder was not synced: %s", q(err.Error()))
		return ExitFailed
	}
	s.printf("recover: the journal of the %s %s was set aside as %s, by the owner at %s; nothing of it was resumed", (&txn{j: j}).kind(), j.ID, q(filepath.Join(s.stateDir(), kept)), s.now())
	return ExitOK
}
