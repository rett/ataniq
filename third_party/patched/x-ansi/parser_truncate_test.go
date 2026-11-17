package ansi

import (
	"bytes"
	"strings"
	"testing"
)

// stringTap records the data of every OSC, DCS and APC string a parser
// dispatches, and what it prints.
type stringTap struct {
	osc, dcs, apc [][]byte
	printed       []rune
}

func tapParser(size int) (*Parser, *stringTap) {
	tap := &stringTap{}
	p := NewParser()
	p.SetHandler(Handler{
		Print:     func(r rune) { tap.printed = append(tap.printed, r) },
		HandleOsc: func(_ int, data []byte) { tap.osc = append(tap.osc, bytes.Clone(data)) },
		HandleDcs: func(_ Cmd, _ Params, data []byte) { tap.dcs = append(tap.dcs, bytes.Clone(data)) },
		HandleApc: func(data []byte) { tap.apc = append(tap.apc, bytes.Clone(data)) },
	})
	p.SetDataSize(size)
	return p, tap
}

// A string longer than the data buffer is cut to it, the rest is dropped, and
// each such string is counted once, however many bytes it loses. A string that
// just fits is not counted.
func TestStringDataTruncationIsCounted(t *testing.T) {
	const size = 16
	for _, tc := range []struct {
		name  string
		start string
		end   string
		got   func(*stringTap) [][]byte
		// prefix is data the parser adds to the string's data buffer.
		prefix string
	}{
		{"osc", "\x1b]0;", "\x1b\\", func(s *stringTap) [][]byte { return s.osc }, "0;"},
		{"dcs", "\x1bP1$q", "\x1b\\", func(s *stringTap) [][]byte { return s.dcs }, ""},
		{"apc", "\x1b_G", "\x1b\\", func(s *stringTap) [][]byte { return s.apc }, "G"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, tap := tapParser(size)
			room := size - len(tc.prefix)

			p.Parse([]byte(tc.start + strings.Repeat("a", room) + tc.end))
			if got := p.Truncated(); got != 0 {
				t.Fatalf("a string that fits was counted: %d", got)
			}
			p.Parse([]byte(tc.start + strings.Repeat("b", room+1) + tc.end))
			if got := p.Truncated(); got != 1 {
				t.Fatalf("Truncated() = %d after one string over the size, want 1", got)
			}
			p.Parse([]byte(tc.start + strings.Repeat("c", 10000) + tc.end))
			if got := p.Truncated(); got != 2 {
				t.Fatalf("Truncated() = %d after a long string, want 2", got)
			}
			p.Parse([]byte(tc.start + "d" + tc.end))
			if got := p.Truncated(); got != 2 {
				t.Fatalf("Truncated() = %d after a short string, want 2", got)
			}
			for i, want := range []string{strings.Repeat("a", room), strings.Repeat("b", room), strings.Repeat("c", room), "d"} {
				strs := tc.got(tap)
				if i >= len(strs) {
					t.Fatalf("string %d was not dispatched", i)
				}
				if got := string(strs[i]); got != tc.prefix+want {
					t.Errorf("string %d: %q, want %q", i, got, tc.prefix+want)
				}
			}
			if len(tap.printed) != 0 {
				t.Errorf("the rest of a string was printed: %q", string(tap.printed))
			}
		})
	}
}

// A string is counted when its first byte is dropped, also when it has not
// ended, and a string cut by Reset does not count twice.
func TestStringDataTruncationCountedWhenItHappens(t *testing.T) {
	p, tap := tapParser(8)
	p.Parse([]byte("\x1b]2;" + strings.Repeat("x", 100)))
	if got := p.Truncated(); got != 1 {
		t.Fatalf("Truncated() = %d in an open string, want 1", got)
	}
	p.Parse([]byte(strings.Repeat("x", 100)))
	if got := p.Truncated(); got != 1 {
		t.Fatalf("Truncated() = %d later in the same string, want 1", got)
	}
	p.Reset()
	p.Parse([]byte("\x1b]2;" + strings.Repeat("y", 100) + "\x07"))
	if got := p.Truncated(); got != 2 {
		t.Fatalf("Truncated() = %d after Reset and a new long string, want 2", got)
	}
	if len(tap.osc) != 1 || string(tap.osc[0]) != "2;"+strings.Repeat("y", 6) {
		t.Errorf("dispatched %q", tap.osc)
	}
}

// A 0x9C byte inside a UTF-8 character stays data across the cut: the string
// ends at the real terminator and none of it reaches the screen.
func TestStringDataTruncationInsideUTF8(t *testing.T) {
	p, tap := tapParser(16)
	p.Parse([]byte("\x1b]2;" + strings.Repeat("✳", 20) + "\x1b\\Z"))
	if len(tap.osc) != 1 || len(tap.osc[0]) != 16 {
		t.Fatalf("dispatched %q", tap.osc)
	}
	if got := p.Truncated(); got != 1 {
		t.Errorf("Truncated() = %d, want 1", got)
	}
	if string(tap.printed) != "Z" {
		t.Errorf("printed %q, want only the text after the string", string(tap.printed))
	}
}

// A parser with an unlimited data buffer drops nothing and counts nothing.
func TestUnlimitedStringDataIsNeverTruncated(t *testing.T) {
	p, tap := tapParser(0)
	p.Parse([]byte("\x1b]2;" + strings.Repeat("x", 100000) + "\x07"))
	if got := p.Truncated(); got != 0 {
		t.Errorf("Truncated() = %d", got)
	}
	if len(tap.osc) != 1 || len(tap.osc[0]) != 100002 {
		t.Errorf("dispatched %d bytes", len(tap.osc[0]))
	}
}
