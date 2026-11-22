package switcher

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// OwedFile holds, in the state folder, the outcomes of settled
// transactions that are still owed to whoever asked for them
// (var/lib/authority-host/owed.json). Every settled transaction adds its
// outcome there before its journal is removed, whichever command settled
// it, and nothing but the owner's acknowledgement (ah-switch ack) or a
// retry of the same request removes it: a result line written to the
// switch's own output is no proof that it reached anyone. It is written
// whole and synced, and read with the checks of the other state files;
// deploy.json, the record of the last command, never carries it, so a
// refusal that records itself cannot lose it.
const OwedFile = "owed.json"

// maxOwed bounds the outcomes owed.json holds: a switch or rollback is
// refused while one is owed, so more than one is only ever left by
// commands that settle journals (recover, a refused request).
const maxOwed = 16

// owedOutcome is one outcome owed: the transaction, its request (the
// direction and the release it went to, as requestID makes it), what was
// asked, the releases (id: the one switched to; from: where current
// pointed before; current: where it points after) and how it ended.
type owedOutcome struct {
	Txn             string `json:"txn"`
	RequestID       string `json:"request_id"`
	Direction       string `json:"direction"`
	Requested       string `json:"requested"`
	ID              string `json:"id"`
	FromID          string `json:"from_id"`
	CurrentID       string `json:"current_id"`
	Result          string `json:"result"`
	FailedStep      string `json:"failed_step"`
	RestoredHealth  string `json:"restored_health"`
	ServicesStopped bool   `json:"services_stopped"`
	Detail          string `json:"detail"`
	Time            string `json:"time"`
	// RequalifyFailed: the transaction was found in phase done and the
	// release current points at did not pass its reload and health check
	// again. It is kept apart from Result, which stays the outcome that
	// was decided: a retry of the request is a failure for as long as it
	// is owed, and decisions stay inhibited.
	RequalifyFailed bool `json:"requalify_failed,omitempty"`
}

// owedFile is the content of owed.json.
type owedFile struct {
	Outcomes []owedOutcome `json:"outcomes"`
}

func (o owedOutcome) kind() string {
	if o.Direction == DirRollback {
		return "rollback to"
	}
	return "switch to"
}

// record is o as printResult takes it.
func (o owedOutcome) record() Record {
	return Record{Result: o.Result, FailedStep: o.FailedStep, CurrentID: o.CurrentID, RestoredHealth: o.RestoredHealth, ServicesStopped: o.ServicesStopped}
}

// matches reports whether a request (arg, rollback) is a retry of o: the
// same direction and the same request id. A rollback asked as previous is
// a retry of any rollback owed, bound to the release that one went to
// (previous is never resolved anew: it would name the release the first
// one left); asked by an id, only of the rollback to that id. A forward
// switch is a retry of the switch to the same id.
func (o owedOutcome) matches(arg string, rollback bool) bool {
	if rollback {
		return o.Direction == DirRollback && (arg == "previous" || o.RequestID == requestID(DirRollback, arg))
	}
	return o.Direction == DirForward && o.RequestID == requestID(DirForward, arg)
}

// check reports what in o is not what a switch writes; its text holds no
// value of o.
func (o owedOutcome) check() error {
	id, err := ParseID(o.ID)
	switch {
	case !txnPattern.MatchString(o.Txn):
		return errors.New("txn")
	case !oneOf(o.Direction, DirForward, DirRollback):
		return errors.New("direction")
	case err != nil || o.RequestID != requestID(o.Direction, id.Name):
		return errors.New("id or request_id")
	case o.Requested != o.ID && (o.Requested != "previous" || o.Direction != DirRollback):
		return errors.New("requested")
	case !oneOf(o.Result, ResultOK, ResultFailed, ResultRolledBack, ResultFailedRecovery):
		return errors.New("result")
	case !oneOf(o.RestoredHealth, "", "ok", "failed"):
		return errors.New("restored_health")
	}
	for _, other := range []string{o.FromID, o.CurrentID} {
		if other == "" {
			continue
		}
		if _, err := ParseID(other); err != nil {
			return errors.New("a release id")
		}
	}
	return nil
}

