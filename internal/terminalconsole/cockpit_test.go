package terminalconsole

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/terminalconsole/sessionpane"
	"ataniq/internal/terminalconsole/sessionreplay"
	"ataniq/internal/terminalconsole/sessionreplay/fixturegen"
)

// recSink records what the console sends to the session.
type recSink struct {
	mu     sync.Mutex
	keys   []string
	pastes []string
	mouse  []uv.MouseEvent
}

func (s *recSink) Key(k uv.KeyPressEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = append(s.keys, tea.KeyPressMsg(k).String())
}
func (s *recSink) Paste(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pastes = append(s.pastes, text)
}
func (s *recSink) Mouse(m uv.MouseEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mouse = append(s.mouse, m)
}

type scheduled struct {
	d  time.Duration
	fn func(time.Time) tea.Msg
}

// rig drives a model with a clock of its own: it replaces tea.Tick, records
// what the model schedules and sends the ticks itself.
type rig struct {
	t     *testing.T
	m     *Model
	sink  *recSink
	sched []scheduled
	t0    time.Time
}

func newRig(t *testing.T, w, h int, o Options) *rig {
	t.Helper()
	if o.Theme == "" {
		o.Theme = "dark"
	}
	r := &rig{t: t, sink: &recSink{}, t0: time.Unix(1_000_000, 0)}
	o.Input = r.sink
	r.m = NewWithOptions(Fixture{}, o)
	t.Cleanup(r.m.Close)
	r.m.after = func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		r.sched = append(r.sched, scheduled{d, fn})
		return nil
	}
	if w > 0 {
		r.size(w, h)
	}
	return r
}

func (r *rig) size(w, h int) tea.Cmd {
	_, cmd := r.m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return cmd
}

// play sends ticks every 40 ms from the start up to d.
func (r *rig) play(d time.Duration) {
	for at := time.Duration(0); at <= d; at += 40 * time.Millisecond {
		r.tick(at)
	}
}

func (r *rig) tick(at time.Duration) {
	r.m.Update(tickMsg{at: r.t0.Add(at), gen: r.m.rep.gen})
}

func (r *rig) key(k tea.KeyPressMsg) tea.Cmd {
	_, cmd := r.m.Update(k)
	return cmd
}

func (r *rig) typ(s string) {
	for _, c := range s {
		r.key(tea.KeyPressMsg{Code: c, Text: string(c)})
	}
}

func (r *rig) view() string { return ansi.Strip(r.m.View().Content) }

func (r *rig) rows() []string { return strings.Split(r.view(), "\n") }

func (r *rig) paneSize() (int, int) { return r.m.rep.pane.Size() }

func decodeKey(t *testing.T, bytes string) tea.KeyPressMsg {
	t.Helper()
	var d uv.EventDecoder
	n, ev := d.Decode([]byte(bytes))
	k, ok := ev.(uv.KeyPressEvent)
	if !ok || n != len(bytes) {
		t.Fatalf("%q decoded to %d bytes of %T", bytes, n, ev)
	}
	return tea.KeyPressMsg(k)
}

var (
	enterKey  = tea.KeyPressMsg{Code: tea.KeyEnter}
	tabKey    = tea.KeyPressMsg{Code: tea.KeyTab}
	zoomPress = tea.KeyPressMsg{Code: tea.KeyF12}
	ctrlC     = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	ctrlK     = tea.KeyPressMsg{Code: 'k', Mod: tea.ModCtrl}
	escKey    = tea.KeyPressMsg{Code: tea.KeyEscape}
)

// focusSession puts the console in session focus.
func (r *rig) focusSession() {
	r.t.Helper()
	for r.m.pane != paneSession {
		r.key(tabKey)
	}
	r.key(enterKey)
	if r.m.focus != sessionFocus {
		r.t.Fatal("session did not take the focus")
	}
}

func TestThreePanesAt140x45(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	r.play(200 * time.Millisecond)
	rows := r.rows()
	if len(rows) != 45 {
		t.Fatalf("%d rows", len(rows))
	}
	for i, row := range rows {
		if w := ansi.StringWidth(row); w > 140 {
			t.Fatalf("row %d is %d wide", i, w)
		}
	}
	// The header row, then the titles of the three panes side by side.
	if !strings.Contains(rows[0], "ATANIQ") || !strings.Contains(rows[0], fixtureLabel) {
		t.Fatal(rows[0])
	}
	titles := rows[1]
	a, b, c := strings.Index(titles, "Projects & leads"), strings.Index(titles, "Session"), strings.Index(titles, "Evidence & approvals")
	if a < 0 || b < 0 || c < 0 || !(a < b && b < c) {
		t.Fatalf("panes are not side by side: %q", titles)
	}
	all := r.view()
	for _, want := range []string{"Context", "SESSION TERMINAL", "Timeline", "ATTACHMENT CONTRACT", "PENDING GATE", "LEAD & DELEGATE",
		"CONSOLE FOCUS", "return key Ctrl+\\", "Attachment: Unqualified", "title test"} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %q:\n%s", want, all)
		}
	}
	// The timeline is below the three panes, and the key bar last.
	if strings.Index(all, "Timeline") < strings.Index(all, "Evidence & approvals") || !strings.Contains(rows[44], "q quit") {
		t.Fatal(all)
	}
	l := r.m.layout()
	if !l.cockpit || l.inner.Dx() != 140-l.tree.Dx()-l.evidence.Dx()-2 {
		t.Fatalf("%+v", l)
	}
	// The pane follows the layout.
	if cols, rows := r.paneSize(); cols != l.inner.Dx() || rows != l.inner.Dy() {
		t.Fatalf("pane %dx%d, area %v", cols, rows, l.inner)
	}
}

