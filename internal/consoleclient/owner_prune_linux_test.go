package consoleclient

import (
	"errors"
	"strings"
	"testing"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

// newPruneCard writes a valid pending record for id "ap-"+slug of a branch
// prune of two merged branches. A prune changes no file: it has no merge, no
// diff summary and no suite record, so the card built from it has no Diff.
func newPruneCard(t *testing.T, slug string) testCard {
	t.Helper()
	p := newTestCard(t, slug, "prune", t0, []record.PathChange{testChange("a.go", "M", 1, 1)}, 0).p
	b := &p.Binding
	zeros := strings.Repeat("0", 40)
	develop := testOID("develop " + slug)
	b.Verb = record.VerbBranchPrune
	b.Argv = []string{}
	b.Effects = []record.Effect{
		{Kind: record.EffectRef, Ref: "refs/heads/feature/one", Old: testOID("one " + slug), New: zeros,
			MergedInto: &record.MergedInto{Ref: "refs/heads/develop", Tip: develop}},
		{Kind: record.EffectRef, Ref: "refs/heads/feature/two", Old: testOID("two " + slug), New: zeros,
			MergedInto: &record.MergedInto{Ref: "refs/heads/develop", Tip: develop}},
	}
	b.Merge = nil
	b.Summary = nil
	b.Evidence = []record.Evidence{}
	plan := "delete 2 merged branches\n"
	p.PlanText = record.Text(plan)
	b.PlanSHA256 = record.PlanDigest(plan)
	digest, err := record.BindingDigest(*b)
	if err != nil {
		t.Fatal(err)
	}
	p.BindingSHA256 = digest
	data, err := record.EncodePending(p)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := record.DecodePending(data)
	if err != nil {
		t.Fatal(err)
	}
	return testCard{p: decoded, data: data}
}

// A prune card has no diff summary, so no review digest. Opening one must
// build the card and not stop the console, and the card can then be decided
// on the connection that read it. Its pages are refused: there is no review
// to ask them for.
func TestAPruneCardOpensAndIsDecided(t *testing.T) {
	c := newPruneCard(t, "prune")
	f := startOwner(t)
	o := newOwner(t, f.dir)
	f.serveCard(c)
	f.onResult(ownerwire.OpDecide, decideResult(t, record.StateApproved, "approved: waiting to be applied", false))

	got, err := o.Open(c.p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Diff != nil {
		t.Fatalf("a prune card has a diff: %+v", got.Diff)
	}
	if len(got.Refs) != 2 || !got.Refs[0].Deletes || !got.Refs[1].Deletes {
		t.Fatalf("a prune card's refs = %+v", got.Refs)
	}

	if _, err := o.Page(c.p.ID, card.PagePaths, 1); !errors.Is(err, ErrCardNotOpen) {
		t.Fatalf("a page of a prune card: %v, want %v", err, ErrCardNotOpen)
	}
	if n := f.count(ownerwire.OpPaths); n != 0 {
		t.Fatalf("%d page requests of a prune card reached the daemon", n)
	}

	out, err := o.Decide(decisionOf(c, true))
	if err != nil {
		t.Fatal(err)
	}
	if out.State != record.StateApproved {
		t.Fatalf("outcome = %+v", out)
	}
	sent := f.of(ownerwire.OpDecide)
	if len(sent) != 1 {
		t.Fatalf("%d decisions reached the daemon", len(sent))
	}
	frame, err := ownerwire.DecodeDecisionFrame([]byte(sent[0].params))
	if err != nil {
		t.Fatal(err)
	}
	if frame.ID != c.p.ID || frame.BindingSHA256 != c.p.BindingSHA256 || frame.Decision != record.DecisionApprove || len(frame.SignedOff) != 0 {
		t.Fatalf("frame = %+v", frame)
	}
}
