package launchpolicy

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"ataniq/internal/pack"
)

// Policy is immutable after Decode. All indexes refer to this document alone.
type Policy struct {
	doc        document
	canonical  []byte
	digest     string
	executable *form
	bySlot     map[pack.Slot]*form
	after      map[int]*form
	spellings  []spelling
	modes      map[string]*mode
	env        map[string][]string
	files      map[string]*fileRow
}

type document struct {
	Schema  string    `json:"schema"`
	Pack    pack.Name `json:"pack"`
	Version string    `json:"version"`
	Fixture struct {
		Path string  `json:"path"`
		SHA  *string `json:"sha256"`
	} `json:"fixture"`
	Options []scope   `json:"options"`
	Forms   []form    `json:"forms"`
	Slots   slotRules `json:"slots"`
	Env     []envRow  `json:"env"`
	Refused []refused `json:"refused"`
	Files   []fileRow `json:"files"`
}
type scope struct {
	Scope string `json:"scope"`
	From  int    `json:"from"`
	To    int    `json:"to"`
}
type form struct {
	ID        string       `json:"id"`
	Shape     string       `json:"shape"`
	Token     optionalText `json:"token,omitzero"`
	Prefix    optionalText `json:"prefix,omitzero"`
	Slot      pack.Slot    `json:"slot,omitzero"`
	Separator bool         `json:"separator,omitzero"`
	After     *int         `json:"after_separator_index,omitzero"`
	At        *struct {
		Index *int `json:"index"`
	} `json:"at,omitzero"`
	Kinds      map[string]cardinality `json:"kinds"`
	Transports []pack.Transport       `json:"transports,omitempty"`
	Aliases    []alias                `json:"aliases,omitempty"`
	Evidence   evidence               `json:"evidence"`
}
type cardinality int

const required cardinality = -1

func (c cardinality) limit() int {
	if c == required {
		return 1
	}
	return int(c)
}
func (c *cardinality) UnmarshalJSONFrom(d *jsontext.Decoder) error {
	if d.PeekKind() == '{' {
		var r struct {
			Repeatable int `json:"repeatable"`
		}
		if err := json.UnmarshalDecode(d, &r); err != nil {
			return err
		}
		if r.Repeatable >= 2 && r.Repeatable <= 8 {
			*c = cardinality(r.Repeatable)
			return nil
		}
	} else {
		var s string
		if err := json.UnmarshalDecode(d, &s); err != nil {
			return err
		}
		switch s {
		case "required":
			*c = required
			return nil
		case "optional":
			*c = 1
			return nil
		}
	}
	return errors.New("invalid cardinality")
}
func (c cardinality) MarshalJSON() ([]byte, error) {
	switch c {
	case required:
		return []byte(`"required"`), nil
	case 1:
		return []byte(`"optional"`), nil
	}
	return fmt.Appendf(nil, `{"repeatable":%d}`, c), nil
}

type evidence struct {
	Source string       `json:"source"`
	Scope  optionalText `json:"scope,omitzero"`
	Line   *int         `json:"line,omitzero"`
	Ref    optionalText `json:"ref,omitzero"`
}

// An absent optional string leaves its zero value. A present locator member,
// spelling, or recognizer must contain text, even on a shape that forbids it.
type optionalText string

func (s *optionalText) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		return errors.New("empty optional string")
	}
	*s = optionalText(b)
	return nil
}

type option struct {
	Token  optionalText `json:"token,omitzero"`
	Prefix optionalText `json:"prefix,omitzero"`
}

func (o option) text() string { return string(o.Token + o.Prefix) }
func (o option) matches(token string) bool {
	return string(o.Token) == token && o.Token != "" || o.Prefix != "" && strings.HasPrefix(token, string(o.Prefix))
}

type alias struct {
	option
	Evidence evidence `json:"evidence"`
}
type refused struct {
	option
	Reason string `json:"reason"`
}
type spelling struct {
	option
	form    *form
	refusal string
}
type slotRules struct {
	Permission struct {
		Values []mode `json:"values"`
	} `json:"permission_mode"`
	Agent *struct {
		Names []string      `json:"names"`
		Keys  []keyEvidence `json:"frontmatter_keys"`
	} `json:"agent,omitzero"`
	Model  *grammar `json:"model,omitzero"`
	Effort *grammar `json:"effort,omitzero"`
	Resume *grammar `json:"resume,omitzero"`
}
type keyEvidence struct {
	Key      string   `json:"key"`
	Evidence evidence `json:"evidence"`
}
type grammar struct {
	Grammar string   `json:"grammar"`
	Accept  []string `json:"accept"`
	Reject  []string `json:"reject"`
	rx      *regexp.Regexp
}

