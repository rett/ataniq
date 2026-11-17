//go:build linux

package serve

import (
	"bytes"
	"encoding/hex"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/wire"
)

// Case A9: a frame over the cap, a frame of an unknown type, a frame that only
// the other side may send, and a second Hello close the connection; the holder
// and the other clients are undisturbed.
func TestBrokenFramesCloseTheConnection(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo; wait")
	waitScreen(t, e.sess, "READY")
	other := e.attach(wire.RoleViewer)
	other.waitSnapshots(1)

	cases := []struct {
		name  string
		frame []byte
		code  string
	}{
		{"over the cap", rawFrame(wire.TypeInput, wire.MaxClientPayload+1, nil), "frame-too-large"},
		{"unknown type", rawFrame('X', 3, []byte("abc")), "unknown-frame-type"},
		{"a holder frame", rawFrame(wire.TypeWelcome, 2, []byte("{}")), "unknown-frame-type"},
		{"a second hello", rawFrame(wire.TypeHello, 2, []byte("{}")), "unexpected-frame"},
		{"a bad resize", rawFrame(wire.TypeResize, 2, []byte("{}")), "bad-resize"},
		{"a bad paste", rawFrame(wire.TypePaste, 2, []byte{0xff, 0xfe}), "bad-paste"},
		{"a detach with a payload", rawFrame(wire.TypeDetach, 1, []byte{1}), "frame-too-large"},
	}
	for i, tc := range cases {
		w := e.attach(wire.RoleWriter)
		w.waitSnapshots(1)
		if _, err := w.conn.Write(tc.frame); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if err := w.waitEnd(); err == nil {
			t.Errorf("%s: the connection was not closed", tc.name)
		}
		e.waitEvent(tc.name, named("refusal", tc.code))
		if got := e.count(cntBadFrames); got != uint64(i+1) {
			t.Errorf("%s: bad frames %d, want %d", tc.name, got, i+1)
		}
	}
	// The viewer was not disturbed, and the lead still takes input.
	other.input("x")
	other.waitNotice(wire.ReasonViewerInputRefused)
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	w.input("ok")
	waitRX(t, e.sess, "ok")
	e.inSync(other)
}

// Case A9 and the handshake: connections that wait for their Hello are limited
// to four, a further one is closed at once; a Hello that does not come in time,
// or a first frame that is not a Hello, ends the connection.
func TestHandshakeLimits(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo; wait", func(o *envOpts) {
		o.cfg = func(c *Config) { c.HandshakeTimeout = 600 * time.Millisecond }
	})
	waitScreen(t, e.sess, "READY")
	var idle []*net.UnixConn
	for i := 0; i < MaxHandshakes; i++ {
		idle = append(idle, e.dialAttach())
	}
	// Connection state is counted when the handler runs: wait until all four
	// are in their handshake, then the fifth is refused at once.
	poll(t, func() string { return "four connections in handshake" }, func() bool {
		e.srv.mu.Lock()
		defer e.srv.mu.Unlock()
		return e.srv.handshaking == MaxHandshakes
	})
	fifth := e.dialAttach()
	start := time.Now()
	if b := readAll(fifth); len(b) != 0 {
		t.Errorf("the fifth connection got %d bytes", len(b))
	}
	if d := time.Since(start); d > 400*time.Millisecond {
		t.Errorf("the fifth connection was closed after %v: not at once", d)
	}
	e.waitCount(cntRefusedFull, 1)
	e.waitEvent("the full handshake", named("refusal", "attach-handshake-full"))

	// The four that say nothing are cut at the deadline.
	for _, c := range idle {
		if b := readAll(c); len(b) != 0 {
			t.Errorf("an idle connection got %d bytes", len(b))
		}
	}
	e.waitCount(cntRefusedHandshake, 4)
	e.waitEvent("the timeout", named("refusal", "handshake-timeout"))

	// A first frame that is not a Hello.
	c := e.dialAttach()
	if _, err := c.Write(rawFrame(wire.TypeInput, 1, []byte("x"))); err != nil {
		t.Fatal(err)
	}
	if b := readAll(c); len(b) != 0 {
		t.Errorf("an answer to a first frame that is not a Hello")
	}
	e.waitEvent("the first frame", named("refusal", "handshake-bad-first-frame"))
	// A Hello that does not decode, in our version.
	c = e.dialAttach()
	bad := []byte(`{"version":1,"ticket":"` + strings.Repeat("0", 64) + `","name":"x","columns":0,"rows":0}`)
	if _, err := c.Write(rawFrame(wire.TypeHello, uint32(len(bad)), bad)); err != nil {
		t.Fatal(err)
	}
	readAll(c)
	e.waitEvent("the bad hello", named("refusal", "handshake-bad-hello"))

	// And the holder is undisturbed.
	e.attach(wire.RoleWriter).waitSnapshots(1)
}

