package doortest

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/record"
)

// A decision delivered again answers what it answered the first time,
// whatever has happened to the record since.
func TestDecideIsIdempotent(t *testing.T) {
	f := newFixture(t)
	p := f.pending("sample-feature")
	dec := f.decision(p, true, nil)
	first, err := f.d.Decide(f.ctx, dec)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func(){
		func() {},
		func() { f.use(p, nil) },
		func() { f.settle(p, door.OutcomeApplied) },
		func() { f.clock.Advance(48 * time.Hour) },
	} {
		step()
		again, err := f.d.Decide(f.ctx, dec)
		if err != nil || again != first {
			t.Fatalf("delivered again: %+v, %v; first %+v", again, err, first)
		}
	}
	if got := f.state(p.ID); got != record.StateApplied {
		t.Errorf("state = %s; a second delivery moved the record", got)
	}

	// Another decision for the same id and binding digest is refused, and
	// the record is not decided twice.
	deny := f.decision(p, false, nil)
	_, err = f.d.Decide(f.ctx, deny)
	wantCode(t, err, record.CodeDecisionInvalid)
	// A decision with another binding digest is still refused for that.
	_, err = f.decide(p, true, func(d *record.Decision) { d.BindingSHA256 = record.Digest([]byte("other binding")) })
	wantCode(t, err, record.CodeBindingMismatch)
	shown, _ := f.d.Show(f.ctx, p.ID)
	if !bytes.Equal(shown.Decision.Bytes, dec.Bytes) {
		t.Error("the kept decision is not the first one")
	}
}

// A deny delivered again answers denied, and an approve after it is
// refused.
func TestDecideIsIdempotentForADeny(t *testing.T) {
	f := newFixture(t)
	p := f.pending("sample-feature")
	deny := f.decision(p, false, func(d *record.Decision) { d.Reason = "not today" })
	first, err := f.d.Decide(f.ctx, deny)
	if err != nil || first.State != record.StateDenied {
		t.Fatalf("deny: %+v, %v", first, err)
	}
	again, err := f.d.Decide(f.ctx, deny)
	if err != nil || again != first {
		t.Errorf("again: %+v, %v", again, err)
	}
	_, err = f.d.Decide(f.ctx, f.decision(p, true, nil))
	wantCode(t, err, record.CodeDecisionInvalid)
	shown, _ := f.d.Show(f.ctx, p.ID)
	if !bytes.Equal(shown.Decision.Bytes, deny.Bytes) {
		t.Error("the kept decision is not the first one")
	}
	if got := f.state(p.ID); got != record.StateDenied {
		t.Errorf("state = %s", got)
	}
}

// The 30-minute limit is for an approval only: a deny that lasts an hour is
// taken, and an approve of the same length is refused.
func TestADenyIsNotHeldToTheApprovalLife(t *testing.T) {
	f := newFixture(t)
	long := func(d *record.Decision) {
		d.ExpiresAt = record.FormatTime(f.clock.Now().Add(time.Hour))
	}
	got, err := f.d.Decide(f.ctx, f.decision(f.pending("sample-feature"), false, long))
	if err != nil || got.State != record.StateDenied {
		t.Errorf("a deny of an hour: %+v, %v", got, err)
	}
	_, err = f.decide(f.pending("other-feature"), true, long)
	wantCode(t, err, record.CodeDecisionInvalid)
}

// A settle told again answers the same, and an unknown outcome can be
// settled once more, which is the owner disposing of it.
func TestSettleIsIdempotent(t *testing.T) {
	for _, o := range []door.Outcome{door.OutcomeApplied, door.OutcomeNotApplied, door.OutcomeUnknown} {
		f := newFixture(t)
		p := f.consumed("sample-feature")
		first, err := f.settle(p, o)
		if err != nil || first.State != o.State() {
			t.Fatalf("%s: %+v, %v", o, first, err)
		}
		f.clock.Advance(72 * time.Hour)
		again, err := f.settle(p, o)
		if err != nil || again != first {
			t.Errorf("%s again: %+v, %v", o, again, err)
		}
	}

	for _, o := range []door.Outcome{door.OutcomeApplied, door.OutcomeNotApplied} {
		f := newFixture(t)
		p := f.consumed("sample-feature")
		if _, err := f.settle(p, door.OutcomeUnknown); err != nil {
			t.Fatal(err)
		}
		got, err := f.settle(p, o)
		if err != nil || got.State != o.State() {
			t.Errorf("unknown then %s: %+v, %v", o, got, err)
		}
		// And then it is final: unknown does not come back.
		_, err = f.settle(p, door.OutcomeUnknown)
		wantCode(t, err, record.CodeApprovalConsumed)
	}
}

