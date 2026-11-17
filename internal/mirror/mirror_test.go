package mirror

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/exchange/gitenv"
	"ataniq/internal/exchange/safefs"
)

// newMirror creates a mirror for the project "proj" in a fresh state folder.
func newMirror(t *testing.T) (gitenv.Git, *Mirror) {
	t.Helper()
	g := needGit(t)
	state := t.TempDir()
	path, err := Path(state, "proj")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	m, err := Create(context.Background(), g, path)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return g, m
}

func TestCreateLayout(t *testing.T) {
	g, m := newMirror(t)
	if m.Git() != g {
		t.Errorf("Git() = %v, want %v", m.Git(), g)
	}
	if !strings.HasSuffix(m.Path(), "/mirrors/proj.git") {
		t.Errorf("Path() = %q", m.Path())
	}

	fi, err := os.Lstat(m.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Errorf("folder mode = %v, want a folder with 0700", fi.Mode())
	}
	marker, err := os.ReadFile(filepath.Join(m.Path(), "ataniq-mirror"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(marker), "{\"format\":1,\"project\":\"proj\"}\n"; got != want {
		t.Errorf("marker = %q, want %q", got, want)
	}
	mfi, _ := os.Lstat(filepath.Join(m.Path(), "ataniq-mirror"))
	if mfi.Mode().Perm() != 0o600 {
		t.Errorf("marker mode = %v", mfi.Mode())
	}
	hooks, err := os.ReadDir(filepath.Join(m.Path(), "hooks"))
	if err != nil || len(hooks) != 0 {
		t.Errorf("hooks = %v, %v; want an empty folder", hooks, err)
	}
	for _, name := range []string{"HEAD", "config", "objects", "refs"} {
		if _, err := os.Lstat(filepath.Join(m.Path(), name)); err != nil {
			t.Errorf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(m.Path(), ".git")); err == nil {
		t.Error("a bare repository has no .git")
	}
	if got := mirrorGit(t, g, m.Path(), nil, "rev-parse", "--is-bare-repository"); got != "true" {
		t.Errorf("is-bare = %q", got)
	}
	for _, kv := range settings {
		if got := mirrorGit(t, g, m.Path(), nil, "config", "--get", kv[0]); got != kv[1] {
			t.Errorf("config %s = %q, want %q", kv[0], got, kv[1])
		}
	}
	// The owner's choices are not set.
	for _, key := range []string{"receive.denyDeletes", "receive.denyNonFastForwards"} {
		c := gitenv.Cmd{GitDir: m.Path(), Args: []string{"config", "--get", key}}
		if _, err := runGit(context.Background(), g, c, nil, time.Minute, 1024); err == nil {
			t.Errorf("%s is set", key)
		}
	}

	// A reopened mirror is the same.
	m2, err := Open(context.Background(), g, m.Path())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if m2.Path() != m.Path() {
		t.Errorf("reopened path = %q", m2.Path())
	}
}

func TestCreateRefusals(t *testing.T) {
	g := needGit(t)
	ctx := context.Background()
	state := t.TempDir()
	mirrors := filepath.Join(state, "mirrors")
	if err := os.Mkdir(mirrors, 0o700); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()

	// Existing paths: folder, file, symlink (dangling and not).
	if err := os.Mkdir(filepath.Join(mirrors, "dir.git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mirrors, "file.git"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(mirrors, "link.git")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(other, "nothing"), filepath.Join(mirrors, "dangling.git")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"dir", "file", "link", "dangling"} {
		if _, err := Create(ctx, g, filepath.Join(mirrors, name+".git")); !errors.Is(err, fs.ErrExist) {
			t.Errorf("Create over %s: err = %v, want exist", name, err)
		}
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Errorf("Create wrote through a symlink: %v", entries)
	}
	if _, err := os.Lstat(filepath.Join(mirrors, "dir.git", "ataniq-mirror")); err == nil {
		t.Error("Create touched an existing folder")
	}

	// The parent: missing, a symlink, a file.
	if _, err := Create(ctx, g, filepath.Join(state, "absent", "p.git")); err == nil {
		t.Error("Create with a missing parent succeeded")
	}
	if err := os.Symlink(other, filepath.Join(state, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, g, filepath.Join(state, "linked", "p.git")); err == nil {
		// The parent "linked" is a symlink: it is not a real folder.
		t.Error("Create under a symlinked parent succeeded")
	}
	if entries, _ := os.ReadDir(other); len(entries) != 0 {
		t.Errorf("Create wrote through a symlinked parent: %v", entries)
	}
	if err := os.WriteFile(filepath.Join(state, "plain"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(ctx, g, filepath.Join(state, "plain", "p.git")); err == nil {
		t.Error("Create under a file succeeded")
	}

	// Bad paths and names.
	for _, p := range []string{
		"", "p.git", "./p.git", mirrors + "/../mirrors/p.git", mirrors + "/p.git/", mirrors + "/p",
		mirrors + "/P.git", mirrors + "/.git", mirrors + "/a b.git", mirrors + "/p.git.git", "/",
	} {
		if _, err := Create(ctx, g, p); err == nil {
			t.Errorf("Create(%q) succeeded", p)
		}
	}
	if err := ctx.Err(); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Create(cctx, g, filepath.Join(mirrors, "late.git")); err == nil {
		t.Error("Create with a cancelled context succeeded")
	}
	if _, err := os.Lstat(filepath.Join(mirrors, "late.git")); err == nil {
		t.Error("a cancelled Create left a folder")
	}
}

func TestCreateFailureLeavesNothing(t *testing.T) {
	// "false" is an executable that always fails: git init does not work.
	bad, err := gitenv.New("/usr/bin/false")
	if err != nil {
		t.Skipf("no /usr/bin/false: %v", err)
	}
	state := t.TempDir()
	path, _ := Path(state, "proj")
	if err := os.Mkdir(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), bad, path); err == nil {
		t.Fatal("Create with a failing git succeeded")
	}
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a failed Create left %s: %v", path, err)
	}
}

// plant runs fn on the mirror's folder and returns the mirror's path.
type plant func(t *testing.T, g gitenv.Git, dir string)

func appendConfig(text string) plant {
	return func(t *testing.T, g gitenv.Git, dir string) {
		t.Helper()
		f, err := os.OpenFile(filepath.Join(dir, "config"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}
}

func writeFile(rel, text string) plant {
	return func(t *testing.T, g gitenv.Git, dir string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func replaceWithSymlink(rel string) plant {
	return func(t *testing.T, g gitenv.Git, dir string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		target := filepath.Join(t.TempDir(), "target")
		if data, err := os.ReadFile(p); err == nil {
			if err := os.WriteFile(target, data, 0o600); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	}
}

func remove(rel string) plant {
	return func(t *testing.T, g gitenv.Git, dir string) {
		t.Helper()
		if err := os.RemoveAll(filepath.Join(dir, rel)); err != nil {
			t.Fatal(err)
		}
	}
}

func chmod(mode os.FileMode) plant {
	return func(t *testing.T, g gitenv.Git, dir string) {
		t.Helper()
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOpenRefusals(t *testing.T) {
	marker := func(s string) plant { return writeFile("ataniq-mirror", s) }
	cases := []struct {
		name   string
		plant  plant
		reason string // a part of the refusal's text
	}{
		{"group writable", chmod(0o770), "writable by group or others"},
		{"others writable", chmod(0o707), "writable by group or others"},
		{"group write only", chmod(0o720), "writable by group or others"},
		{"marker missing", remove("ataniq-mirror"), "marker file is missing"},
		{"marker a symlink", replaceWithSymlink("ataniq-mirror"), "marker file is missing"},
		{"marker a folder", func(t *testing.T, g gitenv.Git, dir string) {
			remove("ataniq-mirror")(t, g, dir)
			if err := os.Mkdir(filepath.Join(dir, "ataniq-mirror"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, "marker file is missing"},
		{"marker empty", marker(""), "marker file is malformed"},
		{"marker garbage", marker("not json"), "marker file is malformed"},
		{"marker trailing text", marker("{\"format\":1,\"project\":\"proj\"} x"), "marker file is malformed"},
		{"marker two documents", marker("{\"format\":1,\"project\":\"proj\"}{}"), "marker file is malformed"},
		{"marker extra field", marker("{\"format\":1,\"project\":\"proj\",\"x\":1}"), "marker file is malformed"},
		{"marker wrong type", marker("{\"format\":\"1\",\"project\":\"proj\"}"), "marker file is malformed"},
		{"marker unknown version", marker("{\"format\":2,\"project\":\"proj\"}"), "unknown format version"},
		{"marker no version", marker("{\"project\":\"proj\"}"), "unknown format version"},
		{"marker other project", marker("{\"format\":1,\"project\":\"other\"}"), "another project"},
		{"marker too large", marker("{\"format\":1,\"project\":\"" + strings.Repeat("a", 5000) + "\"}"), "marker file is missing"},
		{"not bare", func(t *testing.T, g gitenv.Git, dir string) {
			mirrorGit(t, g, dir, nil, "config", "core.bare", "false")
		}, "not bare"},
		{"bare unset", func(t *testing.T, g gitenv.Git, dir string) {
			mirrorGit(t, g, dir, nil, "config", "--unset", "core.bare")
		}, "not bare"},
		{"HEAD missing", remove("HEAD"), "HEAD is missing"},
		{"HEAD a symlink", replaceWithSymlink("HEAD"), "HEAD is missing"},
		{"objects missing", remove("objects"), "objects is missing"},
		{"objects a symlink", replaceWithSymlink("objects"), "objects is missing"},
		{"refs missing", remove("refs"), "refs is missing"},
		{"hooks missing", remove("hooks"), "hooks is missing"},
		{"hooks a symlink", replaceWithSymlink("hooks"), "hooks is missing"},
		{"planted hook", writeFile("hooks/pre-receive", "#!/bin/sh\nexit 0\n"), "hooks folder is not empty"},
		{"planted sample hook", writeFile("hooks/pre-commit.sample", "x"), "hooks folder is not empty"},
		{"planted hook folder", func(t *testing.T, g gitenv.Git, dir string) {
			if err := os.Mkdir(filepath.Join(dir, "hooks", "post-update.d"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, "hooks folder is not empty"},
		{"planted hook symlink", func(t *testing.T, g gitenv.Git, dir string) {
			if err := os.Symlink("/bin/true", filepath.Join(dir, "hooks", "update")); err != nil {
				t.Fatal(err)
			}
		}, "hooks folder is not empty"},
		{"alternates", writeFile("objects/info/alternates", "/elsewhere/objects\n"), "alternates file"},
		{"http alternates", writeFile("objects/info/http-alternates", "http://example.invalid/\n"), "alternates file"},
		{"alternates empty", writeFile("objects/info/alternates", ""), "alternates file"},
		{"alternates a folder", func(t *testing.T, g gitenv.Git, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "objects", "info", "alternates"), 0o700); err != nil {
				t.Fatal(err)
			}
		}, "alternates file"},
		{"alternates dangling symlink", func(t *testing.T, g gitenv.Git, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "objects", "info"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/nonexistent/alt", filepath.Join(dir, "objects", "info", "alternates")); err != nil {
				t.Fatal(err)
			}
		}, "alternates file"},
		{"objects/info a symlink", replaceWithSymlink("objects/info"), "alternates file cannot be checked"},
		{"config a symlink", replaceWithSymlink("config"), "config file cannot be read"},
		{"config missing", remove("config"), "config file cannot be read"},
		{"config too large", appendConfig("# " + strings.Repeat("x", 70<<10) + "\n"), "config file cannot be read"},
		{"config malformed", appendConfig("[core\n\tx = 1\n"), "config file is malformed"},
		{"config malformed line", appendConfig("[x]\n\t= 1\n"), "config file is malformed"},
		{"include", appendConfig("[include]\n\tpath = /etc/gitconfig\n"), "an include"},
		{"include upper case", appendConfig("[INCLUDE]\n\tPATH = x\n"), "an include"},
		{"include if", appendConfig("[includeIf \"gitdir:/\"]\n\tpath = x\n"), "an include"},
		{"include if upper case", appendConfig("[INCLUDEIF \"onbranch:x\"]\n\tpath = x\n"), "an include"},
		{"hooksPath", appendConfig("[core]\n\thooksPath = /x\n"), "core.hookspath"},
		{"hooksPath odd case", appendConfig("[CoRe]\n\tHoOkSpAtH = /x\n"), "core.hookspath"},
		{"hooksPath after comment", appendConfig("[core]\n\t# a\n\tfoo = 1\n\thooksPath=/x\n"), "core.hookspath"},
		{"hooksPath on the section line", appendConfig("[core] hooksPath = /x\n"), "core.hookspath"},
		{"fsmonitor", appendConfig("[core]\n\tfsmonitor = /x\n"), "core.fsmonitor"},
		{"sshCommand", appendConfig("[core]\n\tsshCommand = /x\n"), "core.sshcommand"},
		{"alternateRefsCommand", appendConfig("[core]\n\talternateRefsCommand = /x\n"), "core.alternaterefscommand"},
		{"worktree", appendConfig("[core]\n\tworktree = /x\n"), "core.worktree"},
		{"packObjectsHook", appendConfig("[uploadpack]\n\tpackObjectsHook = /x\n"), "uploadpack.packObjectsHook"},
		{"url rewrite", appendConfig("[url \"https://a.invalid/\"]\n\tinsteadOf = b:\n"), "url rewrite"},
		{"url pushInsteadOf", appendConfig("[url \"x\"]\n\tpushInsteadOf = y\n"), "url rewrite"},
		{"filter", appendConfig("[filter \"f\"]\n\tclean = /x\n"), "a filter"},
		{"filter required", appendConfig("[filter \"f\"]\n\trequired = true\n"), "a filter"},
		{"diff command", appendConfig("[diff \"d\"]\n\tcommand = /x\n"), "diff command or textconv"},
		{"diff textconv", appendConfig("[diff \"d\"]\n\ttextconv = /x\n"), "diff command or textconv"},
		{"diff textconv dotted driver", appendConfig("[diff \"a.b\"]\n\tTextConv = /x\n"), "diff command or textconv"},
		{"diff external", appendConfig("[diff]\n\texternal = /x\n"), "diff.external"},
		{"remote", appendConfig("[remote \"origin\"]\n\turl = /x\n"), "a remote"},
		{"remote bare section", appendConfig("[remote]\n\tpushDefault = origin\n"), "a remote"},
		{"alias", appendConfig("[alias]\n\tpush = !sh -c x\n"), "an alias"},
		{"alias upper", appendConfig("[ALIAS]\n\tx = y\n"), "an alias"},
		{"core.bare set twice, last false", appendConfig("[core]\n\tbare = false\n"), "not bare"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g, m := newMirror(t)
			tc.plant(t, g, m.Path())
			got, err := Open(context.Background(), g, m.Path())
			if !errors.Is(err, ErrNotMirror) {
				t.Fatalf("Open = %v, %v; want ErrNotMirror", got, err)
			}
			if !strings.Contains(err.Error(), tc.reason) {
				t.Errorf("error %q does not say %q", err, tc.reason)
			}
		})
	}
}

func TestOpenPathRefusals(t *testing.T) {
	g, m := newMirror(t)
	ctx := context.Background()
	state := t.TempDir()

	// A symlink to a real mirror, under a name that passes the name check.
	link := filepath.Join(state, "proj.git")
	if err := os.Symlink(m.Path(), link); err != nil {
		t.Fatal(err)
	}
	_, err := Open(ctx, g, link)
	if !errors.Is(err, ErrNotMirror) || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("Open(symlink to a mirror) = %v", err)
	}
	// A symlink in the parent is the owner's and is resolved as usual.
	parentLink := filepath.Join(state, "via")
	if err := os.Symlink(filepath.Dir(m.Path()), parentLink); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, g, filepath.Join(parentLink, "proj.git")); err != nil {
		t.Errorf("Open through a symlinked parent: %v", err)
	}

	// A plain file, a missing path, and a dangling link.
	file := filepath.Join(state, "file.git")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, g, file); !errors.Is(err, ErrNotMirror) {
		t.Errorf("Open(file) = %v", err)
	}
	if _, err := Open(ctx, g, filepath.Join(state, "absent.git")); !errors.Is(err, ErrNotMirror) || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(absent) = %v", err)
	}
	dangling := filepath.Join(state, "dangling.git")
	if err := os.Symlink(filepath.Join(state, "nothing"), dangling); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, g, dangling); !errors.Is(err, ErrNotMirror) {
		t.Errorf("Open(dangling) = %v", err)
	}
	// An empty folder is not a mirror.
	empty := filepath.Join(state, "empty.git")
	if err := os.Mkdir(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, g, empty); !errors.Is(err, ErrNotMirror) {
		t.Errorf("Open(empty) = %v", err)
	}
	// A mirror moved under another name is for another project.
	moved := filepath.Join(state, "moved.git")
	if err := os.Rename(m.Path(), moved); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, g, moved); !errors.Is(err, ErrNotMirror) || !strings.Contains(err.Error(), "another project") {
		t.Errorf("Open(moved) = %v", err)
	}
	// Paths that are not a place for a mirror at all.
	for _, p := range []string{"", "x.git", moved + "/", filepath.Join(state, "a b.git"), "/"} {
		if _, err := Open(ctx, g, p); err == nil {
			t.Errorf("Open(%q) succeeded", p)
		}
	}
}

func TestOpenAcceptsOtherSettings(t *testing.T) {
	g, m := newMirror(t)
	// Unknown sections and look-alikes are not refused, and neither is a
	// folder that group and others can read.
	appendConfig("[foo]\n\tbar = 1\n[remotes]\n\tx = y\n[core]\n\tabbrev = 12\n[receive]\n\tdenyDeletes = true\n")(t, g, m.Path())
	if err := os.Chmod(m.Path(), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), g, m.Path()); err != nil {
		t.Errorf("Open: %v", err)
	}
}

func TestOpenDoesNotRunPlantedProgram(t *testing.T) {
	// A hook named in a planted core.hooksPath would run on a ref update.
	// The control shows that the planted hook fires with plain git; Open on
	// the same folder must refuse it without running anything.
	g, m := newMirror(t)
	hooks := t.TempDir()
	canary := filepath.Join(t.TempDir(), "canary")
	script := "#!/bin/sh\ntouch '" + canary + "'\n"
	if err := os.WriteFile(filepath.Join(hooks, "reference-transaction"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	appendConfig("[core]\n\thooksPath = "+hooks+"\n")(t, g, m.Path())

	fixtureGit(t, g, t.TempDir(), "--git-dir="+m.Path(), "symbolic-ref", "HEAD", "refs/heads/control")
	if _, err := os.Lstat(canary); err != nil {
		t.Fatalf("the control did not fire, so this test proves nothing: %v", err)
	}
	if err := os.Remove(canary); err != nil {
		t.Fatal(err)
	}

	if _, err := Open(context.Background(), g, m.Path()); !errors.Is(err, ErrNotMirror) {
		t.Fatalf("Open = %v", err)
	}
	if _, err := os.Lstat(canary); err == nil {
		t.Error("a planted hook ran")
	}
}

func TestCheckConfig(t *testing.T) {
	list := func(entries ...string) []byte { return []byte(strings.Join(entries, "\x00") + "\x00") }
	if r := checkConfig(list("core.bare\ntrue", "core.logallrefupdates\nalways")); r != "" {
		t.Errorf("plain config refused: %s", r)
	}
	if r := checkConfig(list("core.bare")); r != "" {
		t.Errorf("bare without a value is true: %s", r)
	}
	for _, v := range []string{"yes", "on", "1", "TRUE"} {
		if r := checkConfig(list("core.bare\n" + v)); r != "" {
			t.Errorf("core.bare=%s refused: %s", v, r)
		}
	}
	for _, v := range []string{"false", "no", "off", "0", "", "maybe"} {
		if r := checkConfig(list("core.bare\n" + v)); r == "" {
			t.Errorf("core.bare=%q accepted", v)
		}
	}
	// A subsection of an allowed section with a refused variable name is
	// not the refused key: core has no subsections in git.
	if r := checkConfig(list("core.bare\ntrue", "core.x.hookspath\n/x")); r != "" {
		t.Errorf("core.x.hookspath refused: %s", r)
	}
	// The reason never holds a subsection or a value.
	r := checkConfig(list("core.bare\ntrue", "remote.SECRET-NAME.url\nSECRET-VALUE"))
	if r == "" || strings.Contains(r, "SECRET") {
		t.Errorf("reason = %q", r)
	}
	if r := checkConfig(nil); r == "" {
		t.Error("an empty listing has no core.bare and was accepted")
	}
}

func TestWithLimits(t *testing.T) {
	_, m := newMirror(t)
	if l := m.Limits(); l.Timeout != DefaultTimeout || l.MaxRefs != DefaultMaxRefs {
		t.Errorf("default limits = %+v", l)
	}
	m2, err := m.WithLimits(Limits{Timeout: time.Second, MaxRefs: 5})
	if err != nil {
		t.Fatal(err)
	}
	if l := m2.Limits(); l.Timeout != time.Second || l.MaxRefs != 5 {
		t.Errorf("limits = %+v", l)
	}
	if l := m.Limits(); l.MaxRefs != DefaultMaxRefs {
		t.Errorf("WithLimits changed the original: %+v", l)
	}
	if _, err := m.WithLimits(Limits{Timeout: -1}); err == nil {
		t.Error("a negative timeout was accepted")
	}
	if _, err := m.WithLimits(Limits{MaxRefs: -1}); err == nil {
		t.Error("a negative ref cap was accepted")
	}
}

func TestSize(t *testing.T) {
	_, m := newMirror(t)
	ctx := context.Background()
	lim := safefs.SizeLimits{MaxBytes: 1 << 30, MaxEntries: 100000}
	before, err := m.Size(ctx, lim)
	if err != nil {
		t.Fatal(err)
	}
	if before.Entries < 10 || before.Bytes <= 0 {
		t.Fatalf("size = %+v, want the whole folder", before)
	}

	// A symlink to a big folder counts as one entry and adds no bytes.
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "big"), make([]byte, 4<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(m.Path(), "refs", "planted")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "big"), filepath.Join(m.Path(), "planted-file")); err != nil {
		t.Fatal(err)
	}
	after, err := m.Size(ctx, lim)
	if err != nil {
		t.Fatal(err)
	}
	if after.Entries != before.Entries+2 {
		t.Errorf("entries %d -> %d, want +2 (a symlink is one entry)", before.Entries, after.Entries)
	}
	if after.Bytes != before.Bytes {
		t.Errorf("bytes %d -> %d: a symlink was followed", before.Bytes, after.Bytes)
	}

	// The limits hold.
	if _, err := m.Size(ctx, safefs.SizeLimits{MaxBytes: 1 << 30, MaxEntries: 3}); !errors.Is(err, safefs.ErrLimit) {
		t.Errorf("entry limit: err = %v", err)
	}
	if _, err := m.Size(ctx, safefs.SizeLimits{MaxBytes: 1, MaxEntries: 1 << 20}); !errors.Is(err, safefs.ErrLimit) {
		t.Errorf("byte limit: err = %v", err)
	}
	if _, err := m.Size(ctx, safefs.SizeLimits{}); err == nil {
		t.Error("zero limits were accepted")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := m.Size(cctx, lim); err == nil {
		t.Error("Size with a cancelled context succeeded")
	}

	// A mirror folder swapped for a symlink is not walked.
	moved := m.Path() + ".moved"
	if err := os.Rename(m.Path(), moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(moved, m.Path()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Size(ctx, lim); !errors.Is(err, safefs.ErrEscape) {
		t.Errorf("Size through a symlinked mirror: err = %v, want ErrEscape", err)
	}
}
