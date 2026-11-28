//go:build linux

package serve

import (
	"encoding/json"
	"errors"
	"image/color"
	"net"
	"time"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole/sessionpane"
)

// handleAttach is one attach connection, from accept to close.
func (s *Server) handleAttach(conn *net.UnixConn) {
	cred, err := s.cfg.PeerCred(conn)
	if err != nil || cred.UID != s.uid {
		s.count(cntRefusedUID)
		s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "attach-peer-uid", ClientPID: cred.PID})
		return
	}
	s.mu.Lock()
	full := s.handshaking >= MaxHandshakes
	if !full {
		s.handshaking++
	}
	s.mu.Unlock()
	if full {
		s.count(cntRefusedFull)
		s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "attach-handshake-full", ClientPID: cred.PID})
		return
	}
	c, rd := s.handshake(conn, cred)
	if c == nil {
		return
	}
	s.serveClient(c, rd)
}

// refuse answers a Hello that is refused for a known cause with a notice (best
// effort), counts it and logs it. The caller closes the connection.
func (s *Server) refuse(conn *net.UnixConn, r wire.Reason, text, counter, code string, pid int) {
	if r != "" {
		if p, err := wire.EncodeNotice(wire.Notice{Reason: r, Text: text}); err == nil {
			_ = conn.SetWriteDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
			_ = wire.Write(conn, wire.ToClient, wire.TypeNotice, p)
		}
	}
	s.count(counter)
	s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: code, ClientPID: pid})
}

// handshake reads the Hello, checks it and, when it is good, claims the slot,
// sends the Welcome and registers the client. It leaves the handshake count
// whatever happens. It returns nil when the connection is refused.
func (s *Server) handshake(conn *net.UnixConn, cred PeerCred) (*client, *wire.Reader) {
	defer func() {
		s.mu.Lock()
		s.handshaking--
		s.mu.Unlock()
	}()
	_ = conn.SetReadDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	rd := wire.NewReader(conn, wire.ToHolder)
	f, err := rd.Next()
	if err != nil || f.Type != wire.TypeHello {
		code := "handshake-bad-first-frame"
		var ne net.Error
		switch {
		case err == nil:
		case errors.As(err, &ne) && ne.Timeout():
			code = "handshake-timeout"
		case errors.Is(err, wire.ErrUnknownType), errors.Is(err, wire.ErrTooLarge), errors.Is(err, wire.ErrTooSmall):
		default:
			code = "handshake-closed"
		}
		s.refuse(conn, "", "", cntRefusedHandshake, code, cred.PID)
		return nil, nil
	}
	hello, err := wire.DecodeHello(f.Payload)
	if err != nil {
		// A ticket that was presented is used up whatever else is wrong with
		// the Hello (a name too long, a colour, a size, a field this decoder
		// does not know): it must not be presentable a second time.
		if tk, ok := presentedTicket(f.Payload); ok {
			_, _ = s.takeTicket(tk)
		}
		switch v, ok := wire.HelloVersion(f.Payload); {
		case ok && v != wire.ProtocolVersion:
			s.refuse(conn, wire.ReasonRefusedVersion, "this holder speaks another protocol version", cntRefusedVersion, "attach-version", cred.PID)
		case ticketProblem(f.Payload):
			s.refuse(conn, wire.ReasonRefusedTicket, "no valid ticket", cntRefusedTicket, "attach-ticket-missing", cred.PID)
		default:
			s.refuse(conn, "", "", cntRefusedHandshake, "handshake-bad-hello", cred.PID)
		}
		return nil, nil
	}
	if hello.Version != wire.ProtocolVersion {
		s.refuse(conn, wire.ReasonRefusedVersion, "this holder speaks another protocol version", cntRefusedVersion, "attach-version", cred.PID)
		return nil, nil
	}
	// The command line is read before the client is claimed: it does not
	// depend on the claim, and once the client holds a place the timekeeper
	// reads it under the lock, for the Role frames of the other clients.
	c := &client{
		s: s, conn: conn, name: hello.Name, pid: cred.PID, cmd: readCmdline(cred.PID),
		wake: make(chan struct{}, 1), done: make(chan struct{}),
	}
	// Release must see either the outstanding ticket or the claimed slot.
	s.mu.Lock()
	role, status := s.takeTicketLocked(hello.Ticket)
	ok := status == "" && !s.closed && !s.finishing && s.claimLocked(c, role)
	s.mu.Unlock()
	if status != "" {
		s.refuse(conn, wire.ReasonRefusedTicket, "the ticket is not valid", cntRefusedTicket, status, cred.PID)
		return nil, nil
	}
	if !ok {
		s.refuse(conn, wire.ReasonRefusedBusy, "the "+string(role)+" place is taken", cntRefusedBusy, "attach-busy", cred.PID)
		return nil, nil
	}
	welcome, err := wire.EncodeWelcome(wire.Welcome{
		Version: wire.ProtocolVersion, Role: role, Lead: s.cfg.Lead, Epoch: s.cfg.Epoch,
		Runtime: s.cfg.Runtime, RuntimeVersion: s.cfg.RuntimeVersion, Caps: wire.DefaultCaps(),
	})
	if err == nil {
		_ = conn.SetWriteDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
		err = wire.Write(conn, wire.ToClient, wire.TypeWelcome, welcome)
	}
	if err != nil {
		s.unclaim(c)
		return nil, nil
	}
	if role == wire.RoleWriter {
		s.applyWriterHello(c, hello)
	}
	s.mu.Lock()
	ok = s.registerLocked(c)
	s.mu.Unlock()
	if !ok {
		s.unclaim(c)
		return nil, nil
	}
	cols, rows := s.sess.Size()
	s.emit(eventlog.EventAttach, eventlog.Fields{
		ClientName: c.name, ClientPID: c.pid, ClientCommand: c.cmd, Reason: string(role), Cols: cols, Rows: rows,
	})
	_ = conn.SetReadDeadline(time.Time{})
	_ = conn.SetWriteDeadline(time.Time{})
	return c, rd
}

