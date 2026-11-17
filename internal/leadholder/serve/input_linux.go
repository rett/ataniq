//go:build linux

package serve

import (
	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
)

// writerFrame handles an Input, Paste or Resize frame of the writer. It
// returns a code, for the log, for a payload that breaks the protocol (the
// connection is then closed), else "".
func (s *Server) writerFrame(c *client, f wire.Frame) string {
	switch f.Type {
	case wire.TypeInput:
		s.afterInput(c, s.sess.Input(f.Payload))
	case wire.TypePaste:
		text, err := wire.DecodePaste(f.Payload)
		if err != nil {
			return "bad-paste"
		}
		s.afterInput(c, s.sess.Paste(text))
	case wire.TypeResize:
		sz, err := wire.DecodeResize(f.Payload)
		if err != nil {
			return "bad-resize"
		}
		// The session clamps the size to the holder's limits and logs the
		// change; the Size frame follows in step with the output.
		_, _, _ = s.sess.Resize(sz.Columns, sz.Rows)
		s.syncAfterResize()
	}
	return ""
}

// afterInput tells the writer what the session held back (cases H11 and H12).
// This is the one place that looks at what a write to the agent came to.
func (s *Server) afterInput(c *client, h session.Held) {
	if h.Err != nil && s.sess.InputBroken() {
		s.tellInputBroken(c)
	}
	if !h.Any() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if h.Suspend > 0 {
		if it, ok := s.notice(wire.ReasonKeyHeldSuspend, "Ctrl+Z is not sent to the session"); ok {
			s.pushLocked(c, it, false)
		}
	}
	if h.Signal > 0 {
		if it, ok := s.notice(wire.ReasonKeyHeldSignal, "an interrupt, quit or suspend key was held back"); ok {
			s.pushLocked(c, it, false)
		}
	}
}

// reasonInputBroken is the Notice reason for a writer whose input the session
// refuses because an earlier write to the agent failed part-way
// ([session.ErrInputBroken]). It is a plain well-formed reason: wire has no
// constant for it, and a client that does not know it shows the text.
const reasonInputBroken wire.Reason = "input-broken"

// tellInputBroken sends the writer a notice that its input does not reach the
// agent, once for each connection, counts it and logs the first. The write
// that broke the input is told as well as the ones that follow it. The notice
// is forced: a writer may type while its snapshot waits, and it is bounded by
// being sent once.
func (s *Server) tellInputBroken(c *client) {
	s.mu.Lock()
	first := !c.brokenTold
	c.brokenTold = true
	if first {
		if it, ok := s.notice(reasonInputBroken, "the agent's input is broken by a failed write; what you type no longer reaches it"); ok {
			s.pushLocked(c, it, true)
		}
	}
	s.mu.Unlock()
	if first {
		s.count(cntInputBroken)
		s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "input-broken", ClientName: c.name, ClientPID: c.pid})
	}
}
