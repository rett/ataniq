package switcher

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
)

// InhibitFile is the switch's decision inhibit, in the state folder
// (var/lib/authority-host/decisions.inhibit), and the one guard of
// decisions: a decision-capable service makes one lookup before each
// decision and refuses unless the file does not exist (any answer but "no
// such file" refuses). Such a service may start while it is there (it
// must, to answer its readiness probe); no unit carries a start condition
// on it. The switch writes it, owned by root, mode 0644 (0600 while it
// holds the journal, below), synced with its folder, at the start of every transaction, before any package step or
// link and before the journal (txn.json) is first written, and removes it
// only when the transaction ends healthy: a switch or rollback whose
// release passed its health check and its post-activation no-AI check, or
// a going back whose release passed its reload, its health check and its
// post-activation no-AI check; a transaction found in phase done by a
// later command first runs the reload list, the health check and the
// no-AI check of current again (see requalifyCurrent). Any other end
// leaves it, across a reboot, until a later transaction ends healthy or
// the owner runs ah-switch inhibit-clear.
//
// The journal is never there without it. A healthy end renames the
// journal over it (the journal is gone and the inhibit, now holding that
// journal, is there, in one atomic step), syncs the folder, then removes
// it: that removal is the one step of the switch that opens decisions. A
// crash between the two leaves the inhibit holding a healthy journal in
// phase done (see readInhibit): the next command writes the journal back
// from it, the inhibit staying, and qualifies current again before it
// settles; inhibit-clear and ack refuse it. Every command that takes the
// lock exclusively and finds a journal without the inhibit (removed by
// hand) writes the inhibit back first, before it runs anything (the stops
// a stopping journal owes included) and before it refuses anything (see
// guardJournal), and recover --discard-journal writes its own record of
// the discard before the journal is set aside.
//
// Why no service following the one-lookup contract can accept a decision
// while the host is not qualified: its lookup is one instant, and these
// are all the changes of the file (each under the switch's lock, each
// write an atomic rename, synced):
//
//	beginTxn          inhibit written; then the journal
//	guardJournal      inhibit written back when a journal is there without it,
//	                  first in every command that takes the lock exclusively
//	                  (a plain record no boot takes over when the journal
//	                  cannot be read); one there is never rewritten by it
//	reconcile         the same; a journal held by the inhibit is written back,
//	                  the inhibit stays
//	an unhealthy end  inhibit rewritten with the outcome; then the journal removed
//	a healthy end     journal renamed over the inhibit; then the inhibit removed
//	discard           inhibit rewritten as a plain record of the discard;
//	                  then the journal set aside
//	inhibit-clear     inhibit removed on the owner's word; refused while a
//	                  journal is there or the inhibit holds one
//	boot              with no journal pending: inhibit written, as boot's own
//	                  record, when it is not there (or is the record of a boot
//	                  that did not finish); then that record removed, once
//	                  current qualified again; any other inhibit left as found.
//	                  With a journal pending never its own record: a plain one,
//	                  which no boot takes over, when none is there
//	a failed boot     boot's record rewritten with the failure; then the
//	                  host's stop list, then the failure owed
//
// Only the second step of a healthy end, boot's removal of its own record
// and inhibit-clear make the file absent. The first runs after the
// transaction's release passed its checks (again, when found in phase
// done), with no journal left; the second after current passed its reload,
// health check and no-AI check since the boot, with no journal open (see
// Boot); the third is the owner's. No change makes it absent while a
// journal is there, and every transaction writes it before anything else:
// a lookup that finds no file is a host that ended healthy or was
// qualified again since its boot, or that the owner cleared, with no
// transaction open. (A removal by hand, which only root
// can do, is outside this; the next command that finds the journal writes
// the file back.)
const InhibitFile = "decisions.inhibit"

// maxInhibitWords bounds the owner's words of inhibit-clear.
const maxInhibitWords = 512

// inhibit is the content of decisions.inhibit: the transaction that set it
// and why.
type inhibit struct {
	Txn    string `json:"txn"`
	Reason string `json:"reason"`
	Time   string `json:"time"`
}

