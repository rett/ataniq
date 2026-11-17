package sessionpane

import (
	"errors"
	"fmt"
	"image/color"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// The snapshot format, version 1. All integers are unsigned varints unless a
// width is given, strings are a varint length and the bytes, and a boolean
// is a byte of 0 or 1. The encoding is deterministic: the same state gives
// the same bytes.
//
//	version                 byte (1)
//	cols, rows              varints
//	flags                   byte: bit 0 alternate screen active, bit 1 pending wrap
//	gl, gr, single shift    three bytes
//	character sets G0..G3   four bytes: 0 US ASCII, 1 UK, 2 special drawing
//	last character          varint
//	session colours         three times: present byte, then 4 x uint16 big endian
//	                        (red, green, blue, alpha) when present: foreground,
//	                        background, cursor
//	modes                   count, then each: kind byte (0 ANSI, 1 DEC), number
//	                        varint, setting byte (1 set, 2 reset); ANSI modes
//	                        first, each kind ascending
//	tab stops               (cols+7)/8 bytes, bit n%8 of byte n/8 is column n
//	open cluster            present byte; then column, row, single shift byte,
//	                        capped byte, text (at most 64 bytes)
//	title                   string
//	styles                  count, then each style of the table, which starts
//	                        at index 1 (index 0 is the style with nothing set)
//	main screen, alternate screen: each
//	  scroll region         minimum x, minimum y, maximum x, maximum y
//	  cursor, saved cursor  column, row, cursor shape, flags byte (bit 0
//	                        steady, bit 1 hidden), pen style index, hyperlink
//	                        (URL string, parameters string; both empty for none)
//	  link table            count (at most 256), each URL and parameters string,
//	                        ascending by URL and then parameters; then the
//	                        number of refusals before the next recount
//	  cells                 runs, until cols*rows cells: run length (at least 1),
//	                        content string (at most 64 bytes), width (0 to 2),
//	                        style index, link index (0 none, else 1 + the table
//	                        index)
//
// A style is: attributes byte, underline byte (0 to 5), then foreground,
// background and underline colour. A colour is a tag byte and its value: 0
// none; 1 basic, one byte (0 to 15); 2 indexed, one byte; 3 RGBA, four bytes;
// 4 CMYK, four bytes; 5 alpha, two bytes big endian.
//
// A cell width is not checked against its neighbours: output makes a wide
// cell at the last column (a resize cuts a line), a cell of width 0 with no
// wide cell before it (a lone combining mark) and a wide cell with no
// placeholder after it (an insert or a delete cuts a pair), and the emulator
// survives each of them.
//
// A hyperlink with no URL is no hyperlink: the parameters of one are not
// kept (OSC 8 sets both, so a lone parameter string never matters).
const snapshotVersion = 1

// MaxSnapshotBytes is the most bytes a snapshot has, and the most Restore
// reads: 16 MiB minus 8 bytes, so that a frame carrying it, with its header,
// stays within 16 MiB.
const MaxSnapshotBytes = 16<<20 - 8

// Errors of the snapshot functions; test with errors.Is.
var (
	// ErrNotQuiescent is returned by AppendSnapshot when a sequence, a
	// string or a UTF-8 character is half received.
	ErrNotQuiescent = errors.New("sessionpane: pane is not quiescent")
	// ErrSnapshotTooLarge is returned by AppendSnapshot when the snapshot
	// would be over MaxSnapshotBytes (a screen of 63-byte clusters of
	// different styles on both screens can be).
	ErrSnapshotTooLarge = errors.New("sessionpane: snapshot over the size limit")
	// ErrUnsupportedState is returned by AppendSnapshot for a state the
	// session's output cannot make, which the format cannot carry: a cell
	// set through the API with a colour of another kind, with a link that is
	// not in the screen's table, or with content over 64 bytes.
	ErrUnsupportedState = errors.New("sessionpane: state the snapshot cannot carry")
	// ErrBadSnapshot is returned by Restore for bytes that are not a
	// snapshot, or that hold a value over a limit of the pane.
	ErrBadSnapshot = errors.New("sessionpane: invalid snapshot")
	// ErrEnded is returned by Restore after End or Close.
	ErrEnded = errors.New("sessionpane: the pane has ended or is closed")
)

// maxCellWidth is the widest cell a snapshot carries. The emulator draws a
// cluster in one or two cells and a placeholder of a wide cell has width 0;
// a charset mapping gives 1. A wider cell is one that output cannot make, and
// that a later write indexes past the line with (ultraviolet's Line.Set
// rewrites the whole span of a wide cell it overwrites).
const maxCellWidth = vt.MaxCellWidth

// Colour tags.
const (
	colNone = iota
	colBasic
	colIndexed
	colRGBA
	colCMYK
	colAlpha
)

// styleBytes is the longest encoded style: two bytes, and three colours of a
// tag and four bytes.
const styleBytes = 2 + 3*5

type styleKey [styleBytes]byte

// appendColour encodes c, or reports that the format has no tag for its kind.
func appendColour(b []byte, c color.Color) ([]byte, bool) {
	switch c := c.(type) {
	case nil:
		return append(b, colNone), true
	case ansi.BasicColor:
		return append(b, colBasic, byte(c)), true
	case ansi.IndexedColor:
		return append(b, colIndexed, byte(c)), true
	case color.RGBA:
		return append(b, colRGBA, c.R, c.G, c.B, c.A), true
	case color.CMYK:
		return append(b, colCMYK, c.C, c.M, c.Y, c.K), true
	case color.Alpha16:
		return append(b, colAlpha, byte(c.A>>8), byte(c.A)), true
	}
	return b, false
}

// appendStyle encodes s and reports whether the format can carry it.
func appendStyle(b []byte, s *uv.Style) ([]byte, bool) {
	b = append(b, s.Attrs, byte(s.Underline))
	var ok bool
	for _, c := range [3]color.Color{s.Fg, s.Bg, s.UnderlineColor} {
		if b, ok = appendColour(b, c); !ok {
			return b, false
		}
	}
	return b, true
}

// normLink is the link as the pane keeps it apart: a link with no URL is no
// link, whatever its parameters.
func normLink(l uv.Link) uv.Link {
	if l.URL == "" {
		return uv.Link{}
	}
	return l
}

// encoder writes a snapshot.
type encoder struct {
	b     []byte
	start int
}

func (e *encoder) u8(v byte)      { e.b = append(e.b, v) }
func (e *encoder) bool(v bool)    { e.b = append(e.b, b2u(v)) }
func (e *encoder) uv(v uint64)    { e.b = appendUvarint(e.b, v) }
func (e *encoder) int(v int)      { e.b = appendUvarint(e.b, uint64(v)) }
func (e *encoder) str(s string)   { e.int(len(s)); e.b = append(e.b, s...) }
func (e *encoder) size() int      { return len(e.b) - e.start }
func (e *encoder) tooLarge() bool { return e.size() > MaxSnapshotBytes }
func b2u(v bool) byte {
	if v {
		return 1
	}
	return 0
}

func appendUvarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

func (e *encoder) optionalColour(c color.Color) {
	if c == nil {
		e.u8(0)
		return
	}
	r, g, b, a := c.RGBA()
	e.u8(1)
	for _, v := range [4]uint32{r, g, b, a} {
		e.b = append(e.b, byte(v>>8), byte(v))
	}
}

func unsupported(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupportedState, fmt.Sprintf(format, args...))
}

