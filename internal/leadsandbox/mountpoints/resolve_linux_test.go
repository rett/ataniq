//go:build linux

package mountpoints

import (
	"errors"
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

func TestResolveSourcesTreeGitThroughLink(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.mkdir(f.out+"/hooks", 0o755)
	f.remove(f.tree + "/.git/hooks")
	f.symlink(f.out+"/hooks", f.tree+"/.git/hooks")
	wantProblem(t, f.resolve().Report, ErrSymlink, f.tree+"/.git/hooks")
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
