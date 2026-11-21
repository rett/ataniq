package intake

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/exchange/leadtext"
)

// objectCommit writes text as a commit object into the bare repository
// without any check, as a lead that does not use git could, and makes it the
// lead branch.
func (s *scene) objectCommit(text string) {
	s.t.Helper()
	// The bare repository gets the tree of the index with a commit of its
	// own that no ref of the review reaches.
	s.publish("refs/keep/tree", s.commit("tree carrier\n"))
	s.cur = s.run([]byte(text), s.work, "--git-dir="+s.bare, "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
	s.tip = s.cur
	s.b("update-ref", leadRef, s.tip)
}

func (s *scene) header() string {
	return "tree " + s.w("write-tree") + "\nparent " + s.base + "\n"
}

const (
	goodAuthor    = "author A <a@example.invalid> 1700000000 +0000\n"
	goodCommitter = "committer C <c@example.invalid> 1700000001 +0000\n"
)

func inspectErr(t *testing.T, s *scene) error {
	t.Helper()
	_, err := Inspect(context.Background(), s.source(), s.request())
	return err
}

// TestCommitEncodingOtherThanUTF8IsRefused: a commit that names a charset
// other than UTF-8 is refused whole, with its id and the value escaped, and
// the review holds none of its text.
func TestCommitEncodingOtherThanUTF8IsRefused(t *testing.T) {
	for _, enc := range []string{"ISO-8859-1", "SHIFT_JIS", "latin1", "UTF-16", "utf-8x", "", "UTF-8 ", "x\x1b[31my\xff"} {
		s := newScene(t)
		s.put("src/a.go", "package main\n")
		id := s.rawCommit("encoding "+enc+"\n", "caf\xe9 \x83\x65 text\n", s.base)
		s.finish()
		err := inspectErr(t, s)
		if !errors.Is(err, ErrCommitEncoding) {
			t.Errorf("encoding %q: error = %v", enc, err)
			continue
		}
		msg := err.Error()
		if !strings.Contains(msg, id) {
			t.Errorf("encoding %q: the message does not name the commit: %q", enc, msg)
		}
		if strings.ContainsAny(msg, "\x1b\xff\xe9") || strings.Contains(msg, "text") {
			t.Errorf("encoding %q: the message holds raw lead text: %q", enc, msg)
		}
		if want, _ := leadtext.Line(enc, 64); !strings.Contains(msg, `"`+want+`"`) {
			t.Errorf("encoding %q: the message does not show the value escaped: %q", enc, msg)
		}
	}
}

// TestCommitEncodingIsNeverConverted: no git command that converts text runs.
func TestCommitEncodingIsNeverConverted(t *testing.T) {
	s := newScene(t)
	s.put("src/a.go", "package main\n")
	s.rawCommit("encoding SHIFT_JIS\n", "\x83\x65\x83X\x83g\n", s.base)
	s.finish()
	rec, logPath := recordingGit(t, s.git)
	src := Source{Git: rec, GitDir: s.bare}
	if _, err := Inspect(context.Background(), src, s.request()); !errors.Is(err, ErrCommitEncoding) {
		t.Fatalf("error = %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
		for _, arg := range strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f") {
			if arg == "log" || arg == "show" || arg == "diff" || strings.HasPrefix(arg, "--encoding") || strings.HasPrefix(arg, "--pretty") ||
				(strings.HasPrefix(arg, "--format") && !strings.Contains(arg, "%(")) || strings.Contains(arg, "i18n") {
				t.Errorf("a command that can convert text ran: %q", arg)
			}
		}
	}
	if !strings.Contains(string(raw), "cat-file") {
		t.Error("the commit was not read with cat-file")
	}
}

// TestCommitEncodingUTF8SpellingsPass: the spellings git treats as UTF-8
// pass, in any case, and the text is as stored.
func TestCommitEncodingUTF8SpellingsPass(t *testing.T) {
	for _, enc := range []string{"UTF-8", "utf-8", "UTF8", "Utf8"} {
		s := newScene(t)
		s.put("src/a.go", "package main\n")
		s.rawCommit("encoding "+enc+"\n", "caf\u00e9 title\n\nbody\n", s.base)
		s.finish()
		r := s.inspect()
		if len(r.Commits) != 1 || r.Commits[0].Subject != "caf\u00e9 title" {
			t.Errorf("encoding %q: %+v", enc, r.Commits)
		}
	}
}

// TestCommitMessageNotUTF8IsRaw: a message that is not valid UTF-8 and has
// no encoding header comes back byte for byte, and the usual escaping of
// lead text shows it without a raw byte.
func TestCommitMessageNotUTF8IsRaw(t *testing.T) {
	s := newScene(t)
	s.put("src/a.go", "package main\n")
	s.rawCommit("", "bad \xff\xfe \xc3\x28 bytes\n\nbody \xff\n", s.base)
	s.finish()
	r := s.inspect()
	if len(r.Commits) != 1 || r.Commits[0].Subject != "bad \xff\xfe \xc3( bytes" {
		t.Fatalf("subject = %q", r.Commits[0].Subject)
	}
	shown := leadtext.Escape(r.Commits[0].Subject)
	if strings.ContainsAny(shown, "\xff\xfe\xc3") || !strings.Contains(shown, `\xff\xfe`) {
		t.Errorf("escaped = %q", shown)
	}
	if r.Digest() != s.inspect().Digest() {
		t.Error("the digest changes between two runs")
	}
}

// malformedCommits are commit objects that the package refuses. The ids are
// made up: the parser does not look at the repository.
var (
	fakeTree   = strings.Repeat("1", 40)
	fakeParent = strings.Repeat("2", 40)
	fakeHeader = "tree " + fakeTree + "\nparent " + fakeParent + "\n"
)

var malformedCommits = map[string]string{
	"header without a value":           fakeHeader + goodAuthor + goodCommitter + "novalue\n\nm\n",
	"continuation line without header": " x\n" + fakeHeader + goodAuthor + goodCommitter + "\nm\n",
	"header not terminated":            fakeHeader + goodAuthor + goodCommitter + "x y",
	"second encoding header":           fakeHeader + goodAuthor + goodCommitter + "encoding UTF-8\nencoding UTF-8\n\nm\n",
	"no tree first":                    "parent " + fakeParent + "\n" + goodAuthor + goodCommitter + "\nm\n",
	"tree is not an id":                "tree xyz\n" + goodAuthor + goodCommitter + "\nm\n",
	"parent is not an id":              "tree " + fakeTree + "\nparent abc\n" + goodAuthor + goodCommitter + "\nm\n",
	"committer before author":          fakeHeader + goodCommitter + goodAuthor + "\nm\n",
	"no committer":                     fakeHeader + goodAuthor + "\nm\n",
	"no author":                        fakeHeader + "\nm\n",
	"parent after author":              fakeHeader + goodAuthor + "parent " + fakeParent + "\n" + goodCommitter + "\nm\n",
	"identity without an address":      fakeHeader + "author A 1700000000 +0000\n" + goodCommitter + "\nm\n",
	"identity without a close bracket": fakeHeader + "author A <a 1700000000 +0000\n" + goodCommitter + "\nm\n",
	"identity without a time":          fakeHeader + "author A <a@x> +0000\n" + goodCommitter + "\nm\n",
	"identity time is not digits":      fakeHeader + "author A <a@x> 17e9 +0000\n" + goodCommitter + "\nm\n",
	"identity time overflows":          fakeHeader + "author A <a@x> 99999999999999999999 +0000\n" + goodCommitter + "\nm\n",
	"identity zone is not a zone":      fakeHeader + "author A <a@x> 1700000000 0000\n" + goodCommitter + "\nm\n",
	"identity with text after":         fakeHeader + "author A <a@x> 1700000000 +0000 x\n" + goodCommitter + "\nm\n",
	"identity with two spaces":         fakeHeader + "author A <a@x>  1700000000 +0000\n" + goodCommitter + "\nm\n",
	"address with an open bracket":     fakeHeader + "author A <a<b> 1700000000 +0000\n" + goodCommitter + "\nm\n",
	"committer is not an identity":     fakeHeader + goodAuthor + "committer nobody\n\nm\n",
	"empty object":                     "",
	"headers only, no committer":       "tree " + fakeTree + "\n",
}

// TestCommitMalformedHeadersAreRefused: every deviation from the form the
// package reads is ErrCommitFormat, and none panics.
func TestCommitMalformedHeadersAreRefused(t *testing.T) {
	id := strings.Repeat("3", 40)
	for name, text := range malformedCommits {
		t.Run(name, func(t *testing.T) {
			_, err := parseCommit(id, []byte(text))
			if !errors.Is(err, ErrCommitFormat) {
				t.Fatalf("error = %v", err)
			}
			if !strings.Contains(err.Error(), id) {
				t.Errorf("the message does not name the commit: %q", err)
			}
		})
	}
}

// TestCommitMalformedHeadersInTheMirror: objects of this kind that git lets
// into a ref are refused when the review reads them. git itself refuses to
// point a ref at some of the others ("no tree first", a tree or a parent that
// is not an id), so those are not reachable in a repository.
func TestCommitMalformedHeadersInTheMirror(t *testing.T) {
	for _, name := range []string{
		"header without a value", "second encoding header",
		"identity without an address", "identity time is not digits", "identity time overflows",
		"identity zone is not a zone", "identity with text after", "identity with two spaces",
		"address with an open bracket", "committer is not an identity",
	} {
		t.Run(name, func(t *testing.T) {
			s := newScene(t)
			s.put("src/a.go", "package main\n")
			text := strings.ReplaceAll(malformedCommits[name], fakeHeader, s.header())
			s.objectCommit(text)
			err := inspectErr(t, s)
			if !errors.Is(err, ErrCommitFormat) {
				t.Fatalf("error = %v", err)
			}
			if !strings.Contains(err.Error(), s.tip) {
				t.Errorf("the message does not name the commit: %q", err)
			}
		})
	}
}

// TestCommitVeryLongHeaderIsLimit: a header of more than maxCommitField
// bytes, in one line or in continuation lines, is ErrLimit, and one at the
// limit passes. A name or a subject over the limit is ErrLimit as well.
func TestCommitVeryLongHeaderIsLimit(t *testing.T) {
	long := strings.Repeat("a", maxCommitField+1)
	cont := strings.Repeat(" "+strings.Repeat("b", 999)+"\n", 40)
	cases := map[string]string{
		"one line":           "x-long " + long + "\n",
		"continuation lines": "gpgsig -----BEGIN-----\n" + cont + " -----END-----\n",
	}
	for name, hdr := range cases {
		s := newScene(t)
		s.put("src/a.go", "package main\n")
		s.objectCommit(s.header() + goodAuthor + goodCommitter + hdr + "\nm\n")
		if err := inspectErr(t, s); !errors.Is(err, ErrLimit) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	s := newScene(t)
	s.put("src/a.go", "package main\n")
	s.objectCommit(s.header() + goodAuthor + goodCommitter + "x-long " + strings.Repeat("a", maxCommitField) + "\n\nm\n")
	if r := s.inspect(); len(r.Commits) != 1 {
		t.Errorf("a header at the limit: %+v", r.Commits)
	}
	s = newScene(t)
	s.put("src/a.go", "package main\n")
	s.objectCommit(s.header() + "author " + strings.Repeat("n", maxCommitField) + " <a@x> 1700000000 +0000\n" + goodCommitter + "\nm\n")
	if err := inspectErr(t, s); !errors.Is(err, ErrLimit) {
		t.Errorf("a name over the limit: %v", err)
	}
	s = newScene(t)
	s.put("src/a.go", "package main\n")
	s.rawCommit("", strings.Repeat("s", maxCommitField+1)+"\n", s.base)
	s.finish()
	if err := inspectErr(t, s); !errors.Is(err, ErrLimit) {
		t.Errorf("a subject over the limit: %v", err)
	}
}

// TestCommitObjectOverTheLimit: a commit object longer than maxCommitObject
// is ErrLimit before it is read, and so are commits over the whole budget.
func TestCommitObjectOverTheLimit(t *testing.T) {
	s := newScene(t)
	s.put("src/a.go", "package main\n")
	s.rawCommit("", "subject\n\n"+strings.Repeat("body line\n", maxCommitObject/9), s.base)
	s.finish()
	if err := inspectErr(t, s); !errors.Is(err, ErrLimit) {
		t.Errorf("a long message: %v", err)
	}

	s = newScene(t)
	body := "subject\n\n" + strings.Repeat("x", maxCommitObject/2)
	for i := 0; i < 4; i++ {
		s.put("src/f"+string(rune('a'+i)), "x\n")
		s.commit(body + "\n")
	}
	s.finish()
	req := s.request()
	req.Limits = Limits{MaxCommits: 4}
	// Four objects of half the object limit are 128 KiB and more, over the
	// budget of four commits (32 KiB) and one maximal object (64 KiB).
	if _, err := Inspect(context.Background(), s.source(), req); !errors.Is(err, ErrLimit) {
		t.Errorf("four commits of %d bytes: error = %v", maxCommitObject/2, err)
	}
}

// TestCommitFieldsMatchGitLog: for commits git itself can read, the fields
// are what "git log" prints for them, so the change of reader changed no
// value.
func TestCommitFieldsMatchGitLog(t *testing.T) {
	messages := []string{
		"plain\n",
		"plain",
		"two\nline subject\n\nbody\n",
		"\n\n\nafter blanks\nsecond\n\nbody\n",
		"trailing space  \t\nnext\r\n\r\nbody\n",
		"  leading space\n  kept\n",
		"   \n\t\nwhite lines skipped\n",
		"\x1b[31mred\x1b[0m\n",
		"nul\x00inside\nline\n",
		"bad \xff bytes\n\xc3(\n",
		"\n",
		"",
		"café\n",
		"a\n \nb\n",
		"x\vy\fz\n",
		"vt kept\v\nnext\f\n",
	}
	names := []string{"Lead Author", "  spaced  name \t", "tail\v", "", "Mal\x1b[31mlory", "bad\xffname", "Name > odd", "Ünï"}
	s := newScene(t)
	var ids []string
	for i, m := range messages {
		s.put("src/f"+string(rune('a'+i)), "x\n")
		s.authorName = names[i%len(names)]
		s.committerName = names[(i+3)%len(names)]
		parent := s.base
		if len(ids) > 0 {
			parent = ids[len(ids)-1]
		}
		text := "tree " + s.w("write-tree") + "\nparent " + parent + "\n" +
			"author " + s.authorName + " <a@example.invalid> " + itoa(1700000000+int64(i)) + " +0100\n" +
			"committer " + s.committerName + " <c@example.invalid> " + itoa(1700001000+int64(i)) + " -0830\n\n" + m
		s.cur = s.run([]byte(text), s.work, "hash-object", "--literally", "-t", "commit", "-w", "--stdin")
		ids = append(ids, s.cur)
	}
	s.finish()
	r := s.inspect()
	if len(r.Commits) != len(messages) {
		t.Fatalf("%d commits", len(r.Commits))
	}
	format := "%H%x00%P%x00%an%x00%ae%x00%cn%x00%ce%x00%at%x00%ct%x00%s"
	for i, id := range ids {
		got := r.Commits[len(ids)-1-i]
		out := s.run(nil, s.work, "--git-dir="+s.bare, "log", "-1", "--no-mailmap", "--format="+format, id)
		// The format is run unconverted by the fixture's git, in the
		// fixture environment. "\n" ends the record; a subject has none.
		f := strings.Split(out, "\x00")
		if len(f) != 9 {
			t.Fatalf("%q: git printed %q", messages[i], out)
		}
		want := Commit{ID: f[0], AuthorName: f[2], AuthorEmail: f[3], CommitterName: f[4], CommitterEmail: f[5], Subject: f[8]}
		if got.ID != want.ID || got.AuthorName != want.AuthorName || got.AuthorEmail != want.AuthorEmail ||
			got.CommitterName != want.CommitterName || got.CommitterEmail != want.CommitterEmail || got.Subject != want.Subject {
			t.Errorf("message %q:\n got %+v\nwant %+v", messages[i], got, want)
		}
		if f[6] != itoa(got.AuthorTime.Unix()) || f[7] != itoa(got.CommitTime.Unix()) {
			t.Errorf("message %q: times %q %q, got %v %v", messages[i], f[6], f[7], got.AuthorTime, got.CommitTime)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
