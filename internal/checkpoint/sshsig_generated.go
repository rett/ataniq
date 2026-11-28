// Code generated from internal/assignment/wire/sshsig.go by TestProfileMatchesAssignmentWire. DO NOT EDIT.

package checkpoint

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/binary"
)

const keyType = "ssh-ed25519"
const magic = "SSHSIG"
const hashName = "sha512"

func sshString(b, data []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(data)))
	return append(b, data...)
}

func keyBlob(pub ed25519.PublicKey) []byte {
	return sshString(sshString(nil, []byte(keyType)), pub)
}

func validPrivate(priv ed25519.PrivateKey) bool {
	return len(priv) == ed25519.PrivateKeySize && priv.Equal(ed25519.NewKeyFromSeed(priv.Seed()))
}

func (p profile) signaturePrefix(pub ed25519.PublicKey) []byte {
	b := binary.BigEndian.AppendUint32([]byte(magic), 1)
	b = sshString(b, keyBlob(pub))
	b = sshString(b, []byte(p))
	b = sshString(b, nil) // reserved
	b = sshString(b, []byte(hashName))
	b = binary.BigEndian.AppendUint32(b, uint32(4+len(keyType)+4+ed25519.SignatureSize))
	b = sshString(b, []byte(keyType))
	return binary.BigEndian.AppendUint32(b, ed25519.SignatureSize)
}

func (p profile) signedData(body []byte) []byte {
	h := sha512.New()
	h.Write([]byte(string(p) + "\x00"))
	h.Write(body)
	b := sshString([]byte(magic), []byte(p))
	b = sshString(b, nil)
	b = sshString(b, []byte(hashName))
	return sshString(b, h.Sum(nil))
}

func (p profile) signBlob(priv ed25519.PrivateKey, body []byte) ([]byte, error) {
	if len(body) > maxFrame {
		return nil, ErrRefused
	}
	if !validPrivate(priv) {
		return nil, ErrRefused
	}
	return append(p.signaturePrefix(priv.Public().(ed25519.PublicKey)), ed25519.Sign(priv, p.signedData(body))...), nil
}

func (p profile) verifyBlob(pub ed25519.PublicKey, body, blob []byte) error {
	if len(body) > maxFrame {
		return ErrRefused
	}
	raw, ok := bytes.CutPrefix(blob, p.signaturePrefix(pub))
	if !ok || len(raw) != ed25519.SignatureSize || !ed25519.Verify(pub, p.signedData(body), raw) {
		return ErrRefused
	}
	return nil
}
