package noaicheck

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// NFT hit details: "HIT nft ruleset digest-differs", a finding, and
// "HIT incomplete nft listing-missing", evidence the check was not given.
const (
	DetailDigestDiffers  = "digest-differs"  // the loaded rules are not the release's
	DetailListingMissing = "listing-missing" // the release pins a digest and no listing was given
	PathNFT              = "nft"             // the name of the incomplete hit
)

// NoteNFTNotPinned is the note of a release without share/nft.digest.
const NoteNFTNotPinned = "nft not pinned"

// NormaliseNFT returns the listing of "nft list ruleset" in the form its
// digest is taken of: line endings are "\n", trailing spaces and blank lines
// are dropped, and the values that change while the rules stay the same are
// removed. Each line is read as tokens (a double-quoted string is one
// token, and "{", "}", "," and ";" are tokens of their own), and only these
// sequences of unquoted tokens are removed, with the space before them:
//
//   - "packets N bytes N" after "counter" (a counter statement, which
//     becomes "counter") or at the start of a line (a named counter's
//     body);
//   - "expires T" (a set element's expiry, T a time such as 59s123ms);
//   - "used N UNIT" (a quota's use, UNIT bytes, kbytes, mbytes or gbytes);
//   - "# handle N" at the end of a line (the handle comment of "nft -a").
//
// A quoted operand (an interface name, a comment) is never changed, so two
// rules that differ only there keep different digests.
//
// The tokens are read one at a time with at most three ahead, never a
// whole line of them, and the output is written as the line is read, so
// the memory it takes is that of the listing and its result. At most
// maxNFTLineTokens tokens of a line are looked at; the rest of a longer
// line is kept as it is.
func NormaliseNFT(listing []byte) []byte {
	s := string(listing)
	var b bytes.Buffer
	b.Grow(len(s))
	for len(s) > 0 {
		line := s
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			line, s = s[:i], s[i+1:]
			line = strings.TrimSuffix(line, "\r")
		} else {
			s = ""
		}
		start := b.Len()
		normaliseNFTLine(&b, line)
		out := b.Bytes()[start:]
		n := len(out)
		for n > 0 && (out[n-1] == ' ' || out[n-1] == '\t') {
			n--
		}
		if n == 0 {
			b.Truncate(start)
			continue
		}
		b.Truncate(start + n)
		b.WriteByte('\n')
	}
	return b.Bytes()
}

// maxNFTLineTokens bounds the tokens of one listing line NormaliseNFT
// looks at.
const maxNFTLineTokens = 1 << 20

// nftToken is a token of a listing line: its bytes are line[start:end].
type nftToken struct {
	start, end int
	quoted     bool
}

// nftLexer reads the tokens of one listing line, one at a time, holding
// at most three read ahead.
type nftLexer struct {
	line  string
	i     int
	ahead [3]nftToken
	n     int
}

// scan reads the next token of the line; false at its end.
func (l *nftLexer) scan() (nftToken, bool) {
	line := l.line
	for l.i < len(line) && (line[l.i] == ' ' || line[l.i] == '\t') {
		l.i++
	}
	if l.i >= len(line) {
		return nftToken{}, false
	}
	i := l.i
	switch c := line[i]; {
	case c == '"':
		j := i + 1
		for j < len(line) && line[j] != '"' {
			if line[j] == '\\' {
				j++
			}
			j++
		}
		j = min(j+1, len(line))
		l.i = j
		return nftToken{i, j, true}, true
	case strings.IndexByte("{},;", c) >= 0:
		l.i = i + 1
		return nftToken{i, i + 1, false}, true
	}
	j := i
	for j < len(line) && !strings.ContainsRune(" \t{},;\"", rune(line[j])) {
		j++
	}
	l.i = j
	return nftToken{i, j, false}, true
}

