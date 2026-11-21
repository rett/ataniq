package mergebuild

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/record"
	"ataniq/internal/exchange/gitenv"
)

var ctx = context.Background()

// formats are the object formats every scene is tried in: the ones Build
// and Check serve. SHA-256 is refused; see TestSHA256IsRefused. When the
// refusal is lifted, restore "sha256" here, and that test then expects the
// merge to conflict.
var formats = []string{"sha1"}

// A clean merge: the tree holds both sides' changes, the commit is the text
// the package comment lays out, and the pin points at it.
func TestBuildWritesTheMergeAndPinsIt(t *testing.T) {
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			noGoroutineLeft(t)
			l := newLabFormat(t, format)
			s := newScene(t, l, nil, nil)
			commits := l.count("commit")

			m, err := Build(ctx, l.repo, inputs(s.target, s.feature), "pin-1")
			if err != nil {
				t.Fatal(err)
			}
			wantTree := l.tree("f.txt", mergedText)
			if m.Tree != wantTree {
				t.Errorf("the tree is %s, want the tree with both changes, %s", m.Tree, wantTree)
			}
			if m.Parents != [2]string{s.target, s.feature} {
				t.Errorf("the parents are %q, want the target's tip and then the feature's", m.Parents)
			}
			kind, raw := l.read(m.Commit)
			if kind != "commit" {
				t.Fatalf("the object is a %s", kind)
			}
			if want := wantText(wantTree, s.target, s.feature); string(raw) != want {
				t.Errorf("the commit text is\n%q\nwant\n%q", raw, want)
			}
			if got := l.count("commit"); got != commits+1 {
				t.Errorf("%d commits were written, want one", got-commits)
			}
			if got, want := l.refs(), m.Commit+" refs/ataniq/pending/pin-1"; got != want {
				t.Errorf("the refs are %q, want only the pin %q", got, want)
			}
			if ref, err := PinRef("pin-1"); err != nil || ref != "refs/ataniq/pending/pin-1" {
				t.Errorf("PinRef = %q, %v", ref, err)
			}
			// git reads the commit as the same merge.
			if got := l.git("", "rev-parse", "--verify", "--end-of-options", m.Commit+"^{tree}"); got != wantTree {
				t.Errorf("git reads the tree %s", got)
			}
			// gitenv refuses "log" (it converts a commit's text by the
			// commit's own encoding header); rev-parse names each parent
			// as git reads it, and there is no third.
			got := [2]string{
				l.git("", "rev-parse", "--verify", "--end-of-options", m.Commit+"^1"),
				l.git("", "rev-parse", "--verify", "--end-of-options", m.Commit+"^2"),
			}
			if got != [2]string{s.target, s.feature} {
				t.Errorf("git reads the parents as %q", got)
			}
			if third, err := l.tryGit("", "rev-parse", "--verify", "--quiet", "--end-of-options", m.Commit+"^3"); err == nil {
				t.Errorf("git reads a third parent, %q", third)
			}
			if err := Check(ctx, l.repo, m.Commit, inputs(s.target, s.feature)); err != nil {
				t.Errorf("Check refuses what Build wrote: %v", err)
			}
		})
	}
}

// The two dates are the collected time, in seconds and in UTC, and not the
// clock's: the fixture's time is in the year 2001, has a part of a second
// and another zone.
func TestBuildDatesAreTheCollectedTime(t *testing.T) {
	l := newLab(t)
	s := newScene(t, l, nil, nil)
	m, err := Build(ctx, l.repo, inputs(s.target, s.feature), "k")
	if err != nil {
		t.Fatal(err)
	}
	_, raw := l.read(m.Commit)
	for _, line := range []string{
		"\nauthor Host Lead <lead@host.invalid> 981155106 +0000\n",
		"\ncommitter Host Lead <lead@host.invalid> 981155106 +0000\n",
	} {
		if !strings.Contains(string(raw), line) {
			t.Errorf("the commit lacks the line %q:\n%s", line, raw)
		}
	}
	if collected.Unix() != 981155106 || time.Since(collected) < 20*365*24*time.Hour {
		t.Fatal("the fixture's time is not the one the test names")
	}
	// The same values give the same commit, whenever it is built; the same
	// instant in another zone, with another part of a second, too.
	in := inputs(s.target, s.feature)
	in.At = time.Unix(981155106, 5).UTC()
	again, err := Build(ctx, l.repo, in, "k2")
	if err != nil {
		t.Fatal(err)
	}
	if again != m {
		t.Errorf("a second build gave %+v, the first %+v", again, m)
	}
	in.At = in.At.Add(time.Second)
	other, err := Build(ctx, l.repo, in, "k3")
	if err != nil {
		t.Fatal(err)
	}
	if other.Commit == m.Commit || other.Tree != m.Tree {
		t.Errorf("a build one second later gave %+v, the first %+v", other, m)
	}
}

