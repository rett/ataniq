package approvals

import (
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/record"
)

func twoCards() (card.Card, card.Card) {
	a := testCard("p-1")
	b := testCard("p-2")
	b.Title.Project = "project-b"
	b.Verb.Feature = "feature-y"
	b.After.Held = true
	return a, b
}

func recentItems() []card.Item {
	return []card.Item{
		{ID: "p-0", Verb: "feature finish", Feature: "old", Project: "project-a", Asked: t0, State: record.StateApplied, Line: "applied: develop moved"},
		{ID: "p-9", Verb: "feature finish", Feature: "other", Project: "project-a", Asked: t0, State: record.StateDenied},
		{ID: "u-1", Verb: "feature finish", Feature: "third", Project: "project-b", Asked: t0, State: record.StateApprovedByRule, Line: "used under rule first"},
	}
}

func TestListShowsPendingRecentRulesAndCount(t *testing.T) {
	a, held := twoCards()
	b := backendWith(t, []card.Card{a, held}, recentItems())
	b.listing.Rules = []card.Rule{{Project: "project-a", Name: "first", Text: "small changes only", On: true, Uses: 3}}
	r := newRig(t, b, 80, 24)
	rows := r.rows()
	if !strings.Contains(rows[0], "APPROVALS") || !strings.Contains(rows[0], "2 pending") {
		t.Fatalf("header row %q", rows[0])
	}
	if !strings.Contains(rows[1], "2 of 7 requests would have met the First template") {
		t.Fatalf("count row %q", rows[1])
	}
	r.seen(
		"Pending (2)", "name=feature-x", "name=feature-y",
		"[decided past the limit]",
		"Recent decisions, uses and refusals (3)", "applied", "denied", "approved-by-rule", "applied: develop", "under rule first",
		"Standing rules, read-only (1)", "first  on  used 3 times: small changes only",
		"Approvals are given only here.",
	)
	// The held mark stands on the held card's row only.
	for _, row := range rows {
		if strings.Contains(row, "decided past the limit") && !strings.Contains(row, "feature-y") {
			t.Fatalf("the held mark is on another row: %q", row)
		}
	}
	r.notSeen("sample cards")
}

func TestListSampleLabel(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	b.listing.Sample = true
	r := newRig(t, b, 80, 24)
	if !strings.Contains(r.rows()[0], "sample cards: a decision changes nothing") {
		t.Fatalf("header row %q", r.rows()[0])
	}
}

func TestListEmptyAndError(t *testing.T) {
	b := newBackend(t)
	r := newRig(t, b, 80, 24)
	r.seen("APPROVALS  0 pending", "From the first card: no request counted yet.", "Pending (0)", "none")
	b.listErr = &record.Error{Code: record.CodeRecordInvalid, Detail: "secret detail"}
	r.run(r.m.Refresh())
	r.seen("the list could not be read (record-invalid)")
	r.notSeen("secret detail")
}

