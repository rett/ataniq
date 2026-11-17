package terminalconsole

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/terminalconsole/sessionpane"
)

// fakeLive is an in-memory LiveSource: what a holder's attach client would
// do, driven by the test.
type fakeLive struct {
	mu       sync.Mutex
	start    LiveStart
	starts   int
	closes   int
	startErr error
	sink     *recSink
	st       LiveStatus
	notices  []LiveNotice
	resizes  [][2]int
	// deaf makes the holder ignore Resize: its size stays what the status
	// says. Otherwise the holder answers a Resize with that size, and takes
	// the size of the pane's area when the source starts.
	deaf bool
}

func newFake(role LiveRole) *fakeLive {
	return &fakeLive{sink: &recSink{}, st: LiveStatus{
		Lead: "scratch-lead", Epoch: 7, Runtime: "claude", Version: "2.1", Role: role,
		Cols: 80, Rows: 24, Conn: LiveAttached,
	}}
}

func (f *fakeLive) Start(s LiveStart) (sessionpane.InputSink, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	f.start = s
	if f.startErr != nil {
		return nil, f.startErr
	}
	if !f.deaf {
		f.st.Cols, f.st.Rows = s.Cols, s.Rows
	}
	return f.sink, nil
}

func (f *fakeLive) Resize(cols, rows int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]int{cols, rows})
	if !f.deaf {
		f.st.Cols, f.st.Rows = cols, rows
	}
}

func (f *fakeLive) Status() LiveStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeLive) Notices() []LiveNotice {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.notices
	f.notices = nil
	return n
}

func (f *fakeLive) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
}

// set changes the status the way an attach client does, and wakes the program.
func (f *fakeLive) set(fn func(*LiveStatus)) {
	f.mu.Lock()
	fn(&f.st)
	wake := f.start.Wake
	f.mu.Unlock()
	if wake != nil {
		wake()
	}
}

func (f *fakeLive) notify(ns ...LiveNotice) {
	f.mu.Lock()
	f.notices = append(f.notices, ns...)
	wake := f.start.Wake
	f.mu.Unlock()
	wake()
}

// emit writes session output to the pane and wakes the program.
func (f *fakeLive) emit(s string) {
	f.start.Pane.Write([]byte(s))
	f.start.Wake()
}

func (f *fakeLive) sizes() [][2]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][2]int(nil), f.resizes...)
}

// liveRig drives a live model with the rig's clock. waiting is the command
// the model last returned for its next wake-up.
type liveRig struct {
	*rig
	f       *fakeLive
	waiting tea.Cmd
}

func newLiveRig(t *testing.T, w, h int, f *fakeLive, o Options) *liveRig {
	t.Helper()
	if o.Theme == "" {
		o.Theme = "dark"
	}
	o.Live = f
	base := &rig{t: t, sink: &recSink{}, t0: time.Unix(1_000_000, 0)}
	base.clock = base.t0
	base.m = NewWithOptions(nil, o)
	t.Cleanup(base.m.Close)
	base.m.after = func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd {
		base.sched = append(base.sched, scheduled{d: d, fn: fn, at: base.clock.Add(d)})
		return nil
	}
	r := &liveRig{rig: base, f: f}
	if w > 0 {
		r.waiting = r.size(w, h)
	}
	return r
}

// next returns the message of the waiting command, which must have a wake-up
// to deliver.
func (r *liveRig) next() tea.Msg {
	r.t.Helper()
	if r.waiting == nil {
		r.t.Fatal("the model is not waiting for a wake-up")
	}
	cmd := r.waiting
	r.waiting = nil
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	select {
	case msg := <-got:
		return msg
	case <-time.After(5 * time.Second):
		r.t.Fatal("no wake-up arrived")
	}
	return nil
}

// settled reports whether the waiting command is still blocked after a short
// time: no wake-up pending.
func (r *liveRig) blocked() bool {
	cmd := r.waiting
	if cmd == nil {
		return true
	}
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	select {
	case msg := <-got:
		r.waiting = func() tea.Msg { return msg }
		return false
	case <-time.After(30 * time.Millisecond):
		// The goroutine stays blocked on the wake channel. Put the command
		// back for a later delivery, with a channel for its result.
		r.waiting = func() tea.Msg { return <-got }
		return true
	}
}

// pump delivers one wake-up and then the gap after it, as the program would,
// and leaves the model waiting again.
func (r *liveRig) pump() {
	r.t.Helper()
	msg := r.next()
	if _, ok := msg.(liveMsg); !ok {
		r.t.Fatalf("got %#v, want a liveMsg", msg)
	}
	r.m.Update(msg)
	r.gap()
}

// gap runs the gap timer the last wake-up scheduled.
func (r *liveRig) gap() {
	r.t.Helper()
	for i, s := range r.sched {
		if m := s.fn(s.at); isGap(m) {
			if s.d != liveGap {
				r.t.Fatalf("the gap is %v, want %v", s.d, liveGap)
			}
			r.sched = append(r.sched[:i:i], r.sched[i+1:]...)
			_, r.waiting = r.m.Update(m)
			return
		}
	}
	r.t.Fatalf("no gap timer was scheduled: %v", r.sched)
}

func isGap(m tea.Msg) bool {
	_, ok := m.(liveGapMsg)
	return ok
}

func (r *liveRig) focus() {
	r.t.Helper()
	r.focusSession()
}

func TestLiveStartHandsOverThePaneAndTheSink(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	if f.starts != 1 {
		t.Fatalf("Start called %d times", f.starts)
	}
	l := r.m.layout()
	s := f.start
	if s.Pane == nil || s.Pane != r.m.rep.pane || s.Wake == nil {
		t.Fatalf("start: %+v", s)
	}
	if s.Cols != l.inner.Dx() || s.Rows != l.inner.Dy() {
		t.Fatalf("start size %dx%d, area %v", s.Cols, s.Rows, l.inner)
	}
	if c, rr := s.Pane.Size(); c != s.Cols || rr != s.Rows {
		t.Fatalf("pane %dx%d", c, rr)
	}
	pal := paletteFor("dark")
	if s.Colours.Foreground != pal.fg || s.Colours.Background != pal.bg {
		t.Fatalf("colours %+v", s.Colours)
	}
	// A second window size does not start it again.
	r.size(150, 50)
	if f.starts != 1 {
		t.Fatalf("Start called %d times", f.starts)
	}
	// What the pane sends reaches the sink the source returned.
	r.focus()
	r.typ("x")
	if got := f.sink.keys; len(got) != 1 || got[0] != "x" {
		t.Fatalf("keys %v", got)
	}
	r.m.onPaste("pasted")
	if got := f.sink.pastes; len(got) != 1 || got[0] != "pasted" {
		t.Fatalf("pastes %v", got)
	}
}

