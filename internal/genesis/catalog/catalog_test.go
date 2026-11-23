package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"ataniq/internal/genesis/brief"
)

var t0 = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)

// v1Digest pins the embedded catalog: a change to it is a new version.
const v1Digest = "8c688a7ac29cecd82b2e2bfc0d6b1ceb73695019491f6576aa071521903b6dad"

func mustV1(t testing.TB) Catalog {
	t.Helper()
	c, err := V1()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func wantCode(t testing.TB, err error, code brief.Code) {
	t.Helper()
	got, ok := brief.CodeOf(err)
	if !ok || got != code {
		t.Fatalf("got %v, want the code %s", err, code)
	}
}

// The embedded file is canonical, its digest is that of its bytes, and
// the digest is pinned.
func TestEmbeddedIsCanonical(t *testing.T) {
	c := mustV1(t)
	out, err := Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, v1Data) {
		t.Fatal("catalog_v1.json is not in the canonical form")
	}
	sum := sha256.Sum256(v1Data)
	if c.Digest() != hex.EncodeToString(sum[:]) {
		t.Fatal("the digest is not the SHA-256 of the file")
	}
	if c.Digest() != v1Digest {
		t.Fatalf("the digest is %s; a changed catalog is a new version", c.Digest())
	}
	if c.Version != "v1" {
		t.Fatalf("the version is %q", c.Version)
	}
}

// A caller that changes its copy of the catalog changes no other copy.
func TestV1ReturnsACopy(t *testing.T) {
	a := mustV1(t)
	a.Nodes[0].Prompt = "changed"
	a.Nodes[1].When.Value = "changed"
	a.Required["software"][0] = "changed"
	b := mustV1(t)
	if b.Nodes[0].Prompt == "changed" || b.Nodes[1].When.Value == "changed" || b.Required["software"][0] == "changed" {
		t.Fatal("a change to one copy reached another")
	}
	out, err := Encode(b)
	if err != nil || !bytes.Equal(out, v1Data) {
		t.Fatal("the catalog changed")
	}
}

// The embedded file's own constraints, checked here on their own and not
// only through Load: plain texts within their caps, no markup and no web
// address, 2 to 8 options, unique ids, the size of the catalog, and the
// three first leaves.
func TestEmbeddedConstraints(t *testing.T) {
	c := mustV1(t)
	if len(c.Nodes) < 20 || len(c.Nodes) > 26 {
		t.Fatalf("%d nodes, want about 20 to 25", len(c.Nodes))
	}
	text := func(where, s string, max int) {
		t.Helper()
		if s == "" || len(s) > max || strings.ContainsAny(s, "<>\n\t") || strings.Contains(s, "://") ||
			strings.Contains(strings.ToLower(s), "www.") || strings.Contains(s, "http") {
			t.Errorf("%s: %q breaks the text rules", where, s)
		}
	}
	ids := map[string]bool{}
	for _, n := range c.Nodes {
		if ids[n.ID] {
			t.Errorf("the node id %s repeats", n.ID)
		}
		ids[n.ID] = true
		text(n.ID+" prompt", n.Prompt, 160)
		if n.Type != Scale && (len(n.Options) < 2 || len(n.Options) > 8) {
			t.Errorf("%s has %d options", n.ID, len(n.Options))
		}
		opts := map[string]bool{}
		for _, o := range n.Options {
			if opts[o.ID] {
				t.Errorf("%s repeats the option %s", n.ID, o.ID)
			}
			opts[o.ID] = true
			text(n.ID+" label", o.Label, 60)
			text(n.ID+" example", o.Example, 100)
		}
	}
	// Each kind of the first slice and its one leaf.
	for kind, leaf := range map[string][]string{
		"software":   {"web_app", "web_tool"},
		"publishing": {"newsletter", "article"},
		"media":      {"podcast", "book"},
	} {
		n, ok := c.Node(kind + ".sub_kind")
		if !ok {
			t.Fatalf("no sub-kind node for %s", kind)
		}
		for _, id := range leaf {
			if !n.hasOption(id) {
				t.Errorf("%s has no option %s", n.ID, id)
			}
		}
	}
	kind, _ := c.Node("kind")
	if !kind.AllowOther || kind.Default != "" || kind.AllowYouDecide {
		t.Fatal("the kind question must take other text and be answered by the owner")
	}
	// Audience, budget and rights are always the owner's choice.
	for _, id := range []string{"audience", "budget_band", "media.rights", "publishing.channel"} {
		n, _ := c.Node(id)
		if n.Default != "" || n.AllowYouDecide {
			t.Errorf("%s can be left to a default", id)
		}
	}
}

