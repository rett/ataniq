package record

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// validPending returns the valid pending vector, decoded.
func validPending(t testing.TB) Pending {
	t.Helper()
	p, err := DecodePending(readVector(t, "pending-valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func validRequest(t testing.TB) Request {
	t.Helper()
	r, err := DecodeRequest(readVector(t, "request-valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func validDecision(t testing.TB) Decision {
	t.Helper()
	d, err := DecodeDecision(readVector(t, "decision-valid.json"))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// sealed returns p with its two digests computed, as a host does before it
// writes a pending record.
func sealed(t testing.TB, p Pending) Pending {
	t.Helper()
	p.Binding.PlanSHA256 = PlanDigest(string(p.PlanText))
	digest, err := BindingDigest(p.Binding)
	if err != nil {
		t.Fatal(err)
	}
	p.BindingSHA256 = digest
	return p
}

func wantCode(t *testing.T, what string, err error, want Code) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Errorf("%s: refused %v, want accepted", what, err)
		}
		return
	}
	if err == nil {
		t.Errorf("%s: accepted, want %s", what, want)
		return
	}
	if got, ok := CodeOf(err); !ok || got != want {
		t.Errorf("%s: refused %v, want %s", what, err, want)
	}
}

func TestTextHasOneForm(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"empty", "", `""`},
		{"ascii", "lib/a.go", `"lib/a.go"`},
		{"quote and backslash", `a"b\c`, `"a\"b\\c"`},
		{"html and separators", "<a>&b\u2028\u2029", "\"<a>&b\u2028\u2029\""},
		{"letters", "\u00e9t\u00e9 \u65e5\u672c\u8a9e \U0001f600", "\"\u00e9t\u00e9 \u65e5\u672c\u8a9e \U0001f600\""},
		{"direction override", "a\u202eb", "\"a\u202eb\""},
		{"not UTF-8", "caf\xe9", `{"b64":"Y2Fm6Q=="}`},
		{"escape byte", "\x1b[2J", `{"b64":"G1sySg=="}`},
		{"line feed", "one\ntwo", `{"b64":"b25lCnR3bw=="}`},
		{"tab", "a\tb", `{"b64":"YQli"}`},
		{"NUL", "\x00", `{"b64":"AA=="}`},
		{"delete", "a\x7f", `{"b64":"YX8="}`},
		{"C1 control", "a\u0085", `{"b64":"YcKF"}`},
		{"last C1 control", "\u009f", `{"b64":"wp8="}`},
		{"first after C1", "\u00a0", "\"\u00a0\""},
		{"surrogate bytes", "\xed\xa0\x80", `{"b64":"7aCA"}`},
	}
	for _, tt := range tests {
		got, err := Text(tt.raw).MarshalJSON()
		if err != nil || string(got) != tt.want {
			t.Errorf("%s: written as %s (%v), want %s", tt.name, got, err, tt.want)
		}
		if plain := !strings.HasPrefix(tt.want, "{"); Text(tt.raw).Plain() != plain {
			t.Errorf("%s: Plain() = %v", tt.name, !plain)
		}
		var back Text
		if err := back.UnmarshalJSON([]byte(tt.want)); err != nil || string(back) != tt.raw {
			t.Errorf("%s: read back as %q (%v)", tt.name, back, err)
		}
	}

	refused := []struct{ name, in string }{
		{"b64 of plain text", `{"b64":"bGliL2EuZ28="}`},
		{"b64 of nothing", `{"b64":""}`},
		{"b64 without padding", `{"b64":"Y2Fm6Q"}`},
		{"b64 with loose bits", `{"b64":"Y2Fm6R=="}`},
		{"b64 in the URL alphabet", `{"b64":"_-8="}`},
		{"b64 with a newline", `{"b64":"Y2Fm\n6Q=="}`},
		{"b64 that is not a string", `{"b64":7}`},
		{"another key", `{"B64":"Y2Fm6Q=="}`},
		{"a second key", `{"b64":"Y2Fm6Q==","x":""}`},
		{"b64 twice", `{"b64":"Y2Fm6Q==","b64":"Y2Fm6Q=="}`},
		{"an empty object", `{}`},
		{"a number", `7`},
		{"a list", `["a"]`},
		{"an escaped line feed", `"one\ntwo"`},
		{"an escaped C1 control", `"a\u0085"`},
		{"a raw delete", "\"a\x7f\""},
		{"a string that is not UTF-8", "\"caf\xe9\""},
		{"data after the text", `"a" "b"`},
	}
	for _, tt := range refused {
		var got Text
		if err := got.UnmarshalJSON([]byte(tt.in)); err == nil {
			t.Errorf("%s: %s was read as %q, want refused", tt.name, tt.in, got)
		}
	}
}

// A package that keeps a Text in a document of its own reads and writes it
// through encoding/json and gets the same one form.
func TestTextThroughEncodingJSON(t *testing.T) {
	type doc struct {
		Path Text `json:"path"`
	}
	for _, raw := range []string{"lib/a.go", "caf\xe9", "one\ntwo"} {
		data, err := json.Marshal(doc{Path: Text(raw)})
		if err != nil {
			t.Fatal(err)
		}
		var back doc
		if err := json.Unmarshal(data, &back); err != nil || string(back.Path) != raw {
			t.Errorf("%q came back as %q (%v) from %s", raw, back.Path, err, data)
		}
	}
	var d doc
	if err := json.Unmarshal([]byte(`{"path":{"b64":"bGliL2EuZ28="}}`), &d); err == nil {
		t.Error("encoding/json read a b64 text that holds plain bytes")
	}
}

func TestRawBytesRoundTripThroughB64(t *testing.T) {
	p := validPending(t)
	notUTF8 := Text("docs/caf\xe9/\xff\xfe.txt")
	escape := Text("src/\x1b]0;title\x07/\x1b[31mred")
	plan := Text("line one\nline two\r\n\tline three\n\nlast line without an end")
	p.Binding.Summary.Paths = []PathChange{
		{Path: notUTF8, Status: "A", OldMode: "000000", NewMode: "100644", Added: 1},
		{Path: escape, Status: "M", OldMode: "100644", NewMode: "100644", Removed: 2},
	}
	p.Binding.Summary.Binary = []Text{notUTF8}
	p.Binding.Summary.Flagged = []Flag{{Kind: FlagExecConfig, Path: escape, Pattern: "src"}}
	p.PlanText = plan
	p.Note = "first line\nsecond line"
	p = sealed(t, p)

	data, err := EncodePending(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"\xe9", "\xff", "\x1b", "\n", "\r", "\t", "\x07"} {
		if bytes.Contains(data, []byte(raw)) {
			t.Errorf("the encoded record holds the raw byte %q", raw)
		}
	}
	for _, want := range []string{
		`"path":{"b64":"ZG9jcy9jYWbpL//+LnR4dA=="}`,
		`"path":{"b64":"c3JjLxtdMDt0aXRsZQcvG1szMW1yZWQ="}`,
		`"plan_text":{"b64":"bGluZSBvbmUKbGluZSB0d28NCglsaW5lIHRocmVlCgpsYXN0IGxpbmUgd2l0aG91dCBhbiBlbmQ="}`,
		`"note":{"b64":"Zmlyc3QgbGluZQpzZWNvbmQgbGluZQ=="}`,
	} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("the encoded record lacks %s", want)
		}
	}
	back, err := DecodePending(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.Binding.Summary.Paths[0].Path != notUTF8 || back.Binding.Summary.Paths[1].Path != escape ||
		back.Binding.Summary.Binary[0] != notUTF8 || back.Binding.Summary.Flagged[0].Path != escape ||
		back.PlanText != plan || back.Note != p.Note {
		t.Error("raw bytes changed on the way through b64")
	}
	again, err := EncodePending(back)
	if err != nil || !bytes.Equal(again, data) {
		t.Errorf("encoding the decoded record gave other bytes (%v)", err)
	}
}

