//go:build linux

package serve

import (
	"net"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
)

// smallQueue is the bound of the queue in the tests that fill it with the
// emulator's own output, which is slow under the race detector.
const smallQueue = 128 << 10

// Cases A4 and 2.5: a client that stops reading never holds the PTY loop. It is
// resynced once (one fresh snapshot), then disconnected as too slow when it
// still does not read; the service never loops through snapshots.
func TestSlowClientResyncedOnceThenTooSlow(t *testing.T) {
	e := newEnv(t, "raw; say READY; flood 0 0 1", func(o *envOpts) {
		o.cfg = func(c *Config) { c.FlushWait = 20 * time.Second; c.QueueBytes = smallQueue }
	})
	waitPosition(t, e.sess, 1000)
	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)
	v.pause()

	// While the client does not read, the output loop goes on: the position
	// advances between samples taken a moment apart.
	last := position(e.sess)
	for i := 0; i < 5; i++ {
		time.Sleep(40 * time.Millisecond)
		now := position(e.sess)
		if now <= last {
			t.Fatalf("the session's position stopped at %d while a client does not read", now)
		}
		last = now
	}
	e.waitCount(cntResyncs, 1)
	e.waitCount(cntTooSlow, 1)
	// The output loop goes on after the disconnect too. The counts above are
	// usually reached at once, so no time has passed since the last sample:
	// read the position now and wait for it to advance, never compare at once.
	waitPosition(t, e.sess, position(e.sess)+1)
	if e.count(cntSnapshotsTaken) != 2 || e.count(cntResyncs) != 1 || e.count(cntTooSlow) != 1 {
		t.Errorf("snapshots %d, resyncs %d, too slow %d: want 2, 1, 1", e.count(cntSnapshotsTaken), e.count(cntResyncs), e.count(cntTooSlow))
	}
	e.waitEvent("the resync", named("resync", "overflow"))
	e.waitEvent("the detach", named("detach", "too-slow"))

	// The client that wakes up finds the end. What was queued when it was
	// disconnected (the resync notice and the second snapshot) was dropped with
	// the queue; the notice that says why is written last.
	v.resume()
	v.waitNotice(wire.ReasonTooSlow)
	v.waitEnd()
	v.noProblems()
	v.mu.Lock()
	if v.snapshots != 1 {
		t.Errorf("the client got %d snapshots", v.snapshots)
	}
	v.mu.Unlock()
	// The place is free again.
	v2 := e.attach(wire.RoleViewer)
	v2.waitSnapshots(1)
}

// Case A13, second half: a client that reads too slowly while the worst-case
// screen is sent is disconnected as too slow, with one snapshot and no loop.
func TestSlowReaderOfTheWorstCaseScreen(t *testing.T) {
	files := worstCaseFiles(t, t.TempDir())
	e := newEnv(t, worstScript(files, 3_000_000), func(o *envOpts) {
		o.session = func(c *session.Config) { c.Cols, c.Rows = 500, 200 }
		o.cfg = func(c *Config) { c.FlushWait = 20 * time.Second; c.QueueBytes = worstQueue }
	})
	waitPosition(t, e.sess, filesSize(t, files))                                     // the screen is filled; the flood has begun
	v := e.attach(wire.RoleViewer, func(h *helloOpts) { h.holdAfterWelcome = true }) // it takes the Welcome and nothing else

	e.waitCount(cntTooSlow, 1)
	// The output loop goes on after the disconnect: read the position now and
	// wait for it to advance, never compare at once.
	waitPosition(t, e.sess, position(e.sess)+1)
	if e.count(cntSnapshotsTaken) != 1 || e.count(cntResyncs) != 0 {
		t.Errorf("snapshots %d, resyncs %d: want 1 and 0: the snapshot is outside the budget and nothing loops", e.count(cntSnapshotsTaken), e.count(cntResyncs))
	}
	v.resume()
	v.waitNotice(wire.ReasonTooSlow)
	v.waitEnd()
	v.noProblems()
	v.mu.Lock()
	if v.snapshots != 1 {
		t.Errorf("the client got %d snapshots, want the one that was being sent", v.snapshots)
	}
	v.mu.Unlock()
	if e.count(cntSnapshotsTaken) != 1 {
		t.Errorf("snapshots sent %d", e.count(cntSnapshotsTaken))
	}
}

