//go:build linux

package mountpoints

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// resolverFor resolves the fixture's own paths on the real file system and
// leaves every other path (/usr, /etc/hosts and the like) as it is, so the
// test does not depend on the host's layout.
func (f *fixture) resolver() Resolver {
	if _, err := os.Lstat(f.gitMeta); err != nil {
		f.handMadeGitMeta() // the folder Prepare makes; this test does not run Prepare
	}
	return func(p string) (string, error) {
		if p == f.base || strings.HasPrefix(p, f.base+"/") {
			return filepath.EvalSymlinks(p)
		}
		return p, nil
	}
}

func (f *fixture) resolve() Resolution {
	f.t.Helper()
	res, err := ResolveSources(f.in, f.resolver())
	if err != nil {
		f.t.Fatalf("ResolveSources: %v", err)
	}
	return res
}

func TestResolveSourcesClean(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		f := newFixture(t, rt)
		res := f.resolve()
		if !res.OK() {
			t.Fatalf("%s: %v", rt, res.Err())
		}
		if len(res.Sources) == 0 {
			t.Fatal("no sources")
		}
		for _, s := range res.Sources {
			if s.Skipped || s.Real != s.Source {
				t.Errorf("%+v", s)
			}
		}
	}
}

func TestResolveSourcesSymlinkIntoNeverMounted(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture) // plants a link; the named source then resolves into a never-mounted folder
		kind  error
	}{
		{"skills folder is a link into the owner's real provider folder", func(f *fixture) {
			f.mkdir(f.base+"/owner/.claude/skills", 0o755)
			f.remove(f.in.LeadSkills)
			f.symlink(f.base+"/owner/.claude/skills", f.in.LeadSkills)
		}, ErrNeverMounted},
		{"instruction file is a link into .ssh", func(f *fixture) {
			f.mkdir(f.base+"/owner/.ssh", 0o700)
			f.write(f.base+"/owner/.ssh/id", "k")
			f.remove(f.in.InstructionFile)
			f.symlink(f.base+"/owner/.ssh/id", f.in.InstructionFile)
		}, ErrNeverMounted},
		{"a source is a link to the owner's checkout", func(f *fixture) {
			f.mkdir(f.base+"/owner/work/sub", 0o755)
			f.remove(f.in.QargiInstall)
			f.symlink(f.base+"/owner/work/sub", f.in.QargiInstall)
		}, ErrNeverMounted},
		{"a source is a link above a never-mounted folder", func(f *fixture) {
			f.mkdir(f.base+"/owner/.ssh", 0o700)
			f.remove(f.in.AgentInstall)
			f.symlink(f.base+"/owner", f.in.AgentInstall)
		}, ErrNeverMounted},
		{"a source is a link into the other runtime's config folder", func(f *fixture) {
			f.mkdir(f.state+"/leads/arc-lead/config/codex", 0o700)
			f.remove(f.in.AssignmentDir)
			f.symlink(f.state+"/leads/arc-lead/config/codex", f.in.AssignmentDir)
		}, ErrNeverMounted},
		{"a never-mounted path is itself a link", func(f *fixture) {
			// The list names a link; the real folder is what a source reaches.
			f.mkdir(f.base+"/data/ssh", 0o700)
			f.remove(f.base + "/owner/.ssh")
			f.mkdir(f.base+"/owner", 0o755)
			f.symlink(f.base+"/data/ssh", f.base+"/owner/.ssh")
			f.remove(f.in.ProjectionDir)
			f.symlink(f.base+"/data/ssh", f.in.ProjectionDir)
		}, ErrNeverMounted},
		{"a folder the other runtime's config folder lies in", func(f *fixture) {
			f.mkdir(f.state+"/leads/arc-lead/config/codex", 0o700)
			f.remove(f.in.SpecDir)
			f.symlink(f.state+"/leads/arc-lead/config", f.in.SpecDir)
		}, ErrNeverMounted},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			// The lexical profile check passes: the paths named look fine.
			if err := f.in.Validate(); err != nil {
				t.Fatal(err)
			}
			c.setup(f)
			if err := f.in.Validate(); err != nil {
				t.Fatalf("the lexical check must not see the link: %v", err)
			}
			res := f.resolve()
			if !res.Has(c.kind) {
				t.Fatalf("want %v, got %v", c.kind, res.Err())
			}
		})
	}
}

