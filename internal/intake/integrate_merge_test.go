//go:build linux

package intake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/intake/verify"
)

func newMergeRig(t *testing.T, change func(l *lab)) (*rig, *mscene) {
	t.Helper()
	m := newMergeScene(t, change)
	return newRig(t, m.scene), m
}

// mergeReq is the integration request for a merge-mode review, fully signed.
func (g *rig) mergeReq(m *mscene, r *Review) IntegrateRequest {
	return IntegrateRequest{
		Project: testProject, Review: m.mergeRequest(), SignOff: okSignOff(r),
		Store: g.store, Now: func() time.Time { return testNow },
	}
}

// TestIntegrateMergeMovesTheTargetFromTheTipToTheMerge: the target goes from
// the tip it had to the host's merge, by compare-and-swap, and nothing else
// changes: not the pin, not the lead's branch, not the feature, not HEAD.
func TestIntegrateMergeMovesTheTargetFromTheTipToTheMerge(t *testing.T) {
	g, m := newMergeRig(t, oddFeature)
	m.publish(leadRef, m.feature)
	m.publish("refs/heads/feature/x", m.feature)
	r := m.inspectMerge()
	g.recordMerge(m, nil)
	others := g.refsExcept(upstreamRef)
	headBefore := mustRead(t, filepath.Join(m.bare, "HEAD"))
	rec, err := Integrate(context.Background(), m.source(), g.mergeReq(m, r))
	if err != nil {
		t.Fatalf("Integrate: %v", err)
	}
	if got := g.id(upstreamRef); got != m.merge {
		t.Errorf("the target is at %s, want the merge %s", got, m.merge)
	}
	if g.refsExcept(upstreamRef) != others {
		t.Errorf("another ref changed:\n%s\n--\n%s", others, g.refsExcept(upstreamRef))
	}
	if got := g.id(testPin); got != m.merge {
		t.Errorf("the pin is at %s", got)
	}
	if mustRead(t, filepath.Join(m.bare, "HEAD")) != headBefore {
		t.Errorf("HEAD changed")
	}
	assertNoWorkTree(t, m.scene)
	if rec.Base != m.target || rec.Tip != m.merge || rec.TargetRef != upstreamRef || rec.TipRef != testPin ||
		rec.ReviewDigest != r.Digest() || rec.Commits != len(r.Commits) || rec.Paths != len(r.Changes) {
		t.Errorf("record: %+v", rec)
	}
	if !rec.Verification.passing() || rec.Verification.Tip != m.merge {
		t.Errorf("verification: %+v", rec.Verification)
	}
	if len(g.intakes()) != 1 {
		t.Errorf("%d intake records", len(g.intakes()))
	}
	// The merge's own parents are intact: the feature and the old tip.
	if got := g.b("rev-parse", "--verify", m.merge+"^1"); got != m.target {
		t.Errorf("first parent = %s", got)
	}
	// A second integration of the same sign-off has nothing to move.
	if _, err := Integrate(context.Background(), m.source(), g.mergeReq(m, r)); err == nil {
		t.Errorf("the same merge integrated twice")
	}
	if len(g.intakes()) != 1 {
		t.Errorf("a second record")
	}
}

func oddFeature(l *lab) {
	l.put("feature.txt", "one\n")
	l.put("a:stream", "x\n")
}

// TestIntegrateMergeNeedsTheSameChecks: the verification of the merge, the
// sign-off including the odd path, and the digest all hold in merge mode, and
// each refusal moves nothing.
func TestIntegrateMergeNeedsTheSameChecks(t *testing.T) {
	g, m := newMergeRig(t, oddFeature)
	r := m.inspectMerge()
	// No verification of the merge: refused. A pass for the feature's tip
	// is no pass for the merge.
	g.recordFor(m.feature, nil)
	if err := g.refuse("unverified merge", g.mergeReq(m, r)); !errors.Is(err, ErrNotVerified) {
		t.Errorf("unverified: %v", err)
	}
	g.recordMerge(m, nil)
	// The sign-off without the odd path is refused.
	req := g.mergeReq(m, r)
	for i, p := range req.SignOff.Paths {
		if p == "a:stream" {
			req.SignOff.Paths = append(req.SignOff.Paths[:i:i], req.SignOff.Paths[i+1:]...)
			break
		}
	}
	var se *SignOffError
	if err := g.refuse("odd path unsigned", req); !errors.As(err, &se) {
		t.Errorf("odd path unsigned: %v", err)
	}
	// A sign-off for another digest is refused as a moved review.
	req = g.mergeReq(m, r)
	req.SignOff.Digest = strings.Repeat("0", 64)
	if err := g.refuse("digest", req); !errors.Is(err, ErrMoved) {
		t.Errorf("digest: %v", err)
	}
	// An attribute file in the repository is refused before anything moves.
	setInfoAttributes(t, m.bare, "* -diff\n")
	if err := g.refuse("attributes", g.mergeReq(m, r)); !errors.Is(err, ErrAttributes) {
		t.Errorf("attributes: %v", err)
	}
	if err := os.Remove(filepath.Join(m.bare, "info", "attributes")); err != nil {
		t.Fatal(err)
	}
	if _, err := Integrate(context.Background(), m.source(), g.mergeReq(m, r)); err != nil {
		t.Fatalf("the clean case: %v", err)
	}
}

