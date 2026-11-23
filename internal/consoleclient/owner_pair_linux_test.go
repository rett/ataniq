package consoleclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/clientapi"
)

// A made-up code of the form of a real one.
const pairCode = "7ZB0KX4M9TQ2VH6NRC3E"

// A made-up pending id of the form of a real one.
const pairID = "0f1e2d3c4b5a6978"

var pairWords = []string{"acorn", "bridge", "cedar", "delta", "ember", "falcon", "garden", "harbor"}

func aKey(revoked bool) ownerwire.KeyItem {
	return ownerwire.KeyItem{ID: "0123456789abcdef", Kind: "unverified", PairedAt: "2026-11-20T14:06:40Z", Revoked: revoked}
}

func keysResult(t *testing.T, items ...ownerwire.KeyItem) []byte {
	t.Helper()
	if items == nil {
		items = []ownerwire.KeyItem{}
	}
	return mustEncode(t)(ownerwire.EncodeKeysView(ownerwire.KeysView{Keys: items}))
}

// pairingFake is a daemon with the owner channel, answering the pairing
// operations with well-formed results.
func pairingFake(t *testing.T) (*ownerFake, *Owner) {
	t.Helper()
	f := startOwner(t)
	o := newOwner(t, f.dir)
	f.onResult(ownerwire.OpPairStart, mustEncode(t)(ownerwire.EncodePairStartResult(
		ownerwire.PairStartResult{Code: pairCode, ExpiresAt: "2026-11-20T14:12:00Z"})))
	f.onResult(ownerwire.OpPairStatus, mustEncode(t)(ownerwire.EncodePairStatus(
		ownerwire.PairStatus{Phase: ownerwire.PairPending, Words: pairWords, PendingID: pairID, ExpiresAt: "2026-11-20T14:12:00Z"})))
	f.onResult(ownerwire.OpPairConfirm, mustEncode(t)(ownerwire.EncodeKeyItem(aKey(false))))
	f.onResult(ownerwire.OpPairReject, ownerwire.EncodeNothing())
	f.onResult(ownerwire.OpKeys, keysResult(t, aKey(false)))
	f.onResult(ownerwire.OpKeyRevoke, keysResult(t, aKey(true)))
	f.onResult(ownerwire.OpKeysOff, keysResult(t, aKey(true)))
	return f, o
}

func TestThePairingOperationsSendTheirParamsAndReturnTheWireTypes(t *testing.T) {
	f, o := pairingFake(t)

	start, err := o.PairStart(true)
	if err != nil || start.Code != pairCode || start.ExpiresAt != "2026-11-20T14:12:00Z" {
		t.Fatalf("PairStart = %v, %v", start, err)
	}
	st, err := o.PairStatus()
	if err != nil || st.Phase != ownerwire.PairPending || strings.Join(st.Words, " ") != strings.Join(pairWords, " ") || st.PendingID != pairID {
		t.Fatalf("PairStatus = %+v, %v", st, err)
	}
	k, err := o.PairConfirm(pairID)
	if err != nil || k != aKey(false) {
		t.Fatalf("PairConfirm = %+v, %v", k, err)
	}
	if err := o.PairReject(pairID); err != nil {
		t.Fatalf("PairReject: %v", err)
	}
	keys, err := o.Keys()
	if err != nil || len(keys.Keys) != 1 || keys.Keys[0] != aKey(false) {
		t.Fatalf("Keys = %+v, %v", keys, err)
	}
	keys, err = o.KeyRevoke("0123")
	if err != nil || !keys.Keys[0].Revoked {
		t.Fatalf("KeyRevoke = %+v, %v", keys, err)
	}
	keys, err = o.KeysOff()
	if err != nil || !keys.Keys[0].Revoked {
		t.Fatalf("KeysOff = %+v, %v", keys, err)
	}

	// Each went out once, with the params the wire writes and none at all for
	// the operations that take none.
	for op, want := range map[string]string{
		ownerwire.OpPairStart:   `{"replace":true}`,
		ownerwire.OpPairStatus:  "",
		ownerwire.OpPairConfirm: `{"pending_id":"` + pairID + `"}`,
		ownerwire.OpPairReject:  `{"pending_id":"` + pairID + `"}`,
		ownerwire.OpKeys:        "",
		ownerwire.OpKeyRevoke:   `{"id":"0123"}`,
		ownerwire.OpKeysOff:     "",
	} {
		calls := f.of(op)
		if len(calls) != 1 {
			t.Errorf("%s was sent %d times", op, len(calls))
			continue
		}
		if calls[0].params != want {
			t.Errorf("%s was sent %q, want %q", op, calls[0].params, want)
		}
	}
	// The start without replace says so.
	if _, err := o.PairStart(false); err != nil {
		t.Fatal(err)
	}
	if got := f.of(ownerwire.OpPairStart); got[1].params != `{"replace":false}` {
		t.Fatalf("%q", got[1].params)
	}
	// The result prints without the code.
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		if out := fmt.Sprintf(verb, start); strings.Contains(out, pairCode) {
			t.Errorf("%s prints the code: %s", verb, out)
		}
	}
}

