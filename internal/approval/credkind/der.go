package credkind

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"math/big"
)

// maxSPKI bounds a stored SubjectPublicKeyInfo; a P-256 one is 91 bytes.
const maxSPKI = 256

// parseSignature reads an ECDSA-Sig-Value, SEQUENCE { r INTEGER, s INTEGER },
// in strict DER: short-form lengths that are exact, each integer in its
// minimal two's-complement form, positive, and in [1, n-1] for the order n of
// P-256, and nothing after the sequence. For P-256 the whole signature is at
// most 72 bytes, so every length is in the short form.
func parseSignature(sig []byte) (r, s *big.Int, ok bool) {
	if len(sig) < 8 || len(sig) > 72 || sig[0] != 0x30 || int(sig[1]) != len(sig)-2 {
		return nil, nil, false
	}
	pos := 2
	r, ok = derInteger(sig, &pos)
	if !ok {
		return nil, nil, false
	}
	s, ok = derInteger(sig, &pos)
	if !ok || pos != len(sig) {
		return nil, nil, false
	}
	return r, s, true
}

// derInteger reads one INTEGER at *pos that is a valid scalar of P-256.
func derInteger(b []byte, pos *int) (*big.Int, bool) {
	p := *pos
	if len(b)-p < 3 || b[p] != 0x02 {
		return nil, false
	}
	n := int(b[p+1])
	if n < 1 || n > 33 || len(b)-p-2 < n {
		return nil, false
	}
	v := b[p+2 : p+2+n]
	if v[0]&0x80 != 0 {
		return nil, false // negative
	}
	if n > 1 && v[0] == 0 && v[1]&0x80 == 0 {
		return nil, false // a leading zero that is not needed
	}
	x := new(big.Int).SetBytes(v)
	if x.Sign() == 0 || x.Cmp(elliptic.P256().Params().N) >= 0 {
		return nil, false
	}
	*pos = p + 2 + n
	return x, true
}

// parseSPKI parses a stored SubjectPublicKeyInfo that must be a P-256 ECDSA
// key in canonical DER: marshalling the key again gives the same bytes.
func parseSPKI(spki []byte) (*ecdsa.PublicKey, bool) {
	if len(spki) == 0 || len(spki) > maxSPKI {
		return nil, false
	}
	pub, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, false
	}
	k, ok := pub.(*ecdsa.PublicKey)
	if !ok || k.Curve != elliptic.P256() {
		return nil, false
	}
	again, err := x509.MarshalPKIXPublicKey(k)
	if err != nil || !bytes.Equal(again, spki) {
		return nil, false
	}
	return k, true
}
