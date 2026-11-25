// Package sessiontest supplies scripted observations for testing session decisions.
package sessiontest

import (
	a "ataniq/internal/agentsession"
	"slices"
)

type Fixture struct {
	Expect a.Expectation
	Deny   bool
}

func Alpha() Fixture {
	return Fixture{a.Expectation{Facts: []a.FactExp{
		{Fact: "hooks-loaded", Required: true, Want: "yes"}, {Fact: "folder-trust", Required: true, Want: "trusted"},
		{Fact: "mode-admitted", Required: true, Want: "yes"}, {Fact: "outranking-policy", Required: true, Want: "no"},
	}}, true}
}
func Beta() Fixture {
	return Fixture{Expect: a.Expectation{Facts: []a.FactExp{
		{Fact: "folder-trust", Required: true, Want: "untrusted"}, {Fact: "mode-admitted", Required: true, Want: "yes"}, {Fact: "outranking-policy", Required: true, Want: "no"},
	}}}
}
func (f Fixture) Matches() a.Readback {
	exp := f.Expect
	exp.Facts = slices.Clone(exp.Facts)
	o := a.ReadbackObs{Attempt: "read", Sources: a.SourcesObs{Seen: true, Digest: exp.Sources.Digest}}
	for _, x := range exp.Facts {
		o.Facts = append(o.Facts, a.FactObs{Fact: x.Fact, Seen: true, Got: x.Want})
	}
	return a.Readback{Exp: exp, Obs: o}
}
func (f Fixture) Contradicts(facts ...a.Fact) a.Readback {
	r := f.Matches()
	for i, x := range r.Obs.Facts {
		if slices.Contains(facts, x.Fact) {
			switch x.Got {
			case "yes":
				r.Obs.Facts[i].Got = "no"
			case "no":
				r.Obs.Facts[i].Got = "yes"
			case "trusted":
				r.Obs.Facts[i].Got = "untrusted"
			case "untrusted":
				r.Obs.Facts[i].Got = "trusted"
			}
		}
	}
	if slices.Contains(facts, "policy-sources") {
		r.Obs.Sources.Digest[0] ^= 1
	}
	return r
}
func (f Fixture) Unavailable(cause a.Cause) a.Readback {
	r := f.Matches()
	if cause == "partial" {
		r.Obs.Sources.Seen = false
	} else {
		r.Obs = a.ReadbackObs{Attempt: a.Outcome(cause)}
	}
	return r
}
func (f Fixture) Independent() a.Independent {
	i := a.Independent{Launch: a.Launch{BundleB: true, DeclaresHookRows: true}, Gates: a.Gates{ReadbackActive: true, ReadbackOffered: true, S3Active: true}, Build: a.Build{DeclaresDenyRow: f.Deny, DenyMeant: f.Deny}, I1: true, I2: a.I2{A8: "pass", A12: "pass", A13: "pass", A14: "not-checked"}, I3: true, Readback: f.Matches()}
	if f.Deny {
		i.I2.A14 = "pass"
	}
	return i
}
