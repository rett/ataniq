package card

import (
	"fmt"
	"time"

	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/record"
)

// Decision is the owner's answer to one card, as a surface hands it to a
// backend. It holds only what the owner did. It has no confirm code, which
// never leaves the surface, and nothing of what the daemon fills in when it
// writes the decision's record.
type Decision struct {
	// ID is the card's id, and BindingSHA256 the digest of the binding
	// the card showed.
	ID            string
	BindingSHA256 string
	// Approve is true for an approve and false for a deny.
	Approve bool
	// SignedOff are the flagged paths the owner signed off, in the order
	// of the card's flagged paths; see [Card.SignedOff].
	SignedOff []record.SignOff
	// Reason is why the owner denied. It may be empty, and an approve has
	// none.
	Reason string
}

// Outcome is what a backend answers to a decision.
type Outcome struct {
	// State is the state of the pending record after the decision.
	State record.State
	// Line is one line that says what happened, written by the host.
	Line string
	// Held says that the approve was past the daily limit: it was
	// recorded and not applied, and the card stays pending.
	Held bool
}

// Listing is what the approvals list shows.
type Listing struct {
	// Pending are the cards that wait for a decision, and Recent the
	// recent decisions and outcomes.
	Pending []Item
	Recent  []Item
	// Rules are the standing rules.
	Rules []Rule
	// Count is the count from the first card.
	Count Count
	// Sample says that the cards are samples: a decision changes nothing.
	Sample bool
	// LookedAt is when the owner last looked at the list. The zero time
	// means never.
	LookedAt time.Time
}

// Item is one row of the approvals list. Like a card, it holds no lead text
// as a plain string: Feature passed the grammar, and the rest is the host's.
type Item struct {
	// ID is the card's id, and BindingSHA256 the digest of its binding.
	ID            string
	BindingSHA256 string
	// Project is the project the request is for.
	Project string
	// Verb is the verb, and Feature the feature's name for a feature
	// finish; see VerbLine.Feature.
	Verb    string
	Feature string
	// Asked is when the host accepted the request, and Expires when it
	// ends unanswered.
	Asked   time.Time
	Expires time.Time
	// State is the state of the pending record (empty means pending), and
	// Line the outcome line, written by the host.
	State record.State
	Line  string
	// Held is the held mark: an approve of the card was made past the
	// daily limit. A surface draws [HeldLabel].
	Held bool
	// Verdicts are the verdicts, nil until the card is decided.
	Verdicts *VerdictBlock
}

// Item returns the card's row for the approvals list.
func (c Card) Item() Item {
	return Item{
		ID:            c.Title.ID,
		BindingSHA256: c.BindingSHA256,
		Project:       c.Title.Project,
		Verb:          c.Verb.Name,
		Feature:       c.Verb.Feature,
		Asked:         c.Title.Asked,
		Expires:       c.Title.Expires,
		State:         c.After.State,
		Line:          c.After.Line,
		Held:          c.After.Held,
		Verdicts:      c.Verdicts,
	}
}

// Rule is one standing rule as the list shows it, read-only: a rule under
// which a request is approved without a card.
type Rule struct {
	// Project is the project the rule is for.
	Project string
	// Name is the rule's short name, and Text the rule in plain words.
	// Both are written by the host.
	Name string
	Text string
	// On says whether the rule is switched on.
	On bool
	// Uses counts the requests approved under the rule.
	Uses int
}

// Count is the count from the first card: of the requests seen so far, how
// many would have met the terms of the First template, and so would have
// needed no card under a standing approval. It decides nothing.
type Count struct {
	// Requests is the number of requests counted.
	Requests int
	// Met is how many of them would have met the terms.
	Met int
}

// Add counts one more request with the answer of the limits package for it.
func (c *Count) Add(answer limits.Counted) {
	c.Requests++
	if answer.Met {
		c.Met++
	}
}

// PageKind says what a page holds.
type PageKind int

// The kinds of page.
const (
	// PagePaths is a page of the whole path list of a review.
	PagePaths PageKind = iota + 1
	// PagePatch is a page of the full patch.
	PagePatch
)

// String returns the kind's name.
func (k PageKind) String() string {
	switch k {
	case PagePaths:
		return "paths"
	case PagePatch:
		return "patch"
	}
	return "unknown"
}

// Page is one page of a card's path list or patch, as a backend returns it.
// Pages are numbered from 0, and the last page has N == Pages-1.
type Page struct {
	// Kind says which of the two the page is part of.
	Kind PageKind
	// N is the page's number and Pages the number of pages.
	N     int
	Pages int
	// ReviewDigest is the digest of the review the page was read from. A
	// page counts for a card only when it is the card's Diff.ReviewDigest.
	ReviewDigest string
	// Paths are the rows of a page of paths; [RowOf] makes each ready to
	// draw.
	Paths []record.PathChange
	// Patch are the lines of a page of the patch. They are raw bytes of a
	// lead, not yet escaped: the pager is the one place that escapes
	// them. Cont[i] is true when Patch[i] continues in the next piece: a
	// line too long for a page is carried in pieces and joined before it
	// is wrapped.
	Patch []string
	Cont  []bool
}

// Format writes the page's kind, number, page count and the number of rows
// and patch lines it holds, for every verb of the fmt package, and none of
// its content. A Page carries raw bytes of a lead in Paths and Patch, so
// printing one, or a value that holds one, must not write them. The fields
// are still there for a caller that reads them.
func (p Page) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "card.Page{kind=%s n=%d pages=%d rows=%d lines=%d}",
		p.Kind, p.N, p.Pages, len(p.Paths), len(p.Patch))
}
