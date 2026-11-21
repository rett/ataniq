package leadctl

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

func TestEveryReceiptIsShownInTheViewsOwnFixedWords(t *testing.T) {
	for _, c := range []struct {
		name string
		r    Receipt
		want []string
		not  []string
	}{
		{"in progress", Receipt{Op: OpStart, Status: InProgress}, []string{"start: accepted, still running"}, nil},
		{"done", Receipt{Op: OpStart, Status: Done}, []string{"start: accepted, done"}, nil},
		{"refused", Receipt{Op: OpStart, Status: Refused, Code: "max-running"}, []string{"start: refused: the limit of running leads is reached"}, nil},
		{"unavailable", Receipt{Op: OpStart, Status: Unavailable, Code: "holder-unreachable"}, []string{"start: unavailable: the lead's holder could not be reached"}, nil},
		{"uncertain", Receipt{Op: OpStart, Status: Uncertain}, []string{"start: uncertain: the outcome is not known", "nothing is sent again for you"}, nil},
		{"unknown code", Receipt{Op: OpStart, Status: Refused, Code: "a code from the future"}, []string{"refused: the reason is not known to this console"}, []string{"future"}},
		{"unknown status", Receipt{Op: OpStart, Status: "weird"}, []string{"the status is not known to this console"}, []string{"weird"}},
		{"submitted", Receipt{Op: OpInject, Status: Done, Outcome: OutcomeSubmitted}, []string{"message: accepted. the message was submitted", "This epoch's one message is spent"}, nil},
		{"not now", Receipt{Op: OpInject, Status: Done, Outcome: OutcomeNotNow}, []string{"message: accepted. NOT delivered: nothing was written, the lead could not take it now", "The epoch's message is free"}, []string{"is spent"}},
		{"lead refused", Receipt{Op: OpInject, Status: Done, Outcome: OutcomeRefused}, []string{"NOT delivered: nothing was written, the lead refused it"}, nil},
		{"uncertain outcome", Receipt{Op: OpInject, Status: Done, Outcome: OutcomeUncertain}, []string{"result unknown: it may have reached the lead"}, nil},
		{"unknown outcome", Receipt{Op: OpInject, Status: Done, Outcome: "sent!!"}, []string{"the result is not known to this console"}, []string{"sent!!", "is spent"}},
		{"unknown reason", Receipt{Op: OpInject, Status: Done, Outcome: OutcomeNotNow, Reason: "a reason from the future", Written: WrittenNothing}, []string{"the result is not known to this console"}, []string{"future", "NOT delivered"}},
		{"a pair the holder does not send", Receipt{Op: OpInject, Status: Done, Outcome: OutcomeSubmitted, Reason: ReasonNotReady, Written: WrittenNothing}, []string{"the result is not known to this console"}, []string{"delivered"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := twoLeads(t)
			op := c.r.Op
			b.answer = func(call) (Receipt, error) { return c.r, nil }
			r := newRig(t, b, 140, 30)
			switch op {
			case OpStart:
				r.key("s")
			case OpInject:
				r.key("j")
				r.key("m")
				r.typ("hi")
				r.key("enter")
			}
			r.seen(c.want...)
			r.notSeen(c.not...)
		})
	}
}

func TestTheTableShowsTheLastReceiptOfEachLead(t *testing.T) {
	b := twoLeads(t)
	b.listing.Leads[0].Last = &Receipt{Op: OpDefine, Status: Refused, Code: "registry-refused"}
	b.listing.Leads[1].Last = &Receipt{Op: OpInject, Status: Uncertain}
	r := newRig(t, b, 140, 30)
	rows := r.rows()
	if !strings.Contains(rows[4], "define: refused") || !strings.Contains(rows[5], "message: uncertain") {
		t.Fatalf("rows:\n%s", r.screen())
	}
}

