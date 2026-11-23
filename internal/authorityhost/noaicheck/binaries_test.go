package noaicheck

import (
	"reflect"
	"strings"
	"testing"
)

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
		{"opt at any depth", func(f *fixture) { f.file("opt/a/b/c/d/e/f/g/claude", "x") },
			[]Hit{bin("/opt/a/b/c/d/e/f/g/claude")}},
		{"usr/local completely", func(f *fixture) { f.file("usr/local/share/x/y/z/codex", "x") },
			[]Hit{bin("/usr/local/share/x/y/z/codex")}},
		{"a tree deeper than the walk goes is incomplete", func(f *fixture) {
			f.dir("opt/" + strings.Repeat("d/", treeDepth+1))
		}, []Hit{{Kind: KindIncomplete, Path: "/opt", Detail: DetailDepth}}},
		{"home bin", func(f *fixture) { f.file("home/user/bin/codex", "x") }, []Hit{bin("/home/user/bin/codex")}},
		{"home go/bin", func(f *fixture) { f.file("home/user/go/bin/claude", "x") }, []Hit{bin("/home/user/go/bin/claude")}},
		{"a bin folder three levels below a home", func(f *fixture) { f.file("home/user/src/tool/bin/claude", "x") },
			[]Hit{bin("/home/user/src/tool/bin/claude")}},
		{"a bin folder four levels below a home is not one", func(f *fixture) {
			f.file("home/user/a/b/c/bin/claude", "not executable")
		}, nil},
		{"an executable file in a home", func(f *fixture) { f.exe("home/user/a/b/c/d/codex") },
			[]Hit{bin("/home/user/a/b/c/d/codex")}},
		{"an executable file at any depth of a home", func(f *fixture) { f.exe("home/user/a/b/c/d/e/f/g/h/codex") },
			[]Hit{bin("/home/user/a/b/c/d/e/f/g/h/codex")}},
		{"a home deeper than the walk goes is incomplete", func(f *fixture) {
			f.dir("home/user/" + strings.Repeat("d/", treeDepth+1))
		}, []Hit{{Kind: KindIncomplete, Path: "/home/user", Detail: DetailDepth}}},
		{"a plain file in a home", func(f *fixture) { f.file("home/user/notes/claude", "x") }, nil},
		{"a linked bin folder", func(f *fixture) {
			mustRemoveAll(f, "home/user/.local/bin")
			f.file("srv/tools/bin/claude", "x")
			f.link("home/user/.local/bin", "/srv/tools/bin")
		}, []Hit{bin("/srv/tools/bin/claude")}},
		{"a deep node_modules/.bin in a home", func(f *fixture) {
			f.link("home/user/a/b/c/d/e/f/g/h/i/node_modules/.bin/claude", "../x/cli.js")
		}, []Hit{bin("/home/user/a/b/c/d/e/f/g/h/i/node_modules/.bin/claude")}},
		{"a home in proc is not walked", func(f *fixture) {
			f.file("etc/passwd", "rtkit:x:133:133::/proc:/usr/bin/nologin\n")
			f.exe("proc/1/root/claude")
		}, nil},
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
		{"node in a home bin", func(f *fixture) { f.file("home/user/bin/node", "x") }, []Hit{tool("/home/user/bin/node")}},
		{"an installer as a plain executable elsewhere in a home", func(f *fixture) { f.exe("home/user/src/npm") }, nil},
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

// The binary, tool and credential checks run together, as in a run.
func binaryChecks(c *Checker) {
	c.CheckBinaries()
	c.CheckTools()
	c.CheckCredentials()
}

// A link loop in a place the check looks into ends the check with an
// "unreadable" hit, not a hang or a crash. A loop fails for every user,
// root included, so these cases run as any user.
func TestLinkLoops(t *testing.T) {
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
		{"a loop as a bin folder of a profile's PATH", func(f *fixture) {
			f.file("etc/profile.d/x.sh", "PATH=$PATH:/srv/ring\n")
			f.link("srv/ring", "ring")
		}, []Hit{unr("/srv/ring", errLoop.Error())}},
	}, binaryChecks)
}

