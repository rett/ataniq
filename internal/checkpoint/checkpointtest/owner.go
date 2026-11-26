package checkpointtest

import (
	cp "ataniq/internal/checkpoint"
	"testing"
)

func ownerCAS(t *testing.T, factory Factory, op string) {
	f := start(t, factory)
	h := f.home(t, "west", "s", "one", true)
	s := f.claim(t, h, f.enroll(t, h), "claim")
	r, err := h.c.Advance(f.ctx, advance(s, "progress"))
	check(t, err)
	a, err := cp.VerifyReceipt(f.pin, r)
	check(t, err)
	m := mutation(s, "stale owner decision")
	if op == "release" {
		_, err = f.Admin.Release(f.ctx, m)
	} else {
		_, err = f.Admin.Retire(f.ctx, cp.Retirement{Mutation: m, DispositionDigest: digest("disposed")})
	}
	want(t, err, cp.ErrStale)
	if f.state(t, h, "after stale decision") != a.State {
		t.Fatal("owner operation changed the head")
	}
}
