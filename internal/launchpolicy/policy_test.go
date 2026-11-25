package launchpolicy

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// These are literal wire documents. No test derives expected bytes from Policy.
//
//go:embed testdata/alpha.golden
var alphaPolicy []byte

//go:embed testdata/beta.golden
var betaPolicy []byte

//go:embed testdata/tiny.golden
var tinyPolicy []byte

const fixtureHelp = "-m,--mode=<v>   select a mode\n"
const tinyPolicySHA256 = "b81e8a914ebf362aaab2d49676714172840f27538e6200f43875d6e94b905ad7"

type obj = map[string]any

func wire(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func decode(t *testing.T, data []byte) *Policy {
	t.Helper()
	p, err := Decode(data, []byte(fixtureHelp))
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func tree(t *testing.T, b []byte) obj {
	t.Helper()
	var d obj
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return d
}
func row(rows any, key, value string) obj {
	for _, r := range rows.([]any) {
		v := r.(obj)
		if v[key] == value {
			return v
		}
	}
	panic("missing fixture row " + value)
}
func formRow(d obj, id string) obj { return row(d["forms"], "id", id) }
func modeRow(d obj, name string) obj {
	return row(d["slots"].(obj)["permission_mode"].(obj)["values"], "name", name)
}
func addForm(d obj, f obj) {
	rows := append(d["forms"].([]any), f)
	slices.SortFunc(rows, func(a, b any) int { return strings.Compare(a.(obj)["id"].(string), b.(obj)["id"].(string)) })
	d["forms"] = rows
}
func dropForm(d obj, id string) {
	d["forms"] = slices.DeleteFunc(d["forms"].([]any), func(r any) bool { return r.(obj)["id"] == id })
}
func fixed(d obj, token string) obj {
	return obj{"id": "extra", "shape": "exact", "token": token, "kinds": obj{"lead": "optional"}, "evidence": formRow(d, "bin")["evidence"]}
}

func TestT3StrictDocument(t *testing.T) {
	for _, b := range [][]byte{
		append(bytes.Clone(alphaPolicy), ' '), append(bytes.Clone(alphaPolicy), '\n'),
		bytes.Replace(alphaPolicy, []byte(`"schema":`), []byte(`"other":true,"schema":`), 1),
		bytes.Replace(alphaPolicy, []byte(`"schema":`), []byte(`"schema":"x","schema":`), 1),
		bytes.Replace(alphaPolicy, []byte(`"from":1`), []byte(`"from":null`), 1),
		bytes.Repeat([]byte(" "), 65<<10),
		bytes.Replace(alphaPolicy, []byte(`"version":"1.0"`), []byte(`"version":"a/b"`), 1),
		bytes.Replace(alphaPolicy, []byte(`"id":"agent"`), []byte(`"id":"z"`), 1),
	} {
		if _, err := Decode(b, []byte(fixtureHelp)); err == nil {
			t.Fatal("accepted malformed document")
		}
	}
}

func TestT4FormReservations(t *testing.T) {
	type mutation func(obj)
	alpha := map[string]mutation{
		"nested prefix": func(d obj) {
			formRow(d, "mode")["aliases"] = []any{obj{"prefix": "--mod", "evidence": formRow(d, "bin")["evidence"]}}
		},
		"exact under prefix":        func(d obj) { addForm(d, fixed(d, "--mode=x")) },
		"repeat slot":               func(d obj) { formRow(d, "agent")["kinds"].(obj)["lead"] = obj{"repeatable": 2} },
		"slot twice":                func(d obj) { formRow(d, "effort")["slot"] = "model" },
		"alias equals form":         func(d obj) { formRow(d, "mode")["aliases"].([]any)[0].(obj)["token"] = "--quiet" },
		"mode optional lead":        func(d obj) { formRow(d, "mode")["kinds"].(obj)["lead"] = "optional" },
		"mode optional coordinator": func(d obj) { delete(formRow(d, "mode")["kinds"].(obj), "coordinator") },
		"fixed twice":               func(d obj) { addForm(d, fixed(d, "--quiet")) },
		"separator alias": func(d obj) {
			formRow(d, "mode")["aliases"] = []any{obj{"token": "--", "evidence": formRow(d, "bin")["evidence"]}}
		},
		"bare name":            func(d obj) { delete(formRow(d, "mode"), "aliases"); addForm(d, fixed(d, "--mode")) },
		"reserved resume slot": func(d obj) { dropForm(d, "resume"); formRow(d, "model")["prefix"] = "--resume=" },
		"resume after split":   func(d obj) { formRow(d, "resume")["after_separator_index"] = 0 },
		"no executable":        func(d obj) { delete(formRow(d, "bin"), "at") },
	}
	for _, token := range []string{"-m=x", "--mod", "--mod=x", "--resume=r1"} {
		alpha[token] = func(d obj) { addForm(d, fixed(d, token)) }
	}
	beta := map[string]mutation{
		"no separator": func(d obj) { dropForm(d, "sep") },
		"positional collision": func(d obj) {
			d["slots"].(obj)["effort"] = tree(t, alphaPolicy)["slots"].(obj)["effort"]
			f := fixed(d, "")
			delete(f, "token")
			f["shape"] = "positional"
			f["slot"] = "effort"
			f["after_separator_index"] = 0
			f["transports"] = []string{"protocol"}
			addForm(d, f)
		},
		"exact and positional collision": func(d obj) {
			f := fixed(d, "after")
			f["after_separator_index"] = 0
			f["transports"] = []string{"protocol"}
			addForm(d, f)
		},
		"repeated separator": func(d obj) { formRow(d, "sep")["kinds"].(obj)["lead"] = obj{"repeatable": 2} },
		"positional resume":  func(d obj) { formRow(d, "model")["slot"] = "resume" },
	}
	for _, test := range []struct {
		b     []byte
		cases map[string]mutation
	}{{alphaPolicy, alpha}, {betaPolicy, beta}} {
		for name, mutate := range test.cases {
			t.Run(name, func(t *testing.T) {
				d := tree(t, test.b)
				mutate(d)
				if _, err := Decode(wire(t, d), []byte(fixtureHelp)); err == nil {
					t.Fatal("accepted invalid form")
				}
			})
		}
	}
}

func TestT5EvidenceAndGrammars(t *testing.T) {
	cases := map[string]func(obj){
		"class":              func(d obj) { modeRow(d, "m-plan")["class"] = "yolo" },
		"undocumented":       func(d obj) { modeRow(d, "m-ask")["basis"] = "undocumented" },
		"missing recognizer": func(d obj) { delete(modeRow(d, "m-plan"), "recognizer") },
		"invalid recognizer": func(d obj) { modeRow(d, "m-plan")["recognizer"] = "Auto" },
		"pending locator":    func(d obj) { formRow(d, "bin")["evidence"] = obj{"source": "pending"} },
		"unknown scope":      func(d obj) { formRow(d, "mode")["evidence"].(obj)["scope"] = "missing" },
		"wrong spelling":     func(d obj) { formRow(d, "quiet")["evidence"] = formRow(d, "mode")["evidence"] },
	}
	for _, g := range []string{"^.*$", "^a", "^abc$", "^a|b$"} {
		cases[g] = func(d obj) { d["slots"].(obj)["model"].(obj)["grammar"] = g }
	}
	for k, v := range (obj{"fixture_sha256": "different", "build": "2", "transport": "terminal", "value": "other", "profiles": []any{}, "sitting": ""}) {
		cases[k] = func(d obj) { modeRow(d, "m-ask")["vector"].(obj)["protocol"].(obj)[k] = v }
	}
	cases["terminal profiles"] = func(d obj) { modeRow(d, "m-ask")["vector"].(obj)["terminal"].(obj)["profiles"] = []string{"d1"} }
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := tree(t, alphaPolicy)
			mutate(d)
			if _, err := Decode(wire(t, d), []byte(fixtureHelp)); err == nil {
				t.Fatal("accepted invalid policy")
			}
		})
	}
	if _, err := Decode(alphaPolicy, []byte(fixtureHelp+".")); err == nil {
		t.Fatal("accepted changed fixture")
	}
}
func TestT6Canonical(t *testing.T) {
	// Tiny is the literal golden for a second version of the same pack.
	for _, gold := range [][]byte{alphaPolicy, betaPolicy, tinyPolicy} {
		var spaced bytes.Buffer
		if err := json.Indent(&spaced, gold, "", "  "); err != nil {
			t.Fatal(err)
		}
		p := decode(t, spaced.Bytes())
		if !bytes.Equal(Canonical(p), gold) {
			t.Fatalf("wrong canonical bytes:\n%s", Canonical(p))
		}
		if decode(t, Canonical(p)).Digest() != p.Digest() {
			t.Fatal("round trip changed digest")
		}
	}
	if decode(t, tinyPolicy).Digest() != tinyPolicySHA256 || decode(t, tinyPolicy).Digest() == decode(t, alphaPolicy).Digest() {
		t.Fatal("digest vector or version binding differs")
	}
	// A pending locator is legal only on an unrecorded policy. Its canonical
	// encoding therefore needs a second vector distinct from the observation.
	pending := unrecorded(t, tinyPolicy)
	p := decode(t, pending)
	if !bytes.Contains(Canonical(p), []byte(`"evidence":{"source":"pending"}`)) {
		t.Fatal("pending locator encoding")
	}
}