// applyWriterHello sets the colours and the size of the session from the Hello
// of a client that claimed the writer slot. It runs with the input mutex held
// and acts only when the client is still the writer then: one that was
// released since the claim (release-writer made it the viewer, or took it out)
// sets neither.
func (s *Server) applyWriterHello(c *client, hello wire.Hello) {
	s.inMu.Lock()
	defer s.inMu.Unlock()
	s.mu.Lock()
	still := s.writer == c && c.role == wire.RoleWriter && !c.removed
	s.mu.Unlock()
	if !still {
		return
	}
	// The writer's colours answer the agent's colour queries, and its size is
	// the session's.
	s.sess.SetColours(helloColours(hello.Colors))
	_, _, _ = s.sess.Resize(hello.Columns, hello.Rows)
	s.syncAfterResize()
}

// presentedTicket reads the ticket of a Hello payload that the strict decoder
// refused, as far as it can be told: it reports false when the payload has no
// ticket or one that is not well formed.
func presentedTicket(payload []byte) (string, bool) {
	var probe struct {
		Ticket *string `json:"ticket"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil || probe.Ticket == nil || wire.CheckTicket(*probe.Ticket) != nil {
		return "", false
	}
	return *probe.Ticket, true
}

// ticketProblem reports whether a Hello payload that the strict decoder
// refused is, as far as can be told, one with no usable ticket: then the
// answer is a refused-ticket notice and not a silent close.
func ticketProblem(payload []byte) bool {
	var probe struct {
		Ticket *string `json:"ticket"`
	}
	if err := json.Unmarshal(payload, &probe); err != nil {
		return false
	}
	return probe.Ticket == nil || wire.CheckTicket(*probe.Ticket) != nil
}

// unclaim gives back the slot of a client that never attached.
func (s *Server) unclaim(c *client) {
	s.mu.Lock()
	if s.writer == c {
		s.clearWriterLocked()
	}
	if s.viewer == c {
		s.viewer = nil
	}
	s.mu.Unlock()
}

// helloColours converts the colours of a Hello; an empty one is left unknown.
func helloColours(c wire.Colors) sessionpane.Colours {
	return sessionpane.Colours{Foreground: parseColour(c.Foreground), Background: parseColour(c.Background), Cursor: parseColour(c.Cursor)}
}

// parseColour reads #rrggbb, which wire has checked; "" is no colour.
func parseColour(s string) color.Color {
	if len(s) != 7 {
		return nil
	}
	var v [3]uint8
	for i := 0; i < 3; i++ {
		hi, lo := hexVal(s[1+2*i]), hexVal(s[2+2*i])
		v[i] = hi<<4 | lo
	}
	return color.RGBA{R: v[0], G: v[1], B: v[2], A: 0xff}
}

func hexVal(c byte) uint8 {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

// syncAfterResize tells the clients of the size after a resize, in step with
// the output (under the session's output lock).
func (s *Server) syncAfterResize() {
	s.sess.Screen(func(p *sessionpane.Pane, _ uint64) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.closed && !s.finishing {
			s.syncSizeLocked(p)
		}
	})
}

// serveClient runs an attached client: its sender, and its reader in this
// goroutine, until the client leaves or is removed.
func (s *Server) serveClient(c *client, rd *wire.Reader) {
	if !s.spawn(c.sender) {
		s.mu.Lock()
		s.removeLocked(c, "closing", "", "", "")
		s.mu.Unlock()
		close(c.done)
		return
	}
	reason := s.readLoop(c, rd)
	s.mu.Lock()
	s.removeLocked(c, reason, "", "", "")
	fin := c.fin != nil
	s.mu.Unlock()
	if !fin {
		c.conn.Close()
	}
	<-c.done
}

// badFrame counts and logs a frame that closes the connection.
func (s *Server) badFrame(c *client, code string) string {
	s.count(cntBadFrames)
	s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: code, ClientName: c.name, ClientPID: c.pid})
	return "bad-frame"
}

// readLoop reads the client's frames until it detaches or breaks the protocol.
// It returns the reason for the log.
func (s *Server) readLoop(c *client, rd *wire.Reader) string {
	for {
		f, err := rd.Next()
		if err != nil {
			switch {
			case errors.Is(err, wire.ErrUnknownType):
				return s.badFrame(c, "unknown-frame-type")
			case errors.Is(err, wire.ErrTooLarge):
				return s.badFrame(c, "frame-too-large")
			case errors.Is(err, wire.ErrTooSmall):
				return s.badFrame(c, "frame-too-small")
			}
			return "eof"
		}
		switch f.Type {
		case wire.TypeDetach:
			return "detach"
		case wire.TypeInput, wire.TypePaste, wire.TypeResize:
			if code, ok := s.forwardWriterFrame(c, f); !ok {
				s.refuseViewerInput(c, f.Type)
			} else if code != "" {
				return s.badFrame(c, code)
			}
		default: // a second Hello
			return s.badFrame(c, "unexpected-frame")
		}
	}
}

// forwardWriterFrame hands a frame of c to the session when c is the writer,
// with the input mutex held from the check to the end of the call (see the
// package doc). It reports false, and does nothing, when c is not the writer;
// else it returns the code of writerFrame.
func (s *Server) forwardWriterFrame(c *client, f wire.Frame) (string, bool) {
	s.inMu.Lock()
	defer s.inMu.Unlock()
	if !s.isWriter(c) {
		return "", false
	}
	return s.writerFrame(c, f), true
}

func (s *Server) isWriter(c *client) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return c.role == wire.RoleWriter && s.writer == c
}

// refuseViewerInput answers an Input, Paste or Resize from a client that is
// not the writer: a notice and a count, never forwarded (design 2.4). Only the
// first is logged for each client; the counter has the rest.
func (s *Server) refuseViewerInput(c *client, typ byte) {
	s.count(cntViewerInput)
	s.mu.Lock()
	first := !c.viewerLogged
	c.viewerLogged = true
	if it, ok := s.notice(wire.ReasonViewerInputRefused, "a viewer cannot send "+wire.TypeName(typ)); ok {
		s.pushLocked(c, it, false)
	}
	s.mu.Unlock()
	if first {
		s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "viewer-input-refused", ClientName: c.name, ClientPID: c.pid})
	}
}
