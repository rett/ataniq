package leadctl

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func twoLeads(t *testing.T) *fakeBackend {
	b := newBackend(t)
	b.listing = Listing{MaxRunning: 2, Running: 1, Leads: []Lead{aLead("alpha"), aRunning("beta")}}
	return b
}

func TestInitReadsTheListAndShowsTheRows(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	if b.lists != 1 {
		t.Fatalf("lists %d", b.lists)
	}
	r.seen("LEAD CONTROL", "1 running of 2", "alpha", "beta", "stopped", "ready", "ataniq-lead-scratch-beta.service")
	rows := r.rows()
	// The cursor mark is on the first row.
	if !strings.HasPrefix(rows[4], " >") || strings.Contains(rows[5], ">") {
		t.Fatalf("cursor:\n%s", r.screen())
	}
	// The row of a running lead shows its epoch and a free message.
	if !strings.Contains(rows[5], "3") || !strings.Contains(rows[5], "free") {
		t.Fatalf("row %q", rows[5])
	}
}

func TestJKAndTheArrowsMoveAndStopAtTheEnds(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	at := func() int { return r.m.cursor }
	r.key("k")
	if at() != 0 {
		t.Fatal("k moved above the first row")
	}
	r.key("j")
	if at() != 1 {
		t.Fatal("j did not move down")
	}
	r.key("down")
	if at() != 1 {
		t.Fatal("down moved past the last row")
	}
	r.key("up")
	if at() != 0 {
		t.Fatal("up did not move up")
	}
	if b.total() != 0 {
		t.Fatalf("a move called the backend: %+v", b.calls)
	}
}

func TestMovingScrollsALongTable(t *testing.T) {
	b := newBackend(t)
	for i := 0; i < 12; i++ {
		b.listing.Leads = append(b.listing.Leads, aLead("lead"+string(rune('a'+i))))
	}
	r := newRig(t, b, 100, 14)
	for i := 0; i < 11; i++ {
		r.key("j")
	}
	r.seen("leadl")
	r.notSeen("leada ")
	if !strings.Contains(strings.Join(r.rows()[4:], "\n"), ">") {
		t.Fatal("the cursor is off the screen")
	}
}

func TestRReadsTheListAgain(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	b.listing.Leads[0].State = "starting"
	r.key("r")
	if b.lists != 2 {
		t.Fatalf("lists %d", b.lists)
	}
	r.seen("starting")
}

func TestDDefinesTheLeadUnderTheCursorOnce(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	r.key("j")
	r.key("d")
	cs := b.callsOf("define")
	if len(cs) != 1 || cs[0].role != "scratch-beta/lead" {
		t.Fatalf("calls %+v", b.calls)
	}
	r.seen("define: accepted, done")
	// The list is read again after the receipt.
	if b.lists != 2 {
		t.Fatalf("lists %d", b.lists)
	}
}

func TestSStartsTheLeadUnderTheCursorOnce(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	r.key("s")
	cs := b.callsOf("start")
	if len(cs) != 1 || cs[0].role != "scratch-alpha/lead" {
		t.Fatalf("calls %+v", b.calls)
	}
	r.seen("start: accepted, done")
}

func TestXAsksBeforeItStops(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	r.key("j")
	r.key("x")
	r.seen("Stop beta (epoch 3)?", "[y] stop the lead")
	if b.total() != 0 {
		t.Fatalf("a stop was sent before the confirmation: %+v", b.calls)
	}
	// Every key but y cancels, and nothing is sent.
	for _, k := range []string{"n", "esc", "enter", "x", "s", "space"} {
		if r.m.confirm == nil {
			r.key("x")
		}
		r.key(k)
		if r.m.confirm != nil || b.total() != 0 {
			t.Fatalf("key %q: confirm %v calls %+v", k, r.m.confirm, b.calls)
		}
		r.seen("stop cancelled")
	}
	r.key("x")
	r.key("y")
	cs := b.callsOf("stop")
	if len(cs) != 1 || cs[0].role != "scratch-beta/lead" || cs[0].epoch != 3 {
		t.Fatalf("calls %+v", b.calls)
	}
	r.seen("stop: accepted, done")
}

