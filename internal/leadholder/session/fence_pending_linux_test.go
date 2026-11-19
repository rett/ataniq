//go:build linux

package session

import (
	"testing"
	"time"
)

// Pending asks the kernel as well as the reader's count: with the reader's
// count hidden, bytes queued on the master that the reader has not taken make it
// true on their own. (The reader waits for room, so it neither reads nor counts
// while the count is hidden.)
func TestViewPendingAsksTheKernelForBytesQueuedOnTheMaster(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; say READY; echo 1; flood 0 400000 2; wait", func(c *Config) { c.heldLimit = 4 << 10 })
	waitScreen(t, s, "READY")
	fenced(t, s, func(f Fenced) {
		writeFrozen(t, f, "g")
		until(t, "bytes to queue on the master and the reader to wait for room", func() bool {
			n, err := s.host.Queued()
			if err != nil || n == 0 {
				return false
			}
			s.hold.mu.Lock()
			defer s.hold.mu.Unlock()
			return len(s.hold.buf) >= s.hold.limit
		})
		// The reader is stopped on the full buffer: its count cannot move.
		time.Sleep(50 * time.Millisecond)
		frozen(t, f, func(v FenceView, _ func([]byte) error) {
			saved := s.unapplied.Swap(0)
			defer s.unapplied.Store(saved)
			if !v.Pending() {
				t.Error("Pending is false with bytes queued on the master and the reader's count at zero")
			}
		})
		// Let the flood in so that the session ends cleanly.
		until(t, "everything to be applied", func() bool {
			settle(t, f, 0, 100*time.Millisecond, 10*time.Second)
			return s.Counters()["output_bytes"] >= uint64(len("READY\r\n")+7+400000)
		})
	})
}
