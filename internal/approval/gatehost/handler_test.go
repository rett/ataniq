package gatehost

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/consoleclient"
	"ataniq/internal/seatgate"
)

func TestNewNeedsTheServiceAndThePinCheck(t *testing.T) {
	if _, err := New(nil, pinnedOnly, nil); err == nil {
		t.Error("New took no service")
	}
	if _, err := New(&fakePort{}, nil, nil); err == nil {
		t.Error("New took no pin check")
	}
	h, err := New(&fakePort{}, pinnedOnly, nil)
	if err != nil || h == nil {
		t.Fatalf("New with no log: %v", err)
	}
	// A nil log discards: a refusal does not panic.
	if _, err := h.Handle(context.Background(), seat, "owner.nothing", nil); codeOfErr(err) != CodeUnknownOp {
		t.Errorf("got %v", err)
	}
}

// The capabilities are pair and read, valid for the gate's hello, and every
// operation served belongs to one of them.
func TestCapsAndOperations(t *testing.T) {
	if got := Caps(); !slices.Equal(got, []string{"pair", "read"}) {
		t.Fatalf("Caps() = %q", got)
	}
	c := Caps()
	c[0] = "changed"
	if Caps()[0] != "pair" {
		t.Error("Caps returns a shared slice")
	}
	want := map[string]string{
		"seat.pair":   "pair",
		"owner.list":  "read",
		"owner.show":  "read",
		"owner.paths": "read",
		"owner.patch": "read",
	}
	ops := Operations()
	if len(ops) != len(want) || len(opCaps) != len(want) {
		t.Fatalf("Operations() = %q", ops)
	}
	for _, op := range ops {
		if !seatgate.ValidOp(op) || opCaps[op] != want[op] {
			t.Errorf("%s has the capability %q", op, opCaps[op])
		}
	}
}

// A seat is handed to the service as the seat surface, with a session named
// by its key and never as the decider, and with nothing of the desk's.
func TestTheSourceIsTheSeatAndNeverTheDecider(t *testing.T) {
	f := &fakePort{open: true, code: pairCode}
	h, _ := newHandler(t, f)
	out, err := h.Handle(context.Background(), seat, ownerwire.OpSeatPair, validParams(t, ownerwire.OpSeatPair))
	if err != nil {
		t.Fatal(err)
	}
	r, err := ownerwire.DecodeSeatPairResult(out)
	if err != nil || !slices.Equal(r.Words, words) {
		t.Fatalf("result %s: %v", out, err)
	}
	calls := f.seen()
	if len(calls) != 1 {
		t.Fatalf("%d calls", len(calls))
	}
	want := ownerchannel.Source{Surface: ownerchannel.SurfaceSeat, Session: seatFP[:16]}
	if !reflect.DeepEqual(calls[0].src, want) {
		t.Errorf("source %+v, want %+v", calls[0].src, want)
	}
	if calls[0].src.Decider {
		t.Error("a seat was handed over as the decider")
	}
	if calls[0].pair.Code != pairCode || string(calls[0].pair.CredentialID) != string(credID) {
		t.Error("the params did not reach the service as sent")
	}

	// Params that name the decider, or any field of the source, are not
	// read into anything: they are refused and the service is not called.
	for _, extra := range []string{`"decider":true`, `"surface":"console"`, `"session":"0000000000000000"`, `"pid":1`} {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(validParams(t, ownerwire.OpSeatPair), &m); err != nil {
			t.Fatal(err)
		}
		k, v, _ := strings.Cut(extra, ":")
		m[strings.Trim(k, `"`)] = json.RawMessage(v)
		b, _ := json.Marshal(m)
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatPair, b); codeOfErr(err) != CodeBadParams {
			t.Errorf("%s: got %v", extra, err)
		}
	}
	if n := len(f.seen()); n != 1 {
		t.Errorf("the service was called %d times", n)
	}
}