func TestLiveStartsAtTheFirstWindowSizeEvenWhenItIsTooSmall(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 0, 0, f, Options{})
	if f.starts != 0 {
		t.Fatal("started before a window size")
	}
	r.waiting = r.size(80, 24)
	if f.starts != 1 || f.start.Cols != liveDefaultCols || f.start.Rows != liveDefaultRows {
		t.Fatalf("starts %d, %dx%d", f.starts, f.start.Cols, f.start.Rows)
	}
	// Growing to a cockpit asks for the area, after the debounce.
	r.size(140, 45)
	if len(r.sched) != 1 || r.sched[0].d != resizeDelay {
		t.Fatalf("%v", r.sched)
	}
}

func TestLiveOutputAppearsAfterAWakeUp(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	f.emit("hello from the holder")
	r.pump()
	if v := r.view(); !strings.Contains(v, "hello from the holder") {
		t.Fatalf("output not shown:\n%s", v)
	}
}

func TestLiveIdleRunsNoTimer(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	if len(r.sched) != 0 {
		t.Fatalf("timers at start: %v", r.sched)
	}
	if !r.blocked() {
		t.Fatal("a wake-up is pending though nothing happened")
	}
	// Window state changes and keys schedule nothing either.
	r.focus()
	r.key(zoomPress)
	if len(r.sched) != 0 {
		t.Fatalf("timers: %v", r.sched)
	}
	f.emit("x")
	r.pump()
	// After a wake-up the gap is the only timer, and it is gone once it ran.
	if len(r.sched) != 0 {
		t.Fatalf("timers left after the gap: %v", r.sched)
	}
	if !r.blocked() {
		t.Fatal("a second wake-up was pending")
	}
}

func TestLiveWakeUpsAreCoalescedAndRateLimited(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	for range 1000 {
		f.start.Wake()
	}
	msg := r.next()
	r.m.Update(msg)
	// The burst is one message, and the model listens again only after the
	// gap, so continuous output costs at most one redraw per gap.
	if r.waiting != nil || len(r.sched) != 1 || r.sched[0].d != liveGap {
		t.Fatalf("waiting %v, timers %v", r.waiting != nil, r.sched)
	}
	if liveGap < time.Second/60 {
		t.Fatalf("the gap %v allows more than 60 redraws a second", liveGap)
	}
	// Wake-ups during the gap are kept, and handled when it ends.
	f.start.Wake()
	f.start.Wake()
	r.gap()
	if r.blocked() {
		t.Fatal("the wake-up of the gap was lost")
	}
	r.pump()
	if !r.blocked() {
		t.Fatal("the coalesced wake-ups came as more than one")
	}
}

