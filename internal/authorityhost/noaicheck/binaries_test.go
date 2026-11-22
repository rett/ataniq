package noaicheck

import "testing"

func TestCheckBinaries(t *testing.T) {
	bin := func(p string) Hit { return Hit{Kind: KindBinary, Path: p} }
	runCases(t, []checkCase{
		{"clean", nil, nil},
		{"usr/bin", func(f *fixture) { f.file("usr/bin/claude", "x") }, []Hit{bin("/usr/bin/claude")}},
		{"through the bin link, reported once",
			func(f *fixture) { f.file("bin/codex", "x") }, []Hit{bin("/usr/bin/codex")}},
		{"a dangling link", func(f *fixture) { f.link("usr/local/bin/claude", "/nowhere") },
			[]Hit{bin("/usr/local/bin/claude")}},
		{"usr/local/sbin", func(f *fixture) { f.file("usr/local/sbin/codex", "x") },
			[]Hit{bin("/usr/local/sbin/codex")}},
		{"opt top", func(f *fixture) { f.file("opt/claude", "x") }, []Hit{bin("/opt/claude")}},
		{"opt at depth 4", func(f *fixture) { f.file("opt/a/b/c/claude", "x") },
			[]Hit{bin("/opt/a/b/c/claude")}},
		{"opt below depth 4 is not looked into, and that is said", func(f *fixture) { f.file("opt/a/b/c/d/claude", "x") },
			[]Hit{{Kind: KindIncomplete, Path: "/opt", Detail: DetailDepth}}},
		{"a folder named like a tool", func(f *fixture) { f.dir("opt/codex") }, []Hit{bin("/opt/codex")}},
		{"home .local/bin", func(f *fixture) { f.file("home/user/.local/bin/claude", "x") },
			[]Hit{bin("/home/user/.local/bin/claude")}},
		{"home .cargo/bin", func(f *fixture) { f.file("home/user/.cargo/bin/codex", "x") },
			[]Hit{bin("/home/user/.cargo/bin/codex")}},
		{"mise install", func(f *fixture) {
			f.file("home/user/.local/share/mise/installs/npm-claude/1.0.0/bin/claude", "x")
		}, []Hit{bin("/home/user/.local/share/mise/installs/npm-claude/1.0.0/bin/claude")}},
		{"pipx venv", func(f *fixture) { f.file("home/user/.local/share/pipx/venvs/x/bin/codex", "x") },
			[]Hit{bin("/home/user/.local/share/pipx/venvs/x/bin/codex")}},
		{"root's npm prefix", func(f *fixture) { f.link("root/.npm-global/bin/claude", "../lib/x.js") },
			[]Hit{bin("/root/.npm-global/bin/claude")}},
		{"node_modules/.bin in a project", func(f *fixture) {
			f.link("home/user/src/app/node_modules/.bin/claude", "../x/cli.js")
		}, []Hit{bin("/home/user/src/app/node_modules/.bin/claude")}},
		{"global node_modules", func(f *fixture) {
			f.file("usr/lib/node_modules/pkg/node_modules/.bin/codex", "x")
		}, []Hit{bin("/usr/lib/node_modules/pkg/node_modules/.bin/codex")}},
		{"home from passwd outside /home", func(f *fixture) { f.file("srv/app/.local/bin/claude", "x") },
			[]Hit{bin("/srv/app/.local/bin/claude")}},
		{"home in /home not in passwd", func(f *fixture) { f.file("home/other/.local/bin/codex", "x") },
			[]Hit{bin("/home/other/.local/bin/codex")}},
		{"a home that is a link", func(f *fixture) {
			f.file("var/home/alt/.local/bin/claude", "x")
			f.link("home/alt", "/var/home/alt")
		}, []Hit{bin("/var/home/alt/.local/bin/claude")}},
		{"a long name", func(f *fixture) { f.file("usr/bin/cursor-agent-longname", "x") },
			[]Hit{bin("/usr/bin/cursor-agent-longname")}},
		{"a name that only starts like one", func(f *fixture) { f.file("usr/bin/claudex", "x") }, nil},
		{"outside the places", func(f *fixture) { f.file("usr/share/claude/claude", "x") }, nil},
	}, (*Checker).CheckBinaries)
}

