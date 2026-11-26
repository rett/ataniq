package assignment

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var header = Header{Schema: 1, Authority: "home", Store: "store", Incarnation: "incarnation", KeyID: "enrolled-key", LedgerID: "ledger"}
var errSignature = errors.New("test signature mismatch")

type testKey struct{}

func (testKey) Sign(h Header, b []byte) (string, error) {
	return h.KeyID + "/" + sha256Hex(b), nil
}
func (k testKey) Verify(h Header, b []byte, sig string) error {
	want, _ := k.Sign(h, b)
	if sig != want {
		return errSignature
	}
	return nil
}

var _ Signer = testKey{}

func TestCanonicalCommitments(t *testing.T) {
	s := fresh(t)
	c := next(s.ledger, key, Seed, "T", "")
	other := c
	c.Actor, c.Authorization = "a\x00b", "c"
	other.Actor, other.Authorization = "a", "b\x00c"
	one, _ := requestDigest(c)
	two, _ := requestDigest(other)
	if one == two {
		t.Fatal("field boundary lost")
	}
	e := s.accept(t, c)
	hash, _ := ledgerDigest(e)
	// Independently computed SHA-256 over each domain, NUL and its schema-1 bytes.
	const wantRequest = "16d174bd564a20701b49ea795dffe1ef6974d5c080bcf4a997f0d1747663f3d3"
	const wantPublic = "5b88a159cdd5a0cbd9d7a677cfbcedb612ebd9e9703d1018ef5ec35e8c65139b"
	const wantLedger = "5a3d82948af6e54770d337a84773ab34b3b2f3ddc2b30106aaebd7cff1c5d6f8"
	if one != wantRequest || e.Result.EventID != wantPublic || hash != wantLedger {
		t.Fatal("commitment differs from golden", one, e.Result.EventID, hash)
	}
	old := hash
	e.Signature += "changed"
	hash, _ = ledgerDigest(e)
	if hash == old {
		t.Fatal("ledger omits signature")
	}
	other.Evidence = slices.Clone(c.Evidence)
	slices.Reverse(other.Evidence)
	other.Actor, other.Authorization = c.Actor, c.Authorization
	refused(t, s.ledger, other, ErrConflict)
	other.Actor = strings.Repeat("x", recordLimit)
	refused(t, s.ledger, other, ErrConflict)
	other.RequestID = "fresh"
	refused(t, s.ledger, other, ErrLimit)
}

func TestInterleavedRows(t *testing.T) {
	s := fresh(t)
	other := key
	other.Project = "second"
	for _, step := range seedSteps {
		for _, k := range []Key{key, other} {
			e := s.accept(t, next(s.ledger, k, step.kind, "same", step.route))
			if e.Sequence != uint64(len(s.events)) {
				t.Fatal(e.Sequence)
			}
		}
	}
	l, err := Replay(header, s.events, testKey{})
	if err != nil || !reflect.DeepEqual(l, s.ledger) {
		t.Fatal(err)
	}
	a, _ := Lookup(l, key, "same", "")
	b, _ := Lookup(l, other, "same", "")
	if a.Head.Key != key || b.Head.Key != other || a.Head.Revision != b.Head.Revision || a.EventID == b.EventID {
		t.Fatal(a, b)
	}
}

type trace struct {
	ledger Ledger
	events []Event
}

func fresh(t *testing.T) trace {
	t.Helper()
	l, err := NewLedger(header)
	if err != nil {
		t.Fatal(err)
	}
	return trace{ledger: l}
}

func next(l Ledger, key Key, k Kind, id, route string) Command {
	r, ok := l.Row(key)
	if !ok {
		r.Head.Key = key
	}
	tr, _ := l.Transfer(key, r.LastTransferID)
	return command(r, tr, k, id, route)
}

