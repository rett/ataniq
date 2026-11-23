package qr

import (
	"errors"
	"fmt"
)

// ErrTooLong is returned by Encode for a text that does not fit version 10
// at the level asked for.
var ErrTooLong = errors.New("qr: text too long for a version 10 code")

// Code is an encoded QR code: a square grid of dark and light modules, with
// no quiet zone.
type Code struct {
	m       *matrix
	version int
	level   Level
	mask    int
}

// Size is the number of modules on a side.
func (c *Code) Size() int { return c.m.n }

// Module reports whether the module in column x and row y is dark; (0, 0)
// is the top left corner. A place outside the code is light.
func (c *Code) Module(x, y int) bool {
	if x < 0 || y < 0 || x >= c.m.n || y >= c.m.n {
		return false
	}
	return c.m.get(x, y)
}

// Version is the version of the code, 1 to 10.
func (c *Code) Version() int { return c.version }

// Level is the error correction level of the code.
func (c *Code) Level() Level { return c.level }

// Mask is the number of the mask applied, 0 to 7.
func (c *Code) Mask() int { return c.mask }

// Encode encodes the bytes of text in byte mode at the given level in the
// smallest version that holds them. It returns ErrTooLong when no version up
// to 10 does.
func Encode(text string, level Level) (*Code, error) {
	version, err := pickVersion(len(text), level)
	if err != nil {
		return nil, err
	}
	return encode(text, level, version, -1)
}

func pickVersion(n int, level Level) (int, error) {
	if level != L && level != M {
		return 0, fmt.Errorf("qr: unknown error correction level %d", int(level))
	}
	for v := MinVersion; v <= MaxVersion; v++ {
		if n <= byteCapacity(v, level) {
			return v, nil
		}
	}
	return 0, ErrTooLong
}

// encode builds the code of text in the given version. mask is the mask to
// use, or -1 to try all eight and keep the one with the lowest penalty (the
// lowest number on a tie).
func encode(text string, level Level, version, mask int) (*Code, error) {
	if level != L && level != M {
		return nil, fmt.Errorf("qr: unknown error correction level %d", int(level))
	}
	if version < MinVersion || version > MaxVersion || len(text) > byteCapacity(version, level) {
		return nil, ErrTooLong
	}
	if mask < -1 || mask > 7 {
		return nil, fmt.Errorf("qr: mask %d out of range", mask)
	}
	data := dataCodewords(text, version, level)
	all := addErrorCorrection(data, version, level)
	base := newMatrix(sizeOf(version))
	base.drawFunctionPatterns(version)
	base.placeData(all)

	masks := []int{mask}
	if mask < 0 {
		masks = []int{0, 1, 2, 3, 4, 5, 6, 7}
	}
	var best *matrix
	bestMask, bestPenalty := 0, 0
	for _, k := range masks {
		m := base.clone()
		m.applyMask(k)
		m.drawFormat(level, k)
		if p := m.penalty(); best == nil || p < bestPenalty {
			best, bestMask, bestPenalty = m, k, p
		}
	}
	return &Code{m: best, version: version, level: level, mask: bestMask}, nil
}

// bitBuffer collects bits, most significant first.
type bitBuffer struct {
	b []byte
	n int
}

func (w *bitBuffer) add(v, count int) {
	for i := count - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		if bit(v, i) {
			w.b[w.n/8] |= 0x80 >> uint(w.n%8)
		}
		w.n++
	}
}

// dataCodewords returns the data codewords of text: the mode indicator, the
// character count, the bytes, the terminator and the pad codewords.
func dataCodewords(text string, version int, level Level) []byte {
	capacity := specs[level][version].dataCodewords() * 8
	var w bitBuffer
	w.add(0x4, 4)
	w.add(len(text), countBits(version))
	for i := 0; i < len(text); i++ {
		w.add(int(text[i]), 8)
	}
	w.add(0, min(4, capacity-w.n))
	w.add(0, (8-w.n%8)%8)
	for pad := 0xEC; len(w.b)*8 < capacity; pad ^= 0xEC ^ 0x11 {
		w.add(pad, 8)
	}
	return w.b
}

// addErrorCorrection splits data into blocks, appends the Reed-Solomon
// codewords to each and interleaves the blocks: the first data codeword of
// every block, then the second, and so on, and then the same for the error
// correction codewords.
func addErrorCorrection(data []byte, version int, level Level) []byte {
	s := specs[level][version]
	gen := rsGenerator(s.ecPerBlock)
	blocks := make([][]byte, 0, s.blocks())
	ecs := make([][]byte, 0, s.blocks())
	pos := 0
	for i := 0; i < s.blocks(); i++ {
		size := s.data1
		if i >= s.blocks1 {
			size++
		}
		d := data[pos : pos+size]
		pos += size
		blocks = append(blocks, d)
		ecs = append(ecs, rsRemainder(d, gen))
	}
	out := make([]byte, 0, len(data)+s.blocks()*s.ecPerBlock)
	for i := 0; i <= s.data1; i++ {
		for _, d := range blocks {
			if i < len(d) {
				out = append(out, d[i])
			}
		}
	}
	for i := 0; i < s.ecPerBlock; i++ {
		for _, e := range ecs {
			out = append(out, e[i])
		}
	}
	return out
}
