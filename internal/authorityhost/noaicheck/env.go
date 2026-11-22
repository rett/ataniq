package noaicheck

import (
	"path"
	"strings"
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

// CheckEnv is check 4: an environment file, shell profile, systemd manager
// configuration or unit file that sets or passes a variable named like a
// provider key (see ProviderKeyName) is a hit of kind "env", with the
// variable's name as its detail; the value is never read into the report.
// The files are those of /etc/environment, /etc/profile and the shells'
// system profiles, /etc/profile.d, the environment.d folders, the systemd
// manager configuration, every unit file and drop-in systemd loads (see
// scanUnitFiles: links into the release are followed within the root, and
// each file is read once), every home's profiles, each file an
// EnvironmentFile= line of those names (a glob in its last component is
// matched within the root) and each file a profile sources (see
// sourcePaths: a literal absolute path, or one that starts with $HOME, ~ or
// $XDG_CONFIG_HOME, resolved in each home; a path the check cannot resolve
// is the hit "incomplete env-source <file>", never passed over).
//
// Each file is read in its own syntax (see parseEnv): a profile as shell
// words, so a comment or a quoted value is never taken for a setting; a
// unit by its Environment=, PassEnvironment= and EnvironmentFile= keys; an
// environment file as KEY=VALUE lines. Only a valid identifier is ever
// reported. A file whose syntax the check cannot parse is the hit
// "incomplete env-syntax <file>".
func (c *Checker) CheckEnv() {
	done := map[string]bool{}
	var queue []envFile
	var scan func(rel string, s envSyntax)
	scan = func(rel string, s envSyntax) {
		p, err := c.t.resolve(rel)
		if err != nil {
			c.unreadable(rel, err)
			return
		}
		if done[p] || p == "dev/null" { // read once; a masked file is empty
			return
		}
		done[p] = true
		_, data, err := c.t.readFile(p, maxEnvFile)
		if err != nil {
			c.unreadable(p, err)
			return
		}
		r := parseEnv(s, string(data), strings.HasSuffix(p, ".fish"))
		for _, n := range r.names {
			c.hit(KindEnv, show(p), n)
		}
		if r.bad {
			c.hit(KindIncomplete, PathEnvSyntax, show(p))
		}
		for _, spec := range r.files {
			for _, f := range c.specifiers(spec, isUserUnit(p)) {
				if !c.envFileGlob(f, func(m string) { queue = append(queue, envFile{m, syntaxEnvFile}) }) {
					c.hit(KindIncomplete, PathEnvSyntax, show(p))
				}
			}
		}
		for _, w := range r.sourced {
			paths, ok := c.sourcePaths(w, c.homeOf(p))
			for _, f := range paths {
				ok = c.envFileGlob(f, func(m string) { queue = append(queue, envFile{m, syntaxShell}) }) && ok
			}
			if !ok {
				c.hit(KindIncomplete, PathEnvSource, show(p))
			}
		}
	}
	scanDir := func(rel string, s envSyntax) {
		c.walk(rel, 1, func(e *wentry) bool {
			if e.isRegular() || e.isLink() {
				scan(e.rel, s)
			}
			return false
		})
	}
	for _, f := range envFiles {
		scan(f.rel, f.syntax)
	}
	for _, d := range envDirs {
		scanDir(d.rel, d.syntax)
	}
	c.scanUnitFiles(func(rel string) { scan(rel, syntaxSystemd) })
	for _, home := range c.homeFolders() {
		for _, f := range homeEnvFiles {
			scan(path.Join(home, f.rel), f.syntax)
		}
		for _, d := range homeEnvDirs {
			scanDir(path.Join(home, d.rel), d.syntax)
		}
	}
	// A file may name or source another; done ends a cycle.
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		scan(f.rel, f.syntax)
	}
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
// default, the home's .config, is what is resolved).
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
// path word w of a "." or "source" command stands for, and whether it
// could resolve it. A word without an expansion must be an absolute path
// or start with "~/". A word with an expansion must start with one of
// homeForms and hold no other: it is resolved in home, or in every home
// when home is "". Anything else (a relative path, another variable, a
// command substitution, "~user") cannot be resolved. The last component
// may be a glob, matched by the caller.
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
	}
	return out, true
}

// isUserUnit reports whether the unit file rel is a user unit: in a
// systemd/user folder or a home's .config/systemd/user.
func isUserUnit(rel string) bool {
	return strings.Contains("/"+rel, "/systemd/user/")
}

// specifiers returns the paths an EnvironmentFile= path stands for once its
// specifiers are resolved: %h is every home for a user unit and /root for
// a system unit, %t every folder of /run/user for a user unit and /run for
// a system unit, %% a percent sign.
func (c *Checker) specifiers(p string, user bool) []string {
	if !strings.Contains(p, "%") {
		return []string{p}
	}
	homes, runs := []string{"/root"}, []string{"/run"}
	if user {
		homes, runs = nil, nil
		for _, h := range c.homeFolders() {
			homes = append(homes, show(h))
		}
		c.walk("run/user", 1, func(e *wentry) bool {
			if e.isDir() {
				runs = append(runs, show(e.rel))
			}
			return false
		})
	}
	out := []string{""}
	for i := 0; i < len(p); i++ {
		var alts []string
		switch {
		case p[i] != '%' || i+1 >= len(p):
			alts = []string{p[i : i+1]}
		case p[i+1] == 'h':
			alts, i = homes, i+1
		case p[i+1] == 't':
			alts, i = runs, i+1
		default:
			alts, i = []string{"%"}, i+1
		}
		var next []string
		for _, o := range out {
			for _, a := range alts {
				next = append(next, o+a)
			}
		}
		out = next
	}
	return out
}
