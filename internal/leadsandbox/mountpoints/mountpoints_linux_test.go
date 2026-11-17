//go:build linux

package mountpoints

import (
	"errors"
	"os"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

func TestListPoints(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := newFixture(t, rt)
			pts, err := List(f.in)
			if err != nil {
				t.Fatal(err)
			}
			cfgName := rt.ConfigDirName()
			want := []Point{
				{Inside: "/home/lead/" + cfgName, HostPath: f.home + "/" + cfgName, Root: RootHome, RootDir: f.home, Rel: cfgName, Dir: true},
				{Inside: "/home/lead/" + cfgName + "/skills", HostPath: f.cfg + "/skills", Root: RootConfig, RootDir: f.cfg, Rel: "skills", Dir: true},
				{Inside: "/home/lead/" + cfgName + "/" + rt.InstructionFileName(), HostPath: f.cfg + "/" + rt.InstructionFileName(), Root: RootConfig, RootDir: f.cfg, Rel: rt.InstructionFileName()},
				{Inside: "/work/ataniq/.git/config", HostPath: f.tree + "/.git/config", Root: RootTree, RootDir: f.tree, Rel: ".git/config", Git: true},
				{Inside: "/work/ataniq/.git/hooks", HostPath: f.tree + "/.git/hooks", Root: RootTree, RootDir: f.tree, Rel: ".git/hooks", Dir: true, Git: true},
				{Inside: "/work/ataniq/.git/info", HostPath: f.tree + "/.git/info", Root: RootTree, RootDir: f.tree, Rel: ".git/info", Dir: true, Git: true},
				{Inside: "/work/ataniq/.envrc", HostPath: f.tree + "/.envrc", Root: RootTree, RootDir: f.tree, Rel: ".envrc", Placeholder: true},
				{Inside: "/work/ataniq/.vscode", HostPath: f.tree + "/.vscode", Root: RootTree, RootDir: f.tree, Rel: ".vscode", Dir: true, Placeholder: true},
				{Inside: "/work/ataniq/tools/local/x.txt", HostPath: f.tree + "/tools/local/x.txt", Root: RootTree, RootDir: f.tree, Rel: "tools/local/x.txt", Placeholder: true},
			}
			if len(pts) != len(want) {
				t.Fatalf("got %d points, want %d: %+v", len(pts), len(want), pts)
			}
			for i := range want {
				if pts[i] != want[i] {
					t.Errorf("point %d:\n got %+v\nwant %+v", i, pts[i], want[i])
				}
			}
		})
	}
}

func TestListRefusesInvalidInputsAndPatternPaths(t *testing.T) {
	f := newFixture(t, profile.Claude)
	bad := f.in
	bad.Role = "Not Valid"
	if _, err := List(bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("got %v, want ErrInvalid", err)
	}
	var p *Problem
	if !errors.As(err2(List(bad)), &p) || p.Err == nil {
		t.Fatalf("the profile's refusal is not kept: %v", p)
	}
	for _, rel := range []string{"a*b", "a?b", "a[b", `a\b`, "a b "} {
		in := f.in
		in.ExtraPlaceholders = []profile.Placeholder{{Rel: rel, Source: f.owned + "/ph/envrc"}}
		if _, err := List(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: got %v, want ErrInvalid", rel, err)
		}
		if err := Prepare(in, PrepareOptions{NoLeadHasRun: true}); !errors.Is(err, ErrInvalid) {
			t.Errorf("Prepare %q: got %v, want ErrInvalid", rel, err)
		}
	}
}

func err2[T any](_ T, err error) error { return err }

func TestPrepareThenCheckClean(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := newFixture(t, rt)
			if rep := f.check(); rep.OK() || !rep.Has(ErrMissing) {
				t.Fatalf("a fresh clone must fail the check before Prepare: %v", rep.Err())
			}
			f.prepare()
			if rep := f.check(); !rep.OK() {
				t.Fatalf("check after prepare: %v", rep.Err())
			}
			cfgName := rt.ConfigDirName()
			for _, d := range []string{f.home + "/" + cfgName, f.cfg + "/skills", f.tree + "/.vscode", f.tree + "/tools/local"} {
				if fi, err := os.Lstat(d); err != nil || !fi.IsDir() {
					t.Errorf("%s: %v", d, err)
				}
			}
			for _, p := range []string{f.cfg + "/" + rt.InstructionFileName(), f.tree + "/.envrc", f.tree + "/tools/local/x.txt"} {
				fi, err := os.Lstat(p)
				if err != nil || !fi.Mode().IsRegular() || fi.Size() != 0 {
					t.Errorf("%s: want an empty regular file, got %v %v", p, fi, err)
				}
			}
			if fi, err := os.Lstat(f.tree + "/.git/hooks"); err != nil || !fi.IsDir() {
				t.Errorf(".git/hooks was not made: %v", err)
			}
			if got, want := f.exclude(), "# comment\n/.envrc\n/.vscode\n/tools/local/x.txt\n"; got != want {
				t.Errorf("exclude:\n got %q\nwant %q", got, want)
			}
			if err := CheckOwn(t, f); err != nil {
				t.Fatalf("own clone: %v", err)
			}
		})
	}
}

