//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/door/doortest"
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/approval/webauthntest"
	"ataniq/internal/clientapi"
)

// A made-up code, the form of a real one.
const pairTestCode = "7ZB0KX4M9TQ2VH6NRC3E"

// A made-up pending id, the form of a real one.
const pairTestID = "0f1e2d3c4b5a6978"

func pairPending() ownerwire.PairPendingParams {
	return ownerwire.PairPendingParams{PendingID: pairTestID}
}

func pairKey() ownerwire.KeyItem {
	return ownerwire.KeyItem{ID: "0123456789abcdef", Kind: "unverified", PairedAt: "2026-11-20T14:06:40Z"}
}

func pairWords() []string {
	return []string{"acorn", "bridge", "cedar", "delta", "ember", "falcon", "garden", "harbor"}
}

// The pairing writes, as the table of a test calls them over a connection.
var pairWrites = []struct {
	name   string
	op     string
	params any
}{
	{"pair-start", ownerwire.OpPairStart, ownerwire.PairStartParams{Replace: true}},
	{"pair-confirm", ownerwire.OpPairConfirm, ownerwire.PairPendingParams{PendingID: pairTestID}},
	{"pair-reject", ownerwire.OpPairReject, ownerwire.PairPendingParams{PendingID: pairTestID}},
	{"key-revoke", ownerwire.OpKeyRevoke, ownerwire.KeyRevokeParams{ID: "0123"}},
	{"keys-off", ownerwire.OpKeysOff, nil},
}

// fakePairService gives the fake the answers of a service that works.
func fakePairService(f *ownerFake) {
	f.pairStart = func(ownerchannel.Source, ownerwire.PairStartParams) (ownerwire.PairStartResult, error) {
		return ownerwire.PairStartResult{Code: pairTestCode, ExpiresAt: "2026-11-20T14:12:00Z"}, nil
	}
	f.pairStatus = func() (ownerwire.PairStatus, error) {
		return ownerwire.PairStatus{Phase: ownerwire.PairPending, Words: pairWords(), PendingID: pairTestID, ExpiresAt: "2026-11-20T14:12:00Z"}, nil
	}
	f.pairConfirm = func(ownerchannel.Source, ownerwire.PairPendingParams) (ownerwire.KeyItem, error) {
		return pairKey(), nil
	}
	f.keys = func() (ownerwire.KeysView, error) {
		return ownerwire.KeysView{Keys: []ownerwire.KeyItem{pairKey()}}, nil
	}
	f.keyRevoke = func(ownerchannel.Source, ownerwire.KeyRevokeParams) (ownerwire.KeysView, error) {
		k := pairKey()
		k.Revoked = true
		return ownerwire.KeysView{Keys: []ownerwire.KeyItem{k}}, nil
	}
	f.keysOff = func(ownerchannel.Source) (ownerwire.KeysView, error) {
		k := pairKey()
		k.Revoked = true
		return ownerwire.KeysView{Keys: []ownerwire.KeyItem{k}}, nil
	}
	f.pairPresent = func(ownerchannel.Source, ownerwire.SeatPairParams) (ownerwire.SeatPairResult, error) {
		return ownerwire.SeatPairResult{Words: pairWords()}, nil
	}
}

