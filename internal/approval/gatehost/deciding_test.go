package gatehost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/seatgate"
)

// The verifier's checks, as fixed errors of its kind (package credkind is
// not among this package's imports, its tests' included).
var (
	errOriginCheck    = errors.New("credkind: origin mismatch")
	errSignatureCheck = errors.New("credkind: signature invalid")
	errUVCheck        = errors.New("credkind: user verification missing")
)

// theGate is the gate's own values the deciding handler hands the service.
var theGate = ownerchannel.Gate{Instance: strings.Repeat("9f", 32), Origin: "https://seat.example", RPID: "seat.example"}

// seatCall is one call of the seat's operations the fake received.
type seatCall struct {
	op     string
	src    ownerchannel.Source
	gate   ownerchannel.Gate
	login  ownerwire.SeatLoginParams
	ch     ownerwire.SeatChallengeParams
	decide ownerwire.SeatDecideParams
}

// fakeSeat is the service's seat operations as the handler sees them. err,
// when set, is what every call answers; otherwise each answers a good result.
type fakeSeat struct {
	mu    sync.Mutex
	calls []seatCall
	err   error
}

func (f *fakeSeat) record(c seatCall) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, c)
	return f.err
}

func (f *fakeSeat) seen() []seatCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]seatCall(nil), f.calls...)
}

func (f *fakeSeat) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

var (
	aNonce     = bytes.Repeat([]byte{0x20}, 32)
	aChallenge = bytes.Repeat([]byte{0x30}, 32)
	anID       = "0123456789abcdef"
)

func (f *fakeSeat) SeatLoginChallenge(_ context.Context, src ownerchannel.Source, g ownerchannel.Gate) (ownerwire.SeatLoginChallengeResult, error) {
	if err := f.record(seatCall{op: ownerwire.OpSeatLoginChallenge, src: src, gate: g}); err != nil {
		return ownerwire.SeatLoginChallengeResult{}, err
	}
	return ownerwire.SeatLoginChallengeResult{ChallengeID: anID, Challenge: aChallenge, Nonce: aNonce, ExpiresAt: "2026-11-22T09:05:00Z", Allow: [][]byte{credID}}, nil
}

func (f *fakeSeat) SeatLogin(_ context.Context, src ownerchannel.Source, g ownerchannel.Gate, p ownerwire.SeatLoginParams) (ownerwire.SeatLoginResult, error) {
	if err := f.record(seatCall{op: ownerwire.OpSeatLogin, src: src, gate: g, login: p}); err != nil {
		return ownerwire.SeatLoginResult{}, err
	}
	return ownerwire.SeatLoginResult{Credential: displayIDOf(credID), CredentialID: credID, Kind: "vault-passkey"}, nil
}

func (f *fakeSeat) SeatChallenge(_ context.Context, src ownerchannel.Source, g ownerchannel.Gate, p ownerwire.SeatChallengeParams) (ownerwire.SeatChallengeResult, error) {
	if err := f.record(seatCall{op: ownerwire.OpSeatChallenge, src: src, gate: g, ch: p}); err != nil {
		return ownerwire.SeatChallengeResult{}, err
	}
	return ownerwire.SeatChallengeResult{ChallengeID: anID, Challenge: aChallenge, Nonce: aNonce, ExpiresAt: "2026-11-22T09:05:00Z"}, nil
}

func (f *fakeSeat) SeatDecide(_ context.Context, src ownerchannel.Source, g ownerchannel.Gate, p ownerwire.SeatDecideParams) (ownerwire.DecideResult, error) {
	if err := f.record(seatCall{op: ownerwire.OpSeatDecide, src: src, gate: g, decide: p}); err != nil {
		return ownerwire.DecideResult{}, err
	}
	return ownerwire.DecideResult{State: record.StateApproved, Line: "approved on the phone"}, nil
}

