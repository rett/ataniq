package demo

import (
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/record"
)

// backendInterface is a local copy of the console's Backend interface of the
// seams note, section 4. This package cannot import the console, so the copy
// is what the assertion below holds.
type backendInterface interface {
	List() (card.Listing, error)
	Open(id string) (card.Card, error)
	Page(id string, kind card.PageKind, n int) (card.Page, error)
	Decide(d card.Decision) (card.Outcome, error)
	Decider() (ok bool, reason string)
	Limit(project string) (limits.View, error)
	SetLimit(project string, c limits.Change) (limits.View, error)
	Activity(at time.Time)
	Looked(at time.Time)
}

var _ backendInterface = (*Backend)(nil)

// clock is a clock the tests move.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newBackend(t *testing.T) (*Backend, *clock) {
	t.Helper()
	c := &clock{t: time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)}
	return New(c.now), c
}

func open(t *testing.T, b *Backend, id string) card.Card {
	t.Helper()
	c, err := b.Open(id)
	if err != nil {
		t.Fatalf("Open(%s): %v", id, err)
	}
	return c
}

func code(err error) record.Code {
	c, _ := record.CodeOf(err)
	return c
}

func TestSamplesAreBuiltLikeRealCards(t *testing.T) {
	b, _ := newBackend(t)
	l, err := b.List()
	if err != nil {
		t.Fatal(err)
	}
	if !l.Sample {
		t.Error("Listing.Sample is false")
	}
	if len(l.Pending) < 8 || len(l.Recent) != 0 {
		t.Fatalf("pending %d, recent %d", len(l.Pending), len(l.Recent))
	}
	projects := map[string]bool{}
	for _, it := range l.Pending {
		if !strings.HasPrefix(it.ID, "ap-sample-") || !record.ValidPendingID(it.ID) {
			t.Errorf("id %q", it.ID)
		}
		projects[it.Project] = true
		c := open(t, b, it.ID)
		if c.BindingSHA256 != it.BindingSHA256 {
			t.Errorf("%s: list and card disagree on the digest", it.ID)
		}
	}
	if len(projects) < 2 {
		t.Errorf("projects %v", projects)
	}
	for _, r := range l.Rules {
		if !strings.Contains(r.Text, "sample") {
			t.Errorf("rule line %q does not say sample", r.Text)
		}
	}
	if l.Count.Requests == 0 {
		t.Error("the count from the first card is empty")
	}
}

func TestSampleShapes(t *testing.T) {
	b, _ := newBackend(t)
	if c := open(t, b, "ap-sample-clean"); len(c.Flagged) != 0 || c.Diff == nil || c.Diff.Truncated {
		t.Errorf("clean: %+v", c.Flagged)
	}
	if c := open(t, b, "ap-sample-flagged"); len(c.Flagged) != 1 {
		t.Errorf("flagged: %d flagged paths", len(c.Flagged))
	} else if p, _ := c.Flagged[0].Path.Line(40); p != "Makefile" {
		t.Errorf("flagged path %q", p)
	}
	h := open(t, b, "ap-sample-hostile")
	if h.Verb.Feature != "approve" || len(h.Flagged) != 1 {
		t.Errorf("hostile: feature %q, flagged %d", h.Verb.Feature, len(h.Flagged))
	}
	m := open(t, b, "ap-sample-many-paths")
	if m.Diff == nil || !m.Diff.Truncated || m.Diff.Files != 5000 || len(m.Diff.Rows) >= 5000 {
		t.Errorf("many paths: %+v", m.Diff)
	}
}

