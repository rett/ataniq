package seatapp

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"testing"
)

// authenticator is a software authenticator: an ES256 key and a credential id
// that builds the registration a browser would send, the client data and the
// authenticator data exactly as a browser makes them.
type authenticator struct {
	key    *ecdsa.PrivateKey
	credID []byte
}

func newAuthenticator(t *testing.T) *authenticator {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	rand.Read(id)
	return &authenticator{key: k, credID: id}
}

// registration is one pairing body before it is encoded, every part open to
// change by a test.
type registration struct {
	Code      string
	Challenge string // the challenge the server issued, as posted
	// ClientData is the client data JSON bytes; built from the fields below
	// when nil.
	ClientData  []byte
	Type        string
	CDChallenge string // the challenge named in the client data
	Origin      string
	CrossOrigin any // omitted when nil
	Extra       map[string]any
	// AuthData is the authenticator data; built from the fields below when
	// nil.
	AuthData     []byte
	RPID         string
	Flags        byte
	AAGUID       [16]byte
	SignCount    uint32
	AuthCredID   []byte
	COSE         []byte
	SPKI         []byte
	PostedCredID []byte
	// Raw base64url strings override the encoding of a field when set.
	Raw map[string]string
}

const (
	uvUPAT = flagUP | flagUV | flagAT
)

// cose is the canonical CTAP2 encoding of a P-256 COSE_Key.
func (a *authenticator) cose() []byte {
	pt, _ := a.key.PublicKey.ECDH()
	raw := pt.Bytes()
	b := []byte{0xa5, 0x01, 0x02, 0x03, 0x26, 0x20, 0x01, 0x21, 0x58, 0x20}
	b = append(b, raw[1:33]...)
	b = append(b, 0x22, 0x58, 0x20)
	return append(b, raw[33:65]...)
}

// register makes the registration for a challenge; mod changes it before it is
// built.
func (a *authenticator) register(t *testing.T, challenge string, mod ...func(*registration)) map[string]string {
	t.Helper()
	spki, err := x509.MarshalPKIXPublicKey(&a.key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	r := &registration{
		Code: "K7M2-Q9XA", Challenge: challenge,
		Type: "webauthn.create", CDChallenge: challenge, Origin: testOrigin,
		RPID: testRPID, Flags: uvUPAT, AuthCredID: a.credID, COSE: a.cose(),
		SPKI: spki, PostedCredID: a.credID,
	}
	for _, m := range mod {
		m(r)
	}
	cd := r.ClientData
	if cd == nil {
		m := map[string]any{"type": r.Type, "challenge": r.CDChallenge, "origin": r.Origin}
		if r.CrossOrigin != nil {
			m["crossOrigin"] = r.CrossOrigin
		}
		for k, v := range r.Extra {
			m[k] = v
		}
		cd, _ = json.Marshal(m)
	}
	ad := r.AuthData
	if ad == nil {
		sum := sha256.Sum256([]byte(r.RPID))
		ad = append(ad, sum[:]...)
		ad = append(ad, r.Flags)
		ad = binary.BigEndian.AppendUint32(ad, r.SignCount)
		ad = append(ad, r.AAGUID[:]...)
		ad = binary.BigEndian.AppendUint16(ad, uint16(len(r.AuthCredID)))
		ad = append(ad, r.AuthCredID...)
		ad = append(ad, r.COSE...)
	}
	enc := base64.RawURLEncoding.EncodeToString
	out := map[string]string{
		"code":               r.Code,
		"challenge":          r.Challenge,
		"credential_id":      enc(r.PostedCredID),
		"client_data_json":   enc(cd),
		"authenticator_data": enc(ad),
		"public_key":         enc(r.SPKI),
	}
	for k, v := range r.Raw {
		out[k] = v
	}
	return out
}

func spkiOf(t *testing.T, pub any) []byte {
	t.Helper()
	b, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// otherKeys are public keys that are not P-256.
func otherKeys(t *testing.T) map[string][]byte {
	t.Helper()
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	rsaKey, _ := rsa.GenerateKey(rand.Reader, 2048)
	edPub, _, _ := ed25519.GenerateKey(rand.Reader)
	return map[string][]byte{
		"P-384":   spkiOf(t, &p384.PublicKey),
		"RSA":     spkiOf(t, &rsaKey.PublicKey),
		"Ed25519": spkiOf(t, edPub),
	}
}
