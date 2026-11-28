package assignment

import (
	"slices"
	"testing"
)

// Use the enrollment confirmation so every subsequent phase, even a freeze
// with no receipt of its own, is exposed to a correction at the original scope.
func TestQuarantineAtEveryPhase(t *testing.T) {
	for _, steps := range [][]step{
		{}, forwardSteps[:1], forwardSteps[:2], forwardSteps[:3], forwardSteps[:4],
		forwardSteps[:5], forwardSteps[:7], forwardSteps[:8], forwardSteps,
		appendSteps(forwardSteps[:1], step{kind: AbortPrepared}),
		appendSteps(forwardSteps[:1], step{kind: AbortPrepared}, step{kind: Aborted}),
		appendSteps(forwardSteps[:1], step{kind: AbortPrepared}, step{kind: AbortedClosed}),
	} {
		s := seeded(t)
		s.steps(t, "move", steps)
		row, _ := s.ledger.Row(key)
		t.Run(string(row.Head.Phase), func(t *testing.T) {
			c := s.events[1].Command
			c.Expected, c.RequestID = row.Head, "correction"
			c.Evidence = slices.Clone(c.Evidence)
			refused(t, s.ledger, c, ErrEvidence) // original object under a new request
			c.Evidence[0].Digest = sha256Hex([]byte("new report"))
			c.Evidence[0].Issuer += "2"
			refused(t, s.ledger, c, ErrEvidence)
			c.Evidence[0].Issuer = s.events[1].Command.Evidence[0].Issuer
			e := s.accept(t, c)
			l, err := recoverAt(s.events, s.ledger.Checkpoint())
			if err != nil {
				t.Fatal(err)
			}
			closed, _ := l.Row(key)
			if closed.Head.Phase != PhaseQuarantined || closed.QuarantineEventID != e.Result.EventID ||
				closed.Head.TransferID != row.LastTransferID || closed.Permits(row.Head) || closed.Permits(closed.Head) {
				t.Fatal(closed)
			}
			for _, kind := range []Kind{Seed, Freeze, Inventory, BeliefAccepted, Reserve, FenceReceipt, Fenced, Commit,
				ConfirmReceipt, Open, HoldStarted, HoldReviewed, AbortPrepared, Aborted, AbortedClosed, RollbackFreeze} {
				id := row.LastTransferID
				if kind == Seed || kind == Freeze || kind == RollbackFreeze {
					id = "new"
				}
				refused(t, l, next(l, key, kind, id, "qargi"), ErrPhase)
			}
			ack := next(l, key, QuarantineAcknowledged, row.LastTransferID, "")
			for _, change := range []func(*Command){
				func(c *Command) { c.Actor += "2" }, func(c *Command) { c.Authorization += "2" },
				func(c *Command) { c.Evidence[0].ScopeEventID = "" },
				func(c *Command) { c.Evidence = nil },
			} {
				bad := ack
				bad.Evidence = slices.Clone(ack.Evidence)
				change(&bad)
				refused(t, l, bad, ErrEvidence)
			}
			a := s.accept(t, ack)
			l, err = recoverAt(s.events, s.ledger.Checkpoint())
			if err != nil {
				t.Fatal(err)
			}
			final, _ := l.Row(key)
			if final.Head.Phase != PhaseQuarantined || final.Permits(final.Head) || final.QuarantineEventID != e.Result.EventID ||
				final.QuarantineAcknowledgement != ack.Evidence[0] {
				t.Fatal(final)
			}
			if retry, r, err := Decide(l, ack); retry != nil || err != nil || r != a.Result {
				t.Fatal(r, err)
			}
			refused(t, l, next(l, key, QuarantineAcknowledged, row.LastTransferID, ""), ErrPhase)
			refused(t, l, next(l, key, RollbackFreeze, "new", ""), ErrPhase)
		})
	}
}
