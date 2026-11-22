package noaicheck

import (
	"path"
	"slices"
	"strings"
)

// NoteDeferredSource is the start of the note of a profile that sources,
// in a function body, a file by a path the check cannot resolve:
// "NOTE deferred-source <file>". Such a source runs only when the function
// is called (bash-completion's loader, say), not at login.
const NoteDeferredSource = "deferred-source"

// startupFile is a shell or environment startup file the startup scan
// reached, and what reading it gave.
type startupFile struct {
	rel        string    // the path it was reached by
	p          string    // its resolved path; "" when it could not be resolved
	r          *envParse // what it sets; nil when it could not be read (err)
	err        error
	unresolved bool // it sources a file by a path the check cannot resolve
	deferred   bool // it sources, in a function body, a file by a path the check cannot resolve
	xdgBad     bool // it sets XDG_CONFIG_HOME to a value the check cannot use (see startup)
}

// startupScan is the result of the startup scan, kept for the run.
type startupScan struct {
	files []*startupFile
	xdg   map[string][]string // the configured XDG_CONFIG_HOME folders of each home
	ready bool                // xdg is complete: sourcePaths uses it
}

// startupFiles are the system's startup files (see envFiles), and
// startupDirs its folders of them, read by the startup scan; the systemd
// manager configuration is not a startup file and is read by CheckEnv.
func startupFiles() (files, dirs []envFile) {
	keep := func(f envFile) bool { return f.syntax == syntaxSystemd }
	return slices.DeleteFunc(slices.Clone(envFiles), keep), slices.DeleteFunc(slices.Clone(envDirs), keep)
}

// xdgHomeFiles are a home's startup files below its configuration folder
// (by default .config), and xdgHomeDirs its folders of them: read again
// below every folder a startup file gives XDG_CONFIG_HOME for the home.
var (
	xdgHomeFiles = []envFile{{"fish/config.fish", syntaxShell}}
	xdgHomeDirs  = []envFile{{"environment.d", syntaxEnvFile}, {"fish/conf.d", syntaxShell}}
)

