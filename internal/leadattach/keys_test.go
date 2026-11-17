package leadattach_test

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/leadattach"
)

func key(code rune, mod uv.KeyMod, text string) uv.KeyPressEvent {
	return uv.KeyPressEvent{Code: code, Mod: mod, Text: text}
}

var keyA = key('a', 0, "a")

const (
	shift = uv.ModShift
	alt   = uv.ModAlt
	ctrl  = uv.ModCtrl
)

func modes(dec ...int) leadattach.ModeSet { return leadattach.ModeSet{DEC: dec} }

func TestEncodeKey(t *testing.T) {
	appCursor := modes(1)
	appKeypad := modes(66)
	bkm := modes(67)
	lnm := leadattach.ModeSet{ANSI: []int{20}}
	none := leadattach.ModeSet{}
	// Modes that do not change a key: the mouse and paste modes, the alternate
	// screen, synchronized output.
	other := modes(25, 1000, 1006, 1049, 2004, 2026)

	cases := []struct {
		name string
		k    uv.KeyPressEvent
		m    leadattach.ModeSet
		o    leadattach.KeyOptions
		want string // "" means no encoding
	}{
		// Printable keys.
		{"a", key('a', 0, "a"), none, leadattach.KeyOptions{}, "a"},
		{"shift+a with text", key('a', shift, "A"), none, leadattach.KeyOptions{}, "A"},
		{"shift+a without text", key('a', shift, ""), none, leadattach.KeyOptions{}, "A"},
		{"shifted code", uv.KeyPressEvent{Code: '1', ShiftedCode: '!', Mod: shift}, none, leadattach.KeyOptions{}, "!"},
		{"shift+1 with text", key('1', shift, "!"), none, leadattach.KeyOptions{}, "!"},
		{"a CJK word", key(uv.KeyExtended, 0, "漢字"), none, leadattach.KeyOptions{}, "漢字"},
		{"an emoji", key('✅', 0, "✅"), none, leadattach.KeyOptions{}, "✅"},
		{"space", key(uv.KeySpace, 0, " "), none, leadattach.KeyOptions{}, " "},
		{"shift+space", key(uv.KeySpace, shift, " "), none, leadattach.KeyOptions{}, " "},
		{"alt+a", key('a', alt, ""), none, leadattach.KeyOptions{}, "\x1ba"},
		{"alt+shift+a", key('a', alt|shift, "A"), none, leadattach.KeyOptions{}, "\x1bA"},
		{"meta+a", key('a', uv.ModMeta, ""), none, leadattach.KeyOptions{}, "\x1ba"},
		{"text with an escape is dropped", key('a', 0, "a\x1b[31m"), none, leadattach.KeyOptions{}, ""},
		{"invalid utf-8 is dropped", key('a', 0, "\xff"), none, leadattach.KeyOptions{}, ""},
		{"a control code without text is dropped", key(0x01, 0, ""), none, leadattach.KeyOptions{}, ""},
		{"caps lock does not matter", key('a', uv.ModCapsLock, "a"), none, leadattach.KeyOptions{}, "a"},

		// Control keys.
		{"ctrl+a", key('a', ctrl, ""), none, leadattach.KeyOptions{}, "\x01"},
		{"ctrl+c", key('c', ctrl, ""), none, leadattach.KeyOptions{}, "\x03"},
		{"ctrl+d", key('d', ctrl, ""), none, leadattach.KeyOptions{}, "\x04"},
		{"ctrl+j", key('j', ctrl, ""), none, leadattach.KeyOptions{}, "\n"},
		{"ctrl+z is its byte", key('z', ctrl, ""), none, leadattach.KeyOptions{}, "\x1a"},
		{"ctrl+z even with a CSI u mode", key('z', ctrl, ""), modes(2026, 9001), leadattach.KeyOptions{ShiftEnter: leadattach.ShiftEnterCSIu}, "\x1a"},
		{"ctrl+shift+c is ctrl+c", key('c', ctrl|shift, ""), none, leadattach.KeyOptions{}, "\x03"},
		{"ctrl+upper-case C", key('C', ctrl, ""), none, leadattach.KeyOptions{}, "\x03"},
		{"ctrl+alt+a", key('a', ctrl|alt, ""), none, leadattach.KeyOptions{}, "\x1b\x01"},
		{"ctrl+space", key(uv.KeySpace, ctrl, ""), none, leadattach.KeyOptions{}, "\x00"},
		{"ctrl+@", key('@', ctrl, ""), none, leadattach.KeyOptions{}, "\x00"},
		{"ctrl+[", key('[', ctrl, ""), none, leadattach.KeyOptions{}, "\x1b"},
		{"ctrl+backslash", key('\\', ctrl, ""), none, leadattach.KeyOptions{}, "\x1c"},
		{"ctrl+]", key(']', ctrl, ""), none, leadattach.KeyOptions{}, "\x1d"},
		{"ctrl+^", key('^', ctrl, ""), none, leadattach.KeyOptions{}, "\x1e"},
		{"ctrl+_", key('_', ctrl, ""), none, leadattach.KeyOptions{}, "\x1f"},
		{"ctrl+/", key('/', ctrl, ""), none, leadattach.KeyOptions{}, "\x1f"},
		{"ctrl+?", key('?', ctrl, ""), none, leadattach.KeyOptions{}, "\x7f"},
		{"ctrl+2", key('2', ctrl, ""), none, leadattach.KeyOptions{}, "\x00"},
		{"ctrl+8", key('8', ctrl, ""), none, leadattach.KeyOptions{}, "\x7f"},
		{"ctrl+1 has no legacy form", key('1', ctrl, ""), none, leadattach.KeyOptions{}, ""},
		{"ctrl+comma has no legacy form", key(',', ctrl, ""), none, leadattach.KeyOptions{}, ""},
		{"ctrl with a CJK text has none", key(uv.KeyExtended, ctrl, "漢"), none, leadattach.KeyOptions{}, ""},

		// Enter, Tab, Backspace, Escape.
		{"enter", key(uv.KeyEnter, 0, ""), none, leadattach.KeyOptions{}, "\r"},
		{"enter in newline mode", key(uv.KeyEnter, 0, ""), lnm, leadattach.KeyOptions{}, "\r\n"},
		{"alt+enter", key(uv.KeyEnter, alt, ""), none, leadattach.KeyOptions{}, "\x1b\r"},
		{"ctrl+enter is enter", key(uv.KeyEnter, ctrl, ""), none, leadattach.KeyOptions{}, "\r"},
		{"shift+enter, default", key(uv.KeyEnter, shift, ""), none, leadattach.KeyOptions{}, "\x1b\r"},
		{"shift+enter, esc-cr", key(uv.KeyEnter, shift, ""), none, leadattach.KeyOptions{ShiftEnter: leadattach.ShiftEnterESCCR}, "\x1b\r"},
		{"shift+enter, lf", key(uv.KeyEnter, shift, ""), none, leadattach.KeyOptions{ShiftEnter: leadattach.ShiftEnterLF}, "\n"},
		{"shift+enter, csi u", key(uv.KeyEnter, shift, ""), none, leadattach.KeyOptions{ShiftEnter: leadattach.ShiftEnterCSIu}, "\x1b[13;2u"},
		{"shift+enter, backslash cr", key(uv.KeyEnter, shift, ""), none, leadattach.KeyOptions{ShiftEnter: leadattach.ShiftEnterBackslashCR}, "\\\r"},
		{"shift+alt+enter is alt+enter", key(uv.KeyEnter, shift|alt, ""), none, leadattach.KeyOptions{ShiftEnter: leadattach.ShiftEnterLF}, "\x1b\r"},
		{"tab", key(uv.KeyTab, 0, ""), none, leadattach.KeyOptions{}, "\t"},
		{"shift+tab", key(uv.KeyTab, shift, ""), none, leadattach.KeyOptions{}, "\x1b[Z"},
		{"alt+tab", key(uv.KeyTab, alt, ""), none, leadattach.KeyOptions{}, "\x1b\t"},
		{"ctrl+tab is tab", key(uv.KeyTab, ctrl, ""), none, leadattach.KeyOptions{}, "\t"},
		{"backspace", key(uv.KeyBackspace, 0, ""), none, leadattach.KeyOptions{}, "\x7f"},
		{"backspace with DECBKM", key(uv.KeyBackspace, 0, ""), bkm, leadattach.KeyOptions{}, "\x08"},
		{"ctrl+backspace", key(uv.KeyBackspace, ctrl, ""), none, leadattach.KeyOptions{}, "\x08"},
		{"ctrl+backspace with DECBKM", key(uv.KeyBackspace, ctrl, ""), bkm, leadattach.KeyOptions{}, "\x7f"},
		{"alt+backspace", key(uv.KeyBackspace, alt, ""), none, leadattach.KeyOptions{}, "\x1b\x7f"},
		{"escape", key(uv.KeyEscape, 0, ""), none, leadattach.KeyOptions{}, "\x1b"},
		{"alt+escape", key(uv.KeyEscape, alt, ""), none, leadattach.KeyOptions{}, "\x1b\x1b"},

		// Cursor keys, by mode and modifier.
		{"up", key(uv.KeyUp, 0, ""), none, leadattach.KeyOptions{}, "\x1b[A"},
		{"down", key(uv.KeyDown, 0, ""), none, leadattach.KeyOptions{}, "\x1b[B"},
		{"right", key(uv.KeyRight, 0, ""), none, leadattach.KeyOptions{}, "\x1b[C"},
		{"left", key(uv.KeyLeft, 0, ""), none, leadattach.KeyOptions{}, "\x1b[D"},
		{"up, other modes on", key(uv.KeyUp, 0, ""), other, leadattach.KeyOptions{}, "\x1b[A"},
		{"up, application cursor keys", key(uv.KeyUp, 0, ""), appCursor, leadattach.KeyOptions{}, "\x1bOA"},
		{"down, application cursor keys", key(uv.KeyDown, 0, ""), appCursor, leadattach.KeyOptions{}, "\x1bOB"},
		{"right, application cursor keys", key(uv.KeyRight, 0, ""), appCursor, leadattach.KeyOptions{}, "\x1bOC"},
		{"left, application cursor keys", key(uv.KeyLeft, 0, ""), appCursor, leadattach.KeyOptions{}, "\x1bOD"},
		{"home", key(uv.KeyHome, 0, ""), none, leadattach.KeyOptions{}, "\x1b[H"},
		{"end", key(uv.KeyEnd, 0, ""), none, leadattach.KeyOptions{}, "\x1b[F"},
		{"home, application cursor keys", key(uv.KeyHome, 0, ""), appCursor, leadattach.KeyOptions{}, "\x1bOH"},
		{"end, application cursor keys", key(uv.KeyEnd, 0, ""), appCursor, leadattach.KeyOptions{}, "\x1bOF"},
		{"shift+up", key(uv.KeyUp, shift, ""), none, leadattach.KeyOptions{}, "\x1b[1;2A"},
		{"alt+left", key(uv.KeyLeft, alt, ""), none, leadattach.KeyOptions{}, "\x1b[1;3D"},
		{"alt+shift+left", key(uv.KeyLeft, alt|shift, ""), none, leadattach.KeyOptions{}, "\x1b[1;4D"},
		{"ctrl+right", key(uv.KeyRight, ctrl, ""), none, leadattach.KeyOptions{}, "\x1b[1;5C"},
		{"ctrl+shift+right", key(uv.KeyRight, ctrl|shift, ""), none, leadattach.KeyOptions{}, "\x1b[1;6C"},
		{"ctrl+alt+up", key(uv.KeyUp, ctrl|alt, ""), none, leadattach.KeyOptions{}, "\x1b[1;7A"},
		{"ctrl+down, application cursor keys", key(uv.KeyDown, ctrl, ""), appCursor, leadattach.KeyOptions{}, "\x1b[1;5B"},
		{"ctrl+home", key(uv.KeyHome, ctrl, ""), none, leadattach.KeyOptions{}, "\x1b[1;5H"},
		{"shift+end, application cursor keys", key(uv.KeyEnd, shift, ""), appCursor, leadattach.KeyOptions{}, "\x1b[1;2F"},
		{"keypad up is up", key(uv.KeyKpUp, 0, ""), none, leadattach.KeyOptions{}, "\x1b[A"},
		{"keypad home is home", key(uv.KeyKpHome, 0, ""), appCursor, leadattach.KeyOptions{}, "\x1bOH"},
		{"keypad page down is page down", key(uv.KeyKpPgDown, 0, ""), none, leadattach.KeyOptions{}, "\x1b[6~"},
		{"keypad begin", key(uv.KeyKpBegin, 0, ""), none, leadattach.KeyOptions{}, "\x1b[E"},

		// Editing keys.
		{"insert", key(uv.KeyInsert, 0, ""), none, leadattach.KeyOptions{}, "\x1b[2~"},
		{"delete", key(uv.KeyDelete, 0, ""), none, leadattach.KeyOptions{}, "\x1b[3~"},
		{"page up", key(uv.KeyPgUp, 0, ""), none, leadattach.KeyOptions{}, "\x1b[5~"},
		{"page down", key(uv.KeyPgDown, 0, ""), none, leadattach.KeyOptions{}, "\x1b[6~"},
		{"delete, application cursor keys", key(uv.KeyDelete, 0, ""), appCursor, leadattach.KeyOptions{}, "\x1b[3~"},
		{"ctrl+delete", key(uv.KeyDelete, ctrl, ""), none, leadattach.KeyOptions{}, "\x1b[3;5~"},
		{"shift+page up", key(uv.KeyPgUp, shift, ""), none, leadattach.KeyOptions{}, "\x1b[5;2~"},
		{"alt+page down", key(uv.KeyPgDown, alt, ""), none, leadattach.KeyOptions{}, "\x1b[6;3~"},

		// Function keys.
		{"f1", key(uv.KeyF1, 0, ""), none, leadattach.KeyOptions{}, "\x1bOP"},
		{"f2", key(uv.KeyF2, 0, ""), none, leadattach.KeyOptions{}, "\x1bOQ"},
		{"f3", key(uv.KeyF3, 0, ""), none, leadattach.KeyOptions{}, "\x1bOR"},
		{"f4", key(uv.KeyF4, 0, ""), none, leadattach.KeyOptions{}, "\x1bOS"},
		{"f1, application cursor keys", key(uv.KeyF1, 0, ""), appCursor, leadattach.KeyOptions{}, "\x1bOP"},
		{"shift+f1", key(uv.KeyF1, shift, ""), none, leadattach.KeyOptions{}, "\x1b[1;2P"},
		{"ctrl+f4", key(uv.KeyF4, ctrl, ""), none, leadattach.KeyOptions{}, "\x1b[1;5S"},
		{"f5", key(uv.KeyF5, 0, ""), none, leadattach.KeyOptions{}, "\x1b[15~"},
		{"f6", key(uv.KeyF6, 0, ""), none, leadattach.KeyOptions{}, "\x1b[17~"},
		{"f7", key(uv.KeyF7, 0, ""), none, leadattach.KeyOptions{}, "\x1b[18~"},
		{"f8", key(uv.KeyF8, 0, ""), none, leadattach.KeyOptions{}, "\x1b[19~"},
		{"f9", key(uv.KeyF9, 0, ""), none, leadattach.KeyOptions{}, "\x1b[20~"},
		{"f10", key(uv.KeyF10, 0, ""), none, leadattach.KeyOptions{}, "\x1b[21~"},
		{"f11", key(uv.KeyF11, 0, ""), none, leadattach.KeyOptions{}, "\x1b[23~"},
		{"f12", key(uv.KeyF12, 0, ""), none, leadattach.KeyOptions{}, "\x1b[24~"},
		{"ctrl+f5", key(uv.KeyF5, ctrl, ""), none, leadattach.KeyOptions{}, "\x1b[15;5~"},
		// F13 to F20 are the forms of Shift+F1 to Shift+F8, as xterm sends them.
		{"f13", key(uv.KeyF13, 0, ""), none, leadattach.KeyOptions{}, "\x1b[1;2P"},
		{"f14", key(uv.KeyF14, 0, ""), none, leadattach.KeyOptions{}, "\x1b[1;2Q"},
		{"f15", key(uv.KeyF15, 0, ""), none, leadattach.KeyOptions{}, "\x1b[1;2R"},
		{"f16", key(uv.KeyF16, 0, ""), none, leadattach.KeyOptions{}, "\x1b[1;2S"},
		{"f17", key(uv.KeyF17, 0, ""), none, leadattach.KeyOptions{}, "\x1b[15;2~"},
		{"f18", key(uv.KeyF18, 0, ""), none, leadattach.KeyOptions{}, "\x1b[17;2~"},
		{"f19", key(uv.KeyF19, 0, ""), none, leadattach.KeyOptions{}, "\x1b[18;2~"},
		{"f20", key(uv.KeyF20, 0, ""), none, leadattach.KeyOptions{}, "\x1b[19;2~"},
		{"ctrl+f13 adds to the shift", key(uv.KeyF13, ctrl, ""), none, leadattach.KeyOptions{}, "\x1b[1;6P"},
		{"alt+f20 adds to the shift", key(uv.KeyF20, alt, ""), none, leadattach.KeyOptions{}, "\x1b[19;4~"},
		{"shift+f13 is f13", key(uv.KeyF13, shift, ""), none, leadattach.KeyOptions{}, "\x1b[1;2P"},
		{"f13, application cursor keys", key(uv.KeyF13, 0, ""), appCursor, leadattach.KeyOptions{}, "\x1b[1;2P"},
		{"f21 has no encoding", key(uv.KeyF21, 0, ""), none, leadattach.KeyOptions{}, ""},

		// The keypad.
		{"keypad 5, numeric", key(uv.KeyKp5, 0, ""), none, leadattach.KeyOptions{}, "5"},
		{"keypad 5, application", key(uv.KeyKp5, 0, ""), appKeypad, leadattach.KeyOptions{}, "\x1bOu"},
		{"keypad 0, application", key(uv.KeyKp0, 0, ""), appKeypad, leadattach.KeyOptions{}, "\x1bOp"},
		{"keypad 9, application", key(uv.KeyKp9, 0, ""), appKeypad, leadattach.KeyOptions{}, "\x1bOy"},
		{"keypad enter, numeric", key(uv.KeyKpEnter, 0, ""), none, leadattach.KeyOptions{}, "\r"},
		{"keypad enter, application", key(uv.KeyKpEnter, 0, ""), appKeypad, leadattach.KeyOptions{}, "\x1bOM"},
		{"keypad plus, numeric", key(uv.KeyKpPlus, 0, ""), none, leadattach.KeyOptions{}, "+"},
		{"keypad plus, application", key(uv.KeyKpPlus, 0, ""), appKeypad, leadattach.KeyOptions{}, "\x1bOk"},
		{"keypad minus, application", key(uv.KeyKpMinus, 0, ""), appKeypad, leadattach.KeyOptions{}, "\x1bOm"},
		{"keypad multiply, application", key(uv.KeyKpMultiply, 0, ""), appKeypad, leadattach.KeyOptions{}, "\x1bOj"},
		{"keypad divide, application", key(uv.KeyKpDivide, 0, ""), appKeypad, leadattach.KeyOptions{}, "\x1bOo"},
		{"keypad decimal, application", key(uv.KeyKpDecimal, 0, ""), appKeypad, leadattach.KeyOptions{}, "\x1bOn"},
		{"keypad equal, application", key(uv.KeyKpEqual, 0, ""), appKeypad, leadattach.KeyOptions{}, "\x1bOX"},
		{"keypad 5 with the cursor-key mode only", key(uv.KeyKp5, 0, ""), appCursor, leadattach.KeyOptions{}, "5"},
		{"alt+keypad 5, application", key(uv.KeyKp5, alt, ""), appKeypad, leadattach.KeyOptions{}, "\x1b\x1bOu"},

		// Keys with no legacy encoding.
		{"super+a", key('a', uv.ModSuper, ""), none, leadattach.KeyOptions{}, ""},
		{"hyper+up", key(uv.KeyUp, uv.ModHyper, ""), none, leadattach.KeyOptions{}, ""},
		{"a lone shift", key(uv.KeyLeftShift, 0, ""), none, leadattach.KeyOptions{}, ""},
		{"a media key", key(uv.KeyMediaPlay, 0, ""), none, leadattach.KeyOptions{}, ""},
		{"caps lock", key(uv.KeyCapsLock, 0, ""), none, leadattach.KeyOptions{}, ""},
		{"the empty key", key(0, 0, ""), none, leadattach.KeyOptions{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := leadattach.EncodeKey(c.k, c.m, c.o)
			if string(got) != c.want || (c.want == "" && got != nil) {
				t.Errorf("EncodeKey(%v) = %q, want %q", c.k, got, c.want)
			}
		})
	}
}