// TestOwnerThePairingOperationsReachTheService: each reaches the service with
// its params, and the writes with the decider's source; the answers are the
// wire's forms.
func TestOwnerThePairingOperationsReachTheService(t *testing.T) {
	r := newOwnerRig(t)
	fakePairService(r.svc)
	c := r.dial()
	r.mustBeDecider(c, ownerSession)

	var start ownerwire.PairStartResult
	if err := c.Call(ownerCtx(t), ownerwire.OpPairStart, ownerwire.PairStartParams{Replace: true}, &start); err != nil || start.Code != pairTestCode {
		t.Fatalf("pair-start: %v, %v", start, err)
	}
	var status ownerwire.PairStatus
	if err := c.Call(ownerCtx(t), ownerwire.OpPairStatus, nil, &status); err != nil || status.Phase != ownerwire.PairPending || len(status.Words) != ownerwire.PairWords || status.PendingID != pairTestID {
		t.Fatalf("pair-status: %+v, %v", status, err)
	}
	var key ownerwire.KeyItem
	if err := c.Call(ownerCtx(t), ownerwire.OpPairConfirm, pairPending(), &key); err != nil || key != pairKey() {
		t.Fatalf("pair-confirm: %+v, %v", key, err)
	}
	if err := c.Call(ownerCtx(t), ownerwire.OpPairReject, pairPending(), nil); err != nil {
		t.Fatalf("pair-reject: %v", err)
	}
	var keys ownerwire.KeysView
	if err := c.Call(ownerCtx(t), ownerwire.OpKeys, nil, &keys); err != nil || len(keys.Keys) != 1 || keys.Keys[0].Revoked {
		t.Fatalf("keys: %+v, %v", keys, err)
	}
	if err := c.Call(ownerCtx(t), ownerwire.OpKeyRevoke, ownerwire.KeyRevokeParams{ID: "0123"}, &keys); err != nil || !keys.Keys[0].Revoked {
		t.Fatalf("key-revoke: %+v, %v", keys, err)
	}
	keys = ownerwire.KeysView{}
	if err := c.Call(ownerCtx(t), ownerwire.OpKeysOff, nil, &keys); err != nil || !keys.Keys[0].Revoked {
		t.Fatalf("keys-off: %+v, %v", keys, err)
	}
	want := "pair-start replace=true,pair-status,pair-confirm " + pairTestID + ",pair-reject " + pairTestID + ",keys,key-revoke 0123,keys-off"
	if got := strings.Join(r.svc.called(), ","); got != want {
		t.Fatalf("the service was called as %q, want %q", got, want)
	}
	r.svc.mu.Lock()
	sources := append([]ownerchannel.Source(nil), r.svc.sources...)
	r.svc.mu.Unlock()
	if len(sources) != 5 {
		t.Fatalf("%d sources", len(sources))
	}
	for i, s := range sources {
		if s.Surface != "console" || !s.Decider || s.Session != ownerSession || s.PID != int64(os.Getpid()) || s.TTY != ownerTTY {
			t.Errorf("source %d: %+v", i, s)
		}
	}
	// A start with replace left out is replace false.
	if err := c.Call(ownerCtx(t), ownerwire.OpPairStart, map[string]any{"replace": false}, &start); err != nil {
		t.Fatal(err)
	}
	if got := r.svc.called(); got[len(got)-1] != "pair-start replace=false" {
		t.Fatalf("%v", got)
	}
}

// TestOwnerThePairingWritesAreForTheDeciderOnly: a connection that is not the
// decider is refused each of the five writes before its params are read,
// whatever they hold, the service is not called, the refusal counts against
// the same bound as a decision frame, and past it the connection is closed.
// Its reads are served.
func TestOwnerThePairingWritesAreForTheDeciderOnly(t *testing.T) {
	for _, w := range pairWrites {
		t.Run(w.name, func(t *testing.T) {
			r := newOwnerRig(t)
			fakePairService(r.svc)
			holder, c := r.dial(), r.dial()
			r.mustBeDecider(holder, ownerSession)
			if res, err := r.askDecider(c, ownerConsoleClient, ownerSession2); err != nil || res.Decider {
				t.Fatalf("%+v %v", res, err)
			}
			for i := 0; i < DecisionRefusalBound; i++ {
				// Params that do not read are refused for the place and not for
				// the form.
				err := c.Call(ownerCtx(t), w.op, map[string]any{"junk": pairTestCode}, nil)
				ce := wantOwnerCode(t, err, "not-decider")
				if ce.Message != msgOwnerNotDecide {
					t.Fatalf("message %q", ce.Message)
				}
			}
			if got := r.svc.called(); len(got) != 0 {
				t.Fatalf("the service was called: %v", got)
			}
			// The reads of a connection that is not the decider are served.
			var st ownerwire.PairStatus
			if err := c.Call(ownerCtx(t), ownerwire.OpPairStatus, nil, &st); err != nil {
				t.Fatalf("pair-status: %v", err)
			}
			var keys ownerwire.KeysView
			if err := c.Call(ownerCtx(t), ownerwire.OpKeys, nil, &keys); err != nil {
				t.Fatalf("keys: %v", err)
			}
			// The next write is over the bound: refused, and the connection ends.
			// (The answer may not reach the peer: the connection ends with it.)
			if err := c.Call(ownerCtx(t), w.op, w.params, nil); err == nil {
				t.Fatal("a write over the bound was taken")
			}
			if err := c.Call(ownerCtx(t), ownerwire.OpPairStatus, nil, &st); err == nil {
				t.Fatal("the connection was not closed past the bound")
			}
			r.wantCounts(map[string]uint64{"granted": 1, "second": 1, "pair-not-decider": DecisionRefusalBound + 1, "decision-flooded": 1})
			// The decider's own connection is not affected.
			if err := holder.Call(ownerCtx(t), w.op, w.params, nil); err != nil {
				t.Fatalf("the decider: %v", err)
			}
		})
	}
}

// TestOwnerAConnectionThatNeverAskedMayNotWrite: no owner.decider, no place.
func TestOwnerAConnectionThatNeverAskedMayNotWritePairings(t *testing.T) {
	r := newOwnerRig(t)
	fakePairService(r.svc)
	c := r.dial()
	for _, w := range pairWrites {
		wantOwnerCode(t, c.Call(ownerCtx(t), w.op, w.params, nil), "not-decider")
	}
	if got := r.svc.called(); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}

