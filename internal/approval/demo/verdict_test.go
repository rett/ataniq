package demo

import (
	"strings"
	"testing"

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
