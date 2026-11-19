package ownerchannel

import (
	"context"
	"errors"
	"testing"
	"time"

	"ataniq/internal/approval/door"
	"ataniq/internal/approval/ownerjournal"
	"ataniq/internal/approval/record"
)

// OC9: stop the service at each step of a decision and start a new one on
// the same journal and door. Nothing is applied twice, the decision is
// delivered once, and the approval ends in the state the mirror shows.
func TestOC9StoppingAtEachStep(t *testing.T) {
	// For each point: where the approval ends, and how many times the ref
	// was updated. A stop after the door used the approval and before the
	// update leaves the mirror at the old value, so the restart says
	// not-applied and never applies it: the lead asks again.
	points := []struct {
		point string
		state record.State
		moves int
	}{
		{"decision-journaled", record.StateApplied, 1},
		{"door-decided", record.StateApplied, 1},
		{"delivered", record.StateApplied, 1},
		{"fresh-read", record.StateApplied, 1},
		{"door-used", record.StateNotApplied, 0},
		{"use-journaled", record.StateNotApplied, 0},
		{"applied", record.StateApplied, 1},
		{"settled", record.StateApplied, 1},
		{"outcome-journaled", record.StateApplied, 1},
	}
	for _, c := range points {
		t.Run(c.point, func(t *testing.T) {
			e := newEnv(t)
			e.start()
			p := e.card("feature-a")
			e.fault = c.point
			_, err := e.decide(p, true)
			if !errors.Is(err, ErrStopped) || !errors.Is(err, errInjected) {
				t.Fatalf("Decide returned %v, want the injected stop", err)
			}
			// A stopped service refuses everything.
			if _, err := e.svc.List(context.Background()); !errors.Is(err, ErrStopped) {
				t.Errorf("a stopped service answered a listing: %v", err)
			}
			e.restart()

			if got := e.state(p.ID); got != c.state {
				t.Errorf("the door's state is %s, want %s", got, c.state)
			}
			if calls, moves := e.intake.appliesSeen(); moves != c.moves || calls != c.moves {
				t.Errorf("applied %d times (%d updates), want %d", calls, moves, c.moves)
			}
			if _, uses, _ := e.door.counts(); uses != 1 {
				t.Errorf("the door's use ran %d times, want once", uses)
			}
			if n := len(e.entries(ownerjournal.KindDecision)); n != 1 {
				t.Errorf("%d decision entries", n)
			}
			if n := len(e.entries(ownerjournal.KindDelivered)); n != 1 {
				t.Errorf("%d delivered entries, want the decision delivered once", n)
			}
			shown, err := e.stand.Show(context.Background(), p.ID)
			if err != nil || string(shown.Decision.Bytes) != string(e.entries(ownerjournal.KindDecision)[0].Record) {
				t.Errorf("the door does not hold the journaled bytes: %v", err)
			}
			if n := len(e.entries(ownerjournal.KindUse)); n != 1 {
				t.Errorf("%d use entries, want one", n)
			}
			out := e.entries(ownerjournal.KindOutcome)
			if len(out) != 1 || !contains(string(out[0].Body), `"state":"`+string(c.state)+`"`) {
				t.Errorf("the outcome entries are %v", out)
			}
			open, err := e.svc.journal().OpenUses()
			if err != nil || len(open) != 0 {
				t.Errorf("uses left open: %v %v", open, err)
			}
			if und, err := e.svc.journal().Undelivered(); err != nil || len(und) != 0 {
				t.Errorf("decisions left undelivered: %v %v", und, err)
			}
			replies := e.mail.repliesFor("rq-test-01")
			if len(replies) == 0 || replies[len(replies)-1].State != c.state {
				t.Errorf("the lead was told %+v, want %s", replies, c.state)
			}
			if n := e.merger.pinCount(); n != 0 {
				t.Errorf("%d pins remain", n)
			}
			// Another restart changes nothing.
			e.restart()
			if calls, _ := e.intake.appliesSeen(); calls != c.moves {
				t.Errorf("a second restart applied again: %d", calls)
			}
			if _, uses, settles := e.door.counts(); uses != 1 || settles != 1 {
				t.Errorf("a second restart reached the door's use or settle again: %d, %d", uses, settles)
			}
		})
	}
}

