package noaicheck

import (
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// pacmanLocal is the pacman database of installed packages.
const pacmanLocal = "var/lib/pacman/local"

// Package hit details.
const (
	DetailForbidden  = "forbidden"   // named in forbidden-packages.list
	DetailNotAllowed = "not-allowed" // not in a non-empty packages.allow
	DetailMissing    = "missing"     // the database is not there to enforce the allowlist
	DetailNoName     = "no-name"     // a package entry without a readable name
)

// CheckPackages is check 7: every installed package (the %NAME% field of each
// desc file in the pacman database, var/lib/pacman/local) that is named in
// forbidden-packages.list is a hit of kind "package" with detail
// "forbidden"; while packages.allow is not empty, every other package not
// in it is a hit with detail "not-allowed". An empty packages.allow means
// not enforced: the note "packages not enforced" or, with RequirePolicy, the
// hit "policy packages.allow empty". A missing database is a hit while the
// allowlist is enforced.
func (c *Checker) CheckPackages() {
	enforced := len(c.lists.PackagesAllow) > 0
	if !enforced {
		c.notEnforced(FilePackagesAllow, DetailEmpty, NotePackagesNotEnforced)
	}
	_, err := c.walk(pacmanLocal, 1, func(e *wentry) bool {
		if !e.isDir() {
			return false
		}
		desc := path.Join(e.rel, "desc")
		_, data, err := c.t.readFile(desc, 1<<20)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				err = errors.New("no desc file")
			}
			c.unreadable(desc, err)
			return false
		}
		name := descName(string(data))
		switch {
		case name == "":
			c.hit(KindPackage, show(e.rel), DetailNoName)
		case slices.Contains(c.lists.ForbiddenPackages, name):
			c.hit(KindPackage, name, DetailForbidden)
		case enforced && !slices.Contains(c.lists.PackagesAllow, name):
			c.hit(KindPackage, name, DetailNotAllowed)
		}
		return false
	})
	if errors.Is(err, fs.ErrNotExist) && enforced {
		c.hit(KindPackage, show(pacmanLocal), DetailMissing)
	}
}

// descName returns the line after %NAME% in a pacman desc file, or "".
func descName(data string) string {
	lines := strings.Split(data, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "%NAME%" && i+1 < len(lines) {
			return strings.TrimSpace(lines[i+1])
		}
	}
	return ""
}
