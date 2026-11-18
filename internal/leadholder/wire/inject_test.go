package wire_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/leadholder/wire"
)

// goldenOldInject is the control request of an inject with no message, as the
// package encoded it before the request had a message id and a text.
const goldenOldInject = `{"op":"inject","epoch":7,"command_id":"inj-1"}`

func TestInjectOldFormStillDecodes(t *testing.T) {
	r, err := wire.DecodeRequest([]byte(goldenOldInject))
	if err != nil {
		t.Fatal(err)
	}
	if r != (wire.Request{Op: wire.OpInject, Epoch: 7, CommandID: "inj-1"}) {
		t.Fatalf("got %+v", r)
	}
	p, err := wire.EncodeRequest(r)
	if err != nil || string(p) != goldenOldInject {
		t.Fatalf("encoding: %q %v", p, err)
	}
	// With no command id either.
	if r, err = wire.DecodeRequest([]byte(`{"op":"inject","epoch":1}`)); err != nil || r.MessageID != "" || r.Text != "" {
		t.Fatalf("bare: %+v %v", r, err)
	}
	// The same form with the two fields present and empty is the old form.
	if r, err = wire.DecodeRequest([]byte(`{"op":"inject","epoch":1,"message_id":"","text":""}`)); err != nil || r.MessageID != "" || r.Text != "" {
		t.Fatalf("empty fields: %+v %v", r, err)
	}
}

func TestInjectRequestRoundTrip(t *testing.T) {
	for name, text := range map[string]string{
		"one line":        "[ataniq-msg id=m1 from=a] hello",
		"lines":           "[ataniq-msg id=m1 from=a]\nsecond line\n\nfourth\n",
		"non-ASCII":       "h\u00e9llo \u4e16\u754c \U0001f600",
		"json specials":   `a "quoted" \ back <tag> & more`,
		"one byte":        "x",
		"the longest":     strings.Repeat("a", wire.MaxInjectText),
		"longest, wide":   strings.Repeat("\u00e9", wire.MaxInjectText/2),
		"only a newline":  "\n",
		"a replacement":   "a\ufffdb",
		"space and signs": " !~ ",
	} {
		t.Run(name, func(t *testing.T) {
			in := wire.Request{Op: wire.OpInject, Epoch: 12, CommandID: "c-1", MessageID: "m1", Text: text}
			p, err := wire.EncodeRequest(in)
			if err != nil {
				t.Fatal(err)
			}
			out, err := wire.DecodeRequest(p)
			if err != nil || out != in {
				t.Fatalf("got %+v, %v", out, err)
			}
		})
	}
}

func TestInjectMessageIDGrammar(t *testing.T) {
	good := []string{"a", "0", "m1", "a.b", "a_b", "a-b", "a0._-", "0a", strings.Repeat("a", 64), "a" + strings.Repeat("-", 63)}
	for _, id := range good {
		if err := wire.CheckMessageID(id); err != nil {
			t.Errorf("%q: %v", id, err)
		}
		if _, err := wire.EncodeRequest(wire.Request{Op: wire.OpInject, Epoch: 1, MessageID: id, Text: "x"}); err != nil {
			t.Errorf("%q encode: %v", id, err)
		}
	}
	bad := []string{
		"", strings.Repeat("a", 65), ".a", "-a", "_a", "A", "aB", "a b", "a/b", "a:b", "a\n", "a\x00", "\u00e9", "a\u00e9", "a\u200b", "a,b", "a+b",
	}
	for _, id := range bad {
		if err := wire.CheckMessageID(id); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%q: %v", id, err)
		}
		if id == "" {
			continue // an empty id with a text is the "only together" rule, tested below
		}
		if _, err := wire.EncodeRequest(wire.Request{Op: wire.OpInject, Epoch: 1, MessageID: id, Text: "x"}); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%q encode: %v", id, err)
		}
		p := fmt.Sprintf(`{"op":"inject","epoch":1,"message_id":%q,"text":"x"}`, id)
		if _, err := wire.DecodeRequest([]byte(p)); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%q decode: %v", id, err)
		}
	}
}

