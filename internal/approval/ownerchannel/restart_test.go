package ownerchannel

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
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

// OC22 is about the wall clock. A reading of time.Now carries a monotonic
// reading, with which After and Sub of two readings ignore a wall clock set
// back, so the service drops it. This test cannot set the wall clock; it
// checks that no reading the service keeps or hands out carries one, which
// is what makes the guard see the wall clock.
func TestTheClockGuardReadsTheWallClockAlone(t *testing.T) {
	if !strings.Contains(time.Now().String(), "m=") {
		t.Skip("this platform's clock has no monotonic reading")
	}
	e := newEnv(t)
	e.start()
	e.svc.cfg.Now = time.Now
	got := e.svc.now()
	if strings.Contains(got.String(), "m=") {
		t.Errorf("now() returned a reading with a monotonic part: %s", got)
	}
	e.svc.mu.Lock()
	high := e.svc.high
	e.svc.mu.Unlock()
	if strings.Contains(high.String(), "m=") {
		t.Errorf("the latest reading the guard keeps has a monotonic part: %s", high)
	}
	if time.Since(got) > time.Minute || time.Since(got) < -time.Minute {
		t.Errorf("now() returned %s, which is not the clock's time", got)
	}
}

// noteEnded reads the clock through the guard, so a card first seen ended
// after the clock moved back marks the clock as moved back too.
func TestNoteEndedReadsTheClockThroughTheGuard(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.svc.now()
	back := e.clock.Now().Add(-2 * time.Minute)
	e.clock.Set(back)
	e.svc.noteEnded(door.Entry{ID: p.ID, State: record.StateWithdrawn})
	e.svc.mu.Lock()
	moved, at := e.svc.back, e.svc.ended[p.ID]
	e.svc.mu.Unlock()
	if !moved {
		t.Error("a reading two minutes back, taken by noteEnded, was not seen by the guard")
	}
	if !at.Equal(back) {
		t.Errorf("the card's end was noted at %s, want %s", at, back)
	}
}

// shaky is a store whose ExpireHeld and AppendCount answer ErrUncertain
// when its failing function says so, and which counts how often it was
// opened (each wrapper made is one opening).
type shaky struct {
	*ownerjournal.Store
	failing func(method string) bool
}

func (f *shaky) ExpireHeld(now time.Time) ([]ownerjournal.HeldCard, error) {
	if f.failing("ExpireHeld") {
		return nil, ownerjournal.ErrUncertain
	}
	return f.Store.ExpireHeld(now)
}

func (f *shaky) AppendCount(c ownerjournal.Count) error {
	if f.failing("AppendCount") {
		return ownerjournal.ErrUncertain
	}
	return f.Store.AppendCount(c)
}

// A store that is uncertain again right after it was reopened is not
// reopened again and again: the service reopens once, and when taking up
// what the journal says fails the same way, it stops.
func TestAStoreUncertainAgainAfterItWasReopenedStopsTheService(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	opened, broken := 0, false
	e.journalWrap = func(s *ownerjournal.Store) Journal {
		mu.Lock()
		opened++
		mu.Unlock()
		return &shaky{Store: s, failing: func(m string) bool {
			mu.Lock()
			defer mu.Unlock()
			return broken && m == "ExpireHeld"
		}}
	}
	e.start()
	p := e.card("feature-a")
	mu.Lock()
	broken, opened = true, 0
	mu.Unlock()
	err := e.svc.Tick(context.Background())
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("Tick returned %v, want ErrStopped", err)
	}
	mu.Lock()
	n := opened
	mu.Unlock()
	if n != 1 {
		t.Errorf("the journal was reopened %d times, want once", n)
	}
	// The service is stopped for every later call.
	if _, err := e.svc.List(context.Background()); !errors.Is(err, ErrStopped) {
		t.Errorf("a stopped service answered: %v", err)
	}
	if _, err := e.svc.Decide(context.Background(), src, frame(p, true)); !errors.Is(err, ErrStopped) {
		t.Errorf("Decide on a stopped service returned %v", err)
	}
	// A new service on the same journal, once it is sound, takes over.
	mu.Lock()
	broken = false
	mu.Unlock()
	e.restart()
	if res, err := e.decide(p, false); err != nil || res.State != record.StateDenied {
		t.Fatalf("%+v %v", res, err)
	}
}

