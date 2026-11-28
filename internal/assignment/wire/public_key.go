package wire

import (
	"crypto/ed25519"
	"math/big"
	"slices"
)

// Public-key validation only. These variable-time operations must never
// handle a secret scalar. RFC 8032 sections 5.1 and 5.1.3 define p, d, L
// and the compressed point encoding.
var (
	one           = big.NewInt(1)
	fieldPrime    = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	curveD, _     = new(big.Int).SetString("37095705934669439343138083508754565189542113879843219016388785533085940283555", 10)
	groupOrder, _ = new(big.Int).SetString("7237005577332262213973186563042994240857116359379907606001950938285454250989", 10)
)

func fieldMul(a, b *big.Int) *big.Int {
	product := new(big.Int).Mul(a, b)
	return product.Mod(product, fieldPrime)
}

// Extended homogeneous coordinates and complete addition from RFC 8032
// section 5.1.4. The same formula doubles a point when both operands agree.
type edwardsPoint struct{ x, y, z, t *big.Int }

func (p edwardsPoint) add(q edwardsPoint) edwardsPoint {
	a := fieldMul(new(big.Int).Sub(p.y, p.x), new(big.Int).Sub(q.y, q.x))
	b := fieldMul(new(big.Int).Add(p.y, p.x), new(big.Int).Add(q.y, q.x))
	c := fieldMul(curveD, fieldMul(p.t, q.t))
	c.Lsh(c, 1)
	d := fieldMul(p.z, q.z)
	d.Lsh(d, 1)
	e, f := new(big.Int).Sub(b, a), new(big.Int).Sub(d, c)
	g, h := new(big.Int).Add(d, c), new(big.Int).Add(b, a)
	return edwardsPoint{fieldMul(e, f), fieldMul(g, h), fieldMul(f, g), fieldMul(e, h)}
}

func validPublic(pub ed25519.PublicKey) bool {
	if len(pub) != ed25519.PublicKeySize {
		return false
	}
	encoded := slices.Clone(pub)
	encoded[31] &= 0x7f
	slices.Reverse(encoded)
	y := new(big.Int).SetBytes(encoded)
	if y.Cmp(fieldPrime) >= 0 {
		return false
	}
	y2 := fieldMul(y, y)
	denom := fieldMul(curveD, y2)
	denom.Add(denom, one)
	reciprocal := new(big.Int).ModInverse(denom, fieldPrime)
	if reciprocal == nil {
		return false
	}
	x2 := fieldMul(new(big.Int).Sub(y2, one), reciprocal)
	x := new(big.Int).ModSqrt(x2, fieldPrime)
	if x == nil || x.Sign() == 0 {
		return false
	}
	// Either sign of nonzero x has the same order. Require [L]P to be
	// the identity. This also excludes points with a torsion component.
	p := edwardsPoint{x, y, one, fieldMul(x, y)}
	multiple := p
	for i := groupOrder.BitLen() - 2; i >= 0; i-- {
		multiple = multiple.add(multiple)
		if groupOrder.Bit(i) != 0 {
			multiple = multiple.add(p)
		}
	}
	return multiple.x.Sign() == 0 && multiple.y.Cmp(multiple.z) == 0
}
