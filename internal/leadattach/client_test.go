package leadattach_test

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadattach"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
	"ataniq/internal/terminalconsole/sessionpane"
)

func TestHandshakeAndWelcomeReachTheStatus(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	if st := e.c.Status(); st.Conn != terminalconsole.LiveWaitingTicket && st.Conn != terminalconsole.LiveConnecting {
		t.Fatalf("before a ticket: %v", st.Conn)
	}
	e.tickets.give(1)
	hc := e.h.accept()

	if hc.hello.Version != wire.ProtocolVersion || hc.hello.Ticket != e.tickets.ticket(0) {
		t.Errorf("hello version/ticket: %+v", hc.hello)
	}
	if hc.hello.Name != "test console" || hc.hello.Columns != 80 || hc.hello.Rows != 24 {
		t.Errorf("hello name and size: %+v", hc.hello)
	}
	want := wire.Colors{Foreground: "#ffffff", Background: "#000000", Cursor: "#ffffff"}
	if hc.hello.Colors != want {
		t.Errorf("hello colours %+v, want %+v", hc.hello.Colors, want)
	}
	if st := e.until("connecting", func(st terminalconsole.LiveStatus) bool { return st.Conn == terminalconsole.LiveConnecting }); st.Role != "" {
		t.Errorf("role before welcome: %q", st.Role)
	}

	hc.welcome(wire.RoleWriter)
	// The Welcome fills the status, but the client is attached only once the
	// first snapshot is applied.
	st := e.until("the welcome", func(st terminalconsole.LiveStatus) bool { return st.Lead != "" })
	if st.Conn != terminalconsole.LiveConnecting {
		t.Errorf("state after the welcome alone: %v", st.Conn)
	}
	hc.snapshot()
	st = e.untilConn(terminalconsole.LiveAttached)
	if st.Role != terminalconsole.LiveWriter || st.Lead != "lead-one" || st.Epoch != 3 || st.Runtime != "claude" || st.Version != "1.2.3" {
		t.Errorf("status after welcome: %+v", st)
	}
	if st.Reason != "" {
		t.Errorf("reason while attached: %q", st.Reason)
	}
}

func TestHelloCarriesTheLatestSizeAsked(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	e.c.Resize(120, 40) // not attached: nothing is sent, the size is remembered
	e.tickets.give(1)
	hc := e.h.accept()
	if hc.hello.Columns != 120 || hc.hello.Rows != 40 {
		t.Errorf("hello size %dx%d, want 120x40", hc.hello.Columns, hc.hello.Rows)
	}
}

func TestWaitingForATicketPollsEveryHalfSecond(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	st := e.untilConn(terminalconsole.LiveWaitingTicket)
	if st.Reason != "waiting for a ticket" {
		t.Errorf("reason %q", st.Reason)
	}
	e.until("a few polls", func(terminalconsole.LiveStatus) bool { return e.tickets.callCount() >= 3 })
	for _, d := range e.clock.asked() {
		if d != 500*time.Millisecond {
			t.Fatalf("waited %v between polls, want 500ms", d)
		}
	}
	e.h.noConnection(30 * time.Millisecond)

	e.tickets.give(1)
	hc := e.h.accept()
	hc.welcome(wire.RoleWriter)
	hc.snapshot()
	e.untilConn(terminalconsole.LiveAttached)
}

func TestATicketSourceThatFailsShowsWhy(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter, noStart: true})
	e.tickets.err = errTest
	e.start()
	st := e.until("the reason", func(st terminalconsole.LiveStatus) bool {
		return st.Conn == terminalconsole.LiveWaitingTicket && strings.Contains(st.Reason, "test error")
	})
	if !strings.HasPrefix(st.Reason, "waiting for a ticket") {
		t.Errorf("reason %q", st.Reason)
	}
}

func TestAMalformedTicketIsNotUsed(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter, noStart: true, mutate: func(c *leadattach.Config) {
		c.Tickets = func() (string, error) { return "not a ticket", nil }
	}})
	e.start()
	e.until("the reason", func(st terminalconsole.LiveStatus) bool {
		return st.Conn == terminalconsole.LiveWaitingTicket && strings.Contains(st.Reason, "malformed")
	})
	e.h.noConnection(30 * time.Millisecond)
}