func TestResolveSourcesLinkInTheOwnedFolderOnTheWay(t *testing.T) {
	// A parent folder of a source is a link: the real path is what counts.
	f := newFixture(t, profile.Claude)
	f.mkdir(f.base+"/owner/.claude/inner", 0o755)
	f.write(f.base+"/owner/.claude/inner/settings.json", "{}")
	f.remove(f.owned + "/managed")
	f.symlink(f.base+"/owner/.claude/inner", f.owned+"/managed")
	res := f.resolve()
	if !res.Has(ErrNeverMounted) {
		t.Fatalf("got %v", res.Err())
	}
	var found bool
	for _, p := range res.Problems {
		if p.Path == f.in.ManagedSettings && strings.Contains(p.Detail, f.base+"/owner/.claude/inner/settings.json") {
			found = true
		}
	}
	if !found {
		t.Fatalf("the refusal does not name the real path: %v", res.Err())
	}
}

func TestResolveSourcesOverlap(t *testing.T) {
	t.Run("a read-only source is a link into the lead's home", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.mkdir(f.home+"/mine", 0o755)
		f.remove(f.in.LeadSkills)
		f.symlink(f.home+"/mine", f.in.LeadSkills)
		if err := f.in.Validate(); err != nil {
			t.Fatal(err)
		}
		res := f.resolve()
		wantProblem(t, res.Report, ErrOverlap, f.in.LeadSkills)
	})
	t.Run("a read-only source is a link into the lead's tree", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.remove(f.in.AssignmentDir)
		f.symlink(f.tree, f.in.AssignmentDir)
		wantProblem(t, f.resolve().Report, ErrOverlap, f.in.AssignmentDir)
	})
	t.Run("the config folder is a link to the home", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.remove(f.cfg)
		f.symlink(f.home, f.cfg)
		res := f.resolve()
		if !res.Has(ErrOverlap) {
			t.Fatalf("got %v", res.Err())
		}
	})
}

func TestResolveSourcesGitMetaThroughLink(t *testing.T) {
	// The git metadata folder is Ataniq's: a link in it is not special for
	// ResolveSources, which resolves it like any source and judges the real
	// path (CheckBindSources and Check refuse the link itself).
	t.Run("the folder is a link into the lead's tree", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.symlink(f.tree+"/.git", f.gitMeta)
		res := f.resolve()
		wantProblem(t, res.Report, ErrOverlap, f.gitMeta+"/config")
	})
	t.Run("an entry is a link into a never-mounted folder", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.handMadeGitMeta()
		f.mkdir(f.base+"/owner/.ssh", 0o700)
		f.remove(f.gitMeta + "/hooks")
		f.symlink(f.base+"/owner/.ssh", f.gitMeta+"/hooks")
		wantProblem(t, f.resolve().Report, ErrNeverMounted, f.gitMeta+"/hooks")
	})
	t.Run("a link in the tree's .git changes nothing", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.mkdir(f.out+"/hooks", 0o755)
		f.remove(f.tree + "/.git/hooks")
		f.symlink(f.out+"/hooks", f.tree+"/.git/hooks")
		if res := f.resolve(); !res.OK() {
			t.Fatalf("%v", res.Err())
		}
	})
}

