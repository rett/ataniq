package assignment

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

var (
	key     = Key{"home", "project", "role"}
	legacy  = Binding{Kind: "legacy", ID: "old", Authority: "host-a", Store: "a", Incarnation: "i", Conversation: "a/chat"}
	managed = Binding{Kind: "managed", ID: "new", Authority: "host-b", Store: "b", Incarnation: "j", Conversation: "b/chat", RoleEpoch: 1, LaunchIntent: "launch"}
)

// command supplies authenticated fixture objects for a single requested step.
func command(r Row, tr Transfer, k Kind, id, route string) Command {
	c := Command{Expected: r.Head, Kind: k, TransferID: id, RequestID: fmt.Sprintf("%s-%d", k, r.Head.Revision), Actor: "owner", Authorization: "approval"}
	scope := r.Head
	var targetID string
	add := func(kind, subject, outcome string) {
		var renewal string
		if k == FenceReceipt || k == ConfirmReceipt {
			renewal = tr.ReconciliationID
		}
		c.Evidence = append(c.Evidence, Evidence{ReconciliationID: renewal,
			Kind: kind, Issuer: "source", Subject: subject, Revision: "snapshot", Outcome: outcome,
			Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(kind+subject+outcome))),
			Scope:  scope, TransferID: id, ScopeEventID: targetID,
		})
	}
	switch k {
	case Seed, Freeze, RollbackFreeze:
		if k == Seed {
			c.Candidate = legacy
			add("enrollment", legacy.ID, "verified")
		} else {
			c.Candidate = managed
			if r.Holder == managed {
				c.Candidate = legacy
			}
			add("barrier", r.Holder.ID, "captured")
			add("effect", "child", "outstanding")
			c.Evidence[len(c.Evidence)-1].Route = "worker"
		}
		add("route", "qargi", "enabled")
		add("route", "worker", "enabled")
	case Inventory:
		if tr.PreviewMismatch {
			add("preview", tr.Candidate.ID, "stopped")
		}
		add("inventory", r.Head.Key.Project, "complete")
		add("handoff", r.Head.Key.Project, "verified")
		for _, name := range tr.Routes {
			add("route", name, "frozen")
		}
	case BeliefAccepted:
		add("belief", tr.Inventory.Digest, "matched")
	case Reserve:
		c.Candidate = tr.Candidate
		add("decision", tr.Candidate.ID, "authorized")
	case FenceReceipt:
		scope, targetID = tr.Reservation, tr.ReservationEventID
		add("route", route, "sealed-and-drained")
		if route == "worker" {
			for _, id := range tr.Outstanding {
				add("effect", id.ID, "settled")
				c.Evidence[len(c.Evidence)-1].Route = id.Route
			}
		}
	case Fenced:
		add("revalidation", tr.Inventory.Digest, "current")
	case Commit:
		c.Candidate = tr.Candidate
		add("revalidation", tr.Decision.Digest, "current")
	case ConfirmReceipt:
		scope, targetID = tr.Committed, tr.CommittedEventID
		add("route", route, "installed-stale-refused")
	case Open:
		add("enrollment", r.Holder.ID, "current")
	case HoldStarted:
		c.HoldStart, c.EarliestRetirement = 100, 100+14*24*3600
		add("fallback", tr.Predecessor.ID, "stopped-disabled-preserved")
	case HoldReviewed:
		add("fallback", tr.Hold.Preservation.Digest, "reviewed")
	case AbortPrepared:
		add("candidate", tr.Candidate.ID, "never-executed")
	case Aborted, AbortedClosed:
		for _, id := range tr.Outstanding {
			add("effect", id.ID, "settled")
			c.Evidence[len(c.Evidence)-1].Route = id.Route
		}
		if k == Aborted {
			for _, name := range tr.Routes {
				add("route", name, "safe-to-reopen")
			}
		} else {
			add("incumbent", r.Holder.ID, "irreversibly-fenced")
		}
	}
	return c
}

type step struct {
	kind  Kind
	route string
	phase Phase
}

var seedSteps = []step{
	{Seed, "", PhaseCommittedClosed}, {ConfirmReceipt, "worker", PhaseCommittedClosed},
	{ConfirmReceipt, "qargi", PhaseCommittedClosed}, {Open, "", PhaseConfirmed},
}
var forwardSteps = []step{
	{Freeze, "", PhaseFrozen}, {Inventory, "", PhaseInventoried}, {BeliefAccepted, "", PhaseBeliefVerified},
	{Reserve, "", PhaseReserved}, {FenceReceipt, "worker", PhasePartiallyFenced}, {FenceReceipt, "qargi", PhasePartiallyFenced},
	{Fenced, "", PhaseFenced}, {Commit, "", PhaseCommittedClosed}, {ConfirmReceipt, "qargi", PhaseCommittedClosed},
	{ConfirmReceipt, "worker", PhaseCommittedClosed}, {Open, "", PhaseConfirmed},
}

func TestTransitionTable(t *testing.T) {
	cases := []struct {
		name   string
		steps  []step
		gen    uint64
		holder Binding
	}{
		{"forward and hold", appendSteps(forwardSteps, step{HoldStarted, "", PhaseConfirmed}, step{HoldReviewed, "", PhaseConfirmed}), 2, managed},
		{"abort and reopen", appendSteps(forwardSteps[:5], step{AbortPrepared, "", PhaseAborting}, step{Aborted, "", PhaseAborted}), 1, legacy},
		{"abort closed", appendSteps(forwardSteps[:4], step{AbortPrepared, "", PhaseAborting}, step{AbortedClosed, "", PhaseAbortedClosed}), 1, legacy},
		{"reverse committed closed", appendSteps(appendSteps(forwardSteps[:8], step{RollbackFreeze, "", PhaseFrozen}), forwardSteps[1:]...), 3, legacy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trace := fresh(t)
			r := Row{Head: Head{Key: key}}
			var tr Transfer
			id := "seed"
			for _, s := range appendSteps(seedSteps, tc.steps...) {
				if s.kind == Freeze {
					id = "T"
				}
				if s.kind == RollbackFreeze {
					id = "U"
				}
				c := command(r, tr, s.kind, id, s.route)
				before := r.Head
				trace.accept(t, c)
				r, _ = trace.ledger.Row(key)
				tr, _ = trace.ledger.Transfer(key, id)
				if r.Head.Phase != s.phase || r.Head.Revision != before.Revision+1 {
					t.Fatalf("%s: %+v", s.kind, r)
				}
				if before.Phase != r.Head.Phase && tr.PhaseRevision != r.Head.Revision {
					t.Fatalf("phase revision: %+v", tr)
				}
				if r.Permits(r.Head) != (s.phase == PhaseConfirmed || s.phase == PhaseAborted) || r.Permits(before) {
					t.Fatalf("admission after %s", s.kind)
				}
			}
			if r.Head.Generation != tc.gen || r.Holder != tc.holder {
				t.Fatalf("holder: %+v", r)
			}
		})
	}
}

func appendSteps(base []step, more ...step) []step {
	return append(append([]step(nil), base...), more...)
}