// Of many uses at once exactly one is taken; the others are refused
// approval-consumed.
func TestUseIsTakenOnce(t *testing.T) {
	f := newFixture(t)
	p := f.approved("sample-feature")
	const n = 32
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.d.Use(f.ctx, door.UseInput{ID: p.ID, Fresh: fresh(p)})
		}(i)
	}
	wg.Wait()
	taken := 0
	for _, err := range errs {
		if err == nil {
			taken++
			continue
		}
		wantCode(t, err, record.CodeApprovalConsumed)
	}
	if taken != 1 {
		t.Errorf("%d uses were taken, want 1", taken)
	}
}

// Ask, withdraw, ask again: the second request for the same target is a
// new record with a new id, and the first stays withdrawn.
func TestAskWithdrawAskAgain(t *testing.T) {
	f := newFixture(t)
	first := f.pending("sample-feature")
	_, err := f.d.Admit(f.ctx, f.request("sample-feature", nil))
	wantCode(t, err, record.CodeRequestDuplicate)
	if err := f.d.Withdraw(first.ID); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Minute)
	second := f.pending("sample-feature")
	if second.ID == first.ID || second.RequestID == first.RequestID {
		t.Errorf("the second record reuses an id: %s %s", second.ID, second.RequestID)
	}
	if f.state(first.ID) != record.StateWithdrawn || f.state(second.ID) != record.StatePending {
		t.Errorf("states = %s, %s", f.state(first.ID), f.state(second.ID))
	}
	// A decision for the first does not reach the second.
	_, err = f.decide(first, true, nil)
	wantCode(t, err, record.CodeApprovalVoid)
	list, _ := f.d.List(f.ctx)
	if len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Errorf("list = %+v", list)
	}
}

// Each final state lets the lead ask for the same target again; consumed
// and unknown do not, since their outcome is still open.
func TestWhatEndsARequest(t *testing.T) {
	ends := map[record.State]func(f *fixture, p record.Pending){
		record.StateDenied:    func(f *fixture, p record.Pending) { f.decide(p, false, nil) },
		record.StateWithdrawn: func(f *fixture, p record.Pending) { f.d.Withdraw(p.ID) },
		record.StateVoid:      func(f *fixture, p record.Pending) { f.d.Void(p.ID) },
		record.StateExpired:   func(f *fixture, p record.Pending) { f.clock.Advance(PendingLife) },
		record.StateApplied: func(f *fixture, p record.Pending) {
			f.decide(p, true, nil)
			f.use(p, nil)
			f.settle(p, door.OutcomeApplied)
		},
		record.StateNotApplied: func(f *fixture, p record.Pending) {
			f.decide(p, true, nil)
			f.use(p, nil)
			f.settle(p, door.OutcomeNotApplied)
		},
		record.StateApproved: func(f *fixture, p record.Pending) { f.decide(p, true, nil) },
		record.StateConsumed: func(f *fixture, p record.Pending) { f.decide(p, true, nil); f.use(p, nil) },
		record.StateUnknown: func(f *fixture, p record.Pending) {
			f.decide(p, true, nil)
			f.use(p, nil)
			f.settle(p, door.OutcomeUnknown)
		},
	}
	for state, end := range ends {
		f := newFixture(t)
		p := f.pending("sample-feature")
		end(f, p)
		if got := f.state(p.ID); got != state {
			t.Errorf("want %s, the record is %s", state, got)
			continue
		}
		_, err := f.d.Admit(f.ctx, f.request("sample-feature", nil))
		if Final(state) {
			if err != nil {
				t.Errorf("after %s: asking again is refused: %v", state, err)
			}
		} else {
			wantCode(t, err, record.CodeRequestDuplicate)
		}
	}
}

