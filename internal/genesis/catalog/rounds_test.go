package catalog

import (
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/genesis/brief"
)

// A path of the interview: the kind and the sub-kind the owner picks.
type kindPath struct {
	name    string
	kind    Answer
	subKind string
	// rounds is how many rounds the path takes when every question is
	// answered, recorded: the kind alone, then three questions a round.
	rounds int
}

// kindPaths are the paths of every kind of v1: each sub-kind of the three
// domains, and a kind in the owner's own words, the generic branch with its
// own required set.
var kindPaths = []kindPath{
	{"software web_app", Answer{Choice: "software"}, "web_app", 6},
	{"software web_tool", Answer{Choice: "software"}, "web_tool", 6},
	{"publishing newsletter", Answer{Choice: "publishing"}, "newsletter", 6},
	{"publishing article", Answer{Choice: "publishing"}, "article", 6},
	{"media podcast", Answer{Choice: "media"}, "podcast", 6},
	{"media book", Answer{Choice: "media"}, "book", 6},
	{"other", Answer{Other: "a board game for two players"}, "", 4},
}

// How the walks below answer the questions that do not steer the walk.
type answerStyle int

const (
	// styleFirst picks the first option (or the step 1).
	styleFirst answerStyle = iota
	// styleYouDecide leaves the question to the host where it allows it.
	styleYouDecide
	// styleSkip leaves the question to its default where it has one.
	styleSkip
	answerStyles
)

func (s answerStyle) String() string {
	return [...]string{"first option", "you decide", "skip"}[s]
}

// answerOf is the answer of a walk to a node: the path's kind and sub-kind,
// and for any other node the style's answer, the first option where the
// node does not allow the style.
func answerOf(p kindPath, n Node, s answerStyle) Answer {
	switch {
	case n.ID == "kind":
		return p.kind
	case strings.HasSuffix(n.ID, ".sub_kind"):
		return Answer{Choice: p.subKind}
	case s == styleYouDecide && n.AllowYouDecide:
		return Answer{YouDecide: true}
	case s == styleSkip && n.Default != "":
		return Answer{Skip: true}
	}
	return variantOf(n, 0)
}

// walkOf walks a path with Next and Apply only, answering every question of
// every round except those blank names (a question left unanswered on the
// page: blank reports, for the round's number and a node, whether to leave
// it). It returns the brief at the end and the ids of each round.
func walkOf(t testing.TB, c Catalog, p kindPath, s answerStyle, blank func(round int, node string) bool) (brief.Brief, [][]string) {
	t.Helper()
	b := brief.New(c.Digest())
	var rounds [][]string
	for {
		r, done, err := Next(c, b, Limits{})
		if err != nil {
			t.Fatalf("%s: %v", p.name, err)
		}
		if done {
			return b, rounds
		}
		if r.Number != len(rounds)+1 || r.Number > MaxRounds {
			t.Fatalf("%s: round %d after %d rounds", p.name, r.Number, len(rounds))
		}
		if len(r.Nodes) < 1 || len(r.Nodes) > MaxPerRound {
			t.Fatalf("%s: round %d holds %d questions", p.name, r.Number, len(r.Nodes))
		}
		rounds = append(rounds, ids(r))
		answered := 0
		for _, n := range r.Nodes {
			if blank != nil && blank(r.Number, n.ID) {
				continue
			}
			a := answerOf(p, n, s)
			a.Round = r.Number
			b, _ = apply(t, c, b, n.ID, a)
			answered++
		}
		if answered == 0 {
			t.Fatalf("%s: round %d was left wholly blank", p.name, r.Number)
		}
	}
}

// ready reports whether every required field of the brief's kind is set,
// and the readiness once the fields the host inferred are confirmed (an
// inferred field counts only once the owner confirms it).
func ready(t testing.TB, c Catalog, b brief.Brief) (missing []string, filled, total int) {
	t.Helper()
	for _, f := range Required(c, b) {
		if _, ok := b.Get(f); !ok {
			missing = append(missing, f)
		}
	}
	for name, f := range b.Fields {
		if f.Provenance == brief.SupervisorInferred {
			if err := b.Confirm(name); err != nil {
				t.Fatal(err)
			}
		}
	}
	filled, total = Readiness(c, b)
	return missing, filled, total
}

