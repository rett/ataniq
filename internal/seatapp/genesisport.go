package seatapp

import (
	"context"
	"errors"
)

// Genesis is the part of a [Daemon] that runs the guided new-project
// interview (the gate's seat.genesis): a port the app finds by a type
// assertion on [Config].Daemon, as it finds a [ChallengeDropper]. Package
// seatlink implements it over the seat gate, and only when the gate's hello
// lists "genesis"; otherwise every call is [ErrNotOffered] and nothing is
// sent. A Daemon without it, or one whose calls are ErrNotOffered, has no
// "New project" button on the card list, and the project pages answer the
// app's "not offered" page.
//
// Genesis makes one call of the verb in c and returns the host's answer.
// The words of the call and of the answer are this package's own plain
// values (the app imports nothing of the repository): the port converts
// them to the wire's and back, and checks each length again. An answer is
// a message from the owner: it fills a field of the project's brief and
// nothing else, and no call of this port decides, approves or starts
// anything.
//
// It returns [ErrGenesisBusy] for a start while two threads are open,
// [ErrGenesisUnknownThread] for a thread the host holds none of (never
// started, finished long ago and pruned, discarded or expired),
// [ErrGenesisBadAnswer] for an answer or a change the question does not
// take, [ErrGenesisOverCap] for one past a cap of the brief,
// [ErrGenesisSecretShaped] for text that looks like a password or a key,
// [ErrGenesisDone] for an answer, a change or a discard of a finished
// thread, [ErrGenesisStaleRound] for an answer to a round the thread has
// already taken (a post sent twice, a second tab),
// [ErrBusy] when the port's room or the gate's rate is spent (nothing is
// sent, or nothing was taken), [ErrNotOffered] when the gate does not run
// the interview, and [ErrDaemonUnavailable] when it cannot be reached. Any
// other error, an answer not of the wire's form among them, holds nothing of
// the call or of the answer, and the app shows its fixed words for a
// failure.
type Genesis interface {
	Genesis(ctx context.Context, c GenesisCall) (GenesisView, error)
}

// The verbs of a [GenesisCall]: the wire's words (package ownerwire), which a
// test of package seatlink holds equal.
const (
	GenesisVerbStart   = "start"
	GenesisVerbList    = "list"
	GenesisVerbRound   = "round"
	GenesisVerbAnswer  = "answer"
	GenesisVerbChange  = "change"
	GenesisVerbSummary = "summary"
	GenesisVerbFinish  = "finish"
	GenesisVerbDiscard = "discard"
)

// The kinds of a [GenesisPick].
const (
	// GenesisKindChoice is one option, or a step of a scale: one id.
	GenesisKindChoice = "choice"
	// GenesisKindChoices is one or more options of a multiple choice.
	GenesisKindChoices = "choices"
	// GenesisKindOther is the owner's own text instead of an option.
	GenesisKindOther = "other"
	// GenesisKindYouDecide leaves the choice to the host.
	GenesisKindYouDecide = "you_decide"
	// GenesisKindSkip leaves the question to its default.
	GenesisKindSkip = "skip"
)

// The states of a [GenesisView].
const (
	// GenesisStateQuestion is a round of one to three questions.
	GenesisStateQuestion = "question"
	// GenesisStateSummary is the summary of the brief, to change from or
	// to finish.
	GenesisStateSummary = "summary"
	// GenesisStateFinished is the frozen brief and its digest.
	GenesisStateFinished = "finished"
	// GenesisStateDiscarded is a thread dropped by the call.
	GenesisStateDiscarded = "discarded"
	// GenesisStateThreads is the answer to a list.
	GenesisStateThreads = "threads"
)

// The types of a [GenesisQuestion].
const (
	GenesisTypeSingle = "single"
	GenesisTypeMulti  = "multi"
	GenesisTypeYesNo  = "yesno"
	GenesisTypeScale  = "scale"
)

// The bounds of the interview: this package's own copies of the wire's
// (package ownerwire), which a test of package seatlink holds equal.
const (
	// GenesisThreadLength is the length of a thread id: lower-case
	// hexadecimal digits.
	GenesisThreadLength = 16
	// MaxGenesisThreads is the most threads open at once.
	MaxGenesisThreads = 2
	// MaxGenesisRounds is the most rounds of an interview.
	MaxGenesisRounds = 6
	// MaxGenesisQuestions is the most questions of a round, and the most
	// answers of a call.
	MaxGenesisQuestions = 3
	// MaxGenesisOptions is the most options of a question.
	MaxGenesisOptions = 8
	// MaxGenesisRows is the most rows of a summary, in all its groups.
	MaxGenesisRows = 64
	// MaxGenesisOther is the longest text of the owner's own, in bytes.
	MaxGenesisOther = 200
	// MaxGenesisPrompt, MaxGenesisLabel, MaxGenesisExample and
	// MaxGenesisValue are the longest texts of a question, an option's
	// label, an option's example and a summary row's value, in bytes.
	MaxGenesisPrompt  = 160
	MaxGenesisLabel   = 60
	MaxGenesisExample = 100
	MaxGenesisValue   = 512
	// ScaleSteps is the number of steps of a scale: 1 to 5.
	ScaleSteps = 5
)

