//go:build linux

package mountpoints

import (
	"io/fs"
	"os"
	"strings"
	"testing"

	"ataniq/internal/leadsandbox/profile"
)

func modeOf(t *testing.T, p string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode()
}

func TestPrepareFillsTheGitMetaFolder(t *testing.T) {
	for _, rt := range profile.Runtimes() {
		t.Run(string(rt), func(t *testing.T) {
			f := newFixture(t, rt)
			f.write(f.tree+"/.git/config", "[core]\n\tbare = false\n[user]\n\tname = x\n")
			// The clone's hooks are not copied: a managed lead's tree has none.
			f.write(f.tree+"/.git/hooks/pre-commit", "#!/bin/sh\n")
			f.write(f.tree+"/.git/info/attributes", "*.txt text\n")
			f.mkdir(f.tree+"/.git/info/sub", 0o755)
			f.write(f.tree+"/.git/info/sub/inner", "not copied")
			f.write(f.out+"/target", "outside")
			f.symlink(f.out+"/target", f.tree+"/.git/info/link")
			cloneBefore := listing(t, f.tree+"/.git")
			f.prepare()

			if got, _ := os.ReadFile(f.gitMeta + "/config"); string(got) != "[core]\n\tbare = false\n[user]\n\tname = x\n" {
				t.Errorf("config = %q", got)
			}
			if names, err := os.ReadDir(f.gitMeta + "/hooks"); err != nil || len(names) != 0 {
				t.Errorf("hooks = %v %v, want an empty folder", names, err)
			}
			var infoNames []string
			entries, _ := os.ReadDir(f.gitMeta + "/info")
			for _, e := range entries {
				infoNames = append(infoNames, e.Name())
			}
			if strings.Join(infoNames, " ") != "attributes exclude" {
				t.Errorf("info entries = %v: only regular files are copied", infoNames)
			}
			if got, _ := os.ReadFile(f.gitMeta + "/info/attributes"); string(got) != "*.txt text\n" {
				t.Errorf("attributes = %q", got)
			}
			if got, want := f.exclude(), "# comment\n/.envrc\n/.vscode\n/tools/local/x.txt\n"; got != want {
				t.Errorf("exclude = %q, want %q", got, want)
			}
			for p, want := range map[string]fs.FileMode{
				f.gitMeta:                      fs.ModeDir | 0o700,
				f.gitMeta + "/config":          0o600,
				f.gitMeta + "/hooks":           fs.ModeDir | 0o700,
				f.gitMeta + "/info":            fs.ModeDir | 0o700,
				f.gitMeta + "/info/attributes": 0o600,
				f.gitMeta + "/info/exclude":    0o600,
			} {
				if got := modeOf(t, p); got != want {
					t.Errorf("%s: mode %v, want %v", p, got, want)
				}
			}
			// The clone itself is not changed, except that .git/hooks stays.
			if after := listing(t, f.tree+"/.git"); strings.Join(after, "\n") != strings.Join(cloneBefore, "\n") {
				t.Errorf("the clone's .git changed\nbefore: %v\nafter:  %v", cloneBefore, after)
			}
			if rep := f.check(); !rep.OK() {
				t.Fatalf("check: %v", rep.Err())
			}
			// Not a link and not in a folder the lead can write.
			for _, w := range []string{f.home, f.cfg, f.tree} {
				if strings.HasPrefix(f.gitMeta, w+"/") || f.gitMeta == w {
					t.Errorf("the git metadata folder %s lies in %s", f.gitMeta, w)
				}
			}
		})
	}
}

