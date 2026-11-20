package doortest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/record"
)

// vectors is the folder of the record vectors, which these tests are driven
// from where a vector fits.
const vectors = "../../record/testdata"

// The object ids of the vectors: the target's tip, the feature's tip and
// the merge the host built from them.
const (
	baseID    = "93c8d68b93c8d68b93c8d68b93c8d68b93c8d68b"
	featureID = "8281895a8281895a8281895a8281895a8281895a"
	mergeID   = "0194dafc0194dafc0194dafc0194dafc0194dafc"
	otherID   = "5555555555555555555555555555555555555555"
	targetRef = "refs/heads/develop"
)

// start is the time the vectors' pending record was written at.
var start = time.Date(2026, 11, 20, 14, 2, 4, 0, time.UTC)

func vector(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(vectors, name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// invalidVectors returns the code each invalid vector is refused with, by
// file name.
func invalidVectors(t *testing.T) map[string]record.Code {
	t.Helper()
	var listed map[string]struct {
		Code record.Code `json:"code"`
	}
	if err := json.Unmarshal(vector(t, "invalid.json"), &listed); err != nil {
		t.Fatal(err)
	}
	out := make(map[string]record.Code, len(listed))
	for name, v := range listed {
		out[name] = v.Code
	}
	return out
}

// fixture is one stand-in with its clock, configured so that its first
// pending record is the vectors' one, byte for byte.
type fixture struct {
	t     *testing.T
	ctx   context.Context
	clock *Clock
	d     *Door
	// host is the vectors' pending record, which the plans of a test take
	// their summary and evidence from.
	host record.Pending
	n    int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	clock := NewClock(start)
	cfg := Sample(clock.Now)
	cfg.Command = "qargi"
	cfg.FirstNumber = 7
	host, err := record.DecodePending(vector(t, "pending-valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, ctx: context.Background(), clock: clock, d: New(cfg), host: host}
}

// request returns a request for finishing the feature name, with an id of
// its own and no note.
func (f *fixture) request(name string, change func(*record.Request)) door.Record {
	f.t.Helper()
	f.n++
	r := record.Request{
		Schema:     record.SchemaRequest,
		RequestID:  "rq-test-" + string(rune('a'+f.n/26)) + string(rune('a'+f.n%26)),
		Epoch:      7,
		Verb:       record.VerbFeatureFinish,
		Argv:       []string{name},
		FeatureSHA: featureID,
		NoteSHA256: record.Digest(nil),
		At:         record.FormatTime(f.clock.Now()),
	}
	if change != nil {
		change(&r)
	}
	b, err := record.EncodeRequest(r)
	if err != nil {
		f.t.Fatal(err)
	}
	return door.RecordOf(b)
}

// admit admits a new request for the feature name.
func (f *fixture) admit(name string) door.Record {
	f.t.Helper()
	req := f.request(name, nil)
	if _, err := f.d.Admit(f.ctx, req); err != nil {
		f.t.Fatalf("admit %s: %v", name, err)
	}
	return req
}

// plan returns the host's findings for req as the vectors have them: the
// merge of the feature into develop, its review and its suite record.
func (f *fixture) plan(req door.Record) door.PlanInput {
	summary := *f.host.Binding.Summary
	return door.PlanInput{
		Request:      req,
		TargetRef:    targetRef,
		Base:         baseID,
		Feature:      featureID,
		Merge:        mergeID,
		Summary:      &summary,
		Evidence:     append([]record.Evidence(nil), f.host.Binding.Evidence...),
		MergeParents: []string{baseID, featureID},
		TargetNow:    baseID,
	}
}

// pending admits and plans a request for the feature name and returns the
// pending record.
func (f *fixture) pending(name string) record.Pending {
	f.t.Helper()
	rec, err := f.d.Plan(f.ctx, f.plan(f.admit(name)))
	if err != nil {
		f.t.Fatalf("plan %s: %v", name, err)
	}
	p, err := record.DecodePending(rec.Bytes)
	if err != nil {
		f.t.Fatalf("the stand-in wrote a pending record that does not decode: %v", err)
	}
	return p
}

// decision returns a decision for p as the daemon would write it now: an
// approve that signs off the flagged list, or a deny.
func (f *fixture) decision(p record.Pending, approve bool, change func(*record.Decision)) door.Record {
	f.t.Helper()
	now := f.clock.Now()
	d := record.Decision{
		Schema:        record.SchemaDecision,
		ID:            p.ID,
		Planner:       p.Planner,
		Decision:      record.DecisionDeny,
		BindingSHA256: p.BindingSHA256,
		Project:       p.Binding.Project,
		Subject:       p.Binding.Subject,
		PlanToken:     p.Binding.PlanToken,
		SignedOff:     []record.SignOff{},
		DecidedAt:     record.FormatTime(now),
		ExpiresAt:     record.FormatTime(now.Add(30 * time.Minute)),
		Confirmed:     record.ConfirmedCode,
		Console: record.Console{
			PID: 41822, TTY: "/dev/pts/4", Client: "console-a 0.0.1",
			Decider: true, Surface: "console", Session: "0123456789abcdef",
		},
	}
	d.Subject.Holder = nil
	if approve {
		d.Decision = record.DecisionApprove
		d.Uses = 1
		if p.Binding.Summary != nil {
			for _, flag := range p.Binding.Summary.Flagged {
				d.SignedOff = append(d.SignedOff, record.SignOff{Kind: flag.Kind, Path: flag.Path})
			}
		}
	}
	if change != nil {
		change(&d)
	}
	b, err := record.EncodeDecision(d)
	if err != nil {
		f.t.Fatal(err)
	}
	return door.RecordOf(b)
}

// approved returns a pending record for the feature name that the owner
// approved.
func (f *fixture) approved(name string) record.Pending {
	f.t.Helper()
	p := f.pending(name)
	got, err := f.d.Decide(f.ctx, f.decision(p, true, nil))
	if err != nil || got.State != record.StateApproved {
		f.t.Fatalf("approve %s: %v, %v", name, got.State, err)
	}
	return p
}

// fresh returns the values a use of p needs when nothing has changed.
func fresh(p record.Pending) door.Fresh {
	out := door.Fresh{}
	for _, e := range p.Binding.Effects {
		out.Refs = append(out.Refs, door.RefValue{Ref: e.Ref, Value: e.Old})
	}
	if s := p.Binding.Summary; s != nil {
		out.ReviewDigest, out.RuleSetSHA256 = s.ReviewDigest, s.ExecConfigSet.SHA256
	}
	for _, e := range p.Binding.Evidence {
		out.Evidence = append(out.Evidence, door.EvidenceState{RecordID: e.RecordID, RecordSHA256: e.RecordSHA256, Newest: true})
	}
	return out
}

// consumed returns a pending record for the feature name whose approval
// was used.
func (f *fixture) consumed(name string) record.Pending {
	f.t.Helper()
	p := f.approved(name)
	if _, err := f.d.Use(f.ctx, door.UseInput{ID: p.ID, Fresh: fresh(p)}); err != nil {
		f.t.Fatalf("use %s: %v", name, err)
	}
	return p
}

// settle settles p with an outcome.
func (f *fixture) settle(p record.Pending, o door.Outcome) (door.Settled, error) {
	return f.d.Settle(f.ctx, door.SettleInput{ID: p.ID, BindingSHA256: p.BindingSHA256, Outcome: o})
}

// state returns the state of the record id, read through Show.
func (f *fixture) state(id string) record.State {
	f.t.Helper()
	shown, err := f.d.Show(f.ctx, id)
	if err != nil {
		f.t.Fatalf("show %s: %v", id, err)
	}
	return shown.Entry.State
}

// wantCode fails the test unless err is a *record.Error with the code.
func wantCode(t *testing.T, err error, want record.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("no refusal, want %s", want)
	}
	refusal, ok := err.(*record.Error)
	if !ok {
		t.Fatalf("the error is %T (%v), want a *record.Error with %s", err, err, want)
	}
	if refusal.Code != want {
		t.Fatalf("refused %s (%s), want %s", refusal.Code, refusal.Detail, want)
	}
	if refusal.Detail == "" {
		t.Errorf("the refusal %s has no detail", want)
	}
}
