package intake

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/approval/mergebuild"
)

// featureChange is a small feature: one new file, one changed, one deleted.
func featureChange(l *lab) {
	l.put("feature.txt", "one\ntwo\n")
	l.put("src/main.go", "package main\n\nfunc main() {}\n")
	l.remove("README.md")
}

func TestPendingPrefixIsTheBuildersPinPrefix(t *testing.T) {
	if PendingPrefix != mergebuild.PinPrefix {
		t.Fatalf("PendingPrefix = %q, mergebuild.PinPrefix = %q", PendingPrefix, mergebuild.PinPrefix)
	}
}

// TestInspectMerge: the base is the target tip, the tip is the merge, and the
// review holds the merge and the feature's commits and the change the merge
// brings, which is the feature's alone, not the target's own move.
func TestInspectMerge(t *testing.T) {
	m := newMergeScene(t, featureChange)
	r := m.inspectMerge()
	if !r.Merge || r.Base != m.target || r.Tip != m.merge || r.TipRef != testPin || r.BaseRef != upstreamRef {
		t.Fatalf("review = %+v", r)
	}
	var ids []string
	for _, c := range r.Commits {
		ids = append(ids, c.ID)
	}
	slices.Sort(ids)
	want := []string{m.feature, m.merge}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Errorf("commits = %v, want %v", ids, want)
	}
	var paths []string
	for _, c := range r.Changes {
		paths = append(paths, c.Path)
	}
	if want := []string{"README.md", "feature.txt", "src/main.go"}; !slices.Equal(paths, want) {
		t.Errorf("changes = %v, want %v (the target's own file must not show)", paths, want)
	}
	if byPath(t, r, "README.md").Status != 'D' || byPath(t, r, "feature.txt").Status != 'A' || byPath(t, r, "src/main.go").Status != 'M' {
		t.Errorf("statuses wrong: %+v", r.Changes)
	}
	// The lead mode is as before: the same scene, read as a lead branch,
	// has no Merge and the lead prefix.
	m.publish(leadRef, m.merge)
	lr := m.inspect()
	if lr.Merge || lr.Tip != m.merge {
		t.Errorf("lead-mode review = %+v", lr)
	}
	if lr.Digest() == r.Digest() {
		t.Errorf("the digest of the merge review equals that of the lead review of the same commits")
	}
}

func TestMergeModeRefNames(t *testing.T) {
	m := newMergeScene(t, featureChange)
	m.publish(leadRef, m.merge)
	ctx := context.Background()
	// A lead ref is not a pin, and a pin is not a lead ref.
	req := m.mergeRequest()
	req.TipRef = leadRef
	if _, err := Inspect(ctx, m.source(), req); !errors.Is(err, ErrRefName) {
		t.Errorf("merge mode with a lead ref: %v", err)
	}
	lreq := m.request()
	lreq.TipRef = testPin
	if _, err := Inspect(ctx, m.source(), lreq); !errors.Is(err, ErrRefName) {
		t.Errorf("lead mode with a pin: %v", err)
	}
	for _, bad := range []string{PendingPrefix, PendingPrefix + "a..b", PendingPrefix + ".k", PendingPrefix + "k.lock", "refs/ataniq/pending", "refs/ataniq/other/k"} {
		req.TipRef = bad
		if _, err := Inspect(ctx, m.source(), req); !errors.Is(err, ErrRefName) {
			t.Errorf("tip ref %q: %v", bad, err)
		}
	}
	req.TipRef = testPin
	req.BaseRef = "refs/leads/role/heads/work"
	if _, err := Inspect(ctx, m.source(), req); !errors.Is(err, ErrRefName) {
		t.Errorf("a lead ref as the target: %v", err)
	}
}

