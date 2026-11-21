package execconfig

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// MatcherVersion names this matcher and its rules' syntax. It is the first
// string of [Digest], so a change of the matcher's reading changes every
// digest. Change it whenever Match would give another answer for the same
// rules.
const MatcherVersion = "execconfig/1"

// Digest returns the SHA-256, in lower-case hexadecimal, of the matcher
// version and of every rule's pattern and source in matching order, each
// string written with its length first (an unsigned varint), so no rule text
// can shift a boundary. The reason of a rule is not part of it: it is
// wording for the owner and does not change what matches. A card binds this
// digest, so an edit of the set between the plan and its use voids the
// approval: a rule added, removed, moved or changed gives another digest. It
// reads the set through [Set.Rules] only.
func Digest(s Set) string {
	h := sha256.New()
	str := func(v string) {
		var n [binary.MaxVarintLen64]byte
		h.Write(n[:binary.PutUvarint(n[:], uint64(len(v)))])
		h.Write([]byte(v))
	}
	str(MatcherVersion)
	for _, r := range s.Rules() {
		str(r.Pattern)
		str(r.Source)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Additions returns how many rules of the set a project added, those whose
// source is [SourceProject].
func Additions(s Set) int {
	n := 0
	for _, r := range s.Rules() {
		if r.Source == SourceProject {
			n++
		}
	}
	return n
}
