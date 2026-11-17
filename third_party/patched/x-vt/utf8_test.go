package vt

import (
	"strings"
	"testing"
	"time"
)

// A combining mark belongs to the character before it, however that character
// arrived. The printable-ASCII path used to write its cell immediately, which
// left a following mark with nowhere to go: it became a zero-width cell of its
// own, and the next write or erase destroyed it.
func TestCombiningMarkJoinsPrecedingASCII(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  []string
	}{
		{"bare", "bcé", []string{"b", "c", "é"}},
		{"then erase", "bcé\x1b[K", []string{"b", "c", "é"}},
		{"then another character", "bcéx", []string{"b", "c", "é", "x"}},
		{"several in a row", "áb́c", []string{"á", "b́", "c"}},
		{"mark on a wide character", "世́x", []string{"世́", "", "x"}},
		{"emoji with a modifier", "\U0001f44b\U0001f3ffx", []string{"\U0001f44b\U0001f3ff", "", "x"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newTestTerminal(t, 10, 1)
			term.WriteString(tc.input)
			for x, want := range tc.want {
				cell := term.CellAt(x, 0)
				if cell == nil {
					t.Fatalf("cell %d: nil", x)
				}
				if cell.Content != want {
					t.Errorf("cell %d: got %q, want %q", x, cell.Content, want)
				}
			}
		})
	}
}

// A character is held back until the next one arrives, so it has to be flushed
// when the write ends or a caller reading the screen would not see it.
func TestLastCharacterIsFlushedWhenTheWriteEnds(t *testing.T) {
	term := newTestTerminal(t, 10, 1)
	term.WriteString("hi")
	for x, want := range []string{"h", "i"} {
		if cell := term.CellAt(x, 0); cell == nil || cell.Content != want {
			t.Errorf("cell %d: got %v, want %q", x, cell, want)
		}
	}
}

// A single shift applies to one character, and the character it applies to may
// be a cluster no charset has a mapping for. Taking the shift only when the
// mapping happens left it set, and it landed on the character after.
func TestSingleShiftDoesNotLeakPastACluster(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
		want  []string
	}{
		// SS2, then a cluster, then a plain character. The cluster has no
		// mapping; the "b" must not be drawn from the shifted set.
		{"cluster then plain", "\x1b*0\x8eáb", []string{"á", "b"}},
		// The same shift with a mappable character, to show it still applies.
		{"plain then plain", "\x1b*0\x8eab", []string{"▒", "b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newTestTerminal(t, 10, 1)
			term.WriteString(tc.input)
			for x, want := range tc.want {
				if cell := term.CellAt(x, 0); cell == nil || cell.Content != want {
					t.Errorf("cell %d: got %v, want %q", x, cell, want)
				}
			}
		})
	}
}

// A run of combining marks is one cluster that keeps growing. Asking where its
// boundaries are on every character costs the length of the buffer each time,
// which is quadratic: 64KB of marks took six seconds before this was a single
// segmentation at the end of the run.
//
// The budget below is thousands of times the linear cost and a fraction of the
// quadratic one, so it says which of the two is in place without being a
// stopwatch on the machine it runs on.
func TestLongClusterRunStaysLinear(t *testing.T) {
	input := "a" + strings.Repeat("́", 32*1024)

	done := make(chan time.Duration, 1)
	go func() {
		term := NewEmulator(80, 24)
		start := time.Now()
		term.WriteString(input)
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		t.Logf("%d bytes in %v", len(input), elapsed)
	case <-time.After(10 * time.Second):
		t.Fatalf("writing %d bytes of one cluster did not finish in 10s", len(input))
	}
}

