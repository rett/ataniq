package all_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	lp "ataniq/internal/launchpolicy"
	"ataniq/internal/pack"
	"ataniq/packs/all"
	"ataniq/packs/holder"
)

type build struct {
	name                        pack.Name
	version, older, captureHash string
}

var builds = []build{
	{"claude", "2.1.332", "2.1.325", "b296ad20ed563e8c57033bd8570724d63a508dd5a7c1de126e95edcbc73461f3"},
	{"codex", "0.172.0", "0.169", "01d84a04796243348f9ae37cad9b4693451ef85aa444bb002583f97f600cb991"},
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (b build) load(t *testing.T) ([]byte, []byte, *lp.Policy) {
	t.Helper()
	doc, help, ok := holder.LaunchPolicy(b.name, b.version)
	if !ok {
		t.Fatal("missing embedded build", b)
	}
	p, err := lp.Decode(doc, help)
	if err != nil {
		t.Fatal(err)
	}
	return doc, help, p
}

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

// A new or orphaned artifact must be listed and qualified in this package.
func TestLaunchPolicyInventory(t *testing.T) {
	for _, pattern := range []string{"../*/launchpolicy/*.json", "../*/launchpolicy/*.help.txt", "testdata/launchpolicy/*/*.golden"} {
		got, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		var want []string
		for _, b := range builds {
			path := strings.Replace(pattern, "*", string(b.name), 1)
			want = append(want, strings.Replace(path, "*", b.version, 1))
		}
		slices.Sort(want)
		if !slices.Equal(want, got) {
			t.Fatalf("%s: got %v, want %v", pattern, got, want)
		}
	}
}

func TestCommittedLaunchPolicies(t *testing.T) {
	r := pack.New()
	if err := all.Register(r); err != nil {
		t.Fatal(err)
	}
	for _, b := range builds {
		t.Run(string(b.name), func(t *testing.T) {
			doc, help, p := b.load(t)
			root := filepath.Join("..", string(b.name))
			dir := filepath.Join(root, "launchpolicy")
			if !bytes.Equal(doc, read(t, filepath.Join(dir, b.version+".json"))) ||
				!bytes.Equal(help, read(t, filepath.Join(dir, b.version+".help.txt"))) {
				t.Fatal("holder listing differs from the pack")
			}
			if bytes.Contains(bytes.ToLower(help), []byte("stand-in")) || digest(help) != b.captureHash {
				t.Fatal("fixture differs from the captured help")
			}
			d := parse(t, doc)
			if d.Pack != b.name || d.Version != b.version || d.Fixture.Path != b.version+".help.txt" || d.Fixture.SHA != "" {
				t.Fatal("wrong build, fixture or recording state")
			}
			for _, m := range d.Slots.Permission.Values {
				if len(m.Vector) != 1 || string(m.Vector["terminal"]) != `"pending"` {
					t.Fatal("unexpected observation", m)
				}
			}
			golden := read(t, filepath.Join("testdata", "launchpolicy", string(b.name), b.version+".golden"))
			if !bytes.Equal(lp.Canonical(p), golden) || p.Digest() != digest(golden) {
				t.Fatal("canonical launch policy differs from golden")
			}
			t.Logf("launch_policy_digest=%s", p.Digest())
			full, ok := r.Lookup(b.name)
			if !ok {
				t.Fatal("missing registration")
			}
			var versions []string
			for _, row := range full.Manifest().Builds {
				versions = append(versions, row.Version)
				if row.Version == b.version {
					continue
				}
				for _, version := range append(slices.Clone(row.Aliases), row.Version) {
					if _, _, ok := holder.LaunchPolicy(b.name, version); ok {
						t.Fatal("unexpected policy", version)
					}
					_, ref := lp.Recheck(nil, lp.Spec{}, lp.Snapshot{Pack: b.name, Version: row.Version}, nil)
					if ref == nil || ref.Error() != "launch refused [pack-launch-policy-missing] stage=0 at=policy" {
						t.Fatal(ref)
					}
				}
			}
			if !slices.Contains(versions, b.older) || !slices.Contains(versions, b.version) {
				t.Fatal(versions)
			}
			_, ref := lp.Recheck(p, lp.Spec{}, lp.Snapshot{Pack: b.name, Version: b.version}, nil)
			if ref == nil || ref.Code != "policy-unrecorded" {
				t.Fatal(ref)
			}
			// Boundary (i), (iv): a declaration can neither carry nor select a policy.
			manifest := read(t, filepath.Join(root, pack.ManifestPath))
			for _, docs := range []map[string][]byte{
				{pack.ManifestPath: manifest, "launchpolicy/" + b.version + ".json": doc},
				{pack.ManifestPath: bytes.Replace(manifest, []byte(`"builds":`), []byte(`"launch_policy":"replacement","builds":`), 1)},
			} {
				if _, err := pack.DecodeDeclaration(docs, pack.BindLayout{}); err == nil {
					t.Fatal("declaration supplied a policy")
				}
			}
			// With the original bytes and hash, qualification next encounters
			// pending evidence. A prose-only change must fail at the hash first.
			d.Fixture.SHA = b.captureHash
			recorded := wire(t, d)
			if _, err := lp.Decode(recorded, help); err == nil || !strings.Contains(err.Error(), "evidence") {
				t.Fatalf("recording with pending evidence: %v", err)
			}
			changed := bytes.Clone(help)
			changed[0] ^= 1
			if _, err := lp.Decode(recorded, changed); err == nil || err.Error() != "fixture hash differs" {
				t.Fatalf("changed fixture: %v", err)
			}
		})
	}
}

// The matcher validates the full schema. This view preserves its data while
// allowing the qualification checks to inspect the reviewed fields.
type locator struct {
	Source string `json:"source"`
	Scope  string `json:"scope,omitempty"`
	Line   int    `json:"line,omitempty"`
	Ref    string `json:"ref,omitempty"`
}
type spelling struct {
	Token  string `json:"token,omitempty"`
	Prefix string `json:"prefix,omitempty"`
}

func (s spelling) word() string { return s.Token + s.Prefix }

type alias struct {
	spelling
	Evidence locator `json:"evidence"`
}
type form struct {
	alias
	ID         string            `json:"id"`
	Shape      string            `json:"shape"`
	Slot       string            `json:"slot,omitempty"`
	At         json.RawMessage   `json:"at,omitempty"`
	Kinds      map[string]string `json:"kinds"`
	Transports []string          `json:"transports"`
	Aliases    []alias           `json:"aliases,omitempty"`
}
type mode struct {
	Name       string                     `json:"name"`
	Class      pack.ModeClass             `json:"class"`
	Recognizer string                     `json:"recognizer"`
	Basis      string                     `json:"basis"`
	Evidence   locator                    `json:"evidence"`
	Vector     map[string]json.RawMessage `json:"vector"`
}
type key struct {
	Key      string  `json:"key"`
	Evidence locator `json:"evidence"`
}
type scope struct {
	Scope string `json:"scope"`
	From  int    `json:"from"`
	To    int    `json:"to"`
}
type document struct {
	Schema  string    `json:"schema"`
	Pack    pack.Name `json:"pack"`
	Version string    `json:"version"`
	Fixture struct {
		Path string `json:"path"`
		SHA  string `json:"sha256"`
	} `json:"fixture"`
	Options []scope `json:"options"`
	Forms   []form  `json:"forms"`
	Slots   struct {
		Permission struct {
			Values []mode `json:"values"`
		} `json:"permission_mode"`
		Agent *struct {
			Names []string `json:"names"`
			Keys  []key    `json:"frontmatter_keys"`
		} `json:"agent,omitempty"`
		Model  json.RawMessage `json:"model"`
		Effort json.RawMessage `json:"effort,omitempty"`
		Resume json.RawMessage `json:"resume,omitempty"`
	} `json:"slots"`
	Env     json.RawMessage `json:"env"`
	Refused []struct {
		spelling
		Reason string `json:"reason"`
	} `json:"refused"`
	Files json.RawMessage `json:"files"`
}

func parse(t *testing.T, raw []byte) document {
	t.Helper()
	var d document
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}
func wire(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func (d *document) locators(visit func(string, *locator)) {
	var modePrefix string
	for i := range d.Forms {
		f := &d.Forms[i]
		visit(f.word(), &f.Evidence)
		for j := range f.Aliases {
			a := &f.Aliases[j]
			visit(a.word(), &a.Evidence)
		}
		if f.Slot == "permission_mode" {
			modePrefix = f.Prefix
		}
	}
	for i := range d.Slots.Permission.Values {
		m := &d.Slots.Permission.Values[i]
		visit(modePrefix+m.Name, &m.Evidence)
	}
	if d.Slots.Agent != nil {
		for i := range d.Slots.Agent.Keys {
			k := &d.Slots.Agent.Keys[i]
			visit(k.Key, &k.Evidence)
		}
	}
}

// Capture format: option definitions start in the option column. Claude
// wraps prose at column 41 (column 7 for descriptions below a definition);
// Codex describes options at column 11, including dash-led value lists.
var optionSyntax = regexp.MustCompile(`^(--(?:\[no-\])?[A-Za-z0-9][A-Za-z0-9-]*|-[A-Za-z0-9])(?:(=| )?(<[A-Za-z0-9_.=|-]+>|\[[A-Za-z0-9_|.-]+\]|[A-Z][A-Z0-9_]*)(?:\.\.\.)?)?$`)

func definition(line string) ([]string, error) {
	head, _, _ := strings.Cut(strings.TrimSpace(line), "  ")
	var out []string
	for _, part := range strings.Split(head, ",") {
		m := optionSyntax.FindStringSubmatch(strings.TrimSpace(part))
		if m == nil {
			return nil, fmt.Errorf("unknown definition syntax %q", part)
		}
		names := []string{m[1]}
		if suffix, ok := strings.CutPrefix(m[1], "--[no-]"); ok {
			names = []string{"--" + suffix, "--no-" + suffix}
		}
		for _, n := range names {
			if m[2] == "=" {
				n += "="
			}
			out = append(out, n)
		}
	}
	return out, nil
}

func inventory(d document, help []byte) error {
	counted := map[string]bool{}
	for _, f := range d.Forms {
		counted[f.word()] = true
		for _, a := range f.Aliases {
			counted[a.word()] = true
		}
	}
	for _, r := range d.Refused {
		counted[r.word()] = true
	}
	lines := strings.Split(strings.TrimSuffix(string(help), "\n"), "\n")
	ranges := map[string]scope{}
	for _, s := range d.Options {
		if s.From < 1 || s.To < s.From || s.To > len(lines) {
			return fmt.Errorf("invalid range %v", s)
		}
		ranges[s.Scope] = s
	}
	type entry struct {
		scope string
		words []string
	}
	defs := map[int]entry{}
	active := false
	name := "main"
	seen := map[string]bool{}
	for i, line := range lines {
		switch line {
		case "=== codex exec --help ===":
			name = "exec"
			active = false
		case "Options:":
			active = true
			seen[name] = true
		case "Commands:", "Arguments:":
			active = false
		}
		left := strings.TrimLeft(line, " ")
		indent := len(line) - len(left)
		if !active || !strings.HasPrefix(left, "-") || indent >= 10 || d.Pack == "claude" && indent >= 6 {
			continue
		}
		words, err := definition(line)
		if err != nil {
			return fmt.Errorf("%s line %d: %w", name, i+1, err)
		}
		s, ok := ranges[name]
		if !ok || i+1 < s.From || i+1 > s.To {
			return fmt.Errorf("%s line %d outside %s", words, i+1, name)
		}
		defs[i+1] = entry{name, words}
		for _, w := range words {
			if !counted[w] {
				return fmt.Errorf("unaccounted %s in %s line %d", w, name, i+1)
			}
		}
	}
	if len(ranges) != len(seen) {
		return fmt.Errorf("scope inventory differs: %v / %v", ranges, seen)
	}
	var faults []error
	d.locators(func(w string, e *locator) {
		switch e.Source {
		case "help":
			row := defs[e.Line]
			if row.scope != e.Scope || !slices.Contains(row.words, w) {
				faults = append(faults, fmt.Errorf("wrong help locator for %s at %s line %d", w, e.Scope, e.Line))
			}
		case "pending":
			if d.Fixture.SHA != "" {
				faults = append(faults, fmt.Errorf("pending evidence for %s", w))
			}
		case "doc":
			if strings.TrimSpace(e.Ref) == "" {
				faults = append(faults, fmt.Errorf("empty documentation for %s", w))
			}
		default:
			faults = append(faults, fmt.Errorf("unresolved evidence for %s", w))
		}
	})
	return errors.Join(faults...)
}

// These lists belong to the qualification test, independently of pack data.
var forbiddenKeys = map[pack.Name][]string{
	"claude": {"permissionMode", "permissions", "mode", "tools", "allowedTools", "disallowedTools", "grants", "hooks", "mcpServers", "mcp"},
	"codex":  {"approval_policy", "approvals_reviewer", "sandbox_mode", "permissions", "permission_profile", "tools", "grants", "hooks", "mcp_servers", "mcp"},
}

func frontmatter(d document) error {
	if d.Slots.Agent != nil {
		for _, k := range d.Slots.Agent.Keys {
			if slices.Contains(forbiddenKeys[d.Pack], k.Key) {
				return fmt.Errorf("forbidden agent key %s", k.Key)
			}
		}
	}
	return nil
}

func TestHelpInventory(t *testing.T) {
	for _, b := range builds {
		t.Run(string(b.name), func(t *testing.T) {
			raw, help, _ := b.load(t)
			d := parse(t, raw)
			if err := inventory(d, help); err != nil {
				t.Fatal(err)
			}
			if err := frontmatter(d); err != nil {
				t.Fatal(err)
			}
			d.locators(func(w string, e *locator) {
				if e.Source == "doc" {
					t.Logf("review %s: %s", w, e.Ref)
				}
			})
			for _, tc := range []struct{ line, missing string }{
				{"-j,--new=<v>", "-j"}, {"--model,--new=<v>", "--new="}, {"--[no-]model", "--no-model"}, {"--bad/#", "syntax"},
			} {
				mut := bytes.Replace(help, []byte("Options:\n"), []byte("Options:\n  "+tc.line+"\n"), 1)
				if err := inventory(d, mut); err == nil || !strings.Contains(err.Error(), tc.missing) {
					t.Fatalf("unchecked %s: %v", tc.line, err)
				}
			}
			// A correct fixture hash cannot make a locator on the wrong
			// definition valid. The other pending locators stay pending.
			d.Fixture.SHA = digest(help)
			f := &d.Forms[slices.IndexFunc(d.Forms, func(f form) bool { return f.ID == "strict" })]
			saved := f.Evidence
			f.Evidence.Line = d.Options[len(d.Options)-1].From
			if err := inventory(d, help); err == nil || !strings.Contains(err.Error(), "wrong help locator for "+f.Token) {
				t.Fatal("accepted another option's line")
			}
			f.Evidence = saved
			if b.name == "codex" {
				f.Evidence.Scope = "exec"
				if err := inventory(d, help); err == nil || !strings.Contains(err.Error(), "wrong help locator for "+f.Token) {
					t.Fatal("accepted another scope")
				}
			}
			d = parse(t, raw)
			for i := range d.Forms {
				if d.Forms[i].Slot == "permission_mode" {
					d.Forms[i].Kinds["lead"] = "optional"
				}
			}
			if _, err := lp.Decode(wire(t, d), help); err == nil {
				t.Fatal("admitted optional lead mode")
			}
		})
	}
	for _, tc := range []struct {
		line string
		want []string
	}{
		{"-m,--mode=<v>", []string{"-m", "--mode="}}, {"-m<MODEL>, --model MODEL", []string{"-m", "--model"}}, {"--[no-]quiet", []string{"--quiet", "--no-quiet"}},
	} {
		words, err := definition(tc.line)
		if err != nil || !slices.Equal(words, tc.want) {
			t.Fatal(tc, words, err)
		}
	}
}

func TestHelpModeClasses(t *testing.T) {
	// Reviewed against the permission option's choices and behavior in each capture.
	want := map[pack.Name]map[string]pack.ModeClass{
		"claude": {"acceptEdits": pack.Bypass, "auto": pack.Bypass, "bypassPermissions": pack.Bypass, "dontAsk": pack.Bypass, "manual": pack.Bypass, "plan": pack.Bypass},
		"codex":  {"never": pack.NoPrompt, "on-request": pack.Bypass},
	}
	for _, b := range builds {
		raw, _, p := b.load(t)
		d := parse(t, raw)
		var prefix string
		for _, f := range d.Forms {
			if f.Slot == "permission_mode" {
				prefix = f.Prefix
			}
		}
		got := map[string]pack.ModeClass{}
		for _, m := range d.Slots.Permission.Values {
			// T58 with committed bytes: R14 takes precedence. The independent
			// class table below guards the lead exclusion before recording.
			s := lp.Snapshot{Pack: b.name, Version: b.version, Role: "demo/lead", Transport: pack.Terminal, Mode: m.Name, Recognizer: m.Recognizer}
			s.Requested = wire(t, lp.View{Pack: b.name, Version: b.version, Mode: m.Name})
			s.ProfileDigest = digest(s.Requested)
			spec := lp.Spec{Argv: []string{string(b.name), prefix + m.Name}, Role: s.Role, Transport: s.Transport, ProfileDigest: s.ProfileDigest}
			_, ref := lp.Recheck(p, spec, s, intent)
			line(t, ref, "policy-unrecorded", 0, "policy")
			got[m.Name] = m.Class
			if (m.Basis == "undocumented") != (b.name == "claude") {
				t.Fatalf("%s %s basis %s", b.name, m.Name, m.Basis)
			}
		}
		if !maps.Equal(got, want[b.name]) {
			t.Fatalf("%s mode classes: %v", b.name, got)
		}
	}
}

// R47. Only the in-memory copy is recorded. Keep line numbers of real
// definitions stable, and label the additional evidence so it cannot be
// mistaken for an attended install. Preserve all forms, classes and grammars.
func synthetic(t *testing.T, b build) (document, []byte) {
	t.Helper()
	raw, help, original := b.load(t)
	d := parse(t, raw)
	roundTrip, err := lp.Decode(wire(t, d), help)
	if err != nil || roundTrip.Digest() != original.Digest() {
		t.Fatalf("qualification view lost policy data: %v", err)
	}
	help = append(bytes.Clone(help), []byte("\nStand-in help for synthetic matcher tests only.\n")...)
	d.Fixture.SHA = digest(help)
	d.locators(func(word string, e *locator) {
		if e.Source == "pending" {
			*e = locator{Source: "doc", Ref: "stand-in/" + word}
		}
	})
	for i := range d.Slots.Permission.Values {
		m := &d.Slots.Permission.Values[i]
		m.Vector["terminal"] = wire(t, map[string]any{"fixture_sha256": d.Fixture.SHA, "build": b.version, "transport": "terminal", "value": m.Name, "sitting": "synthetic", "profiles": []string{}})
	}
	return d, help
}
func decoded(t *testing.T, d document, help []byte) *lp.Policy {
	t.Helper()
	p, err := lp.Decode(wire(t, d), help)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

type launchCase struct {
	b                       build
	p                       *lp.Policy
	l                       pack.Launch
	s                       lp.Snapshot
	prefix, strict, refused string
}

func intent(raw []byte) (string, lp.View, error) {
	var v lp.View
	err := json.Unmarshal(raw, &v)
	return digest(raw), v, err
}
func (x *launchCase) bind(t *testing.T) {
	x.s.Requested = wire(t, lp.View{Pack: x.b.name, Version: x.s.Version, Mode: x.s.Mode, Model: x.s.Model, Effort: x.s.Effort})
	x.s.ProfileDigest = digest(x.s.Requested)
}
func baseline(t *testing.T, b build) launchCase {
	t.Helper()
	d, help := synthetic(t, b)
	x := launchCase{b: b, p: decoded(t, d, help)}
	x.s = lp.Snapshot{Pack: b.name, Version: b.version, Role: "demo/coord/f1", Transport: pack.Terminal}
	x.l.Grammars = map[pack.Slot]string{pack.ModelSlot: "^[a-z][a-z0-9.-]{0,63}$"}
	if b.name == "claude" {
		x.prefix, x.strict, x.refused = "--permission-mode=", "--strict-mcp-config", "--dangerously-skip-permissions"
		x.s.Mode, x.s.Recognizer, x.s.Agent = "manual", "manual", "reviewer"
		x.l.Agents = []pack.Agent{{Name: "reviewer", Builtin: true}}
		x.l.Grammars[pack.EffortSlot] = "^(low|medium|high|xhigh|max)$"
	} else {
		x.prefix, x.strict, x.refused = "--ask-for-approval=", "--strict-config", "--dangerously-bypass-approvals-and-sandbox"
		x.s.Mode, x.s.Recognizer = "never", "never"
	}
	x.l.Argv = []string{string(b.name), x.prefix + "{permission_mode}", "--model={model}"}
	if b.name == "claude" {
		x.l.Argv = append(x.l.Argv, "--agent={agent}", "--effort={effort}", "--resume={resume}")
	}
	for _, m := range d.Slots.Permission.Values {
		x.l.PermissionModes = append(x.l.PermissionModes, pack.PermissionMode{Name: m.Name})
	}
	x.bind(t)
	return x
}

func (x launchCase) expand(t *testing.T) lp.Spec {
	t.Helper()
	spec, r := lp.Expand(x.p, x.l, x.s)
	if r != nil {
		t.Fatal(r)
	}
	return spec
}
func (x launchCase) judge(spec lp.Spec) *lp.Refusal {
	_, r := lp.Judge(x.p, x.l, x.s, spec, intent)
	return r
}
func (x launchCase) recheck(t *testing.T, spec lp.Spec) *lp.Refusal {
	t.Helper()
	var received lp.Spec
	if err := json.Unmarshal(wire(t, spec), &received); err != nil {
		t.Fatal(err)
	}
	_, r := lp.Recheck(x.p, received, x.s, intent)
	return r
}

// Synthetic native ID, grammar-valid on Claude. The matcher must never
// disclose its bytes, even when a refusal echoes an unrelated slot.
const nativeID = "00000000-0000-4000-8000-000000731905"

func line(t *testing.T, r *lp.Refusal, code string, stage int, at string, value ...string) {
	t.Helper()
	if code == "" {
		if r != nil {
			t.Fatal(r)
		}
		return
	}
	want := fmt.Sprintf("launch refused [pack-launch-%s] stage=%d at=%s", code, stage, at)
	if len(value) == 1 {
		want += " value=" + strconv.Quote(value[0])
	}
	if r == nil || r.Error() != want {
		t.Fatalf("got %v, want %s", r, want)
	}
	msg := r.Error()
	if len(msg) > 200 || !utf8.ValidString(msg) || strings.ContainsAny(msg, "\n\r\x00\x7f") || strings.Contains(msg, nativeID) || strings.Contains(msg, "r731905") || strings.Contains(msg, "private-env") {
		t.Fatal("unsafe refusal")
	}
}

func TestRealTerminalModes(t *testing.T) {
	for _, b := range builds {
		t.Run(string(b.name), func(t *testing.T) {
			x := baseline(t, b)
			raw, _, _ := b.load(t)
			for _, m := range parse(t, raw).Slots.Permission.Values {
				x.s.Recognizer = m.Recognizer
				x.s.Mode = m.Name
				for _, role := range []string{"demo/lead", "demo/coord/f1"} {
					x.s.Role = role
					x.bind(t)
					spec := x.expand(t)
					// T9, T28, T63 and the recognizer half of T66.
					if role == "demo/lead" {
						line(t, x.judge(spec), "selection-lead-flag", 0, "policy")
						bad := x
						bad.l.PermissionModes = []pack.PermissionMode{{Name: m.Name, Lead: true}}
						line(t, bad.judge(spec), "selection-outside-policy", 0, "policy")
					}
					if b.name == "codex" && role == "demo/coord/f1" && m.Name == "never" {
						v, r := lp.Judge(x.p, x.l, x.s, spec, intent)
						line(t, r, "", 0, "")
						line(t, x.recheck(t, spec), "", 0, "")
						if v.Class != pack.NoPrompt || v.Recognizer != "never" || v.PolicyDigest != x.p.Digest() {
							t.Fatal(v)
						}
					} else {
						line(t, x.recheck(t, spec), "mode-class-refused", 2, "argv[1]", m.Name)
						if role == "demo/coord/f1" {
							line(t, x.judge(spec), "mode-class-refused", 2, "argv[1]", m.Name)
						}
					}
				}
			}
		})
	}
}

func TestRealTemplates(t *testing.T) {
	for _, b := range builds {
		t.Run(string(b.name), func(t *testing.T) {
			x := baseline(t, b)
			spec := x.expand(t)
			bin, mode := string(b.name), x.prefix+"{permission_mode}"
			literal := x.prefix + x.s.Mode
			other := "on-request"
			if b.name == "claude" {
				other = "plan"
			}
			alias := strings.TrimSuffix(x.prefix, "=")
			for _, tc := range []struct {
				name     string
				argv     []string
				code, at string
				detail   []string
			}{
				{"T10 missing mode", []string{bin, x.strict}, "required-missing", "form[mode]", nil},
				{"T11 literal equals request", []string{bin, literal}, "literal-in-slot", "argv[1]", []string{x.s.Mode}},
				{"T11 literal differs", []string{bin, x.prefix + other}, "literal-in-slot", "argv[1]", []string{other}},
				{"T14 separate spelling", []string{bin, alias, other}, "alias-spelling", "argv[1]", nil},
				{"T15 wrong mode slot", []string{bin, x.prefix + "{model}"}, "slot-mismatch", "argv[1]", []string{"{model}"}},
				{"T15 wrong model slot", []string{bin, "--model={permission_mode}"}, "slot-mismatch", "argv[1]", []string{"{permission_mode}"}},
				{"T16 repeated mode", []string{bin, mode, mode}, "form-repeated", "argv[2]", []string{"{permission_mode}"}},
				{"T17 literal first", []string{bin, literal, mode}, "literal-in-slot", "argv[1]", []string{x.s.Mode}},
				{"T18 fixed twice", []string{bin, mode, x.strict, x.strict}, "form-repeated", "argv[3]", []string{x.strict}},
				{"T18 repeatable absent", []string{bin, mode, "--tag", "--tag"}, "unknown-token", "argv[2]", nil},
				{"T19 bypass", []string{bin, mode, x.refused}, "refused-option", "argv[2]", nil},
				{"T19 attached", []string{bin, mode, "--add-dir=private"}, "refused-option", "argv[2]", nil},
				{"T20 abbreviation", []string{bin, mode, "--mod={permission_mode}"}, "unknown-token", "argv[2]", nil},
				{"T20 unknown", []string{bin, mode, "--zzz"}, "unknown-token", "argv[2]", nil},
				{"T21 wrong index", []string{x.strict, bin, mode}, "form-position", "argv[0]", []string{x.strict}},
				{"T21 repeated executable", []string{bin, mode, bin}, "form-position", "argv[2]", []string{bin}},
				{"T33 unsupported positional model", []string{bin, mode, "--", "{model}"}, "unknown-token", "argv[2]", nil},
				{"T34 repeated separator", []string{bin, mode, "--", "{model}", "--"}, "unknown-token", "argv[2]", nil},
				{"T36 extra positional", []string{bin, mode, "--", "{model}", "extra"}, "unknown-token", "argv[2]", nil},
				{"T38 repeated positional", []string{bin, mode, "--", "{model}", "{model}"}, "unknown-token", "argv[2]", nil},
				{"T38a separator with omitted model", []string{bin, mode, "--", "{model}"}, "unknown-token", "argv[2]", nil},
			} {
				t.Run(tc.name, func(t *testing.T) {
					y := x
					y.l.Argv = slices.Clone(tc.argv)
					if b.name == "claude" {
						y.l.Argv = append(y.l.Argv, "--agent={agent}")
					}
					line(t, y.judge(spec), tc.code, 1, tc.at, tc.detail...)
				})
			}
			short := "-a"
			if b.name == "claude" {
				short = "-r"
			}
			for _, words := range [][]string{{short + "=" + nativeID}, {short, nativeID}} {
				y := x
				y.l.Argv = append([]string{bin}, words...)
				line(t, y.judge(spec), "alias-spelling", 1, "argv[1]")
			}
			// T22, T23, T60. Codex has no agent form; Claude requires one on coordinators.
			y := x
			if b.name == "claude" {
				y.l.Argv = []string{bin, mode}
				line(t, y.judge(spec), "required-missing", 1, "form[agent]")
				y = x
				y.s.Agent = ""
				line(t, y.judge(spec), "required-missing", 1, "slot[agent]")
				d, h := synthetic(t, b)
				for i := range d.Forms {
					if d.Forms[i].Slot == "agent" {
						delete(d.Forms[i].Kinds, "lead")
					}
				}
				y = x
				y.p = decoded(t, d, h)
				y.s.Role = "demo/lead"
				_, r := lp.Expand(y.p, y.l, y.s)
				line(t, r, "form-not-admitted", 1, "argv[3]")
				d, h = synthetic(t, b)
				for i := range d.Forms {
					if d.Forms[i].Slot == "resume" {
						delete(d.Forms[i].Kinds, "coordinator")
					}
				}
				y = x
				y.p = decoded(t, d, h)
				y.s.Resume = nativeID
				line(t, y.judge(spec), "form-not-admitted", 1, "argv[5]")
			} else {
				y.l.Argv = append(slices.Clone(x.l.Argv), "--agent={agent}")
				line(t, y.judge(spec), "unknown-token", 1, "argv[3]")
			}
			y = x
			y.s.Mode = ""
			y.bind(t)
			line(t, y.judge(spec), "required-missing", 1, "slot[permission_mode]")
			y.s.Role = "demo/lead"
			line(t, y.judge(spec), "selection-lead-flag", 0, "policy")
		})
	}
}

func TestRealExpandedValues(t *testing.T) {
	for _, b := range builds {
		t.Run(string(b.name), func(t *testing.T) {
			x := baseline(t, b)
			spec := x.expand(t)
			// Put the operand under test before the mode. This also exercises
			// Claude's value checks, since no Claude mode can pass stage 2.
			beforeMode := func(token string) lp.Spec {
				s := spec
				s.Argv = append([]string{string(b.name), token}, spec.Argv[1:]...)
				return s
			}
			fields := map[string]func(*lp.Snapshot, string){
				"permission_mode": func(s *lp.Snapshot, v string) { s.Mode = v }, "model": func(s *lp.Snapshot, v string) { s.Model = v },
				"effort": func(s *lp.Snapshot, v string) { s.Effort = v }, "agent": func(s *lp.Snapshot, v string) { s.Agent = v },
				"resume": func(s *lp.Snapshot, v string) { s.Resume = v },
			}
			prefixes := map[string]string{"permission_mode": x.prefix, "model": "--model="}
			if b.name == "claude" {
				prefixes["effort"] = "--effort="
				prefixes["agent"] = "--agent="
				prefixes["resume"] = "--resume="
			}
			for slot, set := range fields {
				for _, value := range []string{"-x", "--yes", "--", "v\n", "v ", "a=b", "a,b", "a/b", `a"b`, "é", x.prefix + x.s.Mode} {
					y := x
					set(&y.s, value)
					line(t, y.judge(spec), "snapshot-invalid", 0, "snapshot")
					if prefix, ok := prefixes[slot]; ok {
						var detail []string
						if slot != "resume" {
							detail = []string{value}
						}
						line(t, x.recheck(t, beforeMode(prefix+value)), "value-outside-ceiling", 2, "argv[1]", detail...)
					}
				}
			}
			// T25/26/27/32, grammar endpoints and membership.
			for _, n := range []int{32, 33, 64} {
				y := x
				y.s.Model = strings.Repeat("a", n)
				y.bind(t)
				// Model first so a passing grammar reaches the real mode gate.
				y.l.Argv = slices.Clone(x.l.Argv)
				y.l.Argv[1], y.l.Argv[2] = y.l.Argv[2], y.l.Argv[1]
				if b.name == "claude" {
					line(t, y.judge(y.expand(t)), "mode-class-refused", 2, "argv[2]", y.s.Mode)
				} else {
					line(t, y.judge(y.expand(t)), "", 0, "")
				}
			}
			line(t, x.recheck(t, beforeMode("--model=Gpt")), "value-grammar", 2, "argv[1]", "Gpt")
			if b.name == "claude" {
				line(t, x.recheck(t, beforeMode("--effort=extra")), "value-grammar", 2, "argv[1]", "extra")
				line(t, x.recheck(t, beforeMode("--agent=other")), "value-unadmitted", 2, "argv[1]", "other")
			}
			y := x
			y.s.Mode = "unknown"
			y.bind(t)
			line(t, y.recheck(t, beforeMode(x.prefix+"unknown")), "value-unadmitted", 2, "argv[1]", "unknown")
			line(t, y.judge(y.expand(t)), "value-unadmitted", 2, "argv[1]", "unknown")
			y.s.Role = "demo/lead"
			line(t, y.judge(spec), "selection-lead-flag", 0, "policy")
			line(t, x.recheck(t, beforeMode(x.prefix)), "value-outside-ceiling", 2, "argv[1]", "")
			// T34/35/37: there is no separator or positional form in either pack.
			for _, token := range []string{"--", "model-id", "r731905", "--resume:r731905"} {
				line(t, x.recheck(t, beforeMode(token)), "unknown-token", 2, "argv[1]")
			}
			// T31 checks the bounds before an earlier token fault.
			bad := beforeMode("--unknown")
			bad.Argv = append(bad.Argv, make([]string, 65-len(bad.Argv))...)
			line(t, x.recheck(t, bad), "bound", 2, "argv[64]")
			bad = beforeMode("--unknown")
			bad.Argv = append(bad.Argv, strings.Repeat("x", 513))
			line(t, x.recheck(t, bad), "bound", 2, fmt.Sprintf("argv[%d]", len(bad.Argv)-1))
			bad = beforeMode("--unknown")
			bad.Env = map[string]string{}
			for i := range 33 {
				bad.Env[fmt.Sprintf("E%02d", i)] = "private-env"
			}
			line(t, x.recheck(t, bad), "bound", 2, "env[E32]")
			// T30/40/52: remove just the mode to isolate the environment gate.
			bad = spec
			bad.Argv = slices.Delete(slices.Clone(spec.Argv), 1, 2)
			line(t, x.recheck(t, bad), "required-missing", 2, "form[mode]")
			if b.name == "claude" {
				bad.Env = map[string]string{"DISABLE_TELEMETRY": "1"}
				line(t, x.recheck(t, bad), "required-missing", 2, "form[mode]")
				bad.Env["DISABLE_TELEMETRY"] = "private-env"
				line(t, x.recheck(t, bad), "env-refused", 2, "env[DISABLE_TELEMETRY]")
			}
			bad.Env = map[string]string{"LD_PRELOAD": "private-env"}
			line(t, x.recheck(t, bad), "env-refused", 2, "env[LD_PRELOAD]")
			for i := range 100 {
				names := []string{"A", "Z"}
				if i%2 == 0 {
					slices.Reverse(names)
				}
				bad.Env = map[string]string{}
				for _, n := range names {
					bad.Env[n] = "private-env"
				}
				line(t, x.recheck(t, bad), "env-refused", 2, "env[A]")
			}
			bad.Env = map[string]string{"A]": "private-env"}
			line(t, x.recheck(t, bad), "env-refused", 2, "env[#0]")
			y = x
			y.l.Argv = append(slices.Clone(x.l.Argv), "--unknown")
			bad.Role = "invalid"
			line(t, y.judge(bad), "unknown-token", 1, fmt.Sprintf("argv[%d]", len(x.l.Argv)))
			bad = beforeMode("--unknown")
			bad.Argv = append(bad.Argv, "--also-unknown")
			line(t, x.recheck(t, bad), "unknown-token", 2, "argv[1]")
			// T54. Check the diagnostic for raw invalid UTF-8 before a wire
			// codec replaces the byte, then check Go's escaped oversized value.
			for _, v := range []string{"\x7f", string([]byte{0xff}), "\n", "]bad\n"} {
				_, r := lp.Recheck(x.p, beforeMode("--model="+v), x.s, intent)
				line(t, r, "value-outside-ceiling", 2, "argv[1]", v)
			}
			_, r := lp.Recheck(x.p, beforeMode("--model="+strings.Repeat("\x00", 40)), x.s, intent)
			header := "launch refused [pack-launch-value-outside-ceiling] stage=2 at=argv[1] value=\""
			want := header + strings.Repeat(`\x00`, (200-len(header)-2)/4) + "\"+"
			if r == nil || r.Error() != want {
				t.Fatalf("truncated diagnostic: %v", r)
			}
		})
	}
}

func TestRealAuthenticatedRequest(t *testing.T) {
	for _, b := range builds {
		t.Run(string(b.name), func(t *testing.T) {
			x := baseline(t, b)
			spec := x.expand(t)
			for _, role := range []string{"l1", "demo/supervisor"} {
				y := x
				y.s.Role = role
				line(t, y.recheck(t, spec), "snapshot-invalid", 0, "snapshot")
			}
			// T45/59/65: a second version can be decoded concurrently, but
			// cannot satisfy a request bound to the first version.
			d, help := synthetic(t, b)
			d.Version += ".1"
			d.Fixture.Path = d.Version + ".help.txt"
			for i := range d.Slots.Permission.Values {
				m := &d.Slots.Permission.Values[i]
				var v map[string]any
				if err := json.Unmarshal(m.Vector["terminal"], &v); err != nil {
					t.Fatal(err)
				}
				v["build"] = d.Version
				m.Vector["terminal"] = wire(t, v)
			}
			y := x
			y.p = decoded(t, d, help)
			if y.p.Digest() == x.p.Digest() {
				t.Fatal("two versions share a policy digest")
			}
			line(t, y.recheck(t, spec), "policy-missing", 0, "policy")
			line(t, y.judge(spec), "policy-missing", 0, "policy")
			y.s.Version = d.Version
			line(t, y.recheck(t, spec), "expected-differs", 0, "snapshot")
			y = x
			y.s.ProfileDigest = ""
			line(t, y.recheck(t, spec), "snapshot-invalid", 0, "snapshot")
			y = x
			y.s.Mode = "another"
			y.bind(t)
			y.s.Mode = x.s.Mode
			line(t, y.recheck(t, spec), "expected-differs", 0, "snapshot")
			y.s.ProfileDigest = x.s.ProfileDigest
			line(t, y.recheck(t, spec), "expected-digest", 0, "snapshot")
			y.s.Transport = pack.Protocol
			line(t, y.recheck(t, spec), "expected-digest", 0, "snapshot")
			y = x
			y.s.Requested = []byte("{")
			line(t, y.recheck(t, spec), "snapshot-invalid", 0, "snapshot")
			// T33/43/44/61/62/66. Both actual executables are terminal-only.
			for _, role := range []string{"demo/lead", "demo/coord/f1"} {
				for _, owner := range []bool{false, true} {
					y = x
					y.s.Role = role
					y.s.Transport = pack.Protocol
					y.s.OwnerProtocol = owner
					y.s.Recognizer = ""
					line(t, y.recheck(t, spec), "transport-not-admitted", 0, "snapshot")
					line(t, y.judge(spec), "transport-not-admitted", 0, "snapshot")
				}
			}
			// T29/62: turning just the terminal vector pending never permits a launch.
			d, help = synthetic(t, b)
			for i := range d.Slots.Permission.Values {
				m := &d.Slots.Permission.Values[i]
				if m.Name == x.s.Mode {
					m.Vector["terminal"] = json.RawMessage(`"pending"`)
				}
			}
			y = x
			y.p = decoded(t, d, help)
			if b.name == "codex" {
				line(t, y.judge(spec), "mode-vector-pending", 2, "argv[1]", x.s.Mode)
			} else {
				line(t, y.judge(spec), "mode-class-refused", 2, "argv[1]", x.s.Mode)
			}
			// An observation cannot be rebound to another build or transport.
			d, help = synthetic(t, b)
			m := &d.Slots.Permission.Values[0]
			m.Vector["protocol"] = m.Vector["terminal"]
			if _, err := lp.Decode(wire(t, d), help); err == nil {
				t.Fatal("accepted copied terminal evidence")
			}
			delete(m.Vector, "protocol")
			d.Version += ".2"
			if _, err := lp.Decode(wire(t, d), help); err == nil {
				t.Fatal("accepted another build's evidence")
			}
			// T64: the declaration's sampled grammar and the actual value check.
			y = x
			y.l.Grammars = maps.Clone(x.l.Grammars)
			y.l.Grammars[pack.ModelSlot] += "|^Gpt$"
			line(t, y.judge(spec), "selection-outside-policy", 0, "policy")
			y.l.Grammars[pack.ModelSlot] = x.l.Grammars[pack.ModelSlot] + "|^EXTRA9$"
			y.s.Model = "EXTRA9"
			y.bind(t)
			y.l.Argv = slices.Clone(x.l.Argv)
			y.l.Argv[1], y.l.Argv[2] = y.l.Argv[2], y.l.Argv[1]
			line(t, y.judge(y.expand(t)), "value-grammar", 2, "argv[1]", "EXTRA9")
		})
	}
}

// Codex's explicitly requested coordinator mode is the real policy with a
// passing stage-2 baseline. Stage 3 can therefore compare independent values.
func TestRealExpectedSpec(t *testing.T) {
	x := baseline(t, builds[1])
	x.s.Model = "o3"
	x.bind(t)
	spec := x.expand(t)
	line(t, x.judge(spec), "", 0, "")
	check := func(s lp.Spec, code, at string) { t.Helper(); line(t, x.recheck(t, s), code, 3, at) }
	s := spec
	s.Role = "demo/lead"
	check(s, "expected-role", "snapshot")
	s = spec
	s.Transport = pack.Protocol
	check(s, "expected-transport", "snapshot")
	s = spec
	s.ProfileDigest = "another"
	check(s, "expected-digest", "snapshot")
	y := x
	y.s.Recognizer = "another"
	line(t, y.recheck(t, spec), "expected-recognizer", 3, "snapshot")
	s = spec
	s.Argv = slices.Clone(spec.Argv)
	s.Argv[2] = "--model=gpt-5"
	line(t, x.judge(s), "template-expansion-differs", 2, "argv[2]")
	check(s, "expected-differs", "slot[model]")
	s.Argv = s.Argv[:2]
	line(t, x.judge(s), "template-expansion-differs", 2, "argv[2]")
	check(s, "expected-missing", "slot[model]")
	y = x
	y.s.Model = ""
	y.bind(t)
	s = spec
	s.ProfileDigest = y.s.ProfileDigest
	line(t, y.recheck(t, s), "expected-unexpected", 3, "slot[model]")
	s = spec
	s.Argv = append(slices.Clone(spec.Argv), x.strict)
	line(t, x.judge(s), "template-expansion-differs", 2, "argv[3]")
	line(t, x.recheck(t, s), "", 0, "")
	for _, slot := range []string{"agent", "resume", "effort"} {
		y = x
		switch slot {
		case "agent":
			y.s.Agent = "reviewer"
		case "resume":
			y.s.Resume = nativeID
		case "effort":
			y.s.Effort = "low"
		}
		y.bind(t)
		s = spec
		s.ProfileDigest = y.s.ProfileDigest
		line(t, y.recheck(t, s), "expected-unplaceable", 3, "slot["+slot+"]")
	}
	d, h := synthetic(t, x.b)
	d.Forms = slices.DeleteFunc(d.Forms, func(f form) bool { return f.Slot == "model" })
	y = x
	y.p = decoded(t, d, h)
	s = spec
	s.Argv = slices.Clone(spec.Argv[:2])
	line(t, y.recheck(t, s), "expected-unplaceable", 3, "slot[model]")
	// T39: evaluate the valid substituted spec before comparing it with
	// the authenticated request. The request names a different real mode.
	y = x
	y.s.Mode = "on-request"
	y.s.Recognizer = "on-request"
	y.bind(t)
	s = spec
	s.ProfileDigest = y.s.ProfileDigest
	line(t, y.judge(s), "template-expansion-differs", 2, "argv[1]")
	line(t, y.recheck(t, s), "expected-differs", 3, "slot[permission_mode]")
	// T51: this pack admits no extra launch environment.
	s = spec
	s.Env = map[string]string{"EXTRA": "private-env"}
	line(t, x.judge(s), "env-refused", 2, "env[EXTRA]")
}

func TestRealResumeAndAgent(t *testing.T) {
	x := baseline(t, builds[0])
	// T41 uses one decoded declaration for both expansions. S0 has no
	// sandbox declaration yet, so assemble its launch document in memory.
	files := map[string][]byte{
		pack.ManifestPath: read(t, "../claude/declaration.json"),
		pack.SandboxPath:  wire(t, map[string]any{"schema": pack.DeclarationSchema, "builds": map[string]any{x.b.version: pack.Sandbox{Launch: &x.l}}}),
	}
	d, err := pack.DecodeDeclaration(files, pack.BindLayout{})
	if err != nil {
		t.Fatal(err)
	}
	x.l = *d.Sandbox[x.b.version].Launch
	fresh := x.expand(t)
	x.s.Resume = nativeID
	resumed := x.expand(t)
	if !slices.Equal(fresh.Argv, []string{"claude", "--permission-mode=manual", "--agent=reviewer"}) ||
		!slices.Equal(resumed.Argv, append(slices.Clone(fresh.Argv), "--resume="+nativeID)) {
		t.Fatal("resume expansion differs")
	}
	line(t, x.judge(resumed), "mode-class-refused", 2, "argv[1]", "manual")
	y := x
	y.s.Resume = ""
	line(t, y.judge(fresh), "mode-class-refused", 2, "argv[1]", "manual")
	// A class check precedes the R41 expected-value checks on this build.
	line(t, y.recheck(t, resumed), "mode-class-refused", 2, "argv[1]", "manual")
	line(t, x.recheck(t, fresh), "mode-class-refused", 2, "argv[1]", "manual")
	check := func(tokens []string, code string, at string, detail ...string) {
		t.Helper()
		s := resumed
		s.Argv = append([]string{"claude"}, tokens...)
		line(t, x.recheck(t, s), code, 2, at, detail...)
	}
	check([]string{"--resume=" + nativeID, "--resume=" + nativeID}, "form-repeated", "argv[2]")
	check([]string{"--resume:" + nativeID}, "unknown-token", "argv[1]")
	check([]string{"--resume=" + nativeID + "x"}, "value-grammar", "argv[1]")
	check([]string{"--resume=00000000-0000-4000-8000-000000000002", "--permission-mode=manual"}, "mode-class-refused", "argv[2]", "manual")
	y = x
	y.l.Argv = slices.Clone(x.l.Argv)
	y.l.Argv[5] = "{resume}"
	line(t, y.judge(resumed), "form-position", 1, "argv[5]")
	y.l.Argv[5] = "--resume=" + nativeID
	line(t, y.judge(resumed), "literal-in-slot", 1, "argv[5]")
	y.l.Argv[5] = "--resume={model}"
	line(t, y.judge(resumed), "slot-mismatch", 1, "argv[5]")
	// T46: a different configured agent cannot be substituted on this build.
	s := resumed
	s.Argv = []string{"claude", "--agent=other", "--permission-mode=manual", "--resume=" + nativeID}
	line(t, x.judge(s), "value-unadmitted", 2, "argv[1]", "other")
	line(t, x.recheck(t, s), "value-unadmitted", 2, "argv[1]", "other")
	// T51: selection accepts the two admitted Claude env values. Even with
	// both selected and the authenticated request intact the mode still refuses.
	y = x
	y.l.Env = map[string]string{"DISABLE_TELEMETRY": "1", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"}
	s = y.expand(t)
	if !maps.Equal(s.Env, y.l.Env) {
		t.Fatal("environment expansion differs")
	}
	delete(s.Env, "DISABLE_TELEMETRY")
	line(t, y.judge(s), "mode-class-refused", 2, "argv[1]", "manual")
	s.Env["DISABLE_TELEMETRY"] = "other"
	line(t, y.judge(s), "mode-class-refused", 2, "argv[1]", "manual")
}

func TestRealPolicyFiles(t *testing.T) {
	for _, b := range builds {
		t.Run(string(b.name), func(t *testing.T) {
			x := baseline(t, b)
			raw, _, _ := b.load(t)
			// File views are inputs supplied by the renderer. Build the passing
			// fixture from the canonical required scalars and preserve path types.
			var rows []struct {
				Role     string `json:"path_role"`
				Required []struct {
					Path       []any
					Type, Text string
				}
			}
			if err := json.Unmarshal(parse(t, raw).Files, &rows); err != nil {
				t.Fatal(err)
			}
			makeFiles := func() (lp.Inventory, []lp.FileView) {
				hash := digest([]byte("stand-in installed file"))
				var inv lp.Inventory
				var files []lp.FileView
				for _, row := range rows {
					w := lp.ExpectedFile{PathRole: row.Role, Dest: "a-policy"}
					f := lp.FileView{PathRole: row.Role, BytesSHA256: hash, InstalledSHA256: hash}
					for _, r := range row.Required {
						path := slices.Clone(r.Path)
						for i, c := range path {
							if n, ok := c.(float64); ok {
								path[i] = int(n)
							}
						}
						f.Entries = append(f.Entries, lp.Entry{Path: path, Type: r.Type, Text: r.Text})
					}
					if row.Role == "agent" {
						w.Dest = "b-agent"
						w.Definition = true
						w.AgentName = x.s.Agent
						f.Entries = append(f.Entries, lp.Entry{Path: []lp.Comp{"name"}, Type: "string", Text: x.s.Agent})
					}
					f.Dest = w.Dest
					inv = append(inv, w)
					files = append(files, f)
				}
				slices.SortFunc(inv, func(a, b lp.ExpectedFile) int { return strings.Compare(a.Dest, b.Dest) })
				slices.SortFunc(files, func(a, b lp.FileView) int { return strings.Compare(a.Dest, b.Dest) })
				return inv, files
			}
			inv, files := makeFiles()
			line(t, lp.JudgeFiles(x.p, x.s, inv, files), "", 0, "")
			line(t, lp.JudgeFiles(x.p, x.s, nil, nil), "", 0, "")
			for _, tc := range []struct {
				name, code, at string
				mutate         func(lp.Inventory, []lp.FileView) (lp.Inventory, []lp.FileView)
			}{
				{"a required entry missing", "file-refused", "file[a-policy]", func(i lp.Inventory, f []lp.FileView) (lp.Inventory, []lp.FileView) {
					f[0].Entries = f[0].Entries[1:]
					return i, f
				}},
				{"b extra key", "file-refused", "file[a-policy]", func(i lp.Inventory, f []lp.FileView) (lp.Inventory, []lp.FileView) {
					f[0].Entries = append(f[0].Entries, lp.Entry{Path: []lp.Comp{"extra"}, Type: "bool", Text: "true"})
					return i, f
				}},
				{"c wrong type", "file-refused", "file[a-policy]", func(i lp.Inventory, f []lp.FileView) (lp.Inventory, []lp.FileView) {
					f[0].Entries[0].Type = "string"
					return i, f
				}},
				{"d wrong value", "file-refused", "file[a-policy]", func(i lp.Inventory, f []lp.FileView) (lp.Inventory, []lp.FileView) {
					f[0].Entries[0].Text = "false"
					return i, f
				}},
				{"e missing file", "file-refused", "file[a-policy]", func(i lp.Inventory, f []lp.FileView) (lp.Inventory, []lp.FileView) { return i, f[1:] }},
				{"f duplicate destination", "file-refused", "file[a-policy]", func(i lp.Inventory, f []lp.FileView) (lp.Inventory, []lp.FileView) { return i, append(f, f[0]) }},
				{"g unknown role", "file-refused", "file[a-policy]", func(i lp.Inventory, f []lp.FileView) (lp.Inventory, []lp.FileView) {
					i[0].PathRole = "unknown"
					f[0].PathRole = "unknown"
					return i, f
				}},
				{"h substituted role", "file-refused", "file[a-policy]", func(i lp.Inventory, f []lp.FileView) (lp.Inventory, []lp.FileView) {
					f[0].PathRole = "agent"
					return i, f
				}},
				{"l installed bytes", "file-binding", "file[a-policy]", func(i lp.Inventory, f []lp.FileView) (lp.Inventory, []lp.FileView) {
					f[0].InstalledSHA256 = digest([]byte("different file"))
					return i, f
				}},
				{"unsafe destination", "file-refused", "file[#0]", func(i lp.Inventory, f []lp.FileView) (lp.Inventory, []lp.FileView) { i[0].Dest = "]bad"; return i, f }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					inv, f := makeFiles()
					inv, f = tc.mutate(inv, f)
					line(t, lp.JudgeFiles(x.p, x.s, inv, f), tc.code, 2, tc.at)
				})
			}
			if b.name == "claude" {
				line(t, lp.JudgeFiles(x.p, x.s, inv, files[:1]), "file-refused", 2, "file[b-agent]")
				line(t, lp.JudgeFiles(x.p, x.s, inv[:1], files[:1]), "", 0, "")
				files[1].Entries[0].Text = "other"
				line(t, lp.JudgeFiles(x.p, x.s, inv, files), "file-refused", 2, "file[b-agent]")
				files[1].Entries[0].Text = x.s.Agent
				for _, bad := range forbiddenKeys[b.name] {
					t.Run("k "+bad, func(t *testing.T) {
						inv, f := makeFiles()
						f[1].Entries = append(f[1].Entries, lp.Entry{Path: []lp.Comp{bad}, Type: "string", Text: "private-env"})
						line(t, lp.JudgeFiles(x.p, x.s, inv, f), "agent-key-refused", 2, "file[b-agent]")
						doc := parse(t, raw)
						doc.Slots.Agent.Keys = append(doc.Slots.Agent.Keys, key{Key: bad})
						if frontmatter(doc) == nil {
							t.Fatal("qualification allowed forbidden key")
						}
					})
				}
				// T52 first sorted file with two faults in reversed input order.
				files[0].Entries = nil
				files[1].InstalledSHA256 = ""
				slices.Reverse(inv)
				slices.Reverse(files)
				line(t, lp.JudgeFiles(x.p, x.s, inv, files), "file-refused", 2, "file[a-policy]")
			} else {
				// T53m: a numeric requirements index is distinct from the object key "0".
				found := false
				for j, e := range files[0].Entries {
					if slices.Equal(e.Path, []any{"allowed_approval_policies", 0}) {
						files[0].Entries[j].Path = []any{"allowed_approval_policies", "0"}
						found = true
					}
				}
				if !found {
					t.Fatal("missing indexed approval requirement")
				}
				line(t, lp.JudgeFiles(x.p, x.s, inv, files), "file-refused", 2, "file[a-policy]")
				inv = append(inv, lp.ExpectedFile{PathRole: "agent", Dest: "b-agent", Definition: true, AgentName: "other"})
				_, files = makeFiles()
				line(t, lp.JudgeFiles(x.p, x.s, inv, files), "file-refused", 2, "file[b-agent]")
			}
		})
	}
}
