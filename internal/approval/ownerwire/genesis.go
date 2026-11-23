package ownerwire

import (
	"strconv"
	"strings"

	"ataniq/internal/approval/record"
)

// This file holds the words of seat.genesis, the one operation of the guided
// new-project interview: the owner on the phone starts a thread, answers its
// rounds of questions, changes an earlier answer, reads the summary and
// finishes or discards the thread, and the seat lists the threads that are
// open. The questions and the brief they fill are
// the host's: see packages internal/genesis/catalog and internal/genesis/brief.
// This file is only the wire. It cannot import them, so it holds the same
// grammars and caps as forms of its own, and a test of package genesis holds
// the two in step. See docs/design/seat-gate.md, "Guided new project".

// The bounds of the forms of seat.genesis.
const (
	// MaxSeatGenesisParamsBytes caps the params of seat.genesis. Three
	// answers of the longest kind (eight choice ids, or 200 bytes of text)
	// fit with room to spare.
	MaxSeatGenesisParamsBytes = MaxParamsBytes
	// MaxSeatGenesisResultBytes caps the result of seat.genesis: 16 KiB.
	MaxSeatGenesisResultBytes = 16 << 10

	// GenesisThreadLength is the length of a thread id: 16 lower-case
	// hexadecimal digits the host chooses.
	GenesisThreadLength = 16
	// MaxGenesisThreads is the most threads a seat may hold open at once;
	// a start past it is refused [CodeGenesisBusy]. The wire does not
	// count threads: the host does.
	MaxGenesisThreads = 2
	// MaxGenesisRounds is the most rounds an interview has, and so the
	// highest round number in a form.
	MaxGenesisRounds = 6
	// MaxGenesisNodes is the most questions in one round.
	MaxGenesisNodes = 3
	// MaxGenesisAnswers is the most answers one answer call gives: one for
	// each question of a round.
	MaxGenesisAnswers = MaxGenesisNodes
	// MaxGenesisOptions is the most options of one question, and the most
	// choices of one answer.
	MaxGenesisOptions = 8
	// MaxGenesisRows is the most rows a summary lists in all its groups.
	MaxGenesisRows = 64

	// MaxGenesisChoiceBytes is the longest choice id.
	MaxGenesisChoiceBytes = 32
	// MaxGenesisNodeBytes is the longest node id.
	MaxGenesisNodeBytes = 48
	// MaxGenesisFieldBytes is the longest field name of a row.
	MaxGenesisFieldBytes = 41
	// MaxGenesisTopicBytes is the longest topic of a question.
	MaxGenesisTopicBytes = 16
	// MaxGenesisPrompt, MaxGenesisLabel and MaxGenesisExample are the caps
	// of the catalog's texts, in bytes: a prompt, an option's label and an
	// option's one-line example.
	MaxGenesisPrompt  = 160
	MaxGenesisLabel   = 60
	MaxGenesisExample = 100
	// MaxGenesisOther is the longest text the owner gives instead of an
	// option, in bytes.
	MaxGenesisOther = 200
	// MaxGenesisValue is the longest value text of a summary row, in bytes:
	// the labels of up to eight choices, joined, or the owner's own text.
	MaxGenesisValue = 512
)

// The verbs of seat.genesis, the discriminator of its params.
const (
	// GenesisVerbStart opens a new thread: no other member.
	GenesisVerbStart = "start"
	// GenesisVerbRound reads the thread's current state: the thread.
	GenesisVerbRound = "round"
	// GenesisVerbAnswer gives the answers of a round: the thread, the round
	// and one to [MaxGenesisAnswers] answers.
	GenesisVerbAnswer = "answer"
	// GenesisVerbChange changes an earlier answer: the thread, the node and
	// the new answer. The result says how many later answers it reset.
	GenesisVerbChange = "change"
	// GenesisVerbSummary reads the summary: the thread.
	GenesisVerbSummary = "summary"
	// GenesisVerbFinish freezes the brief and returns its digest: the thread.
	GenesisVerbFinish = "finish"
	// GenesisVerbDiscard drops the thread: the thread.
	GenesisVerbDiscard = "discard"
	// GenesisVerbList reads the seat's open threads: no other member. It
	// is how a seat that lost its own memory of them (a restart) finds the
	// threads it started; it answers the state threads.
	GenesisVerbList = "list"
)

