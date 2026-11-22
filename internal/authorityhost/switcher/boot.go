package switcher

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// DirBoot is the direction of the outcome a failed ah-switch boot owes
// (see Boot). It is no transaction's: no switch or rollback request is a
// retry of it, so every one is refused until the owner acknowledges it.
const DirBoot = "boot"

// bootReason is the reason of the inhibit ah-switch boot writes before it
// runs anything. An inhibit holding exactly this reason, with no journal
// open and no outcome owed under its run, is the mark of a boot that did
// not finish (see Boot).
const bootReason = "boot: decisions are refused from the boot until ah-switch boot has qualified current again"

// bootPendingReason is the reason of the inhibit ah-switch boot writes when
// a journal is open that cannot be read and no inhibit is there (the
// inhibit of a readable one is the transaction's, see guardJournal). It is
// not bootReason: no boot takes it over, and only a transaction that ends
// healthy or the owner's inhibit-clear removes it.
const bootPendingReason = "boot: a transaction journal is open and cannot be read; decisions stay inhibited until a transaction ends healthy or ah-switch inhibit-clear --asked WORDS"

// Boot runs ah-switch boot, which its unit runs at every boot (see
// deploy/authority-host/README.md, "Boot requalification"): after a
// reboot decisions stay refused until the host has been qualified since
// that boot.
//
// Under the lock, before anything is run, it writes decisions.inhibit as
// its own record (reason boot, the id of this run) when there is none and
// no journal is open, and takes over the inhibit of an earlier boot that
// did not finish (see unfinishedBoot), writing it again as this run's.
// While a journal is open it never writes its own record: the inhibit of
// a readable journal is the transaction's, written back first when it was
// removed (see guardJournal), and one that cannot be read gets a plain
// record no boot takes over (bootPendingReason). It then qualifies the
// release current points at again as a settling transaction does
// (qualifyAgain: verified with the switch policy's trust, then its reload
// list, its health check and its post-activation no-AI check), and
// removes that inhibit, still this run's, only when all of it passed. A
// failure (the policy that cannot be used and the release that does not
// verify included) is written into the inhibit first, then handled as a
// settling transaction's (requalifyFailed: the host's stop list runs),
// then added to owed.json with requalify_failed, and exits 1 (see
// bootFailed).
//
// Any other inhibit stays exactly as found (a transaction's, a held
// journal, a failure, one waiting for the owner's inhibit-clear, one that
// cannot be read strictly): boot lifts nothing then. It settles an open
// journal through reconcile, as every command does (that transaction
// lifts the inhibit by its own rules), and prints what is owed. It never
// acknowledges anything and never writes deploy.json. A host with no
// current release and no journal (nothing deployed) is refused with
// ExitNoRelease, and nothing is created or changed.
func (s *Switcher) Boot(ctx context.Context) int {
	if err := s.checkConfig(); err != nil {
		s.errorf("%v", err)
		return ExitUsage
	}
	if s.cfg.Dry || s.cfg.From != "" || s.cfg.Mode != "" || s.cfg.NoAICheck != "" {
		s.errorf("boot takes only --root and --allow-exec")
		return ExitUsage
	}
	// A policy that cannot be used is a failure of the qualification,
	// once decisions are inhibited; it never keeps them from being.
	code, usage, blocked := s.applyPolicy()
	if usage {
		return code
	}
	s.printf("ah-switch: boot (root %s)", q(s.cfg.Root))
	// The lock is taken without creating anything: a host where nothing
	// was deployed is left as it is.
	lk, code := s.lockWith(false, false)
	if lk == nil {
		return code
	}
	defer lk.release()
	defer s.closeRoots()
	if s.lockFile == nil {
		// No state folder, or no lock file in it: no switch ever took
		// the lock here.
		_, ok, err := s.bootCurrent()
		if err == nil && !ok {
			return s.noRelease()
		}
		if err == nil && s.stateRoot == nil {
			err = failf(CodeStateInvalid, "the state folder %s is missing although current is there", q(s.stateDir()))
		}
		if err != nil {
			s.printf("boot: FAILED: %s; nothing is run or changed", describe(err))
			return ExitFailed
		}
		// A current release, and a state folder without its lock file:
		// the lock file is made, as every switch makes it.
		s.closeRoots()
		if lk, code = s.lockWith(false, true); lk == nil {
			return code
		}
		defer lk.release()
	}
	s.guardJournal()
	s.stopsOwed(ctx)
	_, open, jerr := s.readJournal()
	in, settling, there, ierr := s.readInhibit()
	cur, curOK, cerr := s.bootCurrent()
	pending := jerr != nil || open || settling != nil
	if !pending && cerr == nil && !curOK {
		return s.noRelease()
	}
	run, mine, ok := s.bootInhibit(in, there, ierr, pending)
	if !ok {
		return ExitFailed
	}
	if mine {
		if err := s.crash(CrashBootInhibit); err != nil {
			return ExitFailed
		}
	}
	if pending {
		return s.bootSettle(ctx, blocked)
	}
	if !mine {
		s.printOwed()
		s.emit("result: decisions stay inhibited: %s was there before this boot and stays as found; look at the host (ah-switch status), then switch, or run ah-switch inhibit-clear --asked WORDS", InhibitFile)
		return ExitFailed
	}
	err := blocked
	if err == nil {
		err = cerr
	}
	if err == nil {
		err = s.openReleases()
	}
	if err == nil {
		_, err = s.qualifyAgain(ctx, checkRef{}, cur, curOK, "boot: since the boot, ")
	}
	if errors.Is(err, errCrashed) {
		return ExitFailed
	}
	if err != nil {
		return s.bootFailed(ctx, run, cur, err)
	}
	if err := s.crash(CrashBootQualified); err != nil {
		return ExitFailed
	}
	return s.bootLift(run, cur)
}

