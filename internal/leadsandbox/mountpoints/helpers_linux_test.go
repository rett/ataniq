//go:build linux

package mountpoints

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// fixture is a lead's folders under a temporary state root, with a fake clone
// built by hand (no git binary) and inputs that name them.
type fixture struct {
	t       *testing.T
	base    string // the real path of the temporary folder
	state   string
	owned   string
	home    string
	cfg     string
	tree    string
	gitMeta string // Ataniq's copy of the protected git files (made by Prepare)
	out     string // a folder outside every lead folder, for link targets
	in      profile.Inputs
}

func newFixture(t *testing.T, rt profile.Runtime) *fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, base: base, state: base + "/state", owned: base + "/owned", out: base + "/outside"}
	l := profile.Layout{StateRoot: f.state, Role: "arc-lead", Project: "ataniq"}
	f.home, f.tree = l.Home(), l.Tree()
	f.gitMeta = l.GitMeta()
	f.cfg, _ = l.ConfigDir(rt)
	for _, d := range []string{f.home, f.cfg, f.tree, l.Requests(), l.Mail(), l.Mailbox(), l.Published()} {
		f.mkdir(d, 0o700)
	}
	f.mkdir(f.out, 0o755)
	f.mkdir(f.state+"/mirrors/ataniq.git", 0o700)
	f.fakeClone(true)

	own := func(p string, dir bool) string {
		p = f.owned + "/" + p
		if dir {
			f.mkdir(p, 0o755)
		} else {
			f.mkdir(filepath.Dir(p), 0o755)
			f.write(p, "x")
		}
		return p
	}
	f.in = profile.Inputs{
		StateRoot:             f.state,
		Role:                  "arc-lead",
		Project:               "ataniq",
		Runtime:               rt,
		RuntimeVersion:        "2.1.0",
		Identity:              profile.IdentityRoot,
		AgentInstall:          own("agent", true),
		ProbeBinary:           own("probe", false),
		SpecDir:               own("spec", true),
		ManagedSettings:       own("managed/settings.json", false),
		ManagedSettingsSHA256: strings.Repeat("c1", 32),
		LeadSkills:            own("skills", true),
		InstructionFile:       own("instructions/"+rt.InstructionFileName(), false),
		GitIdentityFile:       own("git/config", false),
		GitMeta:               l.GitMeta(),
		QargiInstall:          own("qargi", true),
		AssignmentDir:         own("assignment", true),
		ProjectionDir:         own("projection", true),
		ResolvConf:            own("resolv.conf", false),
		ExtraPlaceholders: []profile.Placeholder{
			{Rel: ".envrc", Source: own("ph/envrc", false)},
			{Rel: ".vscode", Source: own("ph/vscode", true), Dir: true},
			{Rel: "tools/local/x.txt", Source: own("ph/x.txt", false)},
		},
		NeverMounted: []string{base + "/owner/work", base + "/owner/.claude", base + "/owner/.codex", base + "/owner/.ssh"},
		Term:         "xterm-256color",
	}
	if rt == profile.Codex {
		f.in.ManagedSettingsInside = "/etc/codex/managed-config.toml"
	}
	if err := f.in.Validate(); err != nil {
		t.Fatalf("fixture inputs: %v", err)
	}
	return f
}

// layout is the host layout of the fixture's lead.
func (f *fixture) layout() profile.Layout { return f.in.Layout() }