func TestEveryCtrlLetterIsItsLegacyByte(t *testing.T) {
	for r := 'a'; r <= 'z'; r++ {
		got := leadattach.EncodeKey(key(r, ctrl, ""), leadattach.ModeSet{}, leadattach.KeyOptions{})
		if len(got) != 1 || got[0] != byte(r-'a')+1 {
			t.Errorf("ctrl+%c = %q", r, got)
		}
		// With every mode the holder can report on, and Shift: the same byte.
		all := leadattach.ModeSet{DEC: []int{1, 25, 66, 67, 1000, 1002, 1003, 1004, 1006, 1049, 2004, 2026, 2027, 9001}, ANSI: []int{20}}
		if got2 := leadattach.EncodeKey(key(r, ctrl|shift, ""), all, leadattach.KeyOptions{ShiftEnter: leadattach.ShiftEnterCSIu}); string(got2) != string(got) {
			t.Errorf("ctrl+shift+%c with every mode on = %q, want %q", r, got2, got)
		}
	}
}

func TestShiftEnterOptionsDiffer(t *testing.T) {
	seen := map[string]leadattach.ShiftEnter{}
	for _, o := range []leadattach.ShiftEnter{leadattach.ShiftEnterESCCR, leadattach.ShiftEnterLF, leadattach.ShiftEnterCSIu, leadattach.ShiftEnterBackslashCR} {
		b := string(leadattach.EncodeKey(key(uv.KeyEnter, shift, ""), leadattach.ModeSet{}, leadattach.KeyOptions{ShiftEnter: o}))
		if b == "" || b == "\r" {
			t.Errorf("%v: %q", o, b)
		}
		if prev, dup := seen[b]; dup {
			t.Errorf("%v and %v send the same bytes %q", prev, o, b)
		}
		seen[b] = o
		if o.String() == "" {
			t.Errorf("%d has no name", int(o))
		}
	}
}