func TestNothingAPeerSaidReachesACellButThroughTheSafeSubset(t *testing.T) {
	hostile := "\x1b]0;pwned\a\x1b[31mred\u202e\u200b\n"
	b := newBackend(t)
	l := aRunning("beta")
	l.Key = "k" + hostile + "ey"
	l.State = "ready" + hostile
	l.Unit = "unit" + hostile + ".service"
	l.Provision = hostile
	l.Last = &Receipt{CommandID: hostile, Op: Op(hostile), Status: Status(hostile), Code: hostile, Outcome: hostile, Folder: hostile, TicketFile: hostile}
	b.listing = Listing{MaxRunning: 2, Running: 1, Leads: []Lead{l}}
	b.answer = func(call) (Receipt, error) {
		return Receipt{CommandID: hostile, Op: Op(hostile), Status: Status(hostile), Code: hostile, Outcome: hostile}, nil
	}
	r := newRig(t, b, 140, 30)
	for _, k := range []string{"s", "d"} {
		r.key(k)
	}
	buf := r.draw()
	for y := 0; y < buf.Bounds().Dy(); y++ {
		for x := 0; x < buf.Bounds().Dx(); x++ {
			c := buf.CellAt(x, y)
			if c == nil {
				continue
			}
			for _, ch := range c.Content {
				if ch < 0x20 || ch == 0x7f || ch == 0x202e || ch == 0x200b || ch > 0x7f && ch != '…' && ch != '─' {
					t.Fatalf("cell (%d,%d) holds %U", x, y, ch)
				}
			}
		}
	}
	// What is drawn is the safe subset: the escape byte is a question mark.
	r.seen("k??0?pwned")
	r.notSeen("\x1b", "\u202e")
}

func TestAnErrorFromTheBackendIsNeverShown(t *testing.T) {
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) { return Receipt{}, errLost }
	b.listErr = errLost
	r := newRig(t, b, 140, 30)
	r.key("s")
	r.key("r")
	r.seen("the list could not be read")
	r.notSeen("connection", "pwned", "reset")
	r.key("s")
}

func TestAListThatCannotBeReadAtFirstSaysSo(t *testing.T) {
	b := newBackend(t)
	b.listErr = errLost
	r := newRig(t, b, 140, 30)
	r.seen("the list could not be read")
	r.notSeen("reading the list", "connection")
	// Reading it again, now that it works, takes the rows in.
	b.listErr = nil
	b.listing.Leads = []Lead{aLead("alpha")}
	r.key("r")
	r.seen("alpha")
	r.notSeen("could not be read")
}

func TestALostAnswerReadsTheListAgainAndSendsNothingElse(t *testing.T) {
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) { return Receipt{}, errLost }
	r := newRig(t, b, 140, 30)
	lists := b.lists
	r.key("s")
	if n := len(b.callsOf("start")); n != 1 || b.total() != 1 {
		t.Fatalf("calls %+v", b.calls)
	}
	if b.lists != lists+1 {
		t.Fatalf("the list was read %d times, want %d", b.lists, lists+1)
	}
	r.seen("no answer came back", "nothing was sent a second time")
}

func TestAnInProgressReceiptIsReadAgainEveryPollUntilItIsFinal(t *testing.T) {
	b := twoLeads(t)
	b.listing.Leads[0].Last = &Receipt{CommandID: "c1", Op: OpStart, Status: InProgress}
	r := newRig(t, b, 140, 30)
	r.seen("start: accepted (running)")
	if len(r.pending) != 1 {
		t.Fatalf("%d timers", len(r.pending))
	}
	// Two polls and still in progress: one timer each time, never two.
	for i := 0; i < 2; i++ {
		lists := b.lists
		r.tick()
		if b.lists != lists+1 || len(r.pending) != 1 {
			t.Fatalf("poll %d: lists %d to %d, timers %d", i, lists, b.lists, len(r.pending))
		}
	}
	// It ends: no timer is left.
	b.listing.Leads[0].Last = &Receipt{CommandID: "c1", Op: OpStart, Status: Done}
	r.tick()
	r.seen("start: accepted (done)")
	if len(r.pending) != 0 {
		t.Fatalf("%d timers after the receipt ended", len(r.pending))
	}
	lists := b.lists
	r.tick()
	if b.lists != lists {
		t.Fatal("the list was read with nothing in progress")
	}
}

func TestTheReceiptLineFollowsTheCommandThroughThePolls(t *testing.T) {
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) { return Receipt{CommandID: "c9", Op: OpStart, Status: InProgress}, nil }
	// The list does not hold the receipt yet.
	r := newRig(t, b, 140, 30)
	r.key("s")
	r.seen("start: accepted, still running")
	if len(r.pending) != 1 {
		t.Fatalf("%d timers: a receipt in progress is polled", len(r.pending))
	}
	b.listing.Leads[0].Last = &Receipt{CommandID: "c9", Op: OpStart, Status: Uncertain}
	r.tick()
	r.seen("start: uncertain")
	r.notSeen("still running")
	if len(r.pending) != 0 {
		t.Fatal("the poll went on after a final receipt")
	}
}

