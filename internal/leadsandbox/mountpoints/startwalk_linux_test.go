//go:build linux

package mountpoints

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

// This file is the unit-level ground of qualification case S21 (rule B1, the
// walk before each start): every path the walk looks at, with a symbolic link
// planted at it and with an entry of a wrong type, for both runtimes. It
// launches nothing; the case itself is a qualification run.

// walkedPath is one path the start walk looks at.
type walkedPath struct {
	name string
	host string
	dir  bool // a folder belongs there; else a regular file
}

// startWalkFixture is a fixture whose records are bound in the home, so that
// the walk has a mount point for them too.
func startWalkFixture(t *testing.T, rt profile.Runtime) *fixture {
	t.Helper()
	f := newFixture(t, rt)
	f.in.QargiRecordsInside = "/home/lead/.qargi"
	if err := f.in.Validate(); err != nil {
		t.Fatalf("fixture inputs: %v", err)
	}
	return f
}

// walkedPaths are the paths of the start walk for the fixture: the folders the
// mount points lie in, the tree's .git folder, and every mount point.
func walkedPaths(f *fixture) []walkedPath {
	out := []walkedPath{
		{"the home", f.home, true},
		{"the config folder", f.cfg, true},
		{"the tree", f.tree, true},
	}
	for _, w := range walkList(f.list()) {
		out = append(out, walkedPath{strings.TrimPrefix(w.HostPath, f.layout().LeadDir()+"/"), w.HostPath, w.Dir})
	}
	return out
}

// The walk looks at exactly these paths, for each runtime: a change of the
// list is a change of what case S21 covers.
func TestStartWalkPaths(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := startWalkFixture(t, rt)
			cfgName, cfgDir := rt.ConfigDirName(), "config/"+string(rt)
			want := []string{
				"the home dir",
				"the config folder dir",
				"the tree dir",
				"home/" + cfgName + " dir",
				cfgDir + "/skills dir",
				cfgDir + "/" + rt.InstructionFileName() + " file",
				"tree/.git dir",
				"tree/.git/config file",
				"tree/.git/hooks dir",
				"tree/.git/info dir",
				"tree/.envrc file",
				"tree/.vscode dir",
				"tree/tools/local/x.txt file",
				"home/.qargi dir",
			}
			var got []string
			for _, w := range walkedPaths(f) {
				got = append(got, w.name+map[bool]string{true: " dir", false: " file"}[w.dir])
			}
			if strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("the walk looks at\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
			}
		})
	}
}

// startWalkPlant puts something that must be refused at a walked path.
type startWalkPlant struct {
	name string
	// forDir and forFile say whether the plant applies to a path where a
	// folder, or a regular file, belongs.
	forDir, forFile bool
	kind            error
	// plant makes the entry at host; outside is a folder outside every folder
	// of the lead, and wantDir says what belongs at host.
	plant func(t *testing.T, f *fixture, host string, wantDir bool)
}

var startWalkPlants = []startWalkPlant{
	{"a link to an entry of the right type outside", true, true, ErrSymlink, func(t *testing.T, f *fixture, host string, wantDir bool) {
		target := f.out + "/target"
		if wantDir {
			f.mkdir(target, 0o755)
		} else {
			f.write(target, "outside content")
		}
		f.symlink(target, host)
	}},
	{"a dangling link", true, true, ErrSymlink, func(t *testing.T, f *fixture, host string, wantDir bool) {
		f.symlink(f.out+"/nowhere", host)
	}},
	{"a relative link to an entry beside it", true, true, ErrSymlink, func(t *testing.T, f *fixture, host string, wantDir bool) {
		beside := host + ".beside"
		if wantDir {
			f.mkdir(beside, 0o755)
		} else {
			f.write(beside, "")
		}
		f.symlink(filepath.Base(beside), host)
	}},
	{"a regular file where a folder belongs", true, false, ErrWrongType, func(t *testing.T, f *fixture, host string, wantDir bool) {
		f.write(host, "x")
	}},
	{"a folder where a file belongs", false, true, ErrWrongType, func(t *testing.T, f *fixture, host string, wantDir bool) {
		f.mkdir(host, 0o755)
	}},
	{"a FIFO", true, true, ErrWrongType, func(t *testing.T, f *fixture, host string, wantDir bool) {
		if err := mkfifo(host); err != nil {
			t.Skipf("no fifo: %v", err)
		}
	}},
}

