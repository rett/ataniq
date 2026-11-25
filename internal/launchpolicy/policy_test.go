package launchpolicy

import (
	"bytes"
	_ "embed"
	"encoding/json"
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
const tinyPolicySHA256 = "db9acf90f5067b5fc67271b5cb793f9673fd0447003f9f37c55a4d155f99cc72"

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
	version2 := bytes.ReplaceAll(alphaPolicy, []byte("1.0"), []byte("2.0"))
	for _, gold := range [][]byte{alphaPolicy, betaPolicy, tinyPolicy, version2} {
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
	if decode(t, tinyPolicy).Digest() != tinyPolicySHA256 || decode(t, version2).Digest() == decode(t, alphaPolicy).Digest() {
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