func TestResolveSourcesMissingAndOptional(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.remove(f.in.ProbeBinary)
	res := f.resolve()
	wantProblem(t, res.Report, ErrMissing, f.in.ProbeBinary)

	// An optional host row that the host lacks is skipped, not refused.
	f = newFixture(t, profile.Claude)
	base := f.resolver()
	res, err := ResolveSources(f.in, func(p string) (string, error) {
		if p == "/etc/ssl" || p == "/etc/localtime" {
			return "", &fs.PathError{Op: "lstat", Path: p, Err: fs.ErrNotExist}
		}
		return base(p)
	})
	if err != nil || !res.OK() {
		t.Fatalf("%v %v", err, res.Err())
	}
	var skipped []string
	for _, s := range res.Sources {
		if s.Skipped {
			skipped = append(skipped, s.Source)
		}
	}
	if len(skipped) != 2 {
		t.Fatalf("skipped %v", skipped)
	}
	// A required host row that the host lacks is refused.
	res, err = ResolveSources(f.in, func(p string) (string, error) {
		if p == "/etc/passwd" {
			return "", fs.ErrNotExist
		}
		return base(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	wantProblem(t, res.Report, ErrMissing, "/etc/passwd")
}

func TestResolveSourcesResolverErrors(t *testing.T) {
	f := newFixture(t, profile.Claude)
	boom := errors.New("boom")
	base := f.resolver()
	res, err := ResolveSources(f.in, func(p string) (string, error) {
		switch p {
		case f.in.ProbeBinary:
			return "", boom
		case f.base + "/owner/.ssh":
			return "", boom
		case f.in.QargiInstall:
			return "relative/path", nil
		}
		return base(p)
	})
	if err != nil {
		t.Fatal(err)
	}
	wantProblem(t, res.Report, ErrUnreadable, f.in.ProbeBinary)
	wantProblem(t, res.Report, ErrUnreadable, f.base+"/owner/.ssh")
	wantProblem(t, res.Report, ErrUnreadable, f.in.QargiInstall)
}

func TestResolveSourcesAbsentNeverMountedBelowALink(t *testing.T) {
	// No listed path below the owner's folder exists, but the folder is a
	// link: the absent paths are resolved by their longest existing prefix, so
	// a source that is the real folder lies above the real .codex.
	f := newFixture(t, profile.Claude)
	f.mkdir(f.base+"/realhome", 0o755)
	f.remove(f.base + "/owner")
	f.symlink(f.base+"/realhome", f.base+"/owner")
	f.remove(f.in.QargiInstall)
	f.symlink(f.base+"/realhome", f.in.QargiInstall)
	if err := f.in.Validate(); err != nil {
		t.Fatalf("the lexical check must not see the link: %v", err)
	}
	wantProblem(t, f.resolve().Report, ErrNeverMounted, f.in.QargiInstall)
}

func TestResolveSourcesStateFoldersOnRealPaths(t *testing.T) {
	cases := []struct {
		name   string
		target string // below the fixture's state root; made as a folder
	}{
		{"another lead's home", "leads/other-lead/home"},
		{"another lead's provider login", "leads/other-lead/config/claude"},
		{"the folder of the other leads", "leads"},
		{"another project's mirror", "mirrors/other.git"},
		{"the folder of the mirrors", "mirrors"},
		{"the lead's holder folder", "leads/arc-lead/holder"},
		{"below the lead's holder folder", "leads/arc-lead/holder/state"},
		{"another role's mailbox", "mailbox/other-lead"},
		{"the folder of the mailboxes", "mailbox"},
		{"the lead's own mailbox, as another row", "mailbox/arc-lead"},
		{"below the lead's own mailbox", "mailbox/arc-lead/a-message"},
		{"another project's published folder", "pub/other"},
		{"the project's own published folder, as another row", "pub/ataniq"},
		{"below the project's own published folder", "pub/ataniq/a-feature/out"},
		{"a coordinator's tree", "coords/ataniq/a-feature/tree"},
		{"a coordinator's home", "coords/other/a-feature/home"},
		{"the folder of the coordinators", "coords"},
		{"the coordinators' login", "coord-login/ataniq/claude"},
		{"the folder of the coordinators' logins", "coord-login"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			f.mkdir(f.state+"/"+c.target, 0o700)
			f.remove(f.in.LeadSkills)
			f.symlink(f.state+"/"+c.target, f.in.LeadSkills)
			if err := f.in.Validate(); err != nil {
				t.Fatalf("the lexical check must not see the link: %v", err)
			}
			wantProblem(t, f.resolve().Report, ErrNeverMounted, f.in.LeadSkills)
		})
	}
	// A row of the host may resolve through the host's own links, so its real
	// path is the only place these rules can catch it.
	for _, target := range []string{"coords/ataniq/a-feature/home", "coord-login/ataniq/codex", "leads/arc-lead/holder", "mailbox/other-lead", "pub/other/a-feature/out"} {
		t.Run("a row of the host that resolves to "+target, func(t *testing.T) {
			f := newFixture(t, profile.Codex)
			f.mkdir(f.state+"/"+target, 0o700)
			base := f.resolver()
			res, err := ResolveSources(f.in, func(p string) (string, error) {
				if p == "/etc/localtime" {
					return f.state + "/" + target, nil
				}
				return base(p)
			})
			if err != nil {
				t.Fatal(err)
			}
			wantProblem(t, res.Report, ErrNeverMounted, "/etc/localtime")
		})
	}
	t.Run("the mailbox and the published folder are fine as their own rows", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		res := f.resolve()
		if !res.OK() {
			t.Fatal(res.Err())
		}
		got := map[string]string{}
		for _, s := range res.Sources {
			got[s.Inside] = s.Real
		}
		l := f.layout()
		for inside, real := range map[string]string{"/ataniq/mailbox": l.Mailbox(), "/coord": l.Published(), "/ataniq/requests": l.Requests(), "/ataniq/mail": l.Mail()} {
			if got[inside] != real {
				t.Errorf("%s resolves to %q, want %q", inside, got[inside], real)
			}
		}
	})
	t.Run("the lead's own folders and the project's mirror are fine", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.mkdir(f.home+"/mine", 0o755) // below the lead's own state: not this check's business
		res := f.resolve()
		if !res.OK() {
			t.Fatal(res.Err())
		}
	})
	t.Run("a state folder that resolves to an unusable path is a problem", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		base := f.resolver()
		res, err := ResolveSources(f.in, func(p string) (string, error) {
			if p == f.state+"/mirrors" {
				return "relative/mirrors", nil
			}
			return base(p)
		})
		if err != nil {
			t.Fatal(err)
		}
		wantProblem(t, res.Report, ErrUnreadable, f.state+"/mirrors")
	})
}

