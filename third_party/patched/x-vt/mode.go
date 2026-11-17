package vt

import "github.com/charmbracelet/x/ansi"

// recordedModes is the only table of modes the emulator records, in one place.
// The program the emulator serves is not trusted, and a map that records any
// mode number it sets grows without bound (a 100 000 distinct numbers cost
// megabytes), so setting or resetting a mode that is not here is ignored and
// counted in [Counters.ModesIgnored], and a request for it (DECRQM) is
// answered "not recognised", as for a mode that was never set.
//
// The table is every mode that x/ansi names, which includes the modes
// resetModes lists and the modes the session pane and the console follow (the
// mouse, focus and paste modes, the alternate screen, 2026 synchronized
// output and 2027 grapheme width), plus the ones a full-screen agent commonly
// sets without the emulator acting on them: DEC 12 (cursor blink), 1007
// (alternate scroll), and ANSI 2, 4, 8, 12 and 20. A mode is added by listing it
// here; the size of the table is the bound on what is recorded.
var recordedModes = map[ansi.Mode]struct{}{
	// ANSI modes.
	ansi.ModeKeyboardAction:       {}, // 2
	ansi.ModeInsertReplace:        {}, // 4
	ansi.ModeBiDirectionalSupport: {}, // 8
	ansi.ModeSendReceive:          {}, // 12
	ansi.ModeLineFeedNewLine:      {}, // 20
	// DEC private modes.
	ansi.ModeCursorKeys:          {}, // 1
	ansi.ModeOrigin:              {}, // 6
	ansi.ModeAutoWrap:            {}, // 7
	ansi.ModeMouseX10:            {}, // 9
	ansi.DECMode(12):             {}, // cursor blink (att610)
	ansi.ModeTextCursorEnable:    {}, // 25
	ansi.ModeNumericKeypad:       {}, // 66
	ansi.ModeBackarrowKey:        {}, // 67
	ansi.ModeLeftRightMargin:     {}, // 69
	ansi.ModeMouseNormal:         {}, // 1000
	ansi.ModeMouseHighlight:      {}, // 1001
	ansi.ModeMouseButtonEvent:    {}, // 1002
	ansi.ModeMouseAnyEvent:       {}, // 1003
	ansi.ModeFocusEvent:          {}, // 1004
	ansi.ModeMouseExtUtf8:        {}, // 1005
	ansi.ModeMouseExtSgr:         {}, // 1006
	ansi.DECMode(1007):           {}, // alternate scroll
	ansi.ModeMouseExtUrxvt:       {}, // 1015
	ansi.ModeMouseExtSgrPixel:    {}, // 1016
	ansi.ModeAltScreen:           {}, // 1047
	ansi.ModeSaveCursor:          {}, // 1048
	ansi.ModeAltScreenSaveCursor: {}, // 1049
	ansi.ModeBracketedPaste:      {}, // 2004
	ansi.ModeSynchronizedOutput:  {}, // 2026
	ansi.ModeUnicodeCore:         {}, // 2027
	ansi.ModeLightDark:           {}, // 2031
	ansi.ModeInBandResize:        {}, // 2048
	ansi.ModeWin32Input:          {}, // 9001
}

// resetModes resets all modes to their default values.
func (e *Emulator) resetModes() {
	e.modes = ansi.Modes{
		// Recognized modes and their default values.
		ansi.ModeCursorKeys:          ansi.ModeReset, // ?1
		ansi.ModeOrigin:              ansi.ModeReset, // ?6
		ansi.ModeAutoWrap:            ansi.ModeSet,   // ?7
		ansi.ModeMouseX10:            ansi.ModeReset, // ?9
		ansi.ModeLineFeedNewLine:     ansi.ModeReset, // ?20
		ansi.ModeTextCursorEnable:    ansi.ModeSet,   // ?25
		ansi.ModeNumericKeypad:       ansi.ModeReset, // ?66
		ansi.ModeLeftRightMargin:     ansi.ModeReset, // ?69
		ansi.ModeMouseNormal:         ansi.ModeReset, // ?1000
		ansi.ModeMouseHighlight:      ansi.ModeReset, // ?1001
		ansi.ModeMouseButtonEvent:    ansi.ModeReset, // ?1002
		ansi.ModeMouseAnyEvent:       ansi.ModeReset, // ?1003
		ansi.ModeFocusEvent:          ansi.ModeReset, // ?1004
		ansi.ModeMouseExtSgr:         ansi.ModeReset, // ?1006
		ansi.ModeAltScreen:           ansi.ModeReset, // ?1047
		ansi.ModeSaveCursor:          ansi.ModeReset, // ?1048
		ansi.ModeAltScreenSaveCursor: ansi.ModeReset, // ?1049
		ansi.ModeBracketedPaste:      ansi.ModeReset, // ?2004
	}

	// Set mode effects.
	for mode, setting := range e.modes {
		e.setMode(mode, setting)
	}
}