func (s slotRules) grammar(slot pack.Slot) *grammar {
	switch slot {
	case pack.ModelSlot:
		return s.Model
	case pack.EffortSlot:
		return s.Effort
	case pack.ResumeSlot:
		return s.Resume
	}
	return nil
}

type mode struct {
	Name       string         `json:"name"`
	Class      pack.ModeClass `json:"class"`
	Recognizer optionalText   `json:"recognizer,omitzero"`
	Basis      string         `json:"basis"`
	Evidence   evidence       `json:"evidence"`
	Vector     struct {
		Terminal *vector `json:"terminal,omitzero"`
		Protocol *vector `json:"protocol,omitzero"`
	} `json:"vector"`
}

func (m mode) vector(t pack.Transport) *vector {
	if t == pack.Protocol {
		return m.Vector.Protocol
	}
	return m.Vector.Terminal
}

type observation struct {
	Fixture   string         `json:"fixture_sha256"`
	Build     string         `json:"build"`
	Transport pack.Transport `json:"transport"`
	Value     string         `json:"value"`
	Sitting   string         `json:"sitting"`
	Profiles  []string       `json:"profiles"`
}
type vector struct{ *observation }

func (v *vector) UnmarshalJSONFrom(d *jsontext.Decoder) error {
	if d.PeekKind() == '{' {
		return json.UnmarshalDecode(d, &v.observation)
	}
	var s string
	if err := json.UnmarshalDecode(d, &s); err != nil {
		return err
	}
	if s != "pending" {
		return errors.New("invalid vector")
	}
	return nil
}
func (v vector) MarshalJSONTo(e *jsontext.Encoder) error {
	if v.observation == nil {
		return e.WriteToken(jsontext.String("pending"))
	}
	return json.MarshalEncode(e, v.observation)
}

type envRow struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}
type fileRow struct {
	Role     string        `json:"path_role"`
	Required []requirement `json:"required"`
	Allowed  [][]Comp      `json:"allowed"`
}
type requirement struct {
	Path []Comp  `json:"path"`
	Type string  `json:"type"`
	Text *string `json:"text"`
}

var members = json.JoinUnmarshalers(json.UnmarshalFromFunc(func(d *jsontext.Decoder, _ any) error {
	if d.PeekKind() == 'n' {
		return errors.New("unexpected null")
	}
	return errors.ErrUnsupported
}), json.UnmarshalFunc(func(b []byte, slot *pack.Slot) error {
	var name string
	if err := json.Unmarshal(b, &name); err != nil {
		return err
	}
	*slot = pack.Slot(name)
	if !slot.Valid() {
		return errors.New("invalid slot")
	}
	return nil
}), json.UnmarshalFromFunc(func(d *jsontext.Decoder, path *[]Comp) error {
	var parts []jsontext.Value
	if err := json.UnmarshalDecode(d, &parts); err != nil {
		return err
	}
	*path = make([]Comp, len(parts))
	for i, part := range parts {
		switch part.Kind() {
		case '"':
			var s string
			if err := json.Unmarshal(part, &s); err != nil {
				return err
			}
			(*path)[i] = s
		case '0':
			var n int
			if err := json.Unmarshal(part, &n); err != nil || n < 0 {
				return errors.New("invalid path index")
			}
			(*path)[i] = n
		default:
			return errors.New("invalid path component")
		}
	}
	return nil
}))

