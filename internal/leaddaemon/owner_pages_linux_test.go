//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
)

const ownerReviewDigest = "0123456789012345678901234567890123456789012345678901234567890123"

// ownerBigPaths is a path list far larger than one response line: 6,000
// paths of about 400 bytes each.
func ownerBigPaths() []record.PathChange {
	paths := make([]record.PathChange, 6000)
	for i := range paths {
		paths[i] = record.PathChange{Path: record.Text(fmt.Sprintf("dir/%05d/%s", i, strings.Repeat("p", 380))), Status: "M", OldMode: "100644", NewMode: "100644"}
	}
	return paths
}

// ownerBigPatch is a patch of about 2.5 MiB whose longest line alone is larger
// than a page.
func ownerBigPatch() []string {
	lines := make([]string, 0, 16000)
	for i := 0; len(lines) < 15000; i++ {
		lines = append(lines, fmt.Sprintf("+line %d %s", i, strings.Repeat("x", 90)))
	}
	lines = append(lines[:7000], append([]string{"+" + strings.Repeat("L", 700<<10)}, lines[7000:]...)...)
	// Raw bytes the pager must be the one to escape.
	lines = append(lines, "+\x1b[31m red \x1b[0m", "+‮ reversed", "+")
	return lines
}

// TestOwnerPagedReadsAreServedUnderTheLineCap: a path list and a patch that
// each need many response lines are read through the real server, page by
// page; no page is refused as too large for a line (the client refuses to read
// one), every page is within the page cap as written, and the pages put back
// together are what the service had, byte for byte, with a patch line that was
// larger than one page rejoined from its pieces.
func TestOwnerPagedReadsAreServedUnderTheLineCap(t *testing.T) {
	r := newOwnerRig(t)
	c := r.dial()
	paths, patch := ownerBigPaths(), ownerBigPatch()
	pathPages, err := ownerwire.SplitPaths("ap-one", ownerReviewDigest, paths)
	if err != nil {
		t.Fatal(err)
	}
	patchPages, err := ownerwire.SplitPatch("ap-one", ownerReviewDigest, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(pathPages) < 4 || len(patchPages) < 8 {
		t.Fatalf("the sample is too small to prove anything: %d path pages, %d patch pages", len(pathPages), len(patchPages))
	}
	r.svc.paths = func(p ownerwire.PageParams) (ownerwire.PathsPage, error) { return pathPages[p.Page], nil }
	r.svc.patch = func(p ownerwire.PageParams) (ownerwire.PatchPage, error) { return patchPages[p.Page], nil }

	var gotPaths []ownerwire.PathsPage
	for n := 0; n < len(pathPages); n++ {
		var page ownerwire.PathsPage
		if err := c.Call(ownerCtx(t), ownerwire.OpPaths, ownerwire.PageParams{ID: "ap-one", ReviewDigest: ownerReviewDigest, Page: n}, &page); err != nil {
			t.Fatalf("paths page %d: %v", n, err)
		}
		gotPaths = append(gotPaths, page)
	}
	back, err := ownerwire.JoinPaths(gotPaths)
	if err != nil || !slices.Equal(back, paths) {
		t.Fatalf("the paths do not come back whole: %v", err)
	}

	var gotPatch []ownerwire.PatchPage
	pieces := 0
	for n := 0; n < len(patchPages); n++ {
		var page ownerwire.PatchPage
		if err := c.Call(ownerCtx(t), ownerwire.OpPatch, ownerwire.PageParams{ID: "ap-one", ReviewDigest: ownerReviewDigest, Page: n}, &page); err != nil {
			t.Fatalf("patch page %d: %v", n, err)
		}
		for _, l := range page.Lines {
			if l.Cont {
				pieces++
			}
		}
		gotPatch = append(gotPatch, page)
		if b, _ := json.Marshal(page); len(b) > ownerwire.MaxPageBytes || len(b)+512 > clientapi.MaxResponseBytes {
			t.Fatalf("patch page %d is %d bytes", n, len(b))
		}
	}
	if pieces == 0 {
		t.Fatal("no line was split: the sample does not hold a line larger than a page")
	}
	lines, err := ownerwire.JoinPatch(gotPatch)
	if err != nil || !slices.Equal(lines, patch) {
		t.Fatalf("the patch does not come back whole: %v", err)
	}
	var total int
	for _, l := range lines {
		total += len(l)
	}
	if total < 2<<20 {
		t.Fatalf("the patch is %d bytes, under the 2 MiB the case is about", total)
	}
}

// TestOwnerAPageReadWhoseReviewChangedIsRefusedInTheMiddle: the service's
// review-changed refusal in the middle of a read reaches the console with its
// code and its line, and the read can be ended there.
func TestOwnerAPageReadWhoseReviewChangedIsRefusedInTheMiddle(t *testing.T) {
	r := newOwnerRig(t)
	c := r.dial()
	pages, err := ownerwire.SplitPatch("ap-one", ownerReviewDigest, ownerBigPatch())
	if err != nil {
		t.Fatal(err)
	}
	r.svc.patch = func(p ownerwire.PageParams) (ownerwire.PatchPage, error) {
		if p.Page >= 3 {
			return ownerwire.PatchPage{}, &record.Error{Code: record.CodeReviewChanged, Detail: "the review behind this card is not the one named"}
		}
		return pages[p.Page], nil
	}
	for n := 0; n < 3; n++ {
		var page ownerwire.PatchPage
		if err := c.Call(ownerCtx(t), ownerwire.OpPatch, ownerwire.PageParams{ID: "ap-one", ReviewDigest: ownerReviewDigest, Page: n}, &page); err != nil {
			t.Fatalf("page %d: %v", n, err)
		}
	}
	err = c.Call(ownerCtx(t), ownerwire.OpPatch, ownerwire.PageParams{ID: "ap-one", ReviewDigest: ownerReviewDigest, Page: 3}, nil)
	ce := wantOwnerCode(t, err, "review-changed")
	if ce.Message != "the review behind this card is not the one named" {
		t.Fatalf("message %q", ce.Message)
	}
	// The connection is still good.
	if err := c.Call(ownerCtx(t), ownerwire.OpLooked, nil, nil); err != nil {
		t.Fatal(err)
	}
}
