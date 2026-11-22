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
	s.printf("reconcile: an interrupted %s %s was found in phase %s", t.kind(), j.ID, j.Phase)
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
	if t.j.Stopped && newWork {
		s.printf("reconcile: the interrupted %s %s ended with the decision services stopped; nothing else is done: look at the host, then run the command again", t.kind(), j.ID)
		return ExitFailed, true
	}
	_ = code
	return 0, false
}

// journalTrust is the trust a journal's transaction began with.
func journalTrust(j journal) trust {
	if j.TrustMode != ModeSigned {
		return trust{}
	}
	return trust{signed: []signer{{signers: j.TrustSigners, principal: j.TrustPrincipal}}}
}

// reportSettled answers a retry of a request whose transaction was settled
// by a later command (a reconciliation) and whose outcome nobody was shown
// yet: the retry reports that outcome and does nothing else, rather than
// doing the request again (a retried --rollback-to previous would otherwise
// resolve previous anew, and go back to the release the first one left).
// The outcome is reported once; the same request after that is a new one.
func (s *Switcher) reportSettled(last Record, arg string, rollback bool) (int, bool) {
	if !last.PendingReport || last.Txn == "" || last.Rollback != rollback || last.Requested != arg {
		return 0, false
	}
	kind := "switch to"
	if rollback {
		kind = "rollback to"
	}
	s.printf("settled: this %s %s was interrupted and has been settled since; it is not done again", kind, arg)
	if last.Detail != "" {
		s.printf("settled: %s", last.Detail)
	}
	last.PendingReport = false
	if err := s.writeRecord(&last); err != nil {
		s.printf("result: unresolved: %s not written: %s", DeployFile, q(err.Error()))
		return ExitFailed, true
	}
	s.printResult(last)
	if last.Result == ResultOK {
		return ExitOK, true
	}
	return ExitFailed, true
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
}

// Recover runs ah-switch recover. Each form takes the lock and checks the
// state as a switch does.
//
//   - --counter N writes highest-counter as N when it is missing or does not
//     hold a counter, on a host whose current link shows that releases were
//     switched to: the switch refuses to guess it. N must be at least the
//     counter of the releases current and previous point at. A journal is
//     settled first.
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
	for _, set := range []bool{o.SetCounter, o.ServicesStopped, o.SetPackageState} {
		if set {
			n++
		}
	}
	if n != 1 {
		s.errorf("recover takes one of --counter N, --package-state SNAPSHOT and --services-stopped")
		return ExitUsage
	}
	if code, done := s.applyPolicy(); done {
		return code
	}
	what := fmt.Sprintf("--counter %d", o.Counter)
	switch {
	case o.ServicesStopped:
		what = "--services-stopped"
	case o.SetPackageState:
		what = "--package-state " + q(o.PackageState)
	}
	s.printf("ah-switch: recover %s (root %s)", what, q(s.cfg.Root))
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
	if o.ServicesStopped {
		return s.recoverStopped(ctx)
	}
	if code, halt := s.reconcile(ctx, false); halt {
		return code
	}
	if o.SetPackageState {
		return s.recoverPackageState(o.PackageState)
	}
	if n, present, err := s.readHighest(); err == nil && present {
		s.printf("recover: %s holds %d; nothing to recover", HighestFile, n)
		return ExitFailed
	}
	least := uint64(0)
	for _, name := range []string{"current", "previous"} {
		t, ok, err := readLink(s.baseRoot, name)
		if err != nil {
			s.printf("recover: FAILED: %s: %s", name, describe(err))
			return ExitFailed
		}
		if ok {
			id, err := s.targetID(t)
			if err != nil {
				s.printf("recover: FAILED: %s: %s", name, describe(err))
				return ExitFailed
			}
			least = max(least, id.Counter)
		}
	}
	if o.Counter < least {
		s.printf("recover: FAILED: --counter %d is below %d, the counter of a release this host already used", o.Counter, least)
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