func TestSnapshotThenOutputIsShownInThePane(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer})
	e.tickets.give(1)
	hc := e.h.accept()
	hc.pane.Write([]byte("\x1b[2J\x1b[Hbefore\x1b[1;31m red"))
	hc.pos = 100
	hc.welcome(wire.RoleViewer)
	hc.snapshot()
	e.until("the snapshot on screen", func(terminalconsole.LiveStatus) bool { return strings.Contains(paneText(e.pane), "before") })
	hc.write("\r\nafter the snapshot")
	e.until("the output on screen", func(terminalconsole.LiveStatus) bool { return strings.Contains(paneText(e.pane), "after the snapshot") })
	e.sameScreen(hc)
	hc.check()
	hc.write(" and more")
	e.sameScreen(hc)
	st := e.c.Status()
	if st.CheckMismatches != 0 || st.Resyncs != 0 {
		t.Errorf("mismatches %d resyncs %d after a matching check", st.CheckMismatches, st.Resyncs)
	}
	if st.Cols != 80 || st.Rows != 24 {
		t.Errorf("size %dx%d", st.Cols, st.Rows)
	}
}

func TestAPositionGapReattaches(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer})
	hc := e.attach(wire.RoleViewer)
	hc.write("one")
	e.sameScreen(hc)

	// The holder skips ahead: bytes 3..9 never reach the client.
	hc.pos += 7
	hc.write("lost the middle")
	e.tickets.give(1)
	types := hc.closed()
	_ = types

	hc2 := e.h.accept()
	if hc2.hello.Ticket != e.tickets.ticket(1) || hc2.hello.Ticket == hc.hello.Ticket {
		t.Errorf("the second connection used ticket %q", hc2.hello.Ticket)
	}
	hc2.pane.Write([]byte("fresh screen"))
	hc2.pos = 500
	hc2.welcome(wire.RoleViewer)
	hc2.snapshot()
	e.until("the new snapshot", func(st terminalconsole.LiveStatus) bool {
		return st.Conn == terminalconsole.LiveAttached && strings.Contains(paneText(e.pane), "fresh screen")
	})
	e.sameScreen(hc2)
	st := e.c.Status()
	if st.Resyncs != 1 || st.CheckMismatches != 1 || counter(st, "client-position-gaps") != 1 {
		t.Errorf("resyncs %d, mismatches %d, gaps %d", st.Resyncs, st.CheckMismatches, counter(st, "client-position-gaps"))
	}
	if ns := e.c.Notices(); len(ns) == 0 || ns[len(ns)-1].Code != "position-mismatch" {
		t.Errorf("notices %+v", ns)
	}
}

func TestCheckMismatchReattachesAndTheNewSnapshotIsEqual(t *testing.T) {
	// Case A7: the holder applies different bytes than it sends, so one cell
	// differs; the next Check does not match; the client reattaches with a
	// second ticket and is equal after the new snapshot.
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer})
	hc := e.attach(wire.RoleViewer)
	hc.corrupt("\x1b[2J\x1b[Hhello X world", "\x1b[2J\x1b[Hhello Y world")
	hc.check()
	st := e.until("the mismatch", func(st terminalconsole.LiveStatus) bool { return st.CheckMismatches == 1 })
	if st.Conn != terminalconsole.LiveResyncing && st.Conn != terminalconsole.LiveAttached {
		t.Errorf("state after the mismatch: %v", st.Conn)
	}
	e.tickets.give(1)
	hc.closed()

	hc2 := e.h.accept()
	if hc2.hello.Ticket == hc.hello.Ticket {
		t.Error("the ticket was used twice")
	}
	hc2.pane.Write([]byte("\x1b[2J\x1b[Hhello X world"))
	hc2.pos = hc.pos
	hc2.welcome(wire.RoleViewer)
	if st := e.until("resyncing", func(st terminalconsole.LiveStatus) bool { return st.Conn == terminalconsole.LiveResyncing }); st.Lead != "lead-one" {
		t.Errorf("status while resyncing: %+v", st)
	}
	hc2.snapshot()
	st = e.untilConn(terminalconsole.LiveAttached)
	e.sameScreen(hc2)
	hc2.check()
	hc2.write("!")
	e.sameScreen(hc2)
	st = e.c.Status()
	if st.CheckMismatches != 1 || st.Resyncs != 1 {
		t.Errorf("mismatches %d, resyncs %d", st.CheckMismatches, st.Resyncs)
	}
	if !strings.Contains(paneText(e.pane), "hello X world!") {
		t.Errorf("screen %q", paneText(e.pane))
	}
	var seen bool
	for _, n := range e.c.Notices() {
		seen = seen || n.Code == "check-mismatch"
	}
	if !seen {
		t.Error("no check-mismatch notice")
	}
}

func TestACheckForAnotherPositionIsSkipped(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer})
	hc := e.attach(wire.RoleViewer)
	hc.write("abc")
	hc.checkAt(hc.pos + 5)
	hc.write("def")
	hc.check()
	e.until("the skipped check", func(st terminalconsole.LiveStatus) bool { return counter(st, "client-checks-skipped") == 1 })
	e.sameScreen(hc)
	if st := e.c.Status(); st.CheckMismatches != 0 || st.Conn != terminalconsole.LiveAttached {
		t.Errorf("status %+v", st)
	}
}