func TestLiveWriterKeysReachTheSinkAndViewerKeysDoNot(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	r.focus()
	r.typ("ab")
	r.key(tea.KeyPressMsg{Code: 'q'})
	r.key(ctrlC)
	if got := strings.Join(f.sink.keys, ","); got != "a,b,q,ctrl+c" {
		t.Fatalf("keys %q", got)
	}
	// The return key and F12 keep their meaning.
	r.key(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	if r.m.focus != consoleFocus {
		t.Fatal("the return key did not return")
	}
	if len(f.sink.keys) != 4 {
		t.Fatalf("the return key was sent: %v", f.sink.keys)
	}

	v := newFake(LiveViewer)
	rv := newLiveRig(t, 140, 45, v, Options{})
	rv.focus()
	rv.typ("ab")
	rv.key(ctrlC)
	rv.m.onPaste("text")
	if len(v.sink.keys) != 0 || len(v.sink.pastes) != 0 {
		t.Fatalf("a viewer sent keys %v, pastes %v", v.sink.keys, v.sink.pastes)
	}
	if !strings.Contains(rv.m.notice, "Read-only viewer") {
		t.Fatalf("notice %q", rv.m.notice)
	}
	// The notice is shown once.
	rv.m.notice = ""
	rv.typ("c")
	if rv.m.notice != "" || len(rv.m.live.notices) != 1 {
		t.Fatalf("notice %q repeated, %d kept", rv.m.notice, len(rv.m.live.notices))
	}
	if rv.m.focus != sessionFocus {
		t.Fatal("a key left the session focus")
	}
	rv.key(zoomPress)
	if !rv.m.zoom {
		t.Fatal("F12 did not zoom")
	}
	rv.key(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	if rv.m.focus != consoleFocus {
		t.Fatal("the return key did not return for a viewer")
	}
}

func TestLiveKeysWithoutARoleAreNotSent(t *testing.T) {
	f := newFake("")
	f.st.Conn = LiveConnecting
	r := newLiveRig(t, 140, 45, f, Options{})
	r.focus()
	r.typ("a")
	if len(f.sink.keys) != 0 || !strings.Contains(r.m.notice, "Not attached as the writer") {
		t.Fatalf("keys %v, notice %q", f.sink.keys, r.m.notice)
	}
}

// keyBar returns the bottom row of the window and the focus bar's second row.
func (r *liveRig) keyBar() (keys, focus string) {
	rows := r.rows()
	l := r.m.layout()
	return rows[len(rows)-1], rows[l.focus.Min.Y+2]
}

func TestLiveKeyBarIsHonestInSessionFocus(t *testing.T) {
	const everyKey = "every key goes to the session"
	for _, tc := range []struct {
		name string
		f    func() *fakeLive
		then func(*fakeLive)
		keys []string
		bar  string
	}{
		{"writer", func() *fakeLive { return newFake(LiveWriter) }, nil,
			[]string{everyKey, "Ctrl+\\ returns"}, "Every key goes to the session"},
		{"viewer", func() *fakeLive { return newFake(LiveViewer) }, nil,
			[]string{"read-only viewer: keys are not sent", "Ctrl+\\ returns"}, "not sent: read-only viewer"},
		{"no role", func() *fakeLive { f := newFake(""); f.st.Conn = LiveConnecting; return f }, nil,
			[]string{"no role yet: keys are not sent", "Ctrl+\\ returns"}, "not sent: no role yet"},
		{"ended", func() *fakeLive { return newFake(LiveWriter) },
			func(f *fakeLive) { f.set(func(s *LiveStatus) { s.End = LiveEnd{Stream: FactYes} }) },
			[]string{"the session has ended: keys are not sent", "any key returns"}, "The session has ended"},
		{"disconnected", func() *fakeLive { return newFake(LiveWriter) },
			func(f *fakeLive) { f.set(func(s *LiveStatus) { s.Conn = LiveDisconnected }) },
			[]string{"not connected: keys are not sent", "any key returns"}, "not sent: not connected"},
		{"waiting for a ticket", func() *fakeLive { return newFake(LiveWriter) },
			func(f *fakeLive) { f.set(func(s *LiveStatus) { s.Conn = LiveWaitingTicket }) },
			[]string{"not connected: keys are not sent", "any key returns"}, "not sent: not connected"},
	} {
		f := tc.f()
		r := newLiveRig(t, 140, 45, f, Options{})
		if tc.then != nil {
			tc.then(f)
			r.pump()
		}
		r.focus()
		keys, focus := r.keyBar()
		for _, want := range tc.keys {
			if !strings.Contains(keys, want) {
				t.Fatalf("%s: key bar %q lacks %q", tc.name, keys, want)
			}
		}
		if !strings.Contains(focus, tc.bar) {
			t.Fatalf("%s: focus bar %q lacks %q", tc.name, focus, tc.bar)
		}
		// The bar says what the keys then do.
		r.typ("x")
		if tc.name == "writer" {
			if r.m.focus != sessionFocus || len(f.sink.keys) != 1 {
				t.Fatalf("writer: focus %v keys %v", r.m.focus.name(), f.sink.keys)
			}
			continue
		}
		if len(f.sink.keys) != 0 {
			t.Fatalf("%s: sent %v", tc.name, f.sink.keys)
		}
		anyKey := strings.Contains(keys, "any key returns")
		if anyKey != (r.m.focus == consoleFocus) {
			t.Fatalf("%s: bar %q, focus after a key %v", tc.name, keys, r.m.focus.name())
		}
		if !anyKey && r.m.focus != sessionFocus {
			t.Fatalf("%s: a key left the session focus", tc.name)
		}
		if strings.Contains(keys, everyKey) || strings.Contains(focus, "Every key") {
			t.Fatalf("%s: a bar says every key is sent: %q, %q", tc.name, keys, focus)
		}
	}
}

func TestLiveTheReadOnlyNoticeIsShownAfterEachLossOfTheWriterRole(t *testing.T) {
	f := newFake(LiveViewer)
	r := newLiveRig(t, 140, 45, f, Options{})
	r.focus()
	readOnly := func() int {
		n := 0
		for _, note := range r.m.live.notices {
			if note.code == noticeReadOnly {
				n++
			}
		}
		return n
	}
	r.typ("a")
	if readOnly() != 1 || !strings.Contains(r.m.notice, "Read-only viewer") {
		t.Fatalf("first: %d notices, notice %q", readOnly(), r.m.notice)
	}
	r.m.notice = ""
	r.typ("b")
	if readOnly() != 1 || r.m.notice != "" {
		t.Fatalf("repeated within one role: %d notices", readOnly())
	}
	// The client becomes the writer and types, then loses the role.
	f.set(func(s *LiveStatus) { s.Role = LiveWriter })
	r.pump()
	r.typ("c")
	if got := f.sink.keys; len(got) != 1 || got[0] != "c" {
		t.Fatalf("keys %v", got)
	}
	f.set(func(s *LiveStatus) { s.Role = LiveViewer })
	r.pump()
	r.m.notice = ""
	r.typ("d")
	if readOnly() != 2 || !strings.Contains(r.m.notice, "Read-only viewer") {
		t.Fatalf("second loss of the role: %d notices, notice %q", readOnly(), r.m.notice)
	}
	if len(f.sink.keys) != 1 {
		t.Fatalf("a viewer sent %v", f.sink.keys)
	}
	// Once more, for a paste, after losing the role again.
	f.set(func(s *LiveStatus) { s.Role = LiveWriter })
	r.pump()
	f.set(func(s *LiveStatus) { s.Role = "" })
	r.pump()
	r.m.onPaste("p")
	if readOnly() != 3 || len(f.sink.pastes) != 0 {
		t.Fatalf("third: %d notices, pastes %v", readOnly(), f.sink.pastes)
	}
}

func TestLiveModesFromTheStatusDriveTheOuterTerminal(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	r.focus()
	// The pane's own emulator says the opposite of the status.
	r.m.rep.pane.Write([]byte("\x1b[?1000h\x1b[?2004h"))
	if modes := r.m.rep.pane.Modes(); modes.Mouse == uv.MouseModeNone || !modes.BracketedPaste {
		t.Fatalf("pane modes %+v", modes)
	}
	v := r.m.View()
	if v.MouseMode != tea.MouseModeCellMotion || v.ReportFocus || !v.DisableBracketedPasteMode {
		t.Fatalf("view follows the pane: mouse %v focus %v paste off %v", v.MouseMode, v.ReportFocus, v.DisableBracketedPasteMode)
	}
	f.set(func(s *LiveStatus) {
		s.Modes = sessionpane.Modes{Mouse: uv.MouseModeMotion, BracketedPaste: true, FocusEvents: true}
	})
	r.pump()
	v = r.m.View()
	if v.MouseMode != tea.MouseModeAllMotion || !v.ReportFocus || v.DisableBracketedPasteMode {
		t.Fatalf("view does not follow the status: mouse %v focus %v paste off %v", v.MouseMode, v.ReportFocus, v.DisableBracketedPasteMode)
	}
	// Console focus asks for none of it, and keeps bracketed paste on so that
	// a paste is not read as keys.
	r.key(tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl})
	v = r.m.View()
	if v.MouseMode != tea.MouseModeCellMotion || v.ReportFocus || v.DisableBracketedPasteMode {
		t.Fatalf("console focus: mouse %v focus %v paste off %v", v.MouseMode, v.ReportFocus, v.DisableBracketedPasteMode)
	}
	// A viewer asks for none of it either, and the mouse selects.
	vf := newFake(LiveViewer)
	vf.st.Modes = sessionpane.Modes{Mouse: uv.MouseModeMotion, BracketedPaste: true, FocusEvents: true}
	rv := newLiveRig(t, 140, 45, vf, Options{})
	rv.focus()
	if v := rv.m.View(); v.MouseMode != tea.MouseModeCellMotion || v.ReportFocus || v.DisableBracketedPasteMode {
		t.Fatalf("viewer: mouse %v focus %v paste off %v", v.MouseMode, v.ReportFocus, v.DisableBracketedPasteMode)
	}
	// A session that has ended asks for none.
	f.start.Pane.End(0, true)
	r.focus()
	if v := r.m.View(); v.MouseMode != tea.MouseModeCellMotion || v.ReportFocus {
		t.Fatalf("ended: mouse %v focus %v", v.MouseMode, v.ReportFocus)
	}
}

func TestLiveMouseGoesToTheSinkByTheStatusModes(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	r.focus()
	x, y := r.win(3, 2)
	click := tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
	// The status says none: nothing is sent, and the click starts a selection.
	r.m.Update(click)
	if len(f.sink.mouse) != 0 {
		t.Fatalf("mouse sent: %v", f.sink.mouse)
	}
	f.set(func(s *LiveStatus) { s.Modes = sessionpane.Modes{Mouse: uv.MouseModeClick} })
	r.pump()
	r.m.Update(click)
	if len(f.sink.mouse) != 1 {
		t.Fatalf("mouse %v", f.sink.mouse)
	}
	if m := f.sink.mouse[0].Mouse(); m.X != 3 || m.Y != 2 {
		t.Fatalf("pane cell %d,%d", m.X, m.Y)
	}
	// Release passes with click tracking, motion does not.
	r.m.Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	r.m.Update(tea.MouseMotionMsg{X: x + 1, Y: y, Button: tea.MouseLeft})
	if len(f.sink.mouse) != 2 {
		t.Fatalf("mouse %v", f.sink.mouse)
	}
	// Outside the pane nothing is sent.
	r.m.Update(tea.MouseClickMsg{X: 0, Y: 0, Button: tea.MouseLeft})
	if len(f.sink.mouse) != 2 {
		t.Fatalf("mouse %v", f.sink.mouse)
	}
	// A viewer sends none, whatever the modes.
	vf := newFake(LiveViewer)
	vf.st.Modes = sessionpane.Modes{Mouse: uv.MouseModeMotion}
	rv := newLiveRig(t, 140, 45, vf, Options{})
	rv.focus()
	x, y = rv.win(3, 2)
	rv.m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	if len(vf.sink.mouse) != 0 {
		t.Fatalf("a viewer sent %v", vf.sink.mouse)
	}
}

func TestLiveResizeIsDebouncedAndNeverSentAsAViewer(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	c0, r0 := f.start.Cols, f.start.Rows
	r.size(150, 50)
	r.size(160, 55)
	if len(f.sizes()) != 0 {
		t.Fatalf("sent at once: %v", f.sizes())
	}
	if len(r.sched) != 2 || r.sched[0].d != resizeDelay {
		t.Fatalf("%v", r.sched)
	}
	r.m.Update(r.sched[0].fn(r.t0))
	if len(f.sizes()) != 0 {
		t.Fatal("a stale resize was sent")
	}
	r.m.Update(r.sched[1].fn(r.t0))
	l := r.m.layout()
	got := f.sizes()
	if len(got) != 1 || got[0] != [2]int{l.inner.Dx(), l.inner.Dy()} || got[0][0] <= c0 || got[0][1] <= r0 {
		t.Fatalf("sent %v, area %v, was %dx%d", got, l.inner, c0, r0)
	}
	// The cockpit never resizes the pane itself: the source follows the holder.
	if c, rr := f.start.Pane.Size(); c != c0 || rr != r0 {
		t.Fatalf("the pane was resized to %dx%d", c, rr)
	}
	// Zoom is at once. The same size is not asked twice.
	r.key(zoomPress)
	r.key(zoomPress)
	got = f.sizes()
	if len(got) != 3 || got[1] == got[0] || got[2] != got[0] {
		t.Fatalf("zoom and back sent %v", got)
	}
	// A window size that gives the size already asked for sends nothing.
	r.m.Update(tea.WindowSizeMsg{Width: 160, Height: 55})
	for _, s := range r.sched[2:] {
		r.m.Update(s.fn(r.t0))
	}
	if n := len(f.sizes()); n != 3 {
		t.Fatalf("sent %d sizes", n)
	}

	vf := newFake(LiveViewer)
	rv := newLiveRig(t, 140, 45, vf, Options{})
	rv.size(150, 50)
	rv.key(zoomPress)
	for _, s := range rv.sched {
		rv.m.Update(s.fn(rv.t0))
	}
	if got := vf.sizes(); len(got) != 0 {
		t.Fatalf("a viewer asked for a size: %v", got)
	}
}

func TestLiveANewWriterAsksForItsSizeAtOnce(t *testing.T) {
	f := newFake(LiveViewer)
	r := newLiveRig(t, 140, 45, f, Options{})
	r.size(150, 50)
	for _, s := range r.sched {
		r.m.Update(s.fn(r.t0))
	}
	if len(f.sizes()) != 0 {
		t.Fatalf("sent as a viewer: %v", f.sizes())
	}
	f.set(func(s *LiveStatus) { s.Role = LiveWriter })
	r.pump()
	l := r.m.layout()
	if got := f.sizes(); len(got) != 1 || got[0] != [2]int{l.inner.Dx(), l.inner.Dy()} {
		t.Fatalf("sent %v, area %v", got, l.inner)
	}
	// And a writer made a viewer stops.
	f.set(func(s *LiveStatus) { s.Role = LiveViewer })
	r.pump()
	r.key(zoomPress)
	if len(f.sizes()) != 1 {
		t.Fatalf("sent %v", f.sizes())
	}
}

// settle runs every timer the model has scheduled, such as the resize
// debounce.
func (r *liveRig) settle() {
	r.t.Helper()
	r.run(24 * time.Hour)
}

func TestLiveAWriterThatGetsTheRoleBackAsksForItsSize(t *testing.T) {
	// The holder is resized by another writer while this client is a viewer;
	// the client's own window has not changed.
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	l := r.m.layout()
	area := [2]int{l.inner.Dx(), l.inner.Dy()}
	f.set(func(s *LiveStatus) { s.Role = LiveViewer })
	r.pump()
	f.start.Pane.Resize(60, 20)
	f.set(func(s *LiveStatus) { s.Cols, s.Rows = 60, 20 })
	r.pump()
	if len(f.sizes()) != 0 {
		t.Fatalf("a viewer asked for a size: %v", f.sizes())
	}
	f.set(func(s *LiveStatus) { s.Role = LiveWriter })
	r.pump()
	if got := f.sizes(); len(got) != 1 || got[0] != area {
		t.Fatalf("the regained writer asked for %v, want %v", got, area)
	}

	// A writer whose holder already has its size asks for nothing.
	f.set(func(s *LiveStatus) { s.Role = LiveViewer })
	r.pump()
	f.set(func(s *LiveStatus) { s.Role = LiveWriter })
	r.pump()
	if n := len(f.sizes()); n != 1 {
		t.Fatalf("asked again for a size the holder has: %v", f.sizes())
	}

	// A holder that does not follow is asked again, with the same size, at
	// each point of the rule, and nothing worse comes of it.
	f.set(func(s *LiveStatus) { s.Cols, s.Rows = 70, 25 })
	r.key(zoomPress)
	r.key(zoomPress)
	got := f.sizes()
	if len(got) != 3 || got[2] != area {
		t.Fatalf("sizes %v", got)
	}
}

func TestLiveAWriterWithAnUnknownHolderSizeAsksOnlyForANewSize(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	f.set(func(s *LiveStatus) { s.Cols, s.Rows = 0, 0 })
	r.pump()
	r.m.Update(resizeMsg{seq: r.m.resizeSeq})
	if len(f.sizes()) != 0 {
		t.Fatalf("asked for the size already asked: %v", f.sizes())
	}
	r.key(zoomPress)
	if len(f.sizes()) != 1 {
		t.Fatalf("zoom: %v", f.sizes())
	}
}

func TestLiveASizeWantedWhileTheConnectionWasDownIsSentWhenItIsBack(t *testing.T) {
	for _, down := range []LiveConn{LiveDisconnected, LiveWaitingTicket} {
		f := newFake(LiveWriter)
		r := newLiveRig(t, 140, 45, f, Options{})
		// The source keeps the role as last granted while the connection is
		// down.
		f.set(func(s *LiveStatus) { s.Conn, s.Reason = down, "gone" })
		r.pump()
		r.size(160, 55)
		r.settle()
		if len(f.sizes()) != 0 {
			t.Fatalf("%v: asked while down: %v", down, f.sizes())
		}
		l := r.m.layout()
		want := [2]int{l.inner.Dx(), l.inner.Dy()}
		f.set(func(s *LiveStatus) { s.Conn, s.Reason = LiveAttached, "" })
		r.pump()
		if got := f.sizes(); len(got) != 1 || got[0] != want {
			t.Fatalf("%v: after the reconnect asked %v, want %v", down, got, want)
		}

		// A window that did not change while down, on a holder that kept
		// its size, asks for nothing.
		f.set(func(s *LiveStatus) { s.Conn = down })
		r.pump()
		f.set(func(s *LiveStatus) { s.Conn = LiveAttached })
		r.pump()
		if n := len(f.sizes()); n != 1 {
			t.Fatalf("%v: asked again: %v", down, f.sizes())
		}

		// A holder that changed its size while the connection was down is
		// asked for the size too.
		f.set(func(s *LiveStatus) { s.Conn = down })
		r.pump()
		f.set(func(s *LiveStatus) { s.Conn, s.Cols, s.Rows = LiveAttached, 90, 30 })
		r.pump()
		if got := f.sizes(); len(got) != 2 || got[1] != want {
			t.Fatalf("%v: asked %v, want %v again", down, got, want)
		}
	}
}

func TestLiveViewerDrawsTheHoldersSizeLetterboxedAndCropped(t *testing.T) {
	f := newFake(LiveViewer)
	r := newLiveRig(t, 140, 45, f, Options{})
	f.start.Pane.Resize(40, 10)
	f.emit("\x1b[2J\x1b[Htop left\r\n\x1b[10;1Hbottom row")
	r.pump()
	l := r.m.layout()
	rows := r.rows()
	if !strings.Contains(rows[l.inner.Min.Y], "top left") || !strings.Contains(rows[l.inner.Min.Y+9], "bottom row") {
		t.Fatalf("not at the pane's corner:\n%s", r.view())
	}
	if len(f.sizes()) != 0 {
		t.Fatalf("a viewer asked for a size: %v", f.sizes())
	}
	// A holder screen larger than the area is cut, never resized.
	f.start.Pane.Resize(200, 80)
	f.emit("\x1b[Hcropped")
	r.pump()
	if !strings.Contains(r.view(), "cropped") || len(f.sizes()) != 0 {
		t.Fatalf("sent %v", f.sizes())
	}
}

func TestLiveNoticesAreShownBoundedAndInZoom(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	f.notify(LiveNotice{Code: NoticeHeldKey})
	r.pump()
	rows := r.rows()
	if last := rows[len(rows)-1]; !strings.Contains(last, "held back") {
		t.Fatalf("key bar %q", last)
	}
	f.notify(LiveNotice{Code: NoticeWriterChanged, Client: "owner-console"})
	r.pump()
	if !strings.Contains(r.m.notice, "owner-console") || !strings.Contains(r.m.notice, "writer") {
		t.Fatalf("notice %q", r.m.notice)
	}
	// Zoom shows the newest in its header.
	r.key(zoomPress)
	if head := r.rows()[0]; !strings.Contains(head, "owner-console") || !strings.Contains(head, "live scratch session") {
		t.Fatalf("zoom header %q", head)
	}
	r.key(zoomPress)

	// More than the limit: the list is bounded, the newest kept.
	var many []LiveNotice
	for i := range 3 * LiveNoticeLimit {
		many = append(many, LiveNotice{Code: NoticeTooSlow, Text: fmt.Sprintf("slow number %02d", i)})
	}
	f.notify(many...)
	r.pump()
	if n := len(r.m.live.notices); n != LiveNoticeLimit {
		t.Fatalf("%d notices kept", n)
	}
	if r.m.notice != "slow number 47" {
		t.Fatalf("newest %q", r.m.notice)
	}
	ev := strings.Join(textOf(r.m.evidenceLines(60)), "\n")
	if !strings.Contains(ev, "slow number 47") || !strings.Contains(ev, "slow number 32") || strings.Contains(ev, "slow number 31") {
		t.Fatalf("evidence:\n%s", ev)
	}
	if strings.Contains(ev, "owner-console") {
		t.Fatal("an old notice was kept past the bound")
	}
	// Notices are bounded and cleaned on the way in.
	f.notify(LiveNotice{Code: "x\x1b[31m" + strings.Repeat("c", 100), Text: "a\x1bb" + strings.Repeat("t", 1000), Client: "c\x07"})
	r.pump()
	last := r.m.live.notices[len(r.m.live.notices)-1]
	if len(last.code) > maxLiveCode || len(last.text) > maxLiveText || strings.ContainsAny(last.code+last.text, "\x1b\x07") {
		t.Fatalf("%q %q", last.code, last.text)
	}
	// A burst larger than the batch bound keeps its newest.
	big := make([]LiveNotice, 10_000)
	for i := range big {
		big[i] = LiveNotice{Code: "n", Text: fmt.Sprintf("n%d", i)}
	}
	f.notify(big...)
	r.pump()
	if r.m.notice != "n9999" {
		t.Fatalf("newest %q", r.m.notice)
	}
}

func textOf(ls []line) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.s)
	}
	return out
}

