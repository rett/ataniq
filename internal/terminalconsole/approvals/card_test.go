package approvals

import (
	"strings"
	"testing"
	"unicode/utf8"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/record"
)

// readAll moves the cursor down through the whole card.
func (r *rig) readAll() {
	r.t.Helper()
	r.scrollAll()
}

// cursorItem returns the flagged-path index (1-based) under the cursor.
func (r *rig) cursorItem() int { return r.m.cs.body[r.m.cs.cursor].item }

// goTo moves the cursor with the keys until it is on a row of flagged path
// i (0-based).
func (r *rig) goTo(i int) {
	r.t.Helper()
	for n := 0; n < len(r.m.cs.body)+2 && r.cursorItem() != i+1; n++ {
		if r.cursorItem() > i+1 {
			r.key("up")
		} else if r.cursorItem() == 0 && n > 0 && r.m.cs.cursor > r.firstRowOfItem(i+1) {
			r.key("up")
		} else {
			r.key("down")
		}
	}
	if r.cursorItem() != i+1 {
		r.t.Fatalf("could not reach flagged path %d", i)
	}
}

func (r *rig) firstRowOfItem(item int) int {
	for i, row := range r.m.cs.body {
		if row.item == item {
			return i
		}
	}
	return -1
}

// signAll reads the whole card and signs off every flagged path.
func (r *rig) signAll() {
	r.t.Helper()
	r.readAll()
	for i := range r.m.cs.c.Flagged {
		r.goTo(i)
		r.key("space")
	}
}

func TestCardLayoutAt80By24(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	rows := r.rows()
	if want := "APPROVAL 7Q4K  project-a / lead-a@lead-1 (runtime-a)"; strings.TrimSpace(rows[0]) != want {
		t.Fatalf("title row %q, want %q", rows[0], want)
	}
	if want := "feature finish name=feature-x  ->  develop    class: ref-move"; strings.TrimSpace(rows[1]) != want {
		t.Fatalf("verb row %q, want %q", rows[1], want)
	}
	if !strings.HasPrefix(rows[2], "──") {
		t.Fatalf("row 2 %q", rows[2])
	}
	if got, want := strings.TrimSpace(rows[20]), "[space] sign off  [a] approve  [x] deny  [d] diff  [l] lead  [esc] back"; got != want {
		t.Fatalf("hints row %q, want %q", got, want)
	}
	if !strings.Contains(rows[23], "Approvals are given only here.") {
		t.Fatalf("footer row %q", rows[23])
	}

	// Every row of the card, over all the screens that read it through.
	seen := map[string]bool{}
	cut := func() {
		for y, row := range r.rows() {
			if strings.Contains(row, "…") || strings.Contains(row, "[cut]") {
				t.Fatalf("a row is cut at 80 by 24 (row %d): %q", y, row)
			}
			if utf8.RuneCountInString(row) > 80 {
				t.Fatalf("a row is wider than 80: %q", row)
			}
			seen[strings.TrimSpace(row)] = true
		}
	}
	cut()
	for i := 0; i < len(r.m.cs.body)+2; i++ {
		r.key("down")
		cut()
	}
	for _, want := range []string{
		"Plan (host, token h-3fa9c2e1) · unverified lead data",
		"result M 0000000 built by the host",
		"Refs   refs/heads/develop  000000000000 -> 000000000000",
		"Merge  " + hex(2) + "  built by the host",
		"Diff   " + short(hex(1)) + ".." + short(hex(2)) + "   3 files, +612 -88, 48 KiB new",
		"Changed paths (3 of 3) · unverified lead data",
		"Binary 0  none", "Symlinks 0  none", "Mode changes 0  none", "Submodules 0  none",
		"Flagged paths (each needs its own sign-off) · unverified lead data",
		"[ ] exec-config  Makefile", "[ ] exec-config  scripts/go-tool.sh",
		"matches an execution-config rule",
		"Evidence", "suite  record v-0412  " + short(hex(7)),
		"Lead's note (lead-stated, not checked) · unverified lead data",
		"Wave 1 task SR merged, review r-91c2.",
		"end of the card",
	} {
		found := false
		for row := range seen {
			if strings.Contains(row, want) {
				found = true
			}
		}
		if !found {
			t.Fatalf("the card never showed %q", want)
		}
	}
}

