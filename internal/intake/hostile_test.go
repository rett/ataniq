package intake

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestInspectHostileNames: paths, ref names, commit text and author names
// that a lead chose come back as the exact bytes it wrote.
func TestInspectHostileNames(t *testing.T) {
	s := newScene(t)
	paths := []string{
		"src/new\nline.txt",
		"src/esc\x1b[31m-red.txt",
		"src/bad\xff\xfe-utf8.txt",
		"src/bidi‮-name.txt",
		"src/tab\there.txt",
		"src/quote\"and\\slash.txt",
		"-rf",
		"--help",
	}
	for _, p := range paths {
		s.put(p, "content of "+p+"\n")
	}
	forged := "ataniq intake diff base=" + strings.Repeat("0", 40) + " tip=" + strings.Repeat("1", 40)
	s.authorName = "Mal\x1b[31mlory"
	s.committerName = "Eve‮​name"
	s.commit(forged + "\n")
	s.put("src/second.go", "package main\n")
	s.authorName, s.committerName = "", ""
	s.commit("title \x1b]0;pwned\x07 with an escape\n\nbody line\n")
	s.finish()

	r := s.inspect()
	want := append([]string{"src/second.go"}, paths...)
	slices.Sort(want)
	var got []string
	for _, c := range r.Changes {
		got = append(got, c.Path)
		if c.Status != 'A' {
			t.Errorf("%q: status %c", c.Path, c.Status)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("paths:\n got %q\nwant %q", got, want)
	}
	if len(r.Commits) != 2 {
		t.Fatalf("commits: %+v", r.Commits)
	}
	if r.Commits[0].Subject != "title \x1b]0;pwned\x07 with an escape" {
		t.Errorf("subject = %q", r.Commits[0].Subject)
	}
	older := r.Commits[1]
	if older.Subject != forged || older.AuthorName != "Mal\x1b[31mlory" || older.CommitterName != "Eve‮​name" {
		t.Errorf("hostile commit text: %+v", older)
	}
	if d := r.Digest(); d != s.inspect().Digest() {
		t.Error("the digest changes between two runs")
	}

	before := snapshot(t, s)
	var out bytes.Buffer
	if err := Diff(context.Background(), s.source(), r, &out, Limits{}); err != nil {
		t.Fatal(err)
	}
	onlyTextBytes(t, "diff of hostile names", out.Bytes())
	s.inspect()
	assertUntouched(t, s, before)
}

// TestInspectCommitTextIsRaw: the stored bytes are returned, with no
// re-encoding, no mailmap and no replacement object.
func TestInspectCommitTextIsRaw(t *testing.T) {
	s := newScene(t)
	// A message in Latin-1 that says so in its encoding header: git
	// converts it to UTF-8. A message with bytes that are not UTF-8 and no
	// encoding header stays as it is.
	s.put("src/a.go", "package main\n")
	s.rawCommit("encoding ISO-8859-1\n", "caf\xe9 in latin one\n", s.base)
	s.put("src/a2.go", "package main\n")
	s.rawCommit("", "bad \xff\xfe bytes\n", s.cur)
	latin := s.cur
	// A commit that a replacement object stands in for.
	s.put("src/b.go", "package main\n")
	replaced := s.commit("the real subject\n")
	s.checkout(latin)
	s.put("src/c.go", "package main\n")
	decoy := s.commit("the decoy subject\n")
	s.checkout(replaced)
	s.put("src/d.go", "package main\n")
	s.commit("tip\n")
	s.finish()
	s.w("push", "--quiet", s.bare, decoy+":refs/keep/decoy")
	s.b("update-ref", "refs/replace/"+replaced, decoy)

	r := s.inspect()
	bySubject := map[string]Commit{}
	for _, c := range r.Commits {
		bySubject[c.Subject] = c
	}
	if _, ok := bySubject["caf\u00e9 in latin one"]; !ok {
		t.Errorf("the Latin-1 subject was not converted: %+v", r.Commits)
	}
	if _, ok := bySubject["bad \xff\xfe bytes"]; !ok {
		t.Errorf("bytes that are not UTF-8 were changed: %+v", r.Commits)
	}
	if c, ok := bySubject["the real subject"]; !ok || c.ID != replaced {
		t.Errorf("the replaced commit is not listed under its own id: %+v", r.Commits)
	}
}

func TestInspectMailmapIsNotRead(t *testing.T) {
	s := newScene(t)
	s.put(".mailmap", "Fake Name <fake@example.invalid> Lead Author <author@example.invalid>\n")
	s.commit("add a mailmap\n")
	s.publish(upstreamRef, s.cur)
	s.put("src/a.go", "package main\n")
	s.commit("work\n")
	s.finish()
	s.b("symbolic-ref", "HEAD", upstreamRef)
	r := s.inspect()
	if c := r.Commits[0]; c.AuthorName != "Lead Author" || c.AuthorEmail != "author@example.invalid" {
		t.Errorf("a mailmap changed the author: %+v", c)
	}
}

// TestInspectHostileRefName: a ref name with bytes that are not UTF-8 is a
// valid name and works; one with control bytes is refused and never echoed raw.
func TestInspectHostileRefName(t *testing.T) {
	s := newScene(t)
	s.put("src/a.go", "package main\n")
	s.commit("work\n")
	s.finish()
	name := "refs/leads/role/heads/caf\xe9-‮"
	s.publish(name, s.tip)
	req := s.request()
	req.TipRef = name
	r, err := Inspect(context.Background(), s.source(), req)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if r.TipRef != name || r.Tip != s.tip {
		t.Errorf("review: %+v", r)
	}
	req.TipRef = "refs/leads/role/heads/x\x1b[31m"
	_, err = Inspect(context.Background(), s.source(), req)
	if !errors.Is(err, ErrRefName) || strings.ContainsAny(err.Error(), "\x1b\n") {
		t.Errorf("error = %q", err)
	}
	req.TipRef = "refs/leads/role/heads/missing‮"
	_, err = Inspect(context.Background(), s.source(), req)
	if !errors.Is(err, ErrNoRef) || strings.Contains(err.Error(), "‮") {
		t.Errorf("error = %q", err)
	}
}

// TestInspectBranchImitatesTheHeader: a commit message that looks like the
// header of Diff does not reach Diff's output as anything but escaped text
// below the first line.
func TestInspectBranchImitatesTheHeader(t *testing.T) {
	s := newScene(t)
	forged := "ataniq intake diff base=" + strings.Repeat("a", 40) + " tip=" + strings.Repeat("b", 40)
	s.put("src/notice.txt", forged+"\n\x1b[1mfake\x1b[0m\n")
	s.commit(forged + "\n")
	s.finish()
	r := s.inspect()
	if r.Commits[0].Subject != forged {
		t.Errorf("subject = %q", r.Commits[0].Subject)
	}
	var out bytes.Buffer
	if err := Diff(context.Background(), s.source(), r, &out, Limits{}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(out.String(), "\n")
	if lines[0] != headerOf(r) {
		t.Errorf("first line = %q", lines[0])
	}
	imitations := 0
	for _, l := range lines[1:] {
		if strings.Contains(l, "ataniq intake diff base=") {
			imitations++
			if !strings.HasPrefix(l, "+") {
				t.Errorf("an imitation is not inside the patch: %q", l)
			}
		}
	}
	if imitations != 1 {
		t.Errorf("%d imitations in the patch", imitations)
	}
	onlyTextBytes(t, "imitation", out.Bytes())
}

// TestInspectExecConfigNamesInOddForms: the matcher decides, and the intake
// passes the path as git stores it.
func TestInspectExecConfigNamesInOddForms(t *testing.T) {
	s := newScene(t)
	s.put(".CLAUDE/Settings.json", "{}\n")
	s.put("sub/dir/.mcp.json ", "{}\n")
	s.put("src/ok.go", "package main\n")
	s.commit("odd forms\n")
	s.finish()
	r := s.inspect()
	for _, p := range []string{".CLAUDE/Settings.json", "sub/dir/.mcp.json "} {
		flagFor(t, byPath(t, r, p).Flags, FlagExecConfig, p)
	}
	if len(byPath(t, r, "src/ok.go").Flags) != 0 {
		t.Error("an ordinary file is flagged")
	}
}

// TestInspectHooksAndConfigOfTheRepositoryAreNotRun: a repository whose
// hooks and config hold programs does not run them for a review.
func TestInspectHooksAndConfigOfTheRepositoryAreNotRun(t *testing.T) {
	s, _ := plainScene(t)
	marker := filepath.Join(t.TempDir(), "ran")
	script := "#!/bin/sh\ntouch " + marker + "\n"
	for _, hook := range []string{"post-checkout", "reference-transaction", "post-index-change", "pre-auto-gc"} {
		if err := os.WriteFile(filepath.Join(s.bare, "hooks", hook), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := os.OpenFile(filepath.Join(s.bare, "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfg.WriteString("[core]\n\tfsmonitor = " + marker + "\n\tpager = touch " + marker + "\n[diff]\n\texternal = touch " + marker + "\n[diff \"x\"]\n\ttextconv = touch " + marker + "\n")
	cfg.Close()
	if err != nil {
		t.Fatal(err)
	}
	r := s.inspect()
	var out bytes.Buffer
	if err := Diff(context.Background(), s.source(), r, &out, Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("a program of the repository ran")
	}
}