// peek returns the token k places ahead (0 the next); false past the end.
func (l *nftLexer) peek(k int) (nftToken, bool) {
	for l.n <= k {
		t, ok := l.scan()
		if !ok {
			return nftToken{}, false
		}
		l.ahead[l.n] = t
		l.n++
	}
	return l.ahead[k], true
}

// next returns the next token and moves past it; false at the end.
func (l *nftLexer) next() (nftToken, bool) {
	t, ok := l.peek(0)
	if !ok {
		return t, false
	}
	copy(l.ahead[:], l.ahead[1:l.n])
	l.n--
	return t, true
}

// normaliseNFTLine writes line to b with its volatile values removed (see
// NormaliseNFT).
func normaliseNFTLine(b *bytes.Buffer, line string) {
	l := &nftLexer{line: line}
	word := func(t nftToken, ok bool) string {
		if !ok || t.quoted {
			return "\x00" // matches nothing
		}
		return line[t.start:t.end]
	}
	at := 0
	var prev nftToken
	hasPrev := false
	// del removes the tokens from first to last, with the space before
	// first, and moves past them.
	del := func(first, last nftToken, skip int) {
		from := first.start
		if hasPrev {
			from = prev.end
		}
		b.WriteString(line[at:from])
		at = last.end
		for ; skip > 0; skip-- {
			l.next()
		}
		prev, hasPrev = last, true
	}
	for count := 0; count < maxNFTLineTokens; count++ {
		t, ok := l.next()
		if !ok {
			break
		}
		n1, ok1 := l.peek(0)
		n2, ok2 := l.peek(1)
		n3, ok3 := l.peek(2)
		switch word(t, true) {
		case "packets":
			if (!hasPrev || word(prev, true) == "counter") && isDigits(word(n1, ok1)) && word(n2, ok2) == "bytes" && isDigits(word(n3, ok3)) {
				del(t, n3, 3)
				continue
			}
		case "expires":
			if isNFTTime(word(n1, ok1)) {
				del(t, n1, 1)
				continue
			}
		case "used":
			if isDigits(word(n1, ok1)) && nftByteUnit(word(n2, ok2)) {
				del(t, n2, 2)
				continue
			}
		case "#":
			if word(n1, ok1) == "handle" && isDigits(word(n2, ok2)) && !ok3 {
				del(t, n2, 2)
				continue
			}
		}
		prev, hasPrev = t, true
	}
	b.WriteString(line[at:])
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// isNFTTime reports whether s is an nft time value: digits and unit
// letters ("59s123ms", "1d2h").
func isNFTTime(s string) bool {
	if s == "" || s[0] < '0' || s[0] > '9' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c == '.') {
			return false
		}
	}
	return true
}

func nftByteUnit(s string) bool {
	return s == "bytes" || s == "kbytes" || s == "mbytes" || s == "gbytes"
}

// NFTDigest is the SHA-256, lower-case hex, of the normalised listing.
func NFTDigest(listing []byte) string {
	sum := sha256.Sum256(NormaliseNFT(listing))
	return hex.EncodeToString(sum[:])
}

// CheckNFT is check 8: when the release pins a firewall digest
// (share/nft.digest), a listing whose digest (see NFTDigest) differs is a hit
// of kind "nft" with detail "digest-differs". No listing (nil) is not a
// finding but missing evidence: the hit "incomplete nft listing-missing",
// which makes the run incomplete. The caller runs "nft list ruleset"; the
// check never does. Without a pinned digest the note "nft not pinned" is
// written or, with RequirePolicy, the hit "policy nft.digest missing".
func (c *Checker) CheckNFT(listing []byte) {
	if c.lists.NFTDigest == "" {
		c.notEnforced(FileNFTDigest, DetailMissing, NoteNFTNotPinned)
		return
	}
	switch {
	case listing == nil:
		c.hit(KindIncomplete, PathNFT, DetailListingMissing)
	case NFTDigest(listing) != c.lists.NFTDigest:
		c.hit(KindNFT, "ruleset", DetailDigestDiffers)
	}
}
