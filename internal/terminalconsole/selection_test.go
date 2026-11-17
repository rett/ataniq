package terminalconsole

import (
	"encoding/base64"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// selRig returns a rig of w by h whose session pane shows text, written after
// the recording's own output is done. The recording's end waits an hour.
func selRig(t *testing.T, w, h int, o Options, text string) *rig {
	t.Helper()
	o.Linger = time.Hour
	r := newRig(t, w, h, o)
	r.play(600 * time.Millisecond)
	r.m.rep.pane.Write([]byte("\x1b[2J\x1b[H" + text))
	return r
}

const selText = "hello world\r\nsecond row\r\nthird row"

// win returns the window cell of the pane cell x, y.
func (r *rig) win(x, y int) (int, int) {
	l := r.m.layout()
	return l.inner.Min.X + x, l.inner.Min.Y + y
}

// mouse sends a mouse message and returns the message of the command it
// returns, or nil.
func (r *rig) mouse(msg tea.MouseMsg) tea.Msg {
	_, cmd := r.m.Update(msg)
	if cmd == nil {
		return nil
	}
	return cmd()
}

// drag presses the left button at window cell (x0, y0), moves to (x1, y1)
// and releases there, and returns the message of the release's command.
func (r *rig) drag(x0, y0, x1, y1 int) tea.Msg {
	r.mouse(tea.MouseClickMsg{X: x0, Y: y0, Button: tea.MouseLeft})
	r.mouse(tea.MouseMotionMsg{X: x1, Y: y1, Button: tea.MouseLeft})
	return r.mouse(tea.MouseReleaseMsg{X: x1, Y: y1, Button: tea.MouseLeft})
}

// dragPane drags from pane cell (x0, y0) to pane cell (x1, y1).
func (r *rig) dragPane(x0, y0, x1, y1 int) tea.Msg {
	wx0, wy0 := r.win(x0, y0)
	wx1, wy1 := r.win(x1, y1)
	return r.drag(wx0, wy0, wx1, wy1)
}

// osc52 is the sequence that copies text to the outer terminal's clipboard.
func osc52(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07"
}

// wantCopy fails the test unless msg writes exactly the OSC 52 copy of text.
func wantCopy(t *testing.T, msg tea.Msg, text string) {
	t.Helper()
	raw, ok := msg.(tea.RawMsg)
	if !ok {
		t.Fatalf("got %#v, want a copy of %q", msg, text)
	}
	if got := raw.Msg; got != osc52(text) {
		t.Fatalf("got %q, want %q", got, osc52(text))
	}
}

// drawnPane draws the session pane as the cockpit does and returns the
// buffer, in window cells.
func (r *rig) drawnPane() *uv.Buffer {
	l := r.m.layout()
	buf := uv.NewBuffer(l.canvas.Dx(), l.canvas.Dy())
	p := &painter{buf: buf, pal: paletteFor(r.m.theme)}
	r.m.drawPane(p, l.inner)
	return buf
}

// selected reports whether pane cell x, y is drawn in the selection colours.
func (r *rig) selected(buf *uv.Buffer, x, y int) bool {
	wx, wy := r.win(x, y)
	pal := paletteFor(r.m.theme)
	c := buf.CellAt(wx, wy)
	return c != nil && c.Style.Bg == pal.selBg && c.Style.Fg == pal.selFg
}

func TestSelectionCopiesAStreamOfRows(t *testing.T) {
	for _, theme := range []string{"dark", "light"} {
		r := selRig(t, 140, 45, Options{Theme: theme}, selText)
		wantCopy(t, r.dragPane(6, 0, 4, 2), "world\nsecond row\nthird")
		// The selected cells are drawn in the selection colours: from the
		// start cell to the end cell, whole rows between.
		buf := r.drawnPane()
		for _, c := range []struct {
			x, y int
			want bool
		}{
			{5, 0, false}, {6, 0, true}, {40, 0, true}, {0, 1, true}, {60, 1, true}, {0, 2, true}, {4, 2, true}, {5, 2, false}, {0, 3, false},
		} {
			if got := r.selected(buf, c.x, c.y); got != c.want {
				t.Errorf("%s: cell %d,%d selected %v, want %v", theme, c.x, c.y, got, c.want)
			}
		}
		// The selection stays after the release.
		if !r.m.sel.shown() {
			t.Fatalf("%s: the selection was cleared on release", theme)
		}
	}
	pal := [2]palette{paletteFor("dark"), paletteFor("light")}
	for _, p := range pal {
		if p.selBg == p.bg || p.selFg == p.selBg {
			t.Fatalf("selection colours do not stand out: %+v", p)
		}
	}
}

func TestSelectionBackwardsCopiesTheSameText(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, selText)
	wantCopy(t, r.dragPane(4, 2, 6, 0), "world\nsecond row\nthird")
}

