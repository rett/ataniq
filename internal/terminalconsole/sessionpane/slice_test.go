package sessionpane_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ataniq/internal/terminalconsole/sessionpane"
)

// Write feeds the emulator in slices of at most 4 KiB and frees the pane's
// lock between two slices, so that a draw, a digest or a snapshot waits for a
// slice and not for a whole chunk (a chunk is up to 64 KiB, and a whole-screen
// erase or a scroll in a narrow region of the largest pane can cost a
// millisecond).

func TestFeedTakesOneSlice(t *testing.T) {
	p := newPane(t, 500, 200, sessionpane.Options{})
	b := []byte(strings.Repeat("x", sessionpane.WriteSlice+1000))
	rest := sessionpane.Feed(p, b)
	if len(rest) != 1000 {
		t.Fatalf("Feed left %d bytes of %d, want 1000", len(rest), len(b))
	}
	if n := strings.Count(sessionpane.ScreenText(p), "x"); n != sessionpane.WriteSlice {
		t.Errorf("%d characters on the screen after one slice, want %d", n, sessionpane.WriteSlice)
	}
	// The lock is free between the slices.
	within(t, 10e9, "Size between two slices", func() { p.Size() })
	if rest = sessionpane.Feed(p, rest); len(rest) != 0 {
		t.Errorf("Feed left %d bytes of the second slice", len(rest))
	}
	if n := strings.Count(sessionpane.ScreenText(p), "x"); n != len(b) {
		t.Errorf("%d characters on the screen, want %d", n, len(b))
	}
}

// A chunk fed in slices, however it is cut (at the slices, at every byte, in
// the middle of a sequence), leaves the pane in the state the same bytes in
// one piece do: the emulator is the same one, and the parser goes on where a
// slice ends. The input has every kind of sequence of the property streams of
// the emulator, frames of synchronized output among them.
func TestSlicesDoNotChangeWhatTheEmulatorShows(t *testing.T) {
	tokens := []string{"a", "bcd", "hello world ", "\n", "\r\n", "\t", "世", "é", "\x1b[1m", "\x1b[0m", "\x1b[31m",
		"\x1b[H", "\x1b[3;4H", "\x1b[2J", "\x1b[J", "\x1b[1K", "\x1b[K", "\x1b[3L", "\x1b[2M", "\x1b[2S", "\x1b[2T", "\x1b[5X",
		"\x1b[?1049h", "\x1b[?1049l", "\x1b[2;5r", "\x1b[r", "\x1bc", "\x1bD", "\x1bM", "\x1b[?2026h", "\x1b[?2026l",
		"\x1b]8;;http://a.example/1\x1b\\", "\x1b]8;;\x1b\\", "\x1b]0;title\x07", "\x1b[44m", "\x1b[40b"}
	rng := rand.New(rand.NewPCG(5, 6))
	var sb strings.Builder
	for sb.Len() < 120<<10 {
		sb.WriteString(tokens[rng.IntN(len(tokens))])
	}
	input := []byte(sb.String())

	whole := newPane(t, 80, 24, sessionpane.Options{})
	whole.Write(input)
	for _, size := range []int{1, 7, sessionpane.WriteSlice - 1, sessionpane.WriteSlice, sessionpane.WriteSlice + 1} {
		cut := newPane(t, 80, 24, sessionpane.Options{})
		for b := input; len(b) > 0; {
			n := min(size, len(b))
			cut.Write(b[:n])
			b = b[n:]
		}
		if whole.Digest() != cut.Digest() {
			t.Errorf("a write in pieces of %d bytes shows another screen than one write", size)
		}
	}
}

