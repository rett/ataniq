package pager

import (
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

// maxCellBytes is the most bytes of text the pager puts in one cell. A
// character with more combining marks than that is spread over several
// cells, so that a line cannot pile megabytes onto one cell. Nothing is
// dropped. 64 bytes is the limit the session pane uses for a cell.
const maxCellBytes = 64

// unitLook is how many bytes of a text are looked at to find the piece for
// one cell.
const unitLook = 4 * maxCellBytes

// wrapper measures escaped text for a row of width cells. With graphemes
// set the outer terminal measures whole grapheme clusters (mode 2027);
// without it the terminal measures code point by code point, and so does
// the wrapper.
type wrapper struct {
	width     int
	graphemes bool
}

// unit returns the first piece of s that is drawn in one cell: its length in
// bytes and its width in cells, 1 or more. carrier is true when the piece
// has no width of its own (combining marks with nothing before them): it is
// then drawn after a space, so that it does not join the cell to its left.
// s is not empty and is valid UTF-8.
func (w wrapper) unit(s string) (n, cells int, carrier bool) {
	if s[0] < utf8.RuneSelf && (len(s) == 1 || s[1] < utf8.RuneSelf) {
		return 1, 1, false
	}
	// Look at the start of s only. A cluster that is longer than a cell
	// may hold is cut to that below, so its full length does not matter,
	// and a character under megabytes of marks is not measured whole for
	// every cell.
	if len(s) > unitLook {
		cut := unitLook
		for !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	c, cells := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
	n = len(c)
	if n == 0 {
		_, n = utf8.DecodeRuneInString(s)
		return n, 1, false
	}
	if !w.graphemes && ansi.WcWidth.StringWidth(c) != cells {
		// The outer terminal would give this cluster another width. Take
		// the first code point with the marks that follow it, which is what
		// that terminal puts in one cell.
		_, n = utf8.DecodeRuneInString(c)
		cells = ansi.WcWidth.StringWidth(c[:n])
		for n < len(c) {
			_, size := utf8.DecodeRuneInString(c[n:])
			if ansi.WcWidth.StringWidth(c[n:n+size]) != 0 {
				break
			}
			n += size
		}
	}
	if n > maxCellBytes {
		_, first := utf8.DecodeRuneInString(s)
		cut := first
		for cut < n {
			_, size := utf8.DecodeRuneInString(s[cut:])
			if cut+size > maxCellBytes {
				break
			}
			cut += size
		}
		n = cut
	}
	if cells <= 0 {
		return n, 1, true
	}
	return n, cells, false
}

// rowEnd returns where the row that starts at off ends in text: the offset
// of the next row, or len(text). A row holds as many units as fit the width
// and at least one, so the rows of a line hold all of it.
func (w wrapper) rowEnd(text string, off int) int {
	used := 0
	for off < len(text) {
		n, cells, _ := w.unit(text[off:])
		if used > 0 && used+cells > w.width {
			break
		}
		used += cells
		off += n
	}
	return off
}
