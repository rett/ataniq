package leadctl

import (
	"strings"
	"testing"
)

// sendOne sends one message from the row of beta, which answers with r. The
// list that the view reads after the answer says whether the epoch's message is
// spent, as the daemon's does: spent when the message may have reached the lead,
// and free after one that was not delivered.
func sendOne(t *testing.T, r Receipt, spent bool) *rig {
	t.Helper()
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) { return r, nil }
	g := newRig(t, b, 150, 30)
	g.key("j")
	g.key("m")
	g.typ("hi")
	b.listing.Leads[1].MessageSpent = spent
	g.key("enter")
	return g
}

func TestTheMsgColumnAndTheReceiptLineSayTheSameOfAMessage(t *testing.T) {
	const notSpent = "The epoch's message is free"
	for _, c := range []struct {
		name  string
		r     Receipt
		spent bool
		msg   string
		line  string
		tail  string
	}{
		{"submitted", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeSubmitted, Written: WrittenWhole}, true,
			"sent ", "the message was submitted", ""},
		{"not ready", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}, false,
			"free (not delivered)", "NOT delivered: nothing was written, the lead's screen is not idle", notSpent},
		{"blocked", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow, Reason: ReasonBlocked, Written: WrittenNothing}, false,
			"free (not delivered)", "NOT delivered: nothing was written, the lead's screen is blocked", notSpent},
		{"untested", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeRefused, Reason: ReasonUntested, Written: WrittenNothing}, false,
			"free (not delivered)", "NOT delivered: nothing was written", notSpent},
		{"text", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeRefused, Reason: ReasonText, Written: WrittenNothing}, false,
			"free (not delivered)", "NOT delivered: nothing was written, the holder refused the text", notSpent},
		{"not submitted", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeUncertain, Reason: ReasonNotSubmitted, Written: WrittenPartial}, true,
			"sent-unsure", "written but not submitted", ""},
		{"died", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeUncertain, Reason: ReasonDied, Written: WrittenUnknown}, true,
			"sent-unsure", "result unknown", ""},
		{"died, nothing written", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeUncertain, Reason: ReasonDied, Written: WrittenNothing}, true,
			"sent-unsure", "result unknown", ""},
		{"outcome alone", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow}, false,
			"free (not delivered)", "NOT delivered: nothing was written, the lead could not take it now", notSpent},
		{"epoch not in the receipt", Receipt{Op: OpInject, Status: Done, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}, false,
			"free (not delivered)", "NOT delivered", notSpent},
		// A result the console has no words for is not a delivery: the message
		// is spent, and the row does not say it was sent as if all were well.
		{"unknown result", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: "sent!!"}, true,
			"sent-unsure", "the result is not known to this console", ""},
		// The daemon holds the epoch spent after a message that was not
		// delivered when it could not write the result down: the row says so,
		// and so does the receipt line, which does not say the message is
		// free (see TestTheReceiptLineDoesNotCallTheEpochFreeWhileTheDaemonHoldsItSpent).
		{"not delivered and held spent", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}, true,
			"spent, not delivered", "NOT delivered", "The daemon holds this epoch's message spent all the same"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := sendOne(t, c.r, c.spent)
			rows := g.rows()
			if !strings.Contains(rows[5], c.msg) || strings.Contains(rows[5], "spent-not-delivered") {
				t.Fatalf("the row of beta does not show %q:\n%s", c.msg, g.screen())
			}
			g.seen("message: accepted. " + c.line)
			if c.tail != "" {
				g.seen(c.tail)
			}
		})
	}
}