// startup reads, once per run, the startup files of every login: the
// system's (/etc/environment, /etc/profile, /etc/bash.bashrc, the zsh
// system files, /etc/profile.d, the environment.d folders) and each
// home's (.profile, .bash_profile, .bash_login, .bashrc, .zshenv,
// .zprofile, .zshrc, .zlogin, .pam_environment, its fish and
// environment.d files), and, transitively, every file one of them sources
// (see sourcePaths), in that order, each once. A home-relative source is
// resolved in the home of the file that sources it, or in every home for
// a system file. The environment check reports what they set (CheckEnv)
// and the binary check what they put on PATH (pathDirs), from this one
// scan.
//
// XDG_CONFIG_HOME is read first: the value each of these files (not a
// sourced one) gives it is resolved like a source's path, in the file's
// home or in every home, and a $XDG_CONFIG_HOME of a source or a PATH
// element then stands for that folder as well as the home's .config, and
// the home's fish and environment.d files are read below it too. A value
// the check cannot resolve (a command substitution, another variable),
// and any value given by a file reached only by sourcing, after the
// sources were resolved, is "incomplete env-source <file>" for the file
// that gives it.
func (c *Checker) startup() []*startupFile {
	if c.su != nil {
		return c.su.files
	}
	c.su = &startupScan{xdg: map[string][]string{}}
	b := c.t.b
	sysFiles, sysDirs := startupFiles()
	var fixed []envFile
	listDir := func(rel string, syn envSyntax) {
		c.walk(rel, 1, func(e *wentry) bool {
			if (e.isRegular() || e.isLink()) && b.hold(queueCost(e.rel)) {
				fixed = append(fixed, envFile{e.rel, syn})
			}
			return false
		})
	}
	fixed = append(fixed, sysFiles...)
	for _, d := range sysDirs {
		listDir(d.rel, d.syntax)
	}
	for _, home := range c.homeFolders() {
		for _, f := range homeEnvFiles {
			fixed = append(fixed, envFile{path.Join(home, f.rel), f.syntax})
		}
		for _, d := range homeEnvDirs {
			listDir(path.Join(home, d.rel), d.syntax)
		}
	}

	// XDG_CONFIG_HOME first, from the files above.
	xdgBad := map[string]bool{}
	for _, f := range fixed {
		p, r, _ := c.loadStartup(f.rel, f.syntax)
		if r == nil {
			continue
		}
		home := c.homeOf(p)
		homes := []string{home}
		if home == "" {
			homes = c.homeFolders()
		}
		for _, w := range r.xdg {
			for _, h := range homes {
				dirs, ok := c.sourcePaths(w, h)
				if !ok {
					xdgBad[p] = true
					continue
				}
				for _, d := range dirs {
					if d != path.Join(h, ".config") && !slices.Contains(c.su.xdg[h], d) && b.hold(int64(len(d))+32) {
						c.su.xdg[h] = append(c.su.xdg[h], d)
					}
				}
			}
		}
	}
	c.su.ready = true
	for _, home := range c.homeFolders() {
		for _, d := range c.su.xdg[home] {
			for _, f := range xdgHomeFiles {
				fixed = append(fixed, envFile{path.Join(d, f.rel), f.syntax})
			}
			for _, sub := range xdgHomeDirs {
				listDir(path.Join(d, sub.rel), sub.syntax)
			}
		}
	}

	done := map[string]bool{}
	var queue []envFile
	read := func(f envFile, sourced bool) {
		p, r, err := c.loadStartup(f.rel, f.syntax)
		if p != "" {
			if done[p] {
				return
			}
			done[p] = true
		}
		if !b.hold(int64(len(f.rel)+len(p)) + 96) {
			return
		}
		sf := &startupFile{rel: f.rel, p: p, r: r, err: err, xdgBad: xdgBad[p]}
		c.su.files = append(c.su.files, sf)
		if r == nil {
			return
		}
		if sourced && len(r.xdg) > 0 {
			sf.xdgBad = true // too late: the sources were resolved without it
		}
		for _, w := range r.sourced {
			paths, ok := c.sourcePaths(w, c.homeOf(p))
			for _, s := range paths {
				ok = c.envFileGlob(s, func(m string) {
					if b.entry() && b.hold(queueCost(m)) {
						queue = append(queue, envFile{m, syntaxShell})
					}
				}) && ok
			}
			switch {
			case ok:
			case w.deferred:
				sf.deferred = true
			default:
				sf.unresolved = true
			}
		}
	}
	for _, f := range fixed {
		read(f, false)
	}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		read(f, true)
		b.release(queueCost(f.rel))
	}
	return c.su.files
}

// loadStartup resolves the startup file rel and returns its resolved path
// and what it sets in the syntax syn, read and parsed once per run (a
// shell file with the run's functions that add their argument to PATH,
// see parseShell); the error of a file that cannot be resolved or read.
func (c *Checker) loadStartup(rel string, syn envSyntax) (string, *envParse, error) {
	p, err := c.t.resolve(rel)
	if err != nil {
		return "", nil, err
	}
	if r := c.parsed[p]; r != nil {
		return p, r, nil
	}
	_, data, err := c.t.readFile(p, maxEnvFile)
	if err != nil {
		return p, nil, err
	}
	if c.funcs == nil {
		c.funcs = map[string]bool{}
	}
	r := &envParse{}
	*r = parseEnvFuncs(syn, string(data), strings.HasSuffix(p, ".fish"), c.funcs, c.t.b)
	if c.t.b.hold(int64(len(p)) + r.cost()) {
		c.parsed[p] = r
	}
	return p, r, nil
}

// reportStartup reports what the startup files set (see CheckEnv): a file
// that cannot be read, each provider-key name, syntax the check cannot
// parse, a source or an XDG_CONFIG_HOME it cannot resolve, and the note of
// a deferred source.
func (c *Checker) reportStartup() {
	for _, f := range c.startup() {
		if f.r == nil {
			if f.err != nil {
				c.unreadable(orElse(f.p, f.rel), f.err)
			}
			continue
		}
		at := show(f.p)
		for _, n := range f.r.names {
			c.hit(KindEnv, at, n)
		}
		if f.r.bad {
			c.hit(KindIncomplete, PathEnvSyntax, at)
		}
		if f.unresolved || f.xdgBad {
			c.hit(KindIncomplete, PathEnvSource, at)
		} else if f.deferred {
			c.note(NoteDeferredSource + " " + field(at))
		}
	}
}

// orElse is a when it is not empty, otherwise b.
func orElse(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
