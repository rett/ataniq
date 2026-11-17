package leadattach

import (
	"strconv"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
)

// ShiftEnter is what the client sends for Shift+Enter. A terminal without an
// enhanced keyboard protocol cannot tell Shift+Enter from Enter, and each
// agent has its own key for "a new line without sending": the choice is the
// caller's, per agent, and the default is the Alt+Enter form.
type ShiftEnter int

// The encodings of Shift+Enter.
const (
	// ShiftEnterESCCR sends ESC CR, what Alt+Enter sends. It is the default
	// (the zero value).
	ShiftEnterESCCR ShiftEnter = iota
	// ShiftEnterLF sends LF, what Ctrl+J sends.
	ShiftEnterLF
	// ShiftEnterCSIu sends CSI 13;2u, the enhanced form. It reaches only an
	// agent that understands it; any other shows the bytes as text.
	ShiftEnterCSIu
	// ShiftEnterBackslashCR sends a backslash and CR, the line continuation
	// that shells and several agents take as "new line".
	ShiftEnterBackslashCR
)

func (s ShiftEnter) String() string {
	switch s {
	case ShiftEnterLF:
		return "lf"
	case ShiftEnterCSIu:
		return "csi-u"
	case ShiftEnterBackslashCR:
		return "backslash-cr"
	}
	return "esc-cr"
}

func (s ShiftEnter) bytes() []byte {
	switch s {
	case ShiftEnterLF:
		return []byte("\n")
	case ShiftEnterCSIu:
		return []byte("\x1b[13;2u")
	case ShiftEnterBackslashCR:
		return []byte("\\\r")
	}
	return []byte("\x1b\r")
}

// KeyOptions are the choices of the key encoding that the session's modes do
// not make.
type KeyOptions struct {
	ShiftEnter ShiftEnter
}

// ModeSet is the set of terminal modes that are on, as the holder reports
// them in its Modes frame: DEC private mode numbers and ANSI mode numbers.
type ModeSet struct {
	DEC, ANSI []int
}

// Has reports whether the DEC private mode n (ansiMode false) or the ANSI
// mode n is on.
func (m ModeSet) Has(n int, ansiMode bool) bool {
	list := m.DEC
	if ansiMode {
		list = m.ANSI
	}
	for _, v := range list {
		if v == n {
			return true
		}
	}
	return false
}

// The modes the encodings depend on.
const (
	decCursorKeys = 1  // DECCKM: application cursor keys
	decKeypad     = 66 // DECNKM: application keypad
	decBackarrow  = 67 // DECBKM: Backspace sends BS, not DEL
	ansiNewline   = 20 // LNM: Enter sends CR LF

	decMousePress  = 9
	decMouseNormal = 1000
	decMouseHilite = 1001
	decMouseDrag   = 1002
	decMouseMotion = 1003
	decMouseSGR    = 1006
	decPaste       = 2004
	decFocus       = 1004
)

// EncodeKey returns the bytes a terminal sends for the key press k while the
// session has the modes ms on, or nil when the key has no encoding. It is a
// pure function of its arguments.
//
// The encoding is the legacy one (xterm without modifyOtherKeys, and without
// the kitty keyboard protocol), because the holder reports no keyboard
// enhancement mode and a session that never asked for one reads the legacy
// bytes:
//
//   - Ctrl with a letter, or with one of @ [ \ ] ^ _ ? space and the digits 2
//     to 8, is its C0 control byte (or DEL for ?), never a CSI u form: the
//     holder holds back Ctrl+Z by its byte and the terminal's signal
//     characters by theirs. Shift does not change it. Ctrl with another
//     character has no legacy form and is dropped.
//   - Alt (and Meta) puts ESC before the bytes of the key, except on a key
//     with a modifier parameter (arrows, Home, End, Insert, Delete, Page Up
//     and Down, F1 to F20), where it is the parameter 1+shift+2*alt+4*ctrl:
//     CSI 1;m final, or CSI n;m ~.
//   - Application cursor keys (DEC 1) make the arrows, Home and End SS3 forms
//     when no modifier is held. Application keypad (DEC 66) makes the keypad
//     keys SS3 forms. DEC 67 makes Backspace send BS (and Ctrl+Backspace
//     DEL). ANSI 20 makes Enter send CR LF.
//   - Shift+Enter is the option's bytes; Shift+Tab is CSI Z.
//   - F13 to F20 are the forms of Shift+F1 to Shift+F8 (CSI 1;2 P to S,
//     CSI 15;2 ~ to CSI 19;2 ~), as xterm sends them; a held modifier adds to
//     that Shift in the parameter.
//   - Ctrl+Enter, Ctrl+Tab and Ctrl+Backspace cannot be told from Enter, Tab
//     and BS in the legacy encoding; they send the same as the plain key
//     (Ctrl+Backspace: BS).
//   - A key held with Super or Hyper, a lone modifier key, a media key and
//     the other keys the legacy encoding lacks are dropped.
//   - A printable key sends its Text; with no Text, its code (shifted when
//     Shift is held). Text with a control character or invalid UTF-8 is
//     dropped.
func EncodeKey(k uv.KeyPressEvent, ms ModeSet, o KeyOptions) []byte {
	mod := k.Mod
	if mod&(uv.ModSuper|uv.ModHyper) != 0 {
		return nil
	}
	m := keyMods{
		shift: mod&uv.ModShift != 0,
		alt:   mod&(uv.ModAlt|uv.ModMeta) != 0,
		ctrl:  mod&uv.ModCtrl != 0,
	}
	b, prefix := encodeBase(k, m, ms, o)
	if len(b) == 0 {
		return nil
	}
	if prefix && m.alt {
		return append([]byte{0x1b}, b...)
	}
	return b
}

