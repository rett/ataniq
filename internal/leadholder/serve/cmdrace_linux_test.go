//go:build linux

package serve

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ataniq/internal/leadholder/wire"
)

// A client's command line is known before the client is claimed: the
// timekeeper reads the writer's command under the server lock, for the Role
// frame of every snapshot, and a command that the handshake wrote after the
// claim and outside the lock is a data race (and, without the race detector,
// a Role frame with no command).
//
// A viewer is attached, and the timekeeper runs with a short poll interval;
// then writers attach and detach many times. A sampler plays the timekeeper's
// part at a far higher rate: it takes the server lock again and again and
// looks at the writer, if there is one. Whenever there is a writer, its
// command must be there.
func TestWriterCommandIsKnownWhenTheWriterIsClaimed(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo; wait", func(o *envOpts) {
		o.cfg = func(c *Config) {
			c.PollInterval = time.Millisecond
			c.CheckInterval = 5 * time.Millisecond
		}
	})
	waitScreen(t, e.sess, "READY")
	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)

	var (
		stop      = make(chan struct{})
		sampled   atomic.Int64
		unknown   atomic.Int64
		samplerWG sync.WaitGroup
	)
	samplerWG.Add(1)
	go func() {
		defer samplerWG.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			e.srv.mu.Lock()
			if w := e.srv.writer; w != nil {
				sampled.Add(1)
				if w.cmd == "" {
					unknown.Add(1)
				}
			}
			e.srv.mu.Unlock()
			runtime.Gosched()
		}
	}()
	var stopOnce sync.Once
	endSampler := func() {
		stopOnce.Do(func() { close(stop) })
		samplerWG.Wait()
	}
	defer endSampler()

	rounds := 60
	if testing.Short() {
		rounds = 20
	}
	for i := 0; i < rounds; i++ {
		// The size changes every round: the writer has something to apply.
		cols := 80 + i%2*20
		w := e.attachWithNoWait(e.newTicket(wire.RoleWriter), func(h *helloOpts) { h.cols = cols })
		w.waitSnapshots(1)
		w.detach()
		w.waitEnd()
	}
	endSampler()
	if sampled.Load() == 0 {
		t.Fatal("the sampler never saw a writer")
	}
	if n := unknown.Load(); n != 0 {
		t.Errorf("the writer had no command line in %d of %d samples while it was claimed", n, sampled.Load())
	}
}
