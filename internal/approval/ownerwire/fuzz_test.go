package ownerwire

import (
	"bytes"
	"errors"
	"testing"

	"ataniq/internal/approval/record"
)

// The fuzz tests feed each decoder any bytes. Each has a seed corpus: the
// valid sample of its payload, that sample broken in the ways every
// decoder refuses, and payloads that break its own rules. So they run as
// ordinary tests on the seeds.
//
// What must hold for any input: the decoder does not panic; a refusal is a
// *record.Error with one of the codes this package answers with and a
// detail that could be shown as it is; and a payload that decodes can be
// encoded, in no more bytes than it came in and within its cap, to bytes
// that decode and encode to themselves.

func fuzzForm(f *testing.F, name string, seeds ...string) {
	f.Helper()
	var fm form
	for _, candidate := range forms(f) {
		if candidate.name == name {
			fm = candidate
		}
	}
	if fm.decode == nil {
		f.Fatalf("no payload is named %q", name)
	}
	f.Add(fm.sample)
	f.Add(append(append([]byte(nil), fm.sample...), 'x'))
	f.Add(fm.sample[:len(fm.sample)-1])
	f.Add(append([]byte(`{"zz":1,`), fm.sample[1:]...))
	f.Add(bytes.ToUpper(fm.sample))
	f.Add([]byte(nil))
	f.Add([]byte("{}"))
	f.Add([]byte("[]"))
	f.Add([]byte("null"))
	f.Add([]byte(`{"b64":"/w=="}`))
	f.Add([]byte(`{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{"a":{}}}}}}}}}}`))
	f.Add([]byte(`{"id":"\ud800"}`))
	f.Add([]byte(`{"id":1.5e3}`))
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := fm.decode(t, data)
		if err != nil {
			var e *record.Error
			if !errors.As(err, &e) {
				t.Fatalf("the refusal is a %T, not a *record.Error: %v", err, err)
			}
			switch e.Code {
			case fm.invalid, record.CodeRecordTooLarge, record.CodeBindingMismatch:
			default:
				t.Fatalf("the refusal has the code %q", e.Code)
			}
			if e.Detail == "" || !record.Text(e.Detail).Plain() {
				t.Fatalf("the refusal has the detail %q", e.Detail)
			}
			return
		}
		if len(out) > len(data) || len(out) > fm.limit {
			t.Fatalf("%d bytes encode to %d", len(data), len(out))
		}
		again, err := fm.decode(t, out)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("the encoded form is not stable (%v):\n%s\n%s", err, out, again)
		}
	})
}

const fuzzBase = `{"id":"ap-20261120-0007","review_digest":"0000000000000000000000000000000000000000000000000000000000000000",`

func FuzzDecodeNothing(f *testing.F) {
	fuzzForm(f, "nothing", " { } ", `{"":0}`, "{}{}")
}

func FuzzDecodeListing(f *testing.F) {
	fuzzForm(f, "listing",
		`{"count":{"requests":0,"would_meet":0},"pending":[],"recent":[],"rules":[]}`,
		`{"count":{"requests":0,"would_meet":1},"pending":[],"recent":[],"rules":[]}`,
		`{"count":{"requests":0,"would_meet":0},"pending":[{"bytes":"YQ=="}],"recent":[],"rules":[]}`,
		`{"count":{"requests":0,"would_meet":0},"looked_at":"","pending":[],"recent":[],"rules":[]}`)
}

func FuzzDecodeShowParams(f *testing.F) {
	fuzzForm(f, "show params", `{"id":"ap-"}`, `{"id":"rq-1"}`, `{"id":"ap-1","review_digest":""}`, `{"ID":"ap-1"}`)
}

func FuzzDecodeShowResult(f *testing.F) {
	fuzzForm(f, "show result",
		`{"held":false,"record":{"bytes":"YQ==","sha256":"ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"}}`,
		`{"held":true,"record":{"bytes":"Yg==","sha256":"ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"}}`,
		`{"held":false,"record":{"bytes":"YQ","sha256":"ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"}}`,
		`{"held":false,"record":{"bytes":"","sha256":"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"}}`,
		`{"held":false,"outcome":{"line":"","state":"held"},"record":{"bytes":"YQ==","sha256":"ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb"}}`)
}

func FuzzDecodePageParams(f *testing.F) {
	fuzzForm(f, "page params", fuzzBase+`"page":0}`, fuzzBase+`"page":-1}`, fuzzBase+`"page":65536}`,
		fuzzBase+`"page":1.0}`, fuzzBase+`"page":9223372036854775808}`, fuzzBase+`"page":0,"pages":1}`)
}

