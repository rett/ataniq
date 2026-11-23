package seatpair

import (
	"crypto/sha256"
	"encoding/binary"
	"strings"
)

// FingerprintWords is the number of words of a fingerprint: 8 bytes of the
// digest, 64 bits. It is the one place the count is written; the packages that
// may not import this one (seatapp, ownerjournal) keep a mirrored constant,
// and a test in tests/ holds each equal to it.
const FingerprintWords = 8

// Fingerprint is the eight short words that the desk console and the phone both
// show for one credential, so the owner can see that the key the daemon
// received is the key the phone made.
type Fingerprint [FingerprintWords]string

// FingerprintOf is the fingerprint of a credential. It is the first
// FingerprintWords bytes of
//
//	SHA-256( BE32(len(credentialID)) || credentialID || BE32(len(spki)) || spki )
//
// each byte the index into the 256 words of the list (WordList). The lengths
// are 4-byte big-endian, so no id and key pair is the same input as another.
// FingerprintOf does not check its input; the credential id and key a
// Pairing keeps have been checked.
func FingerprintOf(credentialID, spki []byte) Fingerprint {
	h := sha256.New()
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(credentialID)))
	h.Write(n[:])
	h.Write(credentialID)
	binary.BigEndian.PutUint32(n[:], uint32(len(spki)))
	h.Write(n[:])
	h.Write(spki)
	sum := h.Sum(nil)
	var f Fingerprint
	for i := range f {
		f[i] = wordList[sum[i]]
	}
	return f
}

// String is the eight words, lower case, one space between them.
func (f Fingerprint) String() string { return strings.Join(f[:], " ") }

// WordList returns the 256 words, in the order of their byte values: the word
// of byte b is WordList()[b].
func WordList() [256]string { return wordList }