// base is a small valid catalog the refusal cases change.
func base() Catalog {
	return Catalog{
		Version: "t1",
		Nodes: []Node{
			{ID: "kind", Field: "kind", Type: Single, Topic: "kind", Prompt: "What kind?", Weight: 100, RoundHint: 1,
				Options: []Option{{"software", "Software", "An app"}, {"media", "Media", "A podcast"}}, AllowOther: true,
				Invalidates: []string{"software.sub_kind", "formats"}},
			{ID: "software.sub_kind", Field: "software.sub_kind", Type: Single, Topic: "about", Prompt: "Which app?", Weight: 90, RoundHint: 2,
				Options: []Option{{"web_app", "Web app", "A site"}, {"web_tool", "Web tool", "A tool"}},
				When:    &Predicate{Op: OpEq, Field: "kind", Value: "software"}, Invalidates: []string{"software.stack"}},
			{ID: "software.stack", Field: "software.stack", Type: Single, Topic: "build", Prompt: "Which language?", Weight: 80, RoundHint: 3,
				Options: []Option{{"go", "Go", "Fast"}, {"python", "Python", "Readable"}}, AllowYouDecide: true, YouDecide: brief.SupervisorInferred, Default: "go",
				When: &Predicate{Op: OpSet, Field: "software.sub_kind"}, Invalidates: []string{}},
			{ID: "formats", Field: "formats", Type: Multi, Topic: "about", Prompt: "Which form?", Weight: 85, RoundHint: 2,
				Options: []Option{{"audio", "Audio", "A recording"}, {"video", "Video", "A film"}},
				When:    &Predicate{Op: OpEq, Field: "kind", Value: "other"}, Invalidates: []string{}},
			{ID: "goal", Field: "goal", Type: Scale, Topic: "about", Prompt: "How important, 1 to 5?", Weight: 70, RoundHint: 2, Default: "3", Invalidates: []string{}},
			{ID: "hosts", Field: "hosts", Type: YesNo, Topic: "where", Prompt: "Your own server?", Weight: 60, RoundHint: 4,
				Options: []Option{{"yes", "Yes", "Yours"}, {"no", "No", "Rented"}}, Invalidates: []string{}},
		},
		Required: map[string][]string{
			"software":   {"kind", "software.sub_kind", "software.stack", "goal", "hosts"},
			"publishing": {"kind", "goal"},
			"media":      {"kind", "goal", "hosts"},
			"other":      {"kind", "formats", "goal"},
		},
	}
}

func load(t testing.TB, c Catalog) (Catalog, error) {
	t.Helper()
	out, err := Encode(c)
	if err != nil {
		t.Fatal(err)
	}
	return Load(out)
}

func TestBaseLoads(t *testing.T) {
	c, err := load(t, base())
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Closure("kind"); strings.Join(got, ",") != "formats,software.stack,software.sub_kind" {
		t.Fatalf("the closure of kind is %v", got)
	}
}

func deep(depth int) *Predicate {
	p := Predicate{Op: OpSet, Field: "kind"}
	for i := 1; i < depth; i++ {
		p = Predicate{Op: OpNot, Args: []Predicate{p}}
	}
	return &p
}

