package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/catalog"
	"ataniq/internal/genesis/internal/canon"
)

// templateDigests pin the embedded templates: a changed template is a new
// version, with a new digest here.
var templateDigests = map[string]string{
	"media":      "becdf86ea0b0311c24aa43eac5db42a0a85eedb8731ea6e77652cec650ad429a",
	"publishing": "5beaa43082ee5cf4c0845a31c7340a5c0db5f8e3f37ddf9229865ffe11f3ac03",
	"software":   "3a081bbc1b13145c38fda5d3736b1ea1e7ceeab18afe00fe891defb25160cec3",
}

// The embedded templates are canonical, one per kind but other, each
// digest the SHA-256 of its file, and each pinned.
func TestEmbeddedTemplates(t *testing.T) {
	ts, err := Templates()
	must(t, err)
	if len(ts) != 3 {
		t.Fatalf("%d templates", len(ts))
	}
	c := v1(t)
	for _, tp := range ts {
		data, err := os.ReadFile(filepath.Join("templates", tp.ID+".json"))
		must(t, err)
		v, err := canon.Parse(data, 8)
		must(t, err)
		out, err := canon.Encode(v)
		must(t, err)
		if string(out) != string(data) {
			t.Errorf("%s.json is not in the canonical form", tp.ID)
		}
		sum := sha256.Sum256(data)
		if tp.Digest() != hex.EncodeToString(sum[:]) {
			t.Errorf("%s: the digest is not that of the file", tp.ID)
		}
		if tp.Digest() != templateDigests[tp.ID] {
			t.Errorf("%s: the digest is %s; a changed template is a new version", tp.ID, tp.Digest())
		}
		if tp.ID != tp.Kind || tp.Version != "v1" {
			t.Errorf("%s: kind %s version %s", tp.ID, tp.Kind, tp.Version)
		}
		must(t, tp.checkAgainst(c))
	}
	// Each call returns its own copy.
	ts[0].Milestones[0].Tasks[0].Title = "changed"
	again, err := Templates()
	must(t, err)
	if again[0].Milestones[0].Tasks[0].Title == "changed" {
		t.Fatal("Templates returns a shared template")
	}
}

// Every option of the catalog for where the files live, the budget and the
// review points has its word in the plan's closed lists, and every area and
// check a template names is in the plan's.
func TestClosedListsCoverTheCatalog(t *testing.T) {
	c := v1(t)
	for field, closed := range map[string][]string{"hosts": Hosts, "budget_band": Spends, "review_points": ReviewPoints} {
		n, ok := c.Node(field)
		if !ok {
			t.Fatalf("no node %s", field)
		}
		for _, o := range n.Options {
			if !contains(closed, o.ID) {
				t.Errorf("%s: the option %s has no word in the plan", field, o.ID)
			}
		}
		if len(n.Options) != len(closed) {
			t.Errorf("%s: the plan names %d, the catalog %d", field, len(closed), len(n.Options))
		}
	}
	for _, list := range [][]string{Areas, Checks, ReviewPoints, Hosts, Services, Spends} {
		if !sortedSet(list) {
			t.Errorf("%v is not sorted", list)
		}
	}
}

func sortedSet(list []string) bool {
	for i := 1; i < len(list); i++ {
		if list[i-1] >= list[i] {
			return false
		}
	}
	return true
}

func softwareData(t testing.TB) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("templates", "software.json"))
	must(t, err)
	return string(data)
}

