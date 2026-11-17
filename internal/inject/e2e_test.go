package inject

import (
	"context"
	"strings"
	"testing"
	"time"

	"ataniq/internal/inject/command"
)

// fenceResult is the mapping the daemon makes from the fence's Result to the
// command machine's FenceResult. The spellings are the same, and a test
// (TestSpellingsMatchCommand) fails if they drift apart.
func fenceResult(r Result) command.FenceResult {
	return command.FenceResult{
		Written:  command.Written(r.Written),
		Outcome:  command.FenceOutcome(r.Outcome),
		Readings: r.Readings(),
	}
}

// journal keeps records in memory.
type journal struct{ recs []command.Record }

func (j *journal) Append(r command.Record) error {
	j.recs = append(j.recs, r)
	return nil
}

// daemon is the daemon's part, in the test: the queue in the command machine,
// the fence run against a fake session, and the result recorded.
type daemon struct {
	t   *testing.T
	m   *command.Machine
	j   *journal
	now time.Time
}

var lead = command.LeadTarget("proj")

func newDaemon(t *testing.T) *daemon {
	t.Helper()
	l := command.DefaultLimits()
	l.QueueTimeLimit = 10 * time.Minute
	j := &journal{}
	m, err := command.New(l, j)
	if err != nil {
		t.Fatal(err)
	}
	return &daemon{t: t, m: m, j: j, now: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
}

func (d *daemon) tick(by time.Duration) { d.now = d.now.Add(by) }

func (d *daemon) submit(id, body string) {
	d.t.Helper()
	if _, err := d.m.Submit(command.Message{ID: id, From: command.Daemon(), To: lead, Kind: "note", Body: body}, d.now); err != nil {
		d.t.Fatalf("Submit %s: %v", id, err)
	}
}

func (d *daemon) begin() command.Dispatch {
	d.t.Helper()
	disp, err := d.m.BeginDispatch(lead, d.now)
	if err != nil {
		d.t.Fatalf("BeginDispatch: %v", err)
	}
	return disp
}

// dispatch runs one command through the fence on the session and records the
// result, as the daemon does.
func (d *daemon) dispatch(f *fake) (command.Dispatch, Result, command.FenceUpdate) {
	d.t.Helper()
	f.nextFence()
	disp := d.begin()
	m := Message{Text: disp.Text, Header: disp.Header}
	res := runFence(d.t, f, m)
	checkInvariants(d.t, f, m, res, invariants{deadline: f.deadline})
	up, err := d.m.RecordFence(disp.ID, fenceResult(res), d.now)
	if err != nil {
		d.t.Fatalf("RecordFence: %v", err)
	}
	return disp, res, up
}

func (d *daemon) state(id string) command.State {
	d.t.Helper()
	v, ok := d.m.Command(id)
	if !ok {
		d.t.Fatalf("no command %q", id)
	}
	return v.State
}

func wantRefusal(t *testing.T, err error, reason command.Reason) {
	t.Helper()
	ref, ok := err.(*command.Refusal)
	if !ok || ref.Reason != reason {
		t.Fatalf("error %v, want a refusal %q", err, reason)
	}
}

// I-13: two messages are queued. They are delivered in order, the second only
// after the first is confirmed delivered.
func TestTwoMessagesInOrder(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		d := newDaemon(t)
		f := newFake(t, rt)
		d.submit("first", "the first message")
		d.submit("second", "the second message")

		disp1, res1, up1 := d.dispatch(f)
		wantResult(t, res1, WrittenPasteAndEnter, OutcomeSubmitted)
		if disp1.ID != "first" || up1.State != command.StateDispatching || up1.Reason != command.ReasonEnterWritten {
			t.Fatalf("first: %+v", up1)
		}

		// The second does not go before the first is confirmed: the daemon
		// cannot even begin it, so no byte is written.
		writes := f.totalWrites()
		_, err := d.m.BeginDispatch(lead, d.now)
		wantRefusal(t, err, command.ReasonAlreadyDispatching)
		if f.totalWrites() != writes {
			t.Fatalf("something was written before the first was confirmed")
		}

		// The agent works on the first; a fence for the second finds it busy
		// and the command stays queued, nothing written.
		d.tick(time.Second)
		if err := d.m.ConfirmDelivery("first", "conversation file, offset 1234", d.now); err != nil {
			t.Fatal(err)
		}
		d.tick(time.Second)
		before := f.totalWrites()
		_, res2, up2 := d.dispatch(f)
		wantResult(t, res2, WrittenNothing, OutcomeNotIdle)
		if up2.State != command.StateQueued || up2.Reason != command.ReasonNotIdle || f.totalWrites() != before {
			t.Fatalf("second while the agent works: %+v", up2)
		}

		// The agent finishes its turn; the second is delivered.
		f.becomeIdle()
		d.tick(time.Second)
		disp3, res3, up3 := d.dispatch(f)
		wantResult(t, res3, WrittenPasteAndEnter, OutcomeSubmitted)
		if disp3.ID != "second" || up3.State != command.StateDispatching {
			t.Fatalf("second: %+v", up3)
		}
		if err := d.m.ConfirmDelivery("second", "conversation file, offset 2345", d.now); err != nil {
			t.Fatal(err)
		}
		want := []string{disp1.Text, disp3.Text}
		if strings.Join(f.submissions, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("the agent took %q, want %q in that order", f.submissions, want)
		}
		if d.state("first") != command.StateDelivered || d.state("second") != command.StateDelivered {
			t.Errorf("states %s %s", d.state("first"), d.state("second"))
		}
	})
}

