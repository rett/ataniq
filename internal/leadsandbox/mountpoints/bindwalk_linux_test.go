//go:build linux

package mountpoints

import (
	"errors"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/safefile"
)

// This file is the test of rule B1 for the bind sources: each source of a row
// Ataniq supplies is reached by handle at the moment of the check, in one
// lookup, and its type is decided on the descriptor that lookup returned.

// suppliedRows are the rows of the fixture that Ataniq supplies, as
// ResolveSources gives them on a prepared fixture.
func suppliedRows(f *fixture) []ResolvedSource {
	f.t.Helper()
	res := f.resolve()
	if !res.OK() {
		f.t.Fatalf("ResolveSources: %v", res.Err())
	}
	var rows []ResolvedSource
	for _, s := range res.Sources {
		if s.Role != profile.RoleHost {
			rows = append(rows, s)
		}
	}
	if len(rows) < 20 {
		f.t.Fatalf("only %d rows that Ataniq supplies", len(rows))
	}
	return rows
}

// Every row Ataniq supplies, below the state root and outside it, for both
// runtimes: a link at the source, and an entry of another type than the row
// binds, are refused with one problem that names the row and the source.
func TestCheckBindSourcesRefusesEveryPlantAtEverySource(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		probe := newFixture(t, rt)
		probe.prepare()
		n := len(suppliedRows(probe))
		for i := 0; i < n; i++ {
			for _, pl := range startWalkPlants {
				f := newFixture(t, rt)
				f.prepare()
				rows := suppliedRows(f)
				row := rows[i]
				if row.Walk.Dir && !pl.forDir || !row.Walk.Dir && !pl.forFile {
					continue
				}
				where := map[bool]string{true: "below the state root", false: "outside the state root"}[row.Walk.BelowState]
				t.Run(string(rt)+"/"+row.Inside+" ("+where+")/"+pl.name, func(t *testing.T) {
					if rep := CheckBindSources(rows); !rep.OK() {
						t.Fatalf("before the plant: %v", rep.Err())
					}
					setAside(t, f, row.Source)
					pl.plant(t, f, row.Source, row.Walk.Dir)
					before := listing(t, f.base)
					rep := CheckBindSources(rows)
					sameListing(t, before, listing(t, f.base))
					if len(rep.Problems) != 1 {
						t.Fatalf("want one problem, about %s; got %v", row.Source, rep.Err())
					}
					p := rep.Problems[0]
					if !errors.Is(p, pl.kind) || p.Path != row.Source || p.Inside != row.Inside {
						t.Fatalf("want a %v problem at %s for the row %s, got %+v", pl.kind, row.Source, row.Inside, p)
					}
					if !strings.Contains(p.Detail, row.Inside) {
						t.Errorf("the detail does not name the row: %q", p.Detail)
					}
					if pl.kind == ErrSymlink && !strings.Contains(p.Detail, row.Source) {
						t.Errorf("the detail does not name the link: %q", p.Detail)
					}
				})
			}
		}
	}
}

// The type is the one the row binds, not the one that happens to be there: the
// same folder is accepted for a row that binds a folder and refused for one
// that binds a file, and the other way round.
func TestCheckBindSourcesTypeIsTheRows(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.prepare()
	dir, file := f.owned+"/agent", f.owned+"/probe"
	stateDir, stateFile := f.gitMeta+"/hooks", f.gitMeta+"/config"
	below := func(inside, p string, isDir bool) ResolvedSource {
		return ResolvedSource{Inside: inside, Source: p, Real: p, Role: profile.RoleGitMeta,
			Walk: SourceWalk{Inside: inside, Root: f.state, Rel: strings.TrimPrefix(p, f.state+"/"), Dir: isDir, BelowState: true}}
	}
	for _, c := range []struct {
		name string
		row  ResolvedSource
		ok   bool
		says string
	}{
		{"a folder bound as a folder", fsRow("/r", dir, true), true, ""},
		{"a folder bound as a file", fsRow("/r", dir, false), false, "is a folder, and the row binds a regular file"},
		{"a file bound as a file", fsRow("/r", file, false), true, ""},
		{"a file bound as a folder", fsRow("/r", file, true), false, "is a regular file, and the row binds a folder"},
		{"below the state root, a folder bound as a folder", below("/r", stateDir, true), true, ""},
		{"below the state root, a folder bound as a file", below("/r", stateDir, false), false, "is a folder, and the row binds a regular file"},
		{"below the state root, a file bound as a file", below("/r", stateFile, false), true, ""},
		{"below the state root, a file bound as a folder", below("/r", stateFile, true), false, "is a regular file, and the row binds a folder"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rep := CheckBindSources([]ResolvedSource{c.row})
			if c.ok {
				if !rep.OK() {
					t.Fatalf("%v", rep.Err())
				}
				return
			}
			if len(rep.Problems) != 1 || !errors.Is(rep.Problems[0], ErrWrongType) || rep.Problems[0].Path != c.row.Source {
				t.Fatalf("want one wrong-type problem at %s, got %v", c.row.Source, rep.Err())
			}
			if !strings.Contains(rep.Problems[0].Detail, c.says) {
				t.Errorf("detail %q, want it to say %q", rep.Problems[0].Detail, c.says)
			}
		})
	}
}