// readInhibit reads decisions.inhibit; a missing file is none. Only its
// existence matters to the services; its content is for the switch and
// status, in one of two shapes: the record of why decisions are
// inhibited, or the journal a healthy end renamed over it and did not
// remove yet (settling, a transaction about to be settled, never an
// outcome: see InhibitFile). A file of neither shape is an error, with
// there true.
func (s *Switcher) readInhibit() (in inhibit, settling *journal, there bool, err error) {
	data, err := s.readStateFile(InhibitFile, maxStateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return inhibit{}, nil, false, nil
	}
	if err != nil {
		return inhibit{}, nil, false, err
	}
	if decodeStrict(data, &in) == nil {
		return in, nil, true, nil
	}
	var j journal
	if decodeStrict(data, &j) != nil || s.checkJournal(j) != nil || !settlingJournal(j) {
		return inhibit{}, nil, true, failf(CodeStateInvalid, "%s does not parse", InhibitFile)
	}
	in = inhibit{Txn: j.Txn, Reason: fmt.Sprintf("the %s %s ended %s and was being settled; current is qualified again before decisions are no longer inhibited", (&txn{j: j}).kind(), j.ID, j.Result)}
	return in, &j, true, nil
}

// settlingJournal reports whether j is what a healthy end renames over
// decisions.inhibit: in phase done, healthy, its requalification not
// failed.
func settlingJournal(j journal) bool {
	return j.Phase == PhaseDone && journalHealthy(j) && !j.RequalifyFailed
}

// setInhibit writes decisions.inhibit for the transaction t with reason,
// whole, and syncs it and the state folder.
func (s *Switcher) setInhibit(t *txn, reason string) error {
	in := inhibit{Txn: t.j.Txn, Reason: boundDetail(reason), Time: s.now()}
	if err := s.writeInhibit(in); err != nil {
		return failf(CodeRecordFailed, "%s not written: %s", InhibitFile, q(err.Error()))
	}
	return nil
}

// writeInhibit writes decisions.inhibit as in, whole, and syncs it and the
// state folder.
func (s *Switcher) writeInhibit(in inhibit) error {
	return writeJSONIn(s.stateRoot, InhibitFile, in, 0o644)
}

// beginTxn starts the transaction t: decisions are inhibited before the
// journal is first written, so no transaction runs without it.
func (s *Switcher) beginTxn(t *txn) error {
	if t.begun {
		return nil
	}
	if err := s.setInhibit(t, fmt.Sprintf("the %s %s is under way", t.kind(), t.j.ID)); err != nil {
		return err
	}
	t.begun = true
	return nil
}

// unreadableJournalReason is the reason of the plain record written when a
// journal is open that cannot be read and no inhibit is there (see
// guardJournal): it is not bootReason, so no boot takes it over, and only a
// transaction that ends healthy or the owner's inhibit-clear removes it,
// also once the journal was set aside or removed by hand.
const unreadableJournalReason = "a transaction journal is open and cannot be read; decisions stay inhibited until a transaction ends healthy or ah-switch inhibit-clear --asked WORDS"