// reportOwed prints the outcome o as it is owed, and, when its
// requalification failed, that decisions stay inhibited (ack is the command
// that acknowledges what is owed, see ackCommand); it says whether it was
// all written.
func (s *Switcher) reportOwed(o owedOutcome, ack string) error {
	err := s.printResult(o.record())
	if o.RequalifyFailed {
		if werr := s.emit("%s", requalifyFailedLine(o.CurrentID, ack)); err == nil {
			err = werr
		}
	}
	return err
}

// requalifyFailedLine is the line that says a transaction found in phase
// done did not pass its reload, health check and no-AI check again,
// current being cur; ack is the command that acknowledges what is owed.
func requalifyFailedLine(cur, ack string) string {
	if cur == "" {
		cur = "none"
	}
	return fmt.Sprintf("result: decisions stay inhibited: %s did not pass its reload, health check and no-AI check again after the interruption; look at the host, acknowledge the outcome with %s, then switch, or run ah-switch inhibit-clear --asked WORDS", cur, ack)
}

// ackCommand is the command that acknowledges the outcomes owed: ack names
// the transactions of every one of them, as status shows them.
func ackCommand(owed []owedOutcome) string {
	return "ah-switch ack --txn " + strings.Join(owedTxns(owed), ",") + " --asked WORDS"
}

// owedTxns is the transactions of the outcomes owed, in their order.
func owedTxns(owed []owedOutcome) []string {
	var ids []string
	for _, o := range owed {
		ids = append(ids, o.Txn)
	}
	return ids
}

