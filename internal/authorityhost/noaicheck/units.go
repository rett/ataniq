package noaicheck

import (
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// Unit-link hit details: a unit link that loops, or whose target climbs
// above the root.
const (
	DetailLoop   = "loop"
	DetailEscape = "escape"
)

// unitTrees are the folders systemd loads units from, system and user;
// every home's .config/systemd/user is one too.
var unitTrees = []string{
	"etc/systemd/system", "run/systemd/system", "usr/lib/systemd/system",
	"etc/systemd/user", "run/systemd/user", "usr/lib/systemd/user",
}

// unitDirs are the folders whose *.wants, *.requires and *.upholds folders
// hold the enabled units, persistent and runtime; every home's
// .config/systemd/user is looked into too.
var unitDirs = []string{"etc/systemd/system", "run/systemd/system", "etc/systemd/user", "run/systemd/user"}

// homeUnitDir is a home's folder of user units.
const homeUnitDir = ".config/systemd/user"

// isDepDir reports whether name is a folder of a unit's dependencies.
func isDepDir(name string) bool {
	return strings.HasSuffix(name, ".wants") || strings.HasSuffix(name, ".requires") || strings.HasSuffix(name, ".upholds")
}

// unitLink is what a unit file entry stands for.
type unitLink int

const (
	unitFile   unitLink = iota // a unit file (target holds it)
	unitMasked                 // a link to /dev/null
	unitNone                   // not there, not a file, or a bad link (reported)
)

// unitTarget resolves the unit file entry e within the root. A link is
// followed to the file it names, in the tree (into the release's
// etc/systemd through current, say); a link that loops or climbs above the
// root is a hit of kind "unit-link", and a link to /dev/null is a mask.
func (c *Checker) unitTarget(e *wentry) (string, unitLink) {
	switch {
	case e.isRegular():
		return e.rel, unitFile
	case !e.isLink():
		return "", unitNone
	case c.isMask(e.rel):
		return "dev/null", unitMasked
	}
	p, err := c.t.resolveStrict(e.rel)
	switch {
	case errors.Is(err, errLoop):
		c.hit(KindUnitLink, show(e.rel), DetailLoop)
		return "", unitNone
	case errors.Is(err, errEscape):
		c.hit(KindUnitLink, show(e.rel), DetailEscape)
		return "", unitNone
	case errors.Is(err, fs.ErrNotExist):
		return "", unitNone // systemd passes over a dangling link too
	case err != nil:
		c.unreadable(e.rel, err)
		return "", unitNone
	case p == "dev/null":
		return p, unitMasked
	}
	return p, unitFile
}

// scanUnitFiles calls scan, once per resolved file, for every unit file and
// drop-in systemd loads from the unit trees and every home's user units: a
// unit at the top of a tree, the units its *.wants, *.requires and
// *.upholds folders link to, and every file of a *.d drop-in folder. A
// link is followed within the root; a masked unit is passed over. A drop-in
// folder that is a link is walked where it leads.
func (c *Checker) scanUnitFiles(scan func(rel string)) {
	trees := slices.Clone(unitTrees)
	for _, home := range c.homeFolders() {
		trees = append(trees, path.Join(home, homeUnitDir))
	}
	done := map[string]bool{}
	file := func(e *wentry) bool {
		if p, k := c.unitTarget(e); k == unitFile && !done[p] {
			done[p] = true
			scan(p)
		}
		return false
	}
	for _, tree := range trees {
		c.walk(tree, 2, func(e *wentry) bool {
			if e.depth == 2 {
				return file(e)
			}
			dep, dropin := isDepDir(e.name), strings.HasSuffix(e.name, ".d")
			switch {
			case e.isDir():
				return dep || dropin
			case e.isLink() && dropin:
				if p, err := c.t.resolveStrict(e.rel); err == nil && !done[p] {
					done[p] = true
					c.walk(e.rel, 1, file)
				}
				return false
			}
			return file(e)
		})
	}
}

// CheckUnits is check 6: an enabled unit (an entry of a *.wants, *.requires
// or *.upholds folder of /etc/systemd/system, /run/systemd/system,
// /etc/systemd/user, /run/systemd/user or a home's .config/systemd/user)
// whose name is not in units.allow is a hit of kind "unit". An instance
// ("getty@tty1.service") is allowed by its own name or by its template's
// ("getty@.service"). An enabled unit that is masked (its entry, or a unit
// of its name at the top of the same folder, links to /dev/null) is not
// enabled: it is the note "unit masked PATH". A link of an enabled unit
// that loops or climbs above the root is a hit of kind "unit-link". An
// empty units.allow allows none; with RequirePolicy it is also the hit
// "policy units.allow empty".
func (c *Checker) CheckUnits() {
	if len(c.lists.UnitsAllow) == 0 && c.RequirePolicy {
		c.notEnforced(FileUnitsAllow, DetailEmpty, "")
	}
	dirs := slices.Clone(unitDirs)
	for _, home := range c.homeFolders() {
		dirs = append(dirs, path.Join(home, homeUnitDir))
	}
	for _, dir := range dirs {
		masks := map[string]bool{}
		var enabled []*wentry
		c.walk(dir, 2, func(e *wentry) bool {
			if e.depth == 1 {
				if e.isDir() && isDepDir(e.name) {
					return true
				}
				if e.isLink() && c.isMask(e.rel) {
					masks[e.name] = true
				}
				return false
			}
			if c.t.b.hold(int64(len(e.rel)) + 96) {
				cp := *e
				enabled = append(enabled, &cp)
			}
			return false
		})
		for _, e := range enabled {
			_, k := c.unitTarget(e)
			switch {
			case k == unitMasked || masks[e.name]:
				c.note("unit masked " + field(show(e.rel)))
			case !c.unitAllowed(e.name):
				c.hit(KindUnit, show(e.rel), "")
			}
		}
	}
}

// isMask reports whether the link rel is a mask: it names /dev/null, as
// systemd reads it, whether or not the tree holds a dev/null.
func (c *Checker) isMask(rel string) bool {
	target, err := c.t.readlink(rel)
	if err != nil {
		return false
	}
	if p, err := c.t.resolveStrict(rel); err == nil && p == "dev/null" {
		return true
	}
	return path.Clean(target) == "/dev/null"
}

func (c *Checker) unitAllowed(name string) bool {
	if slices.Contains(c.lists.UnitsAllow, name) {
		return true
	}
	at := strings.IndexByte(name, '@')
	dot := strings.LastIndexByte(name, '.')
	if at < 0 || dot < at {
		return false
	}
	return slices.Contains(c.lists.UnitsAllow, name[:at+1]+name[dot:])
}