// A conflict is the typed refusal merge-conflict, and leaves no commit and
// no ref.
func TestBuildRefusesAConflict(t *testing.T) {
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			noGoroutineLeft(t)
			l := newLabFormat(t, format)
			s := newScene(t, l, nil, nil)
			commits := l.count("commit")

			m, err := Build(ctx, l.repo, inputs(s.clashTarget, s.clashFeature), "pin-1")
			var refusal *record.Error
			if !errors.As(err, &refusal) || refusal.Code != record.CodeMergeConflict {
				t.Fatalf("Build returned %+v, %v; want the refusal merge-conflict", m, err)
			}
			if code, ok := record.CodeOf(err); !ok || code != "merge-conflict" {
				t.Errorf("the code is %q", code)
			}
			if strings.Contains(err.Error(), "f.txt") {
				t.Errorf("the refusal names a path: %v", err)
			}
			if m != (Merge{}) {
				t.Errorf("a refusal came with %+v", m)
			}
			if got := l.count("commit"); got != commits {
				t.Errorf("%d commits were written", got-commits)
			}
			if got := l.refs(); got != "" {
				t.Errorf("refs were written: %q", got)
			}
			// The key is free: the same key serves a merge that works.
			if _, err := Build(ctx, l.repo, inputs(s.target, s.feature), "pin-1"); err != nil {
				t.Errorf("the key of a refused build cannot be used: %v", err)
			}
		})
	}
}

// A key has one pin: a second Build with it is refused, whatever it would
// build, and writes nothing.
func TestBuildRefusesAKeyThatHasAPin(t *testing.T) {
	l := newLab(t)
	s := newScene(t, l, nil, nil)
	first, err := Build(ctx, l.repo, inputs(s.target, s.feature), "pin-1")
	if err != nil {
		t.Fatal(err)
	}
	commits, refs := l.count("commit"), l.refs()

	for name, in := range map[string]Inputs{
		"the same merge": inputs(s.target, s.feature),
		"another merge":  inputs(s.clashTarget, s.feature),
		"a conflict":     inputs(s.clashTarget, s.clashFeature),
	} {
		m, err := Build(ctx, l.repo, in, "pin-1")
		if !errors.Is(err, ErrPinExists) || m != (Merge{}) {
			t.Errorf("%s: Build returned %+v, %v; want ErrPinExists", name, m, err)
		}
	}
	if got := l.count("commit"); got != commits {
		t.Errorf("%d commits were written by refused builds", got-commits)
	}
	if got := l.refs(); got != refs {
		t.Errorf("the refs changed from %q to %q", refs, got)
	}

	// Another key takes another merge, and the first pin stays.
	other, err := Build(ctx, l.repo, inputs(s.clashTarget, s.feature), "pin-2")
	if err != nil {
		t.Fatal(err)
	}
	if other.Commit == first.Commit {
		t.Fatal("two merges have one id")
	}
	if got, want := l.refs(), first.Commit+" refs/ataniq/pending/pin-1\n"+other.Commit+" refs/ataniq/pending/pin-2"; got != want {
		t.Errorf("the refs are %q, want %q", got, want)
	}
}

