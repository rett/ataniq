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

// manager is a systemd manager whose units the check reads: the system
// manager, or the user manager of one user (a home with its user id). It
// is carried with every unit through resolution and deduplication: the
// specifiers of a unit's EnvironmentFile= are those of the manager that
// loads it, whatever the path of the file it is linked to.
type manager struct {
	user bool
	home string // the user's home, relative to the root; "" for the system manager
	uid  string // the user's id; "" for the system manager
}

// key names the manager in a deduplication key.
func (m manager) key() string {
	if !m.user {
		return "system"
	}
	return "user:" + m.uid + ":" + m.home
}

// spec is the value of the specifier %x in the manager's context, the same
// for every unit it loads: %h the home (/root for the system manager), %t
// the runtime folder (/run/user/UID, /run for the system manager), %% a
// percent sign. Any other specifier is not resolved.
func (m manager) spec(x byte) (string, bool) {
	switch x {
	case 'h':
		if m.user {
			return show(m.home), m.home != ""
		}
		return "/root", true
	case 't':
		if m.user {
			return "/run/user/" + m.uid, m.uid != ""
		}
		return "/run", true
	case '%':
		return "%", true
	}
	return "", false
}

// lookupDir is a folder a manager loads units from; enable says its
// *.wants, *.requires and *.upholds folders are enablement (persistent or
// runtime) rather than a vendor's static dependencies or a generator's.
type lookupDir struct {
	rel    string
	enable bool
}

// systemDirs are the system manager's unit folders in systemd's order of
// precedence: a unit of a name in an earlier folder hides one of that name
// in a later folder (/etc over /run over /usr/lib), a mask included.
var systemDirs = []lookupDir{
	{"etc/systemd/system.control", false},
	{"run/systemd/system.control", false},
	{"run/systemd/transient", false},
	{"run/systemd/generator.early", false},
	{"etc/systemd/system", true},
	{"etc/systemd/system.attached", true},
	{"run/systemd/system", true},
	{"run/systemd/system.attached", true},
	{"run/systemd/generator", false},
	{"usr/local/lib/systemd/system", false},
	{"usr/lib/systemd/system", false},
	{"run/systemd/generator.late", false},
}

// homeUnitDir is a home's folder of user units.
const homeUnitDir = ".config/systemd/user"

// dirs are the manager's unit folders in order of precedence; a user
// manager's are those of systemd's user lookup path for its home and
// runtime folder.
func (m manager) dirs() []lookupDir {
	if !m.user {
		return systemDirs
	}
	h := func(p string) string {
		if m.home == "" {
			return ""
		}
		return path.Join(m.home, p)
	}
	run := "run/user/" + m.uid
	if m.uid == "" {
		run = ""
	}
	r := func(p string) string {
		if run == "" {
			return ""
		}
		return path.Join(run, p)
	}
	all := []lookupDir{
		{h(".config/systemd/user.control"), false},
		{r("systemd/user.control"), false},
		{r("systemd/transient"), false},
		{r("systemd/generator.early"), false},
		{h(homeUnitDir), true},
		{"etc/xdg/systemd/user", true},
		{"etc/systemd/user", true},
		{r("systemd/user"), true},
		{"run/systemd/user", true},
		{r("systemd/generator"), false},
		{h(".local/share/systemd/user"), false},
		{"usr/local/share/systemd/user", false},
		{"usr/share/systemd/user", false},
		{"usr/local/lib/systemd/user", false},
		{"usr/lib/systemd/user", false},
		{r("systemd/generator.late"), false},
	}
	return slices.DeleteFunc(all, func(d lookupDir) bool { return d.rel == "" })
}

// managers are the system manager and a user manager for every home with
// its user id (see homeFolders); with no home at all, one user manager
// without a home, so that the global user units are still read.
func (c *Checker) managers() []manager {
	out := []manager{{}}
	for _, h := range c.homeFolders() {
		out = append(out, manager{user: true, home: h, uid: c.uids[h]})
	}
	if len(out) == 1 {
		out = append(out, manager{user: true})
	}
	return out
}

// isDepDir reports whether name is a folder of a unit's dependencies.
func isDepDir(name string) bool {
	return strings.HasSuffix(name, ".wants") || strings.HasSuffix(name, ".requires") || strings.HasSuffix(name, ".upholds")
}

