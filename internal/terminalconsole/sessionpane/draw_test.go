package sessionpane_test

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/terminalconsole/sessionpane"
)

// Both buffer kinds of ultraviolet are canvases.
var (
	_ sessionpane.Canvas = (*uv.Buffer)(nil)
	_ sessionpane.Canvas = uv.ScreenBuffer{}
	_ sessionpane.Canvas = uv.Screen(nil)
)

const family = "\U0001F468‍\U0001F469‍\U0001F467‍\U0001F466"

// draw draws p into a fresh buffer of its own size and returns the buffer.
func draw(p *sessionpane.Pane) *uv.Buffer {
	cols, rows := p.Size()
	buf := uv.NewBuffer(cols, rows)
	p.Draw(buf, buf.Bounds())
	return buf
}

// row returns the content of row y of buf as the outer terminal receives it,
// the first column of each wide cell only.
func row(buf *uv.Buffer, y int) string {
	var b strings.Builder
	for x := 0; x < buf.Width(); {
		c := buf.CellAt(x, y)
		if c == nil || c.Width == 0 {
			x++
			continue
		}
		b.WriteString(c.Content)
		x += c.Width
	}
	return strings.TrimRight(b.String(), " ")
}

// checkGeometry fails when a cell of buf would be drawn by the outer terminal
// at another width than its cell width, under the given width method. That is
// what would shift the columns after it.
func checkGeometry(t *testing.T, buf *uv.Buffer, m ansi.Method) {
	t.Helper()
	for y := 0; y < buf.Height(); y++ {
		for x := 0; x < buf.Width(); {
			c := buf.CellAt(x, y)
			if c.Width == 0 {
				x++
				continue
			}
			if got := m.StringWidth(c.Content); got != c.Width {
				t.Errorf("cell %d,%d %q: width %d, the outer terminal draws %d", x, y, c.Content, c.Width, got)
			}
			x += c.Width
		}
	}
}

func TestLegacyWidthPlaceholderInTheZWJFixture(t *testing.T) {
	rec := fixture(t, "zwj-split.jsonl")
	legacy := play(t, rec, sessionpane.Options{})
	modern := play(t, rec, sessionpane.Options{GraphemeWidths: true})

	lb, mb := draw(legacy), draw(modern)
	if got, want := row(lb, 1), "family ？ and technologist ？"; got != want {
		t.Errorf("legacy row %q, want %q", got, want)
	}
	if got, want := row(mb, 1), "family "+family+" and technologist \U0001F469‍\U0001F4BB"; got != want {
		t.Errorf("grapheme row %q, want %q", got, want)
	}
	checkGeometry(t, lb, ansi.WcWidth)
	checkGeometry(t, mb, ansi.GraphemeWidth)
	// The geometry is the pane's in both modes: every cell is where it was.
	for x := 0; x < lb.Width(); x++ {
		l, m := lb.CellAt(x, 1), mb.CellAt(x, 1)
		if l.Width != m.Width {
			t.Fatalf("column %d: width %d in legacy mode, %d with grapheme widths", x, l.Width, m.Width)
		}
		if l.Width == 1 && l.Content != m.Content {
			t.Fatalf("column %d: %q in legacy mode, %q with grapheme widths", x, l.Content, m.Content)
		}
	}
}

func TestLegacyWidthPlaceholderOnlyWhereWidthsDiffer(t *testing.T) {
	text := "a" + family + "b⚠️céd漢e\U0001F44D\U0001F3FDf"
	for _, c := range []struct {
		graphemes bool
		want      string
	}{
		// Kept: "e" with a combining accent (one column either way) and a
		// single wide character. Replaced: the family, the warning sign with
		// VS16, and the thumb with a skin tone.
		{false, "a？b？céd漢e？f"},
		{true, text},
	} {
		p := newPane(t, 40, 2, sessionpane.Options{GraphemeWidths: c.graphemes})
		p.Write([]byte(text))
		buf := draw(p)
		if got := row(buf, 0); got != c.want {
			t.Errorf("grapheme widths %v: %q, want %q", c.graphemes, got, c.want)
		}
		method := ansi.WcWidth
		if c.graphemes {
			method = ansi.GraphemeWidth
		}
		checkGeometry(t, buf, method)
	}
}

func TestLegacyWidthPlaceholderFollowsSetGraphemeWidths(t *testing.T) {
	p := newPane(t, 20, 1, sessionpane.Options{})
	p.Write([]byte("x" + family + "y"))
	if got := row(draw(p), 0); got != "x？y" {
		t.Fatalf("legacy: %q", got)
	}
	p.SetGraphemeWidths(true)
	if got := row(draw(p), 0); got != "x"+family+"y" {
		t.Fatalf("grapheme widths: %q", got)
	}
	p.SetGraphemeWidths(false)
	if got := row(draw(p), 0); got != "x？y" {
		t.Fatalf("legacy again: %q", got)
	}
}

func TestLegacyWidthPlaceholderKeepsTheStyle(t *testing.T) {
	p := newPane(t, 10, 1, sessionpane.Options{})
	p.Write([]byte("\x1b[1;31m" + family))
	c := draw(p).CellAt(0, 0)
	if c.Content != "？" || c.Width != 2 || c.Style.Attrs&uv.AttrBold == 0 || c.Style.Fg == nil {
		t.Fatalf("placeholder cell %+v", c)
	}
}

