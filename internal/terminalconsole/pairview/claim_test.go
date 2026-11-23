package pairview

import (
	"strings"
	"testing"
	"time"
)

// claimed is the pending state of a credential whose authenticator claimed a
// kind and backup flags.
func (r *rig) claimed(kind string, be, bs bool) {
	r.t.Helper()
	r.b.state = State{Phase: Pending, PendingID: testPID, Words: testWords, ExpiresAt: r.now.Add(5 * time.Minute),
		Kind: kind, BackupEligible: be, BackupState: bs}
	r.fire()
	if r.m.screen != scPending {
		r.t.Fatalf("screen %v after the poll, want pending", r.m.screen)
	}
}

// What the authenticator claimed is shown at the pending step, before the
// owner confirms, and always as a claim: in the view's own words, never the
// daemon's.
func TestTheClaimIsShownAsAClaimBeforeTheConfirm(t *testing.T) {
	for _, c := range []struct {
		name   string
		kind   string
		be, bs bool
		want   string
	}{
		{"a synced vault passkey", "vault-passkey", true, true, "Claimed, not proven: vault passkey, synced."},
		{"a platform passkey not synced yet", "platform-passkey", true, false, "Claimed, not proven: platform passkey, can sync, not synced yet."},
		{"a passkey of no known provider", "passkey", false, false, "Claimed, not proven: passkey of an unknown kind, kept on one device."},
		{"a backup state without eligibility", "vault-passkey", false, true, "Claimed, not proven: vault passkey, kept on one device."},
		{"a word the console does not know", "hard\x1b[31mware", true, true, "Claimed, not proven: a kind this console does not know, synced."},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, &fakeBackend{}, testOrigin, 0, 0)
			r.start()
			r.claimed(c.kind, c.be, c.bs)
			r.see(80, 24, "1 lemon", c.want, "Compare with your phone.", "c: confirm")
			r.notSee(80, 24, "\x1b", "hardware", "ware")
			// The second question keeps it in view.
			r.press("c")
			if r.m.screen != scConfirm {
				t.Fatalf("screen %v", r.m.screen)
			}
			r.see(80, 24, c.want, confirmLine2)
		})
	}
}

// A pending state with no claim draws no claim line: the screens of a pairing
// without one are as they were (the goldens pending-* hold them).
func TestNoClaimDrawsNoClaimLine(t *testing.T) {
	r := newRig(t, &fakeBackend{}, testOrigin, 0, 0)
	r.start()
	r.pending()
	r.notSee(80, 24, "Claimed", "claimed")
	// Flags without a kind are no claim either.
	r.b.state.BackupEligible, r.b.state.BackupState = true, true
	r.fire()
	r.notSee(80, 24, "Claimed", "synced")
}

// A claim that changes under the second question takes the question away: the
// owner answers only about what he looked at.
func TestAClaimThatChangesTakesTheQuestionAway(t *testing.T) {
	r := newRig(t, &fakeBackend{}, testOrigin, 0, 0)
	r.start()
	r.claimed("vault-passkey", true, true)
	r.m.SetSize(80, 24)
	r.press("c")
	if r.m.screen != scConfirm {
		t.Fatalf("screen %v", r.m.screen)
	}
	r.b.state.Kind = "platform-passkey"
	r.fire()
	if r.m.screen != scPending {
		t.Fatalf("screen %v after the claim changed, want pending", r.m.screen)
	}
	r.see(80, 24, "Claimed, not proven: platform passkey, synced.")
	r.notSee(80, 24, confirmLine2)
}

func TestTheKindOfAPairedKeyIsShownAsAClaim(t *testing.T) {
	r := newRig(t, &fakeBackend{}, testOrigin, 0, 0)
	r.start()
	r.claimed("vault-passkey", true, true)
	r.m.SetSize(80, 24)
	r.b.paired = Paired{ID: "cred-7f3a9b", Kind: "vault-passkey"}
	r.press("c")
	r.now = r.now.Add(time.Second)
	r.press("y")
	s := text(r.m, 80, 24)
	if !strings.Contains(s, "Paired. Kind, claimed: vault passkey.") || strings.Contains(s, "vault-passkey") {
		t.Fatalf("paired screen:\n%s", s)
	}
}

func TestClaimGoldens(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
	}{
		{"pending-claim-80x24", 80, 24},
		{"pending-claim-60x20", 60, 20},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, &fakeBackend{}, testOrigin, 0, 0)
			r.start()
			r.claimed("vault-passkey", true, true)
			golden(t, c.name, text(r.m, c.w, c.h))
		})
	}
}
