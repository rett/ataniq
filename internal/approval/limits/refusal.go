package limits

// Kind says which rule refused. A caller that maps a refusal to a code of its
// own, or that tests for one rule, reads the kind and never the reason's text.
type Kind string

const (
	// UnderMin: the number asked for is under Min.
	UnderMin Kind = "under-min"
	// AboveMax: the number asked for is above Max.
	AboveMax Kind = "above-max"
	// BadLimit: the present limit handed in is itself outside Min to Max, so
	// no rule can be applied to it.
	BadLimit Kind = "bad-limit"
	// NotTyped: the number is higher than the present one and was not typed.
	NotTyped Kind = "not-typed"
	// CannotRaise: the present limit is so small that half of it, rounded
	// down, adds nothing. This is the limit of 1.
	CannotRaise Kind = "cannot-raise"
	// RaiseTooLarge: the number is more than half above the present one.
	RaiseTooLarge Kind = "raise-too-large"
	// AlreadyRaised: the limit was raised once already on this day.
	AlreadyRaised Kind = "already-raised"
)

// Refusal is the package's typed "no". Reason is one line, written by this
// package from numbers only: it never holds text that came from outside, so
// it can be shown as it is.
type Refusal struct {
	Kind   Kind
	Reason string
}

// Error returns the reason, so that a *Refusal is an error.
func (r *Refusal) Error() string { return r.Reason }
