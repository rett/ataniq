package credkind

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/binary"
	"errors"
)

const (
	skAlgorithm   = "sk-ssh-ed25519@openssh.com"
	skApplication = "ssh:ataniq-console"
	skNamespace   = "ataniq-console-decision-v1"
	skHash        = "sha512"
	skMagic       = "SSHSIG"
	skRoute       = "console-decision"
)

var (
	ErrSKCounter    = errors.New("credkind: security-key counter did not advance")
	ErrSKProvenance = errors.New("credkind: security-key enrollment requires witnessed origin")
)

// SKProvenance records the independent evidence for a dedicated enrolled key.
// Only the trusted enrollment supplies this value. A signature cannot prove it.
type SKProvenance string

// SKWitnessed records the physical enrollment ceremony. Attested origin
// requires an attestation verifier before it can be admitted.
const SKWitnessed SKProvenance = "witnessed"

// ScratchSKCredential is the verifier's trusted record for a credential.
// Enrollment must call ValidateScratchSKKey, and Counter is the last value
// durably spent for this credential.
type ScratchSKCredential struct {
	ID         string
	PublicKey  ed25519.PublicKey
	Provenance SKProvenance
	Counter    uint32
}

// ScratchSKScope is the home's authorization context. AllowFeatureFinish is
// an explicit project opt-in, DeskClass comes from the authenticated desk's
// enrollment, and the remaining fields come from the pending card and trial.
// These values must be obtained independently of the submitted signature.
type ScratchSKScope struct {
	AllowFeatureFinish bool
	DeskClass          string
	DisposableLocal    bool
	CardClass          string
	Verb               string
}

// ScratchSKDecision describes one expected console decision at the home.
// The desk reconstructs it from its own session and the card it displays.
// ExporterSHA256 is the hash of the desk connection's contextual TLS exporter.
// GateFingerprint and FrameSHA256 are raw SHA-256 digests.
type ScratchSKDecision struct {
	HomeID           string
	GateFingerprint  [32]byte
	PrincipalID      string
	DeviceID         string
	CredentialID     string
	SessionID        string
	ExporterSHA256   [32]byte
	PendingID        string
	Decision         string
	ExpectedRevision uint64
	PolicyRevision   uint64
	ExpiresUnix      int64 // seconds
	FrameSHA256      [32]byte
	Nonce            [32]byte
}

// SigningBytes encodes the fixed credential kind and route followed by the
// fields in declaration order. Each field has a four-byte big-endian length.
// Revisions and expiry each occupy eight big-endian bytes.
func (d ScratchSKDecision) SigningBytes() ([]byte, error) {
	for _, id := range []string{d.HomeID, d.PrincipalID, d.DeviceID, d.CredentialID, d.SessionID, d.PendingID} {
		if len(id) == 0 || len(id) > 1024 {
			return nil, ErrExpect
		}
	}
	if (d.Decision != "approve" && d.Decision != "deny") || d.ExpiresUnix <= 0 ||
		d.GateFingerprint == ([32]byte{}) || d.ExporterSHA256 == ([32]byte{}) ||
		d.FrameSHA256 == ([32]byte{}) || d.Nonce == ([32]byte{}) {
		return nil, ErrExpect
	}
	b := skFields(nil, []byte(NameSecurityKey), []byte(skRoute), []byte(d.HomeID), d.GateFingerprint[:],
		[]byte(d.PrincipalID), []byte(d.DeviceID), []byte(d.CredentialID), []byte(d.SessionID),
		d.ExporterSHA256[:], []byte(d.PendingID), []byte(d.Decision))
	for _, n := range []uint64{d.ExpectedRevision, d.PolicyRevision, uint64(d.ExpiresUnix)} {
		b = skFields(b, binary.BigEndian.AppendUint64(nil, n))
	}
	return skFields(b, d.FrameSHA256[:], d.Nonce[:]), nil
}

func skFields(b []byte, fields ...[]byte) []byte {
	for _, field := range fields {
		b = binary.BigEndian.AppendUint32(b, uint32(len(field)))
		b = append(b, field...)
	}
	return b
}

// PROTOCOL.sshsig v1 with PROTOCOL.u2f's Ed25519 key and signature. For the
// independently pinned public key the entire prefix, including both nested
// blob lengths, has one representation. The suffix is signature || flags || counter.
func skPrefix(pub ed25519.PublicKey) []byte {
	keyBlob := skFields(nil, []byte(skAlgorithm), pub, []byte(skApplication))
	b := binary.BigEndian.AppendUint32([]byte(skMagic), 1)
	b = skFields(b, keyBlob, []byte(skNamespace), nil, []byte(skHash))
	const signatureBlobLen = 4 + len(skAlgorithm) + 4 + ed25519.SignatureSize + 1 + 4
	b = binary.BigEndian.AppendUint32(b, uint32(signatureBlobLen))
	b = skFields(b, []byte(skAlgorithm))
	return binary.BigEndian.AppendUint32(b, ed25519.SignatureSize)
}

// VerifyScratchSK checks a binary SSHSIG against the home's expectation and
// returns the signed counter for the challenge spend. The caller must recheck
// eligibility, the current counter, expiry and challenge state at spend time
// and retain the counter atomically with the spend. No decision is issued here.
func VerifyScratchSK(c ScratchSKCredential, expected ScratchSKDecision, scope ScratchSKScope, blob []byte) (uint32, error) {
	if !scope.AllowFeatureFinish || scope.DeskClass != "laptop" || !scope.DisposableLocal ||
		scope.CardClass != "ref-move" || scope.Verb != "feature finish" {
		return 0, ErrNotAccepted
	}
	if c.Provenance != SKWitnessed {
		return 0, ErrSKProvenance
	}
	if err := ValidateScratchSKKey(c.PublicKey); err != nil {
		return 0, err
	}
	if c.ID != expected.CredentialID {
		return 0, ErrCredential
	}
	raw, ok := bytes.CutPrefix(blob, skPrefix(c.PublicKey))
	if !ok || len(raw) != ed25519.SignatureSize+1+4 {
		return 0, ErrSignature
	}
	msg, err := expected.SigningBytes()
	if err != nil {
		return 0, err
	}
	messageHash := sha512.Sum512(msg)
	signedData := skFields([]byte(skMagic), []byte(skNamespace), nil, []byte(skHash), messageHash[:])
	dataHash := sha256.Sum256(signedData)
	appHash := sha256.Sum256([]byte(skApplication))
	trailer := raw[ed25519.SignatureSize:]
	payload := append(appHash[:], trailer...)
	payload = append(payload, dataHash[:]...)
	if !ed25519.Verify(c.PublicKey, payload, raw[:ed25519.SignatureSize]) {
		return 0, ErrSignature
	}
	if trailer[0]&flagUP == 0 {
		return 0, ErrUserPresence
	}
	if trailer[0]&flagUV == 0 {
		return 0, ErrUserVerification
	}
	counter := binary.BigEndian.Uint32(trailer[1:])
	if counter <= c.Counter {
		return 0, ErrSKCounter
	}
	return counter, nil
}