func TestResolveSourcesUnusableNeverMountedAnswer(t *testing.T) {
	f := newFixture(t, profile.Claude)
	base := f.resolver()
	for _, bad := range []string{"relative/ssh", f.base + "/owner/../x", f.base + "/ow\x01ner/ssh", "/" + strings.Repeat("a", 5000)} {
		res, err := ResolveSources(f.in, func(p string) (string, error) {
			if p == f.base+"/owner/.ssh" {
				return bad, nil
			}
			return base(p)
		})
		if err != nil {
			t.Fatal(err)
		}
		if res.OK() {
			t.Errorf("%.40q: an unusable answer was dropped", bad)
		}
		wantProblem(t, res.Report, ErrUnreadable, f.base+"/owner/.ssh")
	}
}

func TestResolveSourcesRefusesBadCalls(t *testing.T) {
	f := newFixture(t, profile.Claude)
	if _, err := ResolveSources(f.in, nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil resolver: %v", err)
	}
	f.in.NeverMounted = nil
	if _, err := ResolveSources(f.in, RealResolver); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad inputs: %v", err)
	}
}

func TestRealResolverEvaluatesLinks(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.mkdir(f.out+"/real", 0o755)
	f.symlink(f.out+"/real", f.out+"/link")
	got, err := RealResolver(f.out + "/link")
	if err != nil || got != f.out+"/real" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := RealResolver(f.out + "/absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Lstat(f.out + "/link"); err != nil {
		t.Fatal(err)
	}
}

// A never-mounted path that is a dangling link (its target is not there yet)
// is resolved through the link: the owner may create the target later, and the
// folder then appears under its real name.
func TestResolveSourcesDanglingNeverMountedLink(t *testing.T) {
	cases := []struct {
		name   string
		target func(f *fixture) string // the target of the link at the owner's .codex
	}{
		{"absolute", func(f *fixture) string { return f.base + "/data/codex" }},
		{"relative", func(f *fixture) string { return "../data/codex" }},
		{"through a second dangling link", func(f *fixture) string {
			f.symlink(f.base+"/data/codex", f.base+"/data/hop")
			return f.base + "/data/hop"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			f.mkdir(f.base+"/data", 0o755)
			f.mkdir(f.base+"/owner", 0o755)
			f.symlink(c.target(f), f.base+"/owner/.codex")
			// A source that is a link to the folder that will hold the real
			// .codex.
			f.remove(f.in.QargiInstall)
			f.symlink(f.base+"/data", f.in.QargiInstall)
			if err := f.in.Validate(); err != nil {
				t.Fatalf("the lexical check must not see the link: %v", err)
			}
			wantProblem(t, f.resolve().Report, ErrNeverMounted, f.in.QargiInstall)
		})
	}
}

