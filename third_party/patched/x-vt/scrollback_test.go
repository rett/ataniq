package vt

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestScrollback(t *testing.T) {
	t.Run("basic push and len", func(t *testing.T) {
		sb := NewScrollback(100)
		if sb.Len() != 0 {
			t.Errorf("expected len 0, got %d", sb.Len())
		}
		if sb.MaxLines() != 100 {
			t.Errorf("expected max 100, got %d", sb.MaxLines())
		}
	})

	t.Run("scrollback in emulator", func(t *testing.T) {
		// Create a small terminal
		e := NewEmulator(10, 5)

		// Fill the screen with numbered lines and force scrolling
		for i := 0; i < 10; i++ {
			e.WriteString("\r\n") // Scroll up
		}

		// Check scrollback has captured some lines
		sbLen := e.ScrollbackLen()
		t.Logf("scrollback length after 10 newlines: %d", sbLen)

		if sbLen == 0 {
			t.Error("expected scrollback to have captured lines, got 0")
		}
	})

	t.Run("scrollback with content", func(t *testing.T) {
		e := NewEmulator(20, 5)

		// Write content that will scroll
		for i := 0; i < 10; i++ {
			e.WriteString("line\r\n")
		}

		// Verify scrollback captured the scrolled content
		sb := e.Scrollback()
		if sb == nil {
			t.Fatal("scrollback is nil")
		}

		// Should have captured lines (at least 5, since screen is 5 tall and we wrote 10 lines)
		if sb.Len() < 5 {
			t.Errorf("expected at least 5 lines in scrollback, got %d", sb.Len())
		}
	})

	t.Run("scrollback max lines", func(t *testing.T) {
		sb := NewScrollback(5)

		// Push more lines than max
		for i := 0; i < 10; i++ {
			sb.Push(nil)
		}

		if sb.Len() != 5 {
			t.Errorf("expected len 5 after overflow, got %d", sb.Len())
		}
	})

	t.Run("clear scrollback", func(t *testing.T) {
		e := NewEmulator(20, 5)

		// Write content that will scroll
		for i := 0; i < 10; i++ {
			e.WriteString("line\r\n")
		}

		// Verify we have scrollback
		if e.ScrollbackLen() == 0 {
			t.Error("expected scrollback before clear")
		}

		// Clear it
		e.ClearScrollback()

		if e.ScrollbackLen() != 0 {
			t.Errorf("expected empty scrollback after clear, got %d", e.ScrollbackLen())
		}
	})

	t.Run("alt screen does not have scrollback", func(t *testing.T) {
		e := NewEmulator(20, 5)

		// Write some content to main screen
		for i := 0; i < 10; i++ {
			e.WriteString("line\r\n")
		}

		mainScrollbackLen := e.ScrollbackLen()
		if mainScrollbackLen == 0 {
			t.Error("expected scrollback on main screen")
		}

		// Enter alt screen
		e.WriteString("\x1b[?1049h") // DECSET alt screen

		// Scrollback should still be from main screen
		if e.ScrollbackLen() != mainScrollbackLen {
			t.Errorf("expected scrollback len %d in alt screen, got %d",
				mainScrollbackLen, e.ScrollbackLen())
		}

		// Write to alt screen - should not affect main scrollback
		for i := 0; i < 10; i++ {
			e.WriteString("alt\r\n")
		}

		// Main screen scrollback should be unchanged
		if e.ScrollbackLen() != mainScrollbackLen {
			t.Errorf("expected scrollback len %d after alt screen writes, got %d",
				mainScrollbackLen, e.ScrollbackLen())
		}
	})

	t.Run("ED 2 saves to scrollback", func(t *testing.T) {
		e := NewEmulator(20, 5)

		// Write some content (not enough to scroll)
		e.WriteString("line 1\r\n")
		e.WriteString("line 2\r\n")
		e.WriteString("line 3\r\n")

		// Should have no scrollback yet (didn't scroll)
		initialLen := e.ScrollbackLen()

		// Clear screen with ED 2 (ESC[2J)
		e.WriteString("\x1b[2J")

		// Should have saved lines to scrollback
		newLen := e.ScrollbackLen()
		if newLen <= initialLen {
			t.Errorf("expected scrollback to grow after ED 2, was %d now %d", initialLen, newLen)
		}
		t.Logf("scrollback after ED 2: %d lines", newLen)
	})

	t.Run("ED 3 clears scrollback", func(t *testing.T) {
		e := NewEmulator(20, 5)

		// Write content that will scroll
		for i := 0; i < 10; i++ {
			e.WriteString("line\r\n")
		}

		// Verify we have scrollback
		if e.ScrollbackLen() == 0 {
			t.Error("expected scrollback before ED 3")
		}

		// ED 3 (ESC[3J) should clear scrollback
		e.WriteString("\x1b[3J")

		if e.ScrollbackLen() != 0 {
			t.Errorf("expected empty scrollback after ED 3, got %d", e.ScrollbackLen())
		}
	})
}

