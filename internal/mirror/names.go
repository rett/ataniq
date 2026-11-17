package mirror

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// ErrBadName is returned for a role, project or branch name that does not
// pass the rules of this package. The error text never holds the name.
var ErrBadName = errors.New("mirror: name is not valid")

// MaxBranchBytes is the longest branch name, in bytes.
const MaxBranchBytes = 255

// maxNameBytes is the longest role or project name.
const maxNameBytes = 63

const (
	headsPrefix = "refs/heads/"
	tagsPrefix  = "refs/tags/"
	leadsPrefix = "refs/leads/"
	leadHeads   = "/heads/"
)

// ValidName reports whether s is a valid role or project name: a lower-case
// letter or digit, then up to 62 lower-case letters, digits or dashes.
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > maxNameBytes {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-' && i > 0:
		default:
			return false
		}
	}
	return true
}

// validRefName applies the rules of "git check-ref-format" to a full
// reference name, without the "--allow-onelevel" and "--refspec-pattern"
// options. It checks, in this order of the git manual:
//
//   - no element begins with a dot or ends with ".lock";
//   - no "..", no "@{", no backslash;
//   - no control byte (below 0x20 or 0x7f), no space and none of "~^:?*[";
//   - no leading or trailing slash and no "//";
//   - no trailing dot;
//   - the name is not the single character "@".
func validRefName(s string) bool {
	if s == "" || s == "@" {
		return false
	}
	if strings.Contains(s, "..") || strings.Contains(s, "@{") {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c == 0x7f {
			return false
		}
		switch c {
		case ' ', '~', '^', ':', '?', '*', '[', '\\':
			return false
		}
	}
	if s[0] == '/' || s[len(s)-1] == '/' || s[len(s)-1] == '.' {
		return false
	}
	for _, el := range strings.Split(s, "/") {
		if el == "" || el[0] == '.' || strings.HasSuffix(el, ".lock") {
			return false
		}
	}
	return true
}

// validShort checks the part of a ref name after its namespace prefix: the
// git rules for the whole ref, the length bound, and UTF-8. The UTF-8 rule is
// stricter than git, which takes any byte from 0x80 up: a name that is not
// UTF-8 cannot be shown or stored as JSON without loss.
func validShort(prefix, s string) bool {
	if len(s) == 0 || len(s) > MaxBranchBytes || !utf8.ValidString(s) {
		return false
	}
	return validRefName(prefix + s)
}

// ValidBranch reports whether s is a branch name that Ataniq accepts. It
// has at most 255 bytes, is UTF-8, and refs/heads/<s> passes the rules of
// "git check-ref-format" (written in Go here and tested against git). Four
// rules are added. A branch does not begin with a dash, so that no program
// reads it as an option. It is not "HEAD" or "@", which both name the
// current branch. It does not begin with "refs/", so that "refs/heads/x" given
// as a branch cannot be mistaken for the full name of another ref.
func ValidBranch(s string) bool {
	if strings.HasPrefix(s, "-") || s == "HEAD" || s == "@" || strings.HasPrefix(s, "refs/") {
		return false
	}
	return validShort(headsPrefix, s)
}

func badName(what string) error { return fmt.Errorf("%w: %s", ErrBadName, what) }

// checkAbsClean checks that p is an absolute, clean path without NUL.
func checkAbsClean(what, p string) error {
	if p == "" || strings.ContainsRune(p, 0) {
		return fmt.Errorf("mirror: %s is empty or holds a NUL", what)
	}
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return fmt.Errorf("mirror: %s must be an absolute, clean path", what)
	}
	if p == "/" {
		return fmt.Errorf("mirror: %s must not be the root folder", what)
	}
	return nil
}

// Path returns the place of a project's mirror: <stateRoot>/mirrors/<project>.git.
// stateRoot must be absolute and clean, and project must pass [ValidName].
func Path(stateRoot, project string) (string, error) {
	if err := checkAbsClean("state root", stateRoot); err != nil {
		return "", err
	}
	if !ValidName(project) {
		return "", badName("project")
	}
	return filepath.Join(stateRoot, "mirrors", project+".git"), nil
}

// LeadTree returns the place of a lead's clone: <stateRoot>/leads/<role>/tree.
// It is the one lead path that owner-side code may name; its parents belong to
// the owner. stateRoot must be absolute and clean, and role must pass
// [ValidName].
func LeadTree(stateRoot, role string) (string, error) {
	if err := checkAbsClean("state root", stateRoot); err != nil {
		return "", err
	}
	if !ValidName(role) {
		return "", badName("role")
	}
	return filepath.Join(stateRoot, "leads", role, "tree"), nil
}

// UpstreamRef returns the full name of an upstream branch: refs/heads/<branch>.
func UpstreamRef(branch string) (string, error) {
	if !ValidBranch(branch) {
		return "", badName("branch")
	}
	return headsPrefix + branch, nil
}

// LeadRef returns the full name of a lead's branch in the mirror:
// refs/leads/<role>/heads/<branch>.
func LeadRef(role, branch string) (string, error) {
	prefix, err := LeadPrefix(role)
	if err != nil {
		return "", err
	}
	if !ValidBranch(branch) {
		return "", badName("branch")
	}
	return prefix + branch, nil
}

// LeadPrefix returns the prefix of all of a lead's branches in the mirror:
// refs/leads/<role>/heads/, with the trailing slash.
func LeadPrefix(role string) (string, error) {
	if !ValidName(role) {
		return "", badName("role")
	}
	return leadsPrefix + role + leadHeads, nil
}

// LeadRefspec returns the refspec that fetches all branches of a lead into
// the lead's namespace: +refs/heads/*:refs/leads/<role>/heads/*. The plus
// sign lets a rebased branch replace the old one; that is safe because the
// namespace belongs to the one lead.
func LeadRefspec(role string) (string, error) {
	prefix, err := LeadPrefix(role)
	if err != nil {
		return "", err
	}
	return "+refs/heads/*:" + prefix + "*", nil
}

// SplitLeadRef splits refs/leads/<role>/heads/<branch> into its role and
// branch. It reports false for anything else, also for a role or branch that
// does not pass the git rules and the bounds ([ValidName]; at most 255
// bytes). The rules that [ValidBranch] adds (a leading dash, "HEAD", "refs/")
// are not applied, because a listing must be able to name every ref that git
// holds.
func SplitLeadRef(ref string) (role, branch string, ok bool) {
	rest, found := strings.CutPrefix(ref, leadsPrefix)
	if !found {
		return "", "", false
	}
	role, rest, found = strings.Cut(rest, "/")
	if !found || !ValidName(role) {
		return "", "", false
	}
	branch, found = strings.CutPrefix(rest, "heads/")
	if !found || !validShort(headsPrefix, branch) {
		return "", "", false
	}
	return role, branch, true
}
