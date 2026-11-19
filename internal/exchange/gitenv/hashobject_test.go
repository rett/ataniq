package gitenv

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// TestHashObjectHasOneForm covers the rule for "hash-object": the arguments
// are exactly "-tcommit", "-w", "--stdin", in that order, and every other
// form is refused with a message that names the one form.
func TestHashObjectHasOneForm(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	ok := "/srv/r.git"

	argv, err := g.Argv(Cmd{GitDir: ok, Args: []string{"hash-object", "-tcommit", "-w", "--stdin"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"--git-dir=/srv/r.git", "hash-object", "-tcommit", "-w", "--stdin"}; !reflect.DeepEqual(argv[len(argv)-5:], want) {
		t.Errorf("the command line ends %q, want %q", argv[len(argv)-5:], want)
	}

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"no argument", nil},
		{"only the type", []string{"-tcommit"}},
		{"no -w", []string{"-tcommit", "--stdin"}},
		{"no --stdin", []string{"-tcommit", "-w"}},
		{"no type", []string{"-w", "--stdin"}},
		{"another order: -w first", []string{"-w", "-tcommit", "--stdin"}},
		{"another order: --stdin first", []string{"--stdin", "-tcommit", "-w"}},
		{"another order: --stdin before -w", []string{"-tcommit", "--stdin", "-w"}},
		{"the type as a separate argument", []string{"-t", "commit", "-w", "--stdin"}},
		{"the type alone as -t", []string{"-t", "-w", "--stdin"}},
		{"another type: blob", []string{"-tblob", "-w", "--stdin"}},
		{"another type: tree", []string{"-ttree", "-w", "--stdin"}},
		{"another type: tag", []string{"-ttag", "-w", "--stdin"}},
		{"the type in another case", []string{"-tCommit", "-w", "--stdin"}},
		{"an extra argument", []string{"-tcommit", "-w", "--stdin", "x"}},
		{"a file name", []string{"-tcommit", "-w", "/etc/hostname"}},
		{"a file name after the form", []string{"-tcommit", "-w", "--stdin", "/etc/hostname"}},
		{"a file name alone", []string{"/etc/hostname"}},
		{"--literally", []string{"-tcommit", "-w", "--stdin", "--literally"}},
		{"--literally first", []string{"--literally", "-tcommit", "-w", "--stdin"}},
		{"--path=", []string{"-tcommit", "-w", "--stdin", "--path=f.txt"}},
		{"--path= first", []string{"--path=f.txt", "-tcommit", "-w", "--stdin"}},
		{"--path with a separate value", []string{"-tcommit", "-w", "--stdin", "--path", "f.txt"}},
		{"--no-filters", []string{"-tcommit", "-w", "--stdin", "--no-filters"}},
		{"--no-filters first", []string{"--no-filters", "-tcommit", "-w", "--stdin"}},
		{"--stdin-paths", []string{"-tcommit", "-w", "--stdin-paths"}},
		{"--stdin-paths after the form", []string{"-tcommit", "-w", "--stdin", "--stdin-paths"}},
		{"the marker --", []string{"-tcommit", "-w", "--stdin", "--"}},
		{"the marker --end-of-options", []string{"-tcommit", "-w", "--stdin", "--end-of-options"}},
		{"a marker first", []string{"--", "-tcommit", "-w", "--stdin"}},
		{"an argument repeated", []string{"-tcommit", "-w", "-w", "--stdin"}},
		{"the type repeated", []string{"-tcommit", "-tblob", "-w", "--stdin"}},
		{"flags joined", []string{"-wtcommit", "--stdin"}},
		{"a long form of the type", []string{"--type=commit", "-w", "--stdin"}},
		{"an empty argument", []string{"-tcommit", "-w", "--stdin", ""}},
		{"a space in an argument", []string{"-tcommit -w", "--stdin"}},
		{"an abbreviation of --stdin", []string{"-tcommit", "-w", "--std"}},
	} {
		argv, err := g.Argv(Cmd{GitDir: ok, Args: append([]string{"hash-object"}, tc.args...)})
		if err == nil {
			t.Errorf("%s: Argv accepted %q", tc.name, argv)
			continue
		}
		if argv != nil {
			t.Errorf("%s: Argv returned %q with its error", tc.name, argv)
		}
		if !strings.Contains(err.Error(), `"hash-object -tcommit -w --stdin"`) || !strings.Contains(err.Error(), "one form only") {
			t.Errorf("%s: error %q does not name the one allowed form", tc.name, err)
		}
	}

	// The rules for every command hold for this one too.
	for _, tc := range []struct {
		name string
		cmd  Cmd
	}{
		{"no GitDir", Cmd{Args: []string{"hash-object", "-tcommit", "-w", "--stdin"}}},
		{"a relative GitDir", Cmd{GitDir: "r.git", Args: []string{"hash-object", "-tcommit", "-w", "--stdin"}}},
		{"an unknown protocol", Cmd{GitDir: ok, Protocols: []string{"ext"}, Args: []string{"hash-object", "-tcommit", "-w", "--stdin"}}},
		{"UploadPack", Cmd{GitDir: ok, Protocols: []string{"file"}, UploadPack: []string{"/usr/bin/wrap"}, Args: []string{"hash-object", "-tcommit", "-w", "--stdin"}}},
		{"an option before the subcommand", Cmd{GitDir: ok, Args: []string{"--literally", "hash-object", "-tcommit", "-w", "--stdin"}}},
		{"a NUL in an argument", Cmd{GitDir: ok, Args: []string{"hash-object", "-tcommit", "-w", "--stdin\x00"}}},
	} {
		if argv, err := g.Argv(tc.cmd); err == nil {
			t.Errorf("%s: Argv accepted %q", tc.name, argv)
		}
	}
}