// TestOwnerThePairingParamsAreStrict: params the wire's decoders refuse are
// answered with their code, and the service is not called.
func TestOwnerThePairingParamsAreStrict(t *testing.T) {
	r := newOwnerRig(t)
	fakePairService(r.svc)
	c := r.dial()
	r.mustBeDecider(c, ownerSession)
	for _, tt := range []struct {
		op     string
		params any
		code   clientapi.ErrorCode
	}{
		{ownerwire.OpPairStart, map[string]any{}, "record-invalid"},
		{ownerwire.OpPairStart, map[string]any{"replace": 1}, "record-invalid"},
		{ownerwire.OpPairStart, map[string]any{"replace": true, "code": pairTestCode}, "record-invalid"},
		{ownerwire.OpKeyRevoke, map[string]any{"id": "01"}, "record-invalid"},
		{ownerwire.OpKeyRevoke, map[string]any{"id": "0123456789ABCDEF"}, "record-invalid"},
		{ownerwire.OpKeyRevoke, nil, "record-invalid"},
		{ownerwire.OpKeyRevoke, map[string]any{"id": "0123", "all": true}, "record-invalid"},
		{ownerwire.OpPairStatus, map[string]any{"x": 1}, clientapi.CodeInvalidRequest},
		{ownerwire.OpPairConfirm, map[string]any{"x": 1}, "record-invalid"},
		{ownerwire.OpPairConfirm, nil, "record-invalid"},
		{ownerwire.OpPairConfirm, map[string]any{}, "record-invalid"},
		{ownerwire.OpPairConfirm, map[string]any{"pending_id": "0F1E2D3C4B5A6978"}, "record-invalid"},
		{ownerwire.OpPairConfirm, map[string]any{"pending_id": pairTestID[:15]}, "record-invalid"},
		{ownerwire.OpPairConfirm, map[string]any{"pending_id": pairTestID, "replace": true}, "record-invalid"},
		{ownerwire.OpPairReject, map[string]any{"x": 1}, "record-invalid"},
		{ownerwire.OpPairReject, nil, "record-invalid"},
		{ownerwire.OpPairReject, map[string]any{"pending_id": ""}, "record-invalid"},
		{ownerwire.OpPairReject, map[string]any{"pending_id": pairTestID, "x": 1}, "record-invalid"},
		{ownerwire.OpKeys, map[string]any{"x": 1}, clientapi.CodeInvalidRequest},
		{ownerwire.OpKeysOff, map[string]any{"x": 1}, clientapi.CodeInvalidRequest},
	} {
		wantOwnerCode(t, c.Call(ownerCtx(t), tt.op, tt.params, nil), tt.code)
	}
	if got := r.svc.called(); len(got) != 0 {
		t.Fatalf("the service was called: %v", got)
	}
}

