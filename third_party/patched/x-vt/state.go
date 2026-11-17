package vt

import (
	"errors"
	"fmt"
	"image/color"
	"reflect"
	"slices"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/ansi/parser"
)

// This file lets a program copy the whole state of an emulator into another
// one: [Emulator.State] reads it and [Emulator.SetState] replaces it. The
// session pane uses the pair to hand the screen of one emulator to a second
// that then follows the same output. The state is the one thing later output
// can depend on, so the test of the pair is that two emulators, one of them
// set from the state of the other, stay equal for ever when they are fed the
// same bytes. The inventory test lists every field of the emulator, its
// screens and its cursors, and whether the state carries it.

// maxParam is the largest value a parameter of a control sequence can have,
// which bounds the cursor styles DECSCUSR can select.
const maxParam = 1<<31 - 1

// CharSetID names one of the three character sets a G0 to G3 slot can hold.
type CharSetID int

// The character sets the emulator can designate.
const (
	CharSetUSASCII CharSetID = iota
	CharSetUK
	CharSetSpecialDrawing
	// CharSetOther is reported for a slot that holds a set the emulator did
	// not designate (the API cannot put one there). SetState refuses it.
	CharSetOther
)

// ModeState is the setting of one recorded mode. A mode that the session never
// set or reset has no entry: DECRQM answers "not recognised" for it, and for
// a mode that was reset it answers "reset".
type ModeState struct {
	Mode    ansi.Mode
	Setting ansi.ModeSetting
}

// OpenClusterState is the grapheme cluster the last write ended in, which the
// next write may still extend (see [Emulator.OpenCluster]).
type OpenClusterState struct {
	// Pos is the cell the cluster was drawn in, and Text its text.
	Pos  uv.Position
	Text string
	// Capped is true when a rune was dropped from the cluster because it was
	// [MaxClusterBytes] long: it does not grow again.
	Capped bool
	// Single is the single shift (2 or 3, or 0 for none) that the cluster
	// consumed when it was drawn.
	Single int
}

// ScreenState is the state of one screen, the main or the alternate one.
type ScreenState struct {
	// Cells are the cells, row by row, State.Width by State.Height of them.
	Cells []uv.Cell
	// Cursor is the screen's cursor with its pen and its open hyperlink, and
	// Saved the cursor DECSC saved.
	Cursor, Saved Cursor
	// Scroll is the scroll region, inside the screen and not empty.
	Scroll uv.Rectangle
	// Links is the table of the links the cells of the screen may reference
	// (see linkTable), sorted by URL and then parameters. A link of the table
	// that no cell references is kept: it fills a slot until a recount drops
	// it, and so decides which later link is refused.
	Links []uv.Link
	// LinkWait is the number of refusals that must pass before the table is
	// recounted again.
	LinkWait int
}

// State is a copy of everything of an [Emulator] that later output can depend
// on. It leaves out what the emulator is given by its caller and not by the
// output (the callbacks, the handlers, the logger and the default colours),
// what only the API sets (the indexed colours and the scrollback), what is
// derived (the damage of the screens, the counters and the scratch buffers),
// and the parser, which [Emulator.SetState] resets to its ground state.
//
// A State describes an emulator between writes: a write always draws the
// grapheme cluster it ends in, so none is half collected.
type State struct {
	Width, Height int
	// Alt is true when the alternate screen is the active one.
	Alt bool
	// Screens are the main screen, then the alternate screen.
	Screens [2]ScreenState
	// Modes are the settings of the modes that have one, sorted: the ANSI
	// modes by number, then the DEC private modes by number.
	Modes []ModeState
	// Charsets are the sets of G0 to G3, GL and GR the locking shifts, and
	// GSingle the single shift (2, 3 or 0) for the next character.
	Charsets        [4]CharSetID
	GL, GR, GSingle int
	// LastChar is the character REP repeats, or 0.
	LastChar rune
	// AtPhantom is the pending wrap: the cursor is at the end of the line and
	// the next character goes to the start of the next.
	AtPhantom bool
	// TabStops are the columns that have a tab stop, ascending.
	TabStops []int
	// Foreground, Background and CursorColor are the colours the session set
	// with OSC 10, 11 and 12. Nil means it set none.
	Foreground, Background, CursorColor color.Color
	// OpenCluster is the cluster the next write may still extend, or nil.
	OpenCluster *OpenClusterState
}