// The test-only methods end a request before it has a record as well, and
// say no when there is nothing to end.
func TestEndingMethods(t *testing.T) {
	f := newFixture(t)
	f.admit("sample-feature")
	if err := f.d.Withdraw("rq-test-ab"); err != nil {
		t.Fatalf("withdraw a request with no record: %v", err)
	}
	if err := f.d.Withdraw("rq-test-ab"); err == nil {
		t.Error("a request was withdrawn twice")
	}
	if list, _ := f.d.List(f.ctx); len(list) != 0 {
		t.Errorf("a request with no record is listed: %+v", list)
	}
	f.admit("sample-feature")
	if err := f.d.Void("rq-test-ac"); err != nil {
		t.Fatalf("void a request with no record: %v", err)
	}

	// With a record, the request id names the record.
	p := f.pending("sample-feature")
	if err := f.d.Withdraw(p.RequestID); err != nil || f.state(p.ID) != record.StateWithdrawn {
		t.Errorf("withdraw by request id: %v, state %s", err, f.state(p.ID))
	}
	for name, end := range map[string]func(string) error{"withdraw": f.d.Withdraw, "void": f.d.Void, "expire": f.d.Expire} {
		if err := end(p.ID); err == nil {
			t.Errorf("%s of a withdrawn record gave no error", name)
		}
		if err := end("ap-none"); err == nil {
			t.Errorf("%s of an unknown id gave no error", name)
		}
		if _, ok := end("ap-none").(*record.Error); ok {
			t.Errorf("%s answers with a refusal code; it is not a verb of the door", name)
		}
	}
	if got := f.state(p.ID); got != record.StateWithdrawn {
		t.Errorf("state = %s", got)
	}

	// A used approval is not withdrawn or voided.
	f.clock.Advance(time.Minute)
	c := f.consumed("other-feature")
	if f.d.Withdraw(c.ID) == nil || f.d.Void(c.ID) == nil || f.d.Expire(c.ID) == nil {
		t.Error("a consumed record was ended")
	}
	if got := f.state(c.ID); got != record.StateConsumed {
		t.Errorf("state = %s", got)
	}
}

// Expire works by the clock: it refuses before the record's time, and the
// record is expired once the clock is past it. An approval runs out at the
// decision's end, not at the pending record's.
func TestExpireFollowsTheClock(t *testing.T) {
	f := newFixture(t)
	p := f.pending("sample-feature")
	f.clock.Advance(PendingLife - time.Second)
	if err := f.d.Expire(p.ID); err == nil {
		t.Fatal("expired a second early")
	}
	if got := f.state(p.ID); got != record.StatePending {
		t.Fatalf("state = %s", got)
	}
	f.clock.Advance(time.Second)
	if err := f.d.Expire(p.ID); err != nil {
		t.Fatalf("expire at the record's time: %v", err)
	}
	shown, _ := f.d.Show(f.ctx, p.ID)
	if shown.Entry.State != record.StateExpired || shown.Entry.ReasonCode != record.CodeApprovalExpired {
		t.Errorf("entry = %+v", shown.Entry)
	}
	// Moving the clock back does not bring it back.
	f.clock.Set(start)
	if got := f.state(p.ID); got != record.StateExpired {
		t.Errorf("state after the clock went back = %s", got)
	}

	f.clock.Set(start.Add(48 * time.Hour))
	a := f.approved("sample-feature")
	if got, _ := f.d.Show(f.ctx, a.ID); got.Entry.ExpiresAt != record.FormatTime(f.clock.Now().Add(30*time.Minute)) {
		t.Errorf("an approved record's expiry = %s, want the decision's", got.Entry.ExpiresAt)
	}
	f.clock.Advance(30*time.Minute - time.Second)
	if got := f.state(a.ID); got != record.StateApproved {
		t.Fatalf("state a second before the approval's end = %s", got)
	}
	f.clock.Advance(time.Second)
	if got := f.state(a.ID); got != record.StateExpired {
		t.Errorf("state at the approval's end = %s", got)
	}
}

// A consumed record does not expire and is not voided by the seat: its
// outcome is still owed.
func TestAConsumedRecordWaitsForItsOutcome(t *testing.T) {
	f := newFixture(t)
	p := f.consumed("sample-feature")
	f.clock.Advance(72 * time.Hour)
	f.d.SetSeatRev(13)
	f.d.SetEpoch(8)
	if got := f.state(p.ID); got != record.StateConsumed {
		t.Fatalf("state = %s", got)
	}
	if got, err := f.settle(p, door.OutcomeApplied); err != nil || got.State != record.StateApplied {
		t.Errorf("settle = %+v, %v", got, err)
	}
}