// Each operation answers its result as the ownerwire encoder writes it, and
// hands the service what the params said.
func TestEachOperationAnswersItsResult(t *testing.T) {
	f := &fakePort{}
	h, lb := newHandler(t, f)
	ctx := context.Background()

	out, err := h.Handle(ctx, seat, ownerwire.OpList, nil)
	if err != nil {
		t.Fatal(err)
	}
	if l, err := ownerwire.DecodeListing(out); err != nil || l.Count.Requests != 3 {
		t.Errorf("owner.list: %s %v", out, err)
	}
	// An empty object is the same as no params.
	if _, err := h.Handle(ctx, seat, ownerwire.OpList, json.RawMessage(`{}`)); err != nil {
		t.Errorf("owner.list {}: %v", err)
	}

	out, err = h.Handle(ctx, seat, ownerwire.OpShow, validParams(t, ownerwire.OpShow))
	if err != nil {
		t.Fatal(err)
	}
	if r, err := ownerwire.DecodeShowResult(out); err != nil || string(r.Record.Bytes) != `{"record":1}` {
		t.Errorf("owner.show: %s %v", out, err)
	}

	out, err = h.Handle(ctx, seat, ownerwire.OpPaths, validParams(t, ownerwire.OpPaths))
	if err != nil {
		t.Fatal(err)
	}
	if p, err := ownerwire.DecodePathsPage(out); err != nil || p.ID != pendingID {
		t.Errorf("owner.paths: %s %v", out, err)
	}

	out, err = h.Handle(ctx, seat, ownerwire.OpPatch, validParams(t, ownerwire.OpPatch))
	if err != nil {
		t.Fatal(err)
	}
	if p, err := ownerwire.DecodePatchPage(out); err != nil || p.ReviewDigest != reviewDigest {
		t.Errorf("owner.patch: %s %v", out, err)
	}

	calls := f.seen()
	gotOps := make([]string, len(calls))
	for i, c := range calls {
		gotOps[i] = c.op
	}
	if !slices.Equal(gotOps, []string{"owner.list", "owner.list", "owner.show", "owner.paths", "owner.patch"}) {
		t.Errorf("calls %q", gotOps)
	}
	if calls[2].id != pendingID || calls[3].page.ID != pendingID || calls[4].page.ReviewDigest != reviewDigest {
		t.Error("the params did not reach the service")
	}
	if lines := lb.all(); len(lines) != 0 {
		t.Errorf("served calls logged %q", lines)
	}
}

// A seat.pair while no pairing is open is refused pairing-refused, and so is
// a wrong code; the right code of an open pairing is answered with the words.
func TestPairWhileNoPairingIsOpenIsRefused(t *testing.T) {
	f := &fakePort{code: pairCode}
	h, lb := newHandler(t, f)
	params := validParams(t, ownerwire.OpSeatPair)
	_, err := h.Handle(context.Background(), seat, ownerwire.OpSeatPair, params)
	if codeOfErr(err) != CodePairingRefused {
		t.Fatalf("got %v, want pairing-refused", err)
	}
	if lines := lb.all(); len(lines) != 1 || !strings.Contains(lines[0], "seat.pair refused pairing-refused") || !strings.Contains(lines[0], seatFP[:16]) {
		t.Errorf("log %q", lines)
	}

	f.mu.Lock()
	f.open, f.code = true, "ZZZZZZZZZZZZZZZZZZZZ"
	f.mu.Unlock()
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatPair, params); codeOfErr(err) != CodePairingRefused {
		t.Fatalf("a wrong code: got %v", err)
	}

	f.mu.Lock()
	f.open, f.code = true, pairCode
	f.mu.Unlock()
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatPair, params); err != nil {
		t.Fatalf("the right code: %v", err)
	}
}