// A program does not choose where its output is split, and a pty read can end
// anywhere. A cluster split across writes, inside a UTF-8 sequence or between
// two runes of one cluster, must be drawn as if it had arrived in one write.
func TestClusterSplitAcrossWrites(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"zwj family", "A\U0001F468\u200d\U0001F469\u200d\U0001F467B"},
		{"variation selector", "x\u2764\ufe0fy"},
		{"skin tone", "x\U0001F44B\U0001F3FFy"},
		{"flag", "x\U0001F1F3\U0001F1FFy"},
		{"two flags", "\U0001F1F3\U0001F1FF\U0001F1FA\U0001F1F8z"},
		{"combining mark on ascii", "be\u0301c"},
		{"combining marks", "a\u0301\u0302\u0303b"},
		{"hangul jamo", "\u1100\u1161\u11a8z"},
		{"keycap", "#\ufe0f\u20e3x"},
		{"at end of line", "abcdefg\U0001F468\u200d\U0001F469"},
		{"wrapped", "abcdefgh\U0001F44B\U0001F3FFz"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := newTestTerminal(t, 8, 3)
			want.WriteString(tc.input)

			check := func(t *testing.T, parts ...string) {
				t.Helper()
				got := newTestTerminal(t, 8, 3)
				for _, p := range parts {
					got.WriteString(p)
				}
				if g, w := got.Render(), want.Render(); g != w {
					t.Errorf("split %q:\ngot  %q\nwant %q", parts, g, w)
				}
				gx, gy := got.scr.CursorPosition()
				wx, wy := want.scr.CursorPosition()
				if gx != wx || gy != wy || got.atPhantom != want.atPhantom {
					t.Errorf("split %q: cursor (%d,%d,%v), want (%d,%d,%v)", parts, gx, gy, got.atPhantom, wx, wy, want.atPhantom)
				}
			}

			// Every byte boundary, which includes every rune boundary.
			for i := 1; i < len(tc.input); i++ {
				check(t, tc.input[:i], tc.input[i:])
			}
			// One byte per write.
			bytes := make([]string, len(tc.input))
			for i := 0; i < len(tc.input); i++ {
				bytes[i] = tc.input[i : i+1]
			}
			check(t, bytes...)
			// One rune per write.
			var runes []string
			for _, r := range tc.input {
				runes = append(runes, string(r))
			}
			check(t, runes...)
		})
	}
}

// A cluster left open at the end of a write is closed by anything that is not
// a character: a sequence between the halves keeps them apart, as it does in
// a single write.
func TestOpenClusterClosedBySequence(t *testing.T) {
	for _, mid := range []string{"\x1b[31m", "\r", "\x1b[C", "\x1b]0;t\x07"} {
		want := newTestTerminal(t, 10, 1)
		want.WriteString("\U0001F468" + mid + "\u200d\U0001F469")
		got := newTestTerminal(t, 10, 1)
		got.WriteString("\U0001F468")
		got.WriteString(mid + "\u200d\U0001F469")
		if g, w := got.Render(), want.Render(); g != w {
			t.Errorf("sequence %q: got %q, want %q", mid, g, w)
		}
	}
}

// A copy of the screen that redraws the open cluster last can be extended by
// the next Write just like the original.
func TestOpenClusterCopy(t *testing.T) {
	orig := newTestTerminal(t, 10, 1)
	orig.WriteString("A\U0001F468‍\U0001F469")
	pos, cluster, ok := orig.OpenCluster()
	if !ok || pos.X != 1 || pos.Y != 0 || cluster != "\U0001F468‍\U0001F469" {
		t.Fatalf("OpenCluster: %v %q %v", pos, cluster, ok)
	}
	cp := newTestTerminal(t, 10, 1)
	cp.WriteString("A\x1b[1;2H" + cluster)
	rest := "‍\U0001F467B"
	orig.WriteString(rest)
	cp.WriteString(rest)
	if g, w := cp.Render(), orig.Render(); g != w {
		t.Errorf("copy %q, original %q", g, w)
	}
	// A base character can still take a combining mark from the next write.
	if pos, cluster, ok := orig.OpenCluster(); !ok || pos.X != 3 || cluster != "B" {
		t.Errorf("after B: %v %q %v", pos, cluster, ok)
	}
	orig.WriteString("\U0001F44B\x1b[m")
	if _, _, ok := orig.OpenCluster(); ok {
		t.Errorf("a sequence after the cluster closes it")
	}
}
