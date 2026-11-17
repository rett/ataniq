package leadattach_test

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"ataniq/internal/leadattach"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
)

// barrier returns when the client has applied every frame the holder sent so
// far: a notice is the last of them, and the client applies frames in order.
func (e *env) barrier(hc *hconn) {
	e.t.Helper()
	hc.notice(wire.Reason("barrier"), "")
	e.until("the barrier", func(terminalconsole.LiveStatus) bool {
		for _, n := range e.c.Notices() {
			if n.Code == "barrier" {
				return true
			}
		}
		return false
	})
}

// input reads Input frames until n bytes have come, and returns them.
func (hc *hconn) input(n int) string {
	hc.t.Helper()
	var b strings.Builder
	for b.Len() < n {
		f := hc.next()
		if f.typ != wire.TypeInput {
			hc.t.Fatalf("got a %s frame, want input", wire.TypeName(f.typ))
		}
		b.Write(f.payload)
	}
	return b.String()
}

func TestWriterKeysArriveAsInputFramesForTheHoldersModes(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)

	e.sink.Key(keyA)
	e.sink.Key(key(uv.KeyUp, 0, ""))
	if got := hc.input(4); got != "a\x1b[A" {
		t.Errorf("without modes: %q", got)
	}

	hc.modes([]int{1}, nil) // application cursor keys
	e.barrier(hc)
	e.sink.Key(key(uv.KeyUp, 0, ""))
	e.sink.Key(key(uv.KeyHome, 0, ""))
	if got := hc.input(6); got != "\x1bOA\x1bOH" {
		t.Errorf("application cursor keys: %q", got)
	}

	hc.modes([]int{1, 66}, nil)
	e.barrier(hc)
	e.sink.Key(key(uv.KeyKp5, 0, ""))
	if got := hc.input(3); got != "\x1bOu" {
		t.Errorf("application keypad: %q", got)
	}

	hc.modes(nil, nil)
	e.barrier(hc)
	e.sink.Key(key(uv.KeyUp, 0, ""))
	e.sink.Key(key('z', ctrl, ""))
	if got := hc.input(4); got != "\x1b[A\x1a" {
		t.Errorf("modes off again: %q", got)
	}
	if st := e.c.Status(); st.RefusedInput != 0 {
		t.Errorf("refused input %d", st.RefusedInput)
	}
}

func TestShiftEnterOptionIsUsed(t *testing.T) {
	for opt, want := range map[leadattach.ShiftEnter]string{
		leadattach.ShiftEnterESCCR:       "\x1b\r",
		leadattach.ShiftEnterLF:          "\n",
		leadattach.ShiftEnterCSIu:        "\x1b[13;2u",
		leadattach.ShiftEnterBackslashCR: "\\\r",
	} {
		t.Run(opt.String(), func(t *testing.T) {
			leakCheck(t)
			e := newEnv(t, setup{mode: leadattach.ModeWriter, shiftEnter: opt})
			hc := e.attach(wire.RoleWriter)
			e.sink.Key(key(uv.KeyEnter, shift, ""))
			if got := hc.input(len(want)); got != want {
				t.Errorf("shift+enter sent %q, want %q", got, want)
			}
		})
	}
}

func TestUnencodableKeysAreCountedNotSent(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)
	e.sink.Key(key('a', uv.ModSuper, ""))
	e.sink.Key(key(uv.KeyMediaPlay, 0, ""))
	e.sink.Key(keyA)
	if got := hc.input(1); got != "a" {
		t.Errorf("sent %q", got)
	}
	if n := counter(e.c.Status(), "client-keys-unencodable"); n != 2 {
		t.Errorf("unencodable keys %d, want 2", n)
	}
}

