package qr

import (
	"fmt"
	"testing"
)

func bits15(s string) int {
	v := 0
	for _, c := range s {
		v = v<<1 | int(c-'0')
	}
	return v
}

func TestGFTables(t *testing.T) {
	// 2^8 reduces by 0x11D to 0x1D; 2 is a generator of the 255 nonzero
	// elements.
	if gf.exp[8] != 0x1D {
		t.Errorf("2^8 = %#x, want 0x1d", gf.exp[8])
	}
	seen := map[byte]bool{}
	for i := 0; i < 255; i++ {
		if seen[gf.exp[i]] {
			t.Fatalf("2^%d repeats", i)
		}
		seen[gf.exp[i]] = true
	}
	if gf.exp[255] != 1 {
		t.Errorf("2^255 = %d, want 1", gf.exp[255])
	}
	for a := 1; a < 256; a++ {
		if got := gf.mul(byte(a), 1); got != byte(a) {
			t.Fatalf("%d*1 = %d", a, got)
		}
		inv := gf.exp[255-gf.log[a]]
		if got := gf.mul(byte(a), inv); got != 1 {
			t.Fatalf("%d * inverse = %d", a, got)
		}
	}
	if gf.mul(0, 7) != 0 || gf.mul(7, 0) != 0 {
		t.Error("zero does not annihilate")
	}
}

func TestRSGenerator(t *testing.T) {
	// The generator of degree 7 as the standard prints it, in powers of 2,
	// highest power of x first, after the leading 1 (a^0).
	want := []int{87, 229, 146, 149, 238, 102, 21}
	g := rsGenerator(7)
	if len(g) != 7 {
		t.Fatalf("len %d", len(g))
	}
	for i, e := range want {
		if g[i] != gf.exp[e] {
			t.Errorf("coefficient %d = a^%d, want a^%d", i, gf.log[g[i]], e)
		}
	}
	// Degree 10 begins a^251, a^67, a^46 ...
	g = rsGenerator(10)
	for i, e := range []int{251, 67, 46, 61, 118, 70, 64, 94, 32, 45} {
		if g[i] != gf.exp[e] {
			t.Errorf("degree 10 coefficient %d = a^%d, want a^%d", i, gf.log[g[i]], e)
		}
	}
	// Every generator has the roots 2^0 ... 2^(d-1).
	for _, d := range []int{7, 10, 13, 15, 16, 18, 20, 22, 24, 26, 30} {
		g := rsGenerator(d)
		for r := 0; r < d; r++ {
			x := gf.exp[r]
			// Horner: x^d + g[0]x^(d-1) + ... + g[d-1].
			v := byte(1)
			for _, c := range g {
				v = gf.mul(v, x) ^ c
			}
			if v != 0 {
				t.Fatalf("degree %d: 2^%d is not a root", d, r)
			}
		}
	}
}

func TestRSRemainderKnown(t *testing.T) {
	// "HELLO WORLD" at version 1, level M: data codewords and the ten error
	// correction codewords from the standard's worked example.
	data := []byte{32, 91, 11, 120, 209, 114, 220, 77, 67, 64, 236, 17, 236, 17, 236, 17}
	want := []byte{196, 35, 39, 119, 235, 215, 231, 226, 93, 23}
	got := rsRemainder(data, rsGenerator(10))
	if string(got) != string(want) {
		t.Errorf("remainder %v, want %v", got, want)
	}
	// A codeword sequence with its remainder appended is a multiple of the
	// generator: dividing it leaves zero.
	full := append(append([]byte{}, data...), want...)
	for i, b := range rsRemainder(full, rsGenerator(10)) {
		if b != 0 {
			t.Errorf("remainder of data+ec byte %d = %d, want 0", i, b)
		}
	}
}

func TestFormatInfoKnown(t *testing.T) {
	want := map[Level][8]string{
		L: {"111011111000100", "111001011110011", "111110110101010", "111100010011101",
			"110011000101111", "110001100011000", "110110001000001", "110100101110110"},
		M: {"101010000010010", "101000100100101", "101111001111100", "101101101001011",
			"100010111111001", "100000011001110", "100111110010111", "100101010100000"},
	}
	for l, row := range want {
		for mask, s := range row {
			if got := formatInfo(l, mask); got != bits15(s) {
				t.Errorf("level %v mask %d: %015b, want %s", l, mask, got, s)
			}
		}
	}
}

func TestVersionInfoKnown(t *testing.T) {
	want := map[int]int{7: 0x07C94, 8: 0x085BC, 9: 0x09A99, 10: 0x0A4D3}
	for v, w := range want {
		if got := versionInfo(v); got != w {
			t.Errorf("version %d: %#x, want %#x", v, got, w)
		}
	}
}

