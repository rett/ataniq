//go:build linux && ataniq_canary

package standin

import (
	"strings"
	"testing"
)

func TestParseToken(t *testing.T) {
	min := strings.Repeat("a", 16)
	max := strings.Repeat("0", 64)
	for _, c := range []struct {
		name, text, want string
		ok               bool
	}{
		{"one line", "canary-token: " + testToken, testToken, true},
		{"minimum length", "canary-token: " + min, min, true},
		{"maximum length", "canary-token: " + max, max, true},
		{"one short", "canary-token: " + min[1:], "", false},
		{"one long", "canary-token: " + max + "0", "", false},
		{"digits letters and hyphens", "canary-token: a0-b1-c2-d3-e4-f5", "a0-b1-c2-d3-e4-f5", true},
		{"after a header line", "[ataniq-msg id=7 from=ataniq kind=canary]\ncanary-token: " + testToken + "\n", testToken, true},
		{"CR line ends", "header\rcanary-token: " + testToken + "\rmore", testToken, true},
		{"CRLF line ends", "header\r\ncanary-token: " + testToken + "\r\nmore", testToken, true},
		{"LF line ends", "header\ncanary-token: " + testToken + "\nmore", testToken, true},
		{"the first valid line wins", "canary-token: " + min + "\ncanary-token: " + max, min, true},
		{"an invalid line is skipped", "canary-token: SHORT\ncanary-token: " + testToken, testToken, true},
		{"empty", "", "", false},
		{"no token line", "hello\nworld\n", "", false},
		{"upper case", "canary-token: 0123456789ABCDEF-sample", "", false},
		{"underscore", "canary-token: 0123456789abcdef_sample", "", false},
		{"a dot", "canary-token: 0123456789abcdef.sample", "", false},
		{"a space inside", "canary-token: 0123456789abcdef sample", "", false},
		{"a trailing space", "canary-token: " + testToken + " ", "", false},
		{"a trailing tab", "canary-token: " + testToken + "\t", "", false},
		{"shell text after", "canary-token: " + testToken + "; touch x", "", false},
		{"shell substitution", "canary-token: $(touch x)0123456789abcdef", "", false},
		{"a backquote", "canary-token: `touch x`0123456789abcdef", "", false},
		{"no space after the colon", "canary-token:" + testToken, "", false},
		{"two spaces after the colon", "canary-token:  " + testToken, "", false},
		{"a leading space", " canary-token: " + testToken, "", false},
		{"not at the line start", "x canary-token: " + testToken, "", false},
		{"another case of the prefix", "Canary-Token: " + testToken, "", false},
		{"the prefix only", "canary-token: ", "", false},
		{"a NUL after", "canary-token: " + testToken + "\x00", "", false},
		{"an ESC after", "canary-token: " + testToken + "\x1b[31m", "", false},
		{"a non-ASCII digit", "canary-token: 0123456789abcdef-sampl٣", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ParseToken(c.text)
			if got != c.want || ok != c.ok {
				t.Fatalf("ParseToken(%q) = %q, %v; want %q, %v", c.text, got, ok, c.want, c.ok)
			}
			if ok && !ValidToken(got) {
				t.Fatalf("ParseToken returned %q, which ValidToken refuses", got)
			}
		})
	}
}

func TestValidToken(t *testing.T) {
	for n := 0; n <= 70; n++ {
		want := n >= 16 && n <= 64
		if got := ValidToken(strings.Repeat("x", n)); got != want {
			t.Errorf("ValidToken(%d characters) = %v, want %v", n, got, want)
		}
	}
	for r := rune(0); r < 0x250; r++ {
		s := strings.Repeat("a", 15) + string(r)
		want := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-'
		if got := ValidToken(s); got != want {
			t.Errorf("ValidToken with the character %U = %v, want %v", r, got, want)
		}
	}
}
