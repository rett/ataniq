package pager

import (
	"errors"
	"fmt"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// numbered returns n short lines, "l000" and so on.
func numbered(n int) []string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("l%03d", i)
	}
	return lines
}

func TestKeysScroll(t *testing.T) {
	r := newRig(t, 40, 12, serve(paginate(numbered(100), 30, 0)))
	for _, step := range []struct {
		key  tea.KeyPressMsg
		want string
	}{
		{keyJ, "l001"}, {keyDown, "l002"}, {keyK, "l001"}, {keyUp, "l000"},
		{keyUp, "l000"}, {keyK, "l000"}, {keyPgUp, "l000"},
		{keyPgDn, "l009"}, {keyPgDn, "l018"}, {keyPgUp, "l009"},
		{keyEnd, "l050"}, {keyEnd, "l080"}, {keyEnd, "l090"}, {keyEnd, "l090"},
		{keyJ, "l090"}, {keyDown, "l090"}, {keyPgDn, "l090"},
		{keyK, "l089"}, {keyHome, "l000"}, {keyHome, "l000"},
		// A lock state reported with a key does not change the key.
		{tea.KeyPressMsg{Code: 'j', Text: "j", Mod: tea.ModNumLock}, "l001"},
		{tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModCapsLock | tea.ModScrollLock}, "l002"},
	} {
		r.press(step.key)
		if got := r.first(); got != step.want {
			t.Fatalf("after %s the first row is %q, want %q", step.key.String(), got, step.want)
		}
	}
}

// Esc is reported to the parent as Close. The pager itself changes nothing,
// reads nothing, and returns no command.
func TestEscAsksTheParentToClose(t *testing.T) {
	r := newRig(t, 40, 12, serve(paginate(numbered(100), 30, 0)))
	r.press(keyPgDn)
	before, calls := r.first(), len(r.src.calls)
	cmd, act := r.m.Update(keyEsc)
	if act != Close || cmd != nil {
		t.Fatalf("Esc: action %d, command nil %v", act, cmd == nil)
	}
	if r.first() != before || len(r.src.calls) != calls {
		t.Fatal("Esc changed the pager")
	}
	// Before anything is read, and in a region too small to read in.
	fresh := New(r.src, Options{})
	if cmd, act := fresh.Update(keyEsc); act != Close || cmd != nil {
		t.Fatalf("Esc on a pager that read nothing: action %d", act)
	}
	r.size(3, 1)
	if cmd, act := r.m.Update(keyEsc); act != Close || cmd != nil {
		t.Fatalf("Esc in a small region: action %d", act)
	}
}

// The pager handles its scroll keys and Esc, and nothing else: no other
// key and no other message changes it, returns a command or asks the parent
// for anything. So it can never end the program.
func TestOtherKeysAndMessagesAreLeftAlone(t *testing.T) {
	r := newRig(t, 40, 12, serve(paginate(numbered(100), 30, 0)))
	r.press(keyPgDn)
	before, calls := r.first(), len(r.src.calls)
	msgs := []tea.Msg{
		tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl},
		tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl},
		tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl},
		tea.KeyPressMsg{Code: 'j', Text: "J", Mod: tea.ModShift},
		tea.KeyPressMsg{Code: 'j', Mod: tea.ModAlt},
		tea.KeyPressMsg{Code: tea.KeyDown, Mod: tea.ModShift},
		tea.KeyPressMsg{Code: tea.KeyEnd, Mod: tea.ModCtrl},
		tea.KeyPressMsg{Code: tea.KeyEscape, Mod: tea.ModAlt},
		tea.KeyPressMsg{Code: tea.KeyEnter},
		tea.KeyPressMsg{Code: tea.KeyTab},
		tea.KeyPressMsg{Code: tea.KeySpace, Text: " "},
		tea.KeyPressMsg{Code: tea.KeyF12},
		tea.KeyReleaseMsg{Code: 'j', Text: "j"},
		tea.KeyReleaseMsg{Code: tea.KeyEscape},
		tea.PasteMsg{Content: "jjjj\x1b"},
		tea.WindowSizeMsg{Width: 10, Height: 5},
		tea.FocusMsg{},
		tea.BlurMsg{},
		tea.QuitMsg{},
		nil,
		"j",
	}
	for _, c := range "qadxlgL?rGhH0123456789:/\\ny" {
		msgs = append(msgs, tea.KeyPressMsg{Code: c, Text: string(c)})
	}
	for _, msg := range msgs {
		cmd, act := r.m.Update(msg)
		if cmd != nil || act != None {
			t.Fatalf("%T %v: action %d, command nil %v", msg, msg, act, cmd == nil)
		}
		if r.first() != before || len(r.src.calls) != calls {
			t.Fatalf("%T %v changed the pager", msg, msg)
		}
	}
}

