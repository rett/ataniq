package sessionpane_test

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/terminalconsole/sessionpane"
)

// clock is an injected clock that moves only when the test moves it.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *clock { return &clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// screen is what a pane shows: its rows as drawn, and its cursor.
func screen(p *sessionpane.Pane) string {
	buf := draw(p)
	rows := make([]string, buf.Height())
	for y := range rows {
		rows[y] = row(buf, y)
	}
	pos, visible := p.Cursor()
	return fmt.Sprintf("%s\ncursor %d,%d %v", strings.TrimRight(strings.Join(rows, "\n"), "\n"), pos.X, pos.Y, visible)
}

func TestSyncFrameFixtureTimeline(t *testing.T) {
	rec := fixture(t, "sync-frames.jsonl")
	c := newClock()
	start := c.Now()
	p := newPane(t, rec.Cols, rec.Rows, sessionpane.Options{Clock: c.Now})
	want := []struct{ row2, row3 string }{
		{"", ""}, // frame test
		{"", ""}, // frame one opened: the screen before it
		{"frame one, line a", "frame one, line b"},     // frame one closed
		{"frame two, line a", "frame two, line b"},     // two closed, three opened in one chunk
		{"frame two, line a", "frame two, line b"},     // three still open, its reset cut
		{"frame three, line a", "frame three, line b"}, // reset completed
		{"multi frame 3", "frame three, line b"},       // three whole frames in one chunk
		{"multi frame 5", "frame three, line b"},       // two more
		{"multi frame 5", "frame three, line b"},       // held frame opened
		// Closed after 240 ms. Line b overwrites a longer line without
		// erasing it, so the last letter of that line stays.
		{"held frame, line a", "held frame, line bb"},
		{"held frame, line a", "held frame, line bb"}, // end
	}
	if len(rec.Events) != len(want) {
		t.Fatalf("fixture has %d events, the test expects %d", len(rec.Events), len(want))
	}
	for i, e := range rec.Events {
		c.advance(start.Add(e.At).Sub(c.Now()))
		apply(p, e)
		buf := draw(p)
		if got := row(buf, 0); got != "frame test" {
			t.Errorf("after event %d: row 0 %q", i, got)
		}
		if r2, r3 := row(buf, 2), row(buf, 3); r2 != want[i].row2 || r3 != want[i].row3 {
			t.Errorf("after event %d: rows %q, %q; want %q, %q", i, r2, r3, want[i].row2, want[i].row3)
		}
		if i == 8 {
			// The held frame times out: shown for 149 ms, not at 150.
			if d, ok := p.HoldDeadline(); !ok || !d.Equal(c.Now().Add(sessionpane.MaxFrameHold)) {
				t.Errorf("HoldDeadline() = %v, %v", d, ok)
			}
			c.advance(sessionpane.MaxFrameHold - time.Millisecond)
			if got := row(draw(p), 2); got != "multi frame 5" {
				t.Errorf("at 149 ms: row 2 %q", got)
			}
			c.advance(time.Millisecond)
			if got := row(draw(p), 2); got != "held frame, line a" {
				t.Errorf("at 150 ms: row 2 %q", got)
			}
			if _, ok := p.HoldDeadline(); ok {
				t.Error("a deadline after the time limit")
			}
		}
	}
}

// syncFrameMarks returns, for out, the end offsets of every literal set and
// reset of mode 2026.
func syncFrameMarks(out []byte) (sets, resets []int) {
	for i := 0; i < len(out); i++ {
		if bytes.HasPrefix(out[i:], []byte("\x1b[?2026h")) {
			sets = append(sets, i+8)
		}
		if bytes.HasPrefix(out[i:], []byte("\x1b[?2026l")) {
			resets = append(resets, i+8)
		}
	}
	return sets, resets
}

// complete returns how much of out had been written when the frame shown
// after out[:k] was complete: k itself, or the start of the set of mode
// 2026 whose frame is still open at k.
func complete(out []byte, sets, resets []int, k int) int {
	open := -1
	for _, s := range sets {
		if s <= k {
			open = s
		}
	}
	for _, r := range resets {
		if r <= k && r > open {
			open = -1
		}
	}
	if open < 0 {
		return k
	}
	return open - 8
}

// reference returns what a pane shows after b is written in one piece, with
// no synchronized output: the screen as it is.
func reference(t *testing.T, cols, rows int, b []byte) string {
	t.Helper()
	p := sessionpane.New(cols, rows, sessionpane.Options{Clock: newClock().Now})
	defer p.Close()
	p.Write(b)
	return screen(p)
}

