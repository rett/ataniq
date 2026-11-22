package seatgate

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"time"
)

// FingerprintLen is the length of a fingerprint: 64 lower-case hex digits.
const FingerprintLen = 64

// identityYears is how long the certificate of a new identity is valid. The
// validity dates are never checked by a peer (the key is pinned), so this
// only has to be long.
const identityYears = 10

// Identity is one end's certificate and private key. The certificate is
// self-signed and exists only to carry the key: a peer pins the key by its
// fingerprint and ignores every other field.
type Identity struct {
	cert tls.Certificate
	fp   string
}

// Fingerprint is the lower-case hex SHA-256 of the DER SubjectPublicKeyInfo of
// the certificate: 64 characters. It is what the owner carries from one end to
// the other at pairing time.
func (id Identity) Fingerprint() string { return id.fp }

// ValidFingerprint reports whether s is exactly 64 lower-case hex digits.
func ValidFingerprint(s string) bool {
	if len(s) != FingerprintLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// spkiFingerprint is the fingerprint of a parsed certificate.
func spkiFingerprint(c *x509.Certificate) string {
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}

// validName reports whether name can be the common name of a certificate: 1 to
// 64 characters of letters, digits, period, hyphen and underscore.
func validName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// GenerateIdentity makes a key (ECDSA P-256) and a self-signed certificate for
// it: the common name is name, the validity is 10 years from now, and the key
// usage is digital signature with both server and client authentication, so
// the one identity serves either end. It returns both as PEM: the certificate
// as a CERTIFICATE block and the key as a PKCS#8 PRIVATE KEY block.
//
// rand should be crypto/rand.Reader. Recent Go releases may ignore it and use
// the system source; a caller must not rely on a fixed reader giving a fixed
// key.
func GenerateIdentity(rand io.Reader, now time.Time, name string) (certPEM, keyPEM []byte, err error) {
	if !validName(name) {
		return nil, nil, errors.New("seatgate: the identity name is not 1 to 64 letters, digits, period, hyphen or underscore")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand)
	if err != nil {
		return nil, nil, errors.New("seatgate: cannot make a key")
	}
	serial, err := io.ReadAll(io.LimitReader(rand, 16))
	if err != nil || len(serial) != 16 {
		return nil, nil, errors.New("seatgate: cannot make a serial number")
	}
	tmpl := &x509.Certificate{
		SerialNumber:          new(big.Int).SetBytes(serial),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             now.UTC(),
		NotAfter:              now.UTC().AddDate(identityYears, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, errors.New("seatgate: cannot make a certificate")
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, errors.New("seatgate: cannot encode the key")
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM, nil
}

// ParseIdentity reads a certificate and key made by [GenerateIdentity] (or any
// of the same shape): one certificate and no chain, an ECDSA P-256 key that
// belongs to it, digital signature and both server and client authentication
// in its usage. It refuses anything else.
func ParseIdentity(certPEM, keyPEM []byte) (Identity, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return Identity{}, errors.New("seatgate: the certificate and key are not a matching pair")
	}
	if len(pair.Certificate) != 1 {
		return Identity{}, errors.New("seatgate: the certificate file must hold exactly one certificate")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return Identity{}, errors.New("seatgate: the certificate cannot be read")
	}
	if err := checkLeaf(leaf, x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth); err != nil {
		return Identity{}, err
	}
	pair.Leaf = leaf
	return Identity{cert: pair, fp: spkiFingerprint(leaf)}, nil
}

// checkLeaf holds a certificate to the shape of this package: an ECDSA P-256
// key, the digital signature usage and every extended usage in need.
func checkLeaf(c *x509.Certificate, need ...x509.ExtKeyUsage) error {
	pub, ok := c.PublicKey.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return errors.New("seatgate: the key is not ECDSA P-256")
	}
	if c.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return errors.New("seatgate: the certificate does not allow digital signature")
	}
	for _, n := range need {
		found := false
		for _, have := range c.ExtKeyUsage {
			if have == n {
				found = true
			}
		}
		if !found {
			return errors.New("seatgate: the certificate lacks a needed extended key usage")
		}
	}
	return nil
}

func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }
