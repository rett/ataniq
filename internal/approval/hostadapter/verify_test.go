//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/record"
	"ataniq/internal/intake/verify"
)

// A suite that does not end clean gives no card: the request is refused
// verification-failed with the word of the closed list, the pin is taken
// away, the request is ended at the door, and the lead gets a reply file. A
// run that was made is kept as a record, a failed one too.
func TestAVerificationThatIsNotCleanRefusesTheRequest(t *testing.T) {
	cases := []struct {
		name   string
		res    SuiteResult
		err    error
		word   string
		record bool
	}{
		{"a failed gate", SuiteResult{Exit: 1, Output: []byte("FAIL\n")}, nil, ownerchannel.VerifyFailed, true},
		{"a timeout", SuiteResult{Exit: -1, Stopped: verify.StoppedTimeout}, nil, ownerchannel.VerifyStopped, true},
		{"an error stop", SuiteResult{Exit: -1, Stopped: verify.StoppedError}, nil, ownerchannel.VerifyStopped, true},
		{"output cut with a pass", SuiteResult{Passed: true, Exit: 0, OutputCut: true}, nil, ownerchannel.VerifyOutputCut, true},
		{"the output limit", SuiteResult{Exit: -1, Stopped: verify.StoppedOutputLimit, OutputCut: true}, nil, ownerchannel.VerifyOutputCut, true},
		{"a pass that did not exit 0", SuiteResult{Passed: true, Exit: 3}, nil, ownerchannel.VerifyFailed, true},
		{"a pass that was stopped", SuiteResult{Passed: true, Exit: 0, Stopped: verify.StoppedTimeout}, nil, ownerchannel.VerifyStopped, true},
		{"a suite that could not run", SuiteResult{}, errors.New("no sandbox"), ownerchannel.VerifyCouldNotRun, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newRig(t, execFeature)
			before := r.lab.refs()
			c.res.Started, c.res.Duration = start, time.Second
			r.suite.set(c.res, c.err)
			id := r.ask()
			st, ok := r.o.Service.Status(id)
			if !ok || st.State != ownerchannel.RequestRefused || st.Code != record.CodeVerificationFailed ||
				!strings.HasSuffix(st.Detail, ": "+c.word) {
				t.Fatalf("status %+v, want refused verification-failed with %q", st, c.word)
			}
			if st.PendingID != "" {
				t.Errorf("a card was made: %+v", st)
			}
			if got := r.lab.refs(); got != before {
				t.Errorf("refs after the refusal:\n%s\nwant\n%s", got, before)
			}
			if list, err := r.stand.List(context.Background()); err != nil || len(list) != 0 {
				t.Errorf("the door lists %d records: %v", len(list), err)
			}
			rp := r.readReply(id)
			if rp.Code != string(record.CodeVerificationFailed) || rp.State != "" || !strings.Contains(rp.Line, c.word) {
				t.Errorf("reply %+v", rp)
			}
			merge := r.suiteMerge()
			recs, err := r.o.Host.suiteRecords("project-a", merge)
			if err != nil {
				t.Fatal(err)
			}
			if c.record && len(recs) != 1 {
				t.Errorf("records %+v", recs)
			}
			if !c.record && len(recs) != 0 {
				t.Errorf("a suite that could not run left %d records", len(recs))
			}
			if c.record && len(recs) == 1 {
				// A record is never a clean pass unless the run was one.
				if rec := recs[0].rec; rec.Passed && rec.Exit == 0 && rec.Stopped == "" && !rec.OutputCut {
					t.Errorf("a clean pass was recorded for %s", c.name)
				}
			}
		})
	}
}

// suiteMerge is the merge commit the suite was last asked about.
func (r *rig) suiteMerge() string {
	r.suite.mu.Lock()
	defer r.suite.mu.Unlock()
	if len(r.suite.calls) == 0 {
		r.t.Fatal("the suite was not run")
	}
	return r.suite.calls[len(r.suite.calls)-1].Merge
}

// The evidence a clean run gives is the record of that run: the id and the
// digest change with the record, so a changed record is not the one bound.
func TestTheEvidenceIsTheRecordOfTheRunAndChangesWithIt(t *testing.T) {
	r := newRig(t, execFeature)
	p := r.card()
	merge := p.Binding.Merge.Commit
	ev := p.Binding.Evidence[0]
	recs, err := r.o.Host.suiteRecords("project-a", merge)
	if err != nil || len(recs) != 1 {
		t.Fatalf("records %v, %v", recs, err)
	}
	if ev.RecordID != recs[0].id || ev.RecordSHA256 != recs[0].sha || !strings.HasPrefix(ev.RecordID, "suite-") || len(ev.RecordSHA256) != 64 {
		t.Errorf("evidence %+v for the record %s %s", ev, recs[0].id, recs[0].sha)
	}
	// A second pass for the same merge is another record with another
	// identity, and the first is no longer the newest.
	r.suite.set(SuiteResult{Passed: true, Output: []byte("ok again\n"), Started: start.Add(time.Hour), Duration: 2 * time.Second}, nil)
	v, err := r.o.Host.Verify(context.Background(), ownerchannel.VerifyInput{Project: "project-a", RequestID: "rq-again-01", Merge: merge, Base: r.scene.target})
	if err != nil || !v.Clean {
		t.Fatalf("Verify: %+v, %v", v, err)
	}
	if v.Evidence.RecordID == ev.RecordID || v.Evidence.RecordSHA256 == ev.RecordSHA256 {
		t.Errorf("a second run has the evidence of the first: %+v", v.Evidence)
	}
	fresh, err := r.o.Host.Fresh(context.Background(), ownerchannel.FreshInput{Project: "project-a", Pending: p})
	if err != nil || len(fresh.Evidence) != 1 {
		t.Fatalf("Fresh: %+v, %v", fresh, err)
	}
	if got := fresh.Evidence[0]; got.RecordID != ev.RecordID || got.RecordSHA256 != ev.RecordSHA256 || got.Newest {
		t.Errorf("the bound record stands as %+v after a later run, want the same record, not newest", got)
	}
}

// Verify refuses what describes no merge, and runs the gates the project has.
func TestVerifyChecksItsInputAndPassesTheProjectsGates(t *testing.T) {
	r := newRig(t, execFeature)
	h := r.o.Host
	merge := strings.Repeat("a", 40)
	for _, in := range []ownerchannel.VerifyInput{
		{Project: "project-a", RequestID: "../x", Merge: merge},
		{Project: "project-a", RequestID: "rq-ok-01", Merge: "abc"},
		{Project: "project-b", RequestID: "rq-ok-01", Merge: merge},
	} {
		if _, err := h.Verify(context.Background(), in); err == nil {
			t.Errorf("Verify accepted %+v", in)
		}
	}
	if r.suite.count() != 0 {
		t.Errorf("the suite ran for a refused input")
	}
	if _, err := h.Verify(context.Background(), ownerchannel.VerifyInput{Project: "project-a", RequestID: "rq-ok-01", Merge: merge, Base: merge}); err != nil {
		t.Fatal(err)
	}
	if got := r.suite.calls[0]; len(got.Gates) != 1 || got.Gates[0].Name != "suite" || strings.Join(got.Gates[0].Argv, " ") != "make test" {
		t.Errorf("gates %+v", got.Gates)
	}
}
