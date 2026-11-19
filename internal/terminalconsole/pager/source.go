package pager

import "errors"

// Source is where the pager reads the patch from, one page at a time. Its
// signature uses built-in types only, so that the package that knows the
// backend can adapt to it without the pager knowing that package.
//
// Page may be called again before an earlier call has returned, and from
// another goroutine: a Source must be safe for concurrent use. The pager
// calls it from commands, which Bubble Tea runs each on a goroutine of its
// own, and when the reader goes to the top while a page is being read the
// pager gives that request up and makes another without waiting for it.
type Source interface {
	// Page returns page n of pages (pages >= 1, n < pages). cont[i] is true
	// when lines[i] continues in the next piece: a line longer than a page
	// is carried in pieces, and the pager joins them before it wraps. A
	// piece that continues is not empty, and a line has at most
	// MaxLinePieces pieces.
	Page(n int) (lines []string, cont []bool, pages int, err error)
}

// ErrReviewChanged is the error a Source returns, alone or wrapped, when the
// review that the patch belongs to changed while it was read. The pager then
// says so in its own words. The text of an error is never drawn.
var ErrReviewChanged = errors.New("pager: the review changed")

// MaxLineBytes is the longest line, in bytes as the lead wrote it and after
// its pieces are joined, that the pager shows. A longer line ends the
// reading; it is not cut. The limit bounds the bytes one line can make the
// pager hold, since a line must be held whole to be wrapped.
const MaxLineBytes = 16 << 20

// MaxLinePieces is the most pieces one line may arrive in. A line in more
// pieces ends the reading, as a line that is too long does. Together with
// the rule that a piece that continues is not empty, the limit bounds the
// pieces one line can make the pager hold and walk through, however small
// the source makes them: a line of MaxLineBytes needs pieces of 4 KiB only.
const MaxLinePieces = 4096