func TestAStopOfALeadWithNoEpochMeansTheCurrentOne(t *testing.T) {
	b := twoLeads(t)
	b.listing.Leads[0].State = "unknown"
	r := newRig(t, b, 120, 30)
	r.key("x")
	r.seen("Stop alpha (the current epoch)?")
	r.key("y")
	if cs := b.callsOf("stop"); len(cs) != 1 || cs[0].epoch != 0 {
		t.Fatalf("calls %+v", b.calls)
	}
}

func TestTheStopConfirmationKeepsTheEpochItShowed(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	r.key("j")
	r.key("x")
	// The list changes while the question is open.
	b.listing.Leads[1].Epoch = 4
	r.run(r.m.Refresh())
	r.seen("Stop beta (epoch 3)?")
	r.key("y")
	if cs := b.callsOf("stop"); len(cs) != 1 || cs[0].epoch != 3 {
		t.Fatalf("calls %+v", b.calls)
	}
}

func TestMSendsOneMessageForTheRowsEpoch(t *testing.T) {
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) {
		return Receipt{CommandID: "m1", Op: OpInject, Status: Done, Outcome: OutcomeSubmitted}, nil
	}
	r := newRig(t, b, 120, 30)
	r.key("j")
	r.key("m")
	r.seen("message to beta (epoch 3)>", "[enter] send once")
	r.typ("hello")
	r.key("space")
	r.typ("wörld")
	r.key("backspace")
	r.key("x")
	r.seen("hello wörlx_")
	if b.total() != 0 {
		t.Fatalf("typing called the backend: %+v", b.calls)
	}
	r.key("enter")
	cs := b.callsOf("inject")
	if len(cs) != 1 {
		t.Fatalf("calls %+v", b.calls)
	}
	c := cs[0]
	if c.role != "scratch-beta/lead" || c.epoch != 3 || c.body != "hello wörlx" {
		t.Fatalf("call %+v", c)
	}
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`).MatchString(c.messageID) {
		t.Fatalf("message id %q is outside the holder's grammar", c.messageID)
	}
	if r.m.edit != nil {
		t.Fatal("the editor stayed open")
	}
	r.seen("message: accepted. the message was submitted")
}

func TestEscDropsTheMessageAndSendsNothing(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	r.key("j")
	r.key("m")
	r.typ("secret")
	r.key("esc")
	if r.m.edit != nil || b.total() != 0 || r.last.Kind != None {
		t.Fatalf("edit %v calls %+v last %+v", r.m.edit, b.calls, r.last)
	}
	// The text is gone: a new editor is empty.
	r.key("m")
	if r.m.edit == nil || r.m.edit.text != "" {
		t.Fatal("the dropped text came back")
	}
}

func TestAMessageThatIsBlankIsNotSent(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	r.key("j")
	r.key("m")
	r.key("enter")
	r.seen("there is nothing to send")
	r.key("m")
	r.key("space")
	r.key("space")
	r.key("enter")
	r.seen("there is nothing to send")
	if b.total() != 0 {
		t.Fatalf("calls %+v", b.calls)
	}
}

func TestCtrlUClearsTheEditorAndKeysThatAreNotTextAreIgnored(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	r.key("j")
	r.key("m")
	r.typ("abc")
	r.key("ctrl+u")
	if r.m.edit.text != "" {
		t.Fatalf("text %q", r.m.edit.text)
	}
	r.typ("q")
	// q, j and the other keys of the view are text in the editor.
	if r.m.edit.text != "q" || r.last.Kind != None {
		t.Fatalf("text %q last %+v", r.m.edit.text, r.last)
	}
	r.feed(tea.KeyPressMsg{Code: 'a', Mod: tea.ModAlt, Text: "a"})
	r.feed(tea.KeyPressMsg{Code: tea.KeyF1})
	r.feed(tea.KeyPressMsg{Code: 'e', Text: "esc"})
	if r.m.edit.text != "q" {
		t.Fatalf("text %q", r.m.edit.text)
	}
}

func TestTheEditorTakesAPasteOnALineAndRefusesOneThatCannotBeSent(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	r.key("j")
	// A paste outside the editor does nothing.
	r.feed(tea.PasteMsg{Content: "ignored"})
	r.key("m")
	r.feed(tea.PasteMsg{Content: "one\ntwo\r\nthree"})
	if r.m.edit.text != "one two three" {
		t.Fatalf("text %q", r.m.edit.text)
	}
	r.feed(tea.PasteMsg{Content: "bad\x1b[31mred"})
	if r.m.edit.text != "one two three" {
		t.Fatalf("text %q", r.m.edit.text)
	}
	r.seen("that paste was not taken")
	r.feed(tea.PasteMsg{Content: strings.Repeat("x", maxBody)})
	r.seen("the message is at the longest the lead takes")
	if r.m.edit.text != "one two three" {
		t.Fatal("a paste past the cap was taken in part")
	}
}

func TestTheEditorStopsAtTheLongestMessage(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	r.key("j")
	r.key("m")
	r.feed(tea.PasteMsg{Content: strings.Repeat("y", maxBody)})
	r.key("z")
	if len(r.m.edit.text) != maxBody {
		t.Fatalf("%d bytes", len(r.m.edit.text))
	}
	r.seen("the message is at the longest")
}

func TestMIsRefusedWhereTheRowSaysNoMessageCanGo(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*Lead)
		want string
	}{
		{"not running", func(l *Lead) { l.Epoch = 0 }, "the lead has no epoch"},
		{"injection off", func(l *Lead) { l.Inject = false }, "messages are off for this lead"},
		{"spent", func(l *Lead) { l.MessageSpent = true }, "a message was already sent in this epoch"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newBackend(t)
			l := aRunning("beta")
			c.edit(&l)
			b.listing.Leads = []Lead{l}
			r := newRig(t, b, 120, 30)
			r.key("m")
			if r.m.edit != nil || b.total() != 0 {
				t.Fatalf("the editor opened or a call was made: %+v", b.calls)
			}
			r.seen(c.want)
		})
	}
}

func TestAMessageWhoseAnswerWasLostIsNotMadeAgainForTheEpoch(t *testing.T) {
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) { return Receipt{}, errLost }
	r := newRig(t, b, 120, 30)
	r.key("j")
	r.key("m")
	r.typ("one")
	r.key("enter")
	if n := len(b.callsOf("inject")); n != 1 {
		t.Fatalf("%d sends", n)
	}
	r.seen("the message may or may not have reached the lead")
	r.notSeen("connection reset", "pwned")
	// A second message for the same epoch is not made, whatever the list
	// says, until the epoch changes.
	r.key("m")
	if r.m.edit != nil {
		t.Fatal("a second editor opened for an unconfirmed epoch")
	}
	r.seen("has no final answer")
	b.listing.Leads[1].Epoch = 4
	r.key("r")
	r.key("m")
	if r.m.edit == nil {
		t.Fatal("a new epoch is a new message")
	}
}

func TestARefusedMessageMayBeTriedAgainButAnUncertainOneMayNot(t *testing.T) {
	for _, c := range []struct {
		name  string
		r     Receipt
		again bool
	}{
		{"refused", Receipt{Status: Refused, Code: "message-in-flight"}, true},
		{"unavailable", Receipt{Status: Unavailable, Code: "holder-unreachable"}, true},
		{"not now", Receipt{Status: Done, Outcome: OutcomeNotNow}, true},
		{"lead refused", Receipt{Status: Done, Outcome: OutcomeRefused}, true},
		{"submitted", Receipt{Status: Done, Outcome: OutcomeSubmitted}, false},
		{"uncertain outcome", Receipt{Status: Done, Outcome: OutcomeUncertain}, false},
		{"uncertain", Receipt{Status: Uncertain}, false},
		{"in progress", Receipt{Status: InProgress}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := twoLeads(t)
			b.answer = func(call) (Receipt, error) { return c.r, nil }
			r := newRig(t, b, 120, 30)
			r.key("j")
			r.key("m")
			r.typ("one")
			r.key("enter")
			r.key("m")
			if got := r.m.edit != nil; got != c.again {
				t.Fatalf("editor open %v, want %v", got, c.again)
			}
		})
	}
}

func TestWAndVAskForATicketAndAttach(t *testing.T) {
	for _, c := range []struct {
		key    string
		writer bool
	}{{"w", true}, {"v", false}} {
		t.Run(c.key, func(t *testing.T) {
			b := twoLeads(t)
			b.answer = func(call) (Receipt, error) {
				return Receipt{CommandID: "t1", Op: OpTicket, Status: Done, Folder: "/srv/lead/beta", TicketFile: "/srv/lead/beta/ticket"}, nil
			}
			r := newRig(t, b, 120, 30)
			r.key("j")
			act := r.key(c.key)
			cs := b.callsOf("ticket")
			if len(cs) != 1 || cs[0].role != "scratch-beta/lead" || cs[0].epoch != 3 || cs[0].writer != c.writer {
				t.Fatalf("calls %+v", b.calls)
			}
			if len(r.actions) != 1 || r.actions[0].Kind != Attach {
				t.Fatalf("actions %+v (last %+v)", r.actions, act)
			}
			want := AttachRequest{Role: "scratch-beta/lead", Epoch: 3, Writer: c.writer, Folder: "/srv/lead/beta", TicketFile: "/srv/lead/beta/ticket"}
			if r.actions[0].Attach != want {
				t.Fatalf("attach %+v, want %+v", r.actions[0].Attach, want)
			}
		})
	}
}

func TestATicketThatIsNotDoneDoesNotAttach(t *testing.T) {
	for _, rc := range []Receipt{
		{Status: Refused, Code: "not-running", Folder: "/srv/a", TicketFile: "/srv/b"},
		{Status: Unavailable, Code: "holder-unreachable"},
		{Status: Uncertain},
		{Status: InProgress, Folder: "/srv/a", TicketFile: "/srv/b"},
	} {
		b := twoLeads(t)
		rc := rc
		b.answer = func(call) (Receipt, error) { return rc, nil }
		r := newRig(t, b, 120, 30)
		r.key("j")
		r.key("w")
		if len(r.actions) != 0 {
			t.Fatalf("receipt %+v attached: %+v", rc, r.actions)
		}
	}
}

func TestATicketWithAnUnusablePathDoesNotAttach(t *testing.T) {
	for _, p := range []string{"", "/srv/a\x00b", "/srv/a\nb", "/srv/\xff", strings.Repeat("/a", maxPath)} {
		b := twoLeads(t)
		p := p
		b.answer = func(call) (Receipt, error) {
			return Receipt{Op: OpTicket, Status: Done, Folder: p, TicketFile: "/srv/ok"}, nil
		}
		r := newRig(t, b, 120, 30)
		r.key("j")
		r.key("v")
		if len(r.actions) != 0 {
			t.Fatalf("path %q attached", p)
		}
		r.seen("the answer could not be used")
	}
}

func TestATicketForALeadThatIsNotRunningIsNotAsked(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	r.key("w")
	r.key("v")
	if b.total() != 0 {
		t.Fatalf("calls %+v", b.calls)
	}
	r.seen("the lead has no epoch")
}

func TestEscClosesAndCtrlCQuits(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	if act := r.key("esc"); act.Kind != Close {
		t.Fatalf("esc: %+v", act)
	}
	if act := r.key("ctrl+c"); act.Kind != Quit {
		t.Fatalf("ctrl+c: %+v", act)
	}
	// ctrl+c drops an open field and still quits.
	r.key("j")
	r.key("m")
	r.typ("x")
	if act := r.key("ctrl+c"); act.Kind != Quit || r.m.edit != nil {
		t.Fatalf("ctrl+c in the editor: %+v, edit %v", act, r.m.edit)
	}
	r.key("x")
	if act := r.key("ctrl+c"); act.Kind != Quit || r.m.confirm != nil {
		t.Fatalf("ctrl+c in the confirmation: %+v", act)
	}
	if b.total() != 0 {
		t.Fatalf("calls %+v", b.calls)
	}
}

func TestEscInAFieldGoesBackOneStepAndDoesNotClose(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	r.key("j")
	r.key("m")
	if r.m.edit == nil {
		t.Fatal("no editor")
	}
	r.key("esc")
	r.key("x")
	if r.m.confirm == nil {
		t.Fatal("no confirmation")
	}
	r.key("esc")
	if len(r.actions) != 0 || r.m.edit != nil || r.m.confirm != nil {
		t.Fatalf("actions %+v", r.actions)
	}
}

func TestEveryKeyDoesNothingWithNoLeadUnderTheCursor(t *testing.T) {
	b := newBackend(t)
	r := newRig(t, b, 120, 30)
	r.seen("no leads in this daemon")
	for _, k := range []string{"d", "s", "x", "m", "w", "v"} {
		r.key(k)
		r.seen("there is no lead under the cursor")
		if r.m.edit != nil || r.m.confirm != nil {
			t.Fatalf("key %q opened a field", k)
		}
	}
	if b.total() != 0 {
		t.Fatalf("calls %+v", b.calls)
	}
}

func TestAKeyOfTheViewDoesNothingWhileACommandForTheLeadIsOut(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	// Take the command without running it: the answer is still out.
	cmd, _ := r.m.Update(keyMsg("s"))
	if cmd == nil {
		t.Fatal("no command")
	}
	for _, k := range []string{"s", "d", "x", "w", "v", "m"} {
		r.key(k)
		r.seen("a command for this lead is still being sent")
		if r.m.confirm != nil || r.m.edit != nil {
			t.Fatalf("key %q opened a field", k)
		}
	}
	if b.total() != 0 {
		t.Fatalf("a second command was made: %+v", b.calls)
	}
	// Another lead is free.
	r.key("j")
	r.key("s")
	if n := len(b.callsOf("start")); n != 1 {
		t.Fatalf("%d starts", n)
	}
	// The answer of the first one arrives once.
	r.run(cmd)
	if n := len(b.callsOf("start")); n != 2 {
		t.Fatalf("%d starts", n)
	}
	r.key("k")
	r.key("s")
	if n := len(b.callsOf("start")); n != 3 {
		t.Fatalf("%d starts after the answer", n)
	}
}

func TestTheBackendIsCalledOnlyFromCommands(t *testing.T) {
	// The fake fails the test when a method runs outside the rig's commands.
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	for _, k := range []string{"d", "s", "j", "x", "y", "m", "a", "enter", "w", "v", "r"} {
		r.key(k)
	}
	if b.total() == 0 {
		t.Fatal("nothing was called")
	}
}

func TestACommandNeverSendsWithoutAnIdSource(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	r.m.random = strings.NewReader("short")
	r.key("j")
	r.key("m")
	r.typ("one")
	r.key("enter")
	r.seen("no source of randomness")
	if b.total() != 0 {
		t.Fatalf("calls %+v", b.calls)
	}
}

func TestAnEarlierTicketReceiptNeverAttaches(t *testing.T) {
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) {
		return Receipt{CommandID: "t1", Op: OpTicket, Status: Done, Folder: "/srv/lead/beta", TicketFile: "/srv/lead/beta/ticket", Earlier: true}, nil
	}
	r := newRig(t, b, 120, 30)
	r.key("j")
	r.key("w")
	if len(r.actions) != 0 {
		t.Fatalf("actions %+v", r.actions)
	}
	r.seen("an earlier attach ticket command")
	r.notSeen(unusableAns)
}