// TestSelectionIsClampedToThePane drags from inside the pane to beyond each
// of its edges: the selection stops at the pane's first or last cell.
func TestSelectionIsClampedToThePane(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, selText)
	l := r.m.layout()
	cols, rows := r.paneSize()
	x, y := r.win(2, 1)
	// Up and left, over the tree and the header.
	wantCopy(t, r.drag(x, y, 0, 0), "hello world\nsec")
	// Down and right, over the evidence pane and the key bar.
	wantCopy(t, r.drag(x, y, l.canvas.Max.X-1, l.canvas.Max.Y-1), "cond row\nthird row"+strings.Repeat("\n", rows-3))
	if h := r.m.sel.head; h.X != cols-1 || h.Y != rows-1 {
		t.Fatalf("head %v, want %d,%d", h, cols-1, rows-1)
	}
	// Motion outside the window too, then a release inside the pane.
	r.mouse(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	r.mouse(tea.MouseMotionMsg{X: -10, Y: 500, Button: tea.MouseLeft})
	if h := r.m.sel.head; h.X != 0 || h.Y != rows-1 {
		t.Fatalf("head %v, want 0,%d", h, rows-1)
	}
	wx, wy := r.win(1, 0)
	wantCopy(t, r.mouse(tea.MouseReleaseMsg{X: wx, Y: wy, Button: tea.MouseLeft}), "ello world\nsec")
}

func TestSelectionCopiesWideCharactersAndClustersOnce(t *testing.T) {
	const family = "\U0001F468‍\U0001F469‍\U0001F467"
	r := selRig(t, 140, 45, Options{}, "界x"+family+"éy   \r\nnext")
	wantCopy(t, r.dragPane(0, 0, 3, 1), "界x"+family+"éy\nnext")
}

func TestSelectionTrimsTrailingBlanks(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, "a  b      \r\n   \r\n  c   ")
	cols, _ := r.paneSize()
	wantCopy(t, r.dragPane(0, 0, cols-1, 2), "a  b\n\n  c")
}

// TestBlankSelectionCopiesNothing checks that a selection of blanks, on one
// row or across rows, copies nothing, so the clipboard keeps what it held,
// and that it is still shown.
func TestBlankSelectionCopiesNothing(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, selText)
	if msg := r.dragPane(20, 0, 40, 0); msg != nil {
		t.Fatalf("blanks on one row copied %#v", msg)
	}
	if msg := r.dragPane(20, 4, 10, 7); msg != nil {
		t.Fatalf("blank rows copied %#v", msg)
	}
	if !r.m.sel.shown() {
		t.Fatal("a blank selection is not shown")
	}
	// Blanks before text on the next row are copied with it.
	wantCopy(t, r.dragPane(20, 1, 4, 2), "\nthird")
}

func TestClickWithoutDragCopiesNothingAndClears(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, selText)
	wantCopy(t, r.dragPane(0, 0, 4, 0), "hello")
	x, y := r.win(2, 1)
	r.mouse(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if r.m.sel.shown() {
		t.Fatal("a press kept the old selection")
	}
	if msg := r.mouse(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}); msg != nil {
		t.Fatalf("a click copied %#v", msg)
	}
	if r.m.sel.shown() || r.m.sel.drag {
		t.Fatalf("a click left a selection: %+v", r.m.sel)
	}
	buf := r.drawnPane()
	if r.selected(buf, 2, 1) || r.selected(buf, 0, 0) {
		t.Fatal("a click drew a selection")
	}
}

// TestSelectionStartsOnlyInThePane checks that a press outside the pane's
// inner area, with another button, on the wheel, or under help, selects
// nothing, and that none of it reaches the session.
func TestSelectionStartsOnlyInThePane(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, selText)
	l := r.m.layout()
	x, y := r.win(3, 1)
	for name, start := range map[string][2]int{
		"tree":     {2, 3},
		"border":   {l.inner.Min.X - 1, l.inner.Min.Y},
		"evidence": {l.evidence.Min.X + 2, l.evidence.Min.Y + 2},
		"timeline": {2, l.timeline.Min.Y + 1},
	} {
		if msg := r.drag(start[0], start[1], x, y); msg != nil || r.m.sel.shown() {
			t.Fatalf("%s: a drag from outside copied %#v, selection %+v", name, msg, r.m.sel)
		}
	}
	r.mouse(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight})
	r.mouse(tea.MouseMotionMsg{X: x + 5, Y: y, Button: tea.MouseRight})
	if msg := r.mouse(tea.MouseReleaseMsg{X: x + 5, Y: y, Button: tea.MouseRight}); msg != nil || r.m.sel.shown() {
		t.Fatalf("a right drag copied %#v", msg)
	}
	if msg := r.mouse(tea.MouseWheelMsg{X: x, Y: y, Button: tea.MouseWheelUp}); msg != nil || r.m.sel.shown() {
		t.Fatalf("the wheel did %#v", msg)
	}
	r.key(tea.KeyPressMsg{Code: '?', Text: "?"})
	if msg := r.drag(x, y, x+5, y); msg != nil || r.m.sel.shown() {
		t.Fatalf("a drag under help copied %#v", msg)
	}
	if len(r.sink.mouse) != 0 {
		t.Fatalf("%d mouse events reached the session", len(r.sink.mouse))
	}
}

