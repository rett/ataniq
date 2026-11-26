package assignment

import (
	"slices"
	"testing"
)

func TestInventoryInvalidatesDependentEvidence(t *testing.T) {
	s := forward(t, 7)
	oldDecision := next(s.ledger, key, Commit, "T", "")
	oldReceipt := s.events[len(seedSteps)+4].Command.Evidence
	snapshot := next(s.ledger, key, Inventory, "T", "")
	snapshot.Evidence[0].Revision = "next"
	snapshot.Evidence[0].Digest = snapshot.Evidence[1].Digest
	s.accept(t, snapshot)
	refused(t, s.ledger, oldDecision, ErrStale)
	tr, _ := s.ledger.Transfer(key, "T")
	if tr.Inventory.Revision != "next" || tr.Belief != (Evidence{}) || tr.Decision != (Evidence{}) ||
		tr.Reservation != (Head{}) || len(tr.Fences) != 0 {
		t.Fatal(tr)
	}
	s.steps(t, "T", forwardSteps[2:4])
	c := next(s.ledger, key, FenceReceipt, "T", "worker")
	c.Evidence = oldReceipt
	refused(t, s.ledger, c, ErrEvidence)
}

func TestRejectedAndUncertainDispositions(t *testing.T) {
	s := forward(t, 1)
	c := next(s.ledger, key, Inventory, "T", "")
	c.Evidence = c.Evidence[:1]
	c.Evidence[0].Outcome = "uncertain"
	uncertain := s.accept(t, c)
	s.steps(t, "T", forwardSteps[1:2])
	c = next(s.ledger, key, BeliefAccepted, "T", "")
	c.Evidence[0].Outcome = "mismatch"
	rejected := s.accept(t, c)
	refused(t, s.ledger, next(s.ledger, key, BeliefAccepted, "T", ""), ErrPhase)
	retry := next(s.ledger, key, Inventory, "T", "")
	missing := retry
	missing.Evidence = retry.Evidence[1:]
	refused(t, s.ledger, missing, ErrEvidence)
	s.accept(t, retry)
	s.steps(t, "T", forwardSteps[2:4])
	for disposition, e := range map[string]Event{"uncertain": uncertain, "rejected": rejected} {
		got, ok := Lookup(s.ledger, key, "T", e.Command.RequestID)
		if !ok || got != e.Result || got.Disposition != disposition {
			t.Fatal(got)
		}
	}
}

func TestAuthorizationAndPhaseRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		n      int
		kind   Kind
		change func(*Command)
		want   error
	}{
		{"seed cannot replace existing identity", 0, Seed, nil, ErrPhase},
		{"unknown kind", 1, Kind("elect"), nil, ErrInvalid},
		{"competing reservation candidate", 3, Reserve, func(c *Command) { c.Candidate.RoleEpoch++ }, ErrConflict},
		{"receipt from another transfer", 4, FenceReceipt, func(c *Command) { c.Evidence[0].TransferID = "U" }, ErrEvidence},
		{"unknown route", 4, FenceReceipt, func(c *Command) { c.Evidence[0].Subject = "unlisted" }, ErrEvidence},
		{"duplicate route", 4, FenceReceipt, func(c *Command) { c.Evidence = append(c.Evidence, c.Evidence[0]) }, ErrEvidence},
		{"unconfirmed fence", 4, Commit, nil, ErrPhase},
		{"different decision on commit", 7, Commit, func(c *Command) { c.Authorization = "other" }, ErrEvidence},
		{"postcommit abort preparation", 8, AbortPrepared, nil, ErrPhase},
		{"postcommit abort", 8, Aborted, nil, ErrPhase},
		{"postcommit closed abort", 8, AbortedClosed, nil, ErrPhase},
		{"ordinary freeze during closed commit", 8, Freeze, nil, ErrPhase},
		{"wrong reverse binding", 8, RollbackFreeze, func(c *Command) { c.Candidate.ID = "other" }, ErrPhase},
		{"confirmation for another generation", 8, ConfirmReceipt, func(c *Command) { c.Evidence[0].Scope.Generation-- }, ErrEvidence},
		{"unconfirmed successor", 9, Open, nil, ErrEvidence},
		{"new authorization on open", 10, Open, func(c *Command) { c.Authorization = "other" }, ErrEvidence},
		{"hold too short", 11, HoldStarted, func(c *Command) { c.EarliestRetirement-- }, ErrEvidence},
		{"hold would wrap", 11, HoldStarted, func(c *Command) { c.HoldStart = ^uint64(0) - 2 }, ErrEvidence},
		{"hold missing", 11, HoldReviewed, func(c *Command) { c.Evidence[0].Subject = sha256Hex([]byte("preservation")) }, ErrPhase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := forward(t, tc.n)
			id := "T"
			switch tc.kind {
			case Seed, Freeze, RollbackFreeze:
				id = "U"
			}
			c := next(s.ledger, key, tc.kind, id, "worker")
			if tc.change != nil {
				tc.change(&c)
			}
			refused(t, s.ledger, c, tc.want)
			if _, ok := Lookup(s.ledger, key, id, c.RequestID); ok {
				t.Fatal("refusal consumed ID")
			}
		})
	}
}

