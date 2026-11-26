package checkpoint

import (
	"ataniq/internal/assignment"
	"errors"
	"fmt"
	"testing"
)

func TestStartupReasons(t *testing.T) {
	f := newSetup(t)
	ctx := t.Context()
	reason := func(err, category error, kind ReasonKind) {
		t.Helper()
		var r *Reason
		if !errors.Is(err, category) || !errors.As(fmt.Errorf("adapter: %w", err), &r) || r.Kind != kind {
			t.Fatalf("got %v, want %v (%s)", err, category, kind)
		}
	}
	read := ReadRequest{Slot: f.e.Slot, Challenge: fixtureID("read")}
	_, err := f.c.Read(ctx, read)
	reason(err, ErrRefused, NotEnrolled)
	_, err = f.w.Administration().Read(ctx, read)
	reason(err, ErrRefused, NotEnrolled)
	_, err = f.w.Administration().Enroll(ctx, f.e)
	requireOK(t, err)
	v, err := f.w.Administration().Read(ctx, read)
	requireOK(t, err)
	claim := Claim{Mutation{fixtureID("claim"), v.Current}, public(f.session)}
	_, err = f.c.Claim(ctx, claim)
	requireOK(t, err)
	_, err = f.w.Administration().Release(ctx, Mutation{fixtureID("stale"), v.Current})
	reason(err, ErrStale, PredecessorMismatch)
	_, err = f.c.Claim(ctx, Claim{Mutation{fixtureID("bad"), State{}}, public(f.session)})
	reason(err, ErrRefused, InvalidRequest)
	_, err = f.w.Administration().Enroll(ctx, f.e)
	reason(err, ErrStale, Superseded)
	v, err = f.w.Administration().Read(ctx, read)
	requireOK(t, err)
	claim.Expected = v.Current
	_, err = f.c.Claim(ctx, claim)
	reason(err, ErrStale, MutationConflict)
	claim.ID = fixtureID("busy")
	_, err = f.c.Claim(ctx, claim)
	reason(err, ErrStale, SessionConflict)
	f.w.Revoke(f.e.Slot)
	_, err = f.c.Read(ctx, read)
	reason(err, ErrRefused, Unauthorized)
}

func TestCompareHeads(t *testing.T) {
	v := vectors(t)["advance-ack"]
	a, err := VerifyReceipt(public(fixtureKey("witness")), Receipt{v.Body, []byte(v.Signatures["witness"])})
	requireOK(t, err)
	s := a.State
	ledger, err := assignment.NewLedger(s.Header)
	requireOK(t, err)
	check := func(label string, home assignment.Checkpoint, w State, want Comparison) {
		t.Helper()
		if got := Compare(home, w); got != want {
			t.Errorf("%s: got %s, want %s", label, got, want)
		}
	}
	check("equal", s.Head, s, Equal)
	check("missing witness", s.Head, State{}, Unknown)
	older := ledger.Checkpoint()
	check("behind", older, s, HomeBehind)
	later := s.Head
	later.Sequence++
	later.Digest = idString(fixtureID("later"))
	check("ahead", later, s, HomeAhead)
	different := s.Head
	different.Digest = later.Digest
	check("fork", different, s, Fork)
	for _, edit := range []func(*assignment.Checkpoint){func(p *assignment.Checkpoint) { p.Authority += "x" }, func(p *assignment.Checkpoint) { p.Store += "x" }, func(p *assignment.Checkpoint) { p.LedgerID += "x" }, func(p *assignment.Checkpoint) { p.Incarnation += "x" }} {
		for _, h := range []assignment.Checkpoint{older, s.Head, later} {
			edit(&h)
			check("different ledger", h, s, Fork)
		}
	}
	for _, h := range []assignment.Checkpoint{older, s.Head, later, different, {}} {
		tomb := s
		tomb.Status = Retired
		tomb.SessionKeyID = ""
		check("tombstone", h, tomb, Retired)
	}
	check("missing home", assignment.Checkpoint{}, s, Unknown)
	invalid := s.Head
	invalid.Digest = ""
	check("invalid home", invalid, s, Unknown)
	invalid = older
	invalid.Digest = later.Digest
	check("genesis fork", invalid, s, Fork)
	s.Status = "invalid"
	check("invalid witness", older, s, Unknown)
}