// TestHashObjectFlagsServeNoOtherSubcommand keeps the rule to its one
// subcommand: "hash-object" is not on the list that the option rule serves,
// its two short arguments are not flags any other subcommand may use, and
// "commit-tree" is not allowed in any form.
func TestHashObjectFlagsServeNoOtherSubcommand(t *testing.T) {
	g := Git{Path: "/usr/bin/git"}
	ok := "/srv/r.git"
	if slices.Contains(subcommands, hashObject) {
		t.Errorf("%q is in subcommands; it has its own rule", hashObject)
	}
	if want := []string{"-tcommit", "-w", "--stdin"}; !reflect.DeepEqual(hashObjectArgs, want) {
		t.Errorf("hashObjectArgs = %q, want %q", hashObjectArgs, want)
	}
	for _, flag := range []string{"-tcommit", "-w", "-t"} {
		if slices.Contains(valuelessFlags, flag) {
			t.Errorf("valuelessFlags holds %q", flag)
		}
		for _, sub := range subcommands {
			c := Cmd{GitDir: ok, Args: []string{sub, flag}}
			if slices.Contains(noGitDir, sub) {
				c.GitDir = ""
			}
			if argv, err := g.Argv(c); err == nil {
				t.Errorf("Argv accepted %q", argv)
			}
		}
	}
	for _, args := range [][]string{
		{"commit-tree"},
		{"commit-tree", "-m", "x", "4b825dc642cb6eb9a060e54bf8d69288fbee4904"},
		{"commit-tree", "-p4b825dc642cb6eb9a060e54bf8d69288fbee4904", "4b825dc642cb6eb9a060e54bf8d69288fbee4904"},
		{"commit-tree", "--end-of-options", "4b825dc642cb6eb9a060e54bf8d69288fbee4904"},
		{"mktree"},
		{"mktag"},
		{"write-tree"},
		{"commit"},
		{"merge"},
	} {
		if argv, err := g.Argv(Cmd{GitDir: ok, Args: args}); err == nil {
			t.Errorf("Argv accepted %q", argv)
		}
	}
}

// ownedIn runs a command built by this package with text on its standard
// input, and returns its output.
func ownedIn(g Git, c Cmd, stdin string) (string, error) {
	argv, err := g.Argv(c)
	if err != nil {
		return "", err
	}
	return run(argv, g.Env(c), c.Dir, stdin)
}

