package gitenv

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUploadPackQuoting(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	for _, tc := range []struct {
		name string
		prog []string
		want string
	}{
		{"one word", []string{"/usr/bin/wrap"}, `--upload-pack='/usr/bin/wrap'`},
		{"two words", []string{"/usr/bin/wrap", "--flag"}, `--upload-pack='/usr/bin/wrap' '--flag'`},
		{"a space", []string{"/usr/bin/wrap", "a b"}, `--upload-pack='/usr/bin/wrap' 'a b'`},
		{"a quote", []string{"/usr/bin/wrap", "it's"}, `--upload-pack='/usr/bin/wrap' 'it'\''s'`},
		{"only a quote", []string{"/usr/bin/wrap", "'"}, `--upload-pack='/usr/bin/wrap' ''\'''`},
		{"a semicolon", []string{"/usr/bin/wrap", "a;b"}, `--upload-pack='/usr/bin/wrap' 'a;b'`},
		{"a command substitution", []string{"/usr/bin/wrap", "$(id)", "`id`"}, "--upload-pack='/usr/bin/wrap' '$(id)' '`id`'"},
		{"a backslash and a glob", []string{"/usr/bin/wrap", `a\b`, "*"}, `--upload-pack='/usr/bin/wrap' 'a\b' '*'`},
		{"a quote in the program", []string{"/usr/bin/it's a dir/wrap"}, `--upload-pack='/usr/bin/it'\''s a dir/wrap'`},
		{"an option look-alike", []string{"/usr/bin/wrap", "--upload-pack=x"}, `--upload-pack='/usr/bin/wrap' '--upload-pack=x'`},
	} {
		prog := append([]string(nil), tc.prog...)
		c := Cmd{GitDir: "/srv/r.git", Protocols: []string{"file"}, UploadPack: prog,
			Args: []string{"fetch", "--end-of-options", "/srv/src", "+refs/heads/*:refs/heads/*"}}
		got, err := g.Argv(c)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		// Right after "fetch", and before the caller's arguments.
		i := indexOf(got, "fetch")
		if want := []string{"fetch", tc.want, "--end-of-options", "/srv/src", "+refs/heads/*:refs/heads/*"}; !reflect.DeepEqual(got[i:], want) {
			t.Errorf("%s: after the settings\n got  %q\n want %q", tc.name, got[i:], want)
		}
		if !reflect.DeepEqual(prog, tc.prog) {
			t.Errorf("%s: Argv changed UploadPack to %q", tc.name, prog)
		}
		// The -c list is the same as without the field.
		plain, err := g.Argv(Cmd{GitDir: c.GitDir, Protocols: c.Protocols, Args: c.Args})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got[:i], plain[:i]) {
			t.Errorf("%s: UploadPack changed the part before the subcommand", tc.name)
		}
	}
	// "--" is the other end of the options. The unset field changes nothing.
	for _, args := range [][]string{
		{"fetch", "--", "/srv/src"},
		{"fetch", "--prune", "--depth=1", "-q", "--end-of-options", "/srv/src", "refs/heads/a"},
	} {
		c := Cmd{GitDir: "/srv/r.git", Protocols: []string{"file"}, UploadPack: []string{"/usr/bin/wrap"}, Args: args}
		if _, err := g.Argv(c); err != nil {
			t.Errorf("%q: %v", args, err)
		}
	}
	for _, empty := range [][]string{nil, {}} {
		c := Cmd{GitDir: "/srv/r.git", Args: []string{"for-each-ref"}, UploadPack: empty}
		got, err := g.Argv(c)
		if err != nil {
			t.Fatalf("unset field %#v: %v", empty, err)
		}
		for _, a := range got {
			if strings.HasPrefix(a, "--upload-pack") {
				t.Errorf("unset field %#v put %q into the command", empty, a)
			}
		}
	}
}