func (s *trace) accept(t *testing.T, c Command) Event {
	t.Helper()
	e, result, err := Decide(s.ledger, c)
	if err != nil || e == nil || e.Result != result {
		t.Fatalf("decide %s: %v, %+v", c.Kind, err, result)
	}
	b, err := e.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	e.Signature, err = (testKey{}).Sign(header, b)
	if err != nil {
		t.Fatal(err)
	}
	s.ledger, err = Apply(s.ledger, *e)
	if err != nil {
		t.Fatalf("apply %s: %v", c.Kind, err)
	}
	s.events = append(s.events, *e)
	return *e
}

func (s *trace) steps(t *testing.T, id string, steps []step) {
	t.Helper()
	for _, x := range steps {
		s.accept(t, next(s.ledger, key, x.kind, id, x.route))
	}
}

func seeded(t *testing.T) trace {
	t.Helper()
	s := fresh(t)
	s.steps(t, "seed", seedSteps)
	return s
}

func refused(t *testing.T, l Ledger, c Command, want error) {
	t.Helper()
	e, _, err := Decide(l, c)
	if e != nil || !errors.Is(err, want) {
		t.Fatalf("%s: got event %v, error %v; want %v", c.Kind, e, err, want)
	}
}

func TestHistoryAndIsolation(t *testing.T) {
	s := seeded(t)
	start := s.ledger
	c := next(start, key, Freeze, "T", "")
	competing := next(start, key, Freeze, "U", "")
	e := s.accept(t, c)
	c.Evidence[0].Subject = "changed"
	refused(t, s.ledger, c, ErrConflict)
	if e.Command.Evidence[0].Subject == "changed" {
		t.Fatal("decision aliases caller evidence")
	}
	tr, _ := s.ledger.Transfer(key, "T")
	tr.Routes[0] = "changed"
	refused(t, s.ledger, competing, ErrStale)
	s.steps(t, "T", forwardSteps[1:])
	got, ok := Lookup(s.ledger, key, "T", "")
	if !ok || got.Disposition != "confirmed" {
		t.Fatal(got, ok)
	}
	dup, res, err := Decide(s.ledger, e.Command)
	if err != nil || dup != nil || res != e.Result {
		t.Fatal(dup, res, err)
	}
	if got, ok := Lookup(s.ledger, key, "T", c.RequestID); !ok || got != e.Result {
		t.Fatal(got, ok)
	}
	if _, ok := Lookup(start, key, "T", ""); ok {
		t.Fatal("older ledger changed")
	}
	c = next(s.ledger, key, Freeze, "T", "")
	refused(t, s.ledger, c, ErrConflict)
	replayed, err := Replay(header, s.events, testKey{})
	if err != nil || !reflect.DeepEqual(replayed, s.ledger) {
		t.Fatalf("replayed indexes: %v", err)
	}
}

func TestAllCASFields(t *testing.T) {
	s := seeded(t)
	s.steps(t, "T", forwardSteps[:1])
	for name, mutate := range map[string]func(*Command){
		"project":         func(c *Command) { c.Expected.Key.Project += "2" },
		"role":            func(c *Command) { c.Expected.Key.Role += "2" },
		"generation":      func(c *Command) { c.Expected.Generation++ },
		"revision":        func(c *Command) { c.Expected.Revision-- },
		"binding":         func(c *Command) { c.Expected.BindingID = "other" },
		"active transfer": func(c *Command) { c.Expected.TransferID = "U" },
		"phase":           func(c *Command) { c.Expected.Phase = PhaseReserved },
		"operation transfer": func(c *Command) {
			c.TransferID = "U"
			for i := range c.Evidence {
				c.Evidence[i].TransferID = "U"
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := next(s.ledger, key, Inventory, "T", "")
			mutate(&c)
			refused(t, s.ledger, c, ErrStale)
		})
	}
}

