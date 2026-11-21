package gitenv

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestSourceFileURLInArgv: with SourceFileURL the source that the caller
// wrote as an absolute path stands in the command line as a "file://" URL
// with each "%" written "%25", and nothing else changes. Without the field
// the source is as written. The field needs UploadPack, and a source that is
// itself a URL stays refused.
func TestSourceFileURLInArgv(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	prog := []string{"/usr/bin/wrap"}
	for _, tc := range []struct{ src, want string }{
		{"/srv/src", "file:///srv/src"},
		{"/srv/a%41", "file:///srv/a%2541"},
		{"/srv/100%", "file:///srv/100%25"},
		{"/srv/%25", "file:///srv/%2525"},
		{"/srv/a b#c?d:e~f\\g", "file:///srv/a b#c?d:e~f\\g"},
	} {
		args := []string{"fetch", "--prune", "--end-of-options", tc.src, "+refs/heads/*:refs/heads/*"}
		c := Cmd{GitDir: "/srv/r.git", Protocols: []string{"file"}, UploadPack: prog, SourceFileURL: true, Args: args}
		got, err := g.Argv(c)
		if err != nil {
			t.Errorf("%q: %v", tc.src, err)
			continue
		}
		i := indexOf(got, "fetch")
		want := []string{"fetch", "--upload-pack='/usr/bin/wrap'", "--prune", "--end-of-options", tc.want, "+refs/heads/*:refs/heads/*"}
		if !reflect.DeepEqual(got[i:], want) {
			t.Errorf("%q: after the settings\n got  %q\n want %q", tc.src, got[i:], want)
		}
		if args[3] != tc.src {
			t.Errorf("%q: Argv changed the caller's Args to %q", tc.src, args)
		}
		c.SourceFileURL = false
		plain, err := g.Argv(c)
		if err != nil {
			t.Fatal(err)
		}
		if want[4] = tc.src; !reflect.DeepEqual(plain[i:], want) {
			t.Errorf("%q: without the field\n got  %q\n want %q", tc.src, plain[i:], want)
		}
	}

	fetch := []string{"fetch", "--end-of-options", "/srv/src"}
	for name, c := range map[string]Cmd{
		"no UploadPack":      {GitDir: "/srv/r.git", Protocols: []string{"file"}, SourceFileURL: true, Args: fetch},
		"a file URL source":  {GitDir: "/srv/r.git", Protocols: []string{"file"}, UploadPack: prog, SourceFileURL: true, Args: []string{"fetch", "--end-of-options", "file:///srv/src"}},
		"a relative source":  {GitDir: "/srv/r.git", Protocols: []string{"file"}, UploadPack: prog, SourceFileURL: true, Args: []string{"fetch", "--end-of-options", "src"}},
		"a host-form source": {GitDir: "/srv/r.git", Protocols: []string{"file"}, UploadPack: prog, SourceFileURL: true, Args: []string{"fetch", "--end-of-options", "host:/srv/src"}},
		"not a fetch":        {GitDir: "/srv/r.git", Protocols: []string{"file"}, UploadPack: prog, SourceFileURL: true, Args: []string{"rev-list", "--end-of-options", "/srv/src"}},
	} {
		if argv, err := g.Argv(c); err == nil {
			t.Errorf("%s: Argv accepted %q", name, argv)
		}
	}
}

