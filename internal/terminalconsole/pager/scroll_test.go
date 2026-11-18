package pager

import (
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/exchange/leadtext"
)

// heldLines is the number of pieces the pager holds.
func (m *Model) heldLines() int {
	n := 0
	for _, pg := range m.held {
		n += len(pg.lines)
	}
	return n
}

// bigPatch is a patch of 100,000 lines on 200 pages, made as it is asked
// for, so that the test holds none of it either.
const (
	bigLines   = 100000
	bigPerPage = 500
)

func bigLine(i int) string { return fmt.Sprintf("+line %06d of the patch", i) }

func bigPage(n int) ([]string, []bool, int, error) {
	lines := make([]string, bigPerPage)
	for i := range lines {
		lines[i] = bigLine(n*bigPerPage + i)
	}
	return lines, make([]bool, bigPerPage), bigLines / bigPerPage, nil
}

// The bound the pager keeps on a screen of 22 rows and pages of 500 lines:
// the rows it looks at (a screen above, two from the top on) lie on two
// pages at most, and it keeps one page on either side of them.
const (
	bigMaxPages = 4
	bigMaxLines = bigMaxPages * bigPerPage
)

func (r *rig) checkBound(where string) {
	r.t.Helper()
	if len(r.m.held) > bigMaxPages || r.m.heldLines() > bigMaxLines {
		r.t.Fatalf("%s: the pager holds %d pages and %d lines", where, len(r.m.held), r.m.heldLines())
	}
	if len(r.m.cache) > r.m.cacheRoom() {
		r.t.Fatalf("%s: the pager holds %d joined lines", where, len(r.m.cache))
	}
}

// checkScreen checks that the screen shows the lines from top on, one a
// row.
func (r *rig) checkScreen(top int) {
	r.t.Helper()
	for y, row := range r.patchRows() {
		if got, want := strings.TrimRight(row.text, " "), bigLine(top+y); got != want || row.marked {
			r.t.Fatalf("row %d with line %d at the top: %q (marked %v), want %q", y, top, got, row.marked, want)
		}
	}
}

func TestScrollsAHundredThousandLinesInPages(t *testing.T) {
	r := newRig(t, 80, 24, bigPage)
	rows := r.h - 2
	top := 0
	for presses := 0; ; presses++ {
		r.checkBound(fmt.Sprintf("line %d", top))
		r.checkScreen(top)
		if top+rows == bigLines {
			break
		}
		if r.m.Reached() {
			t.Fatalf("the end is reached with line %d at the top", top)
		}
		if presses > bigLines {
			t.Fatal("the end is never reached")
		}
		r.press(keyPgDn)
		top = min(top+rows-1, bigLines-rows)
	}
	if !r.m.Reached() {
		t.Fatal("the end is on the screen and not reached")
	}
	if got := r.footer(); !strings.HasPrefix(got, "end reached · end of the patch · Esc closes") {
		t.Fatalf("footer %q", got)
	}
	// Every page was read once on the way down, and none twice.
	if len(r.src.calls) != bigLines/bigPerPage {
		t.Fatalf("%d pages were read on the way down", len(r.src.calls))
	}

	// Back up through pages that were let go: they are read again.
	for i := 0; i < 200; i++ {
		r.press(keyPgUp)
		top -= rows - 1
		r.checkBound(fmt.Sprintf("line %d, going up", top))
		r.checkScreen(top)
	}
	r.press(keyK, keyUp)
	top -= 2
	r.checkScreen(top)
	r.press(keyJ, keyDown, keyDown)
	top += 3
	r.checkScreen(top)

	// Home reads the first page again, End goes as far as is read.
	r.press(keyHome)
	r.checkBound("home")
	r.checkScreen(0)
	if !r.m.Reached() {
		t.Fatal("scrolling back up took the reached end away")
	}
}

// End goes to the end of what is read and reads on: pressed again and
// again it reaches the end of the patch, a page or so at a time.
func TestEndReachesTheEndPageByPage(t *testing.T) {
	r := newRig(t, 80, 24, bigPage)
	presses := 0
	for ; !r.m.Reached(); presses++ {
		if presses > 2*bigLines/bigPerPage {
			t.Fatal("End never reaches the end")
		}
		r.press(keyEnd)
		r.checkBound(fmt.Sprintf("End %d", presses))
		if got := r.bottom(); !strings.HasPrefix(got, "+line ") {
			t.Fatalf("after End the last row is %q", got)
		}
	}
	if presses < bigLines/bigPerPage/2 {
		t.Fatalf("End reached the end of 200 pages in %d presses: it went past what was read", presses)
	}
	r.checkScreen(bigLines - (r.h - 2))
}

