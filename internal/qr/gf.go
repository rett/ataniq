package qr

// GF(256) with the QR code's primitive polynomial x^8+x^4+x^3+x^2+1 (0x11D)
// and generator element 2.

type gfTables struct {
	exp [510]byte
	log [256]int
}

var gf = newGF()

func newGF() *gfTables {
	t := new(gfTables)
	x := 1
	for i := 0; i < 255; i++ {
		t.exp[i] = byte(x)
		t.log[x] = i
		x <<= 1
		if x >= 256 {
			x ^= 0x11D
		}
	}
	for i := 255; i < len(t.exp); i++ {
		t.exp[i] = t.exp[i-255]
	}
	return t
}

func (t *gfTables) mul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return t.exp[t.log[a]+t.log[b]]
}

// rsGenerator returns the coefficients of the Reed-Solomon generator
// polynomial (x-a^0)(x-a^1)...(x-a^(degree-1)), highest power first, without
// the leading coefficient, which is 1.
func rsGenerator(degree int) []byte {
	g := make([]byte, degree)
	g[degree-1] = 1
	root := byte(1)
	for i := 0; i < degree; i++ {
		for j := range g {
			g[j] = gf.mul(g[j], root)
			if j+1 < len(g) {
				g[j] ^= g[j+1]
			}
		}
		root = gf.mul(root, 2)
	}
	return g
}

// rsRemainder returns the remainder of data (times x^len(gen)) divided by
// the generator polynomial gen: the error correction codewords of data.
func rsRemainder(data, gen []byte) []byte {
	r := make([]byte, len(gen))
	for _, b := range data {
		f := b ^ r[0]
		copy(r, r[1:])
		r[len(r)-1] = 0
		for i := range r {
			r[i] ^= gf.mul(gen[i], f)
		}
	}
	return r
}