func TestSyncFrameSplitAtEveryByte(t *testing.T) {
	rec := fixture(t, "sync-frames.jsonl")
	out := output(rec)
	sets, resets := syncFrameMarks(out)
	if len(sets) != 9 || len(resets) != 9 {
		t.Fatalf("fixture changed: %d sets, %d resets", len(sets), len(resets))
	}
	final := reference(t, rec.Cols, rec.Rows, out)
	refs := map[int]string{}
	ref := func(j int) string {
		if s, ok := refs[j]; ok {
			return s
		}
		refs[j] = reference(t, rec.Cols, rec.Rows, out[:j])
		return refs[j]
	}
	for k := 0; k <= len(out); k++ {
		// The clock never moves, so no frame times out.
		p := sessionpane.New(rec.Cols, rec.Rows, sessionpane.Options{Clock: newClock().Now})
		p.Write(out[:k])
		j := complete(out, sets, resets, k)
		if got, want := screen(p), ref(j); got != want {
			p.Close()
			t.Fatalf("split at %d (frame complete at %d):\n%s\nwant:\n%s", k, j, got, want)
		}
		p.Write(out[k:])
		got := screen(p)
		p.Close()
		if got != final {
			t.Fatalf("split at %d, after the rest:\n%s\nwant:\n%s", k, got, final)
		}
	}
}

func TestSyncFrameWholeMultiFrameChunks(t *testing.T) {
	frame := func(n int) string {
		return fmt.Sprintf("\x1b[?2026h\x1b[1;1H\x1b[2Kframe %d top\r\n\x1b[2Kframe %d bottom\x1b[?2026l", n, n)
	}
	c := newClock()
	p := newPane(t, 30, 4, sessionpane.Options{Clock: c.Now})
	// Three whole frames and the start of a fourth, in one chunk: the third is
	// shown, with its cursor.
	p.Write([]byte(frame(1) + frame(2) + frame(3) + "\x1b[?2026h\x1b[1;1H\x1b[2Kframe 4 top\r\n\x1b[2K\x1b[?25l"))
	buf := draw(p)
	if r0, r1 := row(buf, 0), row(buf, 1); r0 != "frame 3 top" || r1 != "frame 3 bottom" {
		t.Fatalf("rows %q, %q", r0, r1)
	}
	if pos, visible := p.Cursor(); pos != uv.Pos(14, 1) || !visible {
		t.Fatalf("cursor %v visible %v, want the third frame's", pos, visible)
	}
	// The rest of the fourth frame and a whole fifth: the fifth is shown.
	p.Write([]byte("frame 4 bottom\x1b[?2026l" + frame(5)))
	buf = draw(p)
	if r0, r1 := row(buf, 0), row(buf, 1); r0 != "frame 5 top" || r1 != "frame 5 bottom" {
		t.Fatalf("rows %q, %q", r0, r1)
	}
	if _, visible := p.Cursor(); visible {
		t.Fatal("the cursor the fourth frame hid is shown")
	}
}

func TestSyncFrameWholeFixtureInOneChunk(t *testing.T) {
	rec := fixture(t, "sync-frames.jsonl")
	out := output(rec)
	p := newPane(t, rec.Cols, rec.Rows, sessionpane.Options{Clock: newClock().Now})
	p.Write(out)
	buf := draw(p)
	if r2, r3 := row(buf, 2), row(buf, 3); r2 != "held frame, line a" || r3 != "held frame, line bb" {
		t.Fatalf("rows %q, %q", r2, r3)
	}
	if _, ok := p.HoldDeadline(); ok {
		t.Fatal("holding a frame after the last reset")
	}
}

func TestSyncFrameResetAndResize(t *testing.T) {
	c := newClock()
	p := newPane(t, 20, 3, sessionpane.Options{Clock: c.Now})
	p.Write([]byte("before\x1b[?2026h\rduring"))
	if got := row(draw(p), 0); got != "before" {
		t.Fatalf("held: %q", got)
	}
	p.Write([]byte("\x1b[?2026l"))
	if got := row(draw(p), 0); got != "during" {
		t.Fatalf("after the reset: %q", got)
	}

	p.Write([]byte("\x1b[?2026h\rresized"))
	if got := row(draw(p), 0); got != "during" {
		t.Fatalf("held again: %q", got)
	}
	p.Resize(30, 3)
	if got := row(draw(p), 0); got != "resized" {
		t.Fatalf("a held frame survived a resize: %q", got)
	}
	if _, ok := p.HoldDeadline(); ok {
		t.Fatal("a deadline after a resize")
	}
}

// TestSyncFrameRepeatedSetKeepsTheFrameAndTheLimit sets mode 2026 again
// while a frame is open. That is no new frame: the pane keeps showing the
// frame from the first set, and the time limit still counts from it, so a
// session that repeats the set cannot hold a half-drawn screen back forever.
func TestSyncFrameRepeatedSetKeepsTheFrameAndTheLimit(t *testing.T) {
	c := newClock()
	start := c.Now()
	p := newPane(t, 10, 1, sessionpane.Options{Clock: c.Now})
	p.Write([]byte("A\x1b[?2026h\rB"))
	c.advance(100 * time.Millisecond)
	p.Write([]byte("\x1b[?2026h\rC"))
	if got := row(draw(p), 0); got != "A" {
		t.Fatalf("after the second set: %q, want the first frame", got)
	}
	if d, ok := p.HoldDeadline(); !ok || !d.Equal(start.Add(sessionpane.MaxFrameHold)) {
		t.Fatalf("HoldDeadline() = %v, %v; want the first set's", d, ok)
	}
	c.advance(sessionpane.MaxFrameHold - 100*time.Millisecond - time.Millisecond)
	if got := row(draw(p), 0); got != "A" {
		t.Fatalf("at 149 ms: %q", got)
	}
	c.advance(time.Millisecond)
	if got := row(draw(p), 0); got != "C" {
		t.Fatalf("at 150 ms: %q, want the screen as it is", got)
	}
	if _, ok := p.HoldDeadline(); ok {
		t.Fatal("a deadline after the time limit")
	}
}