// An unreadable folder in a place the check looks into is an "unreadable"
// hit. Root reads a folder whatever its mode, so a folder without
// permissions cannot stand for one when the tests run as root: these
// cases alone are skipped there.
func TestUnreadableFolders(t *testing.T) {
	skipIfRoot(t)
	unr := func(p, why string) Hit { return Hit{Kind: KindUnreadable, Path: p, Detail: why} }
	runCases(t, []checkCase{
		{"an unreadable folder in a home", func(f *fixture) {
			f.unreadable("home/user/.local/share/mise")
		}, []Hit{unr("/home/user/.local/share/mise", "permission denied")}},
		{"an unreadable bin folder", func(f *fixture) {
			f.unreadable("usr/local/bin")
		}, []Hit{unr("/usr/local/bin", "permission denied")}},
	}, binaryChecks)
}

// The PATH inventory: the folders the system and home profiles put on
// PATH are looked into one level deep, as are the immediate subfolders of
// the system bin folders; a PATH setting the check cannot resolve is
// "incomplete path <file>".
func TestCheckBinariesPathInventory(t *testing.T) {
	bin := func(p string) Hit { return Hit{Kind: KindBinary, Path: p} }
	inc := func(file string) Hit { return Hit{Kind: KindIncomplete, Path: PathPATH, Detail: file} }
	const profile = "append_path () {\n    case \":$PATH:\" in\n        *:\"$1\":*) ;;\n        *) PATH=\"${PATH:+$PATH:}$1\"\n    esac\n}\n" +
		"append_path '/usr/local/sbin'\nappend_path '/usr/bin'\nexport PATH\n"
	runCases(t, []checkCase{
		{"clean", func(f *fixture) {
			f.file("etc/profile", profile)
			f.file("etc/profile.d/perlbin.sh", "[ -d /usr/bin/core_perl ] && append_path '/usr/bin/core_perl'\nexport PATH\n")
			f.file("etc/profile.d/perlbin.csh", "[ -d /usr/bin/core_perl ] && setenv PATH ${PATH}:/usr/bin/core_perl\n")
		}, nil},
		{"an immediate subfolder of /usr/bin", func(f *fixture) { f.file("usr/bin/core_perl/claude", "x") },
			[]Hit{bin("/usr/bin/core_perl/claude")}},
		{"deeper in /usr/bin is not on a PATH", func(f *fixture) { f.file("usr/bin/a/b/claude", "x") }, nil},
		{"a folder a profile.d function call adds", func(f *fixture) {
			f.file("etc/profile", profile)
			f.file("etc/profile.d/tools.sh", "append_path '/srv/tools/bin'\n")
			f.file("srv/tools/bin/claude", "not executable")
		}, []Hit{bin("/srv/tools/bin/claude")}},
		{"a folder etc/environment adds", func(f *fixture) {
			f.file("etc/environment", "PATH=\"/usr/local/sbin:/usr/local/bin:/usr/bin:/srv/env/bin\"\n")
			f.file("srv/env/bin/codex", "x")
		}, []Hit{bin("/srv/env/bin/codex")}},
		{"a folder csh's setenv adds", func(f *fixture) {
			f.file("etc/profile.d/x.csh", "setenv PATH ${PATH}:/srv/csh/bin\n")
			f.file("srv/csh/bin/claude", "x")
		}, []Hit{bin("/srv/csh/bin/claude")}},
		{"a home folder a home's profile adds", func(f *fixture) {
			f.file("home/user/.profile", "export PATH=\"$HOME/tools:$PATH\"\n")
			f.file("home/user/tools/claude", "not executable")
		}, []Hit{bin("/home/user/tools/claude")}},
		{"a home folder a system profile adds, in every home", func(f *fixture) {
			f.file("etc/profile.d/mine.sh", "PATH=$PATH:~/.mybin\n")
			f.file("srv/app/.mybin/codex", "not executable")
		}, []Hit{bin("/srv/app/.mybin/codex")}},
		{"a folder bash.bashrc and .zshenv add", func(f *fixture) {
			f.file("etc/bash.bashrc", "PATH+=:/srv/a\n")
			f.file("root/.zshenv", "export PATH=/srv/b:$PATH\n")
			f.file("srv/a/claude", "x")
			f.file("srv/b/codex", "x")
		}, []Hit{bin("/srv/a/claude"), bin("/srv/b/codex")}},
		{"PATH settings the check cannot resolve", func(f *fixture) {
			f.file("etc/profile.d/x.sh", "PATH=$(cat /etc/paths):$PATH\n")
			f.file("home/user/.bashrc", "export PATH=$OTHER/bin:$PATH\nPATH=bin:$PATH\n")
		}, []Hit{inc("/etc/profile.d/x.sh"), inc("/home/user/.bashrc")}},
		{"a folder a sourced file adds, outside the fixed trees", func(f *fixture) {
			f.file("etc/profile", profile+". /etc/extra/path.sh\n")
			f.file("etc/extra/path.sh", "append_path /srv/sourced/bin\nPATH=\"$PATH:/srv/sourced/sbin\"\n")
			f.file("srv/sourced/bin/claude", "not executable")
			f.file("srv/sourced/sbin/codex", "not executable")
		}, []Hit{bin("/srv/sourced/bin/claude"), bin("/srv/sourced/sbin/codex")}},
		{"a folder a home's sourced file adds", func(f *fixture) {
			f.file("home/user/.bashrc", ". ~/.paths.sh\n")
			f.file("home/user/.paths.sh", "export PATH=\"$HOME/x:$PATH\"\n")
			f.file("home/user/x/claude", "not executable")
		}, []Hit{bin("/home/user/x/claude")}},
		{"the login files of bash and zsh", func(f *fixture) {
			f.file("home/user/.bash_profile", "PATH=/srv/bp:$PATH\n")
			f.file("home/user/.bash_login", "PATH=/srv/bl:$PATH\n")
			f.file("root/.zprofile", "path+=(/srv/zp)\nPATH=$PATH:/srv/zp\n")
			f.file("root/.zshrc", "PATH=$PATH:/srv/zr\n")
			f.file("root/.zlogin", "PATH=$PATH:/srv/zl\n")
			for _, d := range []string{"bp", "bl", "zp", "zr", "zl"} {
				f.file("srv/"+d+"/claude", "not executable")
			}
		}, []Hit{bin("/srv/zp/claude"), bin("/srv/zr/claude"), bin("/srv/zl/claude"), bin("/srv/bp/claude"), bin("/srv/bl/claude")}},
		{"an element a sourced file cannot resolve", func(f *fixture) {
			f.file("etc/bash.bashrc", ". /etc/extra/dyn.sh\n")
			f.file("etc/extra/dyn.sh", "PATH=$TOOLS/bin:$PATH\n")
		}, []Hit{inc("/etc/extra/dyn.sh")}},
		{"a folder below a configured XDG_CONFIG_HOME", func(f *fixture) {
			f.file("home/user/.profile", "export XDG_CONFIG_HOME=/srv/cfg\nPATH=\"$XDG_CONFIG_HOME/bin:$PATH\"\n")
			f.file("srv/cfg/bin/codex", "not executable")
		}, []Hit{bin("/srv/cfg/bin/codex")}},
		{"a function's own PATH of its argument", func(f *fixture) {
			f.file("home/user/.bashrc", "add() { PATH=\"$1:$PATH\"; }\nadd /srv/c\n")
			f.file("srv/c/claude", "x")
		}, []Hit{bin("/srv/c/claude")}},
	}, (*Checker).CheckBinaries)
}

func TestSplitPath(t *testing.T) {
	for in, want := range map[string][]string{
		"/a:/b":                {"/a", "/b"},
		"${PATH:+$PATH:}$1":    {"${PATH:+$PATH:}$1"},
		"$(x a:b):/c":          {"$(x a:b)", "/c"},
		"`x a:b`:/c":           {"`x a:b`", "/c"},
		"":                     {""},
		"$HOME/bin:${X:-/y:z}": {"$HOME/bin", "${X:-/y:z}"},
	} {
		if got := splitPath(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitPath(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"$PATH": "", "${PATH}": "", "${PATH:+$PATH:}$1": "$1", "$1${PATH:+:${PATH}}": "$1", "$PATHX": "$PATHX", "/a$PATH": "/a",
	} {
		if got := dropPathRefs(in); got != want {
			t.Errorf("dropPathRefs(%q) = %q, want %q", in, got, want)
		}
	}
}
