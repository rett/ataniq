//go:build linux

package session

import (
	"bytes"
	"math/rand"
	"sync"
	"testing"
	"time"
)

// The tests here set the fence's buffer by hand (holdBytes) and watch the
// order and the pace in which it is applied. The fake agent only waits, so the
// buffer holds exactly what a test put there, and a subscriber that sleeps for
// each slice makes the emulator slower than any agent, the case in which the
// end of a fence cannot apply everything in its budget.

// slowStream is a subscriber that sleeps for each slice it is given and keeps
// the bytes, in the order it got them.
type slowStream struct {
	delay time.Duration
	mu    sync.Mutex
	got   []byte
}

func (c *slowStream) fn(_ uint64, chunk []byte) {
	time.Sleep(c.delay)
	c.mu.Lock()
	c.got = append(c.got, chunk...)
	c.mu.Unlock()
}

func (c *slowStream) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

func (c *slowStream) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.got...)
}

// testStream is slices*OutputSlice letters in an order that a swap of two
// slices, or a slice lost or applied twice, cannot keep.
func testStream(seed int64, slices int) []byte {
	r := rand.New(rand.NewSource(seed))
	b := make([]byte, slices*OutputSlice)
	for i := range b {
		b[i] = byte('a' + r.Intn(26))
	}
	return b
}

// holdBytes puts b in the fence's buffer as the reader does when it reads it
// while a fence holds the lock.
func holdBytes(s *Session, b []byte) {
	h := &s.hold
	s.unapplied.Add(int64(len(b)))
	h.mu.Lock()
	h.buf = append(h.buf, b...)
	h.mu.Unlock()
	poke(h.arrive)
}

func drainsStarted(s *Session) int {
	s.hold.mu.Lock()
	defer s.hold.mu.Unlock()
	return s.hold.drains
}

func drainAlive(s *Session) bool {
	s.hold.mu.Lock()
	defer s.hold.mu.Unlock()
	return s.hold.draining
}

func subscribeSlow(t *testing.T, s *Session, delay time.Duration) *slowStream {
	t.Helper()
	c := &slowStream{delay: delay}
	unsub, err := s.Subscribe(c.fn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub)
	return c
}

// With a backlog far larger than the budget the end of a fence returns within
// the budget and one slice (not after the whole backlog), and the rest is
// applied in order by the goroutine it leaves.
func TestEndOfFenceReturnsWithinItsBudgetWhileALargeBacklogIsHeld(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; wait")
	c := subscribeSlow(t, s, 10*time.Millisecond)
	stream := testStream(1, 400) // 4 s of applying at 10 ms a slice
	began := time.Now()
	fenced(t, s, func(Fenced) { holdBytes(s, stream) })
	took := time.Since(began)
	if limit := endFenceBudget + 10*time.Millisecond + 500*time.Millisecond; took > limit {
		t.Errorf("the fence took %v with a backlog, want its budget of %v and one slice", took, endFenceBudget)
	}
	if took < endFenceBudget {
		t.Errorf("the fence took %v, less than its budget %v: the backlog was not held", took, endFenceBudget)
	}
	if n := c.len(); n >= len(stream) {
		t.Fatalf("all %d bytes were applied when the fence returned: the test holds no backlog", n)
	}
	if !drainAlive(s) {
		t.Error("no goroutine is draining the rest")
	}
	until(t, "the rest to be applied", func() bool { return c.len() >= len(stream) })
	if !bytes.Equal(c.bytes(), stream) {
		t.Error("the bytes were applied out of order, or not all of them")
	}
	until(t, "the drain to end", func() bool { return !drainAlive(s) })
	if got := position(s); got != uint64(len(stream)) {
		t.Errorf("position = %d, want %d", got, len(stream))
	}
}