// A deny stopped between the journal and the door is delivered once.
func TestAStoppedDenyIsDeliveredOnce(t *testing.T) {
	for _, point := range []string{"decision-journaled", "door-decided"} {
		t.Run(point, func(t *testing.T) {
			e := newEnv(t)
			e.start()
			p := e.card("feature-a")
			e.fault = point
			if _, err := e.decide(p, false); !errors.Is(err, errInjected) {
				t.Fatalf("Decide returned %v", err)
			}
			e.restart()
			if e.state(p.ID) != record.StateDenied {
				t.Errorf("the door's state is %s", e.state(p.ID))
			}
			if n := len(e.entries(ownerjournal.KindDelivered)); n != 1 {
				t.Errorf("%d delivered entries", n)
			}
			replies := e.mail.repliesFor("rq-test-01")
			if len(replies) != 1 || replies[0].State != record.StateDenied {
				t.Errorf("the lead was told %+v", replies)
			}
			if n := e.merger.pinCount(); n != 0 {
				t.Errorf("%d pins remain", n)
			}
		})
	}
}

// OC9: the restart reads what the mirror shows, which is not always the
// old or the new value.
func TestARestartSettlesAUseLeftOpenFromTheMirror(t *testing.T) {
	cases := []struct {
		name   string
		mirror func(e *env)
		want   record.State
	}{
		{"the new value is there", func(e *env) {
			e.intake.refs[targetRef] = mergeID
		}, record.StateApplied},
		{"the old value is there", func(e *env) {}, record.StateNotApplied},
		{"another value is there", func(e *env) {
			e.intake.refs[targetRef] = "5555555555555555555555555555555555555555"
		}, record.StateUnknown},
		{"the mirror cannot be read", func(e *env) { e.intake.mirrorErr = errors.New("no mirror") }, record.StateUnknown},
		{"the mirror answers a word that is not one", func(e *env) { e.intake.mirrorWord = "maybe" }, record.StateUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.start()
			p := e.card("feature-a")
			e.fault = "door-used"
			if _, err := e.decide(p, true); !errors.Is(err, errInjected) {
				t.Fatal(err)
			}
			e.intake.mu.Lock()
			c.mirror(e)
			e.intake.mu.Unlock()
			e.restart()
			if got := e.state(p.ID); got != c.want {
				t.Errorf("the door's state is %s, want %s", got, c.want)
			}
			if calls, _ := e.intake.appliesSeen(); calls != 0 {
				t.Errorf("the restart applied: %d", calls)
			}
			if c.want == record.StateUnknown {
				u, err := e.svc.journal().UnknownOutcomes()
				if err != nil || len(u) != 1 {
					t.Errorf("unknown outcomes: %v %v", u, err)
				}
				if n := e.merger.pinCount(); n != 1 {
					t.Errorf("%d pins, want the pin kept", n)
				}
			}
		})
	}
}

// OC9 and OC21: a request stopped during its verification is not resumed:
// nothing was decided for it. The pending records that exist are listed
// again from the door and keep their pins.
func TestARestartKeepsThePendingCardsAndTheirPins(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.advance(time.Minute)
	e.fault = "verified"
	if _, err := e.svc.Submit(context.Background(), e.collected(e.request("feature-b", nil))); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	e.restart()
	l, err := e.svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Pending) != 1 || l.Pending[0].ID != p.ID || l.Pending[0].Held {
		t.Fatalf("the listing after the restart is %+v", l.Pending)
	}
	if e.state(p.ID) != record.StatePending {
		t.Errorf("the door's state is %s", e.state(p.ID))
	}
	// The card that was pending has its pin; the interrupted request's pin
	// is left for whoever cleans up (the request has no pending record).
	if n := e.merger.pinCount(); n != 2 {
		t.Errorf("%d pins, want the pending card's and the interrupted request's", n)
	}
	// Nothing was decided meanwhile.
	if n := len(e.entries(ownerjournal.KindDecision)); n != 0 {
		t.Errorf("%d decisions", n)
	}
	// The card can still be decided.
	if res, err := e.decide(p, true); err != nil || res.State != record.StateApplied {
		t.Fatalf("%+v %v", res, err)
	}
}

// A request planned by the door whose count the service did not journal
// before it stopped is counted at the restart.
func TestARestartCountsARequestThatWasPlannedButNotCounted(t *testing.T) {
	e := newEnv(t)
	e.start()
	e.fault = "planned"
	if _, err := e.svc.Submit(context.Background(), e.collected(e.request("feature-a", nil))); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	e.restart()
	counts := e.entries(ownerjournal.KindCount)
	if len(counts) != 1 || counts[0].PendingID == "" {
		t.Fatalf("the count entries are %v", counts)
	}
	e.restart()
	if n := len(e.entries(ownerjournal.KindCount)); n != 1 {
		t.Errorf("%d count entries after another restart", n)
	}
	l, err := e.svc.List(context.Background())
	if err != nil || l.Count.Requests != 1 {
		t.Errorf("%+v %v", l.Count, err)
	}
}