// The same for a failure met by a call that takes no decision lock first
// (a listing): one reopen, and then it stops.
func TestAStoreThatKeepsFailingStopsAfterOneReopenForEveryKindOfCall(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	opened, broken := 0, false
	e.journalWrap = func(s *ownerjournal.Store) Journal {
		mu.Lock()
		opened++
		mu.Unlock()
		return &shaky{Store: s, failing: func(m string) bool {
			mu.Lock()
			defer mu.Unlock()
			return broken && m == "ExpireHeld"
		}}
	}
	e.start()
	e.card("feature-a")
	mu.Lock()
	broken, opened = true, 0
	mu.Unlock()
	if _, err := e.svc.List(context.Background()); !errors.Is(err, ErrStopped) {
		t.Fatalf("List returned %v, want ErrStopped", err)
	}
	mu.Lock()
	n := opened
	mu.Unlock()
	if n != 1 {
		t.Errorf("the journal was reopened %d times, want once", n)
	}
}

// Two calls that began with the same store and both saw it fail: the first
// reopens it, and the second does not reopen the new one.
func TestACallDoesNotReopenAStoreAnotherCallReplacedAlready(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	opened := 0
	e.journalWrap = func(s *ownerjournal.Store) Journal {
		mu.Lock()
		opened++
		mu.Unlock()
		return s
	}
	e.start()
	first, done1, err := e.svc.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done1()
	second, done2, err := e.svc.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done2()
	mu.Lock()
	opened = 0
	mu.Unlock()
	if err := e.svc.jfix(first, ownerjournal.ErrUncertain); !errors.Is(err, ErrJournalReopened) {
		t.Fatalf("the first call: %v", err)
	}
	if err := e.svc.jfix(second, ownerjournal.ErrUncertain); !errors.Is(err, ErrJournalReopened) {
		t.Fatalf("the second call: %v", err)
	}
	mu.Lock()
	n := opened
	mu.Unlock()
	if n != 1 {
		t.Errorf("the journal was reopened %d times for two calls that saw the same store fail, want once", n)
	}
	// A call that began after the reopening, and sees the new store fail,
	// reopens it.
	third, done3, err := e.svc.begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer done3()
	if err := e.svc.jfix(third, ownerjournal.ErrUncertain); !errors.Is(err, ErrJournalReopened) {
		t.Fatalf("the third call: %v", err)
	}
	mu.Lock()
	n = opened
	mu.Unlock()
	if n != 2 {
		t.Errorf("the journal was reopened %d times in all, want 2", n)
	}
}

// heldFails is a store whose HeldCards answers ErrUncertain while fails says
// so.
type heldFails struct {
	*ownerjournal.Store
	fails func() bool
}

func (h *heldFails) HeldCards(project string) ([]ownerjournal.HeldCard, error) {
	if h.fails() {
		return nil, ownerjournal.ErrUncertain
	}
	return h.Store.HeldCards(project)
}

// listHook is a door that runs a function once, at the first listing.
type listHook struct {
	door.Door
	fired atomic.Bool
	hook  func()
}

func (l *listHook) List(ctx context.Context) ([]door.Entry, error) {
	// The flag is set first: the hook's own work lists the door too.
	if l.fired.CompareAndSwap(false, true) {
		l.hook()
	}
	return l.Door.List(ctx)
}

