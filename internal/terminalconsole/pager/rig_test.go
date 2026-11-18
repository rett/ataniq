package pager

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// fakeSource is a Source for tests. It fails the test when a page is asked
// for anywhere but inside a command that the rig runs: not from Update, not
// from SetSize and not from Draw.
type fakeSource struct {
	t     *testing.T
	page  func(n int) ([]string, []bool, int, error)
	inCmd bool
	calls []int
}

func (f *fakeSource) Page(n int) ([]string, []bool, int, error) {
	if !f.inCmd {
		f.t.Fatalf("Page(%d) was called outside a command", n)
	}
	f.calls = append(f.calls, n)
	return f.page(n)
}

// tpage is one page of a test patch.
type tpage struct {
	lines []string
	cont  []bool
}

// serve returns the page function of a patch held as pages.
func serve(pages []tpage) func(int) ([]string, []bool, int, error) {
	return func(n int) ([]string, []bool, int, error) {
		return pages[n].lines, pages[n].cont, len(pages), nil
	}
}

// paginate cuts lines into pieces of at most pieceBytes bytes (0: never
// cut) and puts perPage pieces on a page. It cuts at any byte, also inside
// a character.
func paginate(lines []string, perPage, pieceBytes int) []tpage {
	var pieces []string
	var cont []bool
	for _, l := range lines {
		for pieceBytes > 0 && len(l) > pieceBytes {
			pieces, cont = append(pieces, l[:pieceBytes]), append(cont, true)
			l = l[pieceBytes:]
		}
		pieces, cont = append(pieces, l), append(cont, false)
	}
	var pages []tpage
	for len(pieces) > perPage {
		pages = append(pages, tpage{pieces[:perPage], cont[:perPage]})
		pieces, cont = pieces[perPage:], cont[perPage:]
	}
	return append(pages, tpage{pieces, cont})
}

// rig drives a pager as a parent would: it runs every command the pager
// returns and hands the answer back.
type rig struct {
	t    *testing.T
	m    *Model
	src  *fakeSource
	w, h int
	buf  *uv.Buffer
}

func newRig(t *testing.T, w, h int, page func(int) ([]string, []bool, int, error)) *rig {
	t.Helper()
	r := &rig{t: t, src: &fakeSource{t: t, page: page}}
	r.m = New(r.src, Options{})
	r.size(w, h)
	r.run(r.m.Init())
	return r
}

// run runs cmd and every command that follows from its answer.
func (r *rig) run(cmd tea.Cmd) {
	r.t.Helper()
	for cmd != nil {
		msg := r.exec(cmd)
		var act Action
		cmd, act = r.m.Update(msg)
		if act != None {
			r.t.Fatalf("a page answer asked the parent for action %d", act)
		}
	}
}

// exec runs one command. No command of the pager may end the program.
func (r *rig) exec(cmd tea.Cmd) tea.Msg {
	r.t.Helper()
	r.src.inCmd = true
	msg := cmd()
	r.src.inCmd = false
	switch msg.(type) {
	case tea.QuitMsg, tea.SuspendMsg, tea.InterruptMsg:
		r.t.Fatalf("a command of the pager returned %T", msg)
	}
	return msg
}

func (r *rig) size(w, h int) {
	r.t.Helper()
	r.w, r.h = w, h
	r.run(r.m.SetSize(w, h))
}

func (r *rig) key(k tea.KeyPressMsg) Action {
	r.t.Helper()
	cmd, act := r.m.Update(k)
	r.run(cmd)
	return act
}

func (r *rig) press(keys ...tea.KeyPressMsg) {
	r.t.Helper()
	for _, k := range keys {
		if act := r.key(k); act != None {
			r.t.Fatalf("key %s: action %d", k.String(), act)
		}
	}
}

var (
	keyJ    = tea.KeyPressMsg{Code: 'j', Text: "j"}
	keyK    = tea.KeyPressMsg{Code: 'k', Text: "k"}
	keyDown = tea.KeyPressMsg{Code: tea.KeyDown}
	keyUp   = tea.KeyPressMsg{Code: tea.KeyUp}
	keyPgDn = tea.KeyPressMsg{Code: tea.KeyPgDown}
	keyPgUp = tea.KeyPressMsg{Code: tea.KeyPgUp}
	keyHome = tea.KeyPressMsg{Code: tea.KeyHome}
	keyEnd  = tea.KeyPressMsg{Code: tea.KeyEnd}
	keyEsc  = tea.KeyPressMsg{Code: tea.KeyEscape}
)

// draw draws the pager into a buffer of its own size. The buffer is used
// again for the next drawing of that size: Draw writes all of its area.
func (r *rig) draw() *uv.Buffer {
	if r.buf == nil || r.buf.Bounds().Dx() != r.w || r.buf.Bounds().Dy() != r.h {
		r.buf = uv.NewBuffer(r.w, r.h)
	}
	r.m.Draw(r.buf, r.buf.Bounds())
	return r.buf
}

// rowText returns the text of the cells of row y from column x0 on.
func rowText(buf *uv.Buffer, y, x0 int) string {
	var b strings.Builder
	for x := x0; x < buf.Bounds().Dx(); x++ {
		if c := buf.CellAt(x, y); c != nil {
			b.WriteString(c.Content)
		}
	}
	return b.String()
}

// trow is one row of the patch as drawn: its text, and whether the margin
// holds the wrap mark.
type trow struct {
	text   string
	marked bool
}

// patchRows returns the rows the patch is drawn in.
func (r *rig) patchRows() []trow {
	buf := r.draw()
	var rows []trow
	for y := 1; y < r.h-1; y++ {
		rows = append(rows, trow{rowText(buf, y, marginCells), buf.CellAt(0, y).Content == wrapMark})
	}
	return rows
}

// first returns the text of the first row of the patch, without the blanks
// that fill the row.
func (r *rig) first() string { return strings.TrimRight(r.patchRows()[0].text, " ") }

// bottom returns the text of the last row of the patch on the screen.
func (r *rig) bottom() string {
	rows := r.patchRows()
	return strings.TrimRight(rows[len(rows)-1].text, " ")
}

func (r *rig) footer() string { return strings.TrimRight(rowText(r.draw(), r.h-1, 0), " ") }

// readAll scrolls from where the pager is to the end, a row at a time, and
// returns the lines it drew: the rows of a line joined, without the blanks
// that fill a line's last row. A patch read this way must fill the screen.
func (r *rig) readAll() []string {
	r.t.Helper()
	var rows []trow
	for {
		screen, before := r.patchRows(), r.m.top
		r.press(keyJ)
		if r.m.top == before {
			rows = append(rows, screen...)
			break
		}
		rows = append(rows, screen[0])
	}
	var lines []string
	for _, row := range rows {
		if row.marked {
			lines[len(lines)-1] += row.text
		} else {
			lines = append(lines, row.text)
		}
	}
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return lines
}
