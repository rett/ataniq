package sessionpane

import (
	"strings"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
)

// FaintSpan marks a run of faint (dim, SGR 2) characters on one row of a
// TextSnapshot. Row is the zero-based row; From and To are zero-based
// character (rune) offsets in that row of the snapshot's Text, From included
// and To excluded. They are not cell columns: a wide character covers two
// cells and is one character of the text, so the two differ after any wide
// character on the row.
type FaintSpan struct{ Row, From, To int }

// TextSnapshot is the screen as plain text, with what a reader of the text
// needs beside it. It is the real state of the emulator, never the frame held
// for synchronized output (mode 2026), and it is one moment: it is taken under
// the pane's lock.
type TextSnapshot struct {
	// Text is every row of the screen, rows joined with "\n", each row
	// without its trailing blanks. A cell with nothing in it is a space,
	// each character is copied once however many cells it covers (a wide
	// character, or a cluster, is taken from its first cell), and a cell
	// whose content holds a control character is a placeholder of its
	// width, as Text does.
	Text string
	// Cols and Rows are the size of the screen, and CursorX and CursorY the
	// zero-based cursor cell: a column, not an offset in the row's text.
	Cols, Rows, CursorX, CursorY int
	CursorHidden                 bool
	// AltScreen is true while the alternate screen is the active one.
	AltScreen bool
	// Title is the window title, as Title returns it.
	Title string
	// Faint lists the runs of faint characters, rows in order and runs in
	// order on a row. A run is cut at the end of its row's text, so a run
	// that lies in the trailing blanks is not listed. Each span is from a
	// run of cells that all have the faint attribute, blank cells included,
	// so the hint a composer shows is one run.
	Faint []FaintSpan
}

// TextSnapshot returns the screen as text with its faint spans, the cursor,
// the alternate screen flag and the title. See [TextSnapshot].
func (p *Pane) TextSnapshot() TextSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := p.emu.CursorPosition()
	ts := TextSnapshot{
		Cols: p.cols, Rows: p.rows,
		CursorX: cur.X, CursorY: cur.Y,
		CursorHidden: p.emu.CursorHidden(),
		AltScreen:    p.emu.IsAltScreen(),
		Title:        p.title,
	}
	var b strings.Builder
	for y := 0; y < p.rows; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		ts.Faint = p.appendRow(&b, ts.Faint, y)
	}
	ts.Text = b.String()
	return ts
}

// appendRow writes row y's text to b, without its trailing blanks, and
// appends the row's faint spans, in rune offsets of that text.
func (p *Pane) appendRow(b *strings.Builder, spans []FaintSpan, y int) []FaintSpan {
	var (
		row   strings.Builder
		runes int // characters written to row so far
		// textRunes is the length of the row's text: the characters up to
		// and including the last cell that is not blank.
		textRunes int
		textBytes int
		// runFrom is the offset where the open faint run began, or -1.
		runFrom = -1
		runs    []FaintSpan
	)
	closeRun := func(to int) {
		if runFrom >= 0 && to > runFrom {
			runs = append(runs, FaintSpan{Row: y, From: runFrom, To: to})
		}
		runFrom = -1
	}
	for x := 0; x < p.cols; x++ {
		c := p.emu.CellAt(x, y)
		if c == nil || c.Width <= 0 {
			// A cell a wide character covers after its first: it adds no
			// character and does not break a run.
			continue
		}
		s := c.Content
		blank := s == "" || s == " "
		switch {
		case blank:
			s = " "
		case hasControl(s):
			s = placeholder(c.Width)
		}
		faint := c.Style.Attrs&uv.AttrFaint != 0
		if faint && runFrom < 0 {
			runFrom = runes
		} else if !faint {
			closeRun(runes)
		}
		row.WriteString(s)
		runes += utf8.RuneCountInString(s)
		if !blank {
			textRunes, textBytes = runes, row.Len()
		}
	}
	closeRun(runes)
	b.WriteString(row.String()[:textBytes])
	for _, sp := range runs {
		sp.To = min(sp.To, textRunes)
		if sp.From < sp.To {
			spans = append(spans, sp)
		}
	}
	return spans
}

// SyncReplies returns when every reply the pane has made so far, to a query
// the output it was given asked or through a handler of HandleOSC, has been
// handed to Options.Replies. A reply is written to the reply pipe while Write
// holds the pane's lock, and the pipe's writer returns when the drain
// goroutine has read the bytes, which is before that goroutine has called
// Options.Replies with them: Write can return a moment before the last reply
// reaches the receiver. A caller that must see them all after it applied
// output (the session's Settle) calls SyncReplies after Write. It changes no
// state of the pane. It must not be called from Options.Replies, nor from a
// handler of HandleOSC. After Close it returns at once.
func (p *Pane) SyncReplies() {
	// An empty write is delivered to the pipe's reader like any other, so
	// it returns only when the drain goroutine, which handles one read at a
	// time, has finished with the replies read before it. It reaches the
	// emulator's reader with nothing to hand on, so no reply is made.
	_, _ = p.pipe.Write(nil)
}
