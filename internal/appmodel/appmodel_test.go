package appmodel

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

const enqueueWire = `{"schema_version":1,"command_id":"cmd-a","project":{"authority":"auth-a","id":"project-a"},"target":{"authority":"auth-a","kind":"conversation","id":"conv-a"},"operation":"input.enqueue","expected_revision":"r7","payload":{"text":"hello"}}`
const cancelWire = `{"schema_version":1,"command_id":"cancel-a","project":{"authority":"auth-a","id":"project-a"},"target":{"authority":"auth-a","kind":"command","client_id":"client-a","id":"cmd-a"},"operation":"queue.cancel","expected_revision":"s1","payload":{}}`

var identity = CompositionIdentity{Authority: "auth-a", EnrolledClientID: "client-a"}

func command(t *testing.T, wire string) Envelope {
	t.Helper()
	e, err := Decode([]byte(wire))
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func binding(t *testing.T, id CompositionIdentity, e Envelope) Binding {
	t.Helper()
	b, err := Bind(id, e)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func wantCode(t *testing.T, err error, code Code) {
	t.Helper()
	var domain DomainError
	if !errors.As(err, &domain) || domain.Code != code || err.Error() != string(code) {
		t.Fatalf("got %v, want %s", err, code)
	}
}

func TestCanonicalVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/canonical-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Vectors []struct {
			Name   string
			Typed  map[string]string `json:"typed_binding"`
			Hex    string            `json:"canonical_hex"`
			Length int               `json:"byte_length"`
			SHA    string            `json:"sha256"`
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Vectors) != 8 {
		t.Fatal("expected all eight reviewed vectors")
	}
	for _, vector := range fixture.Vectors {
		t.Run(vector.Name, func(t *testing.T) {
			v := vector.Typed
			e := Envelope{SchemaVersion: 1, CommandID: CommandID(v["command_id"]), Project: ProjectRef{v["project_authority"], v["project_id"]}, ExpectedRevision: Revision(v["expected_revision"])}
			switch v["operation"] {
			case "input.enqueue":
				if v["target_kind"] != "conversation" || v["target_client_id"] != "" {
					t.Fatal("invalid vector target")
				}
				e.Operation = Enqueue{ConversationRef{v["target_authority"], v["target_id"]}, EnqueuePayload{v["text"]}}
			case "queue.cancel":
				if v["target_kind"] != "command" {
					t.Fatal("invalid vector target")
				}
				e.Operation = Cancel{CommandKey{v["target_authority"], v["target_client_id"], CommandID(v["target_id"])}}
			default:
				t.Fatal("unknown vector operation")
			}
			id := CompositionIdentity{v["authority_id"], v["enrolled_client_id"]}
			b := binding(t, id, e)
			digest := b.Digest()
			if hex.EncodeToString(b.Bytes()) != vector.Hex || len(b.Bytes()) != vector.Length || hex.EncodeToString(digest[:]) != vector.SHA {
				t.Fatal("canonical bytes/length/SHA differ from reviewed vector")
			}
			wire, err := Encode(e)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := Decode(wire)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(e, decoded) || !b.Equal(binding(t, id, decoded)) {
				t.Fatal("normal API round trip changed binding")
			}
			if b.Key() != (CommandKey{id.Authority, id.EnrolledClientID, e.CommandID}) {
				t.Fatal("wrong command key")
			}
		})
	}
}

func TestEquivalentWireAndExactText(t *testing.T) {
	original := binding(t, identity, command(t, enqueueWire))
	for _, wire := range []string{
		" \r\n\t" + enqueueWire + "\n",
		`{"payload":{"text":"he\u006clo"},"expected_revision":"r7","operation":"input.enqueue","target":{"id":"conv-a","kind":"conversation","authority":"auth-a"},"project":{"id":"project-a","authority":"auth-a"},"command_id":"cmd-a","schema_version":1}`,
		strings.Replace(enqueueWire, `"command_id"`, `"\u0063ommand_id"`, 1),
	} {
		if !original.Equal(binding(t, identity, command(t, wire))) {
			t.Fatal("wire spelling changed identity")
		}
	}
	var previous []Binding
	for _, text := range []string{"", " ", "\t \r\n", "\x00", "\n", "é", "e\u0301", "😀", "\ufffd", "<>&"} {
		e := command(t, enqueueWire)
		op := e.Operation.(Enqueue)
		op.Payload.Text = text
		e.Operation = op
		wire, err := Encode(e)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := Decode(wire)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Operation.(Enqueue).Payload.Text != text {
			t.Fatal("text was normalized")
		}
		b := binding(t, identity, decoded)
		for _, other := range previous {
			if b.Equal(other) {
				t.Fatal("distinct text collapsed")
			}
		}
		previous = append(previous, b)
	}
	paired := strings.Replace(enqueueWire, `"hello"`, `"\ud83d\ude00"`, 1)
	if command(t, paired).Operation.(Enqueue).Payload.Text != "😀" {
		t.Fatal("paired surrogate lost")
	}
}

func TestWireRejections(t *testing.T) {
	cases := map[string]string{
		"empty": "", "null": "null", "array": "[]", "scalar": "1", "missing": "{}",
		"BOM":                  "\xef\xbb\xbf" + enqueueWire,
		"BOM after whitespace": " \xef\xbb\xbf" + enqueueWire,
		"trailing object":      enqueueWire + "{}", "trailing scalar": enqueueWire + "true", "trailing junk": enqueueWire + "x",
		"bad utf8":                strings.Replace(enqueueWire, "hello", "\xff", 1),
		"encoded surrogate":       strings.Replace(enqueueWire, "hello", "\xed\xa0\x80", 1),
		"bad key utf8":            strings.Replace(enqueueWire, "command_id", "\xff", 1),
		"outer duplicate":         strings.Replace(enqueueWire, `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"escaped outer duplicate": strings.Replace(enqueueWire, `"command_id":"cmd-a"`, `"command_id":"cmd-a","\u0063ommand_id":"cmd-a"`, 1),
		"project duplicate":       strings.Replace(enqueueWire, `"id":"project-a"`, `"id":"project-a","\u0069d":"project-a"`, 1),
		"target duplicate":        strings.Replace(enqueueWire, `"id":"conv-a"`, `"id":"conv-a","\u0069d":"conv-a"`, 1),
		"payload duplicate":       strings.Replace(enqueueWire, `"text":"hello"`, `"text":"hello","te\u0078t":"hello"`, 1),
		"irrelevant client":       strings.Replace(enqueueWire, `"kind":"conversation"`, `"kind":"conversation","client_id":""`, 1),
		"wrong enqueue kind":      strings.Replace(enqueueWire, `"conversation"`, `"command"`, 1),
		"wrong cancel kind":       strings.Replace(cancelWire, `"command"`, `"conversation"`, 1),
		"cancel text":             strings.Replace(cancelWire, `"payload":{}`, `"payload":{"text":""}`, 1),
		"empty enqueue payload":   strings.Replace(enqueueWire, `{"text":"hello"}`, `{}`, 1),
		"case folded key":         strings.Replace(enqueueWire, `"command_id"`, `"COMMAND_ID"`, 1),
		"fractional version":      strings.Replace(enqueueWire, `"schema_version":1`, `"schema_version":1.0`, 1),
		"exponent version":        strings.Replace(enqueueWire, `"schema_version":1`, `"schema_version":1e0`, 1),
		"leading zero":            strings.Replace(enqueueWire, `"schema_version":1`, `"schema_version":01`, 1),
		"trailing comma":          enqueueWire[:len(enqueueWire)-1] + ",}",
	}
	for _, escape := range []string{`\ud800`, `\udfff`, `\ud800x`, `\ud800\u0041`, `\ud800\ud800`, `\ud800\u`, `\uXXXX`, `\x00`, "\x00", "\n"} {
		cases["invalid text "+escape] = strings.Replace(enqueueWire, "hello", escape, 1)
	}
	for _, field := range []string{"principal", "client", "credential", "policy", "rpc_id"} {
		cases["forged "+field] = enqueueWire[:len(enqueueWire)-1] + `,"` + field + `":""}`
	}
	for name, wire := range cases {
		t.Run(name, func(t *testing.T) { _, err := Decode([]byte(wire)); wantCode(t, err, InvalidInput) })
	}
	for _, version := range []string{"0", "2", "-1", strings.Repeat("9", 200)} {
		_, err := Decode([]byte(strings.Replace(enqueueWire, `"schema_version":1`, `"schema_version":`+version, 1)))
		wantCode(t, err, UnsupportedVersion)
	}
	_, err := Decode([]byte(strings.Replace(enqueueWire, `"input.enqueue"`, `"future.operation"`, 1)))
	wantCode(t, err, UnsupportedCapability)
	// Syntax errors and duplicate decoded keys precede version interpretation.
	_, err = Decode([]byte(strings.Replace(cases["payload duplicate"], `"schema_version":1`, `"schema_version":2`, 1)))
	wantCode(t, err, InvalidInput)
}

// Exercise every required field, not merely representative struct fields.
func TestEveryFieldRequiredClosedAndTyped(t *testing.T) {
	for _, wire := range []string{enqueueWire, cancelWire} {
		var root map[string]any
		if err := json.Unmarshal([]byte(wire), &root); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"", "project", "target", "payload"} {
			obj := root
			if path != "" {
				obj = root[path].(map[string]any)
			}
			keys := make([]string, 0, len(obj))
			for k := range obj {
				keys = append(keys, k)
			}
			check := func() {
				t.Helper()
				raw, err := json.Marshal(root)
				if err != nil {
					t.Fatal(err)
				}
				_, err = Decode(raw)
				wantCode(t, err, InvalidInput)
			}
			obj["extra"] = ""
			check()
			delete(obj, "extra")
			for _, key := range keys {
				old := obj[key]
				delete(obj, key)
				check()
				for _, bad := range []any{nil, true, []any{}, float64(1), "", map[string]any{}} {
					// Empty text and queue.cancel's empty object are required valid values.
					if reflect.DeepEqual(bad, old) || (key == "text" && bad == "") {
						continue
					}
					obj[key] = bad
					check()
				}
				obj[key] = old
			}
		}
	}
}

func TestBindingChangesAndOwnership(t *testing.T) {
	e := command(t, enqueueWire)
	original := binding(t, identity, e)
	for name, change := range map[string]func(*Envelope){
		"command":           func(e *Envelope) { e.CommandID = "cmd-b" },
		"revision":          func(e *Envelope) { e.ExpectedRevision = "r8" },
		"project authority": func(e *Envelope) { e.Project.Authority = "auth-b" },
		"project":           func(e *Envelope) { e.Project.ID = "project-b" },
		"target authority":  func(e *Envelope) { o := e.Operation.(Enqueue); o.Target.Authority = "auth-b"; e.Operation = o },
		"target":            func(e *Envelope) { o := e.Operation.(Enqueue); o.Target.ID = "conv-b"; e.Operation = o },
		"payload":           func(e *Envelope) { o := e.Operation.(Enqueue); o.Payload.Text = "changed"; e.Operation = o },
		"operation":         func(e *Envelope) { e.Operation = Cancel{CommandKey{"auth-a", "client-a", "conv-a"}} },
	} {
		t.Run(name, func(t *testing.T) {
			changed := e
			change(&changed)
			if original.Equal(binding(t, identity, changed)) {
				t.Fatal("binding failed to change")
			}
		})
	}
	for _, id := range []CompositionIdentity{{"auth-b", "client-a"}, {"auth-a", "client-b"}} {
		if original.Equal(binding(t, id, e)) {
			t.Fatal("composition identity omitted")
		}
	}
	cancel := command(t, cancelWire)
	other := cancel
	op := other.Operation.(Cancel)
	op.Target.EnrolledClientID = "client-b"
	other.Operation = op
	if binding(t, identity, cancel).Equal(binding(t, identity, other)) {
		t.Fatal("original client omitted from cancellation")
	}
	buf := []byte(enqueueWire)
	decoded, err := Decode(buf)
	if err != nil {
		t.Fatal(err)
	}
	for i := range buf {
		buf[i] = 'x'
	}
	if !original.Equal(binding(t, identity, decoded)) {
		t.Fatal("decoder retained caller buffer")
	}
	owned := original.Bytes()
	owned[0] ^= 0xff
	digest := original.Digest()
	digest[0] ^= 0xff
	key := original.Key()
	key.CommandID = "other"
	e.CommandID = "other"
	if !original.Equal(binding(t, identity, decoded)) || bytes.Equal(owned, original.Bytes()) || digest == original.Digest() || key == original.Key() {
		t.Fatal("binding ownership broken")
	}
	if (Binding{}).Equal(Binding{}) {
		t.Fatal("zero bindings valid")
	}
	// Equality must remain full-byte comparison even if a digest collides.
	forged := binding(t, identity, other)
	forged.digest = original.digest
	if original.Equal(forged) {
		t.Fatal("digest-only equality")
	}
}

func TestTypedValidationAtEveryEntry(t *testing.T) {
	base := command(t, enqueueWire)
	var nilEnqueue *Enqueue
	cases := []Envelope{{}, base, base, base, base, base, base, base}
	cases[1].CommandID = "bad/id"
	cases[2].Operation = nil
	cases[3].Operation = nilEnqueue
	op := base.Operation.(Enqueue)
	cases[4].Operation = &op
	op.Payload.Text = "\xff"
	cases[5].Operation = op
	cases[6].ExpectedRevision = ""
	cases[7].Operation = Cancel{CommandKey{"auth-a", "", "cmd-a"}}
	for _, e := range cases {
		if e.Validate() == nil {
			t.Fatal("invalid typed value validated")
		}
		if wire, err := Encode(e); err == nil || wire != nil {
			t.Fatal("invalid typed value encoded")
		}
		if b, err := Bind(identity, e); err == nil || len(b.Bytes()) != 0 {
			t.Fatal("invalid typed value bound")
		}
	}
	for _, id := range []CompositionIdentity{{}, {"auth-a", ""}, {"auth-a", "bad/client"}, {"\xff", "client-a"}} {
		_, err := Bind(id, base)
		wantCode(t, err, InvalidInput)
	}
}

func TestLimits(t *testing.T) {
	t.Run("wire bytes", func(t *testing.T) {
		wire := enqueueWire + strings.Repeat(" ", MaxWireBytes-len(enqueueWire))
		command(t, wire)
		_, err := Decode([]byte(wire + " "))
		wantCode(t, err, InvalidInput)
	})
	t.Run("decoded text bytes", func(t *testing.T) {
		for _, unit := range []string{"x", "é", "\x00"} {
			e := command(t, enqueueWire)
			op := e.Operation.(Enqueue)
			op.Payload.Text = strings.Repeat(unit, MaxTextBytes/len(unit))
			e.Operation = op
			wire, err := Encode(e)
			if err != nil {
				t.Fatal(err)
			}
			command(t, string(wire))
			binding(t, identity, e)
			op.Payload.Text += "x"
			e.Operation = op
			if _, err := Encode(e); err == nil {
				t.Fatal("oversized typed text encoded")
			}
			if _, err := Bind(identity, e); err == nil {
				t.Fatal("oversized typed text bound")
			}
			raw, _ := json.Marshal(op.Payload.Text)
			_, err = Decode([]byte(strings.Replace(enqueueWire, `"hello"`, string(raw), 1)))
			wantCode(t, err, InvalidInput)
		}
	})
	t.Run("IDs and revisions", func(t *testing.T) {
		for _, wire := range []string{enqueueWire, cancelWire} {
			for _, value := range []string{"cmd-a", "cancel-a", "auth-a", "project-a", "conv-a", "client-a", "r7", "s1"} {
				if !strings.Contains(wire, `"`+value+`"`) {
					continue
				}
				valid := strings.ReplaceAll(wire, `"`+value+`"`, `"`+strings.Repeat("a", MaxIDBytes)+`"`)
				command(t, valid)
				for _, bad := range []string{"", strings.Repeat("a", MaxIDBytes+1), "a b", "a/b", "é", "a\\u0000"} {
					_, err := Decode([]byte(strings.ReplaceAll(wire, `"`+value+`"`, `"`+bad+`"`)))
					wantCode(t, err, InvalidInput)
				}
			}
		}
		for _, id := range []CompositionIdentity{{strings.Repeat("a", MaxIDBytes), "client-a"}, {"auth-a", strings.Repeat("a", MaxIDBytes)}} {
			binding(t, id, command(t, enqueueWire))
		}
		for _, id := range []CompositionIdentity{{strings.Repeat("a", MaxIDBytes+1), "client-a"}, {"auth-a", strings.Repeat("a", MaxIDBytes+1)}} {
			_, err := Bind(id, command(t, enqueueWire))
			wantCode(t, err, InvalidInput)
		}
		command(t, strings.ReplaceAll(enqueueWire, "cmd-a", "AZaz09._:-"))
	})
	t.Run("depth", func(t *testing.T) {
		at := `{"x":` + strings.Repeat("[", MaxDepth-1) + "0" + strings.Repeat("]", MaxDepth-1) + "}"
		if err := scanJSON([]byte(at)); err != nil {
			t.Fatal("depth boundary", err)
		}
		over := `{"x":` + strings.Repeat("[", MaxDepth) + "0" + strings.Repeat("]", MaxDepth) + "}"
		wantCode(t, scanJSON([]byte(over)), InvalidInput)
		_, err := Decode([]byte(over))
		wantCode(t, err, InvalidInput)
	})
	t.Run("tokens", func(t *testing.T) {
		// Root delimiters + key + array delimiters = five tokens.
		at := `{"x":[` + strings.Repeat("0,", MaxTokens-6) + "0]}"
		if err := scanJSON([]byte(at)); err != nil {
			t.Fatal("token boundary", err)
		}
		over := strings.Replace(at, "0]", "0,0]", 1)
		wantCode(t, scanJSON([]byte(over)), InvalidInput)
		_, err := Decode([]byte(over))
		wantCode(t, err, InvalidInput)
	})
	t.Run("canonical bytes", func(t *testing.T) {
		// Current v1 field limits cannot reach the independent binding ceiling.
		// Exercise its overflow guard directly, as protection for future variants.
		field := strings.Repeat("a", MaxBindingBytes-len(bindingHeader)-4)
		b, err := canonicalFields([]string{field})
		if err != nil || len(b) != MaxBindingBytes {
			t.Fatal("binding boundary", err)
		}
		_, err = canonicalFields([]string{field + "a"})
		wantCode(t, err, InvalidInput)
		_, err = canonicalFields([]string{field, ""})
		wantCode(t, err, InvalidInput)
	})
}

func TestStrictScannerGrammar(t *testing.T) {
	for _, value := range []string{`0`, `-0`, `1.5`, `2e+3`, `-2E-3`, `true`, `false`, `null`, `[]`, `{}`, `[{"a":1},{"a":2}]`, `"\\\"\/\b\f\n\r\t\u0000"`} {
		if err := scanJSON([]byte(`{"x":` + value + `}`)); err != nil {
			t.Fatalf("valid JSON %s: %v", value, err)
		}
	}
	for _, value := range []string{`+1`, `01`, `-01`, `-`, `.1`, `1.`, `1e`, `1e+`, `tru`, `NULL`, `[1,]`, `[,1]`, `[1 2]`, `{"a" 1}`, `{"a":1,}`, `{"a":1,"\u0061":2}`, `[{"a":1,"a":2}]`, `"unterminated`, `"\u123"`, `"\ud800\udfff\udfff"`} {
		wantCode(t, scanJSON([]byte(`{"x":`+value+`}`)), InvalidInput)
	}
}

func FuzzDecode(f *testing.F) {
	for _, seed := range []string{enqueueWire, cancelWire, "", "{}", strings.Replace(enqueueWire, "hello", `\ud800`, 1), strings.Replace(enqueueWire, "hello", `\ud83d\ude00`, 1), strings.Replace(enqueueWire, "hello", "\xff", 1)} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, wire []byte) {
		e, err := Decode(wire)
		if err != nil {
			return
		}
		encoded, err := Encode(e)
		if err != nil {
			t.Fatal(err)
		}
		round, err := Decode(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(e, round) || !binding(t, identity, e).Equal(binding(t, identity, round)) {
			t.Fatal("accepted input cannot round trip")
		}
	})
}
