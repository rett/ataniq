package mergebuild

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"ataniq/internal/exchange/boundedexec"
	"ataniq/internal/exchange/gitenv"
)

// These tests run the installed git on temporary bare repositories, and
// only through gitenv and the bounded runner: the repository is made with
// "init", ordinary commits are written with "hash-object", refs with
// "update-ref". The pinned environment writes no blob and no tree, and it
// refuses to write a commit whose headers are malformed, so the fixtures
// put those objects into the object store themselves, as loose objects
// (the object's header and body, compressed, under the name of their
// digest). No test starts git in any other way.

// Lines of the file "f.txt" in the fixtures. The base holds the lines 1 to
// 9. The target and the feature change different lines, so they merge
// cleanly; the two clashing sides change the same line.
const (
	baseText        = "1\n2\n3\n4\n5\n6\n7\n8\n9\n"
	targetText      = "1\ntwo-target\n3\n4\n5\n6\n7\n8\n9\n"
	featureText     = "1\n2\n3\n4\n5\n6\n7\neight-feature\n9\n"
	mergedText      = "1\ntwo-target\n3\n4\n5\n6\n7\neight-feature\n9\n"
	clashTargetText = "1\n2\n3\n4\nfive-target\n6\n7\n8\n9\n"
	clashFeature    = "1\n2\n3\n4\nfive-feature\n6\n7\n8\n9\n"
)

// collected is the time the fixtures give as the collected time. It is far
// from the clock, has a part of a second and is not in UTC, so a test sees
// that the commit holds this time, in seconds, in UTC.
var collected = time.Date(2001, 2, 3, 4, 5, 6, 789, time.FixedZone("east", 5*3600))

// collectedUnix is [collected] as the commit must state it.
const collectedUnix = "981155106"

func needGit(t *testing.T) gitenv.Git {
	t.Helper()
	g, err := gitenv.Find()
	if err != nil {
		t.Fatalf("these tests need an installed git: %v", err)
	}
	return g
}

// lab is a temporary bare repository.
type lab struct {
	t      *testing.T
	repo   Repo
	sha256 bool
	clock  int64
}

// newLab makes an empty bare repository whose object ids are SHA-1.
func newLab(t *testing.T) *lab {
	t.Helper()
	return newLabFormat(t, "sha1")
}

// newLabFormat makes an empty bare repository with the object format named.
func newLabFormat(t *testing.T, format string) *lab {
	t.Helper()
	g := needGit(t)
	dir := filepath.Join(t.TempDir(), "r.git")
	c := gitenv.Cmd{Args: []string{"init", "--bare", "--quiet", "--template=", "--object-format=" + format, "--end-of-options", dir}}
	argv, err := g.Argv(c)
	if err != nil {
		t.Fatal(err)
	}
	if res, err := boundedexec.Run(context.Background(), boundedexec.Cmd{
		Argv: argv, Env: g.Env(c), Timeout: 30 * time.Second, MaxStdout: 1 << 16, MaxStderr: 1 << 16,
	}); err != nil {
		t.Fatalf("git init: %v: %s", err, res.Stderr)
	}
	return &lab{t: t, repo: Repo{Git: g, GitDir: dir, Timeout: 30 * time.Second}, sha256: format == "sha256", clock: 1_700_000_000}
}

// tryGit runs git on the repository through gitenv and the bounded runner.
func (l *lab) tryGit(stdin string, args ...string) (string, error) {
	l.t.Helper()
	c := gitenv.Cmd{GitDir: l.repo.GitDir, Args: args}
	argv, err := l.repo.Git.Argv(c)
	if err != nil {
		l.t.Fatalf("gitenv refuses %q: %v", args, err)
	}
	run := boundedexec.Cmd{Argv: argv, Env: l.repo.Git.Env(c), Timeout: 30 * time.Second, MaxStdout: 4 << 20, MaxStderr: 1 << 16}
	if stdin != "" {
		run.Stdin = strings.NewReader(stdin)
	}
	res, err := boundedexec.Run(context.Background(), run)
	return string(res.Stdout), err
}

// git is tryGit that must succeed. It returns the output without its last
// newline.
func (l *lab) git(stdin string, args ...string) string {
	l.t.Helper()
	out, err := l.tryGit(stdin, args...)
	if err != nil {
		l.t.Fatalf("git %q: %v", args, err)
	}
	return strings.TrimSuffix(out, "\n")
}

func (l *lab) hash() hash.Hash {
	if l.sha256 {
		return sha256.New()
	}
	return sha1.New()
}

// object stores a loose object and returns its id.
func (l *lab) object(kind string, body []byte) string {
	l.t.Helper()
	whole := append([]byte(kind+" "+strconv.Itoa(len(body))+"\x00"), body...)
	h := l.hash()
	h.Write(whole)
	id := hex.EncodeToString(h.Sum(nil))
	var packed bytes.Buffer
	w := zlib.NewWriter(&packed)
	w.Write(whole)
	if err := w.Close(); err != nil {
		l.t.Fatal(err)
	}
	dir := filepath.Join(l.repo.GitDir, "objects", id[:2])
	if err := os.MkdirAll(dir, 0o755); err != nil {
		l.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id[2:]), packed.Bytes(), 0o444); err != nil && !errors.Is(err, fs.ErrPermission) {
		// A permission error means the object is there already, read-only.
		l.t.Fatal(err)
	}
	return id
}