func TestAnAnswerForAViewThatClosedOrAnOlderListIsLeftAlone(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 140, 30)
	other := New(Options{Backend: b})
	if cmd, act := r.m.Update(listedMsg{owner: other, seq: 1, l: Listing{}}); cmd != nil || act.Kind != None || len(r.m.leads) != 2 {
		t.Fatal("the answer of another view was taken")
	}
	r.m.listSeq = 5
	r.m.Update(listedMsg{owner: r.m, seq: 4, l: Listing{}})
	if len(r.m.leads) != 2 {
		t.Fatal("an answer to an older list request replaced the rows")
	}
	if cmd, act := r.m.Update(doneMsg{owner: r.m, seq: 99, role: "scratch-alpha/lead", op: OpStart, r: Receipt{Status: Done}}); cmd != nil || act.Kind != None || r.m.status != nil {
		t.Fatal("an answer to a command that was not out was taken")
	}
}

func TestACommandIsNeverSentByAnAnswerToAnother(t *testing.T) {
	// An attach asked for one lead is not made by the receipt of another
	// command.
	b := twoLeads(t)
	b.answer = func(c call) (Receipt, error) {
		return Receipt{Op: OpTicket, Status: Done, Folder: "/srv/a", TicketFile: "/srv/b"}, nil
	}
	r := newRig(t, b, 140, 30)
	r.key("j")
	r.key("x")
	r.key("y")
	if len(r.actions) != 0 {
		t.Fatalf("a stop produced %+v", r.actions)
	}
	r.seen("stop: accepted, done")
}

func TestLockStatesAndModifiersDoNotChangeKeys(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 140, 30)
	r.feed(tea.KeyPressMsg{Code: 'j', Text: "j", Mod: tea.ModCapsLock})
	if r.m.cursor != 1 {
		t.Fatal("caps lock changed j")
	}
	for _, k := range []tea.KeyPressMsg{
		{Code: 's', Mod: tea.ModCtrl}, {Code: 's', Mod: tea.ModAlt, Text: "s"}, {Code: 'S', Mod: tea.ModShift, Text: "S"},
		{Code: 'y', Text: "y"}, {Code: 'x', Text: "xx"},
	} {
		r.feed(k)
	}
	if b.total() != 0 {
		t.Fatalf("calls %+v", b.calls)
	}
}

func TestTheViewDrawsInsideItsRegionAtEverySize(t *testing.T) {
	b := newBackend(t)
	for i := 0; i < 40; i++ {
		l := aRunning(fmt.Sprintf("%s-%d", strings.Repeat("k", 60), i))
		l.Last = &Receipt{Op: OpStart, Status: Uncertain}
		b.listing.Leads = append(b.listing.Leads, l)
	}
	b.listing.MaxRunning, b.listing.Running = 2, 1
	r := newRig(t, b, 120, 30)
	states := []func(){
		func() {},
		func() { r.key("x") },
		func() {
			r.key("esc")
			r.key("m")
			r.typ("héllo 😀 wörld")
			r.feed(tea.PasteMsg{Content: strings.Repeat("long ", 80)})
		},
		func() { r.key("ctrl+u"); r.key("esc") },
	}
	for _, st := range states {
		st()
		for _, sz := range [][2]int{{0, 0}, {1, 1}, {3, 2}, {8, 4}, {20, 9}, {40, 10}, {60, 12}, {79, 23}, {120, 30}, {400, 90}} {
			r.w, r.h = sz[0], sz[1]
			r.m.SetSize(sz[0], sz[1])
			// A bigger canvas than the region: nothing is drawn outside it.
			canvas := uv.NewBuffer(sz[0]+6, sz[1]+4)
			area := uv.Rect(3, 2, sz[0], sz[1])
			r.m.Draw(canvas, area)
			for y := 0; y < canvas.Bounds().Dy(); y++ {
				for x := 0; x < canvas.Bounds().Dx(); x++ {
					c := canvas.CellAt(x, y)
					if c == nil || c.Content == "" || c.Content == " " {
						continue
					}
					if !inside(x, y, area) {
						t.Fatalf("size %v: a cell (%d,%d) %q outside the region", sz, x, y, c.Content)
					}
					if !utf8.ValidString(c.Content) {
						t.Fatalf("size %v: invalid cell", sz)
					}
				}
			}
		}
	}
}