// The end is reached only when the last piece of the last page was read
// and its last row is on the screen.
func TestReached(t *testing.T) {
	t.Run("only with the last row on the screen", func(t *testing.T) {
		r := newRig(t, 80, 12, serve(paginate(numbered(25), 10, 0)))
		// Every page is read ahead of the screen, and that is not enough.
		if got := len(r.src.calls); got != 3 {
			t.Fatalf("%d pages read at the top, want all 3", got)
		}
		for i := 0; ; i++ {
			atEnd := r.bottom() == "l024"
			if r.m.Reached() != atEnd {
				t.Fatalf("with %q in the last row: reached %v", r.bottom(), r.m.Reached())
			}
			if atEnd {
				break
			}
			if i > 25 {
				t.Fatal("the end never comes")
			}
			if got := r.footer(); !strings.HasPrefix(got, "end not reached · ") {
				t.Fatalf("footer %q", got)
			}
			r.press(keyJ)
		}
		if got := r.footer(); !strings.HasPrefix(got, "end reached · end of the patch · Esc closes") {
			t.Fatalf("footer %q", got)
		}
		r.press(keyHome)
		if !r.m.Reached() || !strings.HasPrefix(r.footer(), "end reached · ") {
			t.Fatalf("back at the top: reached %v, footer %q", r.m.Reached(), r.footer())
		}
	})
	t.Run("a patch shorter than the screen", func(t *testing.T) {
		r := newRig(t, 40, 12, serve(paginate(numbered(3), 10, 0)))
		if !r.m.Reached() {
			t.Fatal("three lines on a screen of ten and not reached")
		}
	})
	t.Run("not before the pager has a size", func(t *testing.T) {
		r := newRig(t, 0, 0, serve(paginate(numbered(3), 10, 0)))
		if r.m.Reached() {
			t.Fatal("reached with nothing on a screen")
		}
		r.size(50, 2)
		if r.m.Reached() {
			t.Fatal("reached in a region with no row for the patch")
		}
		if got := r.footer(); got != "end not reached · too small to show the patch" {
			t.Fatalf("footer %q", got)
		}
		r.size(3, 12)
		if r.m.Reached() {
			t.Fatal("reached in a region with no column for the patch")
		}
		r.size(40, 12)
		if !r.m.Reached() {
			t.Fatal("not reached once the patch is on the screen")
		}
	})
	t.Run("an empty patch", func(t *testing.T) {
		r := newRig(t, 40, 12, serve([]tpage{{}, {}}))
		if !r.m.Reached() {
			t.Fatal("an empty patch, read whole, is not reached")
		}
		if got := r.footer(); !strings.HasPrefix(got, "end reached · the patch is empty") {
			t.Fatalf("footer %q", got)
		}
		r.press(keyJ, keyK, keyPgDn, keyPgUp, keyHome, keyEnd)
	})
	t.Run("pages without a piece", func(t *testing.T) {
		pages := []tpage{{}, {[]string{"a", "b"}, []bool{false, true}}, {}, {[]string{"c"}, []bool{false}}, {}}
		r := newRig(t, 40, 12, serve(pages))
		if got := r.patchRows(); got[0].text[:2] != "a " || got[1].text[:3] != "bc " || !r.m.Reached() {
			t.Fatalf("rows %+v, reached %v", got[:3], r.m.Reached())
		}
	})
}