// TestSelectionRefusesMoreThanTheCap fills the pane with clusters of
// combining marks until its text is over maxCopy: the release copies nothing
// and says why.
func TestSelectionRefusesMoreThanTheCap(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, "")
	cluster := r.fillOverCap()
	r.dragAll(true)
	if r.m.notice != copyRefused || !strings.Contains(r.view(), copyRefused) {
		t.Fatalf("notice %q", r.m.notice)
	}
	// A selection under the cap is still copied, and the notice goes.
	wantCopy(t, r.dragPane(0, 0, 1, 0), cluster+cluster)
	if r.m.notice != "" || strings.Contains(r.view(), copyRefused) {
		t.Fatalf("after a copy the notice is %q", r.m.notice)
	}
}

// fillOverCap fills the session pane with clusters of combining marks until
// its text is over maxCopy, and returns the cluster.
func (r *rig) fillOverCap() string {
	r.t.Helper()
	cols, rows := r.paneSize()
	cluster := "a" + strings.Repeat("́", 700)
	cells := maxCopy/len(cluster) + 1
	if cells > cols*rows {
		r.t.Fatalf("a %dx%d pane cannot hold %d clusters", cols, rows, cells)
	}
	var b strings.Builder
	for i := 0; i < cells; i++ {
		b.WriteString(cluster)
	}
	r.m.rep.pane.Write([]byte("\x1b[2J\x1b[H" + b.String()))
	return cluster
}

// dragAll selects the whole pane and fails the test unless that copied
// nothing, when refused is true, or copied something.
func (r *rig) dragAll(refused bool) {
	r.t.Helper()
	cols, rows := r.paneSize()
	msg := r.dragPane(0, 0, cols-1, rows-1)
	if refused && msg != nil {
		r.t.Fatalf("copied %d bytes", len(msg.(tea.RawMsg).Msg.(string)))
	}
	if !refused && msg == nil {
		r.t.Fatal("copied nothing")
	}
}

