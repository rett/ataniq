//go:build linux

package mountpoints

import (
	"os"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

func (f *fixture) ownClone(uid uint32) Report {
	f.t.Helper()
	rep, err := CheckOwnClone(f.in, uid)
	if err != nil {
		f.t.Fatalf("CheckOwnClone: %v", err)
	}
	return rep
}

func TestOwnCloneClean(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		f := newFixture(t, rt)
		if rep := f.ownClone(uint32(os.Getuid())); !rep.OK() {
			t.Fatalf("%s: %v", rt, rep.Err())
		}
	}
}

func TestOwnCloneRefusals(t *testing.T) {
	uid := uint32(os.Getuid())
	cases := []struct {
		name  string
		setup func(f *fixture)
		kind  error
		at    func(f *fixture) string
	}{
		{"a .git file (worktree or submodule)", func(f *fixture) {
			f.remove(f.tree + "/.git")
			f.write(f.tree+"/.git", "gitdir: /elsewhere/.git/worktrees/x\n")
		}, ErrNotOwnClone, func(f *fixture) string { return f.tree + "/.git" }},
		{"no .git at all", func(f *fixture) { f.remove(f.tree + "/.git") }, ErrNotOwnClone, func(f *fixture) string { return f.tree + "/.git" }},
		{".git is a link", func(f *fixture) {
			f.mkdir(f.out+"/real.git", 0o755)
			f.remove(f.tree + "/.git")
			f.symlink(f.out+"/real.git", f.tree+"/.git")
		}, ErrSymlink, func(f *fixture) string { return f.tree + "/.git" }},
		{"no HEAD", func(f *fixture) { f.remove(f.tree + "/.git/HEAD") }, ErrNotOwnClone, func(f *fixture) string { return f.tree + "/.git/HEAD" }},
		{"no config", func(f *fixture) { f.remove(f.tree + "/.git/config") }, ErrNotOwnClone, func(f *fixture) string { return f.tree + "/.git/config" }},
		{"no objects", func(f *fixture) { f.remove(f.tree + "/.git/objects") }, ErrNotOwnClone, func(f *fixture) string { return f.tree + "/.git/objects" }},
		{"no refs", func(f *fixture) { f.remove(f.tree + "/.git/refs") }, ErrNotOwnClone, func(f *fixture) string { return f.tree + "/.git/refs" }},
		{"objects is a link", func(f *fixture) {
			f.mkdir(f.out+"/objects", 0o755)
			f.remove(f.tree + "/.git/objects")
			f.symlink(f.out+"/objects", f.tree+"/.git/objects")
		}, ErrSymlink, func(f *fixture) string { return f.tree + "/.git/objects" }},
		{"refs is a file", func(f *fixture) {
			f.remove(f.tree + "/.git/refs")
			f.write(f.tree+"/.git/refs", "x")
		}, ErrWrongType, func(f *fixture) string { return f.tree + "/.git/refs" }},
		{"commondir present", func(f *fixture) { f.write(f.tree+"/.git/commondir", "../..\n") }, ErrNotOwnClone, func(f *fixture) string { return f.tree + "/.git/commondir" }},
		{"alternates present", func(f *fixture) {
			f.mkdir(f.tree+"/.git/objects/info", 0o755)
			f.write(f.tree+"/.git/objects/info/alternates", f.base+"/owner/work/.git/objects\n")
		}, ErrNotOwnClone, func(f *fixture) string { return f.tree + "/.git/objects/info/alternates" }},
		{"http-alternates present", func(f *fixture) {
			f.mkdir(f.tree+"/.git/objects/info", 0o755)
			f.write(f.tree+"/.git/objects/info/http-alternates", "http://example.invalid/objects\n")
		}, ErrNotOwnClone, func(f *fixture) string { return f.tree + "/.git/objects/info/http-alternates" }},
		{"alternates is a link", func(f *fixture) {
			f.mkdir(f.tree+"/.git/objects/info", 0o755)
			f.symlink(f.out, f.tree+"/.git/objects/info/alternates")
		}, ErrNotOwnClone, func(f *fixture) string { return f.tree + "/.git/objects/info/alternates" }},
		{"below a repository in the lead's folder", func(f *fixture) { f.mkdir(f.state+"/leads/arc-lead/.git", 0o755) }, ErrNotOwnClone, func(f *fixture) string { return f.state + "/leads/arc-lead/.git" }},
		{"below a repository in leads", func(f *fixture) { f.mkdir(f.state+"/leads/.git", 0o755) }, ErrNotOwnClone, func(f *fixture) string { return f.state + "/leads/.git" }},
		{"below a repository at the state root (a file)", func(f *fixture) { f.write(f.state+"/.git", "gitdir: x\n") }, ErrNotOwnClone, func(f *fixture) string { return f.state + "/.git" }},
		{"below a repository (a link)", func(f *fixture) { f.symlink(f.out, f.state+"/leads/.git") }, ErrNotOwnClone, func(f *fixture) string { return f.state + "/leads/.git" }},
		{"tree mode", func(f *fixture) { os.Chmod(f.tree, 0o755) }, ErrMode, func(f *fixture) string { return f.tree }},
		{"tree mode group execute", func(f *fixture) { os.Chmod(f.tree, 0o710) }, ErrMode, func(f *fixture) string { return f.tree }},
		{"home mode", func(f *fixture) { os.Chmod(f.home, 0o750) }, ErrMode, func(f *fixture) string { return f.home }},
		{"config mode", func(f *fixture) { os.Chmod(f.cfg, 0o770) }, ErrMode, func(f *fixture) string { return f.cfg }},
		{"setgid tree", func(f *fixture) { os.Chmod(f.tree, 0o700|os.ModeSetgid) }, ErrMode, func(f *fixture) string { return f.tree }},
		{"tree is a link", func(f *fixture) {
			os.Rename(f.tree, f.out+"/tree")
			f.symlink(f.out+"/tree", f.tree)
		}, ErrSymlink, func(f *fixture) string { return f.tree }},
		{"home is a link", func(f *fixture) {
			os.Rename(f.home, f.out+"/home")
			f.symlink(f.out+"/home", f.home)
		}, ErrSymlink, func(f *fixture) string { return f.home }},
		{"config is a link", func(f *fixture) {
			os.Rename(f.cfg, f.out+"/cfg")
			f.symlink(f.out+"/cfg", f.cfg)
		}, ErrSymlink, func(f *fixture) string { return f.cfg }},
		{"requests mode", func(f *fixture) { os.Chmod(f.layout().Requests(), 0o755) }, ErrMode, func(f *fixture) string { return f.layout().Requests() }},
		{"mail mode", func(f *fixture) { os.Chmod(f.layout().Mail(), 0o701) }, ErrMode, func(f *fixture) string { return f.layout().Mail() }},
		{"requests missing", func(f *fixture) { f.remove(f.layout().Requests()) }, ErrMissing, func(f *fixture) string { return f.layout().Requests() }},
		{"mail missing", func(f *fixture) { f.remove(f.layout().Mail()) }, ErrMissing, func(f *fixture) string { return f.layout().Mail() }},
		{"requests is a link", func(f *fixture) {
			os.Rename(f.layout().Requests(), f.out+"/requests")
			f.symlink(f.out+"/requests", f.layout().Requests())
		}, ErrSymlink, func(f *fixture) string { return f.layout().Requests() }},
		{"mail is a link", func(f *fixture) {
			os.Rename(f.layout().Mail(), f.out+"/mail")
			f.symlink(f.out+"/mail", f.layout().Mail())
		}, ErrSymlink, func(f *fixture) string { return f.layout().Mail() }},
		{"mail is a file", func(f *fixture) {
			f.remove(f.layout().Mail())
			f.write(f.layout().Mail(), "x")
		}, ErrWrongType, func(f *fixture) string { return f.layout().Mail() }},
		{"tree is a file", func(f *fixture) {
			f.remove(f.tree)
			f.write(f.tree, "x")
		}, ErrWrongType, func(f *fixture) string { return f.tree }},
		{"config missing", func(f *fixture) { f.remove(f.cfg) }, ErrMissing, func(f *fixture) string { return f.cfg }},
		{"a link in the lead's path", func(f *fixture) {
			os.Rename(f.state+"/leads/arc-lead", f.out+"/lead")
			f.symlink(f.out+"/lead", f.state+"/leads/arc-lead")
		}, ErrSymlink, func(f *fixture) string { return f.tree }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			c.setup(f)
			rep := f.ownClone(uid)
			if !rep.Has(c.kind) {
				t.Fatalf("want %v, got %v", c.kind, rep.Err())
			}
			if c.at != nil {
				wantProblem(t, rep, c.kind, c.at(f))
			}
		})
	}
}