func TestPrepareGitMetaBounds(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fixture)
		kind  error
	}{
		{"config over the cap", func(f *fixture) {
			f.write(f.tree+"/.git/config", strings.Repeat("#"+strings.Repeat("x", 1022)+"\n", 1100))
		}, ErrUnreadable},
		{"config hard-linked", func(f *fixture) {
			if err := os.Link(f.tree+"/.git/config", f.out+"/alias"); err != nil {
				f.t.Fatal(err)
			}
		}, ErrWrongType},
		{"more info entries than the cap", func(f *fixture) {
			for i := 0; i < maxInfoEntries+1; i++ {
				f.write(f.tree+"/.git/info/f"+strings.Repeat("x", i%7)+string(rune('a'+i/7)), "x")
			}
		}, ErrUnreadable},
		{"an info file over the cap", func(f *fixture) { f.write(f.tree+"/.git/info/big", strings.Repeat("x", maxInfoFile+1)) }, ErrUnreadable},
		{"info files over the total", func(f *fixture) {
			for _, n := range []string{"a", "b", "c", "d", "e"} {
				f.write(f.tree+"/.git/info/"+n, strings.Repeat("x", maxInfoFile))
			}
		}, ErrUnreadable},
		{"an info file hard-linked", func(f *fixture) {
			f.write(f.tree+"/.git/info/attributes", "x")
			if err := os.Link(f.tree+"/.git/info/attributes", f.out+"/alias"); err != nil {
				f.t.Fatal(err)
			}
		}, ErrWrongType},
		{"config is a link", func(f *fixture) {
			f.write(f.out+"/other", "x")
			f.remove(f.tree + "/.git/config")
			f.symlink(f.out+"/other", f.tree+"/.git/config")
		}, ErrSymlink},
		{"info is a link", func(f *fixture) {
			f.mkdir(f.out+"/other", 0o755)
			f.remove(f.tree + "/.git/info")
			f.symlink(f.out+"/other", f.tree+"/.git/info")
		}, ErrSymlink},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			tt.setup(f)
			before := listing(t, f.base)
			wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), tt.kind)
			// Everything is looked at before anything is written.
			sameListing(t, before, listing(t, f.base))
			if _, err := os.Lstat(f.gitMeta); err == nil {
				t.Error("a folder was made")
			}
		})
	}
}

func TestPrepareKeepsAnExistingGitMetaFolderAndOnlyChecksIt(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.prepare()
	// A change in the clone after the first Prepare is not copied again.
	f.write(f.tree+"/.git/config", "[core]\n\tchanged = true\n")
	before := listing(t, f.base)
	f.prepare()
	sameListing(t, before, listing(t, f.base))
	if got, _ := os.ReadFile(f.gitMeta + "/config"); strings.Contains(string(got), "changed") {
		t.Errorf("an existing config was replaced: %q", got)
	}

	// An incomplete folder (an interrupted Prepare) is refused, not repaired.
	f.remove(f.gitMeta + "/hooks")
	before = listing(t, f.base)
	wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrMissing)
	sameListing(t, before, listing(t, f.base))
	wantProblem(t, f.check(), ErrMissing, f.gitMeta+"/hooks")

	// A folder that is a link, even to a good copy, is refused.
	f = newFixture(t, profile.Claude)
	f.prepare()
	if err := os.Rename(f.gitMeta, f.out+"/moved"); err != nil {
		t.Fatal(err)
	}
	f.symlink(f.out+"/moved", f.gitMeta)
	wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrSymlink)
	wantProblem(t, f.check(), ErrSymlink, f.gitMeta)
}

func TestPrepareNeverWritesThroughALinkAtTheGitMetaFolder(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.mkdir(f.out+"/elsewhere", 0o755)
	f.symlink(f.out+"/elsewhere", f.gitMeta)
	wantKind(t, Prepare(f.in, PrepareOptions{NoLeadHasRun: true}), ErrSymlink)
	if names, _ := os.ReadDir(f.out + "/elsewhere"); len(names) != 0 {
		t.Fatalf("something was written through the link: %v", names)
	}
}

