package leadattach_test

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/leadattach"
	"ataniq/internal/terminalconsole/sessionpane"
)

func TestEncodeMouse(t *testing.T) {
	click := func(x, y int, b uv.MouseButton, m uv.KeyMod) uv.MouseEvent {
		return uv.MouseClickEvent{X: x, Y: y, Button: b, Mod: m}
	}
	release := func(x, y int, b uv.MouseButton) uv.MouseEvent {
		return uv.MouseReleaseEvent{X: x, Y: y, Button: b}
	}
	motion := func(x, y int, b uv.MouseButton) uv.MouseEvent {
		return uv.MouseMotionEvent{X: x, Y: y, Button: b}
	}
	wheel := func(x, y int, b uv.MouseButton) uv.MouseEvent {
		return uv.MouseWheelEvent{X: x, Y: y, Button: b}
	}
	sgr := func(dec ...int) leadattach.ModeSet { return modes(append(dec, 1006)...) }

	cases := []struct {
		name string
		ev   uv.MouseEvent
		m    leadattach.ModeSet
		want string
	}{
		// SGR.
		{"sgr left click", click(4, 2, uv.MouseLeft, 0), sgr(1000), "\x1b[<0;5;3M"},
		{"sgr middle click", click(0, 0, uv.MouseMiddle, 0), sgr(1000), "\x1b[<1;1;1M"},
		{"sgr right click", click(0, 0, uv.MouseRight, 0), sgr(1000), "\x1b[<2;1;1M"},
		{"sgr left release", release(4, 2, uv.MouseLeft), sgr(1000), "\x1b[<0;5;3m"},
		{"sgr right release", release(4, 2, uv.MouseRight), sgr(1002), "\x1b[<2;5;3m"},
		{"sgr release of no button is the left", release(4, 2, uv.MouseNone), sgr(1000), "\x1b[<0;5;3m"},
		{"sgr wheel up", wheel(0, 0, uv.MouseWheelUp), sgr(1000), "\x1b[<64;1;1M"},
		{"sgr wheel down", wheel(7, 9, uv.MouseWheelDown), sgr(1000), "\x1b[<65;8;10M"},
		{"sgr wheel left", wheel(0, 0, uv.MouseWheelLeft), sgr(1000), "\x1b[<66;1;1M"},
		{"sgr shift ctrl click", click(1, 1, uv.MouseLeft, uv.ModShift|uv.ModCtrl), sgr(1000), "\x1b[<20;2;2M"},
		{"sgr alt click", click(1, 1, uv.MouseLeft, uv.ModAlt), sgr(1000), "\x1b[<8;2;2M"},
		{"sgr a big coordinate", click(300, 150, uv.MouseLeft, 0), sgr(1000), "\x1b[<0;301;151M"},
		{"sgr drag", motion(3, 3, uv.MouseLeft), sgr(1002), "\x1b[<32;4;4M"},
		{"sgr motion with no button, any-motion", motion(3, 3, uv.MouseNone), sgr(1003), "\x1b[<35;4;4M"},
		{"sgr button 8", click(0, 0, uv.MouseBackward, 0), sgr(1000), "\x1b[<128;1;1M"},

		// Legacy.
		{"legacy left click", click(4, 2, uv.MouseLeft, 0), modes(1000), "\x1b[M \x25\x23"},
		{"legacy right click", click(0, 0, uv.MouseRight, 0), modes(1000), "\x1b[M\"!!"},
		{"legacy release has no button", release(4, 2, uv.MouseLeft), modes(1000), "\x1b[M#\x25\x23"},
		{"legacy wheel up", wheel(0, 0, uv.MouseWheelUp), modes(1000), "\x1b[M`!!"},
		{"legacy the last cell it can carry", click(222, 222, uv.MouseLeft, 0), modes(1000), "\x1b[M \xff\xff"},
		{"legacy one cell too far", click(223, 0, uv.MouseLeft, 0), modes(1000), ""},
		{"legacy a high button", click(0, 0, uv.MouseBackward, 0), modes(1000), "\x1b[M\xa0!!"},
		{"legacy ctrl click", click(0, 0, uv.MouseLeft, uv.ModCtrl), modes(1000), "\x1b[M0!!"},

		// What each tracking mode reports.
		{"no mode, no event", click(0, 0, uv.MouseLeft, 0), modes(25, 2004), ""},
		{"x10 click", click(4, 2, uv.MouseLeft, uv.ModCtrl), modes(9), "\x1b[M \x25\x23"},
		{"x10 wheel", wheel(0, 0, uv.MouseWheelUp), modes(9), "\x1b[M`!!"},
		{"x10 has no release", release(4, 2, uv.MouseLeft), modes(9), ""},
		{"x10 has no motion", motion(4, 2, uv.MouseLeft), modes(9), ""},
		{"x10 with sgr", click(4, 2, uv.MouseLeft, uv.ModShift), sgr(9), "\x1b[<0;5;3M"},
		{"normal has no motion", motion(4, 2, uv.MouseLeft), modes(1000), ""},
		{"highlight is click tracking", release(4, 2, uv.MouseLeft), modes(1001), "\x1b[M#\x25\x23"},
		{"drag has no motion without a button", motion(4, 2, uv.MouseNone), modes(1002), ""},
		{"drag with a button", motion(4, 2, uv.MouseLeft), modes(1002), "\x1b[M@\x25\x23"},
		{"any motion without a button", motion(4, 2, uv.MouseNone), modes(1003), "\x1b[MC\x25\x23"},
		{"the strongest mode wins", motion(4, 2, uv.MouseNone), modes(9, 1000, 1002, 1003), "\x1b[MC\x25\x23"},

		// Nothing to send.
		{"a negative cell", click(-1, 0, uv.MouseLeft, 0), sgr(1000), ""},
		{"nil", nil, sgr(1000), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := leadattach.EncodeMouse(c.ev, c.m)
			if string(got) != c.want || (c.want == "" && got != nil) {
				t.Errorf("EncodeMouse = %q, want %q", got, c.want)
			}
		})
	}
}

func TestModeSetModes(t *testing.T) {
	for _, c := range []struct {
		m    leadattach.ModeSet
		want sessionpane.Modes
	}{
		{leadattach.ModeSet{}, sessionpane.Modes{}},
		{modes(9), sessionpane.Modes{Mouse: uv.MouseModePress}},
		{modes(1000), sessionpane.Modes{Mouse: uv.MouseModeClick}},
		{modes(1001), sessionpane.Modes{Mouse: uv.MouseModeClick}},
		{modes(1002), sessionpane.Modes{Mouse: uv.MouseModeDrag}},
		{modes(1003), sessionpane.Modes{Mouse: uv.MouseModeMotion}},
		{modes(9, 1000, 1002), sessionpane.Modes{Mouse: uv.MouseModeDrag}},
		{modes(1006), sessionpane.Modes{MouseEncoding: uv.MouseEncodingSGR}},
		{modes(2004, 1004), sessionpane.Modes{BracketedPaste: true, FocusEvents: true}},
		{modes(1, 25, 66, 1049, 2026, 2027), sessionpane.Modes{}},
		{leadattach.ModeSet{ANSI: []int{1000, 2004}}, sessionpane.Modes{}},
	} {
		if got := c.m.Modes(); got != c.want {
			t.Errorf("%+v: %+v, want %+v", c.m, got, c.want)
		}
	}
}
