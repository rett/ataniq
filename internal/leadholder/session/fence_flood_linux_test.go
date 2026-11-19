//go:build linux

package session

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"ataniq/internal/leadholder/fakeagent"
)

// checkedStream is a subscriber that sleeps for each slice and compares it
// with the stream it expects, at the offset it has reached: it keeps how many
// bytes it got and where the first byte that differs was, if any.
type checkedStream struct {
	delay time.Duration
	want  []byte
	mu    sync.Mutex
	n     int
	bad   int // the offset of the first difference, or -1
}

func (c *checkedStream) fn(_ uint64, chunk []byte) {
	time.Sleep(c.delay)
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, b := range chunk {
		if c.bad < 0 && (c.n+i >= len(c.want) || c.want[c.n+i] != b) {
			c.bad = c.n + i
		}
	}
	c.n += len(chunk)
}

// state is the number of bytes got and the offset of the first difference
// (-1: none so far).
func (c *checkedStream) state() (n, bad int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n, c.bad
}

// Two fences in a row while the agent itself writes a known stream, faster
// than a slow subscriber lets it be applied: the reader reads all the time,
// the buffer is small and stays full, the first fence leaves a drain and the
// second ends while that drain runs. The drain then finds new bytes in the
// buffer each time it has applied what it took, and it must go on holding the
// reader: a reader let go with bytes still held would apply what it reads next
// before them. The subscriber's bytes are the agent's, in order.
func TestTwoFencesInARowWithAFloodFromTheAgentKeepTheOrder(t *testing.T) {
	const total = 3 << 19 // 1.5 MiB: 384 slices, far more than two budgets apply
	e := newEnv(t)
	s := e.start("raw; say READY; echo 1; flood 0 "+strconv.Itoa(total)+" 13; say DONE; wait", func(c *Config) { c.heldLimit = 64 << 10 })
	waitScreen(t, s, "READY")
	want := append([]byte("RX:67\r\n"), fakeagent.FloodBytes(13, total)...)
	want = append(want, "DONE\r\n"...)
	c := &checkedStream{delay: 4 * time.Millisecond, want: want, bad: -1}
	unsub, err := s.Subscribe(c.fn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub)

	fenced(t, s, func(f Fenced) {
		writeFrozen(t, f, "g") // the flood begins
		until(t, "the buffer to fill", func() bool { return s.Counters()["held_output_full"] > 0 })
	})
	if !drainAlive(s) {
		t.Error("the first fence left no drain: the test holds too small a backlog")
	}
	fenced(t, s, func(f Fenced) {
		// The fence's own applying and the drain's take turns.
		f.Settle(fenceCtx(t), 0, 5*time.Millisecond, 30*time.Millisecond)
		frozen(t, f, func(v FenceView, _ func([]byte) error) { _ = v.Snapshot() })
	})
	if !drainAlive(s) {
		t.Error("the drain was over when the second fence ended: the test holds too small a backlog")
	}
	if n := drainsStarted(s); n != 1 {
		t.Errorf("%d drain goroutines were started by two fences, want 1", n)
	}

	until(t, "the agent's stream to be applied", func() bool {
		n, bad := c.state()
		return bad >= 0 || n >= len(want)
	})
	if n, bad := c.state(); bad >= 0 || n != len(want) {
		t.Fatalf("the subscriber got %d bytes of %d, the first difference at %d: the output is out of order or not whole", n, len(want), bad)
	}
	until(t, "the drain to end", func() bool { return !drainAlive(s) })
	if n := drainsStarted(s); n != 1 {
		t.Errorf("%d drain goroutines in all, want 1", n)
	}
	if n, bad := c.state(); bad >= 0 || n != len(want) {
		t.Errorf("after the drain the subscriber has %d bytes of %d, the first difference at %d", n, len(want), bad)
	}
	if n := s.Counters()["output_bytes"]; n != uint64(len("READY\r\n")+len(want)) {
		t.Errorf("output_bytes %d, want %d", n, len("READY\r\n")+len(want))
	}
}
