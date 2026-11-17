//go:build linux

package serve

import (
	"testing"
	"time"

	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole/sessionpane"
)

// hubRig is a Server that is not serving, with a pane of its own and a clock
// the test moves: the snapshot points are driven by hand with pointLocked, so
// that the rules of the hub are tested without a PTY and without waiting.
type hubRig struct {
	t     *testing.T
	s     *Server
	p     *sessionpane.Pane
	clock *fakeClock
}

func newHubRig(t *testing.T, mods ...func(*Config)) *hubRig {
	t.Helper()
	clock := &fakeClock{t: time.Now()}
	e, _ := newBase(t, "raw; wait", func(o *envOpts) { o.cfg = func(c *Config) { c.Now = clock.Now } })
	cfg := e.cfg
	for _, m := range mods {
		m(&cfg)
	}
	p := sessionpane.New(80, 24, sessionpane.Options{})
	t.Cleanup(p.Close)
	s := &Server{
		cfg: cfg, sess: e.sess, pane: p, clients: map[*client]struct{}{},
		evq: make(chan evItem, 512), kick: make(chan struct{}, 1),
	}
	s.cfg.withDefaults()
	return &hubRig{t: t, s: s, p: p, clock: clock}
}

// client adds a viewer that streams from a snapshot: it waits for its first
// snapshot point from now on.
func (r *hubRig) client() *client {
	r.t.Helper()
	c := &client{s: r.s, conn: connPair(r.t), wake: make(chan struct{}, 1), role: wire.RoleViewer, name: "c"}
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.clients[c] = struct{}{}
	r.s.startWaitLocked(c, r.clock.Now())
	return c
}

// point is a snapshot point at position end at the clock's time.
func (r *hubRig) point(end uint64) {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	r.s.pointLocked(r.p, end, r.clock.Now())
}

func (r *hubRig) streaming(c *client) bool {
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	return c.streaming
}

// The 2026 hold counts from when the pane first became quiescent in the wait,
// and a later point that is not quiescent does not restart it: under output
// that keeps the mode set and alternates between ground and the middle of an
// escape sequence, the snapshot is taken at the first quiescent point after
// the hold ran out.
func TestSnapshotHoldIsNotRestartedByANonQuiescentPoint(t *testing.T) {
	const hold = 150 * time.Millisecond
	r := newHubRig(t, func(c *Config) { c.SnapshotHold = hold; c.SnapshotNotice = time.Hour })
	r.p.Write([]byte("\x1b[?2026hframe"))
	c := r.client()

	const step = 50 * time.Millisecond
	var taken time.Duration = -1
	for i := 0; i < 80; i++ {
		inString := i%2 == 1
		if inString {
			r.p.Write([]byte("\x1b]0;half")) // the parser is inside an OSC string
		} else {
			r.p.Write([]byte("\x07")) // the string ends: ground again
		}
		if got := r.p.Quiescent(); got == inString {
			t.Fatalf("point %d: the pane's quiescent state is %v", i, got)
		}
		r.point(uint64(100 + i))
		if r.streaming(c) {
			taken = time.Duration(i) * step
			break
		}
		r.clock.advance(step)
	}
	if taken < 0 {
		t.Fatalf("no snapshot in 4 s with the hold at %v", hold)
	}
	// The points at 0, 100, 200 ms ... are at ground and the ones between are
	// in the string. The hold runs from 0, so the point at 100 ms is too early
	// and the one at 200 ms takes the snapshot.
	if taken != 200*time.Millisecond {
		t.Errorf("the snapshot was taken at %v, want 200ms", taken)
	}
	if n := r.s.sess.Counters()[cntSnapshotsTaken]; n != 1 {
		t.Errorf("snapshots taken %d", n)
	}
}
