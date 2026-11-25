package sessiontest

import (
	a "ataniq/internal/agentsession"
	"testing"
)

func screen(busy, dialog string) func(string) Frame {
	return func(text string) Frame {
		f := Frame{Transport: a.Claim{State: "idle", Basis: "screen"}, Sight: "rules"}
		switch text {
		case busy:
			f.Transport.State = "busy"
		case dialog:
			f.Veto = a.Veto{"dialog"}
		case "obscured":
			f.Sight = "blind"
		case "unrelated":
			f.Veto = a.Veto{"overlay"}
		}
		return f
	}
}
func self(s a.State, source a.ClaimSource) *a.Claim {
	return &a.Claim{State: s, Basis: "self", Source: source}
}
func TestTC10AndTC13NativePrompt(t *testing.T) {
	offer := a.Inputs{Transport: a.Claim{State: "idle", Basis: "screen"}, Sight: "rules"}
	for _, words := range [][2]string{{"working", "permission"}, {"running", "confirm"}} {
		busy, dialog := words[0], words[1]
		read := screen(busy, dialog)
		script := ScreenScript{Anchor: 1, End: 3, Draws: []Draw{{0, "idle"}, {1, busy}, {2, busy}, {3, dialog}}, Read: read}
		if ok, why := script.Offer(offer); !ok || why != "" {
			t.Fatal(why)
		}
		for _, draw := range script.Draws[1:] {
			frame := read(draw.Text)
			for _, policy := range []a.SelfDelivery{"", "checked"} {
				for _, s := range []*a.Claim{nil, self("idle", "client"), self("asking", "client"), self("blocked", "client"), self("busy", "client")} {
					for _, h := range []*a.Claim{nil, self("idle", "hook"), self("blocked", "hook"), self("busy", "hook"), nil} {
						in := a.Inputs{Transport: frame.Transport, Veto: frame.Veto, Sight: frame.Sight, Policy: a.Policy{Self: policy}, Self: s, Hook: h}
						if a.Compose(in).Deliverable {
							t.Fatal(draw, in)
						}
					}
				}
			}
		}
		for _, bad := range []ScreenScript{
			{1, 2, []Draw{{1, "obscured"}, {2, "obscured"}}, read},
			{1, 2, []Draw{{1, "unrelated"}, {2, "unrelated"}}, read},
			{1, 3, []Draw{{1, "idle"}, {2, "idle"}, {3, dialog}}, read},
			{0, 2, []Draw{{1, busy}, {2, dialog}}, read},
			{2, 1, nil, read}, {1, 2, []Draw{{1, busy}}, read}, {1, 3, []Draw{{1, busy}, {3, dialog}}, read},
		} {
			ok, why := bad.Offer(offer)
			if ok || why != "[pack-unqualified] fence held: the interval before a native prompt is not covered" {
				t.Fatal(bad, ok, why)
			}
		}
	}
}
