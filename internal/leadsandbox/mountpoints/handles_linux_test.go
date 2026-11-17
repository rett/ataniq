//go:build linux

package mountpoints

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
	"ataniq/internal/leadsandbox/safefile"
)

// This file is the test of rule B1 for the folders below the state root: each
// is looked up once, from the state root's handle, and what is done afterwards
// is done beneath the descriptor that lookup returned. Nothing is opened by
// name but the state root.

// recordByName records every folder the package opens by name from now to the
// end of the test.
func recordByName(t *testing.T) *[]string {
	t.Helper()
	prev := openRootByName
	opened := &[]string{}
	openRootByName = func(dir string) (*safefile.Root, error) {
		*opened = append(*opened, dir)
		return prev(dir)
	}
	t.Cleanup(func() { openRootByName = prev })
	return opened
}

// onSubRoot runs fn right after each folder is opened from a handle, from now
// to the end of the test: the moment a lead would change what the name holds.
func onSubRoot(t *testing.T, fn func(dir string)) {
	t.Helper()
	prev := afterSubRoot
	afterSubRoot = fn
	t.Cleanup(func() { afterSubRoot = prev })
}

// swapOnce returns a hook that, the first time the folder at dir was opened,
// runs swap, and counts how often the folder was opened.
func swapOnce(dir string, swap func()) (hook func(string), opens *int) {
	opens = new(int)
	return func(got string) {
		if got != dir {
			return
		}
		if *opens++; *opens == 1 {
			swap()
		}
	}, opens
}

func mustRename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

// The checks and Prepare open exactly one folder by name: the state root.
func TestOnlyTheStateRootIsOpenedByName(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := newFixture(t, rt)
			opened := recordByName(t)
			want := []string{f.state}
			for _, step := range []struct {
				name string
				run  func()
			}{
				{"Prepare", f.prepare},
				{"Prepare again", f.prepare},
				{"Check", func() {
					if rep := f.check(); !rep.OK() {
						t.Fatalf("Check: %v", rep.Err())
					}
				}},
				{"CheckOwnClone", func() {
					if err := CheckOwn(t, f); err != nil {
						t.Fatalf("CheckOwnClone: %v", err)
					}
				}},
			} {
				*opened = nil
				step.run()
				if strings.Join(*opened, "\n") != strings.Join(want, "\n") {
					t.Errorf("%s opened by name %q, want only the state root %q", step.name, *opened, want)
				}
			}
		})
	}
}

// Each folder below the state root is opened once per call.
func TestEachFolderIsOpenedOnce(t *testing.T) {
	f := newFixture(t, profile.Claude)
	count := map[string]int{}
	onSubRoot(t, func(dir string) { count[dir]++ })
	l := f.layout()
	steps := []struct {
		name string
		run  func()
		want []string
	}{
		{"Prepare", f.prepare, []string{f.home, f.cfg, f.tree, l.LeadDir(), l.LeadDir() + "/gitmeta.tmp"}},
		{"Prepare again", f.prepare, []string{f.home, f.cfg, f.tree, l.LeadDir(), f.gitMeta}},
		{"Check", func() { f.check() }, []string{f.home, f.cfg, f.tree, f.gitMeta}},
		{"CheckOwnClone", func() { CheckOwn(t, f) }, []string{f.home, f.cfg, f.tree, f.tree + "/.git", l.QargiRecords(), l.Requests(), l.Mail()}},
	}
	for _, step := range steps {
		clear(count)
		step.run()
		var got []string
		for dir, n := range count {
			if n != 1 {
				t.Errorf("%s opened %s %d times", step.name, dir, n)
			}
			got = append(got, dir)
		}
		sort.Strings(got)
		want := append([]string(nil), step.want...)
		sort.Strings(want)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s opened\n  %s\nwant\n  %s", step.name, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		}
	}
}

// swapTargets are the folders Check opens, by a name for the test.
func swapTargets(f *fixture) map[string]string {
	return map[string]string{"home": f.home, "config folder": f.cfg, "tree": f.tree, "git metadata folder": f.gitMeta}
}

