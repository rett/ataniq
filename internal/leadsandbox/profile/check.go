package profile

import (
	"path"
	"strings"
)

// Bounds on every string and list that comes from the caller.
const (
	maxPathLen    = 4096
	maxValueLen   = 256
	maxVersionLen = 64
	maxNameLen    = 63
	maxExtraEnv   = 64
	maxExtras     = 64
	maxNever      = 256
)

// ValidName reports whether s is a role or project name: [a-z0-9][a-z0-9-]{0,62}.
func ValidName(s string) bool {
	if len(s) == 0 || len(s) > maxNameLen {
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

// CheckName refuses a role or project name that is not ValidName. field names
// the input in the refusal.
func CheckName(field, s string) error {
	if !ValidName(s) {
		return refuse(field, "must match [a-z0-9][a-z0-9-]{0,62}")
	}
	return nil
}

// CheckPath refuses a path that is empty, relative, not in clean form, longer
// than 4096 bytes, or that holds a control character. It is lexical.
func CheckPath(field, p string) error {
	switch {
	case p == "":
		return refuse(field, "is empty")
	case len(p) > maxPathLen:
		return refuse(field, "is longer than %d bytes", maxPathLen)
	case hasControl(p):
		return refuse(field, "holds a control character")
	case !path.IsAbs(p):
		return refuse(field, "must be an absolute path")
	case path.Clean(p) != p:
		return refuse(field, "must be a clean path (no '.', '..', empty or trailing element)")
	}
	return nil
}

// CheckValue refuses an environment value that holds a NUL, a newline or any
// other control character, or that is longer than 256 bytes.
func CheckValue(field, v string) error {
	switch {
	case len(v) > maxValueLen:
		return refuse(field, "is longer than %d bytes", maxValueLen)
	case hasControl(v):
		return refuse(field, "holds a NUL, newline or other control character")
	}
	return nil
}

func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// related reports whether a and b are the same path or one lies below the
// other. Both must be clean absolute paths.
func related(a, b string) bool {
	return a == b || below(a, b) || below(b, a)
}

// below reports whether p lies strictly below dir. Both must be clean absolute
// paths.
func below(p, dir string) bool {
	if dir == "/" {
		return p != "/"
	}
	return strings.HasPrefix(p, dir+"/")
}