func TestReplayChecksChainAndReduction(t *testing.T) {
	s := seeded(t)
	before := s.ledger
	s.steps(t, "T", forwardSteps[:2])
	for name, mutate := range map[string]func(*Event){
		"gap":             func(e *Event) { e.Sequence++ },
		"previous hash":   func(e *Event) { e.PreviousHash = "fork" },
		"row predecessor": func(e *Event) { e.Command.Expected.Revision++; e.RequestDigest, _ = requestDigest(e.Command) },
		"request digest":  func(e *Event) { e.Command.Actor = "other" },
		"result":          func(e *Event) { e.Result.Head.Phase = PhaseConfirmed },
		"schema":          func(e *Event) { e.Schema++ },
	} {
		t.Run(name, func(t *testing.T) {
			e := s.events[len(seedSteps)]
			mutate(&e)
			l, err := Apply(before, e)
			if err == nil || !reflect.DeepEqual(l, before) {
				t.Fatal("accepted or partially applied invalid event")
			}
		})
	}
	short, err := Replay(header, s.events[:len(seedSteps)], testKey{})
	if err != nil || VerifyCheckpoint(short.Checkpoint(), before.Checkpoint()) != nil {
		t.Fatal(err)
	}
	if VerifyCheckpoint(short.Checkpoint(), s.ledger.Checkpoint()) == nil {
		t.Fatal("restored prefix matched newer pin")
	}
	fork := trace{ledger: before}
	fork.steps(t, "U", forwardSteps[:1])
	if _, err := Apply(fork.ledger, s.events[len(seedSteps)+1]); !errors.Is(err, ErrChain) {
		t.Fatal(err)
	}
	bad := slices.Clone(s.events)
	bad[1].Signature = "other"
	if l, err := Replay(header, bad, testKey{}); !errors.Is(err, errSignature) || l.checkpoint != (Checkpoint{}) {
		t.Fatal(err)
	}
}

func TestCounterExhaustion(t *testing.T) {
	s := seeded(t)
	s.steps(t, "T", forwardSteps[:7])
	for _, counter := range []string{"sequence", "revision", "generation"} {
		t.Run(counter, func(t *testing.T) {
			l := s.ledger
			l.rows = map[Key]Row{key: l.rows[key]}
			r := l.rows[key]
			switch counter {
			case "sequence":
				l.checkpoint.Sequence = math.MaxUint64
			case "revision":
				r.Head.Revision = math.MaxUint64
			case "generation":
				r.Head.Generation = math.MaxUint64
			}
			l.rows[key] = r
			refused(t, l, next(l, key, Commit, "T", ""), ErrOverflow)
		})
	}
}

func TestReceiptNamesExactEvent(t *testing.T) {
	for _, tc := range []struct {
		target, receipt Kind
		n               int
	}{{Seed, ConfirmReceipt, 0}, {Reserve, FenceReceipt, 3}, {Commit, ConfirmReceipt, 7}} {
		t.Run(string(tc.target), func(t *testing.T) {
			s := fresh(t)
			id := "seed"
			if tc.target != Seed {
				s = forward(t, tc.n)
				id = "T"
			}
			a, b := trace{ledger: s.ledger}, trace{ledger: s.ledger}
			c := next(s.ledger, key, tc.target, id, "")
			ea := a.accept(t, c)
			c.Actor = "other-owner"
			eb := b.accept(t, c)
			if ea.Result.Head != eb.Result.Head || ea.Result.EventID == eb.Result.EventID {
				t.Fatal("targets must fork at equal heads")
			}
			receipt := next(a.ledger, key, tc.receipt, id, "worker")
			refused(t, b.ledger, receipt, ErrEvidence)
			b.accept(t, next(b.ledger, key, tc.receipt, id, "worker"))
		})
	}
}