// displayIDOf is a credential's display id, as the key list gives it: the
// first 8 bytes of the SHA-256 of its id, in hex.
func displayIDOf(id []byte) string {
	sum := sha256.Sum256(id)
	return hex.EncodeToString(sum[:8])
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// newDeciding makes a deciding handler over f and s, with the seat's key
// pinned, on a hand-moved clock.
func newDeciding(t *testing.T, f *fakePort, s *fakeSeat) (*Handler, *logBook, *testClock) {
	t.Helper()
	lb := &logBook{}
	h, err := NewDeciding(f, s, theGate, func(fp string) bool { return fp == seatFP || fp == secondFP }, lb.logf)
	if err != nil {
		t.Fatal(err)
	}
	clk := &testClock{t: time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)}
	h.now = clk.now
	return h, lb, clk
}

// anAssertion is an assertion within the wire's bounds.
func anAssertion() ownerwire.Assertion {
	return ownerwire.Assertion{
		CredentialID: credID, AuthenticatorData: bytes.Repeat([]byte{0xaa}, 37),
		ClientDataJSON: []byte(`{"type":"webauthn.get","marker":"client-data"}`), Signature: []byte{0x30, 0x06, 0x02, 0x01, 0x01, 0x02, 0x01, 0x01},
	}
}

// aFrame is an approve frame of the right form.
func aFrame() ownerwire.DecisionFrame {
	return ownerwire.DecisionFrame{ID: "ap-20261120-0007", BindingSHA256: strings.Repeat("12", 32), Decision: record.DecisionApprove,
		SignedOff: []record.SignOff{{Kind: record.FlagExecConfig, Path: "Makefile"}}}
}

