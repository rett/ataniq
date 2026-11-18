package card

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"ataniq/internal/approval/record"
)

// shownByHand computes the digest as the comment of ShownDigest states it,
// with no code of the package.
func shownByHand(binding, noteDigest, earlier string, held byte) string {
	var data []byte
	for _, part := range []string{binding, noteDigest, earlier, string([]byte{held})} {
		n := len(part)
		data = append(data, 0, 0, 0, 0, 0, 0, 0, byte(n))
		data = append(data, part...)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestShownDigest(t *testing.T) {
	first := validPending(t)
	p := seal(t, laterPending())
	earlierBytes, err := record.EncodePending(first)
	if err != nil {
		t.Fatal(err)
	}
	earlierDigest := record.Digest(earlierBytes)
	noteDigest := record.Digest([]byte(p.Note))

	plain := mustCard(t, p, Inputs{})
	held := mustCard(t, p, Inputs{Held: true})
	differs := mustCard(t, p, Inputs{Earlier: &Earlier{Record: first, ReadAt: asked}})
	both := mustCard(t, p, Inputs{Held: true, Earlier: &Earlier{Record: first, ReadAt: asked}})

	for name, tc := range map[string]struct {
		c    Card
		want string
	}{
		"plain":            {plain, shownByHand(p.BindingSHA256, noteDigest, "", 0)},
		"held":             {held, shownByHand(p.BindingSHA256, noteDigest, "", 1)},
		"differs":          {differs, shownByHand(p.BindingSHA256, noteDigest, earlierDigest, 0)},
		"held and differs": {both, shownByHand(p.BindingSHA256, noteDigest, earlierDigest, 1)},
	} {
		got := tc.c.ShownDigest()
		if got != tc.want {
			t.Errorf("%s: got %s, want %s", name, got, tc.want)
		}
		if len(got) != 64 || got != hex.EncodeToString(mustHex(t, got)) {
			t.Errorf("%s: %q is not 64 lower-case hexadecimal digits", name, got)
		}
	}
	if differs.Differs.EarlierSHA256 != earlierDigest {
		t.Errorf("the block's digest of the earlier record is %s, want %s", differs.Differs.EarlierSHA256, earlierDigest)
	}

	// One fixed value, so that a change of the form is seen as one.
	fixed := shownByHand(
		"0000000000000000000000000000000000000000000000000000000000000000",
		"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", "", 0)
	c := Card{BindingSHA256: "0000000000000000000000000000000000000000000000000000000000000000"}
	if got := c.ShownDigest(); got != fixed {
		t.Errorf("a card with an empty note: got %s, want %s", got, fixed)
	}
	if record.Digest(nil) != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Error("the digest of no bytes is not the known one")
	}

	// Each of the four parts moves the digest, and the four cards above
	// have four digests.
	seen := map[string]string{}
	add := func(name string, c Card) {
		d := c.ShownDigest()
		if other, dup := seen[d]; dup {
			t.Errorf("%s and %s have the same digest", name, other)
		}
		seen[d] = name
	}
	add("plain", plain)
	add("held", held)
	add("differs", differs)
	add("held and differs", both)

	q := laterPending()
	q.Note = "another note"
	add("another note", mustCard(t, seal(t, q), Inputs{}))
	q = laterPending()
	q.Binding.Summary.Added++
	add("another binding", mustCard(t, seal(t, q), Inputs{}))
	otherEarlier := rawPending()
	otherEarlier.Note = "the earlier card had another note"
	add("another earlier record", mustCard(t, p, Inputs{Earlier: &Earlier{Record: seal(t, otherEarlier), ReadAt: asked}}))

	// Two cards for the same feature name have two digests.
	add("the earlier card of the same feature", mustCard(t, first, Inputs{}))

	// What came after the decision is not part of what was shown, and
	// neither is when the earlier card was read.
	for name, c := range map[string]Card{
		"a state":       mustCard(t, p, Inputs{State: record.StateApplied}),
		"a line":        mustCard(t, p, Inputs{Line: "applied"}),
		"verdicts":      mustCard(t, p, Inputs{State: record.StateApplied, Verdicts: &VerdictBlock{}}),
		"state pending": mustCard(t, p, Inputs{State: record.StatePending}),
	} {
		if c.ShownDigest() != plain.ShownDigest() {
			t.Errorf("%s moves the digest of what was shown", name)
		}
	}
	if c := mustCard(t, p, Inputs{Earlier: &Earlier{Record: first, ReadAt: asked.Add(time5min)}}); c.ShownDigest() != differs.ShownDigest() {
		t.Error("the time the earlier card was read moves the digest")
	}

	// The lengths keep the parts apart: a note digest cannot slide into
	// the place of the earlier record's digest.
	if shownByHand("a", "bc", "", 0) == shownByHand("a", "b", "c", 0) {
		t.Error("two splits of the same bytes have one digest")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("%q is not hexadecimal: %v", s, err)
	}
	return b
}
