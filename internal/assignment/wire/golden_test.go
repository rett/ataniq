package wire

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ataniq/internal/assignment"
)

var update = flag.Bool("update", false, "regenerate the assignment wire golden fixtures")

const identity = "assignment-fixture"

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type pendingGoldens map[string][]byte

func (pending pendingGoldens) check(t *testing.T, name string, want []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		pending[path] = bytes.Clone(want)
		return
	}
	if !bytes.Equal(readFixture(t, path), want) {
		t.Fatalf("%s differs from golden", name)
	}
}

func (pending pendingGoldens) event(t *testing.T, name string, e assignment.Event) {
	t.Helper()
	body, err := Marshal(e.Public)
	if err != nil {
		t.Fatal(err)
	}
	pending.check(t, name+".json", body)
	blob, err := base64.RawURLEncoding.Strict().DecodeString(e.Signature)
	if err != nil {
		t.Fatal(err)
	}
	pending.check(t, name+".sig", []byte(armor(blob)))
	p, err := Unmarshal(body)
	if err != nil || !reflect.DeepEqual(p, e.Public) {
		t.Fatal(name, err)
	}
}

var rowKey = assignment.Key{Home: "fixture-home", Project: "fixture-project", Role: "lead"}

func fixtureCommand(l assignment.Ledger, k assignment.Kind, id string) assignment.Command {
	r, exists := l.Row(rowKey)
	if !exists {
		r.Head.Key = rowKey
	}
	tr, _ := l.Transfer(rowKey, r.LastTransferID)
	c := assignment.Command{Kind: k, Expected: r.Head, TransferID: id, RequestID: fmt.Sprintf("%s-%d", k, r.Head.Revision),
		Actor: "fixture-owner", Authorization: "fixture-decision"}
	scope := r.Head
	var eventID string
	add := func(kind, subject, outcome string) {
		digest := sha256.Sum256([]byte(strings.Join([]string{kind, subject, outcome}, "\x00")))
		c.Evidence = append(c.Evidence, assignment.Evidence{Kind: kind, Subject: subject, Outcome: outcome,
			Issuer: "fixture-source", Revision: "source-rev-A", Digest: hex.EncodeToString(digest[:]),
			Scope: scope, ScopeEventID: eventID, TransferID: id})
	}
	switch k {
	case assignment.Seed, assignment.Freeze, assignment.RollbackFreeze:
		c.Candidate = assignment.Binding{Kind: "legacy", ID: "old", Authority: "fixture-a", Store: "store-a", Incarnation: "one", Conversation: "a"}
		if k == assignment.Seed {
			add("enrollment", c.Candidate.ID, "verified")
		} else {
			c.Candidate = assignment.Binding{Kind: "managed", ID: "new", Authority: "fixture-b", Store: "store-b", Incarnation: "two", Conversation: "b"}
			if k == assignment.RollbackFreeze {
				c.Candidate = r.Previous
			}
			add("barrier", r.Holder.ID, "captured")
		}
		add("route", "qargi", "enabled")
	case assignment.Inventory:
		add("inventory", rowKey.Project, "complete")
		add("handoff", rowKey.Project, "verified")
		add("route", "qargi", "frozen")
	case assignment.BeliefAccepted:
		add("belief", tr.Inventory.Digest, "matched")
	case assignment.Reserve:
		c.Candidate = tr.Candidate
		add("decision", c.Candidate.ID, "authorized")
	case assignment.FenceReceipt:
		scope, eventID = tr.Reservation, tr.ReservationEventID
		add("route", "qargi", "sealed-and-drained")
	case assignment.Fenced:
		add("revalidation", tr.Inventory.Digest, "current")
	case assignment.Commit:
		c.Candidate = tr.Candidate
		add("revalidation", tr.Decision.Digest, "current")
	case assignment.ConfirmReceipt:
		scope, eventID = tr.Committed, tr.CommittedEventID
		add("route", "qargi", "installed-stale-refused")
	case assignment.Open:
		add("enrollment", r.Holder.ID, "current")
	case assignment.HoldStarted:
		c.HoldStart, c.EarliestRetirement = 10, 10+14*24*3600
		add("fallback", tr.Predecessor.ID, "stopped-disabled-preserved")
	case assignment.HoldReviewed:
		held, _ := l.Transfer(rowKey, id)
		add("fallback", held.Hold.Preservation.Digest, "reviewed")
	case assignment.AbortPrepared:
		add("candidate", tr.Candidate.ID, "never-executed")
	case assignment.Aborted:
		add("route", "qargi", "safe-to-reopen")
	}
	return c
}

func TestRegenerationWaitsForIndependentCheck(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("testdata", 0755); err != nil {
		t.Fatal(err)
	}
	const path = "testdata/01-seed.json"
	if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
		t.Fatal(err)
	}
	old := *update
	*update = true
	t.Cleanup(func() { *update = old })
	pending := make(pendingGoldens)
	pending.check(t, "01-seed.json", []byte("projection error"))
	if string(readFixture(t, path)) != "original" {
		t.Fatal("regeneration wrote before the independent projection check")
	}
}

