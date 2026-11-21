//go:build linux && ataniq_canary

package leaddaemon

import (
	"net"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadholder/wire"
	"ataniq/internal/leadwire"
)

// The tests of what a row of lead.list says about the lead's state and about
// its epoch's message: the reason code of the shown state, and the holder's
// three words about the message.

// A lead that was defined shows the registry's reason of that state, and one
// that the observer promoted shows ready with holder-ready, from the registry's
// entry: nothing in the row is made up by the view.
func TestLeadListRowShowsTheStatesReason(t *testing.T) {
	l := newLeadEnv(t)
	l.readyBounds(10 * time.Second)
	l.start()
	if row := l.row("alpha"); row.State != "" || row.StateReason != "" || row.MessageResult != (leadwire.MessageResult{}) {
		t.Fatalf("a lead that is not defined: %+v", row)
	}
	want(t, l.define("alpha"), leadwire.StatusDone, "")
	if row := l.row("alpha"); row.State != "stopped" || row.StateReason != "defined" {
		t.Fatalf("a lead that was defined: state %q reason %q", row.State, row.StateReason)
	}
	h := newReadyHolder(roleOf("alpha"))
	h.epoch.Store(1)
	l.holder("alpha", h.answer)
	want(t, l.startLead("alpha"), leadwire.StatusDone, "")
	l.waitState("alpha", "ready")
	row := l.row("alpha")
	if row.State != "ready" || row.StateReason != "holder-ready" || row.Epoch != 1 {
		t.Fatalf("the promoted lead: %+v", row)
	}
	if row.MessageSpent || row.MessageResult != (leadwire.MessageResult{}) {
		t.Fatalf("a message result before a message: %+v", row)
	}
	// The reason is the registry's, and it follows the state: a stop gives a
	// reason of its own, and the old one is not kept.
	want(t, l.stopLead("alpha", 1), leadwire.StatusDone, "")
	if row := l.row("alpha"); row.State != "stopped" || row.StateReason == "holder-ready" {
		t.Fatalf("after the stop: state %q reason %q", row.State, row.StateReason)
	}
}

// notNowHolder answers an inject with a not-now and every other request as the
// tests' usual holder does.
func notNowHolder(req wire.Request, conn net.Conn) {
	if req.Op != wire.OpInject {
		submitted(req, conn)
		return
	}
	writeResponse(conn, wire.Response{OK: true, Inject: &wire.InjectResult{
		Outcome: wire.InjectOutcomeNotNow, Reason: wire.InjectReasonNotReady,
		Written: wire.InjectWrittenNothing, Detail: "sample detail",
	}})
}

// A row has the holder's three words about the epoch's message once it was
// delivered, and nothing before. The row's receipt is the summary form: no
// result and no message. The words stay when a later command is the role's last
// receipt, and, after a restart of the daemon, they are read from the command
// table while the inject is still the last receipt and are not known once
// another command follows it.
func TestLeadListRowShowsTheMessagesWords(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	sr := l.up("alpha", notNowHolder)
	words := leadwire.MessageResult{Outcome: "not-now", Reason: "not-ready", Written: "nothing"}

	if row := l.row("alpha"); row.MessageSpent || row.MessageResult != (leadwire.MessageResult{}) {
		t.Fatalf("before the message: %+v", row)
	}
	want(t, l.inject("alpha", sr.Epoch, "msg-one", "body one"), leadwire.StatusDone, "")
	row := l.row("alpha")
	if !row.MessageSpent || row.MessageResult != words {
		t.Fatalf("after the message: %+v", row)
	}
	if row.Last == nil || row.Last.Op != leadwire.OpInject || row.Last.Result != nil || row.Last.Message != "" {
		t.Fatalf("the row's receipt is not the summary form: %+v", row.Last)
	}

	// A restart: the inject is still the last receipt, and the table has it.
	l.restart()
	if row := l.row("alpha"); !row.MessageSpent || row.MessageResult != words {
		t.Fatalf("after the restart: %+v", row)
	}

	// A command after the inject: this daemon kept nothing since the restart,
	// so the words are not known, and the message is still shown spent.
	want(t, l.ticket("alpha", sr.Epoch, leadwire.AttachViewer), leadwire.StatusDone, "")
	if row := l.row("alpha"); !row.MessageSpent || row.MessageResult != (leadwire.MessageResult{}) {
		t.Fatalf("after a ticket that followed the restart: %+v", row)
	}
}

// The words are kept in memory while the daemon runs, so a command after the
// inject does not take them from the row.
func TestLeadListRowKeepsTheMessagesWordsAcrossLaterCommands(t *testing.T) {
	l := newLeadEnv(t)
	l.start()
	sr := l.up("alpha", notNowHolder)
	want(t, l.inject("alpha", sr.Epoch, "msg-one", "body one"), leadwire.StatusDone, "")
	want(t, l.ticket("alpha", sr.Epoch, leadwire.AttachViewer), leadwire.StatusDone, "")
	row := l.row("alpha")
	if row.Last == nil || row.Last.Op != leadwire.OpTicket {
		t.Fatalf("the last receipt: %+v", row.Last)
	}
	if want := (leadwire.MessageResult{Outcome: "not-now", Reason: "not-ready", Written: "nothing"}); row.MessageResult != want {
		t.Fatalf("message result %+v, want %+v", row.MessageResult, want)
	}
	// A new epoch has no message and no words.
	want(t, l.stopLead("alpha", sr.Epoch), leadwire.StatusDone, "")
	want(t, l.startLead("alpha"), leadwire.StatusDone, "")
	if row := l.row("alpha"); row.Epoch != sr.Epoch+1 || row.MessageSpent || row.MessageResult != (leadwire.MessageResult{}) {
		t.Fatalf("the next epoch: %+v", row)
	}
}

func TestFixedWord(t *testing.T) {
	for _, tc := range []struct {
		in   string
		max  int
		want string
	}{
		{"", 32, ""},
		{"holder-ready", 32, "holder-ready"},
		{"control-group-not-proven-empty", leadwire.MaxStateReason, "control-group-not-proven-empty"},
		{strings.Repeat("a", 33), 32, ""},
		{strings.Repeat("a", 32), 32, strings.Repeat("a", 32)},
		{"Holder", 32, ""},
		{"1abc", 32, ""},
		{"-abc", 32, ""},
		{"a b", 32, ""},
		{"a_b", 32, ""},
		{"a\nb", 32, ""},
		{"a1-b2", 32, "a1-b2"},
		{"not-submitted", leadwire.MaxDeliveryWord, "not-submitted"},
	} {
		if got := fixedWord(tc.in, tc.max); got != tc.want {
			t.Errorf("fixedWord(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}
