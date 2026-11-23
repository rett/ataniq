package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"time"
)

// MaxTokenLife is the longest a VAPID JWT may be valid: its exp is at most
// this long after the clock that signs it. RFC 8292 allows 24 hours; this
// package allows half of that.
const MaxTokenLife = 12 * time.Hour

// maxSubject bounds the sub claim.
const maxSubject = 256

// pemType is the type of the PEM block a VAPID key is kept in.
const pemType = "PRIVATE KEY"

// b64 is the base64url encoding without padding that JWTs and Web Push keys
// use.
var b64 = base64.RawURLEncoding

// Errors of the VAPID key and token. Each is a fixed text.
var (
	// ErrKey is returned when a VAPID key is not a PKCS#8 ECDSA P-256 key in
	// one PEM block.
	ErrKey = errors.New("webpush: not a VAPID key (a PKCS#8 ECDSA P-256 key in one PEM block)")
	// ErrToken is returned when a JWT cannot be made from the given claims.
	ErrToken = errors.New("webpush: the VAPID token's claims are not valid")
)

// VAPIDKey is an application server's ES256 key pair (RFC 8292). A browser
// subscribes with its public half as applicationServerKey; a push service
// then accepts messages to that subscription only when they are signed with
// the private half.
type VAPIDKey struct {
	priv *ecdsa.PrivateKey
	pub  []byte // the uncompressed point, 65 bytes
}

// GenerateVAPIDKey makes a new key from the system's secure random source.
// (Since Go 1.26 the ECDSA key generation ignores a caller's reader, so the
// function takes none.)
func GenerateVAPIDKey() (*VAPIDKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return newVAPIDKey(priv)
}

func newVAPIDKey(priv *ecdsa.PrivateKey) (*VAPIDKey, error) {
	if priv.Curve != elliptic.P256() {
		return nil, ErrKey
	}
	pub, err := priv.PublicKey.Bytes()
	if err != nil || len(pub) != 65 {
		return nil, ErrKey
	}
	return &VAPIDKey{priv: priv, pub: pub}, nil
}

// ParseVAPIDKey reads a key from exactly one PEM block of type PRIVATE KEY
// holding a PKCS#8 ECDSA P-256 key, with nothing but white space around it.
func ParseVAPIDKey(data []byte) (*VAPIDKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || block.Type != pemType || len(block.Headers) != 0 || strings.TrimSpace(string(rest)) != "" {
		return nil, ErrKey
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, ErrKey
	}
	priv, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, ErrKey
	}
	return newVAPIDKey(priv)
}

// MarshalPEM returns the key as a PKCS#8 PRIVATE KEY PEM block.
func (k *VAPIDKey) MarshalPEM() ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(k.priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: pemType, Bytes: der}), nil
}

// PublicKey returns the public key as an uncompressed P-256 point (65
// bytes), a copy.
func (k *VAPIDKey) PublicKey() []byte {
	return append([]byte(nil), k.pub...)
}

// PublicKeyString returns the public key in base64url without padding: the
// k parameter of the Authorization header and the applicationServerKey a
// browser subscribes with.
func (k *VAPIDKey) PublicKeyString() string {
	return b64.EncodeToString(k.pub)
}

// jwtHeader is the fixed header of every token, already encoded.
var jwtHeader = b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))

// claims are the three claims of a VAPID token, in this order.
type claims struct {
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Sub string `json:"sub"`
}

// Token returns an ES256 JWT with the claims aud (audience, the origin of a
// push service, "https://host" or "https://host:port"), exp (now plus life,
// in whole seconds) and sub (subject, a mailto: or https: URI). life must be
// more than zero and at most [MaxTokenLife]; anything else is [ErrToken].
func (k *VAPIDKey) Token(audience, subject string, now time.Time, life time.Duration) (string, error) {
	if life <= 0 || life > MaxTokenLife || !validAudience(audience) || !validSubject(subject) {
		return "", ErrToken
	}
	body, err := json.Marshal(claims{Aud: audience, Exp: now.Add(life).Unix(), Sub: subject})
	if err != nil {
		return "", ErrToken
	}
	input := jwtHeader + "." + b64.EncodeToString(body)
	digest := sha256.Sum256([]byte(input))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, digest[:])
	if err != nil {
		return "", err
	}
	// JWS (RFC 7518 3.4): the signature is R and S, each 32 bytes big-endian.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return input + "." + b64.EncodeToString(sig), nil
}

// AuthorizationHeader returns the value of the Authorization header of
// RFC 8292: "vapid t=<token>, k=<public key>", with a token made by
// [VAPIDKey.Token] from the same arguments.
func (k *VAPIDKey) AuthorizationHeader(audience, subject string, now time.Time, life time.Duration) (string, error) {
	t, err := k.Token(audience, subject, now, life)
	if err != nil {
		return "", err
	}
	return "vapid t=" + t + ", k=" + k.PublicKeyString(), nil
}

// validAudience accepts an https origin: the scheme and a host with an
// optional port, nothing else.
func validAudience(aud string) bool {
	host, ok := strings.CutPrefix(aud, "https://")
	return ok && host != "" && len(aud) <= 300 && !strings.ContainsAny(host, "/?#@ \t\r\n\"\\")
}

// validSubject accepts a mailto: or https: URI of printable ASCII with no
// space, at most 256 bytes.
func validSubject(sub string) bool {
	if len(sub) > maxSubject {
		return false
	}
	rest, ok := strings.CutPrefix(sub, "mailto:")
	if !ok {
		rest, ok = strings.CutPrefix(sub, "https://")
	}
	if !ok || rest == "" {
		return false
	}
	for i := 0; i < len(sub); i++ {
		if sub[i] <= ' ' || sub[i] >= 0x7f || sub[i] == '"' || sub[i] == '\\' {
			return false
		}
	}
	return true
}
