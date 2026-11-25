package agentsession

import (
	"reflect"
	"slices"
	"testing"
)

func TestTR8ToTR14aAndTR33Readback(t *testing.T) {
	for _, trust := range []Want{"trusted", "untrusted"} {
		e := Expectation{Facts: []FactExp{{"folder-trust", true, trust}, {"outranking-policy", true, "no"}, {"mode-admitted", false, "yes"}}}
		o := ReadbackObs{Attempt: "read", Facts: []FactObs{{"folder-trust", true, trust}, {"outranking-policy", true, "no"}}, Sources: SourcesObs{Seen: true}}
		check := func(w Reading, c Cause, facts ...Fact) {
			t.Helper()
			v, err := ClassifyReadback(e, o)
			if err != nil || v.Reading() != w || v.Cause() != c || !slices.Equal(v.Contradicted(), facts) || v.Outranked() != slices.Contains(facts, Fact("outranking-policy")) {
				t.Fatal(e, o, v, err)
			}
		}
		check("matches", "")
		o.Facts[0] = FactObs{Fact: "folder-trust"}
		check("unavailable", "partial")
		opposite := Want("trusted")
		if trust == "trusted" {
			opposite = "untrusted"
		}
		o.Facts[0] = FactObs{"folder-trust", true, opposite}
		o.Facts[1] = FactObs{Fact: "outranking-policy"}
		check("contradicts", "", "folder-trust")
		o.Facts[1] = FactObs{"outranking-policy", true, "yes"}
		check("contradicts", "", "folder-trust", "outranking-policy")
		o.Facts = slices.Clip(slices.Clone(o.Facts))
		slices.Reverse(o.Facts)
		check("contradicts", "", "folder-trust", "outranking-policy")
		o.Facts = []FactObs{{"folder-trust", true, trust}, {"outranking-policy", true, "no"}}
		o.Sources.Digest[4] = 7
		check("contradicts", "", "policy-sources")
		for _, outcome := range []Outcome{"not-performed", "error", "timeout", "unparsable"} {
			o = ReadbackObs{Attempt: outcome}
			check("unavailable", Cause(outcome))
			o.Facts = []FactObs{{"folder-trust", true, trust}}
			v, err := ClassifyReadback(e, o)
			requireError(t, err, "Obs.Facts[0].Seen", "inconsistent")
			if !reflect.DeepEqual(v, I4{}) {
				t.Fatal(v)
			}
		}
	}
}
func TestTR34ReadbackInvalid(t *testing.T) {
	e := Expectation{Facts: []FactExp{{"folder-trust", true, "trusted"}}}
	for _, tc := range []struct {
		x FactExp
		r InputReason
		m string
	}{
		{FactExp{}, "empty-value", "Fact"}, {FactExp{Fact: "other"}, "unknown-value", "Fact"}, {FactExp{Fact: "policy-sources"}, "inconsistent", "Fact"},
		{FactExp{Fact: "folder-trust"}, "empty-value", "Want"}, {FactExp{Fact: "folder-trust", Want: "yes"}, "inconsistent", "Want"},
	} {
		requireError(t, ValidateExpectation(Expectation{Facts: []FactExp{tc.x}}), "Expectation.Facts[0]."+tc.m, tc.r)
	}
	dup := e
	dup.Facts = append(slices.Clone(e.Facts), e.Facts...)
	requireError(t, ValidateExpectation(dup), "Expectation.Facts[1].Fact", "duplicate")
	for _, tc := range []struct {
		x FactObs
		m string
		r InputReason
	}{
		{FactObs{Fact: "hooks-loaded", Seen: true, Got: "yes"}, "Fact", "inconsistent"}, {FactObs{Fact: "policy-sources"}, "Fact", "inconsistent"},
		{FactObs{Fact: "folder-trust", Got: "no"}, "Got", "inconsistent"}, {FactObs{"folder-trust", true, "no"}, "Got", "inconsistent"},
		{FactObs{"folder-trust", true, "x"}, "Got", "unknown-value"}, {FactObs{"folder-trust", true, ""}, "Got", "empty-value"},
	} {
		o := ReadbackObs{Attempt: "read", Facts: []FactObs{tc.x}}
		requireError(t, ValidateObs(e, o), "Obs.Facts[0]."+tc.m, tc.r)
		v, err := ClassifyReadback(e, o)
		requireError(t, err, "Obs.Facts[0]."+tc.m, tc.r)
		if !reflect.DeepEqual(v, I4{}) {
			t.Fatal(v)
		}
	}
	o := ReadbackObs{Attempt: "read", Facts: []FactObs{{Fact: "folder-trust"}, {Fact: "folder-trust"}}}
	requireError(t, ValidateObs(e, o), "Obs.Facts[1].Fact", "duplicate")
}
func TestTR38ReducedClassifier(t *testing.T) {
	// For each scalar, cross absent/optional/required expectation with unseen,
	// equal and different observations. Cross the source set independently.
	facts := []Fact{"hooks-loaded", "folder-trust", "mode-admitted", "outranking-policy"}
	var walk func(int, Expectation, ReadbackObs, []Fact, bool, bool)
	walk = func(n int, e Expectation, o ReadbackObs, diff []Fact, missing, bad bool) {
		if n == len(facts) {
			for j := range 3 {
				o.Sources = SourcesObs{Seen: j != 0}
				if j == 2 {
					o.Sources.Digest[0] = 1
				}
				want := slices.Clone(diff)
				if j == 2 {
					want = append(want, "policy-sources")
				}
				v, err := ClassifyReadback(e, o)
				if bad {
					if err == nil || !reflect.DeepEqual(v, I4{}) {
						t.Fatal(e, o, v, err)
					}
					continue
				}
				reading := Reading("matches")
				if missing || j == 0 {
					reading = "unavailable"
				}
				if len(want) > 0 {
					reading = "contradicts"
				}
				if err != nil || reading != v.Reading() || !slices.Equal(want, v.Contradicted()) || v.Outranked() != slices.Contains(want, Fact("outranking-policy")) || (v.Cause() != "") != (reading == "unavailable") {
					t.Fatal(e, o, v, err)
				}
			}
			return
		}
		f := facts[n]
		yes, no := Want("yes"), Want("no")
		if f == "folder-trust" {
			yes, no = "trusted", "untrusted"
		}
		for exp := range 3 {
			for obs := range 3 {
				ee, oo, dd := e, o, diff
				if exp != 0 {
					ee.Facts = append(slices.Clip(e.Facts), FactExp{f, exp == 2, yes})
				}
				if obs != 0 {
					got := yes
					if obs == 2 {
						got = no
						dd = append(slices.Clip(diff), f)
					}
					oo.Facts = append(slices.Clip(o.Facts), FactObs{f, true, got})
				}
				walk(n+1, ee, oo, dd, missing || exp == 2 && obs == 0, bad || exp == 0 && obs != 0)
			}
		}
	}
	walk(0, Expectation{}, ReadbackObs{Attempt: "read"}, nil, false, false)
}
