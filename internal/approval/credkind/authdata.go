package credkind

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/binary"
)

// The flags of the authenticator data (WebAuthn level 3, section 6.1). Bits
// 1 and 5 are reserved, and ignored as the specification says.
const (
	flagUP = 0x01
	flagUV = 0x04
	flagBE = 0x08
	flagBS = 0x10
	flagAT = 0x40
	flagED = 0x80
)

// Sizes of the authenticator data and its parts.
const (
	authHead        = 32 + 1 + 4 // rpIdHash, flags, signCount
	attestedHead    = 16 + 2     // aaguid, credentialIdLength
	minCredentialID = 16
	maxCredentialID = 1023 // WebAuthn level 3, section 4
	maxAssertionAD  = 1024
	maxRegisterAD   = 4096
)

// authData is the authenticator data, parsed.
type authData struct {
	rpIDHash []byte // 32 bytes
	flags    byte
	count    uint32
	// Of a registration only: the attested credential data.
	aaguid [16]byte
	credID []byte
	spki   []byte // the COSE_Key, as canonical DER SubjectPublicKeyInfo
}

// parseAssertionAuthData parses the authenticator data of an assertion: the
// head, then only the extensions map the ED flag announces. Attested
// credential data in an assertion is refused.
func parseAssertionAuthData(b []byte) (authData, error) {
	if len(b) < authHead || len(b) > maxAssertionAD {
		return authData{}, ErrAuthData
	}
	ad := parseHead(b)
	if ad.flags&flagAT != 0 {
		return authData{}, ErrAuthData
	}
	if !extensionsFit(ad.flags, b[authHead:]) {
		return authData{}, ErrAuthData
	}
	return ad, nil
}

// parseRegistrationAuthData parses the authenticator data of a registration:
// the head, the attested credential data the AT flag must announce (an ES256
// P-256 COSE_Key in its canonical form), then only the extensions map the ED
// flag announces. A COSE_Key of the right shape for another key type,
// algorithm or curve, or whose point is not on the curve, is ErrKey; any
// other fault is ErrAuthData.
func parseRegistrationAuthData(b []byte) (authData, error) {
	if len(b) < authHead+attestedHead || len(b) > maxRegisterAD {
		return authData{}, ErrAuthData
	}
	ad := parseHead(b)
	if ad.flags&flagAT == 0 {
		return authData{}, ErrAuthData
	}
	copy(ad.aaguid[:], b[authHead:authHead+16])
	n := int(binary.BigEndian.Uint16(b[authHead+16 : authHead+attestedHead]))
	start := authHead + attestedHead
	if n < minCredentialID || n > maxCredentialID || len(b) < start+n {
		return authData{}, ErrAuthData
	}
	ad.credID = b[start : start+n]
	spki, used, err := coseES256(b[start+n:])
	if err != nil {
		return authData{}, err
	}
	ad.spki = spki
	if !extensionsFit(ad.flags, b[start+n+used:]) {
		return authData{}, ErrAuthData
	}
	return ad, nil
}

func parseHead(b []byte) authData {
	return authData{
		rpIDHash: b[:32],
		flags:    b[32],
		count:    binary.BigEndian.Uint32(b[33:37]),
	}
}

// extensionsFit reports whether rest is what the flags announce: one strict
// extensions map with ED, nothing without it.
func extensionsFit(flags byte, rest []byte) bool {
	if flags&flagED != 0 {
		return extensionsOK(rest)
	}
	return len(rest) == 0
}

// coseES256 reads the COSE_Key at the start of b: exactly the five-entry map
// {1: 2, 3: -7, -1: 1, -2: x, -3: y} of an EC2 ES256 key on P-256, in the
// canonical order of CTAP2, x and y 32-byte strings. It returns the
// canonical DER SubjectPublicKeyInfo and the bytes the key took.
func coseES256(b []byte) ([]byte, int, error) {
	r := &cborReader{b: b}
	if major, n, ok := r.head(); !ok || major != cborMap || n != 5 {
		return nil, 0, ErrAuthData
	}
	wrong := false
	for _, e := range [...]struct{ key, want int64 }{{1, 2}, {3, -7}, {-1, 1}} {
		if k, isInt := r.intIs(e.key); !k || !isInt {
			return nil, 0, ErrAuthData
		}
		v, isInt := r.intIs(e.want)
		if !isInt {
			return nil, 0, ErrAuthData
		}
		wrong = wrong || !v
	}
	var xy [2][]byte
	for i, key := range [...]int64{-2, -3} {
		if k, isInt := r.intIs(key); !k || !isInt {
			return nil, 0, ErrAuthData
		}
		major, n, ok := r.head()
		if !ok || major != cborBytes || n != 32 {
			return nil, 0, ErrAuthData
		}
		xy[i], _ = r.take(32)
		if xy[i] == nil {
			return nil, 0, ErrAuthData
		}
	}
	if wrong {
		return nil, 0, ErrKey
	}
	point := make([]byte, 0, 65)
	point = append(append(append(point, 4), xy[0]...), xy[1]...)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
	if err != nil {
		return nil, 0, ErrKey
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, 0, ErrKey
	}
	return spki, r.pos, nil
}
