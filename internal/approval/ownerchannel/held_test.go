package ownerchannel

import (
	"context"
	"errors"
	"testing"
	"time"

	"ataniq/internal/approval/limits"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/record"
)

// setLimit changes the project's limit as the journal keeps it.
func (e *env) setLimit(to int) {
	e.t.Helper()
	from, err := e.svc.limitOf("project-a")
	if err != nil {
		e.t.Fatal(err)
	}
	err = e.svc.journal().AppendLimit(ownerjournal.LimitChange{Project: "project-a", At: e.clock.Now(), From: from, To: to})
	if err != nil {
		e.t.Fatal(err)
	}
}

// useUp approves cards until the day's uses reach n, one a minute.
func (e *env) useUp(n int, names ...string) {
	e.t.Helper()
	for i := 0; i < n; i++ {
		e.resetRef()
		p := e.card(names[i])
		if res, err := e.decide(p, true); err != nil || res.State != record.StateApplied {
			e.t.Fatalf("approve %s: %+v %v", names[i], res, err)
		}
		e.advance(time.Minute)
	}
}

func (e *env) heldCount() int {
	e.t.Helper()
	cards, err := e.svc.journal().HeldCards("project-a")
	if err != nil {
		e.t.Fatal(err)
	}
	return len(cards)
}

func TestAnApprovePastTheLimitIsHeldAndNotDelivered(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()
	e.setLimit(2)
	e.useUp(2, "feature-a", "feature-b")

	// Reaching the limit marks the day.
	days, err := e.svc.journal().Days("project-a", e.clock.Now())
	if err != nil || !days[len(days)-1].ReachedLimit || days[len(days)-1].Uses != 2 {
		t.Fatalf("the days are %+v %v", days[len(days)-1], err)
	}

	e.resetRef()
	c := e.card("feature-c")
	decides, _, _ := e.door.counts()
	res, err := e.decide(c, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Held || res.State != record.StatePending || res.Line != limits.HeldReason(2, 2) {
		t.Fatalf("the result is %+v, want pending with the held mark", res)
	}
	if after, _, _ := e.door.counts(); after != decides {
		t.Error("a held decision was handed to the door")
	}
	if e.state(c.ID) != record.StatePending {
		t.Errorf("the door's state is %s: the record stays pending", e.state(c.ID))
	}
	if calls, _ := e.intake.appliesSeen(); calls != 2 {
		t.Errorf("applied %d times", calls)
	}
	if n := len(e.entries(ownerjournal.KindHeld)); n != 1 {
		t.Errorf("%d held entries", n)
	}
	if n := len(e.entries(ownerjournal.KindDecision)); n != 2 {
		t.Errorf("%d decision entries, want the two uses", n)
	}
	if n := e.heldCount(); n != 1 {
		t.Errorf("%d held cards", n)
	}
	// The limit was reached once for the day: a hold does not mark it again.
	if n := len(e.entries(ownerjournal.KindAlert)); n != 1 {
		t.Errorf("%d alerts, want the one for the day that reached its limit", n)
	}
	if n := e.merger.pinCount(); n != 1 {
		t.Errorf("%d pins, want the held card's merge kept", n)
	}

	// The listing and the card show the mark.
	l, err := e.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Pending) != 1 || l.Pending[0].ID != c.ID || !l.Pending[0].Held {
		t.Errorf("the pending rows are %+v", l.Pending)
	}
	if s, err := e.svc.Show(ctx, c.ID); err != nil || !s.Held {
		t.Errorf("%+v %v", s, err)
	}

	// A deny is never held, and is not a use.
	e.advance(time.Minute)
	d := e.card("feature-d")
	if res, err := e.decide(d, false); err != nil || res.Held || res.State != record.StateDenied {
		t.Fatalf("%+v %v", res, err)
	}

	// His later decisions are not queued behind the held one: each is judged
	// by itself. The limit is still reached, so another approve is held too.
	e.advance(time.Minute)
	f := e.card("feature-e")
	if res, err := e.decide(f, true); err != nil || !res.Held {
		t.Fatalf("%+v %v", res, err)
	}

	// A restart delivers nothing for a held decision.
	before, _, _ := e.door.counts()
	e.restart()
	if after, _, _ := e.door.counts(); after != before {
		t.Errorf("the restart handed %d decisions to the door", after-before)
	}
	if e.state(c.ID) != record.StatePending || e.state(f.ID) != record.StatePending {
		t.Error("a restart moved a held card")
	}
	if n := e.heldCount(); n != 2 {
		t.Errorf("%d held cards after the restart", n)
	}
}

