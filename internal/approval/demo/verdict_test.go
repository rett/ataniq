package demo

import (
	"slices"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/record"
)

func TestVerdictSamplesAreTwoDecidedCards(t *testing.T) {
	b, _ := newBackend(t)
	agreed := open(t, b, "ap-sample-verdict-agreed")
	differed := open(t, b, "ap-sample-verdict-differed")
	for _, c := range []card.Card{agreed, differed} {
		if !c.Decided() || c.Verdicts == nil {
			t.Fatalf("%s: decided %v, verdicts %v", c.Title.ID, c.Decided(), c.Verdicts)
		}
		for _, r := range c.Verdicts.Runs {
			for _, s := range []string{r.ID, r.Provider, r.Model} {
				if s == "" || card.VerdictText(s) != s {
					t.Errorf("%s: string %q", c.Title.ID, s)
				}
			}
		}
	}
	l, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	if a, n := itemOf(l, agreed.Title.ID).Agreed(); !a || !n {
		t.Errorf("agreed sample: agreed %v counted %v", a, n)
	}
	if a, n := itemOf(l, differed.Title.ID).Agreed(); a || !n {
		t.Errorf("differed sample: agreed %v counted %v", a, n)
	}
}

func TestVerdictSamplesStayOutOfPendingAndNameNoVendor(t *testing.T) {
	b, c0 := newBackend(t)
	l, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range l.Pending {
		if it.Verdicts != nil {
			t.Errorf("pending %s holds verdicts", it.ID)
		}
	}
	for _, s := range verdictSamples(c0.t) {
		if s.state != record.StateApproved || s.verdicts == nil {
			t.Errorf("sample %s: state %q", s.pending.ID, s.state)
		}
		for _, r := range s.verdicts.Runs {
			if !strings.HasPrefix(r.Provider, "provider-") || !strings.HasPrefix(r.Model, "model-") {
				t.Errorf("not neutral: %q %q", r.Provider, r.Model)
			}
		}
	}
}

// The two decided samples carry a time of decision, so that Recent sorts them
// like cards decided during the run: the later decision first, and a card
// decided now ahead of both.
func TestVerdictSamplesHaveATimeOfDecision(t *testing.T) {
	b, c := newBackend(t)
	at := map[string]time.Time{}
	for _, e := range b.cards {
		if e.state != "" {
			at[e.pending.ID] = e.decidedAt
		}
	}
	agreed, differed := at["ap-sample-verdict-agreed"], at["ap-sample-verdict-differed"]
	if agreed.IsZero() || differed.IsZero() || !agreed.Before(c.t) || !differed.Before(c.t) {
		t.Fatalf("times of decision %v and %v, now %v", agreed, differed, c.t)
	}
	if !agreed.After(differed) {
		t.Errorf("the agreed sample was decided at %v, not after %v", agreed, differed)
	}
	b.Activity(c.t)
	k := open(t, b, "ap-sample-clean")
	if _, err := b.Decide(card.Decision{ID: k.Title.ID, BindingSHA256: k.BindingSHA256, Approve: true}); err != nil {
		t.Fatal(err)
	}
	l, _ := b.List()
	var ids []string
	for _, it := range l.Recent {
		ids = append(ids, it.ID)
	}
	want := []string{"ap-sample-clean", "ap-sample-verdict-agreed", "ap-sample-verdict-differed"}
	if !slices.Equal(ids, want) {
		t.Errorf("recent %v, want %v", ids, want)
	}
}
