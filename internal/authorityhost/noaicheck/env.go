package noaicheck

import (
	"errors"
	"io/fs"
	"path"
	"strings"
	"syscall"
)

// maxEnvFile bounds an environment file, profile or unit file; a larger one
// is a hit of kind "unreadable".
const maxEnvFile = 4 << 20

// PathEnvSyntax is the name of the incomplete hit for a file whose syntax
// the check cannot parse: "HIT incomplete env-syntax <file>".
const PathEnvSyntax = "env-syntax"

// PathEnvSource is the name of the incomplete hit for a profile that
// sources a file by a path the check cannot resolve:
// "HIT incomplete env-source <file>".
const PathEnvSource = "env-source"

// envFile is a file that sets variables, and the syntax it is read in.
type envFile struct {
	rel    string
	syntax envSyntax
}

// envFiles are the system files that set variables for every login or
// service.
var envFiles = []envFile{
	{"etc/environment", syntaxEnvFile},
	{"etc/profile", syntaxShell}, {"etc/bash.bashrc", syntaxShell},
	{"etc/zsh/zshenv", syntaxShell}, {"etc/zsh/zprofile", syntaxShell},
	{"etc/zsh/zshrc", syntaxShell}, {"etc/zsh/zlogin", syntaxShell},
	{"etc/systemd/system.conf", syntaxSystemd}, {"etc/systemd/user.conf", syntaxSystemd},
}

// envDirs are folders whose regular files set variables, one level deep.
var envDirs = []envFile{
	{"etc/profile.d", syntaxShell},
	{"etc/environment.d", syntaxEnvFile},
	{"usr/lib/environment.d", syntaxEnvFile},
	{"etc/systemd/system.conf.d", syntaxSystemd},
	{"etc/systemd/user.conf.d", syntaxSystemd},
}

// homeEnvFiles are a home's shell profiles and environment files, and
// homeEnvDirs its folders of them.
var (
	homeEnvFiles = []envFile{
		{".profile", syntaxShell}, {".bash_profile", syntaxShell}, {".bash_login", syntaxShell},
		{".bashrc", syntaxShell}, {".zshenv", syntaxShell}, {".zprofile", syntaxShell},
		{".zshrc", syntaxShell}, {".zlogin", syntaxShell}, {".pam_environment", syntaxPam},
		{".config/fish/config.fish", syntaxShell},
	}
	homeEnvDirs = []envFile{
		{".config/environment.d", syntaxEnvFile},
		{".config/fish/conf.d", syntaxShell},
	}
)

