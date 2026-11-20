package mergebuild

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Check accepts the commit Build wrote and refuses each tampered form of
// it: every one differs from the host's text in one place.
func TestCheckRefusesEachTamperedForm(t *testing.T) {
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			noGoroutineLeft(t)
			l := newLabFormat(t, format)
			s := newScene(t, l, nil, nil)
			in := inputs(s.target, s.feature)
			m, err := Build(ctx, l.repo, in, "k")
			if err != nil {
				t.Fatal(err)
			}
			good := wantText(m.Tree, s.target, s.feature)
			if id := l.object("commit", []byte(good)); id != m.Commit {
				t.Fatalf("the expected text has the id %s, the merge %s", id, m.Commit)
			}
			if err := Check(ctx, l.repo, m.Commit, in); err != nil {
				t.Fatalf("Check refuses the host's merge: %v", err)
			}
			commits, refs := l.count("commit"), l.refs()

			featureTree := l.tree("f.txt", featureText)
			who := "Host Lead <lead@host.invalid> " + collectedUnix + " +0000\n"
			// change returns the good text with one piece replaced, and
			// fails the test when the piece is not there exactly once.
			change := func(old, new string) string {
				t.Helper()
				if strings.Count(good, old) != 1 {
					t.Fatalf("the text holds %q %d times", old, strings.Count(good, old))
				}
				return strings.Replace(good, old, new, 1)
			}
			target, feature := "parent "+s.target+"\n", "parent "+s.feature+"\n"
			for _, tc := range []struct {
				name, text, reason string
			}{
				{"a third parent", change(feature, feature+"parent "+s.base+"\n"), "3 parents"},
				{"the feature's tip twice", change(feature, feature+feature), "3 parents"},
				{"one parent: the target's tip", change(feature, ""), "1 parents"},
				{"one parent: the feature's tip", change(target, ""), "1 parents"},
				{"no parent", change(target+feature, ""), "0 parents"},
				{"swapped parents", change(target+feature, feature+target), "first parent"},
				{"another first parent", change(target, "parent "+s.base+"\n"), "first parent"},
				{"another second parent", change(feature, "parent "+s.base+"\n"), "second parent"},
				{"another tree: the feature's", change("tree "+m.Tree, "tree "+featureTree), "the tree"},
				{"a commit in the place of the tree", change("tree "+m.Tree, "tree "+s.base), "the tree"},
				{"no tree line", change("tree "+m.Tree+"\n", ""), "0 parents"},
				{"another author name", change("author Host Lead", "author Lead Author"), "headers"},
				{"another author address", change("author Host Lead <lead@host.invalid>", "author Host Lead <lead@example.invalid>"), "headers"},
				{"another committer", change("committer Host Lead", "committer Lead Committer"), "headers"},
				{"another author date", change("author "+who, "author Host Lead <lead@host.invalid> 981155107 +0000\n"), "headers"},
				{"another committer date", change("committer "+who, "committer Host Lead <lead@host.invalid> 981155105 +0000\n"), "headers"},
				{"another zone", change("committer "+who, "committer Host Lead <lead@host.invalid> "+collectedUnix+" +0500\n"), "headers"},
				{"a further header", change("\n\nMerge", "\nencoding UTF-8\n\nMerge"), "headers"},
				{"a signature header", change("\n\nMerge", "\ngpgsig -----BEGIN-----\n x\n -----END-----\n\nMerge"), "headers"},
				{"a parent after the author", change("\n\nMerge", "\n"+strings.TrimSuffix(target, "\n")+"\n\nMerge"), "headers"},
				{"another feature in the message", change("feature/sample-work", "feature/other-work"), "headers"},
				{"another target in the message", change("into develop", "into master"), "headers"},
				{"another request id", change("rq-0001", "rq-0002"), "headers"},
				{"no trailer", change("\nRequest-Id: rq-0001\n", ""), "headers"},
				{"a further line", good + "Approved-By: owner\n", "headers"},
				{"a further line before the trailer", change("\n\nRequest-Id", "\n\nmore\n\nRequest-Id"), "headers"},
				{"no last newline", strings.TrimSuffix(good, "\n"), "headers"},
				{"a carriage return", change("into develop\n", "into develop\r\n"), "headers"},
				{"a long message", good + strings.Repeat("x", 64<<10) + "\n", "longer"},
			} {
				id := l.object("commit", []byte(tc.text))
				err := Check(ctx, l.repo, id, in)
				if !errors.Is(err, ErrMismatch) {
					t.Errorf("%s: Check returned %v, want ErrMismatch", tc.name, err)
					continue
				}
				if !strings.Contains(err.Error(), tc.reason) {
					t.Errorf("%s: the refusal is %q, want one that says %q", tc.name, err, tc.reason)
				}
			}

			// Objects that are no commits.
			for name, id := range map[string]string{
				"a tree":    m.Tree,
				"a blob":    l.blob(good),
				"no object": strings.Repeat("34", len(m.Commit)/2),
			} {
				if err := Check(ctx, l.repo, id, in); !errors.Is(err, ErrMismatch) {
					t.Errorf("%s: Check returned %v, want ErrMismatch", name, err)
				}
			}
			if err := Check(ctx, l.repo, "refs/ataniq/pending/k", in); !errors.Is(err, ErrValue) {
				t.Errorf("a ref name in place of the id: %v", err)
			}

			// The host's merge under other values.
			other := func(change func(*Inputs)) Inputs {
				in := inputs(s.target, s.feature)
				change(&in)
				return in
			}
			for name, in := range map[string]Inputs{
				"the parents expected the other way": inputs(s.feature, s.target),
				"another target tip expected":        inputs(s.clashTarget, s.feature),
				"another feature tip expected":       inputs(s.target, s.clashFeature),
				"another request id":                 other(func(in *Inputs) { in.RequestID = "rq-0002" }),
				"another feature name":               other(func(in *Inputs) { in.Feature = "sample-wor" }),
				"another target name":                other(func(in *Inputs) { in.Target = "Develop" }),
				"another name":                       other(func(in *Inputs) { in.Identity.Name = "Host  Lead" }),
				"another address":                    other(func(in *Inputs) { in.Identity.Email = "lead@host.invalid " }),
				"a second later":                     other(func(in *Inputs) { in.At = in.At.Add(time.Second) }),
			} {
				if err := Check(ctx, l.repo, m.Commit, in); !errors.Is(err, ErrMismatch) {
					t.Errorf("%s: Check returned %v, want ErrMismatch", name, err)
				}
			}
			// The same instant, written another way, is the same value.
			if err := Check(ctx, l.repo, m.Commit, other(func(in *Inputs) { in.At = time.Unix(981155106, 999).UTC() })); err != nil {
				t.Errorf("the same second in UTC: %v", err)
			}

			// Check wrote no commit (the tampered ones are the test's) and
			// no ref, and did not touch the pin.
			if got := l.refs(); got != refs {
				t.Errorf("the refs changed from %q to %q", refs, got)
			}
			if got := l.count("commit"); got != commits+29 {
				t.Errorf("the store holds %d commits more, want the 29 tampered ones", got-commits)
			}
		})
	}
}