// The tail of the line of a message that was not delivered comes from the row
// of the list that was read after the answer: free when the row says so, the
// daemon's hold when the row says spent, and neither claim until the list has
// been read (the row of the list that was drawn before the answer says nothing
// of this message). A row at another epoch says nothing either.
func TestTheReceiptLineDoesNotCallTheEpochFreeWhileTheDaemonHoldsItSpent(t *testing.T) {
	notNow := Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}
	for _, c := range []struct {
		name  string
		spent bool
		epoch uint64
		want  string
		not   string
	}{
		{"row free", false, 3, "The epoch's message is free", "spent"},
		{"row spent", true, 3, "holds this epoch's message spent", "is free"},
		{"row at another epoch", false, 4, "free unless the list says it is spent", "The epoch's message is free."},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := twoLeads(t)
			b.answer = func(call) (Receipt, error) { return notNow, nil }
			g := newRig(t, b, 150, 30)
			g.key("j")
			g.key("m")
			g.typ("hi")
			b.listing.Leads[1].MessageSpent = c.spent
			b.listing.Leads[1].Epoch = c.epoch
			// The answer is in and the list is not read again yet: the old row
			// is not asked.
			cmd, _ := g.m.Update(keyMsg("enter"))
			b.inCmd = true
			answer := cmd()
			b.inCmd = false
			cmd, _ = g.m.Update(answer)
			g.seen("NOT delivered: nothing was written", "free unless the list says it is spent")
			g.run(cmd)
			g.seen(c.want)
			g.notSeen(c.not)
		})
	}
}

// After a message that was not delivered the view lets another be sent for the
// same epoch, as often as the daemon says the epoch's message is free, and no
// more once the daemon says it is spent.
func TestAMessageThatWasNotDeliveredMayBeSentAgain(t *testing.T) {
	notNow := Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}
	g := sendOne(t, notNow, false)
	b := g.b
	// A source of randomness that does not repeat, so that each message has
	// an id of its own.
	g.m.random = &countingReader{n: 100}
	for i := 2; i <= 3; i++ {
		g.key("m")
		g.notSeen("a message was already sent in this epoch", "a message for this epoch has no final answer")
		g.typ("again")
		g.key("enter")
		if n := len(b.callsOf("inject")); n != i {
			t.Fatalf("%d messages were sent, want %d\n%s", n, i, g.screen())
		}
		if !strings.Contains(g.rows()[5], "free (not delivered)") {
			t.Fatalf("\n%s", g.screen())
		}
	}
	ids := map[string]bool{}
	for _, c := range b.callsOf("inject") {
		ids[c.messageID] = true
	}
	if len(ids) != 3 {
		t.Fatalf("the messages share an id: %v", b.callsOf("inject"))
	}
	// The third one is delivered: the epoch's message is spent and the view
	// says so and sends no more.
	b.answer = func(call) (Receipt, error) {
		return Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeSubmitted, Written: WrittenWhole}, nil
	}
	g.key("m")
	g.typ("last")
	b.listing.Leads[1].MessageSpent = true
	g.key("enter")
	if !strings.Contains(g.rows()[5], "sent ") {
		t.Fatalf("\n%s", g.screen())
	}
	g.key("m")
	g.seen("a message was already sent in this epoch")
	if n := len(b.callsOf("inject")); n != 4 {
		t.Fatalf("%d messages were sent", n)
	}
}

// What the view kept of an earlier message of the epoch is not read as the
// answer to a later message whose answer was lost: the row then says that the
// message was sent and that it is not known whether the lead has it.
func TestALostAnswerAfterANotDeliveredMessageIsNotReadAsThatMessage(t *testing.T) {
	notNow := Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}
	g := sendOne(t, notNow, false)
	g.b.answer = func(call) (Receipt, error) { return Receipt{}, errLost }
	g.key("m")
	g.typ("again")
	g.b.listing.Leads[1].MessageSpent = true
	g.key("enter")
	rows := g.rows()
	if !strings.Contains(rows[5], "sent-unsure") || strings.Contains(rows[5], "not delivered") {
		t.Fatalf("\n%s", g.screen())
	}
}

func TestAFreeMessageIsFreeAndASpentOneIsSentUnsureWhenNothingIsKnownOfIt(t *testing.T) {
	b := twoLeads(t)
	b.listing.Leads[0].Inject = false
	b.listing.Leads[1].Epoch = 3
	r := newRig(t, b, 150, 30)
	if rows := r.rows(); !strings.Contains(rows[4], "off") || !strings.Contains(rows[5], "free") {
		t.Fatalf("\n%s", r.screen())
	}
	// The daemon says it is spent, this view sent nothing and the adapter
	// gave no result: the frame was written and nothing says what became of
	// the message.
	b.listing.Leads[1].MessageSpent = true
	r.key("r")
	if rows := r.rows(); !strings.Contains(rows[5], "sent-unsure") || strings.Contains(rows[5], "not delivered") {
		t.Fatalf("\n%s", r.screen())
	}
}

