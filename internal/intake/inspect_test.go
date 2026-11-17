package intake

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/exchange/boundedexec"
)

const zeroID = "0000000000000000000000000000000000000000"

// plainScene is a branch of three commits on top of the upstream root.
func plainScene(t *testing.T) (*scene, [3]string) {
	t.Helper()
	s := newScene(t)
	var c [3]string
	s.put("src/a.go", "package main\n// a\n")
	c[0] = s.commit("add a\n\nlonger text\n")
	s.put("src/main.go", "package main\n// changed\n")
	c[1] = s.commit("change main\n")
	s.remove("README.md")
	s.put("docs/x.txt", "x\n")
	c[2] = s.commit("drop the readme, add docs\n")
	s.finish()
	return s, c
}

func TestInspectPlainBranch(t *testing.T) {
	s, c := plainScene(t)
	r := s.inspect()

	if r.BaseRef != upstreamRef || r.TipRef != leadRef || r.Base != s.base || r.Tip != s.tip {
		t.Fatalf("refs and ids: %+v", r)
	}
	if len(r.Commits) != 3 {
		t.Fatalf("%d commits, want 3", len(r.Commits))
	}
	for i, want := range []string{c[2], c[1], c[0]} {
		if r.Commits[i].ID != want {
			t.Errorf("commit %d = %s, want %s (newest first)", i, r.Commits[i].ID, want)
		}
	}
	first := r.Commits[2]
	wantParent := s.base
	if first.ID != c[0] || len(first.Parents) != 1 || first.Parents[0] != wantParent {
		t.Errorf("first commit %+v", first)
	}
	if first.Subject != "add a" || first.AuthorName != "Lead Author" || first.AuthorEmail != "author@example.invalid" ||
		first.CommitterName != "Lead Committer" || first.CommitterEmail != "committer@example.invalid" {
		t.Errorf("first commit text: %+v", first)
	}
	if !first.AuthorTime.Before(first.CommitTime) || first.AuthorTime.Location() != time.UTC || first.AuthorTime.Unix() < 1_700_000_000 {
		t.Errorf("times: %v %v", first.AuthorTime, first.CommitTime)
	}
	if !r.Commits[0].CommitTime.After(r.Commits[2].CommitTime) {
		t.Error("the newest commit is not the latest")
	}

	type want struct {
		path             string
		status           byte
		oldMode, newMode uint32
	}
	wants := []want{
		{"README.md", 'D', 0o100644, 0},
		{"docs/x.txt", 'A', 0, 0o100644},
		{"src/a.go", 'A', 0, 0o100644},
		{"src/main.go", 'M', 0o100644, 0o100644},
	}
	if len(r.Changes) != len(wants) {
		t.Fatalf("changes: %+v", r.Changes)
	}
	for i, w := range wants {
		ch := r.Changes[i]
		if ch.Path != w.path || ch.Status != w.status || ch.OldMode != w.oldMode || ch.NewMode != w.newMode {
			t.Errorf("change %d = %+v, want %+v", i, ch, w)
		}
		if len(ch.Flags) != 0 {
			t.Errorf("%s: unexpected flags %+v", ch.Path, ch.Flags)
		}
	}
	if a := byPath(t, r, "src/a.go"); a.OldID != zeroID || a.NewID != s.w("rev-parse", s.tip+":src/a.go") {
		t.Errorf("ids of src/a.go: %+v", a)
	}
	if d := byPath(t, r, "README.md"); d.NewID != zeroID || d.OldID != s.w("rev-parse", s.base+":README.md") {
		t.Errorf("ids of README.md: %+v", d)
	}
	if len(r.Flagged) != 0 {
		t.Errorf("Flagged = %+v", r.Flagged)
	}

	d1 := r.Digest()
	if d2 := s.inspect().Digest(); d1 != d2 || len(d1) != 64 {
		t.Errorf("digest not stable: %q %q", d1, d2)
	}

	// A new commit on the lead branch changes the digest.
	s.put("src/b.go", "package main\n")
	s.commit("add b\n")
	s.finish()
	r2 := s.inspect()
	if r2.Digest() == d1 || len(r2.Commits) != 4 {
		t.Errorf("digest after a new commit: %q (was %q), %d commits", r2.Digest(), d1, len(r2.Commits))
	}
}