func TestInjectTextRules(t *testing.T) {
	good := []string{"x", "a\nb", "\n", "caf\u00e9", "\u4e16", "\U0001f600", " ", "~", strings.Repeat("a", wire.MaxInjectText)}
	for _, s := range good {
		if err := wire.CheckInjectText(s); err != nil {
			t.Errorf("%.20q: %v", s, err)
		}
	}
	bad := map[string]string{
		"empty":                "",
		"over the cap":         strings.Repeat("a", wire.MaxInjectText+1),
		"over the cap, wide":   strings.Repeat("\u00e9", wire.MaxInjectText/2+1),
		"a tab":                "a\tb",
		"a carriage return":    "a\rb",
		"a crlf":               "a\r\nb",
		"NUL":                  "a\x00b",
		"escape":               "a\x1bb",
		"DEL":                  "a\x7fb",
		"a C1 control":         "a\u0085b",
		"a no-break space":     "a\u00a0b",
		"an ideographic space": "a\u3000b",
		"a soft hyphen":        "a\u00adb",
		"zero width space":     "a\u200bb",
		"a bidi control":       "a\u202eb",
		"the byte order mark":  "\ufeffa",
		"a line separator":     "a\u2028b",
		"a paragraph sep.":     "a\u2029b",
		"a private use char":   "a\ue000b",
		"an unassigned char":   "a\U000e0080b",
		"an invalid byte":      "a\xffb",
		"a truncated rune":     "a\xe4\xb8",
		"a surrogate half":     "a\xed\xa0\x80b",
	}
	for name, s := range bad {
		if err := wire.CheckInjectText(s); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := wire.EncodeRequest(wire.Request{Op: wire.OpInject, Epoch: 1, MessageID: "m1", Text: s}); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s encode: %v", name, err)
		}
	}
	// An error never repeats the text.
	if err := wire.CheckInjectText("secret\x00words"); err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("error %v", err)
	}
}