// TestZoomShowsTheRefusal checks that zoom, which has no key bar, shows the
// notice of a selection that was not copied in its header, and keeps the
// fixture label there, with the keys when the line has room for them.
func TestZoomShowsTheRefusal(t *testing.T) {
	header := func(r *rig) string { return r.rows()[0] }
	r := selRig(t, 140, 45, Options{}, "")
	r.focusSession()
	r.key(zoomPress)
	r.fillOverCap()
	r.dragAll(true)
	h := header(r)
	if !strings.HasPrefix(h, copyRefused) || !strings.Contains(h, "return Ctrl+\\") || !strings.HasSuffix(strings.TrimRight(h, " "), fixtureLabel) {
		t.Fatalf("session focus, 140 columns: header %q", h)
	}
	// A press removes the notice and brings back the usual header.
	r.mouse(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if h := header(r); !strings.HasPrefix(h, "SESSION FOCUS") {
		t.Fatalf("after a press: header %q", h)
	}
	// Too narrow for the keys too: the notice and the fixture label.
	r = selRig(t, 100, 30, Options{}, "")
	r.key(zoomPress)
	r.fillOverCap()
	r.dragAll(true)
	if h := header(r); !strings.HasPrefix(h, copyRefused+" · "+fixtureLabel) {
		t.Fatalf("console focus, 100 columns: header %q", h)
	}
	// A notice too long for the line is cut, and the label stays whole.
	r.key(zoomPress)
	r.key(tea.KeyPressMsg{Code: 'a', Text: "a"})
	r.key(zoomPress)
	if h := strings.TrimRight(header(r), " "); !strings.HasPrefix(h, "Unavailable: ") || !strings.HasSuffix(h, "… · "+fixtureLabel) {
		t.Fatalf("a long notice: header %q", h)
	}
}

// TestPressDropsTheRefusalOnly checks that a press removes the notice of a
// selection that was not copied, even when the session takes the mouse, and
// leaves any other notice.
func TestPressDropsTheRefusalOnly(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, selText)
	x, y := r.win(2, 1)
	r.m.notice = copyRefused
	r.mouse(tea.MouseClickMsg{X: 2, Y: 3, Button: tea.MouseLeft})
	if r.m.notice != "" {
		t.Fatalf("a press in the tree kept the notice %q", r.m.notice)
	}
	r.focusSession()
	r.m.rep.pane.Write([]byte("\x1b[?1000h"))
	r.m.notice = copyRefused
	r.mouse(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if r.m.notice != "" {
		t.Fatalf("a press sent to the session kept the notice %q", r.m.notice)
	}
	const other = "Paste not sent: it is longer than the console forwards."
	r.m.notice = other
	r.mouse(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if r.m.notice != other {
		t.Fatalf("a press removed another notice: %q", r.m.notice)
	}
}

// TestSessionMouseGoesToTheSessionAndSelectsNothing checks that a session
// that asked for the mouse, in session focus, gets presses, motion and
// releases in the pane as before, and that no selection is made.
func TestSessionMouseGoesToTheSessionAndSelectsNothing(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, selText)
	r.focusSession()
	r.m.rep.pane.Write([]byte("\x1b[?1002h"))
	if msg := r.dragPane(1, 0, 5, 1); msg != nil {
		t.Fatalf("copied %#v", msg)
	}
	if r.m.sel.shown() || r.m.sel.drag {
		t.Fatalf("selection %+v", r.m.sel)
	}
	if len(r.sink.mouse) != 3 {
		t.Fatalf("%d mouse events reached the session", len(r.sink.mouse))
	}
	if m := r.sink.mouse[2].Mouse(); m.X != 5 || m.Y != 1 {
		t.Fatalf("release at %+v", m)
	}
	// Once the session stops asking, the same drag selects.
	r.m.rep.pane.Write([]byte("\x1b[?1002l"))
	wantCopy(t, r.dragPane(1, 0, 5, 1), "ello world\nsecond")
	if len(r.sink.mouse) != 3 {
		t.Fatal("the selection reached the session")
	}
}

// TestSelectionInSessionFocusWithoutSessionMouse checks that in session
// focus a session that did not ask for the mouse leaves it to the console.
func TestSelectionInSessionFocusWithoutSessionMouse(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, selText)
	r.focusSession()
	wantCopy(t, r.dragPane(0, 1, 5, 1), "second")
	if len(r.sink.mouse) != 0 || len(r.sink.keys) != 0 {
		t.Fatalf("the session got %d mouse events and keys %v", len(r.sink.mouse), r.sink.keys)
	}
}

func TestSelectionInZoom(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, selText)
	r.key(zoomPress)
	if !r.m.layout().zoom {
		t.Fatal("not zoomed")
	}
	r.m.rep.pane.Write([]byte("\x1b[2J\x1b[H" + selText))
	wantCopy(t, r.dragPane(0, 2, 2, 2), "thi")
	if l := r.m.layout(); l.inner.Min.Y != 1 || l.inner.Min.X != 0 {
		t.Fatalf("zoomed inner %v", l.inner)
	}
}

// TestMouseModeInEveryState checks what the cockpit asks of the outer
// terminal: cell motion whenever the cockpit is shown, all motion only for a
// session that asked for it and has the focus, and nothing on the narrow
// pages.
func TestMouseModeInEveryState(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, selText)
	want := func(state string, mode tea.MouseMode) {
		t.Helper()
		if got := r.m.View().MouseMode; got != mode {
			t.Fatalf("%s: mouse mode %v, want %v", state, got, mode)
		}
	}
	want("overview, console focus", tea.MouseModeCellMotion)
	r.focusSession()
	want("session focus, no session mouse", tea.MouseModeCellMotion)
	r.m.rep.pane.Write([]byte("\x1b[?1000h"))
	want("session focus, session click tracking", tea.MouseModeCellMotion)
	r.m.rep.pane.Write([]byte("\x1b[?1003h"))
	want("session focus, session all motion", tea.MouseModeAllMotion)
	r.key(zoomPress)
	want("zoom, session focus, session all motion", tea.MouseModeAllMotion)
	r.key(decodeKey(t, "\x1c"))
	want("zoom, console focus, session all motion", tea.MouseModeCellMotion)
	r.key(zoomPress)
	want("overview, console focus, session all motion", tea.MouseModeCellMotion)
	r.m.rep.pane.Write([]byte("\x1b[?1003l\x1b[?1000l"))
	r.key(zoomPress)
	want("zoom, console focus", tea.MouseModeCellMotion)
	r.key(zoomPress)
	r.size(99, 30)
	want("narrow page 99x30", tea.MouseModeNone)
	r.size(100, 29)
	want("narrow page 100x29", tea.MouseModeNone)
	// On the narrow pages the mouse does nothing.
	if msg := r.drag(1, 1, 10, 5); msg != nil {
		t.Fatalf("a drag on the narrow page did %#v", msg)
	}
	r.size(100, 30)
	want("cockpit at 100x30", tea.MouseModeCellMotion)
}