func TestRealOfDanglingLinks(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.mkdir(f.base+"/data", 0o755)
	f.symlink("../data/codex", f.out+"/rel")
	f.symlink(f.base+"/data/codex", f.out+"/abs")
	f.symlink("loop2", f.out+"/loop1")
	f.symlink("loop1", f.out+"/loop2")
	for i := 0; i < maxLinkHops+5; i++ {
		f.symlink(fmt.Sprintf("chain%d", i+1), fmt.Sprintf("%s/chain%d", f.out, i))
	}
	f.symlink("a/../b", f.out+"/dotdot")
	for _, c := range []struct {
		name, path, want string
	}{
		{"relative target", f.out + "/rel", f.base + "/data/codex"},
		{"absolute target", f.out + "/abs", f.base + "/data/codex"},
		{"a path below the dangling link", f.out + "/abs/sub/x", f.base + "/data/codex/sub/x"},
		{"no link at all", f.out + "/absent/x", f.out + "/absent/x"},
	} {
		got, err := realOf(f.resolver(), os.Readlink, c.path)
		if err != nil || got != c.want {
			t.Errorf("%s: realOf = %q, %v; want %q", c.name, got, err, c.want)
		}
	}
	for _, c := range []struct{ name, path, want string }{
		{"a loop of links", f.out + "/loop1", "too many links"},
		{"a chain of dangling links", f.out + "/chain0", "symbolic links"},
		{"a \"..\" after a component", f.out + "/dotdot", "\"..\""},
	} {
		if got, err := realOf(f.resolver(), os.Readlink, c.path); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: realOf = %q, %v; want an error naming %q", c.name, got, err, c.want)
		}
	}
	// A link that cannot be read is an error, not the lexical path.
	boom := errors.New("boom")
	if got, err := realOf(f.resolver(), func(string) (string, error) { return "", boom }, f.out+"/rel"); !errors.Is(err, boom) {
		t.Errorf("a link that cannot be read: %q, %v", got, err)
	}
}

// A dangling link that cannot be followed is reported, not resolved by its
// lexical path.
func TestResolveSourcesDanglingNeverMountedLinkUnreadable(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.mkdir(f.base+"/owner", 0o755)
	f.symlink("x/../y", f.base+"/owner/.codex")
	res := f.resolve()
	wantProblem(t, res.Report, ErrUnreadable, f.base+"/owner/.codex")
}

