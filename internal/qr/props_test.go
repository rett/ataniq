package qr

import (
	"errors"
	"strings"
	"testing"
)

func TestDeterministic(t *testing.T) {
	for _, l := range []Level{L, M} {
		for _, text := range []string{"", "a", "https://example.invalid/#abc", pattern(200)} {
			a, errA := Encode(text, l)
			b, errB := Encode(text, l)
			if errA != nil || errB != nil {
				t.Fatal(errA, errB)
			}
			if a.Size() != b.Size() || a.Mask() != b.Mask() || a.Version() != b.Version() {
				t.Fatalf("%q at %v: two encodings differ in shape", text, l)
			}
			if x, y, ok := sameMatrix(a.m, b.m); !ok {
				t.Fatalf("%q at %v: two encodings differ at (%d, %d)", text, l, x, y)
			}
		}
	}
}

func TestErrTooLongBoundary(t *testing.T) {
	for l, max := range map[Level]int{L: 271, M: 213} {
		c, err := Encode(strings.Repeat("a", max), l)
		if err != nil || c.Version() != MaxVersion || c.Size() != 57 {
			t.Errorf("%d bytes at %v: %v", max, l, err)
		}
		c, err = Encode(strings.Repeat("a", max+1), l)
		if !errors.Is(err, ErrTooLong) || c != nil {
			t.Errorf("%d bytes at %v: %v, want ErrTooLong", max+1, l, err)
		}
		if _, err = Encode(strings.Repeat("\xff", 10000), l); !errors.Is(err, ErrTooLong) {
			t.Errorf("10000 bytes at %v: %v, want ErrTooLong", l, err)
		}
	}
	// A text that fits at L but not at M.
	text := strings.Repeat("a", 214)
	if _, err := Encode(text, L); err != nil {
		t.Errorf("214 bytes at L: %v", err)
	}
	if _, err := Encode(text, M); !errors.Is(err, ErrTooLong) {
		t.Errorf("214 bytes at M: %v, want ErrTooLong", err)
	}
}

func TestSmallestVersion(t *testing.T) {
	for _, l := range []Level{L, M} {
		for v := MinVersion; v <= MaxVersion; v++ {
			n := byteCapacity(v, l)
			c := mustEncode(t, strings.Repeat("a", n), l)
			if c.Version() != v || c.Size() != 17+4*v {
				t.Errorf("%d bytes at %v: version %d size %d, want version %d", n, l, c.Version(), c.Size(), v)
			}
			if v > MinVersion {
				c := mustEncode(t, strings.Repeat("a", byteCapacity(v-1, l)+1), l)
				if c.Version() != v {
					t.Errorf("%d bytes at %v: version %d, want %d", byteCapacity(v-1, l)+1, l, c.Version(), v)
				}
			}
		}
	}
	if c := mustEncode(t, "", M); c.Version() != 1 {
		t.Errorf("empty text: version %d", c.Version())
	}
}

func TestUnknownLevel(t *testing.T) {
	for _, l := range []Level{-1, 2, 99} {
		c, err := Encode("a", l)
		if err == nil || errors.Is(err, ErrTooLong) || c != nil {
			t.Errorf("level %d: %v, %v", int(l), c, err)
		}
	}
	var zero Level
	if zero != M || DefaultLevel != M {
		t.Error("the zero level and the default are not M")
	}
}

func TestMaskChoiceIsLowestPenalty(t *testing.T) {
	for _, text := range []string{"a", "https://example.invalid/#abc123", pattern(100), strings.Repeat("A", 50)} {
		for _, l := range []Level{L, M} {
			c := mustEncode(t, text, l)
			best, bestMask := -1, -1
			for mask := 0; mask < 8; mask++ {
				f, err := encode(text, l, c.Version(), mask)
				if err != nil {
					t.Fatal(err)
				}
				if f.Mask() != mask {
					t.Fatalf("forced mask %d, got %d", mask, f.Mask())
				}
				if p := f.m.penalty(); best < 0 || p < best {
					best, bestMask = p, mask
				}
			}
			if c.Mask() != bestMask {
				t.Errorf("%d bytes at %v: mask %d, lowest penalty %d is of mask %d", len(text), l, c.Mask(), best, bestMask)
			}
			if got := c.m.penalty(); got != best {
				t.Errorf("%d bytes at %v: penalty %d, lowest %d", len(text), l, got, best)
			}
		}
	}
}

func TestFormatInformationInCode(t *testing.T) {
	// Both copies of the format information in a finished code carry the
	// level and mask, and the dark module is dark.
	for _, l := range []Level{L, M} {
		c := mustEncode(t, "https://example.invalid/#abc", l)
		gotLevel, gotMask := maskOf(t, c.m)
		if gotLevel != l || gotMask != c.Mask() {
			t.Errorf("first copy says %v mask %d, code is %v mask %d", gotLevel, gotMask, l, c.Mask())
		}
		n := c.Size()
		bits := formatInfo(l, c.Mask())
		for i := 0; i < 8; i++ {
			if c.Module(n-1-i, 8) != bit(bits, i) {
				t.Errorf("second copy, bit %d", i)
			}
		}
		for i := 8; i < 15; i++ {
			if c.Module(8, n-15+i) != bit(bits, i) {
				t.Errorf("second copy, bit %d", i)
			}
		}
		if !c.Module(8, n-8) {
			t.Error("the dark module is light")
		}
	}
}

func FuzzEncode(f *testing.F) {
	for _, s := range []string{"", "a", "https://example.invalid/#abc", "\xff\x00\x80", strings.Repeat("x", 213), strings.Repeat("y", 271), strings.Repeat("z", 272)} {
		f.Add(s, false)
		f.Add(s, true)
	}
	f.Fuzz(func(t *testing.T, text string, low bool) {
		l := M
		if low {
			l = L
		}
		c, err := Encode(text, l)
		if err != nil {
			if !errors.Is(err, ErrTooLong) || len(text) <= byteCapacity(MaxVersion, l) {
				t.Fatalf("%d bytes at %v: %v", len(text), l, err)
			}
			return
		}
		if len(text) > byteCapacity(MaxVersion, l) {
			t.Fatalf("%d bytes at %v encoded", len(text), l)
		}
		if n := c.Size(); n < 21 || n > 57 || (n-17)%4 != 0 {
			t.Fatalf("size %d", n)
		}
		for y := -1; y <= c.Size(); y++ {
			for x := -1; x <= c.Size(); x++ {
				_ = c.Module(x, y)
			}
		}
		if len(c.HalfBlocks(0)) == 0 || len(c.PNG(1, 0)) == 0 {
			t.Fatal("empty rendering")
		}
	})
}
