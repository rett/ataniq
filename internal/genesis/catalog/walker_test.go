package catalog

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"ataniq/internal/genesis/brief"
)

func apply(t testing.TB, c Catalog, b brief.Brief, node string, a Answer) (brief.Brief, Result) {
	t.Helper()
	out, res, err := Apply(c, b, node, a, t0)
	if err != nil {
		t.Fatalf("%s: %v", node, err)
	}
	return out, res
}

func ids(r Round) []string {
	out := make([]string, len(r.Nodes))
	for i, n := range r.Nodes {
		out[i] = n.ID
	}
	return out
}

// branchFields are the fields some condition names: the answers that steer
// the walk.
func branchFields(c Catalog) map[string]bool {
	out := map[string]bool{}
	for _, n := range c.Nodes {
		if n.When != nil {
			for _, f := range n.When.fields(nil) {
				out[f] = true
			}
		}
	}
	return out
}

// choicesOf returns every answer the path test tries for a node that
// steers the walk: each option, and other text when allowed.
func choicesOf(n Node) []Answer {
	var out []Answer
	for _, o := range n.Options {
		if n.Type == Multi {
			out = append(out, Answer{Choices: []string{o.ID}})
		} else {
			out = append(out, Answer{Choice: o.ID})
		}
	}
	if n.AllowOther {
		out = append(out, Answer{Other: "a board game for two players"})
	}
	return out
}

// Variants of the answers to a node that does not steer the walk. Variant
// v below len(options) picks option v (wrapping); the variants after them
// pick every option of a multiple choice, you decide, skip, and other text,
// each falling back to the first option when the node does not allow it.
const (
	variantAll = 8 + iota
	variantYouDecide
	variantSkip
	variantOther
	variants
)

func variantOf(n Node, v int) Answer {
	first := Answer{Choice: "1"}
	if len(n.Options) > 0 {
		first = choicesOf(n)[0]
	}
	switch v {
	case variantAll:
		if n.Type == Multi {
			all := make([]string, len(n.Options))
			for i, o := range n.Options {
				all[i] = o.ID
			}
			return Answer{Choices: all}
		}
		return first
	case variantYouDecide:
		if n.AllowYouDecide {
			return Answer{YouDecide: true}
		}
		return first
	case variantSkip:
		if n.Default != "" {
			return Answer{Skip: true}
		}
		return first
	case variantOther:
		if n.AllowOther {
			return Answer{Other: "something of my own"}
		}
		return first
	}
	if n.Type == Scale {
		return Answer{Choice: string(rune('1' + v%5))}
	}
	o := n.Options[v%len(n.Options)]
	if n.Type == Multi {
		return Answer{Choices: []string{o.ID}}
	}
	return Answer{Choice: o.ID}
}

