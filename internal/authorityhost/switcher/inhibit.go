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
// (var/lib/authority-host/decisions.inhibit): while it is there, every
// decision-capable service refuses every decision. Such a service may
// start while it is there (it must, to answer its readiness probe): the
// application checks the file before each decision and refuses when it is
// there or cannot be read. No unit carries a start condition on it. The
// switch writes it, owned by root, mode 0644, synced with its folder, at
// the start of every transaction, before any package step or link, and
// removes it only when the transaction ends healthy: a switch or rollback
// whose release passed its health check and its post-activation no-AI
// check, or a going back whose release passed its reload, its health check
// and its post-activation no-AI check; a transaction found in phase done
// by a later command first runs the reload list, the health check and the
// no-AI check of current again (see requalifyCurrent). Any other end
// leaves it, across a reboot, until a later transaction ends healthy or
// the owner runs ah-switch inhibit-clear.
//
// The service also refuses every decision while the journal, txn.json in
// the same folder, is there (or whether it is there cannot be told): a
// healthy end removes the inhibit, synced, before it removes the journal,
// so a crash between the two leaves a journal in phase done and no
// inhibit, and the next command qualifies current again (writing the
// inhibit back first) before it settles. A transaction is open from the
// inhibit's writing to the journal's removal, so at every crash point
// one of the two files is there until it is settled. (The other order
// would leave, after a crash between the two, an inhibit with no journal
// to qualify the host again from, which only the owner could clear.)
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
// existence matters to the services; its content is for status.
func (s *Switcher) readInhibit() (inhibit, bool, error) {
	data, err := s.readStateFile(InhibitFile, maxStateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return inhibit{}, false, nil
	}
	if err != nil {
		return inhibit{}, false, err
	}
	var in inhibit
	if err := decodeStrict(data, &in); err != nil {
		return inhibit{}, true, failf(CodeStateInvalid, "%s does not parse", InhibitFile)
	}
	return in, true, nil
}

// setInhibit writes decisions.inhibit for the transaction t with reason,
// whole, and syncs it and the state folder.
func (s *Switcher) setInhibit(t *txn, reason string) error {
	in := inhibit{Txn: t.j.Txn, Reason: boundDetail(reason), Time: s.now()}
	if err := writeJSONIn(s.stateRoot, InhibitFile, in, 0o644); err != nil {
		return failf(CodeRecordFailed, "%s not written: %s", InhibitFile, q(err.Error()))
	}
	return nil
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

// ensureInhibit makes sure decisions are inhibited while the transaction
// t is driven: a resumed transaction whose inhibit is gone (removed by
// hand) gets it again.
func (s *Switcher) ensureInhibit(t *txn) error {
	if t.begun {
		return nil
	}
	_, ok, err := s.readInhibit()
	if err == nil && ok {
		t.begun = true
		return nil
	}
	if err := s.setInhibit(t, fmt.Sprintf("the %s %s was interrupted and is being resumed", t.kind(), t.j.ID)); err != nil {
		return err
	}
	t.begun = true
	return nil
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

// endInhibit is the inhibit at the end of the transaction t, once its
// outcome is recorded: removed when it ended healthy (and, found in phase
// done, passed its reload, health check and no-AI check again), otherwise kept, with
// the outcome as its reason.
func (s *Switcher) endInhibit(t *txn) error {
	if t.j.RequalifyFailed {
		if err := s.setInhibit(t, fmt.Sprintf("the %s %s ended %s, but after an interruption current did not pass its reload, health check and no-AI check again", t.kind(), t.j.ID, t.j.Result)); err != nil {
			return err
		}
		s.step(9, "decisions stay inhibited (%s): current did not pass its reload, health check and no-AI check again", InhibitFile)
		return nil
	}
	if t.healthy() {
		if err := removeFileIn(s.stateRoot, InhibitFile); err != nil {
			return failf(CodeRecordFailed, "%s not removed: %s", InhibitFile, q(err.Error()))
		}
		s.step(9, "decisions no longer inhibited: %s removed", InhibitFile)
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
// (any command settles it first) and when there is no inhibit.
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
	if j, ok, err := s.readJournal(); err != nil || ok {
		if err == nil {
			err = failf(CodeStateInvalid, "the %s %s (transaction %s) is still open in phase %s; run any command to settle it first", (&txn{j: j}).kind(), j.ID, j.Txn, j.Phase)
		}
		s.printf("inhibit-clear: FAILED: %s", describe(err))
		return ExitFailed
	}
	in, ok, err := s.readInhibit()
	if err != nil && !ok {
		s.printf("inhibit-clear: FAILED: %s", describe(err))
		return ExitFailed
	}
	if !ok {
		s.printf("inhibit-clear: decisions are not inhibited; nothing to clear")
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
