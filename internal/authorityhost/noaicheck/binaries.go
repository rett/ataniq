package noaicheck

import (
	"path"
	"slices"
	"strings"
)

// BinDirs are the fixed system folders of every user's PATH, looked into one
// level deep (a folder inside one is not on a PATH). The check uses this
// fixed list, not the PATH of whoever runs it.
var BinDirs = []string{"usr/bin", "usr/sbin", "bin", "sbin", "usr/local/bin", "usr/local/sbin"}

// WholeTrees are the folders looked through completely, at every depth:
// whatever is installed by hand lands in them.
var WholeTrees = []string{"usr/local", "opt"}

// treeDepth is the most levels a walk that must be complete goes down. A
// folder deeper than that is not entered and the run is incomplete; no
// ordinary tree comes near it.
const treeDepth = 40

// homeBinDepth is how deep below a home a folder named "bin" is a bin
// folder (1: the home's own bin/), and homeExecDepth how deep a regular
// executable file named like an agent tool is a hit wherever it is.
const (
	homeBinDepth  = 3
	homeExecDepth = 6
)

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

	for _, dir := range BinDirs {
		c.walk(dir, 1, one)
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
			case e.isRegular() && e.depth <= homeExecDepth && slices.Contains(c.lists.AINames, e.name):
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
//   - an entry of a fixed system bin folder (BinDirs);
//   - anywhere in /usr/local or /opt (WholeTrees), at any depth;
//   - an entry of any node_modules/.bin there, in /usr/lib/node_modules or
//     in a home;
//   - in a home: an entry of a folder named "bin" at most three levels
//     below it (bin, .local/bin, .cargo/bin, go/bin, ~/tools/x/bin ...),
//     anywhere in a tool manager's install folder (mise, npm, pnpm, volta,
//     nvm, uv, pipx), or a regular executable file at most six levels
//     below it.
//
// The homes are those of /etc/passwd, /root and every folder in /home. A
// walk stopped by its depth limit (treeDepth) is "incomplete", never a
// silent skip.
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
