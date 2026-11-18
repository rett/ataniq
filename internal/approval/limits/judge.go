package limits

import "fmt"

// Outcome is what the limit says about one decision.
type Outcome int

const (
	// NotCounted: the decision is a deny. A deny is never held and is never
	// a use, whatever the day holds: the limit bounds forged approvals, and
	// holding a deny would protect nothing.
	NotCounted Outcome = iota
	// Use: the decision is an approve and the day has room. It goes on and
	// counts as one card use of that day, whatever its later outcome.
	Use
	// Held: the decision is an approve past the day's limit. It is recorded
	// and not applied, it is not a use, and its card comes back as pending.
	Held
)

// String names the outcome.
func (o Outcome) String() string {
	switch o {
	case NotCounted:
		return "not counted"
	case Use:
		return "use"
	case Held:
		return "held"
	}
	return fmt.Sprintf("outcome %d", int(o))
}

// Reached reports whether uses reached limit: at it, or above it, which
// happens when the limit was lowered during the day.
func Reached(uses, limit int) bool { return uses >= limit }

// Judge says what one decision is, given the project's card uses so far on
// the day of the decision and its limit. approve is false for a deny.
//
// Each decision is judged by itself. usesToday counts uses only: a held
// approve is not a use, so held decisions never take room, and a decision is
// never queued behind one. After the limit is raised, or on the next day, the
// next approve is a use again, and a held card that is decided again is
// judged like any other.
func Judge(approve bool, usesToday, limit int) Outcome {
	if !approve {
		return NotCounted
	}
	if Reached(usesToday, limit) {
		return Held
	}
	return Use
}

// HeldReason is the one-line reason for a held approve, for the record and
// for the screen.
func HeldReason(usesToday, limit int) string {
	return fmt.Sprintf("decided past the limit: %d card uses today, and the limit is %d", usesToday, limit)
}
