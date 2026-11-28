package approvals

import (
	"strings"
	"testing"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/record"
)

func TestBindingTagOnPendingAndRecentCards(t *testing.T) {
	pending, recent := twoCards()
	pending.BindingSHA256 = strings.Repeat("1", 64)
	recent.BindingSHA256 = strings.Repeat("2", 64)
	recent.After.State = record.StateDenied
	pending.Plan.Lines = lead("plan\x1b[2J\u202e")
	b := backendWith(t, []card.Card{pending}, []card.Item{recent.Item()})
	b.cards[recent.Title.ID] = recent
	b.deciderO = false
	r := newRig(t, b, 120, 40)
	for _, c := range []card.Card{pending, recent} {
		tag := "Binding tag: " + card.BindingTag(c.BindingSHA256)
		r.seen("name=" + c.Verb.Feature + "  " + tag)
		r.key("enter")
		r.seen(tag)
		r.notSeen("[a] approve", "[x] deny")
		checkCells(t, "open card", r.draw())
		if c.Title.ID == pending.Title.ID {
			r.seen(`plan\x1b[2J\u{202E}`)
		}
		r.key("esc")
		r.key("down")
	}
	if len(b.opens) != 2 || len(b.decisions) != 0 {
		t.Fatalf("opens %v, decisions %v", b.opens, b.decisions)
	}
}

func TestBindingTagMissingFromListing(t *testing.T) {
	c := testCard("p-1")
	c.BindingSHA256 = strings.Repeat("a", 64)
	b := backendWith(t, []card.Card{c}, nil)
	b.listing.Pending[0].BindingSHA256 = ""
	r := newRig(t, b, 80, 24)
	r.seen("open for binding tag")
	if len(b.opens) != 0 {
		t.Fatalf("reading the list opened %v", b.opens)
	}
	r.openFirst()
	r.seen("Binding tag: " + card.BindingTag(c.BindingSHA256))
}

func TestCF07BindingTagListRow(t *testing.T) {
	b := newBackend(t)
	b.listing.Pending = []card.Item{
		{Verb: "feature finish", Feature: "first", BindingSHA256: "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"},
		{Verb: "feature finish", Feature: "second"},
	}
	r := newRig(t, b, 120, 24)
	r.seen("name=first  Binding tag: marsh candle koala lotus", "name=second  open for binding tag")
	if len(b.opens) != 0 {
		t.Fatalf("drawing the list opened %v", b.opens)
	}
}

func TestBindingTagMalformedIsInert(t *testing.T) {
	c := testCard("p-1")
	c.BindingSHA256 = strings.Repeat("a", 59) + "\x1b[2J!"
	r := newRig(t, backendWith(t, []card.Card{c}, nil), 80, 24)
	for _, view := range []string{"list", "open card"} {
		if view == "open card" {
			r.openFirst()
		}
		r.seen("Binding tag unavailable")
		r.notSeen("[2J!")
		checkCells(t, view, r.draw())
	}
}