// Every error of the service is answered with its fixed code, on every
// operation, and the error the gate gets holds nothing but the code.
func TestEveryServiceErrorMapsToItsCode(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{ownerchannel.ErrPairRefused, "pairing-refused"},
		{ownerchannel.ErrCredentialInvalid, "credential-invalid"},
		{fmt.Errorf("%w: open /some/path: denied", ownerchannel.ErrKeysUnavailable), "keys-unavailable"},
		{ownerchannel.ErrKeysUnavailable, "keys-unavailable"},
		{ownerchannel.ErrKeysFull, "keys-full"},
		{ownerchannel.ErrKeyUnknown, "key-unknown"},
		{ownerchannel.ErrKeyAmbiguous, "key-ambiguous"},
		{ownerchannel.ErrPairingOpen, "pairing-open"},
		{ownerchannel.ErrNoPairing, "pairing-none"},
		{ownerchannel.ErrPairingExpired, "pairing-expired"},
		{ownerchannel.ErrPairingChanged, "pairing-changed"},
		{ownerchannel.ErrClosed, "unavailable"},
		{ownerchannel.ErrStopped, "unavailable"},
		{context.Canceled, "unavailable"},
		{fmt.Errorf("wrapped: %w", context.Canceled), "unavailable"},
		{context.DeadlineExceeded, "deadline-exceeded"},
		{errors.Join(ownerchannel.ErrDoorUnavailable, errors.New("dial: refused")), "door-unavailable"},
		{ownerchannel.ErrJournalReopened, "journal-reopened"},
		{ownerchannel.ErrNotSeat, "failed"},
		{errors.New("something else"), "failed"},
		{fmt.Errorf("ownerchannel: the pairing could not start: %w", errors.New("rand")), "failed"},
		{&record.Error{Code: record.CodeApprovalVoid, Detail: "no pending record has this id"}, "approval-void"},
		{&record.Error{Code: record.CodeApprovalExpired}, "approval-expired"},
		{&record.Error{Code: record.CodeApprovalConsumed}, "approval-consumed"},
		{&record.Error{Code: record.CodeDecisionInvalid}, "decision-invalid"},
		{&record.Error{Code: record.CodePreviousEpoch}, "previous-epoch"},
		{&record.Error{Code: record.CodeReviewChanged, Detail: "the review now has another digest"}, "review-changed"},
		{&record.Error{Code: record.CodeRecordInvalid}, "record-invalid"},
		{&record.Error{Code: record.CodeRecordTooLarge}, "record-too-large"},
		// A refusal the door answered keeps its code through the door's
		// wrapping.
		{errors.Join(ownerchannel.ErrDoorUnavailable, &record.Error{Code: record.CodeApprovalVoid}), "approval-void"},
		// Refusals the five operations cannot meet are not passed on.
		{&record.Error{Code: record.CodeNotDecider}, "failed"},
		{&record.Error{Code: record.CodeOwnerAway}, "failed"},
		{&record.Error{Code: record.CodeLimitRefused}, "failed"},
		{&record.Error{Code: record.Code("Not A Code")}, "failed"},
	}
	for _, op := range Operations() {
		for _, c := range cases {
			f := &fakePort{err: c.err, open: true, code: pairCode}
			h, lb := newHandler(t, f)
			out, err := h.Handle(context.Background(), seat, op, validParams(t, op))
			if out != nil {
				t.Errorf("%s %v: a result with the error", op, c.err)
			}
			if got := codeOfErr(err); got != c.code || !seatgate.ValidCode(got) {
				t.Errorf("%s %v: code %q, want %q", op, c.err, got, c.code)
				continue
			}
			if err.Error() != "seatgate: refused: "+c.code {
				t.Errorf("%s %v: the error says %q", op, c.err, err.Error())
			}
			lines := lb.all()
			if len(lines) != 1 || !strings.Contains(lines[0], op+" refused "+c.code) {
				t.Errorf("%s %v: log %q", op, c.err, lines)
			}
			// The cause is logged for a failure and for an unusable list, and
			// for nothing else.
			hasCause := strings.Contains(lines[0], "refused "+c.code+": ")
			if hasCause && !strings.Contains(lines[0], safeText(c.err.Error())) {
				t.Errorf("%s %v: the log has another cause: %q", op, c.err, lines[0])
			}
			if wantCause := c.code == CodeFailed || c.code == CodeKeysUnavailable; hasCause != wantCause {
				t.Errorf("%s %v: cause in the log is %v: %q", op, c.err, hasCause, lines[0])
			}
		}
	}
}

