package assignment

import (
	"encoding/hex"
	"slices"
	"strings"
)

// Take each reference once. Unknown, duplicate and unused assertions refuse
// the command even when its required assertions were present.
type evidenceSet []Evidence

func proofs(c Command, scope Head) (evidenceSet, error) {
	for i, e := range c.Evidence {
		digest, err := hex.DecodeString(e.Digest)
		if (e.Kind == "effect") != (e.Route != "") || err != nil || len(digest) != 32 || strings.ToLower(e.Digest) != e.Digest ||
			e.Scope != scope || e.TransferID != c.TransferID || e.Kind == "" ||
			e.Issuer == "" || e.Subject == "" || e.Revision == "" || e.Outcome == "" {
			return nil, ErrEvidence
		}
		for _, earlier := range c.Evidence[:i] {
			if earlier.Kind == e.Kind && earlier.Subject == e.Subject {
				return nil, ErrEvidence
			}
		}
	}
	return slices.Clone(c.Evidence), nil
}

func (p *evidenceSet) take(kind, subject, outcome string) (Evidence, error) {
	for i, e := range *p {
		if e.Kind == kind && e.Subject == subject && e.Outcome == outcome {
			*p = slices.Delete(*p, i, i+1)
			return e, nil
		}
	}
	return Evidence{}, ErrEvidence
}

func (p *evidenceSet) group(kind, outcome string) []Evidence {
	var matches []Evidence
	*p = slices.DeleteFunc(*p, func(e Evidence) bool {
		if e.Kind != kind || e.Outcome != outcome {
			return false
		}
		matches = append(matches, e)
		return true
	})
	return matches
}

func subjects(es []Evidence) []string {
	var names []string
	for _, e := range es {
		names = append(names, e.Subject)
	}
	return names
}

func covers(names, required []string) bool {
	for _, name := range required {
		if !slices.Contains(names, name) {
			return false
		}
	}
	return true
}

func (t *Transfer) capture(p *evidenceSet, required []string) error {
	t.Routes = subjects(p.group("route", "enabled"))

	if !slices.Contains(t.Routes, "qargi") || !covers(t.Routes, required) {
		return ErrEvidence
	}
	return t.inventoryEffects(p)
}

func (t *Transfer) inventoryEffects(p *evidenceSet) error {
	for _, e := range p.group("effect", "outstanding") {
		if !slices.Contains(t.Routes, e.Route) {
			return ErrEvidence
		}
		effect := Effect{e.Subject, e.Route}
		i := slices.IndexFunc(t.Outstanding, func(x Effect) bool { return x.ID == e.Subject })
		if i < 0 {
			t.Outstanding = append(t.Outstanding, effect)
		} else if t.Outstanding[i] != effect {
			return ErrEvidence
		}
	}
	return nil
}

func (t *Transfer) settle(p *evidenceSet, route string) error {
	for _, e := range p.group("effect", "settled") {
		if route != "" && e.Route != route {
			return ErrEvidence
		}
		i := slices.Index(t.Outstanding, Effect{e.Subject, e.Route})
		if i < 0 {
			return ErrEvidence
		}
		t.Outstanding = slices.Delete(t.Outstanding, i, i+1)
	}
	for _, effect := range t.Outstanding {
		if route == "" || effect.Route == route {
			return ErrEvidence
		}
	}
	return nil
}

func receipt(p *evidenceSet, routes []string, existing []Evidence, outcome string) ([]Evidence, error) {
	es := p.group("route", outcome)
	if len(es) != 1 || !slices.Contains(routes, es[0].Subject) || slices.Contains(subjects(existing), es[0].Subject) {
		return nil, ErrEvidence
	}
	return append(existing, es[0]), nil
}
