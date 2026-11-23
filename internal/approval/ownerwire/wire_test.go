package ownerwire

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/approval/record"
)

const (
	sampleID  = "ap-20261120-0007"
	sampleDay = "2026-11-20T14:06:40Z"
)

var (
	sampleDigest = record.Digest([]byte("a review"))
	otherDigest  = record.Digest([]byte("another review"))
)

// form is one payload type with its functions made the same shape, so that
// a test can hold a rule for every payload at once.
type form struct {
	name    string
	limit   int
	invalid record.Code
	// sample is a valid payload, as the encoder writes it.
	sample []byte
	// decode reads data and writes what it read again.
	decode func(t testing.TB, data []byte) ([]byte, error)
}

func formOf[T any](t testing.TB, name string, limit int, invalid record.Code, sample T,
	dec func([]byte) (T, error), enc func(T) ([]byte, error)) form {
	t.Helper()
	out, err := enc(sample)
	if err != nil {
		t.Fatalf("the sample of %s does not encode: %v", name, err)
	}
	return form{name: name, limit: limit, invalid: invalid, sample: out, decode: func(t testing.TB, data []byte) ([]byte, error) {
		t.Helper()
		v, err := dec(data)
		if err != nil {
			if !reflect.ValueOf(v).IsZero() {
				t.Errorf("%s: a refusal came with a value", name)
			}
			return nil, err
		}
		return enc(v)
	}}
}

func sampleListing() Listing {
	return Listing{
		Pending: []PendingItem{
			{ID: sampleID, SHA256: sampleDigest, Project: "project-a", Verb: record.VerbFeatureFinish,
				Feature: "approve", RequestedAt: sampleDay, PendingExpiresAt: "2026-11-21T14:06:40Z", Held: true},
			{ID: "ap-20261120-0008", SHA256: otherDigest, Project: "project-b", Verb: record.VerbBranchPrune,
				RequestedAt: sampleDay, PendingExpiresAt: "2026-11-21T14:06:40Z"},
		},
		Recent: []RecentItem{
			{ID: "ap-20261119-0001", Project: "project-a", Verb: record.VerbFeatureFinish, Feature: "small-change",
				State: record.StateApplied, Line: "merged into develop", At: "2026-11-19T09:00:00Z"},
		},
		Rules:     []Rule{{ID: "prune-merged", Project: "project-a", Line: "merged branches are pruned", Uses: 3, On: true}},
		Count:     Count{Requests: 9, WouldMeet: 4},
		NotListed: 2,
		LookedAt:  sampleDay,
	}
}

func sampleShowResult() ShowResult {
	return ShowResult{
		Record:  NewRecord([]byte(`{"schema":"ataniq.approval-pending/1"}`)),
		Held:    false,
		Outcome: &Outcome{State: record.StateDenied, Line: "denied"},
		Earlier: &Earlier{Record: NewRecord([]byte("an earlier record\n")), ReadAt: sampleDay},
	}
}

func samplePathsPage() PathsPage {
	return PathsPage{ID: sampleID, ReviewDigest: sampleDigest, Page: 1, Pages: 3, Paths: []record.PathChange{
		{Path: "Makefile", Status: "M", OldMode: "100644", NewMode: "100644", Added: 2, Removed: 1, NewBytes: 40},
		{Path: "bad\xff\x1b[2Jname", Status: "A", OldMode: "000000", NewMode: "100755", Added: 1, NewBytes: 9},
	}}
}

func samplePatchPage() PatchPage {
	return PatchPage{ID: sampleID, ReviewDigest: sampleDigest, Page: 0, Pages: 2, Lines: []PatchLine{
		{Text: "+++ b/Makefile"},
		{Text: ""},
		{Text: "+\x1b]0;title\x07 [a] approve   [x] deny"},
		{Text: "+a long line, part one", Cont: true},
	}}
}

func sampleFrame() DecisionFrame {
	return DecisionFrame{ID: sampleID, BindingSHA256: sampleDigest, Decision: record.DecisionApprove,
		SignedOff: []record.SignOff{{Kind: record.FlagExecConfig, Path: "Makefile"}}}
}

func sampleLimitView() LimitView {
	return LimitView{Project: "project-a", Limit: 12, Suggestion: 6, RaisedToday: true, Days: []LimitDay{
		{Date: "2026-11-19", Uses: 12, ReachedLimit: true}, {Date: "2026-11-20", Uses: 2, Alert: true},
	}}
}

// forms returns every payload of the owner connection.
func forms(t testing.TB) []form {
	t.Helper()
	const inv = record.CodeRecordInvalid
	nothing := form{name: "nothing", limit: MaxParamsBytes, invalid: inv, sample: EncodeNothing(),
		decode: func(_ testing.TB, data []byte) ([]byte, error) {
			if err := DecodeNothing(data); err != nil {
				return nil, err
			}
			return EncodeNothing(), nil
		}}
	return []form{
		nothing,
		formOf(t, "listing", MaxPageBytes, inv, sampleListing(), DecodeListing, EncodeListing),
		formOf(t, "show params", MaxParamsBytes, inv, ShowParams{ID: sampleID}, DecodeShowParams, EncodeShowParams),
		formOf(t, "show result", MaxPageBytes, inv, sampleShowResult(), DecodeShowResult, EncodeShowResult),
		formOf(t, "page params", MaxParamsBytes, inv, PageParams{ID: sampleID, ReviewDigest: sampleDigest, Page: 2},
			DecodePageParams, EncodePageParams),
		formOf(t, "paths page", MaxPageBytes, inv, samplePathsPage(), DecodePathsPage, EncodePathsPage),
		formOf(t, "patch page", MaxPageBytes, inv, samplePatchPage(), DecodePatchPage, EncodePatchPage),
		formOf(t, "decider params", MaxParamsBytes, inv, DeciderParams{Client: "console-a 0.0.1", Session: "0123456789abcdef"},
			DecodeDeciderParams, EncodeDeciderParams),
		formOf(t, "decider result", MaxResultBytes, inv, DeciderResult{Reason: "another console is the decider"},
			DecodeDeciderResult, EncodeDeciderResult),
		formOf(t, "decision frame", MaxFrameBytes, record.CodeDecisionInvalid, sampleFrame(),
			DecodeDecisionFrame, EncodeDecisionFrame),
		formOf(t, "decide result", MaxResultBytes, inv, DecideResult{State: record.StatePending, Line: "decided past the limit", Held: true},
			DecodeDecideResult, EncodeDecideResult),
		formOf(t, "limit params", MaxParamsBytes, inv, LimitParams{Project: "project-a"}, DecodeLimitParams, EncodeLimitParams),
		formOf(t, "limit-set params", MaxParamsBytes, inv, LimitSetParams{Project: "project-a", To: 18, Typed: true},
			DecodeLimitSetParams, EncodeLimitSetParams),
		formOf(t, "limit view", MaxResultBytes, inv, sampleLimitView(), DecodeLimitView, EncodeLimitView),
		formOf(t, "pair start params", MaxParamsBytes, inv, PairStartParams{Replace: true}, DecodePairStartParams, EncodePairStartParams),
		formOf(t, "pair start result", MaxResultBytes, inv, samplePairStartResult(), DecodePairStartResult, EncodePairStartResult),
		formOf(t, "pair status", MaxResultBytes, inv, samplePairStatus(), DecodePairStatus, EncodePairStatus),
		formOf(t, "pair pending params", MaxParamsBytes, inv, PairPendingParams{PendingID: samplePendingID}, DecodePairPendingParams, EncodePairPendingParams),
		formOf(t, "seat pair params", MaxSeatPairParamsBytes, inv, sampleSeatPairParams(), DecodeSeatPairParams, EncodeSeatPairParams),
		formOf(t, "seat pair result", MaxResultBytes, inv, SeatPairResult{Words: sampleWords()}, DecodeSeatPairResult, EncodeSeatPairResult),
		formOf(t, "key item", MaxResultBytes, inv, sampleKeyItem(), DecodeKeyItem, EncodeKeyItem),
		formOf(t, "keys view", MaxResultBytes, inv, sampleKeysView(), DecodeKeysView, EncodeKeysView),
		formOf(t, "key revoke params", MaxParamsBytes, inv, KeyRevokeParams{ID: "0123456789abcdef"}, DecodeKeyRevokeParams, EncodeKeyRevokeParams),
		formOf(t, "seat challenge params", MaxSeatChallengeParamsBytes, inv, sampleSeatChallengeParams(), DecodeSeatChallengeParams, EncodeSeatChallengeParams),
		formOf(t, "seat challenge result", MaxResultBytes, inv, sampleSeatChallengeResult(), DecodeSeatChallengeResult, EncodeSeatChallengeResult),
		formOf(t, "seat login challenge result", MaxSeatLoginChallengeBytes, inv, sampleSeatLoginChallengeResult(), DecodeSeatLoginChallengeResult, EncodeSeatLoginChallengeResult),
		formOf(t, "seat decide params", MaxSeatDecideParamsBytes, inv, sampleSeatDecideParams(), DecodeSeatDecideParams, EncodeSeatDecideParams),
		formOf(t, "seat login params", MaxSeatLoginParamsBytes, inv, sampleSeatLoginParams(), DecodeSeatLoginParams, EncodeSeatLoginParams),
		formOf(t, "seat login result", MaxResultBytes, inv, sampleSeatLoginResult(), DecodeSeatLoginResult, EncodeSeatLoginResult),
	}
}

