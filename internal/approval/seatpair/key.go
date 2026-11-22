package seatpair

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
)

const (
	// MinCredentialIDLen and MaxCredentialIDLen bound the length in bytes of a
	// credential id.
	MinCredentialIDLen = 16
	MaxCredentialIDLen = 1024

	// maxKeyLen bounds what is parsed at all. A P-256 key is 91 bytes.
	maxKeyLen = 4096
)

// The refusals of a credential.
const (
	// ErrCredentialID: the credential id is shorter than MinCredentialIDLen
	// or longer than MaxCredentialIDLen.
	ErrCredentialID Error = "seatpair: credential id must be 16 to 1024 bytes"
	// ErrKeyFormat: the bytes are not one DER SubjectPublicKeyInfo and
	// nothing else (garbage, trailing bytes, or a form that is not the
	// canonical encoding of its key).
	ErrKeyFormat Error = "seatpair: credential key is not a DER SubjectPublicKeyInfo"
	// ErrKeyAlgorithm: the key is of another algorithm than ES256, which is
	// ECDSA on the curve P-256.
	ErrKeyAlgorithm Error = "seatpair: credential key is not an ES256 (ECDSA P-256) key"
	// ErrKeyPoint: the key says P-256 and its point is not a point on it.
	ErrKeyPoint Error = "seatpair: credential key is not a point on the curve P-256"
)

var (
	oidECPublicKey = asn1.ObjectIdentifier{1, 2, 840, 10045, 2, 1}
	oidP256        = asn1.ObjectIdentifier{1, 2, 840, 10045, 3, 1, 7}
)

// CheckCredentialID reports whether id has a length a credential id may have,
// MinCredentialIDLen to MaxCredentialIDLen bytes.
func CheckCredentialID(id []byte) error {
	if len(id) < MinCredentialIDLen || len(id) > MaxCredentialIDLen {
		return ErrCredentialID
	}
	return nil
}

// ParseKey reads the public key of a credential in the form the browser's
// getPublicKey() gives: one DER SubjectPublicKeyInfo. It accepts an ES256 key
// only: ECDSA on P-256, a point on the curve, and the canonical encoding with
// no byte after it. Every other algorithm is ErrKeyAlgorithm (Ed25519, RSA,
// another curve, any other object), a point off the curve is ErrKeyPoint, and
// everything that is not such a structure is ErrKeyFormat.
//
// Only the canonical encoding is accepted, so one key has one byte form, and
// the fingerprint of the byte form is the fingerprint of the key.
func ParseKey(spki []byte) (*ecdsa.PublicKey, error) {
	if len(spki) == 0 || len(spki) > maxKeyLen {
		return nil, ErrKeyFormat
	}
	var info struct {
		Algorithm pkix.AlgorithmIdentifier
		PublicKey asn1.BitString
	}
	rest, err := asn1.Unmarshal(spki, &info)
	if err != nil || len(rest) != 0 {
		return nil, ErrKeyFormat
	}
	if !info.Algorithm.Algorithm.Equal(oidECPublicKey) {
		return nil, ErrKeyAlgorithm
	}
	var curve asn1.ObjectIdentifier
	if rest, err := asn1.Unmarshal(info.Algorithm.Parameters.FullBytes, &curve); err != nil || len(rest) != 0 {
		// Explicit curve parameters, or none: not a named curve, so not P-256
		// as the browser gives it.
		return nil, ErrKeyAlgorithm
	}
	if !curve.Equal(oidP256) {
		return nil, ErrKeyAlgorithm
	}
	parsed, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, ErrKeyPoint
	}
	key, ok := parsed.(*ecdsa.PublicKey)
	if !ok || key.Curve != elliptic.P256() {
		return nil, ErrKeyAlgorithm
	}
	// ECDH conversion checks the point is on the curve and not the infinity.
	if _, err := key.ECDH(); err != nil {
		return nil, ErrKeyPoint
	}
	canonical, err := x509.MarshalPKIXPublicKey(key)
	if err != nil || !bytes.Equal(canonical, spki) {
		return nil, ErrKeyFormat
	}
	return key, nil
}