// isDropinDir reports whether name is a unit's drop-in folder.
func isDropinDir(name string) bool { return strings.HasSuffix(name, ".d") }

// uent is an entry of a unit folder: its path (in the folder it was listed
// in, links in the way resolved) and name, and whether it is a link.
type uent struct {
	rel  string
	name string
	link bool
}

// udir is a unit folder as listed once per run: its unit entries in name
// order, and the entries of each of its dependency and drop-in folders
// (one that is a link within the root is listed where it leads).
type udir struct {
	units  map[string]*uent
	order  []string
	subs   map[string][]*uent
	sorder []string
}

// unitDir lists the unit folder rel, once per run.
func (c *Checker) unitDir(rel string) *udir {
	if d, ok := c.udirs[rel]; ok {
		return d
	}
	d := &udir{units: map[string]*uent{}, subs: map[string][]*uent{}}
	c.udirs[rel] = d
	keep := func(e *wentry) *uent {
		if !c.t.b.hold(int64(len(e.rel)) + 96) {
			return nil
		}
		return &uent{rel: e.rel, name: e.name, link: e.isLink()}
	}
	var subs []*wentry
	c.walk(rel, 1, func(e *wentry) bool {
		sub := isDepDir(e.name) || isDropinDir(e.name)
		switch {
		case sub && (e.isDir() || e.isLink()):
			cp := *e
			subs = append(subs, &cp)
		case e.isRegular() || e.isLink():
			if u := keep(e); u != nil {
				d.units[e.name] = u
				d.order = append(d.order, e.name)
			}
		}
		return false
	})
	for _, s := range subs {
		start := s.rel
		if s.isLink() {
			p, err := c.t.resolveStrict(s.rel)
			switch {
			case errors.Is(err, errLoop):
				c.hit(KindUnitLink, show(s.rel), DetailLoop)
				continue
			case errors.Is(err, errEscape):
				c.hit(KindUnitLink, show(s.rel), DetailEscape)
				continue
			case errors.Is(err, fs.ErrNotExist):
				continue
			case err != nil:
				c.unreadable(s.rel, err)
				continue
			}
			start = p
		}
		var list []*uent
		c.walk(start, 1, func(e *wentry) bool {
			if e.isRegular() || e.isLink() {
				if u := keep(e); u != nil {
					list = append(list, u)
				}
			}
			return false
		})
		if _, ok := d.subs[s.name]; !ok {
			d.sorder = append(d.sorder, s.name)
		}
		d.subs[s.name] = append(d.subs[s.name], list...)
	}
	return d
}

// view is one manager's effective unit view: its unit folders, listed, in
// order of precedence.
type view struct {
	m    manager
	dirs []*udir
	look []lookupDir
}

func (c *Checker) view(m manager) *view {
	v := &view{m: m, look: m.dirs()}
	for _, d := range v.look {
		v.dirs = append(v.dirs, c.unitDir(d.rel))
	}
	return v
}

// lookup returns the entry systemd loads for the unit name: the first of
// that name in the folders in order of precedence, and for an instance
// ("getty@tty1.service") without one of its own, its template's
// ("getty@.service").
func (v *view) lookup(name string) *uent {
	for _, d := range v.dirs {
		if e := d.units[name]; e != nil {
			return e
		}
	}
	if t := template(name); t != "" {
		return v.lookup(t)
	}
	return nil
}

// template is the template of an instance name, or "".
func template(name string) string {
	at := strings.IndexByte(name, '@')
	dot := strings.LastIndexByte(name, '.')
	if at < 0 || dot < at || at+1 == dot {
		return ""
	}
	return name[:at+1] + name[dot:]
}

// masked reports whether the unit name is masked in the view: the entry
// systemd loads for it is a link to /dev/null. A mask in a folder of
// higher precedence hides a unit of the name in every lower one; one in a
// lower folder is itself hidden.
func (c *Checker) masked(v *view, name string) bool {
	e := v.lookup(name)
	return e != nil && e.link && c.isMask(e.rel)
}