// The creation of the pin never moves a ref: "update-ref" is told to create
// it, which git refuses for a ref that exists.
func TestPinCreationDoesNotOverwrite(t *testing.T) {
	l := newLab(t)
	s := newScene(t, l, nil, nil)
	l.git("create refs/ataniq/pending/taken "+s.base+"\n", "update-ref", "--no-deref", "--stdin")
	c, err := newCall(l.repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.format(ctx); err != nil {
		t.Fatal(err)
	}
	err = c.updateRef(ctx, "create refs/ataniq/pending/taken "+s.target)
	var exit *gitExit
	if !errors.As(err, &exit) {
		t.Fatalf("creating a pin that exists returned %v", err)
	}
	if got, want := l.refs(), s.base+" refs/ataniq/pending/taken"; got != want {
		t.Errorf("the refs are %q, want %q", got, want)
	}
}

// A repository whose info/attributes is not empty is refused before
// anything runs: with a git that does not exist the refusal is still the
// attribute one.
func TestBuildRefusesInfoAttributesBeforeAnythingRuns(t *testing.T) {
	l := newLab(t)
	s := newScene(t, l, nil, nil)
	info := filepath.Join(l.repo.GitDir, "info")
	if err := os.MkdirAll(info, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(info, "attributes")
	commits := l.count("commit")
	noGit := l.repo
	noGit.Git = gitenv.Git{Path: filepath.Join(t.TempDir(), "no-such-git")}

	for name, content := range map[string]string{
		"a merge driver": "* merge=union\n",
		"a comment":      "# nothing\n",
		"a blank line":   "\n",
	} {
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, repo := range []Repo{noGit, l.repo} {
			if _, err := Build(ctx, repo, inputs(s.clashTarget, s.clashFeature), "k"); !errors.Is(err, ErrAttributes) {
				t.Errorf("%s: Build returned %v, want ErrAttributes", name, err)
			}
			if err := Check(ctx, repo, s.target, inputs(s.clashTarget, s.clashFeature)); !errors.Is(err, ErrAttributes) {
				t.Errorf("%s: Check returned %v, want ErrAttributes", name, err)
			}
		}
	}
	if got := l.count("commit"); got != commits {
		t.Errorf("%d commits were written", got-commits)
	}
	if got := l.refs(); got != "" {
		t.Errorf("refs were written: %q", got)
	}

	// Without the check the control fails: the git that does not exist is
	// what Build meets first once the file is empty.
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(ctx, noGit, inputs(s.target, s.feature), "k"); err == nil || errors.Is(err, ErrAttributes) {
		t.Errorf("with an empty file and no git, Build returned %v", err)
	}
	if _, err := Build(ctx, l.repo, inputs(s.target, s.feature), "k"); err != nil {
		t.Errorf("an empty info/attributes is refused: %v", err)
	}
}

// A merge commit a lead made, named as the feature's tip, is ordinary
// history: the host builds its own commit on top, with the lead's merge as
// the second parent.
func TestBuildTreatsALeadMadeMergeAsHistory(t *testing.T) {
	l := newLab(t)
	s := newScene(t, l, nil, nil)
	// The lead merged the target into its feature itself and wrote the
	// message the host would write.
	leadTree := l.tree("f.txt", mergedText, "extra.txt", "from the lead's merge\n")
	leadMerge := l.commit(leadTree, "Merge feature/sample-work into develop\n\nRequest-Id: rq-0001", s.target, s.feature)

	m, err := Build(ctx, l.repo, inputs(s.target, leadMerge), "k")
	if err != nil {
		t.Fatal(err)
	}
	if m.Commit == leadMerge {
		t.Fatal("the lead's merge commit was taken as the merge")
	}
	if m.Parents != [2]string{s.target, leadMerge} {
		t.Errorf("the parents are %q", m.Parents)
	}
	if m.Tree != leadTree {
		t.Errorf("the tree is %s, want the lead's tree %s merged into the target", m.Tree, leadTree)
	}
	_, raw := l.read(m.Commit)
	if want := wantText(leadTree, s.target, leadMerge); string(raw) != want {
		t.Errorf("the commit text is\n%q\nwant\n%q", raw, want)
	}
	// The lead's merge is not the host's: Check refuses it under the
	// values of the request, and accepts the host's.
	if err := Check(ctx, l.repo, leadMerge, inputs(s.target, s.feature)); !errors.Is(err, ErrMismatch) {
		t.Errorf("Check of the lead's merge returned %v", err)
	}
	if err := Check(ctx, l.repo, m.Commit, inputs(s.target, leadMerge)); err != nil {
		t.Errorf("Check of the host's merge returned %v", err)
	}

	// A feature that already holds the target is merged all the same: the
	// host's commit is always written, and it has the feature's tree.
	fast, err := Build(ctx, l.repo, inputs(s.base, s.feature), "k-fast")
	if err != nil {
		t.Fatal(err)
	}
	if kind, _ := l.read(fast.Commit); kind != "commit" || fast.Commit == s.base || fast.Commit == s.feature {
		t.Errorf("the merge is %s", fast.Commit)
	}
	if want := l.tree("f.txt", featureText); fast.Tree != want {
		t.Errorf("the tree is %s, want the feature's, %s", fast.Tree, want)
	}
}

// A feature that the target already holds changes nothing, and is refused
// with its own error before anything is written: the target's tip being
// the feature's parent, and further back in its history.
func TestBuildRefusesAFeatureTheTargetHolds(t *testing.T) {
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			noGoroutineLeft(t)
			l := newLabFormat(t, format)
			s := newScene(t, l, nil, nil)
			// The target is two commits past the feature's tip.
			later := l.commit(l.tree("f.txt", mergedText), "more work", s.target)
			commits := l.count("commit")

			for name, tips := range map[string][2]string{
				"the target is the feature's child": {s.feature, s.base},
				"the target is further on":          {later, s.base},
			} {
				m, err := Build(ctx, l.repo, inputs(tips[0], tips[1]), "k")
				if !errors.Is(err, ErrNothingToMerge) || m != (Merge{}) {
					t.Errorf("%s: Build returned %+v, %v", name, m, err)
				}
			}
			if got := l.count("commit"); got != commits {
				t.Errorf("%d commits were written", got-commits)
			}
			if got := l.refs(); got != "" {
				t.Errorf("refs were written: %q", got)
			}
		})
	}
}