// Quiescent reports whether the parser is at its ground state: no escape
// sequence, no control string and no UTF-8 character is half received. A
// grapheme cluster that a write ended in does not count: it is drawn, and
// [State] carries it as the open cluster.
func (e *Emulator) Quiescent() bool {
	return e.parser.State() == parser.GroundState
}

// RecordedModes returns the modes the emulator records, which are the ones
// [Emulator.ModeSettings] can report: the ANSI modes by number, then the DEC
// private modes by number.
func RecordedModes() []ansi.Mode {
	modes := make([]ansi.Mode, 0, len(recordedModes))
	for m := range recordedModes {
		modes = append(modes, m)
	}
	slices.SortFunc(modes, compareModes)
	return modes
}

func compareModes(a, b ansi.Mode) int {
	_, aDEC := a.(ansi.DECMode)
	_, bDEC := b.(ansi.DECMode)
	switch {
	case aDEC != bDEC && aDEC:
		return 1
	case aDEC != bDEC:
		return -1
	}
	return int(a.Mode()) - int(b.Mode())
}

// ModeSettings returns the settings of the modes that have one: the modes at
// their defaults and those the session set or reset.
func (e *Emulator) ModeSettings() []ModeState {
	out := make([]ModeState, 0, len(e.modes))
	for m, s := range e.modes {
		out = append(out, ModeState{Mode: m, Setting: s})
	}
	slices.SortFunc(out, func(a, b ModeState) int { return compareModes(a.Mode, b.Mode) })
	return out
}

// State returns a copy of the emulator's state. The strings of the copy are
// shared with the emulator, which never changes a string.
func (e *Emulator) State() State {
	w, h := e.scrs[0].Width(), e.scrs[0].Height()
	st := State{
		Width: w, Height: h,
		Alt:         e.scr == &e.scrs[1],
		Modes:       e.ModeSettings(),
		GL:          e.gl,
		GR:          e.gr,
		GSingle:     e.gsingle,
		LastChar:    e.lastChar,
		AtPhantom:   e.atPhantom,
		Foreground:  e.fgColor,
		Background:  e.bgColor,
		CursorColor: e.curColor,
	}
	for i := range e.scrs {
		st.Screens[i] = e.scrs[i].state()
	}
	for i, cs := range e.charsets {
		st.Charsets[i] = charSetID(cs)
	}
	for x := 0; x < w; x++ {
		if e.tabstops.IsStop(x) {
			st.TabStops = append(st.TabStops, x)
		}
	}
	if pos, text, ok := e.OpenCluster(); ok {
		st.OpenCluster = &OpenClusterState{
			Pos: pos, Text: text, Capped: e.open.capped, Single: e.open.single,
		}
	}
	return st
}

func charSetID(cs CharSet) CharSetID {
	if cs == nil {
		return CharSetUSASCII
	}
	p := reflect.ValueOf(cs).Pointer()
	switch p {
	case reflect.ValueOf(UK).Pointer():
		return CharSetUK
	case reflect.ValueOf(SpecialDrawing).Pointer():
		return CharSetSpecialDrawing
	}
	return CharSetOther
}

func (id CharSetID) charSet() CharSet {
	switch id {
	case CharSetUK:
		return UK
	case CharSetSpecialDrawing:
		return SpecialDrawing
	}
	return nil
}

// state copies the screen's state.
func (s *Screen) state() ScreenState {
	w, h := s.Width(), s.Height()
	st := ScreenState{
		Cells:    make([]uv.Cell, 0, w*h),
		Cursor:   s.cur,
		Saved:    s.saved,
		Scroll:   s.scroll,
		LinkWait: s.links.wait,
	}
	for _, line := range s.buf.Lines {
		st.Cells = append(st.Cells, line...)
	}
	for l := range s.links.set {
		st.Links = append(st.Links, l)
	}
	slices.SortFunc(st.Links, func(a, b uv.Link) int {
		if a.URL != b.URL {
			if a.URL < b.URL {
				return -1
			}
			return 1
		}
		switch {
		case a.Params < b.Params:
			return -1
		case a.Params > b.Params:
			return 1
		}
		return 0
	})
	return st
}