// I-7: a paste without Enter freezes the queue, and nothing is retried.
func TestPasteOnlyFreezesTheQueue(t *testing.T) {
	each(t, func(t *testing.T, rt string) {
		d := newDaemon(t)
		f := newFake(t, rt).at(stepSettle2, evOpenDialog)
		d.submit("m1", "a message")
		d.submit("m2", "another message")

		disp, res, up := d.dispatch(f)
		wantResult(t, res, WrittenPaste, OutcomeNotSubmitted)
		if up.State != command.StateUncertain || up.Reason != command.ReasonNotSubmitted {
			t.Fatalf("update %+v", up)
		}
		writes := f.totalWrites()

		// Time passes, ticks run, the daemon tries again to dispatch: the
		// queue is frozen and no byte is written.
		for i := 0; i < 5; i++ {
			d.tick(time.Minute)
			if _, err := d.m.Tick(d.now); err != nil {
				t.Fatal(err)
			}
			_, err := d.m.BeginDispatch(lead, d.now)
			wantRefusal(t, err, command.ReasonQueueFrozen)
		}
		if f.totalWrites() != writes || f.approvals != 0 || len(f.submissions) != 0 {
			t.Errorf("writes %d (was %d), approvals %d, submissions %d", f.totalWrites(), writes, f.approvals, len(f.submissions))
		}
		if d.state(disp.ID) != command.StateUncertain || d.state("m2") != command.StateQueued {
			t.Errorf("states %s %s", d.state(disp.ID), d.state("m2"))
		}
		v := d.m.View().Targets[0]
		if !v.Frozen || v.FrozenBy != "m1" {
			t.Errorf("view %+v", v)
		}
	})
}

// I-7, the other form: Enter was written and the conversation file never shows
// the message. After 30 s the command is uncertain, the queue is frozen, and
// the fence is not run again.
func TestSubmittedNotConfirmedFreezes(t *testing.T) {
	d := newDaemon(t)
	f := newFake(t, "claude")
	d.submit("m1", "a message")
	d.submit("m2", "another message")
	_, res, up := d.dispatch(f)
	wantResult(t, res, WrittenPasteAndEnter, OutcomeSubmitted)
	if up.State != command.StateDispatching {
		t.Fatalf("update %+v", up)
	}
	d.tick(29 * time.Second)
	if recs, _ := d.m.Tick(d.now); len(recs) != 0 {
		t.Fatalf("records before 30 s: %+v", recs)
	}
	d.tick(time.Second)
	if _, err := d.m.Tick(d.now); err != nil {
		t.Fatal(err)
	}
	v, _ := d.m.Command("m1")
	if v.State != command.StateUncertain || v.Reason != command.ReasonSubmittedNotConfirmed {
		t.Fatalf("m1 is %s (%s)", v.State, v.Reason)
	}
	writes := f.totalWrites()
	_, err := d.m.BeginDispatch(lead, d.now)
	wantRefusal(t, err, command.ReasonQueueFrozen)
	if f.totalWrites() != writes {
		t.Errorf("a write was made after the freeze")
	}
}

