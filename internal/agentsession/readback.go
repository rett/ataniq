package agentsession

import (
	"encoding/json"
	"slices"
	"strings"
)

// I4 is a classified observation. Its zero value represents an unperformed read.
type I4 struct {
	reading      Reading
	cause        Cause
	contradicted []Fact
}

func (v I4) Reading() Reading {
	if v.reading == "" {
		return "unavailable"
	}
	return v.reading
}
func (v I4) Cause() Cause {
	if v.reading == "" {
		return "not-performed"
	}
	return v.cause
}
func (v I4) Contradicted() []Fact { return slices.Clone(v.contradicted) }
func (v I4) Outranked() bool      { return slices.Contains(v.contradicted, "outranking-policy") }
func (v I4) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Reading      Reading
		Cause        Cause
		Outranked    bool
		Contradicted []Fact
	}{v.Reading(), v.Cause(), v.Outranked(), v.contradicted})
}
func scalarFact(f Fact) InputReason {
	if f == "" {
		return "empty-value"
	}
	if !f.Valid() {
		return "unknown-value"
	}
	if f == "policy-sources" {
		return "inconsistent"
	}
	return ""
}
func factValue(f Fact, w Want) InputReason {
	if w == "" {
		return "empty-value"
	}
	if !w.Valid() {
		return "unknown-value"
	}
	if (f == "folder-trust") != (w == "trusted" || w == "untrusted") {
		return "inconsistent"
	}
	return ""
}
func ValidateExpectation(e Expectation) error {
	seen := map[Fact]bool{}
	for i, x := range e.Facts {
		if reason := scalarFact(x.Fact); reason != "" {
			return invalid(index("Expectation.Facts", i)+".Fact", reason)
		}
		if seen[x.Fact] {
			return invalid(index("Expectation.Facts", i)+".Fact", "duplicate")
		}
		seen[x.Fact] = true
		if reason := factValue(x.Fact, x.Want); reason != "" {
			return invalid(index("Expectation.Facts", i)+".Want", reason)
		}
	}
	return nil
}
func ValidateObs(e Expectation, o ReadbackObs) error {
	if err := ValidateExpectation(e); err != nil {
		return err
	}
	if err := enum("Obs.Attempt", o.Attempt); err != nil {
		return err
	}
	seen := map[Fact]bool{}
	for i, x := range o.Facts {
		if reason := scalarFact(x.Fact); reason != "" {
			return invalid(index("Obs.Facts", i)+".Fact", reason)
		}
		if seen[x.Fact] {
			return invalid(index("Obs.Facts", i)+".Fact", "duplicate")
		}
		seen[x.Fact] = true
		if x.Seen && !slices.ContainsFunc(e.Facts, func(w FactExp) bool { return w.Fact == x.Fact }) {
			return invalid(index("Obs.Facts", i)+".Fact", "inconsistent")
		}
		if x.Seen && o.Attempt != "read" {
			return invalid(index("Obs.Facts", i)+".Seen", "inconsistent")
		}
		if !x.Seen && x.Got != "" {
			return invalid(index("Obs.Facts", i)+".Got", "inconsistent")
		}
		if x.Seen {
			if reason := factValue(x.Fact, x.Got); reason != "" {
				return invalid(index("Obs.Facts", i)+".Got", reason)
			}
		}
	}
	if o.Sources.Seen && o.Attempt != "read" {
		return invalid("Obs.Sources.Seen", "inconsistent")
	}
	return nil
}

func ClassifyReadback(e Expectation, o ReadbackObs) (I4, error) {
	if err := ValidateObs(e, o); err != nil {
		return I4{}, err
	}
	if o.Attempt != "read" {
		return I4{reading: "unavailable", cause: Cause(o.Attempt)}, nil
	}
	missing := !o.Sources.Seen
	var differences []Fact
	for _, f := range Fact("").All() {
		if f == "policy-sources" {
			if o.Sources.Seen && o.Sources.Digest != e.Sources.Digest {
				differences = append(differences, f)
			}
			continue
		}
		i := slices.IndexFunc(e.Facts, func(w FactExp) bool { return w.Fact == f })
		if i < 0 {
			continue
		}
		j := slices.IndexFunc(o.Facts, func(x FactObs) bool { return x.Fact == f && x.Seen })
		if j < 0 {
			missing = missing || e.Facts[i].Required
		} else if o.Facts[j].Got != e.Facts[i].Want {
			differences = append(differences, f)
		}
	}
	switch {
	case len(differences) > 0:
		return I4{reading: "contradicts", contradicted: differences}, nil
	case missing:
		return I4{reading: "unavailable", cause: "partial"}, nil
	default:
		return I4{reading: "matches"}, nil
	}
}

func nestedReadbackError(err error) error {
	e := err.(*InputError)
	path := e.Member
	if strings.HasPrefix(path, "Expectation.") {
		path = "Exp." + strings.TrimPrefix(path, "Expectation.")
	}
	return invalid("Independent.Readback."+path, e.Reason)
}
