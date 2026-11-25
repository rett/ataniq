package all_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

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
	var fault error
	d.locators(func(w string, e *locator) {
		switch e.Source {
		case "help":
			row := defs[e.Line]
			if row.scope != e.Scope || !slices.Contains(row.words, w) {
				fault = fmt.Errorf("wrong help locator for %s at %s line %d", w, e.Scope, e.Line)
			}
		case "pending":
			if d.Fixture.SHA != "" {
				fault = fmt.Errorf("pending evidence for %s", w)
			}
		case "doc":
			if strings.TrimSpace(e.Ref) == "" {
				fault = fmt.Errorf("empty documentation for %s", w)
			}
		default:
			fault = fmt.Errorf("unresolved evidence for %s", w)
		}
	})
	return fault
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
			// Check help locators against the real capture with its correct hash.
			// Only the help locators participate in this negative control.
			d.Fixture.SHA = digest(help)
			d.locators(func(w string, e *locator) {
				if e.Source == "pending" {
					*e = locator{Source: "doc", Ref: d.Fixture.Path}
				}
			})
			if err := inventory(d, help); err != nil {
				t.Fatal(err)
			}
			f := &d.Forms[slices.IndexFunc(d.Forms, func(f form) bool { return f.ID == "strict" })]
			saved := f.Evidence
			f.Evidence.Line = d.Options[len(d.Options)-1].From
			if err := inventory(d, help); err == nil {
				t.Fatal("accepted another option's line")
			}
			f.Evidence = saved
			if b.name == "codex" {
				f.Evidence.Scope = "exec"
				if err := inventory(d, help); err == nil {
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
		raw, _, _ := b.load(t)
		got := map[string]pack.ModeClass{}
		for _, m := range parse(t, raw).Slots.Permission.Values {
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