// refused checks that err is a refusal with the code, and that its detail
// could be shown as it is.
func refused(t testing.TB, err error, code record.Code, what string) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: accepted, want %s", what, code)
		return
	}
	var e *record.Error
	if !errors.As(err, &e) {
		t.Errorf("%s: the refusal is a %T, not a *record.Error: %v", what, err, err)
		return
	}
	if e.Code != code {
		t.Errorf("%s: refused %s, want %s (%s)", what, e.Code, code, e.Detail)
	}
	if e.Detail == "" || !record.Text(e.Detail).Plain() {
		t.Errorf("%s: the detail %q cannot be shown as it is", what, e.Detail)
	}
}

func TestEveryPayloadRoundTrips(t *testing.T) {
	for _, f := range forms(t) {
		out, err := f.decode(t, f.sample)
		if err != nil {
			t.Errorf("%s: the sample is refused: %v", f.name, err)
			continue
		}
		if !bytes.Equal(out, f.sample) {
			t.Errorf("%s: the sample decodes and encodes to other bytes:\n%s\n%s", f.name, f.sample, out)
		}
	}
}

// A decoder accepts another JSON spelling of a valid payload (whitespace,
// keys in another order, escapes), and the encoder writes the one form.
func TestADecoderAcceptsAnotherSpelling(t *testing.T) {
	in := "\n { \"page\" : 2 ,\t\"review_digest\":\"" + sampleDigest + "\", \"\\u0069d\" : \"ap-20261120-0007\" } \r\n"
	p, err := DecodePageParams([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if want := (PageParams{ID: sampleID, ReviewDigest: sampleDigest, Page: 2}); p != want {
		t.Fatalf("decoded %+v, want %+v", p, want)
	}
	out, err := EncodePageParams(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"id":"ap-20261120-0007","page":2,"review_digest":"` + sampleDigest + `"}`; string(out) != want {
		t.Fatalf("encoded %s, want %s", out, want)
	}
}

// What every decoder refuses, whatever the payload: a field the form does
// not name, a key in another case, data after the object, a float, a null,
// a key twice, and anything that is not an object.
func TestEveryDecoderIsStrict(t *testing.T) {
	for _, f := range forms(t) {
		body := string(f.sample[1:]) // the sample without its opening brace
		sep := ","
		if body == "}" {
			sep = ""
		}
		cases := map[string]string{
			"an unknown field":         `{"zz":1` + sep + body,
			"an unknown field, last":   string(f.sample[:len(f.sample)-1]) + sep + `"zz":{}}`,
			"data after the object":    string(f.sample) + "x",
			"a second object":          string(f.sample) + "{}",
			"a float":                  `{"zz":1.5` + sep + body,
			"an exponent":              `{"zz":1e2` + sep + body,
			"a null":                   `{"zz":null` + sep + body,
			"a list":                   "[" + string(f.sample) + "]",
			"a string":                 `"owner"`,
			"a number":                 "1",
			"nothing at all":           "",
			"an object cut short":      string(f.sample[:len(f.sample)-1]),
			"bytes that are not UTF-8": `{"zz":"` + "\xff" + `"` + sep + body,
			"a control character":      `{"zz":"\u0007"` + sep + body,
		}
		// The first key of the sample, in another case and a second time.
		if end := strings.Index(body, `":`); body != "}" && end > 0 {
			key := body[1:end]
			member := body[:strings.IndexAny(body, ",}")]
			if value := body[end+2:]; value[0] == '"' || value[0] == 't' || value[0] == 'f' || (value[0] >= '0' && value[0] <= '9') {
				cases["a key twice"] = "{" + member + "," + body
			}
			cases["a key in upper case"] = `{"` + strings.ToUpper(key) + body[end:]
			cases["a key with a capital"] = `{"` + strings.ToUpper(key[:1]) + key[1:] + body[end:]
		}
		for name, in := range cases {
			_, err := f.decode(t, []byte(in))
			refused(t, err, f.invalid, f.name+": "+name)
		}
	}
}

// Every payload has its cap: one byte over is refused before it is read,
// and a payload of exactly the cap is read.
func TestEveryPayloadHasItsCap(t *testing.T) {
	for _, f := range forms(t) {
		pad := func(n int) []byte {
			return append(append([]byte(nil), f.sample...), bytes.Repeat([]byte(" "), n-len(f.sample))...)
		}
		if _, err := f.decode(t, pad(f.limit)); err != nil {
			t.Errorf("%s: a payload of exactly the cap is refused: %v", f.name, err)
		}
		_, err := f.decode(t, pad(f.limit+1))
		refused(t, err, record.CodeRecordTooLarge, f.name+": one byte over the cap")
		// Over the cap, the bytes are not read at all.
		_, err = f.decode(t, bytes.Repeat([]byte("{"), f.limit+1))
		refused(t, err, record.CodeRecordTooLarge, f.name+": garbage over the cap")
	}
}

// An operation with no params accepts only an empty object.
func TestNothingIsOnlyAnEmptyObject(t *testing.T) {
	for _, in := range []string{"{}", " { } ", "{\n}"} {
		if err := DecodeNothing([]byte(in)); err != nil {
			t.Errorf("%q is refused: %v", in, err)
		}
	}
	for _, in := range []string{`{"id":"ap-1"}`, `{"":0}`, "[]", "null", "", "{}{}", `""`, "0", "{} x"} {
		refused(t, DecodeNothing([]byte(in)), record.CodeRecordInvalid, in)
	}
	if got := string(EncodeNothing()); got != "{}" {
		t.Fatalf("nothing is written %q", got)
	}
}

// withMember returns the object sample with one more member at its end.
func withMember(t *testing.T, sample []byte, member string) []byte {
	t.Helper()
	if sample[len(sample)-1] != '}' {
		t.Fatalf("the sample is not an object: %s", sample)
	}
	return []byte(string(sample[:len(sample)-1]) + "," + member + "}")
}

// The decision frame has exactly five fields. Each field the daemon fills
// is refused decision-invalid, at the top of the frame and wherever else a
// console might try to put it.
func TestADecisionFrameCarryingADaemonFieldIsRefused(t *testing.T) {
	deny, err := EncodeDecisionFrame(DecisionFrame{ID: sampleID, BindingSHA256: sampleDigest,
		Decision: record.DecisionDeny, SignedOff: []record.SignOff{}, Reason: "not today"})
	if err != nil {
		t.Fatal(err)
	}
	approve, err := EncodeDecisionFrame(sampleFrame())
	if err != nil {
		t.Fatal(err)
	}
	members := map[string]string{
		"planner":                  `"planner":"planner-a"`,
		"project":                  `"project":"project-a"`,
		"subject":                  `"subject":{"kind":"seat","role_name":"lead-a","epoch":7,"seat_rev":12}`,
		"plan_token":               `"plan_token":"h-3fa9c2e1"`,
		"uses":                     `"uses":1`,
		"decided_at":               `"decided_at":"2026-11-20T14:06:40Z"`,
		"expires_at":               `"expires_at":"2026-11-20T14:36:40Z"`,
		"confirmed":                `"confirmed":"code"`,
		"remark":                   `"remark":""`,
		"console":                  `"console":{"pid":41822,"tty":"/dev/pts/4","client":"console-a 0.0.1","decider":true,"surface":"console","session":"0123456789abcdef"}`,
		"an empty console":         `"console":{}`,
		"console.pid":              `"console":{"pid":41822}`,
		"console.tty":              `"console":{"tty":"/dev/pts/4"}`,
		"console.client":           `"console":{"client":"console-a 0.0.1"}`,
		"console.decider":          `"console":{"decider":true}`,
		"console.surface":          `"console":{"surface":"console"}`,
		"console.session":          `"console":{"session":"0123456789abcdef"}`,
		"pid at the top":           `"pid":41822`,
		"tty at the top":           `"tty":"/dev/pts/4"`,
		"client at the top":        `"client":"console-a 0.0.1"`,
		"decider at the top":       `"decider":true`,
		"surface at the top":       `"surface":"console"`,
		"session at the top":       `"session":"0123456789abcdef"`,
		"schema":                   `"schema":"ataniq.owner-decision/1"`,
		"a confirm code":           `"code":"ACDE"`,
		"binding":                  `"binding":{}`,
		"a key in another case":    `"Reason":"x"`,
		"the id a second time":     `"id":"ap-20261120-0007"`,
		"the decision in capitals": `"DECISION":"approve"`,
	}
	for name, member := range members {
		for kind, frame := range map[string][]byte{"approve": approve, "deny": deny} {
			_, err := DecodeDecisionFrame(withMember(t, frame, member))
			refused(t, err, record.CodeDecisionInvalid, kind+" with "+name)
		}
	}
	// The same fields inside a sign-off are refused as well.
	in := strings.Replace(string(approve), `"kind":"exec-config"`, `"kind":"exec-config","session":"0123456789abcdef"`, 1)
	if in == string(approve) {
		t.Fatal("the sample has no sign-off to change")
	}
	_, err = DecodeDecisionFrame([]byte(in))
	refused(t, err, record.CodeDecisionInvalid, "a sign-off with a session")
}

// The frame's type has the five fields of the seams note and no other, so
// no code in this build can fill a sixth.
func TestTheDecisionFrameHasExactlyItsFiveFields(t *testing.T) {
	want := map[string]bool{"id": true, "binding_sha256": true, "decision": true, "signed_off": true, "reason": true}
	typ := reflect.TypeOf(DecisionFrame{})
	if typ.NumField() != len(want) {
		t.Fatalf("the frame has %d fields, want %d", typ.NumField(), len(want))
	}
	for i := 0; i < typ.NumField(); i++ {
		name, _, _ := strings.Cut(typ.Field(i).Tag.Get("json"), ",")
		if !want[name] {
			t.Errorf("the frame has a field %q", name)
		}
	}
	// A sign-off is the record's: a kind and a path.
	if so := reflect.TypeOf(record.SignOff{}); so.NumField() != 2 {
		t.Errorf("a sign-off has %d fields, want 2", so.NumField())
	}
}

func TestDecisionFrameContent(t *testing.T) {
	signed := []record.SignOff{{Kind: record.FlagExecConfig, Path: "Makefile"}}
	valid := map[string]DecisionFrame{
		"an approve with a sign-off":  {ID: sampleID, BindingSHA256: sampleDigest, Decision: "approve", SignedOff: signed},
		"an approve with no sign-off": {ID: sampleID, BindingSHA256: sampleDigest, Decision: "approve", SignedOff: []record.SignOff{}},
		"a deny with no reason":       {ID: sampleID, BindingSHA256: sampleDigest, Decision: "deny", SignedOff: []record.SignOff{}},
		"a deny with a reason of 500 bytes": {ID: sampleID, BindingSHA256: sampleDigest, Decision: "deny",
			SignedOff: []record.SignOff{}, Reason: strings.Repeat("r", record.MaxReasonBytes)},
		"a sign-off of a hostile path": {ID: sampleID, BindingSHA256: sampleDigest, Decision: "approve",
			SignedOff: []record.SignOff{{Kind: record.FlagSymlink, Path: "a\xff\x1b[31mb"}}},
	}
	for name, f := range valid {
		out, err := EncodeDecisionFrame(f)
		if err != nil {
			t.Errorf("%s: does not encode: %v", name, err)
			continue
		}
		back, err := DecodeDecisionFrame(out)
		if err != nil || !reflect.DeepEqual(back, f) {
			t.Errorf("%s: decodes to %+v (%v)", name, back, err)
		}
	}

	invalid := map[string]DecisionFrame{
		"an approve with a reason": {ID: sampleID, BindingSHA256: sampleDigest, Decision: "approve", SignedOff: signed, Reason: "because"},
		"a deny with sign-offs":    {ID: sampleID, BindingSHA256: sampleDigest, Decision: "deny", SignedOff: signed},
		"a reason of 501 bytes": {ID: sampleID, BindingSHA256: sampleDigest, Decision: "deny",
			Reason: strings.Repeat("r", record.MaxReasonBytes+1)},
		"a reason of 501 bytes in two-byte characters": {ID: sampleID, BindingSHA256: sampleDigest, Decision: "deny",
			Reason: strings.Repeat("é", 250) + "r"},
		"a reason with a line feed":   {ID: sampleID, BindingSHA256: sampleDigest, Decision: "deny", Reason: "one\ntwo"},
		"a reason that is not UTF-8":  {ID: sampleID, BindingSHA256: sampleDigest, Decision: "deny", Reason: "\xff"},
		"a decision of another word":  {ID: sampleID, BindingSHA256: sampleDigest, Decision: "approved", SignedOff: signed},
		"a decision in capitals":      {ID: sampleID, BindingSHA256: sampleDigest, Decision: "Approve", SignedOff: signed},
		"an empty decision":           {ID: sampleID, BindingSHA256: sampleDigest, SignedOff: signed},
		"a decision of both words":    {ID: sampleID, BindingSHA256: sampleDigest, Decision: "approve deny", SignedOff: signed},
		"an id outside the grammar":   {ID: "ap-../x", BindingSHA256: sampleDigest, Decision: "approve"},
		"a request id":                {ID: "rq-20261120-0007", BindingSHA256: sampleDigest, Decision: "approve"},
		"a digest in capitals":        {ID: sampleID, BindingSHA256: strings.ToUpper(sampleDigest), Decision: "approve"},
		"a short digest":              {ID: sampleID, BindingSHA256: sampleDigest[:63], Decision: "approve"},
		"a sign-off of no known kind": {ID: sampleID, BindingSHA256: sampleDigest, Decision: "approve", SignedOff: []record.SignOff{{Kind: "anything", Path: "x"}}},
		"a sign-off twice":            {ID: sampleID, BindingSHA256: sampleDigest, Decision: "approve", SignedOff: append(signed[:1:1], signed[0])},
	}
	for name, f := range invalid {
		_, err := EncodeDecisionFrame(f)
		refused(t, err, record.CodeDecisionInvalid, "encoding "+name)
	}

	// The same refusals on the bytes a console could send, which need not
	// come from this encoder.
	base := `{"binding_sha256":"` + sampleDigest + `","id":"` + sampleID + `",`
	wire := map[string]string{
		"an approve with a reason":            base + `"decision":"approve","signed_off":[],"reason":"because"}`,
		"an approve with an empty reason":     base + `"decision":"approve","signed_off":[],"reason":""}`,
		"a deny with an empty reason":         base + `"decision":"deny","signed_off":[],"reason":""}`,
		"a deny with sign-offs":               base + `"decision":"deny","signed_off":[{"kind":"exec-config","path":"Makefile"}]}`,
		"a reason of 501 bytes":               base + `"decision":"deny","signed_off":[],"reason":"` + strings.Repeat("r", 501) + `"}`,
		"a reason with an escaped control":    base + `"decision":"deny","signed_off":[],"reason":"a\u001bb"}`,
		"a decision of another word":          base + `"decision":"maybe","signed_off":[]}`,
		"a decision that is true":             base + `"decision":true,"signed_off":[]}`,
		"a decision that is a list":           base + `"decision":["approve"],"signed_off":[]}`,
		"no decision":                         base + `"signed_off":[]}`,
		"no sign-off list":                    base + `"decision":"approve"}`,
		"no sign-off list on a deny":          base + `"decision":"deny"}`,
		"sign-offs that are null":             base + `"decision":"approve","signed_off":null}`,
		"a sign-off with a b64 that is plain": base + `"decision":"approve","signed_off":[{"kind":"symlink","path":{"b64":"TWFrZWZpbGU="}}]}`,
		"a sign-off with no path":             base + `"decision":"approve","signed_off":[{"kind":"symlink"}]}`,
		"no id":                               `{"binding_sha256":"` + sampleDigest + `","decision":"approve","signed_off":[]}`,
		"no digest":                           `{"id":"` + sampleID + `","decision":"approve","signed_off":[]}`,
	}
	for name, in := range wire {
		_, err := DecodeDecisionFrame([]byte(in))
		refused(t, err, record.CodeDecisionInvalid, "decoding "+name)
	}
	if _, err := DecodeDecisionFrame([]byte(base + `"decision":"deny","signed_off":[],"reason":"` + strings.Repeat("r", 500) + `"}`)); err != nil {
		t.Errorf("a deny with a reason of 500 bytes is refused: %v", err)
	}
}

// A record travels as its bytes with their digest. The decoder checks that
// the digest is the bytes' own, and the bytes come out as they went in,
// whatever they are: nothing reads them or writes them again.
func TestARecordTravelsAsBytesWithItsDigest(t *testing.T) {
	for name, data := range map[string][]byte{
		"a record with odd spacing": []byte("{ \"schema\" :\t\"ataniq.approval-pending/1\" ,\"z\":1,\"a\":2 }\n"),
		"bytes that are not JSON":   []byte("\x00\xff not a record \x1b[2J"),
		"a record of the cap":       bytes.Repeat([]byte("r"), record.MaxPendingBytes),
	} {
		r := NewRecord(data)
		if r.SHA256 != record.Digest(data) {
			t.Fatalf("%s: NewRecord gives another digest", name)
		}
		out, err := EncodeShowResult(ShowResult{Record: r, Earlier: &Earlier{Record: r, ReadAt: sampleDay}})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		back, err := DecodeShowResult(out)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(back.Record.Bytes, data) || !bytes.Equal(back.Earlier.Record.Bytes, data) {
			t.Errorf("%s: the bytes changed on the way", name)
		}
		if back.Record.SHA256 != r.SHA256 {
			t.Errorf("%s: the digest changed on the way", name)
		}
	}
}

func TestARecordWhoseDigestIsNotItsBytesIsRefused(t *testing.T) {
	good := NewRecord([]byte("a pending record"))
	other := NewRecord([]byte("a pending record "))
	swapped := Record{Bytes: good.Bytes, SHA256: other.SHA256}

	for name, r := range map[string]ShowResult{
		"the record":         {Record: swapped},
		"the earlier record": {Record: good, Earlier: &Earlier{Record: swapped, ReadAt: sampleDay}},
	} {
		_, err := EncodeShowResult(r)
		refused(t, err, record.CodeBindingMismatch, "encoding "+name)
	}

	out, err := EncodeShowResult(ShowResult{Record: good, Earlier: &Earlier{Record: other, ReadAt: sampleDay}})
	if err != nil {
		t.Fatal(err)
	}
	// One byte of the record changed on the wire: "a" for "b" in base64.
	for name, in := range map[string]string{
		"the record's bytes changed":  strings.Replace(string(out), `"record":{"bytes":"YSBw`, `"record":{"bytes":"YiBw`, 2),
		"the record's digest changed": strings.Replace(string(out), good.SHA256, otherDigest, 1),
		"the two digests swapped":     strings.NewReplacer(good.SHA256, other.SHA256, other.SHA256, good.SHA256).Replace(string(out)),
	} {
		if in == string(out) {
			t.Fatalf("%s: the test changed nothing", name)
		}
		_, err := DecodeShowResult([]byte(in))
		refused(t, err, record.CodeBindingMismatch, name)
	}

	for name, in := range map[string]string{
		"no bytes":                `{"held":false,"record":{"sha256":"` + good.SHA256 + `"}}`,
		"no digest":               `{"held":false,"record":{"bytes":"YQ=="}}`,
		"empty bytes":             `{"held":false,"record":{"bytes":"","sha256":"` + record.Digest(nil) + `"}}`,
		"bytes without padding":   `{"held":false,"record":{"bytes":"YQ","sha256":"` + record.Digest([]byte("a")) + `"}}`,
		"bytes in URL base64":     `{"held":false,"record":{"bytes":"_-8=","sha256":"` + sampleDigest + `"}}`,
		"bytes with a line break": `{"held":false,"record":{"bytes":"YQ==\n","sha256":"` + sampleDigest + `"}}`,
		"bytes as a b64 object":   `{"held":false,"record":{"bytes":{"b64":"YQ=="},"sha256":"` + sampleDigest + `"}}`,
		"bytes as a list":         `{"held":false,"record":{"bytes":[97],"sha256":"` + sampleDigest + `"}}`,
		"the record as a string":  `{"held":false,"record":"YQ=="}`,
		"the record decoded":      `{"held":false,"record":{"schema":"ataniq.approval-pending/1"}}`,
		"a digest in capitals":    `{"held":false,"record":{"bytes":"YQ==","sha256":"` + strings.ToUpper(record.Digest([]byte("a"))) + `"}}`,
		"no held mark":            `{"record":{"bytes":"YQ==","sha256":"` + record.Digest([]byte("a")) + `"}}`,
		"an outcome of no state":  `{"held":false,"outcome":{"line":"x","state":"held"},"record":{"bytes":"YQ==","sha256":"` + record.Digest([]byte("a")) + `"}}`,
		"an earlier with no time": `{"earlier":{"record":{"bytes":"YQ==","sha256":"` + record.Digest([]byte("a")) + `"}},"held":false,"record":{"bytes":"YQ==","sha256":"` + record.Digest([]byte("a")) + `"}}`,
	} {
		_, err := DecodeShowResult([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}

	// A record over the cap of a pending record is refused for its size.
	big := NewRecord(bytes.Repeat([]byte("r"), record.MaxPendingBytes+1))
	_, err = EncodeShowResult(ShowResult{Record: big})
	refused(t, err, record.CodeRecordTooLarge, "a record one byte over the cap")
}

var (
	textType  = reflect.TypeOf(record.Text(""))
	bytesType = reflect.TypeOf([]byte(nil))
)

// The listing carries no record bytes: no type it is made of has a field
// that could hold them.
func TestTheListingCarriesNoRecordBytes(t *testing.T) {
	var walk func(reflect.Type)
	seen := map[reflect.Type]bool{}
	walk = func(typ reflect.Type) {
		if typ == reflect.TypeOf(Record{}) || typ == bytesType || typ == textType {
			t.Errorf("the listing holds a %s", typ)
		}
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice {
			typ = typ.Elem()
		}
		if typ == bytesType.Elem() {
			t.Errorf("the listing holds bytes")
		}
		if typ.Kind() != reflect.Struct || seen[typ] {
			return
		}
		seen[typ] = true
		for i := 0; i < typ.NumField(); i++ {
			name := typ.Field(i).Tag.Get("json")
			if strings.Contains(name, "bytes") || strings.Contains(name, "record") {
				t.Errorf("%s has a field %q", typ.Name(), name)
			}
			walk(typ.Field(i).Type)
		}
	}
	walk(reflect.TypeOf(Listing{}))
	if len(seen) != 5 {
		t.Fatalf("the walk saw %d types, want the listing's five", len(seen))
	}
	for _, in := range []string{
		`{"count":{"requests":0,"would_meet":0},"not_listed":0,"pending":[],"recent":[],"rules":[],"record":{"bytes":"YQ==","sha256":"` + record.Digest([]byte("a")) + `"}}`,
		`{"count":{"requests":0,"would_meet":0},"not_listed":0,"pending":[{"bytes":"YQ==","held":false,"id":"ap-1","pending_expires_at":"2026-11-21T14:06:40Z","project":"project-a","requested_at":"2026-11-20T14:06:40Z","sha256":"` + sampleDigest + `","verb":"branch prune"}],"recent":[],"rules":[]}`,
	} {
		_, err := DecodeListing([]byte(in))
		refused(t, err, record.CodeRecordInvalid, "a listing with record bytes")
	}
}

// The listing stays under one page. The encoder refuses one that would not
// and cuts nothing itself; with recent decisions dropped it fits.
func TestAListingOverOnePageIsRefused(t *testing.T) {
	l := Listing{Pending: []PendingItem{}, Recent: []RecentItem{}, Rules: []Rule{}}
	line := strings.Repeat("a decision and its outcome. ", 36)
	if len(line) > MaxHostLineBytes {
		t.Fatalf("the test's line is %d bytes", len(line))
	}
	for i := 0; i < MaxRecent; i++ {
		l.Recent = append(l.Recent, RecentItem{ID: sampleID, Project: "project-a", Verb: record.VerbBranchPrune,
			State: record.StateApplied, Line: line, At: sampleDay})
	}
	out, err := EncodeListing(l)
	if err != nil {
		t.Fatalf("a listing of %d recent rows is refused: %v", MaxRecent, err)
	}
	if len(out) > MaxPageBytes {
		t.Fatalf("the listing is %d bytes as encoded", len(out))
	}

	// Many pending rows take it over the page.
	for i := 0; len(l.Pending) < 700; i++ {
		l.Pending = append(l.Pending, PendingItem{ID: "ap-" + strings.Repeat("0", 40) + itoa(i), SHA256: sampleDigest,
			Project: "project-a", Verb: record.VerbFeatureFinish, Feature: strings.Repeat("f", 48),
			RequestedAt: sampleDay, PendingExpiresAt: sampleDay})
	}
	_, err = EncodeListing(l)
	refused(t, err, record.CodeRecordTooLarge, "a listing over one page")

	// The caller trims recent decisions, oldest first, until it fits.
	for err != nil && len(l.Recent) > 0 {
		l.Recent = l.Recent[:len(l.Recent)-1]
		out, err = EncodeListing(l)
	}
	if err != nil {
		t.Fatalf("the listing does not fit with no recent row: %v", err)
	}
	if len(out) > MaxPageBytes {
		t.Fatalf("the trimmed listing is %d bytes as encoded", len(out))
	}
	if _, err := DecodeListing(out); err != nil {
		t.Fatalf("the trimmed listing does not decode: %v", err)
	}

	l.Recent = make([]RecentItem, MaxRecent+1)
	for i := range l.Recent {
		l.Recent[i] = RecentItem{ID: sampleID, Project: "project-a", Verb: record.VerbBranchPrune, State: record.StateDenied, At: sampleDay}
	}
	l.Pending = nil
	_, err = EncodeListing(l)
	refused(t, err, record.CodeRecordInvalid, "a listing of 101 recent rows")
}

func itoa(i int) string {
	const digits = "0123456789"
	return string([]byte{digits[i/1000%10], digits[i/100%10], digits[i/10%10], digits[i%10]})
}

func TestListingContent(t *testing.T) {
	change := func(edit func(*Listing)) Listing {
		l := sampleListing()
		edit(&l)
		return l
	}
	for name, l := range map[string]Listing{
		"a feature name outside the grammar": change(func(l *Listing) { l.Pending[0].Feature = "Approve it" }),
		"a feature name of 49 bytes":         change(func(l *Listing) { l.Pending[0].Feature = strings.Repeat("f", 49) }),
		"a feature finish with no name":      change(func(l *Listing) { l.Pending[0].Feature = "" }),
		"a prune with a name":                change(func(l *Listing) { l.Pending[1].Feature = "x" }),
		"a verb outside the list":            change(func(l *Listing) { l.Pending[0].Verb = "feature start" }),
		"an id outside the grammar":          change(func(l *Listing) { l.Pending[0].ID = "ap-\x1b[2J" }),
		"an id twice":                        change(func(l *Listing) { l.Pending[1].ID = l.Pending[0].ID }),
		"a short digest":                     change(func(l *Listing) { l.Pending[0].SHA256 = "abc" }),
		"no project":                         change(func(l *Listing) { l.Pending[0].Project = "" }),
		"a project with a control character": change(func(l *Listing) { l.Pending[0].Project = "a\x1bb" }),
		"a time with an offset":              change(func(l *Listing) { l.Pending[0].RequestedAt = "2026-11-20T14:06:40+00:00" }),
		"a time with a fraction":             change(func(l *Listing) { l.Pending[0].PendingExpiresAt = "2026-11-20T14:06:40.5Z" }),
		"a recent state outside the list":    change(func(l *Listing) { l.Recent[0].State = "held" }),
		"a recent line over the cap":         change(func(l *Listing) { l.Recent[0].Line = strings.Repeat("x", MaxHostLineBytes+1) }),
		"a recent line with a line feed":     change(func(l *Listing) { l.Recent[0].Line = "a\nb" }),
		"a recent time that is none":         change(func(l *Listing) { l.Recent[0].At = "yesterday" }),
		"a rule with no id":                  change(func(l *Listing) { l.Rules[0].ID = "" }),
		"a rule with negative uses":          change(func(l *Listing) { l.Rules[0].Uses = -1 }),
		"a negative count":                   change(func(l *Listing) { l.Count.Requests = -1 }),
		"a negative number left out":         change(func(l *Listing) { l.NotListed = -1 }),
		"more that would meet than asked":    change(func(l *Listing) { l.Count.WouldMeet = l.Count.Requests + 1 }),
		"a looked mark that is no time":      change(func(l *Listing) { l.LookedAt = "0" }),
	} {
		_, err := EncodeListing(l)
		refused(t, err, record.CodeRecordInvalid, name)
	}

	// He never looked: the mark is left out, and an empty one is refused.
	never := change(func(l *Listing) { l.LookedAt = "" })
	out, err := EncodeListing(never)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "looked_at") {
		t.Errorf("a listing with no mark writes one: %s", out)
	}
	_, err = DecodeListing(withMember(t, out, `"looked_at":""`))
	refused(t, err, record.CodeRecordInvalid, "an empty looked mark")
	// A list that is missing is not an empty list.
	for _, key := range []string{"pending", "recent", "rules", "count", "not_listed"} {
		in := `{"count":{"requests":0,"would_meet":0},"not_listed":0,"pending":[],"recent":[],"rules":[]}`
		in = strings.Replace(in, `"`+key+`":`, `"zz":`, 1)
		_, err := DecodeListing([]byte(in))
		refused(t, err, record.CodeRecordInvalid, "a listing without "+key)
	}
}

// owner.decider carries the client and the session: 16 lower-case
// hexadecimal digits.
func TestDeciderParamsCarryClientAndSession(t *testing.T) {
	out, err := EncodeDeciderParams(DeciderParams{Client: "console-a 0.0.1", Session: "0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"client":"console-a 0.0.1","session":"0123456789abcdef"}`; string(out) != want {
		t.Fatalf("encoded %s, want %s", out, want)
	}
	for name, p := range map[string]DeciderParams{
		"a session of 15 digits":     {Client: "console-a", Session: "0123456789abcde"},
		"a session of 17 digits":     {Client: "console-a", Session: "0123456789abcdef0"},
		"a session in capitals":      {Client: "console-a", Session: "0123456789ABCDEF"},
		"a session with a letter g":  {Client: "console-a", Session: "0123456789abcdeg"},
		"a session with a space":     {Client: "console-a", Session: "0123456789abcde "},
		"a session of wide digits":   {Client: "console-a", Session: "０１２３４５"},
		"no session":                 {Client: "console-a"},
		"no client":                  {Session: "0123456789abcdef"},
		"a client over the cap":      {Client: strings.Repeat("c", MaxNameBytes+1), Session: "0123456789abcdef"},
		"a client with an escape":    {Client: "console-a\x1b[2J", Session: "0123456789abcdef"},
		"a client that is not UTF-8": {Client: "console-\xff", Session: "0123456789abcdef"},
	} {
		_, err := EncodeDeciderParams(p)
		refused(t, err, record.CodeRecordInvalid, name)
	}
	for name, in := range map[string]string{
		"no session":            `{"client":"console-a"}`,
		"no client":             `{"session":"0123456789abcdef"}`,
		"a session as a number": `{"client":"console-a","session":1234567890123456}`,
		"a pid":                 `{"client":"console-a","session":"0123456789abcdef","pid":1}`,
		"a tty":                 `{"client":"console-a","session":"0123456789abcdef","tty":"/dev/pts/4"}`,
	} {
		_, err := DecodeDeciderParams([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

func TestDeciderResultSaysWhyNot(t *testing.T) {
	for name, r := range map[string]DeciderResult{
		"the decider":             {Decider: true},
		"not the decider and why": {Reason: "another console is the decider"},
	} {
		out, err := EncodeDeciderResult(r)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if back, err := DecodeDeciderResult(out); err != nil || back != r {
			t.Errorf("%s: decodes to %+v (%v)", name, back, err)
		}
	}
	for name, r := range map[string]DeciderResult{
		"the decider with a reason":   {Decider: true, Reason: "x"},
		"not the decider and no word": {},
		"a reason over the cap":       {Reason: strings.Repeat("x", MaxHostLineBytes+1)},
	} {
		_, err := EncodeDeciderResult(r)
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

func TestDecideResultContent(t *testing.T) {
	for _, s := range []record.State{record.StatePending, record.StateApproved, record.StateDenied, record.StateApplied,
		record.StateNotApplied, record.StateUnknown, record.StateExpired, record.StateVoid, record.StateWithdrawn, record.StateConsumed} {
		r := DecideResult{State: s, Line: "a line"}
		out, err := EncodeDecideResult(r)
		if err != nil {
			t.Errorf("%s: %v", s, err)
			continue
		}
		if back, err := DecodeDecideResult(out); err != nil || back != r {
			t.Errorf("%s: decodes to %+v (%v)", s, back, err)
		}
	}
	for name, r := range map[string]DecideResult{
		"a state outside the list": {State: "held", Line: "x"},
		"no state":                 {Line: "x"},
		"held and approved":        {State: record.StateApproved, Held: true},
		"held and denied":          {State: record.StateDenied, Held: true},
		"a line over the cap":      {State: record.StateDenied, Line: strings.Repeat("x", MaxHostLineBytes+1)},
		"a line with an escape":    {State: record.StateDenied, Line: "\x1b[2J"},
		"a line that is not UTF-8": {State: record.StateDenied, Line: "\xff"},
	} {
		_, err := EncodeDecideResult(r)
		refused(t, err, record.CodeRecordInvalid, name)
	}
	for name, in := range map[string]string{
		"no held mark":        `{"line":"x","state":"denied"}`,
		"held as a number":    `{"held":1,"line":"x","state":"denied"}`,
		"held as a string":    `{"held":"true","line":"x","state":"denied"}`,
		"a state in capitals": `{"held":false,"line":"x","state":"Denied"}`,
	} {
		_, err := DecodeDecideResult([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

func TestPageParamsContent(t *testing.T) {
	for name, p := range map[string]PageParams{
		"a negative page":           {ID: sampleID, ReviewDigest: sampleDigest, Page: -1},
		"a page past the most":      {ID: sampleID, ReviewDigest: sampleDigest, Page: MaxPages},
		"an id outside the grammar": {ID: "../../etc", ReviewDigest: sampleDigest},
		"a digest of 40 digits":     {ID: sampleID, ReviewDigest: sampleDigest[:40]},
		"no digest":                 {ID: sampleID},
	} {
		_, err := EncodePageParams(p)
		refused(t, err, record.CodeRecordInvalid, name)
	}
	base := `{"id":"` + sampleID + `","review_digest":"` + sampleDigest + `",`
	for name, in := range map[string]string{
		"a page as a float":        base + `"page":1.0}`,
		"a page with an exponent":  base + `"page":1e0}`,
		"a page as a string":       base + `"page":"1"}`,
		"a page with a zero first": base + `"page":01}`,
		"a page of minus zero":     base + `"page":-0}`,
		"a page past int64":        base + `"page":9223372036854775808}`,
		"no page":                  `{"id":"` + sampleID + `","review_digest":"` + sampleDigest + `"}`,
	} {
		_, err := DecodePageParams([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}
	if _, err := EncodePageParams(PageParams{ID: sampleID, ReviewDigest: sampleDigest, Page: MaxPages - 1}); err != nil {
		t.Errorf("the last page that can be asked for is refused: %v", err)
	}
}

func TestPageContent(t *testing.T) {
	changePaths := func(edit func(*PathsPage)) PathsPage {
		p := samplePathsPage()
		edit(&p)
		return p
	}
	for name, p := range map[string]PathsPage{
		"a page past the last":        changePaths(func(p *PathsPage) { p.Page = 3 }),
		"a negative page":             changePaths(func(p *PathsPage) { p.Page = -1 }),
		"no pages":                    changePaths(func(p *PathsPage) { p.Page, p.Pages = 0, 0 }),
		"more pages than the most":    changePaths(func(p *PathsPage) { p.Pages = MaxPages + 1 }),
		"an empty page among several": changePaths(func(p *PathsPage) { p.Paths = nil }),
		"an id outside the grammar":   changePaths(func(p *PathsPage) { p.ID = "ap-" }),
		"a short review digest":       changePaths(func(p *PathsPage) { p.ReviewDigest = "abcd" }),
		"a status outside the list":   changePaths(func(p *PathsPage) { p.Paths[0].Status = "R" }),
		"a mode of five digits":       changePaths(func(p *PathsPage) { p.Paths[0].OldMode = "10064" }),
		"a mode with an 8":            changePaths(func(p *PathsPage) { p.Paths[0].NewMode = "100648" }),
		"a negative count of lines":   changePaths(func(p *PathsPage) { p.Paths[0].Added = -1 }),
		"a negative count of bytes":   changePaths(func(p *PathsPage) { p.Paths[1].NewBytes = -1 }),
	} {
		_, err := EncodePathsPage(p)
		refused(t, err, record.CodeRecordInvalid, "paths: "+name)
	}
	if _, err := EncodePathsPage(PathsPage{ID: sampleID, ReviewDigest: sampleDigest, Pages: 1}); err != nil {
		t.Errorf("the one page of an empty path list is refused: %v", err)
	}

	changePatch := func(edit func(*PatchPage)) PatchPage {
		p := samplePatchPage()
		edit(&p)
		return p
	}
	for name, p := range map[string]PatchPage{
		"a page past the last":             changePatch(func(p *PatchPage) { p.Page = 2 }),
		"no pages":                         changePatch(func(p *PatchPage) { p.Pages = 0 }),
		"an empty page among several":      changePatch(func(p *PatchPage) { p.Lines = []PatchLine{} }),
		"an empty piece that continues":    changePatch(func(p *PatchPage) { p.Lines[1].Cont = true }),
		"a last page that ends continuing": changePatch(func(p *PatchPage) { p.Page = 1 }),
		"a short review digest":            changePatch(func(p *PatchPage) { p.ReviewDigest = "" }),
		"an id outside the grammar":        changePatch(func(p *PatchPage) { p.ID = "rq-1" }),
		"a line in too many pieces": changePatch(func(p *PatchPage) {
			p.Lines = nil
			for i := 0; i < MaxLinePieces; i++ {
				p.Lines = append(p.Lines, PatchLine{Text: "x", Cont: true})
			}
		}),
	} {
		_, err := EncodePatchPage(p)
		refused(t, err, record.CodeRecordInvalid, "patch: "+name)
	}
	if _, err := EncodePatchPage(PatchPage{ID: sampleID, ReviewDigest: sampleDigest, Pages: 1}); err != nil {
		t.Errorf("the one page of an empty patch is refused: %v", err)
	}
	// A line may be in as many pieces as the pager reads, and no more.
	most := PatchPage{ID: sampleID, ReviewDigest: sampleDigest, Pages: 1}
	for i := 0; i < MaxLinePieces-1; i++ {
		most.Lines = append(most.Lines, PatchLine{Text: "x", Cont: true})
	}
	most.Lines = append(most.Lines, PatchLine{Text: ""})
	if _, err := EncodePatchPage(most); err != nil {
		t.Errorf("a line in %d pieces is refused: %v", MaxLinePieces, err)
	}

	base := `{"id":"` + sampleID + `","page":0,"pages":1,"review_digest":"` + sampleDigest + `","lines":`
	for name, in := range map[string]string{
		"a line with no cont":         base + `[{"text":"x"}]}`,
		"a line with no text":         base + `[{"cont":false}]}`,
		"a line that is a string":     base + `["x"]}`,
		"a cont that is a number":     base + `[{"cont":0,"text":"x"}]}`,
		"a text with a control":       base + `[{"cont":false,"text":"a\u001bb"}]}`,
		"a b64 that is plain":         base + `[{"cont":false,"text":{"b64":"eA=="}}]}`,
		"a b64 with a second key":     base + `[{"cont":false,"text":{"b64":"/w==","b":1}}]}`,
		"a b64 without padding":       base + `[{"cont":false,"text":{"b64":"/w"}}]}`,
		"an empty b64 that continues": base + `[{"cont":true,"text":{"b64":""}},{"cont":false,"text":"x"}]}`,
		"lines that are an object":    base + `{}}`,
		"an escaped line":             base + `[{"cont":false,"text":"x","escaped":true}]}`,
	} {
		_, err := DecodePatchPage([]byte(in))
		refused(t, err, record.CodeRecordInvalid, "patch: "+name)
	}
	// Raw bytes come back as they were, control bytes and all.
	p, err := DecodePatchPage([]byte(base + `[{"cont":false,"text":{"b64":"G1sySv8="}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(p.Lines[0].Text); got != "\x1b[2J\xff" {
		t.Fatalf("the line is %q", got)
	}
}

func TestLimitContent(t *testing.T) {
	for name, p := range map[string]LimitSetParams{
		"no project":      {To: 12},
		"a limit of zero": {Project: "project-a"},
		"a limit under 0": {Project: "project-a", To: -3, Typed: true},
	} {
		_, err := EncodeLimitSetParams(p)
		refused(t, err, record.CodeRecordInvalid, name)
	}
	_, err := EncodeLimitParams(LimitParams{})
	refused(t, err, record.CodeRecordInvalid, "a limit read with no project")
	base := `{"project":"project-a",`
	for name, in := range map[string]string{
		"no typed mark":    base + `"to":12}`,
		"no number":        base + `"typed":true}`,
		"a float":          base + `"to":12.0,"typed":true}`,
		"a typed number":   base + `"to":"12","typed":true}`,
		"typed as a word":  base + `"to":12,"typed":"yes"}`,
		"the limit itself": base + `"to":12,"typed":true,"limit":40}`,
	} {
		_, err := DecodeLimitSetParams([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}

	change := func(edit func(*LimitView)) LimitView {
		v := sampleLimitView()
		edit(&v)
		return v
	}
	for name, v := range map[string]LimitView{
		"no project":            change(func(v *LimitView) { v.Project = "" }),
		"a limit of zero":       change(func(v *LimitView) { v.Limit = 0 }),
		"a negative suggestion": change(func(v *LimitView) { v.Suggestion = -1 }),
		"negative uses":         change(func(v *LimitView) { v.Days[0].Uses = -1 }),
		"a date with a time":    change(func(v *LimitView) { v.Days[0].Date = "2026-11-19T00:00:00Z" }),
		"a date of no day":      change(func(v *LimitView) { v.Days[0].Date = "2026-02-30" }),
		"a short date":          change(func(v *LimitView) { v.Days[0].Date = "2026-1-9" }),
		"too many days": change(func(v *LimitView) {
			v.Days = nil
			for i := 0; i <= MaxLimitDays; i++ {
				v.Days = append(v.Days, LimitDay{Date: "2026-11-19", Uses: 1})
			}
		}),
	} {
		_, err := EncodeLimitView(v)
		refused(t, err, record.CodeRecordInvalid, name)
	}
	full := change(func(v *LimitView) {
		v.Days = nil
		for i := 0; i < MaxLimitDays; i++ {
			v.Days = append(v.Days, LimitDay{Date: "2026-11-19", Uses: 40, ReachedLimit: true, Alert: true})
		}
	})
	if _, err := EncodeLimitView(full); err != nil {
		t.Errorf("a view of %d days is refused: %v", MaxLimitDays, err)
	}
}

// No refusal repeats what the payload brought: a detail names a field by
// its place in the form.
func TestADetailHoldsNoByteOfThePayload(t *testing.T) {
	const marker = "zq-marker-zq"
	for name, in := range map[string]string{
		"an unknown key":   `{"` + marker + `":1}`,
		"a wrong decision": `{"binding_sha256":"` + sampleDigest + `","decision":"` + marker + `","id":"` + sampleID + `","signed_off":[]}`,
		"a wrong id":       `{"binding_sha256":"` + sampleDigest + `","decision":"deny","id":"` + marker + `","signed_off":[]}`,
		"a wrong kind":     `{"binding_sha256":"` + sampleDigest + `","decision":"approve","id":"` + sampleID + `","signed_off":[{"kind":"` + marker + `","path":"x"}]}`,
		"an unknown word":  marker,
	} {
		_, err := DecodeDecisionFrame([]byte(in))
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), marker) || strings.Contains(err.Error(), "zq") {
			t.Errorf("%s: the refusal repeats the payload: %v", name, err)
		}
	}
}

// The checks below were each found to survive their removal.

// A record's bytes have one base64 form: padding bits that are not zero
// are refused, though the digest of the bytes they decode to is right.
func TestRecordBytesHaveOneBase64Form(t *testing.T) {
	digest := record.Digest([]byte("a"))
	ok := `{"held":false,"record":{"bytes":"YQ==","sha256":"` + digest + `"}}`
	if _, err := DecodeShowResult([]byte(ok)); err != nil {
		t.Fatalf("the canonical form is refused: %v", err)
	}
	for name, form := range map[string]string{"loose padding bits": "YR==", "no padding": "YQ", "a url alphabet": "Y-=="} {
		in := `{"held":false,"record":{"bytes":"` + form + `","sha256":"` + digest + `"}}`
		_, err := DecodeShowResult([]byte(in))
		refused(t, err, record.CodeRecordInvalid, name)
	}
}

// A string that is not valid UTF-8 is refused wherever it stands, also in a
// field the form names, where only the check of the whole payload sees it.
func TestInvalidUTF8InAKnownFieldIsRefused(t *testing.T) {
	for name, c := range map[string]struct {
		in     string
		decode func([]byte) error
	}{
		"a pending id": {"{\"id\":\"ap-\xff\",\"page\":0,\"review_digest\":\"" + sampleDigest + "\"}",
			func(b []byte) error { _, err := DecodePageParams(b); return err }},
		"a project": {"{\"project\":\"project-\xff\"}",
			func(b []byte) error { _, err := DecodeLimitParams(b); return err }},
		"a line of the patch": {"{\"id\":\"" + sampleID + "\",\"lines\":[{\"cont\":false,\"text\":\"a\xffb\"}],\"page\":0,\"pages\":1,\"review_digest\":\"" + sampleDigest + "\"}",
			func(b []byte) error { _, err := DecodePatchPage(b); return err }},
	} {
		err := c.decode([]byte(c.in))
		refused(t, err, record.CodeRecordInvalid, name)
		if err != nil && !strings.Contains(err.Error(), "not valid UTF-8") {
			t.Errorf("%s: the refusal is not the UTF-8 one: %v", name, err)
		}
	}
}

// The payload reader nests 8 levels and no more. No payload form is that
// deep, so the reader is tried with a type that is.
func TestThePayloadReaderNestsEightLevels(t *testing.T) {
	type deep7 struct {
		L [][][][][][][]string `json:"l"`
	}
	type deep8 struct {
		L [][][][][][][][]string `json:"l"`
	}
	// The object is level 0 and each list one level more.
	if err := reader.Unmarshal([]byte(`{"l":[[[[[[["a"]]]]]]]}`), "deep", new(deep7)); err != nil {
		t.Errorf("seven nested lists are refused: %v", err)
	}
	err := reader.Unmarshal([]byte(`{"l":[[[[[[[["a"]]]]]]]]}`), "deep", new(deep8))
	if err == nil || !strings.Contains(err.Error(), "the payload nests too deep") {
		t.Errorf("eight nested lists: %v, want the payload nests too deep", err)
	}
}

// The number of pending rows left out of a listing travels in a field of
// its own, which is always written, so that a console does not have to guess
// it from a rule row.
func TestTheListingSaysHowManyPendingRowsWereLeftOut(t *testing.T) {
	l := sampleListing()
	out, err := EncodeListing(l)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"not_listed":2`) {
		t.Errorf("the listing does not carry the number left out: %s", out)
	}
	got, err := DecodeListing(out)
	if err != nil || got.NotListed != 2 {
		t.Fatalf("decoded %d, %v", got.NotListed, err)
	}
	l.NotListed = 0
	out, err = EncodeListing(l)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"not_listed":0`) {
		t.Errorf("a zero is not written: %s", out)
	}
	// A string, a float and a null are not a number of rows.
	for _, in := range []string{`"2"`, `2.5`, `null`, `true`} {
		_, err := DecodeListing(bytes.Replace(out, []byte(`"not_listed":0`), []byte(`"not_listed":`+in), 1))
		refused(t, err, record.CodeRecordInvalid, "not_listed "+in)
	}
}