func TestAHolderThatReplacesTheScreenIsCountedAsAResync(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer})
	hc := e.attach(wire.RoleViewer)
	hc.write("before")
	hc.notice(wire.ReasonResync, "output dropped: the client was too slow")
	hc.pane.Write([]byte("\x1b[2J\x1b[Hnew"))
	hc.pos += 50
	hc.snapshot()
	e.until("the second snapshot", func(st terminalconsole.LiveStatus) bool { return st.Resyncs == 1 })
	e.sameScreen(hc)
	hc.write("!")
	e.sameScreen(hc)
}

func TestModesDriveTheStatusModes(t *testing.T) {
	// Case A6: a session with mouse modes on.
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)

	hc.modes([]int{1, 1000, 1006, 2004}, nil)
	st := e.until("modes", func(st terminalconsole.LiveStatus) bool { return st.Modes.Mouse != 0 })
	want := sessionpane.Modes{Mouse: 2, MouseEncoding: 1, BracketedPaste: true}
	if st.Modes != want {
		t.Errorf("modes %+v, want %+v", st.Modes, want)
	}

	for _, c := range []struct {
		dec  []int
		want sessionpane.Modes
	}{
		{[]int{9}, sessionpane.Modes{Mouse: 1}},
		{[]int{1000, 1001}, sessionpane.Modes{Mouse: 2}},
		{[]int{1001}, sessionpane.Modes{Mouse: 2}},
		{[]int{1002}, sessionpane.Modes{Mouse: 3}},
		{[]int{9, 1000, 1002, 1003}, sessionpane.Modes{Mouse: 4}},
		{[]int{1003, 1004, 1006}, sessionpane.Modes{Mouse: 4, MouseEncoding: 1, FocusEvents: true}},
		{[]int{1, 25, 66, 67, 1049, 2026}, sessionpane.Modes{}},
	} {
		hc.modes(c.dec, nil)
		e.until("modes", func(st terminalconsole.LiveStatus) bool { return st.Modes == c.want })
	}
}

func TestModesComeFromTheSnapshotWhenNoModesFrameCame(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	e.tickets.give(1)
	hc := e.h.accept()
	hc.pane.Write([]byte("\x1b[?1002h\x1b[?1006h\x1b[?2004h"))
	hc.welcome(wire.RoleWriter)
	hc.snapshot()
	st := e.until("modes", func(st terminalconsole.LiveStatus) bool { return st.Modes.Mouse != 0 })
	want := sessionpane.Modes{Mouse: 3, MouseEncoding: 1, BracketedPaste: true}
	if st.Modes != want {
		t.Errorf("modes %+v, want %+v", st.Modes, want)
	}
	// A Modes frame wins over the snapshot's.
	hc.modes(nil, nil)
	e.until("modes off", func(st terminalconsole.LiveStatus) bool { return st.Modes == sessionpane.Modes{} })
}

func TestSizeResizesThePaneMidStream(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer})
	hc := e.attach(wire.RoleViewer)
	hc.write("0123456789")
	hc.pane.Resize(100, 30)
	hc.size(100, 30)
	st := e.until("the new size", func(st terminalconsole.LiveStatus) bool { return st.Cols == 100 && st.Rows == 30 })
	if c, r := e.pane.Size(); c != 100 || r != 30 {
		t.Errorf("pane size %dx%d", c, r)
	}
	hc.write("\r\nafter")
	hc.check()
	hc.write("!")
	e.sameScreen(hc)
	if st = e.c.Status(); st.CheckMismatches != 0 {
		t.Errorf("mismatches %d", st.CheckMismatches)
	}
}

func TestStateFrameSetsTheFactsWithoutEndingThePane(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer})
	hc := e.attach(wire.RoleViewer)
	status := 3
	hc.send(wire.TypeState, enc(t)(wire.EncodeState(wire.State{
		Facts:      wire.Facts{StreamEnded: wire.FactNo, TUIExited: wire.FactYes, TreeGone: wire.FactUnknown},
		ExitStatus: &status, Idle: "busy", Title: "a title",
	})))
	st := e.until("the state", func(st terminalconsole.LiveStatus) bool { return st.Title == "a title" })
	want := terminalconsole.LiveEnd{Stream: terminalconsole.FactNo, TUIExited: terminalconsole.FactYes, TreeGone: terminalconsole.FactUnknown, ExitStatus: 3, ExitStatusKnown: true}
	if st.End != want || st.Idle != "busy" {
		t.Errorf("end %+v idle %q", st.End, st.Idle)
	}
	if ended, _, _ := e.pane.Ended(); ended {
		t.Error("a State frame ended the pane")
	}
	hc.write("still writing")
	e.sameScreen(hc)
}

