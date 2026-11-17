package execconfig

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Limits of Set.With.
const (
	// MaxPatternBytes is the longest pattern With accepts.
	MaxPatternBytes = 255
	// MaxAdditions is the most project additions a Set holds.
	MaxAdditions = 256
)

// ErrPattern is wrapped by every error that With returns.
var ErrPattern = errors.New("execconfig: invalid pattern")

// compiled is a pattern split into lower-case elements. A trailing "/" in
// the pattern is dropped: "name/" and "name" match the same paths.
type compiled struct {
	elems    []string
	anchored bool
}

// compile parses a pattern. It is strict: it is used for built-in rules and
// for project additions alike.
func compile(p string) (compiled, error) {
	var c compiled
	switch {
	case p == "":
		return c, fmt.Errorf("%w: empty", ErrPattern)
	case len(p) > MaxPatternBytes:
		return c, fmt.Errorf("%w: %d bytes, the limit is %d", ErrPattern, len(p), MaxPatternBytes)
	case strings.ContainsRune(p, 0):
		return c, fmt.Errorf("%w: %q contains NUL", ErrPattern, p)
	case strings.ContainsAny(p, "\n\r"):
		return c, fmt.Errorf("%w: %q contains a line break", ErrPattern, p)
	case strings.Contains(p, `\`):
		return c, fmt.Errorf("%w: %q contains a backslash", ErrPattern, p)
	case strings.ContainsAny(p, "[]"):
		return c, fmt.Errorf("%w: %q contains a bracket", ErrPattern, p)
	}
	body := p
	if strings.HasPrefix(body, "/") {
		c.anchored = true
		body = body[1:]
	}
	if strings.HasSuffix(body, "/") {
		body = body[:len(body)-1]
	}
	if body == "" {
		return c, fmt.Errorf("%w: %q names no element", ErrPattern, p)
	}
	for _, e := range strings.Split(body, "/") {
		switch e {
		case "":
			return c, fmt.Errorf("%w: %q has an empty element", ErrPattern, p)
		case ".", "..":
			return c, fmt.Errorf("%w: %q has a %q element", ErrPattern, p, e)
		}
		if strings.TrimRight(e, ". ") != e {
			// A path element is matched without its trailing dots and
			// spaces, so a pattern element that ends in one never matches.
			return c, fmt.Errorf("%w: %q has an element ending in a dot or a space", ErrPattern, p)
		}
		c.elems = append(c.elems, lower(e))
	}
	return c, nil
}

// lower folds ASCII letters only.
func lower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// target is a path prepared for matching.
type target struct {
	cands [][]string // per element, the names it may match as
	odd   bool       // anchoring is ignored
}

func prepare(path string) target {
	var t target
	parts := strings.Split(path, "/")
	for i, e := range parts {
		switch {
		case e == "":
			if i != len(parts)-1 {
				t.odd = true // a leading "/" or an empty element inside
			}
			continue
		case e == "." || e == "..":
			t.odd = true
		}
		t.cands = append(t.cands, candidates(e))
	}
	return t
}

// candidates returns the names an element is matched as: its lower-case
// form, the form without trailing dots and spaces, and ".git" for "git~1".
func candidates(e string) []string {
	l := lower(e)
	out := []string{l}
	if s := strings.TrimRight(l, ". "); s != "" && s != l {
		out = append(out, s)
	}
	for _, c := range out {
		if c == "git~1" {
			out = append(out, ".git")
			break
		}
	}
	return out
}

func (c compiled) matches(t target) bool {
	n, k := len(t.cands), len(c.elems)
	if k > n {
		return false
	}
	last := n - k
	if c.anchored && !t.odd {
		last = 0
	}
	for i := 0; i <= last; i++ {
		if c.runAt(t, i) {
			return true
		}
	}
	return false
}

func (c compiled) runAt(t target, i int) bool {
	for j, pe := range c.elems {
		ok := false
		for _, name := range t.cands[i+j] {
			if glob(pe, name) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// glob matches name against pat with "*" (any run of bytes) and "?" (one
// character; one byte when the bytes are not UTF-8). Every other byte of pat
// must equal the byte of name.
func glob(pat, name string) bool {
	px, nx := 0, 0
	starP, starN := -1, 0
	for nx < len(name) {
		switch {
		case px < len(pat) && pat[px] == '*':
			starP, starN = px, nx
			px++
		case px < len(pat) && pat[px] == '?':
			_, w := utf8.DecodeRuneInString(name[nx:])
			px++
			nx += w
		case px < len(pat) && pat[px] == name[nx]:
			px++
			nx++
		case starP >= 0:
			_, w := utf8.DecodeRuneInString(name[starN:])
			starN += w
			px, nx = starP+1, starN
		default:
			return false
		}
	}
	for px < len(pat) && pat[px] == '*' {
		px++
	}
	return px == len(pat)
}