// A call is judged by the store it read last, not by the one it began with:
// a listing that began with the first store, found it replaced by another
// call, and then saw the replacement fail, reopens the replacement.
func TestACallReopensTheStoreItReadWhenItWasReplacedAfterTheCallBegan(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	opened := 0
	e.journalWrap = func(s *ownerjournal.Store) Journal {
		mu.Lock()
		opened++
		n := opened
		mu.Unlock()
		// Only the second store, the one another call opens, fails.
		return &heldFails{Store: s, fails: func() bool { return n == 2 }}
	}
	e.start()
	e.card("feature-a")
	e.svc.cfg.Door = &listHook{Door: e.door, hook: func() {
		other, done, err := e.svc.begin(context.Background())
		if err != nil {
			t.Error(err)
			return
		}
		defer done()
		if err := e.svc.jfix(other, ownerjournal.ErrUncertain); !errors.Is(err, ErrJournalReopened) {
			t.Errorf("the other call: %v", err)
		}
	}}
	if _, err := e.svc.List(context.Background()); !errors.Is(err, ErrJournalReopened) {
		t.Fatalf("List returned %v, want ErrJournalReopened", err)
	}
	mu.Lock()
	n := opened
	mu.Unlock()
	// The first opening, the other call's, and the listing's own.
	if n != 3 {
		t.Errorf("the journal was opened %d times, want 3: the listing did not reopen the store it saw fail", n)
	}
}

// faultyHandles makes one of the four writes through a handle answer
// ErrUncertain, once. With landed set the write is made first, as a commit
// that was made and not acknowledged is; without it, nothing is written.
type faultyHandles struct {
	op     string
	landed bool
	hits   int
}

func (h *faultyHandles) fail(op string) bool {
	if h.op != op || h.hits > 0 {
		return false
	}
	h.hits++
	return true
}

func (h *faultyHandles) MarkDelivered(d *ownerjournal.Decision, at time.Time) (*ownerjournal.Delivery, error) {
	if h.fail("MarkDelivered") {
		if h.landed {
			_, _ = d.MarkDelivered(at)
		}
		return nil, ownerjournal.ErrUncertain
	}
	return d.MarkDelivered(at)
}

func (h *faultyHandles) MarkRefusedByDoor(d *ownerjournal.Decision, at time.Time, code record.Code) error {
	if h.fail("MarkRefusedByDoor") {
		if h.landed {
			_ = d.MarkRefusedByDoor(at, code)
		}
		return ownerjournal.ErrUncertain
	}
	return d.MarkRefusedByDoor(at, code)
}

func (h *faultyHandles) AppendUse(u *ownerjournal.Delivery, at time.Time, code record.Code) error {
	if h.fail("AppendUse") {
		if h.landed {
			_ = u.AppendUse(at, code)
		}
		return ownerjournal.ErrUncertain
	}
	return u.AppendUse(at, code)
}

func (h *faultyHandles) AppendOutcome(u *ownerjournal.Delivery, at time.Time, state record.State, line string) error {
	if h.fail("AppendOutcome") {
		if h.landed {
			_ = u.AppendOutcome(at, state, line)
		}
		return ownerjournal.ErrUncertain
	}
	return u.AppendOutcome(at, state, line)
}

// Each write made through a handle can be uncertain: the service reopens
// the journal and takes up what the file says, says so to the caller and
// does not tell it as a refusal, and nothing is lost or done twice.
func TestAnUncertainWriteThroughAHandleIsRecovered(t *testing.T) {
	for _, c := range []struct {
		op string
		// state is where the approval ends, and applies how often the ref
		// was updated: a stop after the door used the approval and before
		// the update leaves the mirror at the old value (OC9).
		state   record.State
		applies int
	}{
		{"MarkDelivered", record.StateApplied, 1},
		{"AppendUse", record.StateNotApplied, 0},
		{"AppendOutcome", record.StateApplied, 1},
	} {
		for _, landed := range []bool{false, true} {
			name := c.op + " not written"
			if landed {
				name = c.op + " written"
			}
			t.Run(name, func(t *testing.T) {
				e := newEnv(t)
				e.start()
				p := e.card("feature-a")
				e.svc.cfg.Handles = &faultyHandles{op: c.op, landed: landed}
				_, err := e.decide(p, true)
				if !errors.Is(err, ErrJournalReopened) {
					t.Fatalf("Decide returned %v, want ErrJournalReopened", err)
				}
				if code, ok := record.CodeOf(err); ok {
					t.Fatalf("the uncertain write was reported as the refusal %s", code)
				}
				if got := e.state(p.ID); got != c.state {
					t.Errorf("the door's state is %s, want %s", got, c.state)
				}
				if calls, _ := e.intake.appliesSeen(); calls != c.applies {
					t.Errorf("applied %d times, want %d", calls, c.applies)
				}
				for kind, want := range map[ownerjournal.Kind]int{
					ownerjournal.KindDecision: 1, ownerjournal.KindDelivered: 1, ownerjournal.KindOutcome: 1,
				} {
					if n := len(e.entries(kind)); n != want {
						t.Errorf("%d %s entries, want %d", n, kind, want)
					}
				}
				var consumed int
				for _, en := range e.entries(ownerjournal.KindUse) {
					if string(en.Body) == `{"code":""}` {
						consumed++
					}
				}
				if consumed != 1 {
					t.Errorf("%d use entries that say the door consumed the approval, want 1", consumed)
				}
				if open, err := e.svc.journal().OpenUses(); err != nil || len(open) != 0 {
					t.Errorf("open uses: %d, %v", len(open), err)
				}
				if n := e.merger.pinCount(); n != 0 {
					t.Errorf("%d pins left", n)
				}
				if got := e.mail.repliesFor(p.RequestID); len(got) != 1 || got[0].State != c.state {
					t.Errorf("the lead was told %+v, want one reply with %s", got, c.state)
				}
			})
		}
	}
}