// The name of a folder is given to something else right after Check looked
// the folder up. Check must go on with the folder it opened: a second lookup
// by name would find the other thing. Three other things are tried: an empty
// folder, in which every mount point is missing; a link to a folder outside;
// and nothing at all.
func TestCheckDoesNotLookAFolderUpAgainAfterASwap(t *testing.T) {
	decoys := []struct {
		name  string
		plant func(f *fixture, dir string)
	}{
		{"an empty folder", func(f *fixture, dir string) { f.mkdir(dir, 0o700) }},
		{"a link to a folder outside", func(f *fixture, dir string) { f.symlink(f.out, dir) }},
		{"nothing", func(f *fixture, dir string) {}},
	}
	for _, rt := range profile.Runtimes() {
		for _, name := range []string{"home", "config folder", "tree", "git metadata folder"} {
			for _, d := range decoys {
				t.Run(string(rt)+" "+name+" "+d.name, func(t *testing.T) {
					f := newFixture(t, rt)
					f.prepare()
					dir := swapTargets(f)[name]
					opened := recordByName(t)
					hook, opens := swapOnce(dir, func() {
						mustRename(t, dir, f.base+"/moved-away")
						d.plant(f, dir)
					})
					onSubRoot(t, hook)
					rep := f.check()
					if *opens != 1 {
						t.Fatalf("the %s was opened %d times, want once", name, *opens)
					}
					if !rep.OK() {
						t.Fatalf("Check looked the %s up again after its name changed: %v", name, rep.Err())
					}
					if len(*opened) != 1 || (*opened)[0] != f.state {
						t.Errorf("opened by name: %q, want only the state root", *opened)
					}
					if names, _ := os.ReadDir(f.out); len(names) != 0 {
						t.Errorf("something was made outside: %v", names)
					}
				})
			}
		}
	}
}

// The other way round: the folder that is opened has a problem, and right
// after the lookup its name is given to a folder without one. Check reports the
// problem of the folder it opened.
func TestCheckReportsTheFolderItOpenedNotTheOneAtItsNameLater(t *testing.T) {
	cases := []struct {
		name  string
		dir   func(f *fixture) string
		spoil func(f *fixture) string // makes a problem in the folder; returns its path
		kind  error
	}{
		{"home", func(f *fixture) string { return f.home }, func(f *fixture) string {
			f.remove(f.home + "/.claude")
			return f.home + "/.claude"
		}, ErrMissing},
		{"config folder", func(f *fixture) string { return f.cfg }, func(f *fixture) string {
			f.remove(f.cfg + "/skills")
			f.symlink(f.out, f.cfg+"/skills")
			return f.cfg + "/skills"
		}, ErrSymlink},
		{"tree", func(f *fixture) string { return f.tree }, func(f *fixture) string {
			f.remove(f.tree + "/.envrc")
			f.mkdir(f.tree+"/.envrc", 0o755)
			return f.tree + "/.envrc"
		}, ErrWrongType},
		{"git metadata folder", func(f *fixture) string { return f.gitMeta }, func(f *fixture) string {
			f.remove(f.gitMeta + "/hooks")
			return f.gitMeta + "/hooks"
		}, ErrMissing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// A second lead's folders, prepared and clean, supply the folder
			// without a problem.
			good := newFixture(t, profile.Claude)
			good.prepare()
			f := newFixture(t, profile.Claude)
			f.prepare()
			dir := c.dir(f)
			at := c.spoil(f)
			hook, opens := swapOnce(dir, func() {
				mustRename(t, dir, f.base+"/moved-away")
				mustRename(t, c.dir(good), dir)
			})
			onSubRoot(t, hook)
			rep := f.check()
			if *opens != 1 {
				t.Fatalf("the %s was opened %d times, want once", c.name, *opens)
			}
			wantProblem(t, rep, c.kind, at)
			// Without the swap in the way, the folder now at the name is clean.
			onSubRoot(t, func(string) {})
			if rep := f.check(); !rep.OK() {
				t.Fatalf("the folder put at the name is not clean, so the test proves nothing: %v", rep.Err())
			}
		})
	}
}

// Prepare writes beneath the folder it opened. The name of the folder is given
// to a link to a folder outside right after the lookup: nothing is made
// outside, and the mount points are made in the folder that was opened.
func TestPrepareWritesIntoTheFolderItOpenedAfterASwap(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		for _, name := range []string{"home", "config folder", "tree"} {
			t.Run(string(rt)+" "+name, func(t *testing.T) {
				f := newFixture(t, rt)
				dir := swapTargets(f)[name]
				moved := f.base + "/moved-away"
				opened := recordByName(t)
				hook, opens := swapOnce(dir, func() {
					mustRename(t, dir, moved)
					f.symlink(f.out, dir)
				})
				onSubRoot(t, hook)
				if err := Prepare(f.in, PrepareOptions{NoLeadHasRun: true}); err != nil {
					t.Fatalf("Prepare: %v", err)
				}
				if *opens != 1 {
					t.Fatalf("the %s was opened %d times, want once", name, *opens)
				}
				if len(*opened) != 1 || (*opened)[0] != f.state {
					t.Errorf("opened by name: %q, want only the state root", *opened)
				}
				if names, _ := os.ReadDir(f.out); len(names) != 0 {
					t.Fatalf("Prepare wrote through the link that took the folder's name: %v", names)
				}
				made := 0
				for _, p := range f.list() {
					if p.RootDir != dir {
						continue
					}
					made++
					fi, err := os.Lstat(moved + "/" + p.Rel)
					if err != nil || fi.IsDir() != p.Dir {
						t.Errorf("%s was not made in the folder that was opened: %v", p.Rel, err)
					}
				}
				if made == 0 {
					t.Fatal("no mount point lies in the folder: the test proves nothing")
				}
			})
		}
	}
}

