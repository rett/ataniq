//go:build linux

package session

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

// Close waits for the goroutine the end of a fence left. The goroutine is held
// by a test hook after it applied the backlog and before it lets the reader
// apply again: nothing is left to apply, the lifetime path ends, and only the
// wait for the goroutine itself keeps Close from returning. Close returns when
// the goroutine is let go.
func TestCloseBlocksUntilTheDrainIsLetGo(t *testing.T) {
	e := newEnv(t)
	reached, gate := make(chan struct{}), make(chan struct{})
	var reachOnce, gateOnce sync.Once
	letGo := func() { gateOnce.Do(func() { close(gate) }) }
	defer letGo()
	s := e.start("raw; wait", func(c *Config) {
		c.beforeDrainRelease = func() {
			reachOnce.Do(func() { close(reached) })
			<-gate
		}
	})
	c := subscribeSlow(t, s, 10*time.Millisecond)
	stream := testStream(7, 60) // 600 ms of applying: more than the budget
	fenced(t, s, func(Fenced) { holdBytes(s, stream) })
	if !drainAlive(s) {
		t.Fatal("the fence left no drain: the test holds too small a backlog")
	}
	drained := s.hold.drainDone
	select {
	case <-reached:
	case <-time.After(patience):
		t.Fatal("the drain did not reach its release")
	}
	if !bytes.Equal(c.bytes(), stream) {
		t.Errorf("the drain is at its release with %d of %d bytes applied, or in another order", c.len(), len(stream))
	}

	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	// Close is past the lifetime path: what is left of it is short, but for
	// the wait for the drain.
	until(t, "Close to pass the lifetime path", s.closed.Load)
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) while the drain goroutine is held before its release", err)
	case <-time.After(300 * time.Millisecond):
	}
	select {
	case <-drained:
		t.Fatal("the drain goroutine ended while the hook held it")
	default:
	}

	letGo()
	select {
	case err := <-closed:
		if err != nil {
			t.Logf("Close: %v", err)
		}
	case <-time.After(patience):
		t.Fatal("Close did not return after the drain was let go")
	}
	select {
	case <-drained:
	default:
		t.Error("Close returned with the drain goroutine alive")
	}
}
