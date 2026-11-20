package card

import (
	"unicode"
	"unicode/utf8"

	"ataniq/internal/approval/record"
)

// MaxVerdictText is the most characters of a verdict run's id, provider or
// model that a surface draws.
const MaxVerdictText = 40

// CheckKind names one check a verdict run made. The list is closed: a
// surface draws only these names, never a name that came with the data.
type CheckKind int

// The checks of a verdict run.
const (
	// CheckPlan is whether the plan text matches the effects.
	CheckPlan CheckKind = iota + 1
	// CheckScope is whether the change stays within what the request
	// said it would change.
	CheckScope
	// CheckFlagged is whether the flagged paths are explained.
	CheckFlagged
	// CheckSuite is whether the suite record is clean.
	CheckSuite
	numChecks = iota
)

// NumChecks is the number of checks in the closed list.
const NumChecks = numChecks

// String returns the check's name in the host's words, or "unknown" for a
// value outside the closed list.
func (k CheckKind) String() string {
	switch k {
	case CheckPlan:
		return "plan"
	case CheckScope:
		return "scope"
	case CheckFlagged:
		return "flagged paths"
	case CheckSuite:
		return "suite"
	}
	return "unknown"
}

// Valid reports whether k is in the closed list.
func (k CheckKind) Valid() bool { return k >= CheckPlan && k <= CheckSuite }

// CheckResult is the answer of one check.
type CheckResult struct {
	Kind CheckKind
	Pass bool
}

// Run is what one verdict run said. ID, Provider and Model are plain strings
// from data; a surface draws them through [VerdictText].
type Run struct {
	ID       string
	Provider string
	Model    string
	// Approve is true when the run approved and false when it rejected.
	Approve bool
	// Checks are the run's checks, from the closed list.
	Checks []CheckResult
}

// VerdictBlock is what the two verdict runs said about a card. It is advice
// of this program and is in no record. It is shown only after the owner
// decided: Card.Verdicts, Item.Verdicts and Inputs.Verdicts point at it, and
// [New] copies it to a card only when the card is decided. Nothing in it
// asks to be believed, and nothing in it is under the binding's digest.
type VerdictBlock struct {
	// Runs are the two runs, in order.
	Runs [2]Run
	// Refused says that the card exists because a run refused. A surface
	// may say so in one line even before the decision.
	Refused bool
}

// VerdictText cleans a string from data for drawing and bounds it to
// [MaxVerdictText] characters, the ellipsis included: a longer text is cut
// to MaxVerdictText-1 characters and an ellipsis. It keeps printable
// characters only: control characters, format characters (the direction
// marks included), combining marks and unassigned code points are dropped.
// White space, a tab and a line break among it, becomes a space, and a run
// of spaces becomes one.
func VerdictText(s string) string {
	var out []rune
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if r == utf8.RuneError || !unicode.IsPrint(r) || unicode.IsMark(r) {
			continue
		}
		if space && len(out) > 0 {
			out = append(out, ' ')
		}
		space = false
		out = append(out, r)
	}
	if len(out) <= MaxVerdictText {
		return string(out)
	}
	out = out[:MaxVerdictText-1]
	for len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return string(out) + "…"
}

// OwnerApproved reports whether the owner approved the request that is in
// state s, and whether the state says that the owner decided at all. A
// request approved under a rule or a grant, one that is pending, and one
// that ended without a decision are not the owner's decisions. The states
// after a use (consumed, applied, not applied, unknown) do not say how the
// request was approved; they are taken as the owner's, since only a card
// carries verdicts.
func OwnerApproved(s record.State) (approved, decided bool) {
	switch s {
	case record.StateApproved, record.StateConsumed, record.StateApplied,
		record.StateNotApplied, record.StateUnknown:
		return true, true
	case record.StateDenied:
		return false, true
	}
	return false, false
}

// Agreed reports whether both runs said what the owner decided, and whether
// the row counts: it counts only when it holds verdicts and the owner
// decided it.
func (it Item) Agreed() (agreed, counted bool) {
	if it.Verdicts == nil {
		return false, false
	}
	approved, decided := OwnerApproved(it.State)
	if !decided {
		return false, false
	}
	for _, r := range it.Verdicts.Runs {
		if r.Approve != approved {
			return false, true
		}
	}
	return true, true
}