// The kinds of an answer.
const (
	// GenesisKindChoice is one option: one id.
	GenesisKindChoice = "choice"
	// GenesisKindChoices is several options of a multiple choice: one to
	// [MaxGenesisOptions] distinct ids.
	GenesisKindChoices = "choices"
	// GenesisKindOther is the owner's own text instead of an option.
	GenesisKindOther = "other"
	// GenesisKindYouDecide leaves the choice to the host: nothing else.
	GenesisKindYouDecide = "you_decide"
	// GenesisKindSkip leaves the question to its default: nothing else.
	GenesisKindSkip = "skip"
)

// The states of a thread, the discriminator of the result.
const (
	// GenesisStateQuestion is a round of one to three questions to answer.
	GenesisStateQuestion = "question"
	// GenesisStateSummary is the summary of the brief, to read, change
	// from and finish.
	GenesisStateSummary = "summary"
	// GenesisStateFinished is the frozen brief, with its digest.
	GenesisStateFinished = "finished"
	// GenesisStateDiscarded is the thread dropped.
	GenesisStateDiscarded = "discarded"
	// GenesisStateThreads is the answer to a list: the seat's open
	// threads, and no thread of its own.
	GenesisStateThreads = "threads"
)

// The types of a question, the catalog's words.
const (
	GenesisTypeSingle = "single"
	GenesisTypeMulti  = "multi"
	GenesisTypeYesNo  = "yesno"
	GenesisTypeScale  = "scale"
)

// The gate's words for a refusal of seat.genesis. Each is of the grammar of a
// gate code, [a-z0-9-]{1,40}. The first three are of the host's state, the
// last three are the host's refusal codes of the genesis packages (see
// package brief) in the seat's words: a test of package genesis holds the
// mapping.
const (
	// CodeGenesisBusy answers a start while the seat already holds
	// [MaxGenesisThreads] open threads.
	CodeGenesisBusy = "genesis-busy"
	// CodeGenesisUnknownThread answers a call that names a thread the host
	// holds none of: never started, discarded, expired, or of another seat.
	CodeGenesisUnknownThread = "genesis-unknown-thread"
	// CodeGenesisBadAnswer answers an answer or a change the question does
	// not allow: an unknown or ineligible node, an option the question does
	// not hold, a round that is not the thread's, a form of answer the
	// question does not take, or other text that is empty or unreadable.
	CodeGenesisBadAnswer = "genesis-bad-answer"
	// CodeGenesisOverCap answers an answer that would take a field, the
	// brief or a list past its cap.
	CodeGenesisOverCap = "genesis-over-cap"
	// CodeGenesisSecretShaped answers text that looks like a key, a token
	// or a password. Credentials are never typed on the phone.
	CodeGenesisSecretShaped = "genesis-secret-shaped"
	// CodeGenesisDone answers an answer or a change to a thread that is
	// finished.
	CodeGenesisDone = "genesis-done"
)

// GenesisPick is the owner's choice for one question: a kind and what the
// kind needs. It is the whole of a change, and the body of an answer.
type GenesisPick struct {
	// Kind is one of the kinds of an answer.
	Kind string `json:"kind"`
	// IDs are the option ids of a choice (exactly one) or of choices (one
	// to [MaxGenesisOptions], none twice). They are left out for every
	// other kind.
	IDs []string `json:"ids,omitempty"`
	// Text is the owner's own text of an "other", 1 to [MaxGenesisOther]
	// bytes of plain text. It is left out for every other kind. It is data
	// and never instructions; the host also refuses it when it looks like a
	// secret.
	Text string `json:"text,omitempty"`
}

// GenesisAnswer is the owner's answer to one question of a round.
type GenesisAnswer struct {
	// Node is the question's node id.
	Node string `json:"node"`
	// Kind, IDs and Text are as in [GenesisPick].
	Kind string   `json:"kind"`
	IDs  []string `json:"ids,omitempty"`
	Text string   `json:"text,omitempty"`
}