func TestLoadRefuses(t *testing.T) {
	cases := []struct {
		name   string
		change func(c *Catalog)
	}{
		{"predicate too deep", func(c *Catalog) { c.Nodes[2].When = deep(5) }},
		{"unknown field in a condition", func(c *Catalog) { c.Nodes[2].When = &Predicate{Op: OpSet, Field: "schedule"} }},
		{"field name in a condition", func(c *Catalog) { c.Nodes[2].When = &Predicate{Op: OpSet, Field: "Kind"} }},
		{"own field in a condition", func(c *Catalog) { c.Nodes[2].When = &Predicate{Op: OpUnset, Field: "software.stack"} }},
		{"condition on a later field", func(c *Catalog) {
			c.Nodes[1].When = &Predicate{Op: OpAnd, Args: []Predicate{{Op: OpEq, Field: "kind", Value: "software"}, {Op: OpSet, Field: "software.stack"}}}
			c.Nodes[2].Invalidates = []string{"software.sub_kind"}
		}},
		{"condition on a same-round field", func(c *Catalog) { c.Nodes[3].When = &Predicate{Op: OpSet, Field: "goal"} }},
		{"value no node can set", func(c *Catalog) { c.Nodes[1].When = &Predicate{Op: OpEq, Field: "kind", Value: "books"} }},
		{"in without values", func(c *Catalog) { c.Nodes[1].When = &Predicate{Op: OpIn, Field: "kind"} }},
		{"in repeating a value", func(c *Catalog) {
			c.Nodes[1].When = &Predicate{Op: OpIn, Field: "kind", Values: []string{"software", "software"}}
		}},
		{"and with one argument", func(c *Catalog) {
			c.Nodes[1].When = &Predicate{Op: OpAnd, Args: []Predicate{{Op: OpEq, Field: "kind", Value: "software"}}}
		}},
		{"not with two arguments", func(c *Catalog) {
			c.Nodes[1].When = &Predicate{Op: OpNot, Args: []Predicate{{Op: OpSet, Field: "kind"}, {Op: OpSet, Field: "kind"}}}
		}},
		{"unknown op", func(c *Catalog) { c.Nodes[1].When = &Predicate{Op: "or", Field: "kind"} }},
		{"one option", func(c *Catalog) { c.Nodes[1].Options = c.Nodes[1].Options[:1] }},
		{"nine options", func(c *Catalog) {
			for i := 0; i < 7; i++ {
				c.Nodes[1].Options = append(c.Nodes[1].Options, Option{string(rune('a' + i)), "Label", "Example"})
			}
		}},
		{"markup in a prompt", func(c *Catalog) { c.Nodes[1].Prompt = "Which <b>app</b>?" }},
		{"markup in a label", func(c *Catalog) { c.Nodes[1].Options[0].Label = "Web > app" }},
		{"web address in an example", func(c *Catalog) { c.Nodes[1].Options[0].Example = "like https://example.org" }},
		{"bare web address", func(c *Catalog) { c.Nodes[1].Options[0].Example = "like www.example.org" }},
		{"prompt too long", func(c *Catalog) { c.Nodes[1].Prompt = strings.Repeat("a", MaxPrompt+1) }},
		{"label too long", func(c *Catalog) { c.Nodes[1].Options[0].Label = strings.Repeat("a", MaxLabel+1) }},
		{"example too long", func(c *Catalog) { c.Nodes[1].Options[0].Example = strings.Repeat("a", MaxExample+1) }},
		{"empty example", func(c *Catalog) { c.Nodes[1].Options[0].Example = "" }},
		{"text with a trailing space", func(c *Catalog) { c.Nodes[1].Prompt = "Which app? " }},
		{"duplicate node ids", func(c *Catalog) { c.Nodes[2].ID = "software.sub_kind" }},
		{"duplicate option ids", func(c *Catalog) { c.Nodes[1].Options[1].ID = "web_app" }},
		{"reserved option id", func(c *Catalog) { c.Nodes[1].Options[1].ID = "other" }},
		{"option id form", func(c *Catalog) { c.Nodes[1].Options[1].ID = "Web" }},
		{"node id form", func(c *Catalog) { c.Nodes[4].ID = "Goal" }},
		{"field not of the brief", func(c *Catalog) { c.Nodes[4].Field = "colour" }},
		{"unknown type", func(c *Catalog) { c.Nodes[4].Type = "text" }},
		{"topic form", func(c *Catalog) { c.Nodes[4].Topic = "About" }},
		{"yes-no with other ids", func(c *Catalog) { c.Nodes[5].Options[1].ID = "nope" }},
		{"scale with options", func(c *Catalog) { c.Nodes[4].Options = c.Nodes[1].Options }},
		{"other text on a yes-no", func(c *Catalog) { c.Nodes[5].AllowOther = true }},
		{"you decide without a default", func(c *Catalog) { c.Nodes[2].Default = "" }},
		{"you decide without a rule", func(c *Catalog) { c.Nodes[2].YouDecide = "" }},
		{"you decide as owner-chosen", func(c *Catalog) { c.Nodes[2].YouDecide = brief.OwnerChosen }},
		{"rule without you decide", func(c *Catalog) { c.Nodes[4].YouDecide = brief.Default }},
		{"default not a choice", func(c *Catalog) { c.Nodes[2].Default = "rust" }},
		{"scale default out of range", func(c *Catalog) { c.Nodes[4].Default = "6" }},
		{"invalidates an unknown field", func(c *Catalog) { c.Nodes[2].Invalidates = []string{"schedule"} }},
		{"invalidates its own field", func(c *Catalog) { c.Nodes[2].Invalidates = []string{"software.stack"} }},
		{"invalidates a field twice", func(c *Catalog) { c.Nodes[1].Invalidates = []string{"software.stack", "software.stack"} }},
		{"cycle in invalidates", func(c *Catalog) { c.Nodes[2].Invalidates = []string{"software.sub_kind"} }},
		{"longer cycle", func(c *Catalog) {
			c.Nodes[4].Invalidates = []string{"hosts"}
			c.Nodes[5].Invalidates = []string{"goal"}
		}},
		{"stale answer left behind", func(c *Catalog) { c.Nodes[1].Invalidates = []string{} }},
		{"kind change leaves an extension field", func(c *Catalog) {
			c.Nodes[0].Invalidates = []string{"formats"}
			c.Nodes[1].When = nil
			c.Nodes[1].RoundHint = 1
		}},
		{"no kind node", func(c *Catalog) { c.Nodes[0].Field = "schedule" }},
		{"required without kind", func(c *Catalog) { c.Required["media"] = []string{"goal"} }},
		{"required unknown field", func(c *Catalog) { c.Required["media"] = []string{"kind", "schedule"} }},
		{"required of another domain", func(c *Catalog) { c.Required["media"] = []string{"kind", "software.stack"} }},
		{"required twice", func(c *Catalog) { c.Required["media"] = []string{"kind", "goal", "goal"} }},
		{"weight zero", func(c *Catalog) { c.Nodes[4].Weight = 0 }},
		{"round hint past the last round", func(c *Catalog) { c.Nodes[5].RoundHint = MaxRounds + 1 }},
		{"version form", func(c *Catalog) { c.Version = "V 1" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			tc.change(&c)
			_, err := load(t, c)
			wantCode(t, err, brief.CodeCatalogInvalid)
			if strings.Contains(err.Error(), "<") || strings.Contains(err.Error(), "example.org") {
				t.Fatalf("the detail quotes the data: %v", err)
			}
		})
	}
}