// OC24: the journal and the door disagree.
func TestOC24TheJournalAndTheDoorDisagree(t *testing.T) {
	e := newEnv(t)
	e.start()
	ctx := context.Background()
	journaled := e.card("feature-a")
	e.advance(time.Minute)
	forged := e.card("feature-b")
	e.advance(time.Minute)
	other := e.card("feature-c")

	// A decision the journal has and the door lacks is delivered at the
	// restart.
	e.fault = "decision-journaled"
	if _, err := e.decide(journaled, true); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	// A decision the door holds and the journal lacks: made at the door
	// directly, by no console.
	forgedDecision := forgedFor(t, e, forged)
	if _, err := e.stand.Decide(ctx, forgedDecision); err != nil {
		t.Fatal(err)
	}
	e.restart()

	if got := e.state(journaled.ID); got != record.StateApplied {
		t.Errorf("the journaled decision's state is %s, want it delivered and applied", got)
	}
	if got := e.state(forged.ID); got != record.StateApproved {
		t.Errorf("the forged approval's state is %s", got)
	}
	if _, uses, _ := e.door.counts(); uses != 1 {
		t.Errorf("the door's use ran %d times: the forged approval must be refused", uses)
	}
	alerts := e.entries(ownerjournal.KindAlert)
	if len(alerts) != 1 || alerts[0].PendingID != forged.ID || contains(string(alerts[0].Body), `"limit_reached":true`) {
		t.Fatalf("the alerts are %v", alerts)
	}
	if got := e.noticed(); len(got) == 0 {
		t.Error("no notice was raised")
	}
	// The alert is not repeated by another restart or tick.
	e.restart()
	if err := e.svc.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(e.entries(ownerjournal.KindAlert)); n != 1 {
		t.Errorf("%d alerts after another restart", n)
	}
	// The listing says so, and others go on.
	l, err := e.svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, r := range l.Recent {
		if r.ID == forged.ID {
			line = r.Line
		}
	}
	if !contains(line, "refused") {
		t.Errorf("the listing's line for the forged approval is %q", line)
	}
	e.resetRef()
	if res, err := e.decide(other, true); err != nil || res.State != record.StateApplied {
		t.Fatalf("another approval did not go on: %+v %v", res, err)
	}
	if e.state(forged.ID) != record.StateApproved {
		t.Errorf("the forged approval changed: %s", e.state(forged.ID))
	}
}

// forgedFor makes a decision that the console never sent.
func forgedFor(t *testing.T, e *env, p record.Pending) door.Record {
	t.Helper()
	d := record.Decision{
		Schema: record.SchemaDecision, ID: p.ID, Planner: p.Planner, Decision: record.DecisionApprove,
		BindingSHA256: p.BindingSHA256, Project: p.Binding.Project, Subject: p.Binding.Subject, PlanToken: p.Binding.PlanToken,
		SignedOff: []record.SignOff{}, Uses: 1,
		DecidedAt: record.FormatTime(e.clock.Now()), ExpiresAt: record.FormatTime(e.clock.Now().Add(30 * time.Minute)),
		Confirmed: record.ConfirmedCode,
		Console:   record.Console{PID: 1, TTY: "/dev/pts/9", Client: "forged", Decider: true, Surface: "console", Session: "1111111111111111"},
	}
	d.Subject.Holder = nil
	for _, f := range p.Binding.Summary.Flagged {
		d.SignedOff = append(d.SignedOff, record.SignOff{Kind: f.Kind, Path: f.Path})
	}
	b, err := record.EncodeDecision(d)
	if err != nil {
		t.Fatal(err)
	}
	return door.RecordOf(b)
}

// OC22: a clock that moves back by more than a minute refuses uses until
// the service restarts, and loses no state.
func TestOC22AClockMovedBackRefusesUses(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	t0 := e.clock.Now()
	// The clock moves back by two minutes after the decision reached the
	// door and before the use.
	e.at["delivered"] = func() { e.clock.Set(t0.Add(-2 * time.Minute)) }
	res, err := e.decide(p, true)
	if err != nil {
		t.Fatal(err)
	}
	delete(e.at, "delivered")
	if res.State != record.StateApproved || res.Line != lineClockBack {
		t.Fatalf("the result is %+v, want the use refused", res)
	}
	if _, uses, _ := e.door.counts(); uses != 0 {
		t.Error("the door's use ran with the clock moved back")
	}
	if calls, _ := e.intake.appliesSeen(); calls != 0 {
		t.Error("applied with the clock moved back")
	}
	// Later ticks, with the clock moving forward again, still refuse.
	e.clock.Set(t0.Add(time.Minute))
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.state(p.ID) != record.StateApproved {
		t.Fatalf("the approval's state is %s: uses must be refused until a restart", e.state(p.ID))
	}
	// A restart takes the approval up where it was: no state was lost.
	e.restart()
	if got := e.state(p.ID); got != record.StateApplied {
		t.Errorf("after the restart the state is %s", got)
	}
	if calls, moves := e.intake.appliesSeen(); calls != 1 || moves != 1 {
		t.Errorf("applied %d times, moved %d", calls, moves)
	}
}