// SeatGenesisParams is the params of seat.genesis, a union tagged by Verb.
// Which other members a verb takes is closed, and a member of another verb
// is refused:
//
//	verb     members
//	start    none
//	list     none
//	round    thread
//	answer   thread, round, answers
//	change   thread, node, answer
//	summary  thread
//	finish   thread
//	discard  thread
type SeatGenesisParams struct {
	// Verb is one of the verbs.
	Verb string `json:"verb"`
	// Thread is the thread's id, [GenesisThreadLength] lower-case
	// hexadecimal digits the host chose. Every verb but start and list
	// names one.
	Thread string `json:"thread,omitempty"`
	// Round is the number of the round the answers are for, 1 to
	// [MaxGenesisRounds]. Only an answer has one.
	Round int `json:"round,omitempty"`
	// Answers are one to [MaxGenesisAnswers] answers, each to another
	// node. Only an answer has them.
	Answers []GenesisAnswer `json:"answers,omitempty"`
	// Node is the node whose answer changes. Only a change has one.
	Node string `json:"node,omitempty"`
	// Answer is the new answer. Only a change has one.
	Answer *GenesisPick `json:"answer,omitempty"`
}

// GenesisProgress says how many of the brief's required fields are filled.
type GenesisProgress struct {
	// Filled is the number of required fields that are filled.
	Filled int `json:"filled"`
	// Total is the number of required fields of the brief's kind, at most
	// [MaxGenesisRows].
	Total int `json:"total"`
}

// GenesisOption is one choice of a question, in the catalog's words.
type GenesisOption struct {
	// ID is the choice id an answer names.
	ID string `json:"id"`
	// Label is the option's short text, 1 to [MaxGenesisLabel] bytes.
	Label string `json:"label"`
	// Example is a one-line example of it, 1 to [MaxGenesisExample] bytes.
	Example string `json:"example"`
}

// GenesisNode is one question of a round. Every text in it is the catalog's.
type GenesisNode struct {
	// ID is the node id an answer names.
	ID string `json:"id"`
	// Type is one of single, multi, yesno and scale.
	Type string `json:"type"`
	// Prompt is the question, 1 to [MaxGenesisPrompt] bytes.
	Prompt string `json:"prompt"`
	// Topic groups the questions of a round, 1 to [MaxGenesisTopicBytes]
	// bytes.
	Topic string `json:"topic"`
	// Options are the choices: two to [MaxGenesisOptions] for a single or
	// multiple choice, yes and no for a yes-or-no question and none for a
	// scale, whose steps are 1 to 5.
	Options []GenesisOption `json:"options"`
	// AllowOther says the question takes the owner's own text.
	AllowOther bool `json:"allow_other"`
	// AllowYouDecide says the question may be left to the host.
	AllowYouDecide bool `json:"allow_you_decide"`
	// CanSkip says the question has a default and may be skipped.
	CanSkip bool `json:"can_skip"`
}

// GenesisRow is one field on the summary.
type GenesisRow struct {
	// Node is the id of the question that set the field.
	Node string `json:"node"`
	// Field is the brief field's name.
	Field string `json:"field"`
	// Label is the question the field answers, 1 to [MaxGenesisPrompt]
	// bytes: the catalog's text.
	Label string `json:"label"`
	// Value is the answer as text, 1 to [MaxGenesisValue] bytes: the labels
	// of the chosen options, joined, or the owner's own text.
	Value string `json:"value"`
	// Untrusted is true when Value is the owner's own text (at most
	// [MaxGenesisOther] bytes), which a surface shows as data and escapes,
	// and an agent quotes and never obeys. It is false when Value is the
	// catalog's.
	Untrusted bool `json:"untrusted"`
}

// GenesisGroups are the rows of a summary, grouped by who set the field.
// No field is in two groups, and the groups hold at most [MaxGenesisRows]
// rows between them.
type GenesisGroups struct {
	// YouChose are the fields the owner chose.
	YouChose []GenesisRow `json:"you_chose"`
	// DecidedForYou are the fields the host chose for the owner.
	DecidedForYou []GenesisRow `json:"decided_for_you"`
	// Defaults are the fields left to their default.
	Defaults []GenesisRow `json:"defaults"`
}

