//go:build linux && ataniq_canary

package leadchat

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/genesis/brief"
)

// The secret-shaped samples are made at run time from a hash of a fixed
// word, so no key-shaped string is in the source.
func syntheticCommitID() string {
	sum := sha256.Sum256([]byte("synthetic commit"))
	return hex.EncodeToString(sum[:])[:40]
}

func syntheticKeyRun() string {
	sum := sha256.Sum256([]byte("synthetic key"))
	return strings.Map(func(r rune) rune {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return r
		}
		return -1
	}, base64.StdEncoding.EncodeToString(append(sum[:], sum[:]...)))
}

func syntheticSecrets(t *testing.T) map[string]string {
	t.Helper()
	sum := sha256.Sum256([]byte("synthetic value"))
	samples := map[string]string{
		"commit id":  "Merged as " + syntheticCommitID() + ".",
		"key run":    "Use " + syntheticKeyRun() + " for the call.",
		"pem marker": "Here:\n" + strings.Repeat("-", 5) + "BEGIN SYNTHETIC BLOCK" + strings.Repeat("-", 5) + "\nend",
		"assignment": "set pass" + "word=" + hex.EncodeToString(sum[:4]) + " then retry",
	}
	for name, s := range samples {
		if !brief.SecretShaped(s) {
			t.Fatalf("the %s sample is not secret-shaped; the test needs another sample", name)
		}
	}
	return samples
}

func TestReplyText(t *testing.T) {
	cases := []struct {
		name string
		in   string
		text string
		v    Verdict
	}{
		{"plain", "Tests pass.", "Tests pass.", VerdictOK},
		{"trailing line feeds trimmed", "Done.\n\n\n", "Done.", VerdictOK},
		{"inner line feeds kept", "one\ntwo\n", "one\ntwo", VerdictOK},
		{"short commit id passes", "Committed 1a2b3c4d5e6f.", "Committed 1a2b3c4d5e6f.", VerdictOK},
		{"exactly the cap", strings.Repeat("a", MaxReplyBytes), strings.Repeat("a", MaxReplyBytes), VerdictOK},
		{"over the cap", strings.Repeat("a", MaxReplyBytes+1), "", VerdictTooLong},
		{"over the cap with only a line feed over", strings.Repeat("a", MaxReplyBytes) + "\n", "", VerdictTooLong},
		{"tab", "a\tb", "", VerdictNotPlain},
		{"carriage return", "a\r\nb", "", VerdictNotPlain},
		{"escape", "a\x1b[31mred", "", VerdictNotPlain},
		{"bad UTF-8", "a\xffb", "", VerdictNotPlain},
		{"bidi override", "abc‮def", "", VerdictNotPlain},
		{"zero width joiner", "a‍b", "", VerdictNotPlain},
		{"C1 control", "a\u0085b", "", VerdictNotPlain},
		{"empty", "", "", VerdictNotPlain},
		{"only line feeds", "\n\n", "", VerdictNotPlain},
		{"only spaces", "   \n", "", VerdictNotPlain},
	}
	for _, c := range cases {
		text, v := ReplyText([]byte(c.in))
		if text != c.text || v != c.v {
			t.Errorf("%s: got %q, %d; want %q, %d", c.name, text, v, c.text, c.v)
		}
	}
	for name, s := range syntheticSecrets(t) {
		if text, v := ReplyText([]byte(s)); v != VerdictWithheld || text != "" {
			t.Errorf("%s: got %q, %d; want withheld", name, text, v)
		}
	}
}

func TestFixedTextsArePlain(t *testing.T) {
	all := []string{TextTurnedOn, TextWithheld, TextStoppedFolder, TextStoppedOdd, TextStoppedFull}
	for _, why := range dropOrder {
		s, ok := dropTexts[why]
		if !ok {
			t.Fatalf("no drop text for %s", why)
		}
		all = append(all, s)
	}
	for _, s := range all {
		if !ownerwire.ValidChatText(s) || brief.SecretShaped(s) {
			t.Errorf("%q is not a plain, harmless text", s)
		}
	}
}

// A text that names a bound names the constant's value, so the two cannot
// drift apart.
func TestFixedTextsNameTheirBounds(t *testing.T) {
	cases := []struct {
		text   string
		bounds []int
	}{
		{dropTexts[ReasonTooLong], []int{MaxReplyBytes}},
		{TextStoppedFull, []int{MaxWaiting}},
	}
	for _, c := range cases {
		for _, b := range c.bounds {
			if !strings.Contains(c.text, " "+strconv.Itoa(b)+" ") {
				t.Errorf("%q does not name %d", c.text, b)
			}
		}
	}
	for _, s := range append([]string{TextTurnedOn, TextWithheld, TextStoppedFolder, TextStoppedOdd, TextStoppedFull}, slices.Collect(maps.Values(dropTexts))...) {
		if !systemText(s) {
			t.Errorf("%q is not a fixed text", s)
		}
	}
	if systemText("Chat was turned on.") {
		t.Error("a text that is not fixed was accepted")
	}
}

func TestValidReplyName(t *testing.T) {
	good := []string{"a.txt", "0001-tests-pass.txt", "x_y.z-1.txt", strings.Repeat("a", 60) + ".txt"}
	bad := []string{
		".txt", ".reply.txt", "-a.txt", "_a.txt", "A.txt", "a.TXT", "a.md", "a", "a .txt", "a/b.txt",
		strings.Repeat("a", 61) + ".txt", "é.txt", "a.txt.tmp",
	}
	for _, n := range good {
		if !ValidReplyName(n) {
			t.Errorf("%q refused", n)
		}
	}
	for _, n := range bad {
		if ValidReplyName(n) {
			t.Errorf("%q accepted", n)
		}
	}
}