// TestOwnerThePairingErrorsHaveFixedCodes: each error of the service is a code
// of its own with a fixed text that holds nothing the error said or the peer
// sent; a cause that names a path is in the log only.
func TestOwnerThePairingErrorsHaveFixedCodes(t *testing.T) {
	r := newOwnerRig(t)
	fakePairService(r.svc)
	c := r.dial()
	r.mustBeDecider(c, ownerSession)
	for _, tt := range []struct {
		name string
		err  error
		code clientapi.ErrorCode
		text string
	}{
		{"keys unavailable", errors.Join(ownerchannel.ErrKeysUnavailable, errors.New("open /home/someone/secret/keys.log: denied")), "keys-unavailable", msgOwnerKeysUnavailable},
		{"keys full", ownerchannel.ErrKeysFull, "keys-full", msgOwnerKeysFull},
		{"key unknown", ownerchannel.ErrKeyUnknown, "key-unknown", msgOwnerKeyUnknown},
		{"key ambiguous", ownerchannel.ErrKeyAmbiguous, "key-ambiguous", msgOwnerKeyAmbiguous},
		{"pairing open", ownerchannel.ErrPairingOpen, "pairing-open", msgOwnerPairingOpen},
		{"no pairing", ownerchannel.ErrNoPairing, "pairing-none", msgOwnerPairingNone},
		{"pairing expired", ownerchannel.ErrPairingExpired, "pairing-expired", msgOwnerPairingExpired},
		{"pairing changed", ownerchannel.ErrPairingChanged, "pairing-changed", msgOwnerPairingChanged},
		{"pairing refused", ownerchannel.ErrPairRefused, "pairing-refused", msgOwnerPairingRefused},
		{"credential invalid", ownerchannel.ErrCredentialInvalid, "credential-invalid", msgOwnerCredentialInvalid},
		{"not decider", &record.Error{Code: record.CodeNotDecider, Detail: "this connection is not the one that may decide"}, "not-decider", "this connection is not the one that may decide"},
		{"owner away", &record.Error{Code: record.CodeOwnerAway, Detail: "no activity of the owner inside the presence window"}, "owner-away", "no activity of the owner inside the presence window"},
		{"a stopped service", ownerchannel.ErrStopped, "unavailable", msgOwnerStopped},
		{"anything else", errors.New("open /home/someone/secret: denied"), "failed", msgOwnerFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r.svc.pairStart = func(ownerchannel.Source, ownerwire.PairStartParams) (ownerwire.PairStartResult, error) {
				return ownerwire.PairStartResult{}, tt.err
			}
			r.svc.pairConfirm = func(ownerchannel.Source, ownerwire.PairPendingParams) (ownerwire.KeyItem, error) {
				return ownerwire.KeyItem{}, tt.err
			}
			r.svc.keyRevoke = func(ownerchannel.Source, ownerwire.KeyRevokeParams) (ownerwire.KeysView, error) {
				return ownerwire.KeysView{}, tt.err
			}
			r.svc.keys = func() (ownerwire.KeysView, error) { return ownerwire.KeysView{}, tt.err }
			for _, op := range []struct {
				name   string
				op     string
				params any
			}{
				{"start", ownerwire.OpPairStart, ownerwire.PairStartParams{}},
				{"confirm", ownerwire.OpPairConfirm, pairPending()},
				{"revoke", ownerwire.OpKeyRevoke, ownerwire.KeyRevokeParams{ID: "0123"}},
				{"keys", ownerwire.OpKeys, nil},
			} {
				err := c.Call(ownerCtx(t), op.op, op.params, nil)
				ce := wantOwnerCode(t, err, tt.code)
				if ce.Message != tt.text {
					t.Fatalf("%s: message %q, want %q", op.name, ce.Message, tt.text)
				}
				if strings.Contains(ce.Message, "/home/") || strings.Contains(ce.Message, pairTestCode) {
					t.Fatalf("the answer holds what the error said: %q", ce.Message)
				}
			}
		})
	}
	if !strings.Contains(r.logs.String(), "open /home/someone/secret/keys.log: denied") {
		t.Fatalf("the log lacks the cause of the list:\n%s", r.logs.String())
	}
	if strings.Contains(r.logs.String(), pairTestCode) {
		t.Fatalf("the code is in the log:\n%s", r.logs.String())
	}
}

// TestOwnerTheCodeIsNotLogged: a start whose answer holds the code, and the
// other calls, leave it out of the daemon's log.
func TestOwnerTheCodeIsNotLogged(t *testing.T) {
	r := newOwnerRig(t)
	fakePairService(r.svc)
	c := r.dial()
	r.mustBeDecider(c, ownerSession)
	var start ownerwire.PairStartResult
	if err := c.Call(ownerCtx(t), ownerwire.OpPairStart, ownerwire.PairStartParams{}, &start); err != nil || start.Code != pairTestCode {
		t.Fatalf("%+v %v", start, err)
	}
	var st ownerwire.PairStatus
	if err := c.Call(ownerCtx(t), ownerwire.OpPairStatus, nil, &st); err != nil {
		t.Fatal(err)
	}
	// A result the wire refuses is not sent, and the log does not say why with
	// the value.
	r.svc.pairStart = func(ownerchannel.Source, ownerwire.PairStartParams) (ownerwire.PairStartResult, error) {
		return ownerwire.PairStartResult{Code: "I" + pairTestCode[1:], ExpiresAt: "2026-11-20T14:12:00Z"}, nil
	}
	err := c.Call(ownerCtx(t), ownerwire.OpPairStart, ownerwire.PairStartParams{}, &start)
	wantOwnerCode(t, err, "record-invalid")
	if strings.Contains(r.logs.String(), pairTestCode[1:]) || strings.Contains(r.logs.String(), pairTestCode) {
		t.Fatalf("the code is in the log:\n%s", r.logs.String())
	}
}