// seatParams are params each seat operation's decoder takes.
func seatParams(t testing.TB, op string) json.RawMessage {
	t.Helper()
	var b []byte
	var err error
	switch op {
	case ownerwire.OpSeatLoginChallenge:
		return nil
	case ownerwire.OpSeatLogin:
		b, err = ownerwire.EncodeSeatLoginParams(ownerwire.SeatLoginParams{ChallengeID: anID, Assertion: anAssertion()})
	case ownerwire.OpSeatChallenge:
		b, err = ownerwire.EncodeSeatChallengeParams(ownerwire.SeatChallengeParams{Frame: aFrame(), Credential: "fedcba9876543210"})
	case ownerwire.OpSeatDecide:
		b, err = ownerwire.EncodeSeatDecideParams(ownerwire.SeatDecideParams{ChallengeID: anID, Frame: aFrame(), Assertion: anAssertion()})
	case ownerwire.OpSeatWait:
		b, err = ownerwire.EncodeSeatWaitParams(ownerwire.SeatWaitParams{})
	case ownerwire.OpSeatGenesis:
		b, err = ownerwire.EncodeSeatGenesisParams(ownerwire.SeatGenesisParams{Verb: ownerwire.GenesisVerbStart})
	default:
		t.Fatalf("no params for %s", op)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// decidingOps are the gate's operations that a deciding handler over the
// fakes of this file serves: login and decide. seat.wait is left out because
// its capability, watch, comes of the service being a [WaitPort], which
// fakePort is not, and seat.genesis because its capability, genesis, comes of
// the handler being given the thread store (WithGenesis), which these are not,
// so these handlers answer both unknown-op (the loop of
// TestWithoutTheCapabilitiesTheSeatOperationsAreUnknown checks that); their own
// served and refused cases are in wait_test.go and genesis_test.go.
func decidingOps() []string {
	return slices.DeleteFunc(ownerwire.GateOperations(), func(op string) bool {
		return op == ownerwire.OpSeatWait || op == ownerwire.OpSeatGenesis
	})
}

func TestNewDecidingNeedsTheSeatOperations(t *testing.T) {
	if _, err := NewDeciding(&fakePort{}, nil, theGate, pinnedOnly, nil); err == nil {
		t.Fatal("a deciding handler without the seat's operations")
	}
	if _, err := NewDeciding(nil, &fakeSeat{}, theGate, pinnedOnly, nil); err == nil {
		t.Fatal("a deciding handler without the service")
	}
	h, _, _ := newDeciding(t, &fakePort{}, &fakeSeat{})
	if !slices.Equal(h.Caps(), DecidingCaps()) {
		t.Fatalf("Caps %q", h.Caps())
	}
}

// Each of the four is handed to the service with the seat's source, the
// gate's own values (never anything of the params), and the params as
// decoded; the result is encoded. Issuing a challenge writes one line, and a
// served call no other.
func TestADecidingHandlerServesLoginAndDecide(t *testing.T) {
	s := &fakeSeat{}
	h, lb, _ := newDeciding(t, &fakePort{}, s)
	want := ownerchannel.Source{Surface: ownerchannel.SurfaceSeat, Session: seatFP[:16]}
	for _, op := range decidingOps() {
		out, err := h.Handle(context.Background(), seat, op, seatParams(t, op))
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		var decodeErr error
		switch op {
		case ownerwire.OpSeatLoginChallenge:
			_, decodeErr = ownerwire.DecodeSeatLoginChallengeResult(out)
		case ownerwire.OpSeatLogin:
			_, decodeErr = ownerwire.DecodeSeatLoginResult(out)
		case ownerwire.OpSeatChallenge:
			_, decodeErr = ownerwire.DecodeSeatChallengeResult(out)
		case ownerwire.OpSeatDecide:
			_, decodeErr = ownerwire.DecodeDecideResult(out)
		}
		if decodeErr != nil {
			t.Fatalf("%s answered %s: %v", op, out, decodeErr)
		}
	}
	calls := s.seen()
	if len(calls) != 4 {
		t.Fatalf("%d calls", len(calls))
	}
	for _, c := range calls {
		if c.src != want || c.gate != theGate {
			t.Errorf("%s: %+v %+v", c.op, c.src, c.gate)
		}
	}
	if calls[1].login.ChallengeID != anID || !bytes.Equal(calls[1].login.Assertion.Signature, anAssertion().Signature) {
		t.Errorf("login params %+v", calls[1].login)
	}
	if calls[2].ch.Credential != "fedcba9876543210" || calls[2].ch.Frame.ID != aFrame().ID {
		t.Errorf("challenge params %+v", calls[2].ch)
	}
	if calls[3].decide.ChallengeID != anID || calls[3].decide.Frame.BindingSHA256 != aFrame().BindingSHA256 {
		t.Errorf("decide params %+v", calls[3].decide)
	}
	l := lb.all()
	if len(l) != 2 || l[0] != "seat gate: seat.login-challenge issued a challenge (peer "+seatFP[:16]+")" ||
		l[1] != "seat gate: seat.challenge issued a challenge (peer "+seatFP[:16]+")" {
		t.Fatalf("log %q", l)
	}
}

// A handler made with New does not serve the four: each is unknown-op before
// its params are read and without the service.
func TestWithoutTheCapabilitiesTheSeatOperationsAreUnknown(t *testing.T) {
	f := &fakePort{}
	for _, op := range ownerwire.GateOperations() {
		// One handler for each operation: the three refusals of each count,
		// and the six operations together would pass the bound.
		h, lb := newHandler(t, f)
		for _, params := range []json.RawMessage{nil, seatParams(t, op), json.RawMessage(`not json`)} {
			if out, err := h.Handle(context.Background(), seat, op, params); out != nil || codeOfErr(err) != CodeUnknownOp {
				t.Fatalf("%s: %s %v", op, out, err)
			}
		}
		for _, l := range lb.all() {
			if !strings.Contains(l, "an unknown operation refused unknown-op") {
				t.Fatalf("log %q", l)
			}
		}
	}
	if len(f.seen()) != 0 {
		t.Fatal("the service was called")
	}
}

// The params of the four are read strictly, and anything the gate's reading
// or the envelope's decoder refuses is bad-params without the service. The
// frame's content of a decide is not judged here: the service takes the
// challenge first, so a decide with a wrong frame reaches it.
func TestTheSeatOperationsReadTheirParamsStrictly(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString
	assertion := func(field string, v any) string {
		a := map[string]any{"credential_id": b64(credID), "authenticator_data": b64(bytes.Repeat([]byte{0xaa}, 37)), "client_data_json": b64([]byte("{}")), "signature": b64([]byte{1})}
		if field != "" {
			if v == nil {
				delete(a, field)
			} else {
				a[field] = v
			}
		}
		return string(mustJSON(a))
	}
	frame := string(mustJSON(aFrame()))
	bad := map[string][]string{
		ownerwire.OpSeatLoginChallenge: {`{"x":1}`, `[]`, `null`},
		ownerwire.OpSeatLogin: {
			`{}`,
			`{"challenge_id":"` + anID + `"}`,
			`{"challenge_id":"0123456789ABCDEF","assertion":` + assertion("", nil) + `}`,
			`{"challenge_id":"` + anID + `","assertion":` + assertion("signature", b64(make([]byte, 73))) + `}`,
			`{"challenge_id":"` + anID + `","assertion":` + assertion("authenticator_data", b64(make([]byte, 36))) + `}`,
			`{"challenge_id":"` + anID + `","assertion":` + assertion("credential_id", "not base64!") + `}`,
			`{"challenge_id":"` + anID + `","assertion":` + assertion("user_handle", b64([]byte("x"))) + `}`,
			`{"challenge_id":"` + anID + `","challenge_id":"` + anID + `","assertion":` + assertion("", nil) + `}`,
			`{"challenge_id":"` + anID + `","assertion":` + assertion("", nil) + `,"extra":1}`,
		},
		ownerwire.OpSeatChallenge: {
			`{"frame":` + frame + `}`,
			`{"frame":` + frame + `,"credential":"FEDCBA9876543210"}`,
			`{"frame":` + strings.Replace(frame, `"approve"`, `"maybe"`, 1) + `,"credential":"fedcba9876543210"}`,
			`{"frame":` + strings.Replace(frame, `"approve"`, `"deny"`, 1) + `,"credential":"fedcba9876543210"}`,
		},
		ownerwire.OpSeatDecide: {
			`{"challenge_id":"` + anID + `","frame":` + frame + `}`,
			`{"challenge_id":"short","frame":` + frame + `,"assertion":` + assertion("", nil) + `}`,
			`{"challenge_id":"` + anID + `","frame":{"id":1},"assertion":` + assertion("", nil) + `}`,
			`{"challenge_id":"` + anID + `","frame":` + frame + `,"assertion":` + assertion("client_data_json", nil) + `}`,
			`{"challenge_id":"` + anID + `","frame":` + frame + `,"assertion":` + assertion("signature", "") + `}`,
		},
	}
	for op, list := range bad {
		for _, params := range list {
			s := &fakeSeat{}
			h, lb, _ := newDeciding(t, &fakePort{}, s)
			if out, err := h.Handle(context.Background(), seat, op, json.RawMessage(params)); out != nil || codeOfErr(err) != CodeBadParams {
				t.Fatalf("%s %s: %s %v", op, params, out, err)
			}
			if len(s.seen()) != 0 {
				t.Fatalf("%s %s reached the service", op, params)
			}
			for _, l := range lb.all() {
				if strings.Contains(l, "Makefile") || strings.Contains(l, anID) {
					t.Fatalf("the log holds params: %q", l)
				}
			}
		}
	}
	// A deny with a sign-off: the envelope is good, and the service judges
	// the frame after it took the challenge.
	s := &fakeSeat{err: ownerchannel.ErrSeatParams}
	h, _, _ := newDeciding(t, &fakePort{}, s)
	wrong := `{"challenge_id":"` + anID + `","frame":` + strings.Replace(frame, `"approve"`, `"deny"`, 1) + `,"assertion":` + assertion("", nil) + `}`
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatDecide, json.RawMessage(wrong)); codeOfErr(err) != CodeBadParams {
		t.Fatalf("a decide with a wrong frame: %v", err)
	}
	if c := s.seen(); len(c) != 1 || c[0].decide.Frame.Decision != record.DecisionDeny {
		t.Fatalf("the decide with a wrong frame did not reach the service: %+v", c)
	}
}

// Each error of the seat's operations has its code; the log of an
// assertion-invalid names the check that failed, and nothing the seat sent.
func TestTheSeatErrorsMapToTheirCodes(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{ownerchannel.ErrChallengeStale, "challenge-stale"},
		{ownerchannel.ErrChallengesFull, "challenges-full"},
		{fmt.Errorf("%w: %w", ownerchannel.ErrAssertionInvalid, errOriginCheck), "assertion-invalid"},
		{ownerchannel.ErrKeyRevoked, "key-revoked"},
		{ownerchannel.ErrKindRefused, "kind-refused"},
		{ownerchannel.ErrKeyUnknown, "key-unknown"},
		{ownerchannel.ErrKeysUnavailable, "keys-unavailable"},
		{ownerchannel.ErrSeatParams, "bad-params"},
		{ownerchannel.ErrNotSeat, "failed"},
		{&record.Error{Code: record.CodeBindingMismatch, Detail: "marker detail"}, "binding-mismatch"},
		{&record.Error{Code: record.CodeDecisionInvalid}, "decision-invalid"},
		{&record.Error{Code: record.CodeApprovalExpired}, "approval-expired"},
		{&record.Error{Code: record.CodeNotDecider}, "failed"},
		{&record.Error{Code: record.CodeOwnerAway}, "failed"},
		{ownerchannel.ErrDoorUnavailable, "door-unavailable"},
		{ownerchannel.ErrJournalReopened, "journal-reopened"},
		{context.DeadlineExceeded, "deadline-exceeded"},
		{errors.New("something else"), "failed"},
	}
	for _, c := range cases {
		for _, op := range decidingOps() {
			s := &fakeSeat{err: c.err}
			h, lb, _ := newDeciding(t, &fakePort{}, s)
			_, err := h.Handle(context.Background(), seat, op, seatParams(t, op))
			if codeOfErr(err) != c.code || !seatgate.ValidCode(c.code) {
				t.Fatalf("%s, %v: %v", op, c.err, err)
			}
			l := lb.all()
			if len(l) != 1 || !strings.Contains(l[0], op+" refused "+c.code) || strings.Contains(l[0], "marker") {
				t.Fatalf("log %q", l)
			}
			if c.code == "assertion-invalid" && !strings.Contains(l[0], errOriginCheck.Error()) {
				t.Fatalf("the log of an assertion-invalid does not name the check: %q", l[0])
			}
			if n := h.Counts()[c.code]; n != 1 {
				t.Fatalf("the count of %s is %d", c.code, n)
			}
		}
	}
}