func TestLoadRefusesData(t *testing.T) {
	good, err := Encode(base())
	if err != nil {
		t.Fatal(err)
	}
	g := string(good)
	cases := map[string]string{
		"unknown top field":  strings.Replace(g, `"nodes":`, `"extra":1,"nodes":`, 1),
		"unknown node field": strings.Replace(g, `"allow_other":true`, `"allow_other":true,"extra":1`, 1),
		"repeated key":       strings.Replace(g, `"allow_other":true`, `"allow_other":true,"allow_other":true`, 1),
		"float":              strings.Replace(g, `"weight":100`, `"weight":100.0`, 1),
		"null":               strings.Replace(g, `"weight":100`, `"weight":null`, 1),
		"schema":             strings.Replace(g, Schema, "ataniq.genesis-catalog/2", 1),
		"missing required":   strings.Replace(g, `"other":["kind","formats","goal"],`, ``, 1),
		"unknown kind":       strings.Replace(g, `"other":`, `"music":["kind"],"other":`, 1),
		"empty default":      strings.Replace(g, `"default":"go"`, `"default":""`, 1),
		"predicate field":    strings.Replace(g, `{"field":"kind","op":"eq","value":"software"}`, `{"field":"kind","op":"eq","value":"software","values":["x"]}`, 1),
		"control character":  strings.Replace(g, `"What kind?"`, `"What\tkind?"`, 1),
		"not an object":      `[]`,
		"trailing data":      g + "{}",
	}
	for name, data := range cases {
		if data == g {
			t.Fatalf("%s: the replacement did not apply", name)
		}
		_, err := Load([]byte(data))
		if code, _ := brief.CodeOf(err); code != brief.CodeCatalogInvalid {
			t.Errorf("%s: got %v", name, err)
		}
	}
	_, err = Load(append(good, bytes.Repeat([]byte(" "), MaxCatalogBytes)...))
	wantCode(t, err, brief.CodeTooLarge)
	// Any JSON spelling of a valid catalog loads to the same digest.
	spaced := strings.ReplaceAll(g, `,"`, `, "`)
	c1, err := Load([]byte(spaced))
	if err != nil {
		t.Fatal(err)
	}
	c2, err := Load(good)
	if err != nil {
		t.Fatal(err)
	}
	if c1.Digest() != c2.Digest() {
		t.Fatal("two spellings of one catalog have different digests")
	}
}