func TestUploadPackRefuses(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	fetch := []string{"fetch", "--end-of-options", "/srv/src", "+refs/heads/*:refs/heads/*"}
	prog := []string{"/usr/bin/wrap", "--flag"}
	file := []string{"file"}
	base := Cmd{GitDir: "/srv/r.git", Protocols: file, UploadPack: prog, Args: fetch}
	if _, err := g.Argv(base); err != nil {
		t.Fatalf("the base case is refused: %v", err)
	}
	with := func(f func(*Cmd)) Cmd {
		c := base
		c.Args = append([]string(nil), fetch...)
		c.UploadPack = append([]string(nil), prog...)
		f(&c)
		return c
	}
	for _, tc := range []struct {
		name string
		cmd  Cmd
	}{
		{"for-each-ref", with(func(c *Cmd) { c.Args = []string{"for-each-ref"} })},
		{"log", with(func(c *Cmd) { c.Args = []string{"log", "--end-of-options", "/srv/src"} })},
		{"init", with(func(c *Cmd) { c.GitDir = ""; c.Args = []string{"init", "--end-of-options", "/srv/src"} })},
		{"no Protocols", with(func(c *Cmd) { c.Protocols = nil })},
		{"empty Protocols", with(func(c *Cmd) { c.Protocols = []string{} })},
		{"relative program", with(func(c *Cmd) { c.UploadPack[0] = "wrap" })},
		{"dot program", with(func(c *Cmd) { c.UploadPack[0] = "./wrap" })},
		{"empty first element", with(func(c *Cmd) { c.UploadPack[0] = "" })},
		{"empty later element", with(func(c *Cmd) { c.UploadPack = append(c.UploadPack, "") })},
		{"NUL in the program", with(func(c *Cmd) { c.UploadPack[0] = "/usr/bin/wr\x00ap" })},
		{"NUL in an element", with(func(c *Cmd) { c.UploadPack[1] = "a\x00b" })},
		{"newline in the program", with(func(c *Cmd) { c.UploadPack[0] = "/usr/bin/wr\nap" })},
		{"newline in an element", with(func(c *Cmd) { c.UploadPack[1] = "a\nb" })},
		{"no end of options", with(func(c *Cmd) { c.Args = []string{"fetch", "/srv/src"} })},
		{"end of options last", with(func(c *Cmd) { c.Args = []string{"fetch", "--end-of-options"} })},
		{"source before the end of options", with(func(c *Cmd) { c.Args = []string{"fetch", "/srv/src", "--end-of-options", "/srv/other"} })},
		{"option value before the end of options", with(func(c *Cmd) { c.Args = []string{"fetch", "--depth", "1", "--end-of-options", "/srv/src"} })},
		{"dash alone before the end of options", with(func(c *Cmd) { c.Args = []string{"fetch", "-", "--end-of-options", "/srv/src"} })},
		{"relative source", with(func(c *Cmd) { c.Args = []string{"fetch", "--end-of-options", "src"} })},
		{"dot source", with(func(c *Cmd) { c.Args = []string{"fetch", "--end-of-options", "./src"} })},
		{"empty source", with(func(c *Cmd) { c.Args = []string{"fetch", "--end-of-options", ""} })},
		{"file URL", with(func(c *Cmd) { c.Args = []string{"fetch", "--end-of-options", "file:///srv/src"} })},
		{"ssh URL", with(func(c *Cmd) { c.Args = []string{"fetch", "--end-of-options", "ssh://host/srv/src"} })},
		{"host form", with(func(c *Cmd) { c.Args = []string{"fetch", "--end-of-options", "host:/srv/src"} })},
		{"remote helper", with(func(c *Cmd) { c.Args = []string{"fetch", "--end-of-options", "ext::sh -c id"} })},
		{"double colon after a slash", with(func(c *Cmd) { c.Args = []string{"fetch", "--end-of-options", "/srv/a::b"} })},
		{"scheme after a slash", with(func(c *Cmd) { c.Args = []string{"fetch", "--end-of-options", "/srv/a://b"} })},
		{"--upload-pack in Args", with(func(c *Cmd) {
			c.Args = []string{"fetch", "--upload-pack=/usr/bin/other", "--end-of-options", "/srv/src"}
		})},
		{"--upload-pack abbreviated in Args", with(func(c *Cmd) {
			c.Args = []string{"fetch", "--upl=/usr/bin/other", "--end-of-options", "/srv/src"}
		})},
	} {
		argv, err := g.Argv(tc.cmd)
		if err == nil {
			t.Errorf("%s: Argv accepted %q", tc.name, argv)
		}
		if argv != nil {
			t.Errorf("%s: Argv returned %q with its error", tc.name, argv)
		}
	}
	// Without the field, the same option in Args is still refused.
	if _, err := g.Argv(Cmd{GitDir: "/srv/r.git", Protocols: file, Args: []string{"fetch", "--upload-pack=x", "--end-of-options", "/srv/src"}}); err == nil {
		t.Error("--upload-pack in Args was accepted")
	}
}

