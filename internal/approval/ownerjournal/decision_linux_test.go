package ownerjournal

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/record"
)

const (
	objectA = "8281895a8281895a8281895a8281895a8281895a"
	objectB = "93c8d68b93c8d68b93c8d68b93c8d68b93c8d68b"
	zeroID  = "0000000000000000000000000000000000000000"
	shown   = "6c621d1a05138a7888d37d9269a9da8e2e11e4aced2f6cfd24b05ab1b9e61bb0"
)

// pendingRecord returns the bytes of a pending record (a prune, the
// smallest form) with the given id, project and end, and its binding
// digest.
func pendingRecord(t *testing.T, id, project string, expires time.Time) ([]byte, string) {
	t.Helper()
	plan := "prune the merged branches"
	p := record.Pending{
		Schema:        record.SchemaPending,
		ID:            id,
		Planner:       "planner-a",
		RequestID:     "rq-" + strings.TrimPrefix(id, "ap-"),
		RequestSHA256: record.Digest([]byte(id)),
		Binding: record.Binding{
			Project: project,
			Subject: record.Subject{Kind: record.SubjectSeat, RoleName: "role-a", Epoch: 7, SeatRev: 12,
				Holder: &record.Holder{Kind: "lead", DisplayID: "role-a@e7", Runtime: "runtime-a"}},
			Verb:       record.VerbBranchPrune,
			Argv:       []string{},
			Class:      record.ClassRefMove,
			PlanToken:  "h-3fa9c2e1",
			PlanSHA256: record.PlanDigest(plan),
			Effects: []record.Effect{{Kind: record.EffectRef, Ref: "refs/heads/feature/old-one", Old: objectA, New: zeroID,
				MergedInto: &record.MergedInto{Ref: "refs/heads/develop", Tip: objectB}}},
			Evidence:         []record.Evidence{},
			RequestedAt:      record.FormatTime(expires.Add(-24 * time.Hour)),
			PendingExpiresAt: record.FormatTime(expires),
		},
		PlanText: record.Text(plan),
	}
	digest, err := record.BindingDigest(p.Binding)
	if err != nil {
		t.Fatal(err)
	}
	p.BindingSHA256 = digest
	b, err := record.EncodePending(p)
	if err != nil {
		t.Fatal(err)
	}
	return b, digest
}