// The pane is not held for the whole chunk: a reader that wants the lock
// while Write feeds a long chunk of slow output gets it between two slices,
// again and again, and does not wait for the end. The output is the repeat of
// a character over the whole of the largest pane, a few hundred microseconds
// each, 64 KiB of them.
func TestWriteLetsReadersInBetweenSlices(t *testing.T) {
	p := newPane(t, 500, 200, sessionpane.Options{})
	chunk := []byte(strings.Repeat("x\x1b[H\x1b[2147483646b", (64<<10)/16))
	var done atomic.Bool
	var wg sync.WaitGroup
	var reads atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !done.Load() {
			p.Size()
			if !done.Load() {
				reads.Add(1)
			}
		}
	}()
	p.Write(chunk)
	done.Store(true)
	wg.Wait()
	slices := (len(chunk) + sessionpane.WriteSlice - 1) / sessionpane.WriteSlice
	t.Logf("%d reads in the %d slices of the chunk", reads.Load(), slices)
	if reads.Load() < int64(slices)/4 {
		t.Errorf("%d reads while Write fed %d slices: the pane was held for the chunk", reads.Load(), slices)
	}
}

// Mode 2026: a frame that opens and closes inside one slice is never shown,
// and not copied; a frame that is still open at the end of a slice is held,
// so a reader between two slices sees the last complete frame, and not the
// half of the next one that is on the screen.
func TestSlicesAndSynchronizedFrames(t *testing.T) {
	const set, reset = "\x1b[?2026h", "\x1b[?2026l"
	c := newClock()

	p := newPane(t, 80, 24, sessionpane.Options{Clock: c.Now})
	p.Write([]byte("OLD" + set + "\x1b[HNEW" + reset))
	if sessionpane.FrameCaptured(p) {
		t.Error("the screen was copied for a frame that closes in its own slice")
	}
	if got := row(draw(p), 0); got != "NEW" {
		t.Errorf("row 0 is %q after a closed frame, want NEW", got)
	}

	// The frame opens in the first slice and closes in the third.
	p = newPane(t, 80, 24, sessionpane.Options{Clock: c.Now})
	p.Write([]byte("OLD"))
	frame := []byte(set + "\x1b[HNEW" + strings.Repeat("\x1b[B", 2*sessionpane.WriteSlice/3) + "\x1b[Hnew" + reset)
	if len(frame) < 2*sessionpane.WriteSlice {
		t.Fatalf("the frame is %d bytes, it should take three slices", len(frame))
	}
	rest := sessionpane.Feed(p, frame)
	if !sessionpane.FrameCaptured(p) {
		t.Error("the screen was not copied for a frame that stays open at the end of its slice")
	}
	if got := row(draw(p), 0); got != "OLD" {
		t.Errorf("between the slices row 0 is %q, want OLD: the half of a frame was shown", got)
	}
	rest = sessionpane.Feed(p, rest)
	if got := row(draw(p), 0); got != "OLD" {
		t.Errorf("in the middle of the frame row 0 is %q, want OLD", got)
	}
	for len(rest) > 0 {
		rest = sessionpane.Feed(p, rest)
	}
	if got := row(draw(p), 0); got != "new" {
		t.Errorf("after the frame row 0 is %q, want new", got)
	}
}

// Frames are the same however the output is cut: 5000 small frames in one
// write show what they show written frame by frame.
func TestManyFramesInOneWrite(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&sb, "\x1b[?2026h\x1b[H%05d\x1b[?2026l", i)
	}
	c := newClock()
	whole := newPane(t, 80, 24, sessionpane.Options{Clock: c.Now})
	whole.Write([]byte(sb.String()))
	if got := row(draw(whole), 0); got != "04999" {
		t.Errorf("row 0 is %q, want 04999", got)
	}
	one := newPane(t, 80, 24, sessionpane.Options{Clock: c.Now})
	for i := 0; i < 5000; i++ {
		one.Write([]byte(fmt.Sprintf("\x1b[?2026h\x1b[H%05d\x1b[?2026l", i)))
	}
	if whole.Digest() != one.Digest() {
		t.Error("5000 frames in one write and frame by frame show different screens")
	}
}