// A pending id the wire refuses is not sent.
func TestAPendingIdTheWireRefusesIsNotSent(t *testing.T) {
	f, o := pairingFake(t)
	for _, id := range []string{"", "0f1e2d3c4b5a697", "0f1e2d3c4b5a69780", "0F1E2D3C4B5A6978", "0f1e2d3c4b5a697g", "0f1e 2d3c4b5a697"} {
		for name, call := range map[string]func() error{
			"PairConfirm": func() error { _, err := o.PairConfirm(id); return err },
			"PairReject":  func() error { return o.PairReject(id) },
		} {
			err := call()
			var rec *record.Error
			if !errors.As(err, &rec) || rec.Code != record.CodeRecordInvalid {
				t.Errorf("%s(%q) = %v", name, id, err)
			}
		}
	}
	if n := f.count(ownerwire.OpPairConfirm) + f.count(ownerwire.OpPairReject); n != 0 {
		t.Fatalf("%d requests were sent", n)
	}
}

// The daemon's word that the credential is not the one named is a PairError
// of its own, and the pairing's other refusals keep theirs.
func TestAPairingChangedAnswerIsItsOwnError(t *testing.T) {
	f, o := pairingFake(t)
	f.refuse(ownerwire.OpPairConfirm, "pairing-changed", "x")
	_, err := o.PairConfirm(pairID)
	var pe *PairError
	if !errors.As(err, &pe) || pe.Code != PairPairingChanged || PairPairingChanged != "pairing-changed" {
		t.Fatalf("PairConfirm: %v", err)
	}
	f.refuse(ownerwire.OpPairReject, "pairing-changed", "x")
	if err := o.PairReject(pairID); !errors.As(err, &pe) || pe.Code != PairPairingChanged {
		t.Fatalf("PairReject: %v", err)
	}
}

// What the wire refuses is not sent.
func TestAnIdTheWireRefusesIsNotSent(t *testing.T) {
	f, o := pairingFake(t)
	for _, id := range []string{"", "012", "0123456789abcdef0", "0123456789ABCDEF", "xyz1", "01 2345"} {
		_, err := o.KeyRevoke(id)
		var rec *record.Error
		if !errors.As(err, &rec) || rec.Code != record.CodeRecordInvalid {
			t.Errorf("KeyRevoke(%q) = %v", id, err)
		}
	}
	if n := f.count(ownerwire.OpKeyRevoke); n != 0 {
		t.Fatalf("%d requests were sent", n)
	}
}

