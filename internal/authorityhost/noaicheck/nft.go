package noaicheck

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// NFT hit details.
const (
	DetailDigestDiffers  = "digest-differs"  // the loaded rules are not the release's
	DetailListingMissing = "listing-missing" // the release pins a digest and no listing was given
)

// NoteNFTNotPinned is the note of a release without share/nft.digest.
const NoteNFTNotPinned = "nft not pinned"

// NormaliseNFT returns the listing of "nft list ruleset" in the form its
// digest is taken of: line endings are "\n", trailing spaces and blank lines
// are dropped, and the values that change while the rules stay the same are
// removed. Each line is split into tokens (a double-quoted string is one
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
func NormaliseNFT(listing []byte) []byte {
	var b strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(string(listing), "\r\n", "\n"), "\n") {
		line = strings.TrimRight(normaliseNFTLine(line), " \t")
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

// nftToken is a token of a listing line: its bytes are line[start:end].
type nftToken struct {
	start, end int
	quoted     bool
}

func nftTokens(line string) []nftToken {
	var out []nftToken
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == ' ' || c == '\t':
			i++
		case c == '"':
			j := i + 1
			for j < len(line) && line[j] != '"' {
				if line[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(line))
			out = append(out, nftToken{i, j, true})
			i = j
		case strings.IndexByte("{},;", c) >= 0:
			out = append(out, nftToken{i, i + 1, false})
			i++
		default:
			j := i
			for j < len(line) && !strings.ContainsRune(" \t{},;\"", rune(line[j])) {
				j++
			}
			out = append(out, nftToken{i, j, false})
			i = j
		}
	}
	return out
}

func normaliseNFTLine(line string) string {
	toks := nftTokens(line)
	word := func(i int) string {
		if i < 0 || i >= len(toks) || toks[i].quoted {
			return "\x00" // matches nothing
		}
		return line[toks[i].start:toks[i].end]
	}
	// del marks tokens a..b for removal, with the space before a.
	type span struct{ from, to int }
	var dels []span
	del := func(a, b int) {
		from := toks[a].start
		if a > 0 {
			from = toks[a-1].end
		}
		dels = append(dels, span{from, toks[b].end})
	}
	for i := 0; i < len(toks); i++ {
		switch word(i) {
		case "packets":
			if (i == 0 || word(i-1) == "counter") && isDigits(word(i+1)) && word(i+2) == "bytes" && isDigits(word(i+3)) {
				del(i, i+3)
				i += 3
			}
		case "expires":
			if isNFTTime(word(i + 1)) {
				del(i, i+1)
				i++
			}
		case "used":
			if isDigits(word(i+1)) && nftByteUnit(word(i+2)) {
				del(i, i+2)
				i += 2
			}
		case "#":
			if i+3 == len(toks) && word(i+1) == "handle" && isDigits(word(i+2)) {
				del(i, i+2)
				i += 2
			}
		}
	}
	if len(dels) == 0 {
		return line
	}
	var b strings.Builder
	at := 0
	for _, d := range dels {
		b.WriteString(line[at:d.from])
		at = d.to
	}
	b.WriteString(line[at:])
	return b.String()
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
// of kind "nft" with detail "digest-differs", and no listing (nil) is a hit
// with detail "listing-missing". The caller runs "nft list ruleset"; the
// check never does. Without a pinned digest the note "nft not pinned" is
// written or, with RequirePolicy, the hit "policy nft.digest missing".
func (c *Checker) CheckNFT(listing []byte) {
	if c.lists.NFTDigest == "" {
		c.notEnforced(FileNFTDigest, DetailMissing, NoteNFTNotPinned)
		return
	}
	switch {
	case listing == nil:
		c.hit(KindNFT, "ruleset", DetailListingMissing)
	case NFTDigest(listing) != c.lists.NFTDigest:
		c.hit(KindNFT, "ruleset", DetailDigestDiffers)
	}
}
