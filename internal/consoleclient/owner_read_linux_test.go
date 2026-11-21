//go:build linux && ataniq_ownerchannel

package consoleclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
)

var t0 = time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)

func TestListMapsEveryFieldOfTheListing(t *testing.T) {
	f := startOwner(t)
	o := newOwner(t, f.dir)
	wire := ownerwire.Listing{
		Pending: []ownerwire.PendingItem{
			{ID: "ap-one", SHA256: strings.Repeat("a", 64), Project: "project-a", Verb: record.VerbFeatureFinish, Feature: "small-fix",
				RequestedAt: record.FormatTime(t0), PendingExpiresAt: record.FormatTime(t0.Add(30 * time.Minute))},
			{ID: "ap-two", SHA256: strings.Repeat("b", 64), Project: "project-b", Verb: record.VerbBranchPrune,
				RequestedAt: record.FormatTime(t0.Add(time.Minute)), PendingExpiresAt: record.FormatTime(t0.Add(31 * time.Minute)), Held: true},
		},
		Recent: []ownerwire.RecentItem{
			{ID: "ap-three", Project: "project-a", Verb: record.VerbFeatureFinish, Feature: "old-work", State: record.StateApplied,
				Line: "applied: develop moved", At: record.FormatTime(t0.Add(-time.Hour))},
			{ID: "ap-four", Project: "project-c", Verb: record.VerbBranchPrune, State: record.StateDenied,
				Line: "denied by the owner", At: record.FormatTime(t0.Add(-2 * time.Hour))},
		},
		Rules:     []ownerwire.Rule{{ID: "first-template", Project: "project-a", Line: "a rule in words", Uses: 3, On: true}},
		NotListed: 4,
		Count:     ownerwire.Count{Requests: 9, WouldMeet: 5},
		LookedAt:  record.FormatTime(t0.Add(-30 * time.Minute)),
	}
	f.onResult(ownerwire.OpList, mustEncode(t)(ownerwire.EncodeListing(wire)))
	got, err := o.List()
	if err != nil {
		t.Fatal(err)
	}
	want := card.Listing{
		Pending: []card.Item{
			{ID: "ap-one", Project: "project-a", Verb: record.VerbFeatureFinish, Feature: "small-fix", Asked: t0, Expires: t0.Add(30 * time.Minute)},
			{ID: "ap-two", Project: "project-b", Verb: record.VerbBranchPrune, Asked: t0.Add(time.Minute), Expires: t0.Add(31 * time.Minute), Held: true},
		},
		Recent: []card.Item{
			{ID: "ap-three", Project: "project-a", Verb: record.VerbFeatureFinish, Feature: "old-work", Asked: t0.Add(-time.Hour), State: record.StateApplied, Line: "applied: develop moved"},
			{ID: "ap-four", Project: "project-c", Verb: record.VerbBranchPrune, Asked: t0.Add(-2 * time.Hour), State: record.StateDenied, Line: "denied by the owner"},
		},
		Rules:    []card.Rule{{Project: "project-a", Name: "first-template", Text: "a rule in words", On: true, Uses: 3}},
		Count:    card.Count{Requests: 9, Met: 5},
		LookedAt: t0.Add(-30 * time.Minute),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listing:\n got %+v\nwant %+v", got, want)
	}
	if got.Sample {
		t.Fatal("a daemon's cards are not samples")
	}
	if n := o.NotListed(); n != 4 {
		t.Fatalf("NotListed = %d", n)
	}
	// The mark is absent when he never looked.
	wire.LookedAt = ""
	wire.NotListed = 0
	f.onResult(ownerwire.OpList, mustEncode(t)(ownerwire.EncodeListing(wire)))
	got, err = o.List()
	if err != nil || !got.LookedAt.IsZero() || o.NotListed() != 0 {
		t.Fatalf("%v %v %d", err, got.LookedAt, o.NotListed())
	}
}

