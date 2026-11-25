package launchpolicy

import (
	"bytes"
	"maps"
	"slices"
	"strings"

	"ataniq/internal/pack"
)

func authenticate(p *Policy, s Snapshot, intent Intent) (string, *Refusal) {
	kind, r := identity(p, s)
	if r != nil {
		return "", r
	}
	if intent == nil {
		return "", refuse("snapshot-invalid", 0, "snapshot")
	}
	digest, v, err := intent(bytes.Clone(s.Requested))
	if err != nil {
		return "", refuse("snapshot-invalid", 0, "snapshot")
	}
	if digest != s.ProfileDigest {
		return "", refuse("expected-digest", 0, "snapshot")
	}
	if v != (View{s.Pack, s.Version, s.Mode, s.Model, s.Effort}) {
		return "", refuse("expected-differs", 0, "snapshot")
	}
	if s.Transport == pack.Protocol {
		m := p.modes[s.Mode]
		if kind != "coordinator" || !s.OwnerProtocol || m == nil || m.Class != pack.NoPrompt || m.Vector.Protocol == nil || m.Vector.Protocol.observation == nil || !slices.Contains(m.Vector.Protocol.Profiles, digest) {
			return "", refuse("transport-not-admitted", 0, "snapshot")
		}
	}
	return kind, nil
}

func (m mode) leadUsable() bool {
	return m.Class == pack.Ask || m.Class == pack.Plan || m.Class == pack.AcceptEdits
}
func selection(p *Policy, l pack.Launch, s Snapshot, kind string) *Refusal {
	outside := refuse("selection-outside-policy", 0, "policy")
	lead := false
	for _, entry := range l.PermissionModes {
		m := p.modes[entry.Name]
		if m == nil || entry.Lead && !m.leadUsable() {
			return outside
		}
		if entry.Name == s.Mode && entry.Lead {
			lead = true
		}
	}
	for _, a := range l.Agents {
		if !p.agent(a.Name) {
			return outside
		}
	}
	for _, name := range slices.Sorted(maps.Keys(l.Env)) {
		if !slices.Contains(p.env[name], l.Env[name]) {
			return outside
		}
	}
	for _, slot := range slices.Sorted(maps.Keys(l.Grammars)) {
		g := p.doc.Slots.grammar(slot)
		if g == nil {
			return outside
		}
		if _, err := g.compile(l.Grammars[slot]); err != nil {
			return outside
		}
	}
	if kind == "lead" && !lead {
		return refuse("selection-lead-flag", 0, "policy")
	}
	return nil
}
func (p *Policy) agent(name string) bool {
	return p.doc.Slots.Agent != nil && slices.Contains(p.doc.Slots.Agent.Names, name)
}

// Judge checks selection and expansion at the daemon, then the final spec.
func Judge(p *Policy, l pack.Launch, s Snapshot, spec Spec, intent Intent) (Verdict, *Refusal) {
	kind, r := authenticate(p, s, intent)
	if r != nil {
		return Verdict{}, r
	}
	if r := selection(p, l, s, kind); r != nil {
		return Verdict{}, r
	}
	expected, r := expand(p, l, s, kind)
	if r != nil {
		return Verdict{}, r
	}
	return judgeSpec(p, spec, s, kind, &l, &expected)
}

// Recheck independently checks a received spec with no access to the template.
func Recheck(p *Policy, spec Spec, s Snapshot, intent Intent) (Verdict, *Refusal) {
	kind, r := authenticate(p, s, intent)
	if r != nil {
		return Verdict{}, r
	}
	return judgeSpec(p, spec, s, kind, nil, nil)
}

