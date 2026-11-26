package wire

import (
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	a "ataniq/internal/assignment"
)

// Fill the entire encoded reference budget, including six-byte JSON escapes.
func fullRef(prefix string) string {
	n := a.MaxReferenceBytes - len(prefix)
	return prefix + strings.Repeat("\x01", n/6) + strings.Repeat("x", n%6)
}

func TestLargestRequiredSignedRecords(t *testing.T) {
	priv := ed25519.NewKeyFromSeed(make([]byte, 32))
	pub := priv.Public().(ed25519.PublicKey)
	key := a.Key{Home: fullRef("home"), Project: fullRef("project"), Role: fullRef("role")}
	h := a.Header{Schema: 1, Authority: key.Home, Incarnation: fullRef("inc"), Store: fullRef("store"), LedgerID: fullRef("ledger"), KeyID: keyID(pub)}
	signer, err := NewSigner(h, priv)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(h, pub)
	if err != nil {
		t.Fatal(err)
	}
	root, err := h.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	l, err := a.NewLedgerFromHeader(root)
	if err != nil {
		t.Fatal(err)
	}
	var events []a.Event
	binding := func(id string) a.Binding {
		return a.Binding{Kind: "managed", ID: fullRef(id), Authority: fullRef("authority"), Store: fullRef("store"), Incarnation: fullRef("inc"),
			Conversation: fullRef("conversation"), LaunchIntent: fullRef("launch"), RoleEpoch: math.MaxUint64}
	}
	routes := func(prefix string) []string {
		names := []string{"qargi"}
		for i := 1; i < a.MaxRoutes; i++ {
			names = append(names, fullRef(fmt.Sprint(prefix, i)))
		}
		return names
	}
	first, second := routes("old"), routes("new")
	var id string
	actor, auth := fullRef("owner"), fullRef("approval")
	var c a.Command
	start := func(k a.Kind) {
		row, ok := l.Row(key)
		if !ok {
			row.Head.Key = key
		}
		c = a.Command{Kind: k, Expected: row.Head, Actor: actor, Authorization: auth, RequestID: fullRef(fmt.Sprint(len(events), k)), TransferID: id}
	}
	add := func(kind, subject, outcome, route string) {
		c.Evidence = append(c.Evidence, a.Evidence{Kind: kind, Subject: subject, Outcome: outcome, Route: route, Issuer: actor, Revision: fullRef("revision"),
			Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(kind+subject+outcome))), Scope: c.Expected, TransferID: id})
	}
	appendSigned := func() {
		t.Helper()
		e, _, err := a.Decide(l, c)
		if err != nil {
			t.Fatalf("%s: %v", c.Kind, err)
		}
		b, err := e.SigningBytes()
		if err != nil {
			t.Fatal(err)
		}
		e.Signature, err = signer.Sign(h, b)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := e.RecordBytes()
		if err != nil || len(raw) > 64<<10 {
			t.Fatal(c.Kind, len(raw), err)
		}
		decoded, err := a.DecodeRecord(raw)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, decoded)
		l, err = a.ReplayFromHeader(root, events, verifier)
		if err != nil {
			t.Fatal(c.Kind, err)
		}
	}
	// Follow-ups use the original reservation/commit even when the head moves.
	receipt := func(k a.Kind, route string) {
		start(k)
		tr, _ := l.Transfer(key, id)
		scope, event, outcome := tr.Committed, tr.CommittedEventID, "installed-stale-refused"
		if k == a.FenceReceipt {
			scope, event, outcome = tr.Reservation, tr.ReservationEventID, "sealed-and-drained"
		}
		add("route", route, outcome, "")
		if k == a.FenceReceipt {
			for _, e := range tr.Outstanding {
				if e.Route == route {
					add("effect", e.ID, "settled", route)
				}
			}
		}
		for i := range c.Evidence {
			c.Evidence[i].Scope, c.Evidence[i].ScopeEventID = scope, event
		}
		appendSigned()
	}
	open := func(names []string) {
		for _, r := range names {
			receipt(a.ConfirmReceipt, r)
		}
		start(a.Open)
		add("enrollment", c.Expected.BindingID, "current", "")
		appendSigned()
	}
	capture := func(k a.Kind, candidate a.Binding, enabled, retired []string) {
		start(k)
		c.Candidate = candidate
		if k == a.Seed {
			add("enrollment", candidate.ID, "verified", "")
		} else {
			add("barrier", c.Expected.BindingID, "captured", "")
			for i := 0; i < a.MaxOutstandingEffects; i++ {
				add("effect", fullRef(fmt.Sprint("effect", i)), "outstanding", enabled[1])
			}
		}
		for _, r := range enabled {
			add("route", r, "enabled", "")
		}
		for _, r := range retired {
			add("route", r, "retired-drained-disabled", "")
		}
		appendSigned()
	}
	id = fullRef("seed")
	fallback := binding("old")
	fallback.Kind = "legacy"
	capture(a.Seed, fallback, first, nil)
	open(first)
	id = fullRef("forward")
	capture(a.Freeze, binding("new"), second, first[1:])
	start(a.Inventory)
	add("inventory", key.Project, "complete", "")
	add("handoff", key.Project, "verified", "")
	for _, r := range second {
		add("route", r, "frozen", "")
	}
	for i := 0; i < a.MaxOutstandingEffects; i++ {
		add("effect", fullRef(fmt.Sprint("effect", i)), "outstanding", second[1])
	}
	appendSigned()
	tr, _ := l.Transfer(key, id)
	saved, prefix := l, slices.Clone(events)
	start(a.BeliefAccepted)
	add("belief", tr.Inventory.Digest, "mismatch", "")
	appendSigned()
	start(a.AbortPrepared)
	add("preview", tr.Candidate.ID, "stopped", "")
	add("candidate", tr.Candidate.ID, "never-executed", "")
	appendSigned()
	aborting, abortPrefix := l, slices.Clone(events)
	for _, k := range []a.Kind{a.Aborted, a.AbortedClosed} {
		l, events = aborting, slices.Clone(abortPrefix)
		start(k)
		for _, e := range tr.Outstanding {
			add("effect", e.ID, "settled", e.Route)
		}
		if k == a.Aborted {
			for _, r := range second {
				add("route", r, "safe-to-reopen", "")
			}
		} else {
			add("incumbent", c.Expected.BindingID, "irreversibly-fenced", "")
		}
		appendSigned()
	}
	l, events = saved, prefix
	start(a.BeliefAccepted)
	add("belief", tr.Inventory.Digest, "matched", "")
	appendSigned()
	start(a.Reserve)
	c.Candidate = tr.Candidate
	add("decision", tr.Candidate.ID, "authorized", "")
	appendSigned()
	for _, r := range second {
		receipt(a.FenceReceipt, r)
	}
	start(a.Fenced)
	add("revalidation", tr.Inventory.Digest, "current", "")
	appendSigned()
	tr, _ = l.Transfer(key, id)
	start(a.Commit)
	c.Candidate = tr.Candidate
	add("revalidation", tr.Decision.Digest, "current", "")
	appendSigned()
	committed, commitPrefix := l, slices.Clone(events)
	open(second)
	start(a.HoldStarted)
	c.HoldStart, c.EarliestRetirement = 1, 1+14*24*60*60
	add("fallback", fallback.ID, "stopped-disabled-preserved", "")
	appendSigned()
	preservation := c.Evidence[0].Digest
	start(a.HoldReviewed)
	add("fallback", preservation, "reviewed", "")
	appendSigned()
	l, events = committed, commitPrefix
	id = fullRef("rollback")
	capture(a.RollbackFreeze, fallback, first, second[1:])

	// The historical correction and the acknowledgement combine identifiers
	// from different transfers. Both may have reached their full budget.
	ackID := id
	id = events[2].Command.TransferID
	start(a.ConfirmReceipt)
	amended := events[2].Command.Evidence[0]
	amended.Outcome, amended.Digest = fullRef("amended"), fmt.Sprintf("%x", sha256.Sum256([]byte("amended")))
	c.Evidence = []a.Evidence{amended}
	for i := 0; i < a.MaxOutstandingEffects; i++ {
		add("effect", fullRef(fmt.Sprint("late", i)), "outstanding", amended.Subject)
		c.Evidence[i+1].Scope, c.Evidence[i+1].ScopeEventID = amended.Scope, amended.ScopeEventID
	}
	appendSigned()
	id = ackID
	start(a.QuarantineAcknowledged)
	row, _ := l.Row(key)
	add("owner-disposition", auth, fullRef("disposition"), "")
	c.Evidence[0].ScopeEventID = row.QuarantineEventID
	appendSigned()
}
