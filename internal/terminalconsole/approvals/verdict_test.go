package approvals

import (
	"strings"
	"testing"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/record"
)

// block makes the advice of two runs, with strings that a surface must draw
// as they are and strings it must clean.
func block(first, second bool) *card.VerdictBlock {
	return &card.VerdictBlock{Runs: [2]card.Run{
		{ID: "run-1", Provider: "provider-a", Model: "model-a", Approve: first,
			Checks: []card.CheckResult{{Kind: card.CheckPlan, Pass: true}, {Kind: card.CheckScope, Pass: first}}},
		{ID: "run-2", Provider: "provider-b", Model: "model-b", Approve: second,
			Checks: []card.CheckResult{{Kind: card.CheckSuite, Pass: true}, {Kind: card.CheckKind(99), Pass: true}}},
	}}
}

func TestVerdictLinesNothingBeforeTheDecision(t *testing.T) {
	c := testCard("p-1")
	c.Verdicts = block(true, true)
	if got := verdictLines(c, false); got != nil {
		t.Fatalf("an undecided card gave %q", got)
	}
	if got := verdictLines(testCard("p-2"), true); got != nil {
		t.Fatalf("a card with no verdicts gave %q", got)
	}
}

// The refused line is not a verdict line: it is drawn in the header, from
// Card.Refused, so verdictLines gives nothing for an undecided card even when
// the block says Refused.
func TestVerdictLinesNothingForARefusedUndecidedCard(t *testing.T) {
	c := testCard("p-1")
	v := block(false, false)
	v.Refused = true
	c.Verdicts = v
	if got := verdictLines(c, false); got != nil {
		t.Fatalf("lines %q", got)
	}
}

// The header of an undecided card says in one line that a run refused, with
// no verdict detail; with Refused false the line is not there.
func TestVerdictRefusedLineInTheHeaderOfAnUndecidedCard(t *testing.T) {
	for _, refused := range []bool{true, false} {
		c := testCard("p-1")
		c.Refused = refused
		b := backendWith(t, []card.Card{c}, nil)
		r := newRig(t, b, 100, 40)
		r.openFirst()
		hidden := []string{"provider-a", "model-a", "run-1", "approved", "rejected", "agreed with you", verdictAdvice}
		if refused {
			r.bodyHas(refusedLine)
			if first := r.m.cs.body[0]; !strings.Contains(bodyRowText(first), refusedLine) {
				t.Errorf("the line is not first: %q", bodyRowText(first))
			}
		} else {
			hidden = append(hidden, refusedLine)
		}
		r.scrollAll()
		r.notSeen(hidden...)
		for _, row := range r.m.cs.body {
			for _, bad := range hidden {
				if strings.Contains(bodyRowText(row), bad) {
					t.Fatalf("refused %v: the body holds %q: %q", refused, bad, bodyRowText(row))
				}
			}
		}
	}
}

// A backend that puts Refused only in the block of an undecided card, not in
// the card's own mark, gets no line: the view draws the mark alone.
func TestVerdictRefusedInTheBlockAloneIsNotDrawnBeforeTheDecision(t *testing.T) {
	c := testCard("p-1")
	v := block(true, true)
	v.Refused = true
	c.Verdicts = v
	b := backendWith(t, []card.Card{c}, nil)
	r := newRig(t, b, 100, 40)
	r.openFirst()
	r.scrollAll()
	r.notSeen(refusedLine, "provider-a", "run-1")
}

func TestVerdictLinesAfterTheDecisionShowBothRunsAsAdvice(t *testing.T) {
	c := testCard("p-1")
	c.Verdicts = block(true, false)
	got := strings.Join(verdictLines(c, true), "\n")
	for _, want := range []string{
		verdictAdvice, "run 1  run-1  provider-a / model-a  approved  (plan pass, scope pass)",
		"run 2  run-2  provider-b / model-b  rejected  (suite pass)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "unknown") {
		t.Errorf("a check outside the closed list was drawn:\n%s", got)
	}
}

func TestVerdictLinesCleanAndBoundData(t *testing.T) {
	c := testCard("p-1")
	v := block(true, true)
	v.Runs[0].ID = "id\x1b[2J\x07"
	v.Runs[0].Provider = "pro‮vider"
	v.Runs[0].Model = strings.Repeat("m", 500)
	c.Verdicts = v
	for _, l := range verdictLines(c, true) {
		for _, r := range l {
			if r < ' ' || r == 0x7f || r == 0x202e {
				t.Fatalf("raw byte %q in %q", r, l)
			}
		}
		if len(l) > 300 {
			t.Fatalf("an unbounded line of %d bytes", len(l))
		}
	}
}

func recentItem(state record.State, first, second bool) card.Item {
	return card.Item{ID: "x", State: state, Verdicts: block(first, second)}
}

