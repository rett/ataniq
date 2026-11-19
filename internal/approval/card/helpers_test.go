package card

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/record"
)

// Three object ids of the sample card: the target's tip (D), the feature's
// tip (F) and the merge commit (M).
var (
	idD = strings.Repeat("93c8d68b", 5)
	idF = strings.Repeat("8281895a", 5)
	idM = strings.Repeat("0194dafc", 5)
	// idF2 and idM2 are a later feature tip and the merge built from it.
	idF2 = strings.Repeat("5e11f00d", 5)
	idM2 = strings.Repeat("7a7a7a7a", 5)
)

const samplePlan = "merge feature/sample-feature (F 8281895) into develop (D 93c8d68) --no-ff\n" +
	"result M 0194daf   built by the host\n"

// seal returns p with its two digests computed, as a host does before it
// writes a pending record. A binding that cannot be written has no digest;
// the record then keeps the one it had, and New refuses it.
func seal(t testing.TB, p record.Pending) record.Pending {
	t.Helper()
	p.Binding.PlanSHA256 = record.PlanDigest(string(p.PlanText))
	if digest, err := record.BindingDigest(p.Binding); err == nil {
		p.BindingSHA256 = digest
	}
	return p
}

// rawPending returns a pending record for a feature finish with two changed
// paths, one of them flagged. Its digests are not computed yet, so a test
// can change it first and then seal it.
func rawPending() record.Pending {
	return record.Pending{
		Schema:        record.SchemaPending,
		ID:            "ap-20261120-0007",
		Planner:       "planner-a",
		RequestID:     "rq-20261120-7c1e",
		RequestSHA256: record.Digest([]byte("the request")),
		Binding: record.Binding{
			Project: "project-a",
			Subject: record.Subject{
				Kind: record.SubjectSeat, RoleName: "role-a", Epoch: 7, SeatRev: 12,
				Holder: &record.Holder{Kind: "lead", DisplayID: "role-a@e7", Runtime: "runtime-a"},
			},
			Verb:      record.VerbFeatureFinish,
			Argv:      []string{"sample-feature"},
			Class:     record.ClassRefMove,
			PlanToken: "h-3fa9c2e1",
			Effects:   []record.Effect{{Kind: record.EffectRef, Ref: "refs/heads/develop", Old: idD, New: idM}},
			Merge:     &record.Merge{Commit: idM, Parents: []string{idD, idF}, BuiltBy: record.BuiltByHost},
			Summary: &record.Summary{
				ReviewDigest: record.Digest([]byte("the review")),
				Base:         idD,
				Tip:          idM,
				Files:        2,
				Added:        305,
				Removed:      1,
				NewBytes:     9240,
				Paths: []record.PathChange{
					{Path: "lib/seat_read.py", Status: "A", OldMode: "000000", NewMode: "100644", Added: 301, NewBytes: 9120},
					{Path: "Makefile", Status: "M", OldMode: "100644", NewMode: "100644", Added: 4, Removed: 1, NewBytes: 120},
				},
				Flagged:     []record.Flag{{Kind: record.FlagExecConfig, Path: "Makefile", Pattern: "Makefile"}},
				Binary:      []record.Text{},
				Symlinks:    []record.Text{},
				ModeChanges: []record.Text{},
				Submodules:  []record.Text{},
				ExecConfigSet: record.ExecConfigSet{
					Matcher: "execconfig/1", SHA256: record.Digest([]byte("the rules")), Additions: 0,
				},
			},
			Evidence: []record.Evidence{
				{Kind: record.EvidenceSuite, RecordID: "v-0412", RecordSHA256: record.Digest([]byte("the suite"))},
			},
			RequestedAt:      "2026-11-20T14:02:04Z",
			PendingExpiresAt: "2026-11-21T14:02:04Z",
		},
		PlanText: samplePlan,
		Note:     "Wave 1 task merged.",
	}
}

// validPending is rawPending, sealed.
func validPending(t testing.TB) record.Pending {
	t.Helper()
	return seal(t, rawPending())
}

