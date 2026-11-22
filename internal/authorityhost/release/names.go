package release

import (
	"regexp"
	"strings"
)

// Limits on what a release holds, the switch's own (see the README, "Limits").
const (
	// MaxEntries bounds the folders and files of a release.
	MaxEntries = 20000
	// MaxFileBytes bounds one file.
	MaxFileBytes = 512 << 20
	// MaxTotalBytes bounds the bytes of all files.
	MaxTotalBytes = 2 << 30
	// maxDepth bounds the folders below the top of a release.
	maxDepth = 16
	// maxSmallFile bounds the files read whole: the sums, the manifest, the
	// lists and the units.
	maxSmallFile = 4 << 20
	// maxPath bounds a path inside a release.
	maxPath = 512
)

// namePattern is the bytes of a name inside a release: letters, digits and
// ._@+-, which covers unit names (foo@.service) and every file this tree
// ships. A newline, a space, a quote, a backslash or a control byte is never
// in one, so a name can be printed and listed in SHA256SUMS as it is.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9._@+-]{1,128}$`)

// validName reports whether name is one element of a path inside a release.
func validName(name string) bool {
	return name != "." && name != ".." && namePattern.MatchString(name)
}

// validPath reports whether p is a clean relative path of valid names.
func validPath(p string) bool {
	if p == "" || len(p) > maxPath {
		return false
	}
	for _, el := range strings.Split(p, "/") {
		if !validName(el) {
			return false
		}
	}
	return true
}

// shown is p fit to print: itself when it is a valid path, a fixed phrase
// otherwise, so no planted name reaches a line.
func shown(p string) string {
	if validPath(p) {
		return p
	}
	return "(name not shown)"
}

// valuePattern is what the builder accepts in the MANIFEST's built_on and
// go_version: letters, digits and ._:+-, 1 to 128 bytes. The switch accepts
// any non-empty text; this is stricter so that a value can never hold a line
// break, a comment mark or padding.
var valuePattern = regexp.MustCompile(`^[A-Za-z0-9._:+-]{1,128}$`)

// commitPattern is the full commit as MANIFEST names it, the switch's own
// limits: lowercase hex, 7 to 64 digits.
var commitPattern = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