func TestT5ProtocolProfileRequiresItsOwnSitting(t *testing.T) {
	const hash = "ecf2ffecfebfd6330f0a5bda285f7bb44ad6e74dc8b8311b8b7e5dd91fe93a88"
	first := ProtocolExercise{Pack: "alpha", Build: "1.0", FixtureSHA256: hash,
		Observation: "synthetic", Mode: "m-quiet", ProfileDigest: "d1"}
	ask := first
	ask.Mode = "m-ask"
	records := map[ProtocolExercise]string{first: "quiet-first", ask: "ask-first"}
	p := decode(t, alphaPolicy)
	if err := p.CheckProtocolSittings(records); err != nil {
		t.Fatal(err)
	}

	d := tree(t, alphaPolicy)
	modeRow(d, "m-quiet")["vector"].(obj)["protocol"].(obj)["profiles"] = []string{"d1", "d2"}
	p = decode(t, wire(t, d))
	if err := p.CheckProtocolSittings(records); err == nil {
		t.Fatal("qualified an appended profile without a sitting")
	}
	second := first
	second.ProfileDigest = "d2"
	for _, wrong := range []func(*ProtocolExercise){
		func(e *ProtocolExercise) { e.Observation = "other" },
		func(e *ProtocolExercise) { e.FixtureSHA256 = "other" },
		func(e *ProtocolExercise) { e.Build = "2.0" },
		func(e *ProtocolExercise) { e.Pack = "beta" },
		func(e *ProtocolExercise) { e.Mode = "m-ask" },
	} {
		e := second
		wrong(&e)
		records[e] = "other-sitting"
		if err := p.CheckProtocolSittings(records); err == nil {
			t.Fatal("qualified an appended profile with unrelated evidence")
		}
		delete(records, e)
	}
	records[second] = records[first]
	if err := p.CheckProtocolSittings(records); err == nil {
		t.Fatal("qualified two profiles with one sitting")
	}
	records[second] = "quiet-second"
	if err := p.CheckProtocolSittings(records); err != nil {
		t.Fatal(err)
	}
}
func unrecorded(t *testing.T, b []byte) []byte {
	d := tree(t, b)
	d["fixture"].(obj)["sha256"] = ""
	for _, m := range d["slots"].(obj)["permission_mode"].(obj)["values"].([]any) {
		v := m.(obj)["vector"].(obj)
		for k := range v {
			v[k] = "pending"
		}
	}
	formRow(d, "bin")["evidence"] = obj{"source": "pending"}
	return wire(t, d)
}