// TestSyncFrameModeReport checks the answer to DECRQM for mode 2026: reset
// before the first set, so that a session that asks first will use it, then
// set or reset as the session left it. Other modes are still answered by
// x/vt.
func TestSyncFrameModeReport(t *testing.T) {
	const query = "\x1b[?2026$p"
	var r replies
	p := newPane(t, 20, 3, sessionpane.Options{Replies: r.add, Clock: newClock().Now})
	for _, c := range []struct{ write, want string }{
		{"", "\x1b[?2026;2$y"},
		{"\x1b[?2026h", "\x1b[?2026;1$y"},
		{"\x1b[?2026h", "\x1b[?2026;1$y"},
		{"\x1b[?2026l", "\x1b[?2026;2$y"},
		{"\x1b[?2026h\x1bc", "\x1b[?2026;2$y"},
		{"\x1b[?1049;2026h", "\x1b[?2026;1$y"},
		{"\x1b[?2026;1049l", "\x1b[?2026;2$y"},
	} {
		r.reset()
		p.Write([]byte(c.write + query + "\x1b[6n"))
		r.await(t, "R")
		if got := r.String(); got != c.want+"\x1b[1;1R" {
			t.Fatalf("after %q: replies %q, want %q", c.write, got, c.want)
		}
	}
	// Split anywhere, the query is answered once.
	q := []byte("\x1b[?2026h" + query)
	for k := 0; k <= len(q); k++ {
		r.reset()
		p.Write(q[:k])
		p.Write(q[k:])
		p.Write([]byte("\x1b[?2026l\x1b[6n"))
		r.await(t, "R")
		if got := r.String(); got != "\x1b[?2026;1$y\x1b[1;1R" {
			t.Fatalf("split at %d: replies %q", k, got)
		}
	}
	r.reset()
	p.Write([]byte("\x1b[?2004$p\x1b[?1000h\x1b[?1000$p\x1b[2026$p\x1b[?9999$p\x1b[6n"))
	r.await(t, "R")
	if got, want := r.String(), "\x1b[?2004;2$y\x1b[?1000;1$y\x1b[2026;0$y\x1b[?9999;0$y\x1b[1;1R"; got != want {
		t.Fatalf("other modes: replies %q, want %q", got, want)
	}
}

func TestSyncFrameFullResetEndsIt(t *testing.T) {
	p := newPane(t, 20, 3, sessionpane.Options{Clock: newClock().Now})
	p.Write([]byte("before\x1b[?2026h\rduring\x1bcafter"))
	if got := row(draw(p), 0); got != "after" {
		t.Fatalf("after a full reset: %q", got)
	}
	if _, ok := p.HoldDeadline(); ok {
		t.Fatal("holding a frame after a full reset")
	}
}

func TestSyncFrameEndKeepsTheHeldFrame(t *testing.T) {
	c := newClock()
	p := newPane(t, 20, 3, sessionpane.Options{Clock: c.Now})
	p.Write([]byte("complete\x1b[?2026h\rtorn"))
	p.End(0, false)
	c.advance(time.Hour)
	if got := row(draw(p), 0); got != "complete" {
		t.Fatalf("ended screen %q", got)
	}
	if _, ok := p.HoldDeadline(); ok {
		t.Fatal("a deadline on the ended screen")
	}

	// A frame that had already timed out is not brought back by the end.
	p = newPane(t, 20, 3, sessionpane.Options{Clock: c.Now})
	p.Write([]byte("complete\x1b[?2026h\r\x1b[2Klater"))
	c.advance(sessionpane.MaxFrameHold)
	p.End(0, false)
	if got := row(draw(p), 0); got != "later" {
		t.Fatalf("ended screen %q", got)
	}
}

// TestSyncFrameMalformedReset covers the one case where a literal reset is
// not a reset: an ESC inside a UTF-8 character is taken as part of it. The
// pane then holds no frame and shows the screen as it is.
func TestSyncFrameMalformedReset(t *testing.T) {
	p := newPane(t, 30, 3, sessionpane.Options{Clock: newClock().Now})
	p.Write([]byte("before\x1b[?2026h\rpartial\xe2\x1b[?2026l"))
	if got := row(draw(p), 0); !strings.HasPrefix(got, "partial") {
		t.Fatalf("row %q", got)
	}
	if _, ok := p.HoldDeadline(); ok {
		t.Fatal("holding a frame it never copied")
	}
	p.Write([]byte("\x1b[?2026l\x1b[?2026h\x1b[2J"))
	if got := row(draw(p), 0); !strings.HasPrefix(got, "partial") {
		t.Fatalf("a later frame is not held: %q", got)
	}
}