// TestHashObjectWritesAMergeCommit runs the one allowed form with the
// installed git. A commit text with two parents, written here in full, is
// stored as it stands: the object read back is the text byte for byte, and
// git lists the parents in the order written. The same text with the
// parents swapped is another commit. A malformed text is refused and
// nothing is stored.
//
// Proves: with this git, the form writes exactly the text it is given, keeps
// the order of the parents, and refuses these malformed texts. Cannot prove:
// that git refuses every malformed text; it accepts a header line it does not
// know, which the package comment says.
func TestHashObjectWritesAMergeCommit(t *testing.T) {
	g := needGit(t)
	s := newMergeScene(t, g, "")
	form := Cmd{GitDir: s.repo, Args: []string{"hash-object", "-tcommit", "-w", "--stdin"}}
	const who = "Host <host@example.invalid> 1767225600 +0000"
	text := func(first, second string) string {
		return "tree " + s.oursTree + "\n" +
			"parent " + first + "\n" +
			"parent " + second + "\n" +
			"author " + who + "\n" +
			"committer " + who + "\n" +
			"\n" +
			"Merge the fixture\n" +
			"\n" +
			"Request-Id: r-1\n"
	}

	want := text(s.ours, s.theirs)
	out, err := ownedIn(g, form, want)
	if err != nil {
		t.Fatalf("hash-object: %v\n%s", err, out)
	}
	id := strings.TrimSpace(out)
	if !IsObjectID(id) {
		t.Fatalf("hash-object printed %q, not one object id", out)
	}
	kind, err := ownedIn(g, Cmd{GitDir: s.repo, Args: []string{"cat-file", "--batch-check"}}, id+"\n")
	if wantKind := id + " commit " + strconv.Itoa(len(want)) + "\n"; err != nil || kind != wantKind {
		t.Errorf("cat-file says %q (err=%v), want %q", kind, err, wantKind)
	}
	// The object as stored, read with plain git so that no byte is trimmed.
	stored, err := run([]string{g.Path, "--git-dir=" + s.repo, "cat-file", "commit", id}, s.env, "", "")
	if err != nil || stored != want {
		t.Errorf("the stored commit is not the text written (err=%v):\n got  %q\n want %q", err, stored, want)
	}
	parents := mustOwned(t, g, Cmd{GitDir: s.repo, Args: []string{"log", "--format=%H %P", "--max-count=1", "--end-of-options", id}})
	if wantLine := id + " " + s.ours + " " + s.theirs; parents != wantLine {
		t.Errorf("parents as git lists them:\n got  %q\n want %q", parents, wantLine)
	}

	// The order of the parents is part of the commit.
	out, err = ownedIn(g, form, text(s.theirs, s.ours))
	if err != nil {
		t.Fatalf("hash-object with the parents swapped: %v\n%s", err, out)
	}
	swapped := strings.TrimSpace(out)
	if swapped == id {
		t.Errorf("the commit with the parents swapped has the same id %s", id)
	}
	parents = mustOwned(t, g, Cmd{GitDir: s.repo, Args: []string{"log", "--format=%H %P", "--max-count=1", "--end-of-options", swapped}})
	if wantLine := swapped + " " + s.theirs + " " + s.ours; parents != wantLine {
		t.Errorf("parents of the swapped commit:\n got  %q\n want %q", parents, wantLine)
	}

	// Malformed texts. Each is refused, and the repository holds no more
	// objects than before.
	count := func() string {
		return mustOwned(t, g, Cmd{GitDir: s.repo, Args: []string{"count-objects"}})
	}
	before := count()
	for _, tc := range []struct {
		name string
		text string
	}{
		{"a parent that is not an object id", strings.Replace(want, "parent "+s.theirs, "parent not-an-object-id", 1)},
		{"a parent cut short", strings.Replace(want, "parent "+s.theirs, "parent "+s.theirs[:39], 1)},
		{"no tree line", strings.TrimPrefix(want, "tree "+s.oursTree+"\n")},
		{"a tree that is not an object id", strings.Replace(want, "tree "+s.oursTree, "tree main", 1)},
		{"no author", strings.Replace(want, "author "+who+"\n", "", 1)},
		{"no committer", strings.Replace(want, "committer "+who+"\n", "", 1)},
		{"a parent before the tree", "parent " + s.ours + "\n" + strings.Replace(want, "parent "+s.ours+"\n", "", 1)},
		{"an author with no date", strings.Replace(want, "author "+who, "author Host <host@example.invalid>", 1)},
		{"an empty text", ""},
		{"a text that is not a commit", "hello\n"},
	} {
		out, err := ownedIn(g, form, tc.text)
		if err == nil {
			t.Errorf("%s: git wrote %s", tc.name, strings.TrimSpace(out))
			continue
		}
		if status := exitStatus(t, err); status == 0 {
			t.Errorf("%s: exit status 0", tc.name)
		}
		if IsObjectID(strings.TrimSpace(out)) {
			t.Errorf("%s: git failed but printed an object id: %q", tc.name, out)
		}
	}
	if after := count(); after != before {
		t.Errorf("the refused texts changed the object count:\nbefore %q\nafter  %q", before, after)
	}
}
