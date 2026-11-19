package card

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"

	"ataniq/internal/approval/record"
)

// ShownVersion is the first part of [Card.ShownDigest]: it names the form of
// the digest. A form with other parts, or with the parts in another order,
// has another tag.
const ShownVersion = "ataniq.shown/1"

// ShownDigest returns the digest of what the card showed the owner before
// he decided: the SHA-256, in lower-case hexadecimal, over five parts, each
// written as its length (eight bytes, big-endian) and then its bytes:
//
//  1. the version tag [ShownVersion], so that a later form of this digest
//     can be told apart from this one;
//  2. the binding's digest, as its 64 hexadecimal digits;
//  3. the digest of the note's raw bytes, in the same form;
//  4. the digest of the earlier record when the card has a "differs from"
//     block (Differs.EarlierSHA256), and nothing, with the length zero,
//     when it has none;
//  5. the held mark: the one byte 1 when the card is held, and 0 when not.
//
// Two cards with the same digest showed the same bound content, the same
// note, the same earlier card and the same mark. What came after the
// decision (the state, the outcome line, the verdicts) is not part of it.
// Neither is the time the owner read the earlier card (Differs.ReadAt): a
// surface draws it, but the digest says which earlier card was shown, not
// when it was read.
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
		[]byte(ShownVersion),
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
