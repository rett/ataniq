package vt

import (
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
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

// allocated runs f and returns the bytes it allocated, which the garbage
// collector cannot change.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A cell keeps at most MaxClusterBytes of a cluster, cut at a rune boundary,
// and the rest is counted, whether the cluster arrives in one write, in one
// byte per write, or in writes that end between the marks.
func TestClusterKeepsAtMostMaxBytes(t *testing.T) {
	const marks = 100 // U+0301 is two bytes
	input := "e" + strings.Repeat("\u0301", marks)
	// The base is one byte and each mark two: 31 marks make 63 bytes, and the
	// 32nd would make 65.
	wantKept := "e" + strings.Repeat("\u0301", (MaxClusterBytes-1)/2)
	wantDropped := uint64(len(input) - len(wantKept))

	for _, tc := range []struct {
		name  string
		parts func() []string
	}{
		{"one write", func() []string { return []string{input} }},
		{"byte per write", func() []string {
			var p []string
			for i := 0; i < len(input); i++ {
				p = append(p, input[i:i+1])
			}
			return p
		}},
		{"rune per write", func() []string {
			var p []string
			for _, r := range input {
				p = append(p, string(r))
			}
			return p
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := newTestTerminal(t, 10, 1)
			for _, p := range tc.parts() {
				term.WriteString(p)
			}
			cell := term.CellAt(0, 0)
			if cell == nil || cell.Content != wantKept {
				t.Fatalf("cell 0: %q, want %d bytes %q", cell.Content, len(wantKept), wantKept)
			}
			if !utf8.ValidString(cell.Content) || len(cell.Content) > MaxClusterBytes {
				t.Errorf("cell 0 holds %d bytes, valid=%v", len(cell.Content), utf8.ValidString(cell.Content))
			}
			if next := term.CellAt(1, 0); next == nil || next.Content != " " {
				t.Errorf("cell 1: %v, want blank: a dropped mark must not start a cell", next)
			}
			if got := term.Counters().ClusterBytesDropped; got != wantDropped {
				t.Errorf("ClusterBytesDropped = %d, want %d", got, wantDropped)
			}
		})
	}
}

// A cluster at the cap is not reopened by the next write and does not grow:
// the mark is dropped and counted, the cursor stays, and the cluster is still
// the open one, so a following mark is dropped as well.
func TestClusterAtCapIsNotReopenedOrGrown(t *testing.T) {
	term := newTestTerminal(t, 10, 1)
	term.WriteString("e" + strings.Repeat("\u0301", 31)) // 63 bytes
	term.WriteString("\u0301")                           // would be 65
	pos, cluster, ok := term.OpenCluster()
	if !ok || pos.X != 0 || len(cluster) != 63 {
		t.Fatalf("OpenCluster after the cap: %v %d bytes %v", pos, len(cluster), ok)
	}
	if x, _ := term.scr.CursorPosition(); x != 1 {
		t.Errorf("cursor x = %d, want 1", x)
	}
	if got := term.Counters().ClusterBytesDropped; got != 2 {
		t.Errorf("ClusterBytesDropped = %d, want 2", got)
	}
	term.WriteString("\u0301\u0301")
	if got := term.Counters().ClusterBytesDropped; got != 6 {
		t.Errorf("ClusterBytesDropped = %d, want 6", got)
	}
	// A character that starts a cluster is drawn beside it as usual.
	term.WriteString("\u00e9")
	if c := term.CellAt(1, 0); c == nil || c.Content != "\u00e9" {
		t.Errorf("cell 1: %v, want the new character", c)
	}
	if len(term.CellAt(0, 0).Content) != 63 {
		t.Errorf("cell 0 grew to %d bytes", len(term.CellAt(0, 0).Content))
	}
}

// Once a rune has been dropped from a cluster, the cluster does not grow
// again, so a cell keeps a prefix of what was sent: a shorter mark after a
// longer one that did not fit is dropped too, in the same write and in the
// next one.
func TestClusterDoesNotGrowAfterADrop(t *testing.T) {
	// 62 bytes: the base, U+20D0 (3 bytes) and 29 x U+0301 (2 bytes). The next
	// U+20D0 would make 65 and is dropped; the U+0301 after it would fit.
	head := "e\u20d0" + strings.Repeat("\u0301", 29)
	if len(head) != MaxClusterBytes-2 {
		t.Fatalf("test input is %d bytes", len(head))
	}
	tail := "\u20d0\u0301\u0301\u20d0\u0301"
	input := head + tail
	// A character that starts a cluster follows, then an ASCII one.
	after := "\u00e9" + "z"

	run := func(t *testing.T, parts []string) {
		term := newTestTerminal(t, 10, 1)
		for _, p := range parts {
			term.WriteString(p)
		}
		if c := term.CellAt(0, 0); c == nil || c.Content != head {
			t.Errorf("cell 0 is %d bytes, want the first %d bytes of the cluster", len(c.Content), len(head))
		}
		if got, want := term.Counters().ClusterBytesDropped, uint64(len(tail)); got != want {
			t.Errorf("ClusterBytesDropped = %d, want %d", got, want)
		}
		if c := term.CellAt(1, 0); c == nil || c.Content != "\u00e9" {
			t.Errorf("cell 1: %v, want the character that follows", c)
		}
		if c := term.CellAt(2, 0); c == nil || c.Content != "z" {
			t.Errorf("cell 2: %v", c)
		}
	}

	t.Run("one write", func(t *testing.T) { run(t, []string{input + after}) })
	t.Run("rune per write", func(t *testing.T) {
		var p []string
		for _, r := range input + after {
			p = append(p, string(r))
		}
		run(t, p)
	})
	t.Run("every split", func(t *testing.T) {
		for i := 0; i <= len(input); i++ {
			if i < len(head) && !utf8.RuneStart(input[i]) {
				continue
			}
			if !utf8.ValidString(input[:i]) {
				continue
			}
			run(t, []string{input[:i], input[i:] + after})
		}
	})
	// The same when the cluster is not alone in the buffer: text with no
	// ASCII before it fills the buffer with several clusters, and the last is
	// the one that is capped.
	t.Run("after other clusters", func(t *testing.T) {
		term := newTestTerminal(t, 40, 1)
		term.WriteString("\u00e9\u00e8" + head + tail + after)
		for i, want := range []string{"\u00e9", "\u00e8", head, "\u00e9", "z"} {
			if c := term.CellAt(i, 0); c == nil || c.Content != want {
				t.Errorf("cell %d: %q, want %q", i, c.Content, want)
			}
		}
	})
}

// Clusters under the cap still join across writes, up to the cap exactly.
func TestClusterUnderCapJoinsAcrossWrites(t *testing.T) {
	// 64 bytes: the base and 21 three-byte marks.
	full := "a" + strings.Repeat("\u20dd", 21)
	if len(full) != MaxClusterBytes {
		t.Fatalf("test input is %d bytes", len(full))
	}
	term := newTestTerminal(t, 10, 1)
	term.WriteString(full[:30])
	term.WriteString(full[30:])
	if c := term.CellAt(0, 0); c == nil || c.Content != full {
		t.Errorf("cell 0: %q, want the %d-byte cluster", c.Content, len(full))
	}
	if got := term.Counters().ClusterBytesDropped; got != 0 {
		t.Errorf("ClusterBytesDropped = %d, want 0", got)
	}
	term.WriteString("\u20dd")
	if got := term.Counters().ClusterBytesDropped; got != 3 {
		t.Errorf("ClusterBytesDropped = %d, want 3", got)
	}
}

// Text with no ASCII in it fills the grapheme buffer with many clusters; the
// cap is on one cluster and must not cut such text.
func TestNonASCIIRunIsNotCutByTheClusterCap(t *testing.T) {
	const n = 1000
	text := strings.Repeat("\u4e16", n) // wide characters, three bytes each
	term := newTestTerminal(t, 2*n, 1)
	term.WriteString(text)
	for i := 0; i < n; i++ {
		if c := term.CellAt(2*i, 0); c == nil || c.Content != "\u4e16" {
			t.Fatalf("cell %d: %v", 2*i, c)
		}
	}
	if got := term.Counters(); got != (Counters{}) {
		t.Errorf("counters = %+v, want none", got)
	}
	// The same text in one-rune writes, and mixed with accents.
	term = newTestTerminal(t, 2*n, 1)
	for _, r := range text {
		term.WriteString(string(r))
	}
	if c := term.CellAt(2*(n-1), 0); c == nil || c.Content != "\u4e16" {
		t.Errorf("last cell: %v", c)
	}
	mixed := strings.Repeat("\u00e9\u4e16\u0301", 200)
	term = newTestTerminal(t, 800, 1)
	term.WriteString(mixed)
	if c := term.CellAt(1, 0); c == nil || c.Content != "\u4e16\u0301" {
		t.Errorf("cell 1: %v", c)
	}
	if got := term.Counters(); got != (Counters{}) {
		t.Errorf("counters = %+v, want none", got)
	}
}

// Design case H1: a 1 MiB run of combining marks uses bounded memory and
// linear time, the cell keeps MaxClusterBytes at most, and the counter rises
// by what was dropped.
func TestMegabyteOfCombiningMarks(t *testing.T) {
	marks := []byte("a" + strings.Repeat("\u0301", (1<<20)/2))
	for _, tc := range []struct {
		name  string
		input []byte
		write func(*Emulator, []byte)
	}{
		{"one write", marks, func(e *Emulator, b []byte) { e.Write(b) }},
		// Writes that end between marks reopen the cluster each time.
		{"4 KiB writes", marks[:1<<17-1], func(e *Emulator, b []byte) {
			for i := 0; i < len(b); i += 4096 {
				e.Write(b[i:min(i+4096, len(b))])
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := NewEmulator(80, 24)
			var elapsed time.Duration
			alloc := allocated(func() {
				start := time.Now()
				tc.write(term, tc.input)
				elapsed = time.Since(start)
			})
			t.Logf("%d bytes: %v, %d bytes allocated", len(tc.input), elapsed, alloc)
			// The run costs a few kilobytes at most; the ceiling is far above
			// that and far below a buffer that keeps the run (the size of the
			// input and its regrowth).
			if alloc > 64<<10 {
				t.Errorf("allocated %d bytes for %d bytes of marks", alloc, len(tc.input))
			}
			if elapsed > 20*time.Second {
				t.Errorf("took %v", elapsed)
			}
			if c := cap(term.grapheme); c > 2*(MaxClusterBytes+utf8.UTFMax) {
				t.Errorf("grapheme buffer capacity %d", c)
			}
			if c := cap(term.open.content); c > 2*(MaxClusterBytes+utf8.UTFMax) {
				t.Errorf("open cluster capacity %d", c)
			}
			cell := term.CellAt(0, 0)
			if cell == nil || len(cell.Content) > MaxClusterBytes || !utf8.ValidString(cell.Content) {
				t.Fatalf("cell 0: %q", cell.Content)
			}
			if got, want := term.Counters().ClusterBytesDropped, uint64(len(tc.input)-len(cell.Content)); got != want {
				t.Errorf("ClusterBytesDropped = %d, want %d", got, want)
			}
		})
	}
}

// The counters are read through SafeEmulator under its lock.
func TestSafeEmulatorCounters(t *testing.T) {
	se := NewSafeEmulator(10, 1)
	se.Write([]byte("e" + strings.Repeat("\u0301", 40)))
	if got := se.Counters().ClusterBytesDropped; got != 80-62 {
		t.Errorf("ClusterBytesDropped = %d, want %d", got, 80-62)
	}
}
