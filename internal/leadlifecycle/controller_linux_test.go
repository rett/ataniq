package leadlifecycle

import (
	"context"
	"testing"
)

// pollUntil decides on ok and not on the reason: a check that fails without
// giving a reason is a failure, and a check that passes is a pass.
func TestPollUntilReportsOkExplicitly(t *testing.T) {
	r := newRig(t)
	run := func(check func(context.Context) (bool, string)) (ok bool, why string, err error) {
		r.drive(func() { ok, why, err = r.ctl.pollUntil(context.Background(), check) })
		return
	}
	t.Run("never ok and no reason", func(t *testing.T) {
		calls := 0
		ok, why, err := run(func(context.Context) (bool, string) { calls++; return false, "" })
		noErr(t, err)
		if ok || why != "" {
			t.Fatalf("ok %v why %q: an empty reason must not read as success", ok, why)
		}
		if calls < 2 {
			t.Fatalf("%d calls, want polling until the bound", calls)
		}
	})
	t.Run("never ok keeps the last reason", func(t *testing.T) {
		ok, why, err := run(func(context.Context) (bool, string) { return false, "not yet" })
		noErr(t, err)
		if ok || why != "not yet" {
			t.Fatalf("ok %v why %q", ok, why)
		}
	})
	t.Run("ok at once", func(t *testing.T) {
		ok, why, err := run(func(context.Context) (bool, string) { return true, "ignored" })
		noErr(t, err)
		if !ok || why != "" {
			t.Fatalf("ok %v why %q", ok, why)
		}
	})
	t.Run("ok after a few polls", func(t *testing.T) {
		n := 0
		ok, _, err := run(func(context.Context) (bool, string) { n++; return n == 3, "waiting" })
		noErr(t, err)
		if !ok || n != 3 {
			t.Fatalf("ok %v after %d calls", ok, n)
		}
	})
	t.Run("a context that ends is not ok", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		ok, _, err := r.ctl.pollUntil(ctx, func(context.Context) (bool, string) { return false, "" })
		if ok || err == nil {
			t.Fatalf("ok %v err %v", ok, err)
		}
	})
}