// A second fence whose end finds the first one's drain still running starts no
// second goroutine: two would apply the backlog in turns, and the reader,
// freed by the first to finish, would apply new bytes between the slices of the
// other. The stream the subscriber gets is the bytes in the order they were
// read, across both fences.
func TestTwoFencesInARowLeaveOneDrainAndKeepTheOrder(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; wait")
	c := subscribeSlow(t, s, 5*time.Millisecond)
	first, second := testStream(2, 300), testStream(3, 60)
	fenced(t, s, func(Fenced) { holdBytes(s, first) })
	if !drainAlive(s) {
		t.Fatal("the first fence left no drain: the test holds too small a backlog")
	}
	fenced(t, s, func(Fenced) { holdBytes(s, second) })
	if !drainAlive(s) {
		t.Fatal("the drain was over before the second fence ended: the test holds too small a backlog")
	}
	if n := drainsStarted(s); n != 1 {
		t.Errorf("%d drain goroutines were started by two fences, want 1", n)
	}
	// New output while the drain runs is held behind it, not applied between
	// its slices.
	third := testStream(4, 5)
	holdBytes(s, third)
	want := append(append(append([]byte(nil), first...), second...), third...)
	until(t, "everything to be applied", func() bool { return c.len() >= len(want) })
	until(t, "the drain to end", func() bool { return !drainAlive(s) })
	if !bytes.Equal(c.bytes(), want) {
		t.Error("the bytes were applied out of order, or not all of them")
	}
	if n := drainsStarted(s); n != 1 {
		t.Errorf("%d drain goroutines in all, want 1", n)
	}
}

// When Close returns, the goroutine a fence left is over and everything the
// fence held has been applied. The lifetime path applies the backlog itself
// when the agent is stopped, so this shows the outcome of a Close, not which
// wait gave it: that Close waits for the goroutine is shown by
// TestCloseBlocksUntilTheDrainIsLetGo.
func TestCloseWaitsForTheDrain(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; wait")
	c := subscribeSlow(t, s, 10*time.Millisecond)
	stream := testStream(5, 100)
	fenced(t, s, func(Fenced) { holdBytes(s, stream) })
	if !drainAlive(s) {
		t.Fatal("the fence left no drain: the test holds too small a backlog")
	}
	done := s.hold.drainDone
	if err := s.Close(); err != nil {
		t.Logf("Close: %v", err)
	}
	select {
	case <-done:
	default:
		t.Fatal("Close returned with the drain goroutine alive")
	}
	if !bytes.Equal(c.bytes(), stream) {
		t.Errorf("the subscriber got %d of %d bytes, or in another order", c.len(), len(stream))
	}
}

// Applying output only notes the time and arms the timer: the digest of the
// screen is not computed on the goroutine that applies it, however long ago the
// last comparison was.
func TestApplyingOutputDoesNotCompareTheScreen(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; wait")
	long := time.Now().Add(-time.Hour)
	// A timer the start's output armed may have fired and wait for the
	// tracker's lock: let it run first, or it compares after the slice below.
	until(t, "the start's comparison to be done", func() bool {
		s.scr.mu.Lock()
		defer s.scr.mu.Unlock()
		return s.scr.timer == nil && !s.scr.dirty
	})
	// A parked timer: noteApplied arms none of its own, which with the last
	// comparison an hour old would fire at once and compare before the
	// check below.
	parked := time.AfterFunc(time.Hour, func() {})
	defer parked.Stop()
	s.scr.mu.Lock()
	s.scr.sigAt, s.scr.dirty, s.scr.timer = long, false, parked
	s.scr.mu.Unlock()
	s.deliverSlice([]byte("x"))
	s.scr.mu.Lock()
	compared, dirty := !s.scr.sigAt.Equal(long), s.scr.dirty
	s.scr.timer = nil
	s.scr.mu.Unlock()
	if compared {
		t.Error("applying a slice compared the digest on the caller's goroutine")
	}
	if !dirty {
		t.Errorf("after a slice dirty = %v, want true", dirty)
	}
	// With no timer armed a slice arms one. Its delay is zero, the last
	// comparison being an hour old, so it may have compared already: then
	// dirty is false again.
	s.deliverSlice([]byte("y"))
	s.scr.mu.Lock()
	armed := s.scr.timer != nil || !s.scr.dirty
	s.scr.mu.Unlock()
	if !armed {
		t.Error("a slice with no timer armed armed none")
	}
	// The timer compares, off the applier's goroutine, and the change is found.
	until(t, "the timer to compare", func() bool {
		s.scr.mu.Lock()
		defer s.scr.mu.Unlock()
		return !s.scr.dirty
	})
	if q := s.screenQuiet(); q > 5*time.Second {
		t.Errorf("ScreenQuiet = %v after a change just made", q)
	}
}
