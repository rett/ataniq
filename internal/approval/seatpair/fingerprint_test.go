package seatpair

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func seqBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// The vector is fixed: the phone's script must give the same eight words for
// the same id and key. It was made with a second implementation (Python's
// hashlib and struct), not with this package.
const (
	vectorSPKI  = "3059301306072a8648ce3d020106082a8648ce3d030107034200046ff03b949241ce1dadd43519e6960e0a85b41a69a05c328103aa2bce1594ca163c4f753a55bf01dc53f6c0b0c7eee78b40c6ff7d25a96e2282b989cef71c144a"
	vectorWords = "poppy ribbon brick goblet nickel ruby rudder camel"
	// vectorDigest is the first eight bytes of the SHA-256 of the input.
	vectorDigest = "bbc91d659ccecf25"
	// listDigest is the SHA-256 of the words, each followed by a newline.
	listDigest = "e7fe4d738b88c5acfd80fde2efc2188aa694e79d0b32e3837e5173963a7c676c"
)

func TestFingerprintVector(t *testing.T) {
	spki, err := hex.DecodeString(vectorSPKI)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(spki, spkiOfScalar(scalarOf(1))) {
		t.Fatal("the vector's key is not the key of the scalar 0x0101...01")
	}
	if _, err := ParseKey(spki); err != nil {
		t.Fatalf("the vector's key: %v", err)
	}
	id := seqBytes(32)
	f := FingerprintOf(id, spki)
	if f.String() != vectorWords {
		t.Errorf("fingerprint = %q, want %q", f, vectorWords)
	}
	// Again, step by step: the length prefixes are 4 bytes, big-endian.
	in := append([]byte{0, 0, 0, 32}, id...)
	in = append(in, 0, 0, 0, byte(len(spki)))
	in = append(in, spki...)
	sum := sha256.Sum256(in)
	if hex.EncodeToString(sum[:FingerprintWords]) != vectorDigest {
		t.Errorf("digest prefix = %x, want %s", sum[:FingerprintWords], vectorDigest)
	}
	for i, w := range f {
		if w != wordList[sum[i]] {
			t.Errorf("word %d = %q, want the word of byte %d", i, w, sum[i])
		}
	}
}

func TestFingerprintFollowsBothInputs(t *testing.T) {
	spki := spkiOfScalar(scalarOf(1))
	id := seqBytes(32)
	base := FingerprintOf(id, spki)
	if base != FingerprintOf(id, spki) {
		t.Error("fingerprint is not stable")
	}
	otherID := append([]byte(nil), id...)
	otherID[31] ^= 1
	otherKey := spkiOfScalar(scalarOf(2))
	if FingerprintOf(otherID, spki) == base {
		t.Error("another id, the same fingerprint")
	}
	if FingerprintOf(id, otherKey) == base {
		t.Error("another key, the same fingerprint")
	}
}

// TestFingerprintLengthPrefix: moving a byte from the key to the id changes
// the input, so the pair (id || b, key) and (id, b || key) differ.
func TestFingerprintLengthPrefix(t *testing.T) {
	a := FingerprintOf([]byte("abcd"), []byte("efgh"))
	for _, c := range [][2]string{{"abcde", "fgh"}, {"abc", "defgh"}, {"", "abcdefgh"}, {"abcdefgh", ""}} {
		if FingerprintOf([]byte(c[0]), []byte(c[1])) == a {
			t.Errorf("(%q, %q) collides with (abcd, efgh)", c[0], c[1])
		}
	}
	if FingerprintOf(nil, nil) != FingerprintOf([]byte{}, []byte{}) {
		t.Error("nil and empty differ")
	}
}

func TestFingerprintString(t *testing.T) {
	f := FingerprintOf(seqBytes(16), spkiOfScalar(scalarOf(3)))
	s := f.String()
	if got := strings.Fields(s); len(got) != FingerprintWords || FingerprintWords != 8 {
		t.Errorf("fingerprint %q has %d words", s, len(got))
	}
	if fmt.Sprint(f) != s || fmt.Sprintf("%s", f) != s {
		t.Errorf("Sprint and %%s differ from String: %q", fmt.Sprint(f))
	}
}

func TestFingerprintSpread(t *testing.T) {
	// Every word of the list is reachable and the words of a run of keys are
	// not skewed to a part of it: 120 keys of 8 words give 960 draws.
	hits := map[string]int{}
	for i := 1; i <= 120; i++ {
		f := FingerprintOf(seqBytes(16), spkiOfScalar(scalarOf(byte(i))))
		for _, w := range f {
			hits[w]++
		}
	}
	if len(hits) < 100 {
		t.Errorf("only %d different words in 960 draws", len(hits))
	}
}

func TestWordListIsPinned(t *testing.T) {
	sum := sha256.Sum256([]byte(strings.Join(wordList[:], "\n") + "\n"))
	if got := hex.EncodeToString(sum[:]); got != listDigest {
		t.Errorf("the word list changed: digest %s, pinned %s; every fingerprint changes with it, so the phone's copy must change too", got, listDigest)
	}
	if WordList() != wordList {
		t.Error("WordList differs from the list")
	}
}

// TestFingerprintHoldsSixtyFourBits holds the design number: the list has 256
// words, 8 bits each, and a fingerprint is 8 of them, 64 bits. A shorter
// fingerprint, or a shorter list, is a weaker one and fails here.
func TestFingerprintHoldsSixtyFourBits(t *testing.T) {
	if len(wordList) != 256 {
		t.Fatalf("the list has %d words, the fingerprint counts 8 bits for each", len(wordList))
	}
	if bits := FingerprintWords * 8; bits != 64 {
		t.Errorf("a fingerprint of %d words is %d bits, want 64", FingerprintWords, bits)
	}
}
