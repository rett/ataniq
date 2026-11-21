//go:build linux && ataniq_ownerchannel

package hostadapter

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

// buildMerge builds and pins a merge by hand, as the service does, and returns
// it with the review input for it.
func (r *rig) buildMerge(requestID string) (ownerchannel.BuiltMerge, ownerchannel.ReviewInput) {
	r.t.Helper()
	key := strings.Repeat("0", 31) + string(rune('a'+r.n%6))
	r.n++
	m, err := r.o.Host.Build(context.Background(), ownerchannel.MergeInput{
		Project: "project-a", Feature: "x", Target: "develop", TargetTip: r.scene.target, FeatureTip: r.scene.feature,
		RequestID: requestID, Collected: start, Key: key,
	})
	if err != nil {
		r.t.Fatalf("Build: %v", err)
	}
	return m, ownerchannel.ReviewInput{Project: "project-a", Base: r.scene.target, Merge: m.Commit}
}

// The intake's ErrLimit, for a change with more content to read than the
// summary's scan takes (64 MiB), is a refusal with a code of its own and no
// card; the review of a smaller change is not.
func TestAChangeLargerThanTheSummaryReadsIsRefusedRecordTooLarge(t *testing.T) {
	r := newRig(t, func(l *lab) {
		// 65 distinct blobs of 1 MiB each: text, under the cap that makes a
		// blob binary, and 65 MiB in all.
		buf := make([]byte, 1<<20)
		for i := 0; i < 65; i++ {
			if _, err := rand.Read(buf); err != nil {
				t.Fatal(err)
			}
			for j := range buf {
				buf[j] = 'a' + buf[j]%26
			}
			buf[len(buf)-1] = '\n'
			l.put(fmt.Sprintf("big/f%02d.txt", i), string(buf))
		}
	})
	_, in := r.buildMerge("rq-limit-01")
	_, err := r.o.Host.Review(context.Background(), in)
	if c, ok := record.CodeOf(err); !ok || c != record.CodeRecordTooLarge {
		t.Fatalf("Review of a change over the scan's limit: %v, want record-too-large", err)
	}
	// Through the service the request is refused and no card is made.
	id := r.ask()
	st, ok := r.o.Service.Status(id)
	if !ok || st.State != ownerchannel.RequestRefused || st.PendingID != "" {
		t.Errorf("status %+v, want refused without a card", st)
	}
	// The lead's reply carries the adapter's own code, not a generic one.
	if rp := r.readReply(id); rp.State != "" || rp.Code != string(record.CodeRecordTooLarge) {
		t.Errorf("reply %+v, want record-too-large", rp)
	}
	// The merge built by hand stays pinned; the service's own was unpinned
	// with the refusal, so one pin is left.
	pins := 0
	for _, line := range strings.Split(r.lab.refs(), "\n") {
		if strings.HasPrefix(line, "refs/ataniq/pending/") {
			pins++
		}
	}
	if pins != 1 || r.pinOfMerge(in.Merge) == "" {
		t.Errorf("%d pins are left, the hand-built one among them: %v", pins, r.pinOfMerge(in.Merge) != "")
	}
}

// A path the matcher cannot read safely (an odd path) is carried to the card
// as a flag of its own kind: the request is not refused silently, the card
// shows the flag, and an approval that does not sign it off is refused.
func TestAnOddPathMakesACardWithTheFlagAndNeedsItsSignOff(t *testing.T) {
	r := newRig(t, func(l *lab) { l.put("feature.txt", "one\n"); l.put("a:stream", "x\n") })
	_, in := r.buildMerge("rq-odd-01")
	sum, err := r.o.Host.Review(context.Background(), in)
	if err != nil {
		t.Fatalf("Review with an odd path: %v", err)
	}
	var odd *record.Flag
	for i, f := range sum.Flagged {
		if f.Kind == record.FlagOddPath {
			odd = &sum.Flagged[i]
		}
	}
	if odd == nil || string(odd.Path) != "a:stream" {
		t.Fatalf("the summary's flags %+v lack the odd path", sum.Flagged)
	}
	// Through the service a card is made, with the flag.
	p := r.card()
	found := false
	for _, f := range p.Binding.Summary.Flagged {
		found = found || (f.Kind == record.FlagOddPath && string(f.Path) == "a:stream")
	}
	if !found {
		t.Fatalf("the card's flags %+v lack the odd path", p.Binding.Summary.Flagged)
	}
	// An approval with no sign-off for it is refused, and the target stays.
	f := frame(p, true)
	f.SignedOff = []record.SignOff{}
	if err := r.o.Service.Beat(src); err != nil {
		t.Fatal(err)
	}
	if _, err := r.o.Service.Decide(context.Background(), src, f); err == nil {
		t.Errorf("an approval without the odd path's sign-off was accepted")
	}
	if got := r.lab.ref(targetBranch); got != r.scene.target {
		t.Errorf("the target moved to %s", got)
	}
	// With the sign-off it is applied.
	res, err := r.decide(p, true)
	if err != nil || res.State != record.StateApplied {
		t.Fatalf("the approval with the sign-off: %+v, %v", res, err)
	}
	if got := r.lab.ref(targetBranch); got != p.Binding.Merge.Commit {
		t.Errorf("the target is at %s, want the merge", got)
	}
}

