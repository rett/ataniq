package brief

import (
	"math"
	"strings"
	"unicode/utf8"

	"ataniq/internal/genesis/internal/canon"
)

// Untrusted is free text: the text of an "other" answer, or a note. It is
// data and never instructions. Whoever shows it escapes it, and whoever
// hands it to an agent quotes it as data. It is the only type in a brief
// that carries free text.
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

// secretAssignments are the names that, followed by an equals sign and a
// value, put a credential in the text.
var secretAssignments = []string{
	"password=", "passwd=", "pwd=", "pass=", "token=", "secret=",
	"api_key=", "apikey=", "api-key=", "access_key=", "private_key=",
	"client_secret=", "auth=", "bearer=",
}

// minRun is the length from which a run of key characters is weighed.
const minRun = 32

// SecretShaped reports whether s looks like a credential: a PEM block, a
// well-known key prefix at the start of a word, a credential assignment
// such as password=..., a run of 32 or more hexadecimal digits holding both
// a digit and a letter, or a run of 32 or more base64 characters holding a
// digit and a capital letter with the spread of a random string. Ordinary
// sentences, web addresses and short codes such as a colour #ff8800 pass.
func SecretShaped(s string) bool {
	if strings.Contains(s, "-----BEGIN") || strings.Contains(s, "PRIVATE KEY") {
		return true
	}
	lower := strings.ToLower(s)
	for _, a := range secretAssignments {
		for i := 0; ; {
			j := strings.Index(lower[i:], a)
			if j < 0 {
				break
			}
			at := i + j
			end := at + len(a)
			if wordStart(s, at) && end < len(s) && s[end] != ' ' {
				return true
			}
			i = at + 1
		}
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
	for _, run := range runs(s, isB64) {
		if len(run) >= minRun && strings.ContainsAny(run, "0123456789") &&
			strings.ContainsAny(run, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") && entropy(run) >= 3.5 {
			return true
		}
	}
	return false
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

func isToken(r rune) bool {
	return isLetter(r) || (r >= '0' && r <= '9') || r == '_' || r == '-'
}

func isB64(r rune) bool { return isToken(r) || r == '+' || r == '/' || r == '=' }