func TestLiveNoticeClearsOnTheNextKey(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	r.focus()
	f.notify(LiveNotice{Code: NoticeHeldKey, Text: "Ctrl+Z is not forwarded"})
	r.pump()
	if r.m.notice != "Ctrl+Z is not forwarded" {
		t.Fatalf("notice %q", r.m.notice)
	}
	r.key(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	if r.m.notice != "" {
		t.Fatalf("the notice stayed over the key hint: %q", r.m.notice)
	}
	if got := f.sink.keys; len(got) != 1 || got[0] != "ctrl+z" {
		t.Fatalf("keys %v", got)
	}
}

func TestLiveEndBannerStatesTheThreeFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  LiveEnd
		want []string
	}{
		{"all known", LiveEnd{Stream: FactYes, TUIExited: FactYes, ExitStatus: 3, ExitStatusKnown: true, TreeGone: FactYes},
			[]string{"terminal stream ended", "TUI exited with status 3", "process tree gone"}},
		{"unknown", LiveEnd{Stream: FactYes},
			[]string{"terminal stream ended", "TUI exit: unknown", "process tree: unknown"}},
		{"no status", LiveEnd{TUIExited: FactYes, TreeGone: FactNo},
			[]string{"terminal stream: unknown", "TUI exited, status unknown", "process tree still present"}},
	} {
		f := newFake(LiveWriter)
		r := newLiveRig(t, 140, 45, f, Options{})
		f.emit("last screen text")
		r.pump()
		if strings.Contains(r.view(), "SESSION ENDED") {
			t.Fatalf("%s: ended early", tc.name)
		}
		f.set(func(s *LiveStatus) { s.End = tc.end })
		if tc.end.Stream == FactYes {
			f.start.Pane.End(tc.end.ExitStatus, tc.end.ExitStatusKnown)
		}
		r.pump()
		v := r.view()
		for _, want := range append([]string{"SESSION ENDED", "last screen text"}, tc.want...) {
			if !strings.Contains(v, want) {
				t.Fatalf("%s: missing %q:\n%s", tc.name, want, v)
			}
		}
		faint, plain := faintText(r.m.View().Content)
		if !strings.Contains(faint, "last screen text") || strings.Contains(faint, "SESSION ENDED") || !strings.Contains(plain, "SESSION ENDED") {
			t.Fatalf("%s: the last screen is not dimmed under the banner: faint %q", tc.name, faint)
		}
		if strings.Contains(v, "fixture") || strings.Contains(v, "replays the recording") {
			t.Fatalf("%s: fixture text in a live session:\n%s", tc.name, v)
		}
		// The evidence pane says the same.
		ev := strings.Join(textOf(r.m.evidenceLines(60)), "\n")
		for _, want := range tc.want {
			if !strings.Contains(ev, want) {
				t.Fatalf("%s: evidence lacks %q:\n%s", tc.name, want, ev)
			}
		}
	}
}

