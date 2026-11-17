package vt

import (
	"strings"
	"testing"
)

// Design case H3: OSC, DCS and APC strings over MaxStringBytes are cut to it
// and counted, the handler sees at most the cap, and nothing of the rest
// reaches the screen, also when the cut or the rest holds a 0x9C byte that
// belongs to a UTF-8 character (U+2733 is E2 9C B3).
func TestStringsOverTheCapAreTruncated(t *testing.T) {
	over := 512 << 10
	if raceEnabled {
		over = 160 << 10 // still over the cap of 64 KiB; the race detector is slow
	}
	for _, tc := range []struct {
		name  string
		body  string
		start string
		end   string
	}{
		{"osc title, bel", strings.Repeat("a", over), "\x1b]0;", "\x07"},
		{"osc title, esc st", strings.Repeat("a", over), "\x1b]2;", "\x1b\\"},
		{"osc title with 0x9c in characters", strings.Repeat("✳", over/3), "\x1b]2;", "\x1b\\"},
		{"osc title cut inside a character", "x" + strings.Repeat("✳", over/3), "\x1b]2;", "\x07"},
		{"osc other", strings.Repeat("a", over), "\x1b]777;", "\x07"},
		{"osc 8 link", strings.Repeat("a", over), "\x1b]8;;http://x/", "\x1b\\"},
		{"dcs", strings.Repeat("a", over), "\x1bP1$q", "\x1b\\"},
		{"dcs with 0x9c in characters", strings.Repeat("✳", over/3), "\x1bP1$q", "\x1b\\"},
		{"apc", strings.Repeat("a", over), "\x1b_G", "\x1b\\"},
		{"apc with 0x9c in characters", strings.Repeat("✳", over/3), "\x1b_G", "\x1b\\"},
		{"pm", strings.Repeat("a", over), "\x1b^", "\x1b\\"},
		{"sos", strings.Repeat("a", over), "\x1bX", "\x1b\\"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEmulator(40, 3)
			var title, icon string
			e.SetCallbacks(Callbacks{
				Title:    func(s string) { title = s },
				IconName: func(s string) { icon = s },
			})
			input := []byte(tc.start + tc.body + tc.end + "after")
			alloc := allocated(func() { e.Write(input) })
			t.Logf("%d bytes in, %d bytes allocated", len(input), alloc)

			if got := e.Counters().StringsTruncated; got != 1 {
				t.Errorf("StringsTruncated = %d, want 1", got)
			}
			if len(title) > MaxStringBytes || len(icon) > MaxStringBytes {
				t.Errorf("a title of %d bytes and an icon name of %d bytes passed the cap", len(title), len(icon))
			}
			if got := strings.TrimRight(e.String(), "\n "); got != "after" {
				t.Errorf("screen %q, want only the text after the string", got)
			}
			// A few copies of the capped string at most; the input is 8 times
			// the cap.
			if alloc > 4*MaxStringBytes {
				t.Errorf("allocated %d bytes for a string of %d", alloc, len(tc.body))
			}
		})
	}
}

// A title at the cap reaches the callback whole; one byte more is cut.
func TestStringAtTheCapIsNotTruncated(t *testing.T) {
	e := NewEmulator(40, 3)
	var title string
	e.SetCallbacks(Callbacks{Title: func(s string) { title = s }})
	// The data is "2;" and the title.
	fits := strings.Repeat("t", MaxStringBytes-len("2;"))
	e.WriteString("\x1b]2;" + fits + "\x07")
	if title != fits || e.Counters().StringsTruncated != 0 {
		t.Fatalf("title of %d bytes, StringsTruncated = %d", len(title), e.Counters().StringsTruncated)
	}
	e.WriteString("\x1b]2;" + fits + "t\x07")
	if title != fits || e.Counters().StringsTruncated != 1 {
		t.Fatalf("title of %d bytes, StringsTruncated = %d", len(title), e.Counters().StringsTruncated)
	}
}

// Many long strings cost the cap each, not their length, and each is counted.
func TestManyLongStrings(t *testing.T) {
	const n = 20
	str := []byte("\x1b]0;" + strings.Repeat("a", 256<<10) + "\x07")
	e := NewEmulator(40, 3)
	alloc := allocated(func() {
		for i := 0; i < n; i++ {
			e.Write(str)
		}
	})
	if got := e.Counters().StringsTruncated; got != n {
		t.Errorf("StringsTruncated = %d, want %d", got, n)
	}
	if alloc > n*3*MaxStringBytes {
		t.Errorf("allocated %d bytes for %d strings of %d", alloc, n, len(str))
	}
}