// End may only go as far as what is read. The page it asks for next is not
// shown until End is pressed again.
func TestEndStopsAtWhatIsRead(t *testing.T) {
	var lines []string
	for i := 0; i < 500; i++ {
		lines = append(lines, fmt.Sprintf("l%03d", i))
	}
	r := newRig(t, 40, 12, serve(paginate(lines, 50, 0)))
	if len(r.src.calls) != 1 {
		t.Fatalf("pages read before any key: %v", r.src.calls)
	}
	cmd, act := r.m.Update(keyEnd)
	if act != None || cmd == nil {
		t.Fatalf("End: action %d, command nil %v", act, cmd == nil)
	}
	if got := r.bottom(); got != "l049" || r.m.Reached() {
		t.Fatalf("End before the next page came: last row %q, reached %v", got, r.m.Reached())
	}
	r.run(cmd)
	if got := r.bottom(); got != "l049" {
		t.Fatalf("the next page moved the screen: last row %q", got)
	}
	r.press(keyEnd)
	if got := r.bottom(); got != "l099" {
		t.Fatalf("second End: last row %q", got)
	}
}

// longLine is a line of n bytes with what an escape has to catch spread
// through it, and characters of more than one byte for a piece to be cut
// in.
func longLine(n int) string {
	var b strings.Builder
	b.Grow(n + 64)
	for i := 0; b.Len() < n; i++ {
		switch i % 7 {
		case 0:
			fmt.Fprintf(&b, "word%07d-", i)
		case 1:
			b.WriteString("\x1b[31mred\x1b[0m")
		case 2:
			b.WriteString("日本語é")
		case 3:
			b.WriteString("‮override​")
		case 4:
			b.WriteString("\xff\xfe")
		case 5:
			b.WriteString("\x1b]0;title\x07\u009b2J")
		default:
			b.WriteString("the_quick_brown_fox_jumps_over_the_lazy_dog_")
		}
	}
	return b.String()[:n]
}

// noBlanks drops the blanks: a row that ends before a wide character has a
// blank cell that the text does not.
func noBlanks(s string) string { return strings.ReplaceAll(s, " ", "") }

// A line of 1 MiB is wrapped over many rows, none cut, and can be scrolled
// through to its end and back. It is read once as one piece and once in
// pieces that are cut inside characters and spread over pages. The line is
// the hostile set's: every screen of it is checked as TestHostileSet checks
// a screen.
func TestALineOfOneMebibyte(t *testing.T) {
	line := longLine(1 << 20)
	want := noBlanks(leadtext.Escape(line))
	for _, tc := range []struct {
		name  string
		pages []tpage
	}{
		{"one piece", paginate([]string{line}, 1, 0)},
		{"pieces on pages", paginate([]string{line}, 3, 4099)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, 100, 202, serve(tc.pages))
			rows := r.h - 2
			var got []string
			r.checkChrome("the top of the line")
			for _, row := range r.patchRows() {
				got = append(got, row.text)
			}
			if r.m.Reached() {
				t.Fatal("reached at the top of the line")
			}
			for {
				before := r.m.top.row
				r.press(keyPgDn)
				moved := r.m.top.row - before
				if moved == 0 {
					break
				}
				r.checkChrome(fmt.Sprintf("row %d of the line", r.m.top.row))
				screen := r.patchRows()
				for _, row := range screen[rows-moved:] {
					if !row.marked {
						t.Fatalf("a row of the long line has no wrap mark: %q", row.text)
					}
					got = append(got, row.text)
				}
			}
			if !r.m.Reached() {
				t.Fatal("the end of the line is not reached")
			}
			if len(got) < 10000 {
				t.Fatalf("the line took %d rows", len(got))
			}
			if joined := noBlanks(strings.Join(got, "")); joined != want {
				t.Fatalf("the rows hold %d bytes, the escaped line %d; they differ", len(joined), len(want))
			}
			// Back up: by a row, by a screen, to the top.
			last := len(got) - rows
			r.press(keyK, keyK, keyK)
			if text := r.patchRows()[0].text; text != got[last-3] {
				t.Fatalf("three rows up: %q, want %q", text, got[last-3])
			}
			r.press(keyPgUp, keyPgUp)
			if text := r.patchRows()[0].text; text != got[last-3-2*(rows-1)] {
				t.Fatalf("two screens up: %q", text)
			}
			r.press(keyHome)
			if text := r.patchRows()[0].text; text != got[0] || r.m.top.row != 0 {
				t.Fatalf("home: %q", text)
			}
		})
	}
}