// TestUploadPackProgramGetsOneWordPerElement fetches from a temporary
// repository through a wrapper script that records its arguments and then
// starts the real git-upload-pack. The elements hold characters a shell
// would act on, and a program path with a space and a quote.
//
// Proves: every element reaches the program as one literal argument, the
// source is the last one, `;` and `$(...)` start nothing, and the fetch
// works. The check of the objects has its own test below. The control shows
// that the same text
// without quoting does run a second command with plain git.
// Cannot prove: that the program is confined; this package does not do that.
func TestUploadPackProgramGetsOneWordPerElement(t *testing.T) {
	g := needGit(t)
	src := source(t, g)
	env := fixtureEnv(t)
	head := fixture(t, g, env, src, "rev-parse", "main")
	_, mark := canary(t)

	dir := filepath.Join(t.TempDir(), "it's a dir")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(dir, "record")
	wrapper := filepath.Join(dir, "wrap")
	// The wrapper writes one argument per line (none holds a newline),
	// then runs the real program on the last argument, the source.
	body := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(record) + "\nfor last; do :; done\nexec git-upload-pack \"$last\"\n"
	if err := os.WriteFile(wrapper, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	elements := []string{
		wrapper,
		"a b",
		"it's",
		"x;: > '" + mark + "'",
		"$(: > '" + mark + "')",
		"`: > '" + mark + "'`",
		"*",
		`back\slash`,
		"--upload-pack=x",
	}

	// Control: git runs the value through the shell, so an unquoted
	// semicolon starts a second command.
	cdir := t.TempDir()
	cmirror := filepath.Join(cdir, "mirror.git")
	fixture(t, g, env, "", "init", "-q", "--bare", cmirror)
	out, err := run([]string{g.Path, "--git-dir=" + cmirror, "fetch", "-q", "--upload-pack=" + shellQuote(wrapper) + " ; : > '" + mark + "'", "--end-of-options", src, "+refs/heads/*:refs/heads/*"}, env, "", "")
	if err != nil {
		t.Logf("control fetch: %v\n%s", err, out)
	}
	mustFire(t, mark, "an unquoted semicolon in --upload-pack")

	mirror := bare(t, g)
	c := Cmd{GitDir: mirror, Protocols: []string{"file"}, UploadPack: elements,
		Args: []string{"fetch", "--end-of-options", src, "+refs/heads/*:refs/heads/*"}}
	if out, err := owned(g, c); err != nil {
		t.Fatalf("fetch through the wrapper: %v\n%s", err, out)
	}
	mustNotFire(t, mark, "the elements of UploadPack")
	data, err := os.ReadFile(record)
	if err != nil {
		t.Fatalf("the wrapper did not run: %v", err)
	}
	want := append(append([]string(nil), elements[1:]...), src)
	if got := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n"); !reflect.DeepEqual(got, want) {
		t.Errorf("the wrapper got\n %q\n want\n %q", got, want)
	}
	if got := mustOwned(t, g, Cmd{GitDir: mirror, Args: []string{"rev-parse", "--verify", "refs/heads/main"}}); got != head {
		t.Errorf("main in the mirror is %q, want %q", got, head)
	}
}

// TestUploadPackFetchChecksObjects fetches a commit with a malformed author
// line through a wrapper program. Git checks the objects on the receiving
// side, so the fetch must fail whichever program serves it.
//
// Proves: with UploadPack set, a malformed object is refused and leaves no
// ref and no object. The control shows that plain git without the check
// takes the same commit through the same wrapper, so the object is only
// refused because of the settings of this package.
// Cannot prove: that every kind of bad object is refused; git's fsck decides.
func TestUploadPackFetchChecksObjects(t *testing.T) {
	g := needGit(t)
	env := fixtureEnv(t, "GIT_ALLOW_PROTOCOL=file")
	evil := filepath.Join(t.TempDir(), "evil.git")
	fixture(t, g, env, "", "init", "-q", "--bare", "-b", "main", evil)
	tree := fixtureIn(t, g, env, evil, "", "mktree")
	// The author line has no email.
	commit := "tree " + tree + "\n" +
		"author Fixture 1767225600 +0000\n" +
		"committer Fixture <fixture@example.invalid> 1767225600 +0000\n" +
		"\nmalformed\n"
	id := fixtureIn(t, g, env, evil, commit, "hash-object", "-t", "commit", "-w", "--literally", "--stdin")
	fixture(t, g, env, evil, "update-ref", "refs/heads/main", id)

	wrapper := filepath.Join(t.TempDir(), "wrap")
	body := "#!/bin/sh\nfor last; do :; done\nexec git-upload-pack \"$last\"\n"
	if err := os.WriteFile(wrapper, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	// Control: plain git, no object check, same wrapper.
	control := bare(t, g)
	if out, err := run([]string{g.Path, "--git-dir=" + control, "fetch", "-q", "--upload-pack=" + shellQuote(wrapper), "--end-of-options", evil, "+refs/heads/*:refs/heads/*"}, env, "", ""); err != nil {
		t.Fatalf("control: plain git did not take the commit through the wrapper, so the test shows nothing: %v\n%s", err, out)
	}
	if got := fixture(t, g, env, control, "rev-parse", "--verify", "refs/heads/main"); got != id {
		t.Fatalf("control: main is %q, want %q", got, id)
	}

	repo := bare(t, g)
	c := Cmd{GitDir: repo, Protocols: []string{"file"}, UploadPack: []string{wrapper},
		Args: []string{"fetch", "--end-of-options", evil, "+refs/heads/*:refs/heads/*"}}
	out, err := owned(g, c)
	if err == nil {
		t.Fatalf("the fetch of a malformed commit through the wrapper succeeded: %s", out)
	}
	if !strings.Contains(out, "fsck") && !strings.Contains(out, "missingEmail") && !strings.Contains(out, "email") {
		t.Errorf("the fetch failed, but the message does not name the check: %q", out)
	}
	if refs := mustOwned(t, g, Cmd{GitDir: repo, Args: []string{"for-each-ref"}}); refs != "" {
		t.Errorf("the failed fetch left refs: %q", refs)
	}
	if out, err := owned(g, Cmd{GitDir: repo, Args: []string{"cat-file", "-e", id}}); err == nil {
		t.Errorf("the failed fetch left the commit %s: %q", id, out)
	}
}