// Rule G1: a row Ataniq supplies must name its real path, because bubblewrap
// binds the path the plan names and follows a link in it. ResolveSources
// refuses a row whose real path differs, naming the row, whatever the role.
func TestResolveSourcesRefusesALinkOnAnAtaniqSource(t *testing.T) {
	cases := []struct {
		name   string
		plant  func(f *fixture)
		inside string // the row the problem names
		source func(f *fixture) string
	}{
		{"agent install is a link", func(f *fixture) {
			f.mkdir(f.base+"/real-agent", 0o755)
			f.remove(f.in.AgentInstall)
			f.symlink(f.base+"/real-agent", f.in.AgentInstall)
		}, "/ataniq/agent", func(f *fixture) string { return f.in.AgentInstall }},
		{"probe is a link to a file", func(f *fixture) {
			f.write(f.base+"/real-probe", "x")
			f.remove(f.in.ProbeBinary)
			f.symlink(f.base+"/real-probe", f.in.ProbeBinary)
		}, "/ataniq/probe", func(f *fixture) string { return f.in.ProbeBinary }},
		{"a parent of the managed settings is a link", func(f *fixture) {
			if err := os.Rename(f.owned+"/managed", f.base+"/managed-moved"); err != nil {
				f.t.Fatal(err)
			}
			f.symlink(f.base+"/managed-moved", f.owned+"/managed")
		}, "/etc/claude-code/managed-settings.json", func(f *fixture) string { return f.in.ManagedSettings }},
		{"the state's mirrors folder is a link", func(f *fixture) {
			if err := os.Rename(f.state+"/mirrors", f.base+"/mirrors-moved"); err != nil {
				f.t.Fatal(err)
			}
			f.symlink(f.base+"/mirrors-moved", f.state+"/mirrors")
		}, "/ataniq/mirror/ataniq.git", func(f *fixture) string { return f.state + "/mirrors/ataniq.git" }},
		{"the home is a link", func(f *fixture) {
			if err := os.Rename(f.home, f.base+"/home-moved"); err != nil {
				f.t.Fatal(err)
			}
			f.symlink(f.base+"/home-moved", f.home)
		}, "/home/lead", func(f *fixture) string { return f.home }},
		{"the lead's state folder is a link", func(f *fixture) {
			if err := os.Rename(f.state+"/leads/arc-lead", f.base+"/lead-moved"); err != nil {
				f.t.Fatal(err)
			}
			f.symlink(f.base+"/lead-moved", f.state+"/leads/arc-lead")
		}, "/work/ataniq/.git/config", func(f *fixture) string { return f.gitMeta + "/config" }},
		{"an extra placeholder source is a link", func(f *fixture) {
			f.write(f.base+"/real-envrc", "x")
			f.remove(f.in.ExtraPlaceholders[0].Source)
			f.symlink(f.base+"/real-envrc", f.in.ExtraPlaceholders[0].Source)
		}, "/work/ataniq/.envrc", func(f *fixture) string { return f.in.ExtraPlaceholders[0].Source }},
	}
	for _, rt := range profile.Runtimes() {
		for _, c := range cases {
			t.Run(string(rt)+" "+c.name, func(t *testing.T) {
				f := newFixture(t, rt)
				f.handMadeGitMeta()
				if err := f.in.Validate(); err != nil {
					t.Fatal(err)
				}
				c.plant(f)
				if err := f.in.Validate(); err != nil {
					t.Fatalf("the lexical check must not see the link: %v", err)
				}
				inside := c.inside
				if rt == profile.Codex && inside == "/etc/claude-code/managed-settings.json" {
					inside = "/etc/codex/managed-config.toml"
				}
				res := f.resolve()
				var found *Problem
				for _, p := range res.Problems {
					if p.Kind == ErrSymlink && p.Inside == inside && p.Path == c.source(f) {
						found = p
					}
				}
				if found == nil {
					t.Fatalf("no symbolic link problem for the row %s at %s: %v", inside, c.source(f), res.Err())
				}
				if !strings.Contains(found.Detail, "real path") || !strings.Contains(found.Detail, inside) {
					t.Errorf("the problem does not name the row and the real path: %q", found.Detail)
				}
				// The row is still listed, with the real path it resolved to.
				for _, s := range res.Sources {
					if s.Inside == inside && (s.Real == s.Source || s.Real == "") {
						t.Errorf("the row %s does not report its real path: %+v", inside, s)
					}
				}
			})
		}
	}
}

// A row of the host may resolve through a link of the host: its real path is
// reported, with the role, and nothing is refused; a link is refused for any
// other row.
func TestResolveSourcesAllowsALinkOnARowOfTheHost(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.handMadeGitMeta()
	f.mkdir(f.base+"/zoneinfo", 0o755)
	f.write(f.base+"/zoneinfo/UTC", "tz")
	base := f.resolver()
	resolve := func(p string) (string, error) {
		if p == "/etc/localtime" {
			return f.base + "/zoneinfo/UTC", nil
		}
		return base(p)
	}
	res, err := ResolveSources(f.in, resolve)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() {
		t.Fatalf("%v", res.Err())
	}
	found := false
	for _, s := range res.Sources {
		if s.Inside != "/etc/localtime" {
			continue
		}
		found = true
		if s.Role != profile.RoleHost || s.Source != "/etc/localtime" || s.Real != f.base+"/zoneinfo/UTC" || s.Bound() != s.Real {
			t.Errorf("the host row: %+v, bound %s", s, s.Bound())
		}
	}
	if !found {
		t.Fatal("no /etc/localtime row")
	}
	for _, s := range res.Sources {
		if s.Role == "" {
			t.Errorf("the row %s has no role", s.Inside)
		}
		if s.Role != profile.RoleHost && s.Bound() != s.Source {
			t.Errorf("the row %s is bound from %s, not its source %s", s.Inside, s.Bound(), s.Source)
		}
	}
}
