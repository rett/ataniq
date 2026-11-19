package ownerchannel

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

func TestTheListingShowsWhatWaitsAndWhatWasDecided(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()

	l, err := e.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Pending) != 0 || len(l.Recent) != 0 || l.Rules == nil || l.LookedAt != "" || l.Count != (ownerwire.Count{}) {
		t.Fatalf("an empty listing is %+v", l)
	}

	a := e.card("feature-a")
	e.advance(time.Minute)
	b := e.card("feature-b")
	l, err = e.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Pending) != 2 || l.Pending[0].ID != a.ID || l.Pending[1].ID != b.ID {
		t.Fatalf("the pending rows are %+v, want the oldest first", l.Pending)
	}
	row := l.Pending[0]
	if row.Project != "project-a" || row.Verb != record.VerbFeatureFinish || row.Feature != "feature-a" || row.Held ||
		row.RequestedAt != a.Binding.RequestedAt || row.PendingExpiresAt != a.Binding.PendingExpiresAt || len(row.SHA256) != 64 {
		t.Errorf("the row is %+v", row)
	}
	shown, _ := e.stand.Show(ctx, a.ID)
	if row.SHA256 != shown.Entry.PendingSHA256 {
		t.Error("the row does not carry the digest of the pending record's bytes")
	}
	if l.Count.Requests != 2 {
		t.Errorf("the count is %+v", l.Count)
	}

	if _, err := e.decide(a, false); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Minute)
	if err := e.svc.Looked(ctx); err != nil {
		t.Fatal(err)
	}
	l, err = e.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Pending) != 1 || l.Pending[0].ID != b.ID {
		t.Errorf("the pending rows are %+v", l.Pending)
	}
	if len(l.Recent) != 1 || l.Recent[0].ID != a.ID || l.Recent[0].State != record.StateDenied || l.Recent[0].Line == "" {
		t.Fatalf("the recent rows are %+v", l.Recent)
	}
	if l.Recent[0].At != record.FormatTime(start.Add(time.Minute)) {
		t.Errorf("the denial's time is %s", l.Recent[0].At)
	}
	if l.LookedAt != record.FormatTime(e.clock.Now()) {
		t.Errorf("the looked mark is %q", l.LookedAt)
	}
	// The listing never carries a record's bytes.
	enc, err := ownerwire.EncodeListing(l)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(enc, []byte("plan_text")) || bytes.Contains(enc, []byte("Makefile")) {
		t.Error("the listing carries the contents of a record")
	}
}

func TestTheListingKeepsTheNewestRecentRowsOnly(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		p := e.card(fmt.Sprintf("feature-%c", 'a'+i))
		if _, err := e.decide(p, false); err != nil {
			t.Fatal(err)
		}
		e.advance(time.Minute)
	}
	l, err := e.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Recent) != 3 || l.Recent[0].At < l.Recent[1].At || l.Recent[1].At < l.Recent[2].At {
		t.Errorf("the recent rows are %+v, want the newest first", l.Recent)
	}
}

