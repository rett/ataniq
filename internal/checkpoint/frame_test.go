package checkpoint

import (
	"ataniq/internal/assignment"
	"bytes"
	"strings"
	"testing"
)

func TestAdvanceFrameBudget(t *testing.T) {
	f := newSetup(t)
	name := strings.Repeat("<", assignment.MaxReferenceBytes)
	w, err := NewMemory(name, 1, f.witness)
	requireOK(t, err)
	slot := Slot{name, name, name}
	requireOK(t, w.Grant(slot, public(f.home), true))
	c, err := w.Connect(slot, f.home, f.session, public(f.witness))
	requireOK(t, err)
	homeID, err := KeyID(public(f.home))
	requireOK(t, err)
	h := assignment.Header{Schema: 1, Authority: name, Store: name, Incarnation: name, LedgerID: name, KeyID: homeID}
	raw, err := h.Bytes()
	requireOK(t, err)
	e := Enrollment{Slot: slot, ID: fixtureID("enroll"), HeaderBytes: raw, HomePublicKey: public(f.home)}
	e.ProofOfPossession, err = ProveEnrollment(f.home, e)
	requireOK(t, err)
	r, err := w.Administration().Enroll(t.Context(), e)
	requireOK(t, err)
	a, err := VerifyReceipt(public(f.witness), r)
	requireOK(t, err)
	r, err = c.Claim(t.Context(), Claim{Mutation{fixtureID("claim"), a.State}, public(f.session)})
	requireOK(t, err)
	a, err = VerifyReceipt(public(f.witness), r)
	requireOK(t, err)
	next := a.State.Head
	next.Sequence++
	next.Digest = idString(fixtureID("head"))
	r, err = c.Advance(t.Context(), Advance{Mutation{fixtureID("advance"), a.State}, next})
	requireOK(t, err)
	a, err = VerifyReceipt(public(f.witness), r)
	requireOK(t, err)
	if a.State.Head != next {
		t.Fatal("wrong head")
	}
	atLimit := append(bytes.Repeat([]byte(" "), maxFrame-1), '0')
	_, err = canonical(atLimit)
	requireOK(t, err)
	if _, err = canonical(append(atLimit, ' ')); err == nil {
		t.Fatal("oversize input accepted")
	}
}