func judgeSpec(p *Policy, spec Spec, s Snapshot, kind string, l *pack.Launch, expanded *Spec) (Verdict, *Refusal) {
	if r := bounds(spec.Argv, spec.Env, 2); r != nil {
		return Verdict{}, r
	}
	q := newSequence(p, kind, s.Transport, 2)
	pairs := make(map[pack.Slot]string)
	for i, token := range spec.Argv {
		f, code := q.finalForm(i, token)
		if code != "" {
			return Verdict{}, tokenFault(code, 2, i, f, token)
		}
		if r := q.place(f, i, token, false); r != nil {
			return Verdict{}, r
		}
		if f.Slot == "" {
			continue
		}
		v := strings.TrimPrefix(token, string(f.Prefix))
		if code := q.checkValue(f.Slot, v); code != "" {
			return Verdict{}, tokenFault(code, 2, i, f, token)
		}
		if l != nil && l.CheckSlotValue(f.Slot, v) != nil {
			return Verdict{}, tokenFault("value-unadmitted", 2, i, f, token)
		}
		pairs[f.Slot] = v
	}
	for i, name := range slices.Sorted(maps.Keys(spec.Env)) {
		if !slices.Contains(p.env[name], spec.Env[name]) {
			return Verdict{}, refuse("env-refused", 2, location("env", name, i))
		}
	}
	if r := q.requiredForms(); r != nil {
		return Verdict{}, r
	}
	if expanded != nil {
		if r := difference(*expanded, spec); r != nil {
			return Verdict{}, r
		}
	}
	return q.expect(s, spec, pairs)
}

func (q *sequence) checkValue(slot pack.Slot, v string) string {
	if !ceiling.MatchString(v) {
		return "value-outside-ceiling"
	}
	switch slot {
	case pack.PermissionModeSlot:
		m := q.policy.modes[v]
		if m == nil {
			return "value-unadmitted"
		}
		if m.Class == pack.Bypass || m.Class == pack.NoPrompt && q.kind != "coordinator" {
			return "mode-class-refused"
		}
		vec := m.vector(q.transport)
		if vec == nil || vec.observation == nil {
			return "mode-vector-pending"
		}
	case pack.AgentSlot:
		if !q.policy.agent(v) {
			return "value-unadmitted"
		}
	default:
		if !q.policy.doc.Slots.grammar(slot).rx.MatchString(v) {
			return "value-grammar"
		}
	}
	return ""
}

func difference(want, got Spec) *Refusal {
	const code = "template-expansion-differs"
	for i := 0; i < max(len(want.Argv), len(got.Argv)); i++ {
		if i >= len(want.Argv) || i >= len(got.Argv) || got.Argv[i] != want.Argv[i] {
			return refuse(code, 2, argvAt(i))
		}
	}
	names := slices.AppendSeq(slices.Collect(maps.Keys(want.Env)), maps.Keys(got.Env))
	slices.Sort(names)
	for i, k := range slices.Compact(names) {
		a, has := want.Env[k]
		b, exists := got.Env[k]
		if has != exists || a != b {
			return refuse(code, 2, location("env", k, i))
		}
	}
	return nil
}
func (q *sequence) expect(s Snapshot, spec Spec, pairs map[pack.Slot]string) (Verdict, *Refusal) {
	if spec.Role != s.Role {
		return Verdict{}, refuse("expected-role", 3, "snapshot")
	}
	if spec.Transport != s.Transport {
		return Verdict{}, refuse("expected-transport", 3, "snapshot")
	}
	if spec.ProfileDigest != s.ProfileDigest {
		return Verdict{}, refuse("expected-digest", 3, "snapshot")
	}
	for _, slot := range slots {
		v, found := pairs[slot]
		want := s.value(slot)
		at := location("slot", string(slot), 0)
		if want == "" {
			if found {
				return Verdict{}, refuse("expected-unexpected", 3, at)
			}
			continue
		}
		f := q.policy.bySlot[slot]
		if f == nil || !f.admitted(q.kind, q.transport) {
			return Verdict{}, refuse("expected-unplaceable", 3, at)
		}
		if !found {
			return Verdict{}, refuse("expected-missing", 3, at)
		}
		if want != v {
			return Verdict{}, refuse("expected-differs", 3, at)
		}
	}
	m := q.policy.modes[s.Mode] // the required mode passed R25 and R29
	recognizer := ""
	if s.Transport == pack.Terminal {
		recognizer = string(m.Recognizer)
	}
	if recognizer != s.Recognizer {
		return Verdict{}, refuse("expected-recognizer", 3, "snapshot")
	}
	return Verdict{m.Class, recognizer, q.policy.digest}, nil
}