// CheckOwn runs the own-clone check as the current user.
func CheckOwn(t *testing.T, f *fixture) error {
	t.Helper()
	rep, err := CheckOwnClone(f.in, uint32(os.Getuid()))
	if err != nil {
		return err
	}
	return rep.Err()
}

func TestPrepareIsIdempotent(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.prepare()
	before := listing(t, f.base)
	f.prepare()
	f.prepare()
	sameListing(t, before, listing(t, f.base))
	if rep := f.check(); !rep.OK() {
		t.Fatal(rep.Err())
	}
}

func TestPrepareKeepsExistingEntries(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.mkdir(f.tree+"/.vscode", 0o700) // a folder of the right type, another mode
	f.write(f.tree+"/.envrc", "kept")
	f.mkdir(f.tree+"/tools/local", 0o755)
	f.write(f.cfg+"/CLAUDE.md", "kept too")
	f.prepare()
	for p, want := range map[string]string{f.tree + "/.envrc": "kept", f.cfg + "/CLAUDE.md": "kept too"} {
		if b, _ := os.ReadFile(p); string(b) != want {
			t.Errorf("%s: got %q, want %q", p, b, want)
		}
	}
	if fi, _ := os.Lstat(f.tree + "/.vscode"); fi.Mode().Perm() != 0o700 {
		t.Errorf("an existing folder was changed: %v", fi.Mode())
	}
}

func TestPrepareRefusesWithoutStatement(t *testing.T) {
	f := newFixture(t, profile.Claude)
	before := listing(t, f.base)
	for _, opts := range []PrepareOptions{{}, {NoLeadHasRun: false}} {
		wantKind(t, Prepare(f.in, opts), ErrNoStatement)
	}
	sameListing(t, before, listing(t, f.base))
}

func TestPrepareRefusesInvalidInputs(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.in.NeverMounted = nil
	wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrInvalid)
	if _, err := Check(f.in); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Check: got %v", err)
	}
}

func TestPrepareRefusesMissingFolders(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.remove(f.home)
	before := listing(t, f.base)
	wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrMissing)
	sameListing(t, before, listing(t, f.base))
	rep := f.check()
	wantProblem(t, rep, ErrMissing, f.home)
}

// plant describes one mount point that is replaced by a link.
type plant struct {
	name string
	// at is the path (below the fixture) the link takes; the mount point it
	// affects is below or at it.
	at string
	// dir says the link points at a folder.
	dir bool
}

var plantNames = []string{"file target", "folder target", "middle component", "git hooks", "git info", "git config", "config instruction file", "config skills", "home config mount point"}

func plants(f *fixture) []plant {
	return []plant{
		{"file target", f.tree + "/.envrc", false},
		{"folder target", f.tree + "/.vscode", true},
		{"middle component", f.tree + "/tools", true},
		{"git hooks", f.tree + "/.git/hooks", true},
		{"git info", f.tree + "/.git/info", true},
		{"git config", f.tree + "/.git/config", false},
		{"config instruction file", f.cfg + "/CLAUDE.md", false},
		{"config skills", f.cfg + "/skills", true},
		{"home config mount point", f.home + "/.claude", true},
	}
}

func TestSymlinkAtMountPointRefusedByPrepare(t *testing.T) {
	for i, name := range plantNames {
		for _, dangling := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "", true: " dangling"}[dangling], func(t *testing.T) {
				f := newFixture(t, profile.Claude)
				pl := plants(f)[i]
				if pl.name != name {
					t.Fatal("plants out of step")
				}
				// A link to something outside, which must stay as it is.
				target := f.out + "/target"
				if !dangling {
					if pl.dir {
						f.mkdir(target, 0o755)
					} else {
						f.write(target, "outside content")
					}
				}
				f.remove(pl.at)
				f.symlink(target, pl.at)
				before := listing(t, f.base)
				err := Prepare(f.in, PrepareOptions{NoLeadHasRun: true})
				wantKind(t, err, ErrSymlink)
				sameListing(t, before, listing(t, f.base))
				if dangling {
					if _, err := os.Lstat(target); err == nil {
						t.Fatal("the target of a dangling link was created")
					}
				}
			})
		}
	}
}