// A third client with both places taken is refused with a notice.
func TestThirdConnectionRefused(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait")
	waitScreen(t, e.sess, "READY")
	e.attach(wire.RoleWriter).waitSnapshots(1)
	e.attach(wire.RoleViewer).waitSnapshots(1)
	never, _ := wire.NewTicket()
	third := e.attachWithNoWait(never)
	third.waitNotice(wire.ReasonRefusedTicket)
	third.waitEnd()
	for _, role := range []wire.Role{wire.RoleWriter, wire.RoleViewer} {
		tk, _ := wire.NewTicket()
		if r := e.mustControl(wire.Request{Op: wire.OpTicket, Epoch: testEpoch, Role: role, Ticket: tk}); r.OK || r.Code != wire.CodeBusy {
			t.Errorf("a %s ticket with both places taken: %+v", role, r)
		}
	}
}

func TestDetachFrame(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait")
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	w.detach()
	if err := w.waitEnd(); err == nil {
		t.Error("no end")
	}
	e.waitEvent("the detach", named("detach", "detach"))
	if e.count(cntBadFrames) != 0 {
		t.Error("a detach is not a bad frame")
	}
}

// The Exit frame after status 3: a State and then the final record, with the
// facts, the status, the counters (the pane's caps too) and the last screen,
// and the client is closed; Serve returns and the sockets are gone.
func TestExitFrameAfterStatus3(t *testing.T) {
	e := newEnv(t, "raw; say HELLO; echo 1; say BYE; exit 3")
	waitScreen(t, e.sess, "HELLO")
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)
	w.input("x")
	for _, c := range []*tclient{w, v} {
		c.waitFor("the exit", func() bool { return c.exit != nil })
		if err := c.waitEnd(); err == nil {
			t.Error("no end")
		}
		c.noProblems()
		c.mu.Lock()
		x := *c.exit
		st := c.states[len(c.states)-1]
		// State comes before Exit, which is the last frame.
		if c.frames[len(c.frames)-1] != wire.TypeExit || c.frames[len(c.frames)-2] != wire.TypeState {
			t.Errorf("the last frames are %q", c.frames[len(c.frames)-3:])
		}
		c.mu.Unlock()
		if x.ExitStatus == nil || *x.ExitStatus != 3 || x.Facts.TUIExited != wire.FactYes || x.Facts.StreamEnded != wire.FactYes ||
			x.Facts.TreeGone != wire.FactYes {
			t.Errorf("exit %+v", x)
		}
		if st.ExitStatus == nil || *st.ExitStatus != 3 {
			t.Errorf("state %+v", st)
		}
		if !strings.Contains(x.LastScreen, "BYE") || !strings.Contains(x.LastScreen, "HELLO") {
			t.Errorf("the last screen %q", x.LastScreen)
		}
		for _, name := range []string{"output_bytes", "cluster_bytes_dropped", "links_over_limit", "clipboard_requests_blocked", "snapshots_taken", "checks_sent"} {
			if _, ok := x.Counters[name]; !ok {
				t.Errorf("no counter %s in the exit record: %v", name, x.Counters)
			}
		}
	}
	select {
	case err := <-e.served:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(patience):
		t.Fatal("Serve did not return")
	}
	for _, name := range []string{AttachSocket, ControlSocket} {
		if _, err := os.Lstat(filepath.Join(e.dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", name, err)
		}
	}
	e.waitEvent("detach at the end", named("detach", "session-over"))
}

// Modes of the folder and the sockets, and what New refuses.
func TestSocketsAndFolderModes(t *testing.T) {
	e, _ := newBase(t, "raw; say READY; wait")
	e.start(true)
	for _, name := range []string{AttachSocket, ControlSocket} {
		var st unix.Stat_t
		if err := unix.Lstat(filepath.Join(e.dir, name), &st); err != nil {
			t.Fatal(err)
		}
		if st.Mode&unix.S_IFMT != unix.S_IFSOCK || st.Mode&0o7777 != 0o600 {
			t.Errorf("%s has mode %o", name, st.Mode)
		}
	}
}