// Decode verifies one complete policy and the exact help bytes it references.
func Decode(data, help []byte) (*Policy, error) {
	if len(data) > 64<<10 {
		return nil, errors.New("policy exceeds 64 KiB")
	}
	d := jsontext.NewDecoder(bytes.NewReader(data))
	p := &Policy{bySlot: make(map[pack.Slot]*form), after: make(map[int]*form), modes: make(map[string]*mode), env: make(map[string][]string), files: make(map[string]*fileRow)}
	if err := json.UnmarshalDecode(d, &p.doc, json.RejectUnknownMembers(true), json.WithUnmarshalers(members)); err != nil {
		return nil, err
	}
	if tail := data[d.InputOffset():]; len(tail) != 0 && !bytes.Equal(tail, []byte("\n")) {
		return nil, errors.New("trailing bytes")
	}
	if err := p.validate(help); err != nil {
		return nil, err
	}
	b, err := json.Marshal(&p.doc, json.Deterministic(true))
	if err != nil {
		return nil, err
	}
	// json/v2 already emits literal UTF-8 and leaves HTML alone. Replace its
	// five short control escapes, consuming each escape pair exactly once.
	for i := 0; i < len(b); i++ {
		if b[i] != '\\' {
			p.canonical = append(p.canonical, b[i])
			continue
		}
		i++
		if n := strings.IndexByte("btnfr", b[i]); n >= 0 {
			p.canonical = fmt.Appendf(p.canonical, `\u%04x`, "\b\t\n\f\r"[n])
		} else {
			p.canonical = append(p.canonical, '\\', b[i])
		}
	}
	p.canonical = append(p.canonical, '\n')
	if len(p.canonical) > 64<<10 {
		return nil, errors.New("canonical policy exceeds 64 KiB")
	}
	p.digest = fmt.Sprintf("%x", sha256.Sum256(p.canonical))
	return p, nil
}
func Canonical(p *Policy) []byte { return bytes.Clone(p.canonical) }
func (p *Policy) Digest() string { return p.digest }

func ordered[T any](rows []T, key func(T) string) bool {
	for i, r := range rows {
		if key(r) == "" || i > 0 && key(rows[i-1]) >= key(r) {
			return false
		}
	}
	return true
}
func sorted[T ~string](s []T) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] >= s[i] {
			return false
		}
	}
	return true
}
func (p *Policy) validate(help []byte) error {
	d := &p.doc
	if _, err := pack.ParseName(string(d.Pack)); err != nil {
		return err
	}
	if d.Schema != "ataniq.launch-policy/1" || !versionPattern.MatchString(d.Version) || d.Fixture.Path == "" || d.Fixture.SHA == nil {
		return errors.New("invalid policy identity or fixture")
	}
	hash := *d.Fixture.SHA
	if hash != "" && hash != fmt.Sprintf("%x", sha256.Sum256(help)) {
		return errors.New("fixture hash differs")
	}
	if d.Options == nil || !ordered(d.Options, func(s scope) string { return s.Scope }) {
		return errors.New("invalid options")
	}
	lines := strings.Split(strings.TrimSuffix(string(help), "\n"), "\n")
	for _, s := range d.Options {
		if s.From < 1 || s.To < s.From || hash != "" && s.To > len(lines) {
			return errors.New("invalid option range")
		}
	}
	check := func(e evidence, spelling string) bool { return p.evidence(e, spelling, lines) }
	if err := p.validateForms(check); err != nil {
		return err
	}
	if err := p.validateSlots(check); err != nil {
		return err
	}
	if d.Env == nil || len(d.Env) > 32 || !ordered(d.Env, func(r envRow) string { return r.Name }) {
		return errors.New("invalid environment rows")
	}
	for _, r := range d.Env {
		if len(r.Values) == 0 || !sorted(r.Values) {
			return errors.New("invalid environment values")
		}
		p.env[r.Name] = r.Values
	}
	if d.Files == nil || len(d.Files) > 12 || !ordered(d.Files, func(r fileRow) string { return r.Role }) {
		return errors.New("invalid file rows")
	}
	for i := range d.Files {
		r := &d.Files[i]
		if r.Required == nil || r.Allowed == nil || len(r.Required)+len(r.Allowed) > 128 {
			return errors.New("invalid file row")
		}
		for j, k := range r.Required {
			if k.Path == nil || !scalar(k.Type) || k.Text == nil || j > 0 && comparePath(r.Required[j-1].Path, k.Path) >= 0 {
				return errors.New("invalid required entry")
			}
		}
		for j, path := range r.Allowed {
			if j > 0 && comparePath(r.Allowed[j-1], path) >= 0 {
				return errors.New("unordered allowed path")
			}
			for _, k := range r.Required {
				if comparePath(path, k.Path) == 0 {
					return errors.New("repeated file path")
				}
			}
		}
		p.files[r.Role] = r
	}
	return nil
}