// TestSelectionIsClearedWhenItNoLongerApplies checks each rule that clears a
// finished selection: a press anywhere, a key or a paste sent to the session,
// a window resize, zoom toggling, a replay, and output that changes the
// selected text. Output elsewhere on the screen keeps it.
func TestSelectionIsClearedWhenItNoLongerApplies(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, selText)
	sel := func() {
		t.Helper()
		wantCopy(t, r.dragPane(0, 0, 4, 1), "hello world\nsecon")
	}
	clears := func(what string, do func()) {
		t.Helper()
		sel()
		do()
		if r.m.sel.shown() {
			t.Fatalf("%s kept the selection", what)
		}
	}
	clears("a press in the tree", func() { r.mouse(tea.MouseClickMsg{X: 2, Y: 3, Button: tea.MouseLeft}) })
	clears("a window resize", func() { r.size(150, 45) })
	r.size(140, 45)
	r.m.rep.pane.Write([]byte("\x1b[2J\x1b[H" + selText))
	clears("zoom", func() { r.key(zoomPress) })
	r.key(zoomPress)
	r.m.rep.pane.Write([]byte("\x1b[2J\x1b[H" + selText))
	r.focusSession()
	clears("a key sent to the session", func() { r.key(tea.KeyPressMsg{Code: 'x', Text: "x"}) })
	clears("a paste sent to the session", func() { r.m.Update(tea.PasteMsg{Content: "p"}) })
	r.key(decodeKey(t, "\x1c"))

	// Output below the selection keeps it; output over it clears it at the
	// next tick.
	sel()
	r.m.rep.pane.Write([]byte("\x1b[5;1Hspinner"))
	r.tick(time.Second)
	if !r.m.sel.shown() {
		t.Fatal("output outside the selection cleared it")
	}
	r.m.rep.pane.Write([]byte("\x1b[1;1Hjello"))
	r.tick(2 * time.Second)
	if r.m.sel.shown() {
		t.Fatal("output over the selection kept it")
	}
	// A scroll moves the text under the selection.
	r.m.rep.pane.Write([]byte("\x1b[2J\x1b[H" + selText))
	sel()
	_, rows := r.paneSize()
	r.m.rep.pane.Write([]byte("\x1b[" + strconv.Itoa(rows) + ";1H\n"))
	r.tick(3 * time.Second)
	if r.m.sel.shown() {
		t.Fatal("a scroll kept the selection")
	}
	r.m.rep.pane.Write([]byte("\x1b[2J\x1b[H" + selText))
	clears("a replay", func() { r.key(tea.KeyPressMsg{Code: 'r', Text: "r"}) })
}

// TestSelectionIsClearedWhenThePaneFollowsTheWindow makes a selection after
// the window size changed but before the pane follows it: the debounced
// resize clears it. A debounced resize that leaves the pane's size as it was
// keeps it.
func TestSelectionIsClearedWhenThePaneFollowsTheWindow(t *testing.T) {
	r := selRig(t, 140, 45, Options{}, selText)
	cols, _ := r.paneSize()
	r.size(120, 45)
	if c, _ := r.paneSize(); c != cols {
		t.Fatal("the pane followed the window at once")
	}
	wantCopy(t, r.dragPane(0, 0, 4, 1), "hello world\nsecon")
	r.m.Update(resizeMsg{seq: r.m.resizeSeq})
	if c, _ := r.paneSize(); c == cols {
		t.Fatal("the debounced resize left the pane as it was")
	}
	if r.m.sel.shown() {
		t.Fatal("the pane's resize kept the selection")
	}
	r.m.rep.pane.Write([]byte("\x1b[2J\x1b[H" + selText))
	wantCopy(t, r.dragPane(0, 0, 4, 1), "hello world\nsecon")
	r.m.Update(resizeMsg{seq: r.m.resizeSeq})
	if !r.m.sel.shown() {
		t.Fatal("a resize to the pane's own size cleared the selection")
	}
}