// A change whose flagged paths and lists do not fit in a card is refused
// record-too-large: they are never cut, since a path that needs a sign-off
// cannot be left out of the list that carries them.
func TestListsThatDoNotFitInACardAreRefused(t *testing.T) {
	r := newRig(t, func(l *lab) {
		for i := 0; i < 400; i++ {
			l.put(fmt.Sprintf("scripts/%s/%03d.sh", strings.Repeat("d", 40), i), "x\n")
			l.putMode("120000", fmt.Sprintf("links/%s/l%03d", strings.Repeat("e", 40), i), "target")
		}
	})
	_, in := r.buildMerge("rq-lists-01")
	_, err := r.o.Host.Review(context.Background(), in)
	if c, ok := record.CodeOf(err); !ok || c != record.CodeRecordTooLarge {
		t.Fatalf("Review of a change with long lists: %v, want record-too-large", err)
	}
}

// A list longer than the card holds is cut and marked, and the card reads the
// rest in pages through the service; a patch longer than one page, with a
// line longer than a page, is read in pages and rejoined byte for byte, and a
// page whose review is not the card's is refused review-changed.
func TestALongListAndALongPatchAreReadInPagesThroughTheService(t *testing.T) {
	long := strings.Repeat("a", 300_000)
	r := newRig(t, func(l *lab) {
		for i := 0; i < 300; i++ {
			l.put(fmt.Sprintf("dir/file-%03d.txt", i), "x\n")
		}
		l.put("long.txt", long+"\n")
		l.put("esc.txt", "red\x1b[31mtext\rtail\tend\n")
	})
	p := r.card()
	sum := p.Binding.Summary
	if !sum.PathsTruncated || len(sum.Paths) >= 302 || sum.Files != 302 {
		t.Fatalf("the summary holds %d of %d paths, truncated %v", len(sum.Paths), sum.Files, sum.PathsTruncated)
	}
	ctx := context.Background()
	var pages []ownerwire.PathsPage
	for n := 0; ; n++ {
		pg, err := r.o.Service.Paths(ctx, ownerwire.PageParams{ID: p.ID, ReviewDigest: sum.ReviewDigest, Page: n})
		if err != nil {
			t.Fatalf("Paths page %d: %v", n, err)
		}
		pages = append(pages, pg)
		if n+1 >= pg.Pages {
			break
		}
	}
	all, err := ownerwire.JoinPaths(pages)
	if err != nil || len(all) != 302 {
		t.Fatalf("the pages hold %d paths: %v", len(all), err)
	}
	if string(all[0].Path) != "dir/file-000.txt" || string(all[len(all)-1].Path) != "long.txt" {
		t.Errorf("paths run from %q to %q", all[0].Path, all[len(all)-1].Path)
	}

	var ppages []ownerwire.PatchPage
	for n := 0; ; n++ {
		pg, err := r.o.Service.Patch(ctx, ownerwire.PageParams{ID: p.ID, ReviewDigest: sum.ReviewDigest, Page: n})
		if err != nil {
			t.Fatalf("Patch page %d: %v", n, err)
		}
		ppages = append(ppages, pg)
		if n+1 >= pg.Pages {
			break
		}
	}
	if len(ppages) < 2 {
		t.Fatalf("a patch with a line of 300000 bytes is one page")
	}
	lines, err := ownerwire.JoinPatch(ppages)
	if err != nil {
		t.Fatal(err)
	}
	var sawLong, sawEsc bool
	for _, l := range lines {
		switch {
		case l == "+"+long:
			sawLong = true
		case l == "+red\x1b[31mtext\rtail\tend":
			// The service gets the raw bytes: the escaping the intake wrote
			// was turned back before the lines were paged.
			sawEsc = true
		}
	}
	if !sawLong || !sawEsc {
		t.Errorf("rejoined patch: long line %v, raw escape line %v, %d lines", sawLong, sawEsc, len(lines))
	}

	// Another digest is another review.
	_, err = r.o.Service.Paths(ctx, ownerwire.PageParams{ID: p.ID, ReviewDigest: strings.Repeat("0", 64), Page: 0})
	wantCode(t, err, record.CodeReviewChanged)
	_, err = r.o.Service.Patch(ctx, ownerwire.PageParams{ID: p.ID, ReviewDigest: strings.Repeat("0", 64), Page: 0})
	wantCode(t, err, record.CodeReviewChanged)
}

