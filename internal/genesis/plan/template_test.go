package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/genesis/brief"
	"ataniq/internal/genesis/catalog"
	"ataniq/internal/genesis/internal/canon"
)

// templateDigests pin the templates by "id@version": a changed template is a
// new version, with a new entry here. An entry is kept when its version is
// no longer embedded, so that the digest a stored plan names stays on record
// and an old version cannot be reused for other bytes.
var templateDigests = map[string]string{
	"media@v1":      "27041dbc83151f59522dca72e9dc0ab5a01bc32c4a7f4689b1c5ff751bd2b97d",
	"publishing@v1": "4f193affc6539a04922cc74d047ac2a9cec2502393764f898ca6eb776ab81e59",
	"software@v1":   "82f4a99439fdcb41d4571574f0d47ac377a20f8d0f1eb402b1ea47b7a06eaf6c",
}

// checkPin holds a template's digest to the pins. A pinned id@version whose
// digest differs is a template changed without a new version: refused
// always, -update or not. An id@version with no pin is refused unless
// -update says the pin is being added; the entry to add is then logged.
func checkPin(pins map[string]string, key, digest string, update bool) (note string, err error) {
	want, pinned := pins[key]
	switch {
	case pinned && want != digest:
		return "", fmt.Errorf("%s: the digest is %s, pinned %s: a changed template is a new version, not a new digest for its version", key, digest, want)
	case !pinned && !update:
		return "", fmt.Errorf("%s: no pin for the digest %s; add \"%s\": \"%s\" to templateDigests (the test with -update only reports it)", key, digest, key, digest)
	case !pinned:
		return fmt.Sprintf("pin to add: %q: %q,", key, digest), nil
	}
	return "", nil
}

// The embedded templates are canonical, one per kind but other, each
// digest the SHA-256 of its file, each pinned, each held to the catalog,
// and small: at most 4 milestones.
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
		note, err := checkPin(templateDigests, tp.ID+"@"+tp.Version, tp.Digest(), *update)
		if err != nil {
			t.Error(err)
		}
		if note != "" {
			t.Log(note)
		}
		if tp.ID != tp.Kind || len(tp.Milestones) > MaxMilestones {
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

// The pins by id@version: a digest that differs from the pin of its
// version fails whatever the flag says; a version with no pin fails unless
// -update; and an entry of a version no longer embedded does no harm.
func TestTemplatePinsAreByVersion(t *testing.T) {
	pins := map[string]string{"software@v1": "aa", "software@v0": "old"}
	for _, c := range []struct {
		name, key, digest string
		update, fails     bool
	}{
		{"same", "software@v1", "aa", false, false},
		{"changed", "software@v1", "bb", false, true},
		{"changed, update", "software@v1", "bb", true, true},
		{"new version", "software@v2", "cc", false, true},
		{"new version, update", "software@v2", "cc", true, false},
		{"another kind", "media@v1", "dd", false, true},
	} {
		note, err := checkPin(pins, c.key, c.digest, c.update)
		if (err != nil) != c.fails {
			t.Errorf("%s: error %v, fails %v", c.name, err, c.fails)
		}
		if c.name == "new version, update" && !strings.Contains(note, `"software@v2": "cc"`) {
			t.Errorf("the pin to add is not reported: %q", note)
		}
	}
	// Every pin is of the form id@version, and every embedded template has
	// one.
	ts, err := Templates()
	must(t, err)
	for key := range templateDigests {
		if i := strings.IndexByte(key, '@'); i < 1 || !validVersion(key[i+1:]) {
			t.Errorf("the pin %q is not id@version", key)
		}
	}
	for _, tp := range ts {
		if _, ok := templateDigests[tp.ID+"@"+tp.Version]; !ok {
			t.Errorf("%s@%s has no pin", tp.ID, tp.Version)
		}
	}
}

// Every option of the catalog for where the files live, the budget and the
// review points has its word in the plan's closed lists.
func TestClosedListsCoverTheCatalog(t *testing.T) {
	c := v1(t)
	for field, closed := range map[string][]string{"hosts": Hosts, "budget_band": BudgetBands, "review_points": ReviewPoints} {
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
	for _, list := range [][]string{Areas, Checks, ReviewPoints, Hosts, Services, BudgetBands, Warnings} {
		for i := 1; i < len(list); i++ {
			if list[i-1] >= list[i] {
				t.Errorf("%v is not sorted", list)
			}
		}
	}
	for _, a := range Areas {
		if !validArea(a) {
			t.Errorf("%s is not of the grammar of an area", a)
		}
	}
	for _, ch := range Checks {
		if !validCheckID(ch) {
			t.Errorf("%s is not of the grammar of a check", ch)
		}
	}
}

func softwareData(t testing.TB) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("templates", "software.json"))
	must(t, err)
	return string(data)
}

