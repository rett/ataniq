package sessionpane_test

import (
	"fmt"
	"sync"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/terminalconsole/sessionpane"
)

// sink records the input a pane passes on.
type sink struct {
	mu  sync.Mutex
	got []string
}

func (s *sink) add(v string) {
	s.mu.Lock()
	s.got = append(s.got, v)
	s.mu.Unlock()
}

func (s *sink) Key(k uv.KeyPressEvent) { s.add("key " + k.String()) }
func (s *sink) Paste(text string)      { s.add(fmt.Sprintf("paste %q", text)) }
func (s *sink) Mouse(m uv.MouseEvent) {
	mm := m.Mouse()
	s.add(fmt.Sprintf("%T %d,%d %v %d", m, mm.X, mm.Y, mm.Button, mm.Mod))
}

func (s *sink) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	got := s.got
	s.got = nil
	return got
}

func click(x, y int) uv.MouseEvent {
	return uv.MouseClickEvent{X: x, Y: y, Button: uv.MouseLeft}
}

func TestMouseTranslationToPaneCells(t *testing.T) {
	var s sink
	p := newPane(t, 20, 5, sessionpane.Options{Input: &s})
	area := uv.Rect(10, 3, 20, 5)

	if p.Mouse(click(12, 4), area) {
		t.Fatal("a click passed before the session asked for the mouse")
	}
	p.Write([]byte("\x1b[?1000h"))
	for _, c := range []struct {
		ev   uv.MouseEvent
		sent bool
		want string
	}{
		{click(12, 4), true, "uv.MouseClickEvent 2,1 left 0"},
		{click(10, 3), true, "uv.MouseClickEvent 0,0 left 0"},
		{click(29, 7), true, "uv.MouseClickEvent 19,4 left 0"},
		{click(9, 4), false, ""},
		{click(30, 4), false, ""},
		{click(12, 2), false, ""},
		{click(12, 8), false, ""},
		{uv.MouseClickEvent{X: 15, Y: 5, Button: uv.MouseRight, Mod: uv.ModCtrl | uv.ModShift}, true, fmt.Sprintf("uv.MouseClickEvent 5,2 right %d", uv.ModCtrl|uv.ModShift)},
		{uv.MouseReleaseEvent{X: 12, Y: 4, Button: uv.MouseLeft}, true, "uv.MouseReleaseEvent 2,1 left 0"},
		{uv.MouseWheelEvent{X: 12, Y: 4, Button: uv.MouseWheelUp}, true, "uv.MouseWheelEvent 2,1 wheelup 0"},
		{uv.MouseMotionEvent{X: 12, Y: 4, Button: uv.MouseLeft}, false, ""},
		{nil, false, ""},
	} {
		if sent := p.Mouse(c.ev, area); sent != c.sent {
			t.Errorf("%v: sent %v, want %v", c.ev, sent, c.sent)
		}
		got := s.take()
		if c.sent && (len(got) != 1 || got[0] != c.want) {
			t.Errorf("%v: sink got %q, want %q", c.ev, got, c.want)
		}
		if !c.sent && len(got) != 0 {
			t.Errorf("%v: sink got %q", c.ev, got)
		}
	}
}

func TestMouseTranslationFollowsTheMouseMode(t *testing.T) {
	press := click(1, 1)
	release := uv.MouseReleaseEvent{X: 1, Y: 1, Button: uv.MouseLeft}
	wheel := uv.MouseWheelEvent{X: 1, Y: 1, Button: uv.MouseWheelDown}
	drag := uv.MouseMotionEvent{X: 1, Y: 1, Button: uv.MouseLeft}
	move := uv.MouseMotionEvent{X: 1, Y: 1}
	for _, c := range []struct {
		seq  string
		want []bool // press, release, wheel, drag, move
	}{
		{"", []bool{false, false, false, false, false}},
		{"\x1b[?9h", []bool{true, false, true, false, false}},
		{"\x1b[?1000h", []bool{true, true, true, false, false}},
		{"\x1b[?1001h", []bool{true, true, true, false, false}},
		{"\x1b[?1002h", []bool{true, true, true, true, false}},
		{"\x1b[?1003h", []bool{true, true, true, true, true}},
		{"\x1b[?1003h\x1b[?1003l", []bool{false, false, false, false, false}},
	} {
		p := newPane(t, 10, 3, sessionpane.Options{})
		p.Write([]byte(c.seq))
		for i, ev := range []uv.MouseEvent{press, release, wheel, drag, move} {
			if got := p.Mouse(ev, uv.Rect(0, 0, 10, 3)); got != c.want[i] {
				t.Errorf("%q, %T %v: sent %v, want %v", c.seq, ev, ev, got, c.want[i])
			}
		}
	}
}

