package intake

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/intake/execconfig"
)

// mscene is a scene for merge mode: the target (refs/heads/main) has moved
// since the feature branched, and the host's merge of the two sits under a
// pin in refs/ataniq/pending/. The merge is written the way the host writes
// it, as a commit of two parents, the target's tip first, and a tree that
// holds both sides.
type mscene struct {
	*scene
	root, target, feature, merge string
}

const testPin = PendingPrefix + "k1"

// newMergeScene makes the root (README.md, src/main.go and the files given),
// moves the target by one commit, runs change on a branch from the root and
// merges the two. change only edits the index; it runs twice, once on the
// feature's side and once on the merged tree.
func newMergeScene(t *testing.T, change func(l *lab), files ...string) *mscene {
	t.Helper()
	return newMergeSceneRoot(t, nil, change, files...)
}

// newMergeSceneRoot is newMergeScene with a function that edits the root's
// index too, for entries that put cannot make.
func newMergeSceneRoot(t *testing.T, root, change func(l *lab), files ...string) *mscene {
	t.Helper()
	l := newLab(t)
	l.put("README.md", "readme\n")
	l.put("src/main.go", "package main\n")
	for i := 0; i+1 < len(files); i += 2 {
		l.put(files[i], files[i+1])
	}
	if root != nil {
		root(l)
	}
	s := &scene{lab: l, base: l.commit("root\n")}
	l.publish(upstreamRef, s.base)
	m := &mscene{scene: s, root: s.base}
	s.put("target-side.txt", "target\n")
	m.target = s.commit("target moves\n")
	s.publish(upstreamRef, m.target)
	s.checkout(m.root)
	change(s.lab)
	m.feature = s.commit("feature\n")
	s.checkout(m.target)
	change(s.lab)
	m.merge = s.rawCommit("", "Merge feature/x into main\n\nRequest-Id: r1\n", m.target, m.feature)
	s.publish(testPin, m.merge)
	s.base, s.tip = m.target, m.merge
	return m
}

func (m *mscene) mergeRequest() Request {
	return Request{BaseRef: upstreamRef, TipRef: testPin, Rules: execconfig.Default(), Merge: true}
}

func (m *mscene) inspectMerge() *Review {
	m.t.Helper()
	r, err := Inspect(context.Background(), m.source(), m.mergeRequest())
	if err != nil {
		m.t.Fatalf("Inspect (merge): %v", err)
	}
	return r
}

// gitlinkAt sets a submodule entry to the commit id made of the byte b.
func (l *lab) gitlinkAt(path string, b string) {
	l.t.Helper()
	l.w("update-index", "--add", "--cacheinfo", "160000", strings.Repeat(b, 20), path)
}

// putIndex sets many entries in one run: lines are "<mode> <id>\t<path>".
func (l *lab) putIndex(lines []string) {
	l.t.Helper()
	l.run([]byte(strings.Join(lines, "\n")+"\n"), l.work, "update-index", "--add", "--index-info")
}

// plainGit runs the installed git on the bare repository with the fixture
// environment, outside gitenv, for the independent computations of the
// tests.
func (l *lab) plainGit(stdin []byte, args ...string) []byte {
	l.t.Helper()
	cmd := exec.Command(l.git.Path, append([]string{"--git-dir=" + l.bare}, args...)...)
	cmd.Env = l.env()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		l.t.Fatalf("git %q: %v: %s", args, err, errb.String())
	}
	return out.Bytes()
}

// setInfoAttributes writes the repository's own attribute file.
func setInfoAttributes(t *testing.T, bare, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(bare, "info"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bare, "info", "attributes"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