// setAside moves what is at host out of the lead's folders, when there is
// something, so that a plant can take its name.
func setAside(t *testing.T, f *fixture, host string) {
	t.Helper()
	if _, err := os.Lstat(host); err != nil {
		return
	}
	mustRename(t, host, f.base+"/set-aside")
}

// Check, the walk of every start, refuses each plant at each walked path with
// one problem of the right kind that names the path, and changes nothing.
func TestStartWalkRefusesEveryPlantAtEveryPath(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		n := len(walkedPaths(startWalkFixture(t, rt)))
		for i := 0; i < n; i++ {
			for _, pl := range startWalkPlants {
				f := startWalkFixture(t, rt)
				w := walkedPaths(f)[i]
				if w.dir && !pl.forDir || !w.dir && !pl.forFile {
					continue
				}
				t.Run(string(rt)+"/"+w.name+"/"+pl.name, func(t *testing.T) {
					f.prepare()
					// An earlier start found nothing: that is not reused.
					if rep := f.check(); !rep.OK() {
						t.Fatalf("before the plant: %v", rep.Err())
					}
					setAside(t, f, w.host)
					pl.plant(t, f, w.host, w.dir)
					before := listing(t, f.base)
					rep := f.check()
					sameListing(t, before, listing(t, f.base))
					if len(rep.Problems) != 1 {
						t.Fatalf("want one problem, about %s; got %v", w.host, rep.Err())
					}
					p := rep.Problems[0]
					if !errors.Is(p, pl.kind) || p.Path != w.host {
						t.Fatalf("want a %v problem at %s, got %v", pl.kind, w.host, p)
					}
					if !strings.Contains(p.Error(), w.host) {
						t.Errorf("the problem does not name the path: %v", p)
					}
					if rep.Has(ErrMissing) {
						t.Errorf("a plant must not read as a missing entry: %v", rep.Err())
					}
				})
			}
		}
	}
}

// Prepare, the walk before the first start, refuses the same plants at the
// same paths, names the path, and writes nothing.
func TestStartWalkPlantsRefusedByPrepare(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		n := len(walkedPaths(startWalkFixture(t, rt)))
		for i := 0; i < n; i++ {
			for _, pl := range startWalkPlants {
				f := startWalkFixture(t, rt)
				w := walkedPaths(f)[i]
				if w.dir && !pl.forDir || !w.dir && !pl.forFile {
					continue
				}
				t.Run(string(rt)+"/"+w.name+"/"+pl.name, func(t *testing.T) {
					// No Prepare has run: the folder a plant lies in may be
					// absent yet.
					f.mkdir(filepath.Dir(w.host), 0o755)
					setAside(t, f, w.host)
					pl.plant(t, f, w.host, w.dir)
					before := listing(t, f.base)
					err := Prepare(f.in, PrepareOptions{NoLeadHasRun: true})
					sameListing(t, before, listing(t, f.base))
					var p *Problem
					if !errors.As(err, &p) {
						t.Fatalf("Prepare: %v, want a problem at %s", err, w.host)
					}
					if !errors.Is(p, pl.kind) || p.Path != w.host {
						t.Fatalf("want a %v problem at %s, got %v", pl.kind, w.host, p)
					}
				})
			}
		}
	}
}