func TestNewRefusesAnUnsafeFolder(t *testing.T) {
	e, _ := newBase(t, "raw; say READY; wait")
	cfg := e.cfg
	try := func(name, dir string) {
		t.Helper()
		cfg.Dir = dir
		s, err := New(cfg)
		if err == nil {
			s.Close()
			t.Errorf("%s: New accepted %s", name, dir)
		}
		if err != nil && strings.Contains(err.Error(), "<nil>") {
			t.Errorf("%s: %v", name, err)
		}
	}
	root := t.TempDir()
	wide := filepath.Join(root, "wide")
	if err := os.Mkdir(wide, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wide, 0o755); err != nil {
		t.Fatal(err)
	}
	try("mode 0755", wide)
	narrow := filepath.Join(root, "narrow")
	if err := os.Mkdir(narrow, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(narrow, 0o500); err != nil {
		t.Fatal(err)
	}
	try("mode 0500", narrow)
	try("a symlink", func() string {
		l := filepath.Join(root, "link")
		if err := os.Symlink(e.dir, l); err != nil {
			t.Fatal(err)
		}
		return l
	}())
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	try("a file", file)
	try("missing", filepath.Join(root, "missing"))
	try("empty", "")
	for _, f := range []struct {
		name string
		mod  func(*Config)
	}{
		{"no session", func(c *Config) { c.Session = nil }},
		{"no log", func(c *Config) { c.Log = nil }},
		{"epoch 0", func(c *Config) { c.Epoch = 0 }},
		{"no lead", func(c *Config) { c.Lead = "" }},
		{"no runtime", func(c *Config) { c.Runtime = "" }},
		{"no daemon lock", func(c *Config) { c.DaemonLock = "" }},
	} {
		c := e.cfg
		f.mod(&c)
		if s, err := New(c); err == nil {
			s.Close()
			t.Errorf("%s: New accepted it", f.name)
		}
	}
	// Nothing was left behind in the good folder, and it still works.
	if es, _ := os.ReadDir(e.dir); len(es) != 0 {
		t.Errorf("left behind: %v", es)
	}
	e.start(true)
}

// A stale socket from an earlier holder is removed; anything else of that name
// is not touched.
func TestStaleSocket(t *testing.T) {
	e, _ := newBase(t, "raw; say READY; wait")
	for _, name := range []string{AttachSocket, ControlSocket} {
		fd, err := sockListenOnly(e.dir, name)
		if err != nil {
			t.Fatal(err)
		}
		unix.Close(fd) // the file stays: a holder that died
		if _, err := os.Lstat(filepath.Join(e.dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	e.start(true)
	waitScreen(t, e.sess, "READY")
	e.attach(wire.RoleViewer).waitSnapshots(1)

	// A file of the name is an error, and is left alone.
	e2, _ := newBase(t, "raw; say READY; wait")
	f := filepath.Join(e2.dir, AttachSocket)
	if err := os.WriteFile(f, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := New(e2.cfg); err == nil {
		s.Close()
		t.Fatal("New replaced a regular file")
	}
	if b, err := os.ReadFile(f); err != nil || string(b) != "not a socket" {
		t.Errorf("the file was touched: %q %v", b, err)
	}
	// A control.sock that cannot be made leaves no attach.sock behind.
	if err := os.Remove(f); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(e2.dir, ControlSocket), 0o700); err != nil {
		t.Fatal(err)
	}
	if s, err := New(e2.cfg); err == nil {
		s.Close()
		t.Fatal("New replaced a folder")
	}
	if _, err := os.Lstat(f); !os.IsNotExist(err) {
		t.Errorf("attach.sock was left behind: %v", err)
	}
}

// sockListenOnly makes a bound socket file and returns the descriptor.
func sockListenOnly(dir, name string) (int, error) {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	if err := unix.Bind(fd, &unix.SockaddrUnix{Name: filepath.Join(dir, name)}); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// Design 1.7 and case H14: neither what is typed or pasted nor what the agent
// prints is in the event log, and neither is a ticket.
func TestEventLogHoldsNoKeysAndNoOutput(t *testing.T) {
	const out, typed, pasted, title = "OUTPUTMARKER9", "TYPEDMARKER7", "PASTEMARKER5", "TITLEMARKER3"
	e := newEnv(t, "raw; say "+out+"; print "+hex.EncodeToString([]byte("\x1b]2;"+title+"\x07"))+"; echo 2; say END; wait")
	waitScreen(t, e.sess, out)
	tk := e.newTicket(wire.RoleWriter)
	w := e.attachWith(tk, func(h *helloOpts) { h.name = "console-A" })
	w.waitSnapshots(1)
	w.input(typed)
	w.paste(pasted)
	w.input("\x1a")
	waitScreen(t, e.sess, "RX:")
	w.waitText(out)
	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)
	v.input("viewerkeys")
	v.waitNotice(wire.ReasonViewerInputRefused)
	e.mustControl(wire.Request{Op: wire.OpReleaseWriter, Epoch: testEpoch, CommandID: "c-1"})
	e.waitEvent("the writer released", named("detach", "released"))
	if err := e.log.Sync(); err != nil {
		t.Fatal(err)
	}
	e.srv.Close() // the queued events are written
	for _, name := range []string{eventlog.FileName, eventlog.RotatedName} {
		b, err := os.ReadFile(filepath.Join(e.logDir, name))
		if err != nil {
			continue
		}
		for _, marker := range []string{out, typed, pasted, title, tk, "viewerkeys", "RX:", "END"} {
			if bytes.Contains(b, []byte(marker)) {
				t.Errorf("%s holds %q", name, marker)
			}
		}
	}
	// What it does hold: the attach with the client's name and command line.
	var names int
	for _, ev := range e.eventsNow() {
		if ev.Event == "attach" && ev.ClientName == "console-A" && ev.ClientPID != 0 && ev.ClientCommand != "" {
			names++
		}
	}
	if names != 1 {
		t.Errorf("attach events with a client: %d", names)
	}
}

// No goroutine or descriptor is left after Close, whatever the clients did.
func TestCloseLeavesNothingBehind(t *testing.T) {
	e, _ := newBase(t, "raw; say READY; flood 200000 0 2")
	waitScreen(t, e.sess, "READY")
	fds0, gos0 := countFDs(t), goroutines()
	e.start(true)
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	v := e.attach(wire.RoleViewer)
	v.waitSnapshots(1)
	v.pause() // a client that does not read
	idle := e.dialAttach()
	w.waitChecks(1)
	// A control request is in flight too.
	if r := e.mustControl(wire.Request{Op: wire.OpStatus, Epoch: testEpoch}); !r.OK {
		t.Fatal(r)
	}
	if err := e.srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := e.srv.Close(); err != nil {
		t.Fatalf("a second Close: %v", err)
	}
	select {
	case err := <-e.served:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(patience):
		t.Fatal("Serve did not return after Close")
	}
	v.resume()
	w.waitEnd()
	v.waitEnd()
	if b := readAll(idle); len(b) != 0 {
		t.Errorf("the idle connection got %d bytes", len(b))
	}
	w.close()
	v.close()
	idle.Close()
	for _, name := range []string{AttachSocket, ControlSocket} {
		if _, err := os.Lstat(filepath.Join(e.dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s is still there: %v", name, err)
		}
	}
	if err := e.srv.Serve(); err == nil {
		t.Error("Serve after Close did not fail")
	}
	// The slot of the session is free: a new service can be made on it.
	s2, err := New(e.cfg)
	if err != nil {
		t.Fatalf("a second service on the session: %v", err)
	}
	s2.Close()
	settled(t, fds0, gos0)
}

// Close while the session runs does not stop the session, and Serve returns.
func TestCloseDoesNotStopTheSession(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait")
	waitScreen(t, e.sess, "READY")
	e.srv.Close()
	select {
	case <-e.served:
	case <-time.After(patience):
		t.Fatal("Serve did not return")
	}
	select {
	case <-e.sess.Done():
		t.Fatal("the session ended")
	default:
	}
}

// Without Serve, Close still lets everything go.
func TestCloseWithoutServe(t *testing.T) {
	e, _ := newBase(t, "raw; say READY; wait")
	fds0, gos0 := countFDs(t), goroutines()
	e.start(false)
	if err := e.srv.Close(); err != nil {
		t.Fatal(err)
	}
	settled(t, fds0, gos0)
}

// A session that ends with no client attached: Serve returns and the sockets
// are gone.
func TestSessionEndsWithNoClients(t *testing.T) {
	e := newEnv(t, "raw; say BYE; exit 0")
	select {
	case err := <-e.served:
		if err != nil {
			t.Errorf("Serve: %v", err)
		}
	case <-time.After(patience):
		t.Fatal("Serve did not return")
	}
	if _, err := sockDial(e.dir, AttachSocket); err == nil {
		t.Error("the attach socket still answers")
	}
}

// The facts and the title reach the clients as State frames while the session
// runs.
func TestStateFramesFollowTheTitleAndTheFacts(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo 1; print "+hex.EncodeToString([]byte("\x1b]2;a new title\x07"))+"; echo 1; exit 0")
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)
	w.input("a")
	w.waitFor("the title", func() bool { return len(w.states) > 0 && w.states[len(w.states)-1].Title == "a new title" })
	w.input("b")
	w.waitFor("the exit", func() bool { return w.exit != nil })
	w.mu.Lock()
	defer w.mu.Unlock()
	var sawExited bool
	for _, st := range w.states {
		if st.Facts.TUIExited == wire.FactYes {
			sawExited = true
		}
	}
	if !sawExited {
		t.Errorf("no State with the exit: %+v", w.states)
	}
}

func goroutines() int { return runtime.NumGoroutine() }