func TestSymlinkAtMountPointFoundByCheck(t *testing.T) {
	for i, name := range plantNames {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			f.prepare()
			pl := plants(f)[i]
			target := f.out + "/target"
			if pl.dir {
				f.mkdir(target+"/inner", 0o755)
			} else {
				f.write(target, "outside content")
			}
			f.remove(pl.at)
			f.symlink(target, pl.at)
			before := listing(t, f.base)
			rep := f.check()
			if !rep.Has(ErrSymlink) {
				t.Fatalf("a link at %s was not found: %v", pl.at, rep.Err())
			}
			if rep.Has(ErrMissing) {
				t.Errorf("a link must not read as a missing entry: %v", rep.Err())
			}
			sameListing(t, before, listing(t, f.base))
		})
	}
}

func TestPlantedLinkAtPlaceholderParentNeverWritesOutside(t *testing.T) {
	// tools is a link to a folder outside: tools/local/x.txt must not be
	// created there, by Prepare run before or after the plant.
	f := newFixture(t, profile.Claude)
	f.symlink(f.out, f.tree+"/tools")
	wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrSymlink)
	if names, _ := os.ReadDir(f.out); len(names) != 0 {
		t.Fatalf("something was created outside: %v", names)
	}
}

func TestSymlinkAsRootFolderRefused(t *testing.T) {
	for _, name := range []string{"tree", "home", "config"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			dir := map[string]string{"tree": f.tree, "home": f.home, "config": f.cfg}[name]
			real := f.base + "/moved"
			if err := os.Rename(dir, real); err != nil {
				t.Fatal(err)
			}
			f.symlink(real, dir)
			wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrSymlink)
			if rep := f.check(); !rep.Has(ErrSymlink) {
				t.Fatalf("check: %v", rep.Err())
			}
		})
	}
}

func TestSymlinkInPathBelowStateRootRefused(t *testing.T) {
	f := newFixture(t, profile.Claude)
	real := f.base + "/moved-lead"
	if err := os.Rename(f.state+"/leads/arc-lead", real); err != nil {
		t.Fatal(err)
	}
	f.symlink(real, f.state+"/leads/arc-lead")
	wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrSymlink)
	if rep := f.check(); !rep.Has(ErrSymlink) {
		t.Fatalf("check: %v", rep.Err())
	}
}

func TestWrongTypeRefused(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture)
		at    func(f *fixture) string
	}{
		{"file where a folder is needed", func(f *fixture) { f.write(f.tree+"/.vscode", "x") }, func(f *fixture) string { return f.tree + "/.vscode" }},
		{"folder where a file is needed", func(f *fixture) { f.mkdir(f.tree+"/.envrc", 0o755) }, func(f *fixture) string { return f.tree + "/.envrc" }},
		{"file as a parent folder", func(f *fixture) { f.write(f.tree+"/tools", "x") }, func(f *fixture) string { return f.tree + "/tools/local/x.txt" }},
		{"folder for the instruction file", func(f *fixture) { f.mkdir(f.cfg+"/CLAUDE.md", 0o755) }, func(f *fixture) string { return f.cfg + "/CLAUDE.md" }},
		{"file for the skills folder", func(f *fixture) { f.write(f.cfg+"/skills", "x") }, func(f *fixture) string { return f.cfg + "/skills" }},
		{"folder for git config", func(f *fixture) { f.remove(f.tree + "/.git/config"); f.mkdir(f.tree+"/.git/config", 0o755) }, func(f *fixture) string { return f.tree + "/.git/config" }},
		{"file for git info", func(f *fixture) { f.remove(f.tree + "/.git/info"); f.write(f.tree+"/.git/info", "x") }, func(f *fixture) string { return f.tree + "/.git/info" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			c.setup(f)
			before := listing(t, f.base)
			wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrWrongType)
			sameListing(t, before, listing(t, f.base))
			f.prepareIgnoringErrors()
			rep := f.check()
			wantProblem(t, rep, ErrWrongType, c.at(f))
		})
	}
}