// Ids that name no commit, and tips with no shared history, are refused
// with their own errors, and nothing is written.
func TestBuildRefusesWhatIsNotACommit(t *testing.T) {
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			l := newLabFormat(t, format)
			s := newScene(t, l, nil, nil)
			tree := l.tree("f.txt", baseText)
			tag := l.object("tag", []byte("object "+s.feature+"\ntype commit\ntag v1\ntagger T <t@example.invalid> 1 +0000\n\nv1\n"))
			missing := strings.Repeat("12", len(s.base)/2)
			lone := l.commit(l.tree("f.txt", "alone\n"), "no shared history")
			commits := l.count("commit")

			for name, id := range map[string]string{"a tree": tree, "a blob": l.blob(baseText), "a tag": tag, "no object": missing} {
				if _, err := Build(ctx, l.repo, inputs(s.target, id), "k"); !errors.Is(err, ErrNoCommit) {
					t.Errorf("%s as the feature's tip: %v", name, err)
				}
				if _, err := Build(ctx, l.repo, inputs(id, s.feature), "k"); !errors.Is(err, ErrNoCommit) {
					t.Errorf("%s as the target's tip: %v", name, err)
				}
				if err := Check(ctx, l.repo, s.target, inputs(s.base, id)); !errors.Is(err, ErrNoCommit) {
					t.Errorf("Check with %s as the feature's tip: %v", name, err)
				}
			}
			if _, err := Build(ctx, l.repo, inputs(s.target, lone), "k"); !errors.Is(err, ErrUnrelated) {
				t.Errorf("tips with no shared history: %v", err)
			}
			if got := l.count("commit"); got != commits {
				t.Errorf("%d commits were written", got-commits)
			}
			if got := l.refs(); got != "" {
				t.Errorf("refs were written: %q", got)
			}
		})
	}
}

// An id of the other object format is refused: git would read it as the
// name of a ref. The test gives git such a ref to find.
func TestBuildRefusesAnIDOfTheOtherLength(t *testing.T) {
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			l := newLabFormat(t, format)
			s := newScene(t, l, nil, nil)
			// 40 digits in a SHA-256 repository, 64 in a SHA-1 one.
			other := strings.Repeat("ab", 20)
			if format == "sha1" {
				other = strings.Repeat("ab", 32)
			}
			// A branch with that name, which the id would be read as.
			l.git("create refs/heads/"+other+" "+s.feature+"\n", "update-ref", "--no-deref", "--stdin")
			refs := l.refs()
			commits := l.count("commit")

			if _, err := Build(ctx, l.repo, inputs(s.target, other), "k"); !errors.Is(err, ErrValue) {
				t.Errorf("the feature's tip: %v", err)
			}
			if _, err := Build(ctx, l.repo, inputs(other, s.feature), "k"); !errors.Is(err, ErrValue) {
				t.Errorf("the target's tip: %v", err)
			}
			if err := Check(ctx, l.repo, other, inputs(s.target, s.feature)); !errors.Is(err, ErrValue) {
				t.Errorf("Check of such a commit id: %v", err)
			}
			if err := Check(ctx, l.repo, s.target, inputs(s.base, other)); !errors.Is(err, ErrValue) {
				t.Errorf("Check with such a tip: %v", err)
			}
			if err := Unpin(ctx, l.repo, "k", other); !errors.Is(err, ErrValue) {
				t.Errorf("Unpin: %v", err)
			}
			if got := l.count("commit"); got != commits {
				t.Errorf("%d commits were written", got-commits)
			}
			if got := l.refs(); got != refs {
				t.Errorf("the refs changed to %q", got)
			}
		})
	}
}

