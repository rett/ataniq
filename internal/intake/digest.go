package intake

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// digestDomain starts every digest, so that the hash of a review cannot be
// taken for a hash of anything else, and a change of the encoding changes it.
const digestDomain = "ataniq-intake-review-v1"

// Digest returns the hex SHA-256 over a canonical encoding of the base and
// tip ids, the two ref names, the changes with their flags and the flagged
// list (the kind, path and pattern of each flag, not its reason text). Every
// string is written with its length first, so no lead text can shift a
// boundary. The commit list is not part of it: base and tip decide it. The
// digest is the same for two reviews of the same refs at the same
// commits, and changes when the base, the tip, a path, a mode, an id or a flag
// changes.
func (r *Review) Digest() string {
	h := sha256.New()
	str := func(s string) {
		var n [binary.MaxVarintLen64]byte
		h.Write(n[:binary.PutUvarint(n[:], uint64(len(s)))])
		h.Write([]byte(s))
	}
	num := func(v uint64) {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], v)
		h.Write(b[:])
	}
	str(digestDomain)
	str(r.BaseRef)
	str(r.TipRef)
	str(r.Base)
	str(r.Tip)
	num(uint64(len(r.Changes)))
	for _, c := range r.Changes {
		str(c.Path)
		num(uint64(c.Status))
		num(uint64(c.OldMode))
		num(uint64(c.NewMode))
		str(c.OldID)
		str(c.NewID)
		writeFlags(str, num, c.Flags)
	}
	writeFlags(str, num, r.Flagged)
	return hex.EncodeToString(h.Sum(nil))
}

// writeFlags writes the kind, the path and the pattern of each flag. The
// reason is not written: it is the package's wording, a function of the
// rest, and a change of wording must not void a sign-off.
func writeFlags(str func(string), num func(uint64), flags []Flag) {
	num(uint64(len(flags)))
	for _, f := range flags {
		str(string(f.Kind))
		str(f.Path)
		str(f.Pattern)
	}
}