func TestGoldenEvents(t *testing.T) {
	pending := make(pendingGoldens)
	seed, err := hex.DecodeString(strings.TrimSpace(string(readFixture(t, "testdata/test-key.seed"))))
	if err != nil || len(seed) != ed25519.SeedSize {
		t.Fatal("invalid test seed", err)
	}
	key := ed25519.NewKeyFromSeed(seed)
	pub := key.Public().(ed25519.PublicKey)
	pending.check(t, "allowed_signers", []byte(identity+" "+keyType+" "+base64.StdEncoding.EncodeToString(keyBlob(pub))+"\n"))
	pin, id, err := LoadPublicKey(string(readFixture(t, "testdata/allowed_signers")))
	if err != nil {
		t.Fatal(err)
	}
	h := assignment.Header{Authority: rowKey.Home, Store: "home-store", Incarnation: "first", KeyID: id, LedgerID: "fixture-ledger", Schema: 1}
	s, err := NewSigner(h, key)
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVerifier(h, pin)
	if err != nil {
		t.Fatal(err)
	}
	l, err := assignment.NewLedger(h)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(before assignment.Ledger, c assignment.Command, name string) (assignment.Ledger, assignment.Event) {
		t.Helper()
		e, _, err := assignment.Decide(before, c)
		if err != nil || e == nil {
			t.Fatalf("decide %s: %v", name, err)
		}
		message, err := e.SigningBytes()
		if err != nil {
			t.Fatal(err)
		}
		e.Signature, err = s.Sign(h, message)
		if err != nil {
			t.Fatal(err)
		}
		if name != "" {
			pending.event(t, name, *e)
		}
		after, err := assignment.Apply(before, *e)
		if err != nil {
			t.Fatal(err)
		}
		return after, *e
	}
	var events []assignment.Event
	var begin assignment.Event
	var cut assignment.Checkpoint
	transfer := "seed"
	for i, step := range []struct {
		name string
		kind assignment.Kind
	}{
		{"seed", assignment.Seed}, {"seed-confirm", assignment.ConfirmReceipt}, {"seed-open", assignment.Open},
		{"begin", assignment.Freeze}, {"inventory", assignment.Inventory}, {"preview", assignment.BeliefAccepted},
		{"reserve", assignment.Reserve}, {"fence-receipt", assignment.FenceReceipt}, {"fence", assignment.Fenced},
		{"commit", assignment.Commit}, {"confirm", assignment.ConfirmReceipt}, {"open", assignment.Open}, {"hold", assignment.HoldStarted},
	} {
		if step.kind == assignment.Freeze {
			transfer = "move"
		}
		c := fixtureCommand(l, step.kind, transfer)
		before := l
		var e assignment.Event
		l, e = apply(l, c, fmt.Sprintf("%02d-%s", i+1, step.name))
		events = append(events, e)
		switch step.kind {
		case assignment.Freeze:
			begin = e
			competing := c
			competing.Candidate.ID = "other-candidate"
			_, fork := apply(before, competing, "conflict")
			if _, _, err := assignment.Decide(l, fork.Command); !errors.Is(err, assignment.ErrConflict) {
				t.Fatal("conflict", err)
			}
		case assignment.Reserve:
			branch, _ := apply(l, fixtureCommand(l, assignment.AbortPrepared, transfer), "abort-prepared")
			apply(branch, fixtureCommand(branch, assignment.Aborted, transfer), "abort")
		case assignment.Fenced:
			pending.event(t, "cut", e)
			short, err := assignment.Replay(h, events, v)
			if err != nil {
				t.Fatal(err)
			}
			cut = short.Checkpoint()
		case assignment.Commit:
			if err := assignment.VerifyCheckpoint(cut, l.Checkpoint()); !errors.Is(err, assignment.ErrCheckpoint) {
				t.Fatal("accepted cut", err)
			}
		}
	}
	if e, r, err := assignment.Decide(l, begin.Command); err != nil || e != nil || r != begin.Result {
		t.Fatal("duplicate", err)
	}
	pending.event(t, "duplicate", begin)
	for _, kind := range []assignment.Kind{assignment.RollbackFreeze, assignment.Inventory, assignment.HoldReviewed} {
		id := "reverse"
		if kind == assignment.HoldReviewed {
			id = "move"
		}
		c := fixtureCommand(l, kind, id)
		if kind == assignment.Inventory {
			c.Evidence[0].Digest = fmt.Sprintf("%x", sha256.Sum256([]byte("reverse inventory")))
		}
		var e assignment.Event
		l, e = apply(l, c, "")
		events = append(events, e)
	}
	replayed, err := assignment.Replay(h, events, v)
	if err != nil || replayed.Checkpoint() != l.Checkpoint() {
		t.Fatal("replay", err)
	}
	// Independently constructed rollback/hold-review projection and signed cursor.
	// This comparison also runs during -update; its inputs are read-only.
	last := events[len(events)-1]
	body, err := Marshal(last.Public)
	if err != nil || !bytes.Equal(body, readFixture(t, "testdata/historical-review.json")) {
		t.Fatal("historical review differs from independent body", err)
	}
	blob, err := unarmor(string(readFixture(t, "testdata/historical-review.sig")))
	if err != nil || base64.RawURLEncoding.EncodeToString(blob) != last.Signature {
		t.Fatal("historical review differs from OpenSSH signature", err)
	}
	row, _ := replayed.Row(rowKey)
	if row.PublicDigest != "de1e8d0ab38529b39846d8a4c9421e4fb27ea583b5a81b1f63149d6def8a8c26" {
		t.Fatal("historical review differs from independent signed cursor", row.PublicDigest)
	}
	for path, contents := range pending {
		if err := os.WriteFile(path, contents, 0644); err != nil {
			t.Fatal(err)
		}
	}
}