func TestTheAdaptersResultForTheMessageIsDrawnAndWinsOverWhatTheViewKept(t *testing.T) {
	b := twoLeads(t)
	b.listing.Leads[1].MessageResult = Delivery{OutcomeNotNow, ReasonBlocked, WrittenNothing}
	r := newRig(t, b, 150, 30)
	if rows := r.rows(); !strings.Contains(rows[5], "free (not delivered)") {
		t.Fatalf("\n%s", r.screen())
	}
	b.listing.Leads[1].MessageSpent = true
	r.key("r")
	if rows := r.rows(); !strings.Contains(rows[5], "spent, not delivered") {
		t.Fatalf("\n%s", r.screen())
	}
	// A result in a combination the holder does not send is not believed:
	// the row says no more than the daemon's flag does.
	b.listing.Leads[1].MessageResult = Delivery{OutcomeNotNow, "x", WrittenWhole}
	r.key("r")
	if rows := r.rows(); strings.Contains(rows[5], "not delivered") || !strings.Contains(rows[5], "sent-unsure") {
		t.Fatalf("\n%s", r.screen())
	}
}

func TestTheViewsMemoryOfAMessageEndsWithItsEpoch(t *testing.T) {
	g := sendOne(t, Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}, false)
	if len(g.m.delivered) != 1 {
		t.Fatalf("kept %v", g.m.delivered)
	}
	// The lead is stopped and started: epoch 4, a new message.
	g.b.listing.Leads[1].Epoch, g.b.listing.Leads[1].MessageSpent = 4, false
	g.key("r")
	if len(g.m.delivered) != 0 || !strings.Contains(g.rows()[5], "free") {
		t.Fatalf("kept %v\n%s", g.m.delivered, g.screen())
	}
	// An answer to an earlier command settles nothing, so it is not kept.
	g = sendOne(t, Receipt{Op: OpInject, Status: Done, Epoch: 3, Earlier: true, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}, true)
	if len(g.m.delivered) != 0 {
		t.Fatalf("kept %v", g.m.delivered)
	}
}

func TestTheReadyStateAndItsReasonAreSaidInFixedWords(t *testing.T) {
	b := twoLeads(t)
	b.listing.Leads[1].StateReason = "holder-ready"
	r := newRig(t, b, 150, 30)
	r.key("j")
	r.seen("beta: ready (holder-ready): the holder is up and the agent started; its login is not observed")
	if rows := r.rows(); !strings.Contains(rows[5], "ready") {
		t.Fatalf("the table does not show the state:\n%s", r.screen())
	}
	// The line is about the row under the cursor.
	r.key("k")
	r.notSeen("the holder is up and the agent started")
	// A reason this console does not know, or one that is not paired with
	// ready, adds no line, and is never drawn.
	for _, c := range []struct{ state, reason string }{
		{"ready", "a-reason-from-the-future"},
		{"ready", ""},
		{"starting", "holder-ready"},
	} {
		b.listing.Leads[1].State, b.listing.Leads[1].StateReason = c.state, c.reason
		r.key("r")
		r.key("j")
		r.notSeen("the holder is up", "future", "holder-ready")
		r.key("k")
	}
}

func TestTheStateLineYieldsToTheEditorAndToANote(t *testing.T) {
	b := twoLeads(t)
	b.listing.Leads[1].StateReason = "holder-ready"
	r := newRig(t, b, 150, 30)
	r.key("j")
	r.key("m")
	r.notSeen("the holder is up")
	r.key("esc")
	r.seen("the holder is up")
	r.key("x")
	r.notSeen("the holder is up")
	r.seen("Stop beta")
}

func TestStateWordsAreShortASCII(t *testing.T) {
	if _, ok := stateWords[[2]string{"ready", "holder-ready"}]; !ok {
		t.Fatal("no words for ready with holder-ready")
	}
	for k, w := range stateWords {
		if len(w) == 0 || len(w) > 80 {
			t.Errorf("%v: %d bytes", k, len(w))
		}
		for i := 0; i < len(w); i++ {
			if w[i] < 0x20 || w[i] > 0x7e {
				t.Errorf("%v: %q is not printable ASCII", k, w)
				break
			}
		}
		if safeID(k[1], 64) != k[1] {
			t.Errorf("%v: the reason is not a short code", k)
		}
	}
}

// countingReader gives bytes that differ from one read to the next.
type countingReader struct{ n byte }