// Every code a refusal can carry is valid on the wire, and the pairing codes
// are the words the console knows.
func TestTheCodesAreValidAndTheConsolesOwn(t *testing.T) {
	for _, s := range sentinels {
		if !seatgate.ValidCode(s.code) {
			t.Errorf("%q", s.code)
		}
	}
	for c := range passedRefusals {
		if !seatgate.ValidCode(string(c)) {
			t.Errorf("%q", c)
		}
	}
	for _, c := range []string{CodeUnknownOp, CodeBadParams, CodeNotPinned, CodeUnavailable, CodeDeadline, CodeFailed, CodeDoorUnavailable, CodeJournalReopened} {
		if !seatgate.ValidCode(c) {
			t.Errorf("%q", c)
		}
	}
	same := map[string]string{
		CodeKeysUnavailable:   consoleclient.PairKeysUnavailable,
		CodeKeysFull:          consoleclient.PairKeysFull,
		CodeKeyUnknown:        consoleclient.PairKeyUnknown,
		CodeKeyAmbiguous:      consoleclient.PairKeyAmbiguous,
		CodePairingOpen:       consoleclient.PairPairingOpen,
		CodePairingNone:       consoleclient.PairPairingNone,
		CodePairingExpired:    consoleclient.PairPairingExpired,
		CodePairingChanged:    consoleclient.PairPairingChanged,
		CodePairingRefused:    consoleclient.PairPairingRefused,
		CodeCredentialInvalid: consoleclient.PairCredentialInvalid,
	}
	for ours, console := range same {
		if ours != console {
			t.Errorf("%q is %q at the console", ours, console)
		}
	}
}

// Any operation but the five is refused unknown-op, before its params are
// read and without the service: the writes of the owner connection above all.
func TestOtherOperationsAreUnknown(t *testing.T) {
	f := &fakePort{open: true, code: pairCode}
	var others []string
	for _, op := range ownerwire.Operations() {
		if _, ok := opCaps[op]; !ok {
			others = append(others, op)
		}
	}
	if len(others) != len(ownerwire.Operations())-5 {
		t.Fatalf("others %q", others)
	}
	others = append(others, "seat.decide", "seat.challenge", "seat.login", "seat.hello2", "owner.nothing", "x.y")
	for _, op := range others {
		// A handler for each operation, so that the refusal bound is not
		// what answers.
		h, lb := newHandler(t, f)
		for _, params := range []json.RawMessage{nil, json.RawMessage(`{}`), json.RawMessage(`{"decision":"approve"}`)} {
			out, err := h.Handle(context.Background(), seat, op, params)
			if out != nil || codeOfErr(err) != CodeUnknownOp {
				t.Errorf("%s: %s %v", op, out, err)
			}
		}
		for _, l := range lb.all() {
			// The name a peer chose is not logged.
			if l != "seat gate: an unknown operation refused unknown-op (peer "+seatFP[:16]+")" {
				t.Errorf("log %q", l)
			}
		}
	}
	if n := len(f.seen()); n != 0 {
		t.Errorf("the service was called %d times", n)
	}
}

