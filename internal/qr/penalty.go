package qr

// Penalty points of the four standard mask evaluation rules.
const (
	penaltyN1 = 3
	penaltyN2 = 3
	penaltyN3 = 40
	penaltyN4 = 10
)

// penalty returns the total penalty of a matrix. It works on a matrix of any
// size.
func (m *matrix) penalty() int {
	return m.penaltyRuns() + m.penaltyBlocks() + m.penaltyFinderLike() + m.penaltyBalance()
}

// penaltyRuns is rule 1: a run of five or more modules of one color in a row
// or a column scores 3 plus 1 for each module beyond five.
func (m *matrix) penaltyRuns() int {
	total := 0
	for line := 0; line < m.n; line++ {
		for _, horizontal := range []bool{true, false} {
			run := 0
			var prev bool
			for k := 0; k < m.n; k++ {
				c := m.at(line, k, horizontal)
				if k > 0 && c == prev {
					run++
				} else {
					if run >= 5 {
						total += penaltyN1 + run - 5
					}
					run = 1
				}
				prev = c
			}
			if run >= 5 {
				total += penaltyN1 + run - 5
			}
		}
	}
	return total
}

// at is the k-th module of a row (horizontal) or of a column.
func (m *matrix) at(line, k int, horizontal bool) bool {
	if horizontal {
		return m.mod[line*m.n+k]
	}
	return m.mod[k*m.n+line]
}

// penaltyBlocks is rule 2: each 2 by 2 block of one color scores 3.
func (m *matrix) penaltyBlocks() int {
	total := 0
	for y := 0; y+1 < m.n; y++ {
		for x := 0; x+1 < m.n; x++ {
			c := m.get(x, y)
			if c == m.get(x+1, y) && c == m.get(x, y+1) && c == m.get(x+1, y+1) {
				total += penaltyN2
			}
		}
	}
	return total
}

// penaltyFinderLike is rule 3: the pattern dark, light, dark dark dark,
// light, dark (1:1:3:1:1) with four light modules before it scores 40, and
// with four light modules after it scores 40, in rows and in columns. The
// modules beyond the edge count as light, as the quiet zone is.
func (m *matrix) penaltyFinderLike() int {
	pattern := [7]bool{true, false, true, true, true, false, true}
	total := 0
	for line := 0; line < m.n; line++ {
		for _, horizontal := range []bool{true, false} {
			get := func(k int) bool {
				if k < 0 || k >= m.n {
					return false
				}
				return m.at(line, k, horizontal)
			}
			for k := 0; k+7 <= m.n; k++ {
				match := true
				for i, p := range pattern {
					if get(k+i) != p {
						match = false
						break
					}
				}
				if !match {
					continue
				}
				if !get(k-4) && !get(k-3) && !get(k-2) && !get(k-1) {
					total += penaltyN3
				}
				if !get(k+7) && !get(k+8) && !get(k+9) && !get(k+10) {
					total += penaltyN3
				}
			}
		}
	}
	return total
}

// penaltyBalance is rule 4: 10 points for each full 5 percent the share of
// dark modules is away from 50 percent.
func (m *matrix) penaltyBalance() int {
	dark := 0
	for _, d := range m.mod {
		if d {
			dark++
		}
	}
	total := len(m.mod)
	return abs(dark*20-total*10) / total * penaltyN4
}
