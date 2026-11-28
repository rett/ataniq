//go:build linux

package serve

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
)

func TestCF05(t *testing.T) {
	newServer := func() *Server {
		return &Server{
			cfg: Config{Epoch: testEpoch, Now: func() time.Time { return time.Unix(1, 0) }, TicketLifetime: time.Minute},
			evq: make(chan evItem, 4),
		}
	}
	issue := func(t *testing.T, s *Server, role wire.Role) string {
		t.Helper()
		ticket, err := wire.NewTicket()
		if err != nil {
			t.Fatal(err)
		}
		if r := s.dispatch(wire.Request{Op: wire.OpTicket, Epoch: testEpoch, Role: role, Ticket: ticket}); !r.OK {
			t.Fatalf("issue %s ticket: %+v", role, r)
		}
		return ticket
	}
	release := func(t *testing.T, s *Server) {
		t.Helper()
		if r := s.dispatch(wire.Request{Op: wire.OpReleaseWriter, Epoch: testEpoch}); !r.OK {
			t.Fatalf("release writer: %+v", r)
		}
	}
	t.Run("ReleaseVoidsOutstandingWriterTicket", func(t *testing.T) {
		s := newServer()
		old := issue(t, s, wire.RoleWriter)
		release(t, s)
		if role, code := s.takeTicket(old); role != "" || code != "attach-ticket-unknown" {
			t.Fatalf("pre-release ticket: role %q, code %q", role, code)
		}
		fresh := issue(t, s, wire.RoleWriter)
		if role, code := s.takeTicket(fresh); role != wire.RoleWriter || code != "" {
			t.Fatalf("fresh ticket: role %q, code %q", role, code)
		}
	})
	t.Run("ViewerTicketsKept", func(t *testing.T) {
		s := newServer()
		issue(t, s, wire.RoleWriter)
		viewer := issue(t, s, wire.RoleViewer)
		release(t, s)
		if role, code := s.takeTicket(viewer); role != wire.RoleViewer || code != "" {
			t.Fatalf("viewer ticket: role %q, code %q", role, code)
		}
	})
}

func TestCF05SocketOneRedemption(t *testing.T) {
	e := newEnv(t, "raw; say READY; wait")
	waitScreen(t, e.sess, "READY")
	p, err := wire.EncodeHello(wire.Hello{
		Version: wire.ProtocolVersion, Ticket: e.newTicket(wire.RoleWriter), Name: "console", Columns: 80, Rows: 24,
	})
	if err != nil {
		t.Fatal(err)
	}
	var conns [MaxHandshakes]*net.UnixConn
	for i := range conns {
		conns[i] = e.dialAttach()
		if err := conns[i].SetDeadline(time.Now().Add(patience)); err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	writes := make(chan error, len(conns))
	for _, conn := range conns {
		go func() {
			<-start
			writes <- wire.Write(conn, wire.ToHolder, wire.TypeHello, p)
		}()
	}
	close(start)
	for range conns {
		if err := <-writes; err != nil {
			t.Fatal(err)
		}
	}
	welcomed := 0
	for _, conn := range conns {
		f, err := wire.NewReader(conn, wire.ToClient).Next()
		if err != nil {
			t.Fatal(err)
		}
		switch f.Type {
		case wire.TypeWelcome:
			w, err := wire.DecodeWelcome(f.Payload)
			if err != nil || w.Role != wire.RoleWriter {
				t.Fatalf("welcome: %+v, %v", w, err)
			}
			welcomed++
		case wire.TypeNotice:
			n, err := wire.DecodeNotice(f.Payload)
			if err != nil || n.Reason != wire.ReasonRefusedTicket {
				t.Fatalf("refusal: %+v, %v", n, err)
			}
		default:
			t.Fatalf("unexpected first frame %s", wire.TypeName(f.Type))
		}
	}
	if welcomed != 1 {
		t.Fatalf("%d of %d Hellos welcomed, want one", welcomed, len(conns))
	}
}

// held reports whether the input mutex is held now.
func (s *Server) held() bool {
	if s.inMu.TryLock() {
		s.inMu.Unlock()
		return false
	}
	return true
}

// Case A3 and the lock order: a writer frame in flight is written before
// release-writer answers, and no byte of the released client reaches the PTY
// after the answer. The frame waits for the session's input lock, which the
// test holds, so that it is certainly in flight when the release is asked for.
func TestReleaseWriterWaitsForAFrameInFlight(t *testing.T) {
	e := newEnv(t, "raw; say READY; echo; wait")
	waitScreen(t, e.sess, "READY")
	w := e.attach(wire.RoleWriter)
	w.waitSnapshots(1)

	locked, free, ended := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		ended <- e.sess.WithInputLock(func(session.AgentWriter) error {
			close(locked)
			<-free
			return nil
		})
	}()
	<-locked
	w.input("A") // blocks in the session, with the input mutex held
	poll(t, func() string { return "the writer frame to be in flight" }, e.srv.held)

	answer := make(chan wire.Response, 1)
	go func() {
		answer <- e.mustControl(wire.Request{Op: wire.OpReleaseWriter, Epoch: testEpoch, CommandID: "rel-1"})
	}()
	select {
	case r := <-answer:
		t.Fatalf("release-writer answered %+v while a writer frame was in flight", r)
	case <-time.After(300 * time.Millisecond):
	}

	close(free)
	if err := <-ended; err != nil {
		t.Fatal(err)
	}
	if r := <-answer; !r.OK {
		t.Fatalf("release-writer: %+v", r)
	}
	// The frame that was in flight was written before the answer.
	waitRX(t, e.sess, "A")

	// Whatever the released client sends now is refused and never written.
	w.waitFor("the role change", func() bool { return w.lastRoleLocked().Role == wire.RoleViewer })
	w.input("B")
	w.waitNotice(wire.ReasonViewerInputRefused)
	waitRX(t, e.sess, "A")
	if e.count(cntViewerInput) != 1 {
		t.Errorf("viewer input refusals %d", e.count(cntViewerInput))
	}
}