func inside(x, y int, r uv.Rectangle) bool {
	return x >= r.Min.X && x < r.Max.X && y >= r.Min.Y && y < r.Max.Y
}

func TestTheEditorShowsTheEndOfALongMessage(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 60, 30)
	r.key("j")
	r.key("m")
	r.feed(tea.PasteMsg{Content: strings.Repeat("abcdefghij ", 12) + "THE END"})
	rows := r.rows()
	line := rows[len(rows)-2]
	if !strings.Contains(line, "THE END_") || !strings.Contains(line, "…") {
		t.Fatalf("editor line %q", line)
	}
}

func TestACanvasThatIsSmallerThanTheRegionIsNotWrittenOutside(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 120, 30)
	canvas := uv.NewBuffer(30, 8)
	r.m.Draw(canvas, uv.Rect(0, 0, 120, 30))
}

func TestPollOptionsAreTakenFromTheOptions(t *testing.T) {
	var got time.Duration
	m := New(Options{Backend: newBackend(t), Poll: 7 * time.Second,
		After: func(d time.Duration, fn func(time.Time) tea.Msg) tea.Cmd { got = d; return nil }})
	m.leads = []Lead{{Role: "r", Last: &Receipt{Status: InProgress}}}
	m.tickCmd()
	if got != 7*time.Second {
		t.Fatalf("poll %v", got)
	}
	if New(Options{Backend: newBackend(t)}).poll != DefaultPoll {
		t.Fatal("the default poll is not used")
	}
}

func TestAListingIsBoundedAndRowsWithoutAUsableRoleAreDropped(t *testing.T) {
	b := newBackend(t)
	for i := 0; i < 50; i++ {
		b.listing.Leads = append(b.listing.Leads, aLead(fmt.Sprintf("l%02d", i)))
	}
	b.listing.Leads = append([]Lead{{Key: "norole"}, {Key: "long", Role: strings.Repeat("r", maxRole+1)}}, b.listing.Leads...)
	r := newRig(t, b, 100, 40)
	if len(r.m.leads) != maxLeads {
		t.Fatalf("%d leads", len(r.m.leads))
	}
	r.notSeen("norole", "long ")
}

func TestAnEarlierReceiptIsDrawnAsTheAnswerToAnEarlierCommand(t *testing.T) {
	for _, c := range []struct {
		name string
		r    Receipt
		want string
		not  string
	}{
		{"start", Receipt{Op: OpStart, Status: Done, Earlier: true}, "an earlier start command: accepted, done", "start: accepted"},
		{"stop refused", Receipt{Op: OpStop, Status: Refused, Code: "stale-epoch", Earlier: true}, "an earlier stop command: refused: the epoch changed", "stop: refused"},
		{"unavailable", Receipt{Op: OpStart, Status: Unavailable, Code: "holder-unreachable", Earlier: true}, "an earlier start command: unavailable: the lead's holder", ""},
		{"uncertain", Receipt{Op: OpStart, Status: Uncertain, Earlier: true}, "an earlier start command: uncertain", ""},
		{"ticket", Receipt{Op: OpTicket, Status: Done, Earlier: true, Folder: "/f", TicketFile: "/f/t"}, "an earlier attach ticket command: accepted, done", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := twoLeads(t)
			b.answer = func(call) (Receipt, error) { return c.r, nil }
			r := newRig(t, b, 140, 30)
			lists := b.lists
			switch c.r.Op {
			case OpStart:
				r.key("s")
			case OpStop:
				r.key("j")
				r.key("x")
				r.key("y")
			case OpTicket:
				r.key("j")
				r.key("w")
			}
			r.seen(c.want)
			if c.not != "" {
				r.notSeen(c.not)
			}
			if b.lists != lists+1 {
				t.Fatalf("%d lists after the answer, want %d: the list is read again", b.lists, lists+1)
			}
			if len(r.actions) != 0 {
				t.Fatalf("actions %+v", r.actions)
			}
		})
	}
}