func TestOwnCloneObjectsInfoWithoutAlternates(t *testing.T) {
	// A clone has objects/info (and objects/pack); only the alternates files
	// are refused.
	f := newFixture(t, profile.Claude)
	f.mkdir(f.tree+"/.git/objects/info", 0o755)
	f.write(f.tree+"/.git/objects/info/packs", "\n")
	if rep := f.ownClone(uint32(os.Getuid())); !rep.OK() {
		t.Fatal(rep.Err())
	}
}

func TestOwnCloneOwner(t *testing.T) {
	f := newFixture(t, profile.Claude)
	other := uint32(os.Getuid()) + 1
	rep := f.ownClone(other)
	for _, p := range []string{f.tree, f.home, f.cfg, f.layout().Requests(), f.layout().Mail()} {
		wantProblem(t, rep, ErrOwner, p)
	}
	if rep.Has(ErrMode) {
		t.Fatalf("modes are fine: %v", rep.Err())
	}
}

func TestOwnCloneReportsEverything(t *testing.T) {
	f := newFixture(t, profile.Claude)
	os.Chmod(f.tree, 0o755)
	f.remove(f.tree + "/.git/HEAD")
	f.write(f.tree+"/.git/commondir", "x")
	f.mkdir(f.state+"/.git", 0o755)
	rep := f.ownClone(uint32(os.Getuid()))
	if len(rep.Problems) != 4 {
		t.Fatalf("got %v", rep.Err())
	}
}

func TestOwnCloneRefusesInvalidInputs(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.in.Project = "Bad Name"
	if _, err := CheckOwnClone(f.in, 0); err == nil {
		t.Fatal("no error")
	} else {
		wantKind(t, err, ErrInvalid)
	}
}

func TestOwnCloneStateRootMissing(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.remove(f.state)
	rep := f.ownClone(uint32(os.Getuid()))
	wantProblem(t, rep, ErrMissing, f.state)
}
