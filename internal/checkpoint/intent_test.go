package checkpoint

import (
	"bytes"
	"errors"
	"testing"
)

func TestAdapterIntentBoundary(t *testing.T) {
	vs := vectors(t)
	pin := public(fixtureKey("witness"))
	wid, err := KeyID(pin)
	requireOK(t, err)
	for _, op := range []string{"enroll", "claim", "advance", "release", "retire"} {
		t.Run(op, func(t *testing.T) {
			v := vs[op]
			intent, err := ParseIntent(op, wid, v.Body)
			requireOK(t, err)
			b, err := intent.Body(wid)
			requireOK(t, err)
			if !bytes.Equal(b, v.Body) {
				t.Fatal("intent round trip changed bytes")
			}
			for role, sig := range v.Signatures {
				k := fixtureKey(role)
				namespace := "ataniq.checkpoint." + role + ".v1"
				got, err := Sign(namespace, k, b)
				requireOK(t, err)
				if string(got) != sig {
					t.Fatal("signature differs from vector")
				}
				requireOK(t, VerifySignature(namespace, public(k), b, got))
			}
			ack := vs[op+"-ack"]
			r := Receipt{ack.Body, []byte(ack.Signatures["witness"])}
			if err := MatchReceipt(pin, 1, op, b, r); err != nil {
				t.Fatal(err)
			}
			if err := MatchReceipt(pin, 2, op, b, r); !errors.Is(err, ErrUnknownOutcome) {
				t.Fatal("wrong epoch", err)
			}
			other := vs["claim-ack"]
			if op == "claim" {
				other = vs["enroll-ack"]
			}
			if err := MatchReceipt(pin, 1, op, b, Receipt{other.Body, []byte(other.Signatures["witness"])}); !errors.Is(err, ErrUnknownOutcome) {
				t.Fatal("accepted unrelated receipt", err)
			}
			if _, err := ParseIntent("read", wid, b); !errors.Is(err, ErrRefused) {
				t.Fatal("operation confusion", err)
			}
		})
	}
}

func TestSignatureBoundaryPinsProfile(t *testing.T) {
	k := fixtureKey("witness")
	body := vectors(t)["retire"].Body
	sig, err := Sign(WitnessNamespace, k, body)
	requireOK(t, err)
	requireOK(t, VerifySignature(WitnessNamespace, public(k), body, sig))
	for _, namespace := range []string{SessionNamespace, HomeNamespace, "other"} {
		if !errors.Is(VerifySignature(namespace, public(k), body, sig), ErrRefused) {
			t.Fatal("accepted wrong namespace", namespace)
		}
	}
	if _, err := Sign("other", k, body); !errors.Is(err, ErrRefused) {
		t.Fatal("signed unsupported namespace", err)
	}
	if !errors.Is(VerifySignature(WitnessNamespace, public(fixtureKey("other")), body, sig), ErrRefused) {
		t.Fatal("accepted wrong pin")
	}
}