// ErrInvalidState is wrapped by the error [Emulator.SetState] returns for a
// state that no emulator can be in or that is over a cap.
var ErrInvalidState = errors.New("vt: invalid state")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidState, fmt.Sprintf(format, args...))
}

// SetState replaces the emulator's whole state, its size included, with st.
// It calls no callback and writes no reply. The parser goes back to its ground
// state, so a sequence half received is dropped. The counters, the
// callbacks, the handlers and the default colours are kept; the scrollback
// is emptied.
//
// A state that does not fit the caps of the emulator is refused and leaves
// the emulator as it was: the cells must be Width by Height, a cell keeps at
// most [MaxClusterBytes] bytes, a table holds at most [MaxLiveLinks] links
// of at most [MaxLinkURL] and [MaxLinkParams] bytes, every link a cell
// references (or whose URL is not empty) must be in the table of its screen,
// and the modes must be in the table of recorded modes. The links go through
// the admission of the screen's table, so the cells share one copy of each,
// as cells that were printed do.
func (e *Emulator) SetState(st State) error {
	if err := st.validate(); err != nil {
		return err
	}
	var scrs [2]Screen
	for i := range scrs {
		s, err := newScreenFromState(&st.Screens[i], st.Width, st.Height)
		if err != nil {
			return err
		}
		s.cb = &e.cb
		// The scrollback is the emulator's own (the caller's size for the
		// main screen, none for the alternate), and what it held no longer
		// belongs to this screen.
		s.scrollback = e.scrs[i].scrollback
		scrs[i] = *s
	}

	// Nothing can fail from here on.
	e.scrs = scrs
	for i := range e.scrs {
		if sb := e.scrs[i].scrollback; sb != nil {
			sb.Clear()
		}
	}
	e.scr = &e.scrs[0]
	if st.Alt {
		e.scr = &e.scrs[1]
	}
	e.modes = make(ansi.Modes, len(st.Modes))
	for _, m := range st.Modes {
		e.modes[m.Mode] = m.Setting
	}
	for i, id := range st.Charsets {
		e.charsets[i] = id.charSet()
	}
	e.gl, e.gr, e.gsingle = st.GL, st.GR, st.GSingle
	e.lastChar = st.LastChar
	e.atPhantom = st.AtPhantom
	e.tabstops = uv.DefaultTabStops(st.Width)
	e.tabstops.Clear()
	for _, x := range st.TabStops {
		e.tabstops.Set(x)
	}
	e.fgColor, e.bgColor, e.curColor = st.Foreground, st.Background, st.CursorColor

	e.grapheme = e.grapheme[:0]
	e.capped = false
	e.open.ok = false
	if o := st.OpenCluster; o != nil {
		c := e.scr.CellAt(o.Pos.X, o.Pos.Y)
		curX, curY := e.scr.CursorPosition()
		e.open = openCluster{
			ok:      true,
			content: append(e.open.content[:0], o.Text...),
			drawn:   c.Content,
			x:       o.Pos.X, y: o.Pos.Y,
			curX: curX, curY: curY,
			phantom: e.atPhantom,
			single:  o.Single,
			capped:  o.Capped,
		}
	}
	e.parser.Reset()
	return nil
}

