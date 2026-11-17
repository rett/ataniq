//go:build linux && race

package serve

// Under the race detector the emulator is about ten times slower: the worst-case
// screen is a part of the full one, with a smaller output budget so that its
// snapshot is still the larger of the two.
const (
	worstRows  = 30
	worstQueue = 64 << 10
)