func TestListCleansHostLinesAndRefusesAProjectThatIsNotPlain(t *testing.T) {
	f := startOwner(t)
	o := newOwner(t, f.dir)
	wire := ownerwire.Listing{
		Pending: []ownerwire.PendingItem{}, Rules: []ownerwire.Rule{{ID: "r" + hostileMark, Project: "project-a", Line: "rule " + hostileMark}},
		Recent: []ownerwire.RecentItem{{ID: "ap-x", Project: "project-a", Verb: record.VerbBranchPrune, State: record.StateDenied,
			Line: "line " + hostileMark, At: record.FormatTime(t0)}},
	}
	f.onResult(ownerwire.OpList, mustEncode(t)(ownerwire.EncodeListing(wire)))
	l, err := o.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{l.Recent[0].Line, l.Rules[0].Text, l.Rules[0].Name} {
		if strings.ContainsAny(s, "\u202e\u200b\u2066") || s == "" {
			t.Errorf("not cleaned: %q", s)
		}
	}
	// A project the escaping would change cannot be asked about by the name
	// shown, so the listing is not shown.
	for _, bad := range []string{"project" + hostileMark, "project​"} {
		for _, place := range []string{"pending", "recent", "rules"} {
			w := ownerwire.Listing{Pending: []ownerwire.PendingItem{}, Recent: []ownerwire.RecentItem{}, Rules: []ownerwire.Rule{}}
			switch place {
			case "pending":
				w.Pending = []ownerwire.PendingItem{{ID: "ap-x", SHA256: strings.Repeat("a", 64), Project: bad, Verb: record.VerbBranchPrune,
					RequestedAt: record.FormatTime(t0), PendingExpiresAt: record.FormatTime(t0)}}
			case "recent":
				w.Recent = []ownerwire.RecentItem{{ID: "ap-x", Project: bad, Verb: record.VerbBranchPrune, State: record.StateDenied, Line: "l", At: record.FormatTime(t0)}}
			case "rules":
				w.Rules = []ownerwire.Rule{{ID: "r", Project: bad, Line: "l"}}
			}
			f.onResult(ownerwire.OpList, mustEncode(t)(ownerwire.EncodeListing(w)))
			_, err := o.List()
			var u *Unavailable
			if !errors.As(err, &u) || u.State != StateBadAnswer {
				t.Errorf("%q in %s: %v", bad, place, err)
			}
		}
	}
}

func TestListRefusesAnAnswerTheWirePackageRefusesAndStopsTrustingTheConnection(t *testing.T) {
	f := startOwner(t)
	o := newOwner(t, f.dir)
	f.onResult(ownerwire.OpList, []byte(`{"pending":[],"recent":[],"rules":[],"not_listed":0,"count":{"requests":0,"would_meet":0},"extra":1}`))
	_, err := o.List()
	var u *Unavailable
	if !errors.As(err, &u) || u.State != StateBadAnswer {
		t.Fatalf("%v", err)
	}
	if o.conn != nil {
		t.Fatal("the connection of a peer that sent a bad answer was kept")
	}
	// A listing that is not even an object.
	f.onResult(ownerwire.OpList, []byte(`[1]`))
	if _, err := o.List(); !errors.As(err, &u) || u.State != StateBadAnswer {
		t.Fatalf("%v", err)
	}
}