// A bound file is checked for its type only: its content is not read, so its
// size and its link count are not looked at.
func TestCheckBindSourcesDoesNotReadABoundFile(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.prepare()
	rows := suppliedRows(f)
	// Unreadable, large and hard-linked: a read would fail or be refused.
	if err := os.Truncate(f.in.ProbeBinary, 64<<20); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(f.in.ProbeBinary, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(f.gitMeta+"/config", f.out+"/alias"); err != nil {
		t.Fatal(err)
	}
	if rep := CheckBindSources(rows); !rep.OK() {
		t.Fatalf("%v", rep.Err())
	}
}

// A row Ataniq supplies that does not say how its source is walked, or whose
// walk leads elsewhere, is refused before anything is looked up: there is no
// weaker check for it.
func TestCheckBindSourcesRefusesARowWithoutItsWalk(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.prepare()
	good := fsRow("/ataniq/agent", f.in.AgentInstall, true)
	cases := []struct {
		name string
		edit func(r *ResolvedSource)
	}{
		{"no walk", func(r *ResolvedSource) { r.Walk = SourceWalk{} }},
		{"no root", func(r *ResolvedSource) { r.Walk.Root = "" }},
		{"no path beneath the root", func(r *ResolvedSource) { r.Walk.Rel = "" }},
		{"the walk of another row", func(r *ResolvedSource) { r.Walk.Inside = "/ataniq/qargi" }},
		{"a walk to another source", func(r *ResolvedSource) { r.Walk.Rel = "qargi" }},
		{"a walk from another root", func(r *ResolvedSource) { r.Walk.Root = f.owned + "/ph" }},
		{"a walk that climbs back", func(r *ResolvedSource) { r.Walk.Root, r.Walk.Rel = f.owned+"/ph", "../agent" }},
		{"a walk with an absolute path beneath the root", func(r *ResolvedSource) { r.Walk.Rel = f.in.AgentInstall }},
		{"below the state root, a walk that climbs back", func(r *ResolvedSource) {
			r.Walk = SourceWalk{Inside: r.Inside, Root: f.state, Rel: "../owned/agent", Dir: true, BelowState: true}
		}},
		{"below the state root, a walk to another source", func(r *ResolvedSource) {
			r.Walk = SourceWalk{Inside: r.Inside, Root: f.state, Rel: "leads/arc-lead/home", Dir: true, BelowState: true}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			row := good
			c.edit(&row)
			calls := 0
			opened := recordByName(t)
			rep := checkBindSources([]ResolvedSource{row}, func(dirfd int, p string, how *unix.OpenHow) (int, error) {
				calls++
				return unix.Openat2(dirfd, p, how)
			})
			if len(rep.Problems) != 1 || !errors.Is(rep.Problems[0], ErrInvalid) || rep.Problems[0].Inside != row.Inside {
				t.Fatalf("want one invalid-input problem for the row, got %v", rep.Err())
			}
			if calls != 0 || len(*opened) != 0 {
				t.Errorf("%d lookups, opened by name %q: nothing is looked up for a row that is refused", calls, *opened)
			}
		})
	}
	if rep := CheckBindSources([]ResolvedSource{good}); !rep.OK() {
		t.Fatalf("the row the cases start from: %v", rep.Err())
	}
}

// countingCall counts the lookups from the root of the file system, by path.
func countingCall(count map[string]int) openat2Func {
	return func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		count["/"+p]++
		return unix.Openat2(dirfd, p, how)
	}
}