// A component on the way to a mount point that is itself no walked path (a
// folder a placeholder lies in) is refused too: the problem is about the mount
// point, and its detail names the component.
func TestStartWalkNamesTheComponentOnTheWay(t *testing.T) {
	cases := []struct {
		name  string
		at    string // below the tree
		plant func(f *fixture, host string)
		kind  error
		says  string
	}{
		{"a link at the first folder", "tools", func(f *fixture, host string) { f.symlink(f.out, host) }, ErrSymlink, "is a symbolic link"},
		{"a link at the second folder", "tools/local", func(f *fixture, host string) { f.symlink(f.out, host) }, ErrSymlink, "is a symbolic link"},
		{"a dangling link", "tools/local", func(f *fixture, host string) { f.symlink(f.out+"/nowhere", host) }, ErrSymlink, "is a symbolic link"},
		{"a file", "tools/local", func(f *fixture, host string) { f.write(host, "x") }, ErrWrongType, "is a regular file, not a folder"},
		{"a FIFO", "tools", func(f *fixture, host string) {
			if err := mkfifo(host); err != nil {
				f.t.Skipf("no fifo: %v", err)
			}
		}, ErrWrongType, "not a folder"},
	}
	for _, rt := range profile.Runtimes() {
		for _, c := range cases {
			t.Run(string(rt)+"/"+c.name, func(t *testing.T) {
				f := startWalkFixture(t, rt)
				f.prepare()
				host, point := f.tree+"/"+c.at, f.tree+"/tools/local/x.txt"
				setAside(t, f, host)
				c.plant(f, host)
				before := listing(t, f.base)
				rep := f.check()
				sameListing(t, before, listing(t, f.base))
				if len(rep.Problems) != 1 {
					t.Fatalf("want one problem, about %s; got %v", point, rep.Err())
				}
				p := rep.Problems[0]
				if !errors.Is(p, c.kind) || p.Path != point {
					t.Fatalf("want a %v problem at %s, got %v", c.kind, point, p)
				}
				if !strings.Contains(p.Detail, host+",") || !strings.Contains(p.Detail, c.says) {
					t.Errorf("the detail does not name the component %s and say it %s: %q", host, c.says, p.Detail)
				}
				if names, _ := os.ReadDir(f.out); len(names) != 0 {
					t.Errorf("something was made outside: %v", names)
				}
			})
		}
	}
}

// The tree's .git is walked itself: a link or a wrong type there is one
// problem that names .git, and its three mount points are not reported as
// well. An absent .git is no problem of its own: the three mount points are
// the ones that are missing.
func TestStartWalkOfTheGitFolder(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := startWalkFixture(t, rt)
			f.prepare()
			git := f.tree + "/.git"
			mustRename(t, git, f.base+"/set-aside")
			rep := f.check()
			if got := problemPaths(rep, ErrMissing); strings.Join(got, "\n") != strings.Join([]string{git + "/config", git + "/hooks", git + "/info"}, "\n") || len(rep.Problems) != 3 {
				t.Fatalf("an absent .git: %v", rep.Err())
			}
			// A link to a whole .git outside, with the three entries in it.
			mustRename(t, f.base+"/set-aside", f.out+"/owner-git")
			f.symlink(f.out+"/owner-git", git)
			rep = f.check()
			if len(rep.Problems) != 1 || !errors.Is(rep.Problems[0], ErrSymlink) || rep.Problems[0].Path != git {
				t.Fatalf("a link at .git: %v", rep.Err())
			}
			wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrSymlink)
		})
	}
}

// The walk is made again at every start, for the runtime of that start: what
// an earlier call found is not reused, and a change of runtime walks the
// other runtime's places.
func TestStartWalkAcrossStartsAndARuntimeSwitch(t *testing.T) {
	f := startWalkFixture(t, profile.Claude)
	f.prepare()
	if rep := f.check(); !rep.OK() {
		t.Fatalf("first start: %v", rep.Err())
	}
	// Between two starts the lead replaces a mount point by a link.
	skills := f.cfg + "/skills"
	f.remove(skills)
	f.symlink(f.out, skills)
	wantProblem(t, f.check(), ErrSymlink, skills)
	f.remove(skills)
	f.mkdir(skills, 0o755)
	if rep := f.check(); !rep.OK() {
		t.Fatalf("after the repair: %v", rep.Err())
	}

	// The same lead under the other runtime: its config folder is another
	// folder, with its own places, and the home holds .codex.
	codex := f.in
	codex.Runtime = profile.Codex
	codex.ManagedSettingsInside = "/etc/codex/managed-config.toml"
	codex.InstructionFile = f.owned + "/instructions/" + profile.Codex.InstructionFileName()
	f.write(codex.InstructionFile, "x")
	cfg, _ := f.layout().ConfigDir(profile.Codex)
	f.mkdir(cfg, 0o700)
	// The lead, while it ran under the first runtime, planted links at the
	// places of the second.
	f.symlink(f.out, f.home+"/"+profile.Codex.ConfigDirName())
	f.symlink(f.out, cfg+"/skills")
	rep, err := Check(codex)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	wantProblem(t, rep, ErrSymlink, f.home+"/"+profile.Codex.ConfigDirName())
	wantProblem(t, rep, ErrSymlink, cfg+"/skills")
	wantProblem(t, rep, ErrMissing, cfg+"/"+profile.Codex.InstructionFileName())
	if names, _ := os.ReadDir(f.out); len(names) != 0 {
		t.Errorf("something was made outside: %v", names)
	}
}