func TestCardFooterSaysHowMuchIsBelow(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	n := len(r.m.cs.body)
	vh := r.m.cardRowsHeight()
	r.seen("▼ " + itoa(n-vh) + " more rows below")
	r.readAll()
	r.seen("end of the card")
	r.notSeen("more rows below")
}

func TestCardTheNoteIsLabelledAndEvenAnEmptyListShows(t *testing.T) {
	c := testCard("p-1")
	c.Note = card.LeadText{}
	c.Flagged = nil
	c.Evidence = nil
	b := backendWith(t, []card.Card{c}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.readAll()
	r.seen("Lead's note (lead-stated, not checked)", "Evidence", "none")
}

func TestCardReadingAndApproveOffReasons(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.seen("approve is off: the list of")
	r.seen("was not read to its end")
	r.key("a")
	r.seen("approve is off: the list of")
	if r.m.FieldOpen() {
		t.Fatal("the confirm field opened before the card was read")
	}
	r.readAll()
	r.seen("approve is off: 2 flagged paths are " + card.ReasonUnsigned)
	r.key("a")
	if r.m.FieldOpen() {
		t.Fatal("the confirm field opened with unsigned paths")
	}
}

// OC16, the console's part: Approve is not offered while a flagged path has
// no sign-off, and a sign-off can be made only on a flagged path.
func TestOC16SignOffs(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.readAll()
	// The cursor is on the last row, the note: not a flagged path.
	r.key("space")
	r.seen("sign-off is off: the cursor is not on a flagged path")
	if len(r.m.cs.reading.SignedOff) != 0 {
		t.Fatalf("a sign-off was made on a row that is no flagged path: %v", r.m.cs.reading.SignedOff)
	}
	r.goTo(0)
	r.key("space")
	r.seen("[x] exec-config  Makefile", "approve is off: 1 flagged path is "+card.ReasonUnsigned)
	r.key("space")
	r.seen("[ ] exec-config  Makefile")
	r.key("space")
	r.goTo(1)
	r.key("space")
	r.seen("approve is ready")
	r.key("a")
	if !r.m.FieldOpen() {
		t.Fatalf("the confirm field did not open:\n%s", r.screen())
	}
	// The decision carries exactly the card's flagged paths.
	if got := r.m.cs.c.SignedOff(r.m.cs.reading); len(got) != 2 {
		t.Fatalf("sign-offs %v", got)
	}
}

func TestCardWithNoFlaggedPathsOffersNoSignOff(t *testing.T) {
	c := testCard("p-1")
	c.Flagged = nil
	b := backendWith(t, []card.Card{c}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.readAll()
	r.key("space")
	r.seen("the cursor is not on a flagged path")
	r.notSeen("[space] sign off")
	r.key("up")
	r.seen("approve is ready")
}

func TestCardDOpensThePagerOnTheFullPatch(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	b.pages = func(id string, kind card.PageKind, n int) (card.Page, error) {
		if id != "p-1" || kind != card.PagePatch {
			t.Errorf("Page(%q, %v, %d)", id, kind, n)
		}
		return card.Page{Kind: card.PagePatch, N: n, Pages: 1, ReviewDigest: "review-a",
			Patch: []string{"diff --git a/x b/x", "+added \x1b[31mline"}, Cont: []bool{false, false}}, nil
	}
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.key("d")
	r.seen("PATCH", "diff --git a/x b/x", `+added \x1b[31mline`)
	if len(b.pageCalls) == 0 {
		t.Fatal("the backend was not asked for a page")
	}
	// The pager's own keys are the pager's; Esc closes it, and the card is
	// where it was.
	r.key("esc")
	r.seen("APPROVAL 7Q4K")
}

func TestCardWithoutADiffHasNoDiffKey(t *testing.T) {
	c := testCard("p-1")
	c.Diff, c.Lists, c.Merge, c.Flagged = nil, nil, nil, nil
	b := backendWith(t, []card.Card{c}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.notSeen("[d] diff")
	r.key("d")
	r.seen("diff is off: this card has no diff")
}

func TestPageSourceMapsReviewChanged(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	b.pages = func(string, card.PageKind, int) (card.Page, error) {
		return card.Page{}, &record.Error{Code: record.CodeReviewChanged, Detail: "x"}
	}
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.key("d")
	r.seen("reading ended: the review changed")
	// A page of another review is the same.
	b.pages = func(_ string, _ card.PageKind, n int) (card.Page, error) {
		return card.Page{Kind: card.PagePatch, N: n, Pages: 1, ReviewDigest: "another", Patch: []string{"x"}, Cont: []bool{false}}, nil
	}
	r.key("esc")
	r.key("d")
	r.seen("reading ended: the review changed")
}

func TestCardOpenErrorsAreOneLine(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	b.openErr = &record.Error{Code: record.CodeRecordInvalid, Detail: "detail text"}
	r := newRig(t, b, 80, 24)
	r.key("enter")
	r.seen("the card could not be opened (record-invalid)")
	r.notSeen("detail text")
	if r.m.CardOpen() {
		t.Fatal("a card is open after an error")
	}
}

func TestCardOpeningCanBeCancelled(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	cmd, _ := r.m.Update(keyMsg("enter"))
	r.seen("opening the card")
	r.key("esc")
	r.run(cmd)
	if r.m.CardOpen() {
		t.Fatal("the answer of a cancelled opening opened the card")
	}
}

func TestCardBelowTheSmallestWindow(t *testing.T) {
	for _, size := range [][2]int{{79, 24}, {80, 23}, {60, 20}, {40, 10}, {20, 7}, {10, 3}, {1, 1}} {
		b := backendWith(t, []card.Card{testCard("p-1")}, nil)
		r := newRig(t, b, size[0], size[1])
		r.key("enter")
		r.clk.Advance(1e9)
		if r.m.cs == nil {
			t.Fatalf("%v: no card", size)
		}
		r.readAll()
		r.key("a")
		if r.m.FieldOpen() {
			t.Fatalf("%v: the confirm field opened below 80 by 24", size)
		}
		if size[1] >= 4 && size[0] >= 60 {
			r.seen("approve is off: " + reasonSmall)
		}
		if r.code() != "" {
			t.Fatalf("%v: the code is drawn below 80 by 24", size)
		}
		r.notSeen("[a] approve")
		r.notSeen("[x] deny")
		// The card can still be read in pages.
		if size[1] >= 10 {
			r.key("pgdown")
		}
	}
	// At 80 by 24 itself Approve is offered.
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.seen("[a] approve")
}

func TestCardReadInPagesAtASmallSize(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 60, 12)
	r.openFirst()
	var all strings.Builder
	for i := 0; i < len(r.m.cs.body); i++ {
		all.WriteString(r.screen())
		r.key("pgdown")
	}
	for _, want := range []string{"Plan", "Submodules 0", "Lead's note"} {
		if !strings.Contains(all.String(), want) {
			t.Fatalf("paging at 60 by 12 never showed %q", want)
		}
	}
}

func TestCardResizeKeepsTheCursor(t *testing.T) {
	b := backendWith(t, []card.Card{testCard("p-1")}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.readAll()
	r.goTo(1)
	r.w, r.h = 50, 15
	r.run(r.m.SetSize(50, 15))
	if r.cursorItem() == 0 {
		t.Fatalf("the cursor left the flagged path after a resize:\n%s", r.screen())
	}
	r.run(r.m.SetSize(50, 15))
	r.seen("scripts/go-tool.sh")
}

func TestCardLongTextWrapsWithAMark(t *testing.T) {
	c := testCard("p-1")
	long := strings.Repeat("directory/", 12) + "file.go"
	c.Flagged[0].Path = card.NewLeadText(long)
	b := backendWith(t, []card.Card{c}, nil)
	r := newRig(t, b, 80, 24)
	r.openFirst()
	r.readAll()
	r.goTo(0)
	s := r.screen()
	if !strings.Contains(s, "↪") {
		t.Fatalf("a long path was not wrapped with a mark:\n%s", s)
	}
	var got strings.Builder
	in := false
	for _, row := range r.rows() {
		switch {
		case strings.Contains(row, "[ ] exec-config"):
			in = true
		case strings.Contains(row, "matches an execution-config rule"):
			in = false
		}
		if in {
			got.WriteString(strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(row), ">↪")))
		}
	}
	if !strings.Contains(strings.ReplaceAll(got.String(), " ", ""), long) {
		t.Fatalf("the long path is not whole on the screen: %q", got.String())
	}
}