// A new epoch voids what is not yet used, of the old one: the listing says
// so with the code, and a request that waits for its plan ends too.
func TestANewEpochVoidsTheOld(t *testing.T) {
	f := newFixture(t)
	pending := f.pending("feature-a")
	approved := f.approved("feature-b")
	waiting := f.admit("feature-c")
	f.d.SetEpoch(8)
	for _, id := range []string{pending.ID, approved.ID} {
		shown, _ := f.d.Show(f.ctx, id)
		if shown.Entry.State != record.StateVoid || shown.Entry.ReasonCode != record.CodePreviousEpoch {
			t.Errorf("%s: %+v", id, shown.Entry)
		}
	}
	// The new epoch's lead asks for the same things, and is admitted: the
	// old epoch's request that waited for its plan has ended as well.
	for _, name := range []string{"feature-a", "feature-b", "feature-c"} {
		if _, err := f.d.Admit(f.ctx, f.request(name, func(r *record.Request) { r.Epoch = 8 })); err != nil {
			t.Errorf("%s in the new epoch: %v", name, err)
		}
	}
	_, err := f.d.Plan(f.ctx, f.plan(waiting))
	wantCode(t, err, record.CodePreviousEpoch)
}

// The per-minute cap counts a minute: the seventh request is taken once
// the first is a minute old.
func TestRateCapPassesWithTheMinute(t *testing.T) {
	f := newFixture(t)
	for i := 0; i < RequestsPerMinute; i++ {
		f.admit("feature-" + string(rune('a'+i)))
		f.endLast()
		f.clock.Advance(time.Second)
	}
	_, err := f.d.Admit(f.ctx, f.request("feature-y", nil))
	wantCode(t, err, record.CodeRequestRate)
	f.clock.Set(start.Add(time.Minute - time.Second))
	_, err = f.d.Admit(f.ctx, f.request("feature-y", nil))
	wantCode(t, err, record.CodeRequestRate)
	// A refused request is not counted, so one more second is enough.
	f.clock.Set(start.Add(time.Minute))
	if _, err := f.d.Admit(f.ctx, f.request("feature-y", nil)); err != nil {
		t.Errorf("a minute after the first: %v", err)
	}
}