// LoadTemplate refuses a template that breaks its rules: an unknown field,
// kind, task kind, tier, area, check or operation, a dependency that is not
// before its task, and a security default that is not the default.
func TestLoadTemplateRefuses(t *testing.T) {
	v := softwareData(t)
	if _, err := LoadTemplate([]byte(v)); err != nil {
		t.Fatal(err)
	}
	replace := func(old, new string) string {
		t.Helper()
		if !strings.Contains(v, old) {
			t.Fatalf("the template does not hold %s", old)
		}
		return strings.Replace(v, old, new, 1)
	}
	cases := map[string]string{
		"an unknown member":           replace(`{"areas"`, `{"extra":1,"areas"`),
		"an unknown member of a task": replace(`"ref":"layout"`, `"ref":"layout","owner":"x"`),
		"a schema":                    replace(TemplateSchema, "ataniq.genesis-plan-template/2"),
		"an unknown kind":             replace(`"kind":"software","milestones"`, `"kind":"music","milestones"`),
		"a task kind":                 replace(`"kind":"build"`, `"kind":"deploy"`),
		"a tier":                      replace(`"tier":"deep"`, `"tier":"opus"`),
		"an area of no template":      replace(`"scope":["repo:build"]`, `"scope":["repo:secrets"]`),
		"an area of another template": replace(`"scope":["repo:build"]`, `"scope":["store:assets"]`),
		"a check of another template": replace(`"checks":["lint"],"depends_on":["layout"]`, `"checks":["loudness"],"depends_on":["layout"]`),
		"a gate not of the checks":    replace(`{"check":"lint"}`, `{"check":"spelling"}`),
		"no gate that always holds":   replace(`"gates":[{"check":"lint"},{"check":"unit-tests"}]`, `"gates":[{"check":"lint","when":{"field":"hosts","op":"set"}}]`),
		"a service":                   replace(`"name":"file host"`, `"name":"payments"`),
		"an operation":                replace(`"op":"eq"`, `"op":"regex"`),
		"a field of no brief":         replace(`"field":"hosts"`, `"field":"colour"`),
		"a field of another kind":     replace(`"field":"software.data"`, `"field":"media.rights"`),
		"an eq without a value":       replace(`"value":"local"`, `"values":["local"]`),
		"a dependency after the task": replace(`"depends_on":["agree-stack"]`, `"depends_on":["agents"]`),
		"a dependency unknown":        replace(`"depends_on":["agree-stack"]`, `"depends_on":["nothing"]`),
		"a repeated ref":              replace(`"ref":"agents"`, `"ref":"layout"`),
		"a push":                      replace(`"push":false`, `"push":true`),
		"an outward step":             replace(`"outward_steps":false`, `"outward_steps":true`),
		"secrets by value":            replace(`"secrets":"by-reference"`, `"secrets":"inline"`),
		"a last demo with a when":     replace(`{"text":"It runs on this computer and one flow works from start to end."}`, `{"text":"It runs.","when":{"field":"hosts","op":"set"}}`),
		"a text with markup":          replace(`Build the first page`, `Build the <b>first</b> page`),
		"a text with an address":      replace(`Build the first page`, `Build https://example.com`),
		"areas unsorted":              replace(`"areas":["repo:build","repo:deploy"`, `"areas":["repo:deploy","repo:build"`),
		"a float":                     replace(`"push":false`, `"push":0.5`),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LoadTemplate([]byte(data))
			wantCode(t, err, brief.CodeTemplateInvalid)
		})
	}
	_, err := LoadTemplate([]byte(strings.Repeat(" ", MaxTemplateBytes+1)))
	wantCode(t, err, brief.CodeTooLarge)
}

// A template that loads is held to the catalog when a plan is compiled: a
// field that is not a required field of the kind, an option no answer sets,
// eq on a field of several choices, contains on a field of one, a title
// field that takes other text, and two templates of one kind are refused;
// a brief whose kind has no template is refused template_missing. None is
// read as a default.
func TestTemplateAgainstTheCatalog(t *testing.T) {
	c := v1(t)
	b := walk(t, c, map[string]catalog.Answer{"kind": pickOne("software"), "software.sub_kind": pickOne("web_app")}, defaults)
	v := softwareData(t)
	load := func(data string) Template {
		t.Helper()
		tp, err := LoadTemplate([]byte(data))
		must(t, err)
		return tp
	}
	replace := func(old, new string) Template {
		t.Helper()
		if !strings.Contains(v, old) {
			t.Fatalf("the template does not hold %s", old)
		}
		return load(strings.Replace(v, old, new, 1))
	}
	cases := map[string]Template{
		"a field no question sets":    replace(`"field":"software.data","op":"eq","value":"local"`, `"field":"software.colour","op":"eq","value":"local"`),
		"a field not required":        replace(`"field":"hosts","op":"eq"`, `"field":"formats","op":"eq"`),
		"an option no answer sets":    replace(`"value":"hosting"`, `"value":"kubernetes"`),
		"other where none is allowed": replace(`"value":"local"`, `"value":"other"`),
		"eq on several choices":       replace(`"field":"hosts","op":"eq","value":"file_host"`, `"field":"review_points","op":"eq","value":"plan"`),
		"contains on one choice":      replace(`"field":"hosts","op":"eq","value":"file_host"`, `"field":"hosts","op":"contains","value":"file_host"`),
		"a title of the owner's text": replace(`"title_field":"software.sub_kind"`, `"title_field":"software.stack"`),
		"a title not required":        replace(`"title_field":"software.sub_kind"`, `"title_field":"software.colour"`),
	}
	for name, tp := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := compileWith(b, c, []Template{tp}, 1, t0)
			wantCode(t, err, brief.CodeTemplateInvalid)
		})
	}
	good := load(v)
	_, err := compileWith(b, c, []Template{good, good}, 1, t0)
	wantCode(t, err, brief.CodeTemplateInvalid)
	ts, err := Templates()
	must(t, err)
	var others []Template
	for _, tp := range ts {
		if tp.Kind != brief.DomainSoftware {
			others = append(others, tp)
		}
	}
	_, err = compileWith(b, c, others, 1, t0)
	wantCode(t, err, brief.CodeTemplateMissing)
	_, err = compileWith(b, c, []Template{{Kind: brief.DomainSoftware}}, 1, t0)
	wantCode(t, err, brief.CodeTemplateInvalid)
	if _, err := compileWith(b, c, []Template{good}, 1, t0); err != nil {
		t.Fatal(err)
	}
}