func set(t testing.TB, b *brief.Brief, field string, v brief.Value) {
	t.Helper()
	if err := b.Set(field, v, brief.OwnerChosen, "n", 1, t0); err != nil {
		t.Fatal(err)
	}
}

func TestPredicateOps(t *testing.T) {
	b := brief.New(strings.Repeat("0", 64))
	set(t, &b, "kind", brief.One("software"))
	set(t, &b, "review_points", brief.Many("demo", "plan"))
	set(t, &b, "software.auth", brief.OtherText("a link by email"))
	eq := func(f, v string) Predicate { return Predicate{Op: OpEq, Field: f, Value: v} }
	cases := []struct {
		name string
		p    Predicate
		want bool
	}{
		{"eq holds", eq("kind", "software"), true},
		{"eq another value", eq("kind", "media"), false},
		{"eq unset field", eq("goal", "try"), false},
		{"eq in several choices", eq("review_points", "plan"), true},
		{"eq not among choices", eq("review_points", "publish"), false},
		{"eq other matches free text", eq("software.auth", "other"), true},
		{"eq choice against free text", eq("software.auth", "none"), false},
		{"in holds", Predicate{Op: OpIn, Field: "kind", Values: []string{"media", "software"}}, true},
		{"in misses", Predicate{Op: OpIn, Field: "kind", Values: []string{"media", "publishing"}}, false},
		{"in over several choices", Predicate{Op: OpIn, Field: "review_points", Values: []string{"publish", "demo"}}, true},
		{"in unset field", Predicate{Op: OpIn, Field: "goal", Values: []string{"try"}}, false},
		{"set holds", Predicate{Op: OpSet, Field: "kind"}, true},
		{"set unset field", Predicate{Op: OpSet, Field: "goal"}, false},
		{"unset holds", Predicate{Op: OpUnset, Field: "goal"}, true},
		{"unset set field", Predicate{Op: OpUnset, Field: "kind"}, false},
		{"and holds", Predicate{Op: OpAnd, Args: []Predicate{eq("kind", "software"), {Op: OpSet, Field: "software.auth"}}}, true},
		{"and fails", Predicate{Op: OpAnd, Args: []Predicate{eq("kind", "software"), {Op: OpSet, Field: "goal"}}}, false},
		{"not holds", Predicate{Op: OpNot, Args: []Predicate{eq("kind", "media")}}, true},
		{"not fails", Predicate{Op: OpNot, Args: []Predicate{eq("kind", "software")}}, false},
		{"nested", Predicate{Op: OpNot, Args: []Predicate{{Op: OpAnd, Args: []Predicate{eq("kind", "software"), {Op: OpUnset, Field: "goal"}}}}}, false},
		{"unknown op", Predicate{Op: "or", Field: "kind"}, false},
		{"empty and", Predicate{Op: OpAnd}, false},
	}
	for _, c := range cases {
		if got := c.p.Eval(b); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	// Each op encodes and decodes to itself.
	for _, c := range cases[:20] {
		back, err := decodePredicate(c.p.encode(), "when", 1)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if back.Eval(b) != c.want {
			t.Errorf("%s: the decoded predicate differs", c.name)
		}
	}
}
