package approvals

import (
	"errors"
	"fmt"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/record"
	"ataniq/internal/terminalconsole/pager"
)

// errPageChanged is what the adapter returns for a page of another review
// than the card's. It wraps pager.ErrReviewChanged.
var errPageChanged = fmt.Errorf("approvals: the page is of another review: %w", pager.ErrReviewChanged)

// pageSource adapts Backend.Page, for the patch of one card, to the pager's
// Source. It holds no state, so it is safe for concurrent use as far as the
// backend is.
type pageSource struct {
	b  Backend
	id string
	// digest is the card's review digest. A page that names another one is
	// of another review.
	digest string
}

// Page returns page n of the patch as the backend gives it. A backend error
// with the code review-changed becomes an error that wraps
// pager.ErrReviewChanged; every other error is passed on, and the pager says
// only that a page could not be read.
func (s pageSource) Page(n int) (lines []string, cont []bool, pages int, err error) {
	p, err := s.b.Page(s.id, card.PagePatch, n)
	if err != nil {
		if code, ok := record.CodeOf(err); ok && code == record.CodeReviewChanged {
			return nil, nil, 0, errPageChanged
		}
		return nil, nil, 0, errors.New("approvals: the backend could not give the page")
	}
	// A page is taken in only when it is a page of the patch, the one asked
	// for and of the review the card was made from; anything else is a
	// review that changed.
	if p.Kind != card.PagePatch || p.N != n || p.ReviewDigest == "" || p.ReviewDigest != s.digest {
		return nil, nil, 0, errPageChanged
	}
	return p.Patch, p.Cont, p.Pages, nil
}