// Params that the gate's strict reading or the operation's decoder refuses
// are bad-params, and the service is not called.
func TestBadParamsAreRefused(t *testing.T) {
	good := map[string]string{}
	for _, op := range Operations() {
		good[op] = string(validParams(t, op))
	}
	cases := map[string][]string{
		ownerwire.OpSeatPair: {
			``, `{}`,
			`{"code":"` + pairCode + `"}`,
			strings.Replace(good[ownerwire.OpSeatPair], `"code"`, `"Code"`, 1),
			strings.Replace(good[ownerwire.OpSeatPair], `{`, `{"code":"`+pairCode+`",`, 1),
			strings.Replace(good[ownerwire.OpSeatPair], pairCode, "MARKERCDE23456789AB!", 1),
			strings.Replace(good[ownerwire.OpSeatPair], pairCode, "short", 1),
			strings.Replace(good[ownerwire.OpSeatPair], `{`, `{"extra":1,`, 1),
			`{"credential_id":"!!","code":"` + pairCode + `","public_key":"AA=="}`,
			`[]`,
		},
		ownerwire.OpList: {
			`{"a":1}`, `{"id":"` + pendingID + `"}`, `[]`, `null`,
		},
		ownerwire.OpShow: {
			``, `{}`, `{"id":"not an id"}`, `{"ID":"` + pendingID + `"}`,
			`{"id":"` + pendingID + `","id":"` + pendingID + `"}`,
			`{"id":"` + pendingID + `","x":1}`, `{"id":1}`,
			`{"id":"` + pendingID + `"} `,
		},
		ownerwire.OpPaths: {
			``, `{"id":"` + pendingID + `"}`,
			`{"id":"` + pendingID + `","review_digest":"zz","page":0}`,
			`{"id":"` + pendingID + `","review_digest":"` + reviewDigest + `","page":-1}`,
			`{"id":"` + pendingID + `","review_digest":"` + reviewDigest + `","page":1.5}`,
			`{"id":"` + pendingID + `","review_digest":"` + reviewDigest + `","page":0,"page":0}`,
		},
		ownerwire.OpPatch: {
			``, `{"id":"` + pendingID + `","review_digest":"` + reviewDigest + `","Page":0}`,
			`{"id":"` + pendingID + `","review_digest":"` + reviewDigest + `","page":0,"more":{}}`,
			`{"id":"` + pendingID + `","review_digest":"` + reviewDigest + `","page":{"a":{"b":{"c":{"d":{"e":{"f":{"g":{"h":1}}}}}}}}}`,
		},
	}
	for op, list := range cases {
		for _, p := range list {
			f := &fakePort{open: true, code: pairCode}
			h, lb := newHandler(t, f)
			var params json.RawMessage
			if p != "" {
				params = json.RawMessage(p)
			}
			if op == ownerwire.OpList && p == "" {
				continue
			}
			out, err := h.Handle(context.Background(), seat, op, params)
			if out != nil || codeOfErr(err) != CodeBadParams {
				t.Errorf("%s %s: %s %v", op, p, out, err)
			}
			if n := len(f.seen()); n != 0 {
				t.Errorf("%s %s: the service was called", op, p)
			}
			if lines := lb.all(); len(lines) != 1 || lines[0] != "seat gate: "+op+" refused bad-params (peer "+seatFP[:16]+")" {
				t.Errorf("%s %s: log %q", op, p, lines)
			}
		}
	}
	// What an ownerwire encoder writes is taken, for every operation.
	for _, op := range Operations() {
		h, _ := newHandler(t, &fakePort{open: true, code: pairCode})
		if _, err := h.Handle(context.Background(), seat, op, validParams(t, op)); err != nil {
			t.Errorf("%s: the encoder's params are refused: %v", op, err)
		}
	}
}