// Prepare reads the clone through the tree it opened: a tree whose name is
// given to another clone right after the lookup is still the one that is copied.
func TestPrepareCopiesFromTheTreeItOpenedAfterASwap(t *testing.T) {
	other := newFixture(t, profile.Claude)
	other.write(other.tree+"/.git/config", "[core]\n\tother = true\n")
	f := newFixture(t, profile.Claude)
	hook, opens := swapOnce(f.tree, func() {
		mustRename(t, f.tree, f.base+"/moved-away")
		mustRename(t, other.tree, f.tree)
	})
	onSubRoot(t, hook)
	f.prepare()
	if *opens != 1 {
		t.Fatalf("the tree was opened %d times, want once", *opens)
	}
	got, err := os.ReadFile(f.gitMeta + "/config")
	if err != nil {
		t.Fatal(err)
	}
	if want := "[core]\n\trepositoryformatversion = 0\n"; string(got) != want {
		t.Fatalf("the copy of config is %q, want that of the tree that was opened, %q", got, want)
	}
}

// The folder Prepare builds the git metadata folder in is opened once, from
// the handle of the lead's state folder, and filled beneath that descriptor.
func TestPrepareFillsTheFolderItOpened(t *testing.T) {
	f := newFixture(t, profile.Claude)
	tmp := f.layout().LeadDir() + "/gitmeta.tmp"
	opened := recordByName(t)
	count := 0
	onSubRoot(t, func(dir string) {
		if dir == tmp {
			count++
		}
	})
	f.prepare()
	if count != 1 {
		t.Fatalf("the folder the git metadata folder is built in was opened %d times, want once", count)
	}
	if len(*opened) != 1 || (*opened)[0] != f.state {
		t.Errorf("opened by name: %q, want only the state root", *opened)
	}
}

// The own-clone check reads the mode and the owner of a folder from the
// descriptor it opened, and looks at the .git of the tree it opened.
func TestOwnCloneDoesNotLookAFolderUpAgainAfterASwap(t *testing.T) {
	uid := uint32(os.Getuid())
	t.Run("a folder", func(t *testing.T) {
		for _, name := range []string{"tree", "home", "config folder"} {
			f := newFixture(t, profile.Claude)
			dir := swapTargets(f)[name]
			opened := recordByName(t)
			hook, opens := swapOnce(dir, func() {
				mustRename(t, dir, f.base+"/moved-away")
				// A folder of another mode, with a .git file: not an own clone.
				f.mkdir(dir, 0o755)
				f.write(dir+"/.git", "gitdir: /elsewhere\n")
			})
			onSubRoot(t, hook)
			rep := f.ownClone(uid)
			if *opens != 1 {
				t.Fatalf("the %s was opened %d times, want once", name, *opens)
			}
			if !rep.OK() {
				t.Fatalf("the %s was looked up again after its name changed: %v", name, rep.Err())
			}
			if len(*opened) != 1 || (*opened)[0] != f.state {
				t.Errorf("opened by name: %q, want only the state root", *opened)
			}
		}
	})
	t.Run("the .git folder", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		git := f.tree + "/.git"
		hook, opens := swapOnce(git, func() {
			mustRename(t, git, f.base+"/moved-away")
			f.mkdir(git, 0o755) // no HEAD, no config, no objects, no refs
		})
		onSubRoot(t, hook)
		rep := f.ownClone(uid)
		if *opens != 1 {
			t.Fatalf(".git was opened %d times, want once", *opens)
		}
		if !rep.OK() {
			t.Fatalf(".git was looked up again after its name changed: %v", rep.Err())
		}
	})
	t.Run("the folder that was opened is the one reported", func(t *testing.T) {
		good := newFixture(t, profile.Claude)
		f := newFixture(t, profile.Claude)
		if err := os.Chmod(f.tree, 0o755); err != nil {
			t.Fatal(err)
		}
		f.remove(f.tree + "/.git/HEAD")
		hook, _ := swapOnce(f.tree, func() {
			mustRename(t, f.tree, f.base+"/moved-away")
			mustRename(t, good.tree, f.tree)
		})
		onSubRoot(t, hook)
		rep := f.ownClone(uid)
		wantProblem(t, rep, ErrMode, f.tree)
		wantProblem(t, rep, ErrNotOwnClone, f.tree+"/.git/HEAD")
	})
}