// A client that is released while its handshake goes on sets neither the
// colours nor the size of the session: with no viewer it becomes the viewer,
// with one it is taken out. The test holds the input mutex, so that the
// handshake is at its colours and size, and makes the release by hand.
func TestWriterReleasedDuringItsHandshakeSetsNothing(t *testing.T) {
	for _, tc := range []struct {
		name   string
		viewer bool
	}{{"becomes the viewer", false}, {"is taken out", true}} {
		t.Run(tc.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "reply")
			e := newEnv(t, "raw; say READY; echo 1; query osc11,da1 "+file+"; say DONE; wait")
			waitScreen(t, e.sess, "READY")
			if tc.viewer {
				e.attach(wire.RoleViewer).waitSnapshots(1)
			}
			cols, rows := e.sess.Size()

			e.srv.inMu.Lock()
			unlocked := false
			unlock := func() {
				if !unlocked {
					unlocked = true
					e.srv.inMu.Unlock()
				}
			}
			defer unlock()
			var c *tclient
			if tc.viewer {
				c = e.attachWithNoWait(e.newTicket(wire.RoleWriter), func(h *helloOpts) {
					h.cols, h.rows = 90, 40
					h.colors = wire.Colors{Foreground: "#aabbcc", Background: "#112233"}
				})
				c.waitFor("the Welcome", func() bool { return c.welcome != nil || c.end != nil })
			} else {
				c = e.attach(wire.RoleWriter, func(h *helloOpts) {
					h.cols, h.rows = 90, 40
					h.colors = wire.Colors{Foreground: "#aabbcc", Background: "#112233"}
				})
			}
			// The Welcome is sent after the claim: release as the daemon would.
			e.srv.mu.Lock()
			released := e.srv.releaseWriterLocked("rel-2")
			e.srv.mu.Unlock()
			if !released {
				t.Fatal("no writer to release")
			}
			unlock()

			if tc.viewer {
				c.waitEnd()
			} else {
				c.waitSnapshots(1)
			}
			if gc, gr := e.sess.Size(); gc != cols || gr != rows {
				t.Errorf("the session is %dx%d, was %dx%d: the released client resized it", gc, gr, cols, rows)
			}
			// The agent asks for the background colour: the answer is not the
			// released client's.
			e.sess.Input([]byte("x"))
			waitScreen(t, e.sess, "DONE")
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(b); !strings.Contains(got, "\x1b]11;") || strings.Contains(got, "rgb:1111/2222/3333") {
				t.Errorf("the agent's reply %q holds the released client's colour or no answer", got)
			}
		})
	}
}
