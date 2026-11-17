//go:build linux

package serve

import (
	"net"
	"time"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
)

// handleControl is one control connection: one request, one response, within
// the deadline.
func (s *Server) handleControl(conn *net.UnixConn) {
	cred, err := s.cfg.PeerCred(conn)
	if err != nil || cred.UID != s.uid {
		s.count(cntControlPeer)
		s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "control-peer-uid", ClientPID: cred.PID})
		return
	}
	// The peer must be the daemon (case A12).
	if code, ok := s.checkControlPeer(conn); !ok {
		s.count(cntControlPeer)
		s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "control-peer-" + code, ClientPID: cred.PID})
		return
	}
	s.mu.Lock()
	busy := s.controlBusy >= maxControlConns
	if !busy {
		s.controlBusy++
	}
	s.mu.Unlock()
	if busy {
		s.count(cntControlOther)
		s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "control-busy"})
		return
	}
	defer func() {
		s.mu.Lock()
		s.controlBusy--
		s.mu.Unlock()
	}()
	_ = conn.SetDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
	f, err := wire.NewReader(conn, wire.ControlRequests).Next()
	if err != nil {
		s.count(cntControlOther)
		s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "control-bad-frame"})
		return
	}
	req, err := wire.DecodeRequest(f.Payload)
	if err != nil {
		s.count(cntControlOther)
		s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "control-bad-request"})
		s.respond(conn, refusal(wire.CodeBadRequest, "the request is not valid"))
		return
	}
	s.respond(conn, s.dispatch(req))
}

func refusal(code, text string) wire.Response { return wire.Response{Code: code, Text: text} }

func (s *Server) respond(conn *net.UnixConn, r wire.Response) {
	p, err := wire.EncodeResponse(r)
	if err != nil {
		p, err = wire.EncodeResponse(refusal(wire.CodeBadRequest, "the answer could not be made"))
	}
	if err == nil {
		// A request may have waited (release-writer waits for a write in
		// flight): the answer has a time of its own.
		_ = conn.SetWriteDeadline(time.Now().Add(s.cfg.HandshakeTimeout))
		_ = wire.Write(conn, wire.ControlResponses, wire.TypeResponse, p)
	}
}

// dispatch carries out one request. Every request carries the epoch it is meant
// for, and one for another epoch is refused and logged (case H8).
func (s *Server) dispatch(req wire.Request) wire.Response {
	if req.Epoch != s.cfg.Epoch {
		s.count(cntControlEpoch)
		s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "wrong-epoch", CommandID: req.CommandID})
		return refusal(wire.CodeWrongEpoch, "the request is for another epoch")
	}
	resp := s.execute(req)
	outcome := "ok"
	if !resp.OK {
		outcome = resp.Code
		if resp.Code != wire.CodeDisabled {
			s.count(cntControlOther)
		}
	}
	s.emit(eventlog.EventState, eventlog.Fields{State: "command", Reason: string(req.Op), Outcome: outcome, CommandID: req.CommandID})
	return resp
}

func (s *Server) execute(req wire.Request) wire.Response {
	switch req.Op {
	case wire.OpTicket:
		return s.issueTicket(req.Role, req.Ticket)
	case wire.OpStatus:
		st := s.status()
		return wire.Response{OK: true, Status: &st}
	case wire.OpStop:
		// Stop waits for the agent's tree: do it in the background.
		s.spawn(s.sess.Stop)
		return wire.Response{OK: true}
	case wire.OpReleaseWriter:
		// The command is the daemon's, not an owner decision, so it is a
		// holder event with the command id.
		// The input mutex first: a writer frame in flight is written before
		// the answer, and none follows it.
		s.inMu.Lock()
		s.mu.Lock()
		s.releaseWriterLocked(req.CommandID)
		s.mu.Unlock()
		s.inMu.Unlock()
		return wire.Response{OK: true}
	case wire.OpInject:
		// Injection is built elsewhere; the session logs the refusal.
		_ = s.sess.Inject(session.InjectRequest{})
		return refusal(wire.CodeDisabled, "injection is disabled")
	}
	return refusal(wire.CodeBadRequest, "unknown operation")
}

// issueTicket registers a ticket for a role. A writer ticket is refused with
// busy while a writer is attached or another writer ticket waits, a viewer
// ticket in the same way, and at most MaxTickets tickets wait (case A3).
func (s *Server) issueTicket(role wire.Role, ticket string) wire.Response {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.finishing {
		return refusal(wire.CodeBusy, "the holder is closing")
	}
	live := s.tickets[:0]
	for _, t := range s.tickets {
		if now.Before(t.expires) {
			live = append(live, t)
		}
	}
	for i := len(live); i < len(s.tickets); i++ {
		s.tickets[i] = ticketRec{}
	}
	s.tickets = live
	taken := s.writer
	if role == wire.RoleViewer {
		taken = s.viewer
	}
	if taken != nil {
		return refusal(wire.CodeBusy, "a "+string(role)+" is attached")
	}
	for _, t := range s.tickets {
		if t.role == role {
			return refusal(wire.CodeBusy, "a "+string(role)+" ticket is outstanding")
		}
		if sameTicket(t.ticket, ticket) {
			return refusal(wire.CodeBadRequest, "the ticket is registered already")
		}
	}
	if len(s.tickets) >= MaxTickets {
		return refusal(wire.CodeBusy, "too many tickets are outstanding")
	}
	s.tickets = append(s.tickets, ticketRec{ticket: ticket, role: role, expires: now.Add(s.cfg.TicketLifetime)})
	return wire.Response{OK: true}
}

// takeTicket consumes the ticket a Hello presents: the first use of a ticket
// uses it up, whether or not the attach completes. It returns the role the
// ticket is for, or a code for the log when there is none: the ticket is not
// registered (never, or used) or has expired. Every ticket is compared in
// constant time.
func (s *Server) takeTicket(presented string) (wire.Role, string) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	hit := -1
	for i, t := range s.tickets {
		if sameTicket(t.ticket, presented) && hit < 0 {
			hit = i
		}
	}
	if hit < 0 {
		return "", "attach-ticket-unknown"
	}
	t := s.tickets[hit]
	s.tickets = append(s.tickets[:hit], s.tickets[hit+1:]...)
	if !now.Before(t.expires) {
		return "", "attach-ticket-expired"
	}
	return t.role, ""
}

// status is the holder's answer to a status request.
func (s *Server) status() wire.Status {
	cols, rows := s.sess.Size()
	facts, _ := wireFacts(s.sess.Facts())
	counters := s.allCounters()
	title := ""
	if s.pane != nil {
		title = wire.Sanitize(s.pane.Title(), wire.MaxTitle, false)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return wire.Status{
		Lead: s.cfg.Lead, Epoch: s.cfg.Epoch, Runtime: s.cfg.Runtime, Idle: wire.IdleUnknown, Title: title,
		Size: wire.Size{Columns: cols, Rows: rows}, Caps: wire.DefaultCaps(),
		WriterPresent: s.writer != nil, ViewerPresent: s.viewer != nil,
		Counters: counters, Facts: facts,
	}
}
