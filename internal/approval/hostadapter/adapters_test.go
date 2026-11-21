//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/mergebuild"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/record"
	"ataniq/internal/intake/execconfig"
	"ataniq/internal/intake/verify"
)

func resolveInput(name string) ownerchannel.ResolveInput {
	return ownerchannel.ResolveInput{Project: "project-a", Request: record.Request{Verb: record.VerbFeatureFinish, Argv: []string{name}}}
}

// Resolve reads the feature and the target by their exact names, once.
func TestResolveReadsTheFeatureAndTheTargetByTheirExactNames(t *testing.T) {
	r := newRig(t, execFeature)
	// Refs whose names only start like the ones asked for, or lie beside
	// them, are not read.
	r.lab.setRef("refs/heads/develop-2", r.scene.root)
	r.lab.setRef("refs/leads/role-a/heads/feature/x-2", r.scene.root)
	r.lab.setRef("refs/leads/role-b/heads/feature/x", r.scene.root)
	got, err := r.o.Host.Resolve(context.Background(), resolveInput("x"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != (ownerchannel.Resolved{TargetRef: targetBranch, TargetTip: r.scene.target, FeatureTip: r.scene.feature}) {
		t.Errorf("Resolve = %+v", got)
	}
	tip, err := r.o.Host.TargetTip(context.Background(), "project-a", targetBranch)
	if err != nil || tip != r.scene.target {
		t.Errorf("TargetTip = %q, %v", tip, err)
	}
	// A ref that is not the project's target, and a project the host does
	// not serve, are failures of the call.
	if _, err := r.o.Host.TargetTip(context.Background(), "project-a", "refs/heads/develop-2"); err == nil {
		t.Errorf("TargetTip answered for another branch")
	}
	if _, err := r.o.Host.TargetTip(context.Background(), "project-b", targetBranch); err == nil {
		t.Errorf("TargetTip answered for another project")
	}
	// A feature that is not in the mirror is feature-moved; a name outside
	// the grammar is feature-name, and nothing is read for it.
	_, err = r.o.Host.Resolve(context.Background(), resolveInput("nope"))
	wantCode(t, err, record.CodeFeatureMoved)
	for _, name := range []string{"../x", "X", "a b", ""} {
		_, err = r.o.Host.Resolve(context.Background(), resolveInput(name))
		wantCode(t, err, record.CodeFeatureName)
	}
	// A target that is gone is not a refusal of the lead's request.
	r.lab.git("", "update-ref", "-d", targetBranch)
	if _, err := r.o.Host.Resolve(context.Background(), resolveInput("x")); err == nil {
		t.Errorf("Resolve with no target")
	} else if _, ok := record.CodeOf(err); ok {
		t.Errorf("a missing target was answered as a refusal: %v", err)
	}
}

// Build writes the merge, pins it with the service's key and reports the
// refusals the service tells apart; Pinned and Unpin act on that pin only.
func TestBuildPinsTheMergeAndPinnedAndUnpinActOnThatPin(t *testing.T) {
	r := newRig(t, execFeature)
	h, ctx := r.o.Host, context.Background()
	key := strings.Repeat("c", 32)
	in := ownerchannel.MergeInput{
		Project: "project-a", Feature: "x", Target: "develop", TargetTip: r.scene.target, FeatureTip: r.scene.feature,
		RequestID: "rq-build-01", Collected: start, Key: key,
	}
	m, err := h.Build(ctx, in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if m.Parents != [2]string{r.scene.target, r.scene.feature} {
		t.Errorf("parents %v", m.Parents)
	}
	if got := r.lab.ref("refs/ataniq/pending/" + key); got != m.Commit {
		t.Errorf("the pin holds %q, want %s", got, m.Commit)
	}
	// The merge commit is the host's: its identity and its message.
	body := r.lab.git("", "cat-file", "commit", m.Commit)
	for _, want := range []string{"author Host Identity <host@example.invalid> 1795183324 +0000", "Merge feature/x into develop", "Request-Id: rq-build-01"} {
		if !strings.Contains(body, want) {
			t.Errorf("the merge commit lacks %q:\n%s", want, body)
		}
	}
	if at, err := h.Pinned(ctx, key); err != nil || at != m.Commit {
		t.Errorf("Pinned = %q, %v", at, err)
	}
	if at, err := h.Pinned(ctx, strings.Repeat("d", 32)); err != nil || at != "" {
		t.Errorf("Pinned of a key with no pin = %q, %v", at, err)
	}
	if _, err := h.Pinned(ctx, "../x"); err == nil {
		t.Errorf("Pinned accepted a key that is not one")
	}
	// The key is not used twice.
	if _, err := h.Build(ctx, in); err == nil {
		t.Errorf("Build overwrote a pin")
	}
	// Unpin removes the pin of that commit only.
	if err := h.Unpin(ctx, key, r.scene.target); err == nil {
		t.Errorf("Unpin removed a pin that holds another commit")
	}
	if err := h.Unpin(ctx, key, m.Commit); err != nil {
		t.Fatalf("Unpin: %v", err)
	}
	if got := r.lab.ref("refs/ataniq/pending/" + key); got != "" {
		t.Errorf("the pin is still there at %s", got)
	}
	if err := h.Unpin(ctx, key, m.Commit); !errors.Is(err, mergebuild.ErrNoPin) {
		t.Errorf("a second Unpin: %v", err)
	}
	// The branches did not move.
	if r.lab.ref(targetBranch) != r.scene.target || r.lab.ref(featureRef) != r.scene.feature {
		t.Errorf("a branch moved")
	}
}

func TestBuildAnswersAConflictAndATargetThatHoldsTheFeature(t *testing.T) {
	l := newLab(t)
	l.put("f.txt", "1\n2\n3\n")
	root := l.commit("root\n")
	l.put("f.txt", "1\ntarget\n3\n")
	target := l.commit("target\n", root)
	l.load(root)
	l.put("f.txt", "1\nfeature\n3\n")
	feature := l.commit("feature\n", root)
	l.setRef(targetBranch, target)
	l.setRef(featureRef, feature)
	h := newHostFor(t, l)
	in := ownerchannel.MergeInput{
		Project: "project-a", Feature: "x", Target: "develop", TargetTip: target, FeatureTip: feature,
		RequestID: "rq-conflict-01", Collected: start, Key: strings.Repeat("a", 32),
	}
	before := l.refs()
	_, err := h.Build(context.Background(), in)
	wantCode(t, err, record.CodeMergeConflict)
	if l.refs() != before {
		t.Errorf("a conflict changed a ref")
	}
	// The root is held by the target already.
	in.FeatureTip, in.Key = root, strings.Repeat("b", 32)
	if _, err := h.Build(context.Background(), in); !errors.Is(err, ownerchannel.ErrNothingToMerge) {
		t.Errorf("Build of a feature the target holds: %v", err)
	}
	if l.refs() != before {
		t.Errorf("a refused build changed a ref")
	}
}

// newHostFor makes a host on the lab's mirror with its own folders, for the
// tests of the adapters that need no service.
func newHostFor(t *testing.T, l *lab) *Host {
	t.Helper()
	mail, err := NewFileMailbox(privateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	store := openStore(t)
	h, err := New(Config{Git: needGit(t), Projects: []Project{testProject(l)}, Suite: newSuite()}, store, mail)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// Mirror says what the mirror shows, and only that.
func TestMirrorSaysAppliedNotAppliedOrUnknownFromTheRefs(t *testing.T) {
	r := newRig(t, execFeature)
	h, ctx := r.o.Host, context.Background()
	old, merged := r.scene.target, r.scene.feature // any two commits stand for the old and the new value
	eff := func(ref string) record.Effect {
		return record.Effect{Kind: record.EffectRef, Ref: ref, Old: old, New: merged}
	}
	in := func(refs ...string) ownerchannel.ApplyInput {
		var es []record.Effect
		for _, ref := range refs {
			es = append(es, eff(ref))
		}
		return ownerchannel.ApplyInput{Project: "project-a", Effects: es}
	}
	r.lab.setRef("refs/heads/second", merged)
	cases := []struct {
		name string
		in   ownerchannel.ApplyInput
		want door.Outcome
	}{
		{"every ref at its old value", in(targetBranch), door.OutcomeNotApplied},
		{"every ref at its new value", in("refs/heads/second"), door.OutcomeApplied},
		{"one at each value", in(targetBranch, "refs/heads/second"), door.OutcomeUnknown},
		{"a ref that is gone", in("refs/heads/gone"), door.OutcomeUnknown},
		{"no effect", ownerchannel.ApplyInput{Project: "project-a"}, door.OutcomeUnknown},
	}
	for _, c := range cases {
		got, err := h.Mirror(ctx, c.in)
		if err != nil || got != c.want {
			t.Errorf("%s: Mirror = %q, %v, want %q", c.name, got, err, c.want)
		}
	}
	// A ref at a third value is neither.
	r.lab.setRef("refs/heads/third", r.scene.root)
	if got, _ := h.Mirror(ctx, in("refs/heads/third")); got != door.OutcomeUnknown {
		t.Errorf("a ref at a third value: %q", got)
	}
	// It writes nothing.
	before := r.lab.refs()
	_, _ = h.Mirror(ctx, in(targetBranch))
	if r.lab.refs() != before {
		t.Errorf("Mirror changed a ref")
	}
	if _, err := h.Mirror(ctx, ownerchannel.ApplyInput{Project: "project-b", Effects: in(targetBranch).Effects}); err == nil {
		t.Errorf("Mirror answered for a project it does not serve")
	}
}

// Apply refuses what is not a move of the project's target by one effect, and
// says not-applied, with nothing written, when the merge has no pin, when the
// old value is not the tip the merge was built on, and when the merge was
// never verified.
func TestApplyRefusesWhatIsNotOneMoveOfTheTargetAndAMergeThatCannotMove(t *testing.T) {
	r := newRig(t, execFeature)
	h, ctx := r.o.Host, context.Background()
	p := r.card()
	merge := p.Binding.Merge.Commit
	good := record.Effect{Kind: record.EffectRef, Ref: targetBranch, Old: r.scene.target, New: merge}
	before := r.lab.refs()
	for name, in := range map[string]ownerchannel.ApplyInput{
		"two effects":     {Project: "project-a", Effects: []record.Effect{good, good}},
		"no effect":       {Project: "project-a"},
		"another branch":  {Project: "project-a", Effects: []record.Effect{{Kind: record.EffectRef, Ref: "refs/heads/main", Old: r.scene.target, New: merge}}},
		"a deletion":      {Project: "project-a", Effects: []record.Effect{{Kind: record.EffectRef, Ref: targetBranch, Old: r.scene.target, New: strings.Repeat("0", 40)}}},
		"a creation":      {Project: "project-a", Effects: []record.Effect{{Kind: record.EffectRef, Ref: targetBranch, Old: strings.Repeat("0", 40), New: merge}}},
		"another project": {Project: "project-b", Effects: []record.Effect{good}},
	} {
		got, err := h.Apply(ctx, in)
		if err == nil || got != door.OutcomeUnknown {
			t.Errorf("%s: Apply = %q, %v, want unknown with an error", name, got, err)
		}
	}
	if r.lab.refs() != before {
		t.Errorf("a refused Apply changed a ref")
	}
	// The merge is pinned and verified, but the old value is not the tip it
	// was built on: the intake would move the target from its present tip,
	// which is not what the approval allowed. A commit with no pin has
	// nothing to move.
	for name, e := range map[string]record.Effect{
		"no pin":    {Kind: record.EffectRef, Ref: targetBranch, Old: r.scene.target, New: r.scene.feature},
		"old moved": {Kind: record.EffectRef, Ref: targetBranch, Old: r.scene.root, New: merge},
	} {
		got, err := h.Apply(ctx, ownerchannel.ApplyInput{Project: "project-a", Effects: []record.Effect{e}})
		if err != nil || got != door.OutcomeNotApplied {
			t.Errorf("%s: Apply = %q, %v, want not-applied", name, got, err)
		}
	}
	if r.lab.refs() != before {
		t.Errorf("a not-applied Apply changed a ref")
	}
	// A merge that was never verified is not applied either: the intake
	// wants a passing record for it.
	m2, _ := r.buildMerge("rq-apply-02")
	got, err := h.Apply(ctx, ownerchannel.ApplyInput{Project: "project-a", Effects: []record.Effect{{Kind: record.EffectRef, Ref: targetBranch, Old: r.scene.target, New: m2.Commit}}})
	if err != nil || got != door.OutcomeNotApplied {
		t.Errorf("an unverified merge: Apply = %q, %v, want not-applied", got, err)
	}
	if got := r.lab.ref(targetBranch); got != r.scene.target {
		t.Errorf("the target is at %s after refusals", got)
	}
	// And the verified one applies, as the control of the refusals above.
	got, err = h.Apply(ctx, ownerchannel.ApplyInput{Project: "project-a", Effects: []record.Effect{good}})
	if err != nil || got != door.OutcomeApplied || r.lab.ref(targetBranch) != merge {
		t.Errorf("the verified merge: Apply = %q, %v, target at %s", got, err, r.lab.ref(targetBranch))
	}
}

// Fresh reads what the door compares: the refs, the review digest, the rule
// set and the evidence; a target that moved or a pin that is gone gives no
// review digest and a ref at its new value.
func TestFreshReadsTheRefsTheReviewTheRulesAndTheEvidence(t *testing.T) {
	r := newRig(t, execFeature)
	h, ctx := r.o.Host, context.Background()
	p := r.card()
	fresh, err := h.Fresh(ctx, ownerchannel.FreshInput{Project: "project-a", Pending: p})
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Refs) != 1 || fresh.Refs[0] != (door.RefValue{Ref: targetBranch, Value: r.scene.target}) {
		t.Errorf("refs %+v", fresh.Refs)
	}
	if fresh.ReviewDigest != p.Binding.Summary.ReviewDigest || fresh.RuleSetSHA256 != execconfig.Digest(execconfig.Default()) ||
		fresh.RuleSetSHA256 != p.Binding.Summary.ExecConfigSet.SHA256 {
		t.Errorf("digests %q %q", fresh.ReviewDigest, fresh.RuleSetSHA256)
	}
	if len(fresh.Evidence) != 1 || !fresh.Evidence[0].Newest || fresh.Evidence[0].RecordID != p.Binding.Evidence[0].RecordID ||
		fresh.Evidence[0].RecordSHA256 != p.Binding.Evidence[0].RecordSHA256 {
		t.Errorf("evidence %+v", fresh.Evidence)
	}
	// The pin goes: there is no review to make.
	pin := r.pinOfMerge(p.Binding.Merge.Commit)
	r.lab.git("", "update-ref", "-d", pin)
	fresh, err = h.Fresh(ctx, ownerchannel.FreshInput{Project: "project-a", Pending: p})
	if err != nil || fresh.ReviewDigest != "" {
		t.Errorf("with no pin: digest %q, %v", fresh.ReviewDigest, err)
	}
	// The evidence of another record is missing, not the newest.
	q := p
	q.Binding.Evidence = []record.Evidence{{Kind: record.EvidenceSuite, RecordID: "suite-0000000000000000", RecordSHA256: strings.Repeat("0", 64)}}
	fresh, err = h.Fresh(ctx, ownerchannel.FreshInput{Project: "project-a", Pending: q})
	if err != nil || len(fresh.Evidence) != 1 || fresh.Evidence[0].Newest || fresh.Evidence[0].RecordSHA256 != "" {
		t.Errorf("evidence of a record that is not there: %+v, %v", fresh.Evidence, err)
	}
	if _, err := h.Fresh(ctx, ownerchannel.FreshInput{Project: "project-b", Pending: p}); err == nil {
		t.Errorf("Fresh answered for a project the host does not serve")
	}
}

func TestNewRefusesWhatCannotServe(t *testing.T) {
	l := newLab(t)
	mail, err := NewFileMailbox(privateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	store := openStore(t)
	good := testProject(l)
	cfg := func(f func(*Config)) Config {
		c := Config{Git: needGit(t), Projects: []Project{good}, Suite: newSuite()}
		f(&c)
		return c
	}
	bad := map[string]Config{
		"no suite":      cfg(func(c *Config) { c.Suite = nil }),
		"no project":    cfg(func(c *Config) { c.Projects = nil }),
		"a negative t":  cfg(func(c *Config) { c.GitTimeout = -1 }),
		"two of a name": cfg(func(c *Config) { c.Projects = []Project{good, good} }),
		"a bad name":    cfg(func(c *Config) { p := good; p.Name = "Bad Name"; c.Projects = []Project{p} }),
		"a bad role":    cfg(func(c *Config) { p := good; p.Role = ""; c.Projects = []Project{p} }),
		"a relative mirror": cfg(func(c *Config) {
			p := good
			p.Mirror = "mirror.git"
			c.Projects = []Project{p}
		}),
		"a target that is not a branch": cfg(func(c *Config) { p := good; p.Target = "refs/tags/v1"; c.Projects = []Project{p} }),
		"no gate":                       cfg(func(c *Config) { p := good; p.Gates = nil; c.Projects = []Project{p} }),
		"a gate that is a builtin": cfg(func(c *Config) {
			p := good
			p.Gates = []verify.Gate{{Name: "g", Argv: []string{"cd", "/"}}}
			c.Projects = []Project{p}
		}),
		"no identity": cfg(func(c *Config) { p := good; p.Identity = mergebuild.Identity{}; c.Projects = []Project{p} }),
	}
	for name, c := range bad {
		if _, err := New(c, store, mail); err == nil {
			t.Errorf("New accepted %s", name)
		}
	}
	if _, err := New(cfg(func(*Config) {}), nil, mail); err == nil {
		t.Errorf("New accepted no store")
	}
	if _, err := New(cfg(func(*Config) {}), store, nil); err == nil {
		t.Errorf("New accepted no mailbox")
	}
	if _, err := New(cfg(func(*Config) {}), store, mail); err != nil {
		t.Errorf("New refused a good configuration: %v", err)
	}
}
