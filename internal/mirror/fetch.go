package mirror

import (
	"errors"
	"fmt"
)

var (
	// ErrBusy is returned by a fetch when another fetch into the same mirror
	// holds the lock.
	ErrBusy = errors.New("mirror: another fetch into this mirror is running")
	// ErrHostileTree is returned by a fetch when the owner-side check of the
	// lead's clone refuses it: a folder on the way that is not a real folder,
	// a ".git" that is not a real folder, or a file that sends git
	// elsewhere. The error text says which check failed and holds no name
	// from the clone.
	ErrHostileTree = errors.New("mirror: the lead's clone is refused")
	// ErrUpstreamChanged is returned by a fetch when, after the move into
	// the mirror, a ref outside the lead's namespace is not what it was. It
	// means a bug or an attack. The error text names the refs.
	ErrUpstreamChanged = errors.New("mirror: a ref outside the lead's namespace changed")
	// ErrRefused is returned by a fetch when what arrived in the staging
	// repository is refused: a lead ref that fails the checks of this
	// package or is not a commit, or a staging repository that is not what
	// this package made. The mirror is unchanged. The error text holds
	// counts and fixed words, and no ref name.
	ErrRefused = errors.New("mirror: the fetched content is refused")
	// ErrFetchTimeout is returned by a fetch that ran past its time limit.
	ErrFetchTimeout = errors.New("mirror: the fetch ran past its time limit")
)

// LimitError is returned when a limit of [Limits] is passed. What is
// "source", "staged", "mirror", "refs" or "entries". Got is what was
// measured, or the count at which the walk or the listing stopped, and Max is
// the limit.
type LimitError struct {
	What     string
	Got, Max int64
}

// Error describes the limit.
func (e *LimitError) Error() string {
	return fmt.Sprintf("mirror: the limit for %s is passed: %d against %d", e.What, e.Got, e.Max)
}