// Pages at the end of the patch that hold no piece are not rows below the
// last row: the footer says the end is the end, the pages are let go, and
// no key reads them again.
func TestPagesWithoutAPieceAtTheEnd(t *testing.T) {
	t.Run("the footer at the end", func(t *testing.T) {
		pages := append(paginate(numbered(30), 10, 0), tpage{}, tpage{})
		r := newRig(t, 80, 12, serve(pages))
		for r.bottom() != "l028" {
			r.press(keyJ)
		}
		// The two pages without a piece are not pages below.
		if got := r.footer(); !strings.HasPrefix(got, "end not reached · last page, more below · ") {
			t.Fatalf("one row above the end: footer %q", got)
		}
		r.press(keyHome)
		for i := 0; i < 5; i++ {
			r.press(keyEnd)
		}
		if got := r.bottom(); got != "l029" || !r.m.Reached() {
			t.Fatalf("after End the last row is %q, reached %v", got, r.m.Reached())
		}
		if got := r.footer(); !strings.HasPrefix(got, "end reached · end of the patch · Esc closes") {
			t.Fatalf("footer %q", got)
		}
		calls := len(r.src.calls)
		r.press(keyJ, keyK, keyEnd, keyPgDn, keyK, keyJ, keyPgUp, keyPgUp, keyHome, keyEnd, keyEnd, keyJ)
		if len(r.src.calls) != calls {
			t.Fatalf("pages read at the end of a patch read whole: %v", r.src.calls[calls:])
		}
		if got := r.footer(); !strings.HasPrefix(got, "end reached · end of the patch · Esc closes") {
			t.Fatalf("footer %q", got)
		}
	})
	t.Run("many pages", func(t *testing.T) {
		const empty = 500
		pages := append(paginate(numbered(1), 10, 0), make([]tpage, empty)...)
		r := newRig(t, 80, 12, serve(pages))
		// To know that the line is the last, every page is read, once.
		if len(r.src.calls) != 1+empty {
			t.Fatalf("%d pages read, want every page once", len(r.src.calls))
		}
		if len(r.m.held) > 3 {
			t.Fatalf("the pager holds %d pages of a patch of one line", len(r.m.held))
		}
		if got := r.footer(); !r.m.Reached() || !strings.HasPrefix(got, "end reached · end of the patch · Esc closes") {
			t.Fatalf("reached %v, footer %q", r.m.Reached(), got)
		}
		r.press(keyJ, keyK, keyEnd, keyPgDn, keyPgUp, keyHome, keyEnd)
		r.size(60, 30)
		if len(r.src.calls) != 1+empty {
			t.Fatalf("pages read again: %v", r.src.calls[1+empty:])
		}
	})
}

// The footer says how much is below, from the number of pages.
func TestFooterSaysHowMuchIsBelow(t *testing.T) {
	r := newRig(t, 100, 12, serve(paginate(numbered(100), 10, 0)))
	keys := " · Esc closes · j/k PgUp/PgDn Home/End"
	if got, want := r.footer(), "end not reached · 9 of 10 pages below"+keys; got != want {
		t.Fatalf("footer %q, want %q", got, want)
	}
	r.press(keyJ)
	if got, want := r.footer(), "end not reached · 8 of 10 pages below"+keys; got != want {
		t.Fatalf("footer %q, want %q", got, want)
	}
	for r.bottom() != "l080" {
		r.press(keyJ)
	}
	if got, want := r.footer(), "end not reached · 1 of 10 pages below"+keys; got != want {
		t.Fatalf("footer %q, want %q", got, want)
	}
	for r.bottom() != "l095" {
		r.press(keyJ)
	}
	if got, want := r.footer(), "end not reached · last page, more below"+keys; got != want {
		t.Fatalf("footer %q, want %q", got, want)
	}
	r.press(keyEnd)
	if got, want := r.footer(), "end reached · end of the patch"+keys; got != want {
		t.Fatalf("footer %q, want %q", got, want)
	}
	// At 80 columns the keys still fit beside the largest numbers.
	r.size(80, 12)
	if got, want := r.footer(), "end reached · end of the patch"+keys; got != want {
		t.Fatalf("footer %q, want %q", got, want)
	}
	// A narrow region keeps the first parts whole and drops the rest.
	r.size(40, 12)
	if got, want := r.footer(), "end reached · end of the patch"; got != want {
		t.Fatalf("footer %q, want %q", got, want)
	}
	r.size(12, 12)
	if got, want := r.footer(), "end reached"; got != want {
		t.Fatalf("footer %q, want %q", got, want)
	}
	r.size(8, 12)
	if got, want := r.footer(), "end rea…"; got != want {
		t.Fatalf("footer %q, want %q", got, want)
	}
	// While a page is being read the footer says so.
	slow := newRig(t, 100, 12, serve(paginate(numbered(100), 30, 0)))
	cmd, _ := slow.m.Update(keyEnd)
	if cmd == nil {
		t.Fatal("End asked for no page")
	}
	if got := slow.footer(); !strings.HasSuffix(got, " · reading a page") {
		t.Fatalf("footer while a page is read: %q", got)
	}
	slow.run(cmd)
	if got := slow.footer(); strings.Contains(got, "reading a page") {
		t.Fatalf("footer after the page came: %q", got)
	}
}