func FuzzDecodePathsPage(f *testing.F) {
	fuzzForm(f, "paths page",
		fuzzBase+`"page":0,"pages":1,"paths":[]}`,
		fuzzBase+`"page":0,"pages":2,"paths":[]}`,
		fuzzBase+`"page":1,"pages":1,"paths":[]}`,
		fuzzBase+`"page":0,"pages":1,"paths":[{"added":0,"new_bytes":0,"new_mode":"100644","old_mode":"000000","path":{"b64":"/w=="},"removed":0,"status":"A"}]}`,
		fuzzBase+`"page":0,"pages":1,"paths":[{"added":0,"new_bytes":0,"new_mode":"100644","old_mode":"000000","path":{"b64":"eA=="},"removed":0,"status":"A"}]}`,
		fuzzBase+`"page":0,"pages":1,"paths":[{"added":0,"new_bytes":0,"new_mode":"100648","old_mode":"000000","path":"x","removed":0,"status":"R"}]}`)
}

func FuzzDecodePatchPage(f *testing.F) {
	fuzzForm(f, "patch page",
		fuzzBase+`"lines":[],"page":0,"pages":1}`,
		fuzzBase+`"lines":[],"page":0,"pages":2}`,
		fuzzBase+`"lines":[{"cont":true,"text":""},{"cont":false,"text":"x"}],"page":0,"pages":1}`,
		fuzzBase+`"lines":[{"cont":true,"text":"x"}],"page":0,"pages":1}`,
		fuzzBase+`"lines":[{"cont":true,"text":"x"}],"page":0,"pages":2}`,
		fuzzBase+`"lines":[{"cont":false,"text":{"b64":"G1sySv8="}}],"page":0,"pages":1}`,
		fuzzBase+`"lines":[{"cont":false,"text":{"b64":"eA=="}}],"page":0,"pages":1}`,
		fuzzBase+`"lines":[{"cont":false,"text":"a\u001bb"}],"page":0,"pages":1}`)
}

func FuzzDecodeDeciderParams(f *testing.F) {
	fuzzForm(f, "decider params",
		`{"client":"console-a","session":"0123456789ABCDEF"}`,
		`{"client":"console-a","session":"0123456789abcde"}`,
		`{"client":"","session":"0123456789abcdef"}`,
		`{"client":"console-a","session":"0123456789abcdef","pid":1}`)
}

func FuzzDecodeDeciderResult(f *testing.F) {
	fuzzForm(f, "decider result", `{"decider":true}`, `{"decider":false}`, `{"decider":true,"reason":"x"}`,
		`{"decider":false,"reason":""}`, `{"decider":"true"}`)
}

func FuzzDecodeDecisionFrame(f *testing.F) {
	const base = `{"binding_sha256":"0000000000000000000000000000000000000000000000000000000000000000","id":"ap-20261120-0007",`
	fuzzForm(f, "decision frame",
		base+`"decision":"deny","signed_off":[]}`,
		base+`"decision":"deny","reason":"not today","signed_off":[]}`,
		base+`"decision":"approve","reason":"because","signed_off":[]}`,
		base+`"decision":"deny","signed_off":[{"kind":"exec-config","path":"Makefile"}]}`,
		base+`"decision":"approve","signed_off":[{"kind":"symlink","path":{"b64":"/w=="}},{"kind":"symlink","path":{"b64":"/w=="}}]}`,
		base+`"decision":"maybe","signed_off":[]}`,
		base+`"decision":"approve","signed_off":[],"planner":"planner-a"}`,
		base+`"decision":"approve","signed_off":[],"uses":1}`,
		base+`"decision":"approve","signed_off":[],"confirmed":"code"}`,
		base+`"decision":"approve","signed_off":[],"console":{"pid":1,"tty":"","client":"console-a","decider":true,"surface":"console","session":"0123456789abcdef"}}`,
		base+`"decision":"approve","signed_off":[],"decided_at":"2026-11-20T14:06:40Z","expires_at":"2026-11-20T14:36:40Z"}`)
}

func FuzzDecodeDecideResult(f *testing.F) {
	fuzzForm(f, "decide result", `{"held":false,"line":"","state":"approved"}`, `{"held":true,"line":"","state":"approved"}`,
		`{"held":false,"line":"x","state":"held"}`, `{"held":false,"line":"a\nb","state":"denied"}`)
}

func FuzzDecodeLimitParams(f *testing.F) {
	fuzzForm(f, "limit params", `{"project":""}`, `{"project":"a","to":1}`, `{"Project":"a"}`)
}

func FuzzDecodeLimitSetParams(f *testing.F) {
	fuzzForm(f, "limit-set params", `{"project":"a","to":0,"typed":false}`, `{"project":"a","to":40,"typed":1}`,
		`{"project":"a","to":4e1,"typed":true}`, `{"project":"a","to":-1,"typed":true}`)
}

func FuzzDecodeLimitView(f *testing.F) {
	fuzzForm(f, "limit view",
		`{"days":[],"limit":1,"project":"a","raised_today":false,"suggestion":0}`,
		`{"days":[],"limit":0,"project":"a","raised_today":false,"suggestion":0}`,
		`{"days":[{"alert":false,"date":"2026-02-30","reached_limit":false,"uses":0}],"limit":1,"project":"a","raised_today":false,"suggestion":6}`)
}