func TestManyPathsAreServedInPagesInAnyOrder(t *testing.T) {
	b, c0 := newBackend(t)
	id := "ap-sample-many-paths"
	c := open(t, b, id)
	first, err := b.Page(id, card.PagePaths, 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Pages < 2 || first.ReviewDigest != c.Diff.ReviewDigest {
		t.Fatalf("pages %d, digest %q", first.Pages, first.ReviewDigest)
	}
	var r card.Reading
	rows := 0
	for n := first.Pages - 1; n >= 0; n-- { // backwards
		p, err := b.Page(id, card.PagePaths, n)
		if err != nil {
			t.Fatalf("page %d: %v", n, err)
		}
		if p.Kind != card.PagePaths || p.N != n || p.Pages != first.Pages || p.ReviewDigest != first.ReviewDigest || len(p.Paths) == 0 {
			t.Fatalf("page %d: %+v", n, p)
		}
		rows += len(p.Paths)
		r.ReadPage(p)
	}
	if rows != 5000 {
		t.Errorf("rows %d", rows)
	}
	r.Opened = c0.t
	r.Lists = [card.NumLists]bool{true, true, true, true}
	if ok, why := c.MayApprove(r, c0.t.Add(time.Second)); !ok {
		t.Errorf("every page read, still refused: %s", why)
	}
	if _, err := b.Page(id, card.PagePaths, first.Pages); err == nil {
		t.Error("a page past the end was served")
	}
	if _, err := b.Page("ap-nope", card.PagePaths, 0); err == nil {
		t.Error("a page of an unknown card was served")
	}
	if _, err := b.Page(id, card.PageKind(9), 0); err == nil {
		t.Error("a page of an unknown kind was served")
	}
}

// joinedPatch reads every page of a card's patch and returns the joined
// lines, checking the rules of the pager on the way.
func joinedPatch(t *testing.T, b *Backend, id string) (lines []string, pages int) {
	t.Helper()
	p0, err := b.Page(id, card.PagePatch, 0)
	if err != nil {
		t.Fatal(err)
	}
	var cur strings.Builder
	for n := 0; n < p0.Pages; n++ {
		p, err := b.Page(id, card.PagePatch, n)
		if err != nil {
			t.Fatal(err)
		}
		if p.Pages != p0.Pages || p.ReviewDigest != p0.ReviewDigest || len(p.Patch) != len(p.Cont) {
			t.Fatalf("page %d: %+v", n, p)
		}
		for i, piece := range p.Patch {
			if piece == "" && p.Cont[i] {
				t.Fatalf("page %d: an empty piece that continues", n)
			}
			cur.WriteString(piece)
			if !p.Cont[i] {
				lines = append(lines, cur.String())
				cur.Reset()
			}
		}
	}
	if cur.Len() != 0 {
		t.Fatal("the last piece continues")
	}
	return lines, p0.Pages
}

func TestBigPatchIsSeveralPagesWithALongLine(t *testing.T) {
	b, _ := newBackend(t)
	lines, pages := joinedPatch(t, b, "ap-sample-big-patch")
	if pages < 3 {
		t.Errorf("pages %d", pages)
	}
	long := 0
	for _, l := range lines {
		long = max(long, len(l))
	}
	if long <= pageBytes() {
		t.Errorf("longest line %d bytes, not longer than a page", long)
	}
}

// pageBytes is the most bytes a page can hold.
func pageBytes() int { return pieceBytes * pageLines }

func TestHostilePatchComesBackWhole(t *testing.T) {
	b, _ := newBackend(t)
	lines, _ := joinedPatch(t, b, "ap-sample-hostile")
	want := hostileSpec().patch
	if len(lines) != len(want) {
		t.Fatalf("%d lines, want %d", len(lines), len(want))
	}
	for i := range lines {
		if lines[i] != want[i] {
			t.Errorf("line %d differs", i)
		}
	}
}

func TestPatchOfEverySampleIsServable(t *testing.T) {
	b, _ := newBackend(t)
	l, _ := b.List()
	for _, it := range l.Pending {
		joinedPatch(t, b, it.ID)
	}
}

func TestSplitPatchRules(t *testing.T) {
	cases := map[string][]string{
		"empty patch":      nil,
		"empty lines":      {"", "", "x", ""},
		"exact piece":      {strings.Repeat("a", pieceBytes)},
		"piece plus one":   {strings.Repeat("a", pieceBytes+1), "b"},
		"two pieces exact": {strings.Repeat("a", 2*pieceBytes), ""},
		"exact page":       manyLines(pageLines),
		"page plus one":    manyLines(pageLines + 1),
		"long line":        {strings.Repeat("z", 5*pageBytes()+7)},
	}
	for name, in := range cases {
		pages := splitPatch(in)
		if len(pages) == 0 {
			t.Errorf("%s: no page", name)
			continue
		}
		var joined []string
		var cur strings.Builder
		for n, pg := range pages {
			if len(pg.lines) != len(pg.cont) || len(pg.lines) > pageLines {
				t.Errorf("%s: page %d malformed", name, n)
			}
			if len(pg.lines) == 0 && !(len(pages) == 1 && len(in) == 0) {
				t.Errorf("%s: page %d has no piece", name, n)
			}
			for i, piece := range pg.lines {
				if len(piece) > pieceBytes {
					t.Errorf("%s: piece of %d bytes", name, len(piece))
				}
				if piece == "" && pg.cont[i] {
					t.Errorf("%s: empty piece that continues", name)
				}
				cur.WriteString(piece)
				if !pg.cont[i] {
					joined = append(joined, cur.String())
					cur.Reset()
				}
			}
		}
		if cur.Len() != 0 || len(joined) != len(in) {
			t.Errorf("%s: lines do not join back", name)
			continue
		}
		for i := range in {
			if joined[i] != in[i] {
				t.Errorf("%s: line %d differs", name, i)
			}
		}
	}
	if got := splitPatch(nil); len(got) != 1 || len(got[0].lines) != 0 {
		t.Errorf("an empty patch is one empty page, got %+v", got)
	}
}

func manyLines(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "line"
	}
	return out
}

