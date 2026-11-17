package vt

import (
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"sync"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// The sequences that blank lines (erase in display and in line, erase
// characters, insert and delete line, scroll up and down, index and reverse
// index, the full reset, the switch of the alternate screen) mark the lines
// they blank and write nothing (blank.go). These tests compare them with the
// code they replaced, which is kept here and works on the cells one at a time
// through the buffer's own functions, and check what the marks say.

// checkMarks returns what is wrong with the marks of the screens of e, or "".
// At rest, between two writes, no line is stale and every mark says what its
// line holds.
func checkMarks(e *Emulator) string {
	for i := range e.scrs {
		s := &e.scrs[i]
		if s.stale != 0 {
			return fmt.Sprintf("screen %d: %d stale lines at rest", i, s.stale)
		}
		if len(s.marks) != 0 && len(s.marks) != len(s.buf.Lines) {
			return fmt.Sprintf("screen %d: %d marks for %d lines", i, len(s.marks), len(s.buf.Lines))
		}
		for y, m := range s.marks {
			if m.stale {
				return fmt.Sprintf("screen %d line %d: stale at rest", i, y)
			}
			if m.cell == nil {
				continue
			}
			from, to := 0, len(s.buf.Lines[y])
			if m.part {
				from, to = m.from, m.to
				if from < 0 || to > len(s.buf.Lines[y]) || from >= to {
					return fmt.Sprintf("screen %d line %d: a part from %d to %d", i, y, from, to)
				}
			}
			for x := from; x < to; x++ {
				if !reflect.DeepEqual(&s.buf.Lines[y][x], m.cell) {
					return fmt.Sprintf("screen %d line %d column %d: the mark says %#v, the cell is %#v", i, y, x, *m.cell, s.buf.Lines[y][x])
				}
			}
		}
	}
	return ""
}

// dropMarks forgets what the marks of the screens say, as a screen that was
// restored or resized knows nothing about its lines.
func dropMarks(e *Emulator) {
	for i := range e.scrs {
		refSync(&e.scrs[i])
	}
}

// The reference: the code of the sequences before the marks. It works on a
// screen whose marks are dropped first, so that nothing of it reads one.

func refSync(s *Screen) {
	s.flushAll()
	s.marks, s.stale, s.lastFill = nil, 0, nil
}

// refFillArea is Screen.FillArea as it was: every cell through the buffer.
func refFillArea(s *Screen, c *uv.Cell, area uv.Rectangle) {
	refSync(s)
	s.buf.FillArea(c, area)
	s.touchArea(area)
}

// refClearArea is Screen.ClearArea as it was.
func refClearArea(s *Screen, area uv.Rectangle) {
	refSync(s)
	s.buf.ClearArea(area)
	if area.Min.X <= 0 && area.Min.Y <= 0 && area.Max.X >= s.Width() && area.Max.Y >= s.Height() {
		clear(s.links.set)
		s.links.wait = 0
	}
	s.touchArea(area)
}

// refClearWithScrollback is Screen.ClearWithScrollback as it was.
func refClearWithScrollback(s *Screen) {
	refSync(s)
	if s.scrollback != nil {
		for y := 0; y < s.buf.Height(); y++ {
			line := s.buf.Line(y)
			if line != nil && !s.isLineEmpty(line) {
				s.scrollback.Push(line)
			}
		}
	}
	refClearArea(s, s.Bounds())
}

// refReset is Screen.Reset as it was.
func refReset(s *Screen) {
	refSync(s)
	s.buf.Clear()
	s.cur = Cursor{}
	s.saved = Cursor{}
	s.scroll = s.buf.Bounds()
	s.buf.Touched = nil
	clear(s.links.set)
	s.links.wait = 0
}

// refInsertLine and refDeleteLine are Screen.InsertLine and DeleteLine as the
// buffer's functions did them. A region as wide as the screen was moved
// with the functions that do not touch the lines outside it, since patch 13
// (the buffer's own touch n lines more, which no reader of this module
// sees).
func refInsertLine(s *Screen, n int) bool {
	if n <= 0 {
		return false
	}
	x, y := s.cur.X, s.cur.Y
	if y < s.scroll.Min.Y || y >= s.scroll.Max.Y || x < s.scroll.Min.X || x >= s.scroll.Max.X {
		return false
	}
	refSync(s)
	if s.scroll.Min.X == 0 && s.scroll.Max.X == s.buf.Width() {
		s.buf.Buffer.InsertLineArea(y, n, s.blankCell(), s.scroll)
		for i := s.scroll.Min.Y; i < s.scroll.Max.Y; i++ {
			s.buf.TouchLine(0, i, s.buf.Width())
		}
	} else {
		s.buf.InsertLineArea(y, n, s.blankCell(), s.scroll)
	}
	return true
}

func refDeleteLine(s *Screen, n int) bool {
	if n <= 0 {
		return false
	}
	scroll := s.scroll
	x, y := s.cur.X, s.cur.Y
	if y < scroll.Min.Y || y >= scroll.Max.Y || x < scroll.Min.X || x >= scroll.Max.X {
		return false
	}
	refSync(s)
	if s.scrollback != nil && y == scroll.Min.Y && scroll.Min.X == 0 && scroll.Max.X == s.buf.Width() {
		s.scrollback.PushN(s.buf, y, min(n, scroll.Max.Y-y))
	}
	if scroll.Min.X == 0 && scroll.Max.X == s.buf.Width() {
		s.buf.Buffer.DeleteLineArea(y, n, s.blankCell(), scroll)
		for i := scroll.Min.Y; i < scroll.Max.Y; i++ {
			s.buf.TouchLine(0, i, s.buf.Width())
		}
	} else {
		s.buf.DeleteLineArea(y, n, s.blankCell(), scroll)
	}
	return true
}

func refScrollUp(s *Screen, n int) {
	x, y := s.CursorPosition()
	s.setCursor(s.cur.X, 0, true)
	refDeleteLine(s, n)
	s.setCursor(x, y, false)
}

func refScrollDown(s *Screen, n int) {
	x, y := s.CursorPosition()
	s.setCursor(s.cur.X, 0, true)
	refInsertLine(s, n)
	s.setCursor(x, y, false)
}

// refED, refEL, refECH, refIL, refDL, refIndex, refReverseIndex, refRIS and
// refAlt are the handlers of the sequences as they were.
func refED(e *Emulator, n int) {
	width, height := e.Width(), e.Height()
	x, y := e.scr.CursorPosition()
	switch n {
	case 0:
		refFillArea(e.scr, e.scr.blankCell(), uv.Rect(x, y, width, 1))
		refFillArea(e.scr, e.scr.blankCell(), uv.Rect(0, y+1, width, height-y-1))
	case 1:
		refFillArea(e.scr, e.scr.blankCell(), uv.Rect(0, 0, width, y+1))
	case 2:
		refClearWithScrollback(e.scr)
	case 3:
		refClearArea(e.scr, e.scr.Bounds())
		if sb := e.scr.Scrollback(); sb != nil {
			sb.Clear()
		}
	}
}

func refEL(e *Emulator, n int) {
	x, y := e.scr.CursorPosition()
	w := e.scr.Width()
	switch n {
	case 0:
		refECH(e, w-x)
	case 1:
		refFillArea(e.scr, e.scr.blankCell(), uv.Rect(0, y, x+1, 1))
	case 2:
		refFillArea(e.scr, e.scr.blankCell(), uv.Rect(0, y, w, 1))
	}
}

func refECH(e *Emulator, n int) {
	if n <= 0 {
		n = 1
	}
	x, y := e.scr.CursorPosition()
	n = min(n, max(e.Width()-x, 1))
	refFillArea(e.scr, e.scr.blankCell(), uv.Rect(x, y, n, 1))
	e.atPhantom = false
}

func refIL(e *Emulator, n int) {
	if refInsertLine(e.scr, n) {
		e.scr.setCursorX(0, true)
	}
}

func refDL(e *Emulator, n int) {
	if refDeleteLine(e.scr, n) {
		e.scr.setCursorX(0, true)
	}
}

func refIndex(e *Emulator) {
	x, y := e.scr.CursorPosition()
	scroll := e.scr.ScrollRegion()
	if y == scroll.Max.Y-1 && x >= scroll.Min.X && x < scroll.Max.X {
		refScrollUp(e.scr, 1)
	} else if y < scroll.Max.Y-1 || !uv.Pos(x, y).In(scroll) {
		e.scr.moveCursor(0, 1)
	}
	e.atPhantom = false
}

func refReverseIndex(e *Emulator) {
	x, y := e.scr.CursorPosition()
	scroll := e.scr.ScrollRegion()
	if y == scroll.Min.Y && x >= scroll.Min.X && x < scroll.Max.X {
		refScrollDown(e.scr, 1)
	} else {
		e.scr.moveCursor(0, -1)
	}
}

func refRIS(e *Emulator) {
	refReset(&e.scrs[0])
	refReset(&e.scrs[1])
	e.resetTabStops()
	e.resetModes()
	e.gl, e.gr = 0, 1
	e.gsingle = 0
	e.charsets = [4]CharSet{}
	e.atPhantom = false
	e.grapheme = e.grapheme[:0]
	e.capped = false
	e.lastChar = 0
}

func refAlt(e *Emulator, mode ansi.Mode, on bool) {
	setting := ansi.ModeReset
	if on {
		setting = ansi.ModeSet
	}
	e.modes[mode] = setting
	if mode == ansi.ModeAltScreenSaveCursor && on {
		e.saveCursor()
	}
	if (on && e.scr == &e.scrs[1]) || (!on && e.scr == &e.scrs[0]) {
		return
	}
	if on {
		e.scr = &e.scrs[1]
		e.scrs[1].cur = e.scrs[0].cur
		refClearArea(e.scr, e.scr.Bounds())
		e.scr.ClearTouched()
		e.setCursor(0, 0)
	} else {
		e.scr = &e.scrs[0]
	}
	if e.cb.AltScreen != nil {
		e.cb.AltScreen(on)
	}
}

// touchedOf returns what a screen has marked as touched, a line with nothing
// marked and a line past the end of the table being the same.
func touchedOf(s *Screen) []uv.LineData {
	t := make([]uv.LineData, s.Height())
	for y := range t {
		if y < len(s.buf.Touched) && s.buf.Touched[y] != nil {
			t[y] = *s.buf.Touched[y]
		}
	}
	return t
}

// scrollbackOf copies the lines the main screen's scrollback holds.
func scrollbackOf(e *Emulator) [][]uv.Cell {
	var out [][]uv.Cell
	for _, l := range e.scrs[0].Scrollback().Lines() {
		out = append(out, append([]uv.Cell{}, l...))
	}
	return out
}

// eraseOp is one of the sequences under test: what is written to the
// emulator and what the reference does to a copy.
type eraseOp struct {
	name string
	seq  string
	ref  func(e *Emulator)
}

func eraseOps(rng *rand.Rand) []eraseOp {
	n := []int{0, 1, 2, 3, 5, 9, 40, 2147483646}[rng.IntN(8)]
	cnt := ""
	if n > 0 {
		cnt = fmt.Sprint(n)
	}
	m := rng.IntN(4)
	return []eraseOp{
		{fmt.Sprintf("ED %d", m), fmt.Sprintf("\x1b[%dJ", m), func(e *Emulator) { refED(e, m) }},
		{fmt.Sprintf("EL %d", m%3), fmt.Sprintf("\x1b[%dK", m%3), func(e *Emulator) { refEL(e, m%3) }},
		{"ECH " + cnt, "\x1b[" + cnt + "X", func(e *Emulator) { refECH(e, n) }},
		{"IL " + cnt, "\x1b[" + cnt + "L", func(e *Emulator) { refIL(e, max(n, 1)) }},
		{"DL " + cnt, "\x1b[" + cnt + "M", func(e *Emulator) { refDL(e, max(n, 1)) }},
		{"SU " + cnt, "\x1b[" + cnt + "S", func(e *Emulator) { refScrollUp(e.scr, max(n, 1)) }},
		{"SD " + cnt, "\x1b[" + cnt + "T", func(e *Emulator) { refScrollDown(e.scr, max(n, 1)) }},
		{"IND", "\x1bD", func(e *Emulator) { refIndex(e) }},
		{"RI", "\x1bM", func(e *Emulator) { refReverseIndex(e) }},
		{"RIS", "\x1bc", func(e *Emulator) { refRIS(e) }},
		{"alternate screen on", "\x1b[?1049h", func(e *Emulator) { refAlt(e, ansi.ModeAltScreenSaveCursor, true) }},
		{"alternate screen 1047 on", "\x1b[?1047h", func(e *Emulator) { refAlt(e, ansi.ModeAltScreen, true) }},
		{"alternate screen off", "\x1b[?1049l", func(e *Emulator) { refAlt(e, ansi.ModeAltScreenSaveCursor, false) }},
	}
}

// The sequences that blank lines leave every field of State, the scrollback
// and the touched lines of both screens what the code that wrote every cell
// left, on every screen the property streams make, in every region, with the
// cursor anywhere, any count (also over the lines left), a pen with a
// background and without one, and a screen that was blanked before (so that
// the marks of its lines are there to be skipped, moved and dropped).
func TestBlankingLinesIsWhatWritingEveryCellGives(t *testing.T) {
	seeds := uint64(120)
	if raceEnabled {
		seeds = 25 // the race detector is slow
	}
	compared, byName := 0, map[string]int{}
	for seed := uint64(1); seed <= seeds; seed++ {
		rng := rand.New(rand.NewPCG(seed, 17))
		w, h, ops := stateStream(rng, 60+rng.IntN(80))
		a := NewEmulator(w, h)
		size := []int{DefaultScrollbackSize, 0, 3}[seed%3]
		a.SetScrollbackSize(size)
		next := 0
		advance := func(k int) {
			for ; k > 0 && next < len(ops); k-- {
				applyStateOp(a, ops[next])
				next++
			}
		}
		for try := 0; try < 24; try++ {
			advance(rng.IntN(4))
			if !a.Quiescent() {
				// Never take a copy in the middle of a sequence.
				a.Write([]byte("\x18"))
				if !a.Quiescent() {
					continue
				}
			}
			// The screen the sequence meets: a region (sometimes narrower than
			// the screen), a pen, the cursor, the alternate screen.
			var prep []byte
			switch rng.IntN(5) {
			case 0:
				prep = fmt.Appendf(prep, "\x1b[%d;%dr", 1+rng.IntN(a.Height()), 1+rng.IntN(a.Height()+2))
			case 1:
				prep = fmt.Appendf(prep, "\x1b[?69h\x1b[%d;%ds\x1b[%d;%dr", 1+rng.IntN(a.Width()), 1+rng.IntN(a.Width()+2), 1+rng.IntN(a.Height()), 1+rng.IntN(a.Height()+2))
			}
			switch rng.IntN(3) {
			case 0:
				prep = append(prep, "\x1b[44m"...)
			case 1:
				prep = append(prep, "\x1b[49m"...)
			}
			prep = fmt.Appendf(prep, "\x1b[%d;%dH", 1+rng.IntN(a.Height()), 1+rng.IntN(a.Width()))
			if rng.IntN(6) == 0 {
				prep = append(prep, "\x1b[?1049h"...)
			}
			a.Write(prep)
			if d := checkMarks(a); d != "" {
				t.Fatalf("seed %d try %d: after the stream: %s", seed, try, d)
			}

			// The copy gets the state and nothing of the marks, and both start
			// with nothing touched and no scrollback.
			b := NewEmulator(2, 2)
			b.SetScrollbackSize(size)
			if err := b.SetState(a.State()); err != nil {
				t.Fatal(err)
			}
			a.ClearScrollback()
			for i := range a.scrs {
				a.scrs[i].ClearTouched()
				b.scrs[i].ClearTouched()
			}
			op := eraseOps(rng)[rng.IntN(13)]
			a.Write([]byte(op.seq))
			op.ref(b)
			where := fmt.Sprintf("seed %d try %d, %s (screen %dx%d, region %v, cursor %v, pen %v, alt %v, scrollback %d)",
				seed, try, op.name, a.Width(), a.Height(), a.scr.scroll, a.scr.cur.Position, a.scr.cur.Pen, a.IsAltScreen(), size)
			if d := stateDiff(a.State(), b.State()); d != "" {
				t.Fatalf("%s: %s", where, d)
			}
			for i := range a.scrs {
				if !reflect.DeepEqual(touchedOf(&a.scrs[i]), touchedOf(&b.scrs[i])) {
					t.Fatalf("%s: the touched lines of screen %d differ: %v and %v", where, i, touchedOf(&a.scrs[i]), touchedOf(&b.scrs[i]))
				}
			}
			if !reflect.DeepEqual(scrollbackOf(a), scrollbackOf(b)) {
				t.Fatalf("%s: the scrollbacks differ: %d and %d lines", where, a.ScrollbackLen(), b.ScrollbackLen())
			}
			if d := checkMarks(a); d != "" {
				t.Fatalf("%s: %s", where, d)
			}
			compared++
			byName[op.name]++
		}
	}
	t.Logf("%d sequences compared", compared)
	if compared < int(seeds)*20 {
		t.Errorf("%d sequences compared", compared)
	}
	for _, name := range []string{"ED 0", "ED 1", "ED 2", "ED 3", "EL 0", "EL 1", "EL 2", "IND", "RI", "RIS", "alternate screen on", "alternate screen off"} {
		if byName[name] == 0 {
			t.Errorf("%s was never compared", name)
		}
	}
}

// A scroll in a region that cuts off only the edges of the lines (margins in
// the second column and the last but one, say) moves the lines and puts the
// cells outside the region back (Screen.shiftColumns), which is what the
// buffer's functions leave, wide characters cut by the edges of the region
// included: the repair of a wide cell at an edge reads the cells the lines
// that come in had before, and writes outside the region.
func TestNarrowScrollKeepsWhatIsOutside(t *testing.T) {
	glyphs := []string{"a", "b", "\u4e16", "\u754c", " ", "e\u0301", "\U0001f468"}
	seeds := uint64(100)
	if raceEnabled {
		seeds = 25
	}
	compared := 0
	for seed := uint64(1); seed <= seeds; seed++ {
		rng := rand.New(rand.NewPCG(seed, 31))
		w, h := 12+rng.IntN(20), 10+rng.IntN(25)
		a := NewEmulator(w, h)
		a.SetScrollbackSize(0)
		var text strings.Builder
		for i := 0; i < w*h; i++ {
			text.WriteString(glyphs[rng.IntN(len(glyphs))])
			if rng.IntN(3) == 0 {
				text.WriteString(fmt.Sprintf("\x1b[%dm", []int{0, 1, 31, 42, 7}[rng.IntN(5)]))
			}
		}
		a.Write([]byte(text.String()))
		for try := 0; try < 12; try++ {
			left := 1 + rng.IntN(3)  // 1-based, the edge cut off is 0 to 2 columns
			right := w - rng.IntN(3) // 1-based, inclusive
			top, bottom := 1+rng.IntN(2), h-rng.IntN(2)
			a.Write([]byte(fmt.Sprintf("\x1b[?69h\x1b[%d;%ds\x1b[%d;%dr", left, right, top, bottom)))
			if left == 1 && right == w {
				continue // as wide as the screen: not the region under test
			}
			if rng.IntN(2) == 0 {
				a.Write([]byte("\x1b[44m"))
			}
			b := NewEmulator(2, 2)
			b.SetScrollbackSize(0)
			if err := b.SetState(a.State()); err != nil {
				t.Fatal(err)
			}
			// A count of a tenth of the lines or less is the one that moves the
			// lines (Screen.moveColumns); a larger one moves the cells.
			n := 1 + rng.IntN(h)
			if rng.IntN(2) == 0 {
				n = 1 + rng.IntN(max(1, h/10))
			}
			x, y := rng.IntN(w), rng.IntN(h)
			a.scr.setCursor(x, y, false)
			b.scr.setCursor(x, y, false)
			for i := range a.scrs {
				a.scrs[i].ClearTouched()
				b.scrs[i].ClearTouched()
			}
			kind := rng.IntN(4)
			seqs := []string{"L", "M", "S", "T"}
			a.Write([]byte(fmt.Sprintf("\x1b[%d%s", n, seqs[kind])))
			switch kind {
			case 0:
				refIL(b, n)
			case 1:
				refDL(b, n)
			case 2:
				refScrollUp(b.scr, n)
			case 3:
				refScrollDown(b.scr, n)
			}
			where := fmt.Sprintf("seed %d try %d: %d%s on %dx%d with margins %d-%d and lines %d-%d, cursor %d,%d", seed, try, n, seqs[kind], w, h, left, right, top, bottom, x, y)
			if d := stateDiff(a.State(), b.State()); d != "" {
				t.Fatalf("%s: %s", where, d)
			}
			if !reflect.DeepEqual(touchedOf(a.scr), touchedOf(b.scr)) {
				t.Fatalf("%s: the touched lines differ", where)
			}
			if d := checkMarks(a); d != "" {
				t.Fatalf("%s: %s", where, d)
			}
			compared++
		}
	}
	t.Logf("%d scrolls compared", compared)
	if compared < int(seeds)*4 {
		t.Errorf("%d scrolls compared", compared)
	}
}

// What the marks know never goes wrong, whatever follows what: a stream that
// blanks lines and writes to them, and keeps the marks from one sequence to
// the next, ends every write in the state of the same stream with the marks
// dropped before every sequence and every control character. The marks are also what the cells are, at the end
// of every write.
func TestMarksAreTransparent(t *testing.T) {
	extra := []string{"\x1b[2J", "\x1b[2J", "\x1b[H", "\x1b[44m", "\x1b[m", "\x1b[3J", "\x1b[0J", "\x1b[1J",
		"\x1b[J", "\x1b[2K", "\x1b[200S", "\x1b[200T", "\x1b[9L", "\x1b[9M", "\x1bc", "\x1b[?1049h", "\x1b[?1049l",
		"\x1b[?69h\x1b[2;4s", "\x1b[2;3r", "\x1b[r", "\x1b[50b", "\x1b[400b", "q", "\n", "\x1bM", "\x1b[41m", "\x1b[7m"}
	seeds := uint64(70)
	if raceEnabled {
		seeds = 15
	}
	chunks := 0
	for seed := uint64(1); seed <= seeds; seed++ {
		rng := rand.New(rand.NewPCG(seed, 29))
		w, h, ops := stateStream(rng, 150)
		a, c := NewEmulator(w, h), NewEmulator(w, h)
		size := []int{DefaultScrollbackSize, 0, 3}[seed%3]
		a.SetScrollbackSize(size)
		c.SetScrollbackSize(size)
		var all []stateOp
		for _, op := range ops {
			all = append(all, op)
			if op.data != nil && rng.IntN(2) == 0 {
				all = append(all, stateOp{data: []byte(extra[rng.IntN(len(extra))] + extra[rng.IntN(len(extra))])})
			}
		}
		for i, op := range all {
			applyStateOp(a, op)
			if op.data == nil {
				applyStateOp(c, op)
			} else {
				// Every control character and escape starts a piece of its own.
				// (Nothing is cut inside a character, which splits the same
				// text another way.)
				data := op.data
				for len(data) > 0 {
					n := 1
					for n < len(data) && data[n] >= 0x20 {
						n++
					}
					dropMarks(c)
					c.Write(data[:n])
					data = data[n:]
				}
			}
			if d := checkMarks(a); d != "" {
				t.Fatalf("seed %d, op %d: %s", seed, i, d)
			}
			if d := stateDiff(a.State(), c.State()); d != "" {
				t.Fatalf("seed %d, op %d (%q): %s", seed, i, op.data, d)
			}
			if !reflect.DeepEqual(scrollbackOf(a), scrollbackOf(c)) {
				t.Fatalf("seed %d, op %d: the scrollbacks differ", seed, i)
			}
			chunks++
		}
	}
	t.Logf("%d writes compared", chunks)
}

// A line that is marked as blank is not written again by an erase to the same
// cell: the cell of a marked line that the test changes behind the emulator's
// back is still there after the erase, and is gone after an erase to another
// cell.
func TestBlankingABlankLineWritesNothing(t *testing.T) {
	e := NewEmulator(20, 4)
	e.Write([]byte("abc\r\ndef\x1b[2J"))
	if d := checkMarks(e); d != "" {
		t.Fatal(d)
	}
	for y := range e.scr.buf.Lines {
		if e.scr.marks[y].cell != &blankFill {
			t.Fatalf("line %d is not marked blank after an erase", y)
		}
		e.scr.buf.Lines[y][3].Content = "Z" // behind the marks' back
	}
	e.Write([]byte("\x1b[2J\x1b[J\x1b[1J\x1bc\x1b[?1049h\x1b[?1049l\x1b[4S\x1b[4T"))
	for y := range e.scr.buf.Lines {
		if e.scr.buf.Lines[y][3].Content != "Z" {
			t.Errorf("line %d was written by an erase to the cell it already holds", y)
		}
	}
	// The same erase with a background is another cell, and writes.
	e.Write([]byte("\x1b[H\x1b[44m\x1b[J"))
	for y := range e.scr.buf.Lines {
		if c := e.scr.buf.Lines[y][3]; c.Content != " " || c.Style.Bg == nil {
			t.Errorf("line %d holds %#v after an erase with a background", y, c)
		}
	}
	if d := checkMarks(e); d != "" {
		t.Fatal(d)
	}

	// A part of a line the same way: the erase of the region of a narrow scroll
	// is skipped when the cells are blank already.
	e = NewEmulator(20, 4)
	e.Write([]byte(strings.Repeat("x", 80) + "\x1b[?69h\x1b[3;10s\x1b[4S"))
	for y := range e.scr.buf.Lines {
		if m := e.scr.marks[y]; !m.part || m.from != 2 || m.to != 10 {
			t.Fatalf("line %d: mark %+v, want the part 2 to 10", y, m)
		}
		e.scr.buf.Lines[y][5].Content = "Z"
		e.scr.buf.Lines[y][12].Content = "Y"
	}
	e.Write([]byte("\x1b[4S"))
	for y := range e.scr.buf.Lines {
		if e.scr.buf.Lines[y][5].Content != "Z" {
			t.Errorf("line %d was written again", y)
		}
		e.scr.buf.Lines[y][5].Content = " " // what the marks say
		e.scr.buf.Lines[y][12].Content = "x"
	}
	// A change of one cell of the part drops the mark, and the erase writes.
	e.Write([]byte("\x1b[1;5Hq\x1b[4S"))
	if c := e.scr.buf.Lines[0][5]; c.Content != " " {
		t.Errorf("the erase did not write the line a character was printed in: %#v", c)
	}
	if d := checkMarks(e); d != "" {
		t.Fatal(d)
	}
}

// The recount of the links reads the cells of a line that has a part marked
// blank, which has links outside the part, and skips the line that is marked
// blank whole.
func TestRecountSeesLinksOutsideAMarkedPart(t *testing.T) {
	e := NewEmulator(20, 2)
	e.Write([]byte("\x1b]8;;http://a.example/1\x1b\\" + strings.Repeat("x", 40) + "\x1b]8;;\x1b\\"))
	e.Write([]byte("\x1b[?69h\x1b[3;10s\x1b[2S"))
	if m := e.scr.marks[0]; !m.part {
		t.Fatalf("mark %+v, want a part", m)
	}
	e.scr.recountLinks()
	if len(e.scr.links.set) != 1 {
		t.Errorf("the recount found %d links, want the 1 outside the part", len(e.scr.links.set))
	}
	e.Write([]byte("\x1b[?69l\x1b[2J"))
	e.scr.recountLinks()
	if len(e.scr.links.set) != 0 {
		t.Errorf("the recount found %d links on a blank screen", len(e.scr.links.set))
	}
}

// Readers that share SafeEmulator's read lock change nothing: after a write
// that blanks the whole screen, no line is left to write, so every way of
// reading finds the cells and none of them changes anything (the race
// detector sees it if one does).
func TestReadersOfBlankedLinesChangeNothing(t *testing.T) {
	se := NewSafeEmulator(80, 24)
	se.Write([]byte("hello\r\nworld"))
	se.Write([]byte("\x1b[44m\x1b[2J\x1b[?1049h\x1b[41m\x1b[J\x1bM\x1b[3T"))
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_ = se.State()
				_ = se.Render()
				_ = se.String()
				for y := 0; y < 24; y++ {
					if c := se.CellAt(5, y); c == nil || c.Content != " " {
						t.Errorf("cell 5,%d is %#v", y, c)
					}
				}
			}
		}()
	}
	wg.Wait()
}
