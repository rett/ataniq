package presence

import (
	"encoding/hex"
	"fmt"
	"io"
)

// sessionLen is the length of a session id in characters; it is made from
// half as many bytes.
const sessionLen = 16

// NewSession makes a session id from the reader the caller supplies: it
// reads eight bytes and returns them as 16 lower-case hexadecimal digits. A
// surface makes one id per run. The caller chooses the reader (the system's
// random source in a program, a fixed one in a test); this package opens
// none. A reader that fails or ends early yields an error and no id.
func NewSession(r io.Reader) (string, error) {
	var b [sessionLen / 2]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return "", fmt.Errorf("presence: session id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// ValidSession reports whether s has the form of a session id: exactly 16
// characters, each a digit or a lower-case letter from a to f.
func ValidSession(s string) bool {
	if len(s) != sessionLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
