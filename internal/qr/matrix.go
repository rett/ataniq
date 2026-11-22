package qr

// matrix is a square grid of modules. fn marks the function modules
// (finder, separator, timing and alignment patterns, the format and version
// information and the dark module), which data and masks never touch.
type matrix struct {
	n   int
	mod []bool
	fn  []bool
}

func newMatrix(n int) *matrix {
	return &matrix{n: n, mod: make([]bool, n*n), fn: make([]bool, n*n)}
}

func (m *matrix) clone() *matrix {
	c := &matrix{n: m.n, mod: make([]bool, len(m.mod)), fn: m.fn}
	copy(c.mod, m.mod)
	return c
}

func (m *matrix) get(x, y int) bool { return m.mod[y*m.n+x] }

func (m *matrix) setFunc(x, y int, dark bool) {
	m.mod[y*m.n+x] = dark
	m.fn[y*m.n+x] = true
}

// drawFunctionPatterns draws everything but the data, the format
// information and the version information, and reserves the places of the
// two latter ones.
func (m *matrix) drawFunctionPatterns(version int) {
	n := m.n
	for i := 0; i < n; i++ {
		m.setFunc(6, i, i%2 == 0)
		m.setFunc(i, 6, i%2 == 0)
	}
	m.drawFinder(3, 3)
	m.drawFinder(n-4, 3)
	m.drawFinder(3, n-4)
	cs := alignmentCenters[version]
	for i, cy := range cs {
		for j, cx := range cs {
			last := len(cs) - 1
			if (i == 0 && j == 0) || (i == 0 && j == last) || (i == last && j == 0) {
				continue
			}
			m.drawAlignment(cx, cy)
		}
	}
	m.drawFormat(M, 0)
	m.drawVersion(version)
}

func (m *matrix) drawFinder(cx, cy int) {
	for dy := -4; dy <= 4; dy++ {
		for dx := -4; dx <= 4; dx++ {
			x, y := cx+dx, cy+dy
			if x < 0 || y < 0 || x >= m.n || y >= m.n {
				continue
			}
			d := max(abs(dx), abs(dy))
			m.setFunc(x, y, d != 2 && d != 4)
		}
	}
}

func (m *matrix) drawAlignment(cx, cy int) {
	for dy := -2; dy <= 2; dy++ {
		for dx := -2; dx <= 2; dx++ {
			m.setFunc(cx+dx, cy+dy, max(abs(dx), abs(dy)) != 1)
		}
	}
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// formatInfo returns the 15 bits of the format information of a level and a
// mask: the five data bits, ten BCH bits, and the XOR mask 101010000010010.
func formatInfo(l Level, mask int) int {
	data := l.formatBits()<<3 | mask
	rem := data
	for i := 0; i < 10; i++ {
		rem = rem<<1 ^ (rem>>9)*0x537
	}
	return (data<<10 | rem) ^ 0x5412
}

func bit(v, i int) bool { return v>>uint(i)&1 != 0 }

// drawFormat writes both copies of the format information and the dark
// module.
func (m *matrix) drawFormat(l Level, mask int) {
	bits := formatInfo(l, mask)
	n := m.n
	for i := 0; i <= 5; i++ {
		m.setFunc(8, i, bit(bits, i))
	}
	m.setFunc(8, 7, bit(bits, 6))
	m.setFunc(8, 8, bit(bits, 7))
	m.setFunc(7, 8, bit(bits, 8))
	for i := 9; i < 15; i++ {
		m.setFunc(14-i, 8, bit(bits, i))
	}
	for i := 0; i < 8; i++ {
		m.setFunc(n-1-i, 8, bit(bits, i))
	}
	for i := 8; i < 15; i++ {
		m.setFunc(8, n-15+i, bit(bits, i))
	}
	m.setFunc(8, n-8, true)
}

// versionInfo returns the 18 bits of the version information of a version
// from 7 up: six data bits and twelve BCH bits.
func versionInfo(version int) int {
	rem := version
	for i := 0; i < 12; i++ {
		rem = rem<<1 ^ (rem>>11)*0x1F25
	}
	return version<<12 | rem
}

func (m *matrix) drawVersion(version int) {
	if version < 7 {
		return
	}
	bits := versionInfo(version)
	for i := 0; i < 18; i++ {
		a := m.n - 11 + i%3
		b := i / 3
		m.setFunc(a, b, bit(bits, i))
		m.setFunc(b, a, bit(bits, i))
	}
}

// placeData writes the codewords into the modules that are not function
// modules, in the standard zigzag from the lower right corner. Modules left
// over (the remainder bits) stay light.
func (m *matrix) placeData(data []byte) {
	n := m.n
	i := 0
	for right := n - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for vert := 0; vert < n; vert++ {
			for j := 0; j < 2; j++ {
				x := right - j
				upward := (right+1)&2 == 0
				y := vert
				if upward {
					y = n - 1 - vert
				}
				if !m.fn[y*n+x] && i < len(data)*8 {
					m.mod[y*n+x] = data[i>>3]>>uint(7-i&7)&1 != 0
					i++
				}
			}
		}
	}
}

func maskCond(mask, x, y int) bool {
	switch mask {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	case 7:
		return ((x+y)%2+x*y%3)%2 == 0
	}
	panic("qr: mask out of range")
}

// applyMask flips the data modules the mask selects. Applying it twice
// restores the matrix.
func (m *matrix) applyMask(mask int) {
	for y := 0; y < m.n; y++ {
		for x := 0; x < m.n; x++ {
			if !m.fn[y*m.n+x] && maskCond(mask, x, y) {
				m.mod[y*m.n+x] = !m.mod[y*m.n+x]
			}
		}
	}
}
