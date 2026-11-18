package record

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// kinds has, for each kind of record, a function that decodes a vector and
// encodes the result again.
var kinds = map[string]func([]byte) ([]byte, any, error){
	"request": func(data []byte) ([]byte, any, error) {
		r, err := DecodeRequest(data)
		if err != nil {
			return nil, nil, err
		}
		out, err := EncodeRequest(r)
		return out, r, err
	},
	"pending": func(data []byte) ([]byte, any, error) {
		p, err := DecodePending(data)
		if err != nil {
			return nil, nil, err
		}
		out, err := EncodePending(p)
		return out, p, err
	},
	"decision": func(data []byte) ([]byte, any, error) {
		d, err := DecodeDecision(data)
		if err != nil {
			return nil, nil, err
		}
		out, err := EncodeDecision(d)
		return out, d, err
	},
}

func readVector(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// kindOf returns the kind a vector's file name starts with.
func kindOf(t testing.TB, name string) string {
	t.Helper()
	kind, _, _ := strings.Cut(name, "-")
	if kinds[kind] == nil {
		t.Fatalf("%s: the name does not start with a kind of record", name)
	}
	return kind
}

// vectorNames returns the files under testdata whose names match the
// pattern.
func vectorNames(t testing.TB, pattern string) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("testdata", pattern))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(paths))
	for _, p := range paths {
		names = append(names, filepath.Base(p))
	}
	sort.Strings(names)
	return names
}

// validVectors returns the canonical valid vectors: every *-valid*.json
// that is not a pretty copy, a digest file or a part of a record.
func validVectors(t testing.TB) []string {
	t.Helper()
	var names []string
	for _, name := range vectorNames(t, "*-valid*.json") {
		if strings.Count(name, ".") == 1 {
			names = append(names, name)
		}
	}
	return names
}

