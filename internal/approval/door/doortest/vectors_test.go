package doortest

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/record"
)

// The whole sequence, with the vectors as the records: the request is
// admitted, the plan writes the vectors' pending record byte for byte, the
// vectors' decision approves it, and it is used once and settled.
func TestVectorsThroughTheSequence(t *testing.T) {
	f := newFixture(t)
	request := door.RecordOf(vector(t, "request-valid.json"))
	want := vector(t, "pending-valid.json")
	var digests struct {
		Binding string `json:"binding_sha256"`
		Record  string `json:"record_sha256"`
	}
	if err := json.Unmarshal(vector(t, "pending-valid.digests.json"), &digests); err != nil {
		t.Fatal(err)
	}

	admission, err := f.d.Admit(f.ctx, request)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	if admission.RequestID != "rq-20261120t140201z-7c1e" || admission.RequestSHA256 != request.SHA256 {
		t.Errorf("admission = %+v", admission)
	}

	in := f.plan(request)
	in.Note = f.host.Note
	pending, err := f.d.Plan(f.ctx, in)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !bytes.Equal(pending.Bytes, want) {
		t.Fatalf("the pending record is not the vector:\n got %s\nwant %s", pending.Bytes, want)
	}
	if pending.SHA256 != digests.Record {
		t.Errorf("pending digest = %s, want %s", pending.SHA256, digests.Record)
	}
	const id = "ap-20261120-0007"
	if got := f.state(id); got != record.StatePending {
		t.Fatalf("state after plan = %s", got)
	}

	// The vectors' decision was made at 14:06:40 and ends at 14:36:40.
	f.clock.Set(time.Date(2026, 11, 20, 14, 6, 41, 0, time.UTC))
	decision := door.RecordOf(vector(t, "decision-valid.json"))
	decided, err := f.d.Decide(f.ctx, decision)
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	if decided.State != record.StateApproved || decided.DecisionSHA256 != decision.SHA256 {
		t.Errorf("decided = %+v", decided)
	}

	used, err := f.d.Use(f.ctx, door.UseInput{ID: id, Fresh: fresh(f.host)})
	if err != nil {
		t.Fatalf("use: %v", err)
	}
	if used.State != record.StateConsumed || used.BindingSHA256 != digests.Binding {
		t.Errorf("used = %+v", used)
	}
	if len(used.Effects) != 1 || used.Effects[0] != f.host.Binding.Effects[0] {
		t.Errorf("effects = %+v, want the binding's", used.Effects)
	}

	settled, err := f.d.Settle(f.ctx, door.SettleInput{ID: id, BindingSHA256: digests.Binding, Outcome: door.OutcomeApplied})
	if err != nil || settled.State != record.StateApplied {
		t.Fatalf("settle = %+v, %v", settled, err)
	}

	shown, err := f.d.Show(f.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shown.Pending.Bytes, want) || !bytes.Equal(shown.Decision.Bytes, decision.Bytes) {
		t.Error("show does not return the records as they travelled")
	}
	wantEntry := door.Entry{
		ID: id, RequestID: admission.RequestID, Verb: record.VerbFeatureFinish,
		State: record.StateApplied, ExpiresAt: "2026-11-20T14:36:40Z",
		BindingSHA256: digests.Binding, PendingSHA256: digests.Record, DecisionSHA256: decision.SHA256,
	}
	if shown.Entry != wantEntry {
		t.Errorf("entry = %+v\n want %+v", shown.Entry, wantEntry)
	}
	list, err := f.d.List(f.ctx)
	if err != nil || len(list) != 1 || list[0] != wantEntry {
		t.Errorf("list = %+v, %v", list, err)
	}
}