// read returns the type and the raw body of a loose object, read from the
// object store without git.
func (l *lab) read(id string) (kind string, body []byte) {
	l.t.Helper()
	f, err := os.Open(filepath.Join(l.repo.GitDir, "objects", id[:2], id[2:]))
	if err != nil {
		l.t.Fatalf("object %s: %v", id, err)
	}
	defer f.Close()
	r, err := zlib.NewReader(f)
	if err != nil {
		l.t.Fatal(err)
	}
	whole, err := io.ReadAll(r)
	if err != nil {
		l.t.Fatal(err)
	}
	header, body, ok := bytes.Cut(whole, []byte{0})
	if !ok {
		l.t.Fatalf("object %s has no header", id)
	}
	kind, size, _ := strings.Cut(string(header), " ")
	if size != strconv.Itoa(len(body)) {
		l.t.Fatalf("object %s says %s bytes and has %d", id, size, len(body))
	}
	return kind, body
}

// count returns how many loose objects of a type the object store holds.
func (l *lab) count(kind string) int {
	l.t.Helper()
	n := 0
	root := filepath.Join(l.repo.GitDir, "objects")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		dir := filepath.Base(filepath.Dir(path))
		if len(dir) != 2 {
			l.t.Fatalf("the object store holds %s, which is not a loose object", path)
		}
		if k, _ := l.read(dir + d.Name()); k == kind {
			n++
		}
		return nil
	})
	if err != nil {
		l.t.Fatal(err)
	}
	return n
}

func (l *lab) blob(text string) string { l.t.Helper(); return l.object("blob", []byte(text)) }

// tree stores a tree of regular files, given as pairs of name and content.
func (l *lab) tree(files ...string) string {
	l.t.Helper()
	type entry struct{ name, id string }
	var entries []entry
	for i := 0; i+1 < len(files); i += 2 {
		entries = append(entries, entry{files[i], l.blob(files[i+1])})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	var body []byte
	for _, e := range entries {
		raw, err := hex.DecodeString(e.id)
		if err != nil {
			l.t.Fatal(err)
		}
		body = append(body, "100644 "+e.name+"\x00"...)
		body = append(body, raw...)
	}
	return l.object("tree", body)
}

// commit writes an ordinary commit through the pinned environment, as a
// lead's commit: its identity and dates are not the host's.
func (l *lab) commit(tree, message string, parents ...string) string {
	l.t.Helper()
	l.clock += 60
	text := "tree " + tree + "\n"
	for _, p := range parents {
		text += "parent " + p + "\n"
	}
	when := strconv.FormatInt(l.clock, 10)
	text += "author Lead Author <lead@example.invalid> " + when + " +0000\n" +
		"committer Lead Committer <lead@example.invalid> " + when + " +0000\n\n" + message + "\n"
	return l.git(text, "hash-object", "-tcommit", "-w", "--stdin")
}

// refs lists every ref of the repository with its value.
func (l *lab) refs() string {
	l.t.Helper()
	return l.git("", "for-each-ref", "--format=%(objectname) %(refname)")
}

// scene is a repository with a base commit and two pairs of commits on it:
// target and feature merge cleanly, clashTarget and clashFeature conflict.
// Every tree of the pairs holds "f.txt"; extra files are added per side.
type scene struct {
	*lab
	base, target, feature, clashTarget, clashFeature string
}

// side lists further files of one side of a scene, as pairs of name and
// content.
type side []string

// newScene builds a scene. inBase are files every tree holds; inFeature are
// files only the feature's trees hold.
func newScene(t *testing.T, l *lab, inBase, inFeature side) *scene {
	t.Helper()
	with := func(text string, extra ...side) string {
		files := []string{"f.txt", text}
		for _, e := range extra {
			files = append(files, e...)
		}
		return l.tree(files...)
	}
	s := &scene{lab: l}
	s.base = l.commit(with(baseText, inBase), "base")
	s.target = l.commit(with(targetText, inBase), "target work", s.base)
	s.feature = l.commit(with(featureText, inBase, inFeature), "feature work", s.base)
	s.clashTarget = l.commit(with(clashTargetText, inBase), "target clash", s.base)
	s.clashFeature = l.commit(with(clashFeature, inBase, inFeature), "feature clash", s.base)
	return s
}

// inputs are valid inputs for merging feature into target.
func inputs(target, feature string) Inputs {
	return Inputs{
		TargetTip:  target,
		FeatureTip: feature,
		Target:     "develop",
		Feature:    "sample-work",
		RequestID:  "rq-0001",
		Identity:   Identity{Name: "Host Lead", Email: "lead@host.invalid"},
		At:         collected,
	}
}

// wantText is the commit text for inputs made by [inputs], written out here
// by hand so that it does not come from the code under test.
func wantText(tree, target, feature string) string {
	return "tree " + tree + "\n" +
		"parent " + target + "\n" +
		"parent " + feature + "\n" +
		"author Host Lead <lead@host.invalid> " + collectedUnix + " +0000\n" +
		"committer Host Lead <lead@host.invalid> " + collectedUnix + " +0000\n" +
		"\n" +
		"Merge feature/sample-work into develop\n" +
		"\n" +
		"Request-Id: rq-0001\n"
}

// noGoroutineLeft fails the test when, at its end, more goroutines run than
// at the call. The runner's goroutines end with each run, so the count
// settles at once; the wait only covers their last steps.
func noGoroutineLeft(t *testing.T) {
	t.Helper()
	before := runtime.NumGoroutine()
	t.Cleanup(func() {
		var now int
		for i := 0; i < 200; i++ {
			if now = runtime.NumGoroutine(); now <= before {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		buf := make([]byte, 1<<16)
		t.Errorf("%d goroutines run after the test, %d before it:\n%s", now, before, buf[:runtime.Stack(buf, true)])
	})
}
