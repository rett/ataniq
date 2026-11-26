package wire

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"slices"

	"ataniq/internal/assignment"
)

type signer struct {
	header assignment.Header
	key    ed25519.PrivateKey
}

type verifier struct {
	header assignment.Header
	key    ed25519.PublicKey
}

var errEnrollment = errors.New("assignment wire: projection does not match enrollment")

func checkPin(h assignment.Header, pub ed25519.PublicKey) error {
	if _, err := h.Bytes(); err != nil {
		return err
	}
	if !validPublic(pub) {
		return errKey
	}
	if h.KeyID != keyID(pub) {
		return errEnrollment
	}
	return nil
}

// NewSigner binds the ledger signer to an enrolled header and a private copy
// of the matching key. The ledger passes Event.SigningBytes to its Sign method.
func NewSigner(h assignment.Header, priv ed25519.PrivateKey) (assignment.Signer, error) {
	if !validPrivate(priv) {
		return nil, errKey
	}
	if err := checkPin(h, priv.Public().(ed25519.PublicKey)); err != nil {
		return nil, err
	}
	return signer{h, slices.Clone(priv)}, nil
}

// NewVerifier binds replay to an independently enrolled header and public key.
func NewVerifier(h assignment.Header, pub ed25519.PublicKey) (assignment.Verifier, error) {
	if err := checkPin(h, pub); err != nil {
		return nil, err
	}
	return verifier{h, slices.Clone(pub)}, nil
}

func enrolledBody(pin, h assignment.Header, message []byte) ([]byte, error) {
	body, ok := bytes.CutPrefix(message, []byte(domain))
	if !ok || h != pin {
		return nil, errEnrollment
	}
	p, err := unmarshalCanonical(body)
	if err != nil {
		return nil, err
	}
	home := assignment.Instance{Authority: h.Authority, Incarnation: h.Incarnation, Store: h.Store}
	if p.Home != home || p.KeyID != h.KeyID || p.Signature != "" {
		return nil, errEnrollment
	}
	return body, nil
}

func (s signer) Sign(h assignment.Header, message []byte) (string, error) {
	body, err := enrolledBody(s.header, h, message)
	if err != nil {
		return "", err
	}
	blob, err := signBlob(s.key, body)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(blob), nil
}

func (v verifier) Verify(h assignment.Header, message []byte, sig string) error {
	body, err := enrolledBody(v.header, h, message)
	if err != nil {
		return err
	}
	blob, err := decodeSignature(sig)
	if err != nil {
		return err
	}
	return verifyBlob(v.key, body, blob)
}

// Strict decoding still ignores embedded CR and LF. Round-trip equality
// gives every authenticated envelope a single signed-chain digest.
func decodeSignature(sig string) ([]byte, error) {
	if len(sig) > recordLimit {
		return nil, assignment.ErrLimit
	}
	blob, err := base64.RawURLEncoding.Strict().DecodeString(sig)
	if err != nil || len(blob) == 0 || base64.RawURLEncoding.EncodeToString(blob) != sig {
		return nil, errSignature
	}
	return blob, nil
}
