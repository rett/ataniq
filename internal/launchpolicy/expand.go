package launchpolicy

import (
	"maps"
	"slices"
	"strings"

	"ataniq/internal/leadrole"
	"ataniq/internal/pack"
)

func identity(p *Policy, s Snapshot) (string, *Refusal) {
	if p == nil || p.doc.Fixture.SHA == nil {
		return "", refuse("policy-missing", 0, "policy")
	}
	if _, err := pack.ParseName(string(s.Pack)); err != nil || !versionPattern.MatchString(s.Version) {
		return "", refuse("snapshot-invalid", 0, "snapshot")
	}
	if p.doc.Pack != s.Pack || p.doc.Version != s.Version {
		return "", refuse("policy-missing", 0, "policy")
	}
	if *p.doc.Fixture.SHA == "" {
		return "", refuse("policy-unrecorded", 0, "policy")
	}
	role, err := leadrole.Parse(s.Role)
	if err != nil || role.Kind() != leadrole.KindLead && role.Kind() != leadrole.KindCoordinator ||
		s.Transport != pack.Terminal && s.Transport != pack.Protocol ||
		s.Recognizer != "" && !recognizerPattern.MatchString(s.Recognizer) || !profilePattern.MatchString(s.ProfileDigest) {
		return "", refuse("snapshot-invalid", 0, "snapshot")
	}
	for _, slot := range slots {
		if v := s.value(slot); v != "" && !ceiling.MatchString(v) {
			return "", refuse("snapshot-invalid", 0, "snapshot")
		}
	}
	return role.Kind().String(), nil
}

func (s *Snapshot) bounds(argv []string, env map[string]string, stage int) *Refusal {
	for i, token := range argv {
		if i >= 64 || len(token) > 512 {
			return refuse("bound", stage, argvAt(i))
		}
	}
	if len(env) > 32 {
		keys := slices.Sorted(maps.Keys(env))
		return refuse("bound", stage, s.location("env", keys[32], 32))
	}
	return nil
}
func (p *Policy) lookup(token string) (*form, string) {
	for _, s := range p.spellings {
		if s.matches(token) {
			return s.form, s.refusal
		}
	}
	return nil, "unknown-token"
}

func (p *Policy) checkTransport(t pack.Transport) *Refusal {
	if !p.executable.transports(t) {
		return refuse("transport-not-admitted", 0, "snapshot")
	}
	return nil
}

type sequence struct {
	policy   *Policy
	kind     string
	snapshot *Snapshot
	stage    int
	counts   map[*form]int
	split    int // index of the separator, -1 until seen
}

func newSequence(p *Policy, kind string, s *Snapshot, stage int) *sequence {
	return &sequence{policy: p, kind: kind, snapshot: s, stage: stage, counts: make(map[*form]int), split: -1}
}
func (q *sequence) requiredForms() *Refusal {
	for i := range q.policy.doc.Forms {
		f := &q.policy.doc.Forms[i]
		if f.Kinds[q.kind] == required && f.transports(q.snapshot.Transport) && q.counts[f] == 0 {
			return refuse("required-missing", q.stage, q.snapshot.location("form", f.ID, i))
		}
	}
	return nil
}
func (q *sequence) place(f *form, i int, token string, written bool) *Refusal {
	if !f.admitted(q.kind, q.snapshot.Transport) {
		return q.snapshot.tokenFault("form-not-admitted", q.stage, i, f, token)
	}
	if q.counts[f] >= f.Kinds[q.kind].limit() {
		// An executable repeated before the split has a known wrong position.
		if f == q.policy.executable && q.split < 0 {
			return q.snapshot.tokenFault("form-position", q.stage, i, f, token)
		}
		return q.snapshot.tokenFault("form-repeated", q.stage, i, f, token)
	}
	if (q.split >= 0) != (f.After != nil) {
		return q.snapshot.tokenFault("form-position", q.stage, i, f, token)
	}
	if !written && (f.At != nil && *f.At.Index != i || f.After != nil && *f.After != i-q.split-1) {
		return q.snapshot.tokenFault("form-position", q.stage, i, f, token)
	}
	q.counts[f]++
	if f.Separator {
		q.split = i
	}
	return nil
}
func (q *sequence) templateForm(token string) (*form, string) {
	p := q.policy
	if prefix, name, ok := strings.Cut(token, "{"); ok {
		name, closed := strings.CutSuffix(name, "}")
		slot := pack.Slot(name)
		if !closed || !slot.Valid() {
			return nil, "unknown-token"
		}
		if prefix == "" {
			f := p.bySlot[slot]
			if q.split < 0 || f == nil || f.Shape != "positional" {
				return f, "form-position"
			}
			return f, ""
		}
		f, code := p.lookup(prefix)
		if code != "" || string(f.Prefix) != prefix {
			return nil, "unknown-token"
		}
		if f.Slot != slot {
			return f, "slot-mismatch"
		}
		return f, ""
	}
	f, code := p.lookup(token)
	if code == "" && f.Slot != "" {
		return f, "literal-in-slot"
	}
	return f, code
}