// cellRef is a cell as the indexes into the tables of the snapshot.
type cellRef struct {
	content string
	width   uint32
	style   uint32
	link    uint32
}

// encodeState writes the snapshot of st and the title.
func (e *encoder) encodeState(st *vt.State, title string) error {
	// The style table first: every style of a cell or of a pen, once, in
	// the order of first use, so that the encoding is deterministic.
	styles := map[styleKey]uint32{}
	var table []byte
	var tableLen uint32
	styleIndex := func(s *uv.Style) (uint32, error) {
		var buf [styleBytes]byte
		enc, ok := appendStyle(buf[:0], s)
		if !ok {
			return 0, unsupported("a style with a colour of another kind")
		}
		var key styleKey
		copy(key[:], enc)
		if key == (styleKey{}) {
			return 0, nil // a style with nothing set encodes as zeros
		}
		if i, ok := styles[key]; ok {
			return i, nil
		}
		tableLen++
		styles[key] = tableLen
		table = append(table, enc...)
		return tableLen, nil
	}

	type screenRefs struct {
		cells        []cellRef
		links        map[uv.Link]uint32
		cursor, save uint32
	}
	var refs [2]screenRefs
	for i := range st.Screens {
		sc := &st.Screens[i]
		r := &refs[i]
		var err error
		if r.cursor, err = styleIndex(&sc.Cursor.Pen); err != nil {
			return err
		}
		if r.save, err = styleIndex(&sc.Saved.Pen); err != nil {
			return err
		}
		r.links = make(map[uv.Link]uint32, len(sc.Links))
		for j, l := range sc.Links {
			r.links[l] = uint32(j + 1)
		}
		r.cells = make([]cellRef, len(sc.Cells))
		var prevStyle uv.Style
		var prevIdx uint32
		var prevLink uv.Link
		var prevLinkIdx uint32
		for k := range sc.Cells {
			c := &sc.Cells[k]
			if len(c.Content) > vt.MaxClusterBytes {
				return unsupported("a cell of %d bytes", len(c.Content))
			}
			if c.Width < 0 || c.Width > maxCellWidth {
				return unsupported("a cell of width %d", c.Width)
			}
			if k == 0 || !sameStyle(&prevStyle, &c.Style) {
				idx, err := styleIndex(&c.Style)
				if err != nil {
					return err
				}
				prevStyle, prevIdx = c.Style, idx
			}
			link := normLink(c.Link)
			if k == 0 || link != prevLink {
				prevLink, prevLinkIdx = link, 0
				if link.URL != "" {
					idx, ok := r.links[link]
					if !ok {
						return unsupported("a cell references a link that is not in the screen's table")
					}
					prevLinkIdx = idx
				}
			}
			r.cells[k] = cellRef{c.Content, uint32(c.Width), prevIdx, prevLinkIdx}
		}
	}

	e.u8(snapshotVersion)
	e.int(st.Width)
	e.int(st.Height)
	flags := b2u(st.Alt) | b2u(st.AtPhantom)<<1
	e.u8(flags)
	e.int(st.GL)
	e.int(st.GR)
	e.int(st.GSingle)
	for _, id := range st.Charsets {
		e.u8(byte(id))
	}
	e.int(int(st.LastChar))
	e.optionalColour(st.Foreground)
	e.optionalColour(st.Background)
	e.optionalColour(st.CursorColor)

	e.int(len(st.Modes))
	for _, m := range st.Modes {
		switch v := m.Mode.(type) {
		case ansi.ANSIMode:
			e.u8(0)
			e.int(int(v))
		case ansi.DECMode:
			e.u8(1)
			e.int(int(v))
		default:
			return unsupported("mode %v", m.Mode)
		}
		e.u8(byte(m.Setting))
	}

	tabs := make([]byte, (st.Width+7)/8)
	for _, x := range st.TabStops {
		tabs[x/8] |= 1 << (x % 8)
	}
	e.b = append(e.b, tabs...)

	if o := st.OpenCluster; o != nil {
		e.u8(1)
		e.int(o.Pos.X)
		e.int(o.Pos.Y)
		e.int(o.Single)
		e.bool(o.Capped)
		e.str(o.Text)
	} else {
		e.u8(0)
	}
	e.str(title)

	e.int(int(tableLen))
	e.b = append(e.b, table...)

	for i := range st.Screens {
		sc := &st.Screens[i]
		r := &refs[i]
		e.int(sc.Scroll.Min.X)
		e.int(sc.Scroll.Min.Y)
		e.int(sc.Scroll.Max.X)
		e.int(sc.Scroll.Max.Y)
		for j, cur := range [2]*vt.Cursor{&sc.Cursor, &sc.Saved} {
			e.int(cur.X)
			e.int(cur.Y)
			e.int(int(cur.Style))
			e.u8(b2u(cur.Steady) | b2u(cur.Hidden)<<1)
			e.int(int([2]uint32{r.cursor, r.save}[j]))
			l := normLink(cur.Link)
			e.str(l.URL)
			e.str(l.Params)
		}
		e.int(len(sc.Links))
		for _, l := range sc.Links {
			e.str(l.URL)
			e.str(l.Params)
		}
		e.int(sc.LinkWait)

		for k := 0; k < len(r.cells); {
			ref := r.cells[k]
			run := 1
			for k+run < len(r.cells) && r.cells[k+run] == ref {
				run++
			}
			e.int(run)
			e.str(ref.content)
			e.int(int(ref.width))
			e.int(int(ref.style))
			e.int(int(ref.link))
			k += run
			if e.tooLarge() {
				return ErrSnapshotTooLarge
			}
		}
	}
	if e.tooLarge() {
		return ErrSnapshotTooLarge
	}
	return nil
}

// sameStyle reports whether two styles encode alike, without encoding them.
// It compares colours by kind and value, so a style is never taken for
// another with a colour of an equal value but of another kind.
func sameStyle(a, b *uv.Style) bool {
	return a.Attrs == b.Attrs && a.Underline == b.Underline &&
		sameColour(a.Fg, b.Fg) && sameColour(a.Bg, b.Bg) && sameColour(a.UnderlineColor, b.UnderlineColor)
}

func sameColour(a, b color.Color) bool {
	switch a := a.(type) {
	case nil:
		return b == nil
	case ansi.BasicColor:
		b, ok := b.(ansi.BasicColor)
		return ok && a == b
	case ansi.IndexedColor:
		b, ok := b.(ansi.IndexedColor)
		return ok && a == b
	case color.RGBA:
		b, ok := b.(color.RGBA)
		return ok && a == b
	case color.CMYK:
		b, ok := b.(color.CMYK)
		return ok && a == b
	case color.Alpha16:
		b, ok := b.(color.Alpha16)
		return ok && a == b
	}
	return false // a kind the format does not carry: encode it, to refuse it
}
