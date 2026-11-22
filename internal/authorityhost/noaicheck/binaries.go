package noaicheck

import (
	"path"
	"slices"
)

// BinDirs are the folders every user's PATH may hold, looked into one level
// deep. The check uses this fixed list, not the PATH of whoever runs it.
var BinDirs = []string{"usr/bin", "usr/local/bin", "usr/local/sbin", "usr/sbin", "bin", "sbin", "opt"}

// optDepth is how deep /opt is looked into.
const optDepth = 4

// installDepth is how deep an install folder or a search for node_modules
// goes.
const installDepth = 8

// homeInstallDirs are the install folders of tool managers in a home, with
// how deep each is looked into: their bin folders, and the trees in which
// mise, npm, pnpm, volta, nvm, uv and pipx keep each installed tool.
var homeInstallDirs = []struct {
	rel   string
	depth int
}{
	{".local/bin", 1},
	{".cargo/bin", 1},
	{"go/bin", 1},
	{".deno/bin", 1},
	{".bun/bin", 1},
	{".local/share/mise", installDepth},
	{".npm-global", installDepth},
	{".local/lib/node_modules", installDepth},
	{".local/share/pnpm", installDepth},
	{".volta", installDepth},
	{".nvm", installDepth},
	{".local/share/uv", installDepth},
	{".local/pipx", installDepth},
	{".local/share/pipx", installDepth},
}

// nodeModuleRoots are the folders outside homes searched for node_modules.
var nodeModuleRoots = []string{"usr/lib/node_modules", "usr/local/lib/node_modules", "opt"}

// ToolNames are the installers and runtimes of agent tools that the host
// never needs: present as a binary in the places the binaries check looks
// into, each is a hit of kind "tool".
var ToolNames = []string{"mise", "npm", "npx", "node", "nodejs", "uv", "uvx", "pipx", "pnpm", "yarn", "bun", "deno"}

// scanBinaries looks, once per run, through every place binaries are
// looked for: the fixed bin folders (one level), /opt (down to optDepth),
// the install folders of every home and every node_modules/.bin below a home
// or a global module folder. It keeps the paths of the entries named like an
// agent tool or an installer, not the entries themselves; the walks stream
// each folder, so a wide folder costs time, not memory.
func (c *Checker) scanBinaries() {
	if c.binScanned {
		return
	}
	c.binScanned = true
	match := func(e *wentry) {
		for _, m := range []struct {
			names []string
			dst   *[]string
		}{{c.lists.AINames, &c.binHits}, {ToolNames, &c.toolHits}} {
			if slices.Contains(m.names, e.name) && c.t.b.hold(int64(len(e.rel))+32) {
				*m.dst = append(*m.dst, e.rel)
			}
		}
	}
	one := func(e *wentry) bool { match(e); return false }
	all := func(e *wentry) bool { match(e); return true }
	for _, dir := range BinDirs {
		c.walk(dir, 1, one)
	}
	c.walk("opt", optDepth, all)

	nodeBins := func(e *wentry) bool {
		if e.isDir() && e.name == "node_modules" {
			c.walk(path.Join(e.rel, ".bin"), 1, one)
		}
		return true
	}
	for _, root := range nodeModuleRoots {
		if path.Base(root) == "node_modules" {
			c.walk(path.Join(root, ".bin"), 1, one)
		}
		c.walk(root, installDepth, nodeBins)
	}
	for _, home := range c.homeFolders() {
		for _, d := range homeInstallDirs {
			if d.depth == 1 {
				c.walk(path.Join(home, d.rel), 1, one)
			} else {
				c.walk(path.Join(home, d.rel), d.depth, all)
			}
		}
		c.walk(home, installDepth, nodeBins)
	}
}

// CheckBinaries is check 1: a file, link or folder named exactly like an
// executable name of ai-names.list in the places binaries are looked for is
// a hit of kind "binary".
func (c *Checker) CheckBinaries() {
	c.scanBinaries()
	for _, rel := range c.binHits {
		c.hit(KindBinary, show(rel), "")
	}
}

// CheckTools is check 2: one of ToolNames in the same places is a hit of
// kind "tool".
func (c *Checker) CheckTools() {
	c.scanBinaries()
	for _, rel := range c.toolHits {
		c.hit(KindTool, show(rel), "")
	}
}
