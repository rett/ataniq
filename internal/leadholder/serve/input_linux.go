//go:build linux

package serve

import (
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
