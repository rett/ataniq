package noaicheck

import (
	"io/fs"
	"path"
	"slices"
	"strings"
)

// unitDirs are the folders whose *.wants, *.requires and *.upholds folders
// hold the enabled units; every home's .config/systemd/user is looked into
// too.
var unitDirs = []string{"etc/systemd/system", "etc/systemd/user"}

// CheckUnits is check 6: an enabled unit (an entry of a *.wants, *.requires
// or *.upholds folder of /etc/systemd/system, /etc/systemd/user or a home's
// .config/systemd/user) whose name is not in units.allow is a hit of kind
// "unit". An instance ("getty@tty1.service") is allowed by its own name or
// by its template's ("getty@.service"). An empty units.allow allows none;
// with RequirePolicy it is also the hit "policy units.allow empty".
func (c *Checker) CheckUnits() {
	if len(c.lists.UnitsAllow) == 0 && c.RequirePolicy {
		c.notEnforced(FileUnitsAllow, DetailEmpty, "")
	}
	dirs := slices.Clone(unitDirs)
	for _, home := range c.homeFolders() {
		dirs = append(dirs, path.Join(home, ".config/systemd/user"))
	}
	for _, dir := range dirs {
		p, entries, err := c.t.readDir(dir)
		if err != nil {
			c.unreadable(dir, err)
			continue
		}
		for _, e := range entries {
			n := e.Name()
			if !e.IsDir() || !(strings.HasSuffix(n, ".wants") || strings.HasSuffix(n, ".requires") || strings.HasSuffix(n, ".upholds")) {
				continue
			}
			wants := path.Join(p, n)
			c.t.walk(wants, 1, func(rel string, _ fs.DirEntry) {
				if !c.unitAllowed(path.Base(rel)) {
					c.hit(KindUnit, show(rel), "")
				}
			}, c.unreadable)
		}
	}
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