func TestReviewHistoricalHold(t *testing.T) {
	s := forward(t, len(forwardSteps))
	s.steps(t, "T", []step{{kind: HoldStarted}})
	held, _ := s.ledger.Transfer(key, "T")
	s.steps(t, "U", []step{{kind: RollbackFreeze}})
	active, _ := s.ledger.Transfer(key, "U")
	row, _ := s.ledger.Row(key)
	c := command(row, held, HoldReviewed, "T", "")
	event := s.accept(t, c)
	body, err := event.SigningBytes()
	if err != nil || !bytes.Contains(body, []byte(`"active_transfer_id":"U"`)) || event.Public.TransferID != "T" {
		t.Fatal("historical review lacks active rollback identity", err)
	}
	l, err := recoverAt(s.events, s.ledger.Checkpoint())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := l.Row(key)
	row.Head.Revision++
	row.PublicDigest = got.PublicDigest
	u, _ := l.Transfer(key, "U")
	reviewed, _ := l.Transfer(key, "T")
	held.Hold.Review = c.Evidence[0]
	if got != row || !reflect.DeepEqual(u, active) || !reflect.DeepEqual(held, reviewed) {
		t.Fatal(got, u, reviewed)
	}
	dup, result, err := Decide(l, c)
	if err != nil || dup != nil || result != event.Result {
		t.Fatal(result, err)
	}
	s.steps(t, "U", forwardSteps[1:])
}

