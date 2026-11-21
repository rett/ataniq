package leadctl

import (
	"strings"
	"testing"
)

// sendOne sends one message from the row of beta, which answers with r. The
// list that the view reads after the answer says that the epoch's message is
// spent, as the daemon's does once a request was written to the holder.
func sendOne(t *testing.T, r Receipt) *rig {
	t.Helper()
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) { return r, nil }
	g := newRig(t, b, 150, 30)
	g.key("j")
	g.key("m")
	g.typ("hi")
	b.listing.Leads[1].MessageSpent = true
	g.key("enter")
	return g
}

func TestTheMsgColumnAndTheReceiptLineSayTheSameOfAMessage(t *testing.T) {
	for _, c := range []struct {
		name string
		r    Receipt
		msg  string
		line string
	}{
		{"submitted", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeSubmitted, Written: WrittenWhole},
			"sent ", "the message was submitted"},
		{"not ready", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing},
			"spent-not-delivered", "NOT delivered: nothing was written, the lead's screen is not idle"},
		{"blocked", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow, Reason: ReasonBlocked, Written: WrittenNothing},
			"spent-not-delivered", "NOT delivered: nothing was written, the lead's screen is blocked"},
		{"untested", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeRefused, Reason: ReasonUntested, Written: WrittenNothing},
			"spent-not-delivered", "NOT delivered: nothing was written"},
		{"text", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeRefused, Reason: ReasonText, Written: WrittenNothing},
			"spent-not-delivered", "NOT delivered: nothing was written, the holder refused the text"},
		{"not submitted", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeUncertain, Reason: ReasonNotSubmitted, Written: WrittenPartial},
			"sent-unsure", "written but not submitted"},
		{"died", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeUncertain, Reason: ReasonDied, Written: WrittenUnknown},
			"sent-unsure", "result unknown"},
		{"outcome alone", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow},
			"spent-not-delivered", "NOT delivered: nothing was written, the lead could not take it now"},
		{"epoch not in the receipt", Receipt{Op: OpInject, Status: Done, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing},
			"spent-not-delivered", "NOT delivered"},
		{"unknown result", Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: "sent!!"},
			"sent ", "the result is not known to this console"},
	} {
		t.Run(c.name, func(t *testing.T) {
			g := sendOne(t, c.r)
			rows := g.rows()
			if !strings.Contains(rows[5], c.msg) {
				t.Fatalf("the row of beta does not show %q:\n%s", c.msg, g.screen())
			}
			g.seen("message: accepted. " + c.line)
		})
	}
}

func TestAFreeMessageIsFreeAndASpentOneIsSentWhenNothingIsKnownOfIt(t *testing.T) {
	b := twoLeads(t)
	b.listing.Leads[0].Inject = false
	b.listing.Leads[1].Epoch = 3
	r := newRig(t, b, 150, 30)
	if rows := r.rows(); !strings.Contains(rows[4], "off") || !strings.Contains(rows[5], "free") {
		t.Fatalf("\n%s", r.screen())
	}
	// The daemon says it is spent, this view sent nothing and the adapter
	// gave no result: it was sent.
	b.listing.Leads[1].MessageSpent = true
	r.key("r")
	if rows := r.rows(); !strings.Contains(rows[5], "sent") || strings.Contains(rows[5], "not-delivered") {
		t.Fatalf("\n%s", r.screen())
	}
}

func TestTheAdaptersResultForTheMessageIsDrawnAndWinsOverWhatTheViewKept(t *testing.T) {
	b := twoLeads(t)
	b.listing.Leads[1].MessageSpent = true
	b.listing.Leads[1].MessageResult = Delivery{OutcomeNotNow, ReasonBlocked, WrittenNothing}
	r := newRig(t, b, 150, 30)
	if rows := r.rows(); !strings.Contains(rows[5], "spent-not-delivered") {
		t.Fatalf("\n%s", r.screen())
	}
	// A result in a combination the holder does not send is not believed:
	// the row says no more than the daemon's flag does.
	b.listing.Leads[1].MessageResult = Delivery{OutcomeNotNow, "x", WrittenWhole}
	r.key("r")
	if rows := r.rows(); strings.Contains(rows[5], "not-delivered") || !strings.Contains(rows[5], "sent") {
		t.Fatalf("\n%s", r.screen())
	}
}

func TestTheViewsMemoryOfAMessageEndsWithItsEpoch(t *testing.T) {
	g := sendOne(t, Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing})
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
	g = sendOne(t, Receipt{Op: OpInject, Status: Done, Epoch: 3, Earlier: true, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing})
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