func (f *fixture) mkdir(p string, mode os.FileMode) {
	f.t.Helper()
	if err := os.MkdirAll(p, mode); err != nil {
		f.t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) write(p, data string) {
	f.t.Helper()
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) symlink(target, link string) {
	f.t.Helper()
	if err := os.Symlink(target, link); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) remove(p string) {
	f.t.Helper()
	if err := os.RemoveAll(p); err != nil {
		f.t.Fatal(err)
	}
}

// fakeClone builds the .git folder of a full clone by hand.
func (f *fixture) fakeClone(hooks bool) {
	f.t.Helper()
	g := f.tree + "/.git"
	f.remove(g)
	for _, d := range []string{g + "/objects", g + "/refs", g + "/info"} {
		f.mkdir(d, 0o755)
	}
	if hooks {
		f.mkdir(g+"/hooks", 0o755)
	}
	f.write(g+"/HEAD", "ref: refs/heads/main\n")
	f.write(g+"/config", "[core]\n\trepositoryformatversion = 0\n")
	f.write(g+"/info/exclude", "# comment\n")
}

func (f *fixture) prepare() {
	f.t.Helper()
	if err := Prepare(f.in, PrepareOptions{NoLeadHasRun: true}); err != nil {
		f.t.Fatalf("Prepare: %v", err)
	}
}

func (f *fixture) check() Report {
	f.t.Helper()
	rep, err := Check(f.in)
	if err != nil {
		f.t.Fatalf("Check: %v", err)
	}
	return rep
}

// exclude is the exclude file in Ataniq's copy, the one git inside the sandbox
// reads.
func (f *fixture) exclude() string {
	f.t.Helper()
	b, err := os.ReadFile(f.gitMeta + "/info/exclude")
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// cloneExclude is the clone's own exclude file.
func (f *fixture) cloneExclude() string {
	f.t.Helper()
	b, err := os.ReadFile(f.tree + "/.git/info/exclude")
	if err != nil {
		f.t.Fatal(err)
	}
	return string(b)
}

// metaExclude is the path of the exclude file of Ataniq's copy.
func (f *fixture) metaExclude() string { return f.gitMeta + "/info/exclude" }

// listing describes every entry under dir (without following links): type,
// permission bits, and the content of a regular file or the target of a link.
func listing(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		line := fmt.Sprintf("%s %s", strings.TrimPrefix(p, dir), info.Mode())
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			tgt, _ := os.Readlink(p)
			line += " -> " + tgt
		case info.Mode().IsRegular():
			b, _ := os.ReadFile(p)
			line += fmt.Sprintf(" %q", b)
		}
		out = append(out, line)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

func sameListing(t *testing.T, before, after []string) {
	t.Helper()
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Fatalf("the folders changed\nbefore:\n  %s\nafter:\n  %s", strings.Join(before, "\n  "), strings.Join(after, "\n  "))
	}
}

func wantKind(t *testing.T, err error, kind error) {
	t.Helper()
	if err == nil {
		t.Fatalf("no error, want %v", kind)
	}
	if !errors.Is(err, kind) {
		t.Fatalf("got %v, want kind %v", err, kind)
	}
}

func problemPaths(r Report, kind error) []string {
	var out []string
	for _, p := range r.Problems {
		if errors.Is(p, kind) {
			out = append(out, p.Path)
		}
	}
	return out
}

func wantProblem(t *testing.T, r Report, kind error, path string) {
	t.Helper()
	for _, p := range r.Problems {
		if errors.Is(p, kind) && p.Path == path {
			return
		}
	}
	t.Fatalf("no %v problem at %s; got %v", kind, path, r.Err())
}

// handMadeGitMeta makes the folder Prepare makes, by hand, for a test that
// resolves sources without a Prepare: a config file, an empty hooks folder and
// an info folder with an exclude file.
func (f *fixture) handMadeGitMeta() {
	f.t.Helper()
	f.mkdir(f.gitMeta, 0o700)
	f.mkdir(f.gitMeta+"/hooks", 0o700)
	f.mkdir(f.gitMeta+"/info", 0o700)
	f.write(f.gitMeta+"/config", "[core]\n")
	f.write(f.gitMeta+"/info/exclude", "# comment\n")
}

// ownRows are rows of a host that is not the host's own: each is checked by
// its own path (its Source, which equals its Real). The inside paths are
// invented, "/row/<n>" with n from 1.
func ownRows(paths ...string) []ResolvedSource {
	rows := make([]ResolvedSource, len(paths))
	for i, p := range paths {
		rows[i] = ResolvedSource{Inside: fmt.Sprintf("/row/%d", i+1), Source: p, Real: p, Role: profile.RoleAgent}
	}
	return rows
}
