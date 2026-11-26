package checkpointtest

import (
	"crypto/ed25519"
	"errors"
	"testing"

	cp "ataniq/internal/checkpoint"
)

func rawProofs(t *testing.T, factory Factory, op string) {
	f := start(t, factory)
	if f.RawSubmit == nil {
		t.Fatal("Harness.RawSubmit is missing; declare unsupported raw submission explicitly")
	}
	h := f.home(t, "west", "s", "one", true)
	s := f.enroll(t, h)
	var intent cp.Intent
	if op == "claim" {
		intent = cp.Claim{Mutation: mutation(s, "raw"), NewSessionPublicKey: pub(h.session)}
	} else {
		s = f.claim(t, h, s, "writer")
		intent = advance(s, "raw")
	}
	body, err := intent.Body("")
	check(t, err)
	sign := func(namespace string, signer ed25519.PrivateKey, body []byte) []byte {
		t.Helper()
		sig, err := cp.Sign(namespace, signer, body)
		check(t, err)
		return sig
	}
	refuse := func(name string, body, homeSig, sessionSig []byte) {
		t.Helper()
		_, err := f.RawSubmit(f.ctx, h.slot, pub(h.private), op, body, homeSig, sessionSig)
		if errors.Is(err, errors.ErrUnsupported) {
			t.Skipf("raw submission unavailable: %v", err)
		}
		if !errors.Is(err, cp.ErrRefused) {
			t.Fatalf("%s with %s: got %v, want ErrRefused", op, name, err)
		}
	}
	if op == "claim" {
		for role, session := range map[string]ed25519.PrivateKey{"home": h.private, "witness": f.witness} {
			q := cp.Claim{Mutation: mutation(s, "raw"), NewSessionPublicKey: pub(session)}
			b, err := q.Body("")
			check(t, err)
			refuse("session equal to "+role, b, sign(cp.HomeNamespace, h.private, b), sign(cp.SessionNamespace, session, b))
		}
	}
	homeSig := sign(cp.HomeNamespace, h.private, body)
	sessionSig := sign(cp.SessionNamespace, h.session, body)
	for _, tc := range []struct {
		name          string
		home, session []byte
	}{
		{"missing home", nil, sessionSig},
		{"wrong home key", sign(cp.HomeNamespace, h.session, body), sessionSig},
		{"wrong home namespace", sign(cp.SessionNamespace, h.private, body), sessionSig},
		{"missing session", homeSig, nil},
		{"wrong session key", homeSig, sign(cp.SessionNamespace, h.private, body)},
		{"wrong session namespace", homeSig, sign(cp.HomeNamespace, h.session, body)},
	} {
		refuse(tc.name, body, tc.home, tc.session)
	}
	q := cp.ReadRequest{Slot: h.slot, Challenge: id("after refused proofs"), ReceiptID: id("raw")}
	v, err := f.Admin.Read(f.ctx, q)
	check(t, err)
	check(t, cp.VerifyView(f.pin, q, v))
	if v.Current != s || v.Prior != nil {
		t.Fatal("refused raw submission changed state or retained a receipt")
	}
	// The same body must reach the witness successfully with both valid
	// proofs. A hook that refuses all submissions cannot pass this matrix.
	r, err := f.RawSubmit(f.ctx, h.slot, pub(h.private), op, body, homeSig, sessionSig)
	check(t, err)
	check(t, cp.MatchReceipt(f.pin, s.Epoch, op, body, r))
	q.Challenge = id("after accepted proofs")
	v, err = f.Admin.Read(f.ctx, q)
	check(t, err)
	check(t, cp.VerifyView(f.pin, q, v))
	if v.Prior == nil || !same(*v.Prior, r) || v.Current.LastReceiptDigest != cp.ReceiptDigest(r) {
		t.Fatal("valid raw submission did not commit")
	}
}