func wantCode(t *testing.T, err error, want record.Code) {
	t.Helper()
	c, ok := record.CodeOf(err)
	if !ok || c != want {
		t.Fatalf("the error is %v, want a refusal with %s", err, want)
	}
}

// The intake's own reads are the escaped ones: the adapter's patch has no
// header line and no raw control byte, and the review it reads is of the pin;
// when the target has moved since the card, a page is review-changed.
func TestThePatchIsTheIntakesEscapedPatchWithoutItsHeaderAndATargetThatMovedChangesTheReview(t *testing.T) {
	r := newRig(t, func(l *lab) { l.put("esc.txt", "red\x1b[31mtext\rtail\tend\n") })
	m, in := r.buildMerge("rq-patch-01")
	ref := ownerchannel.ReviewRef{Project: in.Project, Base: in.Base, Merge: in.Merge}
	rp, err := r.o.Host.Patch(context.Background(), ref)
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if len(rp.Lines) == 0 || !strings.HasPrefix(rp.Lines[0], "diff --git ") {
		t.Fatalf("lines %q", rp.Lines)
	}
	joined := strings.Join(rp.Lines, "\n")
	if strings.Contains(joined, "ataniq intake diff") || strings.ContainsAny(joined, "\x1b\r") {
		t.Errorf("the patch holds the header or a raw control byte: %q", joined)
	}
	if !strings.Contains(joined, `+red\x1b[31mtext\rtail`+"\tend") {
		t.Errorf("the escaped line is not in the patch: %q", joined)
	}
	rev := r.reviewOf(r.pinOfMerge(m.Commit))
	if rp.ReviewDigest != rev.Digest() {
		t.Errorf("the patch's digest %s is not the review's %s", rp.ReviewDigest, rev.Digest())
	}
	paths, err := r.o.Host.Paths(context.Background(), ref)
	if err != nil || paths.ReviewDigest != rev.Digest() || len(paths.Paths) != 1 || string(paths.Paths[0].Path) != "esc.txt" {
		t.Errorf("Paths: %+v, %v", paths, err)
	}
	// The target moves.
	r.lab.load(r.scene.target)
	r.lab.put("other.txt", "x\n")
	r.lab.setRef(targetBranch, r.lab.commit("another writer\n", r.scene.target))
	if _, err := r.o.Host.Patch(context.Background(), ref); err == nil {
		t.Errorf("a patch of a review whose target moved was read")
	} else {
		wantCode(t, err, record.CodeReviewChanged)
	}
	if _, err := r.o.Host.Paths(context.Background(), ref); err == nil {
		t.Errorf("paths of a review whose target moved were read")
	} else {
		wantCode(t, err, record.CodeReviewChanged)
	}
	if _, err := r.o.Host.Review(context.Background(), in); err == nil {
		t.Errorf("a review of a merge whose target moved was made")
	} else {
		wantCode(t, err, record.CodeBindingMismatch)
	}
	// A merge with no pin has no review.
	if _, err := r.o.Host.Patch(context.Background(), ownerchannel.ReviewRef{Project: in.Project, Base: in.Base, Merge: r.scene.feature}); err == nil {
		t.Errorf("a patch of a commit with no pin was read")
	}
}