func TestOpenBuildsTheCardFromTheRecordTheOutcomeAndTheEarlierCard(t *testing.T) {
	f := startOwner(t)
	o := newOwner(t, f.dir)
	paths := []record.PathChange{testChange("lib/a.go", "M", 3, 1), testChange("lib/b.go", "A", 8, 0)}
	cur := newTestCard(t, "now", "small-fix", t0, paths, 0)
	prev := newTestCard(t, "before", "small-fix", t0.Add(-time.Hour), paths[:1], 0)
	readAt := t0.Add(-30 * time.Minute)
	f.onResult(ownerwire.OpShow, cur.show(t, func(r *ownerwire.ShowResult) {
		r.Held = true
		r.Outcome = &ownerwire.Outcome{State: record.StatePending, Line: "the door refused the last answer " + hostileMark}
		r.Earlier = &ownerwire.Earlier{Record: ownerwire.NewRecord(prev.data), ReadAt: record.FormatTime(readAt)}
	}))
	got, err := o.Open(cur.p.ID)
	if err != nil {
		t.Fatal(err)
	}
	want, err := card.New(cur.p, card.Inputs{
		Held: true, State: record.StatePending, Line: clean("the door refused the last answer " + hostileMark),
		Earlier: &card.Earlier{Record: prev.p, ReadAt: readAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the card is not the one the record, the outcome and the earlier record make:\n got %+v\nwant %+v", got, want)
	}
	if !got.After.Held || got.After.State != record.StatePending || got.After.Line != clean("the door refused the last answer "+hostileMark) || got.Differs == nil {
		t.Fatalf("after = %+v, differs = %v", got.After, got.Differs)
	}
	if got.Diff.ReviewDigest != cur.review || got.BindingSHA256 != cur.p.BindingSHA256 {
		t.Fatal("the digests of the card are not the record's")
	}
	if p := f.of(ownerwire.OpShow); len(p) != 1 || p[0].params != `{"id":"`+cur.p.ID+`"}` {
		t.Fatalf("owner.show params = %v", p)
	}
	// A decided card: the daemon's outcome is the card's state.
	f.onResult(ownerwire.OpShow, cur.show(t, func(r *ownerwire.ShowResult) {
		r.Outcome = &ownerwire.Outcome{State: record.StateApplied, Line: "applied"}
	}))
	got, err = o.Open(cur.p.ID)
	if err != nil || got.After.State != record.StateApplied || !got.Decided() {
		t.Fatalf("%v %+v", err, got.After)
	}
}

func TestOpenRefusesWhatIsNotTheCardAskedFor(t *testing.T) {
	f := startOwner(t)
	o := newOwner(t, f.dir)
	paths := []record.PathChange{testChange("a.go", "M", 1, 1)}
	other := newTestCard(t, "other", "small-fix", t0, paths, 0)
	asked := newTestCard(t, "asked", "small-fix", t0, paths, 0)
	// Another card's record.
	f.onResult(ownerwire.OpShow, other.show(t, nil))
	var u *Unavailable
	if _, err := o.Open(asked.p.ID); !errors.As(err, &u) || u.State != StateBadAnswer {
		t.Fatalf("another card: %v", err)
	}
	// A record whose digest is not its bytes'.
	bad := ownerwire.NewRecord(asked.data)
	bad.SHA256 = strings.Repeat("0", 64)
	raw, _ := json.Marshal(ownerwire.ShowResult{Record: bad})
	f.onResult(ownerwire.OpShow, raw)
	if _, err := o.Open(asked.p.ID); !errors.As(err, &u) || u.State != StateBadAnswer {
		t.Fatalf("a digest that is not the bytes': %v", err)
	}
	// Bytes that are no record.
	f.onResult(ownerwire.OpShow, mustEncode(t)(ownerwire.EncodeShowResult(ownerwire.ShowResult{Record: ownerwire.NewRecord([]byte(`{"x":1}`))})))
	if _, err := o.Open(asked.p.ID); !errors.As(err, &u) || u.State != StateBadAnswer {
		t.Fatalf("bytes that are no record: %v", err)
	}
	// An earlier record that is no record.
	f.onResult(ownerwire.OpShow, asked.show(t, func(r *ownerwire.ShowResult) {
		r.Earlier = &ownerwire.Earlier{Record: ownerwire.NewRecord([]byte(`{"x":1}`)), ReadAt: record.FormatTime(t0)}
	}))
	if _, err := o.Open(asked.p.ID); !errors.As(err, &u) || u.State != StateBadAnswer {
		t.Fatalf("an earlier record that is no record: %v", err)
	}
	// A card the console cannot make is the card package's refusal, by code.
	hostile := newTestCard(t, "feature-name", "small-fix", t0, paths, 0)
	hostile.p.Binding.Argv = []string{"Not A Name"}
	// The decoder refuses it first; the refusal is the daemon's record being bad.
	data, err := record.EncodePending(hostile.p)
	if err == nil {
		f.onResult(ownerwire.OpShow, mustEncode(t)(ownerwire.EncodeShowResult(ownerwire.ShowResult{Record: ownerwire.NewRecord(data)})))
		if _, err := o.Open(hostile.p.ID); err == nil {
			t.Fatal("a card with a feature name outside the grammar was made")
		}
	}
	// Nothing was kept of a card that was not made: its pages cannot be asked.
	if _, err := o.Page(asked.p.ID, card.PagePatch, 0); !errors.Is(err, ErrCardNotOpen) {
		t.Fatalf("Page of a card that was not opened: %v", err)
	}
}

// bigPaths makes n paths with long names.
func bigPaths(n int) []record.PathChange {
	out := make([]record.PathChange, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, testChange(fmt.Sprintf("generated/%s/file-%05d.txt", strings.Repeat("d", 200), i), "A", 3, 0))
	}
	return out
}

// pagedFake makes the fake serve the pages of a path list and a patch.
func pagedFake(t *testing.T, f *ownerFake, c testCard, paths []record.PathChange, patch []string) (pathPages []ownerwire.PathsPage, patchPages []ownerwire.PatchPage) {
	t.Helper()
	var err error
	if pathPages, err = ownerwire.SplitPaths(c.p.ID, c.review, paths); err != nil {
		t.Fatal(err)
	}
	if patchPages, err = ownerwire.SplitPatch(c.p.ID, c.review, patch); err != nil {
		t.Fatal(err)
	}
	pageOf := func(params json.RawMessage) ownerwire.PageParams {
		q, err := ownerwire.DecodePageParams(params)
		if err != nil {
			t.Errorf("the page request: %v", err)
		}
		return q
	}
	f.on(ownerwire.OpPaths, func(params json.RawMessage) (any, *clientapi.ErrorBody) {
		q := pageOf(params)
		if q.ID != c.p.ID || q.ReviewDigest != c.review || q.Page < 0 || q.Page >= len(pathPages) {
			return nil, &clientapi.ErrorBody{Code: clientapi.ErrorCode(record.CodeReviewChanged), Message: "no such page"}
		}
		return json.RawMessage(mustEncode(t)(ownerwire.EncodePathsPage(pathPages[q.Page]))), nil
	})
	f.on(ownerwire.OpPatch, func(params json.RawMessage) (any, *clientapi.ErrorBody) {
		q := pageOf(params)
		if q.ID != c.p.ID || q.ReviewDigest != c.review || q.Page < 0 || q.Page >= len(patchPages) {
			return nil, &clientapi.ErrorBody{Code: clientapi.ErrorCode(record.CodeReviewChanged), Message: "no such page"}
		}
		return json.RawMessage(mustEncode(t)(ownerwire.EncodePatchPage(patchPages[q.Page]))), nil
	})
	return pathPages, patchPages
}

func TestAPathListAndAPatchLargerThanOneResponseAreReadInPages(t *testing.T) {
	f := startOwner(t)
	o := newOwner(t, f.dir)
	paths := bigPaths(7000) // over 1 MiB as encoded
	var patch []string
	for i := 0; i < 4000; i++ {
		patch = append(patch, fmt.Sprintf("+line %05d %s", i, strings.Repeat("p", 200)))
	}
	long := strings.Repeat("0123456789,", 40000) // 440 KB: longer than the room of a page
	patch = append(patch, "+"+long, "tail \xff\xfe not utf-8", "")
	c := newTestCard(t, "big", "bulk-import", t0, paths, 100)
	pathPages, patchPages := pagedFake(t, f, c, paths, patch)
	if len(pathPages) < 4 || len(patchPages) < 4 {
		t.Fatalf("the fixtures are not larger than one response: %d and %d pages", len(pathPages), len(patchPages))
	}
	openOne(t, f, o, c)

	var gotPaths []record.PathChange
	for n := 0; n < len(pathPages); n++ {
		p, err := o.Page(c.p.ID, card.PagePaths, n)
		if err != nil {
			t.Fatalf("paths page %d: %v", n, err)
		}
		if p.Kind != card.PagePaths || p.N != n || p.Pages != len(pathPages) || p.ReviewDigest != c.review || p.Patch != nil || p.Cont != nil {
			t.Fatalf("paths page %d = %v", n, p)
		}
		gotPaths = append(gotPaths, p.Paths...)
	}
	if !reflect.DeepEqual(gotPaths, paths) {
		t.Fatalf("the paths read in pages (%d) are not the list (%d)", len(gotPaths), len(paths))
	}

	var gotPatch []string
	var open strings.Builder
	pieces := 0
	for n := 0; n < len(patchPages); n++ {
		p, err := o.Page(c.p.ID, card.PagePatch, n)
		if err != nil {
			t.Fatalf("patch page %d: %v", n, err)
		}
		if p.Kind != card.PagePatch || p.N != n || p.Pages != len(patchPages) || p.ReviewDigest != c.review || p.Paths != nil || len(p.Patch) != len(p.Cont) {
			t.Fatalf("patch page %d = %v", n, p)
		}
		for i, l := range p.Patch {
			open.WriteString(l)
			pieces++
			if !p.Cont[i] {
				gotPatch = append(gotPatch, open.String())
				open.Reset()
				pieces = 0
			}
		}
	}
	if pieces != 0 {
		t.Fatal("the patch ended in the middle of a line")
	}
	if !reflect.DeepEqual(gotPatch, patch) {
		t.Fatalf("the patch read in pages is not the patch: %d lines against %d", len(gotPatch), len(patch))
	}
	// The line that did not fit a page came in pieces.
	sawCont := false
	for _, pg := range patchPages {
		for _, l := range pg.Lines {
			sawCont = sawCont || l.Cont
		}
	}
	if !sawCont {
		t.Fatal("no line was carried in pieces: the fixture does not hold the case")
	}
	// Each request named the card's review digest and the page.
	for i, call := range f.of(ownerwire.OpPatch) {
		want := fmt.Sprintf(`{"id":%q,"page":%d,"review_digest":%q}`, c.p.ID, i, c.review)
		if call.params != want {
			t.Fatalf("patch request %d = %s, want %s", i, call.params, want)
		}
	}
}

func TestAReviewThatChangedInTheMiddleOfAReadingEndsIt(t *testing.T) {
	f := startOwner(t)
	o := newOwner(t, f.dir)
	paths := bigPaths(3000)
	var patch []string
	for i := 0; i < 3000; i++ {
		patch = append(patch, fmt.Sprintf("+%05d %s", i, strings.Repeat("q", 300)))
	}
	c := newTestCard(t, "moving", "bulk-import", t0, paths, 50)
	pathPages, patchPages := pagedFake(t, f, c, paths, patch)
	if len(patchPages) < 3 || len(pathPages) < 3 {
		t.Fatalf("pages: %d %d", len(pathPages), len(patchPages))
	}
	openOne(t, f, o, c)
	// The daemon's review moves after page 1: it refuses the later pages by
	// its code.
	inner := f.handlers[ownerwire.OpPatch]
	f.on(ownerwire.OpPatch, func(params json.RawMessage) (any, *clientapi.ErrorBody) {
		q, _ := ownerwire.DecodePageParams(params)
		if q.Page >= 2 {
			return nil, &clientapi.ErrorBody{Code: clientapi.ErrorCode(record.CodeReviewChanged), Message: "the review is not the one named"}
		}
		return inner(params)
	})
	for n := 0; n < 2; n++ {
		if _, err := o.Page(c.p.ID, card.PagePatch, n); err != nil {
			t.Fatalf("page %d: %v", n, err)
		}
	}
	_, err := o.Page(c.p.ID, card.PagePatch, 2)
	if code, ok := record.CodeOf(err); !ok || code != record.CodeReviewChanged {
		t.Fatalf("page 2: %v, want review-changed", err)
	}
	// A refusal is an answer: the connection stays, and a page that is still
	// good can be read.
	if _, err := o.Page(c.p.ID, card.PagePatch, 1); err != nil {
		t.Fatalf("after review-changed: %v", err)
	}
	if n := f.count(ownerwire.OpDecider); n != 1 {
		t.Fatalf("%d connections", n)
	}
}

func TestAPageOfAnotherReviewOrAnotherPageIsNotTakenIn(t *testing.T) {
	f := startOwner(t)
	o := newOwner(t, f.dir)
	paths := bigPaths(3000)
	c := newTestCard(t, "pages", "bulk-import", t0, paths, 50)
	pathPages, patchPages := pagedFake(t, f, c, paths, []string{"a", "b"})
	openOne(t, f, o, c)
	if len(pathPages) < 2 || len(patchPages) != 1 {
		t.Fatalf("%d %d", len(pathPages), len(patchPages))
	}
	// A page that names another review digest: review-changed, though the
	// daemon did not say so.
	foreign := pathPages[1]
	foreign.ReviewDigest = record.Digest([]byte("another review"))
	f.onResult(ownerwire.OpPaths, mustEncode(t)(ownerwire.EncodePathsPage(foreign)))
	_, err := o.Page(c.p.ID, card.PagePaths, 1)
	if code, ok := record.CodeOf(err); !ok || code != record.CodeReviewChanged {
		t.Fatalf("another review: %v", err)
	}
	if n := f.count(ownerwire.OpDecider); n != 1 {
		t.Fatal("a page of another review ended the connection")
	}
	// A page of another card.
	foreign = pathPages[1]
	foreign.ID = "ap-someone-else"
	f.onResult(ownerwire.OpPaths, mustEncode(t)(ownerwire.EncodePathsPage(foreign)))
	if _, err := o.Page(c.p.ID, card.PagePaths, 1); err == nil {
		t.Fatal("another card's page was taken in")
	} else if code, ok := record.CodeOf(err); !ok || code != record.CodeReviewChanged {
		t.Fatalf("another card: %v", err)
	}
	// A page of another number than the one asked for is a bad answer.
	f.onResult(ownerwire.OpPaths, mustEncode(t)(ownerwire.EncodePathsPage(pathPages[0])))
	var u *Unavailable
	if _, err := o.Page(c.p.ID, card.PagePaths, 1); !errors.As(err, &u) || u.State != StateBadAnswer {
		t.Fatalf("another page: %v", err)
	}
	// A kind of page that is not one.
	if _, err := o.Page(c.p.ID, card.PageKind(0), 0); err == nil {
		t.Fatal("a page of no kind was read")
	}
	// A page of a card never opened is not asked for.
	before := len(f.sent())
	if _, err := o.Page("ap-never-opened", card.PagePatch, 0); !errors.Is(err, ErrCardNotOpen) || len(f.sent()) != before {
		t.Fatalf("%v, %d requests", err, len(f.sent())-before)
	}
	// A page number the wire refuses is not sent.
	before = len(f.sent())
	if _, err := o.Page(c.p.ID, card.PagePatch, -1); err == nil || len(f.sent()) != before {
		t.Fatalf("%v, %d requests", err, len(f.sent())-before)
	}
}

func TestLimitAndSetLimitMapTheViewAndSendTheOwnersNumber(t *testing.T) {
	f := startOwner(t)
	o := newOwner(t, f.dir)
	wire := ownerwire.LimitView{Project: "project-a", Limit: 12, Suggestion: 8, RaisedToday: true,
		Days: []ownerwire.LimitDay{{Date: "2026-11-18", Uses: 3}, {Date: "2026-11-19", Uses: 12, ReachedLimit: true, Alert: true}}}
	want := limits.View{Project: "project-a", Limit: 12, Suggestion: 8, RaisedToday: true,
		Days: []limits.Day{{Date: "2026-11-18", Uses: 3}, {Date: "2026-11-19", Uses: 12, ReachedLimit: true, Alert: true}}}
	f.onResult(ownerwire.OpLimit, mustEncode(t)(ownerwire.EncodeLimitView(wire)))
	f.onResult(ownerwire.OpLimitSet, mustEncode(t)(ownerwire.EncodeLimitView(wire)))
	got, err := o.Limit("project-a")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("%v\n got %+v\nwant %+v", err, got, want)
	}
	if p := f.of(ownerwire.OpLimit); len(p) != 1 || p[0].params != `{"project":"project-a"}` {
		t.Fatalf("owner.limit params = %v", p)
	}
	for _, typed := range []bool{true, false} {
		got, err = o.SetLimit("project-a", limitsChange(15, typed))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%v %+v", err, got)
		}
	}
	calls := f.of(ownerwire.OpLimitSet)
	if len(calls) != 2 || calls[0].params != `{"project":"project-a","to":15,"typed":true}` || calls[1].params != `{"project":"project-a","to":15,"typed":false}` {
		t.Fatalf("owner.limit-set params = %v", calls)
	}
	// The wire's own rules are held before anything is sent.
	before := len(f.sent())
	if _, err := o.SetLimit("project-a", limitsChange(0)); err == nil || len(f.sent()) != before {
		t.Fatalf("a limit under 1: %v", err)
	}
	if _, err := o.Limit(""); err == nil || len(f.sent()) != before {
		t.Fatalf("no project: %v", err)
	}
	// The limit's refusal comes with its reason and no view.
	f.refuse(ownerwire.OpLimitSet, clientapi.ErrorCode(record.CodeLimitRefused), "the limit was raised once today already")
	v, err := o.SetLimit("project-a", limitsChange(18, true))
	var rec *record.Error
	if !errors.As(err, &rec) || rec.Code != record.CodeLimitRefused || rec.Detail != "the limit was raised once today already" || !reflect.DeepEqual(v, limits.View{}) {
		t.Fatalf("%v %+v", err, v)
	}
	// A view of another project is not the answer.
	other := wire
	other.Project = "project-b"
	f.onResult(ownerwire.OpLimit, mustEncode(t)(ownerwire.EncodeLimitView(other)))
	var u *Unavailable
	if _, err := o.Limit("project-a"); !errors.As(err, &u) || u.State != StateBadAnswer {
		t.Fatalf("%v", err)
	}
}

func limitsChange(to int, typed ...bool) limits.Change {
	return limits.Change{To: to, Typed: len(typed) > 0 && typed[0]}
}
