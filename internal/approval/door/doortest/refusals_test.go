package doortest

import (
	"sort"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/record"
)

// refusal is one row of the table: a call the stand-in refuses, the code
// it refuses with, and the state the record is in afterwards.
type refusal struct {
	verb string
	name string
	code record.Code
	// state is the state of the record after the refusal, or empty when
	// the row has no record to look at.
	state record.State
	// run sets the case up and returns the refusal with the id of the
	// record it was about, if any.
	run func(f *fixture) (id string, err error)
}

// use uses p with fresh values a test has changed.
func (f *fixture) use(p record.Pending, change func(*door.Fresh)) (string, error) {
	in := door.UseInput{ID: p.ID, Fresh: fresh(p)}
	if change != nil {
		change(&in.Fresh)
	}
	_, err := f.d.Use(f.ctx, in)
	return p.ID, err
}

// decide decides p with a decision a test has changed.
func (f *fixture) decide(p record.Pending, approve bool, change func(*record.Decision)) (string, error) {
	_, err := f.d.Decide(f.ctx, f.decision(p, approve, change))
	return p.ID, err
}

// planWith admits a request and plans it with findings a test has changed.
func (f *fixture) planWith(change func(*door.PlanInput)) (string, error) {
	in := f.plan(f.admit("sample-feature"))
	change(&in)
	_, err := f.d.Plan(f.ctx, in)
	return "", err
}