// A commit over two parents that conflict is refused, whatever it holds:
// there is no tree the host would have written.
func TestCheckRefusesAMergeOfAConflict(t *testing.T) {
	l := newLab(t)
	s := newScene(t, l, nil, nil)
	// The lead resolved the conflict its own way and wrote the host's text
	// around its tree.
	tree := l.tree("f.txt", clashFeature)
	id := l.object("commit", []byte(wantText(tree, s.clashTarget, s.clashFeature)))
	err := Check(ctx, l.repo, id, inputs(s.clashTarget, s.clashFeature))
	if !errors.Is(err, ErrMismatch) || !strings.Contains(err.Error(), "conflict") {
		t.Errorf("Check returned %v", err)
	}
	// And over two parents with no shared history.
	lone := l.commit(l.tree("f.txt", "alone\n"), "no shared history")
	id = l.object("commit", []byte(wantText(tree, s.target, lone)))
	if err := Check(ctx, l.repo, id, inputs(s.target, lone)); !errors.Is(err, ErrUnrelated) {
		t.Errorf("Check over unrelated parents returned %v", err)
	}
}

// The first lines of a commit are read as git reads them.
func TestCommitHead(t *testing.T) {
	for _, tc := range []struct {
		text, tree string
		parents    []string
	}{
		{"tree T\nparent A\nparent B\nauthor x\n\nmsg\n", "T", []string{"A", "B"}},
		{"tree T\nauthor x\nparent A\n\nmsg\n", "T", nil},
		{"tree T\nparent A\n\nparent B\n", "T", []string{"A"}},
		{"tree T\nparent A\n parent B\n", "T", []string{"A"}},
		{"parent A\ntree T\n", "", nil},
		{"", "", nil},
		{"tree T", "T", nil},
	} {
		tree, parents := commitHead([]byte(tc.text))
		if tree != tc.tree || strings.Join(parents, ",") != strings.Join(tc.parents, ",") {
			t.Errorf("commitHead(%q) = %q, %q; want %q, %q", tc.text, tree, parents, tc.tree, tc.parents)
		}
	}
}

// A commit over a target that already holds the feature is refused, as
// Build refuses to write one: only someone who made it by hand, with the
// host's exact values, could present it.
func TestCheckRefusesAFeatureTheTargetHolds(t *testing.T) {
	for _, format := range formats {
		t.Run(format, func(t *testing.T) {
			noGoroutineLeft(t)
			l := newLabFormat(t, format)
			s := newScene(t, l, nil, nil)
			// The target is the feature's child; the commit is the host's
			// text for those two parents, around the target's own tree.
			in := inputs(s.feature, s.base)
			tree := l.tree("f.txt", featureText)
			id := l.object("commit", []byte(wantText(tree, s.feature, s.base)))
			if err := Check(ctx, l.repo, id, in); !errors.Is(err, ErrNothingToMerge) {
				t.Errorf("Check returned %v, want ErrNothingToMerge", err)
			}
		})
	}
}
