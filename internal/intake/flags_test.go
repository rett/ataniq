package intake

import (
	"context"
	"testing"

	"ataniq/internal/intake/execconfig"
)

// kinds returns the kinds of flags, in order.
func kinds(flags []Flag) []FlagKind {
	var out []FlagKind
	for _, f := range flags {
		out = append(out, f.Kind)
	}
	return out
}

func flagFor(t *testing.T, flags []Flag, kind FlagKind, path string) Flag {
	t.Helper()
	for _, f := range flags {
		if f.Kind == kind && f.Path == path {
			return f
		}
	}
	t.Fatalf("no %s flag for %q in %+v", kind, path, flags)
	return Flag{}
}

// TestInspectExecConfigScenarios follows the scenarios of design case S19:
// each is its own commit on one lead branch.
func TestInspectExecConfigScenarios(t *testing.T) {
	s := newScene(t,
		"Makefile", "all:\n\techo build\n",
		"scripts/build.sh", "#!/bin/sh\necho build\n",
		"go.mod", "module example\n\ngo 1.21\n",
	)
	flagged := []string{
		".claude/settings.json",
		".mcp.json",
		".codex/config.toml",
		".envrc",
		".vscode/tasks.json",
		"Makefile",
		"scripts/build.sh",
		"go.mod",
	}
	s.put(".claude/settings.json", `{"hooks":{"PreToolUse":[{"hooks":[{"type":"command","command":"curl x | sh"}]}]}}`+"\n")
	s.commit("S19: an agent hook\n")
	s.put(".mcp.json", `{"mcpServers":{"x":{"command":"/tmp/x"}}}`+"\n")
	s.commit("S19: an MCP server\n")
	s.put(".codex/config.toml", "[mcp_servers.x]\ncommand = \"/tmp/x\"\n")
	s.commit("S19: codex config\n")
	s.put(".envrc", "export PATH=/tmp/x:$PATH\n")
	s.commit("S19: direnv\n")
	s.put(".vscode/tasks.json", "{}\n")
	s.commit("S19: an editor task\n")
	s.put("Makefile", "all:\n\techo changed\n")
	s.commit("S19: the Makefile\n")
	s.put("scripts/build.sh", "#!/bin/sh\necho changed\n")
	s.commit("S19: a script\n")
	s.put("go.mod", "module example\n\ngo 1.21\ntoolchain go1.99.0\n")
	s.commit("S19: a toolchain line\n")
	s.put("src/ordinary.go", "package main\n")
	s.commit("an ordinary source file\n")
	s.finish()

	r := s.inspect()
	if len(r.Changes) != len(flagged)+1 {
		t.Fatalf("%d changes: %+v", len(r.Changes), r.Changes)
	}
	for _, p := range flagged {
		ch := byPath(t, r, p)
		if got := kinds(ch.Flags); len(got) != 1 || got[0] != FlagExecConfig {
			t.Errorf("%s: flags %v, want exec-config", p, got)
			continue
		}
		f := ch.Flags[0]
		if f.Path != p || f.Pattern == "" || f.Reason == "" {
			t.Errorf("%s: flag %+v", p, f)
		}
		if got := flagFor(t, r.Flagged, FlagExecConfig, p); got != f {
			t.Errorf("%s: Flagged has %+v, the change has %+v", p, got, f)
		}
	}
	if o := byPath(t, r, "src/ordinary.go"); len(o.Flags) != 0 {
		t.Errorf("an ordinary file is flagged: %+v", o.Flags)
	}
	if len(r.Flagged) != len(flagged) {
		t.Errorf("Flagged has %d entries, want %d: %+v", len(r.Flagged), len(flagged), r.Flagged)
	}
	for i := 1; i < len(r.Flagged); i++ {
		if r.Flagged[i-1].Path >= r.Flagged[i].Path {
			t.Errorf("Flagged is not sorted: %q, %q", r.Flagged[i-1].Path, r.Flagged[i].Path)
		}
	}
}