// A decision the door refuses, journaled as refused by the door through a
// handle that answers ErrUncertain, is recovered the same way.
func TestAnUncertainRefusalMarkIsRecovered(t *testing.T) {
	for _, landed := range []bool{false, true} {
		name := "not written"
		if landed {
			name = "written"
		}
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.start()
			p := e.card("feature-a")
			// The door is out of reach, so the decision is journaled and not
			// handed over; by the time it is, its own time has run out.
			e.door.failDecide = errors.New("connection refused")
			if _, err := e.decide(p, true); !errors.Is(err, ErrDoorUnavailable) {
				t.Fatalf("Decide returned %v", err)
			}
			e.door.failDecide = nil
			e.advance(31 * time.Minute)
			e.svc.cfg.Handles = &faultyHandles{op: "MarkRefusedByDoor", landed: landed}
			if err := e.svc.Tick(context.Background()); !errors.Is(err, ErrJournalReopened) {
				t.Fatalf("Tick returned %v, want ErrJournalReopened", err)
			}
			delivered := e.entries(ownerjournal.KindDelivered)
			if len(delivered) != 1 || string(delivered[0].Body) != `{"code":"approval-expired"}` {
				t.Fatalf("the delivered entries: %+v", delivered)
			}
			if undelivered, err := e.svc.journal().Undelivered(); err != nil || len(undelivered) != 0 {
				t.Errorf("undelivered: %d, %v", len(undelivered), err)
			}
			if got := e.state(p.ID); got != record.StatePending {
				t.Errorf("the door's state is %s, want the card still pending", got)
			}
		})
	}
}

