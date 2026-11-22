package ownerwire

import (
	"regexp"
	"strings"
	"testing"
)

// The client protocol's seam for added operations refuses a name with a
// second dot, so every name is a prefix and one part of lower-case letters,
// digits and dashes. The prefix is "owner." for the console's operations,
// and "seat." for the seat app's one call, which is the only name with it.
func TestEveryOperationNameIsOwnerOrSeatAndOnePart(t *testing.T) {
	form := regexp.MustCompile(`^(owner|seat)\.[a-z0-9-]+$`)
	for _, op := range Operations() {
		if !form.MatchString(op) {
			t.Errorf("%q is not owner. or seat. and one part of lower-case letters, digits and dashes", op)
		}
		if strings.HasPrefix(op, "seat.") && op != OpSeatPair {
			t.Errorf("%q: the seat's only operation is %q", op, OpSeatPair)
		}
		dots := 0
		for i := 0; i < len(op); i++ {
			if op[i] == '.' {
				dots++
			}
		}
		if dots != 1 {
			t.Errorf("%q has %d dots, want exactly one", op, dots)
		}
	}
}

// The list is the table of the seams note, in its order, and each constant
// is in it once.
func TestOperationsAreTheEighteenOfTheTableInOrder(t *testing.T) {
	want := []string{
		"owner.list", "owner.show", "owner.paths", "owner.patch", "owner.decider",
		"owner.decide", "owner.beat", "owner.limit", "owner.limit-set", "owner.looked",
		"owner.pair-start", "owner.pair-status", "owner.pair-confirm", "owner.pair-reject",
		"owner.keys", "owner.key-revoke", "owner.keys-off", "seat.pair",
	}
	got := Operations()
	if len(got) != len(want) {
		t.Fatalf("%d operations, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("operation %d is %q, want %q", i, got[i], want[i])
		}
	}
	consts := []string{
		OpList, OpShow, OpPaths, OpPatch, OpDecider, OpDecide, OpBeat, OpLimit, OpLimitSet, OpLooked,
		OpPairStart, OpPairStatus, OpPairConfirm, OpPairReject, OpKeys, OpKeyRevoke, OpKeysOff, OpSeatPair,
	}
	for i := range want {
		if consts[i] != want[i] {
			t.Errorf("constant %d is %q, want %q", i, consts[i], want[i])
		}
	}
}

// A caller that changes the list it was given does not change the next
// caller's.
func TestOperationsReturnsANewList(t *testing.T) {
	a := Operations()
	a[0] = "changed"
	if b := Operations(); b[0] != OpList {
		t.Fatalf("the list is shared: its first name is now %q", b[0])
	}
}
