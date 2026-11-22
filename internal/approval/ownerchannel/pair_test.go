package ownerchannel

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	crand "crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/ownerwire"
	"ataniq/internal/approval/record"
	"ataniq/internal/approval/seatpair"
)

// countingRand gives bytes that differ from call to call, so that two codes
// differ and a test is the same each time.
type countingRand struct{ n uint32 }

func (r *countingRand) Read(p []byte) (int, error) {
	for i := range p {
		r.n = r.n*1664525 + 1013904223
		p[i] = byte(r.n >> 24)
	}
	return len(p), nil
}

// seatSrc is the connection of the seat app.
var seatSrc = Source{Surface: SurfaceSeat, Session: "fedcba9876543210"}

// phone is a credential the phone made: its id and its key, as the seat sends
// them.
type phone struct{ id, spki []byte }

func newPhone(t testing.TB, n byte) phone {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(&k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	id := make([]byte, 32)
	if _, err := crand.Read(id); err != nil {
		t.Fatal(err)
	}
	id[0] = n
	return phone{id: id, spki: spki}
}

func (p phone) params(code string) ownerwire.SeatPairParams {
	return ownerwire.SeatPairParams{Code: code, CredentialID: p.id, PublicKey: p.spki}
}

func (p phone) displayID() string {
	sum := sha256.Sum256(p.id)
	return hex.EncodeToString(sum[:8])
}

// pairEnv is an env with a fake list of keys and a deterministic code source.
func pairEnv(t *testing.T) (*env, *fakeKeys) {
	t.Helper()
	e := newEnv(t)
	k := newFakeKeys()
	e.keys, e.pairRand = k, &countingRand{}
	e.start()
	return e, k
}

func (e *env) pairStart(replace bool) (ownerwire.PairStartResult, error) {
	e.t.Helper()
	e.beat()
	return e.svc.PairStart(context.Background(), src, ownerwire.PairStartParams{Replace: replace})
}

func (e *env) mustStart() string {
	e.t.Helper()
	r, err := e.pairStart(false)
	if err != nil {
		e.t.Fatalf("PairStart: %v", err)
	}
	return r.Code
}

func (e *env) present(code string, p phone) (ownerwire.SeatPairResult, error) {
	return e.svc.PairPresent(context.Background(), seatSrc, p.params(code))
}

func (e *env) status() ownerwire.PairStatus {
	e.t.Helper()
	st, err := e.svc.PairStatus(context.Background())
	if err != nil {
		e.t.Fatalf("PairStatus: %v", err)
	}
	if _, err := ownerwire.EncodePairStatus(st); err != nil {
		e.t.Fatalf("the status does not encode: %v", err)
	}
	return st
}

// noPending is the pending id a test gives when the status shows none: a
// well-formed id no credential has.
const noPending = "0000000000000000"

// pendingID is the pending id the status shows now, or noPending.
func (e *env) pendingID() string {
	e.t.Helper()
	if id := e.status().PendingID; id != "" {
		return id
	}
	return noPending
}

// confirm is the owner confirming the credential the status shows.
func (e *env) confirm() (ownerwire.KeyItem, error) {
	e.t.Helper()
	return e.confirmID(e.pendingID())
}

func (e *env) confirmID(id string) (ownerwire.KeyItem, error) {
	e.t.Helper()
	e.beat()
	return e.svc.PairConfirm(context.Background(), src, ownerwire.PairPendingParams{PendingID: id})
}

// reject is the owner rejecting the credential the status shows.
func (e *env) reject() error {
	e.t.Helper()
	return e.rejectID(e.pendingID())
}

func (e *env) rejectID(id string) error {
	e.t.Helper()
	e.beat()
	return e.svc.PairReject(context.Background(), src, ownerwire.PairPendingParams{PendingID: id})
}

// pairedUp runs a whole pairing of a phone up to the credential pending.
func (e *env) pairedUp(p phone) (code string, words []string) {
	e.t.Helper()
	code = e.mustStart()
	r, err := e.present(code, p)
	if err != nil {
		e.t.Fatalf("PairPresent: %v", err)
	}
	return code, r.Words
}

func TestTheCodesAndTheLimitsAreTheSeatpairs(t *testing.T) {
	for _, c := range []struct {
		name      string
		wire, pkg int
	}{
		{"the code's length", ownerwire.PairCodeLength, seatpair.CodeLength},
		{"the words of a fingerprint", ownerwire.PairWords, seatpair.FingerprintWords},
		{"the shortest credential id", ownerwire.MinCredentialIDBytes, seatpair.MinCredentialIDLen},
		{"the longest credential id", ownerwire.MaxCredentialIDBytes, seatpair.MaxCredentialIDLen},
		{"the keys on the list", ownerwire.MaxKeys, ownerjournal.MaxKeys},
		{"the length of a display id", ownerwire.KeyIDLength, ownerjournal.DisplayIDLength},
	} {
		if c.wire != c.pkg {
			t.Errorf("%s: the wire says %d, the package %d", c.name, c.wire, c.pkg)
		}
	}
	// A code seatpair makes passes the wire's form.
	code, _ := seatpair.NewCode(&countingRand{}, start)
	if _, err := ownerwire.EncodePairStartResult(ownerwire.PairStartResult{Code: code, ExpiresAt: record.FormatTime(start)}); err != nil {
		t.Errorf("a code of seatpair is refused by the wire: %v", err)
	}
}

func TestAPairingGoesFromTheCodeToAKeyOnTheList(t *testing.T) {
	e, keys := pairEnv(t)
	ph := newPhone(t, 1)

	if st := e.status(); st.Phase != ownerwire.PairNone || st.Words != nil || st.ExpiresAt != "" {
		t.Fatalf("before a start: %+v", st)
	}
	r, err := e.pairStart(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerwire.EncodePairStartResult(r); err != nil {
		t.Fatalf("the start result does not encode: %v", err)
	}
	if want := record.FormatTime(start.Add(seatpair.CodeLifetime)); r.ExpiresAt != want {
		t.Fatalf("the code ends at %s, want %s", r.ExpiresAt, want)
	}
	if st := e.status(); st.Phase != ownerwire.PairWaiting || st.ExpiresAt != r.ExpiresAt || st.Words != nil {
		t.Fatalf("waiting: %+v", st)
	}

	e.advance(time.Minute)
	got, err := e.present(r.Code, ph)
	if err != nil {
		t.Fatal(err)
	}
	if want := seatpair.FingerprintOf(ph.id, ph.spki); strings.Join(got.Words, " ") != want.String() {
		t.Fatalf("the words are %v, want %s", got.Words, want)
	}
	if _, err := ownerwire.EncodeSeatPairResult(got); err != nil {
		t.Fatalf("the seat result does not encode: %v", err)
	}
	st := e.status()
	if st.Phase != ownerwire.PairPending || strings.Join(st.Words, " ") != strings.Join(got.Words, " ") ||
		st.ExpiresAt != record.FormatTime(start.Add(time.Minute+seatpair.PendingLifetime)) {
		t.Fatalf("pending: %+v", st)
	}
	// Nothing is on the list before the owner confirms.
	if view, err := e.svc.Keys(context.Background()); err != nil || len(view.Keys) != 0 {
		t.Fatalf("a credential is on the list before it is confirmed: %+v %v", view, err)
	}

	e.advance(time.Minute)
	item, err := e.confirm()
	if err != nil {
		t.Fatal(err)
	}
	want := ownerwire.KeyItem{ID: ph.displayID(), Kind: seatpair.KindUnverified, PairedAt: record.FormatTime(start.Add(2 * time.Minute))}
	if item != want {
		t.Fatalf("the key is %+v, want %+v", item, want)
	}
	if _, err := ownerwire.EncodeKeyItem(item); err != nil {
		t.Fatal(err)
	}
	if st := e.status(); st.Phase != ownerwire.PairNone {
		t.Fatalf("after the confirm: %+v", st)
	}
	view, err := e.svc.Keys(context.Background())
	if err != nil || len(view.Keys) != 1 || view.Keys[0] != want {
		t.Fatalf("the list: %+v %v", view, err)
	}
	if _, err := ownerwire.EncodeKeysView(view); err != nil {
		t.Fatal(err)
	}
	// What the store holds is what the phone made, with the words of the desk.
	stored, _ := keys.Keys()
	if len(stored) != 1 || string(stored[0].CredentialID) != string(ph.id) || string(stored[0].PublicKey) != string(ph.spki) ||
		stored[0].Words != got.Words[0]+" "+strings.Join(got.Words[1:], " ") || stored[0].Kind != "unverified" {
		t.Fatalf("the store holds %+v", stored)
	}
	// The code is spent: it brings no second credential, and no second
	// confirm makes a second key.
	if _, err := e.present(r.Code, newPhone(t, 2)); !errors.Is(err, ErrPairRefused) {
		t.Fatalf("the code again: %v, want %v", err, ErrPairRefused)
	}
	if _, err := e.confirm(); !errors.Is(err, ErrNoPairing) {
		t.Fatalf("a second confirm: %v, want %v", err, ErrNoPairing)
	}
	// The owner journal records nothing of a pairing.
	if kinds := e.kinds(); len(kinds) != 0 {
		t.Fatalf("the journal holds %v", kinds)
	}
}

func TestARejectedPairingIsJournaledWithoutAKey(t *testing.T) {
	e, keys := pairEnv(t)
	ph := newPhone(t, 1)
	code, words := e.pairedUp(ph)
	if err := e.reject(); err != nil {
		t.Fatal(err)
	}
	if st := e.status(); st.Phase != ownerwire.PairNone {
		t.Fatalf("after the reject: %+v", st)
	}
	if len(keys.keys) != 0 {
		t.Fatal("a rejected credential is on the list")
	}
	if len(keys.rejected) != 1 {
		t.Fatalf("%d rejected pairings journaled", len(keys.rejected))
	}
	got := keys.rejected[0]
	if got.ID != ph.displayID() || got.Words != strings.Join(words, " ") || !got.At.Equal(start) {
		t.Fatalf("journaled %+v", got)
	}
	// The same code makes no second try, and there is nothing to confirm.
	if _, err := e.present(code, ph); !errors.Is(err, ErrPairRefused) {
		t.Fatalf("the code after a reject: %v", err)
	}
	if _, err := e.confirm(); !errors.Is(err, ErrNoPairing) {
		t.Fatalf("a confirm after a reject: %v", err)
	}
	if err := e.reject(); !errors.Is(err, ErrNoPairing) {
		t.Fatalf("a second reject: %v", err)
	}
	// A pairing that waits for a phone has nothing to reject either.
	e.mustStart()
	if err := e.reject(); !errors.Is(err, ErrNoPairing) {
		t.Fatalf("a reject while waiting: %v", err)
	}
	if len(keys.rejected) != 1 {
		t.Fatalf("%d rejected pairings journaled", len(keys.rejected))
	}
}

func TestAStartWhileOneIsOpenNeedsReplace(t *testing.T) {
	e, _ := pairEnv(t)
	first := e.mustStart()
	if _, err := e.pairStart(false); !errors.Is(err, ErrPairingOpen) {
		t.Fatalf("a second start: %v, want %v", err, ErrPairingOpen)
	}
	// The refused start left the pairing as it was: the first code works.
	ph := newPhone(t, 1)
	r, err := e.pairStart(true)
	if err != nil {
		t.Fatal(err)
	}
	if r.Code == first {
		t.Fatal("a replaced pairing has the same code")
	}
	if _, err := e.present(first, ph); !errors.Is(err, ErrPairRefused) {
		t.Fatalf("the replaced code: %v, want %v", err, ErrPairRefused)
	}
	if _, err := e.present(r.Code, ph); err != nil {
		t.Fatalf("the new code: %v", err)
	}
	// A credential pending is an open pairing too, and replace ends it.
	if _, err := e.pairStart(false); !errors.Is(err, ErrPairingOpen) {
		t.Fatalf("a start over a pending credential: %v", err)
	}
	if _, err := e.pairStart(true); err != nil {
		t.Fatal(err)
	}
	if _, err := e.confirm(); !errors.Is(err, ErrNoPairing) {
		t.Fatalf("a confirm of a replaced credential: %v", err)
	}
	// A pairing that is over needs no replace.
	e.advance(seatpair.CodeLifetime)
	if _, err := e.pairStart(false); err != nil {
		t.Fatalf("a start after the code ran out: %v", err)
	}
}

func TestThePhasesOverTime(t *testing.T) {
	e, _ := pairEnv(t)
	ph := newPhone(t, 1)
	code := e.mustStart()
	end := record.FormatTime(start.Add(seatpair.CodeLifetime))
	e.advance(seatpair.CodeLifetime - time.Second)
	if st := e.status(); st.Phase != ownerwire.PairWaiting {
		t.Fatalf("one second before the end: %+v", st)
	}
	e.advance(time.Second)
	if st := e.status(); st.Phase != ownerwire.PairExpired || st.ExpiresAt != end || st.Words != nil {
		t.Fatalf("at the end: %+v", st)
	}
	if _, err := e.present(code, ph); !errors.Is(err, ErrPairRefused) {
		t.Fatalf("an expired code: %v", err)
	}
	if st := e.status(); st.Phase != ownerwire.PairExpired {
		t.Fatalf("it reads expired until the next start: %+v", st)
	}

	// A pending credential that waits too long.
	_, _ = e.pairedUp(ph)
	presented := e.clock.Now()
	seen := e.pendingID()
	e.advance(seatpair.PendingLifetime - time.Second)
	if st := e.status(); st.Phase != ownerwire.PairPending {
		t.Fatalf("a second before the end: %+v", st)
	}
	e.advance(time.Second)
	if st := e.status(); st.Phase != ownerwire.PairExpired || st.ExpiresAt != record.FormatTime(presented.Add(seatpair.PendingLifetime)) {
		t.Fatalf("at the end: %+v", st)
	}
	if _, err := e.confirmID(seen); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("a confirm after the end: %v", err)
	}
	if st := e.status(); st.Phase != ownerwire.PairExpired {
		t.Fatalf("%+v", st)
	}
	// A new start clears it.
	e.mustStart()
	if st := e.status(); st.Phase != ownerwire.PairWaiting {
		t.Fatalf("%+v", st)
	}
}

func TestAnExpiredPendingCredentialIsRefusedAtTheConfirm(t *testing.T) {
	e, keys := pairEnv(t)
	e.pairedUp(newPhone(t, 1))
	seen := e.pendingID()
	// Past the time, with no status read in between.
	e.advance(seatpair.PendingLifetime)
	if _, err := e.confirmID(seen); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("%v, want %v", err, ErrPairingExpired)
	}
	if len(keys.keys) != 0 {
		t.Fatal("an expired credential was put on the list")
	}
}

