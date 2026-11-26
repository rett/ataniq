package assignment

import (
	"fmt"
	"strings"
	"testing"
)

func TestReconcileInBatches(t *testing.T) {
	s := forward(t, 11)
	const count = 512
	var reports []Evidence
	for i := 0; i < count; i++ {
		c := next(s.ledger, key, ConfirmReceipt, "T", "worker")
		report := c.Evidence[0]
		report.Outcome = "needs-review"
		report.Digest = sha256Hex([]byte(fmt.Sprint(i)))
		c.Evidence = []Evidence{report}
		if i == 0 {
			work := report
			work.Kind, work.Subject, work.Route, work.Outcome = "effect", "child", "worker", "outstanding"
			c.Evidence = append(c.Evidence, work)
		}
		s.accept(t, c)
		reports = append(reports, report)
	}
	for start := 0; start < count; start += 32 {
		c := next(s.ledger, key, ReceiptReconciled, "T", "")
		for _, report := range reports[start : start+32] {
			p := report
			p.Kind, p.Subject, p.Outcome, p.Scope, p.ScopeEventID = "reconciliation", report.Digest, "reconciled", c.Expected, ""
			c.Evidence = append(c.Evidence, p)
		}
		e := s.accept(t, c)
		b, err := e.RecordBytes()
		if err != nil || len(b) > 65536 || e.Result.Head.Phase != PhaseReceiptDisputed {
			t.Fatal(start, len(b), e.Result, err)
		}
	}
	end := next(s.ledger, key, ReceiptReconciled, "T", "")
	refused(t, s.ledger, end, ErrEvidence)
	end.Evidence = []Evidence{{Kind: "effect", Subject: "child", Route: "worker", Outcome: "settled", Issuer: "source", Digest: sha256Hex([]byte("done")), Revision: "done", Scope: end.Expected, TransferID: "T"}}
	e := s.accept(t, end)
	if e.Result.Head.Phase != PhaseCommittedClosed {
		t.Fatal(e.Result)
	}
	s.steps(t, "T", forwardSteps[4:6])
	s.steps(t, "T", forwardSteps[8:])
}

func TestDecideReservesSignedFrameSpace(t *testing.T) {
	s := fresh(t)
	c := next(s.ledger, key, Seed, "seed", "")
	e, _, err := Decide(s.ledger, c)
	if err != nil {
		t.Fatal(err)
	}
	e.Signature = strings.Repeat("A", 262) // unpadded base64url of the schema-1 SSHSIG
	b, err := e.RecordBytes()
	if err != nil {
		t.Fatal(err)
	}
	c.Actor += strings.Repeat("a", 65536-len(b))
	e, _, err = Decide(s.ledger, c)
	if err != nil {
		t.Fatal(err)
	}
	e.Signature = strings.Repeat("A", 262)
	b, err = e.RecordBytes()
	if err != nil || len(b) != 65536 {
		t.Fatal(len(b), err)
	}
	c.Actor += "a"
	refused(t, s.ledger, c, ErrLimit)
}
