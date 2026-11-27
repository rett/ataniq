package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	a "ataniq/internal/assignment"
	"ataniq/internal/assignment/wire"
	cp "ataniq/internal/checkpoint"
	"ataniq/internal/checkpoint/link"
	"ataniq/internal/enrollment"
)

// Commit the real request to the memory witness and lose only its reply.
type lostReply struct {
	cp.Client
	advance cp.Advance
}

func (w *lostReply) Advance(ctx context.Context, q cp.Advance) (cp.Receipt, error) {
	w.advance = q
	_, err := w.Client.Advance(ctx, q)
	if err != nil {
		return cp.Receipt{}, err
	}
	return cp.Receipt{}, cp.ErrUnknownOutcome
}

func signedEvent(t *testing.T, store *enrollment.Store, b enrollment.Binding, key ed25519.PrivateKey) a.Event {
	t.Helper()
	l, err := store.Ledger()
	must(t, err)
	h := a.Head{Key: a.Key{Home: b.Slot.Authority, Project: "project", Role: "lead"}}
	c := a.Command{Kind: a.Seed, Expected: h, RequestID: "request", TransferID: "transfer", Actor: "owner", Authorization: "approval",
		Candidate: a.Binding{Kind: "legacy", ID: "holder", Authority: h.Key.Home, Store: "store", Incarnation: "incarnation", Conversation: "conversation"}}
	for _, e := range [][3]string{{"enrollment", "holder", "verified"}, {"route", "qargi", "enabled"}} {
		c.Evidence = append(c.Evidence, a.Evidence{Kind: e[0], Subject: e[1], Outcome: e[2], Issuer: "owner", Revision: "one", Digest: strings.Repeat("a", 64), Scope: h, TransferID: c.TransferID})
	}
	event, _, err := a.Decide(l, c)
	must(t, err)
	signer, err := wire.NewSigner(b.Header, key)
	must(t, err)
	body, err := event.SigningBytes()
	must(t, err)
	event.Signature, err = signer.Sign(b.Header, body)
	must(t, err)
	return *event
}

func TestServeRestoresPendingAdvance(t *testing.T) {
	path, _, witness, _ := ceremonyConfig(t)
	call(t, path, "identity-init")
	c, err := loadConfig(path, fullPhase)
	must(t, err)
	p, err := loadPairing(c)
	must(t, err)
	ctx := t.Context()
	w, err := cp.NewMemory(c.Slot.Witness, c.Epoch, witness)
	must(t, err)
	home := p.home.key.Public().(ed25519.PublicKey)
	must(t, w.Grant(c.Slot, home, true))
	root := enrollment.FileRoot{Path: c.State}
	b, err := enrollment.Enroll(ctx, root, w.Administration(), c.Slot, c.Epoch, p.home.key, p.witness, nil)
	must(t, err)
	must(t, enrollment.WriteBinding(c.Binding, b))
	_, session, err := ed25519.GenerateKey(nil)
	must(t, err)
	client, err := w.Connect(c.Slot, p.home.key, session, p.witness)
	must(t, err)
	backend := &lostReply{Client: client}
	s, err := enrollment.Start(ctx, root, b, backend, session, nil)
	must(t, err)
	defer s.Close()
	_, err = enrollment.AppendAndAdvance(ctx, s, signedEvent(t, s, b, p.home.key))
	if !errors.Is(err, cp.ErrUnknownOutcome) {
		t.Fatal(err)
	}
	must(t, s.Close())
	stage := filepath.Join(c.State, "advance.pending")
	saved := read(t, stage)
	body, err := cp.AdvanceBody(backend.advance)
	must(t, err)
	want := link.PendingMutation{ID: backend.advance.ID, Digest: cp.RequestDigest(body)}
	for _, path := range []string{c.Binding, ""} {
		c.Binding = path
		ep, err := homeEndpoint(c, p)
		must(t, err)
		if got, ok := ep.PendingMutation(); !ok || got != want {
			t.Fatal("lost staged mutation", got)
		}
		other := backend.advance
		other.ID[0] ^= 1
		raw, err := cp.AdvanceBody(other)
		must(t, err)
		bounded, cancel := context.WithTimeout(ctx, time.Second)
		_, err = ep.RawSubmit(bounded, c.Slot, home, "advance", raw, nil, nil)
		cancel()
		if !errors.Is(err, link.ErrNotSent) || !errors.Is(err, cp.ErrBusy) {
			t.Fatal("different mutation after restart", err)
		}
	}
	c.Epoch++
	if _, err = homeEndpoint(c, p); err == nil {
		t.Fatal("restored an advance from another epoch")
	}
	if !bytes.Equal(saved, read(t, stage)) {
		t.Fatal("startup changed the durable stage")
	}
	if got := call(t, path, "status"); !strings.Contains(got, "ledger_head_sequence=1\n") {
		t.Fatal(got)
	}
	ledger, err := os.OpenFile(filepath.Join(c.State, "events.v1.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	_, err = ledger.WriteString("torn")
	must(t, err)
	must(t, ledger.Close())
	got, _ := cli(t, 1, "status", "--config", path)
	if strings.Contains(got, "ledger_head_sequence=") {
		t.Fatal("reported a head for a torn ledger", got)
	}
}