func TestEachPairingCodeOfTheDaemonHasItsFixedText(t *testing.T) {
	f, o := pairingFake(t)
	const hostile = "‮EVIL​ /home/someone/secret " + pairCode
	calls := map[string]func() error{
		ownerwire.OpPairStart:   func() error { _, err := o.PairStart(false); return err },
		ownerwire.OpPairStatus:  func() error { _, err := o.PairStatus(); return err },
		ownerwire.OpPairConfirm: func() error { _, err := o.PairConfirm(pairID); return err },
		ownerwire.OpPairReject:  func() error { return o.PairReject(pairID) },
		ownerwire.OpKeys:        func() error { _, err := o.Keys(); return err },
		ownerwire.OpKeyRevoke:   func() error { _, err := o.KeyRevoke("0123"); return err },
		ownerwire.OpKeysOff:     func() error { _, err := o.KeysOff(); return err },
	}
	for code := range pairTexts {
		for op, call := range calls {
			f.refuse(op, clientapi.ErrorCode(code), hostile)
			err := call()
			var pe *PairError
			if !errors.As(err, &pe) || pe.Code != code {
				t.Errorf("%s with %s: %v", op, code, err)
				continue
			}
			if pe.Error() != pairTexts[code] || pe.Error() == "" {
				t.Errorf("%s: the text is %q", code, pe.Error())
			}
			if strings.ContainsAny(pe.Error(), "‮​") || strings.Contains(pe.Error(), "someone") || strings.Contains(pe.Error(), pairCode) {
				t.Errorf("the text holds what the daemon said: %q", pe.Error())
			}
		}
	}
	// The codes of the connection itself keep their form.
	for code, want := range map[string]record.Code{
		"not-decider": record.CodeNotDecider, "owner-away": record.CodeOwnerAway, "record-invalid": record.CodeRecordInvalid,
	} {
		f.refuse(ownerwire.OpPairStart, clientapi.ErrorCode(code), hostile)
		_, err := o.PairStart(false)
		var rec *record.Error
		if !errors.As(err, &rec) || rec.Code != want || rec.Detail != "" {
			t.Errorf("%s: %v", code, err)
		}
	}
	// A code that is no one's is an unavailable daemon, with no text of its own.
	f.refuse(ownerwire.OpPairStart, "pairing-whatever", hostile)
	_, err := o.PairStart(false)
	var u *Unavailable
	if !errors.As(err, &u) || strings.Contains(err.Error(), "someone") {
		t.Errorf("an unknown code: %v", err)
	}
	// Every code of the daemon is one the client knows.
	for _, code := range []string{"keys-unavailable", "keys-full", "key-unknown", "key-ambiguous", "pairing-open",
		"pairing-none", "pairing-expired", "pairing-changed", "pairing-refused", "credential-invalid"} {
		if _, ok := pairTexts[code]; !ok {
			t.Errorf("the code %s has no text", code)
		}
	}
	if len(pairTexts) != 10 {
		t.Errorf("%d texts", len(pairTexts))
	}
}

