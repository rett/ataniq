package pager

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"ataniq/internal/exchange/leadtext"
)

// wrapSamples are escaped texts with what makes wrapping hard: wide
// characters, marks with and without a character before them, clusters the
// two ways of measuring disagree on, and a character with more marks than a
// cell may hold.
var wrapSamples = []string{
	"",
	"a",
	"plain ascii text that is longer than any of the widths tried here",
	"日本語のテキスト、全角の文字が並ぶ行",
	"a日b本c語d",
	"́́ starts with marks",
	"éééééé",
	"👍🏽👍🏽 🇩🇪🇩🇪 가가",
	"x" + strings.Repeat("́", 1000) + "y",
	strings.Repeat("́", 300),
	leadtext.Escape("\x1b[31m‮\xff​ tail"),
}

func TestRowsHoldTheWholeLine(t *testing.T) {
	for _, graphemes := range []bool{false, true} {
		for width := 2; width <= 9; width++ {
			w := wrapper{width: width, graphemes: graphemes}
			for _, s := range wrapSamples {
				var joined strings.Builder
				for off := 0; off < len(s); {
					end := w.rowEnd(s, off)
					if end <= off {
						t.Fatalf("width %d, %q: the row at %d is empty", width, s, off)
					}
					cells := 0
					for u := off; u < end; {
						n, c, _ := w.unit(s[u:])
						if n <= 0 || c < 1 || n > maxCellBytes {
							t.Fatalf("width %d, %q: unit at %d has %d bytes and %d cells", width, s, u, n, c)
						}
						u, cells = u+n, cells+c
					}
					if cells > width {
						t.Fatalf("width %d, %q: the row at %d takes %d cells", width, s, off, cells)
					}
					joined.WriteString(s[off:end])
					off = end
				}
				if joined.String() != s {
					t.Fatalf("width %d, %q: the rows hold %q", width, s, joined.String())
				}
			}
		}
	}
}

func TestUnits(t *testing.T) {
	g, c := wrapper{width: 80, graphemes: true}, wrapper{width: 80}
	for _, tc := range []struct {
		name    string
		w       wrapper
		s       string
		n       int
		cells   int
		carrier bool
	}{
		{"ascii", c, "ab", 1, 1, false},
		{"ascii before a mark", c, "éx", 3, 1, false},
		{"wide", c, "日本", 3, 2, false},
		{"marks alone are carried by a space", c, "́́x", 4, 1, true},
		{"a cluster the terminal measures whole", g, "👍🏽x", 8, 2, false},
		{"the same, code point by code point", c, "👍🏽x", 4, 2, false},
		{"its second code point", c, "🏽x", 4, 2, false},
	} {
		n, cells, carrier := tc.w.unit(tc.s)
		if n != tc.n || cells != tc.cells || carrier != tc.carrier {
			t.Errorf("%s: unit(%q) = %d bytes, %d cells, carrier %v; want %d, %d, %v", tc.name, tc.s, n, cells, carrier, tc.n, tc.cells, tc.carrier)
		}
	}
}

// A character with a thousand marks is spread over cells of at most
// maxCellBytes bytes. The first holds the character; the others hold marks
// only and are carried by a space.
func TestManyMarksAreSpreadOverCells(t *testing.T) {
	s := "x" + strings.Repeat("́", 1000)
	w := wrapper{width: 80, graphemes: true}
	units := 0
	for off := 0; off < len(s); units++ {
		n, cells, carrier := w.unit(s[off:])
		if n > maxCellBytes || cells != 1 || carrier != (units > 0) {
			t.Fatalf("unit %d: %d bytes, %d cells, carrier %v", units, n, cells, carrier)
		}
		off += n
	}
	// "x" with 31 marks, then 969 marks in cells of 32.
	if units != 32 {
		t.Fatalf("%d units, want 32", units)
	}
}

func TestWrapMarkIsOneCell(t *testing.T) {
	if ansi.StringWidth(wrapMark) != 1 || ansi.WcWidth.StringWidth(wrapMark) != 1 {
		t.Fatalf("the wrap mark is not one cell under both ways of measuring")
	}
}