func TestStringsAreWrittenAsTheyAre(t *testing.T) {
	const text = "<script>alert(1)</script> & \u2028\u2029 \u00e9t\u00e9 \u00df \u65e5\u672c\u8a9e \u0416 \U0001f600"

	p := validPending(t)
	p.Note = Text(text)
	p.Binding.Project = text
	p.Binding.Summary.Paths[0].Path = Text(text)
	p = sealed(t, p)
	data, err := EncodePending(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"note":"` + text + `"`, `"project":"` + text + `"`, `"path":"` + text + `"`} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("the pending record lacks %s", want)
		}
	}
	if bytes.Contains(data, []byte(`\u`)) {
		t.Error("the pending record holds a \\u escape")
	}
	back, err := DecodePending(data)
	if err != nil || string(back.Note) != text || back.Binding.Project != text {
		t.Errorf("the text did not come back: %v", err)
	}

	d := validDecision(t)
	d.Remark = text + ` "quoted" back\slash`
	data, err = EncodeDecision(d)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"remark":"` + text + ` \"quoted\" back\\slash"`; !bytes.Contains(data, []byte(want)) {
		t.Errorf("the decision lacks %s in %s", want, data)
	}
	if back, err := DecodeDecision(data); err != nil || back.Remark != d.Remark {
		t.Errorf("the remark did not come back: %v", err)
	}
}

func TestCanonicalFormSortsKeysAndHasNoWhitespace(t *testing.T) {
	data, err := EncodeRequest(validRequest(t))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"argv":["sample-feature"],"at":"2026-11-20T14:02:01Z","epoch":7,` +
		`"feature_sha":"8281895a8281895a8281895a8281895a8281895a",` +
		`"note_sha256":"` + validRequest(t).NoteSHA256 + `",` +
		`"request_id":"rq-20261120t140201z-7c1e","schema":"qargi.approval-request/1","verb":"feature finish"}`
	if string(data) != want {
		t.Errorf("canonical request:\n got %s\nwant %s", data, want)
	}
}

// bs is a backslash. The JSON escapes of the rows that are about an escape
// are built from it, so that no tool that reads or writes this file can turn
// the escape into the character it stands for and leave a row that changes
// nothing.
const bs = "\\"

// strictCases are spellings of the valid request that a decoder must
// refuse, and a few it must accept. old is replaced by new once.
var strictCases = []struct {
	name, old, new string
	want           Code
}{
	{"as it is", "", "", ""},
	{"whitespace between tokens", `,"epoch":7,`, " ,\n\t\"epoch\" : 7 ,\r\n", ""},
	{"an escaped character in a value", `"feature finish"`, `"feature fin` + bs + `u0069sh"`, ""},
	{"an escaped solidus", `"qargi.approval-request/1"`, `"qargi.approval-request\/1"`, ""},
	{"an escaped character in a key", `"epoch"`, `"` + bs + `u0065poch"`, ""},
	{"every character of a key escaped", `"epoch"`, `"` + bs + `u0065` + bs + `u0070` + bs + `u006f` + bs + `u0063` + bs + `u0068"`, ""},
	{"a key in upper case", `"epoch"`, `"EPOCH"`, CodeRequestInvalid},
	{"a key with one capital", `"verb"`, `"Verb"`, CodeRequestInvalid},
	{"a key with a Kelvin sign", `"request_id"`, "\"request_id\u212a\"", CodeRequestInvalid},
	{"a repeated key, same value", `"epoch":7,`, `"epoch":7,"epoch":7,`, CodeRequestInvalid},
	{"a repeated key, other value", `"epoch":7,`, `"epoch":8,"epoch":7,`, CodeRequestInvalid},
	{"a repeated key spelled with an escape", `"epoch":7,`, `"epoch":7,"` + bs + `u0065poch":8,`, CodeRequestInvalid},
	{"a repeated key, the first spelled with an escape", `"epoch":7,`, `"` + bs + `u0065poch":8,"epoch":7,`, CodeRequestInvalid},
	{"a repeated key, both spelled with an escape", `"epoch":7,`, `"` + bs + `u0065poch":7,"epoc` + bs + `u0068":8,`, CodeRequestInvalid},
	{"an unknown key", `"epoch":7,`, `"epoch":7,"project":"project-a",`, CodeRequestInvalid},
	{"an empty key", `"epoch":7,`, `"epoch":7,"":0,`, CodeRequestInvalid},
	{"a missing key", `"epoch":7,`, ``, CodeRequestInvalid},
	{"a fraction", `"epoch":7`, `"epoch":7.0`, CodeRequestInvalid},
	{"an exponent", `"epoch":7`, `"epoch":7e0`, CodeRequestInvalid},
	{"a capital exponent", `"epoch":7`, `"epoch":7E+1`, CodeRequestInvalid},
	{"a leading zero", `"epoch":7`, `"epoch":07`, CodeRequestInvalid},
	{"a plus sign", `"epoch":7`, `"epoch":+7`, CodeRequestInvalid},
	{"minus zero", `"epoch":7`, `"epoch":-0`, CodeRequestInvalid},
	{"a negative epoch", `"epoch":7`, `"epoch":-7`, CodeRequestInvalid},
	{"a number past int64", `"epoch":7`, `"epoch":9223372036854775808`, CodeRequestInvalid},
	{"the largest int64", `"epoch":7`, `"epoch":9223372036854775807`, ""},
	{"a number as a string", `"epoch":7`, `"epoch":"7"`, CodeRequestInvalid},
	{"a null", `"epoch":7`, `"epoch":null`, CodeRequestInvalid},
	{"a boolean for a number", `"epoch":7`, `"epoch":true`, CodeRequestInvalid},
	{"an escaped NUL", `"feature finish"`, `"feature finish\u0000"`, CodeRequestInvalid},
	{"an escaped line feed", `"feature finish"`, `"feature finish\n"`, CodeRequestInvalid},
	{"a raw line feed", `"feature finish"`, "\"feature finish\n\"", CodeRequestInvalid},
	{"a raw delete", `"feature finish"`, "\"feature finish\x7f\"", CodeRequestInvalid},
	{"a raw C1 control", `"feature finish"`, "\"feature finish\u009b\"", CodeRequestInvalid},
	{"an escaped C1 control", `"feature finish"`, `"feature finish\u009B"`, CodeRequestInvalid},
	{"a control character in a key", `"epoch"`, `"epoch\u0007"`, CodeRequestInvalid},
	{"a high surrogate alone", `"feature finish"`, `"feature finish\ud83d"`, CodeRequestInvalid},
	{"a low surrogate alone", `"feature finish"`, `"feature finish\ude00"`, CodeRequestInvalid},
	{"two high surrogates", `"feature finish"`, `"feature finish\ud83d\ud83d"`, CodeRequestInvalid},
	{"an unknown escape", `"feature finish"`, `"feature\ finish"`, CodeRequestInvalid},
	{"a short \\u escape", `"feature finish"`, `"feature finish\u00e"`, CodeRequestInvalid},
	{"bytes that are not UTF-8", `"feature finish"`, "\"feature finish\xff\"", CodeRequestInvalid},
	{"a string that does not end", `"verb":"feature finish"}`, `"verb":"feature finish`, CodeRequestInvalid},
	{"an object that does not end", `"verb":"feature finish"}`, `"verb":"feature finish"`, CodeRequestInvalid},
	{"a trailing comma", `"verb":"feature finish"}`, `"verb":"feature finish",}`, CodeRequestInvalid},
	{"a trailing comma in a list", `["sample-feature"]`, `["sample-feature",]`, CodeRequestInvalid},
	{"a comment", `{"argv"`, `{/* */"argv"`, CodeRequestInvalid},
	{"a single-quoted string", `"feature finish"`, `'feature finish'`, CodeRequestInvalid},
	{"a second object after it", `"verb":"feature finish"}`, `"verb":"feature finish"}{}`, CodeRequestInvalid},
	{"a word after it", `"verb":"feature finish"}`, `"verb":"feature finish"} x`, CodeRequestInvalid},
	{"a NUL after it", `"verb":"feature finish"}`, "\"verb\":\"feature finish\"}\x00", CodeRequestInvalid},
	{"whitespace after it", `"verb":"feature finish"}`, "\"verb\":\"feature finish\"}\n \t\r\n", ""},
	{"a byte order mark before it", `{"argv"`, "\ufeff{\"argv\"", CodeRequestInvalid},
	{"a list for a string", `"verb":"feature finish"`, `"verb":["feature finish"]`, CodeRequestInvalid},
	{"an object for a list", `["sample-feature"]`, `{"0":"sample-feature"}`, CodeRequestInvalid},
	{"a number in the argv", `["sample-feature"]`, `[7]`, CodeRequestInvalid},
	{"an empty optional field", `"feature_sha":"8281895a8281895a8281895a8281895a8281895a"`, `"feature_sha":""`, CodeRequestInvalid},
	{"a short object id", `"feature_sha":"8281895a8281895a8281895a8281895a8281895a"`, `"feature_sha":"8281895"`, CodeRequestInvalid},
	{"an object id in upper case", `"feature_sha":"8281895a8281895a8281895a8281895a8281895a"`, `"feature_sha":"8281895A8281895A8281895A8281895A8281895A"`, CodeRequestInvalid},
	{"a time with a fraction", `"2026-11-20T14:02:01Z"`, `"2026-11-20T14:02:01.5Z"`, CodeRequestInvalid},
	{"a time with an offset", `"2026-11-20T14:02:01Z"`, `"2026-11-20T14:02:01+00:00"`, CodeRequestInvalid},
	{"a time in lower case", `"2026-11-20T14:02:01Z"`, `"2026-11-20t14:02:01z"`, CodeRequestInvalid},
	{"a day that does not exist", `"2026-11-20T14:02:01Z"`, `"2026-11-31T14:02:01Z"`, CodeRequestInvalid},
	{"a feature name with a capital", `["sample-feature"]`, `["Sample-feature"]`, CodeFeatureName},
	{"a feature name with a slash", `["sample-feature"]`, `["sample/feature"]`, CodeFeatureName},
	{"an empty feature name", `["sample-feature"]`, `[""]`, CodeFeatureName},
	{"a feature name of 49 bytes", `["sample-feature"]`, `["` + strings.Repeat("a", 49) + `"]`, CodeFeatureName},
	{"a feature name of 48 bytes", `["sample-feature"]`, `["` + strings.Repeat("a", 48) + `"]`, ""},
	{"no feature name", `["sample-feature"]`, `[]`, CodeFeatureName},
	{"another verb", `"feature finish"`, `"feature start"`, CodeVerbNotRequestable},
	{"a verb in upper case", `"feature finish"`, `"Feature Finish"`, CodeVerbNotRequestable},
}