// L4 of the wire's review: a listing that cannot fit one page even with no
// recent decisions carries the oldest pending rows that fit, and says how
// many were left out.
func TestAListingTooLargeForOnePageKeepsTheOldestPendingRows(t *testing.T) {
	var l ownerwire.Listing
	l.Rules = []ownerwire.Rule{{ID: "prune-merged", Project: "project-a", Line: "merged branches are pruned", On: true}}
	const n = 2500
	for i := 0; i < n; i++ {
		l.Pending = append(l.Pending, ownerwire.PendingItem{
			ID: fmt.Sprintf("ap-%06d", i), SHA256: record.Digest([]byte{byte(i)}), Project: "project-a",
			Verb: record.VerbFeatureFinish, Feature: "feature-a",
			RequestedAt: record.FormatTime(start.Add(time.Duration(i) * time.Second)), PendingExpiresAt: record.FormatTime(start.Add(24 * time.Hour)),
		})
	}
	for i := 0; i < 100; i++ {
		l.Recent = append(l.Recent, ownerwire.RecentItem{
			ID: fmt.Sprintf("ap-r%05d", i), Project: "project-a", Verb: record.VerbFeatureFinish, Feature: "feature-a",
			State: record.StateApplied, Line: "applied: the ref was moved", At: record.FormatTime(start),
		})
	}
	if _, err := ownerwire.EncodeListing(l); err == nil {
		t.Fatal("the test listing fits one page: make it larger")
	}
	got, err := fitListing(l)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ownerwire.EncodeListing(got)
	if err != nil || len(b) > ownerwire.MaxPageBytes {
		t.Fatalf("the cut listing does not encode: %d bytes, %v", len(b), err)
	}
	if len(got.Recent) != 0 {
		t.Errorf("%d recent rows kept beside rows that were cut", len(got.Recent))
	}
	if len(got.Pending) == 0 || len(got.Pending) >= n {
		t.Fatalf("%d pending rows kept of %d", len(got.Pending), n)
	}
	for i, p := range got.Pending {
		if p.ID != l.Pending[i].ID {
			t.Fatalf("row %d is %s: the oldest rows must be the ones kept", i, p.ID)
		}
	}
	if got.NotListed != int64(n-len(got.Pending)) {
		t.Errorf("the listing says %d rows were left out, want %d", got.NotListed, n-len(got.Pending))
	}
	// The count travels in its own field: the standing rules are the
	// rules and nothing else.
	if !reflect.DeepEqual(got.Rules, l.Rules) {
		t.Errorf("the rules are %+v, want only the standing rules %+v", got.Rules, l.Rules)
	}
	// One row more would not have fit.
	more := cut(l, l.Pending, len(got.Pending)+1)
	if _, err := ownerwire.EncodeListing(more); err == nil {
		t.Error("another row would have fit")
	}
	// A listing that fits is returned as it is.
	small := ownerwire.Listing{Pending: l.Pending[:3], Recent: l.Recent[:3], Rules: []ownerwire.Rule{}}
	if out, err := fitListing(small); err != nil || len(out.Pending) != 3 || len(out.Recent) != 3 || len(out.Rules) != 0 || out.NotListed != 0 {
		t.Errorf("a small listing was changed: %+v %v", out, err)
	}
	// Recent rows are dropped first, oldest first.
	lo, hi := 0, n
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if _, err := ownerwire.EncodeListing(ownerwire.Listing{Pending: l.Pending[:mid], Rules: []ownerwire.Rule{}}); err == nil {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	k := lo
	mid := ownerwire.Listing{Pending: l.Pending[:k], Recent: l.Recent, Rules: []ownerwire.Rule{}}
	out, err := fitListing(mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Pending) != k || len(out.Recent) >= 100 || len(out.Rules) != 0 || out.NotListed != 0 {
		t.Errorf("%d pending and %d recent rows kept of %d", len(out.Pending), len(out.Recent), k)
	}
	for i, r := range out.Recent {
		if r.ID != l.Recent[i].ID {
			t.Fatalf("recent row %d is %s", i, r.ID)
		}
	}
}

func TestShowReturnsTheRecordTheHeldMarkAndTheOutcome(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()
	p := e.card("feature-a")

	res, err := e.svc.Show(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	shown, _ := e.stand.Show(ctx, p.ID)
	if !bytes.Equal(res.Record.Bytes, shown.Pending.Bytes) || res.Record.SHA256 != shown.Pending.SHA256 {
		t.Error("the record is not the bytes the door holds, as they are")
	}
	if res.Held || res.Outcome != nil || res.Earlier != nil {
		t.Errorf("a new card shows %+v", res)
	}
	if _, err := e.decide(p, true); err != nil {
		t.Fatal(err)
	}
	res, err = e.svc.Show(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome == nil || res.Outcome.State != record.StateApplied || res.Outcome.Line == "" || res.Held {
		t.Errorf("a decided card shows %+v", res)
	}
	_, err = e.svc.Show(ctx, "ap-nothing-here")
	wantCode(t, err, record.CodeApprovalVoid)
	_, err = e.svc.Show(ctx, "not an id")
	wantCode(t, err, record.CodeApprovalVoid)
}

func TestShowNamesTheEarlierCardTheOwnerOpened(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()

	// Two cards for one verb and target, the second after the first ended.
	first := e.card("feature-a")
	if res, err := e.svc.Show(ctx, first.ID); err != nil || res.Earlier != nil {
		t.Fatalf("%+v %v", res, err)
	}
	readAt := e.clock.Now()
	if err := e.stand.Withdraw(first.ID); err != nil {
		t.Fatal(err)
	}
	e.advance(2 * time.Hour)
	second := e.card("feature-a")
	res, err := e.svc.Show(ctx, second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Earlier == nil {
		t.Fatal("no earlier card for the same verb and target that he opened two hours ago")
	}
	prev, _ := e.stand.Show(ctx, first.ID)
	if !bytes.Equal(res.Earlier.Record.Bytes, prev.Pending.Bytes) || res.Earlier.ReadAt != record.FormatTime(readAt) {
		t.Errorf("the earlier card is %+v", res.Earlier)
	}
	// The first card never has the second as its earlier one.
	if res, err := e.svc.Show(ctx, first.ID); err != nil || res.Earlier != nil {
		t.Errorf("the older card shows an earlier one: %+v %v", res.Earlier, err)
	}
	// Another target has none, and nor does one opened over 24 hours ago.
	other := e.card("feature-b")
	if res, err := e.svc.Show(ctx, other.ID); err != nil || res.Earlier != nil {
		t.Errorf("%+v %v", res.Earlier, err)
	}
	// The second card expires at the door after 24 hours.
	e.advance(25 * time.Hour)
	third := e.card("feature-a")
	if res, err := e.svc.Show(ctx, third.ID); err != nil || res.Earlier != nil {
		t.Errorf("a card opened over 24 hours ago is the earlier one: %+v %v", res.Earlier, err)
	}
}

func TestPathsAndPatchPagesCarryTheReviewDigest(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()
	sum := e.intake.summary
	sum.PathsTruncated = true
	e.intake.summary = sum
	for i := 0; i < 3000; i++ {
		e.intake.paths = append(e.intake.paths, record.PathChange{
			Path: record.Text(fmt.Sprintf("internal/some/long/folder/name/file-%05d.go", i)), Status: "M",
			OldMode: "100644", NewMode: "100644", Added: int64(i), Removed: 1, NewBytes: 1000,
		})
	}
	long := strings.Repeat("a long line of the patch ", 30000)
	e.intake.patch = []string{"diff --git a/x b/x", "+" + long, "\x1b[2J not utf-8 \xff\xfe", "", "+last"}
	p := e.card("feature-a")
	digest := p.Binding.Summary.ReviewDigest

	var all []ownerwire.PathsPage
	for n := 0; ; n++ {
		page, err := e.svc.Paths(ctx, ownerwire.PageParams{ID: p.ID, ReviewDigest: digest, Page: n})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page)
		if _, err := ownerwire.EncodePathsPage(page); err != nil {
			t.Fatal(err)
		}
		if n == page.Pages-1 {
			break
		}
	}
	if len(all) < 2 {
		t.Fatalf("%d pages for 3000 paths", len(all))
	}
	joined, err := ownerwire.JoinPaths(all)
	if err != nil || len(joined) != 3000 || string(joined[2999].Path) != "internal/some/long/folder/name/file-02999.go" {
		t.Fatalf("the pages do not join: %d %v", len(joined), err)
	}

	var patch []ownerwire.PatchPage
	for n := 0; ; n++ {
		page, err := e.svc.Patch(ctx, ownerwire.PageParams{ID: p.ID, ReviewDigest: digest, Page: n})
		if err != nil {
			t.Fatal(err)
		}
		patch = append(patch, page)
		if n == page.Pages-1 {
			break
		}
	}
	lines, err := ownerwire.JoinPatch(patch)
	if err != nil || len(lines) != 5 || lines[1] != "+"+long || lines[2] != e.intake.patch[2] {
		t.Fatalf("the patch pages do not join to the lines the intake gave: %d %v", len(lines), err)
	}

	// Another digest, a page that is not there, an id that is not a card.
	_, err = e.svc.Paths(ctx, ownerwire.PageParams{ID: p.ID, ReviewDigest: record.Digest([]byte("other")), Page: 0})
	wantCode(t, err, record.CodeReviewChanged)
	_, err = e.svc.Patch(ctx, ownerwire.PageParams{ID: p.ID, ReviewDigest: record.Digest([]byte("other")), Page: 0})
	wantCode(t, err, record.CodeReviewChanged)
	_, err = e.svc.Paths(ctx, ownerwire.PageParams{ID: p.ID, ReviewDigest: digest, Page: len(all)})
	wantCode(t, err, record.CodeRecordInvalid)
	_, err = e.svc.Patch(ctx, ownerwire.PageParams{ID: p.ID, ReviewDigest: digest, Page: len(patch)})
	wantCode(t, err, record.CodeRecordInvalid)
	_, err = e.svc.Paths(ctx, ownerwire.PageParams{ID: "ap-nothing-here", ReviewDigest: digest, Page: 0})
	wantCode(t, err, record.CodeApprovalVoid)

	// The review behind the card is not the one named: the intake now
	// computes another digest.
	e.svc.mu.Lock()
	e.svc.pages = nil
	e.svc.mu.Unlock()
	e.intake.mu.Lock()
	e.intake.digest = record.Digest([]byte("the mirror changed"))
	e.intake.mu.Unlock()
	_, err = e.svc.Paths(ctx, ownerwire.PageParams{ID: p.ID, ReviewDigest: digest, Page: 0})
	wantCode(t, err, record.CodeReviewChanged)
	_, err = e.svc.Patch(ctx, ownerwire.PageParams{ID: p.ID, ReviewDigest: digest, Page: 0})
	wantCode(t, err, record.CodeReviewChanged)
}

func TestAPathListThatFitsTheRecordIsPagedFromTheRecord(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.intake.mu.Lock()
	e.intake.digest = "not asked for"
	e.intake.mu.Unlock()
	page, err := e.svc.Paths(context.Background(), ownerwire.PageParams{ID: p.ID, ReviewDigest: p.Binding.Summary.ReviewDigest, Page: 0})
	if err != nil {
		t.Fatal(err)
	}
	if page.Pages != 1 || len(page.Paths) != len(p.Binding.Summary.Paths) {
		t.Errorf("%+v", page)
	}
}

func TestBeatAndLookedAreRefusedForAnIllFormedSurface(t *testing.T) {
	e := newEnv(t)
	e.start()
	bad := src
	bad.Session = "short"
	wantCode(t, e.svc.Beat(bad), record.CodeRecordInvalid)
	_, err := e.svc.Decide(context.Background(), bad, ownerwire.DecisionFrame{})
	wantCode(t, err, record.CodeDecisionInvalid)
}