func TestPairDiffersOnceTheFirstWasRead(t *testing.T) {
	b, _ := newBackend(t)
	if c := open(t, b, "ap-sample-pair-2"); c.Differs != nil {
		t.Error("a differs block before the first card was read")
	}
	b, _ = newBackend(t)
	first := open(t, b, "ap-sample-pair-1")
	second := open(t, b, "ap-sample-pair-2")
	if first.Verb.Feature != second.Verb.Feature || first.Verb.Target != second.Verb.Target || first.Title.Project != second.Title.Project {
		t.Error("the pair is not for one target")
	}
	d := second.Differs
	if d == nil || d.EarlierID != "ap-sample-pair-1" {
		t.Fatalf("differs: %+v", d)
	}
	if !d.Merge.Changed() || len(d.PathsAdded) != 1 || len(d.PathsRemoved) != 1 {
		t.Errorf("the second does not differ: %+v", d)
	}
}

func TestEndsWhileOpen(t *testing.T) {
	b, c := newBackend(t)
	id := "ap-sample-ends"
	if got := open(t, b, id); got.Ended() {
		t.Fatal("ended at once")
	}
	c.t = c.t.Add(sampleEndsAfter - time.Second)
	if got := open(t, b, id); got.Ended() {
		t.Fatal("ended early")
	}
	c.t = c.t.Add(time.Second)
	got := open(t, b, id)
	if !got.Ended() || got.After.State != record.StateExpired || !strings.Contains(got.After.Line, "sample") {
		t.Errorf("after: %+v", got.After)
	}
	l, _ := b.List()
	if len(l.Recent) != 1 || l.Recent[0].ID != id {
		t.Errorf("recent: %+v", l.Recent)
	}
	b.Activity(c.t)
	if _, err := b.Decide(card.Decision{ID: id, BindingSHA256: got.BindingSHA256}); code(err) != record.CodeApprovalExpired {
		t.Errorf("decide on an ended card: %v", err)
	}
}

func TestEndsNeverBeforeItIsOpened(t *testing.T) {
	b, c := newBackend(t)
	c.t = c.t.Add(10 * sampleEndsAfter)
	if got := open(t, b, "ap-sample-ends"); got.Ended() {
		t.Error("ended before it was first opened")
	}
}

func TestDecideNeedsActivity(t *testing.T) {
	b, c := newBackend(t)
	k := open(t, b, "ap-sample-clean")
	d := card.Decision{ID: k.Title.ID, BindingSHA256: k.BindingSHA256, Approve: true}
	if _, err := b.Decide(d); code(err) != record.CodeOwnerAway {
		t.Fatalf("no activity: %v", err)
	}
	b.Activity(c.t)
	c.t = c.t.Add(presence11())
	if _, err := b.Decide(d); code(err) != record.CodeOwnerAway {
		t.Fatalf("stale activity: %v", err)
	}
	b.Activity(c.t)
	out, err := b.Decide(d)
	if err != nil {
		t.Fatal(err)
	}
	if out.State != record.StateApproved || out.Held || !strings.Contains(out.Line, "sample") || !strings.Contains(out.Line, "nothing was changed") {
		t.Errorf("outcome %+v", out)
	}
	if _, err := b.Decide(d); code(err) != record.CodeApprovalConsumed {
		t.Errorf("a second decision: %v", err)
	}
	l, _ := b.List()
	if len(l.Recent) != 1 || l.Recent[0].State != record.StateApproved {
		t.Errorf("recent: %+v", l.Recent)
	}
}

// presence11 is a little more than the default presence window.
func presence11() time.Duration { return 11 * time.Minute }

