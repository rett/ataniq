package mirror

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"ataniq/internal/exchange/gitenv"
)

func TestLockFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj.git.lock")
	release, err := lockFile(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
		t.Fatalf("lock file = %v, %v; want a regular file with mode 0600", fi, err)
	}
	// A second holder, in the same process, is refused: flock belongs to the
	// open file, not to the process.
	if _, err := lockFile(path); !errors.Is(err, ErrBusy) {
		t.Errorf("second lock: err = %v, want ErrBusy", err)
	}
	release()
	release2, err := lockFile(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	// The file stays, and a later holder uses the same file.
	release2()
	if _, err := os.Lstat(path); err != nil {
		t.Errorf("the lock file was removed: %v", err)
	}

	// A symlink at the path is not followed, also when it leads to a file.
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.lock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if rel, err := lockFile(link); err == nil || errors.Is(err, ErrBusy) {
		if rel != nil {
			rel()
		}
		t.Errorf("lock through a symlink: err = %v", err)
	}
	// Another lock on the target proves that nothing held it.
	if rel, err := lockFile(target); err != nil {
		t.Errorf("the target was locked through the symlink: %v", err)
	} else {
		rel()
	}
	// A folder is not a lock file.
	folder := filepath.Join(dir, "folder.lock")
	if err := os.Mkdir(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	if rel, err := lockFile(folder); err == nil {
		rel()
		t.Error("a folder was taken as a lock file")
	}
	// A path in a folder that is missing.
	if rel, err := lockFile(filepath.Join(dir, "absent", "x.lock")); err == nil {
		rel()
		t.Error("a lock file in a missing folder was made")
	}
}

var stagingPath = regexp.MustCompile(`\.git\.staging-[0-9a-f]{16}$`)

func TestNewStaging(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	s, err := f.m.newStaging(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s.remove()
	if !stagingPath.MatchString(s.path) || filepath.Dir(s.path) != filepath.Dir(f.m.Path()) {
		t.Errorf("staging path = %q", s.path)
	}
	fi, err := os.Lstat(s.path)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatalf("staging folder = %v, %v", fi, err)
	}
	if got, err := os.ReadFile(filepath.Join(s.path, "ataniq-staging")); err != nil || string(got) != "ataniq-mirror-staging 1\n" {
		t.Errorf("marker = %q, %v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(s.path, "objects", "info", "alternates")); err != nil || string(got) != f.m.Path()+"/objects\n" {
		t.Errorf("alternates = %q, %v", got, err)
	}
	if hooks, err := os.ReadDir(filepath.Join(s.path, "hooks")); err != nil || len(hooks) != 0 {
		t.Errorf("hooks = %v, %v; want an empty folder", hooks, err)
	}
	if got := mirrorGit(t, f.g, s.path, nil, "rev-parse", "--is-bare-repository"); got != "true" {
		t.Errorf("is-bare = %q", got)
	}
	if reason, err := s.check(ctx); err != nil || reason != "" {
		t.Errorf("check of a fresh staging repository = %q, %v", reason, err)
	}
	// The mirror's objects are visible through the alternate, and the staging
	// repository has no ref of its own.
	if got := mirrorGit(t, f.g, s.path, nil, "rev-list", "--objects", "--end-of-options", f.first); !strings.Contains(got, f.first) {
		t.Errorf("the mirror's commit is not visible in the staging repository: %q", got)
	}
	if recs, err := f.m.snapshot(ctx, s.path, 10, 1<<20); err != nil || len(recs) != 0 {
		t.Errorf("refs of the staging repository = %v, %v", recs, err)
	}
	// A second one has another name.
	s2, err := f.m.newStaging(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.remove()
	if s2.path == s.path {
		t.Error("two staging repositories share a path")
	}
	s.remove()
	if _, err := os.Lstat(s.path); err == nil {
		t.Error("remove left the folder")
	}
	s.remove() // twice is fine
}

func TestNewStagingFailureLeavesNothing(t *testing.T) {
	_, m := newMirror(t)
	bad := &Mirror{git: fakeGit(t, "exit 1"), path: m.Path(), project: m.project}
	if _, err := bad.newStaging(context.Background()); err == nil {
		t.Fatal("a failing git made a staging repository")
	}
	entries, _ := os.ReadDir(filepath.Dir(m.Path()))
	if len(entries) != 1 {
		t.Errorf("left in the mirrors folder: %v", entries)
	}
	if _, err := (&Mirror{path: "/tmp/new\nline.git"}).newStaging(context.Background()); err == nil {
		t.Error("a mirror path with a newline was accepted")
	}
}

func TestStagingCheckRefusals(t *testing.T) {
	cases := []struct {
		name   string
		plant  plant
		reason string
	}{
		{"alternate changed", writeFile("objects/info/alternates", "/elsewhere/objects\n"), "alternates file is not the one written"},
		{"alternate added", func(t *testing.T, g gitenv.Git, dir string) {
			b, err := os.ReadFile(filepath.Join(dir, "objects", "info", "alternates"))
			if err != nil {
				t.Fatal(err)
			}
			writeFile("objects/info/alternates", string(b)+"/elsewhere/objects\n")(t, g, dir)
		}, "alternates file is not the one written"},
		{"alternate removed", remove("objects/info/alternates"), "alternates file is not the one written"},
		{"alternate a symlink", replaceWithSymlink("objects/info/alternates"), "alternates file is not the one written"},
		{"http alternates", writeFile("objects/info/http-alternates", "http://example.invalid/\n"), "http-alternates file"},
		{"shallow", writeFile("shallow", ""), "shallow file"},
		{"shallow a symlink", replaceWithSymlink("shallow"), "shallow file"},
		{"commondir", writeFile("commondir", "../x\n"), "commondir file"},
		{"gitdir", writeFile("gitdir", "/x\n"), "gitdir file"},
		{"config.worktree", writeFile("config.worktree", "[core]\n\thooksPath = /x\n"), "config.worktree file"},
		{"extension", appendConfig("[extensions]\n\tworktreeConfig = true\n"), "an extension"},
		{"url rewrite", appendConfig("[url \"/e/\"]\n\tinsteadOf = /x/\n"), "url rewrite"},
		{"hooksPath", appendConfig("[core]\n\thooksPath = /x\n"), "core.hookspath"},
		{"not bare", func(t *testing.T, g gitenv.Git, dir string) {
			mirrorGit(t, g, dir, nil, "config", "core.bare", "false")
		}, "not bare"},
		{"config removed", remove("config"), "config file cannot be read"},
		{"hook", writeFile("hooks/pre-receive", "#!/bin/sh\n"), "hooks folder is not empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			s, err := f.m.newStaging(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer s.remove()
			tc.plant(t, f.g, s.path)
			reason, err := s.check(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(reason, tc.reason) {
				t.Errorf("reason = %q, want it to hold %q", reason, tc.reason)
			}
		})
	}
}

func TestRemoveStale(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	mirrors := filepath.Dir(f.m.Path())
	base := filepath.Base(f.m.Path())
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A leftover of a crashed fetch, with a symlink inside that must not be
	// followed.
	crashed, err := f.m.newStaging(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(crashed.path, "refs", "escape")); err != nil {
		t.Fatal(err)
	}

	mk := func(name, marker string) string {
		p := filepath.Join(mirrors, name)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		if marker != "" {
			if err := os.WriteFile(filepath.Join(p, "ataniq-staging"), []byte(marker), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(p, "data"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	hex := "0123456789abcdef"
	keep := []string{
		mk(base+".staging-"+hex, ""),                                      // similar name, no marker
		mk(base+".staging-"+hex[:15]+"0", "something else\n"),             // marker with other text
		mk(base+".staging-"+hex[:15]+"1", "ataniq-mirror-staging 1\nx\n"), // marker with more text
		mk(base+".staging-xyz", "ataniq-mirror-staging 1\n"),              // marker, name not ours
		mk(base+".staging-"+strings.ToUpper(hex), "ataniq-mirror-staging 1\n"),
		mk("other.git.staging-"+hex, "ataniq-mirror-staging 1\n"), // another mirror's
		mk(base+".stagingX"+hex, "ataniq-mirror-staging 1\n"),
	}
	// A symlink with a good name that leads to a folder with a marker, and a
	// plain file with a good name.
	trap := t.TempDir()
	if err := os.WriteFile(filepath.Join(trap, "ataniq-staging"), []byte("ataniq-mirror-staging 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(mirrors, base+".staging-"+hex[:15]+"2")
	if err := os.Symlink(trap, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(mirrors, base+".staging-"+hex[:15]+"3")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	keep = append(keep, link, file)
	stale := mk(base+".staging-"+hex[:15]+"4", "ataniq-mirror-staging 1\n")

	f.m.removeStale()

	for _, p := range []string{crashed.path, stale} {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("leftover %s was not removed", filepath.Base(p))
		}
	}
	for _, p := range keep {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Lstat(filepath.Join(trap, "ataniq-staging")); err != nil {
		t.Errorf("the folder behind a symlink lost its content: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "keep")); err != nil {
		t.Errorf("a symlink inside a leftover was followed: %v", err)
	}
	if _, err := os.Lstat(f.m.Path()); err != nil {
		t.Errorf("the mirror was removed: %v", err)
	}
}

func TestRemoveStaleRemovesAnEmptyFolderOnly(t *testing.T) {
	// A crash between the making of the folder and the writing of the marker
	// leaves an empty folder with the name of a staging folder. It is
	// removed, with rmdir semantics only.
	f := newFixture(t)
	mirrors := filepath.Dir(f.m.Path())
	base := filepath.Base(f.m.Path())
	hex := "0123456789abcdef"
	dir := func(name string) string {
		p := filepath.Join(mirrors, name)
		if err := os.Mkdir(p, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	empty := dir(base + ".staging-" + hex)
	// What must stay: a folder with content and no marker, an empty folder
	// whose name is not a staging name or is another mirror's, a symlink to
	// an empty folder, a plain file, and a folder that holds only an empty
	// folder.
	nonEmpty := dir(base + ".staging-" + hex[:15] + "0")
	if err := os.WriteFile(filepath.Join(nonEmpty, "keep"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	nested := dir(base + ".staging-" + hex[:15] + "1")
	if err := os.Mkdir(filepath.Join(nested, "inner"), 0o700); err != nil {
		t.Fatal(err)
	}
	keep := []string{
		nonEmpty, nested,
		dir(base + ".staging-xyz"),
		dir("other.git.staging-" + hex),
		dir(base + ".staging-" + strings.ToUpper(hex)),
	}
	target := t.TempDir()
	link := filepath.Join(mirrors, base+".staging-"+hex[:15]+"2")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(mirrors, base+".staging-"+hex[:15]+"3")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	keep = append(keep, link, file)

	f.m.removeStale()

	if _, err := os.Lstat(empty); err == nil {
		t.Error("an empty folder with a staging name was not removed")
	}
	for _, p := range keep {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Lstat(target); err != nil {
		t.Errorf("the folder behind a symlink was removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(nonEmpty, "keep")); err != nil {
		t.Errorf("a folder with content lost it: %v", err)
	}

	// A fetch does it too, and the folder is gone with the next one.
	e := &fetchEnv{g: f.g, m: f.m, state: stateOf(f.m), planner: newPlanner(t)}
	e.lead = newLeadClone(t, f.g, e.state, "coder")
	empty2 := dir(base + ".staging-" + hex[:15] + "4")
	if _, err := f.m.Fetch(context.Background(), e.req()); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := os.Lstat(empty2); err == nil {
		t.Error("a fetch left an empty staging folder")
	}
}

func TestSnapshot(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.updateRef(t, "refs/leads/coder/heads/x\xff", f.first)
	mirrorGit(t, f.g, f.m.Path(), nil, "symbolic-ref", "refs/heads/alias", "refs/heads/main")

	recs, err := f.m.snapshotMirror(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range recs {
		got = append(got, r.Name)
	}
	want := []string{
		"refs/heads/alias", "refs/heads/develop", "refs/heads/main",
		"refs/leads/coder/heads/develop", "refs/leads/coder/heads/feature/x",
		"refs/leads/coder/heads/x\xff", "refs/leads/other/heads/develop",
		"refs/tags/t-blob", "refs/tags/t-commit", "refs/tags/t-tree", "refs/tags/v1",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("snapshot names = %q\nwant %q", got, want)
	}
	for _, r := range recs {
		switch r.Name {
		case "refs/heads/alias":
			if r.Symref != "refs/heads/main" {
				t.Errorf("symref of alias = %q", r.Symref)
			}
		case "refs/tags/v1":
			if r.ID != f.src.tag || r.Type != "tag" || r.Symref != "" {
				t.Errorf("v1 = %+v", r)
			}
		}
	}
	again, err := f.m.snapshotMirror(ctx)
	if err != nil || len(again) != len(recs) {
		t.Fatalf("second snapshot: %v, %v", again, err)
	}
	for i := range recs {
		if recs[i] != again[i] {
			t.Errorf("snapshots differ at %d: %+v and %+v", i, recs[i], again[i])
		}
	}
	// Too many refs is an error, never a cut list.
	if _, err := f.m.snapshot(ctx, f.m.Path(), 3, 1<<20); !errors.Is(err, ErrTooManyRefs) {
		t.Errorf("snapshot with a cap of 3: err = %v", err)
	}
	if _, err := f.m.snapshot(ctx, f.m.Path(), len(recs), 1<<20); err != nil {
		t.Errorf("snapshot with a cap of exactly its size: %v", err)
	}
	// A slow git is bounded.
	slow := &Mirror{git: fakeGit(t, "exec sleep 30"), path: f.m.Path(), limits: Limits{Timeout: 200 * time.Millisecond}}
	if _, err := slow.snapshotMirror(ctx); err == nil {
		t.Error("a slow git was not stopped")
	}
}