// The bound counts exactly the refusals the contract says: none of a login
// (but of an operation not served); of a decision's challenge and decide,
// not challenge-stale, challenges-full, key-revoked or kind-refused; and
// bad-params, key-unknown, assertion-invalid and the record codes there.
func TestTheBoundCountsTheSeatRefusalsTheContractSays(t *testing.T) {
	type row struct {
		ops    []string
		err    error
		counts bool
	}
	login := []string{ownerwire.OpSeatLoginChallenge, ownerwire.OpSeatLogin}
	decide := []string{ownerwire.OpSeatChallenge, ownerwire.OpSeatDecide}
	rows := []row{
		{login, ownerchannel.ErrChallengeStale, false},
		{login, fmt.Errorf("%w: %w", ownerchannel.ErrAssertionInvalid, errSignatureCheck), false},
		{login, ownerchannel.ErrKeyUnknown, false},
		{login, ownerchannel.ErrKeyRevoked, false},
		{login, ownerchannel.ErrKindRefused, false},
		{login, ownerchannel.ErrSeatParams, false},
		{login, ownerchannel.ErrKeysUnavailable, false},
		{decide, ownerchannel.ErrChallengeStale, false},
		{decide, ownerchannel.ErrChallengesFull, false},
		{decide, ownerchannel.ErrKeyRevoked, false},
		{decide, ownerchannel.ErrKindRefused, false},
		{decide, ownerchannel.ErrKeysUnavailable, false},
		{decide, ownerchannel.ErrDoorUnavailable, false},
		{decide, ownerchannel.ErrSeatParams, true},
		{decide, ownerchannel.ErrKeyUnknown, true},
		{decide, fmt.Errorf("%w: %w", ownerchannel.ErrAssertionInvalid, errUVCheck), true},
		{decide, &record.Error{Code: record.CodeBindingMismatch}, true},
		{decide, &record.Error{Code: record.CodeDecisionInvalid}, true},
		{decide, &record.Error{Code: record.CodeApprovalExpired}, true},
	}
	for _, r := range rows {
		for _, op := range r.ops {
			s := &fakeSeat{err: r.err}
			h, _, clk := newDeciding(t, &fakePort{}, s)
			for i := 0; i < RefusalBound; i++ {
				if _, err := h.Handle(context.Background(), seat, op, seatParams(t, op)); codeOfErr(err) == CodeTooManyRefusals {
					t.Fatalf("%s %v shut out after %d", op, r.err, i)
				}
				clk.add(time.Second)
			}
			_, err := h.Handle(context.Background(), seat, ownerwire.OpList, nil)
			if shut := codeOfErr(err) == CodeTooManyRefusals; shut != r.counts {
				t.Fatalf("%s %v: after %d the peer is shut out %v, want %v", op, r.err, RefusalBound, shut, r.counts)
			}
		}
	}
	// bad-params of a login as the handler reads it does not count either,
	// and unknown-op of a login the handler does not serve does.
	h, _, _ := newDeciding(t, &fakePort{}, &fakeSeat{})
	for i := 0; i < 2*RefusalBound; i++ {
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatLogin, json.RawMessage(`{"x":1}`)); codeOfErr(err) != CodeBadParams {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	plain, _ := newHandler(t, &fakePort{})
	for i := 0; i < RefusalBound; i++ {
		_, _ = plain.Handle(context.Background(), seat, ownerwire.OpSeatLogin, nil)
	}
	if _, err := plain.Handle(context.Background(), seat, ownerwire.OpList, nil); codeOfErr(err) != CodeTooManyRefusals {
		t.Fatalf("unknown-op of a login not served: %v", err)
	}
	// A shut-out peer's login is refused too, without the service, and is
	// counted as such.
	s := &fakeSeat{err: ownerchannel.ErrKeyUnknown}
	d, _, _ := newDeciding(t, &fakePort{}, s)
	for i := 0; i < RefusalBound; i++ {
		_, _ = d.Handle(context.Background(), seat, ownerwire.OpSeatDecide, seatParams(t, ownerwire.OpSeatDecide))
	}
	n := len(s.seen())
	if _, err := d.Handle(context.Background(), seat, ownerwire.OpSeatLoginChallenge, nil); codeOfErr(err) != CodeTooManyRefusals || len(s.seen()) != n {
		t.Fatalf("a shut-out peer's login: %v", err)
	}
	if d.Counts()[CodeTooManyRefusals] != 1 || d.Counts()["key-unknown"] != int64(RefusalBound) {
		t.Fatalf("counts %v", d.Counts())
	}
}

// The deciding handler behind the real gate: the hello lists the four
// capabilities, and a login challenge is answered over TLS.
func TestADecidingHandlerOverTheGate(t *testing.T) {
	gateID, seatID := newIdentity(t, "gate"), newIdentity(t, "seat")
	s := &fakeSeat{}
	g := theGate
	g.Instance = gateID.Fingerprint()
	h, err := NewDeciding(&fakePort{}, s, g, func(fp string) bool { return fp == seatID.Fingerprint() }, nil)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() {
		served <- seatgate.Serve(ctx, ln, seatgate.ServerConfig{TLS: seatgate.ServerTLS(gateID, []string{seatID.Fingerprint()}), Caps: h.Caps()}, h)
	}()
	defer func() {
		cancel()
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			t.Error("Serve did not end")
		}
	}()
	dctx, dcancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dcancel()
	c, err := seatgate.Dial(dctx, ln.Addr().String(), seatgate.ClientConfig{TLS: seatgate.ClientTLS(seatID, gateID.Fingerprint(), "gate"), Caps: DecidingCaps()})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if !slices.Equal(c.Caps(), DecidingCaps()) {
		t.Fatalf("the hello listed %q", c.Caps())
	}
	var raw json.RawMessage
	if err := c.Call(dctx, ownerwire.OpSeatLoginChallenge, nil, &raw); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerwire.DecodeSeatLoginChallengeResult(raw); err != nil {
		t.Fatal(err)
	}
	if calls := s.seen(); len(calls) != 1 || calls[0].gate != g || calls[0].src.Session != seatID.Fingerprint()[:16] {
		t.Fatalf("calls %+v", calls)
	}
}