// The vectors' deny ends the record as denied, and a denied record is
// never used.
func TestVectorDeny(t *testing.T) {
	f := newFixture(t)
	p := f.pending("sample-feature")
	if p.ID != "ap-20261120-0007" || p.BindingSHA256 != f.host.BindingSHA256 {
		t.Fatalf("the fixture's first record is not the vectors': %s %s", p.ID, p.BindingSHA256)
	}
	f.clock.Set(time.Date(2026, 11, 20, 14, 6, 41, 0, time.UTC))
	decided, err := f.d.Decide(f.ctx, door.RecordOf(vector(t, "decision-valid-deny.json")))
	if err != nil || decided.State != record.StateDenied {
		t.Fatalf("decide = %+v, %v", decided, err)
	}
	_, err = f.d.Use(f.ctx, door.UseInput{ID: p.ID, Fresh: fresh(p)})
	wantCode(t, err, record.CodeApprovalVoid)
	if got := f.state(p.ID); got != record.StateDenied {
		t.Errorf("state = %s, want denied", got)
	}
}

// A prune is planned from the deletes the host gives, and its binding is
// the vectors' prune binding.
func TestVectorPrune(t *testing.T) {
	f := newFixture(t)
	want, err := record.DecodePending(vector(t, "pending-valid-prune.json"))
	if err != nil {
		t.Fatal(err)
	}
	request := door.RecordOf(vector(t, "request-valid-prune.json"))
	if _, err := f.d.Admit(f.ctx, request); err != nil {
		t.Fatalf("admit: %v", err)
	}
	rec, err := f.d.Plan(f.ctx, door.PlanInput{Request: request, Deletes: want.Binding.Effects})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	p, err := record.DecodePending(rec.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if p.BindingSHA256 != want.BindingSHA256 {
		t.Errorf("binding digest = %s, want the vector's %s", p.BindingSHA256, want.BindingSHA256)
	}
	if p.PlanText != want.PlanText || p.RequestSHA256 != request.SHA256 {
		t.Errorf("plan text %q, request digest %s", p.PlanText, p.RequestSHA256)
	}

	// A prune has no flagged path and no review: an approve signs off
	// nothing, and a use gives the refs and no digest.
	if _, err := f.d.Decide(f.ctx, f.decision(p, true, nil)); err != nil {
		t.Fatalf("decide: %v", err)
	}
	used, err := f.d.Use(f.ctx, door.UseInput{ID: p.ID, Fresh: fresh(p)})
	if err != nil {
		t.Fatalf("use: %v", err)
	}
	if len(used.Effects) != 1 || used.Effects[0].MergedInto == nil || *used.Effects[0].MergedInto != *want.Binding.Effects[0].MergedInto {
		t.Errorf("effects = %+v", used.Effects)
	}
	// The effects returned are a copy: changing them changes nothing kept.
	used.Effects[0].MergedInto.Ref = "refs/heads/elsewhere"
	shown, _ := f.d.Show(f.ctx, p.ID)
	if !bytes.Equal(shown.Pending.Bytes, rec.Bytes) {
		t.Error("the kept record changed")
	}
}

// Every invalid request of the vectors is refused at Admit, and every
// invalid decision at Decide, with the code the vectors give. Nothing is
// kept of any of them.
func TestInvalidVectorsAreRefused(t *testing.T) {
	requests, decisions := 0, 0
	for name, code := range invalidVectors(t) {
		f := newFixture(t)
		var err error
		switch {
		case strings.HasPrefix(name, "request-"):
			requests++
			_, err = f.d.Admit(f.ctx, door.RecordOf(vector(t, name)))
		case strings.HasPrefix(name, "decision-"):
			decisions++
			// The vectors' pending record is there, so that only the
			// decision's own form can be what is refused.
			f.pending("sample-feature")
			_, err = f.d.Decide(f.ctx, door.RecordOf(vector(t, name)))
		default:
			continue
		}
		t.Run(name, func(t *testing.T) { wantCode(t, err, code) })
		list, _ := f.d.List(f.ctx)
		for _, e := range list {
			if e.State != record.StatePending || e.DecisionSHA256 != "" {
				t.Errorf("%s: left a record %+v", name, e)
			}
		}
	}
	if requests < 5 || decisions < 5 {
		t.Errorf("ran %d request and %d decision vectors; the vectors were not found", requests, decisions)
	}
}
