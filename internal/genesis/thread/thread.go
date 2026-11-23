package thread

import (
	"errors"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/brief"
)

// The bounds of the store.
const (
	// MaxOpen is the most threads open at once. It is the wire's number, and a
	// start past it is refused genesis-busy.
	MaxOpen = ownerwire.MaxGenesisThreads
	// MaxFiles is the most thread files the store keeps.
	MaxFiles = 8
	// Expiry is how long an untouched thread lives.
	Expiry = 14 * 24 * time.Hour
	// Schema is the schema name of a thread file.
	Schema = "ataniq.genesis-thread/1"
	// DirName is the name of the store's folder in the owner state folder.
	DirName = "genesis"

	// maxFileBytes is the most a thread file may hold: the brief's cap and
	// room for the thread's own members.
	maxFileBytes = brief.MaxBriefBytes + 4096
	// maxEntries is the most files the folder may hold; a folder with more
	// is not a store of ours.
	maxEntries = 64
)

// State is the state of a thread.
type State string

// The states of a thread.
const (
	// StateOpen is an interview that goes on.
	StateOpen State = "open"
	// StateFinished is a frozen brief.
	StateFinished State = "finished"
	// StateDiscarded is a thread the owner dropped or that expired.
	StateDiscarded State = "discarded"
)

func (s State) valid() bool { return s == StateOpen || s == StateFinished || s == StateDiscarded }

// record is one thread. A record is never changed in place: a call makes a
// new one, writes it, and only then replaces the old one in the store.
type record struct {
	id      string
	state   State
	brief   brief.Brief
	catalog string
	created time.Time
	updated time.Time
	// frozen is the digest of the brief of a finished thread, and "" for
	// any other.
	frozen string
}

// CodeBadParams is the gate's word for params the store refuses to take as
// they are, which the gate's own decoder normally has refused first.
const CodeBadParams = "bad-params"

// Refusal is the answer of the store to a call it refuses for what was sent or
// for the state of the thread: it carries the gate's word for it. Its text is
// the word and nothing of the data.
type Refusal struct {
	code string
}

// Error returns the gate's word.
func (r *Refusal) Error() string { return "genesis: " + r.code }

// GateCode returns the word the seat gate answers the refusal with: one of the
// six words of seat.genesis, or [CodeBadParams].
func (r *Refusal) GateCode() string { return r.code }

func refuse(code string) error { return &Refusal{code: code} }

// RefusalCode returns the gate's word of err when err is, or wraps, a
// [*Refusal].
func RefusalCode(err error) (string, bool) {
	var r *Refusal
	if errors.As(err, &r) {
		return r.code, true
	}
	return "", false
}

// ErrClosed is the error of a call to a store that was closed.
var ErrClosed = errors.New("genesis: the thread store is closed")

// gateWord maps each refusal code of the brief and the walker to the word the
// seat gate answers it with. The empty word says the refusal is the host's
// own fault and not the owner's mistake (a brief that breaks its own rules,
// a catalog of another version): the call fails, and the cause is logged.
// A test of package genesis holds this map to the wire's.
var gateWord = map[brief.Code]string{
	brief.CodeSecretShaped: ownerwire.CodeGenesisSecretShaped,
	brief.CodeTooLarge:     ownerwire.CodeGenesisOverCap,

	brief.CodeNodeUnknown:     ownerwire.CodeGenesisBadAnswer,
	brief.CodeNodeNotEligible: ownerwire.CodeGenesisBadAnswer,
	brief.CodeAnswerInvalid:   ownerwire.CodeGenesisBadAnswer,
	brief.CodeRoundInvalid:    ownerwire.CodeGenesisBadAnswer,
	brief.CodeValueInvalid:    ownerwire.CodeGenesisBadAnswer,
	brief.CodeFieldName:       ownerwire.CodeGenesisBadAnswer,

	brief.CodeBriefInvalid:    "",
	brief.CodeDomainMismatch:  "",
	brief.CodeNotInferred:     "",
	brief.CodeCatalogInvalid:  "",
	brief.CodeCatalogMismatch: "",
}

// GateWord returns the word the seat gate answers a refusal code of the brief
// and the walker with. The word is "" when the refusal is not the owner's
// mistake, and ok is false for a code the store does not know.
func GateWord(c brief.Code) (word string, ok bool) {
	word, ok = gateWord[c]
	return word, ok
}

// mapAnswerError turns the error of the walker or the brief, on an answer the
// owner sent, into the store's: a [*Refusal] for the owner's mistake, and the
// error itself, which the gate answers failed, for the host's own fault.
func mapAnswerError(err error) error {
	if code, ok := brief.CodeOf(err); ok {
		if word := gateWord[code]; word != "" {
			return refuse(word)
		}
	}
	return err
}

// secretShaped reports whether err is the brief's refusal of text that looks
// like a secret.
func secretShaped(err error) bool {
	code, ok := brief.CodeOf(err)
	return ok && code == brief.CodeSecretShaped
}
