package door

import (
	"testing"

	"ataniq/internal/approval/record"
)

func TestRecordOfCarriesTheDigestOfItsBytes(t *testing.T) {
	b := []byte(`{"a":1}`)
	r := RecordOf(b)
	if r.SHA256 != record.Digest(b) {
		t.Errorf("SHA256 = %s, want the digest of the bytes", r.SHA256)
	}
	if &r.Bytes[0] != &b[0] {
		t.Error("RecordOf copied the bytes; its comment says it keeps them")
	}
	if got := RecordOf(nil).SHA256; got != record.Digest(nil) {
		t.Errorf("the digest of no bytes = %s", got)
	}
}

// Each outcome is the name of a state of the closed list, and nothing else
// is an outcome: a settle cannot name a state such as approved.
func TestOutcomes(t *testing.T) {
	want := map[Outcome]record.State{
		OutcomeApplied:    record.StateApplied,
		OutcomeNotApplied: record.StateNotApplied,
		OutcomeUnknown:    record.StateUnknown,
	}
	for o, state := range want {
		if !o.Valid() {
			t.Errorf("%q is not valid", o)
		}
		if o.State() != state || !o.State().Valid() {
			t.Errorf("%q leads to %q, want %q", o, o.State(), state)
		}
	}
	for _, s := range []record.State{
		record.StatePending, record.StateApproved, record.StateApprovedByRule,
		record.StateApprovedByGrant, record.StateDenied, record.StateWithdrawn,
		record.StateExpired, record.StateVoid, record.StateConsumed, "", "held",
	} {
		if Outcome(s).Valid() {
			t.Errorf("%q is taken as an outcome", s)
		}
	}
}