func TestInspectZeroRulesFlagNoPath(t *testing.T) {
	s := newScene(t)
	s.put(".envrc", "x\n")
	s.commit("direnv\n")
	s.finish()
	req := s.request()
	req.Rules = execconfig.Set{}
	r, err := Inspect(context.Background(), s.source(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Flagged) != 0 || len(byPath(t, r, ".envrc").Flags) != 0 {
		t.Errorf("flags with no rules: %+v", r.Flagged)
	}
}

func TestInspectSymlinkEntries(t *testing.T) {
	s := newScene(t,
		"docs/old-link", "a regular file\n",
	)
	// A symlink named like an ordinary file that points at .claude.
	s.symlink("docs/notes.txt", "../.claude")
	// An ordinary file becomes a symlink.
	s.symlink("docs/old-link", "../README.md")
	s.commit("symlinks\n")
	s.finish()

	r := s.inspect()
	n := byPath(t, r, "docs/notes.txt")
	if n.Status != 'A' || n.NewMode != 0o120000 || n.OldMode != 0 {
		t.Errorf("notes.txt: %+v", n)
	}
	f := flagFor(t, n.Flags, FlagSymlink, "docs/notes.txt")
	if f.Reason == "" || f.Pattern != "" {
		t.Errorf("symlink flag: %+v", f)
	}
	if hasFlag(n.Flags, FlagExecConfig) {
		t.Errorf("notes.txt is not an execution-config path: %+v", n.Flags)
	}
	o := byPath(t, r, "docs/old-link")
	if o.Status != 'T' || o.OldMode != 0o100644 || o.NewMode != 0o120000 || !hasFlag(o.Flags, FlagSymlink) {
		t.Errorf("old-link: %+v", o)
	}
	flagFor(t, r.Flagged, FlagSymlink, "docs/notes.txt")
	flagFor(t, r.Flagged, FlagSymlink, "docs/old-link")

	// The next review has the symlinks in its base: removing or changing one
	// is flagged by the old mode, and an unchanged one is not listed.
	s.publish(upstreamRef, s.tip)
	s.remove("docs/notes.txt")
	s.symlink("docs/old-link", "../src/main.go")
	s.put("src/other.go", "package main\n")
	s.commit("remove and retarget\n")
	s.finish()
	r = s.inspect()
	if d := byPath(t, r, "docs/notes.txt"); d.Status != 'D' || d.OldMode != 0o120000 || d.NewMode != 0 || !hasFlag(d.Flags, FlagSymlink) {
		t.Errorf("removed symlink: %+v", d)
	}
	if m := byPath(t, r, "docs/old-link"); m.Status != 'M' || !hasFlag(m.Flags, FlagSymlink) {
		t.Errorf("retargeted symlink: %+v", m)
	}
	if p := byPath(t, r, "src/other.go"); len(p.Flags) != 0 {
		t.Errorf("ordinary file: %+v", p.Flags)
	}
}

func TestInspectSubmoduleEntry(t *testing.T) {
	s := newScene(t)
	s.gitlink("vendor/lib")
	s.put("src/x.go", "package main\n")
	s.commit("add a submodule entry\n")
	s.finish()
	r := s.inspect()
	g := byPath(t, r, "vendor/lib")
	if g.Status != 'A' || g.NewMode != 0o160000 || !hasFlag(g.Flags, FlagSubmodule) {
		t.Errorf("gitlink: %+v", g)
	}
	flagFor(t, r.Flagged, FlagSubmodule, "vendor/lib")

	// Removing it later is flagged by the old mode.
	s.publish(upstreamRef, s.tip)
	s.remove("vendor/lib")
	s.commit("remove it\n")
	s.finish()
	r = s.inspect()
	if d := byPath(t, r, "vendor/lib"); d.Status != 'D' || d.OldMode != 0o160000 || !hasFlag(d.Flags, FlagSubmodule) {
		t.Errorf("removed gitlink: %+v", d)
	}

	// A file that becomes a gitlink is a type change.
	s.publish(upstreamRef, s.tip)
	s.put("vendor/lib", "now a file\n")
	s.commit("a file in its place\n")
	s.publish(upstreamRef, s.cur)
	s.gitlink("vendor/lib")
	s.commit("a submodule in its place\n")
	s.finish()
	r = s.inspect()
	if c := byPath(t, r, "vendor/lib"); c.Status != 'T' || c.OldMode != 0o100644 || c.NewMode != 0o160000 || !hasFlag(c.Flags, FlagSubmodule) {
		t.Errorf("file to gitlink: %+v", c)
	}
}

func TestInspectExecutableBitIsReportedNotFlagged(t *testing.T) {
	s := newScene(t, "tools/run.sh", "#!/bin/sh\n")
	s.putMode("tools/run.sh", "100755", "#!/bin/sh\n")
	s.commit("make it executable\n")
	s.finish()
	r := s.inspect()
	c := byPath(t, r, "tools/run.sh")
	if c.Status != 'M' || c.OldMode != 0o100644 || c.NewMode != 0o100755 || c.OldID != c.NewID {
		t.Errorf("mode change: %+v", c)
	}
	if len(c.Flags) != 0 || len(r.Flagged) != 0 {
		t.Errorf("an executable bit is flagged: %+v %+v", c.Flags, r.Flagged)
	}
}
