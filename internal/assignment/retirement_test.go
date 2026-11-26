package assignment

import (
	"slices"
	"testing"
)

func TestFreezeWithRetiredRoute(t *testing.T) {
	for _, k := range []Kind{Freeze, RollbackFreeze} {
		s := forward(t, len(forwardSteps))
		c := next(s.ledger, key, k, "U", "")
		for i := range c.Evidence {
			if c.Evidence[i].Subject == "worker" {
				c.Evidence[i].Outcome = "retired-drained-disabled"
				c.Evidence[i].Digest = sha256Hex([]byte("worker retirement and disposition"))
			}
		}
		refused(t, s.ledger, c, ErrEvidence) // barrier still sees outstanding work on worker
		c.Evidence = slices.DeleteFunc(c.Evidence, func(e Evidence) bool { return e.Kind == "effect" })
		wrong := c
		wrong.Evidence = slices.Clone(c.Evidence)
		wrong.Evidence[len(wrong.Evidence)-1].Scope.Revision--
		refused(t, s.ledger, wrong, ErrEvidence)
		s.accept(t, c)
		tr, _ := s.ledger.Transfer(key, "U")
		if !slices.Equal(tr.Routes, []string{"qargi"}) {
			t.Fatal(tr.Routes)
		}
		for _, step := range forwardSteps[1:] {
			if step.route != "worker" {
				s.accept(t, next(s.ledger, key, step.kind, "U", step.route))
			}
		}
		assertHolder(t, s.ledger, legacy, 3, true)
	}
}