// finalForm is shared by expansion and the independent spec check. Beyond the
// separator, the index determines the interpretation even for option-shaped
// values and values equal to the executable.
func (q *sequence) finalForm(i int, token string) (*form, string) {
	p := q.policy
	if q.split >= 0 {
		if f := p.after[i-q.split-1]; f != nil {
			if f.Shape == "exact" && string(f.Token) != token {
				return nil, "unknown-token"
			}
			return f, ""
		}
		if f, _ := p.lookup(token); f != nil && q.counts[f] > 0 {
			return f, "form-repeated"
		}
		return nil, "unknown-token"
	}
	f, code := p.lookup(token)
	if (i == 0) != (f == p.executable) {
		return f, "form-position"
	}
	return f, code
}

// Expand checks the written template, fills its slots and checks the surviving
// positions. Intent authentication and declaration selection belong to Judge.
func Expand(p *Policy, launch pack.Launch, s Snapshot) (Spec, *Refusal) {
	kind, r := identity(p, s)
	if r != nil {
		return Spec{}, r
	}
	if r := p.checkTransport(s.Transport); r != nil {
		return Spec{}, r
	}
	return expand(p, launch, s, kind)
}
func expand(p *Policy, launch pack.Launch, s Snapshot, kind string) (Spec, *Refusal) {
	if r := s.bounds(launch.Argv, nil, 1); r != nil {
		return Spec{}, r
	}
	written := newSequence(p, kind, &s, 1)
	forms := make([]*form, len(launch.Argv))
	for i, token := range launch.Argv {
		f, code := written.templateForm(token)
		if code != "" {
			return Spec{}, s.tokenFault(code, 1, i, f, token)
		}
		if r := written.place(f, i, token, true); r != nil {
			return Spec{}, r
		}
		forms[i] = f
	}
	if r := written.requiredForms(); r != nil {
		return Spec{}, r
	}
	spec := Spec{Role: s.Role, Transport: s.Transport, ProfileDigest: s.ProfileDigest, Env: maps.Clone(launch.Env)}
	surviving := make([]*form, 0, len(forms))
	for _, f := range forms {
		token := string(f.Token)
		if f.Slot != "" {
			v := s.value(f.Slot)
			if v == "" {
				if f.Kinds[kind] == required {
					return Spec{}, refuse("required-missing", 1, s.slotLocation(f.Slot))
				}
				continue
			}
			token = string(f.Prefix) + v
		}
		spec.Argv = append(spec.Argv, token)
		surviving = append(surviving, f)
	}
	if n := len(surviving); n > 0 && surviving[n-1].Separator && surviving[n-1].Kinds[kind] != required {
		spec.Argv = spec.Argv[:n-1]
		surviving = surviving[:n-1]
	}
	final := newSequence(p, kind, &s, 1)
	for i, f := range surviving {
		token := spec.Argv[i]
		found, code := final.finalForm(i, token)
		if f != found && f.After != nil {
			code = "form-position"
		}
		if code != "" {
			return Spec{}, s.tokenFault(code, 1, i, f, token)
		}
		if r := final.place(f, i, token, false); r != nil {
			return Spec{}, r
		}
	}
	if r := final.requiredForms(); r != nil {
		return Spec{}, r
	}
	return spec, nil
}