// TestIntegrateMergeTargetMoved: when the target moved after the merge was
// built, whether forward, sideways or back, the integration is ErrMoved and
// changes nothing.
func TestIntegrateMergeTargetMoved(t *testing.T) {
	cases := map[string]func(m *mscene) string{
		"forward": func(m *mscene) string {
			m.checkout(m.target)
			m.put("later.txt", "later\n")
			return m.commit("target moves on\n")
		},
		"sideways": func(m *mscene) string {
			m.checkout(m.root)
			m.put("side.txt", "side\n")
			return m.commit("another branch\n")
		},
		"back": func(m *mscene) string { return m.root },
	}
	for name, move := range cases {
		t.Run(name, func(t *testing.T) {
			g, m := newMergeRig(t, featureChange)
			r := m.inspectMerge()
			g.recordMerge(m, nil)
			req := g.mergeReq(m, r)
			m.publish(upstreamRef, move(m))
			if err := g.refuse(name, req); !errors.Is(err, ErrMoved) {
				t.Errorf("error = %v, want ErrMoved", err)
			}
		})
	}
	// The target moves between the checks and the update: the compare-and-
	// swap refuses it, from the base the review saw.
	g, m := newMergeRig(t, featureChange)
	r := m.inspectMerge()
	g.recordMerge(m, nil)
	req := g.mergeReq(m, r)
	var moved string
	req.afterChecks = func() {
		m.checkout(m.target)
		m.put("late.txt", "x\n")
		moved = m.commit("target moves at the last moment\n")
		m.publish(upstreamRef, moved)
	}
	if _, err := Integrate(context.Background(), m.source(), req); !errors.Is(err, ErrMoved) {
		t.Errorf("a move at the last moment: %v", err)
	}
	if got := g.id(upstreamRef); got != moved {
		t.Errorf("the target is at %s, want the other writer's %s", got, moved)
	}
	if len(g.intakes()) != 0 {
		t.Errorf("an intake record was written")
	}
}

func TestIntegrateMergeAtTheTarget(t *testing.T) {
	g, m := newMergeRig(t, featureChange)
	r := m.inspectMerge()
	g.recordMerge(m, nil)
	req := g.mergeReq(m, r)
	m.publish(upstreamRef, m.merge)
	if err := g.refuse("already applied", req); !errors.Is(err, ErrNothingToIntegrate) {
		t.Errorf("error = %v", err)
	}
}

// TestIntegrateLeadModeAsBefore: the lead mode takes no pin, and a merge-mode
// request takes no lead branch.
func TestIntegrateLeadModeAsBefore(t *testing.T) {
	g, m := newMergeRig(t, featureChange)
	g.recordMerge(m, nil)
	m.publish(leadRef, m.merge)
	lr := m.inspect()
	req := g.req(lr)
	req.Review.TipRef = testPin
	if err := g.refuse("pin in lead mode", req); !errors.Is(err, ErrRefName) {
		t.Errorf("pin in lead mode: %v", err)
	}
	req = g.mergeReq(m, m.inspectMerge())
	req.Review.TipRef = leadRef
	if err := g.refuse("lead ref in merge mode", req); !errors.Is(err, ErrRefName) {
		t.Errorf("lead ref in merge mode: %v", err)
	}
}

// mergeResult is a hand-made passing result of merge mode: the pin, the merge
// and the base it was built against.
func mergeResult(tip, base string) verify.Result {
	r := passResult(tip)
	r.TipRef, r.Merge, r.Base = testPin, true, base
	return r
}

// recordMerge stores a passing merge-mode verification of the scene's merge
// on the scene's base. mod changes the result first.
func (g *rig) recordMerge(m *mscene, mod func(*verify.Result)) {
	g.t.Helper()
	g.recordMergeFor(m.merge, m.target, mod)
}

func (g *rig) recordMergeFor(tip, base string, mod func(*verify.Result)) {
	g.t.Helper()
	res := mergeResult(tip, base)
	if mod != nil {
		mod(&res)
	}
	if _, err := g.store.PutVerification(testProject, res, testGates); err != nil {
		g.t.Fatal(err)
	}
}

