package intake

import (
	"fmt"
	"slices"
	"strings"

	"ataniq/internal/exchange/leadtext"
)

// SignOff is what the owner accepted: the change as a whole, and separately
// each flagged path. Both are bound to one tip and to the digest of the review
// the owner saw, so a sign-off of one review is never taken for another.
type SignOff struct {
	// Tip is the full commit id the owner reviewed.
	Tip string
	// Digest is [Review.Digest] of the review the owner saw.
	Digest string
	// Change is true when the owner accepts the change as a whole.
	Change bool
	// Paths holds each flagged path the owner accepts, as raw bytes as in
	// [Flag.Path]. One entry accepts every flag of that path, so a path that is
	// flagged for two kinds needs one entry.
	Paths []string
}

// SignOffError says everything that is wrong with a sign-off, all at once. It
// is the error of [Review.CheckSignOff]; get it with errors.As.
type SignOffError struct {
	// TipMismatch is true when the sign-off names another tip than the review.
	TipMismatch bool
	// DigestMismatch is true when the sign-off names the digest of another
	// review.
	DigestMismatch bool
	// ChangeMissing is true when the owner did not accept the change as a
	// whole.
	ChangeMissing bool
	// Missing holds the flags whose path the owner did not accept, in the
	// order of [Review.Flagged].
	Missing []Flag
	// Unknown holds the accepted paths that are not flagged, sorted and
	// without repeats: a list from an older or another review.
	Unknown []string
}

// maxListed is how many paths an error message names. The fields hold all.
const maxListed = 8

// Error describes the problems. Paths in it are lead text and are escaped
// with leadtext.Escape.
func (e *SignOffError) Error() string {
	var parts []string
	if e.TipMismatch {
		parts = append(parts, "the sign-off is for another tip")
	}
	if e.DigestMismatch {
		parts = append(parts, "the sign-off is for another review (digest differs)")
	}
	if e.ChangeMissing {
		parts = append(parts, "the change as a whole is not accepted")
	}
	if len(e.Missing) > 0 {
		var names []string
		for _, f := range e.Missing {
			names = append(names, fmt.Sprintf("%s (%s)", leadtext.Escape(f.Path), f.Kind))
		}
		parts = append(parts, fmt.Sprintf("%d flagged path(s) not accepted: %s", len(e.Missing), listed(names)))
	}
	if len(e.Unknown) > 0 {
		var names []string
		for _, p := range e.Unknown {
			names = append(names, leadtext.Escape(p))
		}
		parts = append(parts, fmt.Sprintf("%d accepted path(s) are not flagged: %s", len(e.Unknown), listed(names)))
	}
	return "intake: sign-off refused: " + strings.Join(parts, "; ")
}

// listed joins at most maxListed names and counts the rest.
func listed(names []string) string {
	if len(names) <= maxListed {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(names[:maxListed], ", "), len(names)-maxListed)
}

// CheckSignOff checks s against the review. It returns nil when the sign-off
// names this review's tip and digest, accepts the change as a whole, accepts
// every flagged path, and accepts no path that is not flagged. Otherwise it
// returns a *[SignOffError] that lists everything that is wrong. A path that
// is flagged for two kinds is accepted by one entry in Paths. An accepted path
// that is not flagged is an error too, so that a stale list is noticed. The
// check is exact: no path is normalized, matched by a pattern or folded.
func (r *Review) CheckSignOff(s SignOff) error {
	e := &SignOffError{
		TipMismatch:    s.Tip != r.Tip,
		DigestMismatch: s.Digest != r.Digest(),
		ChangeMissing:  !s.Change,
	}
	accepted := make(map[string]bool, len(s.Paths))
	for _, p := range s.Paths {
		accepted[p] = true
	}
	flagged := make(map[string]bool, len(r.Flagged))
	for _, f := range r.Flagged {
		flagged[f.Path] = true
		if !accepted[f.Path] {
			e.Missing = append(e.Missing, f)
		}
	}
	for p := range accepted {
		if !flagged[p] {
			e.Unknown = append(e.Unknown, p)
		}
	}
	slices.Sort(e.Unknown)
	if e.TipMismatch || e.DigestMismatch || e.ChangeMissing || len(e.Missing) > 0 || len(e.Unknown) > 0 {
		return e
	}
	return nil
}
