package card

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// Changing any word would give previously issued bindings different tags.
func TestBindingTagVocabulary(t *testing.T) {
	sum := sha256.Sum256([]byte(strings.Join(bindingWords[:], "\n") + "\n"))
	if hex.EncodeToString(sum[:]) != "e7fe4d738b88c5acfd80fde2efc2188aa694e79d0b32e3837e5173963a7c676c" {
		t.Fatalf("binding vocabulary digest changed to %x", sum)
	}
}

func TestBindingTag(t *testing.T) {
	const digest = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	const want = "marsh candle koala lotus"
	for range 2 {
		if got := BindingTag(digest); got != want {
			t.Fatalf("BindingTag = %q, want %q", got, want)
		}
	}
	raw, err := hex.DecodeString(digest)
	if err != nil {
		t.Fatal(err)
	}
	for bit := 0; bit < len(raw)*8; bit++ {
		mask := byte(1 << (bit % 8))
		raw[bit/8] ^= mask
		if got := BindingTag(hex.EncodeToString(raw)); got == want || got == "" {
			t.Errorf("flipping bit %d gave %q", bit, got)
		}
		raw[bit/8] ^= mask
	}
}

func TestBindingTagInvalid(t *testing.T) {
	for _, digest := range []string{"", strings.Repeat("a", 63), strings.Repeat("0", 65),
		strings.Repeat("F", 64), strings.Repeat("a", 63) + "\x1b"} {
		if got := BindingTag(digest); got != "" {
			t.Errorf("BindingTag(%q) = %q, want empty", digest, got)
		}
	}
}