// ProviderKeyName reports whether name is named like a provider key:
// *_API_KEY, *_AUTH_TOKEN, ANTHROPIC_*, OPENAI_*, GEMINI_*, CLAUDE_* or
// CODEX_*, in any case.
func ProviderKeyName(name string) bool {
	n := strings.ToUpper(name)
	if strings.HasSuffix(n, "_API_KEY") || strings.HasSuffix(n, "_AUTH_TOKEN") {
		return true
	}
	for _, p := range []string{"ANTHROPIC_", "OPENAI_", "GEMINI_", "CLAUDE_", "CODEX_"} {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	return false
}

// PathEnvFile is the name of the incomplete hit for a required
// EnvironmentFile= (one without a leading '-') that is not there, or a
// required glob that matches nothing: "HIT incomplete env-file <path>".
// systemd would not start the unit, and the check cannot know what the
// file would set once it is there.
const PathEnvFile = "env-file"

// CheckEnv is check 4: an environment file, shell profile, systemd manager
// configuration or unit file that sets or passes a variable named like a
// provider key (see ProviderKeyName) is a hit of kind "env", with the
// variable's name as its detail; the value is never read into the report.
// The files are those of /etc/environment, /etc/profile and the shells'
// system profiles, /etc/profile.d, the environment.d folders, the systemd
// manager configuration, every unit file and drop-in a manager loads (see
// scanUnitFiles: each manager's effective unit view, links into the
// release followed within the root), every home's profiles, each file an
// EnvironmentFile= line of those units names (its %h, %t and %% those of
// the manager that loads the unit; a glob in its last component matched
// within the root; the lines of a unit and its drop-ins evaluated in
// systemd's order first, an empty EnvironmentFile= dropping those before
// it) and each file a profile sources (see startup and sourcePaths: a
// literal absolute path, or one that starts with $HOME, ~ or
// $XDG_CONFIG_HOME, resolved in each home; a path the check cannot resolve
// is the hit "incomplete env-source <file>", never passed over, except
// in a function body, where it is the note "deferred-source <file>"). A
// required EnvironmentFile= that is not there is the hit
// "incomplete env-file <path>"; an optional one ('-') is passed over.
//
// Each file is read in its own syntax (see parseEnv): a profile as shell
// words, so a comment or a quoted value is never taken for a setting; a
// unit by its Environment=, PassEnvironment= and EnvironmentFile= keys; an
// environment file as KEY=VALUE lines. Each file is read and parsed once,
// its EnvironmentFile= lines resolved once per manager. Only a valid
// identifier is ever reported. A file whose syntax the check cannot parse
// is the hit "incomplete env-syntax <file>".
func (c *Checker) CheckEnv() {
	c.reportStartup()
	s := c.newEnvScan()
	for _, f := range envFiles {
		if f.syntax == syntaxSystemd {
			s.scan(f.rel, f.syntax)
		}
	}
	for _, d := range envDirs {
		if d.syntax != syntaxSystemd {
			continue
		}
		c.walk(d.rel, 1, func(e *wentry) bool {
			if e.isRegular() || e.isLink() {
				s.scan(e.rel, d.syntax)
			}
			return false
		})
	}
	c.scanUnitFiles(s.unit)
	s.drain()
}

// envScan reads the systemd manager configuration, the units and the
// environment files they name: each once (per manager, for a unit),
// following the files they name through a queue. The startup files and
// what they source are read by the startup scan (see startup).
type envScan struct {
	c     *Checker
	done  map[string]bool
	queue []envFile
}

func (c *Checker) newEnvScan() *envScan { return &envScan{c: c, done: map[string]bool{}} }

// push queues a file. Each queued file counts as an entry and holds its
// memory until it is read, as a walk's names do; once the budget is spent
// no more is queued and the report ends with "incomplete budget".
func (s *envScan) push(rel string, syn envSyntax) {
	if s.c.t.b.entry() && s.c.t.b.hold(queueCost(rel)) {
		s.queue = append(s.queue, envFile{rel: rel, syntax: syn})
	}
}

// drain reads the queued files, and those they queue; done ends a cycle.
func (s *envScan) drain() {
	for len(s.queue) > 0 {
		f := s.queue[0]
		s.queue = s.queue[1:]
		s.scan(f.rel, f.syntax)
		s.c.t.b.release(queueCost(f.rel))
	}
}

// scan reads the file rel of the root in the syntax syn (see parse),
// and queues the files its EnvironmentFile= lines name in the system
// manager's context (a manager configuration file); once per file.
func (s *envScan) scan(rel string, syn envSyntax) {
	p, r := s.parse(rel, syn)
	if r == nil || s.done[p] {
		return
	}
	s.done[p] = true
	s.envFiles(show(p), r.files, manager{})
}

// unit reads the files systemd reads for one unit in the manager m, in
// its order (the unit file, then its drop-ins; see scanUnitFiles), once
// per manager and set of files. Each file's names are reported as it is
// read; then its EnvironmentFile= lines are evaluated in order before any
// of the files they name is opened: an empty EnvironmentFile= drops every
// one before it, in its own file or an earlier one, as systemd does, and
// only those left are queued.
func (s *envScan) unit(files []string, m manager) {
	key := m.key() + "\x00" + strings.Join(files, "\x00")
	if s.done[key] || !s.c.t.b.hold(int64(len(key))+32) {
		return
	}
	s.done[key] = true
	type ref struct {
		envRef
		unit string // the file that names it, as shown
	}
	var refs []ref
	for _, f := range files {
		p, r := s.parse(f, syntaxSystemd)
		if r == nil {
			continue
		}
		if r.reset {
			refs = refs[:0]
		}
		for _, x := range r.files {
			refs = append(refs, ref{x, show(p)})
		}
	}
	for _, x := range refs {
		s.envFiles(x.unit, []envRef{x.envRef}, m)
	}
}

// parse resolves the file rel of the root and returns its resolved path
// and what it sets in the syntax syn, read and parsed once per run: its
// provider-key names are reported, and its syntax trouble too, on the
// first read. A file that cannot be read is reported and nil; so is a
// masked file (a link to /dev/null), which is empty.
func (s *envScan) parse(rel string, syn envSyntax) (string, *envParse) {
	c := s.c
	p, err := c.t.resolve(rel)
	if err != nil {
		c.unreadable(rel, err)
		return "", nil
	}
	if p == "dev/null" {
		return p, nil
	}
	if r := c.parsed[p]; r != nil {
		return p, r
	}
	_, data, err := c.t.readFile(p, maxEnvFile)
	if err != nil {
		c.unreadable(p, err)
		return p, nil
	}
	r := &envParse{}
	*r = parseEnv(syn, string(data), strings.HasSuffix(p, ".fish"))
	if c.t.b.hold(int64(len(p)) + r.cost()) {
		c.parsed[p] = r
	}
	for _, n := range r.names {
		c.hit(KindEnv, show(p), n)
	}
	if r.bad {
		c.hit(KindIncomplete, PathEnvSyntax, show(p))
	}
	return p, r
}

// envFiles queues the files a unit's EnvironmentFile= lines name, in the
// manager m (the unit shown as unit): each path's specifiers replaced in
// m's context and a glob in its last component matched within the root. A
// path that cannot be resolved is "incomplete env-syntax <unit>", a
// required file that is not there "incomplete env-file <path>".
func (s *envScan) envFiles(unit string, files []envRef, m manager) {
	c := s.c
	for _, ref := range files {
		f, held, err := c.expand(ref.path, m)
		switch {
		case errors.Is(err, errBudget):
			return
		case err != nil:
			c.hit(KindIncomplete, PathEnvSyntax, unit)
			continue
		}
		n := 0
		globbed := c.envFileGlob(f, func(rel string) { n++; s.push(rel, syntaxEnvFile) })
		switch {
		case !globbed:
			c.hit(KindIncomplete, PathEnvSyntax, unit)
		case !ref.optional && (n == 0 || c.missing(f)):
			c.hit(KindIncomplete, PathEnvFile, show(clean(f)))
		}
		c.t.b.release(held)
	}
}

// queueCost is the memory a queued file holds.
func queueCost(rel string) int64 { return int64(len(rel)) + 48 }

// errSpecifier is returned by expand for a specifier it cannot resolve.
var errSpecifier = errors.New("specifier not resolved")

// missing reports whether the path p (a file an EnvironmentFile= names,
// not a glob) is not there within the root.
func (c *Checker) missing(p string) bool {
	if strings.ContainsAny(path.Base(p), "*?[") {
		return false
	}
	_, err := c.t.resolve(p)
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR)
}

