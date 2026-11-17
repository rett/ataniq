//go:build linux

package serve

import (
	"errors"
	"net"
	"os"
	"time"

	"ataniq/internal/leadholder/wire"
)

// stallChunk is the most bytes written between two deadlines, so that a client
// that takes bytes slowly but steadily is not a stalled one.
const stallChunk = 16 << 10

// stallWriter writes to a connection with a deadline per chunk: a socket that
// accepts no byte for the stall time fails the write with a timeout.
type stallWriter struct {
	conn  net.Conn
	stall time.Duration
}

func (w *stallWriter) Write(p []byte) (int, error) {
	total := 0
	for len(p) > 0 {
		n := min(len(p), stallChunk)
		_ = w.conn.SetWriteDeadline(time.Now().Add(w.stall))
		m, err := w.conn.Write(p[:n])
		total += m
		if err != nil {
			return total, err
		}
		p = p[n:]
	}
	return total, nil
}

// sender is the one goroutine that writes to a client's socket. It takes the
// frames from the queue in order, encodes them off the hub lock and writes
// them. When the client is removed or the session is over it writes the last
// notice, if there is one, and closes the connection.
func (c *client) sender() {
	s := c.s
	defer close(c.done)
	defer c.conn.Close()
	w := wire.NewWriter(&stallWriter{conn: c.conn, stall: s.cfg.Stall}, wire.ToClient)
	for {
		s.mu.Lock()
		var it item
		have := len(c.q) > 0
		if have {
			it = c.q[0]
			c.q[0] = item{}
			c.q = c.q[1:]
			c.qBytes -= it.cost()
		}
		over, fin := c.dead || c.draining, c.fin
		s.mu.Unlock()
		if have {
			p, err := it.payload()
			if err == nil {
				err = w.WriteFrame(it.typ, p)
			}
			if err != nil {
				s.senderFailed(c, err)
				return
			}
			if it.snap {
				s.mu.Lock()
				c.snapUnsent = false
				s.mu.Unlock()
			}
			continue
		}
		if over {
			if fin != nil {
				_ = w.WriteFrame(wire.TypeNotice, fin) // best effort
			}
			return
		}
		select {
		case <-c.wake:
		case <-s.stop:
			return
		}
	}
}

// senderFailed ends a client whose socket failed: a timeout is a stalled
// client, anything else a client that went away.
func (s *Server) senderFailed(c *client, err error) {
	stalled := errors.Is(err, os.ErrDeadlineExceeded)
	s.mu.Lock()
	already := c.removed
	if stalled {
		s.removeLocked(c, "stalled", "", "", "")
	} else {
		s.removeLocked(c, "write-error", "", "", "")
	}
	s.mu.Unlock()
	if stalled && !already {
		s.count(cntStalled)
		// Best effort: the socket took nothing for the stall time, so this
		// is unlikely to get through.
		if p, perr := wire.EncodeNotice(wire.Notice{Reason: wire.ReasonStalled, Text: "the client did not read for too long"}); perr == nil {
			_ = c.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			_ = wire.Write(c.conn, wire.ToClient, wire.TypeNotice, p)
		}
	}
}
