package checkpointtest

import (
	"ataniq/internal/assignment/wire"
	cp "ataniq/internal/checkpoint"
	"encoding/json"
	"testing"
)

func lyingReply(t *testing.T, factory Factory, op, field string) {
	f := start(t, factory)
	h := f.home(t, "west", "s", "one", true)
	s := f.enroll(t, h)
	if op == "advance" {
		s = f.claim(t, h, s, "claim")
	} else if field == "home" {
		// Genesis binds the header digest. Use a later head so the lie can
		// change just the home identity and remain a well-formed state.
		s = f.claim(t, h, s, "initial claim")
		_, err := h.c.Advance(f.ctx, advance(s, "first frame"))
		check(t, err)
		_, err = f.Admin.Release(f.ctx, mutation(f.state(t, h, "advanced"), "release"))
		check(t, err)
		h = f.reconnect(t, h, key("next session"))
		s = f.state(t, h, "ready to claim")
	}
	read := cp.ReadRequest{Slot: h.slot, Challenge: id("before reply fault"), ReceiptID: h.e.ID}
	before, err := h.c.Read(f.ctx, read)
	check(t, err)
	check(t, cp.VerifyView(f.pin, read, before))
	if before.Prior == nil {
		t.Fatal("missing enrollment")
	}
	var real cp.Receipt
	f.TransformNextReply(func(r cp.Receipt) cp.Receipt {
		real = r
		if field == "replay" {
			return *before.Prior
		}
		if field == "profile_home" || field == "profile_session" {
			namespace := cp.HomeNamespace
			if field == "profile_session" {
				namespace = cp.SessionNamespace
			}
			sig, err := cp.Sign(namespace, f.witness, r.Body)
			check(t, err)
			return cp.Receipt{Body: r.Body, Signature: sig}
		}
		var body map[string]any
		check(t, json.Unmarshal(r.Body, &body))
		result := body["result"].(map[string]any)
		signer := f.witness
		switch field {
		case "operation":
			body["operation"] = "release"
		case "id", "request_digest":
			body[field] = digest("another request")
		case "predecessor":
			result["last_receipt_digest"] = digest("another receipt")
		case "head":
			head := result["head"].(map[string]any)
			head["sequence"] = "1"
			head["digest"] = digest("another head")
		case "fence":
			result["fence"] = "200"
		case "session":
			result["session_key_id"] = s.HomeKeyID
		case "epoch":
			result["epoch"] = "2"
		case "sequence goes backwards":
			result["journal_sequence"] = "1"
		case "sequence not bound":
			delete(result, "journal_sequence")
		case "revision":
			result["revision"] = "100"
		case "home":
			other, err := cp.KeyID(pub(key("another home")))
			check(t, err)
			result["home_key_id"] = other
			result["header"].(map[string]any)["key_id"] = other
		case "witness_slot":
			result["slot"].(map[string]any)["witness"] = "another witness"
		case "signer":
			signer = key("another witness")
		}
		b, err := json.Marshal(body)
		check(t, err)
		b, err = wire.Canonicalize(b)
		check(t, err)
		sig, err := cp.Sign(cp.WitnessNamespace, signer, b)
		check(t, err)
		lie := cp.Receipt{Body: b, Signature: sig}
		if field != "signer" && field != "sequence not bound" {
			_, err = cp.VerifyReceipt(f.pin, lie)
			check(t, err)
		}
		return lie
	})
	m := mutation(s, "uncertain")
	if op == "claim" {
		_, err = h.c.Claim(f.ctx, cp.Claim{Mutation: m, NewSessionPublicKey: pub(h.session)})
	} else {
		_, err = h.c.Advance(f.ctx, advance(s, "uncertain"))
	}
	want(t, err, cp.ErrUnknownOutcome)
	read.ReceiptID = m.ID
	read.Challenge = id("reconcile reply fault")
	v, err := h.c.Read(f.ctx, read)
	check(t, err)
	check(t, cp.VerifyView(f.pin, read, v))
	if v.Prior == nil || !same(*v.Prior, real) || v.Current.LastReceiptDigest != cp.ReceiptDigest(real) {
		t.Fatal("could not reconcile original intent")
	}
}