// bootCurrent reads where current points; ok is false when opt/authority-host
// or current is missing: nothing is deployed.
func (s *Switcher) bootCurrent() (cur string, ok bool, err error) {
	base, err := s.openTrusted(BaseDir, false)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer base.Close()
	return readLink(base, "current")
}

// noRelease is the refusal of boot on a host where nothing is deployed:
// nothing is run, created or changed, decisions.inhibit included.
func (s *Switcher) noRelease() int {
	s.printf("boot: refused: there is no current release and no transaction open (nothing is deployed): nothing is qualified, run or changed, and %s is left as found", InhibitFile)
	s.printf("boot: no decision-capable service runs without a release, since each comes with one; the decision guard answers by the state folder alone: it refuses while that folder is missing or %s is there, and allows otherwise", InhibitFile)
	return ExitNoRelease
}

// unfinishedBoot reports whether the inhibit in is the record of an
// earlier boot that did not finish: exactly the record boot writes (read
// strictly), with no journal open and no outcome owed under its run. Such
// a boot died after it inhibited decisions and before its failure was
// recorded or the inhibit lifted; nothing but that boot changed since it
// found no inhibit, so this boot does what it did, from the start.
func (s *Switcher) unfinishedBoot(in inhibit, pending bool) bool {
	if pending || in.Reason != bootReason || !txnPattern.MatchString(in.Txn) {
		return false
	}
	owed, err := s.readOwed()
	if err != nil {
		return false
	}
	for _, o := range owed {
		if o.Txn == in.Txn {
			return false
		}
	}
	return true
}

// bootInhibit makes sure decisions are inhibited before boot runs
// anything: with no journal pending, it writes this run's record when
// there is no inhibit, or when the inhibit is an unfinished boot's, and
// leaves any other as found. With a journal pending (open, held or
// unreadable) the inhibit is never this run's: one there stays as found,
// and when none is there (a journal that cannot be read, which
// guardJournal cannot guard) a plain record is written that no boot takes
// over. mine says the inhibit is this run's (run); ok false is a failure,
// said.
func (s *Switcher) bootInhibit(in inhibit, there bool, ierr error, pending bool) (run inhibit, mine, ok bool) {
	switch {
	case ierr != nil:
		s.printf("boot: %s is there and cannot be read strictly (%s); it stays as found", InhibitFile, describe(ierr))
		return inhibit{}, false, true
	case there && !s.unfinishedBoot(in, pending):
		s.printf("boot: decisions are inhibited (%s) since %s, set by transaction or boot run %s: %s; it stays as found: ah-switch boot lifts only the inhibit it wrote itself", InhibitFile, q(in.Time), q(in.Txn), q(in.Reason))
		return inhibit{}, false, true
	case there:
		s.printf("boot: %s is the record of an earlier ah-switch boot (run %s, since %s) that did not finish; this boot takes it over and qualifies current from the start", InhibitFile, q(in.Txn), q(in.Time))
	case pending:
		id, err := newTxnID()
		if err == nil {
			err = s.writeInhibit(inhibit{Txn: id, Reason: bootPendingReason, Time: s.now()})
		}
		if err != nil {
			s.printf("boot: FAILED: %s not written: %s; nothing is run, and decisions are NOT inhibited", InhibitFile, q(err.Error()))
			return inhibit{}, false, false
		}
		s.printf("boot: decisions inhibited (%s, run %s) for the transaction still open; no boot takes this record over", InhibitFile, id)
		return inhibit{}, false, true
	}
	id, err := newTxnID()
	if err == nil {
		run = inhibit{Txn: id, Reason: bootReason, Time: s.now()}
		err = s.writeInhibit(run)
	}
	if err != nil {
		state := "decisions are NOT inhibited"
		if there {
			state = "the earlier record stays, and decisions stay inhibited"
		}
		s.printf("boot: FAILED: %s not written: %s; nothing is run, and %s", InhibitFile, q(err.Error()), state)
		return inhibit{}, false, false
	}
	s.printf("boot: decisions inhibited (%s, run %s) before anything runs", InhibitFile, run.Txn)
	return run, true, true
}

