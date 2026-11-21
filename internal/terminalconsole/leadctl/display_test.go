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
		{"submitted", Receipt{Op: OpInject, Status: Done, Outcome: OutcomeSubmitted}, []string{"message: accepted. the message was submitted"}, nil},
		{"not now", Receipt{Op: OpInject, Status: Done, Outcome: OutcomeNotNow}, []string{"could not take it now; nothing was written"}, nil},
		{"lead refused", Receipt{Op: OpInject, Status: Done, Outcome: OutcomeRefused}, []string{"the lead refused it; nothing was written"}, nil},
		{"uncertain outcome", Receipt{Op: OpInject, Status: Done, Outcome: OutcomeUncertain}, []string{"it may have reached the lead"}, nil},
		{"unknown outcome", Receipt{Op: OpInject, Status: Done, Outcome: "sent!!"}, []string{"the result is not known to this console"}, []string{"sent!!"}},
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
