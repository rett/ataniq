//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"strings"
	"testing"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/record"
	"ataniq/internal/intake/verify"
)

// The adapter runs the suite in merge mode of package verify: the run is told
// the pin that holds the merge, and the record it leaves names the merge as
// the tip, the base the merge was built on, and the pin as its ref.
func TestTheVerificationIsAMergeModeRecordOfTheMergeOnItsBase(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	merge := p.Binding.Merge.Commit
	pin := r.pinOfMerge(merge)
	if pin == "" || !strings.HasPrefix(pin, verify.PendingPrefix) {
		t.Fatalf("the merge has no pin: %q", pin)
	}
	call := r.suite.calls[0]
	if call.Merge != merge || call.Base != r.scene.target || call.TipRef != pin {
		t.Errorf("the suite was asked %+v, want merge %s on base %s through %s", call, merge, r.scene.target, pin)
	}
	all, err := r.o.Host.store.Verifications(merge)
	if err != nil || len(all) != 1 {
		t.Fatalf("records %v, %v", all, err)
	}
	if rec := all[0]; !rec.Merge || rec.Base != r.scene.target || rec.TipRef != pin || rec.Tip != merge || !rec.Passed {
		t.Errorf("the record %+v is not a merge-mode pass of the merge on its base through the pin", rec)
	}
	if strings.Contains(r.lab.refs(), "refs/leads/role-a/heads/merge/") {
		t.Errorf("a made-up lead ref is in the mirror")
	}
}

// A merge with no pin has nothing the sandbox can fetch: nothing runs and no
// record is made.
func TestAMergeWithNoPinIsNotRunAndLeavesNoRecord(t *testing.T) {
	r := newRig(t, execFeature)
	before := r.suite.count()
	v, err := r.o.Host.Verify(context.Background(), ownerchannel.VerifyInput{
		Project: "project-a", RequestID: "rq-nopin-01", Merge: r.scene.feature, Base: r.scene.target,
	})
	if err != nil || v.Clean || v.Detail != ownerchannel.VerifyCouldNotRun {
		t.Fatalf("Verify = %+v, %v", v, err)
	}
	if r.suite.count() != before {
		t.Errorf("the suite ran on a commit with no pin")
	}
	if all, err := r.o.Host.store.Verifications(r.scene.feature); err != nil || len(all) != 0 {
		t.Errorf("records %v, %v", all, err)
	}
}

// Only a merge-mode pass of this merge on this base counts for the adapter. A
// lead-mode record of the same commit never does, nor does a merge record made
// against another base, and an approval then is not applied, with a notice that
// names the real cause (no counting record), not a moved ref. The one record
// that counts lets the very merge that was verified, and no other, move the
// target.
func TestOnlyAMergeRecordOnTheReviewsBaseCountsForTheAdapter(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, execFeature)
	p := r.card()
	merge := p.Binding.Merge.Commit
	pin := r.pinOfMerge(merge)

	// A host over an empty records folder of its own, to which the records
	// are added one by one; the mirror is the rig's.
	var notices []string
	cfg := r.o.Host.cfg
	cfg.Notice = func(l string) { notices = append(notices, l) }
	h, err := New(cfg, openStore(t), r.o.Host.mailbox)
	if err != nil {
		t.Fatal(err)
	}
	gates := r.proj.Gates
	put := func(res verify.Result) {
		t.Helper()
		res.Tip, res.PlanDigest, res.Passed, res.Exit = merge, verify.PlanDigest(gates), true, 0
		res.Started, res.Duration = start, 0
		if _, err := h.store.PutVerification("project-a", res, gates); err != nil {
			t.Fatal(err)
		}
	}
	in := ownerchannel.ApplyInput{Project: "project-a", Effects: []record.Effect{{Kind: record.EffectRef, Ref: targetBranch, Old: r.scene.target, New: merge}}}
	refused := func(what string, counting int) {
		t.Helper()
		before := r.lab.refs()
		notices = nil
		got, err := h.Apply(ctx, in)
		if err != nil || got != door.OutcomeNotApplied {
			t.Fatalf("%s: Apply = %q, %v, want not-applied", what, got, err)
		}
		if r.lab.refs() != before {
			t.Errorf("%s: a ref changed", what)
		}
		if len(notices) != 1 || !strings.Contains(notices[0], "no verification record counts") || strings.Contains(notices[0], "moved") {
			t.Errorf("%s: notices %q do not name the missing record", what, notices)
		}
		if recs, err := h.suiteRecords("project-a", merge, r.scene.target); err != nil || len(recs) != counting {
			t.Errorf("%s: %d records count, %v, want %d", what, len(recs), err, counting)
		}
	}

	refused("no record", 0)
	// A lead-mode pass of the same commit id.
	put(verify.Result{TipRef: "refs/leads/role-a/heads/feature/x"})
	refused("a lead-mode record", 0)
	// A merge-mode pass on another base, which is the feature's tip here.
	put(verify.Result{TipRef: pin, Merge: true, Base: r.scene.feature})
	refused("a merge record with another base", 0)
	// The newest record that counts is a failure: not a pass.
	put(verify.Result{TipRef: pin, Merge: true, Base: r.scene.target})
	if _, err := h.store.PutVerification("project-a", verify.Result{
		Tip: merge, TipRef: pin, Merge: true, Base: r.scene.target, PlanDigest: verify.PlanDigest(gates), Exit: 1, Started: start,
	}, gates); err != nil {
		t.Fatal(err)
	}
	refused("a later failure", 2)

	// A pass after it counts, and the merge that was verified is the one
	// applied.
	put(verify.Result{TipRef: pin, Merge: true, Base: r.scene.target})
	recs, err := h.suiteRecords("project-a", merge, r.scene.target)
	if err != nil || len(recs) != 3 {
		t.Fatalf("records that count: %d, %v, want 3", len(recs), err)
	}
	if got, err := h.Apply(ctx, in); err != nil || got != door.OutcomeApplied {
		t.Fatalf("Apply = %q, %v, want applied", got, err)
	}
	if got := r.lab.ref(targetBranch); got != merge || got != r.suite.calls[0].Merge {
		t.Errorf("the target is at %s, want the merge %s that the suite was run on (%s)", got, merge, r.suite.calls[0].Merge)
	}
	if rec := recs[len(recs)-1].rec; rec.Tip != merge || !rec.Merge || rec.Base != r.scene.target {
		t.Errorf("the counting record %+v", rec)
	}
}

// Fresh lists only the records that count for the fresh review, so a record
// of another mode or base is no evidence and is not the newest.
func TestFreshCountsOnlyTheRecordsThatCountForTheReview(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	merge := p.Binding.Merge.Commit
	pin := r.pinOfMerge(merge)
	gates := r.proj.Gates
	// A later pass on another base, and a later lead-mode pass: neither is
	// the newest record for the review, and the bound one still is.
	for _, res := range []verify.Result{
		{TipRef: pin, Merge: true, Base: r.scene.feature},
		{TipRef: "refs/leads/role-a/heads/feature/x"},
	} {
		res.Tip, res.PlanDigest, res.Passed, res.Started = merge, verify.PlanDigest(gates), true, start.Add(1)
		if _, err := r.o.Host.store.PutVerification("project-a", res, gates); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := r.o.Host.Fresh(context.Background(), ownerchannel.FreshInput{Project: "project-a", Pending: p})
	if err != nil || len(fresh.Evidence) != 1 || !fresh.Evidence[0].Newest {
		t.Fatalf("Fresh = %+v, %v: the bound record must stay the newest that counts", fresh.Evidence, err)
	}
}