func TestDecodingIsStrict(t *testing.T) {
	valid := string(readVector(t, "request-valid.json"))
	same := validRequest(t)
	// The accepted cases that change a value and not only its spelling.
	otherRecord := map[string]bool{"the largest int64": true, "a feature name of 48 bytes": true}
	unchanged := 0
	for _, tt := range strictCases {
		if tt.old != "" && !strings.Contains(valid, tt.old) {
			t.Fatalf("%s: the valid vector does not hold %s", tt.name, tt.old)
		}
		// A row whose input is the valid vector tests nothing, whatever
		// its name says. Only the first row, which is the vector as it
		// is, may leave it unchanged.
		in := strings.Replace(valid, tt.old, tt.new, 1)
		if tt.old == "" && tt.new == "" {
			if unchanged++; unchanged > 1 {
				t.Fatalf("%s: a second case that changes nothing", tt.name)
			}
		} else if tt.old == tt.new || in == valid {
			t.Fatalf("%s: the case changes nothing", tt.name)
		}
		if strings.Contains(tt.name, "escape") && !strings.Contains(tt.new, bs) {
			t.Fatalf("%s: the case holds no escape", tt.name)
		}
		r, err := DecodeRequest([]byte(in))
		wantCode(t, tt.name, err, tt.want)
		// An accepted spelling of the same record is the same record: an
		// escaped key is matched to its field, and no value is lost.
		if tt.want == "" && err == nil && !otherRecord[tt.name] && !reflect.DeepEqual(r, same) {
			t.Errorf("%s: decoded to another record than the valid vector", tt.name)
		}
	}

	for _, tt := range []struct{ name, in string }{
		{"nothing", ""},
		{"whitespace", " \n"},
		{"a list", "[]"},
		{"a string", `"request"`},
		{"a number", "7"},
		{"null", "null"},
		{"an empty object", "{}"},
		{"deep lists", strings.Repeat("[", 4000) + strings.Repeat("]", 4000)},
		{"deep objects", strings.Repeat(`{"a":`, 1000) + "7" + strings.Repeat("}", 1000)},
	} {
		_, err := DecodeRequest([]byte(tt.in))
		wantCode(t, tt.name, err, CodeRequestInvalid)
		_, err = DecodePending([]byte(tt.in))
		wantCode(t, tt.name, err, CodeRecordInvalid)
		_, err = DecodeDecision([]byte(tt.in))
		wantCode(t, tt.name, err, CodeDecisionInvalid)
	}
}