func TestRoleChangeUpdatesTheStatusAndTheHoldersNoticeIsTheOnlyNotice(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)
	hc.roleFrame(wire.RoleWriter, "test console")
	hc.roleFrame(wire.RoleViewer, "other console")
	st := e.until("viewer", func(st terminalconsole.LiveStatus) bool { return st.Role == terminalconsole.LiveViewer })
	if st.Conn != terminalconsole.LiveAttached {
		t.Errorf("state %v", st.Conn)
	}
	if ns := e.c.Notices(); len(ns) != 0 {
		t.Fatalf("a Role frame made notices: %+v", ns)
	}

	// The holder follows a Role frame with one Notice: one notice for the
	// writer change, naming the new writer from the Role frame.
	hc.notice(wire.ReasonWriterChanged, "the writer is now other console")
	ns := e.notices(1)
	if len(ns) != 1 || ns[0].Code != terminalconsole.NoticeWriterChanged || ns[0].Client != "other console" {
		t.Fatalf("notices %+v", ns)
	}
	if again := e.c.Notices(); len(again) != 0 {
		t.Errorf("notices are kept after being taken: %+v", again)
	}

	// A release is the same: the Role frame is silent, the holder's Notice
	// is the one notice, with its own text.
	hc.roleFrame(wire.RoleViewer, "")
	hc.notice(wire.ReasonWriterReleased, "the writer was released")
	hc.notice(wire.Reason("barrier"), "")
	ns = e.notices(2)
	if len(ns) != 2 || ns[0].Code != "writer-released" || ns[0].Text != "the writer was released" || ns[1].Code != "barrier" {
		t.Errorf("notices after a release: %+v", ns)
	}
}

func TestNoticeReasonsAreMapped(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)
	hc.roleFrame(wire.RoleWriter, "me")
	hc.roleFrame(wire.RoleViewer, "someone")
	e.until("role", func(st terminalconsole.LiveStatus) bool { return st.Role == terminalconsole.LiveViewer })
	e.c.Notices()

	for _, c := range []struct {
		reason wire.Reason
		text   string
		want   terminalconsole.LiveNotice
	}{
		{wire.ReasonKeyHeldSuspend, "Ctrl+Z was not sent", terminalconsole.LiveNotice{Code: terminalconsole.NoticeHeldKey, Text: "Ctrl+Z was not sent"}},
		{wire.ReasonKeyHeldSignal, "", terminalconsole.LiveNotice{Code: terminalconsole.NoticeHeldKey}},
		{wire.ReasonViewerInputRefused, "viewers do not type", terminalconsole.LiveNotice{Code: terminalconsole.NoticeViewerRefused, Text: "viewers do not type"}},
		{wire.ReasonWriterChanged, "ignored text", terminalconsole.LiveNotice{Code: terminalconsole.NoticeWriterChanged, Client: "someone"}},
		{wire.ReasonTooSlow, "", terminalconsole.LiveNotice{Code: terminalconsole.NoticeTooSlow}},
		{wire.ReasonResync, "a new snapshot follows", terminalconsole.LiveNotice{Code: "resync", Text: "a new snapshot follows"}},
		{wire.Reason("future-reason"), "from a newer holder", terminalconsole.LiveNotice{Code: "future-reason", Text: "from a newer holder"}},
	} {
		hc.notice(c.reason, c.text)
		var got []terminalconsole.LiveNotice
		e.until(string(c.reason), func(terminalconsole.LiveStatus) bool {
			got = append(got, e.c.Notices()...)
			return len(got) > 0
		})
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("%s: notices %+v, want %+v", c.reason, got, c.want)
		}
	}
	if st := e.c.Status(); st.RefusedInput != 1 {
		t.Errorf("refused input %d, want 1 (the viewer-refused notice)", st.RefusedInput)
	}
}

func TestNoticesAreBoundedAndTheLossCounted(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer})
	hc := e.attach(wire.RoleViewer)
	for i := 0; i < 200; i++ {
		hc.notice(wire.ReasonResync, "notice "+strings.Repeat("x", i%7)+string(rune('a'+i%26))+string(rune('A'+i/26)))
	}
	hc.write("sync")
	e.sameScreen(hc)
	ns := e.c.Notices()
	if len(ns) == 0 || len(ns) > 64 {
		t.Fatalf("%d notices kept, want 1 to 64", len(ns))
	}
	if got := counter(e.c.Status(), "client-notices-dropped"); got != uint64(200-len(ns)) {
		t.Errorf("dropped %d, kept %d of 200", got, len(ns))
	}
}

