package vt

// Limits on what the emulator keeps of the bytes it is given. The bytes come
// from a program the owner does not trust, so every one of these is a cap
// that the emulator holds itself; a cap that is hit is counted in [Counters]
// and never silent. They are reviewed changes to the upstream module (see
// NOTICE.md).
const (
	// MaxClusterBytes is the most bytes of one grapheme cluster that a cell
	// keeps. The rest of a longer cluster, cut at a rune boundary, is dropped.
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
}

// Counters returns the counters of the caps that were hit.
func (e *Emulator) Counters() Counters {
	return e.counters
}
