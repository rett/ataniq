package brief

import (
	"math"
	"strings"
	"unicode/utf8"

	"ataniq/internal/genesis/internal/canon"
)

// Untrusted is free text: the text of an "other" answer, or a note. It is
// data and never instructions. It is for the owner's eyes only: whoever
// shows it escapes it, and it is never served to an implementation agent
// (a plan keeps it in its notes and leaves it out of what an agent is
// given). It is the only type in a brief that carries free text.
type Untrusted string

// MaxFreeText is the cap of one free text, in bytes.
const MaxFreeText = 200

// checkFree checks one free text: 1 to MaxFreeText bytes of valid UTF-8,
// no control character, not only spaces, and nothing shaped like a secret.
func checkFree(s string, where string) error {
	switch {
	case len(s) > MaxFreeText:
		return fail(CodeTooLarge, where+" is longer than 200 bytes")
	case strings.TrimSpace(s) == "":
		return fail(CodeValueInvalid, where+" is empty")
	case !canon.Plain(s):
		return fail(CodeValueInvalid, where+" holds a control character or bytes that are not UTF-8")
	case SecretShaped(s):
		return fail(CodeSecretShaped, where+" looks like a key, a token or a password")
	}
	return nil
}

// secretPrefixes are the starts of well-known credential formats. A prefix
// counts only at the start of a word and followed by at least
// minPrefixedRun token characters.
var secretPrefixes = []string{
	"sk-", "sk_live_", "sk_test_", "rk_live_", "pk_live_",
	"ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_", "glpat-",
	"xoxa-", "xoxb-", "xoxp-", "xoxr-", "xoxs-", "xoxe-",
	"AKIA", "ASIA", "AIza", "npm_", "hf_",
}

const minPrefixedRun = 10

// secretAssignments are the names that, followed by a separator and a value,
// put a credential in the text. Each may have spaces before and after its
// separator. With an equals sign any value that is not empty counts
// (password=x, password= hunter2). With a colon only the names of
// colonAssignments count, and only for a value of at least minColonValue
// bytes that is not letters alone (password: hunter2hunter2, token: ...),
// since a colon is common in a sentence ("password: required at login").
var secretAssignments = []string{
	"password", "passwd", "pwd", "pass", "token", "secret",
	"api_key", "apikey", "api-key", "api key", "access_key", "private_key",
	"client_secret", "auth", "bearer",
}

// colonAssignments are the names of secretAssignments that count with a
// colon as well.
var colonAssignments = map[string]bool{
	"password": true, "passwd": true, "token": true, "secret": true,
	"api_key": true, "apikey": true, "api-key": true, "api key": true,
	"access_key": true, "private_key": true, "client_secret": true,
}

// minColonValue is the shortest value of an assignment with a colon that
// counts.
const minColonValue = 6

// minRun is the length from which a run of key characters is weighed.
const minRun = 32