func (r *countingReader) Read(p []byte) (int, error) {
	for i := range p {
		r.n++
		p[i] = r.n
	}
	return len(p), nil
}

// The words of a message that the holder refused itself (the row carries the
// outcome refused and no other word): not delivered, free when the daemon says
// the message is not spent, and spent and not delivered when it says it is
// (the refusal's line could not be written).
func TestTheMsgColumnSaysAHoldersRefusalWasNotDelivered(t *testing.T) {
	refused := Delivery{Outcome: OutcomeRefused}
	for _, c := range []struct {
		spent bool
		want  string
	}{{false, "free (not delivered)"}, {true, "spent, not delivered"}} {
		if got := msgWord(Lead{Inject: true, State: "ready", Epoch: 3, MessageSpent: c.spent}, refused); got != c.want {
			t.Errorf("spent %v: %q, want %q", c.spent, got, c.want)
		}
	}
}

// Every spent message is "sent-unsure" unless its words say it was submitted:
// words that are not a combination the holder sends, an outcome alone that the
// console does not know, and no words at all (the daemon holds the epoch spent
// and has recorded no result) are all drawn so, and a submitted one is "sent".
func TestASpentMessageIsSentOnlyWhenItsWordsSaySubmitted(t *testing.T) {
	for _, c := range []struct {
		name string
		d    Delivery
		want string
	}{
		{"submitted", Delivery{OutcomeSubmitted, "", WrittenWhole}, "sent"},
		{"submitted, the outcome alone", Delivery{Outcome: OutcomeSubmitted}, "sent"},
		{"uncertain", Delivery{OutcomeUncertain, ReasonDied, WrittenUnknown}, "sent-unsure"},
		{"uncertain, the outcome alone", Delivery{Outcome: OutcomeUncertain}, "sent-unsure"},
		{"an unknown outcome", Delivery{Outcome: "sent!!"}, "sent-unsure"},
		{"not a combination the holder sends", Delivery{OutcomeSubmitted, ReasonDied, WrittenNothing}, "sent-unsure"},
		{"no words", Delivery{}, "sent-unsure"},
	} {
		if got := msgWord(Lead{Inject: true, State: "ready", Epoch: 3, MessageSpent: true}, c.d); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// A lead that is not running has no message to speak of: the epoch's words
// belong to its last epoch and the column draws "-", except "off", which is the
// lead's config and stays.
func TestTheMsgColumnOfALeadThatIsNotRunningIsADash(t *testing.T) {
	words := Delivery{Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}
	for _, c := range []struct {
		name string
		l    Lead
		want string
	}{
		{"stopped with its last epoch", Lead{State: "stopped", Epoch: 3}, "-"},
		{"ended", Lead{State: "ended", Epoch: 3}, "-"},
		{"desired", Lead{State: "desired", Epoch: 1}, "-"},
		{"unknown", Lead{State: "unknown", Epoch: 2}, "-"},
		{"no epoch", Lead{State: "ready"}, "-"},
		{"no state", Lead{Epoch: 3}, "-"},
		{"stopped and spent", Lead{State: "stopped", Epoch: 3, MessageSpent: true}, "-"},
		{"starting", Lead{State: "starting", Epoch: 1}, "free (not delivered)"},
		{"ready", Lead{State: "ready", Epoch: 3}, "free (not delivered)"},
		{"stopping", Lead{State: "stopping", Epoch: 3}, "free (not delivered)"},
	} {
		l := c.l
		l.Inject = true
		if got := msgWord(l, words); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		l.Inject = false
		if got := msgWord(l, words); got != "off" {
			t.Errorf("%s with messages off: %q, want off", c.name, got)
		}
	}
}

func TestTheMsgColumnOfAStoppedLeadIsADashAfterItsMessageWasSent(t *testing.T) {
	g := sendOne(t, Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}, false)
	if !strings.Contains(g.rows()[5], "free (not delivered)") {
		t.Fatalf("the running lead:\n%s", g.screen())
	}
	// The lead stops; the list still carries the last epoch.
	g.m.leads[1].State = "stopped"
	if row := g.rows()[5]; strings.Contains(row, "free") || strings.Contains(row, "not delivered") {
		t.Fatalf("a stopped lead keeps the words of its last epoch:\n%s", g.screen())
	}
}
