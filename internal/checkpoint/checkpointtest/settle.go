package checkpointtest

import (
	"errors"
	"testing"

	cp "ataniq/internal/checkpoint"
)

func settlement(t *testing.T, f fixture) {
	h := f.home(t, "west", "s", "one", true)
	s := f.enroll(t, h)
	claim := cp.Claim{Mutation: mutation(s, "late claim"), NewSessionPublicKey: pub(h.session)}
	_, err := f.Admin.Settle(f.ctx, h.slot, claim.ID)
	want(t, err, cp.ErrNotApplied)
	_, err = h.c.Claim(f.ctx, claim)
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatalf("owner fence did not stop late claim: %v", err)
	}
	claim.ID = id("new claim")
	_, err = h.c.Settle(f.ctx, h.slot, claim.ID)
	want(t, err, cp.ErrNotApplied)
	_, err = h.c.Claim(f.ctx, claim)
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatalf("home fence did not stop late claim: %v", err)
	}
	claim.ID = id("committed")
	receipt, err := h.c.Claim(f.ctx, claim)
	check(t, err)
	_, err = f.Admin.Release(f.ctx, mutation(f.state(t, h, "claimed"), "release"))
	check(t, err)
	for _, settler := range []cp.Settler{f.Admin, h.c} {
		prior, err := settler.Settle(f.ctx, h.slot, claim.ID)
		if err != nil || !same(receipt, prior) {
			t.Fatal("settlement lost committed receipt")
		}
	}
	f.Revoke(h.slot)
	_, err = h.c.Settle(f.ctx, h.slot, claim.ID)
	want(t, err, cp.ErrRefused)
	prior, err := f.Admin.Settle(f.ctx, h.slot, claim.ID)
	check(t, err)
	if !same(receipt, prior) {
		t.Fatal("owner lost settlement after revocation")
	}
}
