package vt

import (
	"strings"
	"testing"
	"time"
)

// The relative timing checks of the big-screen tests. A sequence that holds
// the emulator, and the lock of whoever called it, for a time that depends on
// the number of cells of the screen is what these tests are for, and the time
// of a machine is not a number to put a ceiling on: a loaded machine takes ten
// times what an idle one takes, and a ceiling that holds there lets a
// regression to the cell-by-cell code through (sixteen kilobytes of erase
// display took ten seconds, a hundred times the line feeds' time, and passed
// a ceiling of a minute). So every bounded sequence is timed against the same
// number of bytes of line feeds on the same screen, in the same run, and
// fails when it takes more than about twenty times as long. A line feed at
// the bottom of the screen is the cheapest thing that changes the whole
// screen: it moves every line, and then writes one.

// timingScreen returns the largest screen of the pane (500 by 200 cells, no
// scrollback) with something on every cell, the cursor at the top.
func timingScreen() *Emulator {
	e := NewEmulator(500, 200)
	e.SetScrollbackSize(0)
	e.Write([]byte(strings.Repeat("x", 500*200) + "\x1b[1;1H"))
	return e
}

// relativeLimit is how many times the time of the line feeds a sequence may
// take.
const relativeLimit = 20

// lineFeedKiB returns the time 1 KiB of line feeds takes on the largest
// screen, the best of three runs of 16 KiB (and at least 100 microseconds, so
// that a clock that is too coarse does not make the limit zero).
func lineFeedKiB(t *testing.T) time.Duration {
	t.Helper()
	best := time.Duration(1<<63 - 1)
	input := []byte(strings.Repeat("\n", 16<<10))
	for range 3 {
		e := timingScreen()
		start := time.Now()
		e.Write(input)
		best = min(best, time.Since(start))
	}
	return max(best/16, 100*time.Microsecond)
}

// writeBounded writes the input to the emulator and returns the time it took.
// It fails the test when the write has not returned in an hour, which is a
// hang and not a measurement (the relative check comes after).
func writeBounded(t *testing.T, e *Emulator, input []byte) time.Duration {
	t.Helper()
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Write(input)
	}()
	select {
	case <-done:
	case <-time.After(time.Hour):
		t.Fatalf("%d bytes did not return in an hour", len(input))
	}
	return time.Since(start)
}

// checkRelative fails the test when writing input took more than
// relativeLimit times what the same number of bytes of line feeds take, and
// logs the time and what it is in 64 KiB.
func checkRelative(t *testing.T, took time.Duration, bytes int, kib time.Duration) {
	t.Helper()
	limit := relativeLimit * kib * time.Duration(bytes) / 1024
	t.Logf("%d bytes: %v (%v in 64 KiB; the limit is %v, %d times the line feeds')", bytes, took, took*(64<<10)/time.Duration(bytes), limit, relativeLimit)
	if took > limit {
		t.Errorf("%d bytes took %v, more than %d times the %v that line feeds take", bytes, took, relativeLimit, limit/relativeLimit)
	}
}

// The sequences that clear the largest screen (500 by 200 cells, the pane's
// size, no scrollback) cost the lines they change, and a clear of a screen
// that is blank costs nothing: each written to 16 KiB, they take at most
// about twenty times what 16 KiB of line feeds take (see timing_test.go). A
// loop that clears the screen, or a program that redraws it with erase
// display at full speed, kept the emulator, and the pane's lock, for 25 to 70
// seconds in 64 KiB before the lines were marked (patch 14): each erase wrote
// the 11 MB of the screen, cell by cell.
func TestClearingTheLargestScreenIsBounded(t *testing.T) {
	kib := lineFeedKiB(t)
	for _, tc := range []struct {
		name  string
		setup string
		unit  string
	}{
		{"full reset", "", "\x1bc"},
		{"erase display", "", "\x1b[2J"},
		{"erase display below", "", "\x1b[J"},
		{"erase display above", "\x1b[200;1H", "\x1b[1J"},
		{"erase display with the scrollback", "", "\x1b[3J"},
		{"erase display with a background", "", "\x1b[44m\x1b[J"},
		{"erase display with a background that changes", "", "\x1b[41m\x1b[J\x1b[42m\x1b[J"},
		{"the clear of curses", "", "\x1b[H\x1b[2J\x1b[3J"},
		{"a line and a clear", "", "hello\r\n\x1b[H\x1b[J"},
		{"a line and a full reset", "", "hello\r\n\x1bc"},
		{"erase line", "", "\x1b[2K\x1b[B"},
		{"erase characters", "", "\x1b[500X\x1b[B"},
		{"erase to the end of the line", "", "\x1b[K\x1b[B"},
		{"the alternate screen", "", "\x1b[?1049h\x1b[?1049l"},
		{"the alternate screen with a line", "", "\x1b[?1049hhello\x1b[?1049l"},
		{"the alternate screen, 1047", "", "\x1b[?1047h\x1b[?1047l"},
		{"erase display in a region", "\x1b[50;150r", "\x1b[2J"},
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