// hostileErr is an error whose text is shaped to mislead. None of it may
// be drawn.
var hostileErr = errors.New("\x1b[2J[a] approve   LEADSAID end reached")

func TestAFailedPageEndsTheReading(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"an error", hostileErr, failPage},
		{"the review changed", ErrReviewChanged, failChanged},
		{"the review changed, wrapped", fmt.Errorf("page 3: %w (%w)", ErrReviewChanged, hostileErr), failChanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pages := paginate(numbered(200), 20, 0)
			r := newRig(t, 100, 12, func(n int) ([]string, []bool, int, error) {
				if n == 3 {
					return []string{"LEADSAID with the error"}, []bool{false}, len(pages), tc.err
				}
				return serve(pages)(n)
			})
			for i := 0; r.m.Err() == nil; i++ {
				if i > 100 {
					t.Fatal("the failing page is never asked for")
				}
				r.press(keyPgDn)
			}
			if !errors.Is(r.m.Err(), tc.err) {
				t.Fatalf("Err() = %v", r.m.Err())
			}
			if got, want := r.footer(), tc.want+" · end not reached · Esc closes · j/k PgUp/PgDn Home/End"; got != want {
				t.Fatalf("footer %q, want %q", got, want)
			}
			if c := r.draw().CellAt(0, r.h-1); c.Style.Fg != r.m.colors.Warn {
				t.Fatalf("the failure line is not in the warning colour")
			}
			// Nothing more is read, whatever is pressed, and what was
			// read can still be scrolled.
			calls := len(r.src.calls)
			r.press(keyEnd, keyEnd, keyPgDn, keyJ, keyHome, keyEnd, keyPgUp, keyK)
			for i := 0; i < 100; i++ {
				r.press(keyPgUp)
			}
			r.size(50, 30)
			r.size(100, 12)
			for i := 0; i < 100; i++ {
				r.press(keyPgDn, keyEnd)
			}
			if len(r.src.calls) != calls {
				t.Fatalf("pages read after the failure: %v", r.src.calls[calls:])
			}
			if got := r.bottom(); got != "l059" {
				t.Fatalf("last row after the failure %q, want the last line read", got)
			}
			if r.m.Reached() {
				t.Fatal("reached after a failed page")
			}
			buf := r.draw()
			for y := 0; y < r.h; y++ {
				if text := rowText(buf, y, 0); strings.Contains(text, "LEADSAID") || strings.Contains(text, "approve") {
					t.Fatalf("row %d shows the error or the failed page: %q", y, text)
				}
			}
		})
	}
}

// A page that fails after the end was reached takes the reached end away:
// the review may have changed.
func TestAFailureAfterTheEndWasReached(t *testing.T) {
	pages := paginate(numbered(2000), 20, 0)
	fail := false
	r := newRig(t, 60, 12, func(n int) ([]string, []bool, int, error) {
		if fail {
			return nil, nil, 0, ErrReviewChanged
		}
		return serve(pages)(n)
	})
	for i := 0; !r.m.Reached(); i++ {
		if i > 200 {
			t.Fatal("End never reaches the end")
		}
		r.press(keyEnd)
	}
	fail = true
	// Home reads the first page again, which was let go.
	r.press(keyHome)
	if r.m.Reached() || !errors.Is(r.m.Err(), ErrReviewChanged) {
		t.Fatalf("reached %v, err %v", r.m.Reached(), r.m.Err())
	}
	if got := r.footer(); !strings.HasPrefix(got, failChanged+" · end not reached") {
		t.Fatalf("footer %q", got)
	}
}