func TestAbortCannotReopenAnUnconfirmedIncumbent(t *testing.T) {
	s := forward(t, 8)
	s.steps(t, "U", []step{{kind: RollbackFreeze}, {kind: AbortPrepared}})
	refused(t, s.ledger, next(s.ledger, key, Aborted, "U", ""), ErrPhase)
	s.steps(t, "U", []step{{kind: AbortedClosed}})
	assertHolder(t, s.ledger, managed, 2, false)
	refused(t, s.ledger, next(s.ledger, key, Freeze, "U", ""), ErrConflict)
	c := next(s.ledger, key, Freeze, "replacement", "")
	c.Evidence = slices.DeleteFunc(c.Evidence, func(e Evidence) bool { return e.Subject == "worker" })
	refused(t, s.ledger, c, ErrEvidence)
	s.steps(t, "replacement", forwardSteps)
	assertHolder(t, s.ledger, legacy, 3, true)
	tr, _ := s.ledger.Transfer(key, "U")
	if tr.Disposition != "aborted-closed" {
		t.Fatal(tr)
	}
}

func TestFreezeRetainsConfirmedRoutes(t *testing.T) {
	s := seeded(t)
	c := next(s.ledger, key, Freeze, "T", "")
	c.Evidence = slices.DeleteFunc(c.Evidence, func(e Evidence) bool { return e.Subject == "worker" || e.Kind == "effect" })
	refused(t, s.ledger, c, ErrEvidence)
	s.steps(t, "T", forwardSteps)
}

func TestAbortNeedsMismatchedPreviewStop(t *testing.T) {
	s := forward(t, 2)
	belief := next(s.ledger, key, BeliefAccepted, "T", "")
	belief.Evidence[0].Outcome = "mismatch"
	s.accept(t, belief)
	c := next(s.ledger, key, AbortPrepared, "T", "")
	stopped := next(s.ledger, key, Inventory, "T", "").Evidence[0]
	refused(t, s.ledger, c, ErrEvidence)
	wrong := c
	wrong.Evidence = append(slices.Clone(c.Evidence), stopped)
	wrong.Evidence[1].Subject = legacy.ID
	refused(t, s.ledger, wrong, ErrEvidence)
	c.Evidence = append(c.Evidence, stopped)
	s.accept(t, c)
	s.steps(t, "T", []step{{kind: Aborted}})
	assertHolder(t, s.ledger, legacy, 1, true)
}

func TestManagedLaunchIdentityPair(t *testing.T) {
	for _, kind := range []Kind{Seed, Freeze} {
		for _, tc := range []struct {
			intent string
			epoch  uint64
			valid  bool
		}{{"launch", 0, false}, {"", 1, false}, {"", 0, true}, {"launch", 1, true}} {
			s := fresh(t)
			if kind == Freeze {
				s = seeded(t)
			}
			c := next(s.ledger, key, kind, "T", "")
			c.Candidate = managed
			c.Candidate.LaunchIntent, c.Candidate.RoleEpoch = tc.intent, tc.epoch
			if kind == Seed {
				c.Evidence[0].Subject = managed.ID
			}
			if tc.valid {
				s.accept(t, c)
			} else {
				refused(t, s.ledger, c, ErrInvalid)
			}
		}
	}
}

func TestHoldReviewedEvidence(t *testing.T) {
	s := forward(t, len(forwardSteps))
	s.steps(t, "T", []step{{kind: HoldStarted}})
	c := next(s.ledger, key, HoldReviewed, "T", "")
	c.Evidence[0].Subject = sha256Hex([]byte("different preservation proof"))
	refused(t, s.ledger, c, ErrEvidence)
	s.accept(t, next(s.ledger, key, HoldReviewed, "T", ""))
}