// matrixOf builds a matrix from rows of '#' (dark) and '.' (light).
func matrixOf(rows ...string) *matrix {
	m := newMatrix(len(rows))
	for y, r := range rows {
		if len(r) != len(rows) {
			panic("not square")
		}
		for x, c := range r {
			m.mod[y*m.n+x] = c == '#'
		}
	}
	return m
}

func rowsOf(n int, f func(x, y int) bool) []string {
	rows := make([]string, n)
	for y := range rows {
		b := make([]byte, n)
		for x := range b {
			b[x] = '.'
			if f(x, y) {
				b[x] = '#'
			}
		}
		rows[y] = string(b)
	}
	return rows
}

func TestPenaltyHandMade(t *testing.T) {
	type score struct{ n1, n2, n3, n4 int }
	cases := []struct {
		name string
		m    *matrix
		want score
	}{
		{
			// Ten runs of five (5 rows and 5 columns), sixteen uniform 2 by
			// 2 blocks, no dark module: 0% dark is 10 steps from 50%.
			"all light 5x5",
			matrixOf(rowsOf(5, func(x, y int) bool { return false })...),
			score{30, 48, 0, 100},
		},
		{
			"all dark 5x5",
			matrixOf(rowsOf(5, func(x, y int) bool { return true })...),
			score{30, 48, 0, 100},
		},
		{
			// Four light rows and four light columns of five: 8 runs; the
			// twelve blocks that miss the center are uniform; 1 of 25 dark
			// is 4% and 9 steps from 50%.
			"one dark module",
			matrixOf(rowsOf(5, func(x, y int) bool { return x == 2 && y == 2 })...),
			score{24, 36, 0, 90},
		},
		{
			// No run of five, no uniform block, half dark.
			"checkerboard 6x6",
			matrixOf(rowsOf(6, func(x, y int) bool { return (x+y)%2 == 0 })...),
			score{0, 0, 0, 0},
		},
		{
			// Only the first row of an 8 by 8 matrix is dark. Rule 1: the
			// dark row is a run of 8 (3+3); each of the seven light rows
			// is a run of 8 (3+3 each); each column is one dark module and
			// a light run of 7 (3+2 each): 6 + 42 + 40. Rule 2: the 42
			// blocks of the seven light rows. Rule 4: 8 of 64 dark is 12.5%,
			// 37.5 points from 50, 7 full steps.
			"runs longer than five",
			matrixOf(rowsOf(8, func(x, y int) bool { return y == 0 })...),
			score{88, 126, 0, 70},
		},
		{
			// 1011101 with four light modules on both sides, in a row of a
			// matrix of light modules: one match with light before and
			// light after is 80. The row is otherwise light, which scores
			// rule 1 and rule 2 as well; only rule 3 is checked here.
			"finder-like pattern both sides",
			matrixOf(rowsOf(15, func(x, y int) bool {
				return y == 7 && x >= 4 && x <= 10 && (x == 4 || x == 6 || x == 7 || x == 8 || x == 10)
			})...),
			score{-1, -1, 80, -1},
		},
		{
			// The same pattern in a column, with dark modules on one side
			// that spoil one of the two quiet sides: 40.
			"finder-like pattern one side",
			matrixOf(rowsOf(15, func(x, y int) bool {
				if x == 7 && y >= 4 && y <= 10 {
					return y == 4 || y == 6 || y == 7 || y == 8 || y == 10
				}
				return x == 7 && y == 13
			})...),
			score{-1, -1, 40, -1},
		},
	}
	for _, c := range cases {
		got := score{c.m.penaltyRuns(), c.m.penaltyBlocks(), c.m.penaltyFinderLike(), c.m.penaltyBalance()}
		want := c.want
		if want.n1 < 0 {
			got.n1, got.n2, got.n4 = -1, -1, -1
		}
		if got != want {
			t.Errorf("%s: rules 1-4 score %+v, want %+v", c.name, got, want)
		}
		if want.n1 >= 0 {
			if total := c.m.penalty(); total != want.n1+want.n2+want.n3+want.n4 {
				t.Errorf("%s: penalty %d, want the sum %d", c.name, total, want.n1+want.n2+want.n3+want.n4)
			}
		}
	}
}

func TestPenaltyBalanceSteps(t *testing.T) {
	// 100 modules in a 10 by 10 matrix: dark share d percent costs 10 per
	// full 5 percent away from 50.
	for _, c := range []struct{ dark, want int }{
		{50, 0}, {47, 0}, {46, 0}, {45, 10}, {44, 10}, {40, 20}, {55, 10}, {54, 0}, {0, 100}, {100, 100},
	} {
		m := newMatrix(10)
		for i := 0; i < c.dark; i++ {
			m.mod[i] = true
		}
		if got := m.penaltyBalance(); got != c.want {
			t.Errorf("%d dark of 100: %d, want %d", c.dark, got, c.want)
		}
	}
}

