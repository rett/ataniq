package catalog

import (
	"sort"
	"time"

	"ataniq/internal/genesis/brief"
)

// The walker's bounds.
const (
	// MaxRounds is the most rounds an interview has.
	MaxRounds = 6
	// MaxPerRound is the most questions one round asks.
	MaxPerRound = 3
)

// Limits are the caller's bounds on a walk. A zero field takes the
// walker's bound, and a field over it is held to it.
type Limits struct {
	// MaxRounds is the most rounds, at most [MaxRounds].
	MaxRounds int
	// MaxPerRound is the most questions a round asks, at most
	// [MaxPerRound].
	MaxPerRound int
	// Enough is set when the owner said enough: the walk is done.
	Enough bool
}

func (l Limits) norm() Limits {
	if l.MaxRounds <= 0 || l.MaxRounds > MaxRounds {
		l.MaxRounds = MaxRounds
	}
	if l.MaxPerRound <= 0 || l.MaxPerRound > MaxPerRound {
		l.MaxPerRound = MaxPerRound
	}
	return l
}

// Round is the next round of questions.
type Round struct {
	// Number is the round's number, from 1; answers to it carry it. A
	// round asked again after the last one (see [Next]) has the last
	// round's number.
	Number int
	// Nodes are the 1 to 3 questions of the round, highest weight first,
	// of one topic or of several.
	Nodes []Node
	// Unanswered is how many required fields are not set yet, before
	// this round.
	Unanswered int
	// Again is set on a round asked after the last round, because an
	// answer was changed: it has the last round's number.
	Again bool
}

// Answer is the owner's answer to one node. Exactly one of Choice,
// Choices, Other, YouDecide and Skip is given.
type Answer struct {
	// Round is the number of the round the answer was given in: the
	// brief's last round, or the next one.
	Round int
	// Choice is an option id for a single choice, yes or no for a
	// yes-or-no question, and 1 to 5 for a scale.
	Choice string
	// Choices are option ids for a multiple choice.
	Choices []string
	// Other is free text given instead of an option.
	Other brief.Untrusted
	// YouDecide leaves the choice to the host.
	YouDecide bool
	// Skip leaves the question to its default.
	Skip bool
}

// Result says what an answer did.
type Result struct {
	// Changed is set when the answer changed an earlier answer to another
	// value.
	Changed bool
	// Reset is how many later answers the change cleared.
	Reset int
	// Provenance is the provenance the field was set with.
	Provenance brief.Provenance
}

func refuse(c brief.Code, detail string) error { return &brief.Error{Code: c, Detail: detail} }

// checkCatalog refuses a catalog that was not loaded, and a brief started
// under another catalog version.
func checkCatalog(c Catalog, b brief.Brief) error {
	if c.digest == "" || c.index == nil {
		return refuse(brief.CodeCatalogInvalid, "the catalog was not loaded")
	}
	if b.Catalog != c.digest {
		return refuse(brief.CodeCatalogMismatch, "the brief was started under another catalog version")
	}
	return nil
}

// Required returns the required fields of the brief's kind; while the kind
// is not chosen, only the kind itself.
func Required(c Catalog, b brief.Brief) []string {
	if b.Kind == "" {
		return []string{brief.FieldKind}
	}
	return append([]string(nil), c.Required[b.Kind]...)
}

// Readiness counts the filled required fields of the brief's kind.
func Readiness(c Catalog, b brief.Brief) (filled, total int) {
	return b.Readiness(Required(c, b))
}

// Next returns the next round for the brief, or done. The walk is done
// when the caller says enough, after the last round, when every required
// field is filled, or when no question is left that fills a required
// field. After the last round, a brief in which an earlier answer was
// changed (its history holds a value that left its field) is not done
// while a question is left that fills a required field: a change can clear
// the answers that depended on it, or move the brief to another kind, and
// those questions are asked again in a round that keeps the last round's
// number (Again), as often as a change leaves some, so the interview never
// has more than [MaxRounds] round numbers. Otherwise the round holds the eligible questions (a question's
// condition holds on the brief as it is, and its field is unset and
// required for the brief's kind) of the highest weight, up to the most a
// round asks, whatever their topics: a round is never short of questions
// while eligible ones are left, so the rounds hold every required question
// of a kind. A question whose condition names the answer to another
// question of the round is not eligible until that answer is given, so it
// waits for a later round. Next is pure: it reads the catalog and the brief
// and changes neither.
func Next(c Catalog, b brief.Brief, limits Limits) (Round, bool, error) {
	if err := checkCatalog(c, b); err != nil {
		return Round{}, false, err
	}
	l := limits.norm()
	required := Required(c, b)
	filled, total := b.Readiness(required)
	again := b.Rounds >= l.MaxRounds
	if l.Enough || filled == total || (again && !changed(b)) {
		return Round{}, true, nil
	}
	cands := candidates(c, b, required)
	if len(cands) == 0 {
		return Round{}, true, nil
	}
	r := Round{Number: b.Rounds + 1, Again: again}
	if again {
		r.Number = b.Rounds
	}
	for _, i := range cands {
		if len(r.Nodes) == l.MaxPerRound {
			break
		}
		r.Nodes = append(r.Nodes, c.Nodes[i])
	}
	for _, f := range required {
		if _, ok := b.Get(f); !ok {
			r.Unanswered++
		}
	}
	return r, false, nil
}

