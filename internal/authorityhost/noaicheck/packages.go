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
	DetailMissing    = "missing"     // a database or listing that is not there
	DetailNoName     = "no-name"     // a package entry without a readable name
)

// PathPackages and DetailDB name the incomplete hits of a package database
// the check cannot use: "HIT incomplete packages missing" (no database)
// and "HIT incomplete packages db" (a database that lists no package).
const (
	PathPackages = "packages"
	DetailDB     = "db"
)

// CheckPackages is check 7: every installed package (the %NAME% field of each
// desc file in the pacman database, var/lib/pacman/local) that is named in
// forbidden-packages.list is a hit of kind "package" with detail
// "forbidden"; while packages.allow is not empty, every other package not
// in it is a hit with detail "not-allowed". An empty packages.allow means
// not enforced: the note "packages not enforced" or, with RequirePolicy, the
// hit "policy packages.allow empty".
//
// The database is the evidence of every one of these, and forbidden
// packages are checked whether or not the allowlist is enforced, so a
// database the check cannot use is never a pass and never a finding: a
// missing database is "incomplete packages missing", one that lists no
// package at all (no installed host has none) "incomplete packages db",
// and an entry whose desc names no package "incomplete <entry> no-name".
func (c *Checker) CheckPackages() { c.checkPackages(true) }

// checkPackages is CheckPackages; without allowlist the installed packages
// are checked against forbidden-packages.list only, never against
// packages.allow (whose being empty is still a note or a policy hit): the
// pre phase runs on the host before the switch's package step moves it to
// the candidate's package set, so a package the old host needs and the
// candidate drops is not a hit there. The post and full phases check the
// allowlist.
func (c *Checker) checkPackages(allowlist bool) {
	enforced := len(c.lists.PackagesAllow) > 0
	if !enforced {
		c.notEnforced(FilePackagesAllow, DetailEmpty, NotePackagesNotEnforced)
	}
	enforced = enforced && allowlist
	entries := 0
	_, err := c.walk(pacmanLocal, 1, func(e *wentry) bool {
		if !e.isDir() {
			return false
		}
		entries++
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
			c.hit(KindIncomplete, show(e.rel), DetailNoName)
		case slices.Contains(c.lists.ForbiddenPackages, name):
			c.hit(KindPackage, name, DetailForbidden)
		case enforced && !slices.Contains(c.lists.PackagesAllow, name):
			c.hit(KindPackage, name, DetailNotAllowed)
		}
		return false
	})
	switch {
	case errors.Is(err, fs.ErrNotExist):
		c.hit(KindIncomplete, PathPackages, DetailMissing)
	case err == nil && entries == 0:
		c.hit(KindIncomplete, PathPackages, DetailDB)
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