type keyMods struct{ shift, alt, ctrl bool }

func (m keyMods) any() bool { return m.shift || m.alt || m.ctrl }

// param is the xterm modifier parameter: 1 + shift + 2 alt + 4 ctrl.
func (m keyMods) param() int {
	p := 1
	if m.shift {
		p++
	}
	if m.alt {
		p += 2
	}
	if m.ctrl {
		p += 4
	}
	return p
}

// csiLetter are the keys that end in a letter: CSI letter, SS3 letter in
// the application modes, and CSI 1;m letter with a modifier.
var csiLetter = map[rune]byte{
	uv.KeyUp: 'A', uv.KeyDown: 'B', uv.KeyRight: 'C', uv.KeyLeft: 'D',
	uv.KeyHome: 'H', uv.KeyEnd: 'F', uv.KeyBegin: 'E',
}

// ss3Letter are F1 to F4: SS3 letter, CSI 1;m letter with a modifier.
var ss3Letter = map[rune]byte{uv.KeyF1: 'P', uv.KeyF2: 'Q', uv.KeyF3: 'R', uv.KeyF4: 'S'}

// csiTilde are the keys that end in a tilde.
var csiTilde = map[rune]int{
	uv.KeyFind: 1, uv.KeyInsert: 2, uv.KeyDelete: 3, uv.KeySelect: 4, uv.KeyPgUp: 5, uv.KeyPgDown: 6,
	uv.KeyF5: 15, uv.KeyF6: 17, uv.KeyF7: 18, uv.KeyF8: 19, uv.KeyF9: 20, uv.KeyF10: 21,
	uv.KeyF11: 23, uv.KeyF12: 24,
}

// shiftedFn are F13 to F20 as xterm sends them (terminfo xterm-256color,
// kf13 to kf20): the forms of Shift+F1 to Shift+F8, so the modifier
// parameter of a held modifier adds to that Shift.
var shiftedFn = map[rune]rune{
	uv.KeyF13: uv.KeyF1, uv.KeyF14: uv.KeyF2, uv.KeyF15: uv.KeyF3, uv.KeyF16: uv.KeyF4,
	uv.KeyF17: uv.KeyF5, uv.KeyF18: uv.KeyF6, uv.KeyF19: uv.KeyF7, uv.KeyF20: uv.KeyF8,
}

// keypadNav are the keypad keys a terminal with NumLock off reports as
// navigation keys: they are sent as those keys.
var keypadNav = map[rune]rune{
	uv.KeyKpUp: uv.KeyUp, uv.KeyKpDown: uv.KeyDown, uv.KeyKpLeft: uv.KeyLeft, uv.KeyKpRight: uv.KeyRight,
	uv.KeyKpPgUp: uv.KeyPgUp, uv.KeyKpPgDown: uv.KeyPgDown, uv.KeyKpHome: uv.KeyHome, uv.KeyKpEnd: uv.KeyEnd,
	uv.KeyKpInsert: uv.KeyInsert, uv.KeyKpDelete: uv.KeyDelete, uv.KeyKpBegin: uv.KeyBegin,
}

// keypad are the other keypad keys: the plain character and the SS3 final
// byte of the application keypad.
var keypad = map[rune]struct {
	plain string
	ss3   byte
}{
	uv.KeyKp0: {"0", 'p'}, uv.KeyKp1: {"1", 'q'}, uv.KeyKp2: {"2", 'r'}, uv.KeyKp3: {"3", 's'},
	uv.KeyKp4: {"4", 't'}, uv.KeyKp5: {"5", 'u'}, uv.KeyKp6: {"6", 'v'}, uv.KeyKp7: {"7", 'w'},
	uv.KeyKp8: {"8", 'x'}, uv.KeyKp9: {"9", 'y'},
	uv.KeyKpEnter: {"\r", 'M'}, uv.KeyKpEqual: {"=", 'X'}, uv.KeyKpMultiply: {"*", 'j'},
	uv.KeyKpPlus: {"+", 'k'}, uv.KeyKpComma: {",", 'l'}, uv.KeyKpMinus: {"-", 'm'},
	uv.KeyKpDecimal: {".", 'n'}, uv.KeyKpDivide: {"/", 'o'}, uv.KeyKpSep: {",", 'l'},
}