func TestExitEndsThePaneWithTheFacts(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)
	hc.write("last words")
	status := 3
	hc.send(wire.TypeExit, enc(t)(wire.EncodeExit(wire.Exit{
		Facts:      wire.Facts{StreamEnded: wire.FactYes, TUIExited: wire.FactYes, TreeGone: wire.FactNo},
		ExitStatus: &status,
		Counters:   map[string]uint64{"b-second": 2, "a-first": 1},
		LastScreen: "last words\n",
	})))
	st := e.until("the end", func(st terminalconsole.LiveStatus) bool { return st.Conn == terminalconsole.LiveDisconnected })
	if st.Reason != "session ended" {
		t.Errorf("reason %q", st.Reason)
	}
	want := terminalconsole.LiveEnd{Stream: terminalconsole.FactYes, TUIExited: terminalconsole.FactYes, TreeGone: terminalconsole.FactNo, ExitStatus: 3, ExitStatusKnown: true}
	if st.End != want || !st.End.Any() {
		t.Errorf("end %+v", st.End)
	}
	ended, code, known := e.pane.Ended()
	if !ended || code != 3 || !known {
		t.Errorf("pane ended %v status %d known %v", ended, code, known)
	}
	if st.Role != terminalconsole.LiveWriter {
		t.Errorf("the role was cleared: %q", st.Role)
	}
	n := len(st.Counters)
	if n < 2 || st.Counters[n-2] != (terminalconsole.LiveCounter{Name: "a-first", Value: 1}) || st.Counters[n-1] != (terminalconsole.LiveCounter{Name: "b-second", Value: 2}) {
		t.Errorf("counters %+v", st.Counters)
	}
	if e.c.LastScreen() != "last words\n" {
		t.Errorf("last screen %q", e.c.LastScreen())
	}
	// No reattach: no ticket is asked for any more.
	calls := e.tickets.callCount()
	e.tickets.give(1)
	e.h.noConnection(60 * time.Millisecond)
	if e.tickets.callCount() != calls {
		t.Error("the client asked for a ticket after the session ended")
	}
	// An exit with no known status.
	e2 := newEnv(t, setup{mode: leadattach.ModeViewer})
	hc2 := e2.attach(wire.RoleViewer)
	hc2.send(wire.TypeExit, enc(t)(wire.EncodeExit(wire.Exit{Facts: wire.Facts{StreamEnded: wire.FactYes, TUIExited: wire.FactUnknown, TreeGone: wire.FactUnknown}})))
	e2.until("the end", func(st terminalconsole.LiveStatus) bool { return st.Conn == terminalconsole.LiveDisconnected })
	if _, _, known := e2.pane.Ended(); known {
		t.Error("an exit with no status has a known status")
	}
}

func TestACutConnectionGoesBackToWaitingForATicket(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)
	hc.nc.Close()
	st := e.until("waiting", func(st terminalconsole.LiveStatus) bool { return st.Conn == terminalconsole.LiveWaitingTicket })
	if !strings.HasPrefix(st.Reason, "waiting for a ticket") || !strings.Contains(st.Reason, "closed") {
		t.Errorf("reason %q", st.Reason)
	}
	if st.Role != terminalconsole.LiveWriter || st.Lead != "lead-one" {
		t.Errorf("the status was cleared: %+v", st)
	}
	hc2 := e.attach(wire.RoleWriter)
	_ = hc2
}

func TestRefusals(t *testing.T) {
	for _, c := range []struct {
		reason  wire.Reason
		stops   bool
		reason2 string
	}{
		{wire.ReasonRefusedTicket, false, "refused the ticket"},
		{wire.ReasonRefusedBusy, false, "busy"},
		{wire.ReasonRefusedVersion, true, "another protocol version"},
	} {
		t.Run(string(c.reason), func(t *testing.T) {
			leakCheck(t)
			e := newEnv(t, setup{mode: leadattach.ModeWriter, tickets: 1})
			hc := e.h.accept()
			hc.notice(c.reason, "details of the refusal")
			if c.stops {
				st := e.until("stopped", func(st terminalconsole.LiveStatus) bool { return st.Conn == terminalconsole.LiveDisconnected })
				if !strings.Contains(st.Reason, c.reason2) {
					t.Errorf("reason %q", st.Reason)
				}
				calls := e.tickets.callCount()
				e.tickets.give(1)
				e.h.noConnection(60 * time.Millisecond)
				if e.tickets.callCount() != calls {
					t.Error("the client went on after refused-version")
				}
				return
			}
			st := e.until("waiting", func(st terminalconsole.LiveStatus) bool {
				return st.Conn == terminalconsole.LiveWaitingTicket && strings.Contains(st.Reason, c.reason2)
			})
			if !strings.Contains(st.Reason, "details of the refusal") && c.reason != wire.ReasonRefusedTicket {
				t.Logf("reason %q", st.Reason)
			}
			hc.closed()
			// A new ticket gives a new attempt.
			e.tickets.give(1)
			hc2 := e.h.accept()
			hc2.welcome(wire.RoleWriter)
			hc2.snapshot()
			e.untilConn(terminalconsole.LiveAttached)
		})
	}
}