func TestMouseTranslationInsideTheCoveredPart(t *testing.T) {
	p := newPane(t, 10, 4, sessionpane.Options{})
	p.Write([]byte("\x1b[?1000h"))
	// The area cuts the pane to 6x2: a click beyond the cut is dropped,
	// though it is inside the pane.
	cut := uv.Rect(5, 5, 6, 2)
	if !p.Mouse(click(10, 6), cut) || p.Mouse(click(11, 6), cut) || p.Mouse(click(10, 7), cut) {
		t.Fatal("cut area")
	}
	// The area is larger than the pane: its blank part takes no clicks.
	zoom := uv.Rect(0, 0, 40, 20)
	if !p.Mouse(click(9, 3), zoom) || p.Mouse(click(10, 3), zoom) || p.Mouse(click(9, 4), zoom) {
		t.Fatal("zoomed area")
	}
}

func TestMouseTranslationInTheModesFixture(t *testing.T) {
	rec := fixture(t, "modes.jsonl")
	var s sink
	p := newPane(t, rec.Cols, rec.Rows, sessionpane.Options{Input: &s})
	move := uv.MouseMotionEvent{X: 3, Y: 2}
	for i, e := range rec.Events {
		apply(p, e)
		// Motion without a button passes from the event that completes mode
		// 1003 until the event that resets the mouse modes.
		want := i >= 5 && i < 12
		if got := p.Mouse(move, uv.Rect(0, 0, rec.Cols, rec.Rows)); got != want {
			t.Errorf("after event %d: motion sent %v, want %v", i, got, want)
		}
	}
	if got := s.take(); len(got) != 7 || got[0] != "uv.MouseMotionEvent 3,2 none 0" {
		t.Fatalf("sink got %q", got)
	}
}

func TestMouseTranslationSinkGetsKeysAndPaste(t *testing.T) {
	var s sink
	p := newPane(t, 10, 3, sessionpane.Options{Input: &s})
	p.Key(uv.KeyPressEvent{Code: 'q', Text: "q"})
	p.Key(uv.KeyPressEvent{Code: 'c', Mod: uv.ModCtrl})
	p.Paste("pasted\ntext")
	want := []string{"key q", "key ctrl+c", `paste "pasted\ntext"`}
	if got := s.take(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("sink got %q, want %q", got, want)
	}

	// Without a sink, input is discarded.
	q := newPane(t, 10, 3, sessionpane.Options{})
	q.Write([]byte("\x1b[?1000h"))
	q.Key(uv.KeyPressEvent{Code: 'x'})
	q.Paste("x")
	if !q.Mouse(click(1, 1), uv.Rect(0, 0, 10, 3)) {
		t.Fatal("a click the session asked for was not sent to the discarding sink")
	}
}

func TestEndedDropsInput(t *testing.T) {
	var s sink
	p := newPane(t, 10, 3, sessionpane.Options{Input: &s})
	p.Write([]byte("\x1b[?1003h"))
	p.End(0, true)
	p.Key(uv.KeyPressEvent{Code: 'x'})
	p.Paste("x")
	if p.Mouse(click(1, 1), uv.Rect(0, 0, 10, 3)) {
		t.Fatal("mouse sent after End")
	}
	if got := s.take(); len(got) != 0 {
		t.Fatalf("sink got %q after End", got)
	}

	q := sessionpane.New(10, 3, sessionpane.Options{Input: &s})
	q.Write([]byte("\x1b[?1003h"))
	q.Close()
	q.Key(uv.KeyPressEvent{Code: 'x'})
	q.Paste("x")
	if q.Mouse(click(1, 1), uv.Rect(0, 0, 10, 3)) {
		t.Fatal("mouse sent after Close")
	}
	if got := s.take(); len(got) != 0 {
		t.Fatalf("sink got %q after Close", got)
	}
}
