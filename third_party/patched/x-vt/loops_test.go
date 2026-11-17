package vt

import (
	"fmt"
	"testing"
	"time"
)

// A parameter is a number of up to two billion, and four sequences looped
// that many times: a tab forward or backward that had reached the last tab
// stop went on, an erase of characters filled past the edge, and a repeat of
// the last character printed it two billion times. One sequence of eight
// bytes kept the process busy for minutes, and the pane's lock with it. Each
// is now bounded, and means what it meant.
func TestSequencesWithHugeCountsReturn(t *testing.T) {
	const huge = "2147483646"
	for _, tc := range []struct {
		name  string
		seq   string // %s is the count
		same  string // the count that gives the same result
		setup string
	}{
		{"CHT", "\x1b[%sI", "500", "abc\x1b[3;5H"},
		{"CHT with tab stops cleared", "\x1b[%sI", "500", "\x1b[3g\x1b[3;5H\x1bH\x1b[3;40H\x1bH\x1b[3;5H"},
		{"CHT at the last column", "\x1b[%sI", "500", "\x1b[3;80H"},
		{"CBT", "\x1b[%sZ", "500", "abc\x1b[3;70H"},
		{"CBT at the first column", "\x1b[%sZ", "500", "\x1b[3;1H"},
		{"CBT with the left margin", "\x1b[%sZ", "500", "\x1b[?69h\x1b[10;50s\x1b[?6h\x1b[3;30H"},
		{"ECH", "\x1b[%sX", "500", "abcdefgh\x1b[3;1Hwxyz\x1b[3;3H"},
		{"ECH at the last column", "\x1b[%sX", "500", "\x1b[3;80Hz\x1b[3;80H"},
		{"ECH over a wide character", "\x1b[%sX", "500", "世界\x1b[3;1H世界\x1b[3;2H"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := func(count string) State {
				e := NewEmulator(80, 24)
				e.Write([]byte(tc.setup))
				done := make(chan struct{})
				go func() {
					defer close(done)
					e.Write([]byte(fmt.Sprintf(tc.seq, count)))
				}()
				select {
				case <-done:
				case <-time.After(20 * time.Second):
					t.Fatalf("count %s: the sequence did not return", count)
				}
				return e.State()
			}
			if d := stateDiff(run(huge), run(tc.same)); d != "" {
				t.Errorf("a count of %s and of %s differ: %s", huge, tc.same, d)
			}
		})
	}
}

// A repeat of more than the cells of the screen is cut to them, and counted.
// The result is the one a repeat of exactly that many gives, which is not
// what a terminal that printed two billion characters would show (the
// cursor column and the last line depend on the count), and is what bounds
// the time.
func TestRepeatIsCutToTheScreen(t *testing.T) {
	const cols, rows = 30, 5
	const cells = cols * rows
	run := func(count int) (State, uint64) {
		e := NewEmulator(cols, rows)
		done := make(chan struct{})
		go func() {
			defer close(done)
			e.Write([]byte(fmt.Sprintf("x\x1b[%db", count)))
		}()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Fatalf("the repeat of %d did not return", count)
		}
		return e.State(), e.Counters().RepeatsCut
	}
	atCap, cut := run(cells)
	if cut != 0 {
		t.Errorf("a repeat of the cells of the screen was counted as cut")
	}
	// The screen is full of the character but for the first cell of the last
	// line, which the scroll made room for.
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			want := "x"
			if y == rows-1 && x > 0 {
				want = " "
			}
			if c := atCap.Screens[0].Cells[y*cols+x]; c.Content != want {
				t.Fatalf("cell %d,%d is %q, want %q", x, y, c.Content, want)
			}
		}
	}
	for _, count := range []int{cells + 1, cells + cols, 1_000_000, 2147483646} {
		st, cut := run(count)
		if cut != 1 {
			t.Errorf("a repeat of %d: RepeatsCut = %d, want 1", count, cut)
		}
		if d := stateDiff(st, atCap); d != "" {
			t.Errorf("a repeat of %d differs from one of %d: %s", count, cells, d)
		}
	}
	for _, count := range []int{1, cols, cells - 1} {
		if _, cut := run(count); cut != 0 {
			t.Errorf("a repeat of %d was counted as cut", count)
		}
	}

	// Below the cap a repeat is what it was.
	e := NewEmulator(10, 2)
	e.Write([]byte("ab\x1b[3b"))
	if got := termText(e)[0]; got != "abbbb     " {
		t.Errorf("a repeat of 3 gave %q", got)
	}
	if e.Counters().RepeatsCut != 0 {
		t.Errorf("counters %+v", e.Counters())
	}
}
