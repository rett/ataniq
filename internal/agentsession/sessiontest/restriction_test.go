package sessiontest

import (
	a "ataniq/internal/agentsession"
	"slices"
	"testing"
)

// Compare adjacent and non-adjacent restrictions of each admitted candidate.
// A basis that cannot say the new state is outside this relation (CR2).
func checkRestrictions(t *testing.T, in a.Inputs, r a.Record) {
	if r.Deliverable {
		return
	}
	order := []a.State{"idle", "asking", "busy", "blocked"}
	for slot, c := range [...]*a.Claim{&in.Transport, in.Self, in.Hook} {
		if c == nil {
			continue
		}
		name := [3]a.Slot{"transport", "self", "hook"}[slot]
		if admission(name, c) != "" {
			continue
		}
		rank := slices.Index(order, c.State)
		if rank < 0 {
			continue
		}
		for _, s := range order[rank+1:] {
			n := in
			candidate := *c
			candidate.State = s
			if admission(name, &candidate) != "" {
				continue
			}
			switch slot {
			case 0:
				n.Transport = candidate
			case 1:
				n.Self = &candidate
			case 2:
				n.Hook = &candidate
			}
			if a.Compose(n).Deliverable {
				t.Fatal("V4 more restrictive candidate", name, in, n)
			}
		}
	}
}
func TestTC7V4Restrictions(t *testing.T) {
	// A dropped optional slot contributes the same decision as an absent one
	// (V9). Dropped transports likewise have one refusal class for this test.
	admitted := func(slot a.Slot) []option {
		var rows []option
		for _, o := range options(slot) {
			if o.why == "" {
				rows = append(rows, o)
			}
		}
		return rows
	}
	transport, selves, hooks := admitted("transport"), admitted("self"), admitted("hook")
	transport = append(transport, option{raw: self("idle", "hook")})
	for _, tr := range transport {
		for _, s := range selves {
			for _, h := range hooks {
				for _, sight := range []a.Sight{"rules", "blind", "none"} {
					for _, policy := range []a.SelfDelivery{"", "checked"} {
						for _, ended := range []bool{false, true} {
							in := a.Inputs{Transport: *tr.raw, Self: s.raw, Hook: h.raw, Sight: sight, Policy: a.Policy{Self: policy}, Ended: ended}
							r := a.Compose(in)
							checkRestrictions(t, in, r)
							in.Veto = a.Veto{"dialog"}
							rr := a.Compose(in)
							if rr.Deliverable {
								t.Fatal("V4 adding veto", in)
							}
							checkRestrictions(t, in, rr)
						}
					}
				}
			}
		}
	}
}