// A page that does not fit the others ends the reading as a failed one
// does.
func TestPagesThatDoNotFit(t *testing.T) {
	good := paginate(numbered(100), 10, 0)
	for _, tc := range []struct {
		name string
		page func(n int) ([]string, []bool, int, error)
		want string
	}{
		{"fewer marks than pieces", func(n int) ([]string, []bool, int, error) {
			return []string{"a", "b"}, []bool{false}, 1, nil
		}, failMalformed},
		{"more marks than pieces", func(n int) ([]string, []bool, int, error) {
			return []string{"a"}, []bool{false, false}, 1, nil
		}, failMalformed},
		{"no pages", func(n int) ([]string, []bool, int, error) {
			return []string{"a"}, []bool{false}, 0, nil
		}, failMalformed},
		{"a negative number of pages", func(n int) ([]string, []bool, int, error) {
			return []string{"a"}, []bool{false}, -5, nil
		}, failMalformed},
		{"the number of pages changes", func(n int) ([]string, []bool, int, error) {
			return good[n].lines, good[n].cont, len(good) + n, nil
		}, failMalformed},
		{"the patch ends in the middle of a line", func(n int) ([]string, []bool, int, error) {
			return []string{"a", "b"}, []bool{false, true}, 1, nil
		}, failMalformed},
		{"the patch ends in the middle of a line, before a page without a piece", func(n int) ([]string, []bool, int, error) {
			if n == 1 {
				return nil, nil, 2, nil
			}
			return []string{"a", "b"}, []bool{false, true}, 2, nil
		}, failMalformed},
		{"a line longer than the pager shows", func(n int) ([]string, []bool, int, error) {
			return []string{strings.Repeat("x", 1<<20)}, []bool{true}, 1000, nil
		}, failTooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, 100, 12, tc.page)
			for i := 0; i < 20; i++ {
				r.press(keyPgDn)
			}
			if r.m.Err() == nil || r.m.Reached() {
				t.Fatalf("err %v, reached %v", r.m.Err(), r.m.Reached())
			}
			if got := r.footer(); !strings.HasPrefix(got, tc.want+" · end not reached") {
				t.Fatalf("footer %q", got)
			}
			calls := len(r.src.calls)
			r.press(keyEnd, keyPgDn, keyHome)
			if len(r.src.calls) != calls {
				t.Fatal("a page was read after the failure")
			}
			if calls > MaxLineBytes>>20+2 {
				t.Fatalf("%d pages were read", calls)
			}
		})
	}
}

// An answer is taken only by the pager that asked, and only for the
// request it is waiting for.
func TestAnswersOfOtherRequestsAreDropped(t *testing.T) {
	pages := paginate(numbered(2000), 20, 0)
	a := newRig(t, 40, 12, serve(pages))
	b := newRig(t, 40, 12, serve(pages))
	a.press(keyPgDn)
	cmd, _ := b.m.Update(keyEnd)
	if cmd == nil {
		t.Fatal("End asked for no page")
	}
	msg := b.exec(cmd)
	before, held := a.first(), len(a.m.held)
	if cmd, act := a.m.Update(msg); cmd != nil || act != None || a.first() != before || len(a.m.held) != held {
		t.Fatal("a pager took the answer to another pager's request")
	}
	// Home lets go of everything: the answer that was on its way is for
	// pages that are gone.
	for i := 0; i < 30; i++ {
		b.run(cmd)
		cmd, _ = b.m.Update(keyEnd)
	}
	if cmd == nil || b.m.lo == 0 {
		t.Fatalf("after 30 times End: command nil %v, first page held %d", cmd == nil, b.m.lo)
	}
	stale := b.exec(cmd)
	homeCmd, _ := b.m.Update(keyHome)
	if homeCmd == nil {
		t.Fatal("Home asked for no page")
	}
	if cmd, _ := b.m.Update(stale); cmd != nil || len(b.m.held) != 0 {
		t.Fatal("the answer to a request that was given up was taken")
	}
	b.run(homeCmd)
	if got := b.first(); got != "l000" {
		t.Fatalf("after Home the first row is %q", got)
	}
	// The same answer twice.
	msg = b.exec(homeCmd)
	if cmd, _ := b.m.Update(msg); cmd != nil || b.first() != "l000" {
		t.Fatal("an answer was taken twice")
	}
}

