//go:build linux

package session

import (
	"bytes"
	"sync"
	"testing"
	"time"
)

// The lifetime path's applying of held output waits for a slice that another
// applier took from the buffer and has not applied: the buffer is empty then,
// and a path that looked only at the buffer would end the pane before that
// slice reached it, and the last screen would lack the agent's last output.
// The other applier here waits for the output lock, as a drain does while a
// Frozen function runs.
func TestTheLifetimePathWaitsForASliceAnotherApplierHolds(t *testing.T) {
	e := newEnv(t)
	s := e.start("raw; wait")
	var c capture
	unsub, err := s.Subscribe(c.fn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unsub)
	slice := testStream(6, 1)

	// The output lock, held as a Frozen function holds it.
	s.omu.Lock()
	var once sync.Once
	unlock := func() { once.Do(s.omu.Unlock) }
	defer unlock()

	holdBytes(s, slice)
	other := make(chan struct{})
	go func() {
		defer close(other)
		s.applyHeld(nil)
	}()
	until(t, "the other applier to take the slice", s.heldEmpty)

	var atReturn []byte
	last := make(chan struct{})
	go func() {
		defer close(last)
		s.applyLast()
		atReturn = c.all()
	}()
	select {
	case <-last:
		t.Error("the lifetime path's applying returned while another applier holds a slice it has not applied")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	for _, ch := range []chan struct{}{last, other} {
		select {
		case <-ch:
		case <-time.After(patience):
			t.Fatal("an applier did not return after the output lock was released")
		}
	}
	if !bytes.Equal(atReturn, slice) {
		t.Errorf("%d of %d held bytes were applied when the lifetime path's applying returned", len(atReturn), len(slice))
	}
}
