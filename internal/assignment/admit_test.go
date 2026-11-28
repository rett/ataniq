package assignment

import (
	"errors"
	"testing"
)

func admitFixture() (Row, Head, Binding, string, LiveFacts) {
	head := Head{
		BindingID: "b1", Generation: 3, Key: Key{Home: "h", Project: "p", Role: "r"},
		Phase: PhaseConfirmed, Revision: 9, TransferID: "t1",
	}
	holder := Binding{
		Authority: "a", Conversation: "c", ID: "b1", Incarnation: "i",
		Kind: "managed", LaunchIntent: "l", RoleEpoch: 4, Store: "s",
	}
	row := Row{Head: head, Holder: holder, PublicDigest: "digest"}
	live := LiveFacts{
		Activation: true, Session: "s1", BoundSession: "s1", RoleEpoch: 4,
		RequiredRoutes: []string{"r1", "r2"}, LiveRoutes: []string{"r2", "r1", "r3"},
	}
	return row, head, holder, "digest", live
}

func TestAdmit(t *testing.T) {
	phases := []Phase{
		PhaseQuarantined, PhaseFrozen, PhaseInventoried, PhaseBeliefVerified, PhaseReserved,
		PhasePartiallyFenced, PhaseFenced, PhaseCommittedClosed, PhaseConfirmed,
		PhaseAborting, PhaseAborted, PhaseAbortedClosed,
	}
	type input struct {
		row      Row
		expected Head
		holder   Binding
		digest   string
		live     LiveFacts
	}
	base := func() input {
		row, head, holder, digest, live := admitFixture()
		return input{row, head, holder, digest, live}
	}
	cases := []struct {
		name   string
		mutate func(*input)
		want   error
	}{
		{"all facts hold", func(*input) {}, nil},
	}
	for _, p := range phases {
		want := error(ReasonPhase)
		if p == PhaseConfirmed || p == PhaseAborted {
			want = nil
		}
		cases = append(cases, struct {
			name   string
			mutate func(*input)
			want   error
		}{"phase " + string(p), func(in *input) { in.row.Head.Phase, in.expected.Phase = p, p }, want})
	}
	// Each Head field differs between the submitted head and the row's head.
	// The row's own head would permit, the submitted one must not.
	for _, f := range []struct {
		name   string
		mutate func(*Head)
	}{
		{"BindingID", func(h *Head) { h.BindingID = "other" }},
		{"Generation", func(h *Head) { h.Generation++ }},
		{"Key.Home", func(h *Head) { h.Key.Home = "other" }},
		{"Key.Project", func(h *Head) { h.Key.Project = "other" }},
		{"Key.Role", func(h *Head) { h.Key.Role = "other" }},
		{"Phase", func(h *Head) { h.Phase = PhaseAborted }},
		{"Revision", func(h *Head) { h.Revision++ }},
		{"TransferID", func(h *Head) { h.TransferID = "other" }},
	} {
		cases = append(cases, struct {
			name   string
			mutate func(*input)
			want   error
		}{"submitted head differs in " + f.name, func(in *input) { f.mutate(&in.expected) }, ReasonHeadChanged})
	}
	add := func(name string, want error, mutate func(*input)) {
		cases = append(cases, struct {
			name   string
			mutate func(*input)
			want   error
		}{name, mutate, want})
	}
	add("holder differs", ReasonHolder, func(in *input) { in.holder.Incarnation = "other" })
	add("holder differs in role epoch", ReasonHolder, func(in *input) { in.holder.RoleEpoch++ })
	add("holder id is not the head's binding", ReasonHolder, func(in *input) {
		in.row.Holder.ID, in.holder.ID = "other", "other"
	})
	add("public digest differs", ReasonDigest, func(in *input) { in.digest = "other" })
	add("public digest empty on both", ReasonDigest, func(in *input) { in.digest, in.row.PublicDigest = "", "" })
	add("activation off", ReasonActivationOff, func(in *input) { in.live.Activation = false })
	add("no session held", ReasonSessionLost, func(in *input) { in.live.Session = "" })
	add("session replaced at the same head", ReasonSessionChanged, func(in *input) { in.live.Session = "s2" })
	add("role epoch changed only", ReasonRoleEpoch, func(in *input) { in.live.RoleEpoch++ })
	add("required route missing", ReasonRouteMissing, func(in *input) { in.live.LiveRoutes = []string{"r1"} })
	add("no live routes at all", ReasonRouteMissing, func(in *input) { in.live.LiveRoutes = nil })
	add("no required routes", nil, func(in *input) { in.live.RequiredRoutes, in.live.LiveRoutes = nil, nil })
	// The first failing check decides when several facts are wrong.
	add("head change outranks holder", ReasonHeadChanged, func(in *input) {
		in.expected.Revision++
		in.holder.ID = "other"
	})
	add("holder outranks activation", ReasonHolder, func(in *input) {
		in.holder.Store = "other"
		in.live.Activation = false
	})

	seen := map[error]string{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.mutate(&in)
			got := Admit(in.row, in.expected, in.holder, in.digest, in.live)
			if !errors.Is(got, tc.want) || (got == nil) != (tc.want == nil) {
				t.Fatalf("Admit = %v, want %v", got, tc.want)
			}
		})
		if tc.want != nil {
			seen[tc.want] = tc.name
		}
	}
	for _, r := range []Reason{
		ReasonHeadChanged, ReasonPhase, ReasonHolder, ReasonDigest, ReasonActivationOff,
		ReasonSessionLost, ReasonSessionChanged, ReasonRoleEpoch, ReasonRouteMissing,
	} {
		if _, ok := seen[r]; !ok {
			t.Errorf("no case yields %q", r)
		}
	}
}

func TestAdmitIgnoresRowHeadWhenSubmittedHeadDiffers(t *testing.T) {
	row, head, holder, digest, live := admitFixture()
	row.Head.Revision++ // the ledger advanced; its own head still permits
	if !row.Permits(row.Head) {
		t.Fatal("fixture row must permit its own head")
	}
	if err := Admit(row, head, holder, digest, live); !errors.Is(err, ReasonHeadChanged) {
		t.Fatalf("Admit = %v, want %v", err, ReasonHeadChanged)
	}
}
