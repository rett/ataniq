package intake

import (
	"errors"
	"fmt"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/leadtext"
)

// PendingPrefix is the namespace of the pins of host-built merges. It is the
// prefix that package mergebuild gives its pins; this package does not import
// that one, and a test holds the two equal.
const PendingPrefix = "refs/ataniq/pending/"

// Errors of merge mode. Test them with errors.Is.
var (
	// ErrNotMerge means that the tip of a merge-mode review is not a commit
	// of exactly two parents.
	ErrNotMerge = errors.New("intake: the tip is not a merge of two parents")
	// ErrAttributes means that the repository's "info/attributes" is not
	// absent or empty, or could not be looked at, so no diff of it is read.
	// It wraps the error of [gitenv.CheckInfoAttributes].
	ErrAttributes = errors.New("intake: the repository's attribute file is refused")
)

// MergeBaseError is returned by a merge-mode [Inspect] when the first parent
// of the merge is not the target's tip: the target moved after the merge was
// built, or the pin is another's. In [Integrate] it also matches [ErrMoved].
type MergeBaseError struct {
	// Base is the target's tip now; Parent is the merge's first parent.
	Base, Parent string
}

// Error describes the problem. It holds object ids only.
func (e *MergeBaseError) Error() string {
	return fmt.Sprintf("intake: the merge's first parent %s is not the target's tip %s", e.Parent, e.Base)
}

// checkAttributes runs the check of gitenv on the repository, so that no
// pinned diff of this package is read from a repository whose own attribute
// file could change what git prints. See [gitenv.CheckInfoAttributes].
func checkAttributes(src Source) error {
	if err := gitenv.CheckInfoAttributes(src.GitDir); err != nil {
		return fmt.Errorf("%w: %w", ErrAttributes, err)
	}
	return nil
}

// checkMergeShape checks that the tip of the review is a commit of two
// parents, the first of which is the base. The tip is the first commit of
// base..tip that git lists, and the review's commits hold it, since the base
// is an ancestor of the tip and they are different.
func checkMergeShape(rev *Review) error {
	for _, c := range rev.Commits {
		if c.ID != rev.Tip {
			continue
		}
		if len(c.Parents) != 2 {
			return fmt.Errorf("%w: %s has %d parent(s)", ErrNotMerge, leadtext.Escape(rev.TipRef), len(c.Parents))
		}
		if c.Parents[0] != rev.Base {
			return &MergeBaseError{Base: rev.Base, Parent: c.Parents[0]}
		}
		return nil
	}
	return errors.New("intake: the tip is not among the commits of the review")
}

// addOddPathFlags adds the odd-path flag to every change whose path is odd.
func addOddPathFlags(changes []Change) {
	for i := range changes {
		if why := OddPath(changes[i].Path); why != "" {
			changes[i].Flags = addFlag(changes[i].Flags, Flag{Kind: FlagOddPath, Path: changes[i].Path,
				Reason: "the path is odd (" + why + "); the matcher does not fold it"})
		}
	}
}