// openDescriptors lists the descriptors of the process, each with what it
// names, without the one the listing itself uses.
func openDescriptors(t *testing.T) []string {
	t.Helper()
	const dir = "/proc/self/fd"
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no %s: %v", dir, err)
	}
	var out []string
	for _, e := range ents {
		target, err := os.Readlink(dir + "/" + e.Name())
		if err != nil {
			continue // the descriptor of the listing, closed by now
		}
		out = append(out, fmt.Sprintf("%s -> %s", e.Name(), target))
	}
	sort.Strings(out)
	return out
}

// noLeak runs fn and fails when a descriptor is open afterwards that was not
// open before.
func noLeak(t *testing.T, name string, fn func()) {
	t.Helper()
	openDescriptors(t) // the first listing may make the runtime open its own
	before := openDescriptors(t)
	fn()
	after := openDescriptors(t)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("%s: the open descriptors changed\nbefore:\n  %s\nafter:\n  %s", name, strings.Join(before, "\n  "), strings.Join(after, "\n  "))
	}
}

// No call leaves a descriptor open, whether it finds nothing, finds problems
// or is refused.
func TestNoDescriptorIsLeftOpen(t *testing.T) {
	uid := uint32(os.Getuid())
	states := []struct {
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
		{"the home is a link", func(f *fixture) {
			mustRename(f.t, f.home, f.base+"/moved-home")
			f.symlink(f.base+"/moved-home", f.home)
		}},
		{"the config folder is a file", func(f *fixture) { f.remove(f.cfg); f.write(f.cfg, "x") }},
		{"the tree is missing", func(f *fixture) { f.remove(f.tree) }},
		{".git is a link", func(f *fixture) {
			mustRename(f.t, f.tree+"/.git", f.base+"/moved-git")
			f.symlink(f.base+"/moved-git", f.tree+"/.git")
		}},
		{".git is a file", func(f *fixture) { f.remove(f.tree + "/.git"); f.write(f.tree+"/.git", "gitdir: x\n") }},
		{"a mount point is a link", func(f *fixture) { f.remove(f.cfg + "/skills"); f.symlink(f.out, f.cfg+"/skills") }},
		{"a mount point has the wrong type", func(f *fixture) { f.remove(f.tree + "/.envrc"); f.mkdir(f.tree+"/.envrc", 0o755) }},
		{"a mount point is a FIFO", func(f *fixture) {
			f.remove(f.tree + "/.envrc")
			if err := mkfifo(f.tree + "/.envrc"); err != nil {
				f.t.Skipf("no fifo: %v", err)
			}
		}},
		{"a mount point is missing", func(f *fixture) { f.remove(f.home + "/.claude") }},
		{"the git metadata folder is missing", func(f *fixture) { f.remove(f.gitMeta) }},
		{"the git metadata folder is a link", func(f *fixture) {
			mustRename(f.t, f.gitMeta, f.base+"/moved-meta")
			f.symlink(f.base+"/moved-meta", f.gitMeta)
		}},
		{"an entry of the git metadata folder is a link", func(f *fixture) {
			f.remove(f.gitMeta + "/config")
			f.symlink(f.out, f.gitMeta+"/config")
		}},
		{"the exclude file is a folder", func(f *fixture) { f.remove(f.metaExclude()); f.mkdir(f.metaExclude(), 0o700) }},
		{"a leftover of an interrupted Prepare", func(f *fixture) { f.mkdir(f.layout().LeadDir()+"/gitmeta.tmp", 0o700) }},
	}
	for _, s := range states {
		t.Run(s.name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			f.prepare()
			s.spoil(f)
			noLeak(t, "Check", func() {
				if _, err := Check(f.in); err != nil {
					t.Fatalf("Check: %v", err)
				}
			})
			noLeak(t, "CheckOwnClone", func() {
				if _, err := CheckOwnClone(f.in, uid); err != nil {
					t.Fatalf("CheckOwnClone: %v", err)
				}
			})
			noLeak(t, "Prepare", func() { Prepare(f.in, PrepareOptions{NoLeadHasRun: true}) })
		})
	}
	t.Run("the first Prepare", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		noLeak(t, "Prepare", f.prepare)
	})
	t.Run("a Prepare that is interrupted before the move", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		prev := beforeGitMetaMove
		beforeGitMetaMove = func() error { return fmt.Errorf("interrupted") }
		t.Cleanup(func() { beforeGitMetaMove = prev })
		noLeak(t, "Prepare", func() {
			if err := Prepare(f.in, PrepareOptions{NoLeadHasRun: true}); err == nil {
				t.Fatal("Prepare was not interrupted")
			}
		})
	})
	t.Run("refused inputs", func(t *testing.T) {
		f := newFixture(t, profile.Claude)
		f.in.NeverMounted = nil
		noLeak(t, "Check", func() { Check(f.in) })
		noLeak(t, "CheckOwnClone", func() { CheckOwnClone(f.in, uid) })
		noLeak(t, "Prepare", func() { Prepare(f.in, PrepareOptions{NoLeadHasRun: true}) })
	})
}