func TestLiveEndWithoutThePaneEndedStillShowsTheBanner(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	f.set(func(s *LiveStatus) {
		s.End = LiveEnd{TUIExited: FactYes, ExitStatus: 3, ExitStatusKnown: true, TreeGone: FactYes}
	})
	r.pump()
	if v := r.view(); !strings.Contains(v, "TUI exited with status 3") {
		t.Fatalf("no banner:\n%s", v)
	}
	// Keys no longer go to the session.
	r.focus()
	r.typ("x")
	if len(f.sink.keys) != 0 || r.m.focus != consoleFocus {
		t.Fatalf("keys %v, focus %v", f.sink.keys, r.m.focus.name())
	}
}

func TestLiveRDoesNotReplay(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	f.emit("screen text")
	r.pump()
	pane := r.m.rep.pane
	r.typ("r")
	if r.m.rep.pane != pane || r.m.rep.player != nil || f.starts != 1 || f.closes != 0 {
		t.Fatal("r restarted something")
	}
	if !strings.Contains(r.view(), "screen text") || !strings.Contains(r.m.notice, "cannot be replayed") {
		t.Fatalf("notice %q", r.m.notice)
	}
	// Nor after the end.
	f.set(func(s *LiveStatus) { s.End = LiveEnd{Stream: FactYes} })
	pane.End(0, false)
	r.pump()
	r.typ("r")
	if r.m.rep.pane != pane || !strings.Contains(r.view(), "screen text") {
		t.Fatal("r replayed an ended live session")
	}
}

