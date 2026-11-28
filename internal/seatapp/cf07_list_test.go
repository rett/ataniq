package seatapp

import (
	"strings"
	"testing"
)

// TestCF07SeatList: a pending row of the list shows the four words of its
// binding tag when the listing carries one, and says to open the card when
// it does not.
func TestCF07SeatList(t *testing.T) {
	const tag = "marsh candle koala lotus"
	const digest = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"
	const fallback = "open for binding tag"

	t.Run("RowShowsTagWhenPresent", func(t *testing.T) {
		r := newRig(t)
		r.daemon.listing.Pending[0].BindingSHA256 = digest
		r.daemon.listing.Pending[0].BindingTag = tag
		body := r.getWith("/cards/", r.login(t)).Body.String()
		if !strings.Contains(body, tag) {
			t.Errorf("the list does not show %q: %s", tag, body)
		}
		if strings.Contains(strings.ToLower(body), fallback) {
			t.Errorf("a row with a tag also says %q", fallback)
		}
	})

	t.Run("RowFallbackWhenAbsent", func(t *testing.T) {
		r := newRig(t)
		body := r.getWith("/cards/", r.login(t)).Body.String()
		if n := strings.Count(strings.ToLower(body), fallback); n != 1 {
			t.Errorf("the list says %q %d times, want once: %s", fallback, n, body)
		}
		if strings.Contains(body, tag) {
			t.Errorf("a row without a tag shows words")
		}
	})
}
