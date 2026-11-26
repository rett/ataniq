package wire

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"

	"ataniq/internal/assignment"
)

const (
	keyType    = "ssh-ed25519"
	magic      = "SSHSIG"
	hashName   = "sha512"
	beginArmor = "-----BEGIN SSH SIGNATURE-----"
	endArmor   = "-----END SSH SIGNATURE-----"
)

var (
	errKey       = errors.New("assignment wire: invalid Ed25519 key")
	errSignature = errors.New("assignment wire: invalid SSH signature")
)

func sshString(b, data []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(data)))
	return append(b, data...)
}

func keyBlob(pub ed25519.PublicKey) []byte {
	return sshString(sshString(nil, []byte(keyType)), pub)
}

// For the pinned PROTOCOL.sshsig profile, all fields up to the signature
// octets have one exact binary representation, including nested lengths.
func signaturePrefix(pub ed25519.PublicKey) []byte {
	b := binary.BigEndian.AppendUint32([]byte(magic), 1)
	b = sshString(b, keyBlob(pub))
	b = sshString(b, []byte(Namespace))
	b = sshString(b, nil) // reserved
	b = sshString(b, []byte(hashName))
	b = binary.BigEndian.AppendUint32(b, uint32(4+len(keyType)+4+ed25519.SignatureSize))
	b = sshString(b, []byte(keyType))
	return binary.BigEndian.AppendUint32(b, ed25519.SignatureSize)
}

func signedData(body []byte) []byte {
	h := sha512.New()
	h.Write([]byte(domain))
	h.Write(body)
	b := sshString([]byte(magic), []byte(Namespace))
	b = sshString(b, nil)
	b = sshString(b, []byte(hashName))
	return sshString(b, h.Sum(nil))
}

func validPrivate(priv ed25519.PrivateKey) bool {
	return len(priv) == ed25519.PrivateKeySize && priv.Equal(ed25519.NewKeyFromSeed(priv.Seed()))
}

// Sign signs the domain prefix, NUL and the exact supplied body. Pass the
// output of Marshal with an empty Signature. The returned armor ends in LF.
func Sign(priv ed25519.PrivateKey, body []byte) (string, error) {
	blob, err := signBlob(priv, body)
	if err != nil {
		return "", err
	}
	return armor(blob), nil
}

func signBlob(priv ed25519.PrivateKey, body []byte) ([]byte, error) {
	if len(body) > recordLimit {
		return nil, assignment.ErrLimit
	}
	if !validPrivate(priv) {
		return nil, errKey
	}
	return append(signaturePrefix(priv.Public().(ed25519.PublicKey)), ed25519.Sign(priv, signedData(body))...), nil
}

func armor(blob []byte) string {
	data := base64.StdEncoding.EncodeToString(blob)
	var b strings.Builder
	b.WriteString(beginArmor + "\n")
	for len(data) > 70 {
		b.WriteString(data[:70])
		b.WriteByte('\n')
		data = data[70:]
	}
	b.WriteString(data)
	b.WriteString("\n" + endArmor + "\n")
	return b.String()
}

func unarmor(sig string) ([]byte, error) {
	if len(sig) > 4096 {
		return nil, errSignature
	}
	sig = strings.ReplaceAll(sig, "\r\n", "\n")
	data, ok := strings.CutPrefix(sig, beginArmor+"\n")
	if !ok {
		return nil, errSignature
	}
	data, ok = strings.CutSuffix(strings.TrimSuffix(data, "\n"), "\n"+endArmor)
	if !ok || strings.Contains(data, "\r") || strings.HasPrefix(data, "\n") ||
		strings.HasSuffix(data, "\n") || strings.Contains(data, "\n\n") {
		return nil, errSignature
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(data)
	if err != nil {
		return nil, errSignature
	}
	return raw, nil
}

// Verify authenticates the exact body bytes against an independently pinned
// public key and the assignment namespace. It does not enroll a key from sig.
func Verify(pub ed25519.PublicKey, body []byte, sig string) error {
	blob, err := unarmor(sig)
	if err != nil {
		return err
	}
	if !validPublic(pub) {
		return errKey
	}
	return verifyBlob(pub, body, blob)
}

// pub has already passed validation at enrollment or at entry to Verify.
func verifyBlob(pub ed25519.PublicKey, body, blob []byte) error {
	if len(body) > recordLimit {
		return assignment.ErrLimit
	}
	raw, ok := bytes.CutPrefix(blob, signaturePrefix(pub))
	if !ok || len(raw) != ed25519.SignatureSize || !ed25519.Verify(pub, signedData(body), raw) {
		return errSignature
	}
	return nil
}

func keyID(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(keyBlob(pub))
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// LoadPublicKey reads one preselected "principals ssh-ed25519 base64" pin
// or a bare OpenSSH public key, with an optional trailing comment. Selection
// of the trusted line is the caller's enrollment decision. Allowed-signers
// options are refused because their policies would require interpretation.
// The ID is the OpenSSH SHA256 fingerprint of the SSH public key blob.
func LoadPublicKey(line string) (ed25519.PublicKey, string, error) {
	if len(line) > recordLimit {
		return nil, "", errKey
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if strings.ContainsAny(line, "\r\n\x00") {
		return nil, "", errKey
	}
	fields := strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == '\t' })
	if len(fields) < 2 || strings.HasPrefix(fields[0], "#") {
		return nil, "", errKey
	}
	if len(fields) >= 3 && fields[1] == keyType {
		fields = fields[1:]
	}
	if len(fields) < 2 || fields[0] != keyType {
		return nil, "", errKey
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(fields[1])
	if err != nil {
		return nil, "", errKey
	}
	header := binary.BigEndian.AppendUint32(sshString(nil, []byte(keyType)), ed25519.PublicKeySize)
	key, ok := bytes.CutPrefix(blob, header)
	if !ok || !validPublic(key) {
		return nil, "", errKey
	}
	pub := ed25519.PublicKey(key)
	return pub, keyID(pub), nil
}