// decisionRecord returns the canonical bytes of a decision.
func decisionRecord(t *testing.T, id, project, binding string, approve bool, at time.Time) []byte {
	t.Helper()
	d := record.Decision{
		Schema:        record.SchemaDecision,
		ID:            id,
		Planner:       "planner-a",
		Decision:      record.DecisionDeny,
		BindingSHA256: binding,
		Project:       project,
		Subject:       record.Subject{Kind: record.SubjectSeat, RoleName: "role-a", Epoch: 7, SeatRev: 12},
		PlanToken:     "h-3fa9c2e1",
		SignedOff:     []record.SignOff{},
		DecidedAt:     record.FormatTime(at),
		ExpiresAt:     record.FormatTime(at.Add(30 * time.Minute)),
		Confirmed:     record.ConfirmedCode,
		Console:       record.Console{PID: 41822, TTY: "/dev/pts/4", Client: "console-a 0.0.11", Decider: true, Surface: "console", Session: "0123456789abcdef"},
	}
	if approve {
		d.Decision, d.Uses = record.DecisionApprove, 1
	} else {
		d.Reason = "not today <&>"
	}
	b, err := record.EncodeDecision(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// approveOf returns an approve for a pending record made on the spot, and
// that record.
func approveOf(t *testing.T, id, project string, at time.Time) (decision, pending []byte) {
	t.Helper()
	pending, digest := pendingRecord(t, id, project, at.Add(time.Hour))
	return decisionRecord(t, id, project, digest, true, at), pending
}

func seqsOf(ds []*Decision) []int64 {
	out := []int64{}
	for _, d := range ds {
		out = append(out, d.Seq())
	}
	return out
}

func openSeqs(t *testing.T, s *Store) []int64 {
	t.Helper()
	open, err := s.OpenUses()
	if err != nil {
		t.Fatal(err)
	}
	out := []int64{}
	for _, u := range open {
		out = append(out, u.Decision().Seq())
	}
	return out
}

func undeliveredSeqs(t *testing.T, s *Store) []int64 {
	t.Helper()
	ds, err := s.Undelivered()
	if err != nil {
		t.Fatal(err)
	}
	return seqsOf(ds)
}

func heldSeqs(t *testing.T, s *Store, project string) []int64 {
	t.Helper()
	cards, err := s.HeldCards(project)
	if err != nil {
		t.Fatal(err)
	}
	out := []int64{}
	for _, c := range cards {
		out = append(out, c.Seq)
	}
	return out
}

func wantSeqs(t *testing.T, what string, got []int64, want ...int64) {
	t.Helper()
	if want == nil {
		want = []int64{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: entries %v, want %v", what, got, want)
	}
}

// The whole life of an approval, with a restart after every step: it is
// journaled, listed for delivery until it is marked, open until it has
// its outcome, and then nowhere.
func TestAnApprovalFromDecisionToOutcomeAcrossRestarts(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	restart := func() {
		t.Helper()
		mustClose(t, s)
		s = mustOpen(t, path)
	}
	decision, _ := approveOf(t, "ap-one", "project-a", base)
	last := base.Add(-90 * time.Second).Add(250 * time.Millisecond)
	d, err := s.AppendDecision(decision, Seen{LastActivity: last, ShownSHA256: shown})
	if err != nil {
		t.Fatal(err)
	}
	if d.Seq() != 1 || d.ID() != "ap-one" || d.Project() != "project-a" || !d.Approve() || !d.DecidedAt().Equal(base) || !bytes.Equal(d.Bytes(), decision) {
		t.Fatalf("the handle: %+v", d)
	}
	entries := mustEntries(t, s)
	if len(entries) != 2 {
		t.Fatalf("%d entries, want the decision and its activity", len(entries))
	}
	rec, _ := record.DecodeDecision(decision)
	wantDecision := Entry{Seq: 1, Kind: KindDecision, At: base, PendingID: "ap-one", Project: "project-a",
		Body: []byte(`{"approve":true,"binding_sha256":"` + rec.BindingSHA256 + `"}`), Record: decision}
	wantActivity := Entry{Seq: 2, Kind: KindActivity, At: base, PendingID: "ap-one", Project: "project-a", Ref: 1,
		Body: []byte(`{"last_activity":"2026-11-20T13:58:30.25Z","session":"0123456789abcdef","shown_sha256":"` + shown + `","surface":"console"}`)}
	if !reflect.DeepEqual(entries[0], wantDecision) || !reflect.DeepEqual(entries[1], wantActivity) {
		t.Fatalf("the entries:\n %+v\n %+v\nwant\n %+v\n %+v", entries[0], entries[1], wantDecision, wantActivity)
	}

	// Not delivered: listed at every restart until it is marked.
	for i := 0; i < 2; i++ {
		restart()
		ds, err := s.Undelivered()
		if err != nil {
			t.Fatal(err)
		}
		if len(ds) != 1 || ds[0].Seq() != 1 || !bytes.Equal(ds[0].Bytes(), decision) || !ds[0].Approve() || ds[0].BindingSHA256() != rec.BindingSHA256 {
			t.Fatalf("restart %d: undelivered %v", i, seqsOf(ds))
		}
		d = ds[0]
		wantSeqs(t, "open uses before the delivery", openSeqs(t, s))
	}
	u, err := d.MarkDelivered(base.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !u.DeliveredAt().Equal(base.Add(time.Second)) || u.Decision() != d {
		t.Fatalf("the delivery handle: %+v", u)
	}
	if _, err := d.MarkDelivered(base.Add(2 * time.Second)); !errors.Is(err, ErrDelivered) {
		t.Fatalf("a second delivery: %v, want %v", err, ErrDelivered)
	}
	if err := d.MarkRefusedByDoor(base.Add(2*time.Second), record.CodeApprovalVoid); !errors.Is(err, ErrDelivered) {
		t.Fatalf("a refusal after the delivery: %v, want %v", err, ErrDelivered)
	}

	// Delivered: never listed for delivery again, and open.
	restart()
	wantSeqs(t, "undelivered after the delivery", undeliveredSeqs(t, s))
	open, err := s.OpenUses()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Decision().Seq() != 1 || !open[0].DeliveredAt().Equal(base.Add(time.Second)) || !bytes.Equal(open[0].Decision().Bytes(), decision) {
		t.Fatalf("open uses after the delivery: %v", openSeqs(t, s))
	}
	u = open[0]
	if err := u.AppendUse(base.Add(3*time.Second), record.CodeEvidenceChanged); err != nil {
		t.Fatal(err)
	}
	if err := u.AppendUse(base.Add(4*time.Second), ""); err != nil {
		t.Fatal(err)
	}
	if err := u.AppendUse(base.Add(5*time.Second), ""); !errors.Is(err, ErrConsumed) {
		t.Fatalf("a second consumption: %v, want %v", err, ErrConsumed)
	}

	// Consumed with no outcome: still open after a restart.
	restart()
	wantSeqs(t, "open uses after the use", openSeqs(t, s), 1)
	open, _ = s.OpenUses()
	u = open[0]
	if err := u.AppendOutcome(base.Add(6*time.Second), record.StateApplied, "refs/heads/develop moved"); err != nil {
		t.Fatal(err)
	}
	if err := u.AppendOutcome(base.Add(7*time.Second), record.StateNotApplied, ""); !errors.Is(err, ErrSettled) {
		t.Fatalf("a second outcome: %v, want %v", err, ErrSettled)
	}
	if err := u.AppendUse(base.Add(7*time.Second), record.CodeApprovalConsumed); !errors.Is(err, ErrSettled) {
		t.Fatalf("a use after the outcome: %v, want %v", err, ErrSettled)
	}

	restart()
	wantSeqs(t, "undelivered at the end", undeliveredSeqs(t, s))
	wantSeqs(t, "open uses at the end", openSeqs(t, s))
	entries = mustEntries(t, s)
	var got []string
	for _, e := range entries[2:] {
		got = append(got, fmt.Sprintf("%d %s ref=%d %s %s %s", e.Seq, e.Kind, e.Ref, e.PendingID, e.Project, e.Body))
	}
	want := []string{
		`3 delivered ref=1 ap-one project-a {"code":""}`,
		`4 use ref=1 ap-one project-a {"code":"evidence-changed"}`,
		`5 use ref=1 ap-one project-a {"code":""}`,
		`6 outcome ref=1 ap-one project-a {"line":"refs/heads/develop moved","state":"applied"}`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the entries after the decision:\n %s\nwant\n %s", strings.Join(got, "\n "), strings.Join(want, "\n "))
	}
}

// The decision's bytes are kept exactly as given, whatever their spelling,
// and the handle gives out copies.
func TestTheDecisionBytesAreKeptAsGiven(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	canonical, _ := approveOf(t, "ap-one", "project-a", base)
	spelled := []byte(" {\n  " + strings.Replace(string(canonical[1:]), `"console":`, "\"console\" :\t", 1) + "\n")
	if _, err := record.DecodeDecision(spelled); err != nil || bytes.Equal(spelled, canonical) {
		t.Fatalf("the test's spelling is not another spelling of a valid record: %v", err)
	}
	given := append([]byte(nil), spelled...)
	d, err := s.AppendDecision(given, Seen{})
	if err != nil {
		t.Fatal(err)
	}
	for i := range given {
		given[i] = 'x' // the caller's slice is its own again
	}
	out := d.Bytes()
	out[0] = 'x'
	if !bytes.Equal(d.Bytes(), spelled) {
		t.Fatal("the handle's bytes changed with a slice that was given or handed out")
	}
	mustClose(t, s)
	s = mustOpen(t, path)
	ds, err := s.Undelivered()
	if err != nil || len(ds) != 1 || !bytes.Equal(ds[0].Bytes(), spelled) {
		t.Fatalf("after a reopen the bytes differ: %v", err)
	}
	if got := mustEntries(t, s)[0].Record; !bytes.Equal(got, spelled) {
		t.Fatal("the entry's record differs from what was given")
	}
}

func TestWhatIsNotADecisionIsRefused(t *testing.T) {
	s := mustOpen(t, newPath(t))
	approve, pending := approveOf(t, "ap-one", "project-a", base)
	_, digest := pendingRecord(t, "ap-one", "project-a", base.Add(time.Hour))
	deny := decisionRecord(t, "ap-one", "project-a", digest, false, base)
	otherID, _ := pendingRecord(t, "ap-two", "project-a", base.Add(time.Hour))
	otherProject, _ := pendingRecord(t, "ap-one", "project-b", base.Add(time.Hour))
	otherBinding, _ := pendingRecord(t, "ap-one", "project-a", base.Add(2*time.Hour))
	tooLarge := append(append([]byte(nil), approve...), bytes.Repeat([]byte(" "), record.MaxDecisionBytes)...)

	for name, b := range map[string][]byte{"nothing": nil, "not JSON": []byte("approve"), "a pending record": pending,
		"an unknown field": []byte(strings.Replace(string(approve), `{"`, `{"held":true,"`, 1)), "too large": tooLarge} {
		if _, err := s.AppendDecision(b, Seen{}); err == nil {
			t.Errorf("AppendDecision took %s", name)
		} else if _, ok := record.CodeOf(err); !ok {
			t.Errorf("AppendDecision of %s: %v, want the record's refusal", name, err)
		}
		if _, err := s.AppendHeld(b, pending, Seen{}); err == nil {
			t.Errorf("AppendHeld took %s", name)
		}
	}
	for name, v := range map[string]Seen{"a short digest": {ShownSHA256: "abc"}, "an upper-case digest": {ShownSHA256: strings.ToUpper(shown)},
		"an activity out of range": {LastActivity: time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)}} {
		if _, err := s.AppendDecision(approve, v); err == nil {
			t.Errorf("AppendDecision took %s", name)
		}
		if _, err := s.AppendHeld(approve, pending, v); err == nil {
			t.Errorf("AppendHeld took %s", name)
		}
	}
	if _, err := s.AppendHeld(deny, pending, Seen{}); err == nil || !strings.Contains(err.Error(), "never held") {
		t.Errorf("a held deny: %v, want a refusal", err)
	}
	for name, p := range map[string][]byte{"another id": otherID, "another project": otherProject, "another binding": otherBinding,
		"no pending record": nil, "a decision for a pending record": approve} {
		if _, err := s.AppendHeld(approve, p, Seen{}); err == nil {
			t.Errorf("AppendHeld took a pending record with %s", name)
		}
	}
	if got := mustEntries(t, s); len(got) != 0 {
		t.Fatalf("the refused appends wrote %d entries", len(got))
	}
}

// A held decision is never listed for delivery. It marks its card until
// the card is decided again, and a later entry for the id closes it.
func TestAHeldDecisionMarksItsCardAndIsNeverDelivered(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	first, pending := approveOf(t, "ap-one", "project-a", base)
	_, digest := pendingRecord(t, "ap-one", "project-a", base.Add(time.Hour))
	card, err := s.AppendHeld(first, pending, Seen{LastActivity: base.Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	want := HeldCard{Seq: 1, ID: "ap-one", Project: "project-a", BindingSHA256: digest, DecidedAt: base,
		PendingExpiresAt: base.Add(time.Hour), Decision: first}
	if !reflect.DeepEqual(card, want) {
		t.Fatalf("the held card:\n %+v\nwant\n %+v", card, want)
	}
	other, otherPending := approveOf(t, "ap-other", "project-b", base.Add(time.Second))
	if _, err := s.AppendHeld(other, otherPending, Seen{}); err != nil {
		t.Fatal(err)
	}
	mustClose(t, s)
	s = mustOpen(t, path)
	wantSeqs(t, "undelivered with held entries only", undeliveredSeqs(t, s))
	wantSeqs(t, "open uses with held entries only", openSeqs(t, s))
	cards, err := s.HeldCards("project-a")
	if err != nil || len(cards) != 1 || !reflect.DeepEqual(cards[0], want) {
		t.Fatalf("the held cards of project-a after a reopen: %+v, %v", cards, err)
	}
	wantSeqs(t, "the held cards of project-b", heldSeqs(t, s, "project-b"), 3)
	wantSeqs(t, "the held cards of a project with none", heldSeqs(t, s, "project-c"))

	// Decided again past the limit: another held entry, and only the later
	// one is the card's.
	second := decisionRecord(t, "ap-one", "project-a", digest, true, base.Add(time.Minute))
	if _, err := s.AppendHeld(second, pending, Seen{}); err != nil {
		t.Fatal(err)
	}
	wantSeqs(t, "the held cards after the second held", heldSeqs(t, s, "project-a"), 5)

	// Decided again with room: a decision, which goes to the door, and no
	// held card is left.
	third := decisionRecord(t, "ap-one", "project-a", digest, true, base.Add(2*time.Minute))
	d, err := s.AppendDecision(third, Seen{})
	if err != nil {
		t.Fatal(err)
	}
	wantSeqs(t, "the held cards after the decision", heldSeqs(t, s, "project-a"))
	wantSeqs(t, "undelivered after the decision", undeliveredSeqs(t, s), d.Seq())
	wantSeqs(t, "the held cards of project-b, untouched", heldSeqs(t, s, "project-b"), 3)

	// One decision for an id: nothing more is decided or held for it.
	fourth := decisionRecord(t, "ap-one", "project-a", digest, true, base.Add(3*time.Minute))
	if _, err := s.AppendDecision(fourth, Seen{}); !errors.Is(err, ErrDecided) {
		t.Fatalf("a second decision: %v, want %v", err, ErrDecided)
	}
	if _, err := s.AppendHeld(fourth, pending, Seen{}); !errors.Is(err, ErrDecided) {
		t.Fatalf("a held after the decision: %v, want %v", err, ErrDecided)
	}
	// Nothing closed by ExpireHeld: no card of project-a is open, and
	// project-b's has not run out.
	if closed, err := s.ExpireHeld(base.Add(30 * time.Minute)); err != nil || len(closed) != 0 {
		t.Fatalf("ExpireHeld: %+v, %v", closed, err)
	}
	// The same id in another project is another card.
	sameID, sameIDPending := approveOf(t, "ap-one", "project-c", base)
	if _, err := s.AppendHeld(sameID, sameIDPending, Seen{}); err != nil {
		t.Fatalf("the same id in another project: %v", err)
	}
}

// A deny is never held; it is a decision and goes to the door, where it
// has a delivery and nothing after it.
func TestADenyIsDeliveredAndHasNoUse(t *testing.T) {
	s := mustOpen(t, newPath(t))
	_, digest := pendingRecord(t, "ap-one", "project-a", base.Add(time.Hour))
	deny := decisionRecord(t, "ap-one", "project-a", digest, false, base)
	d, err := s.AppendDecision(deny, Seen{})
	if err != nil {
		t.Fatal(err)
	}
	if d.Approve() {
		t.Fatal("the deny reads as an approve")
	}
	if got := string(mustEntries(t, s)[0].Body); got != `{"approve":false,"binding_sha256":"`+digest+`"}` {
		t.Fatalf("the deny's body: %s", got)
	}
	wantSeqs(t, "undelivered", undeliveredSeqs(t, s), 1)
	u, err := d.MarkDelivered(base.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	wantSeqs(t, "undelivered after the delivery", undeliveredSeqs(t, s))
	wantSeqs(t, "open uses", openSeqs(t, s))
	if err := u.AppendUse(base.Add(2*time.Second), ""); err == nil {
		t.Fatal("a deny took a use")
	}
	if err := u.AppendOutcome(base.Add(2*time.Second), record.StateApplied, ""); err == nil {
		t.Fatal("a deny took an outcome")
	}
	if got := mustEntries(t, s); len(got) != 3 {
		t.Fatalf("%d entries, want 3", len(got))
	}
}

// A decision the door refused is closed: not handed over again, and never
// an open use.
func TestADecisionTheDoorRefusedIsClosed(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	approve, _ := approveOf(t, "ap-one", "project-a", base)
	d, err := s.AppendDecision(approve, Seen{})
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []record.Code{"", "Approval-Void", "approval void", record.Code(strings.Repeat("a", 65))} {
		if err := d.MarkRefusedByDoor(base.Add(time.Second), code); err == nil {
			t.Fatalf("the code %q was taken", code)
		}
	}
	if err := d.MarkRefusedByDoor(time.Time{}, record.CodeApprovalVoid); err == nil {
		t.Fatal("a refusal with no time was taken")
	}
	if err := d.MarkRefusedByDoor(base.Add(time.Second), record.CodeApprovalVoid); err != nil {
		t.Fatal(err)
	}
	if _, err := d.MarkDelivered(base.Add(2 * time.Second)); !errors.Is(err, ErrDelivered) {
		t.Fatalf("a delivery after the refusal: %v, want %v", err, ErrDelivered)
	}
	mustClose(t, s)
	s = mustOpen(t, path)
	wantSeqs(t, "undelivered", undeliveredSeqs(t, s))
	wantSeqs(t, "open uses", openSeqs(t, s))
	last := mustEntries(t, s)[2]
	if last.Kind != KindDelivered || last.Ref != 1 || string(last.Body) != `{"code":"approval-void"}` {
		t.Fatalf("the delivered entry: %+v", last)
	}
	// A handle made before the refusal cannot go on to a use.
	if err := (&Delivery{d: &Decision{s: s, seq: d.seq, rec: d.rec}}).AppendUse(base.Add(3*time.Second), ""); err == nil {
		t.Fatal("a decision the door refused took a use")
	}
}

func TestAnUnknownOutcomeWaitsAndIsSettledOnce(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	deliver := func(id string, at time.Time) *Delivery {
		t.Helper()
		approve, _ := approveOf(t, id, "project-a", at)
		d, err := s.AppendDecision(approve, Seen{})
		if err != nil {
			t.Fatal(err)
		}
		u, err := d.MarkDelivered(at.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	one, two, three := deliver("ap-one", base), deliver("ap-two", base.Add(time.Minute)), deliver("ap-three", base.Add(2*time.Minute))
	for _, state := range []record.State{record.StatePending, record.StateApproved, record.StateApprovedByRule,
		record.StateApprovedByGrant, record.StateDenied, record.StateConsumed, "", "done"} {
		if err := one.AppendOutcome(base.Add(time.Hour), state, ""); err == nil {
			t.Fatalf("the outcome %q was taken", state)
		}
	}
	for _, line := range []string{"line\nbreak", "\x1b[2J", "bad\xff", strings.Repeat("l", 501)} {
		if err := one.AppendOutcome(base.Add(time.Hour), record.StateApplied, line); err == nil {
			t.Fatalf("the line %q was taken", line)
		}
	}
	// An outcome that follows a consumption needs the consumption first;
	// a use the door refused is not one.
	consumedStates := []record.State{record.StateApplied, record.StateNotApplied, record.StateUnknown}
	for _, state := range consumedStates {
		if err := one.AppendOutcome(base.Add(time.Hour), state, ""); !errors.Is(err, ErrNotConsumed) {
			t.Fatalf("%s before any use: %v, want %v", state, err, ErrNotConsumed)
		}
	}
	if err := one.AppendUse(base.Add(time.Hour), record.CodeEvidenceChanged); err != nil {
		t.Fatal(err)
	}
	for _, state := range consumedStates {
		if err := one.AppendOutcome(base.Add(time.Hour), state, ""); !errors.Is(err, ErrNotConsumed) {
			t.Fatalf("%s after a refused use: %v, want %v", state, err, ErrNotConsumed)
		}
	}
	if err := one.AppendUse(base.Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	if err := one.AppendOutcome(base.Add(time.Hour), record.StateUnknown, "the ref is at neither value"); err != nil {
		t.Fatal(err)
	}
	if err := two.AppendOutcome(base.Add(time.Hour), record.StateExpired, ""); err != nil {
		t.Fatal(err)
	}
	unknownSeqs := func() []int64 {
		t.Helper()
		us, err := s.UnknownOutcomes()
		if err != nil {
			t.Fatal(err)
		}
		out := []int64{}
		for _, u := range us {
			out = append(out, u.Decision().Seq())
		}
		return out
	}
	mustClose(t, s)
	s = mustOpen(t, path)
	wantSeqs(t, "open uses", openSeqs(t, s), three.Decision().Seq())
	wantSeqs(t, "unknown outcomes", unknownSeqs(), one.Decision().Seq())
	us, _ := s.UnknownOutcomes()
	one = us[0]
	for _, state := range []record.State{record.StateUnknown, record.StateVoid, record.StateExpired, record.StateWithdrawn} {
		if err := one.AppendOutcome(base.Add(2*time.Hour), state, ""); !errors.Is(err, ErrSettled) {
			t.Fatalf("%s after unknown: %v, want %v", state, err, ErrSettled)
		}
	}
	if err := one.AppendOutcome(base.Add(2*time.Hour), record.StateNotApplied, "the owner looked"); err != nil {
		t.Fatal(err)
	}
	if err := one.AppendOutcome(base.Add(3*time.Hour), record.StateApplied, ""); !errors.Is(err, ErrSettled) {
		t.Fatalf("a third outcome: %v, want %v", err, ErrSettled)
	}
	wantSeqs(t, "unknown outcomes after he looked", unknownSeqs())
	wantSeqs(t, "open uses after he looked", openSeqs(t, s), three.Decision().Seq())
}

// An approval that ends without a use (expired, void or withdrawn) is
// closed with no use journaled, and is no longer listed as open.
func TestAnApprovalThatEndsUnusedIsClosedWithoutAUse(t *testing.T) {
	s := mustOpen(t, newPath(t))
	var seqs []int64
	for i, state := range []record.State{record.StateExpired, record.StateVoid, record.StateWithdrawn} {
		at := base.Add(time.Duration(i) * time.Minute)
		approve, _ := approveOf(t, []string{"ap-one", "ap-two", "ap-three"}[i], "project-a", at)
		d, err := s.AppendDecision(approve, Seen{})
		if err != nil {
			t.Fatal(err)
		}
		u, err := d.MarkDelivered(at.Add(time.Second))
		if err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, d.Seq())
		wantSeqs(t, "open uses before "+string(state), openSeqs(t, s), seqs...)
		if err := u.AppendOutcome(at.Add(time.Hour), state, ""); err != nil {
			t.Fatalf("%s with no use: %v", state, err)
		}
		seqs = seqs[:len(seqs)-1]
		wantSeqs(t, "open uses after "+string(state), openSeqs(t, s), seqs...)
		// The approval is settled, and that is the reason a later outcome
		// is refused, not the missing use.
		if err := u.AppendOutcome(at.Add(2*time.Hour), record.StateApplied, ""); !errors.Is(err, ErrSettled) {
			t.Fatalf("applied after %s: %v, want %v", state, err, ErrSettled)
		}
	}
}

func TestARefusedDecisionIsJournaledAndNeverListed(t *testing.T) {
	s := mustOpen(t, newPath(t))
	good := Refused{At: base, PendingID: "ap-one", Project: "project-a", Code: record.CodeOwnerAway,
		Surface: "console", Session: "0123456789abcdef", LastActivity: base.Add(-11 * time.Minute)}
	bad := map[string]func(r *Refused){
		"no time":              func(r *Refused) { r.At = time.Time{} },
		"a door code":          func(r *Refused) { r.Code = record.CodeApprovalVoid },
		"no code":              func(r *Refused) { r.Code = "" },
		"limit-refused":        func(r *Refused) { r.Code = record.CodeLimitRefused },
		"no surface":           func(r *Refused) { r.Surface = "" },
		"a hostile surface":    func(r *Refused) { r.Surface = "con\x1b[2Jsole" },
		"a long surface":       func(r *Refused) { r.Surface = strings.Repeat("s", 33) },
		"a short session":      func(r *Refused) { r.Session = "0123" },
		"an upper session":     func(r *Refused) { r.Session = "0123456789ABCDEF" },
		"a malformed id":       func(r *Refused) { r.PendingID = "ap-<script>" },
		"a hostile project":    func(r *Refused) { r.Project = "project\n-a" },
		"an activity too late": func(r *Refused) { r.LastActivity = time.Date(2300, 1, 1, 0, 0, 0, 0, time.UTC) },
	}
	for name, spoil := range bad {
		r := good
		spoil(&r)
		if err := s.AppendRefused(r); err == nil {
			t.Errorf("AppendRefused took %s", name)
		}
	}
	if got := mustEntries(t, s); len(got) != 0 {
		t.Fatalf("the refused appends wrote %d entries", len(got))
	}
	if err := s.AppendRefused(good); err != nil {
		t.Fatal(err)
	}
	// A frame that named no usable id and no known project is kept too.
	nameless := Refused{At: base.Add(time.Second), Code: record.CodeDecisionInvalid, Surface: "console", Session: "fedcba9876543210"}
	if err := s.AppendRefused(nameless); err != nil {
		t.Fatal(err)
	}
	for _, code := range []record.Code{record.CodeNotDecider, record.CodeBindingMismatch} {
		r := good
		r.Code = code
		if err := s.AppendRefused(r); err != nil {
			t.Fatalf("%s: %v", code, err)
		}
	}
	entries := mustEntries(t, s)
	if len(entries) != 8 {
		t.Fatalf("%d entries, want 8", len(entries))
	}
	if e := entries[0]; e.Kind != KindRefused || e.PendingID != "ap-one" || e.Project != "project-a" || !e.At.Equal(base) ||
		string(e.Body) != `{"code":"owner-away","session":"0123456789abcdef","surface":"console"}` || e.Record != nil {
		t.Fatalf("the refused entry: %+v", e)
	}
	if e := entries[2]; e.Kind != KindRefused || e.PendingID != "" || e.Project != "" {
		t.Fatalf("the nameless refused entry: %+v", e)
	}
	a, err := s.ActivityOf(1)
	want := Activity{Ref: 1, At: base, Surface: "console", Session: "0123456789abcdef", LastActivity: base.Add(-11 * time.Minute)}
	if err != nil || !reflect.DeepEqual(a, want) {
		t.Fatalf("the activity of the refused entry: %+v, %v; want %+v", a, err, want)
	}
	wantSeqs(t, "undelivered", undeliveredSeqs(t, s))
	wantSeqs(t, "open uses", openSeqs(t, s))
	wantSeqs(t, "held cards", heldSeqs(t, s, "project-a"))
	// A refusal does not stand in the way of a decision for the same id.
	approve, _ := approveOf(t, "ap-one", "project-a", base.Add(time.Minute))
	if _, err := s.AppendDecision(approve, Seen{}); err != nil {
		t.Fatalf("a decision after a refusal: %v", err)
	}
}

func TestTheActivityBeforeADecision(t *testing.T) {
	s := mustOpen(t, newPath(t))
	approve, pending := approveOf(t, "ap-one", "project-a", base)
	local := time.FixedZone("local", -9*3600)
	last := base.Add(-42 * time.Second).In(local)
	card, err := s.AppendHeld(approve, pending, Seen{LastActivity: last, ShownSHA256: shown})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.ActivityOf(card.Seq)
	want := Activity{Ref: 1, At: base, Surface: "console", Session: "0123456789abcdef", LastActivity: last.UTC(), ShownSHA256: shown}
	if err != nil || !reflect.DeepEqual(a, want) {
		t.Fatalf("the activity of the held entry: %+v, %v; want %+v", a, err, want)
	}
	// With no activity before it and no digest, the entry says so.
	second := decisionRecord(t, "ap-one", "project-a", card.BindingSHA256, true, base.Add(time.Minute))
	d, err := s.AppendDecision(second, Seen{})
	if err != nil {
		t.Fatal(err)
	}
	a, err = s.ActivityOf(d.Seq())
	want = Activity{Ref: d.Seq(), At: base.Add(time.Minute), Surface: "console", Session: "0123456789abcdef"}
	if err != nil || !reflect.DeepEqual(a, want) {
		t.Fatalf("the activity of the decision: %+v, %v; want %+v", a, err, want)
	}
	// An activity entry has none of its own, and neither has an entry
	// that is not there.
	for _, seq := range []int64{2, 99, 0} {
		if _, err := s.ActivityOf(seq); err == nil {
			t.Fatalf("ActivityOf(%d) answered", seq)
		}
	}
}

func TestExpireHeldClosesWhatRanOut(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	hold := func(id, project string, at, expires time.Time) HeldCard {
		t.Helper()
		pending, digest := pendingRecord(t, id, project, expires)
		card, err := s.AppendHeld(decisionRecord(t, id, project, digest, true, at), pending, Seen{})
		if err != nil {
			t.Fatal(err)
		}
		return card
	}
	soon, later := base.Add(time.Hour), base.Add(2*time.Hour)
	hold("ap-soon", "project-a", base, soon)                                  // 1, 2
	again := hold("ap-soon", "project-a", base.Add(time.Minute), soon)        // 3, 4: closes 1
	otherProject := hold("ap-soon", "project-b", base.Add(time.Minute), soon) // 5, 6
	stays := hold("ap-later", "project-a", base.Add(2*time.Minute), later)    // 7, 8
	decided := hold("ap-decided", "project-a", base.Add(3*time.Minute), soon) // 9, 10
	if _, err := s.AppendDecision(decisionRecord(t, "ap-decided", "project-a", decided.BindingSHA256, true, base.Add(4*time.Minute)), Seen{}); err != nil {
		t.Fatal(err) // 11, 12
	}
	if _, err := s.ExpireHeld(time.Time{}); err == nil {
		t.Fatal("ExpireHeld took no time")
	}
	// Not yet, and not at the very second the record ends.
	for _, now := range []time.Time{base.Add(30 * time.Minute), soon} {
		closed, err := s.ExpireHeld(now)
		if err != nil || len(closed) != 0 {
			t.Fatalf("ExpireHeld at %v: %+v, %v", now, closed, err)
		}
	}
	if got := mustEntries(t, s); len(got) != 12 {
		t.Fatalf("%d entries before anything ran out, want 12", len(got))
	}
	now := soon.Add(time.Second)
	closed, err := s.ExpireHeld(now)
	if err != nil || !reflect.DeepEqual(closed, []HeldCard{again, otherProject}) {
		t.Fatalf("ExpireHeld:\n %+v, %v\nwant\n %+v", closed, err, []HeldCard{again, otherProject})
	}
	entries := mustEntries(t, s)
	if len(entries) != 14 {
		t.Fatalf("%d entries, want 14", len(entries))
	}
	for i, c := range []HeldCard{again, otherProject} {
		e := entries[12+i]
		if e.Kind != KindExpired || e.Ref != c.Seq || e.PendingID != c.ID || e.Project != c.Project || !e.At.Equal(now) ||
			string(e.Body) != `{"pending_expires_at":"2026-11-20T15:00:00Z"}` {
			t.Fatalf("the expired entry: %+v", e)
		}
	}
	// Once: a second call, and a call after a reopen, close nothing more.
	if closed, err := s.ExpireHeld(now.Add(time.Second)); err != nil || len(closed) != 0 {
		t.Fatalf("a second ExpireHeld: %+v, %v", closed, err)
	}
	mustClose(t, s)
	s = mustOpen(t, path)
	wantSeqs(t, "held cards of project-a", heldSeqs(t, s, "project-a"), stays.Seq)
	wantSeqs(t, "held cards of project-b", heldSeqs(t, s, "project-b"))
	// Nothing more is decided for an id that ran out.
	if _, err := s.AppendDecision(decisionRecord(t, "ap-soon", "project-a", again.BindingSHA256, true, now), Seen{}); !errors.Is(err, ErrExpired) {
		t.Fatalf("a decision for an expired id: %v, want %v", err, ErrExpired)
	}
	pending, _ := pendingRecord(t, "ap-soon", "project-a", soon)
	if _, err := s.AppendHeld(decisionRecord(t, "ap-soon", "project-a", again.BindingSHA256, true, now), pending, Seen{}); !errors.Is(err, ErrExpired) {
		t.Fatalf("a held for an expired id: %v, want %v", err, ErrExpired)
	}
	closed, err = s.ExpireHeld(later.Add(time.Hour))
	if err != nil || !reflect.DeepEqual(closed, []HeldCard{stays}) {
		t.Fatalf("the last ExpireHeld: %+v, %v", closed, err)
	}
	wantSeqs(t, "held cards at the end", heldSeqs(t, s, "project-a"))
}

// The door's decided records are compared with the journal: one the
// journal lacks, one it only held, and one with other bytes are reported.
func TestUnjournaledReportsWhatTheDoorHoldsAndTheJournalLacks(t *testing.T) {
	s := mustOpen(t, newPath(t))
	journaled, _ := approveOf(t, "ap-journaled", "project-a", base)
	if _, err := s.AppendDecision(journaled, Seen{}); err != nil {
		t.Fatal(err)
	}
	_, digest := pendingRecord(t, "ap-denied", "project-a", base.Add(time.Hour))
	denied := decisionRecord(t, "ap-denied", "project-a", digest, false, base)
	if _, err := s.AppendDecision(denied, Seen{}); err != nil {
		t.Fatal(err)
	}
	held, pending := approveOf(t, "ap-held", "project-a", base)
	if _, err := s.AppendHeld(held, pending, Seen{}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendRefused(Refused{At: base, PendingID: "ap-refused", Project: "project-a", Code: record.CodeNotDecider,
		Surface: "console", Session: "0123456789abcdef"}); err != nil {
		t.Fatal(err)
	}
	forged, _ := approveOf(t, "ap-journaled", "project-a", base.Add(time.Second))
	door := []DoorDecision{
		{Project: "project-a", ID: "ap-journaled"},
		{Project: "project-a", ID: "ap-journaled", SHA256: record.Digest(journaled)},
		{Project: "project-a", ID: "ap-denied", SHA256: record.Digest(denied)},
		{Project: "project-a", ID: "ap-held"},                                     // lacking: held only
		{Project: "project-a", ID: "ap-held", SHA256: record.Digest(held)},        // lacking: held only
		{Project: "project-a", ID: "ap-refused"},                                  // lacking: refused only
		{Project: "project-a", ID: "ap-never"},                                    // lacking
		{Project: "project-b", ID: "ap-journaled"},                                // lacking: another project
		{Project: "project-a", ID: "ap-journaled", SHA256: record.Digest(forged)}, // lacking: other bytes
		{Project: "project-a", ID: "not an id"},                                   // lacking
		{},                                                                        // lacking
	}
	got, err := s.Unjournaled(door)
	if err != nil {
		t.Fatal(err)
	}
	if want := door[3:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("Unjournaled:\n %+v\nwant\n %+v", got, want)
	}
	if got, err := s.Unjournaled(nil); err != nil || len(got) != 0 {
		t.Fatalf("Unjournaled of nothing: %+v, %v", got, err)
	}
	if got, err := s.Unjournaled(door[:3]); err != nil || len(got) != 0 {
		t.Fatalf("Unjournaled of what the journal has: %+v, %v", got, err)
	}
}

// A decision and its activity entry are written together. A copy of the
// files taken at any step of the write holds both or neither, and a held
// entry likewise.
func TestACopyTakenWhileADecisionIsWrittenHoldsBothEntriesOrNeither(t *testing.T) {
	for _, kind := range []Kind{KindDecision, KindHeld} {
		t.Run(string(kind), func(t *testing.T) {
			path := newPath(t)
			type shot struct{ point, path string }
			var shots []shot
			armed := false
			s, err := openWithFault(path, func(p string) error {
				if armed {
					shots = append(shots, shot{p, copyStore(t, path)})
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			earlier, _ := approveOf(t, "ap-earlier", "project-a", base)
			if _, err := s.AppendDecision(earlier, Seen{}); err != nil {
				t.Fatal(err)
			}
			before := mustEntries(t, s)
			approve, pending := approveOf(t, "ap-one", "project-a", base.Add(time.Minute))
			armed = true
			if kind == KindDecision {
				_, err = s.AppendDecision(approve, Seen{LastActivity: base})
			} else {
				_, err = s.AppendHeld(approve, pending, Seen{LastActivity: base})
			}
			armed = false
			if err != nil {
				t.Fatal(err)
			}
			after := mustEntries(t, s)
			if len(after) != len(before)+2 {
				t.Fatalf("the write added %d entries, want 2", len(after)-len(before))
			}
			var points []string
			for _, sh := range shots {
				points = append(points, sh.point)
			}
			wantPoints := []string{"before:begin", "after:begin", "before:insert", "after:insert", "before:insert", "after:insert", "before:commit", "commit", "after:commit"}
			if !reflect.DeepEqual(points, wantPoints) {
				t.Fatalf("the steps of the write: %v, want %v", points, wantPoints)
			}
			for i, sh := range shots {
				want := before
				if sh.point == "commit" || sh.point == "after:commit" {
					want = after
				}
				c, err := Open(sh.path)
				if err != nil {
					t.Fatalf("the copy at step %d (%s) does not open: %v", i, sh.point, err)
				}
				if got := mustEntries(t, c); !reflect.DeepEqual(got, want) {
					t.Fatalf("the copy at step %d (%s) holds %d entries, want %d", i, sh.point, len(got), len(want))
				}
				// What the copy says is under way matches what it holds.
				wantUndelivered := []int64{1}
				if (sh.point == "commit" || sh.point == "after:commit") && kind == KindDecision {
					wantUndelivered = []int64{1, 3}
				}
				wantSeqs(t, "undelivered in the copy at "+sh.point, undeliveredSeqs(t, c), wantUndelivered...)
				mustClose(t, c)
			}
		})
	}
}

// A fault between the two entries of a decision leaves neither.
func TestAFaultBetweenADecisionAndItsActivityLeavesNeither(t *testing.T) {
	boom := errors.New("fault")
	inserts := 0
	path := newPath(t)
	s, err := openWithFault(path, func(p string) error {
		if p == "before:insert" {
			if inserts++; inserts == 2 {
				return boom
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	approve, _ := approveOf(t, "ap-one", "project-a", base)
	if _, err := s.AppendDecision(approve, Seen{}); !errors.Is(err, boom) {
		t.Fatalf("AppendDecision: %v, want the fault", err)
	}
	if got := mustEntries(t, s); len(got) != 0 {
		t.Fatalf("%d entries after the fault, want none", len(got))
	}
	wantSeqs(t, "undelivered after the fault", undeliveredSeqs(t, s))
	// The same decision can be journaled afterwards.
	d, err := s.AppendDecision(approve, Seen{})
	if err != nil || d.Seq() != 1 {
		t.Fatalf("the decision after the fault: %v", err)
	}
	mustClose(t, s)
	s2 := mustOpen(t, path)
	wantSeqs(t, "undelivered after a reopen", undeliveredSeqs(t, s2), 1)
}

// Several goroutines decide the same card at once: one decision is
// journaled and the others are refused. Several deliver the same decision
// at once: one delivery is journaled. Meanwhile others decide cards of
// their own, and nothing is lost.
func TestConcurrentDecisionsForOneCardGiveOneDecision(t *testing.T) {
	const racers, others = 8, 6
	path := newPath(t)
	s := mustOpen(t, path)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var won []*Decision
	fail := make(chan string, 4*racers+others)
	for i := 0; i < racers; i++ {
		// Each racer has its own bytes for the same card.
		approve, _ := approveOf(t, "ap-one", "project-a", base.Add(time.Duration(i)*time.Second))
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := s.AppendDecision(approve, Seen{})
			switch {
			case err == nil:
				mu.Lock()
				won = append(won, d)
				mu.Unlock()
			case !errors.Is(err, ErrDecided):
				fail <- err.Error()
			}
		}()
	}
	for i := 0; i < others; i++ {
		approve, _ := approveOf(t, fmt.Sprintf("ap-other-%d", i), "project-a", base)
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := s.AppendDecision(approve, Seen{})
			if err != nil {
				fail <- err.Error()
				return
			}
			if _, err := d.MarkDelivered(base.Add(time.Minute)); err != nil {
				fail <- err.Error()
			}
		}()
	}
	wg.Wait()
	if len(won) != 1 {
		t.Fatalf("%d decisions for one card, want 1", len(won))
	}
	delivered := 0
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := won[0].MarkDelivered(base.Add(time.Minute))
			switch {
			case err == nil:
				mu.Lock()
				delivered++
				mu.Unlock()
			case !errors.Is(err, ErrDelivered):
				fail <- err.Error()
			}
		}()
	}
	wg.Wait()
	close(fail)
	for msg := range fail {
		t.Fatal(msg)
	}
	if delivered != 1 {
		t.Fatalf("%d deliveries of one decision, want 1", delivered)
	}
	mustClose(t, s)
	s = mustOpen(t, path)
	counts := map[Kind]int{}
	for i, e := range mustEntries(t, s) {
		if e.Seq != int64(i+1) {
			t.Fatalf("sequence number %d at place %d", e.Seq, i)
		}
		counts[e.Kind]++
	}
	want := map[Kind]int{KindDecision: 1 + others, KindActivity: 1 + others, KindDelivered: 1 + others}
	if !reflect.DeepEqual(counts, want) {
		t.Fatalf("the entries by kind: %v, want %v", counts, want)
	}
	wantSeqs(t, "undelivered", undeliveredSeqs(t, s))
	if got := openSeqs(t, s); len(got) != 1+others {
		t.Fatalf("%d open uses, want %d", len(got), 1+others)
	}
}

// The digest of an entry covers the record's bytes: a decision whose
// record was changed in the file is found when the store is opened.
func TestAChangedRecordIsFound(t *testing.T) {
	var drop, restore string
	for _, object := range schema {
		if object.name == "entries_no_update" {
			drop, restore = "DROP TRIGGER entries_no_update", object.ddl
		}
	}
	for name, change := range map[string]string{
		"other bytes":                   `UPDATE entries SET record = CAST(replace(CAST(record AS TEXT), '"approve"', '"deny"') AS BLOB) WHERE seq = 1`,
		"no record":                     "UPDATE entries SET record = NULL WHERE seq = 1",
		"a record where there was none": "UPDATE entries SET record = CAST('{}' AS BLOB) WHERE seq = 2",
	} {
		t.Run(name, func(t *testing.T) {
			path := newPath(t)
			s := mustOpen(t, path)
			approve, _ := approveOf(t, "ap-one", "project-a", base)
			if _, err := s.AppendDecision(approve, Seen{}); err != nil {
				t.Fatal(err)
			}
			mustClose(t, s)
			rawExec(t, path, drop, change, restore)
			got, err := Open(path)
			if !errors.Is(err, ErrDamaged) {
				if err == nil {
					got.Close()
				}
				t.Fatalf("Open: %v, want %v", err, ErrDamaged)
			}
		})
	}
}

// A decision the door refused while the record stayed pending leaves the
// card undecided: a new decision for the id is accepted, the refused one is
// no card use, and a decision that stands still blocks a second.
func TestACardTheDoorRefusedCanBeDecidedAgain(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	first, pending := approveOf(t, "ap-one", "project-a", base)
	_, digest := pendingRecord(t, "ap-one", "project-a", base.Add(time.Hour))
	d1, err := s.AppendDecision(first, Seen{})
	if err != nil {
		t.Fatal(err)
	}
	// Until the door has answered, the decision stands.
	again := decisionRecord(t, "ap-one", "project-a", digest, true, base.Add(time.Minute))
	if _, err := s.AppendDecision(again, Seen{}); !errors.Is(err, ErrDecided) {
		t.Fatalf("a decision beside an undelivered one: %v, want %v", err, ErrDecided)
	}
	if err := d1.MarkRefusedByDoor(base.Add(2*time.Minute), record.CodeApprovalExpired); err != nil {
		t.Fatal(err)
	}
	wantSeqs(t, "undelivered after the refusal", undeliveredSeqs(t, s))
	wantSeqs(t, "open uses after the refusal", openSeqs(t, s))
	if uses, err := s.CardUses("project-a", base); err != nil || len(uses) != 0 {
		t.Fatalf("a decision the door refused is a card use: %v, %v", uses, err)
	}

	// The card is undecided again: a deny and an approve are both taken,
	// and so is a held entry.
	deny := decisionRecord(t, "ap-one", "project-a", digest, false, base.Add(3*time.Minute))
	d2, err := s.AppendDecision(deny, Seen{})
	if err != nil {
		t.Fatalf("a decision after the door refused the first: %v", err)
	}
	wantSeqs(t, "undelivered after the second decision", undeliveredSeqs(t, s), d2.Seq())
	if err := d2.MarkRefusedByDoor(base.Add(4*time.Minute), record.CodeApprovalExpired); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AppendHeld(decisionRecord(t, "ap-one", "project-a", digest, true, base.Add(5*time.Minute)), pending, Seen{}); err != nil {
		t.Fatalf("a held entry after two refusals: %v", err)
	}
	third := decisionRecord(t, "ap-one", "project-a", digest, true, base.Add(6*time.Minute))
	d3, err := s.AppendDecision(third, Seen{})
	if err != nil {
		t.Fatalf("a decision after a held entry that followed refusals: %v", err)
	}
	if _, err := d3.MarkDelivered(base.Add(7 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	// The one the door took stands.
	if _, err := s.AppendDecision(decisionRecord(t, "ap-one", "project-a", digest, false, base.Add(8*time.Minute)), Seen{}); !errors.Is(err, ErrDecided) {
		t.Fatalf("a decision after one the door took: %v, want %v", err, ErrDecided)
	}
	if _, err := s.AppendHeld(decisionRecord(t, "ap-one", "project-a", digest, true, base.Add(8*time.Minute)), pending, Seen{}); !errors.Is(err, ErrDecided) {
		t.Fatalf("a held entry after one the door took: %v, want %v", err, ErrDecided)
	}
	// Only the one that stands is a card use.
	if uses, err := s.CardUses("project-a", base); err != nil || len(uses) != 1 || !uses[0].Equal(base.Add(6*time.Minute)) {
		t.Fatalf("the card uses: %v, %v", uses, err)
	}
	days, err := s.Days("project-a", base)
	if err != nil || days[len(days)-1].Uses != 1 {
		t.Fatalf("the days: %+v, %v", days, err)
	}

	// The door holds the decision that stands; the refused ones are the
	// journal's too, so none of them is lacking.
	lacking, err := s.Unjournaled([]DoorDecision{
		{Project: "project-a", ID: "ap-one", SHA256: record.Digest(third)},
		{Project: "project-a", ID: "ap-one", SHA256: record.Digest(first)},
		{Project: "project-a", ID: "ap-one", SHA256: record.Digest(again)},
	})
	if err != nil || len(lacking) != 1 || lacking[0].SHA256 != record.Digest(again) {
		t.Fatalf("Unjournaled: %+v, %v", lacking, err)
	}

	// The same after a restart, which reads and checks the whole file.
	mustClose(t, s)
	s = mustOpen(t, path)
	wantSeqs(t, "open uses after the restart", openSeqs(t, s), d3.Seq())
	if _, err := s.AppendDecision(decisionRecord(t, "ap-one", "project-a", digest, false, base.Add(9*time.Minute)), Seen{}); !errors.Is(err, ErrDecided) {
		t.Fatalf("a decision after the restart: %v, want %v", err, ErrDecided)
	}
}

// The file's own rule holds the same: a second decision entry for an id is
// refused by the file unless the door refused the first.
func TestTheFileRefusesASecondDecisionUnlessTheDoorRefusedTheFirst(t *testing.T) {
	path := newPath(t)
	s := mustOpen(t, path)
	first, _ := approveOf(t, "ap-one", "project-a", base)
	if _, err := s.AppendDecision(first, Seen{}); err != nil {
		t.Fatal(err)
	}
	other, _ := approveOf(t, "ap-two", "project-a", base)
	if _, err := s.AppendDecision(other, Seen{}); err != nil {
		t.Fatal(err)
	}
	mustClose(t, s)
	insert := func(id string, seq int) string {
		return fmt.Sprintf("INSERT INTO entries (seq, kind, at, pending_id, project, ref, body, record, sum) VALUES (%d, 'decision', 1, '", seq) + id +
			"', 'project-a', 0, CAST('{}' AS BLOB), NULL, zeroblob(32))"
	}
	db := rawDB(t, path)
	for _, id := range []string{"ap-one", "ap-two"} {
		if _, err := db.Exec(insert(id, 99)); err == nil || !strings.Contains(err.Error(), "the pending id has a decision") {
			t.Fatalf("a second decision for %s: %v, want the file's refusal", id, err)
		}
	}
	// Once the door has refused the first decision of ap-one, the file lets
	// a second through.
	s = mustOpen(t, path)
	undelivered, err := s.Undelivered()
	if err != nil || len(undelivered) != 2 {
		t.Fatalf("%d undelivered, %v", len(undelivered), err)
	}
	if err := undelivered[0].MarkRefusedByDoor(base.Add(time.Second), record.CodeApprovalVoid); err != nil {
		t.Fatal(err)
	}
	mustClose(t, s)
	db = rawDB(t, path)
	if _, err := db.Exec(insert("ap-one", 99)); err != nil {
		t.Fatalf("a decision after the door's refusal: %v", err)
	}
	if _, err := db.Exec(insert("ap-two", 100)); err == nil {
		t.Fatal("a second decision for an id the door has not refused")
	}
}
