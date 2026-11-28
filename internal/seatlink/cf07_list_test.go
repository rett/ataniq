package seatlink

import (
	"testing"

	"ataniq/internal/approval/card"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
)

// TestCF07SeatList: a listing that carries the binding digest of a row
// decodes through the seat's strict reader and maps to a row with the
// digest and its four words; a row without it maps to none.
func TestCF07SeatList(t *testing.T) {
	t.Run("StrictReaderAcceptsField", func(t *testing.T) {
		const day = "2026-11-20T14:06:40Z"
		digest := record.Digest([]byte("the pending binding"))
		item := func(id, binding string) ownerwire.PendingItem {
			return ownerwire.PendingItem{ID: id, SHA256: record.Digest([]byte(id)), BindingSHA256: binding,
				Project: "project-a", Verb: record.VerbBranchPrune, RequestedAt: day, PendingExpiresAt: "2026-11-21T14:06:40Z"}
		}
		raw, err := ownerwire.EncodeListing(ownerwire.Listing{
			Pending: []ownerwire.PendingItem{item("ap-20261120-0007", digest), item("ap-20261120-0008", "")},
			Recent:  []ownerwire.RecentItem{},
			Rules:   []ownerwire.Rule{},
		})
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := ownerwire.DecodeListing(raw)
		if err != nil {
			t.Fatalf("the strict reader refused a listing with binding_sha256: %v", err)
		}
		l, err := listingOf(decoded)
		if err != nil {
			t.Fatalf("listing: %v", err)
		}
		if len(l.Pending) != 2 {
			t.Fatalf("pending %+v", l.Pending)
		}
		want := card.BindingTag(digest)
		if want == "" {
			t.Fatal("the digest has no tag")
		}
		if got := l.Pending[0]; got.BindingSHA256 != digest || got.BindingTag != want {
			t.Errorf("row with digest: %+v, want tag %q", got, want)
		}
		if got := l.Pending[1]; got.BindingSHA256 != "" || got.BindingTag != "" {
			t.Errorf("row without digest: %+v", got)
		}
	})
}