func scalar(s string) bool {
	switch s {
	case "string", "bool", "int", "float", "null", "empty-object", "empty-array":
		return true
	}
	return false
}
func comparePath(a, b []Comp) int {
	return slices.CompareFunc(a, b, func(x, y Comp) int {
		n, xi := x.(int)
		m, yi := y.(int)
		switch {
		case xi && yi:
			return cmp.Compare(n, m)
		case xi:
			return -1
		case yi:
			return 1
		default:
			return strings.Compare(x.(string), y.(string))
		}
	})
}

func (p *Policy) evidence(e evidence, spelling string, lines []string) bool {
	switch e.Source {
	case "pending":
		return *p.doc.Fixture.SHA == "" && e.Ref == "" && e.Line == nil && e.Scope == ""
	case "doc":
		return e.Ref != "" && e.Line == nil && e.Scope == ""
	case "help":
		if e.Ref != "" || e.Line == nil || *e.Line < 1 || *e.Line > len(lines) {
			return false
		}
		for _, s := range p.doc.Options {
			if s.Scope == string(e.Scope) && s.From <= *e.Line && *e.Line <= s.To {
				return defines(lines[*e.Line-1], spelling)
			}
		}
	}
	return false
}

// A locator names one option definition, including a grouped spelling.
// Stop at the description; words in prose never qualify a spelling.
func defines(line, want string) bool {
	fields := strings.Fields(line)
	for _, field := range fields {
		if !strings.HasPrefix(field, "-") {
			break
		}
		for part := range strings.SplitSeq(field, ",") {
			if !strings.HasPrefix(part, "-") {
				continue
			}
			if part == want {
				return true
			}
			name, _, attached := strings.Cut(part, "=")
			if attached {
				name += "="
			}
			if name == want {
				return true
			}
			if strings.HasPrefix(name, "--[no-]") {
				suffix := strings.TrimPrefix(name, "--[no-]")
				if "--"+suffix == want || "--no-"+suffix == want {
					return true
				}
			}
		}
		if !strings.HasSuffix(field, ",") {
			break
		}
	}
	return false
}

func (f *form) transports(t pack.Transport) bool {
	return len(f.Transports) == 0 || slices.Contains(f.Transports, t)
}
func (f *form) admitted(kind string, t pack.Transport) bool {
	return f.Kinds[kind] != 0 && f.transports(t)
}
func (f *form) option() option { return option{f.Token, f.Prefix} }

type evidenceCheck func(evidence, string) bool

