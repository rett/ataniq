package sessiontest

import (
	a "ataniq/internal/agentsession"
	"math/rand"
	"slices"
	"testing"
)

// The oracle uses the table of allowed (basis, state) pairs, then looks for
// the first candidate in precedence order. No model decision helper is shared.
func admission(slot a.Slot, c *a.Claim) a.DroppedReason {
	if c == nil {
		return ""
	}
	allowed := map[a.Basis][]a.State{
		"host":      {"unknown"},
		"screen":    {"unknown", "idle", "busy", "blocked"},
		"transport": {"unknown", "idle", "busy", "blocked", "asking"},
		"self":      {"unknown", "idle", "busy", "blocked", "asking"},
	}
	if !slices.Contains(allowed[c.Basis], c.State) {
		return "basis-not-allowed"
	}
	switch slot {
	case "transport":
		if c.Basis != "screen" && c.Basis != "transport" {
			return "slot-basis"
		}
		if c.Source != "" {
			return "slot-source"
		}
	case "self":
		if c.Basis != "self" {
			return "slot-basis"
		}
		if c.Source == "hook" {
			return "slot-source"
		}
	case "hook":
		if c.Basis != "self" {
			return "slot-basis"
		}
		if c.Source != "hook" {
			return "slot-source"
		}
		if c.State == "asking" {
			return "hook-asking"
		}
	}
	return ""
}

type option struct {
	raw, accepted *a.Claim
	why           a.DroppedReason
}

func options(slot a.Slot) []option {
	var out []option
	if slot != "transport" {
		out = append(out, option{})
	}
	for _, basis := range a.Basis("").All() {
		for _, source := range a.ClaimSource("").All() {
			for _, state := range a.State("").All() {
				c := &a.Claim{State: state, Basis: basis, Source: source}
				why := admission(slot, c)
				o := option{raw: c, why: why}
				if why == "" {
					o.accepted = c
				}
				out = append(out, o)
			}
		}
	}
	return out
}

type decision struct {
	state    a.State
	basis    a.Basis
	permit   a.Permit
	disagree bool
}

func model(in a.Inputs, tr, s, h option) decision {
	d := decision{state: "unknown"}
	counts := 0
	for _, state := range [...]a.State{"blocked", "busy", "asking", "idle"} {
		for _, c := range [...]*a.Claim{tr.accepted, s.accepted, h.accepted} {
			if c != nil && c.State == state {
				if counts == 0 {
					d.state = state
					d.basis = c.Basis
				}
				counts++
				break
			}
		}
	}
	d.disagree = counts > 1
	if in.Ended {
		d.state = "ended"
		d.basis = "host"
	}
	if (d.state == "idle" || d.state == "asking") && in.Sight != "blind" && len(in.Veto) == 0 && tr.accepted != nil {
		switch tr.raw.State {
		case "idle", "asking":
			d.permit = "transport"
		case "unknown":
			if in.Sight == "rules" && in.Policy.Self == "checked" && s.accepted != nil && s.raw.State == "idle" && s.raw.Source == "client" {
				d.permit = "self-checked"
			}
		}
	}
	return d
}
func compareCompose(t *testing.T, in a.Inputs, tr, s, h option, r a.Record) decision {
	want := model(in, tr, s, h)
	got := decision{r.State, r.Basis, r.Permit, r.Disagree}
	if got != want || r.Deliverable != (want.permit != "") {
		t.Fatalf("decision %v want %v, input %+v", r, want, in)
	}
	count, claims := 0, 0
	for i, o := range [...]option{tr, s, h} {
		if o.raw != nil {
			if claims >= len(r.Claims) {
				t.Fatal(in, r)
			}
			c := r.Claims[claims]
			if c.State != o.raw.State || c.Basis != o.raw.Basis || c.Source != o.raw.Source || c.At != o.raw.At || !slices.Equal(c.Detail, o.raw.Detail) {
				t.Fatal(in, r)
			}
			claims++
		}
		if o.why != "" {
			slot := [3]a.Slot{"transport", "self", "hook"}[i]
			if len(r.Dropped) <= count || r.Dropped[count] != (a.DroppedClaim{Slot: slot, Reason: o.why}) {
				t.Fatal(in, r)
			}
			count++
		}
	}
	if count != len(r.Dropped) || claims != len(r.Claims) || !slices.Equal(in.Veto, r.Veto) || r.Sight != in.Sight {
		t.Fatal(in, r)
	}
	reasons := slices.Clone([]string(in.Veto))
	if want.state != "idle" && want.state != "asking" {
		reasons = append(reasons, "state "+string(want.state))
	}
	if tr.accepted == nil {
		reasons = append(reasons, "transport dropped")
	} else if tr.raw.State == "unknown" && !(in.Sight == "rules" && in.Policy.Self == "checked" && s.accepted != nil && s.raw.State == "idle" && s.raw.Source == "client") {
		reasons = append(reasons, "transport cannot tell")
	}
	if in.Sight == "blind" {
		reasons = append(reasons, "untested build")
	}
	if !slices.Equal(reasons, r.Refusal) {
		t.Fatal(in, r, reasons)
	}
	if r.Deliverable && (len(in.Veto) > 0 || tr.why != "" || in.Sight == "blind" || r.Permit == "self-checked" && (in.Policy.Self != "checked" || s.accepted == nil || s.raw.Source != "client" || s.raw.State != "idle")) {
		t.Fatal("V1/V3/V6/V8", in, r)
	}
	return got
}