func TestMergeModeRefusesWhatIsNotTheHostsShape(t *testing.T) {
	m := newMergeScene(t, featureChange)
	ctx := context.Background()
	m.checkout(m.feature)
	oneParent := m.rawCommit("", "not a merge\n", m.target)
	m.publish(PendingPrefix+"one", oneParent)
	m.checkout(m.feature)
	three := m.rawCommit("", "octopus\n", m.target, m.feature, m.root)
	m.publish(PendingPrefix+"three", three)
	m.checkout(m.feature)
	swapped := m.rawCommit("", "swapped parents\n", m.feature, m.target)
	m.publish(PendingPrefix+"swapped", swapped)
	m.checkout(m.feature)
	other := m.rawCommit("", "first parent is the root\n", m.root, m.feature)
	m.publish(PendingPrefix+"other", other)

	for _, name := range []string{"one", "three"} {
		req := m.mergeRequest()
		req.TipRef = PendingPrefix + name
		if _, err := Inspect(ctx, m.source(), req); !errors.Is(err, ErrNotMerge) {
			t.Errorf("%s: error = %v, want ErrNotMerge", name, err)
		}
	}
	// The target is the second parent of the swapped merge, so it is an
	// ancestor of the tip, and the shape check is what refuses it.
	req := m.mergeRequest()
	req.TipRef = PendingPrefix + "swapped"
	_, err := Inspect(ctx, m.source(), req)
	var mb *MergeBaseError
	if !errors.As(err, &mb) || mb.Base != m.target || mb.Parent != m.feature {
		t.Errorf("swapped: error = %v", err)
	}
	// The target is no ancestor of a merge built on the root: that is a
	// plain not-fast-forward.
	req.TipRef = PendingPrefix + "other"
	var nf *NotFastForwardError
	if _, err := Inspect(ctx, m.source(), req); !errors.As(err, &nf) {
		t.Errorf("other: error = %v", err)
	}
	// A target that is an ancestor of the merge but not its first parent is
	// the case the shape check exists for: the pin "other" has the root as
	// its first parent, and the target is moved back to the root's child
	// that the feature also holds, so it is an ancestor of the merge.
	m.publish(upstreamRef, m.root)
	req.TipRef = PendingPrefix + "other"
	if _, err := Inspect(ctx, m.source(), req); err != nil {
		t.Fatalf("the merge whose first parent is the target: %v", err)
	}
	req.TipRef = testPin // first parent is the old target tip, the target is at the root
	_, err = Inspect(ctx, m.source(), req)
	mb = nil
	if !errors.As(err, &mb) || mb.Base != m.root || mb.Parent != m.target {
		t.Errorf("target moved back: error = %v", err)
	}
	if !strings.Contains(mb.Error(), m.root) || !strings.Contains(mb.Error(), m.target) {
		t.Errorf("the error names no ids: %v", mb)
	}
}

func TestMergeModeAtTheTargetHasNothingToReview(t *testing.T) {
	m := newMergeScene(t, featureChange)
	m.publish(upstreamRef, m.merge)
	r := m.inspectMerge()
	if r.Base != r.Tip || len(r.Changes) != 0 || !r.Merge {
		t.Errorf("review = %+v", r)
	}
}