func TestVerdictAgreementLine(t *testing.T) {
	if got := agreementLine(card.Listing{}); got != "" {
		t.Errorf("an empty listing gave %q", got)
	}
	l := card.Listing{Recent: []card.Item{
		{ID: "no-verdicts", State: record.StateApplied},
		recentItem(record.StateApplied, true, true),
		recentItem(record.StateDenied, false, false),
		recentItem(record.StateApproved, true, false),
		recentItem(record.StateExpired, true, true),
	}}
	l.Pending = []card.Item{recentItem("", true, true)}
	got := agreementLine(l)
	if !strings.Contains(got, "agreed with you on 2 of 3") {
		t.Errorf("line %q", got)
	}
	l.Recent = l.Recent[:1]
	if got := agreementLine(l); got != "" {
		t.Errorf("no verdicts gave %q", got)
	}
}

// A backend that wrongly supplies verdicts on a pending card must show none
// of them, on the list or on the card. The after-the-decision block is drawn
// on an undecided card too (a held card, and one whose last decision the door
// refused); there the view decides whether verdicts show.
func TestVerdictNeverVisibleOnAnUndecidedCard(t *testing.T) {
	for _, after := range []card.After{
		{},
		{Held: true},
		{State: record.StatePending, Line: "the door refused the last decision"},
	} {
		c := testCard("p-1")
		c.After = after
		c.Verdicts = block(true, true)
		b := backendWith(t, []card.Card{c}, nil)
		b.listing.Pending[0].Verdicts = c.Verdicts
		r := newRig(t, b, 100, 40)
		hidden := []string{"provider-a", "model-a", "run-1", "approved", "agreed with you", verdictAdvice, refusedLine}
		r.notSeen(hidden...)
		r.openFirst()
		r.scrollAll()
		for i := 0; i < len(r.m.cs.body); i++ {
			r.key("up")
			r.notSeen(hidden...)
		}
		for _, row := range r.m.cs.body {
			for _, bad := range hidden {
				if strings.Contains(bodyRowText(row), bad) {
					t.Fatalf("the body holds %q: %q", bad, bodyRowText(row))
				}
			}
		}
	}
}

// After the decision the card shows both runs, and the list shows the count.
func TestVerdictShownAfterTheDecision(t *testing.T) {
	c := testCard("p-1")
	c.After = card.After{State: record.StateApplied, Line: "applied: develop moved"}
	c.Verdicts = block(true, false)
	b := backendWith(t, nil, []card.Item{c.Item()})
	b.cards["p-1"] = c
	r := newRig(t, b, 100, 40)
	r.seen("verdict runs agreed with you on 0 of 1")
	r.openFirst()
	r.bodyHas("provider-a / model-a")
	r.bodyHas("provider-b / model-b")
	r.bodyHas(verdictAdvice)
}

// bodyRowText is the text of one drawn row.
func bodyRowText(r row) string {
	var line strings.Builder
	for _, s := range r.segs {
		line.WriteString(s.s)
	}
	return line.String()
}

// A card decided in place is read again, so that its verdict lines show and
// the header line of a card that exists because a run refused goes, without
// the card being opened a second time.
func TestVerdictShownOnTheOpenCardRightAfterTheDecision(t *testing.T) {
	c := testCard("p-1")
	c.Refused = true
	b := backendWith(t, []card.Card{c}, nil)
	b.decide = func(card.Decision) (card.Outcome, error) {
		// The backend now holds the decided card, as card.New makes it: its
		// verdicts, and no refused mark.
		d := c
		d.Refused = false
		d.After = card.After{State: record.StateDenied, Line: "denied"}
		d.Verdicts = block(true, false)
		b.mu.Lock()
		b.cards["p-1"] = d
		b.mu.Unlock()
		return card.Outcome{State: record.StateDenied, Line: "denied"}, nil
	}
	r := newRig(t, b, 100, 40)
	r.openFirst()
	r.bodyHas(refusedLine)
	r.key("x")
	r.key("enter")
	if len(b.opens) != 2 {
		t.Fatalf("the card was opened %d times, not once and read again once", len(b.opens))
	}
	r.bodyHas(verdictAdvice)
	r.bodyHas("provider-a / model-a")
	r.bodyHas("provider-b / model-b")
	for _, row := range r.m.cs.body {
		if strings.Contains(bodyRowText(row), refusedLine) {
			t.Fatalf("the decided card still says a run refused: %q", bodyRowText(row))
		}
	}
	if r.m.cs.outcome != "denied" || r.m.cs.cursor != 0 {
		t.Fatalf("the outcome or the cursor changed: %q %d", r.m.cs.outcome, r.m.cs.cursor)
	}
}

// A card read again that is not decided on the backend's side shows nothing
// of the verdicts: the view takes them only from a decided card.
func TestVerdictNotTakenFromARereadCardThatIsNotDecided(t *testing.T) {
	c := testCard("p-1")
	b := backendWith(t, []card.Card{c}, nil)
	b.decide = func(card.Decision) (card.Outcome, error) {
		d := c
		d.Verdicts = block(true, false) // a wrong backend: verdicts, not decided
		b.mu.Lock()
		b.cards["p-1"] = d
		b.mu.Unlock()
		return card.Outcome{State: record.StateDenied, Line: "denied"}, nil
	}
	r := newRig(t, b, 100, 40)
	r.openFirst()
	r.key("x")
	r.key("enter")
	for _, row := range r.m.cs.body {
		if strings.Contains(bodyRowText(row), "provider-a") {
			t.Fatalf("verdicts of an undecided card were taken: %q", bodyRowText(row))
		}
	}
}