func (st *State) validate() error {
	w, h := st.Width, st.Height
	if w < 1 || h < 1 || w > maxParam/h {
		return invalid("size %dx%d", w, h)
	}
	for i := range st.Screens {
		if n := len(st.Screens[i].Cells); n != w*h {
			return invalid("screen %d has %d cells, want %d", i, n, w*h)
		}
	}
	prev := ansi.Mode(nil)
	for _, m := range st.Modes {
		if _, ok := recordedModes[m.Mode]; !ok {
			return invalid("mode %v is not recorded", m.Mode)
		}
		if m.Setting != ansi.ModeSet && m.Setting != ansi.ModeReset {
			return invalid("mode %v has setting %v", m.Mode, m.Setting)
		}
		if prev != nil && compareModes(prev, m.Mode) >= 0 {
			return invalid("modes are not in order at %v", m.Mode)
		}
		prev = m.Mode
	}
	for _, id := range st.Charsets {
		if id < CharSetUSASCII || id >= CharSetOther {
			return invalid("character set %d", id)
		}
	}
	if st.GL < 0 || st.GL > 3 || st.GR < 0 || st.GR > 3 {
		return invalid("locking shifts %d, %d", st.GL, st.GR)
	}
	if st.GSingle != 0 && st.GSingle != 2 && st.GSingle != 3 {
		return invalid("single shift %d", st.GSingle)
	}
	if st.LastChar != 0 && !utf8.ValidRune(st.LastChar) {
		return invalid("last character %d", st.LastChar)
	}
	last := -1
	for _, x := range st.TabStops {
		if x <= last || x >= w {
			return invalid("tab stop %d", x)
		}
		last = x
	}
	if o := st.OpenCluster; o != nil {
		if o.Pos.X < 0 || o.Pos.X >= w || o.Pos.Y < 0 || o.Pos.Y >= h {
			return invalid("open cluster at %v", o.Pos)
		}
		if n := len(o.Text); n == 0 || n > MaxClusterBytes || !utf8.ValidString(o.Text) {
			return invalid("open cluster of %d bytes", n)
		}
		if o.Single != 0 && o.Single != 2 && o.Single != 3 {
			return invalid("open cluster single shift %d", o.Single)
		}
	}
	return nil
}

func validCursor(c *Cursor, w, h int) error {
	if c.X < 0 || c.X >= w || c.Y < 0 || c.Y >= h {
		return invalid("cursor at %v on a screen of %dx%d", c.Position, w, h)
	}
	if c.Style < 0 || c.Style > maxParam {
		return invalid("cursor style %d", c.Style)
	}
	if len(c.Link.URL) > MaxLinkURL || len(c.Link.Params) > MaxLinkParams {
		return invalid("pen link of %d and %d bytes", len(c.Link.URL), len(c.Link.Params))
	}
	return nil
}

// newScreenFromState builds a screen from its state, or refuses it.
func newScreenFromState(st *ScreenState, w, h int) (*Screen, error) {
	if err := validCursor(&st.Cursor, w, h); err != nil {
		return nil, err
	}
	if err := validCursor(&st.Saved, w, h); err != nil {
		return nil, err
	}
	if r := st.Scroll; r.Min.X < 0 || r.Min.Y < 0 || r.Max.X > w || r.Max.Y > h ||
		r.Min.X >= r.Max.X || r.Min.Y >= r.Max.Y {
		return nil, invalid("scroll region %v on a screen of %dx%d", r, w, h)
	}
	if st.LinkWait < 0 {
		return nil, invalid("link wait %d", st.LinkWait)
	}
	if len(st.Links) > MaxLiveLinks {
		return nil, invalid("%d links in the table, at most %d", len(st.Links), MaxLiveLinks)
	}

	s := &Screen{buf: uv.NewRenderBuffer(w, h)}
	s.cur, s.saved, s.scroll = st.Cursor, st.Saved, st.Scroll
	for _, l := range st.Links {
		if l.URL == "" || len(l.URL) > MaxLinkURL || len(l.Params) > MaxLinkParams {
			return nil, invalid("link of %d and %d bytes in the table", len(l.URL), len(l.Params))
		}
		if _, dup := s.links.set[l]; dup {
			return nil, invalid("link %q twice in the table", l.URL)
		}
		if _, ok := s.admitLink(l); !ok {
			return nil, invalid("link table full")
		}
	}
	s.links.wait = st.LinkWait

	for y := 0; y < h; y++ {
		line := s.buf.Lines[y]
		for x := range line {
			c := st.Cells[y*w+x]
			if len(c.Content) > MaxClusterBytes || c.Width < 0 || c.Width > MaxClusterBytes {
				return nil, invalid("cell %d,%d of %d bytes and width %d", x, y, len(c.Content), c.Width)
			}
			if len(c.Link.Params) > MaxLinkParams || len(c.Link.URL) > MaxLinkURL {
				return nil, invalid("cell %d,%d has a link of %d and %d bytes", x, y, len(c.Link.URL), len(c.Link.Params))
			}
			if c.Link.URL != "" {
				l, ok := s.links.set[c.Link]
				if !ok {
					return nil, invalid("cell %d,%d references a link that is not in the table", x, y)
				}
				c.Link = l
			}
			line[x] = c
		}
	}
	return s, nil
}