func TestCapsHoldOnDecode(t *testing.T) {
	pad := func(data []byte, size int) []byte {
		return append(bytes.Clone(data), bytes.Repeat([]byte(" "), size-len(data))...)
	}
	request := readVector(t, "request-valid.json")
	_, err := DecodeRequest(pad(request, MaxRequestBytes))
	wantCode(t, "a request of exactly the cap", err, "")
	_, err = DecodeRequest(pad(request, MaxRequestBytes+1))
	wantCode(t, "a request one byte over", err, CodeRecordTooLarge)

	pending := readVector(t, "pending-valid.json")
	_, err = DecodePending(pad(pending, MaxPendingBytes))
	wantCode(t, "a pending record of exactly the cap", err, "")
	_, err = DecodePending(pad(pending, MaxPendingBytes+1))
	wantCode(t, "a pending record one byte over", err, CodeRecordTooLarge)

	decision := readVector(t, "decision-valid.json")
	_, err = DecodeDecision(pad(decision, MaxDecisionBytes))
	wantCode(t, "a decision of exactly the cap", err, "")
	_, err = DecodeDecision(pad(decision, MaxDecisionBytes+1))
	wantCode(t, "a decision one byte over", err, CodeRecordTooLarge)

	// The cap is checked before anything is parsed: bytes over the cap
	// that are not JSON at all are too large, not invalid.
	_, err = DecodeRequest(bytes.Repeat([]byte{0xff}, MaxRequestBytes+1))
	wantCode(t, "bytes over the cap that are not JSON", err, CodeRecordTooLarge)

	if MaxRequestBytes != 8192 || MaxNoteBytes != 2048 || MaxPendingBytes != 65536 ||
		MaxDecisionBytes != 8192 || MaxReasonBytes != 500 {
		t.Error("a cap is not the one the contract fixes")
	}
}

func TestCapsHoldOnEncode(t *testing.T) {
	// A request has no free text, so only a long argv of a prune could
	// grow it, and a prune takes none. Its cap is reached through the
	// encoder directly.
	r := validRequest(t)
	if _, err := encode(&r, 10, CodeRequestInvalid, "request"); err == nil {
		t.Error("a request over the cap was encoded")
	} else {
		wantCode(t, "a request over the cap", err, CodeRecordTooLarge)
	}

	// The four lists are never cut: a record they do not fit in is
	// refused, and the caller makes no card.
	p := validPending(t)
	for i := 0; len(p.Binding.Summary.Binary) < 1500; i++ {
		p.Binding.Summary.Binary = append(p.Binding.Summary.Binary, Text(strings.Repeat("x", 45)))
	}
	_, err := EncodePending(sealed(t, p))
	wantCode(t, "a pending record whose lists do not fit", err, CodeRecordTooLarge)

	// The largest path list that fits is accepted, and one more path is
	// refused.
	p = validPending(t)
	p.Binding.Summary.PathsTruncated = true
	row := PathChange{Path: Text(strings.Repeat("p", 100)), Status: "M", OldMode: "100644", NewMode: "100644"}
	for {
		p.Binding.Summary.Paths = append(p.Binding.Summary.Paths, row)
		data, err := EncodePending(sealed(t, p))
		if err != nil {
			wantCode(t, "one path too many", err, CodeRecordTooLarge)
			break
		}
		if len(data) > MaxPendingBytes {
			t.Fatalf("encoded %d bytes, over the cap", len(data))
		}
		if _, err := DecodePending(data); err != nil {
			t.Fatalf("an encoded record of %d bytes does not decode: %v", len(data), err)
		}
	}

	p = validPending(t)
	p.Note = Text(strings.Repeat("n", MaxNoteBytes))
	_, err = EncodePending(sealed(t, p))
	wantCode(t, "a note of exactly the cap", err, "")
	p.Note += "n"
	_, err = EncodePending(sealed(t, p))
	wantCode(t, "a note one byte over", err, CodeRecordTooLarge)
	// The cap is on the note's raw bytes, not on its base64.
	p.Note = Text(strings.Repeat("\n", MaxNoteBytes))
	_, err = EncodePending(sealed(t, p))
	wantCode(t, "a b64 note of exactly the cap", err, "")

	d := validDecision(t)
	for i := 0; i < 200; i++ {
		d.SignedOff = append(d.SignedOff, SignOff{Kind: FlagExecConfig, Path: Text(strings.Repeat("s", 50) + string(rune('a'+i%26)) + strings.Repeat("t", i))})
	}
	_, err = EncodeDecision(d)
	wantCode(t, "a decision over the cap", err, CodeRecordTooLarge)

	deny, err := DecodeDecision(readVector(t, "decision-valid-deny.json"))
	if err != nil {
		t.Fatal(err)
	}
	deny.Reason = strings.Repeat("r", MaxReasonBytes)
	_, err = EncodeDecision(deny)
	wantCode(t, "a reason of exactly the cap", err, "")
	deny.Reason += "r"
	_, err = EncodeDecision(deny)
	wantCode(t, "a reason one byte over", err, CodeDecisionInvalid)
}