// An interview that answers every question becomes ready within the
// rounds, for every kind and every style of answer, and takes the number of
// rounds recorded for its path; each round holds one to three questions.
// This is the test that fails when a catalog edit makes a kind need more
// rounds than the interview has: the catalog's tests find it, not the owner.
func TestEveryKindIsReadyWithinTheRounds(t *testing.T) {
	c := mustV1(t)
	kinds := map[string]bool{}
	for _, p := range kindPaths {
		for s := answerStyle(0); s < answerStyles; s++ {
			t.Run(fmt.Sprintf("%s, %s", p.name, s), func(t *testing.T) {
				b, rounds := walkOf(t, c, p, s, nil)
				kinds[b.Kind] = true
				missing, filled, total := ready(t, c, b)
				if len(missing) != 0 || filled != total || total != len(c.Required[b.Kind]) {
					t.Fatalf("after %d rounds %v: missing %v, %d of %d", len(rounds), rounds, missing, filled, total)
				}
				if len(rounds) != p.rounds || b.Rounds != p.rounds {
					t.Fatalf("%d rounds %v, recorded %d", len(rounds), rounds, p.rounds)
				}
				if s == styleFirst {
					// With no answer left to the host, the brief is ready as it
					// stands, with nothing to confirm.
					if f, tot := Readiness(c, b); f != tot {
						t.Fatalf("%d of %d with every option chosen", f, tot)
					}
				}
			})
		}
	}
	for k := range c.Required {
		if !kinds[k] {
			t.Errorf("no path walks the kind %s", k)
		}
	}
}

// The rounds of a path of each kind, pinned: the kind alone, then the
// questions three at a time by weight, whatever their topic (the media
// path's rounds 4 and 5 mix topics), so the last round asks the review
// points.
func TestRoundsOfEveryKind(t *testing.T) {
	c := mustV1(t)
	want := map[string][][]string{
		"software web_app": {
			{"kind"},
			{"software.sub_kind", "goal", "audience"},
			{"software.stack", "software.data", "software.auth"},
			{"software.deploy_kind", "hosts", "budget_band"},
			{"deliverables", "quality_bar", "schedule"},
			{"review_points"},
		},
		"publishing article": {
			{"kind"},
			{"publishing.sub_kind", "goal", "audience"},
			{"publishing.medium", "publishing.editorial_steps", "publishing.style_guide"},
			{"publishing.channel", "hosts", "budget_band"},
			{"deliverables", "quality_bar", "schedule"},
			{"review_points"},
		},
		"media book": {
			{"kind"},
			{"media.sub_kind", "goal", "audience"},
			{"media.format.book", "media.length", "media.sources"},
			{"hosts", "budget_band", "deliverables"},
			{"quality_bar", "schedule", "media.rights"},
			{"review_points"},
		},
		"other": {
			{"kind"},
			{"goal", "audience", "formats"},
			{"hosts", "budget_band", "deliverables"},
			{"quality_bar", "schedule", "review_points"},
		},
	}
	for _, p := range kindPaths {
		w, ok := want[p.name]
		if !ok {
			continue
		}
		_, got := walkOf(t, c, p, styleFirst, nil)
		if fmt.Sprint(got) != fmt.Sprint(w) {
			t.Errorf("%s: rounds\n%v\nwant\n%v", p.name, got, w)
		}
	}
}

// A question left unanswered is asked again in the next round beside the
// questions of the highest weight left, so a round is never short while
// questions are left. The phone trial's interview left the goal unanswered
// in round 2: with rounds of one topic, round 3 asked the goal alone and
// the review points never came; now round 3 asks the goal with two
// questions of the build.
func TestAQuestionLeftUnansweredJoinsTheNextRound(t *testing.T) {
	c := mustV1(t)
	b, rounds := walkOf(t, c, kindPaths[0], styleFirst, func(round int, node string) bool { return round == 2 && node == "goal" })
	if got := strings.Join(rounds[2], ","); got != "goal,software.stack,software.data" {
		t.Fatalf("round 3 is %s", got)
	}
	if missing, filled, total := ready(t, c, b); len(missing) != 0 || filled != total || len(rounds) != MaxRounds {
		t.Fatalf("after %v: missing %v, %d of %d", rounds, missing, filled, total)
	}
}

// One question left unanswered, in any round of any path, still lets the
// interview end ready within the rounds: every kind has a spare place.
func TestOneQuestionLeftUnansweredStillEndsReady(t *testing.T) {
	c := mustV1(t)
	for _, p := range kindPaths {
		_, rounds := walkOf(t, c, p, styleFirst, nil)
		for i, r := range rounds {
			if len(r) == 1 {
				// A round of one question cannot be left blank: the page
				// sends at least one answer.
				continue
			}
			for _, node := range r {
				round := i + 1
				b, got := walkOf(t, c, p, styleFirst, func(n int, id string) bool { return n == round && id == node })
				if missing, filled, total := ready(t, c, b); len(missing) != 0 || filled != total {
					t.Errorf("%s, %s left in round %d: rounds %v, missing %v", p.name, node, round, got, missing)
				}
			}
		}
	}
}
