//go:build linux

package session

import (
	"context"
	"encoding/hex"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The neutral OSC command the tests give a hook, and the strings built from
// it: a query the fake agent prints, the hook's reply, and the reply the
// emulator makes to a request for the device attributes (DA1), which the
// agent prints after the query.
const (
	testOSC     = 9911
	testQuery   = "\x1b]9911;?\x07"
	testAck     = "\x1b]9911;ack\x07"
	testDA1     = "\x1b[c"
	testDA1Resp = "\x1b[?62;1;6;22c"
)

func hx(s string) string { return hex.EncodeToString([]byte(s)) }

// ackHook is an OSC hook that answers testOSC with testAck and counts its
// calls.
type ackHook struct{ calls atomic.Int64 }

func (h *ackHook) hook() OSCHook {
	return OSCHook{Cmd: testOSC, Fn: func([]byte) ([]byte, bool) {
		h.calls.Add(1)
		return []byte(testAck), true
	}}
}

func withHook(h *ackHook) func(*Config) {
	return func(c *Config) { c.OSC = []OSCHook{h.hook()} }
}

// bigScreen makes the screen the largest the holder allows, so that a long
// report of the fake agent's echo step fits on it.
func bigScreen(c *Config) { c.Cols, c.Rows = MaxCols, MaxRows }

// fenceCtx is a context that ends well after any test should.
func fenceCtx(t testing.TB) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*patience)
	t.Cleanup(cancel)
	return ctx
}

// fenced runs fn in a fence and fails the test if the fence or fn fails.
func fenced(t testing.TB, s *Session, fn func(f Fenced)) {
	t.Helper()
	err := s.WithFence(fenceCtx(t), func(f Fenced) error {
		fn(f)
		return nil
	})
	if err != nil {
		t.Fatalf("WithFence: %v", err)
	}
}

// settle is a Settle that must succeed and drain.
func settle(t testing.TB, f Fenced, min, quiet, max time.Duration) {
	t.Helper()
	drained, err := f.Settle(fenceCtx(t), min, quiet, max)
	if err != nil || !drained {
		t.Fatalf("Settle = %v, %v, want drained", drained, err)
	}
}

// frozen runs fn in a Frozen call with a far deadline; the write function is
// handed on.
func frozen(t testing.TB, f Fenced, fn func(v FenceView, write func([]byte) error)) {
	t.Helper()
	err := f.Frozen(time.Now().Add(patience), func(v FenceView, write func([]byte) error) error {
		fn(v, write)
		return nil
	})
	if err != nil {
		t.Fatalf("Frozen: %v", err)
	}
}

// viewText is the screen text as a Frozen function sees it.
func viewText(t testing.TB, f Fenced) string {
	t.Helper()
	var text string
	frozen(t, f, func(v FenceView, _ func([]byte) error) { text = v.Snapshot().Text })
	return text
}

// writeFrozen writes b inside a Frozen call.
func writeFrozen(t testing.TB, f Fenced, b string) {
	t.Helper()
	frozen(t, f, func(_ FenceView, write func([]byte) error) {
		if err := write([]byte(b)); err != nil {
			t.Fatalf("write %q: %v", b, err)
		}
	})
}

// rxFromText decodes the bytes the fake agent's echo step reported in a text
// of the screen, rows joined by line feeds, a long report wrapped over rows
// included.
func rxFromText(text string) []byte {
	return rxBytes(strings.ReplaceAll(text, "\n", ""))
}

// until polls f until it is true.
func until(t testing.TB, what string, f func() bool) {
	t.Helper()
	poll(t, func() string { return what }, f)
}