func TestEncodeKeyIsPure(t *testing.T) {
	m := leadattach.ModeSet{DEC: []int{1}}
	k := key(uv.KeyUp, 0, "")
	a := leadattach.EncodeKey(k, m, leadattach.KeyOptions{})
	a[0] = 'X' // the caller owns the result
	if b := leadattach.EncodeKey(k, m, leadattach.KeyOptions{}); string(b) != "\x1bOA" {
		t.Errorf("a second call gave %q", b)
	}
	if len(m.DEC) != 1 || m.DEC[0] != 1 {
		t.Error("the mode set changed")
	}
}

func FuzzEncodeKey(f *testing.F) {
	f.Add(int32('a'), 0, "a", true)
	f.Add(int32(uv.KeyUp), 5, "", false)
	f.Fuzz(func(t *testing.T, code int32, mod int, text string, app bool) {
		var m leadattach.ModeSet
		if app {
			m = leadattach.ModeSet{DEC: []int{1, 66, 67}, ANSI: []int{20}}
		}
		b := leadattach.EncodeKey(uv.KeyPressEvent{Code: rune(code), Mod: uv.KeyMod(mod), Text: text}, m, leadattach.KeyOptions{})
		for _, c := range b {
			_ = c
		}
		if len(b) > 64+len(text) {
			t.Fatalf("%d bytes for one key", len(b))
		}
	})
}
