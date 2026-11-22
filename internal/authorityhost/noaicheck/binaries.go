package noaicheck

import (
	"errors"
	"io/fs"
	"path"
	"slices"
	"strings"
	"syscall"
)

// BinDirs are the fixed system folders of every user's PATH, looked into one
// level deep (a folder inside one is not on a PATH). The check uses this
// fixed list and the PATH settings of the system and home profiles (see
// pathDirs), not the PATH of whoever runs it.
var BinDirs = []string{"usr/bin", "usr/sbin", "bin", "sbin", "usr/local/bin", "usr/local/sbin"}

// BinParents are the system bin folders whose every immediate subfolder is
// looked into too, as a folder a profile may put on PATH (Arch's perl
// folders /usr/bin/core_perl, /usr/bin/vendor_perl, ...).
var BinParents = []string{"usr/bin", "usr/local/bin", "usr/local/sbin"}

// pathFiles are the files whose PATH settings add folders to the binary
// inventory, besides every file of etc/profile.d; homePathFiles are a
// home's.
var (
	pathFiles = []envFile{
		{"etc/profile", syntaxShell}, {"etc/environment", syntaxEnvFile}, {"etc/bash.bashrc", syntaxShell},
	}
	pathDirFiles  = "etc/profile.d"
	homePathFiles = []string{".profile", ".bashrc", ".zshenv"}
)

// PathPATH is the name of the incomplete hit for a PATH setting whose
// folder the check cannot resolve: "HIT incomplete path <file>".
const PathPATH = "path"

// WholeTrees are the folders looked through completely, at every depth:
// whatever is installed by hand lands in them.
var WholeTrees = []string{"usr/local", "opt"}

// treeDepth is the most levels a walk that must be complete goes down. A
// folder deeper than that is not entered and the run is incomplete; no
// ordinary tree comes near it.
const treeDepth = 40

// homeBinDepth is how deep below a home a folder named "bin" is a bin
// folder (1: the home's own bin/). A regular executable file named like an
// agent tool is a hit at any depth of a home's walk.
const homeBinDepth = 3

// homeBinDirs are the bin folders of a home that are looked into even when
// they are links to elsewhere: the home's walk does not follow a link.
var homeBinDirs = []string{"bin", ".local/bin", ".cargo/bin", "go/bin", ".deno/bin", ".bun/bin"}

// homeInstallDirs are the trees in which mise, npm, pnpm, volta, nvm, uv
// and pipx keep each installed tool: an entry named like a tool or an
// installer anywhere in them is a hit.
var homeInstallDirs = []string{
	".local/share/mise", ".npm-global", ".local/lib/node_modules", ".local/share/pnpm", ".volta",
	".nvm", ".local/share/uv", ".local/pipx", ".local/share/pipx",
}

// nodeModuleRoots are the global module folders searched for
// node_modules/.bin, besides the homes and the whole trees.
var nodeModuleRoots = []string{"usr/lib/node_modules"}

// ToolNames are the installers and runtimes of agent tools that the host
// never needs: present as a binary in the places the binaries check looks
// into, each is a hit of kind "tool".
var ToolNames = []string{"mise", "npm", "npx", "node", "nodejs", "uv", "uvx", "pipx", "pnpm", "yarn", "bun", "deno"}

// inNodeBin reports whether rel is an entry of a node_modules/.bin folder.
func inNodeBin(rel string) bool {
	dir := path.Dir(rel)
	return path.Base(dir) == ".bin" && path.Base(path.Dir(dir)) == "node_modules"
}

// scanBinaries looks, once per run, through every place binaries are
// looked for (see CheckBinaries). It keeps the paths of the entries named
// like an agent tool or an installer, not the entries themselves; the walks
// stream each folder, so a wide folder costs time, not memory.
func (c *Checker) scanBinaries() {
	if c.binScanned {
		return
	}
	c.binScanned = true
	keep := func(dst *[]string, rel string) {
		if c.t.b.hold(int64(len(rel)) + 32) {
			*dst = append(*dst, rel)
		}
	}
	match := func(e *wentry) {
		if slices.Contains(c.lists.AINames, e.name) {
			keep(&c.binHits, e.rel)
		}
		if slices.Contains(ToolNames, e.name) {
			keep(&c.toolHits, e.rel)
		}
	}
	one := func(e *wentry) bool { match(e); return false }
	all := func(e *wentry) bool { match(e); return true }
	nodeBins := func(e *wentry) bool {
		if inNodeBin(e.rel) {
			match(e)
		}
		return true
	}

	for _, dir := range c.pathDirs() {
		c.walk(dir, 1, one)
	}
	for _, dir := range BinParents {
		c.walk(dir, 2, func(e *wentry) bool {
			if e.depth == 2 {
				match(e)
				return false
			}
			return e.isDir()
		})
	}
	for _, dir := range WholeTrees {
		c.walk(dir, treeDepth, all)
	}
	for _, dir := range nodeModuleRoots {
		c.walk(dir, treeDepth, nodeBins)
	}
	for _, home := range c.homeFolders() {
		var installs []string
		for _, d := range homeInstallDirs {
			installs = append(installs, path.Join(home, d)+"/")
		}
		c.walk(home, treeDepth, func(e *wentry) bool {
			parent := path.Dir(e.rel)
			switch {
			case path.Base(parent) == "bin" && e.depth-1 <= homeBinDepth,
				inNodeBin(e.rel),
				slices.ContainsFunc(installs, func(p string) bool { return strings.HasPrefix(e.rel, p) }):
				match(e)
			case e.isRegular() && slices.Contains(c.lists.AINames, e.name):
				if m, err := statAt(e.dirfd, e.name); err == nil && m.Type() == 0 && m&0o111 != 0 {
					keep(&c.binHits, e.rel)
				}
			}
			return true
		})
		// The home's walk does not follow links: a bin or install folder
		// that is a link to elsewhere is walked where it leads.
		for _, d := range homeBinDirs {
			c.walkLinked(path.Join(home, d), 1, one)
		}
		for _, d := range homeInstallDirs {
			c.walkLinked(path.Join(home, d), treeDepth, all)
		}
	}
}