func TestLiveDisconnectedAndWaitingKeepTheDimmedScreenUnderABanner(t *testing.T) {
	for _, tc := range []struct {
		conn  LiveConn
		title string
		want  string
	}{
		{LiveDisconnected, "DISCONNECTED", "reset by peer"},
		{LiveWaitingTicket, "WAITING FOR A TICKET", "no ticket free"},
	} {
		f := newFake(LiveWriter)
		r := newLiveRig(t, 140, 45, f, Options{})
		f.emit("kept screen")
		r.pump()
		if v := r.view(); strings.Contains(v, tc.title) {
			t.Fatalf("%s shown while attached", tc.title)
		}
		f.set(func(s *LiveStatus) { s.Conn, s.Reason = tc.conn, tc.want })
		r.pump()
		v := r.view()
		for _, want := range []string{tc.title, tc.want, "kept screen"} {
			if !strings.Contains(v, want) {
				t.Fatalf("%s: missing %q:\n%s", tc.title, want, v)
			}
		}
		faint, plain := faintText(r.m.View().Content)
		if !strings.Contains(faint, "kept screen") || strings.Contains(faint, tc.title) || !strings.Contains(plain, tc.title) {
			t.Fatalf("%s: faint %q", tc.title, faint)
		}
		// Typing sends nothing and returns to the console.
		r.focus()
		r.typ("x")
		if len(f.sink.keys) != 0 || r.m.focus != consoleFocus {
			t.Fatalf("%s: keys %v focus %v", tc.title, f.sink.keys, r.m.focus.name())
		}
		// No modes, no size, no paste while down.
		f.set(func(s *LiveStatus) { s.Modes = sessionpane.Modes{Mouse: uv.MouseModeMotion} })
		r.pump()
		r.focus()
		if v := r.m.View(); v.MouseMode != tea.MouseModeCellMotion {
			t.Fatalf("%s: asked for the mouse", tc.title)
		}
		r.m.onPaste("p")
		r.size(150, 50)
		for _, s := range r.sched {
			r.m.Update(s.fn(r.t0))
		}
		if len(f.sink.pastes) != 0 || len(f.sizes()) != 0 {
			t.Fatalf("%s: pastes %v sizes %v", tc.title, f.sink.pastes, f.sizes())
		}
	}
}