// TestLayoutTiles checks, over many sizes, that the blocks do not overlap and
// fit the window, and that the fixture label is always on screen.
func TestLayoutTiles(t *testing.T) {
	for _, sz := range [][2]int{{100, 30}, {101, 31}, {120, 40}, {140, 45}, {200, 60}, {300, 100}, {1000, 500}} {
		r := newRig(t, sz[0], sz[1], Options{})
		l := r.m.layout()
		blocks := []uv.Rectangle{l.header, l.keys, l.tree, l.context, l.meta, l.frame, l.focus, l.evidence, l.timeline}
		for i, a := range blocks {
			if a.Empty() || !a.In(l.canvas) {
				t.Fatalf("%v: block %d is %v", sz, i, a)
			}
			for _, b := range blocks[i+1:] {
				if !a.Intersect(b).Empty() {
					t.Fatalf("%v: %v overlaps %v", sz, a, b)
				}
			}
		}
		if !l.inner.In(l.frame) || l.inner.Empty() {
			t.Fatalf("%v: %v", sz, l.inner)
		}
		rows := r.rows()
		if len(rows) != l.canvas.Dy() || !strings.Contains(rows[0], fixtureLabel) || !strings.Contains(rows[l.frame.Max.Y-1], fixtureLabel) {
			t.Fatalf("%v: label missing:\n%s", sz, r.view())
		}
		if cols, rows := r.paneSize(); cols > sessionpane.MaxCols || rows > sessionpane.MaxRows {
			t.Fatalf("%v: pane %dx%d", sz, cols, rows)
		}
	}
}

func TestFallbackBelowTheMinimum(t *testing.T) {
	for _, sz := range [][2]int{{99, 30}, {100, 29}, {80, 24}, {99, 99}, {40, 3}, {4, 1}} {
		r := newRig(t, sz[0], sz[1], Options{})
		if r.m.layout().cockpit {
			t.Fatalf("%v is a cockpit", sz)
		}
		v := r.view()
		if strings.Contains(v, "Evidence & approvals") || !strings.Contains(v, "ATANIQ"[:min(sz[0], 6)]) {
			t.Fatalf("%v:\n%s", sz, v)
		}
	}
	r := newRig(t, 100, 30, Options{})
	if !r.m.layout().cockpit || !strings.Contains(r.view(), "Evidence & approvals") {
		t.Fatal(r.view())
	}
	// The single-column pages still work below the minimum.
	r = newRig(t, 80, 24, Options{})
	r.key(enterKey)
	r.key(enterKey)
	if !strings.Contains(r.view(), "Project: Northstar (fixture) / Lead: Planning lead") {
		t.Fatal(r.view())
	}
}

func TestReturnKeyInBothEncodings(t *testing.T) {
	for _, enc := range []struct{ name, bytes string }{{"legacy", "\x1c"}, {"kitty", "\x1b[92;5u"}, {"kitty with event type", "\x1b[92;5:1u"}} {
		r := newRig(t, 140, 45, Options{})
		r.focusSession()
		r.key(decodeKey(t, enc.bytes))
		if r.m.focus != consoleFocus {
			t.Fatalf("%s: did not return", enc.name)
		}
		if len(r.sink.keys) != 0 {
			t.Fatalf("%s: the return key reached the session: %v", enc.name, r.sink.keys)
		}
	}
}

func TestCtrl4InKittyFormDoesNotReturn(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	r.focusSession()
	r.key(decodeKey(t, "\x1b[52;5u"))
	if r.m.focus != sessionFocus || len(r.sink.keys) != 1 || r.sink.keys[0] != "ctrl+4" {
		t.Fatalf("focus %v keys %v", r.m.focus, r.sink.keys)
	}
}

func TestReturnKeyIsConfigurable(t *testing.T) {
	k, err := ParseReturnKey("ctrl+g")
	if err != nil {
		t.Fatal(err)
	}
	r := newRig(t, 140, 45, Options{ReturnKey: k})
	r.focusSession()
	r.key(decodeKey(t, "\x1c"))
	if r.m.focus != sessionFocus {
		t.Fatal("the old return key still returns")
	}
	r.key(decodeKey(t, "\x07"))
	if r.m.focus != consoleFocus {
		t.Fatal("the configured key did not return")
	}
	if !strings.Contains(r.view(), "return key Ctrl+G") {
		t.Fatal(r.view())
	}
}

