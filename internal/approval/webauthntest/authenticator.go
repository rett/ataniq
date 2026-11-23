package webauthntest

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
)

// The flags of the authenticator data (WebAuthn level 3, section 6.1).
const (
	FlagUP byte = 0x01 // user present
	FlagUV byte = 0x04 // user verified
	FlagBE byte = 0x08 // backup eligible
	FlagBS byte = 0x10 // backed up
	FlagAT byte = 0x40 // attested credential data included
	FlagED byte = 0x80 // extension data included
)

// The client data types of the two ceremonies.
const (
	TypeCreate = "webauthn.create"
	TypeGet    = "webauthn.get"
)

// CredentialIDSize is the length of the credential id New makes.
const CredentialIDSize = 32

// Authenticator is one credential of a software authenticator. Every field
// may be changed between calls; the responses are made from the fields as
// they are at the call.
type Authenticator struct {
	// Key is the credential's private key, on P-256.
	Key *ecdsa.PrivateKey
	// CredentialID is the credential id.
	CredentialID []byte
	// RPID is the relying party id whose SHA-256 opens the authenticator
	// data.
	RPID string
	// Origin is the origin the client data names.
	Origin string
	// AAGUID is the authenticator's claimed model, put into the attested
	// credential data of a registration. New leaves it all zero, as an
	// authenticator that does not say what it is.
	AAGUID [16]byte
	// Flags are the flags of every response, exactly: Create adds FlagAT,
	// and nothing else is added or removed. New sets FlagUP and FlagUV.
	Flags byte
	// SignCount is the signature counter a response reports. New leaves it
	// at 0, as a synced passkey does.
	SignCount uint32
	// CounterStep is added to SignCount before each Get reports it. With 0
	// the counter stays where it is.
	CounterStep uint32
	// Extensions, when not nil, is appended to the authenticator data as is,
	// as the extensions map. The ED flag is not set for it: set it in Flags.
	Extensions []byte
}

// New makes an authenticator with a fresh P-256 key and a random credential
// id of CredentialIDSize bytes, for the relying party id and origin given,
// with the flags FlagUP and FlagUV.
func New(rpID, origin string) (*Authenticator, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	id := make([]byte, CredentialIDSize)
	if _, err := rand.Read(id); err != nil {
		return nil, err
	}
	return &Authenticator{
		Key:          key,
		CredentialID: id,
		RPID:         rpID,
		Origin:       origin,
		Flags:        FlagUP | FlagUV,
	}, nil
}

// Registration is what navigator.credentials.create hands the page, as the
// raw bytes the page encodes for the server.
type Registration struct {
	CredentialID      []byte
	ClientDataJSON    []byte
	AuthenticatorData []byte
	// PublicKey is the DER SubjectPublicKeyInfo of the credential's key, the
	// value of AuthenticatorAttestationResponse.getPublicKey().
	PublicKey []byte
}

// Assertion is what navigator.credentials.get hands the page, as the raw
// bytes the page encodes for the server.
type Assertion struct {
	CredentialID      []byte
	ClientDataJSON    []byte
	AuthenticatorData []byte
	// Signature is the ASN.1 DER ECDSA signature over AuthenticatorData ||
	// SHA-256(ClientDataJSON).
	Signature []byte
}

// SPKI returns the DER SubjectPublicKeyInfo of the credential's public key.
func (a *Authenticator) SPKI() []byte {
	b, err := x509.MarshalPKIXPublicKey(&a.Key.PublicKey)
	if err != nil {
		// A P-256 ECDSA key always marshals.
		panic("webauthntest: " + err.Error())
	}
	return b
}

// COSEKey returns the credential's public key as the COSE_Key of the
// attested credential data, in the canonical CBOR of CTAP2:
// {1: 2, 3: -7, -1: 1, -2: x, -3: y}.
func (a *Authenticator) COSEKey() []byte {
	pub, err := a.Key.PublicKey.ECDH()
	if err != nil {
		panic("webauthntest: " + err.Error())
	}
	pt := pub.Bytes() // 0x04 || x (32) || y (32)
	b := []byte{
		0xa5,       // map of 5
		0x01, 0x02, // kty: EC2
		0x03, 0x26, // alg: ES256 (-7)
		0x20, 0x01, // crv (-1): P-256
		0x21, 0x58, 0x20, // x (-2): bytes(32)
	}
	b = append(b, pt[1:33]...)
	b = append(b, 0x22, 0x58, 0x20) // y (-3): bytes(32)
	return append(b, pt[33:65]...)
}

