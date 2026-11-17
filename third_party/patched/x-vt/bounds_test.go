package vt

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// A scroll margin past the last line or column of the screen made the
// scrolling operations and the cursor index past the buffer, and a session
// could crash the process with a few bytes. A region ends at the screen's last
// line, as in xterm.
func TestScrollMarginsStayInsideTheScreen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  string
		region uv.Rectangle
	}{
		{"bottom margin past the screen", "\x1b[3;99r", uv.Rect(0, 2, 10, 3)},
		{"bottom margin at the screen", "\x1b[3;5r", uv.Rect(0, 2, 10, 3)},
		{"default bottom margin", "\x1b[2r", uv.Rect(0, 1, 10, 4)},
		{"top margin below the screen is ignored", "\x1b[40;90r", uv.Rect(0, 0, 10, 5)},
		{"top margin on the last line", "\x1b[5;99r", uv.Rect(0, 4, 10, 1)},
		{"empty region is ignored", "\x1b[4;4r", uv.Rect(0, 0, 10, 5)},
		{"right margin past the screen", "\x1b[?69h\x1b[2;99s", uv.Rect(1, 0, 9, 5)},
		{"left margin past the screen is ignored", "\x1b[?69h\x1b[40;99s", uv.Rect(0, 0, 10, 5)},
		{"left margin on the last column", "\x1b[?69h\x1b[10;99s", uv.Rect(9, 0, 1, 5)},
		{"right margin at the screen", "\x1b[?69h\x1b[3;10s", uv.Rect(2, 0, 8, 5)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEmulator(10, 5)
			e.Write([]byte(tc.input))
			if got := e.scr.ScrollRegion(); got != tc.region {
				t.Fatalf("scroll region %v, want %v", got, tc.region)
			}
			// Every operation that uses the region.
			e.Write([]byte("\x1b[3;3H\x1b[3L\x1b[2M\x1b[2S\x1b[2T\x1bM\x1bM\x1bD\x1bD\x1bD\x1bD\x1bD\x1bD\x1bD"))
			e.Write([]byte("\x1b[?6h\x1b[40;40H\x1b[4@\x1b[4Pabc\x1b[2J\r\n\r\n\r\n\r\n\r\n\r\nxyz\x1b[?6l"))
			if pos := e.CursorPosition(); pos.X < 0 || pos.X >= 10 || pos.Y < 0 || pos.Y >= 5 {
				t.Errorf("the cursor left the screen: %v", pos)
			}
		})
	}
}

// A region that the cut leaves with one line (or one column), on the last
// line, is set and sends the cursor home. That is what the released module
// does, and four of its tests rely on it (CSI 2;3 r on a screen of two lines).
// It differs from xterm, which cuts the bottom margin first and ignores a
// region whose bottom is not below its top (reasoned from its rule for
// DECSTBM, not run), and it is not consistent inside this emulator, which
// ignores CSI 5;5 r because the margins as they were sent are not ordered.
// NOTICE.md, patch 10, says so; the test pins the behaviour so that a change
// to it is a decision.
func TestOneLineRegionOnTheLastLineIsSetUnlikeXterm(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  string
		region uv.Rectangle
		home   uv.Position
	}{
		{"CSI 5;99 r", "\x1b[5;99r", uv.Rect(0, 4, 10, 1), uv.Pos(0, 0)},
		{"CSI 5;6 r", "\x1b[5;6r", uv.Rect(0, 4, 10, 1), uv.Pos(0, 0)},
		{"CSI 5;5 r is ignored", "\x1b[5;5r", uv.Rect(0, 0, 10, 5), uv.Pos(4, 2)},
		{"CSI ? 69 h CSI 10;99 s", "\x1b[?69h\x1b[10;99s", uv.Rect(9, 0, 1, 5), uv.Pos(0, 0)},
		{"CSI ? 69 h CSI 10;10 s is ignored", "\x1b[?69h\x1b[10;10s", uv.Rect(0, 0, 10, 5), uv.Pos(4, 2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEmulator(10, 5)
			e.Write([]byte("\x1b[3;5H"))
			e.Write([]byte(tc.input))
			if got := e.scr.ScrollRegion(); got != tc.region {
				t.Errorf("scroll region %v, want %v", got, tc.region)
			}
			if got := e.CursorPosition(); got != tc.home {
				t.Errorf("cursor %v, want %v", got, tc.home)
			}
			e.Write([]byte("\x1b[3L\x1b[2M\x1b[2S\x1b[2T\x1bM\x1bD\x1bD\nabc\n\n"))
			if pos := e.CursorPosition(); pos.X < 0 || pos.X >= 10 || pos.Y < 0 || pos.Y >= 5 {
				t.Errorf("the cursor left the screen: %v", pos)
			}
		})
	}
}

// The cursor of the screen that is not active, and the saved cursors, were
// left where they were by a resize, so a smaller screen could be left with a
// cursor outside it; setting a tab stop or printing there indexed past the
// buffer.
func TestResizeKeepsEveryCursorInsideTheScreen(t *testing.T) {
	e := NewEmulator(40, 20)
	// A saved cursor on the main screen, then the alternate screen with its
	// own saved cursor, both far from the origin.
	e.Write([]byte("\x1b[18;38H\x1b7\x1b[?1049h\x1b[19;39H\x1b7\x1b[17;37H"))
	e.Resize(10, 5)
	for i := range e.scrs {
		for name, c := range map[string]Cursor{"cursor": e.scrs[i].cur, "saved cursor": e.scrs[i].saved} {
			if c.X < 0 || c.X >= 10 || c.Y < 0 || c.Y >= 5 {
				t.Errorf("screen %d: %s at %v after the resize", i, name, c.Position)
			}
		}
	}
	// Restore the saved cursors and use them, on both screens.
	e.Write([]byte("\x1b8\x1bHx\x1b[?1049l\x1b8\x1bHy\x1b[?1049h\x1b8\x1bHz"))
	e.Write([]byte("\x1b[?1049l\x1b8\x1b[g\x1b[3g\t\x1bH\ta"))
	if pos := e.CursorPosition(); pos.X < 0 || pos.X >= 10 || pos.Y < 0 || pos.Y >= 5 {
		t.Errorf("the cursor is outside the screen: %v", pos)
	}

	// Growing the screen moves nothing.
	e = NewEmulator(10, 5)
	e.Write([]byte("\x1b[4;9H\x1b7\x1b[?1049h\x1b[2;2H"))
	e.Resize(30, 20)
	if got := e.scrs[0].saved.Position; got != uv.Pos(8, 3) {
		t.Errorf("saved cursor moved to %v by a larger size", got)
	}
}
