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

// checkDigests returns the digests of the Check frames queued for c and
// empties the queue.
func (r *hubRig) checkDigests(c *client) []string {
	r.t.Helper()
	r.s.mu.Lock()
	defer r.s.mu.Unlock()
	var out []string
	for _, it := range c.q {
		if it.typ != wire.TypeCheck {
			continue
		}
		ck, err := wire.DecodeCheck(it.data)
		if err != nil {
			r.t.Fatal(err)
		}
		out = append(out, ck.Digest)
	}
	c.q, c.qBytes = nil, 0
	return out
}

// An idle screen is digested once for every client and every interval: the
// digest is kept by position, and a resize, which changes the screen without
// moving the position, clears it. The pane is changed behind the hub's back
// here, which a position that stands still never does, to show that the kept
// digest is the one that is sent.
func TestDigestIsKeptByPositionAndClearedByAResize(t *testing.T) {
	r := newHubRig(t, func(c *Config) { c.CheckInterval = time.Second })
	r.p.Write([]byte("hello"))
	digest := func() string { return wire.DigestHex(r.p.Digest()) }
	c := r.client()
	r.s.mu.Lock()
	c.wantSnap, c.streaming = false, true // streaming from a snapshot taken before
	r.s.mu.Unlock()

	r.point(100)
	first := digest()
	if got := r.checkDigests(c); len(got) != 1 || got[0] != first {
		t.Fatalf("the first check %v, want %s", got, first)
	}

	// The same position, the screen changed behind the hub: the kept digest.
	r.p.Write([]byte(" and more"))
	changed := digest()
	if changed == first {
		t.Fatal("the pane did not change")
	}
	r.clock.advance(2 * time.Second)
	r.point(100)
	if got := r.checkDigests(c); len(got) != 1 || got[0] != first {
		t.Errorf("the same position was digested again: %v, want the kept %s", got, first)
	}

	// Another position: digested again.
	r.clock.advance(2 * time.Second)
	r.point(101)
	if got := r.checkDigests(c); len(got) != 1 || got[0] != changed {
		t.Errorf("a new position: %v, want %s", got, changed)
	}

	// A resize at the same position clears the kept digest.
	r.p.Resize(60, 20)
	resized := digest()
	if resized == changed {
		t.Fatal("the resize did not change the digest")
	}
	r.clock.advance(2 * time.Second)
	r.point(101)
	if got := r.checkDigests(c); len(got) != 1 || got[0] != resized {
		t.Errorf("after a resize: %v, want %s", got, resized)
	}

	// Two clients at one point share one digest.
	c2 := r.client()
	r.s.mu.Lock()
	c2.wantSnap, c2.streaming = false, true
	r.s.mu.Unlock()
	r.clock.advance(2 * time.Second)
	r.point(101)
	if a, b := r.checkDigests(c), r.checkDigests(c2); len(a) != 1 || len(b) != 1 || a[0] != resized || b[0] != resized {
		t.Errorf("two clients: %v %v", a, b)
	}
}