func TestAWelcomeOfAnotherVersionStops(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter, tickets: 1})
	hc := e.h.accept()
	// A newer holder's Welcome has a field this client's decoder does not know.
	hc.send(wire.TypeWelcome, []byte(`{"version":2,"role":"writer","lead":"x","epoch":1,"surprise":true}`))
	st := e.until("stopped", func(st terminalconsole.LiveStatus) bool { return st.Conn == terminalconsole.LiveDisconnected })
	if !strings.Contains(st.Reason, "version 2") {
		t.Errorf("reason %q", st.Reason)
	}
	e.tickets.give(1)
	e.h.noConnection(60 * time.Millisecond)
}

func TestDialFailureShowsAndRetries(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer, tickets: 1, noStart: true})
	e.h.ln.Close()
	e.start()
	st := e.until("cannot connect", func(st terminalconsole.LiveStatus) bool {
		return strings.Contains(st.Reason, "cannot connect")
	})
	if st.Conn != terminalconsole.LiveDisconnected && st.Conn != terminalconsole.LiveWaitingTicket {
		t.Errorf("state %v", st.Conn)
	}
	if strings.Contains(st.Reason, e.h.path) {
		t.Errorf("the reason names the socket path: %q", st.Reason)
	}
}

func TestHostileHolders(t *testing.T) {
	header := func(typ byte, n uint32) []byte {
		b := make([]byte, 5)
		b[0] = typ
		binary.BigEndian.PutUint32(b[1:], n)
		return b
	}
	for _, c := range []struct {
		name  string
		early bool            // send after the Welcome alone, before any snapshot
		after func(hc *hconn) // sent after the Welcome
		raw   func(hc *hconn) // raw bytes, instead of a Welcome
		want  string
	}{
		{name: "an Output over its cap", raw: func(hc *hconn) { hc.nc.Write(header(wire.TypeOutput, wire.MaxHolderPayload+1)) }, want: "over its cap"},
		{name: "a Snapshot over its cap", raw: func(hc *hconn) { hc.nc.Write(header(wire.TypeSnapshot, wire.MaxSnapshotPayload+1)) }, want: "over its cap"},
		{name: "an unknown type", raw: func(hc *hconn) { hc.nc.Write(header('X', 3)) }, want: "unknown type"},
		{name: "a client frame type", raw: func(hc *hconn) { hc.nc.Write(header(wire.TypeInput, 3)) }, want: "unknown type"},
		{name: "a Welcome that is not JSON", raw: func(hc *hconn) { hc.nc.Write(append(header(wire.TypeWelcome, 4), "nope"...)) }, want: "bad welcome"},
		{name: "a frame before the Welcome", raw: func(hc *hconn) {
			hc.send(wire.TypeModes, enc(hc.t)(wire.EncodeModes(wire.Modes{})))
		}, want: "before the welcome"},
		{name: "a second Welcome", after: func(hc *hconn) { hc.welcome(wire.RoleWriter) }, want: "out of order"},
		{name: "a bad snapshot", after: func(hc *hconn) {
			hc.send(wire.TypeSnapshot, enc(hc.t)(wire.EncodeSnapshot(0, []byte("not a snapshot"))))
		}, want: "snapshot the pane refused"},
		{name: "output before a snapshot", early: true, after: func(hc *hconn) {
			hc.send(wire.TypeOutput, enc(hc.t)(wire.EncodeOutput(0, []byte("x"))))
		}, want: "before a snapshot"},
		{name: "a short Output", after: func(hc *hconn) { hc.nc.Write(append(header(wire.TypeOutput, 3), 1, 2, 3)) }, want: "too short"},
		{name: "a Modes frame with an unknown field", after: func(hc *hconn) { hc.send(wire.TypeModes, []byte(`{"dec":[],"ansi":[],"x":1}`)) }, want: "bad modes"},
		{name: "a State frame with a bad fact", after: func(hc *hconn) {
			hc.send(wire.TypeState, []byte(`{"facts":{"stream_ended":"maybe","tui_exited":"no","tree_gone":"no"},"idle":"x","idle_signals":[],"title":""}`))
		}, want: "bad state"},
		{name: "a frame cut in the middle", after: func(hc *hconn) {
			hc.nc.Write(append(header(wire.TypeNotice, 100), "{"...))
			hc.nc.Close()
		}, want: "broke inside a frame"},
	} {
		t.Run(c.name, func(t *testing.T) {
			leakCheck(t)
			e := newEnv(t, setup{mode: leadattach.ModeWriter, tickets: 1})
			hc := e.h.accept()
			if c.raw != nil {
				c.raw(hc)
			} else if c.after != nil {
				hc.welcome(wire.RoleWriter)
				if c.early {
					e.until("the welcome", func(st terminalconsole.LiveStatus) bool { return st.Lead != "" })
				} else {
					hc.snapshot()
					e.untilConn(terminalconsole.LiveAttached)
				}
				c.after(hc)
			} else {
				return
			}
			st := e.until("the disconnect", func(st terminalconsole.LiveStatus) bool {
				return st.Conn == terminalconsole.LiveWaitingTicket || st.Conn == terminalconsole.LiveDisconnected
			})
			if !strings.Contains(st.Reason, c.want) {
				t.Errorf("reason %q, want it to say %q", st.Reason, c.want)
			}
			// The client lives on and asks for a ticket (after its backoff).
			e.tickets.give(1)
			hc2 := e.h.accept()
			hc2.welcome(wire.RoleWriter)
			hc2.snapshot()
			e.untilConn(terminalconsole.LiveAttached)
		})
	}
}