// V9 includes refusal ordering when an optional claim is dropped.
func sameDecision(x, y a.Record) bool {
	return x.State == y.State && x.Basis == y.Basis && x.Deliverable == y.Deliverable && x.Permit == y.Permit && x.Disagree == y.Disagree && slices.Equal(x.Refusal, y.Refusal)
}
func variants(t *testing.T, in a.Inputs, tr, s, h option, r, noSelf, noHook a.Record) {
	if r.Deliverable && (in.Policy.Self == "" || r.Permit == "transport") && (!noSelf.Deliverable || noSelf.Permit != r.Permit || tr.accepted == nil || tr.raw.State != "asking" && tr.raw.State != "idle") {
		t.Fatal("V2", in, r)
	}
	if r.Deliverable && (!noHook.Deliverable || noHook.Permit != r.Permit) {
		t.Fatal("V5", in, r)
	}
	if s.why != "" && !sameDecision(r, noSelf) || h.why != "" && !sameDecision(r, noHook) {
		t.Fatal("V9", in, r)
	}
}
func TestTC7CompositionProduct(t *testing.T) {
	transport, selves, hooks := options("transport"), options("self"), options("hook")
	for _, policy := range []a.SelfDelivery{"", "checked"} {
		for _, sight := range a.Sight("").All() {
			for _, ended := range []bool{false, true} {
				for _, veto := range []a.Veto{nil, {"dialog"}} {
					in := a.Inputs{Ended: ended, Sight: sight, Policy: a.Policy{Self: policy}, Veto: veto}
					for _, tr := range transport {
						in.Transport = *tr.raw
						in.Self = nil
						in.Hook = nil
						noSelf := make([]a.Record, len(hooks))
						for j, h := range hooks {
							in.Hook = h.raw
							noSelf[j] = a.Compose(in)
						}
						for _, s := range selves {
							in.Self = s.raw
							in.Hook = nil
							noHook := a.Compose(in)
							for j, h := range hooks {
								in.Hook = h.raw
								r := a.Compose(in)
								compareCompose(t, in, tr, s, h, r)
								variants(t, in, tr, s, h, r, noSelf[j], noHook)
								// V4: any admitted busy or blocked candidate vetoes every permit;
								// changing idle to asking cannot create one in the checked client cell.
								if r.Deliverable {
									for _, c := range [...]*a.Claim{tr.accepted, s.accepted, h.accepted} {
										if c != nil && (c.State == "busy" || c.State == "blocked") {
											t.Fatal("V4", in)
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}
}
func TestTC7SeededComposition(t *testing.T) {
	rng := rand.New(rand.NewSource(28))
	transport, selves, hooks := options("transport"), options("self"), options("hook")
	for range 200000 {
		tr, s, h := transport[rng.Intn(len(transport))], selves[rng.Intn(len(selves))], hooks[rng.Intn(len(hooks))]
		in := a.Inputs{Transport: *tr.raw, Self: s.raw, Hook: h.raw, Sight: []a.Sight{"rules", "none", "blind"}[rng.Intn(3)], Ended: rng.Intn(2) == 1}
		if rng.Intn(2) == 1 {
			in.Policy.Self = "checked"
		}
		if rng.Intn(2) == 1 {
			in.Veto = a.Veto{"dialog"}
		}
		r := a.Compose(in)
		compareCompose(t, in, tr, s, h, r)
		checkRestrictions(t, in, r)
		ns, nh := in, in
		ns.Self = nil
		nh.Hook = nil
		variants(t, in, tr, s, h, r, a.Compose(ns), a.Compose(nh))
		narrower := in
		narrower.Veto = append(slices.Clone(in.Veto), "hold")
		if a.Compose(narrower).Deliverable {
			t.Fatal("V4 veto", in)
		}
		for _, state := range []a.State{"busy", "blocked"} {
			narrower = in
			narrower.Hook = self(state, "hook")
			if a.Compose(narrower).Deliverable {
				t.Fatal("V4 hook", in)
			}
		}
	}
}