// Unpin removes the pin only while it holds the commit named.
func TestUnpin(t *testing.T) {
	noGoroutineLeft(t)
	l := newLab(t)
	s := newScene(t, l, nil, nil)
	m, err := Build(ctx, l.repo, inputs(s.target, s.feature), "pin-1")
	if err != nil {
		t.Fatal(err)
	}
	pinned := l.refs()

	if err := Unpin(ctx, l.repo, "pin-2", m.Commit); !errors.Is(err, ErrNoPin) {
		t.Errorf("a key with no pin: %v", err)
	}
	if err := Unpin(ctx, l.repo, "pin-1", s.target); !errors.Is(err, ErrPinMoved) {
		t.Errorf("another old value: %v", err)
	}
	if err := Unpin(ctx, l.repo, "Pin-1", m.Commit); !errors.Is(err, ErrValue) {
		t.Errorf("a key outside the grammar: %v", err)
	}
	if err := Unpin(ctx, l.repo, "pin-1", "refs/heads/main"); !errors.Is(err, ErrValue) {
		t.Errorf("a name in place of the commit: %v", err)
	}
	if got := l.refs(); got != pinned {
		t.Fatalf("a refused Unpin changed the refs to %q", got)
	}

	if err := Unpin(ctx, l.repo, "pin-1", m.Commit); err != nil {
		t.Fatal(err)
	}
	if got := l.refs(); got != "" {
		t.Errorf("after Unpin the refs are %q", got)
	}
	if kind, _ := l.read(m.Commit); kind != "commit" {
		t.Error("Unpin removed the commit")
	}
	if err := Unpin(ctx, l.repo, "pin-1", m.Commit); !errors.Is(err, ErrNoPin) {
		t.Errorf("a second Unpin: %v", err)
	}
	// The key is free again.
	if again, err := Build(ctx, l.repo, inputs(s.target, s.feature), "pin-1"); err != nil || again != m {
		t.Errorf("Build after Unpin returned %+v, %v", again, err)
	}

	// The delete itself is guarded by the old value, should the pin move
	// after Unpin looked.
	c, err := newCall(l.repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.format(ctx); err != nil {
		t.Fatal(err)
	}
	var exit *gitExit
	if err := c.updateRef(ctx, "delete refs/ataniq/pending/pin-1 "+s.target); !errors.As(err, &exit) {
		t.Errorf("a delete with another old value returned %v", err)
	}
	if got, want := l.refs(), m.Commit+" refs/ataniq/pending/pin-1"; got != want {
		t.Errorf("the refs are %q, want %q", got, want)
	}
}

// A SHA-256 repository is refused by Build and Check, before anything is
// written. The reason is the attribute pin, a SHA-1 id: in a SHA-256
// repository git reads it as a name, so a ref of that name (a tag or a
// branch, which comes from upstream) is where attributes would be read.
// The test plants such a tag at a commit whose tree says "* merge=union",
// which without the refusal makes the conflicting merge end cleanly.
func TestSHA256IsRefused(t *testing.T) {
	noGoroutineLeft(t)
	l := newLabFormat(t, "sha256")
	s := newScene(t, l, side{".gitattributes", "* merge=union\n"}, nil)
	const emptyTreeName = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	l.git("create refs/tags/"+emptyTreeName+" "+s.target+"\n", "update-ref", "--no-deref", "--stdin")
	refs, commits, trees := l.refs(), l.count("commit"), l.count("tree")

	in := inputs(s.clashTarget, s.clashFeature)
	m, err := Build(ctx, l.repo, in, "k")
	if !errors.Is(err, ErrObjectFormat) || m != (Merge{}) {
		t.Errorf("Build returned %+v, %v; want ErrObjectFormat", m, err)
	}
	if err := Check(ctx, l.repo, s.clashTarget, in); !errors.Is(err, ErrObjectFormat) {
		t.Errorf("Check returned %v; want ErrObjectFormat", err)
	}
	if l.refs() != refs || l.count("commit") != commits || l.count("tree") != trees {
		t.Error("a refused call wrote a ref or an object")
	}

	// Unpin merges nothing and still works there.
	pin := "refs/ataniq/pending/k"
	l.git("create "+pin+" "+s.target+"\n", "update-ref", "--no-deref", "--stdin")
	if err := Unpin(ctx, l.repo, "k", s.target); err != nil {
		t.Errorf("Unpin: %v", err)
	}
}