// inhibitThere reports whether decisions.inhibit is there, in any form
// (one that cannot be read included): its presence, apart from its
// content. err is a lookup that answers neither.
func (s *Switcher) inhibitThere() (bool, error) {
	_, err := s.stateRoot.Lstat(InhibitFile)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// ensureInhibit makes sure decisions are inhibited while the journal of
// the transaction t is there: a journal found without the inhibit
// (removed by hand) gets it again, before it is checked against the host
// or resumed. An inhibit that is there stays as found, whatever it holds
// (one that cannot be read is evidence, kept when the journal is then
// refused); the transaction rewrites or removes it at its end, as any
// transaction does. A lookup that answers neither is an error: nothing is
// resumed then.
func (s *Switcher) ensureInhibit(t *txn) error {
	if t.begun {
		return nil
	}
	there, err := s.inhibitThere()
	if err != nil {
		return failf(CodeStateInvalid, "%s cannot be looked up: %s", InhibitFile, q(err.Error()))
	}
	if there {
		t.begun = true
		return nil
	}
	if err := s.setInhibit(t, fmt.Sprintf("the %s %s was interrupted and its journal is still open", t.kind(), t.j.ID)); err != nil {
		return err
	}
	t.begun = true
	return nil
}

// guardJournal is the first thing every command that takes the lock
// exclusively does, before it runs anything (the host's stop list owed by
// a stopping journal included) and before it can refuse: when a journal is
// there and decisions.inhibit is not (removed by hand), the inhibit is
// written back, so that a journal is never left without it, whatever the
// command then does or refuses. For a journal that can be read it is that
// transaction's (as ensureInhibit writes it); for one that cannot be read
// (it does not parse, or it is not trusted) a plain record with a run id
// of its own and unreadableJournalReason, which no boot takes over (see
// unfinishedBoot). An inhibit that is there stays as found, one that
// cannot be read included: it is evidence, and a command that refuses
// keeps it (only recover --discard-journal, a resumed transaction's end
// and boot's own rules rewrite one). A dry run writes nothing. A failure
// is said, and the command goes on: the stops it owes still run, and its
// reconcile refuses to resume a journal without the inhibit.
func (s *Switcher) guardJournal() {
	if s.cfg.Dry || s.stateRoot == nil || s.lockFile == nil {
		return
	}
	j, open, jerr := s.readJournal()
	if jerr == nil && !open {
		return
	}
	if there, err := s.inhibitThere(); err != nil || there {
		return
	}
	if jerr != nil {
		id, err := newTxnID()
		if err == nil {
			err = s.writeInhibit(inhibit{Txn: id, Reason: unreadableJournalReason, Time: s.now()})
		}
		if err != nil {
			s.printf("reconcile: FAILED: %s not written: %s; %s is there and cannot be read, and decisions are NOT inhibited", InhibitFile, q(err.Error()), JournalFile)
			return
		}
		s.printf("reconcile: %s was not there beside %s, which cannot be read (%s); a plain record (run %s) is written before anything else, which no boot takes over", InhibitFile, JournalFile, describe(jerr), id)
		return
	}
	t := &txn{j: j}
	if err := s.setInhibit(t, fmt.Sprintf("the %s %s was interrupted and its journal is still open", t.kind(), j.ID)); err != nil {
		s.printf("reconcile: FAILED: %s; %s is there without it, and decisions are NOT inhibited", describe(err), JournalFile)
		return
	}
	s.printf("reconcile: %s was not there beside the journal of the %s %s (transaction %s); it is written back before anything else", InhibitFile, t.kind(), j.ID, j.Txn)
}

// healthy reports whether the transaction t ended healthy: its release
// passed its health check and its post-activation no-AI check (ok), or it
// went back to the earlier release, whose reload, health check and
// post-activation no-AI check passed (rolled-back with restored health
// ok).
func (t *txn) healthy() bool {
	return journalHealthy(t.j)
}

// journalHealthy is healthy of the transaction j journals.
func journalHealthy(j journal) bool {
	return j.Result == ResultOK || j.Result == ResultRolledBack && j.RestoredHealth == "ok"
}

// keepInhibit is the inhibit at an end of the transaction t that is not
// healthy, or whose requalification failed, once its outcome is recorded
// and before its journal is removed: rewritten with the outcome as its
// reason, so that it stays when the journal goes.
func (s *Switcher) keepInhibit(t *txn) error {
	if t.j.RequalifyFailed {
		if err := s.setInhibit(t, fmt.Sprintf("the %s %s ended %s, but after an interruption current did not pass its reload, health check and no-AI check again", t.kind(), t.j.ID, t.j.Result)); err != nil {
			return err
		}
		s.step(9, "decisions stay inhibited (%s): current did not pass its reload, health check and no-AI check again", InhibitFile)
		return nil
	}
	reason := fmt.Sprintf("the %s %s ended %s at %s", t.kind(), t.j.ID, t.j.Result, t.j.FailedStep)
	if t.j.Stopped {
		reason += ", with the decision services stopped"
	}
	if err := s.setInhibit(t, reason); err != nil {
		return err
	}
	s.step(9, "decisions stay inhibited (%s): the %s %s did not end healthy", InhibitFile, t.kind(), t.j.ID)
	return nil
}

// InhibitClear runs ah-switch inhibit-clear --asked WORDS: the owner's
// explicit removal of the decision inhibit, after looking at the host.
// words, what the owner asked (or said), are recorded in deploy.json with
// the time. It runs no program. It is refused while a transaction is open
// (any command settles it first), the journal of one that ended healthy
// held by the inhibit included (the host is qualified again first), and
// when there is no inhibit.
func (s *Switcher) InhibitClear(ctx context.Context, words string) int {
	if !filepath.IsAbs(s.cfg.Root) {
		s.errorf("--root %s is not an absolute path", q(s.cfg.Root))
		return ExitUsage
	}
	words = strings.TrimSpace(words)
	if words == "" || len(words) > maxInhibitWords {
		s.errorf("inhibit-clear needs --asked WORDS: what the owner asked or saw, 1 to %d bytes", maxInhibitWords)
		return ExitUsage
	}
	s.printf("ah-switch: inhibit-clear (root %s)", q(s.cfg.Root))
	lk, code := s.lockWith(false, false)
	if lk == nil {
		return code
	}
	defer lk.release()
	defer s.closeRoots()
	if s.stateRoot == nil || s.lockFile == nil {
		s.printf("inhibit-clear: no state folder or lock file; nothing to clear")
		return ExitFailed
	}
	s.guardJournal()
	if j, ok, err := s.readJournal(); err != nil || ok {
		if err == nil {
			err = failf(CodeStateInvalid, "the %s %s (transaction %s) is still open in phase %s; run any command to settle it first", (&txn{j: j}).kind(), j.ID, j.Txn, j.Phase)
		}
		s.printf("inhibit-clear: FAILED: %s", describe(err))
		return ExitFailed
	}
	in, settling, ok, err := s.readInhibit()
	if err != nil && !ok {
		s.printf("inhibit-clear: FAILED: %s", describe(err))
		return ExitFailed
	}
	if !ok {
		s.printf("inhibit-clear: decisions are not inhibited; nothing to clear")
		return ExitFailed
	}
	if settling != nil {
		s.printf("inhibit-clear: FAILED: %s holds the journal of the %s %s (transaction %s), which ended %s and was interrupted while it was settled: the host must be qualified again first, which inhibit-clear does not do; run any switch, rollback or recover to qualify current again and settle it", InhibitFile, (&txn{j: *settling}).kind(), settling.ID, settling.Txn, settling.Result)
		return ExitFailed
	}
	rec, _, rerr := s.readRecord()
	if rerr != nil {
		s.printf("inhibit-clear: FAILED: %s; the clearing could not be recorded", describe(rerr))
		return ExitFailed
	}
	if s.cfg.Dry {
		s.printf("inhibit-clear: would remove %s (set by transaction %s: %s) and record the owner's words", q(filepath.Join(s.stateDir(), InhibitFile)), q(in.Txn), q(in.Reason))
		return ExitOK
	}
	rec.InhibitClearedAt = s.now()
	rec.InhibitClearedAsked = words
	if err := s.rewriteRecord(&rec); err != nil {
		s.printf("inhibit-clear: FAILED: %s not written: %s; the inhibit stays", DeployFile, q(err.Error()))
		return ExitFailed
	}
	if err := removeFileIn(s.stateRoot, InhibitFile); err != nil {
		s.printf("inhibit-clear: FAILED: %s not removed: %s", InhibitFile, q(err.Error()))
		return ExitFailed
	}
	s.printf("inhibit-clear: %s removed by the owner at %s (asked: %s); decision-capable services take decisions again", InhibitFile, rec.InhibitClearedAt, q(words))
	return ExitOK
}