func TestOptionalMembersRejectNull(t *testing.T) {
	for _, mutate := range []func(obj){
		func(d obj) { formRow(d, "mode")["aliases"] = nil },
		func(d obj) { formRow(d, "bin")["at"] = nil },
		func(d obj) { d["slots"].(obj)["model"] = nil },
		func(d obj) { modeRow(d, "m-ask")["vector"].(obj)["protocol"] = nil },
		func(d obj) { formRow(d, "bin")["separator"] = nil },
		func(d obj) { d["files"].([]any)[1].(obj)["allowed"] = []any{nil} },
	} {
		d := tree(t, alphaPolicy)
		mutate(d)
		if _, err := Decode(wire(t, d), []byte(fixtureHelp)); err == nil {
			t.Fatal("accepted null member")
		}
	}
}

func TestOptionalMemberPresence(t *testing.T) {
	for _, change := range []func(obj){
		func(d obj) { formRow(d, "bin")["prefix"] = "" },
		func(d obj) { formRow(d, "bin")["slot"] = "" },
		func(d obj) { formRow(d, "model")["token"] = "" },
		func(d obj) { formRow(d, "bin")["evidence"].(obj)["scope"] = "" },
		func(d obj) { modeRow(d, "q-plan")["recognizer"] = "" },
	} {
		d := tree(t, betaPolicy)
		change(d)
		if _, err := Decode(wire(t, d), []byte(fixtureHelp)); err == nil {
			t.Fatal("accepted an inapplicable empty field")
		}
	}
}