// TestSourceFileURLWithGit asks the installed git what the field relies on.
//
//   - A "file://" URL gives the program the path, with "%XX" decoded, and
//     the encoding of the field makes that the path the caller wrote: for
//     each odd name, the wrapper records the source it was given and the
//     fetch works. The control, a "file://" URL without the encoding, gives
//     the wrapper another path for a name with "%41".
//   - A file at the path that is a git bundle: with the source as a plain
//     path, git reads the bundle itself and the wrapper never starts (the
//     control); with the field, the wrapper starts and the bundle is not
//     read.
//
// Proves: with this git, the field has both effects. Cannot prove: anything
// for another git version.
func TestSourceFileURLWithGit(t *testing.T) {
	g := needGit(t)
	env := fixtureEnv(t)
	src := source(t, g)
	head := fixture(t, g, env, src, "rev-parse", "main")

	wrapDir := t.TempDir()
	record := filepath.Join(wrapDir, "record")
	wrapper := filepath.Join(wrapDir, "wrap")
	body := "#!/bin/sh\nfor last; do :; done\nprintf '%s' \"$last\" > " + shellQuote(record) + "\nexec git-upload-pack \"$last\"\n"
	if err := os.WriteFile(wrapper, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	fetch := func(t *testing.T, source string, asURL bool) (string, error) {
		t.Helper()
		_ = os.Remove(record)
		repo := bare(t, g)
		out, err := owned(g, Cmd{GitDir: repo, Protocols: []string{"file"}, UploadPack: []string{wrapper}, SourceFileURL: asURL,
			Args: []string{"fetch", "--end-of-options", source, "+refs/heads/*:refs/heads/*"}})
		if err == nil {
			if got := mustOwned(t, g, Cmd{GitDir: repo, Args: []string{"rev-parse", "--verify", "refs/heads/main"}}); got != head {
				t.Errorf("%s: main is %q, want %q", source, got, head)
			}
		}
		return out, err
	}
	recorded := func() (string, bool) {
		data, err := os.ReadFile(record)
		return string(data), err == nil
	}

	names := []string{"plain", "pc%41", "pc%2F", "pc%25", "pct%", "sp ace", "ha#sh", "qu?ery", "co:lon", "br[ack]",
		"tilde~", `back\slash`, "at@x", "semi;", "eq=a", "plus+x", "star*", "it's", `dq"x`, "tab\tx", "ünï"}
	root := t.TempDir()
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name)
			fixture(t, g, env, "", "clone", "-q", "--bare", src, path)
			if out, err := fetch(t, path, true); err != nil {
				t.Fatalf("with the field: %v\n%s", err, out)
			}
			if got, ok := recorded(); !ok || got != path {
				t.Errorf("the wrapper got %q (ran: %v), want %q", got, ok, path)
			}
		})
	}

	t.Run("control: no encoding", func(t *testing.T) {
		path := filepath.Join(root, "pc%41")
		_ = os.Remove(record)
		repo := bare(t, g)
		// A URL that the caller writes is refused by Argv, so the command line
		// is built from the path and then changed by hand.
		argv, aerr := g.Argv(Cmd{GitDir: repo, Protocols: []string{"file"}, UploadPack: []string{wrapper},
			Args: []string{"fetch", "--end-of-options", path, "+refs/heads/*:refs/heads/*"}})
		if aerr != nil {
			t.Fatal(aerr)
		}
		for i, a := range argv {
			if a == path {
				argv[i] = "file://" + path
			}
		}
		if out, err := run(argv, g.Env(Cmd{GitDir: repo, Protocols: []string{"file"}}), "", ""); err == nil {
			t.Logf("control fetch ended cleanly:\n%s", out)
		}
		if got, ok := recorded(); !ok || got != strings.ReplaceAll(path, "%41", "A") {
			t.Errorf("control: the wrapper got %q (ran: %v), want the decoded %q", got, ok, strings.ReplaceAll(path, "%41", "A"))
		}
	})

	t.Run("a bundle at the path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "work")
		fixture(t, g, env, src, "bundle", "create", path, "main")
		// The control: as a plain path, git reads the bundle itself.
		if out, err := fetch(t, path, false); err != nil {
			t.Fatalf("control: the plain path of a bundle was not fetched: %v\n%s", err, out)
		}
		if got, ok := recorded(); ok {
			t.Fatalf("control: the wrapper ran (with %q), so the test shows nothing", got)
		}
		// With the field the wrapper is started, and git-upload-pack fails
		// on the bundle, so nothing is read from it.
		out, err := fetch(t, path, true)
		if err == nil {
			t.Fatalf("a bundle at the path was fetched with the field:\n%s", out)
		}
		if got, ok := recorded(); !ok || got != path {
			t.Errorf("the wrapper got %q (ran: %v), want %q", got, ok, path)
		}
	})
}