func TestCheckGitMeta(t *testing.T) {
	type tc struct {
		name  string
		setup func(f *fixture)
		kind  error
		path  func(f *fixture) string
	}
	meta := func(rel string) func(f *fixture) string {
		return func(f *fixture) string { return f.gitMeta + rel }
	}
	tests := []tc{
		{"folder missing", func(f *fixture) { f.remove(f.gitMeta) }, ErrMissing, meta("")},
		{"folder is a link", func(f *fixture) {
			f.mkdir(f.out+"/g", 0o700)
			f.remove(f.gitMeta)
			f.symlink(f.out+"/g", f.gitMeta)
		}, ErrSymlink, meta("")},
		{"folder is a file", func(f *fixture) { f.remove(f.gitMeta); f.write(f.gitMeta, "x") }, ErrWrongType, meta("")},
		{"config missing", func(f *fixture) { f.remove(f.gitMeta + "/config") }, ErrMissing, meta("/config")},
		{"config is a link", func(f *fixture) {
			f.write(f.out+"/c", "x")
			f.remove(f.gitMeta + "/config")
			f.symlink(f.out+"/c", f.gitMeta+"/config")
		}, ErrSymlink, meta("/config")},
		{"config is a folder", func(f *fixture) { f.remove(f.gitMeta + "/config"); f.mkdir(f.gitMeta+"/config", 0o700) }, ErrWrongType, meta("/config")},
		{"hooks missing", func(f *fixture) { f.remove(f.gitMeta + "/hooks") }, ErrMissing, meta("/hooks")},
		{"hooks is a link", func(f *fixture) {
			f.mkdir(f.out+"/h", 0o700)
			f.remove(f.gitMeta + "/hooks")
			f.symlink(f.out+"/h", f.gitMeta+"/hooks")
		}, ErrSymlink, meta("/hooks")},
		{"hooks is a file", func(f *fixture) { f.remove(f.gitMeta + "/hooks"); f.write(f.gitMeta+"/hooks", "x") }, ErrWrongType, meta("/hooks")},
		{"info missing", func(f *fixture) { f.remove(f.gitMeta + "/info") }, ErrMissing, meta("/info")},
		{"info is a link", func(f *fixture) {
			f.mkdir(f.out+"/i", 0o700)
			f.write(f.out+"/i/exclude", "/.envrc\n/.vscode\n/tools/local/x.txt\n")
			f.remove(f.gitMeta + "/info")
			f.symlink(f.out+"/i", f.gitMeta+"/info")
		}, ErrSymlink, meta("/info")},
		{"info is a file", func(f *fixture) { f.remove(f.gitMeta + "/info"); f.write(f.gitMeta+"/info", "x") }, ErrWrongType, meta("/info")},
		{"folder mode", func(f *fixture) { os.Chmod(f.gitMeta, 0o755) }, ErrMode, meta("")},
		{"config mode", func(f *fixture) { os.Chmod(f.gitMeta+"/config", 0o666) }, ErrMode, meta("/config")},
		{"hooks mode", func(f *fixture) { os.Chmod(f.gitMeta+"/hooks", 0o777) }, ErrMode, meta("/hooks")},
		{"info mode", func(f *fixture) { os.Chmod(f.gitMeta+"/info", 0o770) }, ErrMode, meta("/info")},
		{"the lead's folder is a link", func(f *fixture) {
			if err := os.Rename(f.state+"/leads/arc-lead", f.out+"/moved"); err != nil {
				f.t.Fatal(err)
			}
			f.symlink(f.out+"/moved", f.state+"/leads/arc-lead")
		}, ErrSymlink, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t, profile.Claude)
			f.prepare()
			tt.setup(f)
			before := listing(t, f.base)
			rep := f.check()
			sameListing(t, before, listing(t, f.base))
			if !rep.Has(tt.kind) {
				t.Fatalf("want %v, got %v", tt.kind, rep.Err())
			}
			if tt.path != nil {
				wantProblem(t, rep, tt.kind, tt.path(f))
			}
		})
	}
}

func TestCheckGitMetaIsAboutAtaniqsCopyNotTheClone(t *testing.T) {
	f := newFixture(t, profile.Claude)
	f.prepare()
	// What the lead does to the clone's own files does not matter to the
	// source of the binds: only the mount points in the tree are checked.
	f.write(f.tree+"/.git/config", "[core]\n\thooksPath = /elsewhere\n")
	f.write(f.tree+"/.git/info/exclude", "")
	f.write(f.tree+"/.git/hooks/post-checkout", "#!/bin/sh\n")
	if rep := f.check(); !rep.OK() {
		t.Fatalf("%v", rep.Err())
	}
}