func TestDecideChecksDigestAndSignOffs(t *testing.T) {
	b, c := newBackend(t)
	b.Activity(c.t)
	k := open(t, b, "ap-sample-flagged")
	d := card.Decision{ID: k.Title.ID, BindingSHA256: k.BindingSHA256, Approve: true}
	bad := d
	bad.BindingSHA256 = strings.Repeat("0", 64)
	if _, err := b.Decide(bad); code(err) != record.CodeBindingMismatch {
		t.Errorf("digest: %v", err)
	}
	if _, err := b.Decide(d); code(err) != record.CodeDecisionInvalid {
		t.Errorf("missing sign-off: %v", err)
	}
	extra := d
	extra.SignedOff = []record.SignOff{k.Flagged[0].SignOff(), {Kind: record.FlagSymlink, Path: "other"}}
	if _, err := b.Decide(extra); code(err) != record.CodeDecisionInvalid {
		t.Errorf("surplus sign-off: %v", err)
	}
	long := d
	long.Approve, long.Reason = false, strings.Repeat("r", record.MaxReasonBytes+1)
	if _, err := b.Decide(long); code(err) != record.CodeDecisionInvalid {
		t.Errorf("long reason: %v", err)
	}
	d.SignedOff = []record.SignOff{k.Flagged[0].SignOff()}
	if out, err := b.Decide(d); err != nil || out.State != record.StateApproved {
		t.Errorf("signed off: %+v %v", out, err)
	}
}

func TestDenyKeepsItsReasonOutOfTheLine(t *testing.T) {
	b, c := newBackend(t)
	b.Activity(c.t)
	k := open(t, b, "ap-sample-flagged")
	out, err := b.Decide(card.Decision{ID: k.Title.ID, BindingSHA256: k.BindingSHA256, Reason: "no thanks"})
	if err != nil || out.State != record.StateDenied || out.Held || !strings.Contains(out.Line, "sample") {
		t.Errorf("deny: %+v %v", out, err)
	}
}

func TestHeldPastTheLimit(t *testing.T) {
	b, c := newBackend(t)
	b.Activity(c.t)
	id := "ap-sample-held"
	k := open(t, b, id)
	if !k.After.Held {
		t.Fatal("the held sample starts without its mark")
	}
	if l, _ := b.List(); !itemOf(l, id).Held {
		t.Error("the list does not mark it")
	}
	d := card.Decision{ID: id, BindingSHA256: k.BindingSHA256, Approve: true}
	out, err := b.Decide(d)
	if err != nil || !out.Held || out.State != record.StatePending || !strings.Contains(out.Line, "sample") {
		t.Fatalf("approve past the limit: %+v %v", out, err)
	}
	if k = open(t, b, id); !k.After.Held || k.Ended() {
		t.Errorf("the card did not come back marked and pending: %+v", k.After)
	}
	d.Approve = false
	out, err = b.Decide(d)
	if err != nil || out.Held || out.State != record.StateDenied {
		t.Errorf("a deny past the limit: %+v %v", out, err)
	}
}

func TestHeldMarkIsClearedWhenTheCardIsDecided(t *testing.T) {
	for _, approve := range []bool{true, false} {
		b, c := newBackend(t)
		b.Activity(c.t)
		id := "ap-sample-held"
		k := open(t, b, id)
		if approve {
			// Room for the held card: the limit of project c is raised.
			if _, err := b.SetLimit(projectC, limits.Change{To: 3, Typed: true}); err != nil {
				t.Fatal(err)
			}
		}
		out, err := b.Decide(card.Decision{ID: id, BindingSHA256: k.BindingSHA256, Approve: approve})
		if err != nil || out.Held {
			t.Fatalf("approve=%v: %+v %v", approve, out, err)
		}
		if k = open(t, b, id); k.After.Held {
			t.Errorf("approve=%v: the card keeps its held mark", approve)
		}
		l, _ := b.List()
		if len(l.Recent) != 1 || l.Recent[0].Held {
			t.Errorf("approve=%v: the list row keeps its held mark: %+v", approve, l.Recent)
		}
	}
}

func itemOf(l card.Listing, id string) card.Item {
	for _, it := range append(append([]card.Item(nil), l.Pending...), l.Recent...) {
		if it.ID == id {
			return it
		}
	}
	return card.Item{}
}