// A line that arrives in pieces is joined before it is escaped and wrapped:
// it is drawn as the same line in one piece is, however it was cut, also
// inside a character or an escape sequence.
func TestPiecesAreJoinedBeforeWrapping(t *testing.T) {
	var lines []string
	for i := 0; i < 12; i++ {
		lines = append(lines, fmt.Sprintf("+ordinary line %d", i), longLine(300+i), "")
	}
	lines = append(lines, "last line")
	var want []string
	for _, l := range lines {
		want = append(want, noBlanks(leadtext.Escape(l)))
	}
	read := func(pages []tpage) []string {
		r := newRig(t, 60, 20, serve(pages))
		got := r.readAll()
		if !r.m.Reached() {
			t.Fatal("read to the end and not reached")
		}
		for i := range got {
			got[i] = noBlanks(got[i])
		}
		return got
	}
	whole := read(paginate(lines, 7, 0))
	if strings.Join(whole, "\n") != strings.Join(want, "\n") {
		t.Fatalf("whole lines are drawn as\n%q\nwant\n%q", whole, want)
	}
	for _, cut := range []struct{ perPage, pieceBytes int }{{1, 1}, {2, 3}, {5, 7}, {3, 64}, {1000, 5}} {
		got := read(paginate(lines, cut.perPage, cut.pieceBytes))
		if strings.Join(got, "\n") != strings.Join(whole, "\n") {
			t.Fatalf("pieces of %d bytes, %d a page, are drawn as\n%q\nwant\n%q", cut.pieceBytes, cut.perPage, got, whole)
		}
	}
}

// The blanks of a line are the line's: every one is drawn, in its place,
// and none is added between two rows. The lines here hold no wide character
// and no mark, so every row but a line's last is full, and the rows of a
// line, joined, are its escaped text and then only the blanks that fill its
// last row. The other tests compare without the blanks.
func TestSpacesAreKept(t *testing.T) {
	lines := []string{
		"+  indented  twice",
		"-\tx  =  1 ;",
		"   ",
		"a b  c   d    e     f",
		" one in front",
		"        eight in front",
		`"a  b" != "a b"`,
		strings.Repeat("ab  cd   e    ", 30),
		"",
	}
	for i := 0; i < 30; i++ {
		lines = append(lines, fmt.Sprintf("  l%03d  filler   line", i))
	}
	for _, w := range []int{80, 31, 20, 9, 5} {
		for _, pieceBytes := range []int{0, 1, 3} {
			r := newRig(t, w, 12, serve(paginate(lines, 7, pieceBytes)))
			got := r.readAllCells()
			if len(got) != len(lines) {
				t.Fatalf("%d columns, pieces of %d bytes: %d lines drawn, want %d", w, pieceBytes, len(got), len(lines))
			}
			for i, l := range lines {
				want := leadtext.Escape(l)
				if !strings.HasPrefix(got[i], want) || strings.TrimRight(got[i][len(want):], " ") != "" {
					t.Fatalf("%d columns, pieces of %d bytes, line %d:\n%q is drawn as\n%q, want\n%q and blanks", w, pieceBytes, i, l, got[i], want)
				}
			}
		}
	}
}

// With a page for every piece, the pager holds the pages of the rows it
// looks at and one on either side, not the patch.
func TestHoldsNoMoreThanItLooksAt(t *testing.T) {
	var lines []string
	for i := 0; i < 5000; i++ {
		lines = append(lines, fmt.Sprintf("l%04d", i))
	}
	r := newRig(t, 40, 12, serve(paginate(lines, 1, 0)))
	rows := r.h - 2
	check := func() {
		t.Helper()
		// A screen of rows above the top, the top row, two screens below
		// it, and a page either side.
		if len(r.m.held) > 3*rows+3 {
			t.Fatalf("the pager holds %d pages of one line each", len(r.m.held))
		}
	}
	for i := 0; !r.m.Reached(); i++ {
		if i > 5000 {
			t.Fatal("the end is never reached")
		}
		r.press(keyPgDn)
		check()
	}
	if got := r.bottom(); got != "l4999" {
		t.Fatalf("last row %q", got)
	}
	for i := 0; i < 50; i++ {
		r.press(keyPgUp)
		check()
	}
	if got, want := r.first(), fmt.Sprintf("l%04d", 5000-rows-50*(rows-1)); got != want {
		t.Fatalf("first row %q, want %q", got, want)
	}
}