func (p *Policy) validateForms(check evidenceCheck) error {
	d := &p.doc
	if len(d.Forms) > 64 || !ordered(d.Forms, func(f form) string { return f.ID }) {
		return errors.New("invalid form list")
	}
	var sep *form
	for i := range d.Forms {
		f := &d.Forms[i]
		switch f.Shape {
		case "exact":
			if f.Token == "" || f.Prefix != "" || f.Slot != "" {
				return errors.New("invalid exact form")
			}
		case "prefixed":
			name, suffix, ok := strings.Cut(string(f.Prefix), "=")
			if !ok || name == "" || suffix != "" || f.Token != "" || !f.Slot.Valid() || f.After != nil {
				return errors.New("invalid prefixed form")
			}
		case "positional":
			if f.Token != "" || f.Prefix != "" || !f.Slot.Valid() || f.After == nil {
				return errors.New("invalid positional form")
			}
		default:
			return errors.New("invalid shape")
		}
		if len(f.Kinds) == 0 || len(f.Kinds) > 2 {
			return errors.New("invalid kinds")
		}
		for _, k := range slices.Sorted(maps.Keys(f.Kinds)) {
			c := f.Kinds[k]
			if k != "lead" && k != "coordinator" || c > 1 && (f.Slot != "" || f.Separator) || f.Slot == pack.ResumeSlot && c != 1 {
				return errors.New("invalid kind or cardinality")
			}
		}
		if f.Transports != nil {
			if len(f.Transports) == 0 || !sorted(f.Transports) {
				return errors.New("invalid transports")
			}
			for _, t := range f.Transports {
				if t != pack.Terminal && t != pack.Protocol {
					return errors.New("invalid transport")
				}
			}
			if len(f.Transports) == 2 {
				f.Transports = nil
			}
		}
		if f.Separator != (f.Token == "--") || f.Separator && (sep != nil || f.After != nil) {
			return errors.New("invalid separator")
		}
		if f.Separator {
			sep = f
		}
		if f.At != nil {
			if f.At.Index == nil || *f.At.Index < 0 {
				return errors.New("invalid absolute position")
			}
			if *f.At.Index == 0 {
				if p.executable != nil || f.Shape != "exact" || f.Separator || f.After != nil || !bothRequired(f) {
					return errors.New("invalid executable")
				}
				p.executable = f
			}
		}
		if f.After != nil {
			if *f.After < 0 || p.after[*f.After] != nil {
				return errors.New("invalid or ambiguous position")
			}
			p.after[*f.After] = f
		}
		if f.Slot != "" {
			if p.bySlot[f.Slot] != nil {
				return errors.New("two forms on a slot")
			}
			p.bySlot[f.Slot] = f
		}
		name, _, _ := strings.Cut(f.option().text(), "=")
		if (name == "--resume") != (f.Slot == pack.ResumeSlot) || f.Slot == pack.ResumeSlot && f.Prefix != "--resume=" {
			return errors.New("resume spelling reserved")
		}
		if strings.ContainsAny(f.option().text(), "{}") {
			return errors.New("form spelling contains placeholder delimiters")
		}
		if !check(f.Evidence, f.option().text()) {
			return errors.New("invalid form evidence")
		}
		if f.Shape != "positional" {
			if err := p.addSpelling(spelling{f.option(), f, ""}); err != nil {
				return err
			}
		}
		if !ordered(f.Aliases, func(a alias) string { return a.text() }) {
			return errors.New("invalid alias list")
		}
		for _, a := range f.Aliases {
			if !check(a.Evidence, a.text()) {
				return errors.New("invalid alias evidence")
			}
			if err := p.addSpelling(spelling{a.option, nil, "alias-spelling"}); err != nil {
				return err
			}
		}
	}
	modeForm := p.bySlot[pack.PermissionModeSlot]
	if p.executable == nil || modeForm == nil || !bothRequired(modeForm) || len(p.after) > 0 && sep == nil {
		return errors.New("missing executable, required mode or separator")
	}
	for _, f := range d.Forms {
		for _, t := range []pack.Transport{pack.Terminal, pack.Protocol} {
			if f.transports(t) && (!p.executable.transports(t) || !modeForm.transports(t)) {
				return errors.New("missing executable or mode on transport")
			}
		}
		if f.After != nil {
			for _, kind := range []string{"lead", "coordinator"} {
				for _, t := range []pack.Transport{pack.Terminal, pack.Protocol} {
					if f.admitted(kind, t) && !sep.admitted(kind, t) {
						return errors.New("after-separator form lacks admitted separator")
					}
				}
			}
		}
	}
	if d.Refused == nil || !ordered(d.Refused, func(r refused) string { return r.text() }) {
		return errors.New("invalid refused list")
	}
	for _, r := range d.Refused {
		if r.Reason == "" {
			return errors.New("missing refusal reason")
		}
		if err := p.addSpelling(spelling{r.option, nil, "refused-option"}); err != nil {
			return err
		}
	}
	for _, f := range d.Forms {
		if f.Shape != "exact" || f.Separator {
			continue
		}
		fixed, _, _ := strings.Cut(string(f.Token), "=")
		for _, s := range p.spellings {
			if s.form != nil && s.form.Slot == "" {
				continue
			}
			name, _, _ := strings.Cut(s.text(), "=")
			if strings.HasPrefix(name, fixed) {
				return errors.New("fixed spelling shadows reserved option name")
			}
		}
	}
	return nil
}
func bothRequired(f *form) bool {
	return f.Kinds["lead"] == required && f.Kinds["coordinator"] == required
}
func (p *Policy) addSpelling(s spelling) error {
	word := s.text()
	if word == "--" && (s.form == nil || !s.form.Separator) {
		return errors.New("reserved separator spelling")
	}
	if (s.Token == "") == (s.Prefix == "") || len(word) > 128 || strings.ContainsFunc(word, unicode.IsControl) {
		return errors.New("invalid spelling")
	}
	for _, old := range p.spellings {
		if old.matches(word) || s.matches(old.text()) {
			return errors.New("overlapping spellings")
		}
	}
	p.spellings = append(p.spellings, s)
	return nil
}

