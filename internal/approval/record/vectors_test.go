package record

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
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

	// Every valid pending record answers the valid request of its verb: it
	// names that request's id and the digest of that request's bytes.
	requests := map[string]string{VerbFeatureFinish: "request-valid.json", VerbBranchPrune: "request-valid-prune.json"}
	answered := make(map[string]bool)
	for _, name := range validVectors(t) {
		if kindOf(t, name) != "pending" {
			continue
		}
		p, err := DecodePending(readVector(t, name))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		file := requests[p.Binding.Verb]
		r, err := DecodeRequest(readVector(t, file))
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		if r.Verb != p.Binding.Verb || !reflect.DeepEqual(r.Argv, p.Binding.Argv) {
			t.Errorf("%s: the verb and argv are not those of %s", name, file)
		}
		if p.RequestID != r.RequestID || p.RequestSHA256 != Digest(readVector(t, file)) {
			t.Errorf("%s: request_id %s and request_sha256 %s are not those of %s", name, p.RequestID, p.RequestSHA256, file)
		}
		answered[p.Binding.Verb] = true
	}
	for verb, file := range requests {
		if !answered[verb] {
			t.Errorf("no valid pending record answers %s", file)
		}
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
		// A vector that is about an escape must still hold one: a tool
		// that rewrote the file may have resolved it.
		if strings.Contains(name, "escaped") && !bytes.Contains(readVector(t, name), []byte{0x5c, 'u'}) {
			t.Errorf("%s: the vector holds no escape", name)
		}
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

// The comment of each Decode function lists the codes it refuses with. It
// must name every code that an invalid vector of its kind is refused with.
func TestDecodeCommentsNameTheirCodes(t *testing.T) {
	var expect map[string]struct{ Code string }
	if err := json.Unmarshal(readVector(t, "invalid.json"), &expect); err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "record.go", nil, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	comments := make(map[string]string)
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Doc != nil {
			comments[fn.Name.Name] = strings.Join(strings.Fields(fn.Doc.Text()), " ")
		}
	}
	functions := map[string]string{"request": "DecodeRequest", "pending": "DecodePending", "decision": "DecodeDecision"}
	checked := 0
	for name, want := range expect {
		function := functions[kindOf(t, name)]
		comment := comments[function]
		if comment == "" {
			t.Fatalf("record.go has no comment for %s", function)
		}
		// A code is a whole word of the comment: record-invalid is not
		// named by a comment that says only "record-invalid-x".
		named := false
		for _, word := range strings.FieldsFunc(comment, func(r rune) bool { return r != '-' && (r < 'a' || r > 'z') }) {
			named = named || word == want.Code
		}
		if !named {
			t.Errorf("the comment of %s does not name %s, which %s is refused with", function, want.Code, name)
		}
		checked++
	}
	if checked < 30 {
		t.Fatalf("checked %d vectors, want the whole set", checked)
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

// The README states the rules in full, so that another implementation can
// find each invalid vector's code from it alone. This holds it in step with
// the forms and the content rules: it must name every field of every form,
// every closed word, every cap and every code a vector is refused with.
func TestReadmeStatesTheRules(t *testing.T) {
	readme := string(readVector(t, "README.md"))
	_, rules, found := strings.Cut(readme, "\n## The rules in full\n")
	if !found {
		t.Fatal("testdata/README.md has no section \"The rules in full\"")
	}
	need := func(what, word string) {
		t.Helper()
		if !strings.Contains(rules, "`"+word+"`") {
			t.Errorf("the rules of testdata/README.md do not name %s `%s`", what, word)
		}
	}

	fields := 0
	seen := make(map[reflect.Type]bool)
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for _, f := range fieldsOf(typ) {
			need("the field", f.name)
			if f.optional && !strings.Contains(rules, "| `"+f.name+"` | *Optional.*") {
				t.Errorf("the rules of testdata/README.md do not mark `%s` as optional", f.name)
			}
			fields++
			walk(typ.Field(f.index).Type)
		}
	}
	for _, v := range []any{Request{}, Pending{}, Decision{}} {
		walk(reflect.TypeOf(v))
	}
	if fields < 90 {
		t.Fatalf("walked %d fields, want those of all three forms", fields)
	}
	if got, want := strings.Count(rules, "*Optional.*"), 6; got != want {
		t.Errorf("the rules mark %d fields as optional, want %d", got, want)
	}

	for _, word := range []string{SchemaRequest, SchemaPending, SchemaDecision, VerbFeatureFinish, VerbBranchPrune,
		ClassRefMove, SubjectSeat, EffectRef, BuiltByHost, EvidenceSuite, DecisionApprove, DecisionDeny,
		ConfirmedCode, FlagExecConfig, FlagSymlink, FlagSubmodule, FlagAliased, FlagHistoryOnly, "A", "M", "D", "T",
		"rq-", "ap-", "b64", PlanDigest("")} {
		need("the word", word)
	}
	for _, limit := range []int{MaxRequestBytes, MaxPendingBytes, MaxDecisionBytes, MaxNoteBytes, MaxReasonBytes, MaxEffects} {
		if !strings.Contains(rules, " "+strconv.Itoa(limit)+" ") {
			t.Errorf("the rules of testdata/README.md do not give the limit %d", limit)
		}
	}

	var expect map[string]struct{ Code string }
	if err := json.Unmarshal(readVector(t, "invalid.json"), &expect); err != nil {
		t.Fatal(err)
	}
	for _, want := range expect {
		need("the code", want.Code)
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