func TestPendingContentRules(t *testing.T) {
	zeros := strings.Repeat("0", 40)
	id := strings.Repeat("a", 40)
	tests := []struct {
		name   string
		change func(*Pending)
		want   Code
	}{
		{"unchanged", func(p *Pending) {}, ""},
		{"object ids of 64 digits", func(p *Pending) {
			long := strings.Repeat("b", 64)
			p.Binding.Effects[0].Old, p.Binding.Effects[0].New = long, long
			p.Binding.Merge.Commit, p.Binding.Merge.Parents = long, []string{long, long}
			p.Binding.Summary.Base, p.Binding.Summary.Tip = long, long
		}, ""},
		{"no flagged path, empty lists", func(p *Pending) {
			p.Binding.Summary.Flagged, p.Binding.Summary.Paths = nil, nil
			p.Binding.Summary.Binary, p.Binding.Summary.Symlinks = nil, nil
		}, ""},
		{"an empty plan and note", func(p *Pending) { p.PlanText, p.Note = "", "" }, ""},
		{"an empty planner", func(p *Pending) { p.Planner = "" }, CodeRecordInvalid},
		{"a request id of another form", func(p *Pending) { p.RequestID = "ap-1" }, CodeRecordInvalid},
		{"a short request digest", func(p *Pending) { p.RequestSHA256 = id }, CodeRecordInvalid},
		{"an empty project", func(p *Pending) { p.Binding.Project = "" }, CodeRecordInvalid},
		{"a control character in the project", func(p *Pending) { p.Binding.Project = "project\x1b[2J" }, CodeRecordInvalid},
		{"a project that is not UTF-8", func(p *Pending) { p.Binding.Project = "project\xff" }, CodeRecordInvalid},
		{"another subject kind", func(p *Pending) { p.Binding.Subject.Kind = "pack" }, CodeRecordInvalid},
		{"an empty role", func(p *Pending) { p.Binding.Subject.RoleName = "" }, CodeRecordInvalid},
		{"a negative epoch", func(p *Pending) { p.Binding.Subject.Epoch = -1 }, CodeRecordInvalid},
		{"a negative seat revision", func(p *Pending) { p.Binding.Subject.SeatRev = -1 }, CodeRecordInvalid},
		{"no holder", func(p *Pending) { p.Binding.Subject.Holder = nil }, CodeRecordInvalid},
		{"a holder with no runtime", func(p *Pending) { p.Binding.Subject.Holder.Runtime = "" }, CodeRecordInvalid},
		{"another class", func(p *Pending) { p.Binding.Class = "outward" }, CodeRecordInvalid},
		{"no plan token", func(p *Pending) { p.Binding.PlanToken = "" }, CodeRecordInvalid},
		{"no effect", func(p *Pending) { p.Binding.Effects = nil }, CodeRecordInvalid},
		{"another effect kind", func(p *Pending) { p.Binding.Effects[0].Kind = "file" }, CodeRecordInvalid},
		{"an effect with no ref", func(p *Pending) { p.Binding.Effects[0].Ref = "" }, CodeRecordInvalid},
		{"an effect with a short id", func(p *Pending) { p.Binding.Effects[0].New = "0194daf" }, CodeRecordInvalid},
		{"an effect with ids of two lengths", func(p *Pending) { p.Binding.Effects[0].New = strings.Repeat("b", 64) }, CodeRecordInvalid},
		{"a delete that does not say where it was merged", func(p *Pending) { p.Binding.Effects[0].New = zeros }, CodeRecordInvalid},
		{"merged_into on a move", func(p *Pending) {
			p.Binding.Effects[0].MergedInto = &MergedInto{Ref: "refs/heads/develop", Tip: id}
		}, CodeRecordInvalid},
		{"a feature finish with no merge", func(p *Pending) { p.Binding.Merge = nil }, CodeRecordInvalid},
		{"a merge with one parent", func(p *Pending) { p.Binding.Merge.Parents = []string{id} }, CodeRecordInvalid},
		{"a merge with three parents", func(p *Pending) { p.Binding.Merge.Parents = []string{id, id, id} }, CodeRecordInvalid},
		{"a merge the lead built", func(p *Pending) { p.Binding.Merge.BuiltBy = "lead" }, CodeRecordInvalid},
		{"a feature finish with no summary", func(p *Pending) { p.Binding.Summary = nil }, CodeRecordInvalid},
		{"a short review digest", func(p *Pending) { p.Binding.Summary.ReviewDigest = id }, CodeRecordInvalid},
		{"a negative count", func(p *Pending) { p.Binding.Summary.Added = -1 }, CodeRecordInvalid},
		{"another status", func(p *Pending) { p.Binding.Summary.Paths[0].Status = "R" }, CodeRecordInvalid},
		{"a mode of five digits", func(p *Pending) { p.Binding.Summary.Paths[0].NewMode = "10064" }, CodeRecordInvalid},
		{"a mode that is not octal", func(p *Pending) { p.Binding.Summary.Paths[0].NewMode = "100648" }, CodeRecordInvalid},
		{"another flag kind", func(p *Pending) { p.Binding.Summary.Flagged[0].Kind = "odd" }, CodeRecordInvalid},
		{"one flag twice", func(p *Pending) {
			p.Binding.Summary.Flagged = append(p.Binding.Summary.Flagged, p.Binding.Summary.Flagged[0])
		}, CodeRecordInvalid},
		{"a control character in a pattern", func(p *Pending) { p.Binding.Summary.Flagged[0].Pattern = "Make\nfile" }, CodeRecordInvalid},
		{"no matcher", func(p *Pending) { p.Binding.Summary.ExecConfigSet.Matcher = "" }, CodeRecordInvalid},
		{"a feature finish with no evidence", func(p *Pending) { p.Binding.Evidence = nil }, CodeRecordInvalid},
		{"another evidence kind", func(p *Pending) { p.Binding.Evidence[0].Kind = "review" }, CodeRecordInvalid},
		{"evidence with no record id", func(p *Pending) { p.Binding.Evidence[0].RecordID = "" }, CodeRecordInvalid},
		{"a time with an offset", func(p *Pending) { p.Binding.RequestedAt = "2026-11-20T14:02:04+00:00" }, CodeRecordInvalid},
		{"a time that is no time", func(p *Pending) { p.Binding.PendingExpiresAt = "tomorrow" }, CodeRecordInvalid},
		{"a feature name with a capital", func(p *Pending) { p.Binding.Argv = []string{"Develop"} }, CodeFeatureName},
		{"a feature name with a control character", func(p *Pending) { p.Binding.Argv = []string{"a\x1b[2J"} }, CodeFeatureName},
		{"two feature names", func(p *Pending) { p.Binding.Argv = []string{"one", "two"} }, CodeFeatureName},
		{"no feature name", func(p *Pending) { p.Binding.Argv = nil }, CodeFeatureName},
		{"another verb", func(p *Pending) { p.Binding.Verb = "release publish" }, CodeVerbNotRequestable},
		{"a prune with a merge and a summary", func(p *Pending) { p.Binding.Verb, p.Binding.Argv = VerbBranchPrune, nil }, CodeRecordInvalid},
		{"a prune with an argument", func(p *Pending) {
			*p = prunePending(t)
			p.Binding.Argv = []string{"old-one"}
		}, CodeRecordInvalid},
		{"a prune of 200 refs", func(p *Pending) {
			*p = prunePending(t)
			for len(p.Binding.Effects) < MaxEffects {
				p.Binding.Effects = append(p.Binding.Effects, p.Binding.Effects[0])
			}
		}, ""},
		{"a prune of 201 refs", func(p *Pending) {
			*p = prunePending(t)
			for len(p.Binding.Effects) <= MaxEffects {
				p.Binding.Effects = append(p.Binding.Effects, p.Binding.Effects[0])
			}
		}, CodeRecordInvalid},
	}
	for _, tt := range tests {
		p := validPending(t)
		tt.change(&p)
		// The digests are made right for the changed record where they
		// can be, so that the refusal is the rule's and not a mismatch.
		p.Binding.PlanSHA256 = PlanDigest(string(p.PlanText))
		if digest, err := BindingDigest(p.Binding); err == nil {
			p.BindingSHA256 = digest
		}
		data, err := EncodePending(p)
		wantCode(t, tt.name, err, tt.want)
		if err == nil {
			if _, err := DecodePending(data); err != nil {
				t.Errorf("%s: encoded, and then refused: %v", tt.name, err)
			}
		}
	}
}

