//go:build linux && ataniq_canary

package standin

import "strings"

// The token's length bounds and the line it is carried in.
const (
	// TokenPrefix starts the line that carries the token.
	TokenPrefix = "canary-token: "
	// MinTokenLen and MaxTokenLen bound the token, in characters.
	MinTokenLen = 16
	MaxTokenLen = 64
)

// ValidToken reports whether s is 16 to 64 characters of a-z, 0-9 and "-".
func ValidToken(s string) bool {
	if len(s) < MinTokenLen || len(s) > MaxTokenLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
		default:
			return false
		}
	}
	return true
}

// ParseToken returns the token of the first line of text that has the exact
// form "canary-token: <token>". Lines end in CR, LF or both. A line that has
// the prefix but not a valid token (a longer or shorter token, another
// character, a space after it) is not the line: the search goes on. Nothing
// but the token is returned.
func ParseToken(text string) (string, bool) {
	for len(text) > 0 {
		line := text
		if i := strings.IndexAny(text, "\r\n"); i >= 0 {
			line, text = text[:i], text[i+1:]
		} else {
			text = ""
		}
		if tok, ok := strings.CutPrefix(line, TokenPrefix); ok && ValidToken(tok) {
			return tok, true
		}
	}
	return "", false
}
