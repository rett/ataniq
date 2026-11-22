package seatapp

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
)

// The COSE_Key of an ES256 credential, as WebAuthn (section 6.5.1) and CTAP2
// (canonical CBOR) put it into the attested credential data:
//
//	{1: 2, 3: -7, -1: 1, -2: x (32 bytes), -3: y (32 bytes)}
//
// that is kty EC2, alg ES256, crv P-256 and the point, in the order of
// canonical CBOR (by encoded key: 1, 3, -1, -2, -3). This file reads exactly
// that and nothing else: a definite-length map of five entries in that order,
// every integer and length in its shortest form, no duplicate and no extra
// key. Anything the key is followed by is the extensions map the ED flag
// announces, which is read only to check it is one well-formed item.

// The limits of the reader of the extensions item.
const (
	maxCBORDepth = 8
	maxCBORItems = 64
)

// CBOR major types.
const (
	cborUint  = 0
	cborNint  = 1
	cborBytes = 2
	cborText  = 3
	cborArray = 4
	cborMap   = 5
	cborTag   = 6
	cborOther = 7
)

// cborReader reads definite-length CBOR from a byte slice, strictly.
type cborReader struct {
	b   []byte
	pos int
}

// head reads the initial byte and its argument. It refuses an indefinite
// length, a reserved additional-information value and an argument that is not
// in its shortest form. For major type 7 the argument is the simple value or
// the float's bits, and it is not checked for shortness.
func (r *cborReader) head() (major byte, arg uint64, ok bool) {
	if r.pos >= len(r.b) {
		return 0, 0, false
	}
	ib := r.b[r.pos]
	major, ai := ib>>5, ib&0x1f
	r.pos++
	switch {
	case ai < 24:
		return major, uint64(ai), true
	case ai > 27:
		return 0, 0, false // reserved, or indefinite (31)
	}
	n := 1 << (ai - 24) // 1, 2, 4 or 8 bytes
	if len(r.b)-r.pos < n {
		return 0, 0, false
	}
	for i := 0; i < n; i++ {
		arg = arg<<8 | uint64(r.b[r.pos+i])
	}
	r.pos += n
	if major == cborOther {
		return major, arg, true
	}
	min := [...]uint64{24, 1 << 8, 1 << 16, 1 << 32}[ai-24]
	if arg < min {
		return 0, 0, false // not the shortest form
	}
	return major, arg, true
}

// take returns the next n bytes.
func (r *cborReader) take(n uint64) ([]byte, bool) {
	if n > uint64(len(r.b)-r.pos) {
		return nil, false
	}
	s := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return s, true
}

// skip reads one item of any type and everything inside it, bounded by
// maxCBORDepth levels and maxCBORItems items; items counts down.
func (r *cborReader) skip(depth int, items *int) bool {
	if depth > maxCBORDepth || *items <= 0 {
		return false
	}
	*items--
	major, arg, ok := r.head()
	if !ok {
		return false
	}
	switch major {
	case cborUint, cborNint, cborOther:
		return true
	case cborBytes, cborText:
		_, ok := r.take(arg)
		return ok
	case cborTag:
		return r.skip(depth+1, items)
	case cborArray, cborMap:
		if major == cborMap {
			if arg > uint64(len(r.b)) {
				return false
			}
			arg *= 2
		}
		if arg > uint64(len(r.b)-r.pos) { // every item takes a byte
			return false
		}
		for i := uint64(0); i < arg; i++ {
			if !r.skip(depth+1, items) {
				return false
			}
		}
		return true
	}
	return false
}

// intIs reads one integer item and reports whether it is want, and whether
// it was an integer at all.
func (r *cborReader) intIs(want int64) (equal, isInt bool) {
	major, arg, ok := r.head()
	if !ok || (major != cborUint && major != cborNint) || arg > 1<<62 {
		return false, false
	}
	if major == cborUint {
		return int64(arg) == want, true
	}
	return -1-int64(arg) == want, true
}

// coseES256 reads the COSE_Key at the start of b. It returns the canonical DER
// SubjectPublicKeyInfo of the key and the number of bytes the COSE_Key took.
// A key that is not the five-entry map is [errAuthData]; a map of that shape
// that is not an EC2, ES256, P-256 key, or whose point is not on the curve,
// is [errKey].
func coseES256(b []byte) (spki []byte, n int, e *apiError) {
	r := &cborReader{b: b}
	if major, arg, ok := r.head(); !ok || major != cborMap || arg != 5 {
		return nil, 0, &errAuthData
	}
	wrongValue := false
	for _, entry := range []struct {
		key  int64
		want int64
	}{{1, 2}, {3, -7}, {-1, 1}} {
		if k, isInt := r.intIs(entry.key); !k || !isInt {
			return nil, 0, &errAuthData
		}
		v, isInt := r.intIs(entry.want)
		if !isInt {
			return nil, 0, &errAuthData
		}
		wrongValue = wrongValue || !v
	}
	var xy [2][]byte
	for i, key := range []int64{-2, -3} {
		if k, isInt := r.intIs(key); !k || !isInt {
			return nil, 0, &errAuthData
		}
		major, arg, ok := r.head()
		if !ok || major != cborBytes || arg != 32 {
			return nil, 0, &errAuthData
		}
		xy[i], ok = r.take(32)
		if !ok {
			return nil, 0, &errAuthData
		}
	}
	if wrongValue {
		return nil, 0, &errKey
	}
	point := append(append([]byte{4}, xy[0]...), xy[1]...)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		return nil, 0, &errKey
	}
	spki, err = x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, 0, &errKey
	}
	return spki, r.pos, nil
}

// extensionsOK reports whether b is exactly one CBOR map, well formed: the
// extensions of the authenticator data.
func extensionsOK(b []byte) bool {
	r := &cborReader{b: b}
	if len(b) == 0 || b[0]>>5 != cborMap {
		return false
	}
	items := maxCBORItems
	return r.skip(0, &items) && r.pos == len(b)
}