// encodeBase returns the bytes of the key without an Alt prefix, and whether
// Alt, when held, is added as an ESC prefix.
func encodeBase(k uv.KeyPressEvent, m keyMods, ms ModeSet, o KeyOptions) (b []byte, prefix bool) {
	code := k.Code
	if to, ok := keypadNav[code]; ok {
		code = to
	}
	if to, ok := shiftedFn[code]; ok {
		code, m.shift = to, true
	}
	switch code {
	case uv.KeyEnter:
		if m.shift && !m.alt && !m.ctrl {
			return o.ShiftEnter.bytes(), false
		}
		if ms.Has(ansiNewline, true) {
			return []byte("\r\n"), true
		}
		return []byte{'\r'}, true
	case uv.KeyTab:
		if m.shift {
			return []byte("\x1b[Z"), true
		}
		return []byte{'\t'}, true
	case uv.KeyBackspace:
		bs := ms.Has(decBackarrow, false)
		// Ctrl swaps the two: Backspace is DEL (BS under DECBKM), and
		// Ctrl+Backspace the other.
		if m.ctrl {
			bs = !bs
		}
		if bs {
			return []byte{0x08}, true
		}
		return []byte{0x7f}, true
	case uv.KeyEscape:
		return []byte{0x1b}, true
	}
	if c, ok := csiLetter[code]; ok {
		return csiKey(c, m, ms.Has(decCursorKeys, false)), false
	}
	if c, ok := ss3Letter[code]; ok {
		if m.any() {
			return []byte("\x1b[1;" + strconv.Itoa(m.param()) + string(c)), false
		}
		return []byte{0x1b, 'O', c}, false
	}
	if n, ok := csiTilde[code]; ok {
		if m.any() {
			return []byte("\x1b[" + strconv.Itoa(n) + ";" + strconv.Itoa(m.param()) + "~"), false
		}
		return []byte("\x1b[" + strconv.Itoa(n) + "~"), false
	}
	if kp, ok := keypad[code]; ok {
		if ms.Has(decKeypad, false) {
			return []byte{0x1b, 'O', kp.ss3}, true
		}
		return []byte(kp.plain), true
	}
	if code > uv.KeyExtended {
		return nil, false // a key with no legacy encoding: a media key, a modifier alone, F21 and up
	}
	return encodeChar(k, m)
}

// csiKey is an arrow, Home, End or Begin: CSI letter, SS3 letter in the
// application cursor mode, and CSI 1;m letter with a modifier.
func csiKey(c byte, m keyMods, app bool) []byte {
	switch {
	case m.any():
		return []byte("\x1b[1;" + strconv.Itoa(m.param()) + string(c))
	case app:
		return []byte{0x1b, 'O', c}
	}
	return []byte{0x1b, '[', c}
}

// encodeChar is a character key: its text, or its Ctrl byte.
func encodeChar(k uv.KeyPressEvent, m keyMods) ([]byte, bool) {
	code := k.Code
	if m.ctrl {
		if code == uv.KeyExtended || code < 0 {
			return nil, false
		}
		if c, ok := ctrlByte(code); ok {
			return []byte{c}, true
		}
		return nil, false
	}
	text := k.Text
	if text == "" {
		if code == uv.KeyExtended || code < 0x20 || code == 0x7f {
			return nil, false
		}
		r := code
		switch {
		case m.shift && k.ShiftedCode > 0x20:
			r = k.ShiftedCode
		case m.shift && r >= 'a' && r <= 'z':
			r -= 'a' - 'A'
		}
		text = string(r)
	}
	if !plainText(text) {
		return nil, false
	}
	return []byte(text), true
}

// plainText reports whether s is valid UTF-8 with no control character.
func plainText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r < 0xa0) {
			return false
		}
	}
	return true
}

// ctrlByte returns the C0 byte that Ctrl with the character r sends in the
// legacy encoding.
func ctrlByte(r rune) (byte, bool) {
	switch {
	case r >= 'a' && r <= 'z':
		return byte(r-'a') + 1, true
	case r >= 'A' && r <= 'Z':
		return byte(r-'A') + 1, true
	case r == '@' || r == ' ' || r == '2':
		return 0, true
	case r == '[' || r == '3':
		return 0x1b, true
	case r == '\\' || r == '4':
		return 0x1c, true
	case r == ']' || r == '5':
		return 0x1d, true
	case r == '^' || r == '6':
		return 0x1e, true
	case r == '_' || r == '7' || r == '/':
		return 0x1f, true
	case r == '?' || r == '8':
		return 0x7f, true
	}
	return 0, false
}