// An answer the wire refuses is a bad answer, the connection is dropped, and
// the next call makes a new one.
func TestAnAnswerTheWireRefusesIsABadAnswer(t *testing.T) {
	f, o := pairingFake(t)
	bad := map[string]func() error{
		ownerwire.OpPairStart:   func() error { _, err := o.PairStart(false); return err },
		ownerwire.OpPairStatus:  func() error { _, err := o.PairStatus(); return err },
		ownerwire.OpPairConfirm: func() error { _, err := o.PairConfirm(pairID); return err },
		ownerwire.OpPairReject:  func() error { return o.PairReject(pairID) },
		ownerwire.OpKeys:        func() error { _, err := o.Keys(); return err },
		ownerwire.OpKeyRevoke:   func() error { _, err := o.KeyRevoke("0123"); return err },
		ownerwire.OpKeysOff:     func() error { _, err := o.KeysOff(); return err },
	}
	answers := map[string][]string{
		ownerwire.OpPairStart:   {`{"code":"short","expires_at":"2026-11-20T14:12:00Z"}`, `{}`, `{"code":"` + pairCode + `","expires_at":"x"}`},
		ownerwire.OpPairStatus:  {`{"phase":"pending","expires_at":"2026-11-20T14:12:00Z"}`, `{"phase":"pending","words":["acorn","bridge","cedar","delta","ember","falcon"],"expires_at":"2026-11-20T14:12:00Z"}`, `{"phase":"waiting","words":[],"expires_at":"2026-11-20T14:12:00Z"}`, `{"phase":"open"}`},
		ownerwire.OpPairConfirm: {`{"id":"x","kind":"unverified","paired_at":"2026-11-20T14:06:40Z","revoked":false}`, `{}`},
		ownerwire.OpPairReject:  {`{"x":1}`},
		ownerwire.OpKeys:        {`{}`, `{"keys":[{"id":"0123456789abcdef"}]}`},
		ownerwire.OpKeyRevoke:   {`{"keys":[],"x":1}`},
		ownerwire.OpKeysOff:     {`[]`},
	}
	for op, call := range bad {
		for _, a := range answers[op] {
			f.onResult(op, []byte(a))
			err := call()
			var u *Unavailable
			if !errors.As(err, &u) || u.State != StateBadAnswer {
				t.Errorf("%s answered %s: %v", op, a, err)
			}
		}
	}
	// The connection was made again after each drop, and the code of a bad
	// answer is in no error text.
	if n := f.count(ownerwire.OpDecider); n < 5 {
		t.Errorf("%d connections were made", n)
	}
}

// A call is sent once: a daemon that hangs up leaves an error, and nothing is
// sent again by the client. The next call, the owner's, makes a new
// connection.
func TestAPairingCallIsNotSentAgainAfterAFailure(t *testing.T) {
	f, o := pairingFake(t)
	f.on(ownerwire.OpPairStart, func(json.RawMessage) (any, *clientapi.ErrorBody) {
		// An answer past the response cap ends the call with a failure.
		return json.RawMessage(`"` + strings.Repeat("x", clientapi.MaxResponseBytes) + `"`), nil
	})
	if _, err := o.PairStart(false); err == nil {
		t.Fatal("a failed call returned no error")
	}
	if n := f.count(ownerwire.OpPairStart); n != 1 {
		t.Fatalf("the start was sent %d times", n)
	}
	f.onResult(ownerwire.OpPairStart, mustEncode(t)(ownerwire.EncodePairStartResult(
		ownerwire.PairStartResult{Code: pairCode, ExpiresAt: "2026-11-20T14:12:00Z"})))
	if res, err := o.PairStart(true); err != nil || res.Code != pairCode {
		t.Fatalf("%v, %v", res, err)
	}
	if n := f.count(ownerwire.OpPairStart); n != 2 {
		t.Fatalf("%d starts", n)
	}
}

func TestThePairingCallsAfterCloseAndWithNoChannel(t *testing.T) {
	_, o := pairingFake(t)
	o.Close()
	if _, err := o.PairStart(false); !errors.Is(err, ErrOwnerClosed) {
		t.Fatalf("%v", err)
	}
	if _, err := o.Keys(); !errors.Is(err, ErrOwnerClosed) {
		t.Fatalf("%v", err)
	}
	f := startOwnerWith(t, clientapi.Options{}, nil)
	o2 := newOwner(t, f.dir)
	for name, call := range map[string]func() error{
		"PairStart":   func() error { _, err := o2.PairStart(false); return err },
		"PairStatus":  func() error { _, err := o2.PairStatus(); return err },
		"PairConfirm": func() error { _, err := o2.PairConfirm(pairID); return err },
		"PairReject":  func() error { return o2.PairReject(pairID) },
		"Keys":        func() error { _, err := o2.Keys(); return err },
		"KeyRevoke":   func() error { _, err := o2.KeyRevoke("0123"); return err },
		"KeysOff":     func() error { _, err := o2.KeysOff(); return err },
	} {
		if err := call(); !errors.Is(err, ErrNoOwnerChannel) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if len(f.sent()) != 0 {
		t.Fatalf("%v", f.sent())
	}
}
