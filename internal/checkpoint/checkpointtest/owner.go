package checkpointtest

import (
	"ataniq/internal/assignment"
	cp "ataniq/internal/checkpoint"
	"encoding/json"
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

func tombstone(t *testing.T, factory Factory, field string) {
	f := start(t, factory)
	h := f.home(t, "west", "s", "one", true)
	original := f.enroll(t, h)
	r, err := f.Admin.Retire(f.ctx, cp.Retirement{Mutation: mutation(original, "retire"), DispositionDigest: digest("d")})
	check(t, err)
	a, err := cp.VerifyReceipt(f.pin, r)
	check(t, err)
	m := mutation(a.State, "new decision")
	switch field {
	case "release":
		_, err = f.Admin.Release(f.ctx, m)
		want(t, err, cp.ErrRetired)
	case "retire":
		_, err = f.Admin.Retire(f.ctx, cp.Retirement{Mutation: m, DispositionDigest: digest("again")})
		want(t, err, cp.ErrRetired)
	default:
		fresh := f.home(t, "west", "s2", "two", true)
		var frame struct{ Header assignment.Header }
		check(t, json.Unmarshal(fresh.e.HeaderBytes, &frame))
		header := frame.Header
		switch field {
		case "ledger":
			header.LedgerID = original.Header.LedgerID
		case "incarnation":
			header.Incarnation = original.Header.Incarnation
		case "key":
			fresh.private = h.private
			fresh.e.HomePublicKey = pub(h.private)
			header.KeyID = original.HomeKeyID
			check(t, f.Grant(fresh.slot, fresh.e.HomePublicKey, true))
		}
		fresh.e.HeaderBytes, err = header.Bytes()
		check(t, err)
		fresh.e.ProofOfPossession, err = cp.ProveEnrollment(fresh.private, fresh.e)
		check(t, err)
		_, err = f.Admin.Enroll(f.ctx, fresh.e)
		want(t, err, cp.ErrStale)
	}
	if f.state(t, h, "tombstone unchanged") != a.State {
		t.Fatal("permanent tombstone changed")
	}
}