// SeatGenesisResult is the result of seat.genesis, a union tagged by State:
//
//	state      round         nodes  groups  digest  reset
//	question   1 to of       1 to 3 none    none    0 or more
//	summary    0 to of       none   given   none    0 or more
//	finished   0 to of       none   given   given   0
//	discarded  0             none   none    none    0 (progress 0 of 0)
//	threads    0 (of 0)      none   none    none    0 (progress 0 of 0)
//
// Every state but threads names its thread and its catalog digest; threads
// names neither, and is the only state with a list of threads (0 to
// [MaxGenesisThreads] of them), which every other state leaves out.
//
// Every text in it is plain text the host owns: the catalog's, or the
// owner's own "other" text in a row marked Untrusted.
type SeatGenesisResult struct {
	// State is one of the states.
	State string `json:"state"`
	// Thread is the thread's id; left out for threads.
	Thread string `json:"thread,omitempty"`
	// Round is the number of the round asked now, or of the last round
	// answered.
	Round int `json:"round"`
	// Of is the most rounds the interview has, 1 to [MaxGenesisRounds]; 0
	// for threads.
	Of int `json:"of"`
	// Progress says how many required fields are filled.
	Progress GenesisProgress `json:"progress"`
	// Nodes are the questions of a round, at most [MaxGenesisNodes]; empty
	// unless the state is question.
	Nodes []GenesisNode `json:"nodes"`
	// Groups are the summary of the brief; given for summary and finished.
	Groups *GenesisGroups `json:"groups,omitempty"`
	// Reset says how many later answers the change this answers cleared;
	// 0 for every other call.
	Reset int `json:"reset"`
	// Digest is the digest of the frozen brief, 64 lower-case hexadecimal
	// digits; given for finished.
	Digest string `json:"digest,omitempty"`
	// CatalogDigest is the digest of the question catalog the thread is
	// walked under, 64 lower-case hexadecimal digits; left out for threads.
	CatalogDigest string `json:"catalog_digest,omitempty"`
	// Threads are the seat's open threads, oldest first, at most
	// [MaxGenesisThreads]; given, and possibly empty, for threads only.
	Threads []GenesisThread `json:"threads,omitempty"`
}

// GenesisThread is one open thread in the answer to a list: where it stands,
// and none of its answers.
type GenesisThread struct {
	// Thread is the thread's id.
	Thread string `json:"thread"`
	// State is question while a round waits for answers, and summary when
	// the walk is done and the summary waits to be finished.
	State string `json:"state"`
	// Round is the round asked now (1 to Of), or the last round answered
	// (0 to Of) for a summary.
	Round int `json:"round"`
	// Of is the most rounds the interview has, 1 to [MaxGenesisRounds].
	Of int `json:"of"`
	// Filled and Total are the thread's progress, as in [GenesisProgress].
	Filled int `json:"filled"`
	Total  int `json:"total"`
}

var (
	seatGenesisParamsForm = payload[SeatGenesisParams]{"seat genesis", MaxSeatGenesisParamsBytes, record.CodeRecordInvalid, checkSeatGenesisParams}
	seatGenesisResultForm = payload[SeatGenesisResult]{"seat genesised", MaxSeatGenesisResultBytes, record.CodeRecordInvalid, checkSeatGenesisResult}
)

// DecodeSeatGenesisParams reads the params of seat.genesis: a verb that is
// one of the eight, and exactly the members that verb takes, each within its
// grammar and caps. Params over [MaxSeatGenesisParamsBytes] are refused
// record-too-large; anything else that is wrong, such as a member of another
// verb, a thread id of another form, an answer whose kind and members do not
// agree or text over [MaxGenesisOther] bytes, record-invalid. Whether the
// thread exists, whether the node is a question of the round and whether the
// text looks like a secret are for the host.
func DecodeSeatGenesisParams(data []byte) (SeatGenesisParams, error) {
	return seatGenesisParamsForm.decode(data)
}