func TestPasteArrivesAsAPasteFrame(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)

	text := "line one\nline two \x1b[201~ café 漢字"
	e.sink.Paste(text)
	f := hc.next()
	if f.typ != wire.TypePaste || string(f.payload) != text {
		t.Errorf("paste frame %s %q", wire.TypeName(f.typ), f.payload)
	}

	// Invalid UTF-8 is replaced, an empty paste sends nothing.
	e.sink.Paste("")
	e.sink.Paste("bad \xff byte")
	f = hc.next()
	if f.typ != wire.TypePaste || string(f.payload) != "bad � byte" {
		t.Errorf("paste frame %s %q", wire.TypeName(f.typ), f.payload)
	}

	// A paste over the cap is refused whole, with a notice.
	e.sink.Paste(strings.Repeat("x", wire.MaxClientPayload+1))
	ns := e.notices(1)
	if ns[0].Code != "paste-too-large" || ns[0].Text == "" {
		t.Errorf("notice %+v", ns[0])
	}
	if n := counter(e.c.Status(), "client-pastes-refused"); n != 1 {
		t.Errorf("refused pastes %d", n)
	}
	// A paste of exactly the cap goes.
	e.sink.Paste(strings.Repeat("y", wire.MaxClientPayload))
	f = hc.next()
	if f.typ != wire.TypePaste || len(f.payload) != wire.MaxClientPayload {
		t.Errorf("paste frame %s of %d bytes", wire.TypeName(f.typ), len(f.payload))
	}
}

func TestMouseArrivesEncodedForTheHoldersModes(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)
	click := uv.MouseClickEvent{X: 4, Y: 2, Button: uv.MouseLeft}

	e.sink.Mouse(click) // no mouse mode yet: nothing
	hc.modes([]int{1000, 1006}, nil)
	e.barrier(hc)
	e.sink.Mouse(click)
	e.sink.Mouse(uv.MouseReleaseEvent{X: 4, Y: 2, Button: uv.MouseLeft})
	e.sink.Mouse(uv.MouseWheelEvent{X: 0, Y: 0, Button: uv.MouseWheelUp})
	want := "\x1b[<0;5;3M" + "\x1b[<0;5;3m" + "\x1b[<64;1;1M"
	if got := hc.input(len(want)); got != want {
		t.Errorf("sgr: %q, want %q", got, want)
	}

	hc.modes([]int{1000}, nil) // legacy encoding
	e.barrier(hc)
	e.sink.Mouse(click)
	want = "\x1b[M \x25\x23"
	if got := hc.input(len(want)); got != want {
		t.Errorf("legacy: %q, want %q", got, want)
	}
}

func TestResizeIsSentAsTheWriterAndCoalesced(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)

	read := func() wire.Size {
		t.Helper()
		f := hc.next()
		if f.typ != wire.TypeResize {
			t.Fatalf("got a %s frame, want resize", wire.TypeName(f.typ))
		}
		return enc2(t)(wire.DecodeResize(f.payload))
	}
	e.c.Resize(100, 30)
	if got := read(); got != (wire.Size{Columns: 100, Rows: 30}) {
		t.Errorf("resize %+v", got)
	}
	// A size outside the pane's limits is clamped before it is sent.
	e.c.Resize(9999, 0)
	if got := read(); got != (wire.Size{Columns: 500, Rows: 1}) {
		t.Errorf("resize %+v", got)
	}
	// The holder's own size is not asked for again.
	hc.size(90, 20)
	e.until("the size", func(st terminalconsole.LiveStatus) bool { return st.Cols == 90 })
	e.c.Resize(90, 20)
	e.c.Resize(91, 21)
	if got := read(); got != (wire.Size{Columns: 91, Rows: 21}) {
		t.Errorf("resize %+v, want 91x21 (90x20 is the holder's size)", got)
	}
}

func enc2(t *testing.T) func(wire.Size, error) wire.Size {
	return func(s wire.Size, err error) wire.Size {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
}

func TestOnlyTheNewestResizeWaitingIsSent(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter, pipe: true})
	hc := e.attach(wire.RoleWriter)
	hc.pause()

	// The sender is stuck on a write the holder does not read.
	e.sink.Paste("first")
	e.sink.Paste("second")
	for i := 0; i < 5; i++ {
		e.c.Resize(100+i, 30+i)
	}
	hc.resume()
	var sizes []wire.Size
	deadline := time.After(wait)
	for len(sizes) == 0 || sizes[len(sizes)-1] != (wire.Size{Columns: 104, Rows: 34}) {
		select {
		case f := <-hc.frames:
			if f.err != nil {
				t.Fatal(f.err)
			}
			if f.typ == wire.TypeResize {
				sizes = append(sizes, enc2(t)(wire.DecodeResize(f.payload)))
			}
		case <-deadline:
			t.Fatalf("sizes %+v never reached 104x34", sizes)
		}
	}
	if len(sizes) > 2 {
		t.Errorf("%d resize frames for 5 calls: %+v", len(sizes), sizes)
	}
}

