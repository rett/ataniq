package vt

import (
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// printableASCII reports whether r is a character that can never join the
// grapheme cluster beside it. No ASCII character is a combining mark, a ZWJ, a
// spacing mark or a regional indicator, and CR and LF are not printable, so a
// cluster boundary between two of these is certain without asking the
// segmenter.
func printableASCII(r rune) bool {
	return r >= ansi.SP && r < ansi.DEL
}

// handlePrint handles printable characters.
//
// A character is held back rather than printed straight away, because the one
// after it may be a combining mark that belongs to the same cluster. Printing
// "e" from "e\u0301" before the mark arrives puts the base in one cell and
// leaves the mark to become a zero-width cell of its own, which the next write
// or erase then destroys.
func (e *Emulator) handlePrint(r rune) {
	// A Write that ended inside a cluster drew it as it stood. If this
	// character continues that cluster, take the cluster back and redraw it
	// in the same place once it is complete.
	if e.open.ok && e.reopenCluster(r) {
		return
	}

	// No ASCII character can extend the cluster before it: none of them is
	// Extend, ZWJ, SpacingMark, Prepend, a regional indicator, or part of an
	// Indic conjunct. So an ASCII character arriving is proof that whatever is
	// buffered is finished, and the buffer can go out.
	//
	// This is also what keeps the buffer from being re-segmented as it grows.
	// Asking where the clusters are on every character costs the length of the
	// buffer each time, which is quadratic over a long run of combining marks
	// that never resolves into more than one cluster.
	if printableASCII(r) {
		// Two printable ASCII characters in a row is the common case by a wide
		// margin, and needs no segmenting at all.
		if len(e.grapheme) == 1 && printableASCII(rune(e.grapheme[0])) {
			e.handleGrapheme(string(e.grapheme[0]), 1)
			e.grapheme = utf8.AppendRune(e.grapheme[:0], r)
			return
		}
		e.flushGrapheme()
	}

	e.appendGrapheme(r)
}

// appendGrapheme adds r to the grapheme buffer, which never holds more than
// [MaxClusterBytes] bytes. The buffer collects text until a character that
// cannot extend it arrives, so it holds several clusters when a run has no
// ASCII in it, and one long one when a run is made of combining marks. A rune
// that does not fit settles the buffer first.
func (e *Emulator) appendGrapheme(r rune) {
	var enc [utf8.UTFMax]byte
	n := utf8.EncodeRune(enc[:], r)
	if len(e.grapheme)+n > MaxClusterBytes {
		e.settleGrapheme(enc[:n])
		return
	}
	e.grapheme = append(e.grapheme, enc[:n]...)
}

// settleGrapheme makes room in a full grapheme buffer for the rune whose
// bytes are enc. If the buffer is one cluster and the rune extends it, the
// rune is dropped and its bytes are counted: a cluster keeps its first
// [MaxClusterBytes] bytes, cut at a rune boundary. Otherwise it draws every
// cluster of the buffer except the last, which is the only one the rune can
// extend; if the rune starts a new cluster, the last one is drawn too and the
// rune starts the buffer. The work is bounded by the size of the buffer,
// whatever the input.
func (e *Emulator) settleGrapheme(enc []byte) {
	e.open.ok = false
	if e.extendsCluster(e.grapheme, enc) {
		e.counters.ClusterBytesDropped += uint64(len(enc))
		return
	}

	graphemes := e.grapheme
	for len(graphemes) > 0 {
		cluster, width := ansi.FirstGraphemeCluster(graphemes, ansi.GraphemeWidth)
		if len(cluster) == 0 || len(cluster) == len(graphemes) {
			break
		}
		e.handleGrapheme(string(cluster), width)
		graphemes = graphemes[len(cluster):]
	}
	e.grapheme = append(e.grapheme[:0], graphemes...)

	if e.extendsCluster(e.grapheme, enc) {
		if len(e.grapheme)+len(enc) <= MaxClusterBytes {
			e.grapheme = append(e.grapheme, enc...)
		} else {
			e.counters.ClusterBytesDropped += uint64(len(enc))
		}
		return
	}
	e.drawGrapheme(false)
	e.grapheme = append(e.grapheme, enc...)
}

// extendsCluster reports whether the rune with the bytes enc extends text,
// which is one cluster or several, so that the whole of text and the rune form
// one cluster. It costs the length of text, which is at most
// [MaxClusterBytes].
func (e *Emulator) extendsCluster(text, enc []byte) bool {
	joined := append(append(e.scratch[:0], text...), enc...)
	// The method only decides the width reported, not where the cluster ends.
	first, _ := ansi.FirstGraphemeCluster(joined, ansi.WcWidth)
	return len(first) == len(joined)
}

// openCluster is the last cluster a Write drew when it ended, with what is
// needed to take it back: where it was drawn, the cursor and pending-wrap state
// it left, and the single shift it consumed.
type openCluster struct {
	ok         bool
	content    []byte
	drawn      string
	x, y       int
	curX, curY int
	phantom    bool
	single     int
}

// reopenCluster puts the open cluster back into the grapheme buffer and the
// cursor back where the cluster was drawn, when r continues it. A program
// writing to a terminal does not know where its writes are split, and a pty
// read can end anywhere: after a ZWJ, before a variation selector or a skin
// tone modifier, between two regional indicators. Without this, the halves
// are committed as two clusters.
//
// A cluster that has reached [MaxClusterBytes], or that r would take past it,
// is not reopened and not grown: r is dropped and counted, and the cluster
// stays open, so that the marks that follow are dropped too. It reports
// whether it dropped r.
func (e *Emulator) reopenCluster(r rune) (dropped bool) {
	o := &e.open
	o.ok = false
	// No ASCII character continues a cluster (see printableASCII).
	if printableASCII(r) || len(e.grapheme) > 0 {
		return false
	}
	if !e.openStillValid() {
		return false
	}
	var enc [utf8.UTFMax]byte
	n := utf8.EncodeRune(enc[:], r)
	if !e.extendsCluster(o.content, enc[:n]) {
		return false
	}
	if len(o.content)+n > MaxClusterBytes {
		e.counters.ClusterBytesDropped += uint64(n)
		o.ok = true
		return true
	}
	e.scr.setCursor(o.x, o.y, false)
	e.atPhantom = false
	e.gsingle = o.single
	e.grapheme = append(e.grapheme[:0], o.content...)
	return false
}

// openStillValid reports whether the open cluster is still where it was drawn.
// Anything that changed the screen or the cursor in between ends the cluster:
// every sequence handler does so by flushing, and this catches changes made
// through the API, such as a resize.
func (e *Emulator) openStillValid() bool {
	o := &e.open
	if x, y := e.scr.CursorPosition(); x != o.curX || y != o.curY || e.atPhantom != o.phantom {
		return false
	}
	c := e.scr.CellAt(o.x, o.y)
	return c != nil && c.Content == o.drawn
}

// OpenCluster returns the grapheme cluster the last Write ended in, which the
// next Write may still extend, and the cell it was drawn in. ok is false when
// no cluster is open. A program that copies the screen into another emulator
// can write this cluster last, at its cell and with its style, so that the
// copy can be extended by the next Write in the same way.
func (e *Emulator) OpenCluster() (pos uv.Position, cluster string, ok bool) {
	if !e.open.ok || !e.openStillValid() {
		return uv.Position{}, "", false
	}
	return uv.Pos(e.open.x, e.open.y), string(e.open.content), true
}

// flushGrapheme flushes the current grapheme buffer, if any, and handles the
// grapheme as a single unit.
func (e *Emulator) flushGrapheme() {
	e.drawGrapheme(false)
}

// drawGrapheme draws the clusters in the grapheme buffer. With keepOpen, the
// last one is remembered so the next Write can still extend it.
func (e *Emulator) drawGrapheme(keepOpen bool) {
	e.open.ok = false
	if len(e.grapheme) == 0 {
		return
	}

	// XXX: We always use [ansi.GraphemeWidth] here to report accurate widths
	// and it's up to the caller to decide how to handle Unicode vs non-Unicode
	// modes.
	method := ansi.GraphemeWidth
	graphemes := e.grapheme
	for len(graphemes) > 0 {
		cluster, width := ansi.FirstGraphemeCluster(graphemes, method)
		if len(cluster) == 0 {
			break
		}
		single := e.gsingle
		x, y := e.handleGrapheme(string(cluster), width)
		graphemes = graphemes[len(cluster):]
		if keepOpen && len(graphemes) == 0 {
			o := &e.open
			o.ok = true
			o.content = append(o.content[:0], cluster...)
			o.x, o.y = x, y
			// What the cell holds, which is blank when the cluster did not fit.
			o.drawn = ""
			if c := e.scr.CellAt(x, y); c != nil {
				o.drawn = c.Content
			}
			o.curX, o.curY = e.scr.CursorPosition()
			o.phantom = e.atPhantom
			o.single = single
		}
	}
	e.grapheme = e.grapheme[:0] // Reset the grapheme buffer.
}

// handleGrapheme handles UTF-8 graphemes. It returns the cell it drew in.
func (e *Emulator) handleGrapheme(content string, width int) (int, int) {
	awm := e.isModeSet(ansi.ModeAutoWrap)
	cell := uv.Cell{
		Content: content,
		Width:   width,
		Style:   e.scr.cursorPen(),
		Link:    e.scr.cursorLink(),
	}

	x, y := e.scr.CursorPosition()
	if e.atPhantom && awm {
		// moves cursor down similar to [Terminal.linefeed] except it doesn't
		// respects [ansi.LNM] mode.
		// This will reset the phantom state i.e. pending wrap state.
		e.index()
		_, y = e.scr.CursorPosition()
		x = 0
	}

	// A single shift applies to the next character, whatever that character
	// turns out to be. Take it now, so it cannot leak onto the one after this
	// when this is a cluster no charset has a mapping for.
	single := e.gsingle
	e.gsingle = 0

	// Handle character set mappings
	if len(content) == 1 { //nolint:nestif
		var charset CharSet
		c := content[0]
		if single > 1 && single < 4 {
			charset = e.charsets[single]
		} else if c < 128 {
			charset = e.charsets[e.gl]
		} else {
			charset = e.charsets[e.gr]
		}

		if charset != nil {
			if r, ok := charset[c]; ok {
				cell.Content = r
				cell.Width = 1
			}
		}
	}

	if cell.Width == 1 && len(content) == 1 {
		e.lastChar, _ = utf8.DecodeRuneInString(content)
	}

	e.scr.SetCell(x, y, &cell)
	cellX := x

	// Handle phantom state at the end of the line
	e.atPhantom = awm && x >= e.scr.Width()-1
	if !e.atPhantom {
		x += cell.Width
	}

	// NOTE: We don't reset the phantom state here, we handle it up above.
	e.scr.setCursor(x, y, false)
	return cellX, y
}