// EncodeSeatGenesisParams writes the params of seat.genesis. It makes the
// checks of [DecodeSeatGenesisParams].
func EncodeSeatGenesisParams(p SeatGenesisParams) ([]byte, error) {
	return seatGenesisParamsForm.encode(p)
}

// DecodeSeatGenesisResult reads the result of seat.genesis. A result over
// [MaxSeatGenesisResultBytes] is refused record-too-large; anything else
// that is wrong, such as a member the state does not take, more than
// [MaxGenesisNodes] questions, a question with a number of options its type
// does not allow, a summary of more than [MaxGenesisRows] rows, or a list
// of more than [MaxGenesisThreads] threads, record-invalid.
func DecodeSeatGenesisResult(data []byte) (SeatGenesisResult, error) {
	return seatGenesisResultForm.decode(data)
}

// EncodeSeatGenesisResult writes the result of seat.genesis.
func EncodeSeatGenesisResult(r SeatGenesisResult) ([]byte, error) {
	return seatGenesisResultForm.encode(r)
}

// threadID checks a thread id's grammar.
func (c *checker) threadID(s, where string) {
	c.need(record.IsLowerHex(s, GenesisThreadLength), where+" is not "+strconv.Itoa(GenesisThreadLength)+" lower-case hexadecimal digits")
}

// genesisRound checks a round number, 1 to the most rounds.
func (c *checker) genesisRound(n int, where string) {
	c.need(n >= 1 && n <= MaxGenesisRounds, where+" is not 1 to "+strconv.Itoa(MaxGenesisRounds))
}

// choiceID checks a choice id: 1 to [MaxGenesisChoiceBytes] lower-case
// letters, digits, "_" and "-", starting with a letter or a digit.
func (c *checker) choiceID(s, where string) {
	ok := len(s) >= 1 && len(s) <= MaxGenesisChoiceBytes
	for i := 0; ok && i < len(s); i++ {
		switch ch := s[i]; {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
		case i > 0 && (ch == '_' || ch == '-'):
		default:
			ok = false
		}
	}
	c.need(ok, where+" is not a choice id of 1 to "+strconv.Itoa(MaxGenesisChoiceBytes)+" lower-case letters, digits, _ and -")
}