func TestAViewerSendsNothing(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer})
	hc := e.attach(wire.RoleViewer)
	hc.modes([]int{1000, 1006, 2004}, nil)
	e.barrier(hc)

	e.sink.Key(keyA)
	e.sink.Key(key(uv.KeyEnter, shift, ""))
	e.sink.Paste("pasted")
	e.sink.Mouse(uv.MouseClickEvent{X: 1, Y: 1, Button: uv.MouseLeft})
	e.c.Resize(100, 30)
	if st := e.c.Status(); st.RefusedInput != 4 {
		t.Errorf("refused input %d, want 4 (the resize is not input)", st.RefusedInput)
	}
	e.c.Close()
	if types := hc.closed(); string(types) != string(wire.TypeDetach) {
		t.Errorf("a viewer sent %q before closing, want only a detach", types)
	}
}

func TestAClientThatWantsToViewSendsNothingEvenIfGrantedTheWriter(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer})
	hc := e.attach(wire.RoleWriter)
	e.sink.Key(keyA)
	e.c.Resize(100, 30)
	st := e.c.Status()
	if st.Role != terminalconsole.LiveWriter || st.RefusedInput != 1 {
		t.Errorf("role %q, refused %d", st.Role, st.RefusedInput)
	}
	ns := e.c.Notices()
	if len(ns) != 1 || ns[0].Code != "role-granted" {
		t.Errorf("notices %+v", ns)
	}
	e.c.Close()
	if types := hc.closed(); string(types) != string(wire.TypeDetach) {
		t.Errorf("sent %q", types)
	}
}

func TestInputStopsWhenTheWriterRoleIsLost(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)
	hc.roleFrame(wire.RoleViewer, "another")
	e.until("viewer", func(st terminalconsole.LiveStatus) bool { return st.Role == terminalconsole.LiveViewer })
	e.sink.Key(keyA)
	e.sink.Paste("x")
	e.c.Resize(100, 30)
	if st := e.c.Status(); st.RefusedInput != 2 {
		t.Errorf("refused input %d", st.RefusedInput)
	}
	hc.roleFrame(wire.RoleWriter, "test console") // the role comes back
	e.until("writer", func(st terminalconsole.LiveStatus) bool { return st.Role == terminalconsole.LiveWriter })
	e.sink.Key(key('b', 0, "b"))
	if got := hc.input(1); got != "b" {
		t.Errorf("sent %q", got)
	}
}

func TestInputIsDroppedWhileNotAttached(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	e.sink.Key(keyA) // waiting for a ticket
	hc := e.attach(wire.RoleWriter)
	hc.corrupt("x", "y")
	hc.check()
	e.until("resyncing", func(st terminalconsole.LiveStatus) bool { return st.CheckMismatches == 1 })
	e.sink.Key(keyA) // resyncing: the old connection is going away
	if st := e.c.Status(); st.RefusedInput != 2 {
		t.Errorf("refused input %d, want 2", st.RefusedInput)
	}
}

func TestTheOutgoingQueueIsBoundedAndTheOverflowCountedWithOneNotice(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter, pipe: true})
	hc := e.attach(wire.RoleWriter)
	hc.pause()

	const pastes = 700
	for i := 0; i < pastes; i++ {
		e.sink.Paste("p")
	}
	dropped := counter(e.c.Status(), "client-input-dropped")
	if dropped < pastes-258 || dropped >= pastes {
		t.Fatalf("dropped %d of %d with a queue of 256", dropped, pastes)
	}
	var overflow int
	for _, n := range e.c.Notices() {
		if n.Code == "input-dropped" {
			overflow++
		}
	}
	if overflow != 1 {
		t.Errorf("%d overflow notices, want one", overflow)
	}
	// What was not dropped arrives, in order, once the holder reads again.
	hc.resume()
	for got := uint64(0); got < pastes-dropped; got++ {
		if f := hc.next(); f.typ != wire.TypePaste {
			t.Fatalf("frame %d is %s", got, wire.TypeName(f.typ))
		}
	}
	// And the queue is usable again.
	e.sink.Key(keyA)
	if got := hc.input(1); got != "a" {
		t.Errorf("sent %q", got)
	}
}

