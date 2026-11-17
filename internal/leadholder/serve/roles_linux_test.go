//go:build linux

package serve

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ataniq/internal/leadholder/wire"
)

// Case A1: the viewer's Input, Paste and Resize are refused with a notice and
// counted, and the agent sees nothing.
func TestViewerInputRefused(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo; wait")
	waitScreen(t, e.sess, "READY")
	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)
	v.input("x")
	v.paste("y")
	v.resize(120, 40)
	v.waitFor("three notices", func() bool { return len(v.notices) >= 3 })
	for _, n := range v.notices {
		if n.Reason != wire.ReasonViewerInputRefused {
			t.Errorf("notice %+v", n)
		}
	}
	e.waitCount(cntViewerInput, 3)
	if cols, rows := e.sess.Size(); cols != 100 || rows != 30 {
		t.Errorf("the viewer's resize changed the session to %dx%d", cols, rows)
	}
	if got := rxBytes(screenText(e.sess)); len(got) != 0 {
		t.Fatalf("the agent received %q from a viewer", got)
	}
	// The writer's input is the only input the agent gets.
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	w.input("z")
	waitRX(t, e.sess, "z")
	e.waitEvent("viewer refusal", named("refusal", "viewer-input-refused"))
}

// Case A3 and the roles: one writer, no second writer ticket, release-writer,
// and every pane shows the change.
func TestWriterTicketBusyAndRelease(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo; wait")
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter, func(h *helloOpts) { h.name = "first" })
	w.waitSnapshots(1)

	tk, _ := wire.NewTicket()
	r := e.mustControl(wire.Request{Op: wire.OpTicket, Epoch: testEpoch, Role: wire.RoleWriter, Ticket: tk})
	if r.OK || r.Code != wire.CodeBusy {
		t.Fatalf("a second writer ticket: %+v", r)
	}

	// With no viewer the writer becomes the viewer.
	r = e.mustControl(wire.Request{Op: wire.OpReleaseWriter, Epoch: testEpoch, CommandID: "rel-1"})
	if !r.OK {
		t.Fatalf("release: %+v", r)
	}
	w.waitFor("the role change", func() bool { return w.lastRoleLocked().Role == wire.RoleViewer })
	n := w.waitNotice(wire.ReasonWriterReleased)
	if !strings.Contains(n.Text, "first") {
		t.Errorf("the notice does not name the writer: %q", n.Text)
	}
	if got := w.lastRole(); got.WriterName != "" || got.WriterPID != 0 {
		t.Errorf("a role with no writer: %+v", got)
	}
	ev := e.waitEvent("the release", named("writer-change", "release-writer"))
	if ev.CommandID != "rel-1" || ev.ClientName != "first" || ev.ClientPID == 0 || ev.ClientCommand == "" {
		t.Errorf("event %+v", ev)
	}

	// Its later input is refused.
	w.input("q")
	w.waitNotice(wire.ReasonViewerInputRefused)
	if got := rxBytes(screenText(e.sess)); len(got) != 0 {
		t.Fatalf("the agent received %q after the release", got)
	}

	// A new writer can come now; the old one is told who it is.
	w2 := e.attach(wire.RoleWriter, func(h *helloOpts) { h.name = "second" })
	w2.waitSnapshots(1)
	w.waitFor("the new writer's role", func() bool { return w.lastRoleLocked().WriterName == "second" })
	n = w.waitNotice(wire.ReasonWriterChanged)
	if !strings.Contains(n.Text, "second") || !strings.Contains(n.Text, itoa(w.lastRole().WriterPID)) {
		t.Errorf("the notice does not name the new writer and its pid: %q", n.Text)
	}
	got := w.lastRole()
	if got.WriterCommand == "" || got.Role != wire.RoleViewer {
		t.Errorf("role %+v", got)
	}
	e.waitEvent("the new writer", named("writer-change", "writer-attached"))
	w2.input("k")
	waitRX(t, e.sess, "k")
}

func itoa(n int) string { return strconv.Itoa(n) }

// With a viewer present, release-writer closes the writer.
func TestReleaseWriterClosesWhenViewerPresent(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo; wait")
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter, func(h *helloOpts) { h.name = "wr" })
	w.waitSnapshots(1)
	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)
	if r := e.mustControl(wire.Request{Op: wire.OpReleaseWriter, Epoch: testEpoch}); !r.OK {
		t.Fatalf("release: %+v", r)
	}
	if err := w.waitEnd(); err == nil {
		t.Error("the writer was not closed")
	}
	w.mu.Lock()
	var released bool
	for _, n := range w.notices {
		released = released || n.Reason == wire.ReasonWriterReleased
	}
	w.mu.Unlock()
	if !released {
		t.Error("the closed writer did not get its notice")
	}
	v.waitNotice(wire.ReasonWriterReleased)
	v.waitFor("a role with no writer", func() bool {
		r := v.lastRoleLocked()
		return len(v.roles) >= 2 && r.WriterName == "" && r.Role == wire.RoleViewer
	})
	// Release with no writer is not an error.
	if r := e.mustControl(wire.Request{Op: wire.OpReleaseWriter, Epoch: testEpoch}); !r.OK {
		t.Fatalf("a second release: %+v", r)
	}
}