// numberedLines returns n lines "<prefix><i>", each ended by CR LF.
func numberedLines(prefix string, n int) string {
	var b strings.Builder
	for i := range n {
		fmt.Fprintf(&b, "%s%d\r\n", prefix, i)
	}
	return b.String()
}

// The alternate screen keeps no scrollback, as in real terminals. It used to
// get the default of 10000 lines, which nothing could read or limit, so a
// full-screen program that scrolled a lot grew the emulator without bound.
// Scrollback and ScrollbackLen report the main screen's scrollback whichever
// screen is active.
func TestAltScreenKeepsNoScrollback(t *testing.T) {
	noAltScrollback := func(t *testing.T, e *Emulator, when string) {
		t.Helper()
		if sb := e.scrs[1].Scrollback(); sb.Len() != 0 || sb.MaxLines() != 0 {
			t.Errorf("%s: the alternate screen's scrollback holds %d lines of %d, want 0 of 0", when, sb.Len(), sb.MaxLines())
		}
	}
	e := NewEmulator(20, 5)
	noAltScrollback(t, e, "new emulator")
	e.WriteString(numberedLines("main ", 10))
	mainLen := e.ScrollbackLen()
	if mainLen == 0 {
		t.Fatal("the main screen kept no scrollback")
	}
	for _, mode := range []string{"1049", "1047"} {
		e.WriteString("\x1b[?" + mode + "h")
		if !e.IsAltScreen() {
			t.Fatalf("mode %s: not on the alternate screen", mode)
		}
		e.WriteString(numberedLines("alt ", 1000))
		noAltScrollback(t, e, "mode "+mode+" after scrolling")
		// ED 2 saves the screen to scrollback on the main screen.
		e.WriteString("\x1b[2J")
		noAltScrollback(t, e, "mode "+mode+" after ED 2")
		// A scroll region whose top is not the first line.
		e.WriteString("\x1b[2;4r\x1b[4;1H" + numberedLines("region ", 20) + "\x1b[r")
		noAltScrollback(t, e, "mode "+mode+" after scrolling a region")
		e.Resize(30, 8)
		e.SetScrollbackSize(50)
		e.WriteString(numberedLines("resized ", 100))
		noAltScrollback(t, e, "mode "+mode+" after a resize")
		if got := e.ScrollbackLen(); got != mainLen {
			t.Errorf("mode %s: ScrollbackLen on the alternate screen is %d, want the main screen's %d", mode, got, mainLen)
		}
		if e.Scrollback() != e.scrs[0].Scrollback() {
			t.Errorf("mode %s: Scrollback on the alternate screen is not the main screen's", mode)
		}
		e.WriteString("\x1b[?" + mode + "l")
		if e.IsAltScreen() {
			t.Fatalf("mode %s: still on the alternate screen", mode)
		}
		if got := e.ScrollbackLen(); got != mainLen {
			t.Errorf("mode %s: the main screen's scrollback is %d lines after the alternate screen, want %d", mode, got, mainLen)
		}
		e.Resize(20, 5)
	}
	e.WriteString("\x1bc") // RIS
	e.WriteString("\x1b[?1049h" + numberedLines("after reset ", 100))
	noAltScrollback(t, e, "after a full reset")
}

// The main screen still keeps the lines that scroll off it, up to the size
// SetScrollbackSize sets, and keeps them across a visit to the alternate
// screen.
func TestMainScreenScrollbackObeysSize(t *testing.T) {
	e := NewEmulator(10, 3)
	e.WriteString(numberedLines("l", 20))
	// l18, l19 and the cursor's empty line are on the screen.
	if got := e.ScrollbackLen(); got != 18 {
		t.Fatalf("scrollback holds %d lines, want 18", got)
	}
	if got := e.Scrollback().MaxLines(); got != DefaultScrollbackSize {
		t.Errorf("scrollback size is %d, want the default %d", got, DefaultScrollbackSize)
	}
	e.SetScrollbackSize(5)
	if got := e.Scrollback().MaxLines(); got != 5 {
		t.Errorf("scrollback size is %d after SetScrollbackSize(5)", got)
	}
	check := func(when string, want ...string) {
		t.Helper()
		var got []string
		for _, l := range e.Scrollback().Lines() {
			got = append(got, l.String())
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: scrollback is %q, want %q", when, got, want)
		}
	}
	check("after SetScrollbackSize(5)", "l13", "l14", "l15", "l16", "l17")
	e.WriteString(numberedLines("m", 4))
	check("after more lines", "l17", "l18", "l19", "m0", "m1")
	e.WriteString("\x1b[?1049h" + numberedLines("alt ", 50) + "\x1b[?1049l")
	check("after the alternate screen", "l17", "l18", "l19", "m0", "m1")
	e.SetScrollbackSize(2)
	check("after SetScrollbackSize(2)", "m0", "m1")
}
