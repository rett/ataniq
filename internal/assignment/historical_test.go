package assignment

import (
	"testing"
)

func TestHistoricalCorrectionQuarantinesCurrentTransfer(t *testing.T) {
	s := forward(t, len(forwardSteps))
	old, _ := s.ledger.Transfer(key, "T")
	s.steps(t, "U", []step{{kind: RollbackFreeze}})
	begin := s.events[len(s.events)-1]
	row, _ := s.ledger.Row(key)
	c := command(row, old, FenceReceipt, "T", "worker")
	report := &c.Evidence[0]
	report.Outcome, report.Digest = "child-discovered", sha256Hex([]byte("late report"))
	child := *report
	child.Kind, child.Subject, child.Route, child.Outcome = "effect", "late", "worker", "outstanding"
	child.Digest = sha256Hex([]byte("late child"))
	c.Evidence = append(c.Evidence, child)
	e := s.accept(t, c)
	if e.Result.Head.TransferID != "U" || e.Result.Head.Phase != PhaseQuarantined {
		t.Fatal(e.Result)
	}
	l, err := recoverAt(s.events, s.ledger.Checkpoint())
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := Lookup(l, key, "T", c.RequestID); !ok || got != e.Result {
		t.Fatal(got)
	}
	active, ok := l.Transfer(key, "U")
	latest, found := Lookup(l, key, "U", "")
	if !ok || !found || latest != e.Result || latest.Disposition != active.Disposition ||
		latest.Head.Phase != active.Phase || latest.Head.Revision != active.PhaseRevision {
		t.Fatal("historical correction missing from active disposition", active, latest)
	}
	if original, ok := Lookup(l, key, "U", begin.Command.RequestID); !ok || original != begin.Result {
		t.Fatal(original)
	}
	if _, misindexed := Lookup(l, key, "U", c.RequestID); misindexed {
		t.Fatal("historical request indexed under active transfer")
	}
	refused(t, l, next(l, key, Inventory, "U", ""), ErrPhase)
}