func TestDigestSeesEveryField(t *testing.T) {
	s, _ := plainScene(t)
	base := s.inspect()
	want := base.Digest()
	mutate := map[string]func(r *Review){
		"base ref": func(r *Review) { r.BaseRef += "x" },
		"tip ref":  func(r *Review) { r.TipRef += "x" },
		"base":     func(r *Review) { r.Base = strings.Repeat("1", 40) },
		"tip":      func(r *Review) { r.Tip = strings.Repeat("1", 40) },
		"path":     func(r *Review) { r.Changes[0].Path += "x" },
		"status":   func(r *Review) { r.Changes[0].Status = 'T' },
		"old mode": func(r *Review) { r.Changes[0].OldMode = 0o100755 },
		"new mode": func(r *Review) { r.Changes[0].NewMode = 0o100755 },
		"old id":   func(r *Review) { r.Changes[0].OldID = strings.Repeat("1", 40) },
		"new id":   func(r *Review) { r.Changes[0].NewID = strings.Repeat("1", 40) },
		"flag":     func(r *Review) { r.Changes[0].Flags = []Flag{{Kind: FlagSymlink, Path: r.Changes[0].Path}} },
		"flagged":  func(r *Review) { r.Flagged = []Flag{{Kind: FlagHistory, Path: "p", Pattern: "q"}} },
		"fewer":    func(r *Review) { r.Changes = r.Changes[1:] },
	}
	for name, f := range mutate {
		r := s.inspect()
		f(r)
		if r.Digest() == want {
			t.Errorf("digest does not change with %s", name)
		}
	}
	// A shifted boundary is a different digest.
	a, b := *base, *base
	a.BaseRef, a.TipRef = "ab", "c"
	b.BaseRef, b.TipRef = "a", "bc"
	if a.Digest() == b.Digest() {
		t.Error("digest does not separate fields")
	}
	// The commit list and the reason text are not part of it.
	r := s.inspect()
	r.Commits = nil
	if r.Digest() != want {
		t.Error("digest depends on the commit list")
	}
}

func TestInspectBaseEqualsTip(t *testing.T) {
	s := newScene(t)
	s.publish(leadRef, s.base)
	r := s.inspect()
	if r.Base != s.base || r.Tip != s.base || len(r.Commits) != 0 || len(r.Changes) != 0 || len(r.Flagged) != 0 {
		t.Errorf("review = %+v", r)
	}
	if r.Digest() == "" {
		t.Error("no digest")
	}
}

func TestInspectNotFastForward(t *testing.T) {
	// Diverged.
	s := newScene(t)
	s.put("up.txt", "up\n")
	up := s.commit("upstream moves\n")
	s.publish(upstreamRef, up)
	s.checkout(s.base)
	s.put("lead.txt", "lead\n")
	s.commit("lead work\n")
	s.finish()
	_, err := Inspect(context.Background(), s.source(), s.request())
	var nf *NotFastForwardError
	if !errors.As(err, &nf) {
		t.Fatalf("diverged: error = %v", err)
	}
	if nf.Base != up || nf.Tip != s.tip || nf.MergeBase != s.base {
		t.Errorf("diverged: %+v", nf)
	}
	if strings.Contains(nf.Error(), "\n") || !strings.Contains(nf.Error(), s.base) {
		t.Errorf("message %q", nf.Error())
	}

	// The tip is behind the base.
	s.publish(leadRef, s.base)
	_, err = Inspect(context.Background(), s.source(), s.request())
	if !errors.As(err, &nf) || nf.MergeBase != s.base || nf.Base != up || nf.Tip != s.base {
		t.Errorf("behind: error = %v, %+v", err, nf)
	}

	// No common ancestor.
	u := newLab(t)
	u.put("other.txt", "o\n")
	orphan := u.commit("unrelated\n")
	u.w("push", "--quiet", "--force", s.bare, orphan+":refs/leads/role/heads/orphan")
	req := s.request()
	req.TipRef = "refs/leads/role/heads/orphan"
	_, err = Inspect(context.Background(), s.source(), req)
	if !errors.As(err, &nf) || nf.MergeBase != "" || nf.Tip != orphan {
		t.Errorf("unrelated: error = %v, %+v", err, nf)
	}
}

func TestInspectRefErrors(t *testing.T) {
	s, _ := plainScene(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name      string
		base, tip string
		want      error
	}{
		{"missing tip", upstreamRef, "refs/leads/role/heads/nope", ErrNoRef},
		{"missing base", "refs/heads/nope", leadRef, ErrNoRef},
		{"a folder is not a ref", upstreamRef, "refs/leads/role/heads", ErrNoRef},
		{"short tip", upstreamRef, "work", ErrRefName},
		{"short base", "main", leadRef, ErrRefName},
		{"tip under heads", upstreamRef, upstreamRef, ErrRefName},
		{"base under leads", leadRef, leadRef, ErrRefName},
		{"tags", "refs/tags/v1", leadRef, ErrRefName},
		{"only the prefix", upstreamRef, "refs/leads/", ErrRefName},
		{"empty", "", "", ErrRefName},
		{"dot dot", upstreamRef, "refs/leads/role/../x", ErrRefName},
		{"pattern", upstreamRef, "refs/leads/role/heads/*", ErrRefName},
		{"space", upstreamRef, "refs/leads/role/heads/a b", ErrRefName},
		{"newline", upstreamRef, "refs/leads/role/heads/a\nb", ErrRefName},
		{"option", upstreamRef, "refs/leads/--upload-pack=x", ErrNoRef},
		{"lock", upstreamRef, "refs/leads/role/heads/a.lock", ErrRefName},
		{"trailing slash", upstreamRef, leadRef + "/", ErrRefName},
		{"hidden element", upstreamRef, "refs/leads/role/heads/.x", ErrRefName},
		{"long", upstreamRef, "refs/leads/" + strings.Repeat("a", 2000), ErrRefName},
	} {
		_, err := Inspect(ctx, s.source(), Request{BaseRef: tc.base, TipRef: tc.tip})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
			continue
		}
		if strings.ContainsAny(err.Error(), "\n\x1b") {
			t.Errorf("%s: message holds a control byte: %q", tc.name, err.Error())
		}
	}
	// The ref has children but is not a ref itself.
	if _, err := Inspect(ctx, s.source(), Request{BaseRef: "refs/heads/mai", TipRef: leadRef}); !errors.Is(err, ErrNoRef) {
		t.Errorf("a prefix of a name: %v", err)
	}
	// Bad limits.
	req := s.request()
	req.Limits.MaxPaths = -1
	if _, err := Inspect(ctx, s.source(), req); err == nil {
		t.Error("negative limit accepted")
	}
	// Bad source.
	if _, err := Inspect(ctx, Source{Git: s.git, GitDir: "relative"}, s.request()); err == nil {
		t.Error("relative git dir accepted")
	}
	if _, err := Inspect(ctx, Source{Git: s.git, GitDir: s.bare + "/missing"}, s.request()); err == nil {
		t.Error("missing git dir accepted")
	}
}