func TestKeysInABurstShareFramesUnderTheCap(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter, pipe: true})
	hc := e.attach(wire.RoleWriter)
	hc.pause()
	e.sink.Paste("blocks the sender")
	e.sink.Paste("really")
	const keys = 100000 // far more than 256 frames if each key were one
	for i := 0; i < keys; i++ {
		e.sink.Key(keyA)
	}
	if n := counter(e.c.Status(), "client-input-dropped"); n != 0 {
		t.Errorf("%d keys dropped; a burst of keys joins frames", n)
	}
	hc.resume()
	total := 0
	for total < keys {
		f := hc.next()
		if f.typ == wire.TypeInput {
			if len(f.payload) > wire.MaxClientPayload {
				t.Fatalf("an input frame of %d bytes", len(f.payload))
			}
			total += len(f.payload)
		}
	}
}

func TestThePanesOwnRepliesAreNeverSent(t *testing.T) {
	// The cockpit makes the pane with no Replies function; a holder's output
	// that queries the terminal gets no answer from the client.
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)
	hc.write("\x1b[6n\x1b]11;?\x1b\\\x1b[c\x1b[?2026$p\x1b[>q")
	e.barrier(hc)
	e.c.Close()
	if types := hc.closed(); string(types) != string(wire.TypeDetach) {
		t.Errorf("the client sent %q", types)
	}
}

func TestStuckHolderIsGivenUp(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter, pipe: true, write: 50 * time.Millisecond})
	hc := e.attach(wire.RoleWriter)
	hc.pause()
	e.sink.Paste("one") // read by the holder's reader before it stops
	e.sink.Paste("two") // never read
	e.until("giving up", func(st terminalconsole.LiveStatus) bool {
		return st.Conn == terminalconsole.LiveWaitingTicket && strings.Contains(st.Reason, "could not send")
	})
}

func TestWakeNeverRunsUnderTheStatusLock(t *testing.T) {
	// A wake callback that calls every method of the source, as the
	// cockpit's may, on every change.
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter, noStart: true})
	var calls atomic.Int64
	_, err := e.c.Start(terminalconsole.LiveStart{Pane: e.pane, Cols: 80, Rows: 24, Wake: func() {
		calls.Add(1)
		e.c.Status()
		e.c.Notices()
		e.c.Resize(80, 24)
		e.c.LastScreen()
	}})
	if err != nil {
		t.Fatal(err)
	}
	e.tickets.give(1)
	hc := e.h.accept()
	hc.welcome(wire.RoleWriter)
	hc.snapshot()
	hc.write("output")
	hc.notice(wire.ReasonResync, "x")
	hc.roleFrame(wire.RoleWriter, "me")
	hc.modes([]int{1000}, nil)
	hc.size(90, 20)
	e.until("the frames", func(st terminalconsole.LiveStatus) bool { return st.Cols == 90 && st.Modes.Mouse != 0 })
	if calls.Load() == 0 {
		t.Error("no wake")
	}
}

