package sessionpane

import (
	"fmt"
	"image/color"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

// maxLinkWait is the most refusals the emulator waits before it recounts the
// links of a screen: the cells of the largest screen over its recount
// interval (256).
const maxLinkWait = MaxCols * MaxRows / 256

// maxStyles is the most styles a snapshot has: one for every cell of both
// screens, and the four pens.
const maxStyles = 2*MaxCols*MaxRows + 4

// A recorded mode as the table of the emulator names it.
type modeKey struct {
	dec bool
	n   int
}

var recordedModes = func() map[modeKey]bool {
	m := map[modeKey]bool{}
	for _, mode := range vt.RecordedModes() {
		_, dec := mode.(ansi.DECMode)
		m[modeKey{dec, mode.Mode()}] = true
	}
	return m
}()

// decoder reads a snapshot. The first fault is kept, every read after it
// returns zero, and the caller looks at err when it needs a value it can
// trust, so that a loop ends at once.
type decoder struct {
	b   []byte
	off int
	err error
}

func (d *decoder) fail(format string, args ...any) {
	if d.err == nil {
		d.err = fmt.Errorf("%w: %s (at byte %d)", ErrBadSnapshot, fmt.Sprintf(format, args...), d.off)
	}
}

func (d *decoder) u8() byte {
	if d.err != nil {
		return 0
	}
	if d.off >= len(d.b) {
		d.fail("truncated")
		return 0
	}
	v := d.b[d.off]
	d.off++
	return v
}

func (d *decoder) bool() bool {
	switch v := d.u8(); v {
	case 0:
		return false
	case 1:
		return true
	default:
		d.fail("boolean %d", v)
		return false
	}
}

// uv reads a varint of its shortest form.
func (d *decoder) uv() uint64 {
	var v uint64
	for i := 0; i < 10; i++ {
		b := d.u8()
		if d.err != nil {
			return 0
		}
		if i == 9 && b > 1 {
			d.fail("number over 64 bits")
			return 0
		}
		v |= uint64(b&0x7f) << (7 * i)
		if b < 0x80 {
			if i > 0 && b == 0 {
				d.fail("number not in its shortest form")
				return 0
			}
			return v
		}
	}
	d.fail("number over 64 bits")
	return 0
}

// in reads a varint that must be in lo..hi.
func (d *decoder) in(what string, lo, hi int) int {
	v := d.uv()
	if d.err != nil {
		return lo
	}
	if v < uint64(lo) || v > uint64(hi) {
		d.fail("%s %d outside %d..%d", what, v, lo, hi)
		return lo
	}
	return int(v)
}

// str reads a string of lo..hi bytes.
func (d *decoder) str(what string, lo, hi int) string {
	n := d.in(what+" length", lo, hi)
	if d.err != nil {
		return ""
	}
	if n > len(d.b)-d.off {
		d.fail("truncated")
		return ""
	}
	s := string(d.b[d.off : d.off+n])
	d.off += n
	return s
}

func (d *decoder) colour() color.Color {
	switch tag := d.u8(); tag {
	case colNone:
		return nil
	case colBasic:
		if v := d.u8(); v > 15 {
			d.fail("basic colour %d", v)
		} else {
			return ansi.BasicColor(v)
		}
	case colIndexed:
		return ansi.IndexedColor(d.u8())
	case colRGBA:
		return color.RGBA{R: d.u8(), G: d.u8(), B: d.u8(), A: d.u8()}
	case colCMYK:
		return color.CMYK{C: d.u8(), M: d.u8(), Y: d.u8(), K: d.u8()}
	case colAlpha:
		hi := d.u8()
		return color.Alpha16{A: uint16(hi)<<8 | uint16(d.u8())}
	default:
		if d.err == nil {
			d.fail("colour tag %d", tag)
		}
	}
	return nil
}

// optionalColour reads a session colour: the 16 bit value, as RGBA reports it.
func (d *decoder) optionalColour() color.Color {
	if !d.bool() {
		return nil
	}
	var v [4]uint16
	for i := range v {
		hi := d.u8()
		v[i] = uint16(hi)<<8 | uint16(d.u8())
	}
	return color.RGBA64{R: v[0], G: v[1], B: v[2], A: v[3]}
}

func (d *decoder) style() uv.Style {
	var s uv.Style
	s.Attrs = d.u8()
	if u := d.in("underline", 0, 5); d.err == nil {
		s.Underline = ansi.Underline(u)
	}
	s.Fg = d.colour()
	s.Bg = d.colour()
	s.UnderlineColor = d.colour()
	return s
}

// decodeSnapshot parses and checks a snapshot. It allocates at most what a
// pane of the largest size holds, whatever the input says.
func decodeSnapshot(b []byte) (st vt.State, title string, err error) {
	if len(b) > MaxSnapshotBytes {
		return st, "", fmt.Errorf("%w: %d bytes, at most %d", ErrBadSnapshot, len(b), MaxSnapshotBytes)
	}
	d := &decoder{b: b}
	if v := d.u8(); d.err == nil && v != snapshotVersion {
		d.fail("version %d, want %d", v, snapshotVersion)
	}
	cols := d.in("columns", MinCols, MaxCols)
	rows := d.in("rows", MinRows, MaxRows)
	st.Width, st.Height = cols, rows
	flags := d.u8()
	if flags&^3 != 0 {
		d.fail("flags %#x", flags)
	}
	st.Alt, st.AtPhantom = flags&1 != 0, flags&2 != 0
	st.GL = d.in("GL", 0, 3)
	st.GR = d.in("GR", 0, 3)
	st.GSingle = d.in("single shift", 0, 3)
	if st.GSingle == 1 {
		d.fail("single shift 1")
	}
	for i := range st.Charsets {
		st.Charsets[i] = vt.CharSetID(d.in("character set", 0, 2))
	}
	st.LastChar = rune(d.in("last character", 0, 0x7e))
	if st.LastChar != 0 && st.LastChar < 0x20 {
		d.fail("last character %d", st.LastChar)
	}
	st.Foreground = d.optionalColour()
	st.Background = d.optionalColour()
	st.CursorColor = d.optionalColour()

	nModes := d.in("modes", 0, len(recordedModes))
	prev := modeKey{}
	for i := 0; i < nModes && d.err == nil; i++ {
		kind := d.in("mode kind", 0, 1)
		k := modeKey{kind == 1, d.in("mode number", 0, 1<<16)}
		setting := ansi.ModeSetting(d.in("mode setting", 1, 2))
		if d.err != nil {
			break
		}
		if !recordedModes[k] {
			d.fail("mode %v is not recorded", k)
			break
		}
		if i > 0 && (prev.dec && !k.dec || prev.dec == k.dec && prev.n >= k.n) {
			d.fail("modes out of order at %v", k)
			break
		}
		prev = k
		var mode ansi.Mode = ansi.ANSIMode(k.n)
		if k.dec {
			mode = ansi.DECMode(k.n)
		}
		st.Modes = append(st.Modes, vt.ModeState{Mode: mode, Setting: setting})
	}

	for i := 0; i < (cols+7)/8 && d.err == nil; i++ {
		v := d.u8()
		for bit := 0; bit < 8; bit++ {
			if v&(1<<bit) == 0 {
				continue
			}
			if x := i*8 + bit; x < cols {
				st.TabStops = append(st.TabStops, x)
			} else {
				d.fail("tab stop %d outside the screen", x)
			}
		}
	}

	if d.bool() {
		o := &vt.OpenClusterState{}
		o.Pos = uv.Pos(d.in("open cluster column", 0, cols-1), d.in("open cluster row", 0, rows-1))
		o.Single = d.in("open cluster single shift", 0, 3)
		if o.Single == 1 {
			d.fail("open cluster single shift 1")
		}
		o.Capped = d.bool()
		o.Text = d.str("open cluster", 1, vt.MaxClusterBytes)
		if d.err == nil && !utf8.ValidString(o.Text) {
			d.fail("open cluster is not UTF-8")
		}
		st.OpenCluster = o
	}

	title = d.str("title", 0, MaxTitle)
	if d.err == nil {
		if clean, cut := cleanTitle([]byte(title)); cut || clean != title {
			d.fail("title is not a clean title")
		}
	}

	nStyles := d.in("styles", 0, maxStyles)
	if d.err == nil && nStyles*5 > len(b)-d.off {
		d.fail("truncated")
	}
	styles := make([]uv.Style, 1, 1+max(nStyles, 0))
	seen := make(map[styleKey]bool, nStyles)
	for i := 0; i < nStyles && d.err == nil; i++ {
		start := d.off
		s := d.style()
		if d.err != nil {
			break
		}
		var key styleKey
		copy(key[:], d.b[start:d.off])
		if key == (styleKey{}) || seen[key] {
			d.fail("style %d is empty or repeated", i+1)
			break
		}
		seen[key] = true
		styles = append(styles, s)
	}

	for i := range st.Screens {
		if d.err != nil {
			break
		}
		st.Screens[i] = d.screen(cols, rows, styles)
	}
	if d.err == nil && d.off != len(b) {
		d.fail("%d trailing bytes", len(b)-d.off)
	}
	if d.err != nil {
		return vt.State{}, "", d.err
	}
	return st, title, nil
}

func (d *decoder) cursor(cols, rows int, styles []uv.Style) vt.Cursor {
	var c vt.Cursor
	c.X = d.in("cursor column", 0, cols-1)
	c.Y = d.in("cursor row", 0, rows-1)
	c.Style = vt.CursorStyle(d.in("cursor shape", 0, 1<<31-1))
	flags := d.u8()
	if flags&^3 != 0 {
		d.fail("cursor flags %#x", flags)
	}
	c.Steady, c.Hidden = flags&1 != 0, flags&2 != 0
	pen := d.in("pen style", 0, len(styles)-1)
	if d.err == nil {
		c.Pen = styles[pen]
	}
	c.Link.URL = d.str("pen link URL", 0, vt.MaxLinkURL)
	c.Link.Params = d.str("pen link parameters", 0, vt.MaxLinkParams)
	if d.err == nil && c.Link.URL == "" && c.Link.Params != "" {
		d.fail("parameters of a link with no URL")
	}
	return c
}

func (d *decoder) screen(cols, rows int, styles []uv.Style) vt.ScreenState {
	var sc vt.ScreenState
	minX := d.in("scroll region", 0, cols-1)
	minY := d.in("scroll region", 0, rows-1)
	maxX := d.in("scroll region", minX+1, cols)
	maxY := d.in("scroll region", minY+1, rows)
	sc.Scroll = uv.Rect(minX, minY, maxX-minX, maxY-minY)
	sc.Cursor = d.cursor(cols, rows, styles)
	sc.Saved = d.cursor(cols, rows, styles)

	nLinks := d.in("links", 0, vt.MaxLiveLinks)
	for i := 0; i < nLinks && d.err == nil; i++ {
		l := uv.Link{
			URL:    d.str("link URL", 1, vt.MaxLinkURL),
			Params: d.str("link parameters", 0, vt.MaxLinkParams),
		}
		if i > 0 && d.err == nil {
			p := sc.Links[i-1]
			if p.URL > l.URL || p.URL == l.URL && p.Params >= l.Params {
				d.fail("links out of order")
			}
		}
		sc.Links = append(sc.Links, l)
	}
	sc.LinkWait = d.in("link wait", 0, maxLinkWait)

	cells := make([]uv.Cell, cols*rows)
	for k := 0; k < len(cells) && d.err == nil; {
		run := d.in("run", 1, len(cells)-k)
		content := d.str("cell content", 0, vt.MaxClusterBytes)
		if d.err == nil && !utf8.ValidString(content) {
			d.fail("cell content is not UTF-8")
		}
		width := d.in("cell width", 0, maxCellWidth)
		style := d.in("cell style", 0, len(styles)-1)
		link := d.in("cell link", 0, len(sc.Links))
		if d.err != nil {
			break
		}
		c := uv.Cell{Content: content, Width: width, Style: styles[style]}
		if link > 0 {
			c.Link = sc.Links[link-1]
		}
		for i := 0; i < run; i++ {
			cells[k+i] = c
		}
		k += run
	}
	sc.Cells = cells
	return sc
}