func TestAHolderThatSendsBadFramesIsBackedOff(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer})
	for i := 0; i < 3; i++ {
		e.tickets.give(1)
		hc := e.h.accept()
		hc.nc.Write([]byte{'X', 0, 0, 0, 1, 0})
		hc.closed()
	}
	var waits []time.Duration
	for _, d := range e.clock.asked() {
		if d > leadattach.TicketPoll {
			waits = append(waits, d)
		}
	}
	if len(waits) < 2 || waits[0] != 2*leadattach.TicketPoll || waits[1] != 4*leadattach.TicketPoll {
		t.Errorf("waits after protocol errors: %v", waits)
	}
}

func TestCloseSendsDetachAndLeavesNothingBehind(t *testing.T) {
	for _, mode := range []leadattach.Mode{leadattach.ModeWriter, leadattach.ModeViewer} {
		t.Run(string(mode), func(t *testing.T) {
			leakCheck(t)
			e := newEnv(t, setup{mode: mode})
			hc := e.attach(wire.Role(mode))
			e.c.Close()
			e.c.Close() // twice
			types := hc.closed()
			if string(types) != string(wire.TypeDetach) {
				t.Errorf("frames before the close: %q, want only a detach", types)
			}
			if st := e.c.Status(); st.Conn != terminalconsole.LiveDisconnected {
				t.Errorf("state after close: %v", st.Conn)
			}
		})
	}
}

func TestCloseWhileWaitingConnectingAndBeforeStart(t *testing.T) {
	leakCheck(t)
	// Before Start.
	e := newEnv(t, setup{noStart: true})
	e.c.Close()
	if _, err := e.c.Start(terminalconsole.LiveStart{Pane: e.pane}); err == nil {
		t.Error("a closed client started")
	}
	// While waiting for a ticket.
	e = newEnv(t, setup{mode: leadattach.ModeWriter})
	e.untilConn(terminalconsole.LiveWaitingTicket)
	e.c.Close()
	// While the holder has not answered the Hello.
	e = newEnv(t, setup{mode: leadattach.ModeWriter, tickets: 1})
	e.h.accept()
	e.c.Close()
	// While the holder is mid-way through a frame.
	e = newEnv(t, setup{mode: leadattach.ModeWriter, tickets: 1})
	hc := e.h.accept()
	hc.nc.Write([]byte{wire.TypeWelcome, 0, 0, 0, 50, '{'})
	e.c.Close()
}

func TestStartRefusesWhatItCannotUse(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{noStart: true})
	if _, err := e.c.Start(terminalconsole.LiveStart{}); err == nil {
		t.Error("no pane")
	}
	if _, err := leadattach.New(leadattach.Config{Tickets: e.tickets.get}).Start(terminalconsole.LiveStart{Pane: e.pane}); err == nil {
		t.Error("no socket")
	}
	if _, err := leadattach.New(leadattach.Config{Socket: "x"}).Start(terminalconsole.LiveStart{Pane: e.pane}); err == nil {
		t.Error("no ticket source")
	}
	e.start()
	if _, err := e.c.Start(terminalconsole.LiveStart{Pane: e.pane}); err == nil {
		t.Error("started twice")
	}
}

func TestTheHandshakeHasATimeLimit(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter, tickets: 1, handshake: 50 * time.Millisecond})
	e.h.accept() // never answers
	e.until("the timeout", func(st terminalconsole.LiveStatus) bool {
		return strings.Contains(st.Reason, "did not answer")
	})
}