// prepareIgnoringErrors makes what it can, to set up a Check on a tree with a
// problem in it: it makes the other mount points by hand.
func (f *fixture) prepareIgnoringErrors() {
	f.t.Helper()
	pts, err := List(f.in)
	if err != nil {
		f.t.Fatal(err)
	}
	for _, p := range pts {
		if _, err := os.Lstat(p.HostPath); err == nil {
			continue
		}
		if p.Dir {
			os.MkdirAll(p.HostPath, 0o755)
		} else if os.MkdirAll(p.HostPath[:strings.LastIndex(p.HostPath, "/")], 0o755) == nil {
			os.WriteFile(p.HostPath, nil, 0o644)
		}
	}
}

func TestCloneEntriesNeverInvented(t *testing.T) {
	for _, name := range []string{"config", "info", ".git"} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			f.remove(f.tree + "/.git/" + name)
			if name == ".git" {
				f.remove(f.tree + "/.git")
			}
			before := listing(t, f.base)
			wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrMissing)
			sameListing(t, before, listing(t, f.base))
		})
	}
}

func TestMissingHooksIsMadeEmpty(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.remove(f.tree + "/.git/hooks")
	f.prepare()
	names, err := os.ReadDir(f.tree + "/.git/hooks")
	if err != nil || len(names) != 0 {
		t.Fatalf("hooks: %v %v", names, err)
	}
}

func TestExcludeFile(t *testing.T) {
	t.Run("created when absent", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.remove(f.tree + "/.git/info/exclude")
		f.prepare()
		if got, want := f.exclude(), "/.envrc\n/.vscode\n/tools/local/x.txt\n"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("only the missing lines are appended", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.write(f.tree+"/.git/info/exclude", "*.log\n/.vscode\n")
		f.prepare()
		if got, want := f.exclude(), "*.log\n/.vscode\n/.envrc\n/tools/local/x.txt\n"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("a missing final newline is supplied", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.write(f.tree+"/.git/info/exclude", "*.log")
		f.prepare()
		if got, want := f.exclude(), "*.log\n/.envrc\n/.vscode\n/tools/local/x.txt\n"; got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("a negation of the line does not count as listed", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.write(f.tree+"/.git/info/exclude", "/.envrc\n!/.envrc\n/.vscode\n/tools/local/x.txt\n")
		rep := f.check()
		if got := problemPaths(rep, ErrNotExcluded); len(got) != 1 || got[0] != f.tree+"/.envrc" {
			t.Fatalf("got %v", got)
		}
		f.prepare()
		if rep := f.check(); !rep.OK() {
			t.Fatal(rep.Err())
		}
	})
	t.Run("a line that is not anchored does not count", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.write(f.tree+"/.git/info/exclude", ".envrc\n#/.vscode\n /tools/local/x.txt\n")
		f.prepareIgnoringErrors()
		rep := f.check()
		if got := problemPaths(rep, ErrNotExcluded); len(got) != 3 {
			t.Fatalf("got %v", got)
		}
	})
	t.Run("a symbolic link as the file is refused", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.write(f.out+"/other", "outside\n")
		f.remove(f.tree + "/.git/info/exclude")
		f.symlink(f.out+"/other", f.tree+"/.git/info/exclude")
		before := listing(t, f.base)
		wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrSymlink)
		sameListing(t, before, listing(t, f.base))
		f.prepareIgnoringErrors()
		wantProblem(t, f.check(), ErrSymlink, f.tree+"/.git/info/exclude")
	})
	t.Run("a folder as the file is refused", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.remove(f.tree + "/.git/info/exclude")
		f.mkdir(f.tree+"/.git/info/exclude", 0o755)
		wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrWrongType)
	})
	t.Run("a file over the cap is refused", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.write(f.tree+"/.git/info/exclude", strings.Repeat("#"+strings.Repeat("x", 1022)+"\n", 1100))
		before := listing(t, f.base)
		wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrUnreadable)
		sameListing(t, before, listing(t, f.base))
		f.prepareIgnoringErrors()
		wantProblem(t, f.check(), ErrUnreadable, f.tree+"/.git/info/exclude")
	})
	t.Run("a hard-linked file is refused", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		if err := os.Link(f.tree+"/.git/info/exclude", f.out+"/alias"); err != nil {
			t.Fatal(err)
		}
		wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrWrongType)
	})
	t.Run("a hard-linked file is refused by Check too", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.prepare()
		if err := os.Link(f.tree+"/.git/info/exclude", f.out+"/alias"); err != nil {
			t.Fatal(err)
		}
		wantProblem(t, f.check(), ErrWrongType, f.tree+"/.git/info/exclude")
	})
	t.Run("lines that end in CR LF count as listed", func(t *testing.T) {
		// git strips the carriage return, so the lines name the paths.
		f := newFixture(t, profile.Claude)
		f.prepare()
		f.write(f.tree+"/.git/info/exclude", "/.envrc\r\n/.vscode\r\n/tools/local/x.txt\r\n")
		if rep := f.check(); !rep.OK() {
			t.Fatal(rep.Err())
		}
		before := f.exclude()
		f.prepare()
		if got := f.exclude(); got != before {
			t.Fatalf("Prepare added lines that are listed: %q", got)
		}
	})
	t.Run("no placeholders, no exclude file touched", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.in.ExtraPlaceholders = nil
		f.remove(f.tree + "/.git/info/exclude")
		f.prepare()
		if _, err := os.Lstat(f.tree + "/.git/info/exclude"); err == nil {
			t.Fatal("the exclude file was created without a placeholder")
		}
		if rep := f.check(); !rep.OK() {
			t.Fatal(rep.Err())
		}
	})
}