// The cap of requests not yet final counts those that wait for a plan,
// those that are pending, approved and consumed, and those whose outcome is
// unknown; one that ends makes room.
func TestPendingCapCountsWhatIsNotFinal(t *testing.T) {
	f := newFixture(t)
	next := func(i int) string {
		f.clock.Advance(15 * time.Second)
		return "feature-" + string(rune('a'+i))
	}
	f.admit(next(0))
	f.pending(next(1))
	f.approved(next(2))
	f.consumed(next(3))
	unknown := f.consumed(next(4))
	if _, err := f.settle(unknown, door.OutcomeUnknown); err != nil {
		t.Fatal(err)
	}
	room := f.pending(next(5))
	f.pending(next(6))
	f.pending(next(7))
	_, err := f.d.Admit(f.ctx, f.request(next(8), nil))
	wantCode(t, err, record.CodeRequestPendingCap)
	if _, err := f.decide(room, false, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.d.Admit(f.ctx, f.request(next(9), nil)); err != nil {
		t.Errorf("after one was denied: %v", err)
	}
}

// The repeat throttle: three requests for one target in an hour are
// taken, the fourth starts ten minutes of refusals, and after them each
// request is judged against the hour just past, so the throttle starts
// again while three are still in it. Another target is not touched by it.
func TestRepeatThrottle(t *testing.T) {
	f := newFixture(t)
	ask := func() error {
		_, err := f.d.Admit(f.ctx, f.request("sample-feature", nil))
		if err == nil {
			f.endLast()
		}
		return err
	}
	for i := 0; i < RepeatLimit; i++ {
		if err := ask(); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
		f.clock.Advance(10 * time.Minute)
	}
	throttledAt := f.clock.Now()
	wantCode(t, ask(), record.CodeRequestThrottled)
	if _, err := f.d.Admit(f.ctx, f.request("other-feature", nil)); err != nil {
		t.Errorf("another target is throttled too: %v", err)
	}
	f.clock.Set(throttledAt.Add(ThrottleFor - time.Second))
	wantCode(t, ask(), record.CodeRequestThrottled)
	// The requests were at minutes 0, 10 and 20, and the throttle began at
	// minute 30: at minute 40 there are still three in the hour.
	f.clock.Set(throttledAt.Add(ThrottleFor))
	wantCode(t, ask(), record.CodeRequestThrottled)
	// At minute 60 only two are left in the hour, so one is taken, and
	// the next is one too many again.
	f.clock.Set(throttledAt.Add(30 * time.Minute))
	if err := ask(); err != nil {
		t.Fatalf("with two left in the hour: %v", err)
	}
	f.clock.Advance(time.Minute)
	wantCode(t, ask(), record.CodeRequestThrottled)

	// Requests spread over more than an hour never reach the throttle.
	g := newFixture(t)
	for i := 0; i < 3*RepeatLimit; i++ {
		req := g.request("sample-feature", nil)
		if _, err := g.d.Admit(g.ctx, req); err != nil {
			t.Fatalf("request %d, one every 31 minutes: %v", i+1, err)
		}
		g.endLast()
		g.clock.Advance(31 * time.Minute)
	}
}

// A refused plan ends its request: the lead asks again with a new id, and
// is not told the first is still live.
func TestARefusedPlanEndsTheRequest(t *testing.T) {
	f := newFixture(t)
	req := f.admit("sample-feature")
	in := f.plan(req)
	in.TargetNow = otherID
	_, err := f.d.Plan(f.ctx, in)
	wantCode(t, err, record.CodeBindingMismatch)
	_, err = f.d.Plan(f.ctx, f.plan(req))
	wantCode(t, err, record.CodeRequestInvalid)
	if list, _ := f.d.List(f.ctx); len(list) != 0 {
		t.Errorf("a refused plan left a record: %+v", list)
	}
	p := f.pending("sample-feature")
	if p.ID != "ap-20261120-0007" {
		t.Errorf("the refused plan took a number: the next id is %s", p.ID)
	}
}

// The pending record is made from the seat and the clock, and is what the
// package record accepts; its plan is the same for the same binding.
func TestPlanWritesThePendingRecord(t *testing.T) {
	f := newFixture(t)
	f.clock.Set(time.Date(2026, 12, 31, 23, 59, 59, 0, time.FixedZone("west", -9*3600)))
	p := f.pending("sample-feature")
	want := record.Subject{Kind: record.SubjectSeat, RoleName: "role-a", Epoch: 7, SeatRev: 12}
	got := p.Binding.Subject
	holder := got.Holder
	got.Holder = nil
	if got != want || holder == nil || *holder != (record.Holder{Kind: "lead", DisplayID: "role-a@e7", Runtime: "runtime-a"}) {
		t.Errorf("subject = %+v, holder %+v", got, holder)
	}
	// The id's date and the times are in UTC.
	if p.ID != "ap-20270101-0007" || p.Binding.RequestedAt != "2027-01-01T08:59:59Z" || p.Binding.PendingExpiresAt != "2027-01-02T08:59:59Z" {
		t.Errorf("id %s, requested %s, expires %s", p.ID, p.Binding.RequestedAt, p.Binding.PendingExpiresAt)
	}
	if p.Planner != "planner-a" || p.Binding.Project != "project-a" || p.Binding.PlanToken != "h-3fa9c2e1" {
		t.Errorf("planner %s, project %s, token %s", p.Planner, p.Binding.Project, p.Binding.PlanToken)
	}
	if record.PlanDigest(string(p.PlanText)) != p.Binding.PlanSHA256 || p.PlanText != f.host.PlanText {
		t.Errorf("plan text = %q", p.PlanText)
	}

	// The numbers count up, and the records are told apart by more than
	// the id: each binding has its own digest.
	f.clock.Advance(time.Minute)
	q := f.pending("other-feature")
	if q.ID != "ap-20270101-0008" || q.BindingSHA256 == p.BindingSHA256 {
		t.Errorf("second record: %s %s", q.ID, q.BindingSHA256)
	}
}

// What the stand-in returns and what it was given are copies: a caller
// that writes into either changes nothing the stand-in keeps.
func TestRecordsAreCopied(t *testing.T) {
	f := newFixture(t)
	req := f.admit("sample-feature")
	in := f.plan(req)
	rec, err := f.d.Plan(f.ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	want := bytes.Clone(rec.Bytes)
	p, _ := record.DecodePending(rec.Bytes)
	dec := f.decision(p, true, nil)
	wantDecision := bytes.Clone(dec.Bytes)
	if _, err := f.d.Decide(f.ctx, dec); err != nil {
		t.Fatal(err)
	}

	clear(rec.Bytes)
	clear(dec.Bytes)
	clear(req.Bytes)
	in.Summary.Flagged[0].Path = "elsewhere"
	in.Evidence[0].RecordID = "v-0000"
	shown, _ := f.d.Show(f.ctx, p.ID)
	if !bytes.Equal(shown.Pending.Bytes, want) || !bytes.Equal(shown.Decision.Bytes, wantDecision) {
		t.Fatal("the stand-in kept the caller's bytes")
	}
	clear(shown.Pending.Bytes)
	clear(shown.Decision.Bytes)
	again, _ := f.d.Show(f.ctx, p.ID)
	if !bytes.Equal(again.Pending.Bytes, want) || !bytes.Equal(again.Decision.Bytes, wantDecision) {
		t.Error("show handed out the stand-in's own bytes")
	}
	// The use still compares with the binding as it was written.
	if _, err := f.use(p, nil); err != nil {
		t.Errorf("use after the caller changed its input: %v", err)
	}
}

// A call that was cancelled fails with the context's error, which is not
// a refusal, and does nothing.
func TestACancelledCallIsNotARefusal(t *testing.T) {
	f := newFixture(t)
	p := f.approved("sample-feature")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := map[string]func() error{
		"admit":  func() error { _, err := f.d.Admit(ctx, f.request("other-feature", nil)); return err },
		"plan":   func() error { _, err := f.d.Plan(ctx, f.plan(f.request("other-feature", nil))); return err },
		"decide": func() error { _, err := f.d.Decide(ctx, f.decision(p, false, nil)); return err },
		"use":    func() error { _, err := f.d.Use(ctx, door.UseInput{ID: p.ID, Fresh: fresh(p)}); return err },
		"settle": func() error {
			_, err := f.d.Settle(ctx, door.SettleInput{ID: p.ID, BindingSHA256: p.BindingSHA256, Outcome: door.OutcomeApplied})
			return err
		},
		"list": func() error { _, err := f.d.List(ctx); return err },
		"show": func() error { _, err := f.d.Show(ctx, p.ID); return err },
	}
	for name, call := range calls {
		err := call()
		if !errors.Is(err, context.Canceled) {
			t.Errorf("%s: %v, want the context's error", name, err)
		}
		if _, refused := record.CodeOf(err); refused {
			t.Errorf("%s: a cancelled call reads as a refusal", name)
		}
	}
	if got := f.state(p.ID); got != record.StateApproved {
		t.Errorf("state = %s; a cancelled call did something", got)
	}
}

// New fills what a configuration leaves out with the sample's values, and
// needs only a clock.
func TestNewDefaults(t *testing.T) {
	clock := NewClock(start)
	d := New(Config{Now: clock.Now})
	f := newFixture(t)
	f.d = d
	f.clock = clock
	p := f.pending("sample-feature")
	if p.ID != "ap-20261120-0001" || p.Binding.Project != "project-a" || p.Planner != "planner-a" {
		t.Errorf("record = %s %s %s", p.ID, p.Binding.Project, p.Planner)
	}
	if got := string(p.PlanText); got[:len("planner ")] != "planner " {
		t.Errorf("plan text = %q", got)
	}
	defer func() {
		if recover() == nil {
			t.Error("New took a configuration with no clock")
		}
	}()
	New(Config{})
}

func TestClock(t *testing.T) {
	c := NewClock(start)
	if got := c.Advance(90 * time.Second); !got.Equal(start.Add(90*time.Second)) || !c.Now().Equal(got) {
		t.Errorf("advance = %v, now %v", got, c.Now())
	}
	c.Set(start.Add(-time.Hour))
	if !c.Now().Equal(start.Add(-time.Hour)) {
		t.Errorf("set back: %v", c.Now())
	}
}
