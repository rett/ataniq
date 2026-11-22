package noaicheck

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// NFT hit details.
const (
	DetailDigestDiffers  = "digest-differs"  // the loaded rules are not the release's
	DetailListingMissing = "listing-missing" // the release pins a digest and no listing was given
)

// NoteNFTNotPinned is the note of a release without share/nft.digest.
const NoteNFTNotPinned = "nft not pinned"

// What changes in a listing while the rules stay the same: the counters'
// values, the expiry of a set element, a quota's use and rule handles.
var (
	nftCounter = regexp.MustCompile(`\bcounter packets [0-9]+ bytes [0-9]+`)
	nftExpires = regexp.MustCompile(`\s+expires [0-9a-z.]+`)
	nftQuota   = regexp.MustCompile(`\bused [0-9]+ [a-z]*bytes\b`)
	nftHandle  = regexp.MustCompile(`\s+# handle [0-9]+`)
)

// NormaliseNFT returns the listing of "nft list ruleset" in the form its
// digest is taken of: line endings are "\n", trailing spaces and blank lines
// are dropped, and what changes while the rules stay the same is removed
// (the values of counters, which become "counter", the expiry of set
// elements, a quota's use and "# handle N" comments).
func NormaliseNFT(listing []byte) []byte {
	var b strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(string(listing), "\r\n", "\n"), "\n") {
		line = nftCounter.ReplaceAllString(line, "counter")
		line = nftExpires.ReplaceAllString(line, "")
		line = nftQuota.ReplaceAllString(line, "")
		line = nftHandle.ReplaceAllString(line, "")
		line = strings.TrimRight(line, " \t")
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return []byte(b.String())
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
// written.
func (c *Checker) CheckNFT(listing []byte) {
	if c.lists.NFTDigest == "" {
		c.note(NoteNFTNotPinned)
		return
	}
	switch {
	case listing == nil:
		c.hit(KindNFT, "ruleset", DetailListingMissing)
	case NFTDigest(listing) != c.lists.NFTDigest:
		c.hit(KindNFT, "ruleset", DetailDigestDiffers)
	}
}
