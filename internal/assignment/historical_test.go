package assignment

import (
	"slices"
	"testing"
)

func TestHistoricalCorrectionFreezesCurrentTransfer(t *testing.T) {
	s := forward(t, len(forwardSteps))
	old, _ := s.ledger.Transfer(key, "T")
	s.steps(t, "U", []step{{kind: RollbackFreeze}})
	row, _ := s.ledger.Row(key)
	c := command(row, old, FenceReceipt, "T", "worker")
	report := &c.Evidence[0]
	report.Outcome, report.Digest = "child-discovered", sha256Hex([]byte("late report"))
	child := *report
	child.Kind, child.Subject, child.Route, child.Outcome = "effect", "late", "worker", "outstanding"
	child.Digest = sha256Hex([]byte("late child"))
	c.Evidence = append(c.Evidence, child)
	e := s.accept(t, c)
	if e.Result.Head.TransferID != "U" || e.Result.Head.Phase != PhaseReceiptDisputed {
		t.Fatal(e.Result)
	}
	l, err := recoverAt(s.events, s.ledger.Checkpoint())
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := Lookup(l, key, "T", c.RequestID); !ok || got != e.Result {
		t.Fatal(got)
	}
	u, _ := l.Transfer(key, "U")
	if !slices.Contains(u.Disputes, c.Evidence[0]) || !slices.Contains(u.Outstanding, Effect{"late", "worker"}) {
		t.Fatal(u)
	}
	refused(t, l, next(l, key, Inventory, "U", ""), ErrEvidence)
	resolve := next(l, key, ReceiptReconciled, "U", "")
	proof := c.Evidence[0]
	proof.Kind, proof.Subject, proof.Outcome = "reconciliation", proof.Digest, "reconciled"
	proof.Scope, proof.ScopeEventID, proof.TransferID = resolve.Expected, "", "U"
	resolve.Evidence = []Evidence{proof}
	for _, work := range u.Outstanding {
		done := proof
		done.Kind, done.Subject, done.Route, done.Outcome = "effect", work.ID, work.Route, "settled"
		resolve.Evidence = append(resolve.Evidence, done)
	}
	s.accept(t, resolve)
	s.steps(t, "U", forwardSteps[1:])
	assertHolder(t, s.ledger, legacy, 3, true)
}
