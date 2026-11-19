package approvals

import (
	"errors"
	"testing"

	"ataniq/internal/approval/card"
	"ataniq/internal/terminalconsole/pager"
)

// The patch adapter takes in only a page of the patch, the one asked for, of
// the review the card was made from. Any other page is a review that changed.
func TestPatchPageSourceRefusesAPageThatIsNotTheOneAsked(t *testing.T) {
	good := card.Page{Kind: card.PagePatch, N: 1, Pages: 2, ReviewDigest: "review-a", Patch: []string{"x"}, Cont: []bool{false}}
	cases := []struct {
		name string
		edit func(p *card.Page)
		ok   bool
	}{
		{"as asked", func(*card.Page) {}, true},
		{"no digest", func(p *card.Page) { p.ReviewDigest = "" }, false},
		{"another digest", func(p *card.Page) { p.ReviewDigest = "review-b" }, false},
		{"another kind", func(p *card.Page) { p.Kind = card.PagePaths }, false},
		{"another number", func(p *card.Page) { p.N = 0 }, false},
	}
	for _, c := range cases {
		b := newBackend(t)
		p := good
		c.edit(&p)
		b.pages = func(string, card.PageKind, int) (card.Page, error) { return p, nil }
		b.inCmd = true
		lines, _, pages, err := pageSource{b: b, id: "p-1", digest: "review-a"}.Page(1)
		b.inCmd = false
		if c.ok {
			if err != nil || len(lines) != 1 || pages != 2 {
				t.Fatalf("%s: lines %v pages %d err %v", c.name, lines, pages, err)
			}
			continue
		}
		if !errors.Is(err, pager.ErrReviewChanged) {
			t.Fatalf("%s: err %v, want a review that changed", c.name, err)
		}
	}
}