// TestEveryPathEndsDone walks the v1 catalog to completion over every
// combination of the answers that steer the walk (every option of a node
// some condition names, and other text where allowed), crossed with the
// variants of every other answer, so that every option of every node is
// chosen on some path. A node no condition names cannot change which
// nodes come next, so this covers every path of the walk. Every path must
// end done within 6 rounds with every required field of its kind set, and
// filled once the inferred values are confirmed.
func TestEveryPathEndsDone(t *testing.T) {
	c := mustV1(t)
	branch := branchFields(c)
	asked := map[string]bool{}
	chosen := map[string]bool{}
	paths := 0
	var walk func(b brief.Brief, v int)
	walk = func(b brief.Brief, v int) {
		r, done, err := Next(c, b, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if done {
			paths++
			checkComplete(t, c, b)
			return
		}
		if r.Number > MaxRounds || len(r.Nodes) == 0 || len(r.Nodes) > MaxPerRound {
			t.Fatalf("round %d holds %d nodes", r.Number, len(r.Nodes))
		}
		// The answers to the round: every choice for a steering node,
		// one variant for the others.
		var step func(b brief.Brief, i int)
		step = func(b brief.Brief, i int) {
			if i == len(r.Nodes) {
				walk(b, v)
				return
			}
			n := r.Nodes[i]
			asked[n.ID] = true
			answers := []Answer{variantOf(n, v)}
			if branch[n.Field] {
				answers = choicesOf(n)
			}
			for _, a := range answers {
				a.Round = r.Number
				next, res := apply(t, c, b, n.ID, a)
				if res.Changed || res.Reset != 0 {
					t.Fatalf("a first answer to %s reports a change", n.ID)
				}
				f, _ := next.Get(n.Field)
				for _, id := range append(f.Value.Many(), f.Value.One()) {
					chosen[n.ID+"="+id] = true
				}
				step(next, i+1)
			}
		}
		step(b, 0)
	}
	for v := 0; v < variants; v++ {
		walk(brief.New(c.Digest()), v)
	}
	if paths < 50 {
		t.Fatalf("walked %d paths", paths)
	}
	for _, n := range c.Nodes {
		if !asked[n.ID] {
			t.Errorf("no path asks %s", n.ID)
		}
		for _, o := range n.Options {
			if !chosen[n.ID+"="+o.ID] {
				t.Errorf("no path chooses %s of %s", o.ID, n.ID)
			}
		}
	}
	t.Logf("%d paths", paths)
}

func checkComplete(t testing.TB, c Catalog, b brief.Brief) {
	t.Helper()
	if b.Rounds > MaxRounds {
		t.Fatalf("done after %d rounds", b.Rounds)
	}
	if b.Kind == "" {
		t.Fatal("done without a kind")
	}
	for _, f := range Required(c, b) {
		if _, ok := b.Get(f); !ok {
			t.Fatalf("done without the required field %s (kind %s)", f, b.Kind)
		}
	}
	for name, f := range b.Fields {
		if f.Provenance == brief.SupervisorInferred {
			if err := b.Confirm(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	if filled, total := Readiness(c, b); filled != total {
		t.Fatalf("readiness %d of %d once confirmed", filled, total)
	}
	if _, err := brief.Encode(b); err != nil {
		t.Fatalf("the brief at the end is not valid: %v", err)
	}
}

// The rounds of one software path: the kind alone, then three at a time by
// weight, in six rounds. (With v1's weights the software path's rounds
// happen to be one topic each; TestRoundsOfEveryKind pins rounds that mix
// topics.)
func TestRoundsOfASoftwarePath(t *testing.T) {
	c := mustV1(t)
	b := brief.New(c.Digest())
	want := [][]string{
		{"kind"},
		{"software.sub_kind", "goal", "audience"},
		{"software.stack", "software.data", "software.auth"},
		{"software.deploy_kind", "hosts", "budget_band"},
		{"deliverables", "quality_bar", "schedule"},
		{"review_points"},
	}
	for i, w := range want {
		r, done, err := Next(c, b, Limits{})
		if err != nil || done {
			t.Fatalf("round %d: done %v, %v", i+1, done, err)
		}
		if r.Number != i+1 || strings.Join(ids(r), ",") != strings.Join(w, ",") {
			t.Fatalf("round %d is %d %v, want %v", i+1, r.Number, ids(r), w)
		}
		if i == 0 && r.Unanswered != 1 {
			t.Fatalf("round 1 has %d unanswered", r.Unanswered)
		}
		for _, n := range r.Nodes {
			a := variantOf(n, 0)
			if n.ID == "kind" {
				a = Answer{Choice: "software"}
			}
			a.Round = r.Number
			b, _ = apply(t, c, b, n.ID, a)
		}
	}
	if _, done, _ := Next(c, b, Limits{}); !done {
		t.Fatal("not done after the last round")
	}
	if filled, total := Readiness(c, b); filled != total || total != 14 {
		t.Fatalf("readiness %d of %d", filled, total)
	}
}

func TestNextStops(t *testing.T) {
	c := mustV1(t)
	b := brief.New(c.Digest())
	if _, done, _ := Next(c, b, Limits{Enough: true}); !done {
		t.Fatal("enough did not stop the walk")
	}
	b, _ = apply(t, c, b, "kind", Answer{Choice: "publishing", Round: 1})
	b, _ = apply(t, c, b, "publishing.sub_kind", Answer{Choice: "article", Round: 2})
	if _, done, _ := Next(c, b, Limits{MaxRounds: 2}); !done {
		t.Fatal("a limit of two rounds did not stop the walk")
	}
	if r, done, _ := Next(c, b, Limits{MaxRounds: 99, MaxPerRound: 1}); done || len(r.Nodes) != 1 || r.Number != 3 {
		t.Fatalf("round %d with %d nodes", r.Number, len(r.Nodes))
	}
	// Six rounds is the most, whatever is left.
	b.Rounds = MaxRounds
	if _, done, _ := Next(c, b, Limits{}); !done {
		t.Fatal("the walk went past six rounds")
	}
}

// A changed earlier answer clears exactly its closure, keeps the cleared
// values as history, and reports how many it reset.
func TestChangedAnswerClearsTheClosure(t *testing.T) {
	c := mustV1(t)
	b := brief.New(c.Digest())
	steps := []struct {
		node string
		a    Answer
	}{
		{"kind", Answer{Choice: "software", Round: 1}},
		{"software.sub_kind", Answer{Choice: "web_app", Round: 2}},
		{"goal", Answer{Choice: "share", Round: 2}},
		{"audience", Answer{Choice: "public", Round: 2}},
		{"software.stack", Answer{Choice: "python", Round: 3}},
		{"software.data", Answer{Choice: "accounts", Round: 3}},
		{"software.auth", Answer{Choice: "passkey", Round: 3}},
		{"hosts", Answer{Skip: true, Round: 4}},
	}
	for _, s := range steps {
		b, _ = apply(t, c, b, s.node, s.a)
	}
	// The same answer again changes nothing.
	same, res := apply(t, c, b, "software.sub_kind", Answer{Choice: "web_app", Round: 4})
	if res.Changed || res.Reset != 0 || !reflect.DeepEqual(same, b) {
		t.Fatalf("the same answer changed the brief: %+v", res)
	}
	// A changed sub-kind resets the three build answers.
	sub, res := apply(t, c, b, "software.sub_kind", Answer{Choice: "web_tool", Round: 4})
	if !res.Changed || res.Reset != 3 {
		t.Fatalf("result %+v, want 3 reset", res)
	}
	for _, f := range []string{"software.stack", "software.data", "software.auth"} {
		if _, ok := sub.Get(f); ok {
			t.Fatalf("%s survived the change", f)
		}
	}
	for _, f := range []string{"goal", "audience", "hosts", "kind"} {
		if _, ok := sub.Get(f); !ok {
			t.Fatalf("%s was cleared outside the closure", f)
		}
	}
	h := sub.History
	if len(h) != 4 || h[3].Reason != brief.ReasonReplaced || h[3].Field != "software.sub_kind" {
		t.Fatalf("the history is %+v", h)
	}
	for _, e := range h[:3] {
		if e.Reason != brief.ReasonInvalidated || e.Cause != "software.sub_kind" {
			t.Fatalf("history entry %+v", e)
		}
	}
	// The original brief is untouched: Apply can preview a change.
	if _, ok := b.Get("software.stack"); !ok || len(b.History) != 0 {
		t.Fatal("Apply changed the brief it was given")
	}
	// A changed kind resets every software answer, and the brief moves
	// to the new domain.
	pub, res := apply(t, c, b, "kind", Answer{Choice: "publishing", Round: 4})
	if res.Reset != 4 || pub.Kind != brief.DomainPublishing {
		t.Fatalf("reset %d, kind %s", res.Reset, pub.Kind)
	}
	for name := range pub.Fields {
		if brief.FieldDomain(name) == brief.DomainSoftware {
			t.Fatalf("%s survived the change of kind", name)
		}
	}
	r, _, err := Next(c, pub, Limits{})
	if err != nil || ids(r)[0] != "publishing.sub_kind" {
		t.Fatalf("the next round is %v (%v)", ids(r), err)
	}
	// A change of provenance only, to the same value, resets nothing.
	b2, res := apply(t, c, b, "hosts", Answer{Choice: "own_server", Round: 4})
	if res.Changed || res.Reset != 0 {
		t.Fatalf("result %+v", res)
	}
	if f, _ := b2.Get("hosts"); f.Provenance != brief.OwnerChosen {
		t.Fatalf("the provenance is %s", f.Provenance)
	}
}

func TestYouDecideAndSkip(t *testing.T) {
	c := mustV1(t)
	b := brief.New(c.Digest())
	b, _ = apply(t, c, b, "kind", Answer{Choice: "software", Round: 1})
	b, _ = apply(t, c, b, "software.sub_kind", Answer{Choice: "web_app", Round: 2})
	cases := []struct {
		node  string
		a     Answer
		value string
		p     brief.Provenance
	}{
		{"software.stack", Answer{YouDecide: true}, "go", brief.SupervisorInferred},
		{"software.data", Answer{YouDecide: true}, "none", brief.Default},
		{"software.auth", Answer{Skip: true}, "passkey", brief.Default},
		{"quality_bar", Answer{Choice: "polished"}, "polished", brief.OwnerChosen},
	}
	for _, tc := range cases {
		tc.a.Round = 3
		var res Result
		b, res = apply(t, c, b, tc.node, tc.a)
		n, _ := c.Node(tc.node)
		f, _ := b.Get(n.Field)
		if f.Value.One() != tc.value || f.Provenance != tc.p || res.Provenance != tc.p || f.Node != tc.node || f.Round != 3 {
			t.Errorf("%s: %+v", tc.node, f)
		}
	}
	b, _ = apply(t, c, b, "review_points", Answer{Skip: true, Round: 3})
	if f, _ := b.Get("review_points"); !f.Value.Equal(brief.Many("publish")) || f.Provenance != brief.Default {
		t.Fatalf("a skipped multiple choice is %+v", f)
	}
	// The inferred field is not filled until confirmed.
	before, _ := b.Readiness([]string{"software.stack"})
	if before != 0 {
		t.Fatal("an inferred field counts before it is confirmed")
	}
	// For planning only, it counts as it is.
	if planned, _ := b.PlanReadiness([]string{"software.stack"}); planned != 1 {
		t.Fatal("an inferred field does not count for planning")
	}
	if f, tot := Readiness(c, b); f != 6 || tot != 14 {
		t.Fatalf("readiness %d of %d", f, tot)
	}
	if f, tot := PlanReadiness(c, b); f != 7 || tot != 14 {
		t.Fatalf("planning readiness %d of %d", f, tot)
	}
	g, err := Summary(c, b)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.DecidedForYou) != 1 || g.DecidedForYou[0].Field != "software.stack" || g.DecidedForYou[0].Labels[0] != "Go" {
		t.Fatalf("decided for you: %+v", g.DecidedForYou)
	}
	if len(g.Defaults) != 3 || len(g.YouChose) != 3 {
		t.Fatalf("defaults %d, you chose %d", len(g.Defaults), len(g.YouChose))
	}
	if g.YouChose[0].Field != "kind" || g.YouChose[0].Prompt == "" {
		t.Fatalf("the first item is %+v", g.YouChose[0])
	}
	if strings.Join(g.Missing, ",") != "goal,audience,software.deploy_kind,hosts,budget_band,deliverables,schedule" {
		t.Fatalf("missing %v", g.Missing)
	}
	if err := b.Confirm("software.stack"); err != nil {
		t.Fatal(err)
	}
	if after, _ := b.Readiness([]string{"software.stack"}); after != 1 {
		t.Fatal("a confirmed inferred field does not count")
	}
}

func TestApplyRefuses(t *testing.T) {
	c := mustV1(t)
	b := brief.New(c.Digest())
	b, _ = apply(t, c, b, "kind", Answer{Choice: "software", Round: 1})
	b, _ = apply(t, c, b, "software.sub_kind", Answer{Choice: "web_app", Round: 2})
	before, err := brief.Encode(b)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		node string
		a    Answer
		code brief.Code
	}{
		{"unknown node", "colour", Answer{Choice: "red", Round: 2}, brief.CodeNodeUnknown},
		{"condition does not hold", "media.sub_kind", Answer{Choice: "book", Round: 2}, brief.CodeNodeNotEligible},
		{"round seven", "goal", Answer{Choice: "try", Round: 7}, brief.CodeRoundInvalid},
		{"round zero", "goal", Answer{Choice: "try"}, brief.CodeRoundInvalid},
		{"a round skipped", "goal", Answer{Choice: "try", Round: 4}, brief.CodeRoundInvalid},
		{"an earlier round", "goal", Answer{Choice: "try", Round: 1}, brief.CodeRoundInvalid},
		{"no answer", "goal", Answer{Round: 2}, brief.CodeAnswerInvalid},
		{"two answers", "goal", Answer{Choice: "try", Skip: true, Round: 2}, brief.CodeAnswerInvalid},
		{"not an option", "goal", Answer{Choice: "fame", Round: 2}, brief.CodeAnswerInvalid},
		{"choices for a single choice", "goal", Answer{Choices: []string{"try"}, Round: 2}, brief.CodeAnswerInvalid},
		{"choice for a multiple choice", "deliverables", Answer{Choice: "plan", Round: 2}, brief.CodeAnswerInvalid},
		{"empty choices", "deliverables", Answer{Choices: []string{}, Round: 2}, brief.CodeAnswerInvalid},
		{"a choice twice", "deliverables", Answer{Choices: []string{"plan", "plan"}, Round: 2}, brief.CodeAnswerInvalid},
		{"choices not options", "deliverables", Answer{Choices: []string{"plan", "fame"}, Round: 2}, brief.CodeAnswerInvalid},
		{"other not allowed", "goal", Answer{Other: "fame", Round: 2}, brief.CodeAnswerInvalid},
		{"you decide not allowed", "goal", Answer{YouDecide: true, Round: 2}, brief.CodeAnswerInvalid},
		{"skip without a default", "goal", Answer{Skip: true, Round: 2}, brief.CodeAnswerInvalid},
		{"a node of another kind", "media.rights", Answer{Choice: "yes", Round: 2}, brief.CodeNodeNotEligible},
		{"other too long", "software.stack", Answer{Other: brief.Untrusted(strings.Repeat("a", 201)), Round: 3}, brief.CodeTooLarge},
		{"other with a control character", "software.stack", Answer{Other: "go\nrust", Round: 3}, brief.CodeValueInvalid},
		{"other shaped like a key", "software.stack", Answer{Other: brief.Untrusted("my key " + "AKI" + "A" + "IOSFODNN7" + "EXAMPLE"), Round: 3}, brief.CodeSecretShaped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, _, err := Apply(c, b, tc.node, tc.a, t0)
			wantCode(t, err, tc.code)
			if after, _ := brief.Encode(out); !bytes.Equal(after, before) {
				t.Fatal("a refused answer changed the brief")
			}
		})
	}
	if _, _, err := Apply(Catalog{}, b, "goal", Answer{Choice: "try", Round: 2}, t0); err == nil {
		t.Fatal("a catalog that was not loaded was used")
	}
}

// A walker under catalog version A is unaffected by version B: a brief
// records the digest of its catalog, and every walker function refuses a
// brief under another.
func TestVersionMismatch(t *testing.T) {
	a := mustV1(t)
	changed := strings.Replace(string(v1Data), "What kind of project is it?", "What kind of project is this?", 1)
	bcat, err := Load([]byte(changed))
	if err != nil {
		t.Fatal(err)
	}
	if bcat.Digest() == a.Digest() {
		t.Fatal("two versions have one digest")
	}
	ba := brief.New(a.Digest())
	ba, _ = apply(t, a, ba, "kind", Answer{Choice: "media", Round: 1})
	if _, _, err := Next(bcat, ba, Limits{}); err == nil {
		t.Fatal("Next walked a brief under another version")
	} else {
		wantCode(t, err, brief.CodeCatalogMismatch)
	}
	_, _, err = Apply(bcat, ba, "media.sub_kind", Answer{Choice: "book", Round: 2}, t0)
	wantCode(t, err, brief.CodeCatalogMismatch)
	_, err = Summary(bcat, ba)
	wantCode(t, err, brief.CodeCatalogMismatch)
	// The brief walks on under its own version.
	r, done, err := Next(a, ba, Limits{})
	if err != nil || done || ids(r)[0] != "media.sub_kind" {
		t.Fatalf("round %v, done %v, %v", ids(r), done, err)
	}
}

func TestOtherKindIsTheGenericBranch(t *testing.T) {
	c := mustV1(t)
	b := brief.New(c.Digest())
	b, _ = apply(t, c, b, "kind", Answer{Other: "a board game for two players", Round: 1})
	if b.Kind != brief.DomainOther {
		t.Fatalf("the kind is %s", b.Kind)
	}
	r, _, err := Next(c, b, Limits{})
	if err != nil || strings.Join(ids(r), ",") != "goal,audience,formats" {
		t.Fatalf("round %v (%v)", ids(r), err)
	}
	if _, total := Readiness(c, b); total != len(c.Required[brief.DomainOther]) {
		t.Fatalf("total %d", total)
	}
}

// FuzzApply feeds the walker random answers to random nodes. What must
// hold: Apply never panics; a refusal is a *brief.Error and leaves the
// brief as it was; an accepted answer gives a valid brief that encodes,
// decodes to itself, stays within six rounds, and holds the answered
// field.
func FuzzApply(f *testing.F) {
	f.Add([]byte{0, 0, 0, 1, 1, 0, 1, 2, 4, 0, 0, 2})
	f.Add([]byte("kind software stack you decide skip other text"))
	f.Add([]byte{0, 3, 0, 1, 0, 2, 5, 9, 200, 17, 3, 3, 3, 3})
	c, err := V1()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		b := brief.New(c.Digest())
		next := func() byte {
			if len(data) == 0 {
				return 0
			}
			x := data[0]
			data = data[1:]
			return x
		}
		for steps := 0; len(data) > 0 && steps < 64; steps++ {
			n := c.Nodes[int(next())%len(c.Nodes)]
			form, pick := next(), next()
			a := Answer{Round: b.Rounds + int(next()%3) - 1 + 1}
			switch form % 7 {
			case 0:
				if len(n.Options) > 0 {
					a.Choice = n.Options[int(pick)%len(n.Options)].ID
				} else {
					a.Choice = string(rune('0' + pick%7))
				}
			case 1:
				for i, o := range n.Options {
					if pick&(1<<i) != 0 {
						a.Choices = append(a.Choices, o.ID)
					}
				}
				if a.Choices == nil {
					a.Choices = []string{}
				}
			case 2:
				k := int(pick) % 40
				if k > len(data) {
					k = len(data)
				}
				a.Other = brief.Untrusted(data[:k])
				data = data[k:]
			case 3:
				a.YouDecide = true
			case 4:
				a.Skip = true
			case 5:
				a.Choice, a.Skip = "x", true
			default:
				a.Choice = string(rune('a' + pick%26))
			}
			before, err := brief.Encode(b)
			if err != nil {
				t.Fatalf("the brief went invalid: %v", err)
			}
			out, res, err := Apply(c, b, n.ID, a, t0.Add(time.Duration(steps)*time.Second))
			if err != nil {
				if _, ok := brief.CodeOf(err); !ok {
					t.Fatalf("a refusal is not a *brief.Error: %v", err)
				}
				if after, _ := brief.Encode(b); !bytes.Equal(after, before) {
					t.Fatal("a refused answer changed the brief")
				}
				continue
			}
			enc, err := brief.Encode(out)
			if err != nil {
				t.Fatalf("an accepted answer gave an invalid brief: %v", err)
			}
			back, err := brief.Decode(enc)
			if err != nil || !reflect.DeepEqual(back, out) {
				t.Fatalf("the brief does not round trip (%v)", err)
			}
			if out.Rounds > MaxRounds {
				t.Fatalf("the brief reached round %d", out.Rounds)
			}
			if fld, ok := out.Get(n.Field); !ok || fld.Node != n.ID || fld.Provenance != res.Provenance {
				t.Fatalf("the answered field is %+v", fld)
			}
			if res.Reset < 0 || (res.Reset > 0 && !res.Changed) {
				t.Fatalf("result %+v", res)
			}
			b = out
		}
		if _, _, err := Next(c, b, Limits{}); err != nil {
			t.Fatalf("Next refused a brief the walker built: %v", err)
		}
		if _, err := Summary(c, b); err != nil {
			t.Fatal(err)
		}
	})
}