// expand returns the path an EnvironmentFile= path stands for in the
// manager m: each specifier replaced by its value in m's context (see
// manager.spec, one value per specifier, the same table for every unit of
// the manager), so a path is never multiplied out. The length of the
// result is reckoned and held against the run's memory budget before it is
// built; the caller releases held once it is done with the path. A
// specifier m cannot resolve is errSpecifier, a budget spent errBudget.
func (c *Checker) expand(p string, m manager) (string, int64, error) {
	if !strings.Contains(p, "%") {
		return p, 0, nil
	}
	n := int64(0)
	for i := 0; i < len(p); i++ {
		if p[i] != '%' {
			n++
			continue
		}
		if i+1 >= len(p) {
			return "", 0, errSpecifier
		}
		v, ok := m.spec(p[i+1])
		if !ok {
			return "", 0, errSpecifier
		}
		n += int64(len(v))
		i++
	}
	if c.t.b.out() || !c.t.b.hold(n) {
		return "", 0, errBudget
	}
	var b strings.Builder
	b.Grow(int(n))
	for i := 0; i < len(p); i++ {
		if p[i] != '%' {
			b.WriteByte(p[i])
			continue
		}
		v, _ := m.spec(p[i+1])
		b.WriteString(v)
		i++
	}
	return b.String(), n, nil
}