// Each source is looked up once. A source outside the state root is one lookup
// from the root of the file system; a source below it is walked from the state
// root's handle, and nothing but the state root is opened by name, once for
// all rows.
func TestCheckBindSourcesLooksEachSourceUpOnce(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := newFixture(t, rt)
			f.prepare()
			rows := suppliedRows(f)
			count := map[string]int{}
			opened := recordByName(t)
			if rep := checkBindSources(rows, countingCall(count)); !rep.OK() {
				t.Fatalf("%v", rep.Err())
			}
			want := map[string]int{f.state: 1}
			below := 0
			for _, r := range rows {
				if r.Walk.BelowState {
					below++
					continue
				}
				want[r.Source] = 1
			}
			if below < 10 || len(want) < 10 {
				t.Fatalf("%d rows below the state root, %d outside", below, len(want)-1)
			}
			for p, n := range count {
				if want[p] != n {
					t.Errorf("%s was looked up %d times from the root of the file system, want %d", p, n, want[p])
				}
			}
			for p := range want {
				if count[p] != 1 {
					t.Errorf("%s was looked up %d times, want once", p, count[p])
				}
			}
			if len(*opened) != 1 || (*opened)[0] != f.state {
				t.Errorf("opened by name: %q, want only the state root, once", *opened)
			}
		})
	}
}

// swapAfterLookup is an openat2 that, right after the lookup of the path at
// returned, runs swap: the moment between a lookup and whatever would look
// the name up again.
func swapAfterLookup(at string, swap func(), count *int) openat2Func {
	return func(dirfd int, p string, how *unix.OpenHow) (int, error) {
		fd, err := unix.Openat2(dirfd, p, how)
		if "/"+p == at {
			if *count++; *count == 1 {
				swap()
			}
		}
		return fd, err
	}
}

// The type of a source outside the state root is read from the descriptor of
// the one lookup. Right after that lookup the name is given to something else:
// the verdict is about what was opened, and the name is not looked up again.
func TestCheckBindSourcesDecidesOnTheDescriptorNotTheName(t *testing.T) {
	t.Run("a folder that is opened stays accepted when its name becomes a file, a link or nothing", func(t *testing.T) {
		for name, plant := range map[string]func(f *fixture, p string){
			"a file":  func(f *fixture, p string) { f.write(p, "x") },
			"a link":  func(f *fixture, p string) { f.symlink(f.out, p) },
			"nothing": func(f *fixture, p string) {},
		} {
			f := newFixture(t, profile.Claude)
			src := f.in.AgentInstall
			lookups := 0
			call := swapAfterLookup(src, func() {
				mustRename(t, src, f.base+"/moved-away")
				plant(f, src)
			}, &lookups)
			rep := checkBindSources([]ResolvedSource{fsRow("/ataniq/agent", src, true)}, call)
			if !rep.OK() {
				t.Errorf("%s: the name was looked up again after the lookup: %v", name, rep.Err())
			}
			if lookups != 1 {
				t.Errorf("%s: %d lookups of the source, want one", name, lookups)
			}
		}
	})
	t.Run("a file that is opened where a folder is bound stays refused when its name becomes a folder", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		src := f.in.AgentInstall
		f.remove(src)
		f.write(src, "x")
		lookups := 0
		call := swapAfterLookup(src, func() {
			f.remove(src)
			f.mkdir(src, 0o755)
		}, &lookups)
		rep := checkBindSources([]ResolvedSource{fsRow("/ataniq/agent", src, true)}, call)
		wantProblem(t, rep, ErrWrongType, src)
		if lookups != 1 {
			t.Errorf("%d lookups of the source, want one", lookups)
		}
	})
	t.Run("a file that is opened stays accepted when its name becomes a folder", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		src := f.in.ProbeBinary
		lookups := 0
		call := swapAfterLookup(src, func() {
			f.remove(src)
			f.mkdir(src, 0o755)
		}, &lookups)
		rep := checkBindSources([]ResolvedSource{fsRow("/ataniq/probe", src, false)}, call)
		if !rep.OK() || lookups != 1 {
			t.Errorf("%v after %d lookups", rep.Err(), lookups)
		}
	})
}