func TestApproveBecomesHeldWhenTheLimitIsReached(t *testing.T) {
	b, c := newBackend(t)
	b.Activity(c.t)
	if _, err := b.SetLimit(projectB, limits.Change{To: 1}); err != nil {
		t.Fatal(err)
	}
	one, two := open(t, b, "ap-sample-pair-1"), open(t, b, "ap-sample-pair-2")
	if out, err := b.Decide(card.Decision{ID: one.Title.ID, BindingSHA256: one.BindingSHA256, Approve: true}); err != nil || out.Held {
		t.Fatalf("first: %+v %v", out, err)
	}
	out, err := b.Decide(card.Decision{ID: two.Title.ID, BindingSHA256: two.BindingSHA256, Approve: true})
	if err != nil || !out.Held {
		t.Fatalf("second: %+v %v", out, err)
	}
	v, _ := b.Limit(projectB)
	if len(v.Days) != limits.WindowDays {
		t.Errorf("days %d", len(v.Days))
	}
}

func TestLimitRulesPerProject(t *testing.T) {
	b, c := newBackend(t)
	va, err := b.Limit(projectA)
	if err != nil || va.Project != projectA || va.Limit != limits.Default {
		t.Fatalf("%+v %v", va, err)
	}
	vb, _ := b.Limit(projectB)
	if vb.Limit == va.Limit {
		t.Error("the projects share a limit")
	}
	if _, err := b.Limit("project-z"); err == nil {
		t.Error("an unknown project has a limit")
	}
	// A raise must be typed, by at most half, once a day.
	if _, err := b.SetLimit(projectA, limits.Change{To: 18}); code(err) != record.CodeLimitRefused {
		t.Errorf("a raise by one key: %v", err)
	}
	if _, err := b.SetLimit(projectA, limits.Change{To: 19, Typed: true}); code(err) != record.CodeLimitRefused {
		t.Errorf("a raise past half: %v", err)
	}
	v, err := b.SetLimit(projectA, limits.Change{To: 18, Typed: true})
	if err != nil || v.Limit != 18 || !v.RaisedToday {
		t.Fatalf("raise: %+v %v", v, err)
	}
	if _, err := b.SetLimit(projectA, limits.Change{To: 20, Typed: true}); code(err) != record.CodeLimitRefused {
		t.Errorf("a second raise: %v", err)
	}
	if v, err = b.SetLimit(projectA, limits.Change{To: 5}); err != nil || v.Limit != 5 {
		t.Errorf("lowering: %+v %v", v, err)
	}
	if v, _ = b.Limit(projectB); v.Limit != 6 {
		t.Errorf("another project moved: %d", v.Limit)
	}
	// The next day the raise is possible again.
	c.t = c.t.Add(24 * time.Hour)
	if v, _ = b.Limit(projectA); v.RaisedToday {
		t.Error("the raise still counts a day later")
	}
	if _, err := b.SetLimit("project-z", limits.Change{To: 3}); err == nil {
		t.Error("an unknown project was set")
	}
}

func TestLookedMovesTheMark(t *testing.T) {
	b, c := newBackend(t)
	if l, _ := b.List(); !l.LookedAt.IsZero() {
		t.Error("the mark starts set")
	}
	b.Looked(c.t)
	if l, _ := b.List(); !l.LookedAt.Equal(c.t) {
		t.Errorf("mark %v", l.LookedAt)
	}
	b.Looked(c.t.Add(-time.Hour))
	if l, _ := b.List(); !l.LookedAt.Equal(c.t) {
		t.Error("the mark moved back")
	}
}

func TestDeciderAndUnknownCard(t *testing.T) {
	b, c := newBackend(t)
	if ok, _ := b.Decider(); !ok {
		t.Error("not a decider")
	}
	b.Activity(c.t)
	if _, err := b.Decide(card.Decision{ID: "ap-nope"}); err == nil {
		t.Error("an unknown card was decided")
	}
	if _, err := b.Open("ap-nope"); err == nil {
		t.Error("an unknown card was opened")
	}
}

func TestSamplesUseNeutralNames(t *testing.T) {
	b, _ := newBackend(t)
	l, _ := b.List()
	for _, it := range l.Pending {
		k := open(t, b, it.ID)
		if k.Title.Subject.Runtime != "runtime-a" || !strings.HasPrefix(k.Title.Project, "project-") {
			t.Errorf("%s: runtime %q, project %q", it.ID, k.Title.Subject.Runtime, k.Title.Project)
		}
	}
}

func newBackendTime() time.Time { return time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC) }