func TestInjectTextOnTheWire(t *testing.T) {
	// A decoder holds the same rules: JSON escapes do not get around them.
	for name, body := range map[string]string{
		"a tab":                `"a\tb"`,
		"escaped NUL":          `"a\u0000b"`,
		"escaped line sep.":    `"a\u2028b"`,
		"escaped no-break":     `"a\u00a0b"`,
		"a lone surrogate":     `"a\ud800b"`,
		"a raw newline":        "\"a\nb\"",
		"an over-long text":    `"` + strings.Repeat("a", wire.MaxInjectText+1) + `"`,
		"the empty text alone": `""`,
		"the null text":        `null`,
		"a number":             `5`,
		"an array of the text": `["a"]`,
	} {
		p := `{"op":"inject","epoch":1,"message_id":"m1","text":` + body + `}`
		if name == "the empty text alone" || name == "the null text" {
			// An empty text beside a message id breaks "only together".
			if _, err := wire.DecodeRequest([]byte(p)); !errors.Is(err, wire.ErrInvalid) {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		if _, err := wire.DecodeRequest([]byte(p)); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An escaped newline and an escaped printable character are fine.
	r, err := wire.DecodeRequest([]byte(`{"op":"inject","epoch":1,"message_id":"m1","text":"a\nbA"}`))
	if err != nil || r.Text != "a\nbA" {
		t.Fatalf("%+v %v", r, err)
	}
	// Field names are exact.
	for _, p := range []string{
		`{"op":"inject","epoch":1,"Message_id":"m1","text":"a"}`,
		`{"op":"inject","epoch":1,"message_id":"m1","Text":"a"}`,
		`{"op":"inject","epoch":1,"message_id":"m1","text":"a","text":"a"}`,
		`{"op":"inject","epoch":1,"message_id":"m1","message_id":"m1","text":"a"}`,
		`{"op":"inject","epoch":1,"messageid":"m1","text":"a"}`,
	} {
		if _, err := wire.DecodeRequest([]byte(p)); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestInjectRequestOnlyTogether(t *testing.T) {
	for name, r := range map[string]wire.Request{
		"an id without a text": {Op: wire.OpInject, Epoch: 1, MessageID: "m1"},
		"a text without an id": {Op: wire.OpInject, Epoch: 1, Text: "x"},
	} {
		if _, err := wire.EncodeRequest(r); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s encode: %v", name, err)
		}
	}
	for _, p := range []string{
		`{"op":"inject","epoch":1,"message_id":"m1"}`,
		`{"op":"inject","epoch":1,"text":"x"}`,
		`{"op":"inject","epoch":1,"message_id":"m1","text":""}`,
		`{"op":"inject","epoch":1,"message_id":"","text":"x"}`,
		`{"op":"inject","epoch":1,"message_id":"m1","text":null}`,
	} {
		if _, err := wire.DecodeRequest([]byte(p)); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: %v", p, err)
		}
	}
}

func TestInjectFieldsOnOtherOperationsRefused(t *testing.T) {
	for _, op := range []wire.Operation{wire.OpStatus, wire.OpStop, wire.OpReleaseWriter, wire.OpTicket} {
		base := wire.Request{Op: op, Epoch: 1}
		if op == wire.OpTicket {
			base.Role, base.Ticket = wire.RoleWriter, ticket64
		}
		for name, mod := range map[string]func(*wire.Request){
			"a text":              func(r *wire.Request) { r.Text = "x" },
			"a message id":        func(r *wire.Request) { r.MessageID = "m1" },
			"both":                func(r *wire.Request) { r.MessageID, r.Text = "m1", "x" },
			"a malformed id only": func(r *wire.Request) { r.MessageID = "BAD" },
		} {
			r := base
			mod(&r)
			if _, err := wire.EncodeRequest(r); !errors.Is(err, wire.ErrInvalid) {
				t.Errorf("%s with %s: encode: %v", op, name, err)
			}
		}
		// And on the wire, in the exact bytes of a valid request of that op.
		p, err := wire.EncodeRequest(base)
		if err != nil {
			t.Fatal(err)
		}
		for _, extra := range []string{`,"text":"x"`, `,"message_id":"m1"`, `,"message_id":"m1","text":"x"`} {
			q := append(append([]byte{}, p[:len(p)-1]...), []byte(extra+"}")...)
			if _, err := wire.DecodeRequest(q); !errors.Is(err, wire.ErrInvalid) {
				t.Errorf("%s decode %s: %v", op, q, err)
			}
		}
	}
}

func TestInjectRequestKeepsEarlierFieldRules(t *testing.T) {
	for name, r := range map[string]wire.Request{
		"a role":           {Op: wire.OpInject, Epoch: 1, Role: wire.RoleWriter, MessageID: "m1", Text: "x"},
		"a ticket":         {Op: wire.OpInject, Epoch: 1, Ticket: ticket64, MessageID: "m1", Text: "x"},
		"epoch 0":          {Op: wire.OpInject, MessageID: "m1", Text: "x"},
		"a bad command id": {Op: wire.OpInject, Epoch: 1, CommandID: "a b", MessageID: "m1", Text: "x"},
	} {
		if _, err := wire.EncodeRequest(r); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// The largest request fits the control payload with room to spare.
	r := wire.Request{Op: wire.OpInject, Epoch: 1<<64 - 1, CommandID: strings.Repeat("c", wire.MaxCommandID), MessageID: strings.Repeat("m", wire.MaxMessageID), Text: strings.Repeat("\n", wire.MaxInjectText)}
	p, err := wire.EncodeRequest(r)
	if err != nil || len(p) > wire.MaxControlPayload {
		t.Fatalf("%d %v", len(p), err)
	}
}

// The three sets and the valid rows of the result, as the seams note gives
// them. This table is the test's own copy; the package's rules are checked
// against it over the whole product.
var (
	allOutcomes = []wire.InjectOutcome{"not-now", "refused", "submitted", "uncertain"}
	allReasons  = []wire.InjectReason{"", "not-ready", "blocked", "untested", "text", "not-submitted", "died"}
	allWritten  = []wire.InjectWritten{"nothing", "partial", "whole", "unknown"}
)

var validRows = [][3]string{
	{"not-now", "not-ready", "nothing"},
	{"not-now", "blocked", "nothing"},
	{"refused", "untested", "nothing"},
	{"refused", "text", "nothing"},
	{"uncertain", "not-submitted", "partial"},
	{"submitted", "", "whole"},
	{"uncertain", "died", "nothing"},
	{"uncertain", "died", "partial"},
	{"uncertain", "died", "whole"},
	{"uncertain", "died", "unknown"},
}

func isValidRow(o, r, w string) bool {
	for _, v := range validRows {
		if v == [3]string{o, r, w} {
			return true
		}
	}
	return false
}

func TestInjectResultWholeProduct(t *testing.T) {
	valid := 0
	for _, o := range allOutcomes {
		for _, r := range allReasons {
			for _, w := range allWritten {
				res := wire.InjectResult{Outcome: o, Reason: r, Written: w}
				want := isValidRow(string(o), string(r), string(w))
				name := fmt.Sprintf("%s/%s/%s", o, r, w)
				verr := res.Validate()
				_, eerr := wire.EncodeResponse(wire.Response{OK: true, Inject: &res})
				if want {
					valid++
					if verr != nil || eerr != nil {
						t.Errorf("%s: validate %v, encode %v", name, verr, eerr)
						continue
					}
					p, _ := wire.EncodeResponse(wire.Response{OK: true, Inject: &res})
					got, err := wire.DecodeResponse(p)
					if err != nil || got.Inject == nil || *got.Inject != res || !got.OK {
						t.Errorf("%s: round trip %+v %v", name, got, err)
					}
					continue
				}
				if !errors.Is(verr, wire.ErrInvalid) || !errors.Is(eerr, wire.ErrInvalid) {
					t.Errorf("%s: validate %v, encode %v", name, verr, eerr)
				}
				// The bytes an encoder never writes are refused on the way in as well.
				body := fmt.Sprintf(`{"ok":true,"inject":{"outcome":%q,"reason":%q,"written":%q}}`, o, r, w)
				if _, err := wire.DecodeResponse([]byte(body)); !errors.Is(err, wire.ErrInvalid) {
					t.Errorf("%s: decode %v", name, err)
				}
				// Also with the reason left out when it is empty.
				if r == "" {
					body = fmt.Sprintf(`{"ok":true,"inject":{"outcome":%q,"written":%q}}`, o, w)
					if _, err := wire.DecodeResponse([]byte(body)); !errors.Is(err, wire.ErrInvalid) {
						t.Errorf("%s: decode without reason %v", name, err)
					}
				}
			}
		}
	}
	if valid != len(validRows) {
		t.Fatalf("%d valid combinations seen, want %d", valid, len(validRows))
	}
	if n := len(allOutcomes) * len(allReasons) * len(allWritten); n != 112 {
		t.Fatalf("product of %d", n)
	}
}

func TestInjectResultSubmittedHasNoReasonOnTheWire(t *testing.T) {
	p, err := wire.EncodeResponse(wire.Response{OK: true, Inject: &wire.InjectResult{Outcome: wire.InjectOutcomeSubmitted, Written: wire.InjectWrittenWhole}})
	if err != nil || string(p) != `{"ok":true,"inject":{"outcome":"submitted","written":"whole"}}` {
		t.Fatalf("%s %v", p, err)
	}
	p, err = wire.EncodeResponse(wire.Response{OK: true, Inject: &wire.InjectResult{Outcome: wire.InjectOutcomeNotNow, Reason: wire.InjectReasonBlocked, Written: wire.InjectWrittenNothing, Detail: "a | b"}})
	if err != nil || string(p) != `{"ok":true,"inject":{"outcome":"not-now","reason":"blocked","written":"nothing","detail":"a | b"}}` {
		t.Fatalf("%s %v", p, err)
	}
}

func TestInjectResultConstantsAreTheSets(t *testing.T) {
	// The exported constants are exactly the members of the closed sets.
	outcomes := []wire.InjectOutcome{wire.InjectOutcomeNotNow, wire.InjectOutcomeRefused, wire.InjectOutcomeSubmitted, wire.InjectOutcomeUncertain}
	reasons := []wire.InjectReason{wire.InjectReasonNone, wire.InjectReasonNotReady, wire.InjectReasonBlocked, wire.InjectReasonUntested, wire.InjectReasonText, wire.InjectReasonNotSubmitted, wire.InjectReasonDied}
	written := []wire.InjectWritten{wire.InjectWrittenNothing, wire.InjectWrittenPartial, wire.InjectWrittenWhole, wire.InjectWrittenUnknown}
	if fmt.Sprint(outcomes) != fmt.Sprint(allOutcomes) || fmt.Sprint(reasons) != fmt.Sprint(allReasons) || fmt.Sprint(written) != fmt.Sprint(allWritten) {
		t.Fatalf("%v %v %v", outcomes, reasons, written)
	}
}

func TestInjectResultUnknownValues(t *testing.T) {
	ok := wire.InjectResult{Outcome: "uncertain", Reason: "died", Written: "unknown"}
	for name, mod := range map[string]func(*wire.InjectResult){
		"an unknown outcome":    func(r *wire.InjectResult) { r.Outcome = "delivered" },
		"the empty outcome":     func(r *wire.InjectResult) { r.Outcome = "" },
		"an outcome by case":    func(r *wire.InjectResult) { r.Outcome = "Uncertain" },
		"an outcome with space": func(r *wire.InjectResult) { r.Outcome = "uncertain " },
		"an unknown reason":     func(r *wire.InjectResult) { r.Reason = "timeout" },
		"a reason by case":      func(r *wire.InjectResult) { r.Reason = "Died" },
		"an unknown written":    func(r *wire.InjectResult) { r.Written = "all" },
		"the empty written":     func(r *wire.InjectResult) { r.Written = "" },
		"a written by case":     func(r *wire.InjectResult) { r.Written = "Unknown" },
	} {
		res := ok
		mod(&res)
		if err := res.Validate(); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: validate %v", name, err)
		}
		if _, err := wire.EncodeResponse(wire.Response{OK: true, Inject: &res}); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: encode %v", name, err)
		}
		body := fmt.Sprintf(`{"ok":true,"inject":{"outcome":%q,"reason":%q,"written":%q}}`, res.Outcome, res.Reason, res.Written)
		if _, err := wire.DecodeResponse([]byte(body)); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: decode %v", name, err)
		}
	}
}

func TestInjectResultRequiredFields(t *testing.T) {
	for _, p := range []string{
		`{"ok":true,"inject":{}}`,
		`{"ok":true,"inject":{"reason":"died","written":"unknown"}}`,
		`{"ok":true,"inject":{"outcome":"uncertain","reason":"died"}}`,
		`{"ok":true,"inject":{"outcome":null,"reason":"died","written":"unknown"}}`,
		`{"ok":true,"inject":{"outcome":"uncertain","reason":"died","written":null}}`,
		`{"ok":true,"inject":{"outcome":"uncertain","reason":"died","written":"unknown","extra":1}}`,
		`{"ok":true,"inject":{"Outcome":"uncertain","reason":"died","written":"unknown"}}`,
		`{"ok":true,"inject":{"outcome":"uncertain","outcome":"uncertain","reason":"died","written":"unknown"}}`,
		`{"ok":true,"inject":[]}`,
		`{"ok":true,"inject":"uncertain"}`,
	} {
		if _, err := wire.DecodeResponse([]byte(p)); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: %v", p, err)
		}
	}
	// A null inject is the same as none.
	r, err := wire.DecodeResponse([]byte(`{"ok":true,"inject":null}`))
	if err != nil || r.Inject != nil || !r.OK {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestInjectDetail(t *testing.T) {
	base := wire.InjectResult{Outcome: wire.InjectOutcomeUncertain, Reason: wire.InjectReasonDied, Written: wire.InjectWrittenUnknown}
	// Every printable ASCII byte is allowed, up to the cap.
	var all []byte
	for c := byte(0x20); c <= 0x7e; c++ {
		all = append(all, c)
	}
	for _, d := range []string{"", "x", string(all), strings.Repeat("a", wire.MaxInjectDetail), strings.Repeat(`"\<&`, wire.MaxInjectDetail/4)} {
		res := base
		res.Detail = d
		p, err := wire.EncodeResponse(wire.Response{OK: true, Inject: &res})
		if err != nil {
			t.Errorf("%.20q: %v", d, err)
			continue
		}
		got, err := wire.DecodeResponse(p)
		if err != nil || got.Inject == nil || got.Inject.Detail != d {
			t.Errorf("%.20q: %+v %v", d, got, err)
		}
		if err := wire.CheckInjectDetail(d); err != nil {
			t.Errorf("%.20q: %v", d, err)
		}
	}
	bad := map[string]string{
		"over the cap":     strings.Repeat("a", wire.MaxInjectDetail+1),
		"a tab":            "a\tb",
		"a newline":        "a\nb",
		"NUL":              "a\x00b",
		"escape":           "a\x1bb",
		"DEL":              "a\x7fb",
		"a non-ASCII char": "caf\u00e9",
		"a high byte":      "a\xffb",
		"a wide character": "\u4e16",
		"a line separator": "a\u2028b",
	}
	for name, d := range bad {
		res := base
		res.Detail = d
		if err := res.Validate(); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: validate %v", name, err)
		}
		if err := wire.CheckInjectDetail(d); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: check %v", name, err)
		}
		if _, err := wire.EncodeResponse(wire.Response{OK: true, Inject: &res}); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: encode %v", name, err)
		}
	}
	// The same on the way in, with JSON escapes and with raw bytes.
	for name, body := range map[string]string{
		"escaped tab":       `"a\tb"`,
		"escaped DEL":       `"a\u007fb"`,
		"escaped non-ASCII": `"caf\u00e9"`,
		"raw non-ASCII":     "\"caf\u00e9\"",
		"over the cap":      `"` + strings.Repeat("a", wire.MaxInjectDetail+1) + `"`,
	} {
		p := `{"ok":true,"inject":{"outcome":"uncertain","reason":"died","written":"unknown","detail":` + body + `}}`
		if _, err := wire.DecodeResponse([]byte(p)); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: decode %v", name, err)
		}
	}
	// Detail never makes a valid combination out of an invalid one.
	res := wire.InjectResult{Outcome: "submitted", Reason: "died", Written: "whole", Detail: "fine"}
	if err := res.Validate(); !errors.Is(err, wire.ErrInvalid) {
		t.Errorf("combination with a detail: %v", err)
	}
}

