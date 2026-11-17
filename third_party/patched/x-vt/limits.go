package vt

// Limits on what the emulator keeps of the bytes it is given. The bytes come
// from a program the owner does not trust, so every one of these is a cap
// that the emulator holds itself; a cap that is hit is counted in [Counters]
// and never silent. They are reviewed changes to the upstream module (see
// NOTICE.md).
const (
	// MaxClusterBytes is the most bytes of one grapheme cluster that a cell
	// keeps. A cell keeps a prefix of the cluster, cut at a rune boundary: the
	// first rune that does not fit and every later rune of the cluster are
	// dropped.
	MaxClusterBytes = 64

	// MaxLinkURL and MaxLinkParams are the most bytes of the URL and of the
	// parameters of a hyperlink (OSC 8). A link over either is dropped, and
	// the pen gets no link, as if the link had been closed.
	MaxLinkURL    = 2048
	MaxLinkParams = 256

	// MaxLiveLinks is the most distinct links (a URL with its parameters)
	// that the cells of one screen, the main and the alternate screen each,
	// may reference. Text printed under a further link is drawn without it.
	MaxLiveLinks = 256

	// MaxStringBytes is the most bytes of the data of one OSC, DCS, SOS, PM or
	// APC string that the emulator keeps. The rest of a longer string is
	// dropped, and the handler of the string sees the data that fitted.
	MaxStringBytes = 64 * 1024
)

// Counters counts the times a cap was hit. The values only grow, and a reader
// compares two readings to see what happened between them.
type Counters struct {
	// ClusterBytesDropped is the number of bytes of grapheme clusters that
	// were dropped because the cluster was already [MaxClusterBytes] long.
	ClusterBytesDropped uint64
	// ModesIgnored is the number of times a mode that is not in the table of
	// recorded modes was set or reset. Nothing was recorded for it and no
	// callback was called.
	ModesIgnored uint64
	// LinksOverLength is the number of hyperlinks (OSC 8) dropped because
	// the URL was over [MaxLinkURL] bytes or the parameters over
	// [MaxLinkParams].
	LinksOverLength uint64
	// LinksOverLimit is the number of cells drawn without their link because
	// the link would have been the [MaxLiveLinks]+1st distinct live link of
	// the screen.
	LinksOverLimit uint64
	// StringsTruncated is the number of OSC, DCS, SOS, PM and APC strings
	// that were longer than [MaxStringBytes] and lost their end, counted when
	// the first byte was dropped.
	StringsTruncated uint64
	// RepeatsCut is the number of REP sequences (CSI n b) whose count was
	// over two lines and two characters (twice the width of the screen, and
	// two), and so was not printed one character at a time: it was done a line
	// at a time where that gives the same state, and else cut to that count.
	RepeatsCut uint64
}

// Counters returns the counters of the caps that were hit.
func (e *Emulator) Counters() Counters {
	c := e.counters
	c.StringsTruncated = e.parser.Truncated()
	return c
}