// I-8: the operator resolves an uncertain command each of the three ways. A
// resend is a new command with a new identity, and it is delivered once.
func TestOperatorResolutions(t *testing.T) {
	// A paste that a dialog swallowed leaves the composer empty: after the
	// owner closes the dialog, a resend can go.
	t.Run("send again", func(t *testing.T) {
		each(t, func(t *testing.T, rt string) {
			d := newDaemon(t)
			f := newFake(t, rt).at(stepFrozen1, evOpenDialog)
			d.submit("m1", "the message")
			disp, res, _ := d.dispatch(f)
			wantResult(t, res, WrittenPaste, OutcomeNotSubmitted)
			if f.swallowed != 1 || len(f.submissions) != 0 {
				t.Fatalf("swallowed %d submissions %d", f.swallowed, len(f.submissions))
			}

			f.closeDialog() // the owner answers the dialog and it is gone
			d.tick(time.Minute)
			rs, err := d.m.Resolve(command.Resolution{ID: "m1", Decision: command.DecisionResend, Operator: "operator", NewID: "m1-again"}, d.now)
			if err != nil {
				t.Fatal(err)
			}
			if rs.NewID != "m1-again" || d.state("m1") != command.StateResolved {
				t.Fatalf("resolved %+v, old is %s", rs, d.state("m1"))
			}

			disp2, res2, up2 := d.dispatch(f)
			wantResult(t, res2, WrittenPasteAndEnter, OutcomeSubmitted)
			if disp2.ID != "m1-again" || up2.State != command.StateDispatching {
				t.Fatalf("resend: %+v %+v", disp2, up2)
			}
			if len(f.submissions) != 1 || f.submissions[0] != disp2.Text || strings.Contains(f.submissions[0], "id=m1 ") {
				t.Errorf("submissions %q", f.submissions)
			}
			if disp.Text == disp2.Text {
				t.Errorf("the resend carries the old identity")
			}
			// Delivered once: the fence is not run again for it.
			if err := d.m.ConfirmDelivery("m1-again", "conversation file, offset 99", d.now); err != nil {
				t.Fatal(err)
			}
			if _, err := d.m.BeginDispatch(lead, d.now); err == nil {
				t.Errorf("a command was dispatched again")
			}
			if len(f.submissions) != 1 {
				t.Errorf("submissions %d", len(f.submissions))
			}
		})
	})
	for _, dec := range []command.Decision{command.DecisionDelivered, command.DecisionDrop} {
		t.Run(string(dec), func(t *testing.T) {
			d := newDaemon(t)
			f := newFake(t, "codex").at(stepSettle2, evOpenDialog)
			d.submit("m1", "the message")
			d.submit("m2", "the next message")
			d.dispatch(f)
			if _, err := d.m.Resolve(command.Resolution{ID: "m1", Decision: dec, Operator: "operator"}, d.now); err != nil {
				t.Fatal(err)
			}
			writes := f.totalWrites()
			// The queue is free again: the second is dispatchable (here the
			// dialog is still open, so the fence blocks and writes nothing).
			_, res, up := d.dispatch(f)
			wantResult(t, res, WrittenNothing, OutcomeBlocked)
			if up.Notice != command.NoticeBlocked || f.totalWrites() != writes {
				t.Errorf("update %+v", up)
			}
		})
	}
}

// A blocked lead keeps its command queued, and the owner is told once.
func TestBlockedKeepsQueued(t *testing.T) {
	d := newDaemon(t)
	f := newFake(t, "claude").setMode(mDialog)
	d.submit("m1", "the message")
	_, res, up := d.dispatch(f)
	wantResult(t, res, WrittenNothing, OutcomeBlocked)
	if up.State != command.StateQueued || up.Notice != command.NoticeBlocked {
		t.Fatalf("update %+v", up)
	}
	_, _, up = d.dispatch(f)
	if up.Notice != "" {
		t.Errorf("the owner was told twice: %+v", up)
	}
	// The queue time limit ends it as not idle: the command machine does it
	// on a tick, not the fence.
	d.tick(11 * time.Minute)
	if _, err := d.m.Tick(d.now); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.m.Command("m1"); v.State != command.StateRefused || v.Reason != command.ReasonNotIdle {
		t.Errorf("m1 is %s (%s)", v.State, v.Reason)
	}
	if f.approvals != 0 || f.totalWrites() != 0 {
		t.Errorf("approvals %d writes %d", f.approvals, f.totalWrites())
	}
}

// A holder that dies during the fence leaves the command uncertain, and it is
// not sent again by itself.
func TestDeathLeavesUncertain(t *testing.T) {
	d := newDaemon(t)
	f := newFake(t, "codex").at(stepSettle2, evDie)
	d.submit("m1", "the message")
	_, res, up := d.dispatch(f)
	wantResult(t, res, WrittenUnknown, OutcomeDied)
	if up.State != command.StateUncertain || up.Reason != command.ReasonHolderDied {
		t.Fatalf("update %+v", up)
	}
	// A restart rebuilds the machine from the journal: the command is still
	// uncertain and the queue is frozen.
	l := command.DefaultLimits()
	l.QueueTimeLimit = 10 * time.Minute
	j2 := &journal{}
	m2, err := command.Restore(l, j2, d.j.recs, d.now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m2.BeginDispatch(lead, d.now.Add(time.Hour)); err == nil {
		t.Errorf("a restarted machine dispatched the frozen queue")
	}
	if v, _ := m2.Command("m1"); v.State != command.StateUncertain {
		t.Errorf("after restart m1 is %s", v.State)
	}
}

// A refused text never reaches the session and the command is refused.
func TestRefusedTextIsRefused(t *testing.T) {
	d := newDaemon(t)
	f := newFake(t, "claude")
	d.submit("m1", "the message")
	disp := d.begin()
	// A text that admission would have refused, tampered after the fact.
	bad := Message{Text: disp.Text + "\x1b[201~", Header: disp.Header}
	res := Deliver(context.Background(), f, bad, Options{ExpectedMode: "manual"})
	wantResult(t, res, WrittenNothing, OutcomeRefusedText)
	up, err := d.m.RecordFence(disp.ID, fenceResult(res), d.now)
	if err != nil {
		t.Fatal(err)
	}
	if up.State != command.StateRefused || up.Reason != command.ReasonTextRefused || f.touched() {
		t.Errorf("update %+v, touched %t", up, f.touched())
	}
}