func prunePending(t testing.TB) Pending {
	t.Helper()
	p, err := DecodePending(readVector(t, "pending-valid-prune.json"))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A pending record whose digests do not cover what they should is refused,
// whichever way the difference came about.
func TestPendingDigestsAreChecked(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Pending)
	}{
		{"another binding digest", func(p *Pending) { p.BindingSHA256 = Digest([]byte("other")) }},
		{"another plan digest", func(p *Pending) { p.Binding.PlanSHA256 = Digest([]byte("other")) }},
		{"a changed plan text", func(p *Pending) { p.PlanText += "and one more step\n" }},
		{"a changed effect", func(p *Pending) { p.Binding.Effects[0].Ref = "refs/heads/master" }},
		{"a changed count", func(p *Pending) { p.Binding.Summary.Files++ }},
		{"a flag taken out", func(p *Pending) { p.Binding.Summary.Flagged = p.Binding.Summary.Flagged[:1] }},
		{"a later expiry", func(p *Pending) { p.Binding.PendingExpiresAt = "2027-11-21T14:02:04Z" }},
	}
	for _, tt := range tests {
		p := validPending(t)
		tt.change(&p)
		_, err := EncodePending(p)
		wantCode(t, tt.name, err, CodeBindingMismatch)
	}

	// The note is outside the binding: it changes no digest.
	p := validPending(t)
	p.Note = "another note"
	if _, err := EncodePending(p); err != nil {
		t.Errorf("a changed note was refused: %v", err)
	}

	// Each byte of the binding is under the digest: a changed byte in the
	// encoded record is a mismatch, or breaks the form.
	data := readVector(t, "pending-valid.json")
	at := bytes.Index(data, []byte(`"files":5`))
	changed := bytes.Clone(data)
	changed[at+len(`"files":`)] = '6'
	_, err := DecodePending(changed)
	wantCode(t, "a changed byte in the binding", err, CodeBindingMismatch)
}

func TestBindingDigest(t *testing.T) {
	p := validPending(t)
	first, err := BindingDigest(p.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := BindingDigest(p.Binding); again != first {
		t.Error("the digest of one binding differs between two calls")
	}
	// An empty list and a list that was never made are the same binding.
	q := validPending(t)
	q.Binding.Summary.Submodules = nil
	if got, _ := BindingDigest(q.Binding); got != first {
		t.Error("a nil list changed the digest")
	}
	q.Binding.Summary.Submodules = []Text{"vendor/lib"}
	if got, _ := BindingDigest(q.Binding); got == first {
		t.Error("a new path left the digest as it was")
	}
	q = validPending(t)
	q.Binding.Project = "bad\x00project"
	if _, err := BindingDigest(q.Binding); err == nil {
		t.Error("a binding with a NUL in a plain string has a digest")
	}

	if got := PlanDigest(""); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("PlanDigest of nothing = %s", got)
	}
	if got := Digest([]byte("abc")); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Errorf("Digest(abc) = %s", got)
	}
	if PlanDigest("abc") != Digest([]byte("abc")) {
		t.Error("the plan digest is not the digest of the plan's bytes")
	}
}

func TestDecisionContentRules(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Decision)
		want   Code
	}{
		{"unchanged", func(d *Decision) {}, ""},
		{"no sign-off", func(d *Decision) { d.SignedOff = nil }, ""},
		{"a remark", func(d *Decision) { d.Remark = "looks right" }, ""},
		{"no terminal", func(d *Decision) { d.Console.TTY = "" }, ""},
		{"a deny with no reason", func(d *Decision) { d.Decision, d.Uses = DecisionDeny, 0 }, ""},
		{"a deny with a reason", func(d *Decision) { d.Decision, d.Uses, d.Reason = DecisionDeny, 0, "not yet" }, ""},
		{"a deny that keeps a use", func(d *Decision) { d.Decision = DecisionDeny }, CodeDecisionInvalid},
		{"an approve with no use", func(d *Decision) { d.Uses = 0 }, CodeDecisionInvalid},
		{"an approve with two uses", func(d *Decision) { d.Uses = 2 }, CodeDecisionInvalid},
		{"an approve with a reason", func(d *Decision) { d.Reason = "because" }, CodeDecisionInvalid},
		{"another answer", func(d *Decision) { d.Decision = "maybe" }, CodeDecisionInvalid},
		{"an answer in upper case", func(d *Decision) { d.Decision = "Approve" }, CodeDecisionInvalid},
		{"a request id for an id", func(d *Decision) { d.ID = "rq-1" }, CodeDecisionInvalid},
		{"no planner", func(d *Decision) { d.Planner = "" }, CodeDecisionInvalid},
		{"a short binding digest", func(d *Decision) { d.BindingSHA256 = "12969c83" }, CodeDecisionInvalid},
		{"a binding digest in upper case", func(d *Decision) { d.BindingSHA256 = strings.ToUpper(d.BindingSHA256) }, CodeDecisionInvalid},
		{"no project", func(d *Decision) { d.Project = "" }, CodeDecisionInvalid},
		{"a subject with a holder", func(d *Decision) {
			d.Subject.Holder = &Holder{Kind: "lead", DisplayID: "role-a@e7", Runtime: "runtime-a"}
		}, CodeDecisionInvalid},
		{"a negative seat revision", func(d *Decision) { d.Subject.SeatRev = -1 }, CodeDecisionInvalid},
		{"no plan token", func(d *Decision) { d.PlanToken = "" }, CodeDecisionInvalid},
		{"a sign-off of another kind", func(d *Decision) { d.SignedOff[0].Kind = "odd" }, CodeDecisionInvalid},
		{"one sign-off twice", func(d *Decision) { d.SignedOff = append(d.SignedOff, d.SignedOff[0]) }, CodeDecisionInvalid},
		{"a time with an offset", func(d *Decision) { d.DecidedAt = "2026-11-20T06:06:40-08:00" }, CodeDecisionInvalid},
		{"no expiry", func(d *Decision) { d.ExpiresAt = "" }, CodeDecisionInvalid},
		{"confirmed another way", func(d *Decision) { d.Confirmed = "key" }, CodeDecisionInvalid},
		{"a control character in the remark", func(d *Decision) { d.Remark = "fine\x1b[2J" }, CodeDecisionInvalid},
		{"a line feed in the reason", func(d *Decision) { d.Decision, d.Uses, d.Reason = DecisionDeny, 0, "no\nyes" }, CodeDecisionInvalid},
		{"no process id", func(d *Decision) { d.Console.PID = 0 }, CodeDecisionInvalid},
		{"no client", func(d *Decision) { d.Console.Client = "" }, CodeDecisionInvalid},
		{"no surface", func(d *Decision) { d.Console.Surface = "" }, CodeDecisionInvalid},
		{"a short session", func(d *Decision) { d.Console.Session = "0123456789abcde" }, CodeDecisionInvalid},
		{"a session in upper case", func(d *Decision) { d.Console.Session = "0123456789ABCDEF" }, CodeDecisionInvalid},
	}
	for _, tt := range tests {
		d := validDecision(t)
		tt.change(&d)
		data, err := EncodeDecision(d)
		wantCode(t, tt.name, err, tt.want)
		if err == nil {
			if _, err := DecodeDecision(data); err != nil {
				t.Errorf("%s: encoded, and then refused: %v", tt.name, err)
			}
		}
	}

	// The surface and the session a decision came from are part of its
	// console block, beside the four fields of the design.
	data := readVector(t, "decision-valid.json")
	const block = `"console":{"client":"ataniq-console 0.0.11","decider":true,"pid":41822,"session":"0123456789abcdef","surface":"console","tty":"/dev/pts/4"}`
	if !bytes.Contains(data, []byte(block)) {
		t.Errorf("the valid decision lacks %s", block)
	}
	for _, key := range []string{"session", "surface", "decider", "pid", "tty", "client"} {
		without := strings.Replace(string(data), `"`+key+`":`, `"x`+key+`":`, 1)
		_, err := DecodeDecision([]byte(without))
		wantCode(t, "a console block without "+key, err, CodeDecisionInvalid)
	}
}