// A change of the size keeps the top of the screen on the same text, and
// no size makes the pager fail.
func TestResize(t *testing.T) {
	var lines []string
	for i := 0; i < 300; i++ {
		lines = append(lines, fmt.Sprintf("l%03d ", i)+strings.Repeat(fmt.Sprintf("w%03d ", i), i%40))
	}
	r := newRig(t, 60, 20, serve(paginate(lines, 25, 16)))
	for i := 0; i < 10; i++ {
		r.press(keyPgDn)
	}
	for r.m.top.off == 0 {
		r.press(keyJ)
	}
	at, off := r.m.top.at, r.m.top.off
	for _, size := range [][2]int{{30, 20}, {200, 50}, {7, 9}, {4, 3}, {61, 20}, {60, 20}} {
		r.size(size[0], size[1])
		if r.m.top.at != at || r.m.top.off > off {
			t.Fatalf("at %dx%d the top moved from %v+%d to %v+%d", size[0], size[1], at, off, r.m.top.at, r.m.top.off)
		}
		w := r.m.wrapper()
		if ln, _ := r.m.lineAt(at); w.rowEnd(ln.text, r.m.top.off) <= off && off != 0 && w.rowEnd(ln.text, r.m.top.off) < len(ln.text) {
			t.Fatalf("at %dx%d the top row does not hold the text that was at the top", size[0], size[1])
		}
		off = r.m.top.off
		r.draw()
	}
	for w := 0; w <= 9; w++ {
		for h := 0; h <= 6; h++ {
			r.size(w, h)
			r.draw()
			r.press(keyJ, keyK, keyPgDn, keyPgUp, keyEnd, keyHome)
		}
	}
	// At the end, a wider region does not leave the screen half empty.
	r.size(60, 20)
	for i := 0; !r.m.Reached(); i++ {
		if i > 100 {
			t.Fatal("End never reaches the end")
		}
		r.press(keyEnd)
	}
	r.size(200, 20)
	if got := r.bottom(); !strings.HasPrefix(got, "l299 ") && !strings.Contains(got, "w299") {
		t.Fatalf("at the end, after widening, the last row is %q", got)
	}
}

// Draw writes all of the area it is given and nothing outside it.
func TestDrawStaysInItsArea(t *testing.T) {
	r := newRig(t, 20, 6, serve(paginate([]string{"日本語日本語日本語日本語日本語", strings.Repeat("x", 500)}, 10, 0)))
	mark := uv.Cell{Content: "#", Width: 1}
	for _, tc := range []struct {
		name string
		area uv.Rectangle
	}{
		{"the size", uv.Rect(5, 2, 20, 6)},
		{"smaller", uv.Rect(5, 2, 11, 3)},
		{"larger", uv.Rect(1, 1, 27, 8)},
		{"across the edge", uv.Rect(20, 6, 20, 6)},
		{"outside", uv.Rect(40, 20, 20, 6)},
		{"empty", uv.Rect(3, 3, 0, 0)},
	} {
		buf := uv.NewBuffer(30, 10)
		for y := 0; y < 10; y++ {
			for x := 0; x < 30; x++ {
				buf.SetCell(x, y, &mark)
			}
		}
		r.m.Draw(buf, tc.area)
		for y := 0; y < 10; y++ {
			for x := 0; x < 30; x++ {
				inside := x >= tc.area.Min.X && x < tc.area.Max.X && y >= tc.area.Min.Y && y < tc.area.Max.Y
				if c := buf.CellAt(x, y); (c.Content == "#") == inside {
					t.Fatalf("%s: cell %d,%d holds %q", tc.name, x, y, c.Content)
				}
			}
		}
	}
}

var testColors = Colors{
	Fg:   color.RGBA{0xe6, 0xe6, 0xe6, 0xff},
	Bg:   color.RGBA{0x0e, 0x0f, 0x12, 0xff},
	Dim:  color.RGBA{0x8a, 0x90, 0x9a, 0xff},
	Warn: color.RGBA{0xf0, 0xb4, 0x29, 0xff},
}

// The colours are the parent's.
func TestColours(t *testing.T) {
	pages := paginate([]string{strings.Repeat("x", 100)}, 10, 0)
	src := &fakeSource{t: t, page: serve(pages)}
	r := &rig{t: t, src: src}
	r.m = New(src, Options{Colors: testColors})
	r.size(40, 6)
	r.run(r.m.Init())
	buf := r.draw()
	for _, tc := range []struct {
		name string
		x, y int
		want uv.Style
	}{
		{"header", 0, 0, uv.Style{Fg: testColors.Dim, Bg: testColors.Bg}},
		{"text", marginCells, 1, uv.Style{Fg: testColors.Fg, Bg: testColors.Bg}},
		{"wrap mark", 0, 2, uv.Style{Fg: testColors.Dim, Bg: testColors.Bg}},
		{"blank", 39, 4, uv.Style{Fg: testColors.Fg, Bg: testColors.Bg}},
		{"footer", 0, 5, uv.Style{Fg: testColors.Dim, Bg: testColors.Bg}},
	} {
		if got := buf.CellAt(tc.x, tc.y).Style; got != tc.want {
			t.Errorf("%s: style %+v, want %+v", tc.name, got, tc.want)
		}
	}
}