func TestNoInputBeforeTheFirstSnapshot(t *testing.T) {
	// The Welcome comes first and the snapshot only at a quiet point: until it
	// is applied the pane shows another screen and the modes are not known,
	// so a key is dropped and counted, never sent.
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter, tickets: 1})
	hc := e.h.accept()
	hc.welcome(wire.RoleWriter)
	e.until("the welcome", func(st terminalconsole.LiveStatus) bool { return st.Lead != "" })
	hc.modes([]int{1}, nil)
	e.barrier(hc)
	e.sink.Key(key(uv.KeyUp, 0, ""))
	e.sink.Paste("text")
	e.sink.Mouse(uv.MouseClickEvent{X: 1, Y: 1, Button: uv.MouseLeft})
	st := e.c.Status()
	if st.Conn != terminalconsole.LiveConnecting || st.RefusedInput != 3 {
		t.Errorf("before the snapshot: state %v, refused %d, want connecting and 3", st.Conn, st.RefusedInput)
	}

	hc.snapshot()
	e.untilConn(terminalconsole.LiveAttached)
	e.sink.Key(key(uv.KeyUp, 0, ""))
	if got := hc.input(3); got != "\x1bOA" { // the first frame the holder reads is this key
		t.Errorf("after the snapshot: %q", got)
	}
}

func TestASizeAskedWhileConnectingIsSentAfterTheFirstSnapshot(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter, tickets: 1})
	hc := e.h.accept() // the Hello carried 80x24
	hc.welcome(wire.RoleWriter)
	e.until("the welcome", func(st terminalconsole.LiveStatus) bool { return st.Lead != "" })
	e.c.Resize(100, 30) // connecting: remembered, nothing to send to yet
	e.barrier(hc)
	hc.snapshot() // the holder's size is still 80x24
	f := hc.next()
	if f.typ != wire.TypeResize {
		t.Fatalf("got a %s frame, want resize", wire.TypeName(f.typ))
	}
	if sz := enc2(t)(wire.DecodeResize(f.payload)); sz != (wire.Size{Columns: 100, Rows: 30}) {
		t.Errorf("resize %+v", sz)
	}

	// The same size as the holder's: nothing is sent. A viewer sends nothing.
	hc.size(100, 30)
	e.until("the size", func(st terminalconsole.LiveStatus) bool { return st.Cols == 100 })
	e.sink.Key(keyA)
	if got := hc.input(1); got != "a" {
		t.Errorf("after a size that is already the holder's: %q", got)
	}
}

func TestAViewerDoesNotSendTheSizeItWants(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeViewer, tickets: 1})
	hc := e.h.accept()
	hc.welcome(wire.RoleViewer)
	e.until("the welcome", func(st terminalconsole.LiveStatus) bool { return st.Lead != "" })
	e.c.Resize(100, 30)
	hc.snapshot()
	e.untilConn(terminalconsole.LiveAttached)
	e.c.Close()
	if types := hc.closed(); string(types) != string(wire.TypeDetach) {
		t.Errorf("frames from a viewer: %q, want only a detach", types)
	}
}

func TestAHolderResyncNoticeStopsInputUntilTheNextSnapshot(t *testing.T) {
	leakCheck(t)
	e := newEnv(t, setup{mode: leadattach.ModeWriter})
	hc := e.attach(wire.RoleWriter)
	e.sink.Key(keyA)
	if got := hc.input(1); got != "a" {
		t.Fatalf("before the notice: %q", got)
	}

	hc.notice(wire.ReasonResync, "a new snapshot follows")
	st := e.untilConn(terminalconsole.LiveResyncing)
	if st.Reason == "" {
		t.Error("no reason while resyncing")
	}
	e.sink.Key(keyA)
	e.sink.Paste("x")
	e.sink.Mouse(uv.MouseClickEvent{X: 1, Y: 1, Button: uv.MouseLeft})
	if st := e.c.Status(); st.RefusedInput != 3 {
		t.Errorf("refused input %d, want 3", st.RefusedInput)
	}
	if ns := e.c.Notices(); len(ns) != 1 || ns[0].Code != "resync" {
		t.Errorf("notices %+v", ns)
	}

	hc.snapshot()
	st = e.until("attached again", func(st terminalconsole.LiveStatus) bool { return st.Conn == terminalconsole.LiveAttached })
	if st.Resyncs != 1 || st.Reason != "" {
		t.Errorf("resyncs %d, reason %q", st.Resyncs, st.Reason)
	}
	e.sink.Key(key('b', 0, "b"))
	if got := hc.input(1); got != "b" {
		t.Errorf("after the snapshot: %q", got)
	}
}