// laterPending is a second request for the same feature and target as
// rawPending, after the feature moved: another id, another feature tip and
// merge, one path gone and one new.
func laterPending() record.Pending {
	p := rawPending()
	p.ID = "ap-20261120-0009"
	p.RequestID = "rq-20261120-9d2f"
	b := &p.Binding
	b.Effects[0].New = idM2
	b.Merge = &record.Merge{Commit: idM2, Parents: []string{idD, idF2}, BuiltBy: record.BuiltByHost}
	b.Summary.Tip = idM2
	b.Summary.Paths = []record.PathChange{
		{Path: "lib/seat_read.py", Status: "A", OldMode: "000000", NewMode: "100644", Added: 310, NewBytes: 9300},
		{Path: "docs/seat.md", Status: "A", OldMode: "000000", NewMode: "100644", Added: 12, NewBytes: 400},
	}
	b.Summary.Flagged = []record.Flag{}
	b.RequestedAt = "2026-11-20T15:10:00Z"
	b.PendingExpiresAt = "2026-11-21T15:10:00Z"
	return p
}

// prunePending returns a sealed pending record for a prune of two branches.
func prunePending(t testing.TB) record.Pending {
	t.Helper()
	p := rawPending()
	b := &p.Binding
	zeros := strings.Repeat("0", 40)
	b.Verb = record.VerbBranchPrune
	b.Argv = []string{}
	b.Effects = []record.Effect{
		{Kind: record.EffectRef, Ref: "refs/heads/feature/one", Old: idF, New: zeros,
			MergedInto: &record.MergedInto{Ref: "refs/heads/develop", Tip: idM}},
		{Kind: record.EffectRef, Ref: "refs/heads/feature/two", Old: idF2, New: zeros,
			MergedInto: &record.MergedInto{Ref: "refs/heads/develop", Tip: idM}},
	}
	b.Merge = nil
	b.Summary = nil
	b.Evidence = []record.Evidence{}
	p.PlanText = "delete 2 merged branches\n"
	return seal(t, p)
}

// truncatedPending returns a sealed pending record whose summary counts
// 5,000 paths and holds as many of them as fit under the record's cap, with
// the truncation flag set. It also returns how many paths the record holds.
func truncatedPending(t testing.TB) (record.Pending, int) {
	t.Helper()
	raw := rawPending()
	s := raw.Binding.Summary
	s.Files = 5000
	s.PathsTruncated = true
	s.Flagged = []record.Flag{}
	s.Paths = nil
	for i := 0; i < 5000; i++ {
		s.Paths = append(s.Paths, record.PathChange{
			Path:   record.Text(fmt.Sprintf("pkg/dir-%02d/file-%04d.go", i%40, i)),
			Status: "M", OldMode: "100644", NewMode: "100644", Added: 3, Removed: 1, NewBytes: 60,
		})
		if i%50 == 49 {
			if _, err := record.EncodePending(seal(t, raw)); err != nil {
				s.Paths = s.Paths[:len(s.Paths)-50]
				break
			}
		}
	}
	held := len(s.Paths)
	if held < 100 || held >= 5000 {
		t.Fatalf("the record holds %d paths, want a part of the 5,000", held)
	}
	return seal(t, raw), held
}

// mustCard builds the card of p and fails the test when New refuses.
func mustCard(t testing.TB, p record.Pending, in Inputs) Card {
	t.Helper()
	c, err := New(p, in)
	if err != nil {
		t.Fatalf("New refused: %v", err)
	}
	return c
}

// wantCode checks that err is a refusal with the code want.
func wantCode(t testing.TB, what string, err error, want record.Code) {
	t.Helper()
	var e *record.Error
	if !errors.As(err, &e) {
		t.Errorf("%s: got %v, want a refusal with the code %s", what, err, want)
		return
	}
	if e.Code != want {
		t.Errorf("%s: refused %s (%s), want %s", what, e.Code, e.Detail, want)
	}
}

// time5min is a stretch of time long enough for every rule that waits.
const time5min = 5 * time.Minute

// asked and expires are the two times of the sample card.
var (
	asked   = time.Date(2026, 11, 20, 14, 2, 4, 0, time.UTC)
	expires = time.Date(2026, 11, 21, 14, 2, 4, 0, time.UTC)
)