func TestInspectNotACommit(t *testing.T) {
	s, _ := plainScene(t)
	ctx := context.Background()
	blob := s.run([]byte("blob\n"), s.work, "--git-dir="+s.bare, "hash-object", "-w", "--stdin")
	tag := s.run([]byte("object "+s.tip+"\ntype commit\ntag v1\ntagger T <t@example.invalid> 1700000000 +0000\n\nmsg\n"),
		s.work, "--git-dir="+s.bare, "mktag")
	s.b("update-ref", "refs/leads/role/heads/blob", blob)
	s.b("update-ref", "refs/leads/role/heads/tag", tag)
	// git update-ref refuses a tag under refs/heads, so write the loose ref.
	if err := os.WriteFile(filepath.Join(s.bare, "refs", "heads", "tagged"), []byte(tag+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A symbolic ref would review the commit of another branch under the
	// lead's name, or stand in for the upstream branch.
	s.b("symbolic-ref", "refs/leads/role/heads/sym", upstreamRef)
	s.b("symbolic-ref", "refs/heads/sym", upstreamRef)
	for _, tc := range []struct{ name, base, tip string }{
		{"symbolic tip", upstreamRef, "refs/leads/role/heads/sym"},
		{"symbolic base", "refs/heads/sym", leadRef},
		{"blob tip", upstreamRef, "refs/leads/role/heads/blob"},
		{"tag tip", upstreamRef, "refs/leads/role/heads/tag"},
		{"tag base", "refs/heads/tagged", leadRef},
	} {
		_, err := Inspect(ctx, s.source(), Request{BaseRef: tc.base, TipRef: tc.tip})
		if !errors.Is(err, ErrNotCommit) {
			t.Errorf("%s: error = %v", tc.name, err)
		}
	}
}

func TestInspectLimits(t *testing.T) {
	s, _ := plainScene(t)
	ctx := context.Background()
	req := s.request()

	req.Limits = Limits{MaxCommits: 2}
	if _, err := Inspect(ctx, s.source(), req); !errors.Is(err, ErrLimit) {
		t.Errorf("commits over the limit: %v", err)
	}
	req.Limits = Limits{MaxCommits: 3}
	if _, err := Inspect(ctx, s.source(), req); err != nil {
		t.Errorf("commits at the limit: %v", err)
	}
	req.Limits = Limits{MaxPaths: 3}
	if _, err := Inspect(ctx, s.source(), req); !errors.Is(err, ErrLimit) {
		t.Errorf("paths over the limit: %v", err)
	}
	req.Limits = Limits{MaxPaths: 4}
	if _, err := Inspect(ctx, s.source(), req); err != nil {
		t.Errorf("paths at the limit: %v", err)
	}
	req.Limits = Limits{Timeout: time.Nanosecond}
	_, err := Inspect(ctx, s.source(), req)
	if !errors.Is(err, ErrLimit) || !errors.Is(err, boundedexec.ErrTimeout) {
		t.Errorf("time over the limit: %v", err)
	}

	// A path longer than the limit.
	long := newScene(t)
	long.put(strings.Repeat(strings.Repeat("d", 250)+"/", 17)+"f", "x\n")
	long.commit("long path\n")
	long.finish()
	if _, err := Inspect(ctx, long.source(), long.request()); !errors.Is(err, ErrLimit) {
		t.Errorf("a path of 4300 bytes: %v", err)
	}

	// A cancelled context stops the call.
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Inspect(cctx, s.source(), s.request()); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled context: %v", err)
	}
}

func TestDefaultLimits(t *testing.T) {
	d := DefaultLimits()
	if d.Timeout != 60*time.Second || d.MaxCommits != 5000 || d.MaxPaths != 20000 || d.MaxDiffBytes != 16<<20 {
		t.Errorf("DefaultLimits() = %+v", d)
	}
	got, err := Limits{}.resolved()
	if err != nil || got != d {
		t.Errorf("zero limits resolve to %+v, %v", got, err)
	}
}