// A peer whose key the pin check does not know is refused not-pinned, and the
// service is not called, whatever the operation.
func TestAPeerThatIsNotPinnedIsRefused(t *testing.T) {
	f := &fakePort{open: true, code: pairCode}
	for _, peer := range []seatgate.Peer{{Fingerprint: otherFP}, {}, {Fingerprint: strings.ToUpper(seatFP)}, {Fingerprint: seatFP[:16]}} {
		// A handler for each peer: the peers with no valid fingerprint share
		// one count of the refusal bound.
		h, lb := newHandler(t, f)
		for _, op := range append(Operations(), "owner.decide") {
			out, err := h.Handle(context.Background(), peer, op, validParamsOr(t, op))
			if out != nil || codeOfErr(err) != CodeNotPinned {
				t.Errorf("%q %s: %s %v", peer.Fingerprint, op, out, err)
			}
		}
		for _, l := range lb.all() {
			if !strings.Contains(l, "refused not-pinned") {
				t.Errorf("log %q", l)
			}
		}
	}
	if n := len(f.seen()); n != 0 {
		t.Errorf("the service was called %d times", n)
	}

	// The pin check is asked on every call: a key unpinned after the
	// handshake is refused from then on.
	pinned := true
	h2, err := New(f, func(fp string) bool { return pinned && fp == seatFP }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h2.Handle(context.Background(), seat, ownerwire.OpList, nil); err != nil {
		t.Fatal(err)
	}
	pinned = false
	if _, err := h2.Handle(context.Background(), seat, ownerwire.OpList, nil); codeOfErr(err) != CodeNotPinned {
		t.Errorf("after unpinning: %v", err)
	}
}

func validParamsOr(t *testing.T, op string) json.RawMessage {
	if _, ok := opCaps[op]; ok {
		return validParams(t, op)
	}
	return json.RawMessage(`{}`)
}

// A result of the service that its encoder refuses is answered failed, and
// nothing of it reaches the seat.
func TestAResultThatDoesNotEncodeFails(t *testing.T) {
	for _, op := range Operations() {
		f := &fakePort{open: true, code: pairCode, badResult: true}
		h, lb := newHandler(t, f)
		out, err := h.Handle(context.Background(), seat, op, validParams(t, op))
		if out != nil || codeOfErr(err) != CodeFailed {
			t.Errorf("%s: %s %v", op, out, err)
		}
		if lines := lb.all(); len(lines) != 1 || !strings.Contains(lines[0], "refused failed: the service's result does not encode") {
			t.Errorf("%s: log %q", op, lines)
		}
	}
}

// Params are never logged, nor put in an error: not the pairing code, the
// credential id or the key, on any path, served or refused.
func TestParamsAreNeverLogged(t *testing.T) {
	secrets := []string{
		pairCode, strings.ToLower(pairCode), "marker",
		base64.StdEncoding.EncodeToString(credID),
		base64.StdEncoding.EncodeToString([]byte("marker-public-key")),
	}
	errs := []error{nil, ownerchannel.ErrPairRefused, ownerchannel.ErrCredentialInvalid, ownerchannel.ErrKeysUnavailable,
		errors.New("plain"), &record.Error{Code: record.CodeRecordInvalid, Detail: "the parameters are not valid for this operation"}}
	paramSets := []json.RawMessage{
		validParams(t, ownerwire.OpSeatPair),
		json.RawMessage(strings.Replace(string(validParams(t, ownerwire.OpSeatPair)), `{`, `{"x":"marker",`, 1)),
		json.RawMessage(`{"code":"` + pairCode + `","credential_id":"bWFya2Vy","public_key":7}`),
		json.RawMessage(`{"id":"ap-marker"}`),
		json.RawMessage(`{"id":"marker","review_digest":"marker","page":0}`),
	}
	var all []string
	for _, op := range append(Operations(), "seat.marker", "owner.decide") {
		for _, e := range errs {
			for _, p := range paramSets {
				for _, peer := range []seatgate.Peer{seat, {Fingerprint: otherFP}} {
					f := &fakePort{err: e, open: true, code: pairCode}
					h, lb := newHandler(t, f)
					_, err := h.Handle(context.Background(), peer, op, p)
					if err != nil {
						all = append(all, err.Error())
					}
					all = append(all, lb.all()...)
				}
			}
		}
	}
	if len(all) == 0 {
		t.Fatal("nothing was logged")
	}
	for _, l := range all {
		for _, s := range secrets {
			if strings.Contains(l, s) {
				t.Errorf("%q holds %q", l, s)
			}
		}
	}
}

// safeText makes one printable line and caps it.
func TestSafeText(t *testing.T) {
	if got := safeText("a\nb\x00c\x7fd\u0085e\xffg"); got != "a?b?c?d?e?g" {
		t.Errorf("%q", got)
	}
	long := strings.Repeat("é", 400)
	if got := safeText(long); len(got) > maxLogText || !strings.HasPrefix(long, got) {
		t.Errorf("%d bytes", len(got))
	}
}