func TestCheckMissingExcludeLine(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.prepare()
	f.write(f.tree+"/.git/info/exclude", "/.envrc\n/tools/local/x.txt\n")
	rep := f.check()
	if got := problemPaths(rep, ErrNotExcluded); len(got) != 1 || got[0] != f.tree+"/.vscode" {
		t.Fatalf("got %v", rep.Err())
	}
	f.remove(f.tree + "/.git/info/exclude")
	if got := problemPaths(f.check(), ErrNotExcluded); len(got) != 3 {
		t.Fatalf("got %v", got)
	}
}

func TestCheckReportsEveryProblemAndChangesNothing(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.prepare()
	f.remove(f.tree + "/.envrc") // missing
	f.remove(f.cfg + "/skills")  // wrong type below
	f.write(f.cfg+"/skills", "x")
	f.remove(f.tree + "/.vscode") // a link
	f.symlink(f.out, f.tree+"/.vscode")
	f.write(f.tree+"/.git/info/exclude", "") // nothing listed
	before := listing(t, f.base)
	rep := f.check()
	sameListing(t, before, listing(t, f.base))
	wantProblem(t, rep, ErrMissing, f.tree+"/.envrc")
	wantProblem(t, rep, ErrWrongType, f.cfg+"/skills")
	wantProblem(t, rep, ErrSymlink, f.tree+"/.vscode")
	wantProblem(t, rep, ErrNotExcluded, f.tree+"/.envrc")
	wantProblem(t, rep, ErrNotExcluded, f.tree+"/tools/local/x.txt")
	if rep.Truncated {
		t.Error("truncated")
	}
	err := rep.Err()
	for _, k := range []error{ErrMissing, ErrWrongType, ErrSymlink, ErrNotExcluded} {
		if !errors.Is(err, k) {
			t.Errorf("the report error does not match %v: %v", k, err)
		}
	}
	if !strings.Contains(err.Error(), "problem(s)") {
		t.Errorf("error text: %v", err)
	}
}

func TestCheckWrongTypeFile(t *testing.T) {
	// A FIFO where a file is needed is a wrong type, and is not opened.
	f := newFixture(t, profile.Claude)
	f.prepare()
	f.remove(f.tree + "/.envrc")
	if err := mkfifo(f.tree + "/.envrc"); err != nil {
		t.Skipf("no fifo: %v", err)
	}
	wantProblem(t, f.check(), ErrWrongType, f.tree+"/.envrc")
	wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrWrongType)
}

func TestReportBounds(t *testing.T) {
	var r Report
	for i := 0; i < MaxProblems+10; i++ {
		r.add(problem(ErrMissing, "/x", "n"))
	}
	r.add(nil)
	if len(r.Problems) != MaxProblems || !r.Truncated {
		t.Fatalf("got %d problems, truncated %v", len(r.Problems), r.Truncated)
	}
	if !strings.Contains(r.Err().Error(), "more not listed") {
		t.Error("the error does not say the list is cut")
	}
	long := problem(ErrMissing, "/x", "%s", strings.Repeat("y", 5000))
	if len(long.Detail) > maxDetail+3 {
		t.Errorf("detail not bounded: %d", len(long.Detail))
	}
	if (Report{}).Err() != nil || !(Report{}).OK() {
		t.Error("an empty report must be ok")
	}
}
