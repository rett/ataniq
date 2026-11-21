package execconfig

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestDigestIsStableAndDefined(t *testing.T) {
	// The encoding is a contract: the version, then each rule's pattern and
	// source, each string behind its length as an unsigned varint. Written
	// out here by hand for a small set, so that a change of the encoding
	// fails this test.
	s, err := Set{}.With([]string{"/a", "b/c"})
	if err != nil {
		t.Fatal(err)
	}
	var want []byte
	str := func(v string) {
		if len(v) > 127 {
			t.Fatal("test strings are short")
		}
		want = append(want, byte(len(v)))
		want = append(want, v...)
	}
	str("execconfig/1")
	str("/a")
	str("project")
	str("b/c")
	str("project")
	sum := sha256.Sum256(want)
	if got := Digest(s); got != hex.EncodeToString(sum[:]) {
		t.Errorf("Digest = %s, want %s", got, hex.EncodeToString(sum[:]))
	}
	empty := sha256.Sum256(append([]byte{12}, "execconfig/1"...))
	if got := Digest(Set{}); got != hex.EncodeToString(empty[:]) {
		t.Errorf("Digest of the empty set = %s", got)
	}
	if Digest(Default()) != Digest(Default()) {
		t.Errorf("Digest of the same set differs")
	}
	if MatcherVersion != "execconfig/1" {
		t.Errorf("MatcherVersion = %q", MatcherVersion)
	}
}

// TestDigestFollowsMatchingOrder: the digest changes when a rule is added,
// removed, moved or reworded, and does not when only the reason differs.
func TestDigestFollowsMatchingOrder(t *testing.T) {
	base := Default()
	d0 := Digest(base)
	added, err := base.With([]string{"extra"})
	if err != nil {
		t.Fatal(err)
	}
	if Digest(added) == d0 {
		t.Errorf("an added rule leaves the digest")
	}
	again, _ := base.With([]string{"extra"})
	if Digest(again) != Digest(added) {
		t.Errorf("the same addition gives another digest")
	}
	other, _ := base.With([]string{"extra2"})
	if Digest(other) == Digest(added) {
		t.Errorf("another pattern gives the same digest")
	}
	two, _ := base.With([]string{"x1", "x2"})
	swapped, _ := base.With([]string{"x2", "x1"})
	if Digest(two) == Digest(swapped) {
		t.Errorf("the order of the rules is not in the digest")
	}
	// Moving a built-in rule changes it too.
	moved := base
	moved.rules = append([]Rule(nil), base.rules...)
	moved.rules[0], moved.rules[1] = moved.rules[1], moved.rules[0]
	if Digest(moved) == d0 {
		t.Errorf("a moved rule leaves the digest")
	}
	// A rule dropped.
	dropped := base
	dropped.rules = base.rules[1:]
	if Digest(dropped) == d0 {
		t.Errorf("a removed rule leaves the digest")
	}
	// The source of a rule counts, and the reason does not.
	src := base
	src.rules = append([]Rule(nil), base.rules...)
	src.rules[0].Source = SourceProject
	if Digest(src) == d0 {
		t.Errorf("a changed source leaves the digest")
	}
	why := base
	why.rules = append([]Rule(nil), base.rules...)
	why.rules[0].Reason = "another wording"
	if Digest(why) != d0 {
		t.Errorf("a reworded reason changes the digest")
	}
	// Length prefixes: patterns that join to the same bytes differ.
	a, _ := Set{}.With([]string{"ab", "c"})
	b, _ := Set{}.With([]string{"a", "bc"})
	if Digest(a) == Digest(b) {
		t.Errorf("no length prefix: a boundary can shift")
	}
}

func TestAdditions(t *testing.T) {
	if n := Additions(Default()); n != 0 {
		t.Errorf("Additions(Default) = %d", n)
	}
	s, _ := Default().With([]string{"a", "b", "c"})
	if n := Additions(s); n != 3 {
		t.Errorf("Additions = %d", n)
	}
	if n := Additions(Set{}); n != 0 {
		t.Errorf("Additions(empty) = %d", n)
	}
}
