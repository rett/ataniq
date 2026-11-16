package sessionpane_test

import (
	"testing"
	"time"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/terminalconsole/sessionpane"
	"ataniq/internal/terminalconsole/sessionreplay"
)

// A fuzz input is a program for a pane. Byte 0 is the starting width and
// byte 1, modulo 64, the starting height (both clamped by the pane). Then
// each operation starts with a byte op:
//
//   - op&3 == 0: resize to byte*2 columns and byte rows (two more bytes), so
//     both caps and both minimums are crossed;
//   - op&3 == 1: end, with status op>>3, known when op&4 is set;
//   - otherwise: write the next op>>2 + 1 bytes (1 to 64).
//
// Every operation also moves the clock 20 ms and flips the grapheme width
// mode when op&0x80 is set.
const (
	opResize = 0
	opEnd    = 1
	opWrite  = 2
	maxWrite = 64
)

// encode turns a recording into a fuzz input.
func encode(rec *sessionreplay.Recording) []byte {
	b := []byte{byte(rec.Cols), byte(rec.Rows)}
	for _, e := range rec.Events {
		switch e.Kind {
		case sessionreplay.KindOutput:
			for d := e.Data; len(d) > 0; {
				n := min(len(d), maxWrite)
				b = append(b, byte((n-1)<<2|opWrite))
				b = append(b, d[:n]...)
				d = d[n:]
			}
		case sessionreplay.KindResize:
			b = append(b, opResize, byte(e.Cols/2), byte(e.Rows))
		case sessionreplay.KindEnd:
			op := byte(e.Status<<3 | opEnd)
			if e.StatusKnown {
				op |= 4
			}
			b = append(b, op)
		}
	}
	return b
}

// seeds returns the fuzz seed corpus: every fixture of the replay package,
// and a few inputs written by hand.
func seeds(t testing.TB) [][]byte {
	t.Helper()
	var out [][]byte
	for _, name := range sessionreplay.FixtureNames() {
		out = append(out, encode(fixture(t, name)))
	}
	w := func(s string) []byte {
		var b []byte
		for len(s) > 0 {
			n := min(len(s), maxWrite)
			b = append(b, byte((n-1)<<2|opWrite))
			b = append(b, s[:n]...)
			s = s[n:]
		}
		return b
	}
	cat := func(parts ...[]byte) []byte {
		var b []byte
		for _, p := range parts {
			b = append(b, p...)
		}
		return b
	}
	out = append(out,
		cat([]byte{80, 24}, w("\x1b]0;✳ x\x07\x1b]2;✅ y\x1b\\"), []byte{opResize, 255, 255}, w("\x1b[999;999Hz")),
		cat([]byte{10, 3}, w("\x1b[?2026hframe"), []byte{opResize, 0, 0}, w("\x1b[?2026l\x1b[?2026h\xe2\x1b[?2026l")),
		cat([]byte{20, 5}, w("\x1b]52;c;aGk=\x07\x1b]8;;x\x1b\\l\x1b[?1003;1006h"), []byte{opEnd | 4 | 3<<3}, w("ignored")),
		cat([]byte{2, 1}, w(family+"⚠️漢\x1bc\x1b[?2048h"), []byte{opResize | 0x80, 250, 200}),
		cat([]byte{0, 0}, w("\x1b]2;"+string(make([]byte, 600))+"\x07\x1bP\x1b[?2026l")),
	)
	return out
}

// exercise runs a fuzz input against a pane and checks what must always
// hold: the size within the limits, a bounded and clean title, a cursor in
// the pane, and drawn cells that are safe to send to the outer terminal and
// keep its geometry.
func exercise(t *testing.T, data []byte) {
	if len(data) < 2 {
		return
	}
	c := newClock()
	p := sessionpane.New(int(data[0]), int(data[1]%64), sessionpane.Options{Clock: c.Now, Replies: func([]byte) {}})
	defer p.Close()
	graphemes := false
	ended := false
	rest := data[2:]
	for len(rest) > 0 {
		op := rest[0]
		rest = rest[1:]
		switch op & 3 {
		case opResize:
			if len(rest) < 2 {
				return
			}
			p.Resize(int(rest[0])*2, int(rest[1]))
			rest = rest[2:]
		case opEnd:
			p.End(int(op>>3), op&4 != 0)
			ended = true
		default:
			n := min(int(op>>2)+1, len(rest))
			p.Write(rest[:n])
			rest = rest[n:]
		}
		c.advance(20 * time.Millisecond)
		if op&0x80 != 0 {
			graphemes = !graphemes
			p.SetGraphemeWidths(graphemes)
		}
		check(t, p, graphemes)
		if e, _, _ := p.Ended(); e != ended {
			t.Fatalf("ended %v, want %v", e, ended)
		}
	}
}

func check(t *testing.T, p *sessionpane.Pane, graphemes bool) {
	t.Helper()
	cols, rows := p.Size()
	if cols < sessionpane.MinCols || cols > sessionpane.MaxCols || rows < sessionpane.MinRows || rows > sessionpane.MaxRows {
		t.Fatalf("size %dx%d outside the limits", cols, rows)
	}
	title := p.Title()
	if len(title) > sessionpane.MaxTitle || !utf8.ValidString(title) {
		t.Fatalf("title %q", title)
	}
	for _, r := range title {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			t.Fatalf("title %q holds a control character", title)
		}
	}
	if pos, _ := p.Cursor(); pos.X < 0 || pos.Y < 0 || pos.X >= cols || pos.Y >= rows {
		t.Fatalf("cursor %v outside %dx%d", pos, cols, rows)
	}
	_ = p.Modes()
	_, _ = p.HoldDeadline()
	p.Mouse(uv.MouseMotionEvent{X: cols - 1, Y: rows - 1}, uv.Rect(0, 0, cols, rows))

	buf := uv.NewBuffer(cols+2, rows+1)
	area := uv.Rect(1, 1, cols, rows)
	p.Draw(buf, area)
	method := ansi.WcWidth
	if graphemes {
		method = ansi.GraphemeWidth
	}
	for y := 0; y < buf.Height(); y++ {
		for x := 0; x < buf.Width(); {
			cell := buf.CellAt(x, y)
			if cell.Width == 0 {
				x++
				continue
			}
			if cell.Link.URL != "" || cell.Link.Params != "" {
				t.Fatalf("cell %d,%d keeps a link", x, y)
			}
			for _, r := range cell.Content {
				if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
					t.Fatalf("cell %d,%d holds control characters: %q", x, y, cell.Content)
				}
			}
			if w := method.StringWidth(cell.Content); w != cell.Width {
				t.Fatalf("cell %d,%d %q: width %d, measured %d", x, y, cell.Content, cell.Width, w)
			}
			x += cell.Width
		}
	}
}

func FuzzWriteResize(f *testing.F) {
	for _, s := range seeds(f) {
		f.Add(s)
	}
	f.Fuzz(exercise)
}

// TestBoundsFuzzCorpus runs the fuzz checks over the seed corpus, so that
// the test name pattern of this package selects them too.
func TestBoundsFuzzCorpus(t *testing.T) {
	s := seeds(t)
	if len(s) < len(sessionreplay.FixtureNames())+5 {
		t.Fatalf("%d seeds", len(s))
	}
	for _, data := range s {
		t.Run("", func(t *testing.T) { exercise(t, data) })
	}
}