func (p *Policy) validateSlots(check evidenceCheck) error {
	s := &p.doc.Slots
	if len(s.Permission.Values) == 0 || len(s.Permission.Values) > 64 || !ordered(s.Permission.Values, func(m mode) string { return m.Name }) {
		return errors.New("invalid mode values")
	}
	for i := range s.Permission.Values {
		m := &s.Permission.Values[i]
		if !ceiling.MatchString(m.Name) || !m.Class.Valid() || m.Basis != "documented" && (m.Basis != "undocumented" || m.Class != pack.Bypass) || !check(m.Evidence, string(p.bySlot[pack.PermissionModeSlot].Prefix)+m.Name) {
			return errors.New("invalid mode")
		}
		if p.executable.transports(pack.Terminal) {
			if !recognizerPattern.MatchString(string(m.Recognizer)) {
				return errors.New("invalid recognizer")
			}
		} else if m.Recognizer != "" {
			return errors.New("recognizer on protocol-only policy")
		}
		for _, t := range []pack.Transport{pack.Terminal, pack.Protocol} {
			v := m.vector(t)
			if p.executable.transports(t) != (v != nil) {
				return errors.New("vector transports differ")
			}
			if v == nil || v.observation == nil {
				continue
			}
			if v.Fixture == "" || v.Fixture != *p.doc.Fixture.SHA || v.Build != p.doc.Version || v.Transport != t || v.Value != m.Name || v.Sitting == "" || v.Profiles == nil || (t == pack.Protocol) != (len(v.Profiles) > 0) || !sorted(v.Profiles) {
				return errors.New("invalid vector observation")
			}
			for _, digest := range v.Profiles {
				if !profilePattern.MatchString(digest) {
					return errors.New("invalid observed profile")
				}
			}
		}
		p.modes[m.Name] = m
	}
	if a := s.Agent; a != nil {
		if a.Names == nil || len(a.Names) > 64 || !sorted(a.Names) || a.Keys == nil || !ordered(a.Keys, func(k keyEvidence) string { return k.Key }) {
			return errors.New("invalid agent rules")
		}
		for _, name := range a.Names {
			if !ceiling.MatchString(name) {
				return errors.New("invalid agent name")
			}
		}
		for _, k := range a.Keys {
			if !check(k.Evidence, k.Key) {
				return errors.New("invalid key evidence")
			}
		}
	} else if p.bySlot[pack.AgentSlot] != nil {
		return errors.New("missing agent rules")
	}
	for _, slot := range []pack.Slot{pack.ModelSlot, pack.EffortSlot, pack.ResumeSlot} {
		g := s.grammar(slot)
		if g == nil {
			if p.bySlot[slot] != nil {
				return errors.New("missing grammar")
			}
			continue
		}
		if len(g.Accept) < 2 || len(g.Reject) < 2 || len(g.Accept) > 64 || len(g.Reject) > 64 || !sorted(g.Accept) || !sorted(g.Reject) {
			return errors.New("invalid grammar examples")
		}
		rx, err := g.compile(g.Grammar)
		if err != nil {
			return err
		}
		g.rx = rx
	}
	return nil
}

// CheckSlotValue for a model compiles a declaration grammar, validates both
// anchors and exercises pack's outside-ceiling corpus. Use that same check
// for every policy grammar (including the policy-owned resume grammar).
func (g grammar) compile(pattern string) (*regexp.Regexp, error) {
	l := pack.Launch{Grammars: map[pack.Slot]string{pack.ModelSlot: pattern}}
	if err := l.CheckSlotValue(pack.ModelSlot, g.Accept[0]); err != nil {
		return nil, err
	}
	rx, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	for _, v := range g.Accept {
		if !ceiling.MatchString(v) || !rx.MatchString(v) {
			return nil, errors.New("grammar rejects accepted example")
		}
	}
	for _, v := range g.Reject {
		if rx.MatchString(v) {
			return nil, errors.New("grammar admits rejected example")
		}
	}
	return rx, nil
}