func TestLiveConnectingAndResyncing(t *testing.T) {
	f := newFake("")
	f.st.Conn = LiveConnecting
	r := newLiveRig(t, 140, 45, f, Options{})
	if v := r.view(); !strings.Contains(v, "CONNECTING") || !strings.Contains(v, "no role yet") {
		t.Fatalf("connecting:\n%s", v)
	}
	f.set(func(s *LiveStatus) { s.Conn, s.Role = LiveResyncing, LiveWriter })
	r.pump()
	v := r.view()
	if strings.Contains(v, "CONNECTING") || strings.Contains(v, "DISCONNECTED") || !strings.Contains(v, "resyncing") {
		t.Fatalf("resyncing:\n%s", v)
	}
}

func TestLiveLabelsNameTheSessionNotTheFixture(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	for _, c := range []struct {
		focus bool
		zoom  bool
	}{{false, false}, {true, false}, {false, true}, {true, true}} {
		r.m.zoom, r.m.focus = c.zoom, consoleFocus
		if c.focus {
			r.m.focus = sessionFocus
		}
		v := r.view()
		if strings.Contains(strings.ToLower(v), "fixture") || strings.Contains(v, "not a live session") {
			t.Fatalf("%+v: fixture text in a live session:\n%s", c, v)
		}
		for _, want := range []string{"live scratch session", "lead scratch-lead", "writer", "epoch 7"} {
			if !strings.Contains(v, want) {
				t.Fatalf("%+v: missing %q:\n%s", c, want, v)
			}
		}
	}
	r.m.zoom, r.m.focus = false, consoleFocus
	f.set(func(s *LiveStatus) { s.Role = LiveViewer })
	r.pump()
	v := r.view()
	if !strings.Contains(v, "read-only viewer") || strings.Contains(v, "writer ·") {
		t.Fatalf("viewer label:\n%s", v)
	}
	// The overlays are live too.
	r.key(tea.KeyPressMsg{Code: '?'})
	hv := r.view()
	if strings.Contains(strings.ToLower(hv), "fixture") || !strings.Contains(hv, "There is no replay") {
		t.Fatalf("help:\n%s", hv)
	}
	r.key(escKey)
	r.key(ctrlK)
	if av := r.view(); !strings.Contains(av, "no daemon") || strings.Contains(strings.ToLower(av), "fixture") {
		t.Fatalf("actions:\n%s", av)
	}
}

func TestLiveShowsNoFixtureProjectionEvenWithASnapshot(t *testing.T) {
	f := newFake(LiveWriter)
	o := Options{Theme: "dark", Live: f}
	m := NewWithOptions(Fixture{}, o)
	t.Cleanup(m.Close)
	m.after = func(time.Duration, func(time.Time) tea.Msg) tea.Cmd { return nil }
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 45})
	v := ansiStrip(m.View().Content)
	for _, bad := range []string{"Northstar", "Harbor", "Planning lead", "Delivery lead", "Merge feature branch", "Fixture", "fixture", "PENDING GATE", "Approver"} {
		if strings.Contains(v, bad) {
			t.Fatalf("%q shown in live mode:\n%s", bad, v)
		}
	}
	for _, want := range []string{"scratch-lead", "claude 2.1", "No daemon, no projects.", "check mismatches 0 · resyncs 0 · refused input 0", sizeText(f.start.Cols, f.start.Rows)} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q:\n%s", want, v)
		}
	}
	ev := strings.Join(textOf(m.evidenceLines(200)), "\n")
	for _, want := range []string{"One writer, no takeover.", "Input is the owner's direct words.", "There is no daemon, project, gate or delegate data"} {
		if !strings.Contains(ev, want) {
			t.Fatalf("evidence lacks %q:\n%s", want, ev)
		}
	}
	// The tree has nothing to select: its cursor, the selected project and
	// lead, the focus and the picture stay what they were.
	before := ansiStrip(m.View().Content)
	cursor, project, lead := m.treeCursor, m.project, m.lead
	m.pane = paneTree
	m.Update(enterKey)
	m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	if m.treeCursor != cursor || m.project != project || m.lead != lead || m.focus != consoleFocus {
		t.Fatalf("the tree moved: cursor %d project %d lead %d focus %v", m.treeCursor, m.project, m.lead, m.focus.name())
	}
	if after := ansiStrip(m.View().Content); after != before {
		t.Fatalf("the tree changed the picture:\n%s\n--- was:\n%s", after, before)
	}
}

func TestLiveCountersAndStatusAreShown(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	f.set(func(s *LiveStatus) { s.CheckMismatches, s.Resyncs, s.RefusedInput = 2, 3, 4 })
	r.pump()
	if v := r.view(); !strings.Contains(v, "check mismatches 2 · resyncs 3 · refused input 4") {
		t.Fatalf("counters:\n%s", v)
	}
	// Status text is cleaned.
	f.set(func(s *LiveStatus) { s.Lead, s.Runtime, s.Reason = "le\x1b[31mad", "run\x07time", "re\x1bason" })
	r.pump()
	if v := r.m.View().Content; strings.Contains(v, "\x1b[31mad") || strings.Contains(v, "\x07") {
		t.Fatalf("control bytes in the view: %q", v)
	}
}

func TestLiveSelectionAndCopyKeepWorking(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	f.emit("\x1b[2J\x1b[H" + selText)
	r.pump()
	wantCopy(t, r.dragPane(0, 0, 4, 0), "hello")
	// A change under the selection clears it.
	r.dragPane(0, 0, 4, 0)
	if !r.m.sel.shown() {
		t.Fatal("no selection")
	}
	f.emit("\x1b[Hjelly")
	r.pump()
	if r.m.sel.shown() {
		t.Fatal("the selection stayed over changed text")
	}
	// A change of the pane's size clears it.
	r.dragPane(0, 0, 4, 0)
	f.start.Pane.Resize(60, 20)
	f.start.Wake()
	r.pump()
	if r.m.sel.shown() {
		t.Fatal("the selection stayed after the pane changed size")
	}
}