func TestWhoMayDoWhat(t *testing.T) {
	notDecider := src
	notDecider.Decider = false
	badSession := src
	badSession.Session = "not-a-session"
	badSurface := src
	badSurface.Surface = "Console"
	e, keys := pairEnv(t)
	ph := newPhone(t, 1)
	e.pairedUp(ph)
	pending := e.pendingID()
	keys.seed("0123456789abcdef", false)

	writes := map[string]func(Source) error{
		"pair-start": func(s Source) error {
			_, err := e.svc.PairStart(context.Background(), s, ownerwire.PairStartParams{Replace: true})
			return err
		},
		"pair-confirm": func(s Source) error {
			_, err := e.svc.PairConfirm(context.Background(), s, ownerwire.PairPendingParams{PendingID: pending})
			return err
		},
		"pair-reject": func(s Source) error {
			return e.svc.PairReject(context.Background(), s, ownerwire.PairPendingParams{PendingID: pending})
		},
		"key-revoke": func(s Source) error {
			_, err := e.svc.KeyRevoke(context.Background(), s, ownerwire.KeyRevokeParams{ID: "0123"})
			return err
		},
		"keys-off": func(s Source) error { _, err := e.svc.KeysOff(context.Background(), s); return err },
	}
	for name, call := range writes {
		for who, s := range map[string]Source{"a connection that is not the decider": notDecider} {
			e.beat()
			wantCode(t, call(s), record.CodeNotDecider)
			_ = who
		}
		for who, s := range map[string]Source{"a bad session": badSession, "a bad surface": badSurface} {
			e.beat()
			wantCode(t, call(s), record.CodeRecordInvalid)
			_ = who
		}
		_ = name
	}
	// Nothing changed: the credential waits, and the list is as it was.
	if st := e.status(); st.Phase != ownerwire.PairPending {
		t.Fatalf("%+v", st)
	}
	if view, _ := e.svc.Keys(context.Background()); len(view.Keys) != 1 || view.Keys[0].Revoked {
		t.Fatalf("%+v", view)
	}
	if keys.writes != 0 {
		t.Fatalf("a refused call wrote %d times", keys.writes)
	}

	// An owner who is away may not start, confirm or reject, and may revoke.
	e.advance(11 * time.Minute)
	for _, name := range []string{"pair-start", "pair-confirm", "pair-reject"} {
		wantCode(t, writes[name](src), record.CodeOwnerAway)
	}
	if st := e.status(); st.Phase == ownerwire.PairNone {
		t.Fatalf("a refused call ended the pairing: %+v", st)
	}
	if _, err := e.svc.KeyRevoke(context.Background(), src, ownerwire.KeyRevokeParams{ID: "0123"}); err != nil {
		t.Fatalf("revoking while away: %v", err)
	}
	if _, err := e.svc.KeysOff(context.Background(), src); err != nil {
		t.Fatalf("phone off while away: %v", err)
	}
	// The reads need no one.
	if _, err := e.svc.Keys(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PairStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyASeatMayPresent(t *testing.T) {
	e, _ := pairEnv(t)
	ph := newPhone(t, 1)
	code := e.mustStart()
	for name, s := range map[string]Source{
		"the console":     src,
		"another surface": {Surface: "phone", Session: seatSrc.Session},
		"no surface":      {Session: seatSrc.Session},
	} {
		_, err := e.svc.PairPresent(context.Background(), s, ph.params(code))
		if !errors.Is(err, ErrNotSeat) {
			t.Errorf("%s: %v, want %v", name, err, ErrNotSeat)
		}
	}
	bad := seatSrc
	bad.Session = "x"
	_, err := e.svc.PairPresent(context.Background(), bad, ph.params(code))
	wantCode(t, err, record.CodeRecordInvalid)
	// None of those spent the code or counted a try.
	if _, err := e.present(code, ph); err != nil {
		t.Fatalf("the code after refused sources: %v", err)
	}
}

func TestThePresentationOfTheSeat(t *testing.T) {
	good := newPhone(t, 1)
	e, _ := pairEnv(t)
	code := e.mustStart()

	// A malformed call is refused as invalid, before the pairing is asked.
	for name, p := range map[string]ownerwire.SeatPairParams{
		"a short code":     good.params(code[:10]),
		"no credential":    {Code: code, PublicKey: good.spki},
		"a short id":       {Code: code, CredentialID: good.id[:8], PublicKey: good.spki},
		"a huge key":       {Code: code, CredentialID: good.id, PublicKey: make([]byte, 600)},
		"a code with an I": good.params("I" + code[1:]),
	} {
		_, err := e.svc.PairPresent(context.Background(), seatSrc, p)
		if c, _ := record.CodeOf(err); c != record.CodeRecordInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A credential that is not an ES256 key is refused after a right code and
	// leaves the code unused.
	notAKey := phone{id: good.id, spki: []byte(strings.Repeat("x", 91))}
	if _, err := e.present(code, notAKey); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("a key that is not one: %v", err)
	}
	if st := e.status(); st.Phase != ownerwire.PairWaiting {
		t.Fatalf("%+v", st)
	}
	short := phone{id: good.id[:16], spki: good.spki}
	if _, err := e.present(code, short); err != nil {
		t.Fatalf("a good credential after a refused one: %v", err)
	}
	// A second credential while one is pending is refused, and so is the
	// code, now it is used.
	if _, err := e.present(code, good); !errors.Is(err, ErrPairRefused) {
		t.Fatalf("a second presentation: %v", err)
	}

	// Wrong codes: five burn the code, and then the right one is dead.
	e.mustStartReplace()
	code = e.lastCode
	for i := 0; i < seatpair.MaxTries; i++ {
		if _, err := e.present(wrongOf(code, i), good); !errors.Is(err, ErrPairRefused) {
			t.Fatalf("wrong try %d: %v", i, err)
		}
	}
	if st := e.status(); st.Phase != ownerwire.PairExpired {
		t.Fatalf("a burnt code reads %+v", st)
	}
	if _, err := e.present(code, good); !errors.Is(err, ErrPairRefused) {
		t.Fatalf("the right code after five wrong: %v", err)
	}
}

func wrongOf(code string, i int) string {
	b := []byte(code)
	b[i] = "0123456789"[(strings.IndexByte("0123456789ABCDEFGHJKMNPQRSTVWXYZ", b[i])+1)%10]
	if string(b) == code {
		b[i] = 'Z'
	}
	return string(b)
}

func (e *env) mustStartReplace() {
	e.t.Helper()
	r, err := e.pairStart(true)
	if err != nil {
		e.t.Fatal(err)
	}
	e.lastCode = r.Code
}

// The code is a secret that is in the result of the start and in no other
// place: not in a status, a list, an error, a notice, the journal, or what
// the store holds. Every operation is run, with the failures that can be made.
func TestTheCodeIsInTheStartResultAndNowhereElse(t *testing.T) {
	e, keys := pairEnv(t)
	ph := newPhone(t, 1)
	var seen []string
	note := func(what string, v any, err error) {
		if err != nil {
			seen = append(seen, what+": "+err.Error()+fmt.Sprintf(" %#v", err))
		}
		if v != nil {
			b, jerr := json.Marshal(v)
			if jerr != nil {
				t.Fatal(jerr)
			}
			seen = append(seen, what+": "+string(b)+fmt.Sprintf(" %v %+v %#v", v, v, v))
		}
	}
	ctx := context.Background()

	// A start refused because one is open, before there is any code.
	code := e.mustStart()
	_, err := e.pairStart(false)
	note("a refused start", nil, err)
	st, err := e.svc.PairStatus(ctx)
	note("status waiting", st, err)
	// Refusals of the presentation: wrong, malformed, bad credential.
	for _, c := range []string{wrongOf(code, 0), code[:19], strings.ToLower(wrongOf(code, 1))} {
		_, err := e.present(c, ph)
		note("a wrong presentation", nil, err)
	}
	_, err = e.present(code, phone{id: ph.id, spki: []byte("nope")})
	note("a bad credential", nil, err)
	res, err := e.present(code, ph)
	note("the presentation", res, err)
	st, err = e.svc.PairStatus(ctx)
	note("status pending", st, err)
	_, err = e.present(code, ph)
	note("a second presentation", nil, err)
	item, err := e.confirm()
	note("the confirm", item, err)
	_, err = e.confirm()
	note("a second confirm", nil, err)
	view, err := e.svc.Keys(ctx)
	note("keys", view, err)
	// A rejection, a replacement and a revocation.
	c2 := e.mustStart()
	_, err = e.present(c2, newPhone(t, 2))
	note("the second presentation", nil, err)
	note("a reject", nil, e.reject())
	view, err = e.svc.KeyRevoke(ctx, src, ownerwire.KeyRevokeParams{ID: "00000000"})
	note("a revoke of nothing", view, err)
	view, err = e.svc.KeysOff(ctx, src)
	note("keys off", view, err)
	// Failures of the store.
	c3 := e.mustStartReplace2()
	keys.failRead = errStoreDown
	_, err = e.present(c3, newPhone(t, 3))
	note("a presentation with the list down", nil, err)
	_, err = e.pairStart(true)
	note("a start with the list down", nil, err)
	keys.failRead = nil
	_, err = e.present(c3, newPhone(t, 3))
	note("a presentation", nil, err)
	keys.failAdd = errStoreDown
	_, err = e.confirm()
	note("a confirm with the add failing", nil, err)
	keys.failAdd = nil

	secrets := []string{code, c2, c3, strings.ToLower(code), strings.ToLower(c2)}
	hay := strings.Join(seen, "\n")
	hay += "\n" + strings.Join(e.noticed(), "\n")
	for _, entry := range e.entries("") {
		hay += "\n" + string(entry.Body) + entry.PendingID + entry.Project
	}
	b, _ := json.Marshal(keys.keys)
	hay += "\n" + string(b)
	b, _ = json.Marshal(keys.rejected)
	hay += "\n" + string(b)
	for _, s := range secrets {
		if strings.Contains(hay, s) {
			t.Errorf("a code is in the answers, the notices, the journal or the list")
		}
	}
	if !strings.Contains(hay, "unverified") {
		t.Fatal("the test saw nothing")
	}
}

func (e *env) mustStartReplace2() string {
	e.t.Helper()
	e.mustStartReplace()
	return e.lastCode
}

func TestTheKeyListAndItsRevocations(t *testing.T) {
	e, keys := pairEnv(t)
	ctx := context.Background()
	phones := []phone{newPhone(t, 1), newPhone(t, 2), newPhone(t, 3)}
	for _, p := range phones {
		e.pairedUp(p)
		if _, err := e.confirm(); err != nil {
			t.Fatal(err)
		}
		e.advance(time.Minute)
	}
	view, err := e.svc.Keys(ctx)
	if err != nil || len(view.Keys) != 3 {
		t.Fatalf("%+v %v", view, err)
	}
	for i, k := range view.Keys {
		if k.ID != phones[i].displayID() || k.Revoked {
			t.Errorf("key %d: %+v", i, k)
		}
	}

	// A prefix of one id revokes that credential and no other.
	e.beat()
	view, err = e.svc.KeyRevoke(ctx, src, ownerwire.KeyRevokeParams{ID: view.Keys[1].ID[:6]})
	if err != nil {
		t.Fatal(err)
	}
	if view.Keys[0].Revoked || !view.Keys[1].Revoked || view.Keys[2].Revoked {
		t.Fatalf("%+v", view)
	}
	if _, err := ownerwire.EncodeKeysView(view); err != nil {
		t.Fatal(err)
	}
	// It stays listed, and a second revoke leaves it so and writes nothing.
	w := keys.writes
	view, err = e.svc.KeyRevoke(ctx, src, ownerwire.KeyRevokeParams{ID: phones[1].displayID()})
	if err != nil || !view.Keys[1].Revoked || len(view.Keys) != 3 || keys.writes != w {
		t.Fatalf("%+v %v (%d writes)", view, err, keys.writes-w)
	}
	// An id that names none, and a malformed one, are refused.
	_, err = e.svc.KeyRevoke(ctx, src, ownerwire.KeyRevokeParams{ID: "0000000000000000"})
	if !errors.Is(err, ErrKeyUnknown) {
		t.Fatalf("%v", err)
	}
	for _, id := range []string{"", "012", "XYZW", "0123456789abcdef0"} {
		_, err = e.svc.KeyRevoke(ctx, src, ownerwire.KeyRevokeParams{ID: id})
		wantCode(t, err, record.CodeRecordInvalid)
	}
	// Phone off revokes the rest, once.
	view, err = e.svc.KeysOff(ctx, src)
	if err != nil || len(view.Keys) != 3 {
		t.Fatalf("%+v %v", view, err)
	}
	for _, k := range view.Keys {
		if !k.Revoked {
			t.Fatalf("a key is left: %+v", k)
		}
	}
	w = keys.writes
	if _, err := e.svc.KeysOff(ctx, src); err != nil || keys.writes != w {
		t.Fatalf("a second phone off: %v (%d writes)", err, keys.writes-w)
	}
	// A revoked credential is never good again: pairing the same credential
	// does not undo it, and a new one is a new key.
	code := e.mustStart()
	if _, err := e.present(code, phones[0]); !errors.Is(err, ErrCredentialInvalid) {
		t.Fatalf("pairing a revoked credential again: %v", err)
	}
	e.pairedUp(newPhone(t, 4))
	item, err := e.confirm()
	if err != nil || item.Revoked {
		t.Fatalf("%+v %v", item, err)
	}
	view, _ = e.svc.Keys(ctx)
	if len(view.Keys) != 4 || !view.Keys[0].Revoked || view.Keys[3].Revoked {
		t.Fatalf("%+v", view)
	}
}

func TestAnIdThatStartsMoreThanOneCredentialNamesNone(t *testing.T) {
	e, keys := pairEnv(t)
	keys.seed("aaaa111122223333", false)
	keys.seed("aaaa444455556666", false)
	keys.seed("bbbb777788889999", false)
	e.beat()
	_, err := e.svc.KeyRevoke(context.Background(), src, ownerwire.KeyRevokeParams{ID: "aaaa"})
	if !errors.Is(err, ErrKeyAmbiguous) {
		t.Fatalf("%v, want %v", err, ErrKeyAmbiguous)
	}
	if keys.writes != 0 {
		t.Fatal("an ambiguous id revoked something")
	}
	view, err := e.svc.KeyRevoke(context.Background(), src, ownerwire.KeyRevokeParams{ID: "aaaa1"})
	if err != nil || !view.Keys[0].Revoked || view.Keys[1].Revoked || view.Keys[2].Revoked {
		t.Fatalf("%+v %v", view, err)
	}
	// The full id of a credential is one even when its start is shared.
	view, err = e.svc.KeyRevoke(context.Background(), src, ownerwire.KeyRevokeParams{ID: "aaaa444455556666"})
	if err != nil || !view.Keys[1].Revoked {
		t.Fatalf("%+v %v", view, err)
	}
}

func TestAFullListTakesNoMoreAndLeavesTheCredentialPending(t *testing.T) {
	e, keys := pairEnv(t)
	for i := 0; i < ownerjournal.MaxKeys; i++ {
		keys.seed(fmt.Sprintf("%016x", i), true)
	}
	e.pairedUp(newPhone(t, 1))
	if _, err := e.confirm(); !errors.Is(err, ErrKeysFull) {
		t.Fatalf("%v, want %v", err, ErrKeysFull)
	}
	if st := e.status(); st.Phase != ownerwire.PairPending {
		t.Fatalf("a refused confirm ended the pairing: %+v", st)
	}
	// The owner can still reject it, and revoke.
	if err := e.reject(); err != nil {
		t.Fatal(err)
	}
}

// A list that cannot be used refuses every operation with one error, a
// pairing is not begun on it, and a credential is not consumed by it.
func TestAListThatCannotBeUsedRefusesTheOperations(t *testing.T) {
	ctx := context.Background()
	ph := newPhone(t, 1)
	ops := map[string]func(e *env) error{
		"pair-start":   func(e *env) error { _, err := e.pairStart(true); return err },
		"pair-confirm": func(e *env) error { _, err := e.confirm(); return err },
		"pair-reject":  func(e *env) error { return e.reject() },
		"keys":         func(e *env) error { _, err := e.svc.Keys(ctx); return err },
		"key-revoke": func(e *env) error {
			e.beat()
			_, err := e.svc.KeyRevoke(ctx, src, ownerwire.KeyRevokeParams{ID: "0123"})
			return err
		},
		"keys-off": func(e *env) error { e.beat(); _, err := e.svc.KeysOff(ctx, src); return err },
		"seat.pair": func(e *env) error {
			_, err := e.present(e.lastCode, ph)
			return err
		},
	}
	// No list at all, which is what a daemon whose file could not be opened has.
	t.Run("none", func(t *testing.T) {
		e := newEnv(t)
		e.pairRand = &countingRand{}
		e.start()
		e.lastCode = strings.Repeat("0", 20)
		for name, op := range ops {
			if err := op(e); !errors.Is(err, ErrKeysUnavailable) {
				t.Errorf("%s: %v, want %v", name, err, ErrKeysUnavailable)
			}
		}
		if st := e.status(); st.Phase != ownerwire.PairNone {
			t.Errorf("%+v", st)
		}
	})
	// A list that answers an error on every read.
	t.Run("a failed read", func(t *testing.T) {
		e, keys := pairEnv(t)
		e.lastCode = e.mustStart()
		keys.keys = nil
		keys.failRead = errStoreDown
		for name, op := range ops {
			err := op(e)
			if !errors.Is(err, ErrKeysUnavailable) {
				t.Errorf("%s: %v, want %v", name, err, ErrKeysUnavailable)
			}
			// The text of the store's error is in the error for the log, and the
			// sentinel is what the daemon tells apart.
			if err != nil && !strings.Contains(err.Error(), "private/path") {
				t.Errorf("%s: the log has no cause: %v", name, err)
			}
		}
		keys.failRead = nil
		// The refusals took nothing: the code still works, and the list is empty.
		if _, err := e.present(e.lastCode, ph); err != nil {
			t.Fatalf("the code after refusals: %v", err)
		}
	})
	// A credential pending, then the list goes down: the confirm refuses before
	// it touches the pairing.
	t.Run("down while pending", func(t *testing.T) {
		e, keys := pairEnv(t)
		e.pairedUp(ph)
		keys.failRead = errStoreDown
		if _, err := e.confirm(); !errors.Is(err, ErrKeysUnavailable) {
			t.Fatalf("%v", err)
		}
		if err := e.reject(); !errors.Is(err, ErrKeysUnavailable) {
			t.Fatalf("%v", err)
		}
		keys.failRead = nil
		if st := e.status(); st.Phase != ownerwire.PairPending {
			t.Fatalf("%+v", st)
		}
		if _, err := e.confirm(); err != nil {
			t.Fatalf("the confirm once the list is back: %v", err)
		}
	})
	// A write that fails: nothing is on the list, and the answer is the same.
	t.Run("failed writes", func(t *testing.T) {
		e, keys := pairEnv(t)
		e.pairedUp(ph)
		keys.failAdd = errStoreDown
		if _, err := e.confirm(); !errors.Is(err, ErrKeysUnavailable) {
			t.Fatalf("%v", err)
		}
		if len(keys.keys) != 0 {
			t.Fatal("a failed add left a key")
		}
		if n := e.noticed(); len(n) != 1 || strings.Contains(n[0], "private") {
			t.Fatalf("notices %q", n)
		}
		// The credential was consumed; a new pairing is needed.
		if st := e.status(); st.Phase != ownerwire.PairNone {
			t.Fatalf("%+v", st)
		}
		keys.failAdd = nil
		keys.seed("aaaaaaaaaaaaaaaa", false)
		keys.failRevoke = errStoreDown
		e.beat()
		if _, err := e.svc.KeyRevoke(ctx, src, ownerwire.KeyRevokeParams{ID: "aaaa"}); !errors.Is(err, ErrKeysUnavailable) {
			t.Fatalf("%v", err)
		}
		if _, err := e.svc.KeysOff(ctx, src); !errors.Is(err, ErrKeysUnavailable) {
			t.Fatalf("%v", err)
		}
		keys.failRevoke = nil
		e.pairedUp(newPhone(t, 2))
		keys.failReject = errStoreDown
		if err := e.reject(); !errors.Is(err, ErrKeysUnavailable) {
			t.Fatalf("%v", err)
		}
	})
	// The errors the store gives for a fact of the list.
	t.Run("the facts of the list", func(t *testing.T) {
		for in, want := range map[error]error{
			ownerjournal.ErrKeysFull:    ErrKeysFull,
			ownerjournal.ErrKeyUnknown:  ErrKeyUnknown,
			ownerjournal.ErrKeyExists:   ErrCredentialInvalid,
			ownerjournal.ErrDamaged:     ErrKeysUnavailable,
			ownerjournal.ErrNotPrivate:  ErrKeysUnavailable,
			ownerjournal.ErrUncertain:   ErrKeysUnavailable,
			ownerjournal.ErrClosed:      ErrKeysUnavailable,
			ownerjournal.ErrLocked:      ErrKeysUnavailable,
			errors.New("anything else"): ErrKeysUnavailable,
		} {
			if got := keysErr(in); !errors.Is(got, want) {
				t.Errorf("%v became %v, want %v", in, got, want)
			}
		}
		if keysErr(nil) != nil {
			t.Error("nil became an error")
		}
	})
}

// A restart abandons the pairing: the code is dead, the credential is gone,
// and nothing was on the list.
func TestARestartAbandonsThePairing(t *testing.T) {
	e, keys := pairEnv(t)
	ph := newPhone(t, 1)
	code, _ := e.pairedUp(ph)
	// A second code, still waiting, is abandoned too.
	e.restart()
	if st := e.status(); st.Phase != ownerwire.PairNone {
		t.Fatalf("after a restart: %+v", st)
	}
	if _, err := e.confirm(); !errors.Is(err, ErrNoPairing) {
		t.Fatalf("a confirm after a restart: %v", err)
	}
	if _, err := e.present(code, ph); !errors.Is(err, ErrPairRefused) {
		t.Fatalf("the old code after a restart: %v", err)
	}
	if len(keys.keys) != 0 {
		t.Fatal("a restart made a key")
	}
	// A new service starts a new pairing with a new code.
	c2 := e.mustStart()
	if c2 == code {
		t.Fatal("a restart gave the same code")
	}
	e.restart()
	if _, err := e.present(c2, ph); !errors.Is(err, ErrPairRefused) {
		t.Fatalf("a code that waited through a restart: %v", err)
	}
}

func TestTheServiceClosesItsKeyList(t *testing.T) {
	e, keys := pairEnv(t)
	e.stop()
	if keys.closed != 1 {
		t.Fatalf("the list was closed %d times", keys.closed)
	}
	ctx := context.Background()
	if _, err := e.svc.Keys(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("%v", err)
	}
	if _, err := e.svc.PairStatus(ctx); !errors.Is(err, ErrClosed) {
		t.Fatalf("%v", err)
	}
	if _, err := e.svc.PairStart(ctx, src, ownerwire.PairStartParams{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("%v", err)
	}
	if _, err := e.svc.PairPresent(ctx, seatSrc, newPhone(t, 1).params(strings.Repeat("0", 20))); !errors.Is(err, ErrClosed) {
		t.Fatalf("%v", err)
	}
	if _, err := e.svc.KeysOff(ctx, src); !errors.Is(err, ErrClosed) {
		t.Fatalf("%v", err)
	}
}

// Of many confirms racing for one credential, one makes the key.
func TestConfirmsRacingMakeOneKey(t *testing.T) {
	e, keys := pairEnv(t)
	e.pairedUp(newPhone(t, 1))
	e.beat()
	pending := e.pendingID()
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok, none := 0, 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.svc.PairConfirm(context.Background(), src, ownerwire.PairPendingParams{PendingID: pending})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				ok++
			case errors.Is(err, ErrNoPairing):
				none++
			default:
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok != 1 || none != 15 || len(keys.keys) != 1 {
		t.Fatalf("%d confirmed, %d refused, %d keys", ok, none, len(keys.keys))
	}
}

// The same flow on the real list in a file: what the owner confirmed is there
// after a restart of the service, and a damaged file refuses the operations and
// is not taken for an empty list.
func TestThePairingOnTheListInAFile(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ownerjournal.KeysFileName)
	open := func() KeyStore {
		k, err := ownerjournal.OpenKeys(path)
		if err != nil {
			t.Fatal(err)
		}
		return k
	}
	e := newEnv(t)
	e.keys, e.pairRand = open(), &countingRand{}
	e.start()
	a, b := newPhone(t, 1), newPhone(t, 2)
	e.pairedUp(a)
	if _, err := e.confirm(); err != nil {
		t.Fatal(err)
	}
	e.pairedUp(b)
	if err := e.reject(); err != nil {
		t.Fatal(err)
	}
	e.stop()

	e.keys = open()
	e.start()
	view, err := e.svc.Keys(ctx)
	if err != nil || len(view.Keys) != 1 || view.Keys[0].ID != a.displayID() || view.Keys[0].Revoked {
		t.Fatalf("after a restart: %+v %v", view, err)
	}
	e.beat()
	if view, err = e.svc.KeysOff(ctx, src); err != nil || !view.Keys[0].Revoked {
		t.Fatalf("%+v %v", view, err)
	}
	e.stop()

	// A file that is damaged is refused by the store, and the daemon, which has
	// no list then, answers every operation with the one error.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerjournal.OpenKeys(path); !errors.Is(err, ownerjournal.ErrDamaged) {
		t.Fatalf("%v", err)
	}
	e.keys = nil
	e.start()
	if _, err := e.svc.Keys(ctx); !errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("%v", err)
	}
	if _, err := e.pairStart(false); !errors.Is(err, ErrKeysUnavailable) {
		t.Fatalf("%v", err)
	}
	if got, _ := os.ReadFile(path); string(got) != string(raw) {
		t.Fatal("the damaged file was changed")
	}
}