// sameTxns reports whether a and b name the same transactions, in any
// order.
func sameTxns(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a, b = append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// checkAckTxns reports what in the transactions given to ack is not a set
// of transaction ids; its text holds none of them.
func checkAckTxns(txns []string) error {
	if len(txns) == 0 || len(txns) > maxOwed {
		return fmt.Errorf("1 to %d transactions", maxOwed)
	}
	seen := map[string]bool{}
	for _, id := range txns {
		if !txnPattern.MatchString(id) {
			return errors.New("a transaction id is 32 hex digits")
		}
		if seen[id] {
			return errors.New("a transaction is named twice")
		}
		seen[id] = true
	}
	return nil
}

// readOwed reads owed.json; a missing file is nothing owed. One that is
// not what a switch writes is an error, never taken as nothing owed.
func (s *Switcher) readOwed() ([]owedOutcome, error) {
	data, err := s.readStateFile(OwedFile, maxStateFile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f owedFile
	if err := decodeStrict(data, &f); err != nil {
		return nil, failf(CodeStateInvalid, "%s does not parse", OwedFile)
	}
	if len(f.Outcomes) == 0 || len(f.Outcomes) > maxOwed {
		return nil, failf(CodeStateInvalid, "%s holds no outcome or more than %d", OwedFile, maxOwed)
	}
	for i, o := range f.Outcomes {
		if err := o.check(); err != nil {
			return nil, failf(CodeStateInvalid, "%s: outcome %d does not describe a transaction: %s", OwedFile, i+1, err.Error())
		}
	}
	return f.Outcomes, nil
}

// writeOwed writes owed.json as owed, whole and synced, or removes it
// when nothing is owed.
func (s *Switcher) writeOwed(owed []owedOutcome) error {
	if len(owed) == 0 {
		return removeFileIn(s.stateRoot, OwedFile)
	}
	return writeJSONIn(s.stateRoot, OwedFile, owedFile{Outcomes: owed}, 0o644)
}

// addOwed adds the outcome of the settled transaction rec records to
// owed.json, replacing an outcome of the same transaction (a settling
// that is done again), and returns the outcomes owed then. An owed.json
// that cannot be read is an error: the outcomes it holds would be lost.
func (s *Switcher) addOwed(rec Record, direction string, requalifyFailed bool) ([]owedOutcome, error) {
	owed, err := s.readOwed()
	if err != nil {
		return nil, err
	}
	o := owedOutcome{
		Txn:             rec.Txn,
		RequestID:       rec.RequestID,
		Direction:       direction,
		Requested:       rec.Requested,
		ID:              rec.ID,
		FromID:          rec.PreviousID,
		CurrentID:       rec.CurrentID,
		Result:          rec.Result,
		FailedStep:      rec.FailedStep,
		RestoredHealth:  rec.RestoredHealth,
		ServicesStopped: rec.ServicesStopped,
		Detail:          boundDetail(rec.Detail),
		Time:            rec.Time,
		RequalifyFailed: requalifyFailed,
	}
	kept := owed[:0]
	for _, old := range owed {
		if old.Txn != o.Txn {
			kept = append(kept, old)
		}
	}
	if len(kept)+1 > maxOwed {
		return nil, failf(CodeRecordFailed, "%s would hold more than %d outcomes; acknowledge them with %s", OwedFile, maxOwed, ackCommand(kept))
	}
	kept = append(kept, o)
	return kept, s.writeOwed(kept)
}

// answerOwed is what a switch or rollback (arg, rollback) does first, once
// any journal is settled: when an outcome is owed and the request is a
// retry of it (see matches), it reports that outcome and does nothing
// else; the outcome stays owed until the owner's ack (a report that could
// not be written fails the command); when outcomes are owed and the
// request is another one, it reports them, leaves them owed and refuses
// the request. done says the command ends there, with code.
func (s *Switcher) answerOwed(arg string, rollback bool) (code int, done bool) {
	if s.stateRoot == nil {
		return 0, false
	}
	owed, err := s.readOwed()
	if err != nil {
		s.printf("state: FAILED: %s", describe(err))
		s.printf("result: failed before step 1; nothing changed or recorded")
		return ExitFailed, true
	}
	if len(owed) == 0 {
		return 0, false
	}
	match := -1
	for i, o := range owed {
		if o.matches(arg, rollback) {
			match = i
		}
	}
	ack := ackCommand(owed)
	if match < 0 {
		for _, o := range owed {
			s.printf("owed: the %s %s (request %s, transaction %s) ended %s at %s and its outcome is not acknowledged yet", o.kind(), o.ID, q(o.RequestID), o.Txn, o.Result, q(o.Time))
			s.reportOwed(o, ack)
		}
		s.printf("refused: an outcome is owed; run the same request again to have it reported, and %s once it is seen; nothing else is done", ack)
		return ExitFailed, true
	}
	o := owed[match]
	kind := "switch to"
	if rollback {
		kind = "rollback to"
	}
	s.printf("settled: this %s %s has been settled already; it is not done again (request %s, transaction %s)", kind, arg, q(o.RequestID), o.Txn)
	if o.Detail != "" {
		s.printf("settled: %s", o.Detail)
	}
	// An outcome that could not be written (or flushed) has not been
	// delivered: it is never taken back then.
	if err := s.reportOwed(o, ack); err != nil {
		s.errorf("the outcome of this %s %s could not be written: %s; it stays owed, and the same request run again reports it", kind, arg, q(err.Error()))
		return ExitFailed, true
	}
	// Reporting is not acknowledging: the outcome stays owed until the
	// owner's ack, whoever runs the request again (a sudo rule for the
	// switch form allows the deploying user to).
	switch {
	case s.cfg.Dry:
		s.printf("settled: dry run: the outcome is still owed; nothing changed")
	case o.RequalifyFailed:
		s.printf("settled: the outcome stays owed: its release did not pass its reload, health check and no-AI check again")
	default:
		s.printf("settled: the outcome stays owed until the owner acknowledges it with %s", ack)
	}
	if o.Result == ResultOK && !o.RequalifyFailed {
		return ExitOK, true
	}
	return ExitFailed, true
}

// maxAckWords bounds the owner's words of ack.
const maxAckWords = 512

// Ack runs ah-switch ack --txn ID[,ID...] --asked WORDS: the owner's
// acknowledgement that every outcome owed was seen. txns names the
// transactions of the outcomes the owner saw (status, the same request run
// again and ack itself print them): when they are not, in any order,
// exactly the transactions owed now, nothing is acknowledged or recorded,
// since the owner gave the words before seeing what is owed now (an ack
// whose answer was lost, repeated after another outcome became owed).
// words, what the owner asked or saw, are recorded in deploy.json with the
// time and the transactions acknowledged, then owed.json is removed. An ack
// of the transactions deploy.json already records as acknowledged (one
// interrupted before that removal, or repeated after it) keeps the recorded
// words and time: it only finishes the removal when they are still owed,
// and leaves any outcome owed since as it is. It runs no program, is
// refused while a transaction is open (any command settles it first), the
// journal held by the inhibit after an interrupted healthy end included,
// and when nothing is owed, and a dry run changes nothing.
func (s *Switcher) Ack(ctx context.Context, txns []string, words string) int {
	if !filepath.IsAbs(s.cfg.Root) {
		s.errorf("--root %s is not an absolute path", q(s.cfg.Root))
		return ExitUsage
	}
	words = strings.TrimSpace(words)
	if words == "" || len(words) > maxAckWords {
		s.errorf("ack needs --asked WORDS: what the owner asked or saw, 1 to %d bytes", maxAckWords)
		return ExitUsage
	}
	if err := checkAckTxns(txns); err != nil {
		s.errorf("ack needs --txn ID[,ID...]: the transactions of every outcome owed, as status shows them (%s)", err.Error())
		return ExitUsage
	}
	s.printf("ah-switch: ack (root %s)", q(s.cfg.Root))
	lk, code := s.lockWith(false, false)
	if lk == nil {
		return code
	}
	defer lk.release()
	defer s.closeRoots()
	if s.stateRoot == nil || s.lockFile == nil {
		s.printf("ack: no state folder or lock file; nothing is owed")
		return ExitFailed
	}
	if j, ok, err := s.readJournal(); err != nil || ok {
		if err == nil {
			err = failf(CodeStateInvalid, "the %s %s (transaction %s) is still open in phase %s; run any command to settle it first", (&txn{j: j}).kind(), j.ID, j.Txn, j.Phase)
		}
		s.printf("ack: FAILED: %s", describe(err))
		return ExitFailed
	}
	if _, settling, there, err := s.readInhibit(); settling != nil || err != nil && !there {
		if err == nil {
			err = failf(CodeStateInvalid, "%s holds the journal of the %s %s (transaction %s), which is still to be settled; run any switch, rollback or recover to settle it first", InhibitFile, (&txn{j: *settling}).kind(), settling.ID, settling.Txn)
		}
		s.printf("ack: FAILED: %s", describe(err))
		return ExitFailed
	}
	owed, err := s.readOwed()
	if err != nil {
		s.printf("ack: FAILED: %s", describe(err))
		return ExitFailed
	}
	rec, _, err := s.readRecord()
	if err != nil {
		s.printf("ack: FAILED: %s; the acknowledgement could not be recorded", describe(err))
		return ExitFailed
	}
	// The transactions deploy.json records are the commit mark of an
	// acknowledgement.
	acked := strings.Fields(rec.OutcomeAckedTxn)
	ids := owedTxns(owed)
	if !sameTxns(txns, ids) {
		if len(acked) > 0 && sameTxns(txns, acked) {
			s.printf("ack: transaction(s) %s were acknowledged by the owner at %s (asked: %s): recorded already, so those words are kept and these are not recorded", strings.Join(acked, ","), rec.OutcomeAckedAt, q(rec.OutcomeAckedAsked))
			if len(owed) > 0 {
				s.printf("ack: the outcomes owed since (transaction(s) %s) are not acknowledged by this; look at them with ah-switch status, then run %s", strings.Join(ids, ","), ackCommand(owed))
			}
			return ExitOK
		}
		if len(owed) == 0 {
			s.printf("ack: no outcome is owed; nothing to acknowledge")
			return ExitFailed
		}
		s.printf("ack: refused: --txn names %s, but the outcomes owed are transaction(s) %s; nothing is acknowledged or recorded. Look at them with ah-switch status, then run %s", strings.Join(txns, ","), strings.Join(ids, ","), ackCommand(owed))
		return ExitFailed
	}
	// The owner acknowledges what was shown: an outcome that could not be
	// written or flushed has not been delivered, so it is never taken back
	// (the rule of a retry, see answerOwed).
	ack := ackCommand(owed)
	var shown error
	for _, o := range owed {
		err := s.emit("ack: the %s %s (request %s, transaction %s) ended %s at %s", o.kind(), o.ID, q(o.RequestID), o.Txn, o.Result, q(o.Time))
		if rerr := s.reportOwed(o, ack); err == nil {
			err = rerr
		}
		if err != nil && shown == nil {
			shown = err
		}
	}
	if shown != nil {
		s.errorf("the outcomes owed could not be written: %s; they stay owed, nothing is recorded, and ack run again shows and acknowledges them", q(shown.Error()))
		return ExitFailed
	}
	if s.cfg.Dry {
		s.printf("ack: would record the owner's words and remove %s", q(filepath.Join(s.stateDir(), OwedFile)))
		return ExitOK
	}
	// When the transactions recorded are these, an ack was recorded and
	// killed before owed.json was removed. Only that removal is finished,
	// and the owner's original words and time are kept.
	if sameTxns(acked, ids) {
		if err := s.writeOwed(nil); err != nil {
			s.printf("ack: FAILED: %s not removed: %s", OwedFile, q(err.Error()))
			return ExitFailed
		}
		s.printf("ack: %d outcome(s) were acknowledged by the owner at %s (asked: %s) before an interruption: recorded already, so those words are kept and these are not recorded; %s is removed", len(owed), rec.OutcomeAckedAt, q(rec.OutcomeAckedAsked), OwedFile)
		return ExitOK
	}
	rec.OutcomeAckedAt = s.now()
	rec.OutcomeAckedAsked = words
	rec.OutcomeAckedTxn = strings.Join(ids, " ")
	if err := s.rewriteRecord(&rec); err != nil {
		s.printf("ack: FAILED: %s not written: %s; the outcomes stay owed", DeployFile, q(err.Error()))
		return ExitFailed
	}
	if err := s.crash(CrashAcked); err != nil {
		return ExitFailed
	}
	if err := s.writeOwed(nil); err != nil {
		s.printf("ack: FAILED: %s not removed: %s", OwedFile, q(err.Error()))
		return ExitFailed
	}
	s.printf("ack: %d outcome(s) acknowledged by the owner at %s (asked: %s); switches and rollbacks are no longer refused for them", len(owed), rec.OutcomeAckedAt, q(words))
	return ExitOK
}

// owedStatus is what status prints of owed.json. pending: a transaction is
// still open, so settling it may still change the outcome owed under its
// transaction (its requalification may fail), and ack is refused until it
// is settled: no ack command is printed then, so that none is copied for
// an outcome that is not final.
func (s *Switcher) owedStatus(pending bool) ([]string, error) {
	owed, err := s.readOwed()
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, o := range owed {
		if pending {
			lines = append(lines, fmt.Sprintf("outcome owed, not final (request %s, transaction %s): the %s %s ended %s at %s, but a transaction is still open and settling it may change this outcome; the next switch, rollback or recover settles it, then status shows the command that acknowledges it", q(o.RequestID), o.Txn, o.kind(), o.ID, o.Result, q(o.Time)))
			continue
		}
		line := fmt.Sprintf("outcome owed (request %s, transaction %s): the %s %s ended %s at %s; the same request run again reports it, %s acknowledges it, and any other switch or rollback is refused until then", q(o.RequestID), o.Txn, o.kind(), o.ID, o.Result, q(o.Time), ackCommand(owed))
		if o.RequalifyFailed {
			line += "; after an interruption its release did not pass its reload, health check and no-AI check again, so decisions stay inhibited and the same request fails until it is acknowledged"
		}
		lines = append(lines, line)
	}
	return lines, nil
}