func TestCanonicalSizeBound(t *testing.T) {
	// A short JSON escape occupies two input bytes but six canonical bytes.
	d := tree(t, alphaPolicy)
	formRow(d, "bin")["evidence"].(obj)["ref"] = strings.Repeat("\n", 12000)
	raw := wire(t, d)
	if len(raw) > 64<<10 {
		t.Fatal("fixture already exceeds the input bound")
	}
	if _, err := Decode(raw, []byte(fixtureHelp)); err == nil {
		t.Fatal("canonical encoding cannot be decoded within the bound")
	}
}

func TestHelpDefinitionEvidence(t *testing.T) {
	for _, tc := range []struct {
		line, spelling string
		want           bool
	}{
		{"  -m, --mode=<v>   select a value", "--mode=", true},
		{"--[no-]quiet     suppress output", "--no-quiet", true},
		{"--[no-]quiet     suppress output", "--[no-]quiet", false},
		{"-m <mode>, --mode=<mode>  choose mode", "--mode=", true},
		{"-m <mode>, --mode=<mode>  choose mode", "--mode=<mode>", false},
		{"-m<mode>, --mode MODE   choose mode", "-m", true},
		{"--quiet   see -m, --mode=<mode>", "--mode=", false},
		{"--quiet, broken", "--quiet", false},
		{"--mode=m-plan    select this value", "--mode=m-plan", true},
		{"examples: --mode=m-plan", "--mode=m-plan", false},
		{"--quiet    see --mode=m-plan", "--mode=m-plan", false},
		{"--mode=m-edit", "--mode=m-plan", false},
	} {
		if got := defines(tc.line, tc.spelling); got != tc.want {
			t.Errorf("defines(%q, %q) = %t", tc.line, tc.spelling, got)
		}
	}
}

func TestGroupedHelpLocator(t *testing.T) {
	const help = "-m <mode>, --mode=<mode>  choose mode\n--[no-]quiet  suppress output\n"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(help)))
	raw := bytes.ReplaceAll(alphaPolicy, []byte(fmt.Sprintf("%x", sha256.Sum256([]byte(fixtureHelp)))), []byte(hash))
	for _, tc := range []struct {
		token string
		valid bool
	}{{"--quiet", true}, {"--no-quiet", true}, {"--[no-]quiet", false}} {
		t.Run(tc.token, func(t *testing.T) {
			d := tree(t, raw)
			scope := d["options"].([]any)[0].(obj)
			scope["to"] = 2
			f := formRow(d, "quiet")
			f["token"] = tc.token
			f["evidence"] = obj{"source": "help", "scope": scope["scope"], "line": 2}
			_, err := Decode(wire(t, d), []byte(help))
			if (err == nil) != tc.valid {
				t.Fatalf("valid = %t, decode error = %v", tc.valid, err)
			}
		})
	}
}
