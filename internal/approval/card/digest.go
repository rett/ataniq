package card

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"ataniq/internal/approval/record"
)

// ShownDigest returns the digest of what the card showed the owner before
// he decided: the SHA-256, in lower-case hexadecimal, over four parts, each
// written as its length (eight bytes, big-endian) and then its bytes:
//
//  1. the binding's digest, as its 64 hexadecimal digits;
//  2. the digest of the note's raw bytes, in the same form;
//  3. the digest of the earlier record when the card has a "differs from"
//     block (Differs.EarlierSHA256), and nothing, with the length zero,
//     when it has none;
//  4. the held mark: the one byte 1 when the card is held, and 0 when not.
//
// Two cards with the same digest showed the same bound content, the same
// note, the same earlier card and the same mark. What came after the
// decision (the state, the outcome line, the verdicts) is not part of it.
func (c Card) ShownDigest() string {
	earlier := ""
	if c.Differs != nil {
		earlier = c.Differs.EarlierSHA256
	}
	held := []byte{0}
	if c.After.Held {
		held[0] = 1
	}
	h := sha256.New()
	for _, part := range [][]byte{
		[]byte(c.BindingSHA256),
		[]byte(record.Digest([]byte(c.Note.bytes()))),
		[]byte(earlier),
		held,
	} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	return hex.EncodeToString(h.Sum(nil))
}