func TestLiveSmallWindowShowsOneHonestPage(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 80, 24, f, Options{})
	v := r.view()
	for _, want := range []string{"needs a window of 100x30 or more", "This window is 80x24", "lead scratch-lead", "writer", "epoch 7"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q:\n%s", want, v)
		}
	}
	for _, bad := range []string{"Northstar", "fixture", "Fixture", "DEMO"} {
		if strings.Contains(v, bad) {
			t.Fatalf("%q in:\n%s", bad, v)
		}
	}
	// Every page key leaves the same page, and q quits and closes the source.
	for _, k := range []tea.KeyPressMsg{{Code: '?', Text: "?"}, ctrlK, enterKey, {Code: 'j', Text: "j"}, escKey} {
		r.key(k)
		if !strings.Contains(r.view(), "needs a window of 100x30 or more") {
			t.Fatalf("key %v changed the page:\n%s", k, r.view())
		}
	}
	cmd := r.key(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if cmd == nil || f.closes != 1 {
		t.Fatalf("quit: cmd %v, closes %d", cmd != nil, f.closes)
	}
	// Notices still show in the page.
	r2 := newLiveRig(t, 80, 24, newFake(LiveWriter), Options{})
	r2.f.notify(LiveNotice{Code: NoticeTooSlow})
	r2.pump()
	if !strings.Contains(r2.view(), "Too slow") {
		t.Fatalf("no notice:\n%s", r2.view())
	}
}

func TestLiveStartErrorIsShown(t *testing.T) {
	f := newFake(LiveWriter)
	f.startErr = errors.New("no holder at the socket\x1b[31m")
	r := newLiveRig(t, 140, 45, f, Options{})
	if r.waiting != nil || r.m.rep.pane != nil {
		t.Fatal("a failed source left a pane or a wait")
	}
	v := r.view()
	if !strings.Contains(v, "LIVE SESSION UNAVAILABLE") || !strings.Contains(v, "no holder at the socket") {
		t.Fatalf("no message:\n%s", v)
	}
	r.focus2()
	r.m.Close()
	if f.closes != 1 {
		t.Fatalf("closes %d", f.closes)
	}
}

// focus2 tries to focus the session and must not panic without a pane.
func (r *liveRig) focus2() {
	r.key(tabKey)
	r.key(tabKey)
	r.key(enterKey)
	r.typ("x")
	r.m.onPaste("x")
	r.m.Update(tea.MouseClickMsg{X: 50, Y: 20, Button: tea.MouseLeft})
}

func TestLiveCloseClosesTheSourceOnceAndEndsTheWait(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	cmd := r.waiting
	got := make(chan tea.Msg, 1)
	go func() { got <- cmd() }()
	r.m.Close()
	r.m.Close()
	select {
	case msg := <-got:
		if msg != nil {
			t.Fatalf("got %#v after Close", msg)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not end at Close")
	}
	if f.closes != 1 {
		t.Fatalf("closes %d", f.closes)
	}
	// A wake-up after Close does nothing, and the gap does not listen again.
	f.start.Wake()
	if _, c := r.m.Update(liveGapMsg{}); c != nil {
		t.Fatal("listening after Close")
	}
	if _, c := r.m.Update(liveMsg{}); c != nil {
		t.Fatal("handled a wake-up after Close")
	}
	_ = r.m.View()
}

func TestLiveCloseBeforeStartDoesNotCallTheSource(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 0, 0, f, Options{})
	r.m.Close()
	if f.closes != 0 || f.starts != 0 {
		t.Fatalf("starts %d closes %d", f.starts, f.closes)
	}
	if v := r.m.View(); v.Content != "" {
		t.Fatalf("content %q", v.Content)
	}
}

func TestLiveHeldFrameIsRedrawnAtItsDeadline(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	// A synchronized output frame is open: the pane holds the last screen
	// and the model asks to be woken when the limit passes.
	f.start.Pane.Write([]byte("\x1b[2J\x1b[Hbefore"))
	f.emit("\x1b[?2026h\x1b[Hduring")
	r.pump()
	var delays []time.Duration
	for _, s := range r.sched {
		if _, ok := s.fn(r.t0).(holdMsg); ok {
			delays = append(delays, s.d)
		}
	}
	if len(delays) != 1 {
		t.Fatalf("redraws scheduled for the held frame: %v, timers %v", delays, r.sched)
	}
	// The delay is measured on the live clock, which the pane's hold limit
	// reads: at most the hold limit from now. On the model's own tick time,
	// which stays zero in live mode, it would be centuries.
	if d := delays[0]; d < 0 || d > sessionpane.MaxFrameHold {
		t.Fatalf("the held frame is redrawn after %v, want at most %v", d, sessionpane.MaxFrameHold)
	}
}

// TestLiveConcurrentFeedAndDraw runs the source's goroutine against the
// program's: the data race detector (the suite runs with -race) is what fails
// it on a race. The checks after it are for the result, which holds without
// the detector too: nothing the source wrote is lost or unbounded.
func TestLiveConcurrentFeedAndDraw(t *testing.T) {
	f := newFake(LiveWriter)
	r := newLiveRig(t, 140, 45, f, Options{})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var lines uint64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			f.start.Pane.Write([]byte(fmt.Sprintf("line %d\r\n", i)))
			f.set(func(s *LiveStatus) { s.Resyncs++ })
			f.notify(LiveNotice{Code: NoticeTooSlow})
			lines = uint64(i) + 1
		}
	}()
	for range 50 {
		// Deliver what arrives, without waiting for more.
		if !r.blocked() {
			r.pump()
		}
		_ = r.m.View()
	}
	close(stop)
	wg.Wait()
	if lines == 0 {
		t.Fatal("the source wrote nothing")
	}
	r.m.onLive()
	if got := r.m.live.st.Resyncs; got != lines {
		t.Fatalf("status shows %d resyncs, the source made %d", got, lines)
	}
	if n := len(r.m.live.notices); n != LiveNoticeLimit {
		t.Fatalf("%d notices kept", n)
	}
	if v := r.view(); !strings.Contains(v, fmt.Sprintf("line %d\n", lines-1)) && !strings.Contains(v, fmt.Sprintf("line %d ", lines-1)) {
		t.Fatalf("the last line (%d) is not shown:\n%s", lines-1, v)
	}
}

func TestLiveFixtureModeStaysTheFixture(t *testing.T) {
	r := newRig(t, 140, 45, Options{})
	v := r.view()
	if !strings.Contains(v, fixtureLabel) || !strings.Contains(v, "DEMO FIXTURE") || r.m.live.on() {
		t.Fatalf("fixture changed:\n%s", v)
	}
	if strings.Contains(v, "live scratch") {
		t.Fatal("live text in fixture mode")
	}
}

func ansiStrip(s string) string { return ansi.Strip(s) }
