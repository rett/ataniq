package vt

import (
	"fmt"
	"math/rand/v2"
	"strings"
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

// repeatTyped is the reference of a repeat: the character typed again, count
// times, in one write.
func repeatTyped(c byte, count int) []byte {
	return []byte(strings.Repeat(string(c), count))
}

// A repeat is the character typed again: for every count, on small screens
// in every state the property streams reach (any pen, margins, modes, charsets
// and shifts, a scroll region, links, wide characters before it), the state
// after a repeat is the state after the character typed that many times, and
// the same again after the next bytes. A repeat of more than two lines is not
// printed one character at a time where it can be done a line at a time (see
// repeatInBulk), which gives the same state; where it cannot (the pen is under
// a link, the region is not as wide as the screen, the cursor is outside it)
// it is cut to two lines and two characters, counted, and the state is the one
// of that many characters typed.
func TestRepeatEqualsTyping(t *testing.T) {
	prefixes := []string{"", "", "", "\x1b[?7l", "\x1b[41m", "\x1b[?69h\x1b[2;4s", "\x1b[2;4r", "\x1b[2;4r\x1b[3;1H", "\x1b(0", "\x1b]8;;http://a.example/1\x1b\\",
		"\x1b[1;1H", "\x1b[99;99H", "\x1b[3;3H\x1b[?6h\x1b[2;3r", "\x1b)0\x0e", "\x1b[?1049h", "\x1b[7;31m\x1b[1;1H"}
	afters := []string{"", "", "", "", "\x1bN", "\x1bO", "\x1b7\x1b[H\x1b8", "\x1b[5C", "\x1b[3;2H"}
	chars := "abcq_ xlk~"
	var cases, bulk, cutCases int
	var cuts uint64
	seeds := uint64(150)
	if raceEnabled {
		seeds = 30 // the race detector is slow
	}
	for seed := uint64(1); seed <= seeds; seed++ {
		rng := rand.New(rand.NewPCG(seed, 11))
		w, h, ops := stateStream(rng, 10+rng.IntN(40))
		prefix, after := prefixes[rng.IntN(len(prefixes))], afters[rng.IntN(len(afters))]
		c := chars[rng.IntN(len(chars))]
		cells := w * h
		limit := 2*w + 2
		counts := []int{1, 2, 3, w, w + 1, limit, limit + 1, limit + 2, 3 * w, 5*w + 1, cells, cells + w, cells + w + 1,
			2*cells + 3, 3*cells + 2*w + 1, 1 + rng.IntN(8*cells), 1 + rng.IntN(8*cells)}
		// A screen with a scrollback (the default) keeps the lines that scroll
		// off, so a repeat of it is never done a line at a time.
		scrollback := rng.IntN(4) == 0
		build := func() *Emulator {
			e := NewEmulator(w, h)
			if !scrollback {
				e.SetScrollbackSize(0)
			}
			for _, op := range ops {
				applyStateOp(e, op)
			}
			e.Write([]byte(prefix))
			e.Write([]byte{c})
			e.Write([]byte(after))
			return e
		}
		for _, count := range counts {
			a, b := build(), build()
			typed := count
			if limit := 2*a.Width() + 2; count > limit && !a.repeatBulkable() {
				typed = limit
				cutCases++
			} else if count > limit {
				bulk++
			}
			before := a.Counters().RepeatsCut
			a.Write([]byte(fmt.Sprintf("\x1b[%db", count)))
			b.Write(repeatTyped(c, typed))
			cases++
			if d := stateDiff(a.State(), b.State()); d != "" {
				t.Fatalf("seed %d (%dx%d, prefix %q, after %q, %q): a repeat of %d differs from typing %d: %s", seed, w, h, prefix, after, c, count, typed, d)
			}
			cuts += a.Counters().RepeatsCut - before
			// What comes next goes the same way.
			a.Write([]byte("zz\x1b[2b\n"))
			b.Write([]byte("zz\x1b[2b\n"))
			if d := stateDiff(a.State(), b.State()); d != "" {
				t.Fatalf("seed %d (%dx%d, prefix %q, after %q, %q): after a repeat of %d and more output: %s", seed, w, h, prefix, after, c, count, d)
			}
		}
	}
	t.Logf("%d repeats compared with typing: %d done a line at a time, %d cut to two lines, %d counted as cut", cases, bulk, cutCases, cuts)
	if want := int(seeds) * 3 / 2; bulk < want || cutCases < want/5 {
		t.Errorf("the streams reach too few states: %d bulk, %d cut", bulk, cutCases)
	}
}

// The repeat of a count over two lines is counted as cut, however it is done;
// a count of up to two lines and two characters is not.
func TestRepeatCutCounter(t *testing.T) {
	const cols, rows = 30, 5
	limit := 2*cols + 2
	for _, tc := range []struct {
		count int
		cut   uint64
	}{{1, 0}, {cols, 0}, {limit, 0}, {limit + 1, 1}, {cols * rows, 1}, {1_000_000, 1}, {2147483646, 1}} {
		e := NewEmulator(cols, rows)
		e.Write([]byte(fmt.Sprintf("x\x1b[%db", tc.count)))
		if got := e.Counters().RepeatsCut; got != tc.cut {
			t.Errorf("a repeat of %d: RepeatsCut = %d, want %d", tc.count, got, tc.cut)
		}
	}

	// A big repeat leaves every line of the screen but the last full of the
	// character, as the printing of two billion of them does, and the last
	// holds the rest: the characters from the third cell of the second line
	// on are 2147483647 (the typed one and the repeat), which is 71582788
	// lines of 30 and 7 over, and the first line has 2 cells before them, so
	// the last line holds 9.
	e := NewEmulator(cols, rows)
	e.SetScrollbackSize(0)
	e.Write([]byte("abc\x1b[2;3Hx\x1b[2147483646b"))
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			want := "x"
			if y == rows-1 && x >= 9 {
				want = " "
			}
			if c := e.CellAt(x, y); c.Content != want {
				t.Fatalf("cell %d,%d is %q, want %q", x, y, c.Content, want)
			}
		}
	}
	if pos := e.CursorPosition(); pos.X != 9 || pos.Y != rows-1 {
		t.Errorf("the cursor is at %v", pos)
	}

	// Below the cap a repeat is what it was.
	e = NewEmulator(10, 2)
	e.Write([]byte("ab\x1b[3b"))
	if got := termText(e)[0]; got != "abbbb     " {
		t.Errorf("a repeat of 3 gave %q", got)
	}
	if e.Counters().RepeatsCut != 0 {
		t.Errorf("counters %+v", e.Counters())
	}
}