// JudgeFiles checks the renderer's typed views against the independently
// derived inventory. The caller binds each view to the bytes it installed.
func JudgeFiles(p *Policy, s Snapshot, inv Inventory, files []FileView) *Refusal {
	if _, r := identity(p, s); r != nil {
		return r
	}
	expected := make(map[string][]ExpectedFile)
	supplied := make(map[string][]FileView)
	roles := make(map[string]int)
	definitions := 0
	var destinations []string
	for _, f := range inv {
		expected[f.Dest] = append(expected[f.Dest], f)
		roles[f.PathRole]++
		if f.Definition {
			definitions++
		}
		destinations = append(destinations, f.Dest)
	}
	for _, f := range files {
		supplied[f.Dest] = append(supplied[f.Dest], f)
		destinations = append(destinations, f.Dest)
	}
	slices.Sort(destinations)
	for i, dest := range slices.Compact(destinations) {
		at := location("file", dest, i)
		want, got := expected[dest], supplied[dest]
		if dest == "" || len(want) != 1 || len(got) != 1 {
			return refuse("file-refused", 2, at)
		}
		w, f := want[0], got[0]
		row := p.files[w.PathRole]
		if row == nil || f.PathRole != w.PathRole || roles[w.PathRole] != 1 || w.Definition && definitions != 1 {
			return refuse("file-refused", 2, at)
		}
		if f.BytesSHA256 != f.InstalledSHA256 || !shaPattern.MatchString(f.BytesSHA256) {
			return refuse("file-binding", 2, at)
		}
		if code := p.fileEntries(s, w, row, f.Entries); code != "" {
			return refuse(code, 2, at)
		}
	}
	return nil
}

func validPath(path []Comp) bool {
	if path == nil {
		return false
	}
	for _, c := range path {
		switch v := c.(type) {
		case string:
		case int:
			if v < 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func (p *Policy) fileEntries(s Snapshot, w ExpectedFile, row *fileRow, entries []Entry) string {
	const invalid = "file-refused"
	if w.Definition && (w.AgentName != s.Agent || !p.agent(w.AgentName)) {
		return invalid
	}
	for _, e := range entries {
		if !validPath(e.Path) || !scalar(e.Type) {
			return invalid
		}
	}
	ordered := slices.Clone(entries)
	slices.SortFunc(ordered, func(a, b Entry) int { return comparePath(a.Path, b.Path) })
	haveName := false
	requiredCount := 0
	for i, e := range ordered {
		if i > 0 && comparePath(ordered[i-1].Path, e.Path) == 0 {
			return invalid
		}
		if w.Definition {
			if len(e.Path) == 0 {
				return "agent-key-refused"
			}
			key, ok := e.Path[0].(string)
			if !ok || !slices.ContainsFunc(p.doc.Slots.Agent.Keys, func(k keyEvidence) bool { return k.Key == key }) {
				return "agent-key-refused"
			}
			if key == "name" && len(e.Path) == 1 {
				if e.Type != "string" || e.Text != w.AgentName {
					return invalid
				}
				haveName = true
			}
		}
		n, needed := slices.BinarySearchFunc(row.Required, e.Path, func(k requirement, path []Comp) int { return comparePath(k.Path, path) })
		if needed {
			k := row.Required[n]
			if k.Type != e.Type || *k.Text != e.Text {
				return invalid
			}
			requiredCount++
		} else if _, allowed := slices.BinarySearchFunc(row.Allowed, e.Path, comparePath); !allowed {
			return invalid
		}
	}
	if requiredCount != len(row.Required) || w.Definition && !haveName {
		return invalid
	}
	return ""
}