func TestDecidingAHeldCardAgainWritesANewDecisionForTheSameID(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()
	e.setLimit(2)
	e.useUp(2, "feature-a", "feature-b")
	e.resetRef()
	c := e.card("feature-c")
	if res, err := e.decide(c, true); err != nil || !res.Held {
		t.Fatalf("%+v %v", res, err)
	}
	// Deciding again with no room is held again, as another entry.
	e.advance(time.Minute)
	if res, err := e.decide(c, true); err != nil || !res.Held {
		t.Fatalf("%+v %v", res, err)
	}
	if n := len(e.entries(ownerjournal.KindHeld)); n != 2 {
		t.Fatalf("%d held entries", n)
	}
	if n := e.heldCount(); n != 1 {
		t.Errorf("%d held cards: the latest entry closes the earlier one", n)
	}

	// With room (the limit raised, as 2 can rise to 3), the same card is
	// decided and goes through.
	e.setLimit(3)
	e.advance(time.Minute)
	res, err := e.decide(c, true)
	if err != nil || res.Held || res.State != record.StateApplied {
		t.Fatalf("%+v %v", res, err)
	}
	if n := len(e.entries(ownerjournal.KindDecision)); n != 3 {
		t.Errorf("%d decision entries, want the two uses and the new one", n)
	}
	if n := e.heldCount(); n != 0 {
		t.Errorf("%d held cards after the card was decided", n)
	}
	l, err := e.svc.List(ctx)
	if err != nil || len(l.Pending) != 0 {
		t.Errorf("%+v %v", l.Pending, err)
	}
	// The decision is for the same id, and is the one the door holds.
	decs := e.entries(ownerjournal.KindDecision)
	if decs[2].PendingID != c.ID {
		t.Errorf("the new decision is for %s", decs[2].PendingID)
	}
}

func TestAHeldCardPastItsExpiryIsClosed(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()
	e.setLimit(1)
	e.useUp(1, "feature-a")
	e.resetRef()
	c := e.card("feature-c")
	if res, err := e.decide(c, true); err != nil || !res.Held {
		t.Fatalf("%+v %v", res, err)
	}
	e.advance(24*time.Hour + time.Minute)
	l, err := e.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Pending) != 0 {
		t.Errorf("the pending rows are %+v", l.Pending)
	}
	found := false
	for _, r := range l.Recent {
		if r.ID == c.ID && r.State == record.StateExpired {
			found = true
		}
	}
	if !found {
		t.Errorf("the recent rows are %+v, want the card expired", l.Recent)
	}
	if n := len(e.entries(ownerjournal.KindExpired)); n != 1 {
		t.Errorf("%d expired entries, want the held card closed", n)
	}
	if n := e.heldCount(); n != 0 {
		t.Errorf("%d held cards", n)
	}
	e.beat()
	_, err = e.svc.Decide(ctx, src, frame(c, true))
	wantCode(t, err, record.CodeApprovalExpired)
	// The lead is told at the next tick, and the pin goes.
	if err := e.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	replies := e.mail.repliesFor("rq-test-02")
	if len(replies) != 1 || replies[0].State != record.StateExpired {
		t.Errorf("the lead was told %+v", replies)
	}
	if n := e.merger.pinCount(); n != 0 {
		t.Errorf("%d pins remain", n)
	}
}

