package sessionpane

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
)

// MaxRecentRowBytes is the most bytes an encoded row of recent output has:
// the bound of the format for a row of MaxCols cells, each with a cluster of
// vt.MaxClusterBytes bytes and a style of its own (the style table of a row
// is then 500 styles of 17 bytes, the segments 500 pairs of two-byte
// numbers, and each cell a two-byte header and its cluster). A test computes
// the bound and checks it against the constant, which the holder's history
// frame (wire.MaxHistoryRowBytes) has to hold.
const MaxRecentRowBytes = 48 << 10

// ErrBadRecentRow is returned by DecodeRecentRow for bytes that are not a
// row that EncodeRecentRow made, or that hold a value over a limit.
var ErrBadRecentRow = errors.New("sessionpane: invalid recent row")

// The row format, version-less (the frame that carries rows is versioned).
// Integers are unsigned varints of their shortest form; styles are those of
// the snapshot format.
//
//	styles     count (at most MaxCols), then each style
//	cells      count (at most MaxCols)
//	segments   until the cells are all given: style index, cell count (at
//	           least 1), then each cell: a varint of content length times 4
//	           plus width (0 to 2), and the content (at most
//	           vt.MaxClusterBytes bytes of UTF-8)
//
// A row keeps no hyperlink: the viewport that draws it draws none, and a
// history that carried the links would carry URLs that nothing shows. A cell
// with a style the format has no tag for (only the API can make one) is kept
// with the style of nothing set, and a cell with content over the cluster cap
// is kept empty.

// EncodeRecentRow encodes a line of cells compactly. The result is at most
// MaxRecentRowBytes for a line of at most MaxCols cells, and a longer line is
// cut at MaxCols.
func EncodeRecentRow(line uv.Line) []byte {
	if len(line) > MaxCols {
		line = line[:MaxCols]
	}
	var styles [][]byte
	index := map[styleKey]int{}
	idx := make([]int, len(line))
	for i := range line {
		c := &line[i]
		var buf [styleBytes]byte
		enc, ok := appendStyle(buf[:0], &c.Style)
		if !ok {
			enc, _ = appendStyle(buf[:0], &uv.Style{})
		}
		var key styleKey
		copy(key[:], enc)
		n, seen := index[key]
		if !seen {
			n = len(styles)
			index[key] = n
			styles = append(styles, append([]byte(nil), enc...))
		}
		idx[i] = n
	}
	out := appendUvarint(nil, uint64(len(styles)))
	for _, s := range styles {
		out = append(out, s...)
	}
	out = appendUvarint(out, uint64(len(line)))
	for i := 0; i < len(line); {
		j := i + 1
		for j < len(line) && idx[j] == idx[i] {
			j++
		}
		out = appendUvarint(out, uint64(idx[i]))
		out = appendUvarint(out, uint64(j-i))
		for ; i < j; i++ {
			c := &line[i]
			content, width := c.Content, c.Width
			if len(content) > vt.MaxClusterBytes || !utf8.ValidString(content) {
				content = ""
			}
			if width < 0 || width > maxCellWidth {
				width = 0
			}
			out = appendUvarint(out, uint64(len(content))<<2|uint64(width))
			out = append(out, content...)
		}
	}
	return out
}

// DecodeRecentRow decodes a row that EncodeRecentRow made. The input is
// untrusted: it is decoded strictly and within bounds (at most MaxCols cells,
// a style table of at most MaxCols, content of at most 64 bytes of UTF-8 in a
// cell, no byte left over) and a violation is an error wrapping
// ErrBadRecentRow, never a panic. The cells carry no hyperlink.
func DecodeRecentRow(b []byte) (uv.Line, error) {
	if len(b) > MaxRecentRowBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrBadRecentRow, len(b))
	}
	d := &decoder{b: b}
	nStyles := d.in("styles", 0, MaxCols)
	styles := make([]uv.Style, 0, nStyles)
	for i := 0; i < nStyles && d.err == nil; i++ {
		styles = append(styles, d.style())
	}
	nCells := d.in("cells", 0, MaxCols)
	if d.err == nil && (nCells == 0) != (nStyles == 0) {
		d.fail("%d styles for %d cells", nStyles, nCells)
	}
	line := make(uv.Line, 0, nCells)
	for len(line) < nCells && d.err == nil {
		style := d.in("segment style", 0, nStyles-1)
		n := d.in("segment cells", 1, nCells-len(line))
		for i := 0; i < n && d.err == nil; i++ {
			head := d.in("cell header", 0, vt.MaxClusterBytes<<2|maxCellWidth)
			width, size := head&3, head>>2
			if d.err == nil && (width > maxCellWidth || size > vt.MaxClusterBytes) {
				d.fail("cell header %d", head)
			}
			if d.err == nil && size > len(d.b)-d.off {
				d.fail("truncated")
			}
			if d.err != nil {
				break
			}
			content := string(d.b[d.off : d.off+size])
			d.off += size
			if !utf8.ValidString(content) {
				d.fail("cell content is not UTF-8")
				break
			}
			line = append(line, uv.Cell{Content: content, Width: width, Style: styles[style]})
		}
	}
	if d.err == nil && d.off != len(d.b) {
		d.fail("%d bytes left over", len(d.b)-d.off)
	}
	if d.err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBadRecentRow, strings.TrimPrefix(d.err.Error(), ErrBadSnapshot.Error()+": "))
	}
	return line, nil
}