// registeredPair is the params of a seat.pair that relays a registration for
// the relying party rpID.
func registeredPair(t *testing.T, rpID string) json.RawMessage {
	t.Helper()
	b, err := ownerwire.EncodeSeatPairParams(ownerwire.SeatPairParams{
		Code: pairCode, CredentialID: credID, PublicKey: []byte("marker-public-key"),
		AuthenticatorData: bytes.Repeat([]byte{0xaa}, ownerwire.MinRegistrationDataBytes), RPID: rpID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A gate with a seat origin registers a credential only for its own relying
// party: a seat.pair whose registration names another is refused
// credential-invalid before the service, so the pairing code is not spent on
// it and no key that could never log in is paired. A registration for the
// gate's own relying party, and a pairing with no registration (which stays
// unverified), reach the service as before; so does any registration on a gate
// without an origin, which has no relying party of its own.
func TestAPairingForAnotherRelyingPartyIsRefused(t *testing.T) {
	f := &fakePort{open: true, code: pairCode}
	h, _, _ := newDeciding(t, f, &fakeSeat{})
	for _, rp := range []string{"other.example", "seat.example.evil", "example"} {
		if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatPair, registeredPair(t, rp)); codeOfErr(err) != CodeCredentialInvalid {
			t.Fatalf("%s: %v", rp, err)
		}
	}
	if n := len(f.seen()); n != 0 {
		t.Fatalf("the service had %d calls", n)
	}
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatPair, registeredPair(t, theGate.RPID)); err != nil {
		t.Fatalf("the gate's own relying party: %v", err)
	}
	if c := f.seen(); len(c) != 1 || c[0].pair.RPID != theGate.RPID {
		t.Fatalf("the service saw %+v", c)
	}
	f.mu.Lock()
	f.open = true
	f.mu.Unlock()
	if _, err := h.Handle(context.Background(), seat, ownerwire.OpSeatPair, validParams(t, ownerwire.OpSeatPair)); err != nil {
		t.Fatalf("no registration: %v", err)
	}

	g := &fakePort{open: true, code: pairCode}
	plain, _ := newHandler(t, g)
	if _, err := plain.Handle(context.Background(), seat, ownerwire.OpSeatPair, registeredPair(t, "other.example")); err != nil {
		t.Fatalf("a gate without an origin: %v", err)
	}
}