// pathDirs is the PATH inventory: the folders of BinDirs and every folder a
// PATH setting adds in /etc/profile, /etc/profile.d/*, /etc/environment,
// /etc/bash.bashrc and each home's .profile, .bashrc and .zshenv, read with
// the environment parsers (PATH=..., export PATH=..., PATH+=..., csh's
// setenv PATH ..., and the argument of a function that adds its argument
// to PATH, such as /etc/profile's append_path). A literal absolute folder
// is taken as it is; one that starts with ~/, $HOME or $XDG_CONFIG_HOME is
// resolved in the home of the file, or in every home for a system file (see
// sourcePaths). An element that only refers to PATH itself adds nothing.
// Any other element (a relative folder, another variable, a command
// substitution) is the hit "incomplete path <file>". Each folder is
// resolved within the root and listed once.
func (c *Checker) pathDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(rel string) {
		p, err := c.t.resolve(rel)
		switch {
		case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
			return
		case err != nil:
			c.unreadable(rel, err)
			return
		}
		if !seen[p] && c.t.b.hold(int64(len(p))+32) {
			seen[p] = true
			dirs = append(dirs, p)
		}
	}
	for _, d := range BinDirs {
		add(d)
	}
	funcs := map[string]bool{}
	read := func(rel string, s envSyntax) {
		p, data, err := c.t.readFile(rel, maxEnvFile)
		if err != nil {
			c.unreadable(rel, err)
			return
		}
		r := parseEnvFuncs(s, string(data), false, funcs)
		home := c.homeOf(p)
		for _, e := range r.paths {
			got, ok := c.sourcePaths(e, home)
			if !ok {
				c.hit(KindIncomplete, PathPATH, show(p))
				continue
			}
			for _, d := range got {
				add(d)
			}
		}
	}
	for _, f := range pathFiles[:1] {
		read(f.rel, f.syntax)
	}
	c.walk(pathDirFiles, 1, func(e *wentry) bool {
		if e.isRegular() || e.isLink() {
			read(e.rel, syntaxShell)
		}
		return false
	})
	for _, f := range pathFiles[1:] {
		read(f.rel, f.syntax)
	}
	for _, home := range c.homeFolders() {
		for _, f := range homePathFiles {
			read(path.Join(home, f), syntaxShell)
		}
	}
	return dirs
}

// walkLinked walks rel when a link is on its way (it resolves elsewhere)
// or it cannot be resolved for another reason than not being there; a
// folder reached without a link is covered by the walk it is in.
func (c *Checker) walkLinked(rel string, maxDepth int, visit func(e *wentry) bool) {
	p, err := c.t.resolve(rel)
	if err == nil && p == clean(rel) {
		return
	}
	c.walk(rel, maxDepth, visit)
}

// CheckBinaries is check 1: a file, link or folder named exactly like an
// executable name of ai-names.list is a hit of kind "binary" when it is
//
//   - an entry of a folder of the PATH inventory (see pathDirs: the fixed
//     system bin folders, BinDirs, and every folder the system and home
//     profiles put on PATH), or of an immediate subfolder of /usr/bin,
//     /usr/local/bin or /usr/local/sbin (BinParents);
//   - anywhere in /usr/local or /opt (WholeTrees), at any depth;
//   - an entry of any node_modules/.bin there, in /usr/lib/node_modules or
//     in a home;
//   - in a home: an entry of a folder named "bin" at most three levels
//     below it (bin, .local/bin, .cargo/bin, go/bin, ~/tools/x/bin ...),
//     anywhere in a tool manager's install folder (mise, npm, pnpm, volta,
//     nvm, uv, pipx), or a regular executable file anywhere in it.
//
// The homes are those of /etc/passwd, /root and every folder in /home. A
// walk stopped by its depth limit (treeDepth) is "incomplete <folder>
// depth", never a silent skip.
func (c *Checker) CheckBinaries() {
	c.scanBinaries()
	for _, rel := range c.binHits {
		c.hit(KindBinary, show(rel), "")
	}
}

// CheckTools is check 2: one of ToolNames in the same places (except as a
// plain executable file elsewhere in a home) is a hit of kind "tool".
func (c *Checker) CheckTools() {
	c.scanBinaries()
	for _, rel := range c.toolHits {
		c.hit(KindTool, show(rel), "")
	}
}
