package sessiontest

import (
	a "ataniq/internal/agentsession"
	"slices"
)

type Frame struct {
	Transport a.Claim
	Veto      a.Veto
	Sight     a.Sight
}
type Draw struct {
	Index int
	Text  string
}

// Anchor is the index actually visible when the tool request started.
type ScreenScript struct {
	Anchor, End int
	Draws       []Draw
	Read        func(string) Frame
}

const FenceHeld = "[pack-unqualified] fence held: the interval before a native prompt is not covered"

// Offer exercises the fake fence's qualification decision against each required
// screen reading, before consulting the current delivery inputs.
func (s ScreenScript) Offer(in a.Inputs) (bool, string) {
	if !s.covered() {
		return false, FenceHeld
	}
	r := a.Compose(in)
	if !r.Deliverable {
		return false, "fence held"
	}
	return true, ""
}
func (s ScreenScript) covered() bool {
	if s.Anchor < 0 || s.End < s.Anchor || s.Read == nil {
		return false
	}
	next := s.Anchor
	for _, d := range s.Draws {
		if d.Index < s.Anchor {
			continue
		}
		if d.Index != next {
			return false
		}
		f := s.Read(d.Text)
		r := a.Compose(a.Inputs{Transport: f.Transport, Sight: f.Sight, Veto: f.Veto})
		if f.Sight != "rules" || !(slices.Contains(f.Veto, "dialog") || r.State == "busy" && len(r.Dropped) == 0) {
			return false
		}
		if d.Index == s.End {
			return true
		}
		next++
	}
	return false
}