func TestUnsignedDecisionCannotAdvanceCheckpoint(t *testing.T) {
	s := seeded(t)
	c := next(s.ledger, key, Freeze, "T", "")
	e, _, err := Decide(s.ledger, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Apply(s.ledger, *e)
	if !errors.Is(err, ErrChain) || !reflect.DeepEqual(got, s.ledger) {
		t.Fatal("unsigned decision advanced checkpoint", err)
	}
	if _, ok := Lookup(got, key, "T", ""); ok {
		t.Fatal("unsigned disposition")
	}
	if _, err = Replay(header, append(s.events, *e), testKey{}); !errors.Is(err, ErrChain) {
		t.Fatal(err)
	}
}

func TestSignerHasStandalonePerRowProjection(t *testing.T) {
	s := forward(t, 10)
	pred := s.events[len(s.events)-1]
	other := key
	other.Project = "second"
	s.accept(t, next(s.ledger, other, Seed, "elsewhere", ""))
	open := s.accept(t, next(s.ledger, key, Open, "T", ""))
	type publicBytes interface {
		SigningBytes() ([]byte, error)
		SignedPublicBytes() ([]byte, error)
	}
	prior, ok := any(pred).(publicBytes)
	if !ok {
		t.Fatal("accepted event lacks the signed public projection")
	}
	b, err := prior.SignedPublicBytes()
	if err != nil {
		t.Fatal(err)
	}
	wantPrev := fmt.Sprintf("%x", sha256.Sum256(b))
	signing, err := any(open).(publicBytes).SigningBytes()
	prefix := []byte("ataniq.assignment.event.v1\x00")
	if err != nil || !bytes.HasPrefix(signing, prefix) {
		t.Fatal("missing signing domain", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(signing[len(prefix):], &doc); err != nil {
		t.Fatal(err)
	}
	if doc["previous_public_digest"] != wantPrev || doc["commit_revision"] != "12" || doc["event_id"] != open.Result.EventID || doc["admission"] != true || doc["generation"] != "2" || doc["key_id"] != header.KeyID || doc["alg"] != "Ed25519" {
		t.Fatal(doc)
	}
	executing, ok := doc["executing_instance"].(map[string]any)
	if !ok || executing["store"] != managed.Store || executing["incarnation"] != managed.Incarnation {
		t.Fatal(doc)
	}
	if _, exists := doc["signature"]; exists {
		t.Fatal("signature in signing input")
	}
	l, err := Replay(header, s.events, testKey{})
	if err != nil || !reflect.DeepEqual(l, s.ledger) {
		t.Fatal(err)
	}
}

func TestJournalEncodingBoundary(t *testing.T) {
	s := fresh(t)
	c := next(s.ledger, key, Seed, "T", "")
	t.Run("invalid UTF-8", func(t *testing.T) { c := c; c.Actor = "bad\xff"; refused(t, s.ledger, c, ErrInvalid) })
	t.Run("escaped record", func(t *testing.T) {
		c := c
		c.Actor = strings.Repeat("\x00", recordLimit/6)
		refused(t, s.ledger, c, ErrLimit)
	})
	t.Run("signed frame", func(t *testing.T) {
		e, _, err := Decide(s.ledger, c)
		if err != nil {
			t.Fatal(err)
		}
		e.Signature = strings.Repeat("\x01", recordLimit/6)
		if _, err := Apply(s.ledger, *e); !errors.Is(err, ErrLimit) {
			t.Fatal("oversize signed record", err)
		}
	})
	t.Run("exact header", func(t *testing.T) {
		frame, err := header.Bytes()
		if err != nil || sha256Hex(frame) != s.ledger.Checkpoint().Digest {
			t.Fatal("wrong root commitment", err)
		}
	})
}

func TestExactFrames(t *testing.T) {
	s := fresh(t)
	c := next(s.ledger, key, Seed, "T", "")
	c.Actor = "line separator\u2028and literal\\u2028<&"
	e := s.accept(t, c)
	raw, err := e.RecordBytes()
	if err != nil || !bytes.Contains(raw, []byte(c.Actor[:14])) {
		t.Fatal(err)
	}
	decoded, err := DecodeRecord(raw)
	if err != nil || !reflect.DeepEqual(decoded, e) {
		t.Fatal("event round trip", err)
	}
	root, err := header.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := ReplayFromHeader(root, []Event{decoded}, testKey{})
	if err != nil || recovered.Checkpoint() != s.ledger.Checkpoint() {
		t.Fatal(err)
	}
	for _, bad := range [][]byte{root[:len(root)-1], append([]byte(" "), root...), bytes.Replace(root, []byte("{"), []byte("{\"format\":\"other\","), 1)} {
		if _, err := NewLedgerFromHeader(bad); err == nil {
			t.Fatal("accepted different header")
		}
	}
	for _, bad := range [][]byte{raw[:len(raw)-1], append([]byte(" "), raw...), bytes.Replace(raw, []byte("{"), []byte("{\"schema\":1,"), 1)} {
		if _, err := DecodeRecord(bad); err == nil {
			t.Fatal("accepted different record")
		}
	}
	e.Signature += strings.Repeat("s", recordLimit-len(raw))
	full, err := e.RecordBytes()
	if err != nil || len(full) != recordLimit {
		t.Fatal(len(full), err)
	}
	e.Signature += "s"
	if _, err = e.RecordBytes(); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
	e.Signature = "\xff"
	if _, err = e.RecordBytes(); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
func TestDecideReservesSignedFrameSpace(t *testing.T) {
	s := seeded(t)
	c := next(s.ledger, key, ConfirmReceipt, "seed", "worker")
	c.Evidence[0].Digest = sha256Hex([]byte("amended"))
	// Attached observations are journaled with a correction. Fill the record
	// with distinct, bounded references and then tune the last bytes exactly.
	work := c.Evidence[0]
	work.Kind, work.Route, work.Outcome = "effect", "worker", "outstanding"
	for i := 0; ; i++ {
		work.Subject = fmt.Sprint("child-", i)
		c.Evidence = append(c.Evidence, work)
		e, _, err := Decide(s.ledger, c)
		if errors.Is(err, ErrLimit) {
			c.Evidence = c.Evidence[:len(c.Evidence)-1]
			break
		}
		if err != nil || e == nil {
			t.Fatal(err)
		}
	}
	e, _, err := Decide(s.ledger, c)
	if err != nil {
		t.Fatal(err)
	}
	e.Signature = strings.Repeat("A", 262)
	raw, err := e.RecordBytes()
	if err != nil {
		t.Fatal(err)
	}
	padding := recordLimit - len(raw)
	for i := 1; padding > 0; i++ {
		add := min(padding, MaxReferenceBytes-len(c.Evidence[i].Revision))
		c.Evidence[i].Revision += strings.Repeat("x", add)
		padding -= add
	}
	e, _, err = Decide(s.ledger, c)
	if err != nil {
		t.Fatal(err)
	}
	e.Signature = strings.Repeat("A", 262)
	raw, err = e.RecordBytes()
	if err != nil || len(raw) != recordLimit {
		t.Fatal(len(raw), err)
	}
	c.Actor += "x"
	refused(t, s.ledger, c, ErrLimit)
}