// A decision the door holds with other bytes than the journal's is looked at
// before any use is tried: the approval is blocked and alerted, and the use
// does not run in the same pass that finds the difference.
func TestADoorDecisionWithOtherBytesIsFoundBeforeAnyUse(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.stop()
	ctx := context.Background()
	// The door holds one decision, made at it directly; the journal holds
	// another for the same card, delivered.
	if _, err := e.stand.Decide(ctx, forgedFor(t, e, p)); err != nil {
		t.Fatal(err)
	}
	e.advance(time.Second)
	other := forgedFor(t, e, p)
	st, err := ownerjournal.Open(e.path)
	if err != nil {
		t.Fatal(err)
	}
	d, err := st.AppendDecision(other.Bytes, ownerjournal.Seen{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.MarkDelivered(e.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	e.start()
	if _, uses, _ := e.door.counts(); uses != 0 {
		t.Errorf("the door's use ran %d times in the pass that found the difference", uses)
	}
	if e.state(p.ID) != record.StateApproved {
		t.Errorf("the door's state is %s", e.state(p.ID))
	}
	if calls, _ := e.intake.appliesSeen(); calls != 0 {
		t.Errorf("applied %d times", calls)
	}
	alerts := e.entries(ownerjournal.KindAlert)
	if len(alerts) != 1 || alerts[0].PendingID != p.ID {
		t.Errorf("the alerts are %v", alerts)
	}
}

// The service never settles an unknown outcome by itself. A stop at the
// point where the door has the outcome as unknown and the journal does not
// leaves it for the owner at the restart, whatever the mirror shows then.
func TestARestartAfterAStopAtSettledLeavesAnUnknownOutcomeForTheOwner(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.intake.applyErr = errors.New("update-ref was killed")
	e.fault = "settled"
	if _, err := e.decide(p, true); !errors.Is(err, errInjected) {
		t.Fatalf("Decide returned %v", err)
	}
	if e.state(p.ID) != record.StateUnknown {
		t.Fatalf("the door's state is %s", e.state(p.ID))
	}
	if n := len(e.entries(ownerjournal.KindOutcome)); n != 0 {
		t.Fatalf("%d outcome entries before the restart", n)
	}
	// The mirror now shows the ref at its new value.
	e.intake.mu.Lock()
	e.intake.refs[targetRef] = mergeID
	e.intake.mu.Unlock()
	_, _, settles := e.door.counts()
	e.restart()
	if got := e.state(p.ID); got != record.StateUnknown {
		t.Fatalf("the service settled an unknown outcome by itself: %s", got)
	}
	if _, _, after := e.door.counts(); after != settles {
		t.Errorf("the restart settled at the door: %d settles, want %d", after, settles)
	}
	outcomes := e.entries(ownerjournal.KindOutcome)
	if len(outcomes) != 1 || !strings.Contains(string(outcomes[0].Body), `"state":"unknown"`) {
		t.Fatalf("the outcome entries: %+v", outcomes)
	}
	if unknown, err := e.svc.journal().UnknownOutcomes(); err != nil || len(unknown) != 1 {
		t.Errorf("the unknown outcomes: %d, %v", len(unknown), err)
	}
	if got := e.mail.all(); len(got) != 0 {
		t.Errorf("the lead was told of an outcome that is not final: %v", got)
	}
	if n := e.merger.pinCount(); n != 1 {
		t.Errorf("%d pins, want the pin kept while the outcome is unknown", n)
	}
	// Only the owner's word moves it.
	e.beat()
	if res, err := e.svc.Dispose(context.Background(), src, p.ID, record.StateApplied); err != nil || res.State != record.StateApplied {
		t.Fatalf("%+v %v", res, err)
	}
}

// A use the door refuses while the approval stays open is tried again by
// each tick, and journaled once for each code it is refused with, not once
// for each try.
func TestARefusedUseIsJournaledOncePerCode(t *testing.T) {
	e := newEnv(t)
	e.start()
	p := e.card("feature-a")
	e.door.refuseUse = &record.Error{Code: record.CodeEvidenceChanged, Detail: "the evidence changed"}
	_, err := e.decide(p, true)
	wantCode(t, err, record.CodeEvidenceChanged)
	coded := func() []string {
		var out []string
		for _, en := range e.entries(ownerjournal.KindUse) {
			out = append(out, string(en.Body))
		}
		return out
	}
	for i := 0; i < 3; i++ {
		e.advance(time.Minute)
		wantCode(t, e.svc.Tick(context.Background()), record.CodeEvidenceChanged)
	}
	if got := coded(); len(got) != 1 || got[0] != `{"code":"evidence-changed"}` {
		t.Fatalf("the use entries after three ticks: %v", got)
	}
	// Another code is another entry, once.
	e.door.refuseUse = &record.Error{Code: record.CodeReviewChanged, Detail: "the review changed"}
	for i := 0; i < 2; i++ {
		wantCode(t, e.svc.Tick(context.Background()), record.CodeReviewChanged)
	}
	if got := coded(); len(got) != 2 || got[1] != `{"code":"review-changed"}` {
		t.Fatalf("the use entries after another code: %v", got)
	}
	// Once the door takes the use, the approval goes through.
	e.door.refuseUse = nil
	if err := e.svc.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.state(p.ID) != record.StateApplied {
		t.Errorf("the door's state is %s", e.state(p.ID))
	}
	if got := coded(); len(got) != 3 || got[2] != `{"code":""}` {
		t.Errorf("the use entries at the end: %v", got)
	}
}