// ClientData returns client data JSON as a browser serialises it: type,
// challenge (base64url without padding) and origin, in that order, then
// crossOrigin false. Characters are not escaped for HTML.
func ClientData(typ string, challenge []byte, origin string) []byte {
	var buf bytes.Buffer
	buf.WriteString(`{"type":`)
	writeJSONString(&buf, typ)
	buf.WriteString(`,"challenge":`)
	writeJSONString(&buf, base64.RawURLEncoding.EncodeToString(challenge))
	buf.WriteString(`,"origin":`)
	writeJSONString(&buf, origin)
	buf.WriteString(`,"crossOrigin":false}`)
	return buf.Bytes()
}

func writeJSONString(buf *bytes.Buffer, s string) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		panic("webauthntest: " + err.Error())
	}
	buf.Truncate(buf.Len() - 1) // the newline Encode adds
}

// head returns the first 37 bytes of authenticator data: the SHA-256 of the
// relying party id, the flags and the counter.
func (a *Authenticator) head(flags byte, count uint32) []byte {
	sum := sha256.Sum256([]byte(a.RPID))
	b := append([]byte{}, sum[:]...)
	b = append(b, flags)
	return binary.BigEndian.AppendUint32(b, count)
}

// RegistrationAuthData returns the authenticator data of a registration: the
// head with Flags|FlagAT and SignCount, the attested credential data (the
// AAGUID, the credential id's length and the id, the COSE_Key), then
// Extensions when not nil.
func (a *Authenticator) RegistrationAuthData() []byte {
	b := a.head(a.Flags|FlagAT, a.SignCount)
	b = append(b, a.AAGUID[:]...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(a.CredentialID)))
	b = append(b, a.CredentialID...)
	b = append(b, a.COSEKey()...)
	return append(b, a.Extensions...)
}

// AssertionAuthData returns the authenticator data of an assertion: the head
// with Flags and SignCount, then Extensions when not nil.
func (a *Authenticator) AssertionAuthData() []byte {
	return append(a.head(a.Flags, a.SignCount), a.Extensions...)
}

// Create makes the registration for a challenge.
func (a *Authenticator) Create(challenge []byte) Registration {
	return Registration{
		CredentialID:      append([]byte{}, a.CredentialID...),
		ClientDataJSON:    ClientData(TypeCreate, challenge, a.Origin),
		AuthenticatorData: a.RegistrationAuthData(),
		PublicKey:         a.SPKI(),
	}
}

// Get makes the assertion for a challenge. It first adds CounterStep to
// SignCount.
func (a *Authenticator) Get(challenge []byte) (Assertion, error) {
	a.SignCount += a.CounterStep
	cd := ClientData(TypeGet, challenge, a.Origin)
	ad := a.AssertionAuthData()
	sig, err := a.Sign(ad, cd)
	if err != nil {
		return Assertion{}, err
	}
	return Assertion{
		CredentialID:      append([]byte{}, a.CredentialID...),
		ClientDataJSON:    cd,
		AuthenticatorData: ad,
		Signature:         sig,
	}, nil
}

// Sign signs authData || SHA-256(clientDataJSON) with the credential's key,
// as an authenticator signs an assertion, and returns the DER signature.
func (a *Authenticator) Sign(authData, clientDataJSON []byte) ([]byte, error) {
	if a.Key == nil {
		return nil, errors.New("webauthntest: no key")
	}
	return ecdsa.SignASN1(rand.Reader, a.Key, SignedDigest(authData, clientDataJSON))
}

// SignedDigest returns SHA-256(authData || SHA-256(clientDataJSON)), the
// digest an assertion's signature is over.
func SignedDigest(authData, clientDataJSON []byte) []byte {
	cdHash := sha256.Sum256(clientDataJSON)
	h := sha256.New()
	h.Write(authData)
	h.Write(cdHash[:])
	return h.Sum(nil)
}