func TestCheckTools(t *testing.T) {
	tool := func(p string) Hit { return Hit{Kind: KindTool, Path: p} }
	runCases(t, []checkCase{
		{"clean", nil, nil},
		{"node", func(f *fixture) { f.file("usr/bin/node", "x") }, []Hit{tool("/usr/bin/node")}},
		{"npm and npx", func(f *fixture) {
			f.link("usr/bin/npm", "../lib/node_modules/npm/bin/npm-cli.js")
			f.link("usr/bin/npx", "../lib/node_modules/npm/bin/npx-cli.js")
		}, []Hit{tool("/usr/bin/npm"), tool("/usr/bin/npx")}},
		{"mise in usr/local", func(f *fixture) { f.file("usr/local/bin/mise", "x") },
			[]Hit{tool("/usr/local/bin/mise")}},
		{"uv in a home", func(f *fixture) { f.file("home/user/.local/bin/uv", "x") },
			[]Hit{tool("/home/user/.local/bin/uv")}},
		{"pipx in opt", func(f *fixture) { f.file("opt/py/bin/pipx", "x") }, []Hit{tool("/opt/py/bin/pipx")}},
		{"nodejs", func(f *fixture) { f.file("usr/sbin/nodejs", "x") }, []Hit{tool("/usr/bin/nodejs")}},
		{"an agent name is not a tool", func(f *fixture) { f.file("usr/bin/claude", "x") }, nil},
	}, (*Checker).CheckTools)
}

func TestCheckCredentials(t *testing.T) {
	cred := func(p string) Hit { return Hit{Kind: KindCredential, Path: p} }
	runCases(t, []checkCase{
		{"clean", nil, nil},
		{"a folder in a home", func(f *fixture) { f.dir("home/user/.claude") },
			[]Hit{cred("/home/user/.claude")}},
		{"a file in root's home", func(f *fixture) { f.file("root/.claude", "token") },
			[]Hit{cred("/root/.claude")}},
		{"a nested config folder", func(f *fixture) { f.file("home/user/.config/opencode/auth.json", "{}") },
			[]Hit{cred("/home/user/.config/opencode")}},
		{"a dangling link", func(f *fixture) { f.link("home/user/.claude", "/elsewhere") },
			[]Hit{cred("/home/user/.claude")}},
		{"a home outside /home", func(f *fixture) { f.dir("srv/app/.claude") }, []Hit{cred("/srv/app/.claude")}},
		{"a home in /home not in passwd", func(f *fixture) { f.dir("home/other/.claude") },
			[]Hit{cred("/home/other/.claude")}},
		{"the root folder, home of system users", func(f *fixture) { f.dir(".claude") }, []Hit{cred("/.claude")}},
		{".config is a file", func(f *fixture) {
			f.file("home/other/.config", "x")
		}, nil},
		{"the name inside another folder", func(f *fixture) { f.dir("home/user/src/.claude") }, nil},
	}, (*Checker).CheckCredentials)
}

// A link loop and an unreadable folder in a place the check looks into end
// the check with an "unreadable" hit, not a hang or a crash.
func TestLoopsAndUnreadableFolders(t *testing.T) {
	skipIfRoot(t)
	unr := func(p, why string) Hit { return Hit{Kind: KindUnreadable, Path: p, Detail: why} }
	runCases(t, []checkCase{
		{"a loop as an install folder", func(f *fixture) {
			f.link("home/user/.cargo/bin", "../.cargo/bin2")
			f.link("home/user/.cargo/bin2", "bin")
		}, []Hit{unr("/home/user/.cargo/bin", errLoop.Error())}},
		{"a loop inside opt is not followed", func(f *fixture) {
			f.link("opt/vendor/loop", "../vendor")
			f.link("opt/vendor/app/up", "/opt")
		}, nil},
		{"a loop as a home", func(f *fixture) { f.link("home/ring", "ring") },
			[]Hit{unr("/home/ring", errLoop.Error())}},
		{"an unreadable folder in a home", func(f *fixture) {
			f.unreadable("home/user/.local/share/mise")
		}, []Hit{unr("/home/user/.local/share/mise", "permission denied")}},
		{"an unreadable bin folder", func(f *fixture) {
			f.unreadable("usr/local/bin")
		}, []Hit{unr("/usr/local/bin", "permission denied")}},
	}, func(c *Checker) {
		c.CheckBinaries()
		c.CheckTools()
		c.CheckCredentials()
	})
}
