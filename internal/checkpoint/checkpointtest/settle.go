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
	_, err := f.Admin.Settle(f.ctx, 1, h.slot, claim.ID)
	want(t, err, cp.ErrNotApplied)
	_, err = h.c.Claim(f.ctx, claim)
	if !errors.Is(err, cp.ErrNotApplied) {
		t.Fatalf("owner fence did not stop late claim: %v", err)
	}
	claim.ID = id("new claim")
	_, err = h.c.Settle(f.ctx, 1, h.slot, claim.ID)
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
		prior, err := settler.Settle(f.ctx, 1, h.slot, claim.ID)
		if err != nil || !same(receipt, prior) {
			t.Fatal("settlement lost committed receipt")
		}
	}
	f.Revoke(h.slot)
	_, err = h.c.Settle(f.ctx, 1, h.slot, claim.ID)
	want(t, err, cp.ErrRefused)
	prior, err := f.Admin.Settle(f.ctx, 1, h.slot, claim.ID)
	check(t, err)
	if !same(receipt, prior) {
		t.Fatal("owner lost settlement after revocation")
	}
}

func settlementBoundaries(t *testing.T, factory Factory) {
	for _, role := range []string{"owner", "home"} {
		t.Run("settle epoch "+role, func(t *testing.T) {
			f := start(t, factory)
			h := f.home(t, "west", "s", "one", true)
			claim := cp.Claim{Mutation: mutation(f.enroll(t, h), "pending"), NewSessionPublicKey: pub(h.session)}
			var settler cp.Settler = h.c
			if role == "owner" {
				settler = f.Admin
			}
			_, err := settler.Settle(f.ctx, 2, h.slot, claim.ID)
			if !errors.Is(err, cp.ErrStale) {
				t.Fatalf("wrong epoch: %v", err)
			}
			_, err = h.c.Claim(f.ctx, claim)
			check(t, err)
		})
	}
	t.Run("settle foreign home", func(t *testing.T) {
		f := start(t, factory)
		h := f.home(t, "west", "s", "one", true)
		claim := cp.Claim{Mutation: mutation(f.enroll(t, h), "pending"), NewSessionPublicKey: pub(h.session)}
		foreign := key("foreign home")
		err := f.Grant(h.slot, pub(foreign), true)
		if f.HomeKey != nil {
			wantReason(t, err, cp.ErrRefused, cp.HomeMismatch)
		} else {
			check(t, err)
			other, err := f.Connect(h.slot, foreign, h.session, f.pin)
			check(t, err)
			_, err = other.Settle(f.ctx, 1, h.slot, claim.ID)
			if !errors.Is(err, cp.ErrRefused) {
				t.Fatalf("foreign home settlement: %v", err)
			}
		}
		check(t, f.Grant(h.slot, pub(h.private), true))
		_, err = h.c.Claim(f.ctx, claim)
		check(t, err)
	})
	t.Run("settle readonly", func(t *testing.T) {
		f := start(t, factory)
		h := f.home(t, "west", "s", "one", false)
		s := f.enroll(t, h)
		m := mutation(s, "read-only fence")
		_, err := h.c.Settle(f.ctx, 1, h.slot, m.ID)
		if !errors.Is(err, cp.ErrRefused) {
			t.Fatalf("read-only settlement: %v", err)
		}
		check(t, f.Grant(h.slot, pub(h.private), true))
		_, err = h.c.Claim(f.ctx, cp.Claim{Mutation: m, NewSessionPublicKey: pub(h.session)})
		check(t, err)
	})
	t.Run("settle owner history", func(t *testing.T) {
		f := start(t, factory)
		h := f.home(t, "west", "s", "one", true)
		enrolled, err := f.Admin.Enroll(f.ctx, h.e)
		check(t, err)
		s := f.claim(t, h, f.state(t, h, "enrolled"), "writer")
		release := mutation(s, "release")
		released, err := f.Admin.Release(f.ctx, release)
		check(t, err)
		retire := cp.Retirement{Mutation: mutation(f.state(t, h, "released"), "retire"), DispositionDigest: digest("disposition")}
		retired, err := f.Admin.Retire(f.ctx, retire)
		check(t, err)
		for _, op := range []struct {
			name    string
			id      cp.ID
			receipt cp.Receipt
		}{
			{"enroll", h.e.ID, enrolled}, {"release", release.ID, released}, {"retire", retire.ID, retired},
		} {
			for _, settler := range []cp.Settler{f.Admin, h.c} {
				r, err := settler.Settle(f.ctx, 1, h.slot, op.id)
				if err != nil || !same(r, op.receipt) {
					t.Fatalf("lost committed %s receipt: %v", op.name, err)
				}
			}
		}
	})
	t.Run("settle before enroll", func(t *testing.T) {
		f := start(t, factory)
		h := f.home(t, "west", "s", "one", true)
		_, err := f.Admin.Settle(f.ctx, 1, h.slot, h.e.ID)
		want(t, err, cp.ErrNotApplied)
		_, err = f.Admin.Enroll(f.ctx, h.e)
		if !errors.Is(err, cp.ErrNotApplied) {
			t.Fatalf("late enrollment escaped fence: %v", err)
		}
	})
	t.Run("settle refence", func(t *testing.T) {
		f := start(t, factory)
		h := f.home(t, "west", "s", "one", true)
		f.enroll(t, h)
		id := id("absent")
		_, err := f.Admin.Settle(f.ctx, 1, h.slot, id)
		want(t, err, cp.ErrNotApplied)
		_, err = f.Admin.Settle(f.ctx, 1, h.slot, id)
		if !errors.Is(err, cp.ErrNotApplied) {
			t.Fatalf("repeated settlement: %v", err)
		}
		// Enrollment and the single fence leave one credit for a new fence.
		f.SetReceiptLimit(3)
		_, err = f.Admin.Settle(f.ctx, 1, h.slot, h.e.ID)
		check(t, err)
		_, err = f.Admin.Settle(f.ctx, 1, h.slot, cp.ID{3})
		want(t, err, cp.ErrNotApplied)
	})
	t.Run("settle changed bytes", func(t *testing.T) {
		f := start(t, factory)
		h := f.home(t, "west", "s", "one", true)
		m := mutation(f.enroll(t, h), "late")
		_, err := h.c.Settle(f.ctx, 1, h.slot, m.ID)
		want(t, err, cp.ErrNotApplied)
		_, err = h.c.Claim(f.ctx, cp.Claim{Mutation: m, NewSessionPublicKey: pub(h.session)})
		want(t, err, cp.ErrNotApplied)
		other := f.reconnect(t, h, key("different session"))
		_, err = other.c.Claim(f.ctx, cp.Claim{Mutation: m, NewSessionPublicKey: pub(other.session)})
		if !errors.Is(err, cp.ErrNotApplied) {
			t.Fatalf("changed claim escaped fence: %v", err)
		}
	})
	for _, guard := range []string{"inhibited", "limit"} {
		t.Run("settle "+guard, func(t *testing.T) {
			f := start(t, factory)
			h := f.home(t, "west", "s", "one", true)
			enrolled, err := f.Admin.Enroll(f.ctx, h.e)
			check(t, err)
			s := f.state(t, h, "enrolled")
			fenced := id("already fenced")
			_, err = f.Admin.Settle(f.ctx, 1, h.slot, fenced)
			want(t, err, cp.ErrNotApplied)
			if guard == "inhibited" {
				f.Inhibit(true)
			} else {
				f.SetReceiptLimit(2)
			}
			r, err := f.Admin.Settle(f.ctx, 1, h.slot, h.e.ID)
			if err != nil || !same(r, enrolled) {
				t.Fatalf("lost receipt during %s: %v", guard, err)
			}
			_, err = f.Admin.Settle(f.ctx, 1, h.slot, fenced)
			want(t, err, cp.ErrNotApplied)
			m := mutation(s, "new")
			settler := cp.Settler(f.Admin)
			expected := cp.ErrUnavailable
			if guard == "limit" {
				expected = cp.ErrCapacity
				settler = h.c
			}
			_, err = settler.Settle(f.ctx, 1, h.slot, m.ID)
			if !errors.Is(err, expected) {
				t.Fatalf("new fence during %s: %v", guard, err)
			}
			f.Inhibit(false)
			f.SetReceiptLimit(0)
			_, err = h.c.Claim(f.ctx, cp.Claim{Mutation: m, NewSessionPublicKey: pub(h.session)})
			check(t, err)
		})
	}
	t.Run("settle restart", func(t *testing.T) {
		f := start(t, factory)
		h := f.home(t, "west", "s", "one", true)
		m := mutation(f.enroll(t, h), "late")
		_, err := f.Admin.Settle(f.ctx, 1, h.slot, m.ID)
		want(t, err, cp.ErrNotApplied)
		homeFence := mutation(m.Expected, "late home")
		_, err = h.c.Settle(f.ctx, 1, h.slot, homeFence.ID)
		want(t, err, cp.ErrNotApplied)
		f.Harness = f.Restart()
		h = f.reconnect(t, h, h.session)
		for _, fence := range []cp.Mutation{m, homeFence} {
			_, err = h.c.Claim(f.ctx, cp.Claim{Mutation: fence, NewSessionPublicKey: pub(h.session)})
			if !errors.Is(err, cp.ErrNotApplied) {
				t.Fatalf("restart lost fence: %v", err)
			}
			for _, settler := range []cp.Settler{h.c, f.Admin} {
				_, err = settler.Settle(f.ctx, 1, h.slot, fence.ID)
				want(t, err, cp.ErrNotApplied)
			}
		}
	})
}