// A held card whose record ended at the door as withdrawn or void is not
// listed as pending: the listing joins the journal's held cards with the
// door's list.
func TestAHeldCardThatEndedAtTheDoorIsNotListedAsPending(t *testing.T) {
	for _, end := range []string{"withdrawn", "void"} {
		t.Run(end, func(t *testing.T) {
			e := newEnv(t)
			e.start()
			e.setLimit(1)
			e.useUp(1, "feature-a")
			e.resetRef()
			c := e.card("feature-c")
			if res, err := e.decide(c, true); err != nil || !res.Held {
				t.Fatalf("%+v %v", res, err)
			}
			var err error
			if end == "withdrawn" {
				err = e.stand.Withdraw(c.ID)
			} else {
				err = e.stand.Void(c.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			l, err := e.svc.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if len(l.Pending) != 0 {
				t.Errorf("the pending rows are %+v", l.Pending)
			}
			found := false
			for _, r := range l.Recent {
				if r.ID == c.ID && string(r.State) == end {
					found = true
				}
			}
			if !found {
				t.Errorf("the recent rows are %+v", l.Recent)
			}
			if err := e.svc.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			replies := e.mail.repliesFor("rq-test-02")
			if len(replies) != 1 || string(replies[0].State) != end {
				t.Errorf("the lead was told %+v", replies)
			}
		})
	}
}

// The limit counts the calendar day in the clock's own location, not the
// UTC date of the decision.
func TestTheLimitCountsTheCalendarDayOfTheClocksLocation(t *testing.T) {
	e := newEnv(t)
	loc := time.FixedZone("zone-a", -9*3600)
	// 23:30 on the 20th there is 08:30 UTC on the 21st.
	e.clock.Set(time.Date(2026, 11, 20, 23, 30, 0, 0, loc))
	e.start()
	e.setLimit(2)
	e.useUp(2, "feature-a", "feature-b")
	e.resetRef()
	c := e.card("feature-c")
	if res, err := e.decide(c, true); err != nil || !res.Held {
		t.Fatalf("a third approve on the same local day: %+v %v", res, err)
	}
	// Past local midnight, the UTC date is the same as before.
	e.clock.Set(time.Date(2026, 11, 21, 0, 10, 0, 0, loc))
	d := e.card("feature-d")
	if utc := e.clock.Now().UTC(); utc.Day() != 21 {
		t.Fatalf("the test needs the UTC date to stay the 21st: %v", utc)
	}
	res, err := e.decide(d, true)
	if err != nil || res.Held || res.State != record.StateApplied {
		t.Fatalf("the first approve of a new local day: %+v %v", res, err)
	}
}

// limitAlerts returns the alert entries that say a day reached its limit.
func (e *env) limitAlerts() []ownerjournal.Entry {
	e.t.Helper()
	var out []ownerjournal.Entry
	for _, en := range e.entries(ownerjournal.KindAlert) {
		if contains(string(en.Body), `"limit_reached":true`) {
			out = append(out, en)
		}
	}
	return out
}

// A stop between the journal's entry and the day's alert does not lose the
// alert: the arrival strip reads it from the journal, and a restart does not
// write it again.
func TestTheLimitAlertIsNotLostByAStopAfterTheDecisionIsJournaled(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.setLimit(1)
	p := e.card("feature-a")
	e.fault = "decision-journaled"
	if _, err := e.decide(p, true); !errors.Is(err, errInjected) {
		t.Fatalf("Decide returned %v", err)
	}
	if n := len(e.limitAlerts()); n != 1 {
		t.Fatalf("%d limit alerts after the stop, want the day's one", n)
	}
	e.restart()
	if n := len(e.limitAlerts()); n != 1 {
		t.Errorf("%d limit alerts after the restart, want the day's one", n)
	}
	days, err := e.svc.journal().Days("project-a", e.clock.Now())
	if err != nil || !days[len(days)-1].ReachedLimit {
		t.Errorf("the day is %+v %v", days[len(days)-1], err)
	}
}

// The same for an approve that is held.
func TestTheLimitAlertIsNotLostByAStopAfterAHeldEntryIsJournaled(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.useUp(2, "feature-a", "feature-b")
	if n := len(e.limitAlerts()); n != 0 {
		t.Fatalf("%d limit alerts before the limit was lowered", n)
	}
	// The limit is lowered under the day's uses: the next approve is held,
	// and the day has no alert yet.
	e.setLimit(1)
	e.resetRef()
	c := e.card("feature-c")
	e.fault = "held-journaled"
	if _, err := e.decide(c, true); !errors.Is(err, errInjected) {
		t.Fatalf("Decide returned %v", err)
	}
	if e.heldCount() != 1 {
		t.Fatalf("%d held cards", e.heldCount())
	}
	if n := len(e.limitAlerts()); n != 1 {
		t.Fatalf("%d limit alerts after the stop, want the day's one", n)
	}
}
