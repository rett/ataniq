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
)

// Counters counts the times a cap was hit. The values only grow, and a reader
// compares two readings to see what happened between them.
type Counters struct {
	// ClusterBytesDropped is the number of bytes of grapheme clusters that
	// were dropped because the cluster was already [MaxClusterBytes] long.
	ClusterBytesDropped uint64
}

// Counters returns the counters of the caps that were hit.
func (e *Emulator) Counters() Counters {
	return e.counters
}