func TestRequestContentRules(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Request)
		want   Code
	}{
		{"unchanged", func(r *Request) {}, ""},
		{"a prune", func(r *Request) { r.Verb, r.Argv, r.FeatureSHA = VerbBranchPrune, nil, "" }, ""},
		{"a prune with a tip", func(r *Request) { r.Verb, r.Argv = VerbBranchPrune, nil }, CodeRequestInvalid},
		{"a prune with an argument", func(r *Request) { r.Verb, r.FeatureSHA = VerbBranchPrune, "" }, CodeRequestInvalid},
		{"a feature finish with no tip", func(r *Request) { r.FeatureSHA = "" }, CodeRequestInvalid},
		{"a pending id for an id", func(r *Request) { r.RequestID = "ap-1" }, CodeRequestInvalid},
		{"an id with a path in it", func(r *Request) { r.RequestID = "rq-../../x" }, CodeRequestInvalid},
		{"a short note digest", func(r *Request) { r.NoteSHA256 = "abc" }, CodeRequestInvalid},
		{"a feature name shaped like a path", func(r *Request) { r.Argv = []string{"../develop"} }, CodeFeatureName},
	}
	for _, tt := range tests {
		r := validRequest(t)
		tt.change(&r)
		data, err := EncodeRequest(r)
		wantCode(t, tt.name, err, tt.want)
		if err == nil {
			if _, err := DecodeRequest(data); err != nil {
				t.Errorf("%s: encoded, and then refused: %v", tt.name, err)
			}
		}
	}
}

func TestValidNames(t *testing.T) {
	long48, long49 := strings.Repeat("a", 48), strings.Repeat("a", 49)
	names := []struct {
		in   string
		want bool
	}{
		{"a", true}, {"lead-seat-gate", true}, {"0", true}, {"-", true}, {"a-then-code", true},
		{"approve", true}, {"develop", true}, {long48, true},
		{"", false}, {long49, false}, {"A", false}, {"a_b", false}, {"a b", false}, {"a/b", false},
		{"a.b", false}, {"a\n", false}, {"\u00e9", false}, {"a\x00", false}, {"a\u202eb", false},
	}
	for _, tt := range names {
		if got := ValidFeatureName(tt.in); got != tt.want {
			t.Errorf("ValidFeatureName(%q) = %v", tt.in, got)
		}
		if got := ValidRequestID("rq-" + tt.in); got != tt.want {
			t.Errorf("ValidRequestID(%q) = %v", "rq-"+tt.in, got)
		}
		if got := ValidPendingID("ap-" + tt.in); got != tt.want {
			t.Errorf("ValidPendingID(%q) = %v", "ap-"+tt.in, got)
		}
	}
	for _, in := range []string{"", "rq", "rq-", "ap-1", "RQ-1", "rq_1", "1", " rq-1", "rq-1 "} {
		if ValidRequestID(in) {
			t.Errorf("ValidRequestID(%q) = true", in)
		}
	}
	for _, in := range []string{"", "ap", "ap-", "rq-1", "AP-1", "ap_1", "1", " ap-1", "ap-1\n"} {
		if ValidPendingID(in) {
			t.Errorf("ValidPendingID(%q) = true", in)
		}
	}

	ids := []struct {
		in   string
		want bool
	}{
		{strings.Repeat("0", 40), true}, {strings.Repeat("a1", 20), true}, {strings.Repeat("f", 64), true},
		{"", false}, {strings.Repeat("a", 39), false}, {strings.Repeat("a", 41), false},
		{strings.Repeat("a", 63), false}, {strings.Repeat("a", 65), false}, {strings.Repeat("A", 40), false},
		{strings.Repeat("g", 40), false}, {strings.Repeat("a", 39) + "\n", false}, {"8281895", false},
	}
	for _, tt := range ids {
		if got := ValidObjectID(tt.in); got != tt.want {
			t.Errorf("ValidObjectID(%q) = %v", tt.in, got)
		}
	}
}

func TestSignOffsEqual(t *testing.T) {
	a := Flag{Kind: FlagExecConfig, Path: "Makefile", Pattern: "Makefile"}
	b := Flag{Kind: FlagSymlink, Path: "Makefile"}
	c := Flag{Kind: FlagExecConfig, Path: "caf\xe9"}
	off := func(f Flag) SignOff { return SignOff{Kind: f.Kind, Path: f.Path} }
	tests := []struct {
		name    string
		flagged []Flag
		signed  []SignOff
		want    bool
	}{
		{"nothing flagged, nothing signed", nil, nil, true},
		{"nil and empty", []Flag{}, nil, true},
		{"all signed in order", []Flag{a, b, c}, []SignOff{off(a), off(b), off(c)}, true},
		{"the pattern is not part of a sign-off", []Flag{a}, []SignOff{{Kind: FlagExecConfig, Path: "Makefile"}}, true},
		{"one missing", []Flag{a, b}, []SignOff{off(a)}, false},
		{"none signed", []Flag{a}, nil, false},
		{"one that is not flagged", []Flag{a}, []SignOff{off(a), off(b)}, false},
		{"signed with nothing flagged", nil, []SignOff{off(a)}, false},
		{"another order", []Flag{a, b}, []SignOff{off(b), off(a)}, false},
		{"another kind for the path", []Flag{a}, []SignOff{off(b)}, false},
		{"another path for the kind", []Flag{a}, []SignOff{off(c)}, false},
		{"one signed twice for two flags", []Flag{a, b}, []SignOff{off(a), off(a)}, false},
		{"a flag listed twice", []Flag{a, a}, []SignOff{off(a), off(a)}, false},
		{"paths that differ by a byte", []Flag{c}, []SignOff{{Kind: FlagExecConfig, Path: "caf\u00e9"}}, false},
	}
	for _, tt := range tests {
		if got := SignOffsEqual(tt.flagged, tt.signed); got != tt.want {
			t.Errorf("%s: SignOffsEqual = %v", tt.name, got)
		}
	}
}