// The sources below the state root are walked from the handle of the state
// root that was opened. Right after it was opened its name is given to an
// empty folder: every row is still found, beneath the handle, and the state
// root is not opened again.
func TestCheckBindSourcesWalksBelowTheStateRootItOpened(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.prepare()
	rows := suppliedRows(f)
	prev := openRootByName
	var opened []string
	openRootByName = func(dir string) (*safefile.Root, error) {
		r, err := prev(dir)
		if opened = append(opened, dir); len(opened) == 1 && err == nil {
			mustRename(t, f.state, f.base+"/moved-state")
			f.mkdir(f.state, 0o700)
		}
		return r, err
	}
	t.Cleanup(func() { openRootByName = prev })
	if rep := CheckBindSources(rows); !rep.OK() {
		t.Fatalf("a row was looked up by its name after the state root was opened: %v", rep.Err())
	}
	if len(opened) != 1 || opened[0] != f.state {
		t.Errorf("opened by name: %q, want only the state root, once", opened)
	}
	// By name, every source below the state root is gone now.
	openRootByName = prev
	rep := CheckBindSources(rows)
	missing := 0
	for _, r := range rows {
		if r.Walk.BelowState {
			missing++
			wantProblem(t, rep, ErrMissing, r.Source)
		}
	}
	if len(rep.Problems) != missing {
		t.Errorf("%d problems for %d rows below the state root: %v", len(rep.Problems), missing, rep.Err())
	}
}

// The state root is found from the root of the file system without following
// a link, and then opened by name. When the name is given to another folder
// between the two, the folder that is opened is not the one that was found, and
// every row below the state root is refused; the rows outside it are not.
func TestCheckBindSourcesRefusesAStateRootThatChangedWhileItWasOpened(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.prepare()
	rows := suppliedRows(f)
	lookups := 0
	call := swapAfterLookup(f.state, func() {
		mustRename(t, f.state, f.base+"/moved-state")
		// A whole other state folder, with every source in it: by name, all is
		// in place.
		if err := os.CopyFS(f.state, os.DirFS(f.base+"/moved-state")); err != nil {
			t.Fatal(err)
		}
	}, &lookups)
	rep := checkBindSources(rows, call)
	below := 0
	for _, r := range rows {
		if !r.Walk.BelowState {
			continue
		}
		below++
		wantProblem(t, rep, ErrUnreadable, r.Source)
	}
	if len(rep.Problems) != below || lookups != 1 {
		t.Fatalf("%d problems for %d rows below the state root, %d lookups of the state root: %v", len(rep.Problems), below, lookups, rep.Err())
	}
	for _, p := range rep.Problems {
		if !strings.Contains(p.Detail, "changed while it was opened") || !strings.Contains(p.Detail, p.Inside) {
			t.Errorf("detail: %q", p.Detail)
		}
	}
}

// A link at the state root, or above it, refuses every row below the state
// root, naming the link: the state root is opened by name, and a link above
// it would be followed.
func TestCheckBindSourcesRefusesALinkAtOrAboveTheStateRoot(t *testing.T) {
	cases := []struct {
		name string
		make func(f *fixture) (stateRoot, link string)
	}{
		{"the state root is a link", func(f *fixture) (string, string) {
			f.symlink(f.state, f.base+"/state-link")
			return f.base + "/state-link", f.base + "/state-link"
		}},
		{"a folder above the state root is a link", func(f *fixture) (string, string) {
			f.symlink(f.base, f.out+"/base-link")
			return f.out + "/base-link/state", f.out + "/base-link"
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			f.prepare()
			stateRoot, link := c.make(f)
			var rows []ResolvedSource
			for _, r := range suppliedRows(f) {
				if !r.Walk.BelowState {
					continue
				}
				// The same rows, named through the link.
				r.Source = stateRoot + "/" + r.Walk.Rel
				r.Real = r.Source
				r.Walk.Root = stateRoot
				rows = append(rows, r)
			}
			// By name, everything is in place.
			for _, r := range rows {
				if _, err := os.Stat(r.Source); err != nil {
					t.Fatalf("the path through the link does not resolve: %v", err)
				}
			}
			opened := recordByName(t)
			rep := CheckBindSources(rows)
			if len(rep.Problems) != len(rows) {
				t.Fatalf("%d problems for %d rows: %v", len(rep.Problems), len(rows), rep.Err())
			}
			for i, p := range rep.Problems {
				if !errors.Is(p, ErrSymlink) || p.Path != rows[i].Source || p.Inside != rows[i].Inside || !strings.Contains(p.Detail, "symbolic link at "+link+":") {
					t.Errorf("row %s: %+v", rows[i].Inside, p)
				}
			}
			if len(*opened) != 0 {
				t.Errorf("opened by name: %q; a state root reached through a link is not opened", *opened)
			}
		})
	}
}