// SecretShaped reports whether s looks like a credential: the start of a PEM
// block (-----BEGIN), a well-known key prefix at the start of a word, a
// credential assignment with a value (password=..., password: ... with a
// value that is not a plain word), a run of 32 or more hexadecimal digits
// holding both a digit and a letter, or a run of 32 or more key characters
// (letters, digits, "_", "+" and "=") holding a digit and a capital letter
// with the spread of a random string.
//
// A dash and a slash end a run, so ordinary sentences, web addresses and
// paths, hyphenated names (Q3-2026-Marketing-Plan-For-New-Product-Launch)
// and short codes such as a colour #ff8800 pass; so does the phrase "private
// key" without the PEM marker. What it catches is a pasted key and an
// obvious assignment, not every typed password: a password typed alone, as
// a word, passes, and so does a key whose dashes or slashes cut it into runs
// shorter than 32. A bare git commit id (40 hexadecimal digits) is refused,
// as it has the shape of a hexadecimal key.
func SecretShaped(s string) bool {
	if strings.Contains(s, "-----BEGIN") {
		return true
	}
	if hasAssignment(s) {
		return true
	}
	for _, p := range secretPrefixes {
		for i := 0; ; {
			j := strings.Index(s[i:], p)
			if j < 0 {
				break
			}
			at := i + j
			if wordStart(s, at) && tokenRun(s[at+len(p):]) >= minPrefixedRun {
				return true
			}
			i = at + 1
		}
	}
	for _, run := range runs(s, isHex) {
		if len(run) >= minRun && strings.ContainsAny(run, "0123456789") && strings.IndexFunc(run, isLetter) >= 0 {
			return true
		}
	}
	for _, run := range runs(s, isKey) {
		if len(run) >= minRun && strings.ContainsAny(run, "0123456789") &&
			strings.ContainsAny(run, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && entropy(run) >= 3.5 {
			return true
		}
	}
	return false
}

// hasAssignment reports whether s holds a credential assignment: a name of
// secretAssignments at the start of a word, spaces, a separator, spaces and a
// value (see secretAssignments).
func hasAssignment(s string) bool {
	// Only ASCII letters are folded, so that every index of lower is the
	// same byte of s.
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	lower := string(b)
	for _, name := range secretAssignments {
		for i := 0; ; {
			j := strings.Index(lower[i:], name)
			if j < 0 {
				break
			}
			at := i + j
			i = at + 1
			if !wordStart(s, at) {
				continue
			}
			k := skipSpaces(lower, at+len(name))
			if k >= len(lower) || (lower[k] != '=' && (lower[k] != ':' || !colonAssignments[name])) {
				continue
			}
			sep := lower[k]
			k = skipSpaces(lower, k+1)
			end := k
			for end < len(lower) && lower[end] != ' ' {
				end++
			}
			value := s[k:end]
			switch {
			case value == "":
			case sep == '=':
				return true
			case len(value) >= minColonValue && strings.IndexFunc(value, func(r rune) bool { return !isLetter(r) }) >= 0:
				return true
			}
		}
	}
	return false
}

// skipSpaces returns the index of the first byte of s at or after i that is
// not a space.
func skipSpaces(s string, i int) int {
	for i < len(s) && s[i] == ' ' {
		i++
	}
	return i
}

// wordStart reports whether the byte at i starts a word: it is the first
// byte, or the byte before it is not a letter or a digit.
func wordStart(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !(r < utf8.RuneSelf && (isLetter(r) || (r >= '0' && r <= '9')))
}

// tokenRun returns how many token characters s starts with.
func tokenRun(s string) int {
	n := 0
	for n < len(s) && isToken(rune(s[n])) {
		n++
	}
	return n
}

// runs returns the maximal runs of bytes in s for which in holds.
func runs(s string, in func(rune) bool) []string {
	var out []string
	start := -1
	for i := 0; i <= len(s); i++ {
		if i < len(s) && in(rune(s[i])) {
			if start < 0 {
				start = i
			}
			continue
		}
		if start >= 0 {
			out = append(out, s[start:i])
			start = -1
		}
	}
	return out
}

// entropy returns the Shannon entropy of the bytes of s in bits per byte.
func entropy(s string) float64 {
	var counts [256]int
	for i := 0; i < len(s); i++ {
		counts[s[i]]++
	}
	h := 0.0
	for _, c := range counts {
		if c > 0 {
			p := float64(c) / float64(len(s))
			h -= p * math.Log2(p)
		}
	}
	return h
}

func isLetter(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// isToken is a character of the body of a token after a well-known prefix:
// the formats use dashes and underscores (xoxb-123-abc).
func isToken(r rune) bool {
	return isLetter(r) || (r >= '0' && r <= '9') || r == '_' || r == '-'
}

// isKey is a character of a run that is weighed as a random key: letters,
// digits, "_", "+" and "=". A dash and a slash are not: they join the words
// of a name or the parts of an address or a path.
func isKey(r rune) bool {
	return isLetter(r) || (r >= '0' && r <= '9') || r == '_' || r == '+' || r == '='
}
