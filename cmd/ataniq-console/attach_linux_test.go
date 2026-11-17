//go:build linux

package main

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/sys/unix"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/fakeagent"
	"ataniq/internal/leadholder/ptyhost"
	"ataniq/internal/leadholder/serve"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
)

// The attach tests run a real holder (a session on a fake agent, and the
// attach and control service) inside the test, and a console model that
// parse and attachModel build from the same command line `run` takes. The
// model is driven without a terminal: the test sends it messages and carries
// out the commands it returns, as the Bubble Tea program would. The fake agent
// is this test binary run again, so TestMain hands control to it first.

func TestMain(m *testing.M) {
	fakeagent.RunIfChild()
	os.Exit(m.Run())
}

const (
	// patience bounds every wait. A wait polls; nothing sleeps for a fixed time.
	patience = 15 * time.Second

	holderEpoch = 7
	leadName    = "proj-lead"
	winCols     = 120
	winRows     = 40
)

// holder is one lead holder under test. The test acts as the daemon: it holds
// the daemon lock with its own pid and registers the tickets.
type holder struct {
	t       *testing.T
	dir     string // the runtime folder: it holds attach.sock
	logDir  string
	control string
	srv     *serve.Server
	daemon  *os.File
}

// startHolder starts the fake agent on script and the service on it. The
// folder is short: a unix socket address holds 107 bytes.
func startHolder(t *testing.T, script string) *holder {
	t.Helper()
	root, err := os.MkdirTemp("", "ac")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	h := &holder{t: t, dir: filepath.Join(root, "rt"), logDir: filepath.Join(root, "log")}
	state := filepath.Join(root, "state")
	for _, d := range []string{h.dir, h.logDir, state} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	log, err := eventlog.Open(h.logDir, eventlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	argv, env := fakeagent.Command(script)
	sess, err := session.Start(session.Config{
		Role: "proj/lead", Epoch: strconv.Itoa(holderEpoch), Argv: argv, Env: env, Cols: 100, Rows: 30,
		RoleLock: filepath.Join(root, "lead.lock"), StateDir: state, Log: log,
		StopGrace: 300 * time.Millisecond, PTY: ptyhost.Config{NoCgroup: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })

	// The daemon stand-in: its pid in the lock file, and the lock held.
	lock := filepath.Join(root, "daemon.lock")
	if err := os.WriteFile(lock, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if h.daemon, err = os.Open(lock); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.daemon.Close() })
	if err := unix.Flock(int(h.daemon.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}

	h.srv, err = serve.New(serve.Config{
		Session: sess, Dir: h.dir, Lead: leadName, Epoch: holderEpoch, Runtime: "fake", RuntimeVersion: "1.2.3",
		Log: log, DaemonLock: lock,
		HandshakeTimeout: 3 * time.Second, FlushWait: 3 * time.Second, Stall: 10 * time.Second,
		PollInterval: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.srv.Close() })
	go h.srv.Serve()
	if n := len(filepath.Join(h.dir, attachSocketName)); n > maxSocketPath {
		t.Fatalf("the test's socket path is %d bytes", n)
	}
	return h
}

// request sends one control request and returns the response.
func (h *holder) request(req wire.Request) wire.Response {
	h.t.Helper()
	var resp wire.Response
	poll(h.t, "the control socket", func() (string, bool) {
		c, err := dialIn(h.dir, serve.ControlSocket)
		if err != nil {
			return err.Error(), false
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(patience))
		p, err := wire.EncodeRequest(req)
		if err != nil {
			h.t.Fatal(err)
		}
		if err := wire.Write(c, wire.ControlRequests, wire.TypeRequest, p); err != nil {
			return err.Error(), false
		}
		f, err := wire.NewReader(c, wire.ControlResponses).Next()
		if err != nil {
			return err.Error(), false
		}
		if resp, err = wire.DecodeResponse(f.Payload); err != nil {
			h.t.Fatal(err)
		}
		return "", true
	})
	return resp
}

// ticket registers a single-use ticket for role, as the daemon would, and
// returns it. A writer ticket is refused while a writer is attached.
func (h *holder) ticket(role wire.Role) string {
	h.t.Helper()
	tk, err := wire.NewTicket()
	if err != nil {
		h.t.Fatal(err)
	}
	if r := h.request(wire.Request{Op: wire.OpTicket, Epoch: holderEpoch, Role: role, Ticket: tk}); !r.OK {
		h.t.Fatalf("register a %s ticket: %+v", role, r)
	}
	return tk
}

// waitNoWriter waits until the holder has no writer: a ticket for the next one
// is refused before that.
func (h *holder) waitNoWriter() {
	h.t.Helper()
	poll(h.t, "the writer to leave", func() (string, bool) {
		r := h.request(wire.Request{Op: wire.OpStatus, Epoch: holderEpoch})
		return fmt.Sprintf("%+v", r), r.OK && r.Status != nil && !r.Status.WriterPresent
	})
}

// waitDetach waits for the event log to record a clean detach.
func (h *holder) waitDetach() {
	h.t.Helper()
	poll(h.t, "the detach in the event log", func() (string, bool) {
		evs, err := eventlog.Read(h.logDir)
		if err != nil && !errors.Is(err, eventlog.ErrCorrupt) {
			h.t.Fatal(err)
		}
		for _, ev := range evs {
			if ev.Event == eventlog.EventDetach && ev.Reason == "detach" {
				return "", true
			}
		}
		return fmt.Sprintf("%d events, none a detach", len(evs)), false
	})
}

// dialIn connects to a socket in dir through /proc/self/fd, which keeps the
// address short.
func dialIn(dir, name string) (*net.UnixConn, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	return net.DialUnix("unix", nil, &net.UnixAddr{Name: fmt.Sprintf("/proc/self/fd/%d/%s", fd, name), Net: "unix"})
}

// poll calls f until it says it is done, or fails the test after patience.
func poll(t *testing.T, what string, f func() (last string, done bool)) {
	t.Helper()
	deadline := time.Now().Add(patience)
	for {
		last, done := f()
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s: %s", what, last)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// writeTicket puts a ticket in the file the way the daemon would: a new 0600
// file renamed into place.
func writeTicket(t *testing.T, path, ticket string) {
	t.Helper()
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(ticket+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// driver runs a console model without a terminal. The test goroutine alone
// calls the model; the commands the model returns run on goroutines of their
// own and their messages wait in the inbox until the test takes them.
type driver struct {
	t      *testing.T
	m      *terminalconsole.Model
	inbox  chan tea.Msg
	done   chan struct{}
	quit   bool
	ticket string
}

// attachConsole builds the console for the command line the way run does and
// gives it a window. args is what follows the command name; the holder's
// folder and a new ticket file are added.
func (h *holder) attachConsole(extra ...string) *driver {
	h.t.Helper()
	ticketFile := filepath.Join(h.t.TempDir(), "ticket")
	args := append([]string{"--attach", h.dir, "--ticket-file", ticketFile}, extra...)
	var errout strings.Builder
	cfg, code, ok := parse(args, &errout)
	if !ok || code != 0 || cfg.attach == nil {
		h.t.Fatalf("parse %v: %d %s", extra, code, errout.String())
	}
	d := &driver{t: h.t, m: attachModel(cfg), inbox: make(chan tea.Msg, 64), done: make(chan struct{}), ticket: ticketFile}
	h.t.Cleanup(func() {
		close(d.done)
		d.m.Close()
	})
	d.send(tea.WindowSizeMsg{Width: winCols, Height: winRows})
	return d
}

func (d *driver) send(msg tea.Msg) {
	_, cmd := d.m.Update(msg)
	d.run(cmd)
}

// run carries out a command on a goroutine of its own. A batch is carried out
// command by command.
func (d *driver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	go func() {
		d.deliver(cmd())
	}()
}

func (d *driver) deliver(msg tea.Msg) {
	switch v := msg.(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range v {
			d.run(c)
		}
	default:
		select {
		case d.inbox <- msg:
		case <-d.done:
		}
	}
}

// pump takes one message of the inbox, if one arrives within wait, and gives
// it to the model.
func (d *driver) pump(wait time.Duration) {
	select {
	case msg := <-d.inbox:
		if _, ok := msg.(tea.QuitMsg); ok {
			d.quit = true
			return
		}
		d.send(msg)
	case <-time.After(wait):
	}
}

// view is the screen as text.
func (d *driver) view() string { return ansi.Strip(d.m.View().Content) }

// until pumps messages until cond holds. cond may read the view.
func (d *driver) until(what string, cond func() bool) {
	d.t.Helper()
	deadline := time.Now().Add(patience)
	for !cond() {
		if time.Now().After(deadline) {
			d.t.Fatalf("timed out waiting for %s. The screen:\n%s", what, d.view())
		}
		d.pump(20 * time.Millisecond)
	}
}

func (d *driver) waitView(what string) {
	d.t.Helper()
	d.until("the screen to show "+strconv.Quote(what), func() bool { return strings.Contains(d.view(), what) })
}

func (d *driver) waitViewAll(whats ...string) {
	d.t.Helper()
	d.until(fmt.Sprintf("the screen to show all of %q", whats), func() bool {
		v := d.view()
		for _, w := range whats {
			if !strings.Contains(v, w) {
				return false
			}
		}
		return true
	})
}

// waitQuit waits for the console to have asked the program to quit.
func (d *driver) waitQuit() {
	d.t.Helper()
	d.until("the quit", func() bool { return d.quit })
}

// focusSession gives the session pane the focus. The session must show its
// first screen already: the caller waits for the agent's own words, and as the
// writer for the session's paste mode, which the view asks of the outer
// terminal.
func (d *driver) focusSession() {
	d.t.Helper()
	d.send(tea.KeyPressMsg{Code: tea.KeyTab})
	d.send(tea.KeyPressMsg{Code: tea.KeyEnter})
	if v := d.view(); !strings.Contains(v, "SESSION FOCUS") {
		d.t.Fatalf("the session did not take the focus:\n%s", v)
	}
}

// waitPasteMode waits for the console to know that the session set bracketed
// paste: as the writer in session focus, it then leaves the outer terminal's
// bracketing on.
func (d *driver) waitPasteMode() {
	d.t.Helper()
	d.until("the session's paste mode in the status", func() bool { return !d.m.View().DisableBracketedPasteMode })
}

func (d *driver) typ(s string) {
	for _, c := range s {
		d.send(tea.KeyPressMsg{Code: c, Text: string(c)})
	}
}

var rxLine = regexp.MustCompile(`RX:([0-9a-f]+)`)

// received is the hex of everything the fake agent echoed so far, in order,
// as the view shows it. The agent writes "RX:" and the hex of each read.
func (d *driver) received() string {
	var b strings.Builder
	for _, m := range rxLine.FindAllStringSubmatch(d.view(), -1) {
		b.WriteString(m[1])
	}
	return b.String()
}

func (d *driver) waitReceived(want string) {
	d.t.Helper()
	d.until("the agent to have received "+want, func() bool {
		got := d.received()
		if len(got) > len(want) || !strings.HasPrefix(want, got) {
			d.t.Fatalf("the agent received %s, want %s", got, want)
		}
		return got == want
	})
}

func hexOf(s string) string { return hex.EncodeToString([]byte(s)) }

// The agent: raw mode, bracketed paste on, a word to wait for, and the echo
// of every read.
const echoAgent = "raw; mode paste; say READY; echo"

var ctrlBackslash = tea.KeyPressMsg{Code: '\\', Mod: tea.ModCtrl}

func TestAttachWriterShowsTheScreenAndSendsKeysAndPaste(t *testing.T) {
	h := startHolder(t, echoAgent)
	d := h.attachConsole()
	writeTicket(t, d.ticket, h.ticket(wire.RoleWriter))

	d.waitView("READY")
	d.waitViewAll("live scratch session", "lead "+leadName, "writer")
	d.focusSession()
	d.waitPasteMode()

	// Ctrl+Z is not forwarded: the agent reads h and i and nothing between.
	d.typ("h")
	d.send(tea.KeyPressMsg{Code: 'z', Mod: tea.ModCtrl})
	d.typ("i")
	d.waitReceived(hexOf("hi"))
	d.waitView("Ctrl+Z is not sent to the session")

	// A paste arrives wrapped, as the agent asked.
	d.send(tea.PasteMsg{Content: "ab"})
	d.waitReceived(hexOf("hi") + hexOf("\x1b[200~ab\x1b[201~"))

	// The return key leaves the session, and q quits from the console. The
	// holder sees a clean detach.
	d.send(ctrlBackslash)
	d.waitView("CONSOLE FOCUS")
	d.typ("q")
	d.waitQuit()
	h.waitDetach()
	h.waitNoWriter()
}

func TestAttachViewerShowsTheScreenAndSendsNothing(t *testing.T) {
	h := startHolder(t, echoAgent)
	writer := h.attachConsole()
	viewer := h.attachConsole("--viewer")
	writeTicket(t, writer.ticket, h.ticket(wire.RoleWriter))
	writeTicket(t, viewer.ticket, h.ticket(wire.RoleViewer))

	writer.waitViewAll("READY", "writer")
	viewer.waitViewAll("READY", "read-only viewer")
	writer.focusSession()
	writer.waitPasteMode()
	viewer.focusSession()
	if v := viewer.view(); !strings.Contains(v, "not sent: read-only viewer") {
		t.Fatalf("the viewer's focus bar does not say its keys are not sent:\n%s", v)
	}

	// The viewer types and pastes first. The writer's keys then arrive after
	// anything of the viewer's that was sent, so that the agent has echoed
	// only the writer's by the time the writer's are seen.
	viewer.typ("v")
	viewer.send(tea.PasteMsg{Content: "pv"})
	writer.typ("w")
	writer.waitReceived(hexOf("w"))
	// The viewer shows what the writer typed, from the same screen, and
	// nothing of its own.
	viewer.waitReceived(hexOf("w"))
	if got := viewer.received(); got != hexOf("w") {
		t.Fatalf("the viewer's screen shows %s", got)
	}
}

func TestAttachViewerFlagWithAWriterTicketSendsNothing(t *testing.T) {
	h := startHolder(t, echoAgent)
	d := h.attachConsole("--viewer")
	writeTicket(t, d.ticket, h.ticket(wire.RoleWriter))
	d.waitViewAll("READY", "writer")
	// The ticket decided the place. The client did not ask to write, so it
	// says so and sends no input.
	d.waitView("Asked to attach as viewer")
	d.focusSession()
	d.typ("k")
	d.send(tea.PasteMsg{Content: "p"})
	d.send(ctrlBackslash)
	d.typ("q")
	d.waitQuit()
	h.waitDetach()

	// Nothing of it reached the agent: a console that does write sees only its
	// own key.
	h.waitNoWriter()
	w := h.attachConsole()
	writeTicket(t, w.ticket, h.ticket(wire.RoleWriter))
	w.waitView("READY")
	w.focusSession()
	w.waitPasteMode()
	w.typ("w")
	w.waitReceived(hexOf("w"))
}

func TestAttachAgentExitShowsTheEndBanner(t *testing.T) {
	h := startHolder(t, "raw; mode paste; say READY; echo 1; exit 3")
	d := h.attachConsole()
	writeTicket(t, d.ticket, h.ticket(wire.RoleWriter))
	d.waitView("READY")
	d.focusSession()
	d.waitPasteMode()
	d.typ("x")
	// The three facts, each its own line, and the status.
	d.waitViewAll("SESSION ENDED", "terminal stream ended", "TUI exited with status 3", "process tree gone")

	// Any key returns to the console, and q leaves it.
	d.typ("x")
	d.waitView("CONSOLE FOCUS")
	d.typ("q")
	d.waitQuit()
}

func TestAttachASecondAttachWithAFreshTicketAfterClosing(t *testing.T) {
	h := startHolder(t, echoAgent)
	first := h.attachConsole()
	writeTicket(t, first.ticket, h.ticket(wire.RoleWriter))
	first.waitView("READY")
	first.focusSession()
	first.waitPasteMode()
	first.typ("a")
	first.waitReceived(hexOf("a"))
	first.send(ctrlBackslash)
	first.typ("q")
	first.waitQuit()
	h.waitDetach()
	h.waitNoWriter()

	// The holder kept the session: a console that attaches later shows what the
	// first one left, and its key follows.
	second := h.attachConsole()
	writeTicket(t, second.ticket, h.ticket(wire.RoleWriter))
	second.waitViewAll("READY", "RX:"+hexOf("a"))
	second.focusSession()
	second.waitPasteMode()
	second.typ("b")
	second.waitReceived(hexOf("a") + hexOf("b"))
}

func TestAttachAUsedTicketLeavesTheConsoleWaitingForAFreshOne(t *testing.T) {
	h := startHolder(t, echoAgent)
	first := h.attachConsole()
	used := h.ticket(wire.RoleWriter)
	writeTicket(t, first.ticket, used)
	first.waitView("READY")
	first.focusSession()
	first.send(ctrlBackslash)
	first.typ("q")
	first.waitQuit()
	h.waitNoWriter()

	// The used ticket is in the file again: the holder refuses it, and the
	// console waits.
	second := h.attachConsole()
	writeTicket(t, second.ticket, used)
	second.waitViewAll("WAITING FOR A TICKET", "refused the ticket")
	// The file was emptied: the ticket is single use.
	if b, err := os.ReadFile(second.ticket); err != nil || len(b) != 0 {
		t.Fatalf("the ticket file holds %q (%v)", b, err)
	}

	// A fresh ticket in the file attaches it, within the client's polling.
	writeTicket(t, second.ticket, h.ticket(wire.RoleWriter))
	second.waitViewAll("READY", "writer")
	second.focusSession()
	second.waitPasteMode()
	second.typ("c")
	second.waitReceived(hexOf("c"))
}