// TestOwnerSeatPairIsRefusedToEveryConnection: this build has no seat source,
// so seat.pair is "unavailable" to the decider, to a console that is not, and
// to a connection that never asked, whatever its params (a valid call, a
// call that does not read, none at all). The service is not called, so no try
// of a code is spent, and neither the params nor the code reach the log.
func TestOwnerSeatPairIsRefusedToEveryConnection(t *testing.T) {
	r := newOwnerRig(t)
	fakePairService(r.svc)
	decider, other, fresh := r.dial(), r.dial(), r.dial()
	r.mustBeDecider(decider, ownerSession)
	if res, err := r.askDecider(other, ownerConsoleClient, ownerSession2); err != nil || res.Decider {
		t.Fatalf("%+v %v", res, err)
	}
	valid := ownerwire.SeatPairParams{Code: pairTestCode, CredentialID: make([]byte, 32), PublicKey: make([]byte, 91)}
	n := 0
	for name, c := range map[string]*clientapi.Client{"the decider": decider, "a second console": other, "a connection that never asked": fresh} {
		for what, params := range map[string]any{
			"valid params": valid, "params that do not read": map[string]any{"code": pairTestCode, "x": 1}, "no params": nil,
		} {
			err := c.Call(ownerCtx(t), ownerwire.OpSeatPair, params, nil)
			ce := wantOwnerCode(t, err, "unavailable")
			if ce.Message != msgOwnerNoSeat {
				t.Fatalf("%s, %s: message %q", name, what, ce.Message)
			}
			n++
		}
	}
	if got := r.svc.called(); len(got) != 0 {
		t.Fatalf("the service was called: %v", got)
	}
	if got := r.oc.Counts().SeatRefused.Load(); got != uint64(n) {
		t.Fatalf("%d refusals counted, want %d", got, n)
	}
	if strings.Contains(r.logs.String(), pairTestCode) {
		t.Fatalf("the code is in the log:\n%s", r.logs.String())
	}
	// The refusals took no part of the pairing: nothing else was counted.
	r.wantCounts(map[string]uint64{"granted": 1, "second": 1, "seat-refused": uint64(n)})
}

// TestOwnerSeatPairReachesTheServiceOnceThereIsASource: with a seat source (a
// test's own; none exists in a build) the call is read strictly, handed to the
// service with the seat's source, and answered with the wire's form; the
// errors have their codes.
func TestOwnerSeatPairReachesTheServiceOnceThereIsASource(t *testing.T) {
	r := newUnstartedOwnerRig(t)
	fakePairService(r.svc)
	seat := ownerchannel.Source{Surface: ownerchannel.SurfaceSeat, Session: ownerSession2}
	r.oc.seat = func(v ownerView) (ownerchannel.Source, bool) { return seat, v.connection != nil }
	r.run = r.e.startDaemon(r.config())
	c := r.dial()
	valid := ownerwire.SeatPairParams{Code: pairTestCode, CredentialID: make([]byte, 32), PublicKey: make([]byte, 91)}
	var res ownerwire.SeatPairResult
	if err := c.Call(ownerCtx(t), ownerwire.OpSeatPair, valid, &res); err != nil || strings.Join(res.Words, " ") != strings.Join(pairWords(), " ") {
		t.Fatalf("%+v %v", res, err)
	}
	r.svc.mu.Lock()
	sources := append([]ownerchannel.Source(nil), r.svc.sources...)
	r.svc.mu.Unlock()
	if len(sources) != 1 || sources[0] != seat {
		t.Fatalf("the service saw %+v, want the seat's source", sources)
	}
	// Params that do not read are refused by the wire, and the service is not asked.
	for _, params := range []any{
		map[string]any{"code": pairTestCode},
		map[string]any{"code": "short", "credential_id": valid.CredentialID, "public_key": valid.PublicKey},
		map[string]any{"code": pairTestCode, "credential_id": valid.CredentialID, "public_key": valid.PublicKey, "kind": "x"},
	} {
		wantOwnerCode(t, c.Call(ownerCtx(t), ownerwire.OpSeatPair, params, nil), "record-invalid")
	}
	if got := len(r.svc.called()); got != 1 {
		t.Fatalf("the service was called %d times", got)
	}
	for _, tt := range []struct {
		err  error
		code clientapi.ErrorCode
	}{
		{ownerchannel.ErrPairRefused, "pairing-refused"},
		{ownerchannel.ErrCredentialInvalid, "credential-invalid"},
		{ownerchannel.ErrKeysUnavailable, "keys-unavailable"},
		{ownerchannel.ErrNotSeat, "unavailable"},
	} {
		r.svc.pairPresent = func(ownerchannel.Source, ownerwire.SeatPairParams) (ownerwire.SeatPairResult, error) {
			return ownerwire.SeatPairResult{}, tt.err
		}
		wantOwnerCode(t, c.Call(ownerCtx(t), ownerwire.OpSeatPair, valid, nil), tt.code)
	}
	if strings.Contains(r.logs.String(), pairTestCode) {
		t.Fatalf("the code is in the log:\n%s", r.logs.String())
	}
}

// stubs for the parts of a real service that the pairing does not touch.
var errStubbed = errors.New("stubbed")

type stubMerger struct{}

func (stubMerger) Build(context.Context, ownerchannel.MergeInput) (ownerchannel.BuiltMerge, error) {
	return ownerchannel.BuiltMerge{}, errStubbed
}
func (stubMerger) Pinned(context.Context, string) (string, error) { return "", nil }
func (stubMerger) Unpin(context.Context, string, string) error    { return nil }

