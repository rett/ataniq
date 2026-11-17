//go:build linux && !race

package serve

// The worst-case screen fills all 200 rows of 500 columns, and the output budget
// is the real one.
const (
	worstRows  = 200
	worstQueue = QueueBytes
)