// nodeID checks a node id: a lower-case letter followed by lower-case
// letters, digits, "_" and ".", at most [MaxGenesisNodeBytes] bytes.
func (c *checker) nodeID(s, where string) {
	ok := len(s) >= 1 && len(s) <= MaxGenesisNodeBytes && s[0] >= 'a' && s[0] <= 'z'
	for i := 0; ok && i < len(s); i++ {
		ch := s[i]
		ok = (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '.'
	}
	c.need(ok, where+" is not a node id")
}

// genesisTopFields are the domain-neutral field names of a brief.
var genesisTopFields = map[string]bool{
	"audience": true, "budget_band": true, "deliverables": true, "formats": true, "goal": true,
	"hosts": true, "kind": true, "quality_bar": true, "review_points": true, "schedule": true,
}

// genesisField checks a brief field name: one of the ten top-level names, or
// software.x, publishing.x or media.x, where x is a lower-case letter and
// lower-case letters, digits and "_"; at most [MaxGenesisFieldBytes] bytes.
func (c *checker) genesisField(s, where string) {
	ok := genesisTopFields[s]
	if !ok && len(s) <= MaxGenesisFieldBytes {
		domain, rest, dotted := strings.Cut(s, ".")
		ok = dotted && (domain == "software" || domain == "publishing" || domain == "media") &&
			rest != "" && rest[0] >= 'a' && rest[0] <= 'z'
		for i := 0; ok && i < len(rest); i++ {
			ch := rest[i]
			ok = (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_'
		}
	}
	c.need(ok, where+" is not a field of the brief")
}

// genesisTopic checks a topic: a lower-case letter followed by lower-case letters,
// digits and "_", at most [MaxGenesisTopicBytes] bytes.
func (c *checker) genesisTopic(s, where string) {
	ok := len(s) >= 1 && len(s) <= MaxGenesisTopicBytes && s[0] >= 'a' && s[0] <= 'z'
	for i := 0; ok && i < len(s); i++ {
		ch := s[i]
		ok = (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '_'
	}
	c.need(ok, where+" is not a topic")
}

// hostText checks a text of the catalog: 1 to max bytes, not blank. The codec
// holds that it is valid UTF-8 without a control character.
func (c *checker) hostText(s string, max int, where string) {
	c.need(len(s) >= 1 && len(s) <= max && strings.TrimSpace(s) != "", where+" is not 1 to "+strconv.Itoa(max)+" bytes of text")
}

// pick checks an answer's kind and the members the kind takes.
func (c *checker) pick(kind string, ids []string, text, where string) {
	switch kind {
	case GenesisKindChoice, GenesisKindChoices:
		max := 1
		if kind == GenesisKindChoices {
			max = MaxGenesisOptions
		}
		c.need(len(ids) >= 1 && len(ids) <= max, where+".ids is not 1 to "+strconv.Itoa(max)+" ids")
		seen := make(map[string]struct{}, len(ids))
		for i, id := range ids {
			at := where + ".ids[" + strconv.Itoa(i) + "]"
			c.choiceID(id, at)
			_, dup := seen[id]
			c.need(!dup, at+" repeats an earlier id")
			seen[id] = struct{}{}
		}
		c.need(text == "", where+".text is given for a "+kind)
	case GenesisKindOther:
		c.need(ids == nil, where+".ids is given for an other")
		c.hostText(text, MaxGenesisOther, where+".text")
	case GenesisKindYouDecide, GenesisKindSkip:
		c.need(ids == nil, where+".ids is given for a "+kind)
		c.need(text == "", where+".text is given for a "+kind)
	default:
		c.need(false, where+".kind is not choice, choices, other, you_decide or skip")
	}
}

func checkSeatGenesisParams(p *SeatGenesisParams) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	switch p.Verb {
	case GenesisVerbStart, GenesisVerbList, GenesisVerbRound, GenesisVerbAnswer, GenesisVerbChange, GenesisVerbSummary, GenesisVerbFinish, GenesisVerbDiscard:
	default:
		c.need(false, "genesis.verb is not start, list, round, answer, change, summary, finish or discard")
		return c.result()
	}
	if p.Verb == GenesisVerbStart || p.Verb == GenesisVerbList {
		c.need(p.Thread == "", "genesis.thread is given for a "+p.Verb)
	} else {
		c.threadID(p.Thread, "genesis.thread")
	}
	if p.Verb == GenesisVerbAnswer {
		c.genesisRound(p.Round, "genesis.round")
		c.need(len(p.Answers) >= 1 && len(p.Answers) <= MaxGenesisAnswers, "genesis.answers is not 1 to "+strconv.Itoa(MaxGenesisAnswers)+" answers")
		seen := make(map[string]struct{}, len(p.Answers))
		for i := range p.Answers {
			a, at := &p.Answers[i], "genesis.answers["+strconv.Itoa(i)+"]"
			c.nodeID(a.Node, at+".node")
			_, dup := seen[a.Node]
			c.need(!dup, at+" repeats the node of an earlier answer")
			seen[a.Node] = struct{}{}
			c.pick(a.Kind, a.IDs, a.Text, at)
		}
	} else {
		c.need(p.Round == 0, "genesis.round is given for a "+p.Verb)
		c.need(p.Answers == nil, "genesis.answers is given for a "+p.Verb)
	}
	if p.Verb == GenesisVerbChange {
		c.nodeID(p.Node, "genesis.node")
		c.need(p.Answer != nil, "genesis.answer is missing for a change")
		if p.Answer != nil {
			c.pick(p.Answer.Kind, p.Answer.IDs, p.Answer.Text, "genesis.answer")
		}
	} else {
		c.need(p.Node == "", "genesis.node is given for a "+p.Verb)
		c.need(p.Answer == nil, "genesis.answer is given for a "+p.Verb)
	}
	return c.result()
}

func (c *checker) genesisNode(n *GenesisNode, where string) {
	c.nodeID(n.ID, where+".id")
	c.genesisTopic(n.Topic, where+".topic")
	c.hostText(n.Prompt, MaxGenesisPrompt, where+".prompt")
	switch n.Type {
	case GenesisTypeSingle, GenesisTypeMulti:
		c.need(len(n.Options) >= 2 && len(n.Options) <= MaxGenesisOptions, where+".options is not 2 to "+strconv.Itoa(MaxGenesisOptions)+" options")
	case GenesisTypeYesNo:
		c.need(len(n.Options) == 2, where+".options of a yes-or-no question is not 2 options")
	case GenesisTypeScale:
		c.need(len(n.Options) == 0, where+".options of a scale is not empty")
	default:
		c.need(false, where+".type is not single, multi, yesno or scale")
	}
	seen := make(map[string]struct{}, len(n.Options))
	for i := range n.Options {
		o, at := &n.Options[i], where+".options["+strconv.Itoa(i)+"]"
		c.choiceID(o.ID, at+".id")
		_, dup := seen[o.ID]
		c.need(!dup, at+" repeats the id of an earlier option")
		seen[o.ID] = struct{}{}
		c.hostText(o.Label, MaxGenesisLabel, at+".label")
		c.hostText(o.Example, MaxGenesisExample, at+".example")
	}
}

// genesisRows checks one group of a summary, and adds its fields to seen.
func (c *checker) genesisRows(rows []GenesisRow, seen map[string]struct{}, where string) {
	for i := range rows {
		r, at := &rows[i], where+"["+strconv.Itoa(i)+"]"
		c.nodeID(r.Node, at+".node")
		c.genesisField(r.Field, at+".field")
		_, dup := seen[r.Field]
		c.need(!dup, at+".field is in a row already")
		seen[r.Field] = struct{}{}
		c.hostText(r.Label, MaxGenesisPrompt, at+".label")
		max := MaxGenesisValue
		if r.Untrusted {
			max = MaxGenesisOther
		}
		c.hostText(r.Value, max, at+".value")
	}
}

// genesisThreads checks the threads of a list: at most [MaxGenesisThreads],
// each open (a question or a summary), with its round and progress within
// their bounds, and none twice.
func (c *checker) genesisThreads(ts []GenesisThread) {
	c.need(ts != nil, "genesis.threads is missing for a threads")
	c.need(len(ts) <= MaxGenesisThreads, "genesis.threads is more than "+strconv.Itoa(MaxGenesisThreads)+" threads")
	seen := make(map[string]struct{}, len(ts))
	for i := range ts {
		t, at := &ts[i], "genesis.threads["+strconv.Itoa(i)+"]"
		c.threadID(t.Thread, at+".thread")
		_, dup := seen[t.Thread]
		c.need(!dup, at+" repeats an earlier thread")
		seen[t.Thread] = struct{}{}
		c.need(t.Of >= 1 && t.Of <= MaxGenesisRounds, at+".of is not 1 to "+strconv.Itoa(MaxGenesisRounds))
		switch t.State {
		case GenesisStateQuestion:
			c.need(t.Round >= 1 && t.Round <= t.Of, at+".round is not 1 to of")
		case GenesisStateSummary:
			c.need(t.Round >= 0 && t.Round <= t.Of, at+".round is not 0 to of")
		default:
			c.need(false, at+".state is not question or summary")
		}
		c.need(t.Filled >= 0 && t.Filled <= t.Total && t.Total <= MaxGenesisRows,
			at+" is not filled of total, with total at most "+strconv.Itoa(MaxGenesisRows))
	}
}

func checkSeatGenesisResult(r *SeatGenesisResult) error {
	c := &checker{invalid: record.CodeRecordInvalid}
	switch r.State {
	case GenesisStateQuestion, GenesisStateSummary, GenesisStateFinished, GenesisStateDiscarded:
	case GenesisStateThreads:
		// A list names no thread of its own and nothing of one.
		c.need(r.Thread == "", "genesis.thread is given for a threads")
		c.need(r.CatalogDigest == "", "genesis.catalog_digest is given for a threads")
		c.need(r.Round == 0 && r.Of == 0 && r.Reset == 0 && r.Progress == GenesisProgress{},
			"genesis.round, of, reset or progress is given for a threads")
		c.need(len(r.Nodes) == 0, "genesis.nodes is given for a threads")
		c.need(r.Groups == nil, "genesis.groups is given for a threads")
		c.need(r.Digest == "", "genesis.digest is given for a threads")
		c.genesisThreads(r.Threads)
		return c.result()
	default:
		c.need(false, "genesis.state is not question, summary, finished, discarded or threads")
		return c.result()
	}
	c.need(r.Threads == nil, "genesis.threads is given for a "+r.State)
	c.threadID(r.Thread, "genesis.thread")
	c.need(record.IsLowerHex(r.CatalogDigest, 64), "genesis.catalog_digest is not 64 lower-case hexadecimal digits")
	c.need(r.Of >= 1 && r.Of <= MaxGenesisRounds, "genesis.of is not 1 to "+strconv.Itoa(MaxGenesisRounds))
	c.need(r.Progress.Filled >= 0 && r.Progress.Filled <= r.Progress.Total && r.Progress.Total <= MaxGenesisRows,
		"genesis.progress is not filled of total, with total at most "+strconv.Itoa(MaxGenesisRows))
	c.need(r.Reset >= 0 && r.Reset <= MaxGenesisRows, "genesis.reset is not 0 to "+strconv.Itoa(MaxGenesisRows))

	// The round: 1 to of while asking, 0 to of after, 0 when discarded.
	switch r.State {
	case GenesisStateQuestion:
		c.need(r.Round >= 1 && r.Round <= r.Of, "genesis.round is not 1 to genesis.of")
	case GenesisStateDiscarded:
		c.need(r.Round == 0, "genesis.round is given for a discarded thread")
	default:
		c.need(r.Round >= 0 && r.Round <= r.Of, "genesis.round is not 0 to genesis.of")
	}

	// The nodes: only a question has them.
	if r.State == GenesisStateQuestion {
		c.need(len(r.Nodes) >= 1 && len(r.Nodes) <= MaxGenesisNodes, "genesis.nodes is not 1 to "+strconv.Itoa(MaxGenesisNodes)+" questions")
	} else {
		c.need(len(r.Nodes) == 0, "genesis.nodes is given for a "+r.State)
	}
	seen := make(map[string]struct{}, len(r.Nodes))
	for i := range r.Nodes {
		n, at := &r.Nodes[i], "genesis.nodes["+strconv.Itoa(i)+"]"
		c.genesisNode(n, at)
		_, dup := seen[n.ID]
		c.need(!dup, at+" repeats an earlier node")
		seen[n.ID] = struct{}{}
	}

	// The summary: for a summary and a finished thread.
	if r.State == GenesisStateSummary || r.State == GenesisStateFinished {
		c.need(r.Groups != nil, "genesis.groups is missing for a "+r.State)
		if r.Groups != nil {
			g := r.Groups
			c.need(len(g.YouChose)+len(g.DecidedForYou)+len(g.Defaults) <= MaxGenesisRows,
				"genesis.groups has more than "+strconv.Itoa(MaxGenesisRows)+" rows")
			fields := make(map[string]struct{})
			c.genesisRows(g.YouChose, fields, "genesis.groups.you_chose")
			c.genesisRows(g.DecidedForYou, fields, "genesis.groups.decided_for_you")
			c.genesisRows(g.Defaults, fields, "genesis.groups.defaults")
		}
	} else {
		c.need(r.Groups == nil, "genesis.groups is given for a "+r.State)
	}

	// The digest of the frozen brief, and nothing else frozen.
	if r.State == GenesisStateFinished {
		c.need(record.IsLowerHex(r.Digest, 64), "genesis.digest is not 64 lower-case hexadecimal digits")
	} else {
		c.need(r.Digest == "", "genesis.digest is given for a "+r.State)
	}
	if r.State == GenesisStateFinished || r.State == GenesisStateDiscarded {
		c.need(r.Reset == 0, "genesis.reset is given for a "+r.State)
	}
	if r.State == GenesisStateDiscarded {
		c.need(r.Progress == GenesisProgress{}, "genesis.progress is given for a discarded thread")
	}
	return c.result()
}