// GenesisCall is one call of the interview. Verb is one of the verbs; the
// other fields are the ones the verb takes, and zero for the rest:
//
//	verb     fields
//	start    none
//	list     none
//	round    Thread
//	answer   Thread, Round, Answers (one to three, each to another question)
//	change   Thread, Node, Pick
//	summary  Thread
//	finish   Thread
//	discard  Thread
type GenesisCall struct {
	Verb    string
	Thread  string
	Round   int
	Answers []GenesisAnswer
	Node    string
	Pick    GenesisPick
}

// GenesisPick is the owner's choice for one question: a kind and what the
// kind needs. IDs are option ids (one for a choice, one or more for
// choices); Text is the owner's own words of an "other", 1 to
// [MaxGenesisOther] bytes of plain text.
type GenesisPick struct {
	Kind string
	IDs  []string
	Text string
}

// GenesisAnswer is the owner's answer to one question of a round.
type GenesisAnswer struct {
	Node string
	Pick GenesisPick
}

// GenesisView is the host's answer to a call. Every text in it is the
// host's (the catalog's words) but the owner's own words in a row marked
// OwnersWords; the app shows each with [Text].
type GenesisView struct {
	// State is one of the states.
	State string
	// Thread is the thread's id; empty for a list.
	Thread string
	// Round is the round asked now, or the last one answered; Of is the
	// most rounds. Filled of Total required fields are filled.
	Round, Of     int
	Filled, Total int
	// Questions are the questions of a round: one to three for a question,
	// none otherwise.
	Questions []GenesisQuestion
	// YouChose, DecidedForYou and Defaults are the summary's three groups,
	// for a summary and a finished thread.
	YouChose, DecidedForYou, Defaults []GenesisRow
	// Reset is how many later answers the change of the call cleared.
	Reset int
	// Digest is the frozen brief's digest, 64 lower-case hexadecimal
	// digits, for a finished thread.
	Digest string
	// Threads are the open threads, oldest first, for a list.
	Threads []GenesisThread
}

// GenesisQuestion is one question of a round, in the catalog's words.
type GenesisQuestion struct {
	ID, Type, Prompt, Topic string
	// Options are the choices of a single or multiple choice and of a
	// yes-or-no question; a scale has none (its steps are 1 to
	// [ScaleSteps]).
	Options []GenesisOption
	// Other says the question takes the owner's own words, YouDecide that
	// it may be left to the host, and Skip that it has a default and may be
	// skipped.
	Other, YouDecide, Skip bool
}

// GenesisOption is one choice of a question.
type GenesisOption struct {
	ID, Label, Example string
}

// GenesisRow is one field on the summary: the question it answers (Label)
// and the answer (Value).
type GenesisRow struct {
	Node, Field, Label, Value string
	// OwnersWords says Value is the owner's own text, which a page shows
	// quoted and marked as the owner's words.
	OwnersWords bool
	// Question is the question of Node as the catalog the thread is walked
	// under words it, for a change of the answer; nil when the port does not
	// hold that catalog, and the row then offers no change.
	Question *GenesisQuestion
}

// GenesisThread is one open thread of a list: where it stands, and nothing
// of its answers.
type GenesisThread struct {
	Thread, State string
	Round, Of     int
	Filled, Total int
}

// The errors of the interview that the app tells apart.
var (
	// ErrGenesisBusy says two threads are open already.
	ErrGenesisBusy = errors.New("seatapp: two projects are open already")
	// ErrGenesisUnknownThread says the host holds no thread of this id.
	ErrGenesisUnknownThread = errors.New("seatapp: no project of this id")
	// ErrGenesisBadAnswer says the question does not take the answer.
	ErrGenesisBadAnswer = errors.New("seatapp: the answer was not accepted")
	// ErrGenesisOverCap says the answer would take the brief past a cap.
	ErrGenesisOverCap = errors.New("seatapp: the answer is too long for the brief")
	// ErrGenesisSecretShaped says the text looks like a password or a key.
	ErrGenesisSecretShaped = errors.New("seatapp: the text looks like a secret")
	// ErrGenesisDone says the thread is finished.
	ErrGenesisDone = errors.New("seatapp: the project is finished")
	// ErrGenesisStaleRound says the round was already answered: the
	// thread has moved on, and its page shows where it stands.
	ErrGenesisStaleRound = errors.New("seatapp: the round was already answered")
)

// ValidThreadID reports whether s is a thread id: [GenesisThreadLength]
// lower-case hexadecimal digits. An id that is not one is never sent.
func ValidThreadID(s string) bool {
	if len(s) != GenesisThreadLength {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