// The wire's Encode refuses nothing the service gives, whatever is on the
// list: a list of the longest and a kind of the longest.
func TestWhatTheServiceAnswersEncodes(t *testing.T) {
	e, keys := pairEnv(t)
	for i := 0; i < ownerjournal.MaxKeys; i++ {
		keys.seed(fmt.Sprintf("%016x", i), i%2 == 0)
	}
	keys.keys[3].Kind = strings.Repeat("a", 32)
	view, err := e.svc.Keys(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerwire.EncodeKeysView(view); err != nil {
		t.Fatal(err)
	}
}

// The status of a pending credential carries its pending id, 16 lower-case
// hexadecimal digits, and no other phase does.
func TestTheStatusOfAPendingCredentialCarriesItsID(t *testing.T) {
	e, _ := pairEnv(t)
	if st := e.status(); st.PendingID != "" {
		t.Fatalf("no pairing: %+v", st)
	}
	code := e.mustStart()
	if st := e.status(); st.PendingID != "" {
		t.Fatalf("waiting: %+v", st)
	}
	if _, err := e.present(code, newPhone(t, 1)); err != nil {
		t.Fatal(err)
	}
	st := e.status()
	if st.Phase != ownerwire.PairPending || len(st.PendingID) != ownerwire.PendingIDLength {
		t.Fatalf("pending: %+v", st)
	}
	if st2 := e.status(); st2.PendingID != st.PendingID {
		t.Fatalf("the id changed between two readings: %q, %q", st.PendingID, st2.PendingID)
	}
	e.advance(seatpair.PendingLifetime + time.Minute)
	if st := e.status(); st.Phase != ownerwire.PairExpired || st.PendingID != "" {
		t.Fatalf("expired: %+v", st)
	}
}

// The owner compared the words of A; A was replaced by B (or A ran out and B
// took its place); his word for A is about nothing now: it changes nothing and
// is answered with its own error, and B stays pending to be confirmed on its
// own id.
func TestAWordAboutAReplacedCredentialChangesNothing(t *testing.T) {
	for _, how := range []string{"replaced", "ran out"} {
		t.Run(how, func(t *testing.T) {
			e, keys := pairEnv(t)
			a, b := newPhone(t, 1), newPhone(t, 2)
			e.pairedUp(a)
			idA := e.status().PendingID
			if how == "replaced" {
				if _, err := e.pairStart(true); err != nil {
					t.Fatal(err)
				}
			} else {
				e.advance(seatpair.PendingLifetime)
			}
			code, err := e.pairStart(true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.present(code.Code, b); err != nil {
				t.Fatal(err)
			}
			idB := e.status().PendingID
			if idA == idB || idB == "" {
				t.Fatalf("the ids of A and B: %q, %q", idA, idB)
			}
			if _, err := e.confirmID(idA); !errors.Is(err, ErrPairingChanged) {
				t.Fatalf("confirm of A while B is pending: %v, want %v", err, ErrPairingChanged)
			}
			if err := e.rejectID(idA); !errors.Is(err, ErrPairingChanged) {
				t.Fatalf("reject of A while B is pending: %v, want %v", err, ErrPairingChanged)
			}
			if len(keys.keys) != 0 || len(keys.rejected) != 0 {
				t.Fatalf("a word about A changed the store: %d keys, %d rejected", len(keys.keys), len(keys.rejected))
			}
			if st := e.status(); st.Phase != ownerwire.PairPending || st.PendingID != idB {
				t.Fatalf("B no longer pending: %+v", st)
			}
			item, err := e.confirmID(idB)
			if err != nil || item.ID != b.displayID() {
				t.Fatalf("confirm of B: %+v, %v", item, err)
			}
			if len(keys.keys) != 1 {
				t.Fatalf("%d keys", len(keys.keys))
			}
		})
	}
}

// A word that does not name the pending credential is refused for every
// spelling of it, and nothing moves.
func TestAWordWithTheWrongIDChangesNothing(t *testing.T) {
	e, keys := pairEnv(t)
	e.pairedUp(newPhone(t, 1))
	good := e.status().PendingID
	other := []byte(good)
	if other[0] == '0' {
		other[0] = '1'
	} else {
		other[0] = '0'
	}
	for _, id := range []string{noPending, string(other)} {
		if _, err := e.confirmID(id); !errors.Is(err, ErrPairingChanged) {
			t.Errorf("confirm with %q: %v", id, err)
		}
		if err := e.rejectID(id); !errors.Is(err, ErrPairingChanged) {
			t.Errorf("reject with %q: %v", id, err)
		}
	}
	if len(keys.keys) != 0 || len(keys.rejected) != 0 || e.status().PendingID != good {
		t.Fatalf("a wrong id moved something: %d keys, %d rejected", len(keys.keys), len(keys.rejected))
	}
	// Params the wire refuses are refused before the pairing is looked at.
	for _, id := range []string{"", good[:15], strings.ToUpper(good), good + "0"} {
		e.beat()
		_, err := e.svc.PairConfirm(context.Background(), src, ownerwire.PairPendingParams{PendingID: id})
		wantCode(t, err, record.CodeRecordInvalid)
		e.beat()
		wantCode(t, e.svc.PairReject(context.Background(), src, ownerwire.PairPendingParams{PendingID: id}), record.CodeRecordInvalid)
	}
	if e.status().PendingID != good {
		t.Fatal("a refused call moved the pairing")
	}
	if err := e.rejectID(good); err != nil {
		t.Fatalf("the right id: %v", err)
	}
}

// A code that was seen expired does not come back when the wall clock is set
// back: the seat's call, which has no presence to check, is the one that could
// otherwise be fed an old code.
func TestAnExpiredCodeDoesNotComeBackWhenTheClockIsSetBack(t *testing.T) {
	e, _ := pairEnv(t)
	ph := newPhone(t, 1)
	code := e.mustStart()
	e.advance(seatpair.CodeLifetime + time.Minute)
	if _, err := e.present(code, ph); !errors.Is(err, ErrPairRefused) {
		t.Fatalf("an expired code: %v", err)
	}
	e.clock.Set(start.Add(5 * time.Minute))
	if _, err := e.present(code, ph); !errors.Is(err, ErrPairRefused) {
		t.Fatalf("the code after the clock was set back: %v, want %v", err, ErrPairRefused)
	}
	if st := e.status(); st.Phase != ownerwire.PairExpired {
		t.Fatalf("after the clock was set back: %+v", st)
	}
	// The pairing is over: a new one needs no replace.
	if _, err := e.pairStart(false); err != nil {
		t.Fatalf("a start after it: %v", err)
	}
}

// Likewise a pending credential that was seen past its time.
func TestAnExpiredPendingCredentialDoesNotComeBackWhenTheClockIsSetBack(t *testing.T) {
	e, keys := pairEnv(t)
	e.pairedUp(newPhone(t, 1))
	seen := e.pendingID()
	e.advance(seatpair.PendingLifetime)
	if st := e.status(); st.Phase != ownerwire.PairExpired {
		t.Fatalf("at the end: %+v", st)
	}
	e.clock.Set(start.Add(time.Minute))
	if st := e.status(); st.Phase != ownerwire.PairExpired || st.PendingID != "" {
		t.Fatalf("after the clock was set back: %+v", st)
	}
	if _, err := e.confirmID(seen); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("a confirm after the clock was set back: %v, want %v", err, ErrPairingExpired)
	}
	if len(keys.keys) != 0 {
		t.Fatal("an expired credential was put on the list")
	}
}

// The pairing's deadlines are measured with the clock's reading as the clock
// gives it, monotonic part included: the service hands the pairing the reading
// it took, and keeps its other, wall-clock watch on a copy without it.
func TestThePairingKeepsTheMonotonicReadingOfTheClock(t *testing.T) {
	has := func(tm time.Time) bool { return strings.Contains(tm.String(), "m=") }
	e, _ := pairEnv(t)
	now := time.Now()
	if !has(now) {
		t.Skip("this clock has no monotonic reading")
	}
	e.clock.Set(now)
	code := e.mustStart()
	if !has(e.svc.pr.at) {
		t.Fatalf("the pairing was given a reading without a monotonic part: %v", e.svc.pr.at)
	}
	e.advance(time.Minute)
	if _, err := e.present(code, newPhone(t, 1)); err != nil {
		t.Fatal(err)
	}
	if !has(e.svc.pr.at) {
		t.Fatalf("seat.pair gave the pairing a reading without a monotonic part: %v", e.svc.pr.at)
	}
	if st := e.status(); st.Phase != ownerwire.PairPending {
		t.Fatalf("%+v", st)
	}
	// What is shown and journaled is wall time, in the record's form.
	if st := e.status(); st.ExpiresAt != record.FormatTime(now.Add(time.Minute+seatpair.PendingLifetime)) {
		t.Fatalf("%+v", st)
	}
}

// A reject whose journal write fails answers the error, and the rejection has
// taken effect all the same: the credential is dropped (a drop only lowers what
// anyone may do) and the pairing is over. The answer says the journal failed;
// the status says none.
func TestARejectThatCouldNotBeJournaledStillEndedThePairing(t *testing.T) {
	for name, fail := range map[string]error{"the store is down": errStoreDown, "the file is full": ownerjournal.ErrKeysFull} {
		t.Run(name, func(t *testing.T) {
			e, keys := pairEnv(t)
			ph := newPhone(t, 1)
			code, _ := e.pairedUp(ph)
			id := e.pendingID()
			keys.failReject = fail
			if err := e.rejectID(id); err == nil {
				t.Fatal("a reject that was not journaled answered nil")
			}
			if st := e.status(); st.Phase != ownerwire.PairNone || st.PendingID != "" {
				t.Fatalf("after a failed reject: %+v, want the phase none", st)
			}
			keys.failReject = nil
			if _, err := e.confirmID(id); !errors.Is(err, ErrNoPairing) {
				t.Fatalf("a confirm of the rejected credential: %v, want %v", err, ErrNoPairing)
			}
			if len(keys.keys) != 0 || len(keys.rejected) != 0 {
				t.Fatalf("%d keys, %d rejected", len(keys.keys), len(keys.rejected))
			}
			// The code is spent as for any reject.
			if _, err := e.present(code, ph); !errors.Is(err, ErrPairRefused) {
				t.Fatalf("the code after it: %v", err)
			}
		})
	}
}

// A credential whose display id is on the list already, revoked or not, is
// refused when it is presented, after the code is checked: the pairing is
// ended (the code spent) and the answer is ErrCredentialInvalid, not a
// pending credential that could only fail at the confirm.
func TestAListedCredentialIsRefusedWhenItIsPresented(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		name := "a key in use"
		if revoked {
			name = "a revoked key"
		}
		t.Run(name, func(t *testing.T) {
			e, keys := pairEnv(t)
			ph := newPhone(t, 1)
			keys.seed(ph.displayID(), revoked)
			code := e.mustStart()
			if _, err := e.present(code, ph); !errors.Is(err, ErrCredentialInvalid) {
				t.Fatalf("a listed credential: %v, want %v", err, ErrCredentialInvalid)
			}
			if st := e.status(); st.Phase == ownerwire.PairPending {
				t.Fatalf("a listed credential is pending: %+v", st)
			}
			// The pairing is over and the code spent.
			if _, err := e.present(code, newPhone(t, 2)); !errors.Is(err, ErrPairRefused) {
				t.Fatalf("the code after it: %v, want %v", err, ErrPairRefused)
			}
			if _, err := e.confirm(); !errors.Is(err, ErrNoPairing) {
				t.Fatalf("a confirm: %v, want %v", err, ErrNoPairing)
			}
			if len(keys.keys) != 1 || len(keys.rejected) != 0 {
				t.Fatalf("the list changed: %d keys, %d rejected", len(keys.keys), len(keys.rejected))
			}
			// And a new pairing with another phone goes through.
			if _, err := e.pairStart(false); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// The check comes after the code: a wrong code with a listed credential is a
// wrong code, and counts a try like any other, so the credential's presence on
// the list is not learned by guessing.
func TestAListedCredentialWithAWrongCodeIsAWrongCode(t *testing.T) {
	e, keys := pairEnv(t)
	ph := newPhone(t, 1)
	keys.seed(ph.displayID(), false)
	code := e.mustStart()
	for i := 0; i < seatpair.MaxTries-1; i++ {
		if _, err := e.present("00000000000000000000", ph); !errors.Is(err, ErrPairRefused) {
			t.Fatalf("a wrong code with a listed credential: %v, want %v", err, ErrPairRefused)
		}
	}
	// The tries were counted: the fifth burns the code, even for the right one.
	if _, err := e.present("00000000000000000001", newPhone(t, 2)); !errors.Is(err, ErrPairRefused) {
		t.Fatal(err)
	}
	if _, err := e.present(code, newPhone(t, 3)); !errors.Is(err, ErrPairRefused) {
		t.Fatalf("the right code after five wrong ones: %v", err)
	}
}

// A pairing whose code is burnt by wrong tries reads as expired, with the time
// of the try that burnt it, which is in the past, and not the time the code
// would have ended; and the desk is told by a notice, which holds nothing of
// the code or of what was tried.
func TestABurntCodeReadsExpiredAtTheTimeItWasBurntAndTheDeskIsTold(t *testing.T) {
	e, _ := pairEnv(t)
	ph := newPhone(t, 1)
	code := e.mustStart()
	end := record.FormatTime(start.Add(seatpair.CodeLifetime))
	wrong := "00000000000000000000"
	for i := 0; i < seatpair.MaxTries-1; i++ {
		e.advance(time.Second)
		if _, err := e.present(wrong, ph); !errors.Is(err, ErrPairRefused) {
			t.Fatal(err)
		}
		if n := e.noticed(); len(n) != 0 {
			t.Fatalf("a notice before the code was burnt: %q", n)
		}
	}
	if st := e.status(); st.Phase != ownerwire.PairWaiting || st.ExpiresAt != end {
		t.Fatalf("before the last try: %+v", st)
	}
	e.advance(time.Second)
	burnt := e.clock.Now()
	if _, err := e.present(wrong, ph); !errors.Is(err, ErrPairRefused) {
		t.Fatal(err)
	}
	st := e.status()
	if st.Phase != ownerwire.PairExpired || st.ExpiresAt != record.FormatTime(burnt) || st.ExpiresAt >= end {
		t.Fatalf("a burnt code reads %+v, want expired at %s, before %s", st, record.FormatTime(burnt), end)
	}
	n := e.noticed()
	if len(n) != 1 || strings.Contains(strings.ToUpper(n[0]), code) || strings.Contains(n[0], wrong) {
		t.Fatalf("notices %q", n)
	}
	// Nothing more is said of the tries that follow, the code being dead.
	if _, err := e.present(code, ph); !errors.Is(err, ErrPairRefused) {
		t.Fatal(err)
	}
	if got := e.noticed(); len(got) != 1 {
		t.Fatalf("notices %q", got)
	}
	// A new start clears it.
	e.mustStart()
	if st := e.status(); st.Phase != ownerwire.PairWaiting {
		t.Fatalf("%+v", st)
	}
}