// LoadTemplate refuses a template that breaks its rules: an unknown field,
// kind, task kind, size, area, check, warning or operation, a dependency
// that is not before its task, and a security default that is not the
// default.
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
		"a size":                      replace(`"size":"large"`, `"size":"opus"`),
		"a tier":                      replace(`"size":"large"`, `"tier":"large"`),
		"an area of no template":      replace(`"scope":["repo:build"]`, `"scope":["repo:secrets"]`),
		"an area of another template": replace(`"scope":["repo:build"]`, `"scope":["store:assets"]`),
		"a check of another template": replace(`"checks":["lint"],"depends_on":["layout"]`, `"checks":["loudness"],"depends_on":["layout"]`),
		"a gate not of the checks":    replace(`{"check":"lint"}`, `{"check":"spelling"}`),
		"no gate that always holds":   replace(`"gates":[{"check":"lint"},{"check":"unit-tests"}]`, `"gates":[{"check":"lint","when":{"field":"hosts","op":"set"}}]`),
		"a service":                   replace(`"name":"file or code host"`, `"name":"payments"`),
		"a warning":                   replace(`"id":"settle-at-desk"`, `"id":"all-clear"`),
		"the estimate's warning":      replace(`"id":"settle-at-desk"`, `"id":"estimate-may-exceed-band"`),
		"a warning always":            replace(`{"id":"settle-at-desk","when":{"field":"software.stack","op":"other"}}`, `{"id":"settle-at-desk"}`),
		"an operation":                replace(`"op":"eq"`, `"op":"regex"`),
		"an other with a value":       replace(`{"field":"software.stack","op":"other"}`, `{"field":"software.stack","op":"other","value":"go"}`),
		"a field of no brief":         replace(`"field":"hosts"`, `"field":"colour"`),
		"a field of another kind":     replace(`"field":"software.data"`, `"field":"media.rights"`),
		"an eq without a value":       replace(`"value":"local"`, `"values":["local"]`),
		"a dependency after the task": replace(`"depends_on":["settle-stack"]`, `"depends_on":["makefile"]`),
		"a dependency unknown":        replace(`"depends_on":["settle-stack"]`, `"depends_on":["nothing"]`),
		"a repeated ref":              replace(`"ref":"makefile"`, `"ref":"layout"`),
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

// A template that loads is held to the catalog when the embedded set is
// loaded and when a plan is compiled: a field that is not a required field
// of the kind, an option no answer sets, "other" compared as an option, an
// other branch on a field that takes no text, a field that takes the
// owner's text read with no other branch, eq on a field of several
// choices, contains on a field of one, a title field that takes other
// text, and two templates of one kind are refused; a brief whose kind has
// no template is refused no_template. None is read as a default.
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
	replace := func(old, new string, n int) Template {
		t.Helper()
		if !strings.Contains(v, old) {
			t.Fatalf("the template does not hold %s", old)
		}
		return load(strings.Replace(v, old, new, n))
	}
	cases := map[string]Template{
		"a field no question sets":    replace(`"field":"software.data","op":"eq","value":"local"`, `"field":"software.colour","op":"eq","value":"local"`, 1),
		"a field not required":        replace(`"field":"hosts","op":"eq"`, `"field":"formats","op":"eq"`, 1),
		"an option no answer sets":    replace(`"value":"hosting"`, `"value":"kubernetes"`, 1),
		"other compared as an option": replace(`"value":"passkey"`, `"value":"other"`, 1),
		"other on a field of no text": replace(`{"field":"software.stack","op":"other"}`, `{"field":"software.data","op":"other"}`, 1),
		"no other branch":             replace(`{"field":"software.auth","op":"other"}`, `{"field":"software.stack","op":"other"}`, -1),
		"eq on several choices":       replace(`"field":"hosts","op":"eq","value":"file_host"`, `"field":"review_points","op":"eq","value":"plan"`, 1),
		"contains on one choice":      replace(`"field":"hosts","op":"eq","value":"file_host"`, `"field":"hosts","op":"contains","value":"file_host"`, 1),
		"a title of the owner's text": replace(`"title_field":"software.sub_kind"`, `"title_field":"software.stack"`, 1),
		"a title not required":        replace(`"title_field":"software.sub_kind"`, `"title_field":"software.colour"`, 1),
	}
	for name, tp := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := compileWith(b, c, []Template{tp})
			wantCode(t, err, brief.CodeTemplateInvalid)
		})
	}
	good := load(v)
	_, err := compileWith(b, c, []Template{good, good})
	wantCode(t, err, brief.CodeTemplateInvalid)
	ts, err := Templates()
	must(t, err)
	var others []Template
	for _, tp := range ts {
		if tp.Kind != brief.DomainSoftware {
			others = append(others, tp)
		}
	}
	_, err = compileWith(b, c, others)
	wantCode(t, err, brief.CodeNoTemplate)
	_, err = compileWith(b, c, []Template{{Kind: brief.DomainSoftware}})
	wantCode(t, err, brief.CodeTemplateInvalid)
	if _, err := compileWith(b, c, []Template{good}); err != nil {
		t.Fatal(err)
	}
}

// A template whose tasks would give a brief more than 12 tasks is refused
// when the plan is compiled, not cut short.
func TestATemplateOverTheCapIsRefused(t *testing.T) {
	c := v1(t)
	b := walk(t, c, map[string]catalog.Answer{"kind": pickOne("software"), "software.sub_kind": pickOne("web_app")}, defaults)
	var m map[string]any
	must(t, json.Unmarshal([]byte(softwareData(t)), &m))
	var tasks []any
	for i := 0; i < MaxTasksPerMilestone; i++ {
		tasks = append(tasks, map[string]any{"checks": []any{}, "depends_on": []any{}, "kind": "write", "ref": "more-" + strconv.Itoa(i),
			"scope": []any{"repo:docs"}, "size": "small", "title": "More work"})
	}
	m["milestones"] = append(m["milestones"].([]any), map[string]any{"title": "More", "goal": "More work", "tasks": tasks})
	data, err := json.Marshal(m)
	must(t, err)
	tp, err := LoadTemplate(data)
	if err != nil {
		t.Fatalf("the template with more tasks: %v", err)
	}
	_, err = compileWith(b, c, []Template{tp})
	wantCode(t, err, brief.CodeTemplateInvalid)
}
