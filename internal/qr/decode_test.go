package qr

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// zbarDecode writes the code as a PNG with a quiet zone of 4 modules at scale
// 8 and returns what zbarimg reads from it. zbarimg converts the charset of
// byte mode data unless it is told the data is binary, which would change
// the bytes with the high bit set, so it is: --set binary.
func zbarDecode(t testing.TB, c *Code) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "code.png")
	if err := os.WriteFile(path, c.PNG(8, 4), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("zbarimg", "--raw", "-q", "--set", "binary", path).Output()
	if err != nil {
		return "", err
	}
	// zbarimg ends the data it prints with a newline.
	return strings.TrimSuffix(string(out), "\n"), nil
}

func needZbar(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("zbarimg"); err != nil {
		t.Skip("zbarimg is not on PATH")
	}
}

func checkDecodes(t *testing.T, text string, l Level, wantVersion int) {
	t.Helper()
	c, err := Encode(text, l)
	if err != nil {
		t.Errorf("Encode of %d bytes at %v: %v", len(text), l, err)
		return
	}
	if wantVersion != 0 && c.Version() != wantVersion {
		t.Errorf("%d bytes at %v: version %d, want %d", len(text), l, c.Version(), wantVersion)
	}
	got, err := zbarDecode(t, c)
	if err != nil {
		t.Errorf("%d bytes at %v (version %d, mask %d): zbarimg: %v", len(text), l, c.Version(), c.Mask(), err)
		return
	}
	if got != text {
		t.Errorf("%d bytes at %v (version %d, mask %d): zbarimg read %q, want %q", len(text), l, c.Version(), c.Mask(), got, text)
	}
}

func TestZbarDecodesExamples(t *testing.T) {
	needZbar(t)
	// The high-bit bytes are valid UTF-8 in one text and invalid in the
	// next, and the last has every byte value from 0x80 up.
	var all []byte
	for b := 0x80; b <= 0xFF; b++ {
		all = append(all, byte(b))
	}
	texts := map[string]string{
		"20 characters":      "pairing code 0123456",
		"60 character URL":   ("https://example.invalid/app/start?seat=1#" + strings.Repeat("Ab3_", 5))[:60],
		"120 character URL":  ("https://example.invalid/app/start?seat=1#" + strings.Repeat("Zx9-", 20))[:120],
		"UTF-8":              "café ☃ über \U0001F512",
		"not UTF-8":          "\xff\xfe\x80\x81 high bits \xc3",
		"every high byte":    string(all),
		"control characters": "a\x00b\x01\x1f\t\r\x7f z",
		"one byte":           "x",
	}
	if len(texts["60 character URL"]) != 60 || len(texts["120 character URL"]) != 120 {
		t.Fatalf("example lengths %d and %d", len(texts["60 character URL"]), len(texts["120 character URL"]))
	}
	if len(texts["20 characters"]) != 20 {
		t.Fatalf("example length %d", len(texts["20 characters"]))
	}
	for name, text := range texts {
		for _, l := range []Level{L, M} {
			t.Run(fmt.Sprintf("%s/%v", name, l), func(t *testing.T) {
				checkDecodes(t, text, l, 0)
			})
		}
	}
}

// pattern is a text of n bytes that uses every byte value.
func pattern(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*7 + i/256)
	}
	return string(b)
}

func TestZbarDecodesMaximumOfEveryVersion(t *testing.T) {
	needZbar(t)
	for _, l := range []Level{L, M} {
		for v := MinVersion; v <= MaxVersion; v++ {
			t.Run(fmt.Sprintf("%v/version %d", l, v), func(t *testing.T) {
				n := byteCapacity(v, l)
				checkDecodes(t, pattern(n), l, v)
				// And the same with plain printable text.
				checkDecodes(t, strings.Repeat("https://example.invalid/#", n)[:n], l, v)
			})
		}
	}
}

func TestZbarDecodesVersionBoundaries(t *testing.T) {
	needZbar(t)
	for _, l := range []Level{L, M} {
		for v := MinVersion; v < MaxVersion; v++ {
			// The first text that no longer fits version v.
			n := byteCapacity(v, l) + 1
			checkDecodes(t, pattern(n), l, v+1)
		}
	}
}

func TestZbarDecodesEveryMask(t *testing.T) {
	needZbar(t)
	// Encode forces one mask at a time, so each mask condition, each format
	// information and each version information is read by a decoder that is
	// not this package.
	for _, l := range []Level{L, M} {
		for _, v := range []int{1, 2, 6, 7, 10} {
			text := pattern(byteCapacity(v, l) * 2 / 3)
			for mask := 0; mask < 8; mask++ {
				c, err := encode(text, l, v, mask)
				if err != nil {
					t.Fatal(err)
				}
				got, err := zbarDecode(t, c)
				if err != nil || got != text {
					t.Errorf("level %v version %d mask %d: zbarimg read %d bytes, err %v", l, v, mask, len(got), err)
				}
			}
		}
	}
}

func TestZbarDecodesManyLengths(t *testing.T) {
	needZbar(t)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 60; i++ {
		l := []Level{L, M}[i%2]
		n := 1 + rng.Intn(byteCapacity(MaxVersion, l))
		b := make([]byte, n)
		rng.Read(b)
		checkDecodes(t, string(b), l, 0)
	}
}

func TestZbarDecodesDefaultLevel(t *testing.T) {
	needZbar(t)
	text := "https://example.invalid/#" + strings.Repeat("q", 40)
	c, err := Encode(text, DefaultLevel)
	if err != nil || c.Level() != M {
		t.Fatalf("%v, level %v", err, c.Level())
	}
	if got, err := zbarDecode(t, c); err != nil || !bytes.Equal([]byte(got), []byte(text)) {
		t.Errorf("read %q, %v", got, err)
	}
}