func TestWakeIsCalledAfterChangesAndNeverUnderTheStatusLock(t *testing.T) {
	// The wake callback of the harness reads Status; with -race and a holder
	// that sends a stream, a call under the lock would deadlock.
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)
	for i := 0; i < 300; i++ {
		hc.write("line of output\r\n")
		if i%50 == 0 {
			hc.check()
			hc.modes([]int{1000}, nil)
			hc.roleFrame(wire.RoleWriter, "me")
		}
	}
	e.sameScreen(hc)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			e.c.Status()
			e.c.Notices()
			e.sink.Key(keyA)
		}
	}()
	select {
	case <-done:
	case <-time.After(wait):
		t.Fatal("deadlock")
	}
}

// mismatch makes the holder send a Check the client's screen does not match,
// and waits until the client has given the connection up.
func (hc *hconn) mismatch() {
	hc.t.Helper()
	hc.corrupt("A", "B")
	hc.check()
	hc.closed()
}

// reattach welcomes the connection the client made next, with a snapshot of
// the holder's screen, and waits for the client to be attached.
func (e *env) reattach() *hconn {
	e.t.Helper()
	hc := e.h.accept()
	hc.welcome(wire.RoleViewer)
	hc.snapshot()
	e.untilConn(terminalconsole.LiveAttached)
	return hc
}

// heldClock holds every wait longer than a ticket poll until the test
// releases it, and records it like the scaled clock does.
type heldClock struct {
	base   *scaledClock
	timers chan chan time.Time
}

type heldTimer struct{ ch chan time.Time }

func (h heldTimer) C() <-chan time.Time { return h.ch }
func (h heldTimer) Stop() bool          { return true }

func (c *heldClock) NewTimer(d time.Duration) leadattach.Timer {
	if d <= leadattach.TicketPoll {
		return c.base.NewTimer(d)
	}
	c.base.mu.Lock()
	c.base.durs = append(c.base.durs, d)
	c.base.mu.Unlock()
	ch := make(chan time.Time, 1)
	c.timers <- ch
	return heldTimer{ch}
}

func TestRepeatedResyncsBackOffUntilACheckMatches(t *testing.T) {
	leakCheck(t)
	var held *heldClock
	e := newEnv(t, setup{mode: leadattach.ModeViewer, tickets: 8, mutate: func(c *leadattach.Config) {
		held = &heldClock{base: c.Clock.(*scaledClock), timers: make(chan chan time.Time, 8)}
		c.Clock = held
	}})
	// release lets the held wait of the client go, once it has begun.
	release := func() {
		t.Helper()
		select {
		case ch := <-held.timers:
			ch <- time.Now()
		case <-time.After(wait):
			t.Fatal("the client did not wait")
		}
	}
	hc := e.reattach()
	waits := func() []time.Duration { return e.clock.asked() } // tickets are ready: only backoff waits are asked

	// The first mismatch is followed at once.
	hc.mismatch()
	hc = e.reattach()
	if w := waits(); len(w) != 0 {
		t.Fatalf("waited %v after the first resync", w)
	}
	// From the second, the wait doubles, and the reason says so.
	hc.mismatch()
	st := e.until("the wait", func(st terminalconsole.LiveStatus) bool {
		return st.Conn == terminalconsole.LiveResyncing && strings.Contains(st.Reason, "trying again in 1s")
	})
	if !strings.HasPrefix(st.Reason, "check mismatch") {
		t.Errorf("reason %q", st.Reason)
	}
	e.h.noConnection(30 * time.Millisecond) // held: no connection meanwhile
	release()
	hc = e.reattach()
	hc.mismatch()
	release()
	hc = e.reattach()
	want := []time.Duration{2 * leadattach.TicketPoll, 4 * leadattach.TicketPoll}
	if w := waits(); len(w) != 2 || w[0] != want[0] || w[1] != want[1] {
		t.Fatalf("waits %v, want %v", w, want)
	}

	// A snapshot alone does not end the back-off, a matching Check does.
	hc.write("ok")
	hc.check()
	e.until("the check", func(terminalconsole.LiveStatus) bool { return e.pane.Digest() == hc.pane.Digest() })
	e.barrier(hc)
	hc.mismatch() // the first resync again: no wait
	hc = e.reattach()
	if w := waits(); len(w) != 2 {
		t.Errorf("waits %v: a mismatch after a matching check was waited for", w)
	}
	hc.mismatch()
	release()
	e.reattach()
	if w := waits(); len(w) != 3 || w[2] != 2*leadattach.TicketPoll {
		t.Errorf("waits %v, want the back-off to start again at 1s", w)
	}
}