func TestValidVectorsRoundTrip(t *testing.T) {
	names := validVectors(t)
	for _, kind := range []string{"request", "pending", "decision"} {
		if !contains(names, kind+"-valid.json") {
			t.Errorf("no valid vector for a %s", kind)
		}
	}
	for _, name := range names {
		data := readVector(t, name)
		out, _, err := kinds[kindOf(t, name)](data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !bytes.Equal(out, data) {
			t.Errorf("%s: decode then encode changed the bytes:\n got %s\nwant %s", name, out, data)
		}
	}
}

// A pretty copy is the same record with whitespace, keys in the order of
// the design and every non-ASCII character as a \u escape. It decodes to
// the same value and encodes to the canonical bytes.
func TestPrettyVectorsDecodeToTheSameRecord(t *testing.T) {
	names := vectorNames(t, "*-valid.pretty.json")
	if len(names) != 3 {
		t.Fatalf("pretty copies: %v, want one for each kind", names)
	}
	for _, name := range names {
		canonical := readVector(t, strings.Replace(name, ".pretty", "", 1))
		decode := kinds[kindOf(t, name)]
		out, got, err := decode(readVector(t, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !bytes.Equal(out, canonical) {
			t.Errorf("%s: encodes to\n%s\nwant\n%s", name, out, canonical)
		}
		if _, want, _ := decode(canonical); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: decodes to another value than the canonical vector", name)
		}
	}
}

// The digest files were written by another implementation of the canonical
// form. This package must compute the same.
func TestVectorDigests(t *testing.T) {
	digests := func(name string) map[string]string {
		var m map[string]string
		if err := json.Unmarshal(readVector(t, name), &m); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return m
	}
	check := func(file, key, got string) {
		t.Helper()
		if want := digests(file)[key]; want == "" || got != want {
			t.Errorf("%s: %s = %s, want %s", file, key, got, want)
		}
	}

	requestBytes := readVector(t, "request-valid.json")
	check("request-valid.digests.json", "record_sha256", Digest(requestBytes))
	request, err := DecodeRequest(requestBytes)
	if err != nil {
		t.Fatal(err)
	}
	check("request-valid.digests.json", "note_sha256", request.NoteSHA256)

	pendingBytes := readVector(t, "pending-valid.json")
	pending, err := DecodePending(pendingBytes)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := BindingDigest(pending.Binding)
	if err != nil {
		t.Fatal(err)
	}
	const file = "pending-valid.digests.json"
	check(file, "record_sha256", Digest(pendingBytes))
	check(file, "binding_sha256", binding)
	check(file, "binding_sha256", pending.BindingSHA256)
	check(file, "binding_sha256", Digest(readVector(t, "pending-valid.binding.json")))
	check(file, "plan_sha256", PlanDigest(string(pending.PlanText)))
	check(file, "plan_sha256", pending.Binding.PlanSHA256)
	check(file, "request_sha256", Digest(requestBytes))
	check(file, "request_sha256", pending.RequestSHA256)

	decisionBytes := readVector(t, "decision-valid.json")
	decision, err := DecodeDecision(decisionBytes)
	if err != nil {
		t.Fatal(err)
	}
	check("decision-valid.digests.json", "record_sha256", Digest(decisionBytes))
	check("decision-valid.digests.json", "binding_sha256", decision.BindingSHA256)

	// The three valid vectors are one approval: the decision answers the
	// pending record, with exactly its sign-offs.
	if decision.ID != pending.ID || decision.BindingSHA256 != pending.BindingSHA256 {
		t.Error("the valid decision does not answer the valid pending record")
	}
	if !SignOffsEqual(pending.Binding.Summary.Flagged, decision.SignedOff) {
		t.Error("the valid decision's sign-offs are not the pending record's flagged list")
	}
}

func TestInvalidVectorsAreRefusedWithTheirCode(t *testing.T) {
	var expect map[string]struct{ Code, Why string }
	if err := json.Unmarshal(readVector(t, "invalid.json"), &expect); err != nil {
		t.Fatal(err)
	}
	names := vectorNames(t, "*-invalid-*.json")
	if len(names) != len(expect) {
		t.Errorf("%d invalid vectors and %d entries in invalid.json", len(names), len(expect))
	}
	perKind := make(map[string]int)
	for _, name := range names {
		want, ok := expect[name]
		if !ok {
			t.Errorf("%s: not in invalid.json", name)
			continue
		}
		kind := kindOf(t, name)
		perKind[kind]++
		_, _, err := kinds[kind](readVector(t, name))
		if err == nil {
			t.Errorf("%s: decoded, want %s (%s)", name, want.Code, want.Why)
			continue
		}
		if code, _ := CodeOf(err); string(code) != want.Code {
			t.Errorf("%s: refused %v, want %s (%s)", name, err, want.Code, want.Why)
		}
	}
	// Every way of being wrong that the vectors must cover, for each kind.
	for kind := range kinds {
		if perKind[kind] < 5 {
			t.Errorf("%d invalid vectors for a %s, want at least 5", perKind[kind], kind)
		}
		for _, what := range []string{"unknown-field", "float", "control-character", "key-case",
			"repeated-key", "too-large", "schema", "id"} {
			if _, ok := expect[kind+"-invalid-"+what+".json"]; !ok {
				t.Errorf("no vector %s-invalid-%s.json", kind, what)
			}
		}
	}
	for _, name := range []string{"request-invalid-feature-name.json", "pending-invalid-feature-name.json",
		"pending-invalid-b64-plain.json", "decision-invalid-b64-plain.json"} {
		if _, ok := expect[name]; !ok {
			t.Errorf("no vector %s", name)
		}
	}
}

// The README says what each file is, so every file must be named in it,
// by its own name or, for an invalid vector, by its kind's pattern.
func TestReadmeNamesEveryVector(t *testing.T) {
	readme := string(readVector(t, "README.md"))
	for _, name := range vectorNames(t, "*") {
		if name == "README.md" || strings.Contains(readme, "`"+name+"`") {
			continue
		}
		kind, rest, _ := strings.Cut(name, "-")
		if strings.HasPrefix(rest, "invalid-") && strings.Contains(readme, "`"+kind+"-invalid-*.json`") {
			continue
		}
		t.Errorf("testdata/README.md does not say what %s is", name)
	}
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}