func TestInjectResponseShape(t *testing.T) {
	res := &wire.InjectResult{Outcome: wire.InjectOutcomeSubmitted, Written: wire.InjectWrittenWhole}
	// Only on an ok response, never with a status.
	for name, r := range map[string]wire.Response{
		"with a status":               {OK: true, Status: &sampleStatus, Inject: res},
		"on a refusal":                {Code: wire.CodeDisabled, Text: "off", Inject: res},
		"on a refusal without a text": {Code: wire.CodeBadRequest, Inject: res},
		"with a code":                 {OK: true, Code: wire.CodeBusy, Inject: res},
		"with a text":                 {OK: true, Text: "x", Inject: res},
		"with an invalid result":      {OK: true, Inject: &wire.InjectResult{}},
	} {
		if _, err := wire.EncodeResponse(r); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: encode %v", name, err)
		}
	}
	for name, p := range map[string]string{
		"with a status":  `{"ok":true,"status":` + mustStatusJSON(t) + `,"inject":{"outcome":"submitted","written":"whole"}}`,
		"on a refusal":   `{"ok":false,"code":"disabled","inject":{"outcome":"submitted","written":"whole"}}`,
		"without ok":     `{"code":"busy","inject":{"outcome":"refused","reason":"text","written":"nothing"}}`,
		"with a code":    `{"ok":true,"code":"busy","inject":{"outcome":"submitted","written":"whole"}}`,
		"with a text":    `{"ok":true,"text":"x","inject":{"outcome":"submitted","written":"whole"}}`,
		"empty and null": `{"ok":true,"status":null,"inject":{}}`,
	} {
		if _, err := wire.DecodeResponse([]byte(p)); !errors.Is(err, wire.ErrInvalid) {
			t.Errorf("%s: decode %v", name, err)
		}
	}
	// A status alone and an inject alone still work, and so does a bare ok.
	for _, r := range []wire.Response{{OK: true, Status: &sampleStatus}, {OK: true, Inject: res}, {OK: true}} {
		p, err := wire.EncodeResponse(r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := wire.DecodeResponse(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	// The encoder leaves the caller's result alone.
	before := *res
	if _, err := wire.EncodeResponse(wire.Response{OK: true, Inject: res}); err != nil || *res != before {
		t.Fatal("the result changed")
	}
}

func mustStatusJSON(t *testing.T) string {
	t.Helper()
	p, err := wire.EncodeResponse(wire.Response{OK: true, Status: &sampleStatus})
	if err != nil {
		t.Fatal(err)
	}
	return string(bytes.TrimSuffix(bytes.TrimPrefix(p, []byte(`{"ok":true,"status":`)), []byte("}")))
}
