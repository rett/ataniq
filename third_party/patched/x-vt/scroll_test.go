package vt

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// Insert and delete of lines in a region as wide as the screen move the lines
// and not their cells (Screen.moveLines). The result is the one of the
// buffer's own functions, which copy the cells one at a time: for any screen
// the property streams make, any region, any position of the cursor in it,
// any count (also over the lines left) and a pen with a background or
// without, the two screens hold the same cells.
func TestMovingLinesIsWhatCopyingCellsGives(t *testing.T) {
	compared := 0
	for seed := uint64(1); seed <= 150; seed++ {
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
	if compared < 800 {
		t.Errorf("%d scrolls compared", compared)
	}
}

// The ways of scrolling the largest screen (500 by 200 cells) cost the lines
// of the region and not their cells: the cells of every line were copied
// before, 11 MB and 1.1 ms for each line feed at the bottom, and 64 KiB of
// line feeds took over a minute. Now 64 KiB of any of them takes a few
// seconds at most. The sequences that clear the whole region (a scroll by the
// screen, a count over the lines left) write every cell of the screen, 11
// MB, which takes about a millisecond whatever the code: 4 KiB of them are
// sent. The time of each is logged.
func TestScrollingTheLargestScreenIsBounded(t *testing.T) {
	ceiling := 20 * time.Second
	if raceEnabled {
		ceiling = 60 * time.Second
	}
	for _, tc := range []struct {
		name   string
		unit   string
		budget int
	}{
		{"line feed", "\n", 64 << 10},
		{"index", "\x1bD", 64 << 10},
		{"reverse index", "\x1bM", 64 << 10},
		{"insert line", "\x1b[L", 64 << 10},
		{"delete line", "\x1b[M", 64 << 10},
		{"scroll up", "\x1b[S", 64 << 10},
		{"scroll down", "\x1b[T", 64 << 10},
		{"scroll up by the screen", "\x1b[200S", 4 << 10},
		{"delete more lines than there are", "\x1b[2147483646M", 4 << 10},
		{"insert more lines than there are", "\x1b[2147483646L", 4 << 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget := tc.budget
			if raceEnabled {
				budget /= 8
			}
			e := NewEmulator(500, 200)
			e.SetScrollbackSize(0)
			e.Write([]byte(strings.Repeat("x", 500*200) + "\x1b[1;1H"))
			input := []byte(strings.Repeat(tc.unit, budget/len(tc.unit)))
			start := time.Now()
			done := make(chan struct{})
			go func() {
				defer close(done)
				e.Write(input)
			}()
			select {
			case <-done:
			case <-time.After(ceiling):
				t.Fatalf("%d bytes did not return in %v", len(input), ceiling)
			}
			t.Logf("%d bytes (%d sequences): %v", len(input), len(input)/len(tc.unit), time.Since(start))
		})
	}
}
