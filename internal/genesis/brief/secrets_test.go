package brief

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
)

// The key-shaped samples of the tests are built at run time from pieces,
// none of which looks like a key, so that no file of the repository holds a
// literal that a secret scanner takes for one. The test of the whole genesis
// tree (package genesis) greps every test file and every testdata file for
// the shapes below and finds none.
//
// Each sample has the same shape and weight as the literal it replaced: the
// same prefix, a body of the same length and the same kinds of characters.

// pem returns the header line of a private key block.
func pem() string { return "-----" + "BEGIN RSA PRIV" + "ATE KEY-----" }

// keyShaped returns the samples SecretShaped must refuse, by name.
func keyShaped() map[string]string {
	return map[string]string{
		"pem":          pem(),
		"openai":       "my key is sk" + "-" + strings.Repeat("abcde", 4),
		"stripe":       "sk" + "_live_" + "51HxAbCdEf" + "GhIjKlMn",
		"github":       "gh" + "p_" + strings.Repeat("aB3dE5", 6),
		"github-fine":  "github" + "_pat_" + "11ABCDEFG" + "0123456789",
		"slack":        "xox" + "b-" + "123456789012" + "-abcdefghij",
		"aws-id":       "AKI" + "A" + "IOSFODNN7" + "EXAMPLE",
		"password":     "pass" + "word" + "=hunter2",
		"token-case":   "use Tok" + "en=abc",
		"api-key":      "api_" + "key=xyz",
		"hex-32":       strings.Repeat("0123456789abcdef", 2),
		"hex-40":       hex.EncodeToString(bytes.Repeat([]byte{0xd6, 0x70, 0x46, 0x0b, 0x4b}, 4)),
		"aws-secret":   "wJalrXUtnFEMI" + "+K7MDENG" + "+bPxRfiCYEXAMPLEKEY",
		"base64-run":   "key: " + base64.StdEncoding.EncodeToString([]byte("foobarbazqux1234567890ABCDEFGHIJK")),
		"assign-short": "pass" + "word=x",
		// The assignment forms with spaces or a colon.
		"assign-space": "pass" + "word= hunter2",
		"assign-colon": "pass" + "word: hunter2hunter2",
		"token-colon":  "Tok" + "en: " + strings.Repeat(".", 6),
		"secret-dots":  "sec" + "ret=" + strings.Repeat(".", 3),
		"api-key-text": "api" + " key: " + "abc123def456",
		"api-key-eq":   "api" + " key = " + "abc123",
	}
}

// notKeyShaped returns texts SecretShaped must pass, by name: names, web
// addresses and phrases that the rules once took for keys. They are built at
// run time like the samples above, for those that a secret scanner might
// read as a key.
func notKeyShaped() map[string]string {
	return map[string]string{
		"a mixed-case address": "see https://github.com/" + "AcmeCorp/Mobile-App-Backend-Service2" + " for the old one",
		"a hyphenated title":   "A Real-Time-Collaborative-" + "Editor-for-Markdown-2026",
		"a hyphenated plan":    "Q3-2026-Marketing-Plan-" + "For-New-Product-Launch",
		"the phrase":           "store each PRIV" + "ATE KEY backup offline",
		"a path":               "src/components/Header2/" + "IndexPage/StyleSheetLoader",
		"a colon and a word":   "pass" + "word: required at login",
		"a colon and a short":  "sec" + "ret: 3 ingredients",
		"api key in a phrase":  "an api" + " key for the weather service",
		"an empty assignment":  "pass" + "word= ",
		"an empty colon":       "tok" + "en:",
	}
}

// keyShape returns one named sample.
func keyShape(t testing.TB, name string) string {
	t.Helper()
	s, ok := keyShaped()[name]
	if !ok {
		t.Fatalf("no key-shaped sample is named %q", name)
	}
	return s
}

// The texts that once were refused and must not be: the check catches a
// pasted key and an obvious assignment, and leaves names, addresses, paths
// and the plain words alone.
func TestTheseTextsAreNotKeyShaped(t *testing.T) {
	for name, s := range notKeyShaped() {
		if SecretShaped(s) {
			t.Errorf("%s is refused: %q", name, s)
		}
		if err := checkFree(s, "the text"); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A bare git commit id has the shape of a hexadecimal key, and is
	// refused: say "the commit" instead.
	if !SecretShaped(keyShape(t, "hex-40")) {
		t.Error("forty hexadecimal digits pass")
	}
	// Bytes that are not UTF-8 and letters that change their length when
	// folded do not upset the search for an assignment.
	for _, s := range []string{"\xffpass" + "word=\xff", "İİİ pass" + "word: x1x1x1x1", "ǅ" + "TOK" + "EN=Ǆ"} {
		_ = SecretShaped(s)
	}
}

// The samples are what they claim to be: every one is refused by
// SecretShaped, so the tests that use them are as strong as with literals.
func TestTheKeyShapedSamplesAreKeyShaped(t *testing.T) {
	for name, s := range keyShaped() {
		if !SecretShaped(s) {
			t.Errorf("the sample %q is not secret-shaped: %q", name, s)
		}
	}
	// The weight of the samples that were literals before.
	for name, want := range map[string]int{"github": 40, "aws-id": 20, "openai": len("my key is sk-") + 20, "hex-40": 40, "hex-32": 32} {
		if got := len(keyShape(t, name)); got != want {
			t.Errorf("the sample %q is %d bytes, want %d", name, got, want)
		}
	}
}