func TestParseReturnKey(t *testing.T) {
	for _, ok := range []string{"ctrl+\\", "ctrl+]", "ctrl+g", "CTRL+G", "ctrl+a", "ctrl+y"} {
		if _, err := ParseReturnKey(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "ctrl+", "ctrl+enter", "ctrl+return", "enter", "f12", "ctrl+c", "ctrl+z", "ctrl+i", "ctrl+m", "ctrl+k", "ctrl+4", "ctrl+gg", "alt+g", "g", "ctrl+\x1b", "ctrl+["} {
		_, err := ParseReturnKey(bad)
		if err == nil {
			t.Errorf("%q accepted", bad)
		} else if err != ErrReturnKey {
			t.Errorf("%q: the error is not the fixed one: %v", bad, err)
		}
	}
	if DefaultReturnKey.String() != `Ctrl+\` || DefaultReturnKey.Spec() != `ctrl+\` {
		t.Fatal(DefaultReturnKey.String(), DefaultReturnKey.Spec())
	}
}

func TestCtrlEnterIsNeverBound(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	ce := tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}
	r.focusSession()
	r.key(ce)
	if r.m.focus != sessionFocus || len(r.sink.keys) != 1 || r.sink.keys[0] != "ctrl+enter" {
		t.Fatalf("session focus: focus %v keys %v", r.m.focus, r.sink.keys)
	}
	r.key(decodeKey(t, "\x1c"))
	before := r.view()
	r.key(ce)
	if r.m.focus != consoleFocus || r.view() != before || r.m.zoom {
		t.Fatal("ctrl+enter did something in console focus")
	}
}

func TestQAndCtrlCDoNotQuitInSessionFocus(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	r.focusSession()
	for _, k := range []tea.KeyPressMsg{{Code: 'q', Text: "q"}, ctrlC, {Code: 'Q', Text: "Q"}, ctrlK, escKey, tabKey, {Code: '?', Text: "?"}} {
		if cmd := r.key(k); cmd != nil {
			t.Fatalf("%v returned a command", k)
		}
	}
	if r.m.focus != sessionFocus || r.m.overlay != "" || r.m.pane != paneSession {
		t.Fatal("the console took a key")
	}
	if got := strings.Join(r.sink.keys, " "); got != "q ctrl+c Q ctrl+k esc tab ?" {
		t.Fatalf("session got %q", got)
	}
	// In console focus the same keys quit.
	r.key(decodeKey(t, "\x1c"))
	if cmd := r.key(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd == nil {
		t.Fatal("q does not quit in console focus")
	} else if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("not a quit")
	}
	r = newRig(t, 140, 45, Options{})
	if cmd := r.key(ctrlC); cmd == nil {
		t.Fatal("ctrl+c does not quit in console focus")
	}
}

func TestZoomResizesThePane(t *testing.T) {
	for _, f := range []focusState{consoleFocus, sessionFocus} {
		r := newRig(t, 140, 45, Options{})
		r.play(100 * time.Millisecond)
		if f == sessionFocus {
			r.focusSession()
		}
		cols, rows := r.paneSize()
		r.key(zoomPress)
		zc, zr := r.paneSize()
		if zc != 140 || zr != 44 || zc <= cols || zr <= rows {
			t.Fatalf("focus %v: pane %dx%d, zoomed %dx%d", f, cols, rows, zc, zr)
		}
		if r.m.focus != f {
			t.Fatal("zoom changed the focus")
		}
		v := r.view()
		if strings.Contains(v, "Evidence & approvals") || !strings.Contains(v, fixtureLabel) || !strings.Contains(v, "title test") ||
			!strings.Contains(strings.Split(v, "\n")[0], f.name()) {
			t.Fatalf("zoomed view:\n%s", v)
		}
		if f == sessionFocus && !strings.Contains(v, "return Ctrl+\\") {
			t.Fatal("the zoomed bar does not show the return key")
		}
		// Zoom keeps going after the session's replay and a new window size.
		r.play(300 * time.Millisecond)
		r.key(zoomPress)
		if c, rr := r.paneSize(); c != cols || rr != rows {
			t.Fatalf("unzoomed pane %dx%d, want %dx%d", c, rr, cols, rows)
		}
	}
	// The pane never exceeds its limit, even zoomed in a window that allows more.
	r := newRig(t, 2000, 900, Options{})
	r.key(zoomPress)
	if c, rr := r.paneSize(); c != sessionpane.MaxCols || rr != sessionpane.MaxRows {
		t.Fatalf("%dx%d", c, rr)
	}
}

func (f focusState) name() string {
	if f == sessionFocus {
		return "SESSION FOCUS"
	}
	return "CONSOLE FOCUS"
}

func TestWindowResizeIsDebounced(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	c0, r0 := r.paneSize()
	r.sched = nil
	r.size(150, 50)
	r.size(160, 55)
	if c, rr := r.paneSize(); c != c0 || rr != r0 {
		t.Fatalf("the pane resized at once: %dx%d", c, rr)
	}
	if len(r.sched) != 2 || r.sched[0].d != resizeDelay {
		t.Fatalf("%v", r.sched)
	}
	// The first message is stale, the last one applies.
	r.m.Update(r.sched[0].fn(r.t0))
	if c, _ := r.paneSize(); c != c0 {
		t.Fatal("a stale resize applied")
	}
	r.m.Update(r.sched[1].fn(r.t0))
	l := r.m.layout()
	if c, rr := r.paneSize(); c != l.inner.Dx() || rr != l.inner.Dy() || c <= c0 {
		t.Fatalf("%dx%d, area %v", c, rr, l.inner)
	}
}

func TestEndedBanner(t *testing.T) {
	for _, tc := range []struct {
		recording string
		want      string
	}{{"exit-zero.jsonl", "Exit status: 0."}, {"exit-nonzero.jsonl", "Exit status: 3."}, {"exit-unknown.jsonl", "Exit status: unknown."}} {
		r := newRig(t, 140, 45, Options{Recording: tc.recording})
		r.play(200 * time.Millisecond)
		if strings.Contains(r.view(), "TERMINAL STREAM ENDED") {
			t.Fatalf("%s: ended early", tc.recording)
		}
		r.play(600 * time.Millisecond)
		v := r.view()
		for _, want := range []string{"TERMINAL STREAM ENDED", tc.want, "not necessarily the agent", "daemon keeps running", "r replays the recording", fixtureLabel} {
			if !strings.Contains(v, want) {
				t.Fatalf("%s: missing %q:\n%s", tc.recording, want, v)
			}
		}
		// The last screen stays under the banner, dimmed.
		if !strings.Contains(v, "session") && !strings.Contains(v, "finished") && !strings.Contains(v, "failure") && !strings.Contains(v, "unknown") {
			t.Fatalf("%s: the last screen is gone:\n%s", tc.recording, v)
		}
		if !cellsFaint(r.m.View().Content) {
			t.Fatalf("%s: the last screen is not dimmed", tc.recording)
		}
	}
}

// cellsFaint reports whether the styled content has any faint text.
func cellsFaint(content string) bool {
	return strings.Contains(content, "\x1b[2m") || strings.Contains(content, ";2m") || strings.Contains(content, ";2;")
}

func TestEndedSessionAnyKeyReturnsToConsole(t *testing.T) {
	r := newRig(t, 140, 45, Options{Recording: "exit-zero.jsonl"})
	r.focusSession()
	r.play(900 * time.Millisecond)
	if !strings.Contains(r.view(), "Press any key to return to the console") {
		t.Fatal(r.view())
	}
	if cmd := r.key(tea.KeyPressMsg{Code: 'x', Text: "x"}); cmd != nil || r.m.focus != consoleFocus {
		t.Fatalf("focus %v", r.m.focus)
	}
	if len(r.sink.keys) != 0 {
		t.Fatalf("keys reached an ended session: %v", r.sink.keys)
	}
	// Even q and Ctrl+C only return.
	r.focusSession()
	if cmd := r.key(ctrlC); cmd != nil || r.m.focus != consoleFocus {
		t.Fatal("ctrl+c did not just return")
	}
	// A pane that has ended can be focused, and zoomed.
	r.focusSession()
	r.key(zoomPress)
	if r.m.focus != sessionFocus || !strings.Contains(r.view(), "TERMINAL STREAM ENDED") {
		t.Fatal(r.view())
	}
}

// TestZoomOfAnEndedPaneKeepsItsSize records the pane's limit: it ignores Resize
// after the end, so the old screen is shown, cut or padded, under the banner.
func TestZoomOfAnEndedPaneKeepsItsSize(t *testing.T) {
	r := newRig(t, 140, 45, Options{Recording: "exit-zero.jsonl"})
	r.play(900 * time.Millisecond)
	c, rr := r.paneSize()
	r.key(zoomPress)
	if zc, zr := r.paneSize(); zc != c || zr != rr {
		t.Fatalf("the ended pane resized to %dx%d", zc, zr)
	}
	v := r.view()
	if !strings.Contains(v, "TERMINAL STREAM ENDED") || !strings.Contains(v, fixtureLabel) {
		t.Fatal(v)
	}
	r.size(110, 32)
	r.key(zoomPress)
	if !strings.Contains(r.view(), "TERMINAL STREAM ENDED") {
		t.Fatal(r.view())
	}
}

func TestRestartReplaysTheRecording(t *testing.T) {
	r := newRig(t, 140, 45, Options{Recording: "exit-zero.jsonl"})
	r.play(900 * time.Millisecond)
	old := r.m.rep.gen
	r.key(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if r.m.rep.gen == old || strings.Contains(r.view(), "TERMINAL STREAM ENDED") {
		t.Fatal("not restarted")
	}
	// A tick of the old replay is ignored.
	r.m.Update(tickMsg{at: r.t0.Add(5 * time.Second), gen: old})
	if strings.Contains(r.view(), "TERMINAL STREAM ENDED") {
		t.Fatal("a stale tick advanced the replay")
	}
	r.t0 = r.t0.Add(time.Hour)
	r.play(900 * time.Millisecond)
	if !strings.Contains(r.view(), "TERMINAL STREAM ENDED") {
		t.Fatal(r.view())
	}
}

func TestLingerKeepsTheSessionOpen(t *testing.T) {
	r := newRig(t, 140, 45, Options{Recording: "exit-zero.jsonl", Linger: 5 * time.Second})
	r.play(4 * time.Second)
	if strings.Contains(r.view(), "TERMINAL STREAM ENDED") {
		t.Fatal("ended before the linger time")
	}
	r.play(6 * time.Second)
	if !strings.Contains(r.view(), "TERMINAL STREAM ENDED") {
		t.Fatal("did not end after the linger time")
	}
}

func TestEveryControlShowsItsReason(t *testing.T) {
	controls := (Fixture{}).Snapshot().Controls
	if len(controls) < 5 {
		t.Fatal("the fixture lists too few controls")
	}
	r := newRig(t, 140, 45, Options{})
	r.key(ctrlK)
	if r.m.overlay != "actions" {
		t.Fatal("no action list")
	}
	v := r.view()
	_, ls := r.m.overlayLines(r.m.overlayBox(r.m.layout()).Dx() - 4)
	var flat []string
	for _, l := range ls {
		flat = append(flat, l.s)
	}
	list := wordsOf(strings.Join(flat, " "))
	for _, c := range controls {
		// Each control is on screen, and its whole reason is in the list.
		if !strings.Contains(v, c.Name) || !strings.Contains(list, "Unavailable: "+wordsOf(c.Reason)) {
			t.Fatalf("control %q or its reason is missing:\n%s", c.Name, v)
		}
	}
	for _, name := range []string{"Approve", "Deny", "Full attach", "Send", "Interrupt"} {
		if !strings.Contains(v, name) {
			t.Fatalf("no %s control", name)
		}
	}
	r.key(escKey)
	if r.m.overlay != "" {
		t.Fatal("Esc did not close the list")
	}
	// The a key says why full attach is unavailable, and the reason is the list's.
	r.key(tea.KeyPressMsg{Code: 'a', Text: "a"})
	full := ""
	for _, c := range controls {
		if c.Key == "a" {
			full = c.Reason
		}
	}
	if full == "" || !strings.Contains(r.m.notice, full) || !strings.Contains(r.view(), "Unavailable: Full attach") {
		t.Fatalf("notice %q\n%s", r.m.notice, r.view())
	}
	if len(r.sink.keys) != 0 {
		t.Fatal("a control key reached the session")
	}
}

// wordsOf collapses white space the way the test collapses the screen.
func wordsOf(s string) string { return strings.Join(strings.Fields(s), " ") }

func TestActionListInTheSingleColumnPages(t *testing.T) {
	r := newRig(t, 99, 80, Options{})
	r.key(ctrlK)
	if r.m.page != "actions" {
		t.Fatal(r.m.page)
	}
	v := r.view()
	for _, c := range (Fixture{}).Snapshot().Controls {
		if !strings.Contains(v, c.Name+": "+c.Reason[:20]) {
			t.Fatalf("%q missing:\n%s", c.Name, v)
		}
	}
	r.key(escKey)
	if r.m.page != "projects" {
		t.Fatal(r.m.page)
	}
}

func TestPaneFocusAndMovement(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	if r.m.pane != paneTree {
		t.Fatal("tree is not first")
	}
	var seen []int
	for i := 0; i < paneCount; i++ {
		r.key(tabKey)
		seen = append(seen, r.m.pane)
	}
	if fmt.Sprint(seen) != fmt.Sprint([]int{paneSession, paneEvidence, paneTimeline, paneTree}) {
		t.Fatal(seen)
	}
	r.key(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if r.m.pane != paneTimeline {
		t.Fatal(r.m.pane)
	}
	// The tree moves over selectable rows and Enter selects.
	r = newRig(t, 140, 45, Options{})
	r.typ("jj")
	rows := r.m.treeRows()
	if !rows[r.m.treeCursor].selectable || rows[r.m.treeCursor].text != "Delivery lead" {
		t.Fatalf("cursor on %+v", rows[r.m.treeCursor])
	}
	r.key(enterKey)
	if r.m.lead != 1 || !strings.Contains(r.view(), "Lead: Delivery lead") || !strings.Contains(r.view(), "fixture-session-2") {
		t.Fatal(r.view())
	}
	r.typ("jj")
	r.key(enterKey)
	if r.m.project != 1 || !strings.Contains(r.view(), "Project: Harbor (fixture)") {
		t.Fatal(r.view())
	}
	// Scrolling the timeline goes back in time and stops at the first entry.
	r.key(tabKey)
	r.key(tabKey)
	r.key(tabKey)
	for i := 0; i < 20; i++ {
		r.typ("k")
	}
	if r.m.scrolls[paneTimeline] != len(r.m.timeline)-1 {
		t.Fatal(r.m.scrolls)
	}
	r.typ("j")
	if r.m.scrolls[paneTimeline] != len(r.m.timeline)-2 {
		t.Fatal(r.m.scrolls)
	}
}

func TestHelpOverlay(t *testing.T) {
	r := newRig(t, 100, 30, Options{})
	r.typ("?")
	v := r.view()
	for _, want := range []string{"HELP", "Ctrl+K", "Ctrl+4", "F12", "q and Ctrl+C", "Session focus"} {
		if want == "q and Ctrl+C" {
			want = "q and Ctrl+C included"
		}
		if !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(v, "┃", " ")), " "), want) {
			t.Fatalf("help lacks %q:\n%s", want, v)
		}
	}
	r.key(escKey)
	if r.m.overlay != "" {
		t.Fatal("help stays")
	}
}

func TestPasteGoesToTheSessionOnlyInSessionFocusWithoutTheEndMarker(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	r.m.Update(tea.PasteMsg{Content: "ignored"})
	if len(r.sink.pastes) != 0 {
		t.Fatal("paste reached the session from console focus")
	}
	r.focusSession()
	r.m.Update(tea.PasteMsg{Content: "plain"})
	r.m.Update(tea.PasteMsg{Content: "a\x1b[201~b\x1b[2\x1b[201~01~c\x1b[201\x1b[201~~"})
	want := []string{"plain", "abc\x1b[201~"[:3]}
	if len(r.sink.pastes) != 2 || r.sink.pastes[0] != want[0] || strings.Contains(r.sink.pastes[1], "\x1b[201~") || r.sink.pastes[1] != "abc" {
		t.Fatalf("%q", r.sink.pastes)
	}
	r.m.Update(tea.PasteMsg{Content: strings.Repeat("x", maxPaste+1)})
	if len(r.sink.pastes) != 2 || !strings.Contains(r.m.notice, "Paste not sent") {
		t.Fatal("a huge paste was forwarded")
	}
}

func TestStripPasteEnd(t *testing.T) {
	for in, want := range map[string]string{
		"":                            "",
		"plain":                       "plain",
		"\x1b[201~":                   "",
		"\x1b[200~keep\x1b[201~":      "\x1b[200~keep",
		"\x1b[2\x1b[201~01~":          "",
		"\x1b[\x1b[2\x1b[201~01~201~": "",
	} {
		if got := stripPasteEnd(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestMouseFollowsTheSessionAndTheFocus(t *testing.T) {
	r := newRig(t, 140, 45, Options{Recording: "modes.jsonl", Linger: time.Hour})
	r.play(500 * time.Millisecond) // every mode is on
	if m := r.m.rep.pane.Modes(); m.Mouse != uv.MouseModeMotion || !m.BracketedPaste || !m.FocusEvents {
		t.Fatalf("%+v", m)
	}
	// Nothing is requested of the outer terminal in console focus.
	if v := r.m.View(); v.MouseMode != tea.MouseModeNone || v.ReportFocus {
		t.Fatalf("console focus asks for %v %v", v.MouseMode, v.ReportFocus)
	}
	r.focusSession()
	if v := r.m.View(); v.MouseMode != tea.MouseModeAllMotion || !v.ReportFocus {
		t.Fatalf("session focus asks for %v %v", v.MouseMode, v.ReportFocus)
	}
	l := r.m.layout()
	// A click inside the pane arrives in pane cells; one outside is dropped.
	r.m.Update(tea.MouseClickMsg{X: l.inner.Min.X + 3, Y: l.inner.Min.Y + 2, Button: tea.MouseLeft})
	r.m.Update(tea.MouseClickMsg{X: 1, Y: 1, Button: tea.MouseLeft})
	r.m.Update(tea.MouseClickMsg{X: l.inner.Max.X, Y: l.inner.Min.Y, Button: tea.MouseLeft})
	if len(r.sink.mouse) != 1 {
		t.Fatalf("%d mouse events", len(r.sink.mouse))
	}
	if m := r.sink.mouse[0].Mouse(); m.X != 3 || m.Y != 2 {
		t.Fatalf("%+v", m)
	}
	// Back in console focus nothing reaches the session.
	r.key(decodeKey(t, "\x1c"))
	r.m.Update(tea.MouseClickMsg{X: l.inner.Min.X + 3, Y: l.inner.Min.Y + 2, Button: tea.MouseLeft})
	if len(r.sink.mouse) != 1 {
		t.Fatal("mouse reached the session from console focus")
	}
	// The session turns the modes off again: the outer terminal is no longer asked.
	r.focusSession()
	r.play(900 * time.Millisecond)
	if v := r.m.View(); v.MouseMode != tea.MouseModeNone || v.ReportFocus {
		t.Fatalf("modes off, but asks for %v %v", v.MouseMode, v.ReportFocus)
	}
}

func TestMouseModeNeedsTheSessionToAsk(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	r.play(100 * time.Millisecond)
	r.focusSession()
	if v := r.m.View(); v.MouseMode != tea.MouseModeNone {
		t.Fatalf("mouse %v", v.MouseMode)
	}
	l := r.m.layout()
	r.m.Update(tea.MouseClickMsg{X: l.inner.Min.X, Y: l.inner.Min.Y, Button: tea.MouseLeft})
	r.m.Update(tea.MouseWheelMsg{X: l.inner.Min.X, Y: l.inner.Min.Y, Button: tea.MouseWheelUp})
	if len(r.sink.mouse) != 0 {
		t.Fatal("mouse reached a session that did not ask for it")
	}
}

func TestSessionCursor(t *testing.T) {
	r := newRig(t, 140, 45, Options{Linger: time.Hour})
	r.play(100 * time.Millisecond)
	if r.m.View().Cursor != nil {
		t.Fatal("a cursor in console focus")
	}
	r.focusSession()
	c := r.m.View().Cursor
	l := r.m.layout()
	if c == nil || c.X < l.inner.Min.X || c.X >= l.inner.Max.X || c.Y < l.inner.Min.Y || c.Y >= l.inner.Max.Y {
		t.Fatalf("%+v not in %v", c, l.inner)
	}
}

func TestSessionOutputNeverReachesTheOuterTerminalAsASequence(t *testing.T) {
	for _, name := range Recordings() {
		r := newRig(t, 140, 45, Options{Recording: name, Linger: time.Hour})
		r.play(1200 * time.Millisecond)
		c := r.m.View().Content
		// Cells and the cockpit's own colours only: nothing but styles.
		if bad := foreignSequence(c); bad != "" {
			t.Fatalf("%s: content has %q", name, bad)
		}
	}
	// The title with the mark in it is shown in the metadata block, as text.
	r := newRig(t, 140, 45, Options{Recording: "title.jsonl", Linger: time.Hour})
	r.play(1000 * time.Millisecond)
	if title := r.m.rep.pane.Title(); title == "" || !strings.Contains(r.view(), "Title: ") {
		t.Fatalf("title %q\n%s", title, r.view())
	}
	if n := newRig(t, 140, 45, Options{Recording: "clipboard.jsonl"}); true {
		n.play(500 * time.Millisecond)
		if !strings.Contains(n.view(), "Clipboard requests blocked: 1") {
			t.Fatal(n.view())
		}
	}
}

func TestHeldFrameIsRedrawnAtItsDeadline(t *testing.T) {
	var held string
	for _, k := range fixturegen.KnownText() {
		if strings.Contains(k, "held frame, line a") {
			held = k
		}
	}
	if held == "" {
		t.Fatal("the fixture's text is not known")
	}
	r := newRig(t, 140, 45, Options{Recording: "sync-frames.jsonl", Linger: time.Hour})
	r.play(440 * time.Millisecond)
	if strings.Contains(r.view(), held) {
		t.Fatal("shown before it is written")
	}
	r.sched = nil
	r.tick(480 * time.Millisecond) // the frame opens: the screen before it stays
	if strings.Contains(r.view(), held) {
		t.Fatal("a frame being drawn is shown")
	}
	var hold *scheduled
	for i := range r.sched {
		if r.sched[i].d == sessionpane.MaxFrameHold {
			hold = &r.sched[i]
		}
	}
	if hold == nil {
		t.Fatalf("no redraw scheduled at the hold deadline: %v", r.sched)
	}
	// Ticks inside the limit keep holding; the redraw at the limit shows it.
	r.tick(520 * time.Millisecond)
	r.tick(600 * time.Millisecond)
	if strings.Contains(r.view(), held) {
		t.Fatal("shown before the limit")
	}
	r.m.Update(hold.fn(r.t0.Add(630 * time.Millisecond)))
	if !strings.Contains(r.view(), held) {
		t.Fatalf("the held frame was not released:\n%s", r.view())
	}
}

func TestOutputIsFedInBoundedChunks(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	big := strings.Repeat("x", 30<<10)
	r.m.rep.queue = nil
	for i := 0; i < 3; i++ {
		r.m.rep.queue = append(r.m.rep.queue, sessionreplayOut(big))
	}
	r.m.feed(0)
	left := 0
	for _, e := range r.m.rep.queue {
		left += len(e.Data)
	}
	if want := 3*len(big) - tickBudget; left != want {
		t.Fatalf("%d bytes left, want %d", left, want)
	}
	for i := 0; i < 10 && len(r.m.rep.queue) > 0; i++ {
		r.m.feed(0)
	}
	if len(r.m.rep.queue) != 0 {
		t.Fatal("the queue does not drain")
	}
}

func TestPaneIsClosedAndMustNotLeak(t *testing.T) {
	m := New(Fixture{}, "dark")
	m.Close()
	m.Close()
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 45})
	_ = m.View()
}

func TestUnknownRecordingLeavesAnEmptyPane(t *testing.T) {
	r := newRig(t, 140, 45, Options{Recording: "no-such.jsonl"})
	r.play(100 * time.Millisecond)
	if !strings.Contains(r.view(), "NO RECORDING") || !strings.Contains(r.view(), fixtureLabel) {
		t.Fatal(r.view())
	}
	r.focusSession()
	r.key(zoomPress)
	r.key(decodeKey(t, "\x1c"))
	r.key(tea.KeyPressMsg{Code: 'r', Text: "r"})
	_ = r.view()
}

func TestShrinkingBelowTheMinimumLeavesSessionFocus(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	r.focusSession()
	r.size(90, 24)
	if r.m.focus != consoleFocus {
		t.Fatal("still in session focus with no pane on screen")
	}
	if cmd := r.key(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd == nil {
		t.Fatal("q does not quit the single-column pages")
	}
	if len(r.sink.keys) != 0 {
		t.Fatalf("keys went to the hidden session: %v", r.sink.keys)
	}
}

func TestModeReportSwitchesGraphemeWidths(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	if r.m.graphemes {
		t.Fatal("grapheme widths before the terminal says so")
	}
	for _, v := range []ansi.ModeSetting{ansi.ModeNotRecognized, ansi.ModeNotRecognized} {
		r.m.Update(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: v})
		if r.m.graphemes {
			t.Fatal("an unrecognized mode counts as supported")
		}
	}
	r.m.Update(tea.ModeReportMsg{Mode: ansi.ModeSynchronizedOutput, Value: ansi.ModeReset})
	if r.m.graphemes {
		t.Fatal("another mode's report counts")
	}
	r.m.Update(tea.ModeReportMsg{Mode: ansi.ModeUnicodeCore, Value: ansi.ModeReset})
	if !r.m.graphemes {
		t.Fatal("a supported mode 2027 is not noticed")
	}
	// A pane made later starts in the same width mode.
	r.key(tea.KeyPressMsg{Code: 'r', Text: "r"})
	_ = r.view()
}

func TestInitAsksForUnicodeCoreWithSSHTTYSet(t *testing.T) {
	for _, env := range [][2]string{{"SSH_TTY", "/dev/pts/9"}, {"SSH_TTY", ""}, {"TERM_PROGRAM", "Apple_Terminal"}} {
		t.Setenv(env[0], env[1])
		m := New(Fixture{}, "dark")
		cmd := m.Init()
		if cmd == nil {
			t.Fatal("Init returns no command")
		}
		raw, ok := cmd().(tea.RawMsg)
		if !ok || raw.Msg != "\x1b[?2027$p" {
			t.Fatalf("%v: Init returned %#v", env, cmd())
		}
		m.Close()
	}
}

func sessionreplayOut(data string) sessionreplay.Event {
	return sessionreplay.Event{Kind: sessionreplay.KindOutput, Data: []byte(data)}
}

func TestRecordedResizeDoesNotResizeThePane(t *testing.T) {
	r := newRig(t, 140, 45, Options{Recording: "resize.jsonl"})
	l := r.m.layout()
	r.play(1100 * time.Millisecond)
	if c, rr := r.paneSize(); c != l.inner.Dx() || rr != l.inner.Dy() {
		t.Fatalf("pane %dx%d, layout %v", c, rr, l.inner)
	}
}

// TestRandomInputKeepsTheCockpitSound sends a long, fixed pseudo-random mix of
// keys, pastes, mouse events, window sizes and ticks, and checks the drawn
// view each time: it never panics, never exceeds its canvas and never carries
// anything but styles.
func TestRandomInputKeepsTheCockpitSound(t *testing.T) {
	keys := []tea.KeyPressMsg{
		{Code: 'q', Text: "q"}, {Code: 'j', Text: "j"}, {Code: 'k', Text: "k"}, {Code: 'a', Text: "a"}, {Code: 'r', Text: "r"}, {Code: '?', Text: "?"},
		enterKey, tabKey, escKey, ctrlK, ctrlC, zoomPress, {Code: tea.KeyTab, Mod: tea.ModShift}, {Code: tea.KeyUp}, {Code: tea.KeyDown},
		{Code: '\\', Mod: tea.ModCtrl}, {Code: '4', Mod: tea.ModCtrl}, {Code: tea.KeyEnter, Mod: tea.ModCtrl},
	}
	sizes := [][2]int{{140, 45}, {100, 30}, {99, 30}, {100, 29}, {80, 24}, {1, 1}, {0, 0}, {160, 50}, {110, 35}}
	for _, name := range []string{"modes.jsonl", "exit-nonzero.jsonl", "queries.jsonl"} {
		r := newRig(t, 140, 45, Options{Recording: name, Linger: 3 * time.Second})
		seed := uint32(12345)
		next := func(n int) int {
			seed = seed*1664525 + 1013904223
			return int(seed>>8) % n
		}
		at := time.Duration(0)
		for i := 0; i < 120; i++ {
			switch next(8) {
			case 0, 1, 2:
				r.key(keys[next(len(keys))])
			case 3:
				sz := sizes[next(len(sizes))]
				r.size(sz[0], sz[1])
			case 4:
				r.m.Update(tea.PasteMsg{Content: "paste\x1b[201~text"})
			case 5:
				r.m.Update(tea.MouseClickMsg{X: next(160), Y: next(60), Button: tea.MouseLeft})
				r.m.Update(tea.MouseMotionMsg{X: next(160), Y: next(60)})
			default:
				at += time.Duration(next(80)) * time.Millisecond
				r.tick(at)
			}
			// A quit closes the pane; the model stays drawable.
			v := r.m.View()
			if bad := foreignSequence(v.Content); bad != "" {
				t.Fatalf("%s step %d: %q", name, i, bad)
			}
			rows := strings.Split(ansi.Strip(v.Content), "\n")
			if len(rows) > maxCanvasRows {
				t.Fatalf("%s step %d: %d rows", name, i, len(rows))
			}
			for _, row := range rows {
				if ansi.StringWidth(row) > maxCanvasCols {
					t.Fatalf("%s step %d: %d columns", name, i, ansi.StringWidth(row))
				}
			}
			if r.m.layout().cockpit {
				if c, rr := r.m.rep.pane.Size(); c > sessionpane.MaxCols || rr > sessionpane.MaxRows {
					t.Fatalf("%s step %d: pane %dx%d", name, i, c, rr)
				}
			}
		}
	}
}