func TestBoundsDrawOnlyInsideTheArea(t *testing.T) {
	p := newPane(t, 6, 2, sessionpane.Options{GraphemeWidths: true})
	p.Write([]byte("abcd漢\r\n123456"))

	fill := func(buf *uv.Buffer) {
		for y := 0; y < buf.Height(); y++ {
			for x := 0; x < buf.Width(); x++ {
				buf.SetCell(x, y, &uv.Cell{Content: "#", Width: 1})
			}
		}
	}
	outside := func(t *testing.T, buf *uv.Buffer, area uv.Rectangle) {
		t.Helper()
		for y := 0; y < buf.Height(); y++ {
			for x := 0; x < buf.Width(); x++ {
				if !(uv.Pos(x, y).In(area)) && buf.CellAt(x, y).Content != "#" {
					t.Fatalf("cell %d,%d outside %v changed to %q", x, y, area, buf.CellAt(x, y).Content)
				}
			}
		}
	}

	t.Run("offset", func(t *testing.T) {
		buf := uv.NewBuffer(12, 5)
		fill(buf)
		area := uv.Rect(3, 1, 6, 2)
		p.Draw(buf, area)
		outside(t, buf, area)
		if got := row(buf, 1); got != "###abcd漢###" {
			t.Fatalf("row 1 %q", got)
		}
		if got := row(buf, 2); got != "###123456###" {
			t.Fatalf("row 2 %q", got)
		}
	})
	t.Run("cut", func(t *testing.T) {
		// Five columns: the wide character at columns 4 and 5 does not fit
		// and is drawn as a blank, not half drawn.
		buf := uv.NewBuffer(8, 3)
		fill(buf)
		area := uv.Rect(1, 1, 5, 1)
		p.Draw(buf, area)
		outside(t, buf, area)
		if got := row(buf, 1); got != "#abcd ##" {
			t.Fatalf("row %q", got)
		}
	})
	t.Run("larger", func(t *testing.T) {
		buf := uv.NewBuffer(10, 4)
		fill(buf)
		p.Draw(buf, buf.Bounds())
		for y, want := range []string{"abcd漢", "123456", "", ""} {
			if got := row(buf, y); got != want {
				t.Fatalf("row %d %q, want %q", y, got, want)
			}
		}
	})
	t.Run("beyond the screen", func(t *testing.T) {
		buf := uv.NewBuffer(4, 2)
		p.Draw(buf, uv.Rect(2, 1, 6, 2))
		p.Draw(buf, uv.Rect(10, 10, 6, 2))
		p.Draw(buf, uv.Rect(-3, -1, 6, 2))
		p.Draw(buf, uv.Rectangle{})
	})
}

func TestBoundsDrawDropsHyperlinks(t *testing.T) {
	p := newPane(t, 20, 1, sessionpane.Options{})
	p.Write([]byte("\x1b]8;;https://example.invalid/x\x1b\\link\x1b]8;;\x1b\\"))
	buf := draw(p)
	if got := row(buf, 0); got != "link" {
		t.Fatalf("row %q", got)
	}
	for x := 0; x < buf.Width(); x++ {
		if l := buf.CellAt(x, 0).Link; l.URL != "" || l.Params != "" {
			t.Fatalf("cell %d keeps link %+v", x, l)
		}
	}
	if r := buf.Render(); strings.Contains(r, "\x1b]") || strings.Contains(r, "example") {
		t.Fatalf("rendered pane contains an OSC: %q", r)
	}
}

func TestBoundsCursorInsideThePane(t *testing.T) {
	p := newPane(t, 10, 4, sessionpane.Options{})
	p.Write([]byte("\x1b[3;5H"))
	if pos, visible := p.Cursor(); pos != uv.Pos(4, 2) || !visible {
		t.Fatalf("cursor %v visible %v", pos, visible)
	}
	p.Write([]byte("\x1b[999;999H"))
	if pos, _ := p.Cursor(); pos != uv.Pos(9, 3) {
		t.Fatalf("cursor %v", pos)
	}
	p.Resize(5, 2)
	if pos, _ := p.Cursor(); pos.X >= 5 || pos.Y >= 2 {
		t.Fatalf("cursor %v outside a 5x2 pane", pos)
	}
	p.Write([]byte("\x1b[?25l"))
	if _, visible := p.Cursor(); visible {
		t.Fatal("hidden cursor visible")
	}
	p.Write([]byte("\x1b[?25h"))
	p.End(0, true)
	if _, visible := p.Cursor(); visible {
		t.Fatal("cursor visible after End")
	}
}

func TestEndedKeepsTheLastScreen(t *testing.T) {
	rec := fixture(t, "exit-nonzero.jsonl")
	p := play(t, rec, sessionpane.Options{})
	if ended, status, known := p.Ended(); !ended || status != 3 || !known {
		t.Fatalf("Ended() = %v, %d, %v", ended, status, known)
	}
	buf := draw(p)
	for y, want := range []string{"failure test", "step 1 ok", "step 2 failed", "exit status 3"} {
		if got := row(buf, y); got != want {
			t.Errorf("row %d %q, want %q", y, got, want)
		}
	}
	p.Write([]byte("\x1b[2J"))
	if got := row(draw(p), 0); got != "failure test" {
		t.Fatalf("a write after End cleared the screen: %q", got)
	}

	p = play(t, fixture(t, "exit-unknown.jsonl"), sessionpane.Options{})
	if ended, status, known := p.Ended(); !ended || status != 0 || known {
		t.Fatalf("unknown status: Ended() = %v, %d, %v", ended, status, known)
	}
	if got := row(draw(p), 2); got != "no exit status was kept" {
		t.Fatalf("row 2 %q", got)
	}
}
