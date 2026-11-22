package qr

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// qrencodeMatrix asks qrencode for the code of text in byte mode at the
// given level and version, as ASCII, and returns it as a matrix without the
// quiet zone.
func qrencodeMatrix(t testing.TB, text string, l Level, version int) *matrix {
	t.Helper()
	out, err := exec.Command("qrencode", "-t", "ASCII", "-8", "-m", "0",
		"-l", l.String(), "-v", fmt.Sprint(version), "-o", "-", text).Output()
	if err != nil {
		t.Fatalf("qrencode: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	n := len(lines)
	if n != sizeOf(version) {
		t.Fatalf("qrencode drew %d rows, want %d for version %d", n, sizeOf(version), version)
	}
	m := newMatrix(n)
	for y, line := range lines {
		// Each module is two characters wide.
		if len(line) != 2*n {
			t.Fatalf("qrencode row %d is %d characters, want %d", y, len(line), 2*n)
		}
		for x := 0; x < n; x++ {
			m.mod[y*n+x] = line[2*x] == '#'
		}
	}
	return m
}

// maskOf reads the mask and the level bits from the first copy of the
// format information of a matrix.
func maskOf(t testing.TB, m *matrix) (Level, int) {
	t.Helper()
	bits := 0
	for i := 0; i < 15; i++ {
		var x, y int
		switch {
		case i <= 5:
			x, y = 8, i
		case i == 6:
			x, y = 8, 7
		case i == 7:
			x, y = 8, 8
		case i == 8:
			x, y = 7, 8
		default:
			x, y = 14-i, 8
		}
		if m.get(x, y) {
			bits |= 1 << uint(i)
		}
	}
	data := (bits ^ 0x5412) >> 10
	mask := data & 7
	for _, l := range []Level{L, M} {
		if formatInfo(l, mask) == bits {
			return l, mask
		}
	}
	t.Fatalf("format information %015b is not valid", bits)
	return 0, 0
}

func needQrencode(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("qrencode"); err != nil {
		t.Skip("qrencode is not on PATH")
	}
}

func sameMatrix(a, b *matrix) (int, int, bool) {
	if a.n != b.n {
		return 0, 0, false
	}
	for y := 0; y < a.n; y++ {
		for x := 0; x < a.n; x++ {
			if a.get(x, y) != b.get(x, y) {
				return x, y, false
			}
		}
	}
	return 0, 0, true
}

func qrencodeTexts() []string {
	return []string{
		"hello, world",
		"https://example.invalid/#d7a3f1c25b8e49",
		"https://example.invalid/app/start?seat=2#" + strings.Repeat("kQ7_", 12),
		"\xc3\xa9\xff\x80 high bits",
		strings.Repeat("abcdefghij", 10),
		strings.Repeat("0123456789 ABC xyz", 11),
		strings.Repeat("z", 150),
		strings.Repeat("u", 213),
	}
}

// The mask is the one qrencode picked, read from its format information:
// qrencode does not take the mask as an option, and its choice is its own
// penalty rule, which scores the third rule not as the standard's text does,
// so for some texts it picks another mask than Encode, and both are valid
// codes (the decoder test reads every mask). The comparison is therefore at
// a fixed mask.
//
// For each text, level and mask qrencode picked, the matrix of this package
// forced to that mask, version and level is the same, module by module:
// the codewords, the interleaving, the placement, the masks and the format
// and version information all agree with an independent encoder.
func TestMatrixEqualsQrencode(t *testing.T) {
	needQrencode(t)
	for _, text := range qrencodeTexts() {
		for _, l := range []Level{L, M} {
			v, err := pickVersion(len(text), l)
			if err != nil {
				t.Fatal(err)
			}
			want := qrencodeMatrix(t, text, l, v)
			gotLevel, mask := maskOf(t, want)
			if gotLevel != l {
				t.Errorf("qrencode wrote level %v, asked %v", gotLevel, l)
			}
			c, err := encode(text, l, v, mask)
			if err != nil {
				t.Fatal(err)
			}
			if x, y, ok := sameMatrix(c.m, want); !ok {
				t.Errorf("%d bytes at %v, version %d, mask %d: differs from qrencode at (%d, %d)", len(text), l, v, mask, x, y)
			}
		}
	}
}
