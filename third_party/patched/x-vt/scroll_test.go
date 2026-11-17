package vt

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

// Insert and delete of lines in a region as wide as the screen move the lines
// and not their cells (Screen.moveLines). The result is the one of the
// buffer's own functions, which copy the cells one at a time: for any screen
// the property streams make, any region, any position of the cursor in it,
// any count (also over the lines left) and a pen with a background or
// without, the two screens hold the same cells.
func TestMovingLinesIsWhatCopyingCellsGives(t *testing.T) {
	compared := 0
	seeds := uint64(150)
	if raceEnabled {
		seeds = 50 // the race detector is slow
	}
	for seed := uint64(1); seed <= seeds; seed++ {
		rng := rand.New(rand.NewPCG(seed, 13))
		w, h, ops := stateStream(rng, 10+rng.IntN(40))
		a := NewEmulator(w, h)
		for _, op := range ops {
			applyStateOp(a, op)
		}
		for try := 0; try < 6; try++ {
			top, bottom := 1+rng.IntN(h), 1+rng.IntN(h+2)
			a.Write([]byte(fmt.Sprintf("\x1b[%d;%dr", top, bottom)))
			if rng.IntN(2) == 0 {
				a.Write([]byte("\x1b[44m"))
			} else {
				a.Write([]byte("\x1b[49m"))
			}
			// The copy has what a has, and the cursor is where the scroll is
			// done.
			b := NewEmulator(2, 2)
			if err := b.SetState(a.State()); err != nil {
				t.Fatal(err)
			}
			n, up := 1+rng.IntN(h+3), rng.IntN(2) == 0
			a.scr.setCursor(rng.IntN(a.Width()), rng.IntN(a.Height()), false)
			x, y := a.scr.CursorPosition()
			b.scr.setCursor(x, y, false)
			if up {
				a.scr.DeleteLine(n)
			} else {
				a.scr.InsertLine(n)
			}
			// The reference: the buffer's own functions, which a region that
			// is not as wide as the screen still uses.
			scroll := b.scr.scroll
			if y >= scroll.Min.Y && y < scroll.Max.Y && x >= scroll.Min.X && x < scroll.Max.X {
				if up {
					b.scr.buf.DeleteLineArea(y, n, b.scr.blankCell(), scroll)
				} else {
					b.scr.buf.InsertLineArea(y, n, b.scr.blankCell(), scroll)
				}
			}
			if d := stateDiff(a.State(), b.State()); d != "" {
				t.Fatalf("seed %d try %d (screen %dx%d, region %v, cursor %d,%d, n %d, up %v, pen %v): %s", seed, try, a.Width(), a.Height(), a.scr.scroll, x, y, n, up, a.scr.cur.Pen, d)
			}
			compared++
		}
	}
	t.Logf("%d scrolls compared", compared)
	if compared < int(seeds)*5 {
		t.Errorf("%d scrolls compared", compared)
	}
}

// The ways of scrolling the largest screen (500 by 200 cells) cost the lines
// of the region and not their cells: the cells of every line were copied
// before, 11 MB and 1.1 ms for each line feed at the bottom, and 64 KiB of
// line feeds took over a minute. A scroll that blanks the whole region (a
// scroll by the screen, a count over the lines left) wrote every cell, which
// took a millisecond whatever the code, and the lines are marked now and not
// written (patch 14). Each takes at most about twenty times what line feeds
// take, byte for byte (see timing_test.go).
func TestScrollingTheLargestScreenIsBounded(t *testing.T) {
	kib := lineFeedKiB(t)
	for _, tc := range []struct {
		name  string
		setup string
		unit  string
	}{
		{"line feed", "", "\n"},
		{"index", "", "\x1bD"},
		{"reverse index", "", "\x1bM"},
		{"insert line", "", "\x1b[L"},
		{"delete line", "", "\x1b[M"},
		{"scroll up", "", "\x1b[S"},
		{"scroll down", "", "\x1b[T"},
		{"scroll up by the screen", "", "\x1b[200S"},
		{"scroll down by the screen", "", "\x1b[200T"},
		{"scroll by the screen with a background", "\x1b[44m", "\x1b[200S"},
		{"scroll by the screen with a background that changes", "", "\x1b[41m\x1b[200S\x1b[42m\x1b[200T"},
		{"scroll up by the screen in a region", "\x1b[2;199r", "\x1b[200S"},
		{"scroll down by the screen in a region", "\x1b[2;199r", "\x1b[200T"},
		{"scroll up by the screen in a region with margins", "\x1b[?69h\x1b[2;499s", "\x1b[200S"},
		{"scroll down by the screen in a region with margins", "\x1b[?69h\x1b[2;499s", "\x1b[200T"},
		{"margins and scroll", "", "\x1b[?69h\x1b[2;499s\x1b[200S"},
		{"delete more lines than there are", "", "\x1b[2147483646M"},
		{"insert more lines than there are", "", "\x1b[2147483646L"},
		{"delete more lines than there are in a region with margins", "\x1b[?69h\x1b[2;499s", "\x1b[2147483646M"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget := 16 << 10
			if raceEnabled {
				budget /= 8 // the race detector is slow
			}
			e := timingScreen()
			e.Write([]byte(tc.setup))
			input := []byte(strings.Repeat(tc.unit, budget/len(tc.unit)))
			checkRelative(t, writeBounded(t, e, input), len(input), kib)
		})
	}
}