func TestAnEarlierMessageReceiptDoesNotClearTheEpochsUnconfirmedMessage(t *testing.T) {
	for _, rc := range []Receipt{
		{Op: OpInject, Status: Refused, Code: "message-in-flight", Earlier: true},
		{Op: OpInject, Status: Done, Outcome: OutcomeNotNow, Earlier: true},
		{Op: OpInject, Status: Unavailable, Code: "holder-unreachable", Earlier: true},
	} {
		b := twoLeads(t)
		rc := rc
		b.answer = func(call) (Receipt, error) { return rc, nil }
		r := newRig(t, b, 140, 30)
		r.key("j")
		r.key("m")
		r.typ("one")
		r.key("enter")
		r.seen("an earlier message command")
		r.key("m")
		if r.m.edit != nil {
			t.Fatalf("receipt %+v: an editor opened for an unconfirmed epoch", rc)
		}
		r.seen("has no final answer")
	}
}

func TestTheEarlierLineKeepsItsWordsWhenTheListHoldsTheSameCommand(t *testing.T) {
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) {
		return Receipt{CommandID: "c1", Op: OpStart, Status: Done, Earlier: true}, nil
	}
	r := newRig(t, b, 140, 30)
	b.listing.Leads[0].Last = &Receipt{CommandID: "c1", Op: OpStart, Status: Done}
	r.key("s")
	r.seen("an earlier start command: accepted, done")
	r.key("r")
	r.seen("an earlier start command: accepted, done")
}

func TestARefusedConflictDoesNotClearTheEpochsMessage(t *testing.T) {
	for code, again := range map[string]bool{"conflict": false, "stale-epoch": true} {
		b := twoLeads(t)
		code := code
		b.answer = func(call) (Receipt, error) { return Receipt{Op: OpInject, Status: Refused, Code: code}, nil }
		r := newRig(t, b, 140, 30)
		r.key("j")
		r.key("m")
		r.typ("one")
		r.key("enter")
		r.key("m")
		if got := r.m.edit != nil; got != again {
			t.Fatalf("%s: editor open %v, want %v", code, got, again)
		}
	}
}

func TestRowsLeftOutAreSaidInOneFixedLine(t *testing.T) {
	b := twoLeads(t)
	r := newRig(t, b, 140, 30)
	r.notSeen("left out")
	b.listing.LeftOut = 3
	r.key("r")
	r.seen("3 rows left out")
	// The rows the view drops itself are counted with them.
	b.listing.Leads = append(b.listing.Leads, Lead{Key: "norole"})
	r.key("r")
	r.seen("4 rows left out")
	b.listing.LeftOut = -5
	b.listing.Leads = b.listing.Leads[:2]
	r.key("r")
	r.notSeen("left out")
	// A list that could not be read keeps its own line.
	b.listing.LeftOut = 2
	r.key("r")
	r.seen("2 rows left out")
	b.listErr = errLost
	r.key("r")
	r.seen(noListLine)
	r.notSeen("left out")
}

// The daemon's list carries each lead's last command as a summary without its
// result, so a list that is read after a message must not take the words of the
// press away.
func TestTheStatusLineKeepsWhatBecameOfAMessageWhenTheListHoldsAWordlessSummary(t *testing.T) {
	for _, c := range []struct {
		name string
		got  Receipt
		line string
	}{
		{"not delivered", Receipt{CommandID: "m1", Op: OpInject, Status: Done, Epoch: 3,
			Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing},
			"message: accepted. NOT delivered: nothing was written, the lead's screen is not idle"},
		{"submitted", Receipt{CommandID: "m1", Op: OpInject, Status: Done, Epoch: 3,
			Outcome: OutcomeSubmitted, Written: WrittenWhole},
			"message: accepted. the message was submitted: the whole of it was written and sent"},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := twoLeads(t)
			b.answer = func(call) (Receipt, error) { return c.got, nil }
			r := newRig(t, b, 150, 30)
			r.key("j")
			r.key("m")
			r.typ("hi")
			// What the real daemon lists: the same command, no result and
			// no epoch.
			b.listing.Leads[1].Last = &Receipt{CommandID: "m1", Op: OpInject, Status: Done}
			r.key("enter")
			if !r.m.statusListed {
				t.Fatal("the list was not read after the answer")
			}
			r.seen(c.line)
			r.notSeen("the result is not known to this console")
			// And again on every later read.
			r.key("r")
			r.seen(c.line)
			r.notSeen("the result is not known to this console")
		})
	}
}