type stubVerifier struct{}

func (stubVerifier) Verify(context.Context, ownerchannel.VerifyInput) (ownerchannel.Verification, error) {
	return ownerchannel.Verification{}, errStubbed
}

type stubIntake struct{}

func (stubIntake) Resolve(context.Context, ownerchannel.ResolveInput) (ownerchannel.Resolved, error) {
	return ownerchannel.Resolved{}, errStubbed
}
func (stubIntake) Review(context.Context, ownerchannel.ReviewInput) (record.Summary, error) {
	return record.Summary{}, errStubbed
}
func (stubIntake) TargetTip(context.Context, string, string) (string, error) { return "", errStubbed }
func (stubIntake) Paths(context.Context, ownerchannel.ReviewRef) (ownerchannel.ReviewPaths, error) {
	return ownerchannel.ReviewPaths{}, errStubbed
}
func (stubIntake) Patch(context.Context, ownerchannel.ReviewRef) (ownerchannel.ReviewPatch, error) {
	return ownerchannel.ReviewPatch{}, errStubbed
}
func (stubIntake) Fresh(context.Context, ownerchannel.FreshInput) (door.Fresh, error) {
	return door.Fresh{}, errStubbed
}
func (stubIntake) Apply(context.Context, ownerchannel.ApplyInput) (door.Outcome, error) {
	return "", errStubbed
}
func (stubIntake) Mirror(context.Context, ownerchannel.ApplyInput) (door.Outcome, error) {
	return "", errStubbed
}

type stubMailbox struct{}

func (stubMailbox) Reply(context.Context, ownerchannel.Reply) error { return nil }