func TestAClockMovedBackByUnderAMinuteIsNotRefused(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	t0 := e.clock.Now()
	e.at["delivered"] = func() { e.clock.Set(t0.Add(-59 * time.Second)) }
	res, err := e.decide(p, true)
	if err != nil || res.State != record.StateApplied {
		t.Fatalf("%+v %v", res, err)
	}
}

// A journal that answers ErrUncertain after a write is closed, opened again
// and read: the decision that was written is delivered, nothing is lost or
// done twice, and the caller is not told of a refusal.
func TestAnUncertainJournalIsReopenedAndNeverReportedAsARefusal(t *testing.T) {
	e := newEnv(t)
	var wrapped []*flaky
	e.journalWrap = func(s *ownerjournal.Store) Journal {
		f := &flaky{Store: s}
		wrapped = append(wrapped, f)
		return f
	}
	e.start()
	p := e.card("feature-a")
	wrapped[0].failDecision = true
	_, err := e.decide(p, true)
	if !errors.Is(err, ErrJournalReopened) {
		t.Fatalf("Decide returned %v, want ErrJournalReopened", err)
	}
	if c, ok := record.CodeOf(err); ok {
		t.Fatalf("the uncertain write was reported as the refusal %s", c)
	}
	if len(wrapped) != 2 {
		t.Fatalf("the journal was opened %d times, want it reopened once", len(wrapped))
	}
	// The write had landed: the restart delivered it and carried it out.
	if got := e.state(p.ID); got != record.StateApplied {
		t.Errorf("the door's state is %s", got)
	}
	if n := len(e.entries(ownerjournal.KindDecision)); n != 1 {
		t.Errorf("%d decision entries", n)
	}
	if calls, moves := e.intake.appliesSeen(); calls != 1 || moves != 1 {
		t.Errorf("applied %d times, moved %d", calls, moves)
	}
	// The service goes on with the new store.
	e.advance(time.Minute)
	q := e.card("feature-b")
	e.resetRef()
	if res, err := e.decide(q, false); err != nil || res.State != record.StateDenied {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestAnUncertainJournalWithNoWayToReopenStopsTheService(t *testing.T) {
	e := newEnv(t)
	var f *flaky
	e.journalWrap = func(s *ownerjournal.Store) Journal { f = &flaky{Store: s}; return f }
	e.start()
	// Take the reopening away.
	e.svc.cfg.Reopen = nil
	p := e.card("feature-a")
	f.failDecision = true
	_, err := e.decide(p, true)
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("Decide returned %v, want ErrStopped", err)
	}
	if _, err := e.svc.List(context.Background()); !errors.Is(err, ErrStopped) {
		t.Errorf("a stopped service answered: %v", err)
	}
}

// flaky is a store that fails as a commit that returned an error does:
// the write landed (or may have), and every later call is uncertain.
type flaky struct {
	*ownerjournal.Store
	failDecision bool
	broken       bool
}

func (f *flaky) AppendDecision(b []byte, seen ownerjournal.Seen) (*ownerjournal.Decision, error) {
	if f.broken {
		return nil, ownerjournal.ErrUncertain
	}
	d, err := f.Store.AppendDecision(b, seen)
	if err == nil && f.failDecision {
		f.broken = true
		return nil, ownerjournal.ErrUncertain
	}
	return d, err
}

func (f *flaky) Entries(after int64, max int) ([]ownerjournal.Entry, error) {
	if f.broken {
		return nil, ownerjournal.ErrUncertain
	}
	return f.Store.Entries(after, max)
}

func (f *flaky) Undelivered() ([]*ownerjournal.Decision, error) {
	if f.broken {
		return nil, ownerjournal.ErrUncertain
	}
	return f.Store.Undelivered()
}

// A door that cannot be reached at the start does not stop the service; the
// tick that follows the door's return takes up the work.
func TestADoorOutOfReachAtStartIsRetriedByATick(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.fault = "decision-journaled"
	if _, err := e.decide(p, true); !errors.Is(err, errInjected) {
		t.Fatal(err)
	}
	e.door.failList = errors.New("connection refused")
	e.restart()
	if err := e.svc.Tick(context.Background()); !errors.Is(err, ErrDoorUnavailable) {
		t.Fatalf("Tick returned %v", err)
	}
	e.door.failList = nil
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.state(p.ID) != record.StateApplied {
		t.Errorf("the door's state is %s", e.state(p.ID))
	}
}
