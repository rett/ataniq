package qr

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func mustEncode(t testing.TB, text string, l Level) *Code {
	t.Helper()
	c, err := Encode(text, l)
	if err != nil {
		t.Fatalf("Encode(%q, %v): %v", text, l, err)
	}
	return c
}

func TestQuietOf(t *testing.T) {
	for _, c := range []struct{ in, want int }{
		{-5, 4}, {0, 4}, {1, 2}, {2, 2}, {3, 3}, {4, 4}, {9, 9},
	} {
		if got := quietOf(c.in); got != c.want {
			t.Errorf("quietOf(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestHalfBlocksDecodes(t *testing.T) {
	for _, text := range []string{"hi", "https://example.invalid/#abc", strings.Repeat("x", 100)} {
		c := mustEncode(t, text, M)
		for _, quiet := range []int{0, 1, 2, 4, 5} {
			q := quietOf(quiet)
			n := c.Size() + 2*q
			rows := c.HalfBlocks(quiet)
			if len(rows) != (n+1)/2 {
				t.Fatalf("quiet %d: %d rows for %d module rows", quiet, len(rows), n)
			}
			for ry, row := range rows {
				cells := []rune(row)
				if len(cells) != n {
					t.Fatalf("quiet %d row %d: %d cells, want %d", quiet, ry, len(cells), n)
				}
				for x, r := range cells {
					wantTop := c.Module(x-q, 2*ry-q)
					wantBottom := c.Module(x-q, 2*ry+1-q)
					want := map[[2]bool]rune{
						{false, false}: ' ', {true, false}: '▀', {false, true}: '▄', {true, true}: '█',
					}[[2]bool{wantTop, wantBottom}]
					if r != want {
						t.Fatalf("quiet %d row %d cell %d: %q, want %q", quiet, ry, x, r, want)
					}
				}
			}
			// The quiet zone is blank: whole rows above and below, and the
			// columns at both sides of every row.
			for ry, row := range rows {
				cells := []rune(row)
				for x := 0; x < q; x++ {
					if cells[x] != ' ' || cells[n-1-x] != ' ' {
						t.Fatalf("quiet %d row %d: side margin not light", quiet, ry)
					}
				}
				if ry < q/2 && strings.Trim(row, " ") != "" {
					t.Fatalf("quiet %d: row %d of the top margin is not light", quiet, ry)
				}
			}
		}
	}
}

func TestHalfBlocksCharacters(t *testing.T) {
	c := mustEncode(t, "hi", M)
	seen := map[rune]bool{}
	for _, row := range c.HalfBlocks(4) {
		for _, r := range row {
			seen[r] = true
		}
	}
	for _, r := range " ▀▄█" {
		if !seen[r] {
			t.Errorf("no %q in a whole code", r)
		}
	}
	if len(seen) != 4 {
		t.Errorf("characters %v, want exactly the four", seen)
	}
	// Version 1 is 21 modules: with a quiet zone of 4 that is 29 module
	// rows and 15 text rows, the last with a light lower half.
	rows := c.HalfBlocks(4)
	if len(rows) != 15 {
		t.Fatalf("%d rows, want 15", len(rows))
	}
	if strings.ContainsAny(rows[14], "▄█") {
		t.Errorf("last row %q has a dark lower half", rows[14])
	}
	// Module rows 0 to 3 are the quiet zone, so text row 2 shows module
	// rows 4 and 5: the top of the finder over its second row, a dark
	// module at each end and light between.
	if want := "    █▀▀▀▀▀█"; !strings.HasPrefix(rows[2], want) {
		t.Errorf("row 2 is %q, want it to begin %q", rows[2], want)
	}
	if strings.Trim(rows[0], " ") != "" || strings.Trim(rows[1], " ") != "" {
		t.Error("the top quiet zone is not blank")
	}
}

func TestPNGDecodes(t *testing.T) {
	c := mustEncode(t, "https://example.invalid/#abcdef", M)
	for _, tc := range []struct{ scale, quiet int }{{1, 4}, {8, 4}, {3, 2}, {5, 0}, {2, 1}} {
		data := c.PNG(tc.scale, tc.quiet)
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("scale %d quiet %d: %v", tc.scale, tc.quiet, err)
		}
		q := quietOf(tc.quiet)
		n := c.Size() + 2*q
		if got := img.Bounds(); got != image.Rect(0, 0, n*tc.scale, n*tc.scale) {
			t.Fatalf("scale %d quiet %d: bounds %v", tc.scale, tc.quiet, got)
		}
		for y := 0; y < n*tc.scale; y++ {
			for x := 0; x < n*tc.scale; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				want := color.White
				if c.Module(x/tc.scale-q, y/tc.scale-q) {
					want = color.Black
				}
				wr, wg, wb, _ := want.RGBA()
				if r != wr || g != wg || b != wb {
					t.Fatalf("scale %d quiet %d: pixel (%d,%d) is %v", tc.scale, tc.quiet, x, y, img.At(x, y))
				}
			}
		}
	}
}

func TestPNGScaleBounds(t *testing.T) {
	c := mustEncode(t, "a", L)
	for _, s := range []int{-3, 0} {
		img, err := png.DecodeConfig(bytes.NewReader(c.PNG(s, 4)))
		if err != nil || img.Width != c.Size()+8 {
			t.Errorf("scale %d: %v, %+v", s, err, img)
		}
	}
	img, err := png.DecodeConfig(bytes.NewReader(c.PNG(1000, 4)))
	if err != nil || img.Width != (c.Size()+8)*MaxScale {
		t.Errorf("scale 1000: %v, %+v", err, img)
	}
}

func TestRenderDeterministic(t *testing.T) {
	a := mustEncode(t, "https://example.invalid/#abc", M)
	b := mustEncode(t, "https://example.invalid/#abc", M)
	if !bytes.Equal(a.PNG(4, 4), b.PNG(4, 4)) {
		t.Error("PNG differs between two encodings")
	}
	if strings.Join(a.HalfBlocks(4), "\n") != strings.Join(b.HalfBlocks(4), "\n") {
		t.Error("HalfBlocks differs between two encodings")
	}
}

func TestModuleOutside(t *testing.T) {
	c := mustEncode(t, "a", M)
	for _, p := range [][2]int{{-1, 0}, {0, -1}, {c.Size(), 0}, {0, c.Size()}, {1 << 20, 1 << 20}} {
		if c.Module(p[0], p[1]) {
			t.Errorf("Module%v is dark outside the code", p)
		}
	}
	if !c.Module(0, 0) || !c.Module(c.Size()-1, 0) || !c.Module(0, c.Size()-1) {
		t.Error("a finder corner is light")
	}
}
