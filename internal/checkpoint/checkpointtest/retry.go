package checkpointtest

import (
	"testing"

	cp "ataniq/internal/checkpoint"
)

func lostAck(t *testing.T, factory Factory, op string) {
	f := start(t, factory)
	h := f.home(t, "west", "s", "one", true)
	mutationID := h.e.ID
	submit := func() (cp.Receipt, error) { return f.Admin.Enroll(f.ctx, h.e) }
	var claim cp.Claim
	if op != "enroll" {
		s := f.enroll(t, h)
		if op != "claim" {
			s = f.claim(t, h, s, "writer")
		}
		m := mutation(s, op)
		mutationID = m.ID
		switch op {
		case "claim":
			claim = cp.Claim{Mutation: m, NewSessionPublicKey: pub(h.session)}
			submit = func() (cp.Receipt, error) { return h.c.Claim(f.ctx, claim) }
		case "release":
			submit = func() (cp.Receipt, error) { return f.Admin.Release(f.ctx, m) }
		case "retire":
			q := cp.Retirement{Mutation: m, DispositionDigest: digest("disposed")}
			submit = func() (cp.Receipt, error) { return f.Admin.Retire(f.ctx, q) }
		}
	}

	f.LoseNextReply()
	_, err := submit()
	want(t, err, cp.ErrUnknownOutcome)
	q := cp.ReadRequest{Slot: h.slot, Challenge: id("locate lost receipt"), ReceiptID: mutationID}
	v, err := f.Admin.Read(f.ctx, q)
	check(t, err)
	check(t, cp.VerifyView(f.pin, q, v))
	if v.Prior == nil || cp.ReceiptDigest(*v.Prior) != v.Current.LastReceiptDigest {
		t.Fatal("lost acknowledgement is missing from current history")
	}
	if op == "claim" {
		replacement := f.reconnect(t, h, key("replacement session"))
		_, err := replacement.c.Claim(f.ctx, claim)
		want(t, err, cp.ErrRefused)
		h = f.reconnect(t, h, h.session)
	}
	for _, inhibited := range []bool{true, false} {
		f.Inhibit(inhibited)
		r, err := submit()
		if err != nil {
			t.Fatalf("exact %s retry (inhibited=%t): %v", op, inhibited, err)
		}
		if !same(*v.Prior, r) {
			t.Fatalf("exact %s retry changed receipt bytes (inhibited=%t)", op, inhibited)
		}
	}
	if f.state(t, h, "after retries") != v.Current {
		t.Fatal("exact retry changed current state")
	}
}

// Reopen once with a live session and again after retirement. Each generation
// must recognize the same committed receipts, including superseded IDs.
func restartHistory(t *testing.T, f fixture) {
	h := f.home(t, "west", "s", "one", true)
	enrolled, err := f.Admin.Enroll(f.ctx, h.e)
	check(t, err)
	claim := cp.Claim{Mutation: mutation(f.state(t, h, "genesis"), "claim"), NewSessionPublicKey: pub(h.session)}
	claimed, err := h.c.Claim(f.ctx, claim)
	check(t, err)
	f.Harness = f.Restart()
	h = f.reconnect(t, h, h.session)
	retry, err := h.c.Claim(f.ctx, claim)
	check(t, err)
	if !same(claimed, retry) {
		t.Fatal("restart changed latest claim receipt")
	}
	next := advance(f.state(t, h, "claimed"), "advance")
	advanced, err := h.c.Advance(f.ctx, next)
	check(t, err)
	release := mutation(f.state(t, h, "advanced"), "release")
	released, err := f.Admin.Release(f.ctx, release)
	check(t, err)
	retire := cp.Retirement{Mutation: mutation(f.state(t, h, "released"), "retire"), DispositionDigest: digest("disposed")}
	retired, err := f.Admin.Retire(f.ctx, retire)
	check(t, err)
	f.Harness = f.Restart()
	for _, saved := range []struct {
		id cp.ID
		r  cp.Receipt
	}{
		{h.e.ID, enrolled}, {claim.ID, claimed}, {next.ID, advanced}, {release.ID, released}, {retire.ID, retired},
	} {
		prior, err := f.Admin.Settle(f.ctx, 1, h.slot, saved.id)
		check(t, err)
		if !same(saved.r, prior) {
			t.Fatal("restart changed historical receipt", saved.id)
		}
	}
	h = f.reconnect(t, h, h.session)
	if f.state(t, h, "tombstone").Status != cp.Retired {
		t.Fatal("restart lost retirement")
	}
}
