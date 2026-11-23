package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"strings"
)

const (
	// RecordSize is the record size of the aes128gcm content coding this
	// package writes, in its header: the 4096 bytes RFC 8291 requires a push
	// service to accept.
	RecordSize = 4096
	// MaxPayload is the largest payload [Encrypt] takes: the record size less
	// the header (86 bytes), the tag (16) and the delimiter (1), so that the
	// whole body is one record of at most 4096 bytes.
	MaxPayload = RecordSize - headerLen - tagLen - 1

	saltLen   = 16
	authLen   = 16
	pointLen  = 65 // an uncompressed P-256 point
	scalarLen = 32
	tagLen    = 16
	headerLen = saltLen + 4 + 1 + pointLen

	// keyTries bounds how often a scalar is read for the ephemeral key: a
	// random 32 bytes is not a valid P-256 scalar with a chance of about
	// 2^-32, so a reader that fails this often is broken.
	keyTries = 8
)

// Errors of the encryption. Each is a fixed text.
var (
	// ErrPayloadTooLarge is returned for a payload over [MaxPayload] bytes.
	ErrPayloadTooLarge = errors.New("webpush: the payload is over 3993 bytes")
	// ErrSubscriptionKeys is returned when a subscription's p256dh is not an
	// uncompressed P-256 point or its auth secret is not 16 bytes.
	ErrSubscriptionKeys = errors.New("webpush: the subscription's p256dh or auth is not valid")
	// ErrRandom is returned when the random reader fails.
	ErrRandom = errors.New("webpush: the random source failed")
)

// Encrypt encrypts payload for a subscription whose public key is p256dh (an
// uncompressed P-256 point, 65 bytes) and whose authentication secret is
// auth (16 bytes), as RFC 8291 says: one aes128gcm record of RFC 8188 with
// record size [RecordSize] and no padding. It reads from random first the
// 16-byte salt and then the 32-byte private key of the ephemeral key pair
// (read again, at most 8 times in all, while it is not a valid P-256 scalar);
// pass crypto/rand.Reader.
func Encrypt(random io.Reader, p256dh, auth, payload []byte) ([]byte, error) {
	if len(payload) > MaxPayload {
		return nil, ErrPayloadTooLarge
	}
	if len(p256dh) != pointLen || len(auth) != authLen {
		return nil, ErrSubscriptionKeys
	}
	uaPublic, err := ecdh.P256().NewPublicKey(p256dh)
	if err != nil {
		return nil, ErrSubscriptionKeys
	}
	salt := make([]byte, saltLen)
	if _, err := io.ReadFull(random, salt); err != nil {
		return nil, ErrRandom
	}
	asPrivate, err := ephemeralKey(random)
	if err != nil {
		return nil, err
	}
	asPublic := asPrivate.PublicKey().Bytes()
	secret, err := asPrivate.ECDH(uaPublic)
	if err != nil {
		return nil, ErrSubscriptionKeys
	}

	// RFC 8291 section 3.4: combine the shared secret with the auth secret,
	// then derive the content encryption key and nonce of RFC 8188.
	prkKey, err := hkdf.Extract(sha256.New, secret, auth)
	if err != nil {
		return nil, err
	}
	var info strings.Builder
	info.WriteString("WebPush: info\x00")
	info.Write(p256dh)
	info.Write(asPublic)
	ikm, err := hkdf.Expand(sha256.New, prkKey, info.String(), 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	out := make([]byte, headerLen, headerLen+len(payload)+1+tagLen)
	copy(out, salt)
	binary.BigEndian.PutUint32(out[saltLen:], RecordSize)
	out[saltLen+4] = pointLen
	copy(out[saltLen+5:], asPublic)
	// The single record is the last one: its plaintext ends with the
	// delimiter 0x02 and has no padding.
	plain := make([]byte, 0, len(payload)+1)
	plain = append(plain, payload...)
	plain = append(plain, 0x02)
	return gcm.Seal(out, nonce, plain, nil), nil
}

// ephemeralKey reads a P-256 private key from random.
func ephemeralKey(random io.Reader) (*ecdh.PrivateKey, error) {
	scalar := make([]byte, scalarLen)
	for range keyTries {
		if _, err := io.ReadFull(random, scalar); err != nil {
			return nil, ErrRandom
		}
		if k, err := ecdh.P256().NewPrivateKey(scalar); err == nil {
			return k, nil
		}
	}
	return nil, ErrRandom
}