// The list's summary has no epoch, so the line keeps the epoch the answer was
// for. When the lead has moved to another epoch since (stopped and started by
// another client) the row's flag is not that message's: the line keeps the old
// result and says that nothing is known here of that epoch's message, and it
// does not say the new epoch's message is free or spent.
func TestTheStatusLineDoesNotReadAnOldResultAgainstTheEpochTheLeadMovedTo(t *testing.T) {
	for _, spent := range []bool{true, false} {
		b := twoLeads(t)
		b.answer = func(call) (Receipt, error) {
			return Receipt{CommandID: "m1", Op: OpInject, Status: Done, Epoch: 3,
				Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}, nil
		}
		r := newRig(t, b, 180, 30)
		r.key("j")
		r.key("m")
		r.typ("hi")
		// The list read after the answer: the same command, no epoch, and a
		// row that has moved to epoch 4.
		b.listing.Leads[1].Last = &Receipt{CommandID: "m1", Op: OpInject, Status: Done}
		b.listing.Leads[1].Epoch, b.listing.Leads[1].MessageSpent = 4, spent
		r.key("enter")
		if s := r.m.status; s == nil || s.Epoch != 3 {
			t.Fatalf("spent %v: the status after the list: %+v", spent, s)
		}
		r.seen("message: accepted. NOT delivered: nothing was written, the lead's screen is not idle" + movedTail)
		r.notSeen("is free", "spent all the same", "unless the list says")
		// Another read changes nothing.
		r.key("r")
		r.seen(movedTail)
		r.notSeen("is free", "spent all the same")
	}
	// The same row at the answer's own epoch is read as before.
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) {
		return Receipt{CommandID: "m1", Op: OpInject, Status: Done, Epoch: 3,
			Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}, nil
	}
	r := newRig(t, b, 180, 30)
	r.key("j")
	r.key("m")
	r.typ("hi")
	b.listing.Leads[1].Last = &Receipt{CommandID: "m1", Op: OpInject, Status: Done}
	b.listing.Leads[1].MessageSpent = true
	r.key("enter")
	r.seen("The daemon holds this epoch's message spent all the same")
	r.notSeen(movedTail)
}

func TestAListReceiptWithWordsIsNotOverriddenByThePress(t *testing.T) {
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) {
		return Receipt{CommandID: "m1", Op: OpInject, Status: Done, Epoch: 3,
			Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}, nil
	}
	r := newRig(t, b, 150, 30)
	r.key("j")
	r.key("m")
	r.typ("hi")
	b.listing.Leads[1].Last = &Receipt{CommandID: "m1", Op: OpInject, Status: Done,
		Outcome: OutcomeSubmitted, Written: WrittenWhole}
	r.key("enter")
	r.seen("message: accepted. the message was submitted")
	r.notSeen("NOT delivered")
}

func TestATicketReceiptKeepsItsFolderAndTicketFileThroughTheList(t *testing.T) {
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) {
		return Receipt{CommandID: "t1", Op: OpTicket, Status: Done, Epoch: 3, Folder: "/run/x/attach", TicketFile: "/run/x/ticket"}, nil
	}
	r := newRig(t, b, 150, 30)
	r.key("j")
	b.listing.Leads[1].Last = &Receipt{CommandID: "t1", Op: OpTicket, Status: Done}
	r.key("w")
	// The attach is made from the answer, before the list is read.
	if len(r.actions) != 1 {
		t.Fatalf("actions %+v", r.actions)
	}
	act := r.actions[0]
	if act.Kind != Attach || act.Attach.Folder != "/run/x/attach" || act.Attach.TicketFile != "/run/x/ticket" || !act.Attach.Writer {
		t.Fatalf("action %+v", act)
	}
	if s := r.m.status; s == nil || s.Folder != "/run/x/attach" || s.TicketFile != "/run/x/ticket" {
		t.Fatalf("the status after the list: %+v", s)
	}
	r.seen("attach ticket: accepted, done")
}

func TestKeepWordsCarriesTheEpochOfThePressWhenTheListHasNone(t *testing.T) {
	press := Receipt{Op: OpInject, Status: Done, Epoch: 3, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing}
	l := Receipt{Op: OpInject, Status: Done}
	keepWords(&l, press)
	if l.Epoch != 3 {
		t.Errorf("the epoch: %+v", l)
	}
	// An epoch of the list's own is kept.
	l = Receipt{Op: OpInject, Status: Done, Epoch: 4}
	keepWords(&l, press)
	if l.Epoch != 4 {
		t.Errorf("the list's epoch was replaced: %+v", l)
	}
	// An unfinished or other receipt takes none.
	l = Receipt{Op: OpTicket, Status: Done}
	keepWords(&l, press)
	if l.Epoch != 0 {
		t.Errorf("another operation took the epoch: %+v", l)
	}
}

