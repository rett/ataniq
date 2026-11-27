package credkind

import (
	"crypto/ed25519"
	"math/big"
	"slices"
)

// Field prime and Edwards d from RFC 8032 section 5.1.
var (
	skFieldPrime = new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	skCurveD, _  = new(big.Int).SetString("37095705934669439343138083508754565189542113879843219016388785533085940283555", 10)
)

// ValidateScratchSKKey checks a public Ed25519 point for canonical encoding,
// curve membership and small order. Enrollment must call it before storing
// the key. Its variable-time arithmetic operates only on public data.
func ValidateScratchSKKey(pub ed25519.PublicKey) error {
	if len(pub) != ed25519.PublicKeySize {
		return ErrKey
	}
	// RFC 8032 section 5.1.3 encodes y little-endian with the sign of x
	// in the high bit. Both signs of a nonzero x have the same order.
	encoded := [ed25519.PublicKeySize]byte(pub)
	encoded[31] &= 0x7f
	slices.Reverse(encoded[:])
	y := new(big.Int).SetBytes(encoded[:])
	if y.Sign() == 0 || y.Cmp(skFieldPrime) >= 0 {
		return ErrKey
	}
	one := big.NewInt(1)
	y2 := new(big.Int).Mul(y, y)
	y2.Mod(y2, skFieldPrime)
	// Recover x² = (y² - 1) / (d*y² + 1). Require a nonzero square:
	// x=0 covers both points of order at most two and their sign aliases.
	denom := new(big.Int).Mul(skCurveD, y2)
	denom.Add(denom, one)
	inverse := new(big.Int).ModInverse(denom, skFieldPrime)
	if inverse == nil {
		return ErrKey
	}
	x2 := new(big.Int).Sub(y2, one)
	x2.Mul(x2, inverse).Mod(x2, skFieldPrime)
	if big.Jacobi(x2, skFieldPrime) != 1 {
		return ErrKey
	}
	// The points of order four have y=0 (excluded above). A point of
	// order eight doubles to one of them. By Edwards doubling, the
	// numerator of y(2P) is y²+x² and its denominator is nonzero.
	sum := new(big.Int).Add(x2, y2)
	if sum.Mod(sum, skFieldPrime).Sign() == 0 {
		return ErrKey
	}
	return nil
}