// refusals is every refusal of the stand-in with its code.
var refusals = []refusal{
	// Admit.
	{"admit", "bytes that do not have their digest", record.CodeRequestInvalid, "", func(f *fixture) (string, error) {
		req := f.request("sample-feature", nil)
		req.SHA256 = record.Digest([]byte("other"))
		_, err := f.d.Admit(f.ctx, req)
		return "", err
	}},
	{"admit", "bytes that are no record", record.CodeRequestInvalid, "", func(f *fixture) (string, error) {
		_, err := f.d.Admit(f.ctx, door.RecordOf([]byte("approve it")))
		return "", err
	}},
	{"admit", "a request over its cap", record.CodeRecordTooLarge, "", func(f *fixture) (string, error) {
		_, err := f.d.Admit(f.ctx, door.RecordOf(vector(f.t, "request-invalid-too-large.json")))
		return "", err
	}},
	{"admit", "a verb that cannot be asked for", record.CodeVerbNotRequestable, "", func(f *fixture) (string, error) {
		_, err := f.d.Admit(f.ctx, door.RecordOf(vector(f.t, "request-invalid-verb.json")))
		return "", err
	}},
	{"admit", "a feature name outside the grammar", record.CodeFeatureName, "", func(f *fixture) (string, error) {
		_, err := f.d.Admit(f.ctx, door.RecordOf(vector(f.t, "request-invalid-feature-name.json")))
		return "", err
	}},
	{"admit", "a role with no assignment", record.CodeNotAssigned, "", func(f *fixture) (string, error) {
		f.d.SetAssigned(false)
		_, err := f.d.Admit(f.ctx, f.request("sample-feature", nil))
		return "", err
	}},
	{"admit", "an epoch before the seat's", record.CodePreviousEpoch, "", func(f *fixture) (string, error) {
		_, err := f.d.Admit(f.ctx, f.request("sample-feature", func(r *record.Request) { r.Epoch = 6 }))
		return "", err
	}},
	{"admit", "an epoch after the seat's", record.CodePreviousEpoch, "", func(f *fixture) (string, error) {
		_, err := f.d.Admit(f.ctx, f.request("sample-feature", func(r *record.Request) { r.Epoch = 8 }))
		return "", err
	}},
	{"admit", "a request id used before", record.CodeRequestInvalid, "", func(f *fixture) (string, error) {
		req := f.admit("sample-feature")
		if err := f.d.Withdraw("rq-test-ab"); err != nil {
			f.t.Fatal(err)
		}
		_, err := f.d.Admit(f.ctx, req)
		return "", err
	}},
	{"admit", "a second live request for one verb and target", record.CodeRequestDuplicate, "", func(f *fixture) (string, error) {
		f.admit("sample-feature")
		_, err := f.d.Admit(f.ctx, f.request("sample-feature", nil))
		return "", err
	}},
	{"admit", "a second request while the first is pending", record.CodeRequestDuplicate, record.StatePending, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		_, err := f.d.Admit(f.ctx, f.request("sample-feature", nil))
		return p.ID, err
	}},
	{"admit", "a second request while the first's outcome is unknown", record.CodeRequestDuplicate, record.StateUnknown, func(f *fixture) (string, error) {
		p := f.consumed("sample-feature")
		if _, err := f.settle(p, door.OutcomeUnknown); err != nil {
			f.t.Fatal(err)
		}
		_, err := f.d.Admit(f.ctx, f.request("sample-feature", nil))
		return p.ID, err
	}},
	{"admit", "a fourth request for one verb and target in an hour", record.CodeRequestThrottled, "", func(f *fixture) (string, error) {
		for i := 0; i < RepeatLimit; i++ {
			f.admit("sample-feature")
			f.endLast()
			f.clock.Advance(5 * time.Minute)
		}
		_, err := f.d.Admit(f.ctx, f.request("sample-feature", nil))
		return "", err
	}},
	{"admit", "a seventh request in one minute", record.CodeRequestRate, "", func(f *fixture) (string, error) {
		for i := 0; i < RequestsPerMinute; i++ {
			f.admit("feature-" + string(rune('a'+i)))
			f.endLast()
			f.clock.Advance(9 * time.Second)
		}
		_, err := f.d.Admit(f.ctx, f.request("feature-z", nil))
		return "", err
	}},
	{"admit", "a ninth request not yet final", record.CodeRequestPendingCap, "", func(f *fixture) (string, error) {
		for i := 0; i < MaxNotFinal; i++ {
			f.admit("feature-" + string(rune('a'+i)))
			f.clock.Advance(15 * time.Second)
		}
		_, err := f.d.Admit(f.ctx, f.request("feature-z", nil))
		return "", err
	}},

	// Plan.
	{"plan", "a request that was not admitted", record.CodeRequestInvalid, "", func(f *fixture) (string, error) {
		_, err := f.d.Plan(f.ctx, f.plan(f.request("sample-feature", nil)))
		return "", err
	}},
	{"plan", "bytes that do not have their digest", record.CodeRequestInvalid, "", func(f *fixture) (string, error) {
		return f.planWith(func(in *door.PlanInput) { in.Request.SHA256 = record.Digest(nil) })
	}},
	{"plan", "a request that has a pending record", record.CodeRequestInvalid, record.StatePending, func(f *fixture) (string, error) {
		req := f.admit("sample-feature")
		rec, err := f.d.Plan(f.ctx, f.plan(req))
		if err != nil {
			f.t.Fatal(err)
		}
		p, _ := record.DecodePending(rec.Bytes)
		_, err = f.d.Plan(f.ctx, f.plan(req))
		return p.ID, err
	}},
	{"plan", "a request that was withdrawn", record.CodeRequestInvalid, "", func(f *fixture) (string, error) {
		req := f.admit("sample-feature")
		f.endLast()
		_, err := f.d.Plan(f.ctx, f.plan(req))
		return "", err
	}},
	{"plan", "a role that lost its assignment", record.CodeNotAssigned, "", func(f *fixture) (string, error) {
		return f.planWith(func(*door.PlanInput) { f.d.SetAssigned(false) })
	}},
	{"plan", "an epoch that ended after the admit", record.CodePreviousEpoch, "", func(f *fixture) (string, error) {
		return f.planWith(func(*door.PlanInput) { f.d.SetEpoch(8) })
	}},
	{"plan", "a feature tip other than the request's", record.CodeFeatureMoved, "", func(f *fixture) (string, error) {
		return f.planWith(func(in *door.PlanInput) {
			in.Feature = otherID
			in.MergeParents = []string{baseID, otherID}
		})
	}},
	{"plan", "a merge whose second parent is not the feature", record.CodeBindingMismatch, "", func(f *fixture) (string, error) {
		return f.planWith(func(in *door.PlanInput) { in.MergeParents = []string{baseID, otherID} })
	}},
	{"plan", "a merge whose first parent is not the target's tip", record.CodeBindingMismatch, "", func(f *fixture) (string, error) {
		return f.planWith(func(in *door.PlanInput) { in.MergeParents = []string{otherID, featureID} })
	}},
	{"plan", "a merge with its parents the other way round", record.CodeBindingMismatch, "", func(f *fixture) (string, error) {
		return f.planWith(func(in *door.PlanInput) { in.MergeParents = []string{featureID, baseID} })
	}},
	{"plan", "a merge with three parents", record.CodeBindingMismatch, "", func(f *fixture) (string, error) {
		return f.planWith(func(in *door.PlanInput) { in.MergeParents = []string{baseID, featureID, otherID} })
	}},
	{"plan", "a merge with one parent", record.CodeBindingMismatch, "", func(f *fixture) (string, error) {
		return f.planWith(func(in *door.PlanInput) { in.MergeParents = []string{baseID} })
	}},
	{"plan", "a target that moved", record.CodeBindingMismatch, "", func(f *fixture) (string, error) {
		return f.planWith(func(in *door.PlanInput) { in.TargetNow = otherID })
	}},
	{"plan", "a summary of other commits", record.CodeRecordInvalid, "", func(f *fixture) (string, error) {
		return f.planWith(func(in *door.PlanInput) { in.Summary.Base = otherID })
	}},
	{"plan", "a target that is not a branch", record.CodeRecordInvalid, "", func(f *fixture) (string, error) {
		return f.planWith(func(in *door.PlanInput) { in.TargetRef = "refs/tags/v1" })
	}},
	{"plan", "no suite record", record.CodeRecordInvalid, "", func(f *fixture) (string, error) {
		return f.planWith(func(in *door.PlanInput) { in.Evidence = nil })
	}},
	{"plan", "a note over its cap", record.CodeRecordTooLarge, "", func(f *fixture) (string, error) {
		return f.planWith(func(in *door.PlanInput) { in.Note = record.Text(strings.Repeat("n", record.MaxNoteBytes+1)) })
	}},

	// Decide.
	{"decide", "bytes that do not have their digest", record.CodeDecisionInvalid, record.StatePending, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		dec := f.decision(p, true, nil)
		dec.SHA256 = record.Digest(nil)
		_, err := f.d.Decide(f.ctx, dec)
		return p.ID, err
	}},
	{"decide", "a field the form does not name", record.CodeDecisionInvalid, record.StatePending, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		_, err := f.d.Decide(f.ctx, door.RecordOf(vector(f.t, "decision-invalid-unknown-field.json")))
		return p.ID, err
	}},
	{"decide", "a decision over its cap", record.CodeRecordTooLarge, record.StatePending, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		_, err := f.d.Decide(f.ctx, door.RecordOf(vector(f.t, "decision-invalid-too-large.json")))
		return p.ID, err
	}},
	{"decide", "an id with no record", record.CodeDecisionInvalid, record.StatePending, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		_, err := f.decide(p, true, func(d *record.Decision) { d.ID = "ap-20261120-0008" })
		return p.ID, err
	}},
	{"decide", "another binding digest", record.CodeBindingMismatch, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) { d.BindingSHA256 = record.Digest(nil) })
	}},
	{"decide", "the binding digest of another record", record.CodeBindingMismatch, record.StatePending, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		f.clock.Advance(time.Second)
		other := f.pending("other-feature")
		return f.decide(p, true, func(d *record.Decision) { d.BindingSHA256 = other.BindingSHA256 })
	}},
	{"decide", "another planner", record.CodeBindingMismatch, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) { d.Planner = "planner-b" })
	}},
	{"decide", "another project", record.CodeBindingMismatch, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) { d.Project = "project-b" })
	}},
	{"decide", "another role", record.CodeBindingMismatch, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) { d.Subject.RoleName = "role-b" })
	}},
	{"decide", "another epoch in the decision", record.CodeBindingMismatch, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) { d.Subject.Epoch = 6 })
	}},
	{"decide", "another seat revision in the decision", record.CodeBindingMismatch, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) { d.Subject.SeatRev = 11 })
	}},
	{"decide", "another plan token", record.CodeBindingMismatch, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) { d.PlanToken = "h-00000000" })
	}},
	{"decide", "a seat whose epoch ended", record.CodePreviousEpoch, record.StateVoid, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		f.d.SetEpoch(8)
		return f.decide(p, true, nil)
	}},
	{"decide", "a seat whose record was written", record.CodeApprovalVoid, record.StateVoid, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		f.d.SetSeatRev(13)
		return f.decide(p, true, nil)
	}},
	{"decide", "a pending record past its time", record.CodeApprovalExpired, record.StateExpired, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		f.clock.Advance(PendingLife)
		return f.decide(p, true, nil)
	}},
	{"decide", "a deny past the record's time", record.CodeApprovalExpired, record.StateExpired, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		f.clock.Advance(PendingLife + time.Hour)
		return f.decide(p, false, nil)
	}},
	{"decide", "a decision that ends before it was made", record.CodeDecisionInvalid, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) { d.ExpiresAt = d.DecidedAt })
	}},
	{"decide", "an approve that lasts longer than the approval life", record.CodeDecisionInvalid, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) {
			d.ExpiresAt = record.FormatTime(f.clock.Now().Add(ApprovalLife + time.Second))
		})
	}},
	{"decide", "a decision made in the future", record.CodeDecisionInvalid, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), false, func(d *record.Decision) {
			d.DecidedAt = record.FormatTime(f.clock.Now().Add(time.Hour))
			d.ExpiresAt = record.FormatTime(f.clock.Now().Add(time.Hour + ApprovalLife))
		})
	}},
	{"decide", "a decision made before the request", record.CodeDecisionInvalid, record.StatePending, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		f.clock.Advance(10 * time.Minute)
		return f.decide(p, false, func(d *record.Decision) {
			d.DecidedAt = record.FormatTime(f.clock.Now().Add(-time.Hour))
			d.ExpiresAt = record.FormatTime(f.clock.Now().Add(ApprovalLife))
		})
	}},
	{"decide", "a decision that arrives after its own end", record.CodeApprovalExpired, record.StatePending, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		dec := f.decision(p, true, nil)
		f.clock.Advance(30 * time.Minute)
		_, err := f.d.Decide(f.ctx, dec)
		return p.ID, err
	}},
	{"decide", "an approve with a sign-off missing", record.CodeDecisionInvalid, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) { d.SignedOff = d.SignedOff[:1] })
	}},
	{"decide", "an approve with no sign-off", record.CodeDecisionInvalid, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) { d.SignedOff = []record.SignOff{} })
	}},
	{"decide", "an approve with a sign-off too many", record.CodeDecisionInvalid, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) {
			d.SignedOff = append(d.SignedOff, record.SignOff{Kind: record.FlagExecConfig, Path: "lib/seat_read.py"})
		})
	}},
	{"decide", "an approve whose sign-offs are in another order", record.CodeDecisionInvalid, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) {
			d.SignedOff[0], d.SignedOff[1] = d.SignedOff[1], d.SignedOff[0]
		})
	}},
	{"decide", "an approve that signs a path off as another kind", record.CodeDecisionInvalid, record.StatePending, func(f *fixture) (string, error) {
		return f.decide(f.pending("sample-feature"), true, func(d *record.Decision) { d.SignedOff[0].Kind = record.FlagSubmodule })
	}},
	{"decide", "another binding digest after the record was decided", record.CodeBindingMismatch, record.StateApproved, func(f *fixture) (string, error) {
		p := f.approved("sample-feature")
		return f.decide(p, true, func(d *record.Decision) { d.BindingSHA256 = record.Digest([]byte("other binding")) })
	}},
	{"decide", "a record that was withdrawn", record.CodeApprovalVoid, record.StateWithdrawn, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		if err := f.d.Withdraw(p.ID); err != nil {
			f.t.Fatal(err)
		}
		return f.decide(p, true, nil)
	}},
	{"decide", "a record that was voided", record.CodeApprovalVoid, record.StateVoid, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		if err := f.d.Void(p.ID); err != nil {
			f.t.Fatal(err)
		}
		return f.decide(p, false, nil)
	}},

	// Use.
	{"use", "an id with no record", record.CodeApprovalVoid, "", func(f *fixture) (string, error) {
		_, err := f.d.Use(f.ctx, door.UseInput{ID: "ap-20261120-0007"})
		return "", err
	}},
	{"use", "a record with no decision", record.CodeApprovalVoid, record.StatePending, func(f *fixture) (string, error) {
		return f.use(f.pending("sample-feature"), nil)
	}},
	{"use", "a record that was denied", record.CodeApprovalVoid, record.StateDenied, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		if _, err := f.decide(p, false, nil); err != nil {
			f.t.Fatal(err)
		}
		return f.use(p, nil)
	}},
	{"use", "a second use", record.CodeApprovalConsumed, record.StateConsumed, func(f *fixture) (string, error) {
		return f.use(f.consumed("sample-feature"), nil)
	}},
	{"use", "a use after it was applied", record.CodeApprovalConsumed, record.StateApplied, func(f *fixture) (string, error) {
		p := f.consumed("sample-feature")
		if _, err := f.settle(p, door.OutcomeApplied); err != nil {
			f.t.Fatal(err)
		}
		return f.use(p, nil)
	}},
	{"use", "a use after it was not applied", record.CodeApprovalConsumed, record.StateNotApplied, func(f *fixture) (string, error) {
		p := f.consumed("sample-feature")
		if _, err := f.settle(p, door.OutcomeNotApplied); err != nil {
			f.t.Fatal(err)
		}
		return f.use(p, nil)
	}},
	{"use", "a use while the outcome is unknown", record.CodeApprovalConsumed, record.StateUnknown, func(f *fixture) (string, error) {
		p := f.consumed("sample-feature")
		if _, err := f.settle(p, door.OutcomeUnknown); err != nil {
			f.t.Fatal(err)
		}
		return f.use(p, nil)
	}},
	{"use", "a target that moved", record.CodeBindingMismatch, record.StateVoid, func(f *fixture) (string, error) {
		return f.use(f.approved("sample-feature"), func(fr *door.Fresh) { fr.Refs[0].Value = otherID })
	}},
	{"use", "a target that is not among the fresh refs", record.CodeBindingMismatch, record.StateVoid, func(f *fixture) (string, error) {
		return f.use(f.approved("sample-feature"), func(fr *door.Fresh) { fr.Refs[0].Ref = "refs/heads/master" })
	}},
	{"use", "a target given twice, once moved", record.CodeBindingMismatch, record.StateVoid, func(f *fixture) (string, error) {
		return f.use(f.approved("sample-feature"), func(fr *door.Fresh) {
			fr.Refs = append(fr.Refs, door.RefValue{Ref: targetRef, Value: otherID})
		})
	}},
	{"use", "no fresh value at all", record.CodeBindingMismatch, record.StateVoid, func(f *fixture) (string, error) {
		return f.use(f.approved("sample-feature"), func(fr *door.Fresh) { *fr = door.Fresh{} })
	}},
	{"use", "another review digest", record.CodeBindingMismatch, record.StateVoid, func(f *fixture) (string, error) {
		return f.use(f.approved("sample-feature"), func(fr *door.Fresh) { fr.ReviewDigest = record.Digest(nil) })
	}},
	{"use", "another rule set", record.CodeBindingMismatch, record.StateVoid, func(f *fixture) (string, error) {
		return f.use(f.approved("sample-feature"), func(fr *door.Fresh) { fr.RuleSetSHA256 = record.Digest(nil) })
	}},
	{"use", "a use after one that did not match", record.CodeApprovalVoid, record.StateVoid, func(f *fixture) (string, error) {
		p := f.approved("sample-feature")
		if _, err := f.use(p, func(fr *door.Fresh) { fr.Refs[0].Value = otherID }); err == nil {
			f.t.Fatal("the mismatch was not refused")
		}
		return f.use(p, nil)
	}},
	{"use", "an approval past its time", record.CodeApprovalExpired, record.StateExpired, func(f *fixture) (string, error) {
		p := f.approved("sample-feature")
		f.clock.Advance(30 * time.Minute)
		return f.use(p, nil)
	}},
	{"use", "an evidence record that changed", record.CodeEvidenceChanged, record.StateVoid, func(f *fixture) (string, error) {
		return f.use(f.approved("sample-feature"), func(fr *door.Fresh) { fr.Evidence[0].RecordSHA256 = record.Digest(nil) })
	}},
	{"use", "an evidence record that is no longer the newest", record.CodeEvidenceChanged, record.StateVoid, func(f *fixture) (string, error) {
		return f.use(f.approved("sample-feature"), func(fr *door.Fresh) { fr.Evidence[0].Newest = false })
	}},
	{"use", "an evidence record that is gone", record.CodeEvidenceChanged, record.StateVoid, func(f *fixture) (string, error) {
		return f.use(f.approved("sample-feature"), func(fr *door.Fresh) { fr.Evidence = nil })
	}},
	{"use", "an evidence record given twice, once changed", record.CodeEvidenceChanged, record.StateVoid, func(f *fixture) (string, error) {
		return f.use(f.approved("sample-feature"), func(fr *door.Fresh) {
			changed := fr.Evidence[0]
			changed.Newest = false
			fr.Evidence = append(fr.Evidence, changed)
		})
	}},
	{"use", "an approval that was voided", record.CodeApprovalVoid, record.StateVoid, func(f *fixture) (string, error) {
		p := f.approved("sample-feature")
		if err := f.d.Void(p.ID); err != nil {
			f.t.Fatal(err)
		}
		return f.use(p, nil)
	}},
	{"use", "an approval the lead withdrew", record.CodeApprovalVoid, record.StateWithdrawn, func(f *fixture) (string, error) {
		p := f.approved("sample-feature")
		if err := f.d.Withdraw(p.ID); err != nil {
			f.t.Fatal(err)
		}
		return f.use(p, nil)
	}},
	{"use", "a seat whose epoch ended", record.CodePreviousEpoch, record.StateVoid, func(f *fixture) (string, error) {
		p := f.approved("sample-feature")
		f.d.SetEpoch(8)
		return f.use(p, nil)
	}},
	{"use", "a seat whose record was written", record.CodeApprovalVoid, record.StateVoid, func(f *fixture) (string, error) {
		p := f.approved("sample-feature")
		f.d.SetSeatRev(13)
		return f.use(p, nil)
	}},

	// Settle.
	{"settle", "a word that is no outcome", record.CodeRecordInvalid, record.StateConsumed, func(f *fixture) (string, error) {
		p := f.consumed("sample-feature")
		_, err := f.settle(p, door.Outcome(record.StateApproved))
		return p.ID, err
	}},
	{"settle", "an id with no record", record.CodeApprovalVoid, "", func(f *fixture) (string, error) {
		_, err := f.d.Settle(f.ctx, door.SettleInput{ID: "ap-20261120-0007", BindingSHA256: record.Digest(nil), Outcome: door.OutcomeApplied})
		return "", err
	}},
	{"settle", "another binding digest", record.CodeBindingMismatch, record.StateConsumed, func(f *fixture) (string, error) {
		p := f.consumed("sample-feature")
		_, err := f.d.Settle(f.ctx, door.SettleInput{ID: p.ID, BindingSHA256: record.Digest(nil), Outcome: door.OutcomeApplied})
		return p.ID, err
	}},
	{"settle", "another binding digest after the record was settled", record.CodeBindingMismatch, record.StateApplied, func(f *fixture) (string, error) {
		p := f.consumed("sample-feature")
		if _, err := f.settle(p, door.OutcomeApplied); err != nil {
			f.t.Fatal(err)
		}
		_, err := f.d.Settle(f.ctx, door.SettleInput{ID: p.ID, BindingSHA256: record.Digest(nil), Outcome: door.OutcomeApplied})
		return p.ID, err
	}},
	{"settle", "an approval that was not used", record.CodeApprovalVoid, record.StateApproved, func(f *fixture) (string, error) {
		p := f.approved("sample-feature")
		_, err := f.settle(p, door.OutcomeApplied)
		return p.ID, err
	}},
	{"settle", "a record with no decision", record.CodeApprovalVoid, record.StatePending, func(f *fixture) (string, error) {
		p := f.pending("sample-feature")
		_, err := f.settle(p, door.OutcomeNotApplied)
		return p.ID, err
	}},
	{"settle", "a record that expired unused", record.CodeApprovalExpired, record.StateExpired, func(f *fixture) (string, error) {
		p := f.approved("sample-feature")
		f.clock.Advance(time.Hour)
		_, err := f.settle(p, door.OutcomeApplied)
		return p.ID, err
	}},
	{"settle", "not applied after applied", record.CodeApprovalConsumed, record.StateApplied, func(f *fixture) (string, error) {
		p := f.consumed("sample-feature")
		if _, err := f.settle(p, door.OutcomeApplied); err != nil {
			f.t.Fatal(err)
		}
		_, err := f.settle(p, door.OutcomeNotApplied)
		return p.ID, err
	}},
	{"settle", "applied after not applied", record.CodeApprovalConsumed, record.StateNotApplied, func(f *fixture) (string, error) {
		p := f.consumed("sample-feature")
		if _, err := f.settle(p, door.OutcomeNotApplied); err != nil {
			f.t.Fatal(err)
		}
		_, err := f.settle(p, door.OutcomeApplied)
		return p.ID, err
	}},
	{"settle", "unknown after applied", record.CodeApprovalConsumed, record.StateApplied, func(f *fixture) (string, error) {
		p := f.consumed("sample-feature")
		if _, err := f.settle(p, door.OutcomeApplied); err != nil {
			f.t.Fatal(err)
		}
		_, err := f.settle(p, door.OutcomeUnknown)
		return p.ID, err
	}},

	// Show.
	{"show", "an id with no record", record.CodeApprovalVoid, "", func(f *fixture) (string, error) {
		_, err := f.d.Show(f.ctx, "ap-20261120-0007")
		return "", err
	}},
	{"show", "a request id", record.CodeApprovalVoid, "", func(f *fixture) (string, error) {
		f.pending("sample-feature")
		_, err := f.d.Show(f.ctx, "rq-test-ab")
		return "", err
	}},
}