// TestMergeModeChecksInfoAttributes: the repository's own attribute file is
// read by git whatever the attribute source is, so every pinned diff of merge
// mode runs the check first; a lead-mode review is as before.
func TestMergeModeChecksInfoAttributes(t *testing.T) {
	m := newMergeScene(t, featureChange)
	ctx := context.Background()
	r := m.inspectMerge()
	// Absent and empty both pass.
	setInfoAttributes(t, m.bare, "")
	if _, err := Inspect(ctx, m.source(), m.mergeRequest()); err != nil {
		t.Fatalf("an empty attribute file: %v", err)
	}
	setInfoAttributes(t, m.bare, "* -diff\n")
	if _, err := Inspect(ctx, m.source(), m.mergeRequest()); !errors.Is(err, ErrAttributes) {
		t.Errorf("Inspect with an attribute file: %v", err)
	}
	var out bytes.Buffer
	if err := Diff(ctx, m.source(), r, &out, Limits{}); !errors.Is(err, ErrAttributes) {
		t.Errorf("Diff of a merge review with an attribute file: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("Diff wrote %q before it refused", out.String())
	}
	if _, err := Summarize(ctx, m.source(), r, Limits{}); !errors.Is(err, ErrAttributes) {
		t.Errorf("Summarize with an attribute file: %v", err)
	}
	// The lead mode does not run the check (as before).
	m.publish(leadRef, m.merge)
	lr, err := Inspect(ctx, m.source(), m.request())
	if err != nil {
		t.Fatalf("lead-mode Inspect with an attribute file: %v", err)
	}
	if err := Diff(ctx, m.source(), lr, &out, Limits{}); err != nil {
		t.Errorf("lead-mode Diff with an attribute file: %v", err)
	}
	// The check shows in the same place for a refused file kind.
	setInfoAttributes(t, m.bare, "")
	if _, err := Summarize(ctx, m.source(), r, Limits{}); err != nil {
		t.Errorf("Summarize with an empty attribute file: %v", err)
	}
}

func TestMergeModeDiffIsThePatchOfTheMerge(t *testing.T) {
	m := newMergeScene(t, featureChange)
	r := m.inspectMerge()
	var out bytes.Buffer
	if err := Diff(context.Background(), m.source(), r, &out, Limits{}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.HasPrefix(text, "ataniq intake diff base="+m.target+" tip="+m.merge+"\n") {
		t.Errorf("header: %q", strings.SplitN(text, "\n", 2)[0])
	}
	if !strings.Contains(text, "+++ b/feature.txt") || strings.Contains(text, "target-side.txt") {
		t.Errorf("the patch is not the feature's change alone:\n%s", text)
	}
}

// oddScene is a merge that adds one path of each odd kind and one plain one.
func oddScene(t *testing.T) *mscene {
	return newMergeScene(t, func(l *lab) {
		l.put("plain.txt", "x\n")
		l.put("PROGRA~1/run", "x\n")
		l.put("a:stream", "x\n")
		l.put("ctl\x01name", "x\n")
		l.put("é.txt", "x\n") // e and a combining acute: not in form C
		l.put("café.txt", "x\n")
		l.put("raw\xffbytes", "x\n")
	})
}

func TestMergeModeFlagsOddPaths(t *testing.T) {
	m := oddScene(t)
	r := m.inspectMerge()
	odd := map[string]bool{}
	for _, f := range r.Flagged {
		if f.Kind == FlagOddPath {
			odd[f.Path] = true
		}
	}
	for _, p := range []string{"PROGRA~1/run", "a:stream", "ctl\x01name", "é.txt", "raw\xffbytes"} {
		if !odd[p] {
			t.Errorf("%q is not flagged odd-path", p)
		}
		if !hasFlag(byPath(t, r, p).Flags, FlagOddPath) {
			t.Errorf("the change of %q holds no odd-path flag", p)
		}
	}
	for _, p := range []string{"plain.txt", "café.txt", "feature.txt"} {
		if odd[p] {
			t.Errorf("%q is flagged odd-path", p)
		}
	}
	// A flagged path needs its own sign-off, and the digest holds the flag.
	so := okSignOff(r)
	so.Paths = slices.DeleteFunc(slices.Clone(so.Paths), func(p string) bool { return p == "a:stream" })
	if err := r.CheckSignOff(so); err == nil {
		t.Errorf("a sign-off without the odd path is accepted")
	}
	if err := r.CheckSignOff(okSignOff(r)); err != nil {
		t.Errorf("the full sign-off is refused: %v", err)
	}
	// The same paths read as a lead branch carry no odd-path flag: the lead
	// mode is as before.
	m.publish(leadRef, m.merge)
	for _, f := range m.inspect().Flagged {
		if f.Kind == FlagOddPath {
			t.Errorf("lead mode flags %q odd-path", f.Path)
		}
	}
}

// TestMergeModeCommandLines: every command of merge mode, the summary and the
// patch has the pinned attribute source where the result depends on
// attributes, ends the options before any id, and takes no lead text as an
// argument.
func TestMergeModeCommandLines(t *testing.T) {
	m := newMergeScene(t, featureChange)
	rec, logPath := recordingGit(t, m.git)
	src := Source{Git: rec, GitDir: m.bare}
	ctx := context.Background()
	req := m.mergeRequest()
	r, err := Inspect(ctx, src, req)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Summarize(ctx, src, r, Limits{}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := Diff(ctx, src, r, &out, Limits{}); err != nil {
		t.Fatal(err)
	}
	raw := mustRead(t, logPath)
	idArg := regexp.MustCompile(`^` + idPat + `(\.\.` + idPat + `|\^\{commit\})?$`)
	seen := map[string]int{}
	pinned := 0
	for _, line := range strings.Split(strings.TrimSuffix(raw, "\n"), "\n") {
		args := strings.Split(strings.TrimSuffix(line, "\x1f"), "\x1f")
		var sets []string
		i := 0
		for i < len(args) && args[i] == "-c" {
			sets = append(sets, args[i+1])
			i += 2
		}
		args = args[i+1:] // after --git-dir
		sub := args[0]
		seen[sub]++
		if sub == "diff" || sub == "diff-tree" || sub == "log" {
			if slices.Contains(sets, "attr.tree=4b825dc642cb6eb9a060e54bf8d69288fbee4904") {
				pinned++
			} else {
				t.Errorf("git %s runs without the attribute pin: %q", sub, sets)
			}
		}
		afterEnd := false
		for _, a := range args[1:] {
			switch {
			case afterEnd:
				if !idArg.MatchString(a) && a != upstreamRef && a != testPin {
					t.Errorf("git %s: %q after the end of the options", sub, a)
				}
			case a == "--end-of-options":
				afterEnd = true
			case strings.HasPrefix(a, "-"):
			default:
				t.Errorf("git %s: bare argument %q before the end of the options", sub, a)
			}
		}
		switch sub {
		case "diff", "diff-tree", "rev-list", "log", "for-each-ref", "rev-parse", "merge-base":
			if !afterEnd && !slices.Contains(args, "--stdin") {
				t.Errorf("git %s has no --end-of-options: %q", sub, args)
			}
		}
	}
	for _, sub := range []string{"diff", "diff-tree", "cat-file"} {
		if seen[sub] == 0 {
			t.Errorf("git %s was not run", sub)
		}
	}
	if pinned < 3 {
		t.Errorf("only %d pinned diff-like runs", pinned)
	}
}