func TestStatesAndCodes(t *testing.T) {
	states := []State{"pending", "approved", "approved-by-rule", "approved-by-grant", "denied", "withdrawn",
		"expired", "void", "consumed", "applied", "not-applied", "unknown"}
	constants := []State{StatePending, StateApproved, StateApprovedByRule, StateApprovedByGrant, StateDenied,
		StateWithdrawn, StateExpired, StateVoid, StateConsumed, StateApplied, StateNotApplied, StateUnknown}
	if !reflect.DeepEqual(states, constants) {
		t.Errorf("the states are %v", constants)
	}
	for _, s := range states {
		if !s.Valid() {
			t.Errorf("State(%q).Valid() = false", s)
		}
	}
	for _, s := range []State{"", "held", "Pending", "collected", "verifying", "refused", "pending "} {
		if s.Valid() {
			t.Errorf("State(%q).Valid() = true", s)
		}
	}

	codes := map[Code]string{
		CodeApprovalRefused: "approval-refused", CodeRequestInvalid: "request-invalid",
		CodeRequestRate: "request-rate", CodeRequestPendingCap: "request-pending-cap",
		CodeRequestDuplicate: "request-duplicate", CodeRequestThrottled: "request-throttled",
		CodePreviousEpoch: "previous-epoch", CodeNotAssigned: "not-assigned",
		CodeVerbNotRequestable: "verb-not-requestable", CodeFeatureName: "feature-name",
		CodeFeatureMoved: "feature-moved", CodeMergeConflict: "merge-conflict",
		CodeVerificationFailed: "verification-failed", CodePruneUnmerged: "prune-unmerged",
		CodePruneProtected: "prune-protected", CodeDecisionInvalid: "decision-invalid",
		CodeBindingMismatch: "binding-mismatch", CodeEvidenceChanged: "evidence-changed",
		CodeApprovalExpired: "approval-expired", CodeApprovalVoid: "approval-void",
		CodeApprovalConsumed: "approval-consumed", CodeDecisionSignature: "decision-signature",
		CodeGrantRevoked: "grant-revoked", CodeGrantNoMatch: "grant-no-match",
		CodeVerbNotGrantable: "verb-not-grantable", CodeRecordTooLarge: "record-too-large",
		CodeRecordInvalid: "record-invalid", CodeReviewChanged: "review-changed",
		CodeOwnerAway: "owner-away", CodeNotDecider: "not-decider", CodeLimitRefused: "limit-refused",
	}
	if len(codes) != 31 {
		t.Errorf("%d codes with different words, want 31", len(codes))
	}
	for code, word := range codes {
		if string(code) != word {
			t.Errorf("the code %s is written %q", word, code)
		}
	}

	if SchemaRequest != "qargi.approval-request/1" || SchemaPending != "ataniq.approval-pending/1" ||
		SchemaDecision != "ataniq.owner-decision/1" {
		t.Error("a schema name is not the one the contract fixes")
	}
}

func TestError(t *testing.T) {
	err := error(&Error{Code: CodeFeatureName, Detail: "the name is too long"})
	if got := err.Error(); got != "feature-name: the name is too long" {
		t.Errorf("Error() = %q", got)
	}
	if got := (&Error{Code: CodeOwnerAway}).Error(); got != "owner-away" {
		t.Errorf("Error() with no detail = %q", got)
	}
	if code, ok := CodeOf(err); !ok || code != CodeFeatureName {
		t.Errorf("CodeOf = %q, %v", code, ok)
	}
	if _, ok := CodeOf(nil); ok {
		t.Error("CodeOf(nil) found a code")
	}
	if _, ok := CodeOf(errors.New("another error")); ok {
		t.Error("CodeOf found a code in an error of another type")
	}
	if code, ok := CodeOf(fmt.Errorf("deciding: %w", err)); !ok || code != CodeFeatureName {
		t.Errorf("CodeOf of a wrapped refusal = %q, %v", code, ok)
	}
}

// The detail of a refusal is shown to the owner without escaping, so it
// must never repeat what the record said: not a key, not a value.
func TestDetailNeverHoldsTextOfTheRecord(t *testing.T) {
	const mark = "LEADTEXT"
	valid := string(readVector(t, "request-valid.json"))
	for _, tt := range []struct{ name, old, new string }{
		{"an unknown key", `"epoch":7,`, `"epoch":7,"` + mark + `":1,`},
		{"a key in another case", `"epoch"`, `"` + mark + `epoch"`},
		{"a schema", `"qargi.approval-request/1"`, `"` + mark + `"`},
		{"a request id", `"rq-20261120t140201z-7c1e"`, `"rq-` + mark + `"`},
		{"a verb", `"feature finish"`, `"` + mark + `"`},
		{"a feature name", `"sample-feature"`, `"` + mark + `"`},
		{"an object id", `"8281895a8281895a8281895a8281895a8281895a"`, `"` + mark + `"`},
		{"a time", `"2026-11-20T14:02:01Z"`, `"` + mark + `"`},
		{"a value of the wrong type", `"epoch":7`, `"epoch":"` + mark + `"`},
		{"a word that is no JSON", `"epoch":7`, `"epoch":` + mark},
		{"a bad escape", `"feature finish"`, `"` + mark + `\q"`},
	} {
		if !strings.Contains(valid, tt.old) {
			t.Fatalf("%s: the valid vector does not hold %s", tt.name, tt.old)
		}
		_, err := DecodeRequest([]byte(strings.Replace(valid, tt.old, tt.new, 1)))
		if err == nil {
			t.Errorf("%s: accepted", tt.name)
			continue
		}
		if strings.Contains(err.Error(), mark) {
			t.Errorf("%s: the detail repeats the record: %v", tt.name, err)
		}
		if !plain(err.Error()) {
			t.Errorf("%s: the detail holds a control character: %q", tt.name, err.Error())
		}
	}

	p := validPending(t)
	p.Binding.Summary.Flagged[0].Kind = mark
	p.Binding.Summary.Paths[0].Status = mark
	p.Binding.Class = mark
	if _, err := EncodePending(p); err == nil || strings.Contains(err.Error(), mark) {
		t.Errorf("a pending record's detail repeats the record: %v", err)
	}
	d := validDecision(t)
	d.Decision = mark
	if _, err := EncodeDecision(d); err == nil || strings.Contains(err.Error(), mark) {
		t.Errorf("a decision's detail repeats the record: %v", err)
	}
}

func TestTimes(t *testing.T) {
	at := time.Date(2026, 11, 20, 6, 2, 1, 999, time.FixedZone("west", -8*3600))
	if got := FormatTime(at); got != "2026-11-20T14:02:01Z" {
		t.Errorf("FormatTime = %s", got)
	}
	back, err := ParseTime("2026-11-20T14:02:01Z")
	if err != nil || !back.Equal(at.Truncate(time.Second)) || back.Location() != time.UTC {
		t.Errorf("ParseTime = %v, %v", back, err)
	}
	for _, in := range []string{"", "2026-11-20T14:02:01", "2026-11-20T14:02:01.000Z", "2026-11-20T14:02:01+00:00",
		"2026-11-20 14:02:01Z", "2026-11-20T14:02:01z", "2026-11-20T24:00:00Z", "2026-02-30T00:00:00Z",
		"2026-11-20T14:02:01Z ", "20261120T140201Z"} {
		if _, err := ParseTime(in); err == nil {
			t.Errorf("ParseTime(%q) accepted", in)
		}
	}
}