// The overflow rules, driven by hand with a clock: the first overflow is a
// resync, a second one within the window (or while the snapshot is unsent)
// disconnects, and after the window a client may be resynced again.
func TestOverflowRules(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	e, _ := newBase(t, "raw; wait", func(o *envOpts) { o.cfg = func(c *Config) { c.Now = clock.Now } })
	s := &Server{cfg: e.cfg, sess: e.sess, clients: map[*client]struct{}{}, evq: make(chan evItem, 512)}
	s.cfg.withDefaults()
	s.cfg.QueueBytes = 1000
	s.cfg.OverflowWindow = 10 * time.Second
	newClient := func() *client {
		a := connPair(t)
		c := &client{s: s, conn: a, wake: make(chan struct{}, 1), streaming: true, role: wire.RoleViewer, name: "c"}
		s.clients[c] = struct{}{}
		return c
	}
	fill := func(c *client) {
		s.mu.Lock()
		defer s.mu.Unlock()
		for i := 0; i < 20 && c.streaming && !c.removed; i++ {
			s.pushLocked(c, item{typ: wire.TypeNotice, data: make([]byte, 300)}, false)
		}
	}
	// The snapshot of a resync was sent: back to streaming.
	snapSent := func(c *client) {
		s.mu.Lock()
		c.wantSnap, c.streaming, c.snapUnsent = false, true, false
		s.mu.Unlock()
	}

	c := newClient()
	fill(c)
	if !c.wantSnap || c.removed || s.sess.Counters()[cntResyncs] != 1 || len(c.q) != 1 {
		t.Fatalf("the first overflow: wantSnap %v removed %v queue %d", c.wantSnap, c.removed, len(c.q))
	}
	snapSent(c)
	clock.advance(9 * time.Second)
	fill(c)
	if !c.removed || c.reason != "too-slow" || s.sess.Counters()[cntTooSlow] != 1 {
		t.Fatalf("a second overflow within the window: removed %v reason %q", c.removed, c.reason)
	}

	c = newClient()
	fill(c)
	snapSent(c)
	clock.advance(11 * time.Second)
	fill(c)
	if c.removed || !c.wantSnap || s.sess.Counters()[cntResyncs] != 3 {
		t.Fatalf("an overflow after the window: removed %v wantSnap %v resyncs %d", c.removed, c.wantSnap, s.sess.Counters()[cntResyncs])
	}

	// A snapshot that is still unsent when the queue overflows again.
	c = newClient()
	fill(c)
	s.mu.Lock()
	c.wantSnap, c.streaming, c.snapUnsent = false, true, true
	s.mu.Unlock()
	clock.advance(11 * time.Second)
	fill(c)
	if !c.removed || c.reason != "too-slow" {
		t.Fatalf("an overflow while the snapshot is unsent: removed %v", c.removed)
	}
}

// A client whose socket takes no bytes for the stall time is disconnected.
func TestStalledClient(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo 1; flood 0 2000000 3; wait", func(o *envOpts) {
		o.cfg = func(c *Config) {
			c.Stall = 300 * time.Millisecond
			c.QueueBytes = 8 << 20
			c.FlushWait = 20 * time.Second
		}
	})
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	w.pause()
	w.input("x")
	e.waitCount(cntStalled, 1)
	e.waitEvent("the stall", named("detach", "stalled"))
	if e.count(cntResyncs) != 0 || e.count(cntTooSlow) != 0 {
		t.Errorf("resyncs %d, too slow %d", e.count(cntResyncs), e.count(cntTooSlow))
	}
	w.resume()
	w.waitEnd()
	// Nothing is typed for a writer that is gone, and the place is free.
	w2 := e.attach(wire.RoleWriter)
	w2.waitSnapshots(1)
}

// connPair is one end of a connected pair of unix sockets; the other end is
// closed with the test.
func connPair(t *testing.T) *net.UnixConn {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ends [2]*net.UnixConn
	for i, fd := range fds {
		f := os.NewFile(uintptr(fd), "pair")
		c, err := net.FileConn(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		ends[i] = c.(*net.UnixConn)
		t.Cleanup(func() { ends[i].Close() })
	}
	return ends[0]
}