// realPairService builds the real service over a real journal in a temporary
// folder, the stand-in door, stubs for the merge, the intake and the suite, and
// the list of keys the caller gives (nil for none). The service is closed with
// the test; it closes the list.
func realPairService(t *testing.T, state string, keys ownerchannel.KeyStore) *ownerchannel.Service {
	t.Helper()
	j, err := ownerjournal.Open(filepath.Join(state, "owner.db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := doortest.Sample(time.Now)
	svc, err := ownerchannel.New(context.Background(), ownerchannel.Config{
		Journal: j, Door: doortest.New(cfg), Merger: stubMerger{}, Intake: stubIntake{}, Verifier: stubVerifier{},
		Mailbox: stubMailbox{}, Now: time.Now, Keys: keys,
	})
	if err != nil {
		j.Close()
		t.Fatal(err)
	}
	return svc
}

// realPairRig starts the daemon on a real service over the list of keys in
// the file at keysPath, as the command does: a list that cannot be opened is
// no list, and the service then refuses every operation on it.
func realPairRig(t *testing.T, state string, keysPath string) (*ownerRig, *ownerchannel.Service) {
	t.Helper()
	var keys ownerchannel.KeyStore
	if k, err := ownerjournal.OpenKeys(keysPath); err == nil {
		keys = k
	}
	svc := realPairService(t, state, keys)
	r := newUnstartedOwnerRig(t)
	r.oc.Build = func() (OwnerService, func() error, error) { return svc, svc.Close, nil }
	r.run = r.e.startDaemon(r.config())
	return r, svc
}

func newPhoneKey(t *testing.T) (id, spki []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err = x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	id = make([]byte, 32)
	if _, err := rand.Read(id); err != nil {
		t.Fatal(err)
	}
	return id, spki
}

func privateState(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestOwnerAPhoneIsPairedEndToEnd: through the daemon, the real service and a
// real list of keys in a file: the console starts a pairing and gets the code,
// seat.pair is refused (no seat source), the credential is presented to the
// service as the seat's call would, with its registration, the console reads
// the eight words and the claimed kind, confirms, reads the list, revokes by a
// prefix, and switches the phone off. The list, with the kind and the claims,
// is still there after the daemon is started again on the same file. A second
// pairing, without a registration, is of the kind unverified, and is rejected.
func TestOwnerAPhoneIsPairedEndToEnd(t *testing.T) {
	state := privateState(t)
	keysPath := filepath.Join(state, ownerjournal.KeysFileName)
	r, svc := realPairRig(t, state, keysPath)
	c := r.dial()
	r.mustBeDecider(c, ownerSession)
	beat := func() {
		t.Helper()
		if err := c.Call(ownerCtx(t), ownerwire.OpBeat, nil, nil); err != nil {
			t.Fatal(err)
		}
	}

	// A write of an owner who has not been seen is refused owner-away.
	wantOwnerCode(t, c.Call(ownerCtx(t), ownerwire.OpPairStart, ownerwire.PairStartParams{}, nil), "owner-away")
	beat()
	var start ownerwire.PairStartResult
	if err := c.Call(ownerCtx(t), ownerwire.OpPairStart, ownerwire.PairStartParams{}, &start); err != nil {
		t.Fatalf("pair-start: %v", err)
	}
	if len(start.Code) != ownerwire.PairCodeLength {
		t.Fatalf("the code is %q", start.Code)
	}
	wantOwnerCode(t, c.Call(ownerCtx(t), ownerwire.OpPairStart, ownerwire.PairStartParams{}, nil), "pairing-open")
	var st ownerwire.PairStatus
	if err := c.Call(ownerCtx(t), ownerwire.OpPairStatus, nil, &st); err != nil || st.Phase != ownerwire.PairWaiting {
		t.Fatalf("%+v %v", st, err)
	}
	if b, _ := ownerwire.EncodePairStatus(st); strings.Contains(string(b), start.Code) {
		t.Fatalf("the status holds the code: %s", b)
	}

	phone, err := webauthntest.New("seat.example.test", "https://seat.example.test")
	if err != nil {
		t.Fatal(err)
	}
	// A synced passkey of a vault, by the AAGUID it claims.
	phone.AAGUID = [16]byte{0xd5, 0x48, 0x82, 0x6e, 0x79, 0xb4, 0xdb, 0x40, 0xa3, 0xd8, 0x11, 0x11, 0x6f, 0x7e, 0x83, 0x49}
	phone.Flags |= webauthntest.FlagBE | webauthntest.FlagBS
	over := ownerwire.SeatPairParams{
		Code: start.Code, CredentialID: phone.CredentialID, PublicKey: phone.SPKI(),
		AuthenticatorData: phone.RegistrationAuthData(), RPID: "seat.example.test",
	}
	wantOwnerCode(t, c.Call(ownerCtx(t), ownerwire.OpSeatPair, over, nil), "unavailable")
	if err := c.Call(ownerCtx(t), ownerwire.OpPairConfirm, pairPending(), nil); err == nil {
		t.Fatal("a confirm with nothing pending")
	} else {
		wantOwnerCode(t, err, "pairing-none")
	}
	presented, err := svc.PairPresent(context.Background(), ownerchannel.Source{Surface: ownerchannel.SurfaceSeat, Session: ownerSession2}, over)
	if err != nil {
		t.Fatalf("PairPresent: %v", err)
	}
	if err := c.Call(ownerCtx(t), ownerwire.OpPairStatus, nil, &st); err != nil || st.Phase != ownerwire.PairPending ||
		strings.Join(st.Words, " ") != strings.Join(presented.Words, " ") {
		t.Fatalf("%+v %v", st, err)
	}
	if st.Kind != "vault-passkey" || st.AAGUID != "d548826e-79b4-db40-a3d8-11116f7e8349" || !st.BackupEligible || !st.BackupState {
		t.Fatalf("the status shows no claim: %+v", st)
	}
	beat()
	if len(st.PendingID) != ownerwire.PendingIDLength {
		t.Fatalf("the status shows no pending id: %+v", st)
	}
	// A word about another credential changes nothing; the one that names this
	// credential confirms it.
	wrong := ownerwire.PairPendingParams{PendingID: "ffffffffffffffff"}
	if st.PendingID == wrong.PendingID {
		wrong.PendingID = "eeeeeeeeeeeeeeee"
	}
	wantOwnerCode(t, c.Call(ownerCtx(t), ownerwire.OpPairConfirm, wrong, nil), "pairing-changed")
	wantOwnerCode(t, c.Call(ownerCtx(t), ownerwire.OpPairReject, wrong, nil), "pairing-changed")
	var key ownerwire.KeyItem
	if err := c.Call(ownerCtx(t), ownerwire.OpPairConfirm, ownerwire.PairPendingParams{PendingID: st.PendingID}, &key); err != nil {
		t.Fatalf("pair-confirm: %v", err)
	}
	if key.Kind != "vault-passkey" || key.AAGUID != st.AAGUID || !key.BackupEligible || !key.BackupState ||
		key.Revoked || len(key.ID) != ownerwire.KeyIDLength {
		t.Fatalf("%+v", key)
	}
	var keys ownerwire.KeysView
	if err := c.Call(ownerCtx(t), ownerwire.OpKeys, nil, &keys); err != nil || len(keys.Keys) != 1 || keys.Keys[0] != key {
		t.Fatalf("%+v %v", keys, err)
	}

	// A second pairing, rejected.
	id2, spki2 := newPhoneKey(t)
	beat()
	if err := c.Call(ownerCtx(t), ownerwire.OpPairStart, ownerwire.PairStartParams{}, &start); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PairPresent(context.Background(), ownerchannel.Source{Surface: ownerchannel.SurfaceSeat, Session: ownerSession2},
		ownerwire.SeatPairParams{Code: start.Code, CredentialID: id2, PublicKey: spki2}); err != nil {
		t.Fatal(err)
	}
	beat()
	// A fresh value: a decoder leaves a member the answer omits as it was.
	st = ownerwire.PairStatus{}
	if err := c.Call(ownerCtx(t), ownerwire.OpPairStatus, nil, &st); err != nil || st.Phase != ownerwire.PairPending || st.Kind != "" {
		t.Fatalf("%+v %v", st, err)
	}
	if err := c.Call(ownerCtx(t), ownerwire.OpPairReject, ownerwire.PairPendingParams{PendingID: st.PendingID}, nil); err != nil {
		t.Fatalf("pair-reject: %v", err)
	}
	if err := c.Call(ownerCtx(t), ownerwire.OpKeys, nil, &keys); err != nil || len(keys.Keys) != 1 {
		t.Fatalf("%+v %v", keys, err)
	}

	// The daemon ends and starts again on the same file: the key is there, and
	// the pairing that was not finished is not.
	if err := r.run.stop(); err != nil {
		t.Fatal(err)
	}
	r2, _ := realPairRig(t, state, keysPath)
	c2 := r2.dial()
	r2.mustBeDecider(c2, ownerSession)
	var after ownerwire.KeysView
	if err := c2.Call(ownerCtx(t), ownerwire.OpKeys, nil, &after); err != nil || len(after.Keys) != 1 || after.Keys[0] != key {
		t.Fatalf("after a restart: %+v %v", after, err)
	}
	if err := c2.Call(ownerCtx(t), ownerwire.OpPairStatus, nil, &st); err != nil || st.Phase != ownerwire.PairNone {
		t.Fatalf("%+v %v", st, err)
	}
	// Revoke by a prefix (no presence needed), then phone off.
	if err := c2.Call(ownerCtx(t), ownerwire.OpKeyRevoke, ownerwire.KeyRevokeParams{ID: key.ID[:5]}, &after); err != nil || !after.Keys[0].Revoked {
		t.Fatalf("%+v %v", after, err)
	}
	wantOwnerCode(t, c2.Call(ownerCtx(t), ownerwire.OpKeyRevoke, ownerwire.KeyRevokeParams{ID: "0000"}, nil), "key-unknown")
	if err := c2.Call(ownerCtx(t), ownerwire.OpKeysOff, nil, &after); err != nil || len(after.Keys) != 1 || !after.Keys[0].Revoked {
		t.Fatalf("%+v %v", after, err)
	}
	if err := r2.run.stop(); err != nil {
		t.Fatal(err)
	}
	// And revoked is revoked after another start.
	r3, _ := realPairRig(t, state, keysPath)
	c3 := r3.dial()
	if err := c3.Call(ownerCtx(t), ownerwire.OpKeys, nil, &after); err != nil || !after.Keys[0].Revoked {
		t.Fatalf("%+v %v", after, err)
	}
}

// TestOwnerADamagedListOfKeysRefusesTheOperations: the command opens the list
// and gives the service none when it cannot; the daemon then answers every
// operation on the list with keys-unavailable and a fixed text, serves the rest,
// and the file is left as it was.
func TestOwnerADamagedListOfKeysRefusesTheOperations(t *testing.T) {
	state := privateState(t)
	keysPath := filepath.Join(state, ownerjournal.KeysFileName)
	k, err := ownerjournal.OpenKeys(keysPath)
	if err != nil {
		t.Fatal(err)
	}
	k.Close()
	if err := os.WriteFile(keysPath, []byte("not a list of keys\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(keysPath)
	r, _ := realPairRig(t, state, keysPath)
	c := r.dial()
	r.mustBeDecider(c, ownerSession)
	if err := c.Call(ownerCtx(t), ownerwire.OpBeat, nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, w := range append(pairWrites, struct {
		name   string
		op     string
		params any
	}{"keys", ownerwire.OpKeys, nil}) {
		err := c.Call(ownerCtx(t), w.op, w.params, nil)
		ce := wantOwnerCode(t, err, "keys-unavailable")
		if ce.Message != msgOwnerKeysUnavailable {
			t.Errorf("%s: message %q", w.name, ce.Message)
		}
	}
	// The pairing status needs no list, and the rest of the owner connection works.
	var st ownerwire.PairStatus
	if err := c.Call(ownerCtx(t), ownerwire.OpPairStatus, nil, &st); err != nil || st.Phase != ownerwire.PairNone {
		t.Fatalf("%+v %v", st, err)
	}
	var listing ownerwire.Listing
	if err := c.Call(ownerCtx(t), ownerwire.OpList, nil, &listing); err != nil {
		t.Fatalf("list: %v", err)
	}
	if after, _ := os.ReadFile(keysPath); string(after) != string(before) {
		t.Fatal("the damaged file was changed")
	}
}