// changed reports whether an earlier answer of the brief was changed: its
// history holds a value that left its field, or held one it dropped.
func changed(b brief.Brief) bool {
	return len(b.History) > 0 || b.HistoryDropped > 0
}

// candidates returns the indexes of the eligible nodes, highest weight
// first, one per field.
func candidates(c Catalog, b brief.Brief, required []string) []int {
	var out []int
	for i, n := range c.Nodes {
		if _, set := b.Get(n.Field); set || !contains(required, n.Field) {
			continue
		}
		if n.When != nil && !n.When.Eval(b) {
			continue
		}
		out = append(out, i)
	}
	sort.SliceStable(out, func(i, j int) bool { return c.Nodes[out[i]].Weight > c.Nodes[out[j]].Weight })
	seen := map[string]bool{}
	kept := out[:0]
	for _, i := range out {
		if !seen[c.Nodes[i].Field] {
			seen[c.Nodes[i].Field] = true
			kept = append(kept, i)
		}
	}
	return kept
}

// Apply checks an answer against its node and returns the brief with the
// answer in it, and what the answer did. The brief given is not changed,
// so a caller can call Apply to learn, before the owner confirms a change,
// how many later answers it resets.
//
// An option, options, yes or no, or a step sets the field as owner-chosen.
// Other text sets it as owner-chosen free text, when the node allows it.
// You-decide sets the node's default with the node's you-decide
// provenance, when the node allows it. Skip sets the default as a
// default, when the node has one. An answer that changes an earlier answer
// to another value first clears the closure of the node's field and keeps
// the cleared values in the history.
func Apply(c Catalog, b brief.Brief, nodeID string, a Answer, at time.Time) (brief.Brief, Result, error) {
	if err := checkCatalog(c, b); err != nil {
		return b, Result{}, err
	}
	i, ok := c.index[nodeID]
	if !ok {
		return b, Result{}, refuse(brief.CodeNodeUnknown, "the catalog holds no node with the id")
	}
	n := c.Nodes[i]
	if n.When != nil && !n.When.Eval(b) {
		return b, Result{}, refuse(brief.CodeNodeNotEligible, "the node's condition does not hold for the brief")
	}
	if a.Round < 1 || a.Round > MaxRounds || (a.Round != b.Rounds && a.Round != b.Rounds+1) {
		return b, Result{}, refuse(brief.CodeRoundInvalid, "the round is not the brief's last or next one, or is past the last round")
	}
	v, p, err := n.value(a)
	if err != nil {
		return b, Result{}, err
	}
	out := b.Clone()
	var res Result
	res.Provenance = p
	if old, set := out.Get(n.Field); set {
		if old.Value.Equal(v) && old.Provenance == p {
			return out, res, nil
		}
		if !old.Value.Equal(v) {
			res.Changed = true
			if res.Reset, err = out.ClearClosure(c.closure[n.Field], n.Field, at); err != nil {
				return b, Result{}, err
			}
		}
	}
	if err := out.Set(n.Field, v, p, n.ID, a.Round, at); err != nil {
		return b, Result{}, err
	}
	return out, res, nil
}

// value turns an answer into the field's value and provenance.
func (n Node) value(a Answer) (brief.Value, brief.Provenance, error) {
	given := 0
	for _, g := range []bool{a.Choice != "", a.Choices != nil, a.Other != "", a.YouDecide, a.Skip} {
		if g {
			given++
		}
	}
	if given != 1 {
		return brief.Value{}, "", refuse(brief.CodeAnswerInvalid, "an answer gives exactly one of a choice, choices, other text, you decide and skip")
	}
	switch {
	case a.Choice != "":
		if n.Type == Multi || !n.canChoose(a.Choice) {
			return brief.Value{}, "", refuse(brief.CodeAnswerInvalid, "the choice is not one of the question")
		}
		return brief.One(a.Choice), brief.OwnerChosen, nil
	case a.Choices != nil:
		if n.Type != Multi || len(a.Choices) == 0 || len(a.Choices) > len(n.Options) {
			return brief.Value{}, "", refuse(brief.CodeAnswerInvalid, "the choices are not one or more options of a multiple choice")
		}
		for j, id := range a.Choices {
			if !n.hasOption(id) || contains(a.Choices[:j], id) {
				return brief.Value{}, "", refuse(brief.CodeAnswerInvalid, "a choice is not an option of the question, or is given twice")
			}
		}
		return brief.Many(a.Choices...), brief.OwnerChosen, nil
	case a.Other != "":
		if !n.AllowOther {
			return brief.Value{}, "", refuse(brief.CodeAnswerInvalid, "the question does not take other text")
		}
		return brief.OtherText(a.Other), brief.OwnerChosen, nil
	case a.YouDecide:
		if !n.AllowYouDecide {
			return brief.Value{}, "", refuse(brief.CodeAnswerInvalid, "the question does not take you decide")
		}
		return n.defaultValue(), n.YouDecide, nil
	default:
		if n.Default == "" {
			return brief.Value{}, "", refuse(brief.CodeAnswerInvalid, "the question cannot be skipped")
		}
		return n.defaultValue(), brief.Default, nil
	}
}

func (n Node) defaultValue() brief.Value {
	if n.Type == Multi {
		return brief.Many(n.Default)
	}
	return brief.One(n.Default)
}
