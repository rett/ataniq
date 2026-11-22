package noaicheck

import (
	"io/fs"
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

// entry is a name found in one of the places binaries are looked for.
type entry struct {
	rel  string
	name string
}

// binaryEntries lists, once per run, every entry of the places binaries are
// looked for: the fixed bin folders (one level), /opt (down to optDepth),
// the install folders of every home and every node_modules/.bin below a home
// or a global module folder.
func (c *Checker) binaryEntries() []entry {
	if c.bins != nil {
		return c.bins
	}
	c.bins = []entry{}
	seen := map[string]bool{}
	add := func(rel string, d fs.DirEntry) {
		if !seen[rel] {
			seen[rel] = true
			c.bins = append(c.bins, entry{rel: rel, name: d.Name()})
		}
	}
	for _, dir := range BinDirs {
		c.t.walk(dir, 1, add, c.unreadable)
	}
	c.t.walk("opt", optDepth, add, c.unreadable)

	nodeBins := func(rel string, d fs.DirEntry) {
		if d.IsDir() && d.Name() == "node_modules" {
			c.t.walk(path.Join(rel, ".bin"), 1, add, c.unreadable)
		}
	}
	for _, root := range nodeModuleRoots {
		if path.Base(root) == "node_modules" {
			c.t.walk(path.Join(root, ".bin"), 1, add, c.unreadable)
		}
		c.t.walk(root, installDepth, nodeBins, c.unreadable)
	}
	for _, home := range c.homeFolders() {
		for _, d := range homeInstallDirs {
			c.t.walk(path.Join(home, d.rel), d.depth, add, c.unreadable)
		}
		c.t.walk(home, installDepth, nodeBins, c.unreadable)
	}
	return c.bins
}

// CheckBinaries is check 1: a file, link or folder named exactly like an
// executable name of ai-names.list in the places binaries are looked for is
// a hit of kind "binary".
func (c *Checker) CheckBinaries() {
	for _, e := range c.binaryEntries() {
		if slices.Contains(c.lists.AINames, e.name) {
			c.hit(KindBinary, show(e.rel), "")
		}
	}
}

// CheckTools is check 2: one of ToolNames in the same places is a hit of
// kind "tool".
func (c *Checker) CheckTools() {
	for _, e := range c.binaryEntries() {
		if slices.Contains(ToolNames, e.name) {
			c.hit(KindTool, show(e.rel), "")
		}
	}
}