// endLast withdraws the request the fixture made last, so that the next
// one for the same target is not a duplicate.
func (f *fixture) endLast() {
	f.t.Helper()
	id := "rq-test-" + string(rune('a'+f.n/26)) + string(rune('a'+f.n%26))
	if err := f.d.Withdraw(id); err != nil {
		f.t.Fatal(err)
	}
}

func TestRefusals(t *testing.T) {
	for _, c := range refusals {
		t.Run(c.verb+"/"+c.name, func(t *testing.T) {
			f := newFixture(t)
			id, err := c.run(f)
			wantCode(t, err, c.code)
			if (id != "") != (c.state != "") {
				t.Fatalf("the row names the record %q and the state %q; it needs both or neither", id, c.state)
			}
			if id == "" {
				return
			}
			if got := f.state(id); got != c.state {
				t.Errorf("state after the refusal = %s, want %s", got, c.state)
			}
		})
	}
}

// The table holds every code the stand-in answers with, and the stand-in
// answers with no code the table lacks: each code a verb's comment names
// is here.
func TestRefusalTableHoldsEveryCode(t *testing.T) {
	want := []record.Code{
		record.CodeApprovalConsumed, record.CodeApprovalExpired, record.CodeApprovalVoid,
		record.CodeBindingMismatch, record.CodeDecisionInvalid, record.CodeEvidenceChanged,
		record.CodeFeatureMoved, record.CodeFeatureName, record.CodeNotAssigned,
		record.CodePreviousEpoch, record.CodeRecordInvalid, record.CodeRecordTooLarge,
		record.CodeRequestDuplicate, record.CodeRequestInvalid, record.CodeRequestPendingCap,
		record.CodeRequestRate, record.CodeRequestThrottled, record.CodeVerbNotRequestable,
	}
	seen := map[record.Code]bool{}
	for _, c := range refusals {
		seen[c.code] = true
	}
	var got []string
	for code := range seen {
		got = append(got, string(code))
	}
	sort.Strings(got)
	var wanted []string
	for _, code := range want {
		wanted = append(wanted, string(code))
	}
	sort.Strings(wanted)
	if strings.Join(got, " ") != strings.Join(wanted, " ") {
		t.Errorf("the table's codes are\n %v\nwant\n %v", got, wanted)
	}
}

// A settle of an approval that was never used says so, and does not say
// the record has no approval.
func TestSettleOfAnUnusedApprovalSaysItWasNotUsed(t *testing.T) {
	f := newFixture(t)
	p := f.approved("sample-feature")
	_, err := f.settle(p, door.OutcomeApplied)
	wantCode(t, err, record.CodeApprovalVoid)
	if got := err.(*record.Error).Detail; !strings.Contains(got, "was not used") {
		t.Errorf("detail = %q", got)
	}
}