func TestBlockSpecs(t *testing.T) {
	// Total codewords per version are the same at both levels; what the
	// function patterns leave of the matrix is that many codewords, and
	// remainder bits (0 in versions 1, 7 to 10 and 7 in 2 to 6).
	total := map[int]int{1: 26, 2: 44, 3: 70, 4: 100, 5: 134, 6: 172, 7: 196, 8: 242, 9: 292, 10: 346}
	rem := map[int]int{1: 0, 2: 7, 3: 7, 4: 7, 5: 7, 6: 7, 7: 0, 8: 0, 9: 0, 10: 0}
	for v := MinVersion; v <= MaxVersion; v++ {
		for _, l := range []Level{L, M} {
			s := specs[l][v]
			if got := s.dataCodewords() + s.blocks()*s.ecPerBlock; got != total[v] {
				t.Errorf("version %d level %v: %d codewords, want %d", v, l, got, total[v])
			}
		}
		m := newMatrix(sizeOf(v))
		m.drawFunctionPatterns(v)
		free := 0
		for _, f := range m.fn {
			if !f {
				free++
			}
		}
		if free != total[v]*8+rem[v] {
			t.Errorf("version %d: %d data modules, want %d codewords and %d bits", v, free, total[v], rem[v])
		}
	}
	// Capacities in bytes as the standard prints them.
	for v, want := range map[int][2]int{1: {17, 14}, 5: {106, 84}, 9: {230, 180}, 10: {271, 213}} {
		if got := [2]int{byteCapacity(v, L), byteCapacity(v, M)}; got != want {
			t.Errorf("version %d capacity %v, want %v", v, got, want)
		}
	}
}

func TestDataCodewordsHelloWorld(t *testing.T) {
	// Byte mode "Hi": 0100 00000010 01001000 01101001 0000 then pad.
	got := dataCodewords("Hi", 1, M)
	want := []byte{0x40, 0x24, 0x86, 0x90, 0xEC, 0x11, 0xEC, 0x11, 0xEC, 0x11, 0xEC, 0x11, 0xEC, 0x11, 0xEC, 0x11}
	if string(got) != string(want) {
		t.Errorf("% x, want % x", got, want)
	}
	// A text that fills the version exactly has a shortened terminator.
	full := make([]byte, byteCapacity(1, M))
	cw := dataCodewords(string(full), 1, M)
	if len(cw) != specs[M][1].dataCodewords() {
		t.Errorf("full version 1: %d codewords", len(cw))
	}
}

func TestMaskInvolution(t *testing.T) {
	base := newMatrix(sizeOf(2))
	base.drawFunctionPatterns(2)
	for mask := 0; mask < 8; mask++ {
		m := base.clone()
		m.applyMask(mask)
		changed := 0
		for i := range m.mod {
			if m.mod[i] != base.mod[i] {
				changed++
				if m.fn[i] {
					t.Fatalf("mask %d changed a function module", mask)
				}
			}
		}
		if changed == 0 {
			t.Errorf("mask %d changed nothing", mask)
		}
		m.applyMask(mask)
		for i := range m.mod {
			if m.mod[i] != base.mod[i] {
				t.Fatalf("mask %d twice is not the identity", mask)
			}
		}
	}
}

func TestFunctionPatternSpots(t *testing.T) {
	m := newMatrix(sizeOf(7))
	m.drawFunctionPatterns(7)
	n := m.n
	dark := map[string]bool{
		"finder corner":        m.get(0, 0),
		"finder inner ring":    !m.get(1, 1),
		"finder center":        m.get(3, 3),
		"timing 8":             m.get(8, 6),
		"timing 9 light":       !m.get(9, 6),
		"dark module":          m.get(8, n-8),
		"alignment center":     m.get(22, 22),
		"alignment ring light": !m.get(21, 22),
		"alignment corner":     m.get(20, 20),
		"top right finder":     m.get(n-1, 0),
		"separator light":      !m.get(7, 0) && !m.get(7, 7) && !m.get(0, 7),
	}
	for name, ok := range dark {
		if !ok {
			t.Errorf("%s is wrong", name)
		}
	}
	// Alignment patterns at the finder corners are skipped: the module
	// next to finder center area keeps the separator.
	if fmt.Sprint(alignmentCenters[7]) != "[6 22 38]" {
		t.Error("alignment centers of version 7")
	}
}