// TestIntegrateMergeCountsOnlyMergeRecordsOfTheBase: in merge mode a pass
// counts only when it is a merge-mode record whose merge is the tip and whose
// base is the review's base. A lead-mode pass of the same commit id, and a
// merge pass made against another base, are no evidence, and neither of them
// withdraws a pass that counts.
func TestIntegrateMergeCountsOnlyMergeRecordsOfTheBase(t *testing.T) {
	t.Run("a lead-mode pass of the merge is no evidence", func(t *testing.T) {
		g, m := newMergeRig(t, featureChange)
		r := m.inspectMerge()
		g.recordFor(m.merge, nil)
		if err := g.refuse("lead record", g.mergeReq(m, r)); !errors.Is(err, ErrNotVerified) {
			t.Errorf("error = %v, want ErrNotVerified", err)
		}
	})
	t.Run("a merge pass on another base is no evidence", func(t *testing.T) {
		g, m := newMergeRig(t, featureChange)
		r := m.inspectMerge()
		g.recordMergeFor(m.merge, m.root, nil)
		if err := g.refuse("other base", g.mergeReq(m, r)); !errors.Is(err, ErrNotVerified) {
			t.Errorf("error = %v, want ErrNotVerified", err)
		}
	})
	t.Run("a merge pass of another merge is no evidence", func(t *testing.T) {
		g, m := newMergeRig(t, featureChange)
		r := m.inspectMerge()
		g.recordMergeFor(m.feature, m.target, nil)
		if err := g.refuse("other merge", g.mergeReq(m, r)); !errors.Is(err, ErrNotVerified) {
			t.Errorf("error = %v, want ErrNotVerified", err)
		}
	})
	t.Run("a merge pass on the base counts, and is the record used", func(t *testing.T) {
		g, m := newMergeRig(t, featureChange)
		r := m.inspectMerge()
		g.recordFor(m.merge, func(res *verify.Result) { res.Passed, res.Exit = false, 1 })
		g.recordMergeFor(m.merge, m.root, func(res *verify.Result) { res.Passed, res.Exit = false, 1 })
		g.recordMerge(m, nil)
		rec, err := Integrate(context.Background(), m.source(), g.mergeReq(m, r))
		if err != nil {
			t.Fatalf("Integrate: %v", err)
		}
		if v := rec.Verification; !v.Merge || v.Base != m.target || v.Tip != m.merge || v.TipRef != testPin {
			t.Errorf("the record that allowed it: %+v", v)
		}
	})
	t.Run("a later record that does not count does not withdraw the pass", func(t *testing.T) {
		g, m := newMergeRig(t, featureChange)
		r := m.inspectMerge()
		g.recordMerge(m, nil)
		g.recordMergeFor(m.merge, m.root, func(res *verify.Result) { res.Passed, res.Exit = false, 1 })
		g.recordFor(m.merge, func(res *verify.Result) { res.Passed, res.Exit = false, 1 })
		if _, err := Integrate(context.Background(), m.source(), g.mergeReq(m, r)); err != nil {
			t.Fatalf("Integrate: %v", err)
		}
	})
}

// TestIntegrateMergeLaterFailureWithdrawsThePass: as in lead mode, the newest
// counting record decides.
func TestIntegrateMergeLaterFailureWithdrawsThePass(t *testing.T) {
	g, m := newMergeRig(t, featureChange)
	r := m.inspectMerge()
	g.recordMerge(m, nil)
	g.recordMerge(m, func(res *verify.Result) { res.Passed, res.Exit = false, 2 })
	if err := g.refuse("withdrawn", g.mergeReq(m, r)); !errors.Is(err, ErrNotVerified) {
		t.Errorf("error = %v, want ErrNotVerified", err)
	}
	g.recordMerge(m, nil) // a later pass restores it
	if _, err := Integrate(context.Background(), m.source(), g.mergeReq(m, r)); err != nil {
		t.Fatalf("Integrate after a later pass: %v", err)
	}
}

// TestIntegrateLeadModeIgnoresMergeRecords: in lead mode a merge-mode record
// of the same commit id is no evidence either.
func TestIntegrateLeadModeIgnoresMergeRecords(t *testing.T) {
	g, m := newMergeRig(t, featureChange)
	m.publish(leadRef, m.merge)
	lr := m.inspect()
	g.recordMerge(m, nil)
	if err := g.refuse("merge record in lead mode", g.req(lr)); !errors.Is(err, ErrNotVerified) {
		t.Errorf("error = %v, want ErrNotVerified", err)
	}
	// A pass of lead mode counts, and a later merge-mode failure does not
	// withdraw it.
	g.recordFor(m.merge, nil)
	g.recordMerge(m, func(res *verify.Result) { res.Passed, res.Exit = false, 1 })
	if _, err := Integrate(context.Background(), m.source(), g.req(lr)); err != nil {
		t.Fatalf("Integrate: %v", err)
	}
}