// A writer that disconnects leaves no writer, and nothing is typed for it.
func TestWriterDisconnect(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo; wait")
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter, func(h *helloOpts) { h.name = "gone" })
	w.waitSnapshots(1)
	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)

	w.conn.Close()
	v.waitFor("a role with no writer", func() bool {
		r := v.lastRoleLocked()
		return len(v.roles) >= 2 && r.WriterName == "" && r.WriterPID == 0
	})
	n := v.waitNotice(wire.ReasonWriterReleased)
	if !strings.Contains(n.Text, "gone") {
		t.Errorf("notice %q", n.Text)
	}
	ev := e.waitEvent("the writer left", named("writer-change", "writer-left"))
	if ev.ClientName != "gone" {
		t.Errorf("event %+v", ev)
	}
	e.waitEvent("detach", named("detach", "eof"))
	if got := rxBytes(screenText(e.sess)); len(got) != 0 {
		t.Fatalf("the agent received %q for a writer that left", got)
	}
	// The place is free again.
	w2 := e.attach(wire.RoleWriter)
	w2.waitSnapshots(1)
	if st := e.mustControl(wire.Request{Op: wire.OpStatus, Epoch: testEpoch}).Status; !st.WriterPresent || !st.ViewerPresent {
		t.Errorf("status %+v", st)
	}
}

// Cases H11 and H12: held keys give the writer a notice and are not forwarded.
func TestHeldKeysNotice(t *testing.T) {
	e := newEnv(t, "cooked; say READY; echo 2; wait")
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	w.input("\x1a")
	w.waitNotice(wire.ReasonKeyHeldSuspend)
	w.input("\x03\x1c")
	w.waitNotice(wire.ReasonKeyHeldSignal)
	w.input("a\n")
	waitRX(t, e.sess, "a\n")
	if f := e.sess.Facts(); f.TUIExited != "no" {
		t.Fatalf("the agent did not survive: %+v", f)
	}
}

// Case A10: five quick resizes end at the last size, within the caps, and every
// client is told.
func TestResizeFiveTimes(t *testing.T) {
	e := newEnv(t, "raw; watchwinch; say READY; wait")
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)
	for i := 0; i < 5; i++ {
		w.resize(90+i, 30+i)
	}
	w.waitFor("the last size", func() bool { return w.lastSizeLocked() == wire.Size{Columns: 94, Rows: 34} })
	v.waitFor("the last size", func() bool { return v.lastSizeLocked() == wire.Size{Columns: 94, Rows: 34} })
	if cols, rows := e.sess.Size(); cols != 94 || rows != 34 {
		t.Fatalf("the session is %dx%d", cols, rows)
	}
	waitScreen(t, e.sess, "WINCH 94x34")
	waitQuiet(t, e.sess)
	e.inSync(w)
	e.inSync(v)

	// The caps hold.
	w.resize(5000, 5000)
	w.waitFor("the upper cap", func() bool { return w.lastSizeLocked() == wire.Size{Columns: 500, Rows: 200} })
	w.resize(2, 1)
	w.waitFor("the lower cap", func() bool { return w.lastSizeLocked() == wire.Size{Columns: 20, Rows: 5} })
	waitQuiet(t, e.sess)
	e.inSync(w)
	e.inSync(v)
}

func (c *tclient) lastSizeLocked() wire.Size {
	if len(c.sizes) == 0 {
		return wire.Size{}
	}
	return c.sizes[len(c.sizes)-1]
}

// Case A11: the agent asks for the colours; the answer carries the writer's
// colours, and the agent gets exactly one answer. The client sends none.
func TestColourQueryAnsweredWithWritersColours(t *testing.T) {
	file := filepath.Join(t.TempDir(), "reply")
	e := newEnv(t, "raw; say READY; echo 1; query osc10,osc11,da1 "+file+"; say DONE; wait")
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter, func(h *helloOpts) {
		h.colors = wire.Colors{Foreground: "#aabbcc", Background: "#112233"}
	})
	w.waitSnapshots(1)
	w.input("x")
	waitScreen(t, e.sess, "DONE")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"\x1b]10;rgb:aaaa/bbbb/cccc\x07", "\x1b]11;rgb:1111/2222/3333\x07"} {
		if strings.Count(got, want) != 1 {
			t.Errorf("the agent's reply %q does not hold %q once", got, want)
		}
	}
	// The client's own emulator answered the same queries into its replies
	// callback, and none of that went anywhere: the agent got one reply.
	w.waitText("DONE")
	if strings.Count(got, "\x1b]11;") != 1 {
		t.Errorf("more than one answer: %q", got)
	}
	e.inSync(w)
	waitRX(t, e.sess, "x")
}

// Case A6 on the holder's side: the Modes frame carries the modes the agent set.
func TestModesFrameFollowsTheAgent(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo 1; mode mouse1000 mouse1006 paste; echo 1; unmode mouse1000; say END; wait")
	waitScreen(t, e.sess, "READY")
	c := e.attach(wire.RoleWriter)
	c.waitChecks(1)
	c.mu.Lock()
	if len(c.modes) != 1 || !equalInts(c.modes[0].DEC, []int{7, 25}) {
		t.Errorf("the modes at the snapshot: %+v", c.modes)
	}
	c.mu.Unlock()
	c.input("a")
	c.waitFor("mouse modes", func() bool {
		return len(c.modes) >= 2 && equalInts(c.modes[len(c.modes)-1].DEC, []int{7, 25, 1000, 1006, 2004})
	})
	c.input("b")
	c.waitFor("a mode reset", func() bool { return equalInts(c.modes[len(c.modes)-1].DEC, []int{7, 25, 1006, 2004}) })
	waitScreen(t, e.sess, "END")
	e.inSync(c)

	// A client that attaches later gets the set in its snapshot sequence.
	late := e.attach(wire.RoleViewer)
	late.waitChecks(1)
	late.mu.Lock()
	defer late.mu.Unlock()
	if !equalInts(late.modes[0].DEC, []int{7, 25, 1006, 2004}) {
		t.Errorf("the modes for a late client: %+v", late.modes)
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