// 64 KiB of the longest repeat on the largest screen (500 by 200) does not
// keep the emulator, and the pane's lock with it, for more than a few
// seconds, whatever state the cursor and the pen are in: at the top and at the
// bottom of a full screen, after a line feed, with autowrap off, in a scroll
// region, under a link (cut to two lines), with a margin (the same). The time of
// each is logged. The repeat of the first version was cut to the cells of the
// screen: one of them took 17 ms and 100 of them with the cursor at the bottom
// 25 s, so 64 KiB of them would have taken half an hour. The cases that are
// not the first send 8 KiB.
func TestRepeatsOnTheLargestScreenAreBounded(t *testing.T) {
	ceiling := 60 * time.Second
	if raceEnabled {
		ceiling = 120 * time.Second
	}
	const rep = "\x1b[2147483646b"
	for _, tc := range []struct {
		name   string
		setup  string
		unit   string
		budget int
		race   bool // also under the race detector
	}{
		{"at the bottom", "x", rep, 64 << 10, true},
		{"at the top each time", "x", "\x1b[H" + rep, 8 << 10, true},
		{"after a line feed", "x", "\n" + rep, 8 << 10, false},
		{"autowrap off", "\x1b[?7lx", "\x1b[H" + rep, 8 << 10, false},
		{"a scroll region", "\x1b[50;150rx", "\x1b[50;1H" + rep, 8 << 10, false},
		{"a scroll region and the cursor below it", "\x1b[50;150rx\x1b[190;1H", rep, 8 << 10, false},
		{"under a link", "\x1b]8;;http://a.example/1\x1b\\x", "\x1b[H" + rep, 8 << 10, false},
		{"a margin", "\x1b[?69h\x1b[10;400sx", "\x1b[H" + rep, 8 << 10, false},
		{"each with a character of its own", "", "q\x1b[H" + rep, 8 << 10, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget := tc.budget
			if raceEnabled {
				// The race detector is slow: three of the states, each a
				// thirty-second of the bytes.
				if !tc.race {
					t.Skip("not under the race detector")
				}
				budget /= 32
			}
			e := NewEmulator(500, 200)
			e.SetScrollbackSize(0)
			e.Write([]byte(tc.setup))
			input := []byte(strings.Repeat(tc.unit, max(1, budget/len(tc.unit))))
			start := time.Now()
			done := make(chan struct{})
			go func() {
				defer close(done)
				e.Write(input)
			}()
			select {
			case <-done:
			case <-time.After(ceiling):
				t.Fatalf("%d bytes of %q did not return in %v", len(input), tc.unit, ceiling)
			}
			t.Logf("%d bytes (%d sequences): %v", len(input), len(input)/len(tc.unit), time.Since(start))
		})
	}
}
