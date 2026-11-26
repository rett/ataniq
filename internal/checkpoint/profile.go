package checkpoint

import (
	"crypto/ed25519"
	"encoding/base64"

	"ataniq/internal/assignment/wire"
)

// Fixed SSHSIG profiles. Both the namespace in the container and the domain
// prefix in the hashed message are part of the protocol.
const (
	HomeNamespace    = "ataniq.checkpoint.home.v1"
	SessionNamespace = "ataniq.checkpoint.session.v1"
	WitnessNamespace = "ataniq.checkpoint.witness.v1"
)

type profile string

const (
	homeProfile    profile = HomeNamespace
	sessionProfile profile = SessionNamespace
	witnessProfile profile = WitnessNamespace
)

// KeyID validates a raw Ed25519 key with the assignment wire pin parser and
// returns the OpenSSH SHA256 fingerprint used in the immutable ledger header.
func KeyID(pub ed25519.PublicKey) (string, error) {
	if len(pub) != ed25519.PublicKeySize {
		return "", &Reason{Kind: InvalidRequest}
	}
	_, id, err := wire.LoadPublicKey("ssh-ed25519 " + base64.StdEncoding.EncodeToString(keyBlob(pub)))
	if err != nil {
		return "", &Reason{Kind: InvalidRequest}
	}
	return id, nil
}

// Verify uses a pin validated at provisioning or by KeyID.
func (p profile) verify(pub ed25519.PublicKey, body, sig []byte) bool {
	if len(sig) > 512 {
		return false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(string(sig))
	return err == nil && base64.RawURLEncoding.EncodeToString(raw) == string(sig) && p.verifyBlob(pub, body, raw) == nil
}

func (p profile) sign(key ed25519.PrivateKey, body []byte) ([]byte, error) {
	raw, err := p.signBlob(key, body)
	if err != nil {
		return nil, &Reason{Kind: InvalidRequest}
	}
	return []byte(base64.RawURLEncoding.EncodeToString(raw)), nil
}

// Sign produces the protocol signature for one of the three fixed namespaces.
func Sign(namespace string, key ed25519.PrivateKey, body []byte) ([]byte, error) {
	switch namespace {
	case HomeNamespace, SessionNamespace, WitnessNamespace:
		return profile(namespace).sign(key, body)
	}
	return nil, &Reason{Kind: InvalidRequest}
}

// VerifySignature authenticates a profile signature against an independent pin.
func VerifySignature(namespace string, pin ed25519.PublicKey, body, sig []byte) error {
	if _, err := KeyID(pin); err != nil {
		return err
	}
	switch namespace {
	case HomeNamespace, SessionNamespace, WitnessNamespace:
		if profile(namespace).verify(pin, body, sig) {
			return nil
		}
	}
	return &Reason{Kind: InvalidRequest}
}