// unitTarget resolves the unit file entry rel within the root. A link is
// followed to the file it names, in the tree (into the release's
// etc/systemd through current, say); a link that loops or climbs above the
// root is a hit of kind "unit-link", and a link to /dev/null is a mask.
func (c *Checker) unitTarget(e *uent) (string, unitLink) {
	switch {
	case !e.link:
		return e.rel, unitFile
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

// unitLink is what a unit file entry stands for.
type unitLink int

const (
	unitFile   unitLink = iota // a unit file (target holds it)
	unitMasked                 // a link to /dev/null
	unitNone                   // not there, not a file, or a bad link (reported)
)

// scanUnitFiles calls scan, once per resolved file and manager, for every
// unit file and drop-in a manager loads, in each manager's effective unit
// view (see view): the unit systemd loads for each name (the first of it
// in the folders in order of precedence, not every one), the drop-ins of
// every drop-in folder (a file name in a folder of higher precedence
// hiding the same name in a lower one), and the units the dependency
// folders link to. A masked unit, its drop-ins and a masked drop-in are
// passed over. A link is followed within the root; a dependency or drop-in
// folder that is a link is listed where it leads.
func (c *Checker) scanUnitFiles(scan func(rel string, m manager)) {
	done := map[string]bool{}
	file := func(e *uent, m manager) {
		if p, k := c.unitTarget(e); k == unitFile && !done[p+"\x00"+m.key()] {
			done[p+"\x00"+m.key()] = true
			scan(p, m)
		}
	}
	for _, m := range c.managers() {
		v := c.view(m)
		seen := map[string]bool{}
		for _, d := range v.dirs {
			for _, name := range d.order {
				if seen[name] {
					continue
				}
				seen[name] = true
				if !c.masked(v, name) {
					file(d.units[name], m)
				}
			}
		}
		seenSub := map[string]bool{}
		for _, d := range v.dirs {
			for _, sub := range d.sorder {
				for _, e := range d.subs[sub] {
					if isDropinDir(sub) {
						unit := strings.TrimSuffix(sub, ".d")
						key := sub + "/" + e.name
						if seenSub[key] {
							continue
						}
						seenSub[key] = true
						if c.masked(v, unit) || (e.link && c.isMask(e.rel)) {
							continue
						}
					} else if c.masked(v, e.name) {
						continue
					}
					file(e, m)
				}
			}
		}
	}
}

// CheckUnits is check 6: an enabled unit not in units.allow is a hit of
// kind "unit". An enabled unit is an entry of a *.wants, *.requires or
// *.upholds folder (one that is a link within the root included) of a
// folder of enablement: /etc/systemd/system, /etc/systemd/system.attached,
// /run/systemd/system, /run/systemd/system.attached for the system
// manager; a home's .config/systemd/user, /etc/xdg/systemd/user,
// /etc/systemd/user, /run/user/UID/systemd/user and /run/systemd/user for
// a user's. An instance ("getty@tty1.service") is allowed by its own name
// or by its template's ("getty@.service"). An enabled unit that is masked
// (its entry links to /dev/null, or the unit systemd loads for its name in
// the manager's effective view is a mask) is not enabled: it is the note
// "unit masked PATH". A link of an enabled unit that loops or climbs above
// the root is a hit of kind "unit-link". An empty units.allow allows none;
// with RequirePolicy it is also the hit "policy units.allow empty".
func (c *Checker) CheckUnits() {
	if len(c.lists.UnitsAllow) == 0 && c.RequirePolicy {
		c.notEnforced(FileUnitsAllow, DetailEmpty, "")
	}
	for _, m := range c.managers() {
		v := c.view(m)
		for i, d := range v.dirs {
			if !v.look[i].enable {
				continue
			}
			for _, sub := range d.sorder {
				if !isDepDir(sub) {
					continue
				}
				for _, e := range d.subs[sub] {
					_, k := c.unitTarget(e)
					switch {
					case k == unitMasked || c.masked(v, e.name):
						c.note("unit masked " + field(show(e.rel)))
					case !c.unitAllowed(e.name):
						c.hit(KindUnit, show(e.rel), "")
					}
				}
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
	t := template(name)
	return t != "" && slices.Contains(c.lists.UnitsAllow, t)
}