// envFileGlob passes the path of an EnvironmentFile= to found, or, when
// its last component is a glob, every entry of its folder that matches.
// A glob in a folder's name, or a pattern that is not valid, cannot be
// resolved: false.
func (c *Checker) envFileGlob(p string, found func(rel string)) bool {
	rel := clean(p)
	dir, base := path.Split(rel)
	if strings.ContainsAny(dir, "*?[") {
		return false
	}
	if !strings.ContainsAny(base, "*?[") {
		found(rel)
		return true
	}
	if _, err := path.Match(base, ""); err != nil {
		return false
	}
	c.walk(dir, 1, func(e *wentry) bool {
		if ok, _ := path.Match(base, e.name); ok && !strings.HasPrefix(e.name, ".") {
			found(e.rel)
		}
		return false
	})
	return true
}

// homeOf returns the home folder p is in, or "" when it is in none: a
// file a home's profile sources from that home is resolved for that home
// alone, any other for every home.
func (c *Checker) homeOf(p string) string {
	best := ""
	for _, h := range c.homeFolders() {
		if (p == h || strings.HasPrefix(p, h+"/")) && len(h) > len(best) {
			best = h
		}
	}
	return best
}

// homeForms are the spellings of a home-relative path a sourced word may
// start with, longest first, and the folder below the home each stands
// for: $HOME, ${HOME}, and $XDG_CONFIG_HOME in its usual forms (its
// default, the home's .config, and every folder a startup file gives it
// for the home, see startup).
var homeForms = []struct{ form, sub string }{
	{"${XDG_CONFIG_HOME:-$HOME/.config}", ".config"},
	{"${XDG_CONFIG_HOME-$HOME/.config}", ".config"},
	{"${XDG_CONFIG_HOME:-${HOME}/.config}", ".config"},
	{"${XDG_CONFIG_HOME-${HOME}/.config}", ".config"},
	{"${XDG_CONFIG_HOME}", ".config"},
	{"$XDG_CONFIG_HOME", ".config"},
	{"${HOME}", ""},
	{"$HOME", ""},
}

// sourcePaths returns the paths, clean and relative to the root, that the
// path word w of a "." or "source" command (or a PATH element, or a value
// of XDG_CONFIG_HOME) stands for, and whether it could resolve it. A word
// without an expansion must be an absolute path or start with "~/". A
// word with an expansion must start with one of homeForms and hold no
// other: it is resolved in home, or in every home when home is "", a
// $XDG_CONFIG_HOME in the home's .config and in each folder the startup
// files give it (see startup). Anything else (a relative path, another
// variable, a command substitution, "~user") cannot be resolved. The last
// component may be a glob, matched by the caller.
func (c *Checker) sourcePaths(w srcWord, home string) ([]string, bool) {
	lit := w.lit
	sub, rest, relative := "", lit, false
	switch {
	case strings.HasPrefix(lit, "~/") || lit == "~":
		rest, relative = lit[1:], true
	case w.exp:
		for _, f := range homeForms {
			if strings.HasPrefix(lit, f.form) {
				next := lit[len(f.form):]
				if f.form[len(f.form)-1] != '}' && next != "" && ident(next[0]) {
					continue // $HOMEX is another variable
				}
				sub, rest, relative = f.sub, next, true
				break
			}
		}
		if !relative {
			return nil, false
		}
	}
	if !relative {
		if !strings.HasPrefix(lit, "/") {
			return nil, false
		}
		return []string{clean(lit)}, true
	}
	if (rest != "" && rest[0] != '/') || (w.exp && strings.ContainsAny(rest, "$`")) {
		return nil, false
	}
	homes := []string{home}
	if home == "" {
		homes = c.homeFolders()
	}
	var out []string
	for _, h := range homes {
		out = append(out, clean(path.Join(h, sub, rest)))
		if sub == ".config" && c.su != nil && c.su.ready {
			for _, d := range c.su.xdg[h] {
				out = append(out, clean(path.Join(d, rest)))
			}
		}
	}
	return out, true
}