// bootSettle settles the transaction left open, through reconcile as any
// command does, and lifts nothing itself: the transaction removes the
// inhibit only when it ends healthy, having qualified its release in this
// command, since this boot. It prints what is owed, and fails unless
// decisions are no longer inhibited.
func (s *Switcher) bootSettle(ctx context.Context, blocked error) int {
	s.printf("boot: a transaction is still open; it is settled as any command settles it, and ah-switch boot lifts nothing itself")
	code := ExitOK
	if blocked != nil {
		code = s.stopFirst(ctx, blocked, false)
	} else if c, done := s.openBase(); done {
		code = c
	} else if c, halt := s.reconcile(ctx, false); halt {
		code = c
	}
	s.printOwed()
	if _, _, there, err := s.readInhibit(); there || err != nil {
		s.printf("boot: decisions stay inhibited (%s): look at the host (ah-switch status)", InhibitFile)
		return ExitFailed
	}
	if code != ExitOK {
		return ExitFailed
	}
	s.printf("boot: decisions are no longer inhibited: the transaction ended healthy, its release qualified since the boot")
	return ExitOK
}

// bootFailed ends a boot whose qualification of current (cur) failed with
// cause. The failure is made durable first: this run's inhibit is
// rewritten with it, a record no later boot takes over (see
// unfinishedBoot), before anything else is done. Then it is handled as a
// settling transaction's failure (the host's stop list runs, see
// requalifyFailed), then owed with requalify_failed. A crash after the
// rewrite loses at most the outcome owed, never the inhibit or its
// failure, and an ack of the outcome owed leaves a record no boot lifts.
// When the rewrite fails, the run's own record stays and the failure is
// still owed, which keeps a later boot from taking that record over.
func (s *Switcher) bootFailed(ctx context.Context, run inhibit, cur string, cause error) int {
	s.printf("boot: FAILED: %s", describe(cause))
	kept := inhibit{Txn: run.Txn, Reason: boundDetail(fmt.Sprintf("boot: after the boot, %s did not pass its reload, health check and no-AI check again (%s)", linkText(cur), describe(cause))), Time: s.now()}
	keptErr := s.writeInhibit(kept)
	if keptErr != nil {
		s.printf("boot: FAILED: %s not rewritten with the failure: %s; it stays as this boot wrote it, decisions stay inhibited, and the failure is owed under its run", InhibitFile, q(keptErr.Error()))
	} else if err := s.crash(CrashBootKept); err != nil {
		return ExitFailed
	}
	notes := s.requalifyFailed(ctx, "after the boot", cur, cause)
	if err := s.crash(CrashBootFailed); err != nil {
		return ExitFailed
	}
	id := ""
	if rid, err := s.targetID(cur); err == nil && cur != "" {
		id = rid.Name
	}
	o := owedOutcome{
		Txn:             run.Txn,
		RequestID:       requestID(DirBoot, id),
		Direction:       DirBoot,
		Requested:       DirBoot,
		ID:              id,
		CurrentID:       id,
		Result:          ResultFailed,
		FailedStep:      DirBoot,
		Detail:          boundDetail(strings.Join(notes, "; ")),
		Time:            s.now(),
		RequalifyFailed: true,
	}
	owed, err := s.addOwedOutcome(o)
	if err != nil {
		s.printf("boot: FAILED: %s not written: %s; %s holds the failure, and decisions stay inhibited", OwedFile, describe(err), InhibitFile)
		return ExitFailed
	}
	if err := s.crash(CrashBootOwed); err != nil {
		return ExitFailed
	}
	s.printf("boot: the failure is owed until %s; decisions stay inhibited (%s)", ackCommand(owed), InhibitFile)
	s.reportOwed(o, ackCommand(owed))
	return ExitFailed
}

// bootLift removes the inhibit of this run, run, once current (cur) was
// qualified again: it must still be exactly what this run wrote.
func (s *Switcher) bootLift(run inhibit, cur string) int {
	in, _, there, err := s.readInhibit()
	if err != nil || !there || in != run {
		s.printf("boot: FAILED: %s is no longer the record this boot wrote; it is left as it is, and nothing is lifted", InhibitFile)
		return ExitFailed
	}
	if err := removeFileIn(s.stateRoot, InhibitFile); err != nil {
		s.printf("boot: FAILED: %s not removed: %s; decisions stay inhibited", InhibitFile, q(err.Error()))
		return ExitFailed
	}
	if err := s.crash(CrashBootLifted); err != nil {
		return ExitFailed
	}
	s.printf("boot: decisions no longer inhibited: %s removed", InhibitFile)
	s.printOwed()
	id, _ := s.targetID(cur)
	s.emit("result: ok; current -> %s, qualified since the boot", id.Name)
	return ExitOK
}

// printOwed prints the outcomes owed, as status shows them, and leaves
// them owed.
func (s *Switcher) printOwed() {
	lines, err := s.owedStatus(false)
	if err != nil {
		s.printf("boot: %s cannot be read: %s", OwedFile, describe(err))
		return
	}
	for _, line := range lines {
		s.printf("boot: %s", line)
	}
}