func TestListKeysAndLimit(t *testing.T) {
	a, held := twoCards()
	b := backendWith(t, []card.Card{a, held}, recentItems())
	r := newRig(t, b, 80, 24)
	cursorRow := func() string {
		for _, row := range r.rows() {
			if strings.HasPrefix(row, ">") {
				return row
			}
		}
		t.Fatalf("no cursor on the screen:\n%s", r.screen())
		return ""
	}
	if !strings.Contains(cursorRow(), "feature-x") {
		t.Fatalf("cursor row %q", cursorRow())
	}
	r.key("L")
	if r.last != (Action{Kind: Limit, Project: "project-a"}) {
		t.Fatalf("L reported %+v", r.last)
	}
	r.key("j")
	if !strings.Contains(cursorRow(), "feature-y") {
		t.Fatalf("cursor row %q", cursorRow())
	}
	r.key("L")
	if r.last != (Action{Kind: Limit, Project: "project-b"}) {
		t.Fatalf("L on the second row reported %+v", r.last)
	}
	r.key("down")
	r.key("down")
	if !strings.Contains(cursorRow(), "denied") {
		t.Fatalf("cursor row %q", cursorRow())
	}
	r.key("up")
	r.key("k")
	r.key("k")
	r.key("k")
	if !strings.Contains(cursorRow(), "feature-x") {
		t.Fatalf("the cursor passed the first row: %q", cursorRow())
	}
	for i := 0; i < 9; i++ {
		r.key("j")
	}
	if !strings.Contains(cursorRow(), "third") {
		t.Fatalf("the cursor passed the last row: %q", cursorRow())
	}
	// Keys that are not the view's are left alone.
	for _, k := range []string{"q", "a", "x", "d", "l", "tab"} {
		if act := r.key(k); act != (Action{}) {
			t.Fatalf("%q reported %+v", k, act)
		}
	}
	// Esc leaves the view, and the owner's look is recorded.
	if act := r.key("esc"); act.Kind != Close {
		t.Fatalf("esc reported %+v", act)
	}
	if len(b.looked) != 1 {
		t.Fatalf("Looked was called %d times", len(b.looked))
	}
}

func TestListLimitWithoutARow(t *testing.T) {
	r := newRig(t, newBackend(t), 80, 24)
	if act := r.key("L"); act.Kind != None {
		t.Fatalf("L with no row reported %+v", act)
	}
	r.seen(reasonNoLimitRow)
	r.key("enter")
	r.seen("there is no card to open")
}

func TestListMarksCardsSinceHeLooked(t *testing.T) {
	a, held := twoCards()
	held.Title.Asked = t0.Add(time.Hour)
	b := backendWith(t, []card.Card{a, held}, nil)
	b.listing.Pending[1].Asked = t0.Add(time.Hour)
	b.listing.LookedAt = t0.Add(time.Minute)
	r := newRig(t, b, 80, 24)
	var marked []string
	for _, row := range r.rows() {
		if strings.Contains(row, "new ") {
			marked = append(marked, row)
		}
	}
	if len(marked) != 1 || !strings.Contains(marked[0], "feature-y") {
		t.Fatalf("new marks: %q", marked)
	}
}

func TestListReadOnlyGivesTheReason(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	b.deciderO, b.deciderW = false, "another console holds the decider's place"
	r := newRig(t, b, 80, 24)
	r.seen("read-only: another console holds the decider's place")
}

func TestListFooterWhileTheDeciderIsUnknown(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := &rig{t: t, b: b, clk: &fakeClock{t: t0}, w: 80, h: 24}
	r.m = New(Options{Backend: b, Now: r.clk.Now})
	r.m.SetSize(80, 24)
	r.seen(reasonChecking, "reading the list")
}

func TestListScrollsToTheCursor(t *testing.T) {
	var cards []card.Card
	for i := 0; i < 40; i++ {
		c := testCard("p-" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
		cards = append(cards, c)
	}
	b := backendWith(t, cards, nil)
	r := newRig(t, b, 80, 24)
	for i := 0; i < 39; i++ {
		r.key("j")
	}
	var cursor string
	for _, row := range r.rows() {
		if strings.HasPrefix(row, ">") {
			cursor = row
		}
	}
	if cursor == "" {
		t.Fatalf("the cursor is off the screen:\n%s", r.screen())
	}
	r.run(r.m.Refresh())
	if r.m.cursor != 39 {
		t.Fatalf("a refresh moved the cursor to %d", r.m.cursor)
	}
}

// The backend interface is declared as the seams note writes it.
var _ Backend = (*fakeBackend)(nil)

func TestVerdictHooksReturnNothingYet(t *testing.T) {
	if got := verdictLines(testCard("p-1"), true); got != nil {
		t.Fatalf("verdictLines returned %q", got)
	}
	if got := agreementLine(card.Listing{Count: card.Count{Requests: 1}}); got != "" {
		t.Fatalf("agreementLine returned %q", got)
	}
}