func TestCheckBindSourcesStateRootRefusals(t *testing.T) {
	cases := []struct {
		name  string
		spoil func(f *fixture)
		kind  error
	}{
		{"missing", func(f *fixture) { mustRename(f.t, f.state, f.base+"/moved-state") }, ErrMissing},
		{"a file", func(f *fixture) { mustRename(f.t, f.state, f.base+"/moved-state"); f.write(f.state, "x") }, ErrWrongType},
		{"a FIFO", func(f *fixture) {
			mustRename(f.t, f.state, f.base+"/moved-state")
			if err := mkfifo(f.state); err != nil {
				f.t.Skipf("no fifo: %v", err)
			}
		}, ErrWrongType},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			f.prepare()
			rows := suppliedRows(f)
			c.spoil(f)
			rep := CheckBindSources(rows)
			below := 0
			for _, r := range rows {
				if r.Walk.BelowState {
					below++
					wantProblem(t, rep, c.kind, r.Source)
				}
			}
			if len(rep.Problems) != below {
				t.Errorf("%d problems for %d rows below the state root: %v", len(rep.Problems), below, rep.Err())
			}
			for _, p := range rep.Problems {
				if p.Inside == "" || !strings.Contains(p.Detail, p.Inside) || !strings.Contains(p.Detail, f.state) {
					t.Errorf("the problem does not name its row and the state root: %+v", p)
				}
			}
		})
	}
}

// Without openat2 a row below the state root is refused like any other: there
// is no fallback to a path-based open.
func TestCheckBindSourcesBelowTheStateRootWithoutOpenat2(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.prepare()
	rows := suppliedRows(f)
	for _, errno := range []error{unix.ENOSYS, unix.EPERM} {
		opened := recordByName(t)
		rep := checkBindSources(rows, func(int, string, *unix.OpenHow) (int, error) { return -1, errno })
		if len(rep.Problems) != len(rows) {
			t.Fatalf("%d problems for %d rows: %v", len(rep.Problems), len(rows), rep.Err())
		}
		for i, p := range rep.Problems {
			if !errors.Is(p, ErrUnreadable) || !errors.Is(p, errno) || p.Path != rows[i].Source {
				t.Errorf("row %s: %+v", rows[i].Inside, p)
			}
		}
		if len(*opened) != 0 {
			t.Errorf("opened by name: %q", *opened)
		}
	}
}

func TestCheckBindSourcesLeavesNoDescriptorOpen(t *testing.T) {
	spoils := []struct {
		name  string
		spoil func(f *fixture)
	}{
		{"clean", func(f *fixture) {}},
		{"the state root is missing", func(f *fixture) { mustRename(f.t, f.state, f.base+"/moved-state") }},
		{"the state root is a link", func(f *fixture) {
			mustRename(f.t, f.state, f.base+"/moved-state")
			f.symlink(f.base+"/moved-state", f.state)
		}},
		{"the lead's folder is a link", func(f *fixture) {
			mustRename(f.t, f.layout().LeadDir(), f.base+"/moved-lead")
			f.symlink(f.base+"/moved-lead", f.layout().LeadDir())
		}},
		{"sources of the wrong type", func(f *fixture) {
			f.remove(f.gitMeta + "/hooks")
			f.write(f.gitMeta+"/hooks", "x")
			f.remove(f.gitMeta + "/config")
			f.mkdir(f.gitMeta+"/config", 0o700)
			f.remove(f.in.AgentInstall)
			f.write(f.in.AgentInstall, "x")
			f.remove(f.in.ProbeBinary)
			f.mkdir(f.in.ProbeBinary, 0o755)
		}},
		{"sources that are links, FIFOs or missing", func(f *fixture) {
			f.remove(f.in.LeadSkills)
			f.symlink(f.out, f.in.LeadSkills)
			f.remove(f.home)
			f.symlink(f.out, f.home)
			f.remove(f.in.ResolvConf)
			f.remove(f.gitMeta + "/info")
			f.remove(f.in.ManagedSettings)
			if err := mkfifo(f.in.ManagedSettings); err != nil {
				f.t.Skipf("no fifo: %v", err)
			}
		}},
	}
	for _, s := range spoils {
		t.Run(s.name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			f.prepare()
			rows := f.resolve().Sources // the rows of the host too
			s.spoil(f)
			noLeak(t, "CheckBindSources", func() { CheckBindSources(rows) })
			noLeak(t, "CheckBindSources without openat2", func() {
				checkBindSources(rows, func(int, string, *unix.OpenHow) (int, error) { return -1, unix.ENOSYS })
			})
			noLeak(t, "CheckBindSources of rows without a walk", func() {
				bare := append([]ResolvedSource(nil), rows...)
				for i := range bare {
					bare[i].Walk = SourceWalk{}
				}
				CheckBindSources(bare)
			})
		})
	}
}