func TestKeepWordsTakesNothingFromAnotherOperationOrAnUnfinishedReceipt(t *testing.T) {
	press := Receipt{Op: OpInject, Status: Done, Outcome: OutcomeNotNow, Reason: ReasonNotReady, Written: WrittenNothing, Folder: "f", TicketFile: "t"}
	for name, list := range map[string]Receipt{
		"other op":    {Op: OpTicket, Status: Done},
		"in progress": {Op: OpInject, Status: InProgress},
		"refused":     {Op: OpInject, Status: Refused, Code: "stale-epoch"},
	} {
		l := list
		keepWords(&l, press)
		if l != list {
			t.Errorf("%s: %+v became %+v", name, list, l)
		}
	}
	l := Receipt{Op: OpInject, Status: Done, Written: WrittenWhole}
	keepWords(&l, press)
	if l.Outcome != "" || l.Reason != "" || l.Written != WrittenWhole {
		t.Errorf("a receipt with a word of its own took the press's: %+v", l)
	}
}

func TestAStartingLeadIsReadAgainByTheTickUntilItIsReady(t *testing.T) {
	b := twoLeads(t)
	b.listing.Leads[0].State = "starting"
	b.listing.Leads[0].Epoch = 1
	r := newRig(t, b, 140, 30)
	r.seen("starting")
	if len(r.pending) != 1 {
		t.Fatalf("%d timers: a starting lead is polled", len(r.pending))
	}
	// Two ticks and still starting: one reading and one timer each time.
	for i := 0; i < 2; i++ {
		lists := b.lists
		r.tick()
		if b.lists != lists+1 || len(r.pending) != 1 {
			t.Fatalf("tick %d: lists %d to %d, timers %d", i, lists, b.lists, len(r.pending))
		}
	}
	// The daemon promotes it: the row follows without a key, and no timer is
	// left.
	b.listing.Leads[0].State = "ready"
	r.tick()
	r.seen("ready")
	r.notSeen("starting")
	if len(r.pending) != 0 {
		t.Fatalf("%d timers after no lead was starting", len(r.pending))
	}
	lists := b.lists
	r.tick()
	if b.lists != lists {
		t.Fatal("the list was read with nothing changing by itself")
	}
}

func TestAStoppingLeadIsReadAgainByTheTickUntilItIsStopped(t *testing.T) {
	b := twoLeads(t)
	b.listing.Leads[1].State = "stopping"
	r := newRig(t, b, 140, 30)
	if len(r.pending) != 1 {
		t.Fatalf("%d timers: a stopping lead is polled", len(r.pending))
	}
	b.listing.Leads[1].State = "stopped"
	b.listing.Leads[1].Epoch = 0
	r.tick()
	r.notSeen("stopping")
	if len(r.pending) != 0 {
		t.Fatalf("%d timers after the lead stopped", len(r.pending))
	}
}

func TestStatesThatOnlyARestartOrAKeyChangesAreNotPolled(t *testing.T) {
	for _, state := range []string{"unknown", "stopped", "ended", "ready", "desired", ""} {
		b := twoLeads(t)
		b.listing.Leads[0].State = state
		r := newRig(t, b, 140, 30)
		if len(r.pending) != 0 {
			t.Errorf("state %q: %d timers", state, len(r.pending))
		}
	}
}

func TestAStartThatTurnsToStartingIsFollowedWithoutAKey(t *testing.T) {
	b := twoLeads(t)
	b.answer = func(call) (Receipt, error) { return Receipt{CommandID: "s1", Op: OpStart, Status: Done, Epoch: 1}, nil }
	r := newRig(t, b, 140, 30)
	// The list that is read after the answer shows the lead starting.
	b.listing.Leads[0].State, b.listing.Leads[0].Epoch = "starting", 1
	b.listing.Leads[0].Last = &Receipt{CommandID: "s1", Op: OpStart, Status: Done}
	r.key("s")
	r.seen("starting")
	if len(r.pending) != 1 {
		t.Fatalf("%d timers: the receipt is done but the lead is starting", len(r.pending))
	}
	b.listing.Leads[0].State = "ready"
	r.tick()
	r.seen("ready")
	if len(r.pending) != 0 {
		t.Fatalf("%d timers", len(r.pending))
	}
}
