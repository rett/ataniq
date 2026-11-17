package leadattach

import (
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
	"ataniq/internal/terminalconsole/sessionpane"
)

// conn is one connection to the holder.
type conn struct {
	nc net.Conn
	q  *outQueue

	stop     chan struct{}
	stopOnce sync.Once
	sendDone chan struct{}

	// lost counts the pushes of frames the sender took from the queue and
	// did not write: the connection was given up under them.
	lost atomic.Int64

	mu       sync.Mutex
	cause    string
	sendFail bool
}

func newConn(nc net.Conn) *conn {
	return &conn{nc: nc, q: newOutQueue(), stop: make(chan struct{}), sendDone: make(chan struct{})}
}

// fail records why the connection is being given up, the first time, and
// closes it so that the reader returns.
func (cn *conn) fail(reason string, send bool) {
	cn.mu.Lock()
	if cn.cause == "" {
		cn.cause = reason
	}
	cn.sendFail = cn.sendFail || send
	cn.mu.Unlock()
	_ = cn.nc.Close()
}

func (cn *conn) failure() (string, bool) {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	return cn.cause, cn.sendFail
}

// interrupt makes a read that is blocked return, without closing the
// connection: the client sends its Detach first.
func (cn *conn) interrupt() {
	_ = cn.nc.SetReadDeadline(time.Unix(1, 0))
}

// finish stops the sender, sends a Detach when asked and the sender is
// healthy, and closes the connection. It returns how many pushes of input
// were queued and never written.
func (cn *conn) finish(detach bool) (dropped int) {
	dropped = cn.q.close()
	cn.stopOnce.Do(func() { close(cn.stop) })
	// A sender stuck in a write to a holder that does not read must let go.
	_ = cn.nc.SetWriteDeadline(time.Now().Add(detachTimeout))
	<-cn.sendDone
	if _, sendFailed := cn.failure(); detach && !sendFailed {
		_ = cn.nc.SetWriteDeadline(time.Now().Add(detachTimeout))
		_ = wire.Write(cn.nc, wire.ToHolder, wire.TypeDetach, nil)
	}
	_ = cn.nc.Close()
	return dropped + int(cn.lost.Load())
}

// sendLoop writes the queued frames to the holder, in order, until the
// connection is finished.
func (c *Client) sendLoop(cn *conn) {
	defer close(cn.sendDone)
	for {
		select {
		case <-cn.q.sig:
		case <-cn.stop:
			return
		}
		for {
			f, ok := cn.q.pop()
			if !ok {
				break
			}
			select {
			case <-cn.stop:
				cn.lost.Add(int64(f.n))
				return
			default:
			}
			_ = cn.nc.SetWriteDeadline(time.Now().Add(c.cfg.WriteTimeout))
			if err := wire.Write(cn.nc, wire.ToHolder, f.typ, f.payload); err != nil {
				cn.lost.Add(int64(f.n))
				if c.ctx.Err() == nil {
					cn.fail("could not send to the holder: "+netReason(err), true)
				}
				return
			}
		}
	}
}

type outcomeKind int

const (
	// outLost: the connection ended, or the holder refused it, and a new
	// ticket may give another.
	outLost outcomeKind = iota
	// outProtocol: the holder broke the protocol; the next attempt waits
	// longer each time.
	outProtocol
	// outResync: the client gave the connection up to attach again.
	outResync
	// outStop: nothing the client does will help (another protocol version).
	outStop
	// outEnded: the session ended.
	outEnded
	// outClosed: the client was closed.
	outClosed
)

type outcome struct {
	kind   outcomeKind
	reason string
	// snapshot is true when a snapshot was applied on the connection.
	snapshot bool
	// checked is true when a Check of the holder past the snapshot's
	// position matched the screen on the connection: the replica followed
	// the holder's output at least once.
	checked bool
}

// session is the state of one connection, used by the client's goroutine
// alone.
type session struct {
	c      *Client
	cn     *conn
	resync bool

	welcomed  bool
	havePos   bool
	pos       uint64
	snapPos   uint64 // the position of the last snapshot applied
	snaps     int
	checked   bool
	modesSeen bool
	writer    string
	hint      string
}

// attach serves one connection: the Hello, the Welcome and then the frames
// of the holder, in order, until the connection ends.
func (c *Client) attach(nc net.Conn, ticket string, resync bool) (out outcome) {
	cn := newConn(nc)
	s := &session{c: c, cn: cn, resync: resync}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = nc.Close()
		return outcome{kind: outClosed}
	}
	c.cur = cn
	c.resetModesLocked()
	hello := c.helloLocked(ticket)
	c.mu.Unlock()
	started := false
	defer func() {
		c.mu.Lock()
		c.cur = nil
		c.mu.Unlock()
		if !started {
			close(cn.sendDone)
		}
		if n := cn.finish(out.kind == outClosed || out.kind == outResync); n > 0 {
			// Input that was queued and never written is not sent later, to
			// a different screen; it is counted.
			c.mu.Lock()
			c.cnt.inputDropped += uint64(n)
			c.mu.Unlock()
		}
		out.snapshot = s.snaps > 0
		out.checked = s.checked
	}()

	payload, err := wire.EncodeHello(hello)
	if err != nil {
		return outcome{kind: outStop, reason: "cannot build the hello: " + shorten(err.Error(), 100)}
	}
	_ = nc.SetWriteDeadline(time.Now().Add(c.cfg.HandshakeTimeout))
	if err := wire.Write(nc, wire.ToHolder, wire.TypeHello, payload); err != nil {
		if c.ctx.Err() != nil {
			return outcome{kind: outClosed}
		}
		return outcome{kind: outLost, reason: "could not send the hello: " + netReason(err)}
	}
	_ = nc.SetReadDeadline(time.Now().Add(c.cfg.HandshakeTimeout))
	started = true
	go c.sendLoop(cn)

	rd := wire.NewReader(nc, wire.ToClient)
	for {
		if c.ctx.Err() != nil {
			return outcome{kind: outClosed}
		}
		f, err := rd.Next()
		if err != nil {
			return s.readError(err)
		}
		if o := s.handle(f); o != nil {
			return *o
		}
	}
}

// resetModesLocked clears the mode set when a connection starts: the new
// connection's holder reports its own, in a Modes frame or in the snapshot.
func (c *Client) resetModesLocked() {
	c.modes = ModeSet{}
	c.st.Modes = sessionpane.Modes{}
}

// readError says what ended the connection from the error of the reader.
func (s *session) readError(err error) outcome {
	c, cn := s.c, s.cn
	if c.ctx.Err() != nil {
		return outcome{kind: outClosed}
	}
	if cause, _ := cn.failure(); cause != "" {
		return outcome{kind: outLost, reason: cause}
	}
	switch {
	case errors.Is(err, wire.ErrUnknownType):
		return outcome{kind: outProtocol, reason: "the holder sent a frame of an unknown type"}
	case errors.Is(err, wire.ErrTooLarge):
		return outcome{kind: outProtocol, reason: "the holder sent a frame over its cap"}
	case errors.Is(err, wire.ErrTooSmall):
		return outcome{kind: outProtocol, reason: "the holder sent a frame that is too short"}
	case errors.Is(err, os.ErrDeadlineExceeded):
		return outcome{kind: outLost, reason: "the holder did not answer the hello in time"}
	case errors.Is(err, io.EOF):
		if s.hint != "" {
			return outcome{kind: outLost, reason: s.hint}
		}
		return outcome{kind: outLost, reason: "the holder closed the connection"}
	case errors.Is(err, io.ErrUnexpectedEOF):
		return outcome{kind: outLost, reason: "the connection broke inside a frame"}
	}
	return outcome{kind: outLost, reason: "read error: " + netReason(err)}
}

func protocolError(reason string) *outcome {
	return &outcome{kind: outProtocol, reason: reason}
}

// handle applies one frame of the holder. A non-nil outcome ends the
// connection.
func (s *session) handle(f wire.Frame) *outcome {
	if !s.welcomed {
		return s.handshake(f)
	}
	switch f.Type {
	case wire.TypeSnapshot:
		return s.snapshot(f.Payload)
	case wire.TypeOutput:
		return s.output(f.Payload)
	case wire.TypeModes:
		return s.modes(f.Payload)
	case wire.TypeSize:
		return s.size(f.Payload)
	case wire.TypeState:
		return s.state(f.Payload)
	case wire.TypeRole:
		return s.roleFrame(f.Payload)
	case wire.TypeCheck:
		return s.check(f.Payload)
	case wire.TypeNotice:
		return s.notice(f.Payload)
	case wire.TypeExit:
		return s.exit(f.Payload)
	}
	// A second Welcome, and any type the reader let through for another
	// direction.
	return protocolError("the holder sent a " + wire.TypeName(f.Type) + " frame out of order")
}

// handshake handles the frames before the Welcome: the Welcome itself, or a
// refusal.
func (s *session) handshake(f wire.Frame) *outcome {
	c := s.c
	switch f.Type {
	case wire.TypeNotice:
		n, err := wire.DecodeNotice(f.Payload)
		if err != nil {
			return protocolError("the holder sent a bad notice")
		}
		switch n.Reason {
		case wire.ReasonRefusedTicket:
			return &outcome{kind: outLost, reason: refusal("the holder refused the ticket", n.Text)}
		case wire.ReasonRefusedBusy:
			return &outcome{kind: outLost, reason: refusal("the holder is busy: it has a client of this role", n.Text)}
		case wire.ReasonRefusedVersion:
			return &outcome{kind: outStop, reason: refusal("the holder speaks another protocol version", n.Text)}
		}
		return protocolError("the holder sent a notice before the welcome")
	case wire.TypeWelcome:
	default:
		return protocolError("the holder sent a " + wire.TypeName(f.Type) + " frame before the welcome")
	}
	w, err := wire.DecodeWelcome(f.Payload)
	if err != nil {
		if v, ok := wire.WelcomeVersion(f.Payload); ok && v != wire.ProtocolVersion {
			return &outcome{kind: outStop, reason: versionReason(v)}
		}
		return protocolError("the holder sent a bad welcome")
	}
	if w.Version != wire.ProtocolVersion {
		return &outcome{kind: outStop, reason: versionReason(w.Version)}
	}
	s.welcomed = true
	_ = s.cn.nc.SetReadDeadline(time.Time{})
	if c.ctx.Err() != nil {
		s.cn.interrupt()
	}
	s.cn.q.setLimit(w.Caps.ClientPayload)
	c.mutate(func() {
		c.st.Lead, c.st.Epoch = w.Lead, w.Epoch
		c.st.Runtime, c.st.Version = w.Runtime, w.RuntimeVersion
		c.st.Role = terminalconsole.LiveRole(w.Role)
		c.role, c.caps = w.Role, w.Caps
		// The state stays connecting (or resyncing) until the first
		// snapshot of the connection is applied: until then the pane shows
		// another screen and the modes are not the holder's, so input
		// would go to a screen the user cannot see.
		if want := wire.Role(c.cfg.Mode); want != w.Role {
			c.addNoticeLocked(terminalconsole.LiveNotice{Code: "role-granted", Text: "Asked to attach as " + string(want) + "; the holder granted " + string(w.Role) + "."})
		}
	})
	return nil
}

func refusal(what, text string) string {
	if t := shorten(text, 100); t != "" {
		return what + ": " + t
	}
	return what
}

func versionReason(v int) string {
	return "the holder speaks protocol version " + strconv.Itoa(v) + ", this console " + strconv.Itoa(wire.ProtocolVersion)
}

func (s *session) snapshot(payload []byte) *outcome {
	c := s.c
	pos, snap, err := wire.DecodeSnapshot(payload)
	if err != nil {
		return protocolError("the holder sent a bad snapshot frame")
	}
	if err := c.pane.Restore(snap); err != nil {
		if errors.Is(err, sessionpane.ErrEnded) {
			return &outcome{kind: outStop, reason: "the pane is closed"}
		}
		return protocolError("the holder sent a snapshot the pane refused")
	}
	s.pos, s.havePos = pos, true
	s.snapPos = pos
	s.snaps++
	cols, rows := c.pane.Size()
	var fallback ModeSet
	if !s.modesSeen {
		set := c.pane.ModeSet()
		fallback = ModeSet{DEC: set.DEC, ANSI: set.ANSI}
	}
	var resize *wire.Size
	c.mutate(func() {
		c.cnt.snapshots++
		if s.snaps > 1 {
			// The holder replaced the screen on its own (it dropped output
			// that this client could not take).
			c.st.Resyncs++
		}
		c.st.Cols, c.st.Rows = cols, rows
		if !s.modesSeen {
			c.modes, c.st.Modes = fallback, fallback.Modes()
		}
		became := c.st.Conn == terminalconsole.LiveResyncing || c.st.Conn == terminalconsole.LiveConnecting
		if became {
			c.st.Conn, c.st.Reason = terminalconsole.LiveAttached, ""
		}
		// A size asked for while connecting or resyncing (the cockpit takes
		// both as up, and asks once) was not in the Hello, or the holder
		// kept another one: as the writer, ask for it whenever a snapshot
		// moves the state to attached, the holder's own resync on this
		// connection included.
		if became && c.inputConnLocked() == s.cn && (c.wantCols != cols || c.wantRows != rows) {
			resize = &wire.Size{Columns: c.wantCols, Rows: c.wantRows}
		}
	})
	if resize != nil {
		s.cn.q.setResize(*resize)
	}
	return nil
}

func (s *session) output(payload []byte) *outcome {
	c := s.c
	pos, data, err := wire.DecodeOutput(payload)
	if err != nil {
		return protocolError("the holder sent a bad output frame")
	}
	if !s.havePos {
		return protocolError("the holder sent output before a snapshot")
	}
	if pos != s.pos {
		c.mutate(func() {
			c.cnt.positionGaps++
			c.st.CheckMismatches++
			c.st.Resyncs++
			c.st.Conn, c.st.Reason = terminalconsole.LiveResyncing, "output position mismatch"
			c.addNoticeLocked(terminalconsole.LiveNotice{Code: "position-mismatch", Text: "The output did not continue where this client was: fetching a new snapshot."})
		})
		return &outcome{kind: outResync}
	}
	c.pane.Write(data)
	s.pos += uint64(len(data))
	c.mutate(func() {})
	return nil
}

func (s *session) modes(payload []byte) *outcome {
	c := s.c
	m, err := wire.DecodeModes(payload)
	if err != nil {
		return protocolError("the holder sent a bad modes frame")
	}
	set := ModeSet{DEC: m.DEC, ANSI: m.ANSI}
	s.modesSeen = true
	c.mutate(func() {
		c.modes, c.st.Modes = set, set.Modes()
	})
	return nil
}

func (s *session) size(payload []byte) *outcome {
	c := s.c
	sz, err := wire.DecodeSize(payload)
	if err != nil {
		return protocolError("the holder sent a bad size frame")
	}
	c.pane.Resize(sz.Columns, sz.Rows)
	cols, rows := c.pane.Size()
	c.mutate(func() { c.st.Cols, c.st.Rows = cols, rows })
	return nil
}

func fact(f wire.Fact) terminalconsole.LiveFact {
	switch f {
	case wire.FactYes:
		return terminalconsole.FactYes
	case wire.FactNo:
		return terminalconsole.FactNo
	}
	return terminalconsole.FactUnknown
}

func endFacts(f wire.Facts, status *int) terminalconsole.LiveEnd {
	e := terminalconsole.LiveEnd{Stream: fact(f.StreamEnded), TUIExited: fact(f.TUIExited), TreeGone: fact(f.TreeGone)}
	if status != nil {
		e.ExitStatus, e.ExitStatusKnown = *status, true
	}
	return e
}

func (s *session) state(payload []byte) *outcome {
	c := s.c
	st, err := wire.DecodeState(payload)
	if err != nil {
		return protocolError("the holder sent a bad state frame")
	}
	// A State that says the terminal stream ended ends the pane, as the Exit
	// that follows it would: if the connection drops between the two, the
	// pane must not stay live with nothing to feed it.
	if st.Facts.StreamEnded == wire.FactYes {
		status, known := 0, st.ExitStatus != nil
		if known {
			status = *st.ExitStatus
		}
		c.pane.End(status, known)
	}
	c.mutate(func() {
		c.st.End = endFacts(st.Facts, st.ExitStatus)
		c.st.Idle, c.st.Title = st.Idle, st.Title
	})
	return nil
}

func (s *session) roleFrame(payload []byte) *outcome {
	c := s.c
	ri, err := wire.DecodeRole(payload)
	if err != nil {
		return protocolError("the holder sent a bad role frame")
	}
	// The Role frame only updates the status and the name a later Notice
	// uses: the holder says every writer change once in a Notice of its
	// own, and the cockpit must not show it twice.
	s.writer = ri.WriterName
	c.mutate(func() {
		c.role = ri.Role
		c.st.Role = terminalconsole.LiveRole(ri.Role)
	})
	return nil
}

func (s *session) check(payload []byte) *outcome {
	c := s.c
	ck, err := wire.DecodeCheck(payload)
	if err != nil {
		return protocolError("the holder sent a bad check frame")
	}
	if !s.havePos || ck.Position != s.pos {
		c.mutate(func() { c.cnt.checksSkipped++ })
		return nil
	}
	if wire.DigestHex(c.pane.Digest()) == ck.Digest {
		// The holder sends a Check at once after each snapshot, and it
		// matches the screen just restored: it proves nothing about
		// following the holder's output. Only a Check past the snapshot's
		// position does, because output was applied and then verified.
		if ck.Position > s.snapPos {
			s.checked = true
		}
		return nil
	}
	c.mutate(func() {
		c.st.CheckMismatches++
		c.st.Resyncs++
		c.st.Conn, c.st.Reason = terminalconsole.LiveResyncing, "check mismatch"
		c.addNoticeLocked(terminalconsole.LiveNotice{Code: "check-mismatch", Text: "The screen did not match the holder's: fetching a new snapshot."})
	})
	return &outcome{kind: outResync}
}

// reasonInputBroken is the reason token of the Notice the holder sends when
// the agent's input is broken (the write to it failed for good). The wire
// package has no constant for it yet.
const reasonInputBroken wire.Reason = "input-broken"

// inputBrokenText is what the cockpit says of it.
const inputBrokenText = "the agent's input is broken: nothing more can be typed"

func (s *session) notice(payload []byte) *outcome {
	c := s.c
	n, err := wire.DecodeNotice(payload)
	if err != nil {
		return protocolError("the holder sent a bad notice")
	}
	text := shorten(n.Text, wire.MaxNoticeText)
	out := terminalconsole.LiveNotice{Code: string(n.Reason), Text: text}
	refused, resyncing, once := false, false, false
	switch n.Reason {
	case wire.ReasonKeyHeldSuspend, wire.ReasonKeyHeldSignal:
		out.Code = terminalconsole.NoticeHeldKey
	case wire.ReasonViewerInputRefused:
		out.Code, refused = terminalconsole.NoticeViewerRefused, true
	case wire.ReasonWriterChanged:
		// The cockpit's own text names the writer.
		out.Code, out.Text, out.Client = terminalconsole.NoticeWriterChanged, "", s.writer
	case reasonInputBroken:
		// The agent's input is broken for good: the cockpit says so once,
		// in its own words, whatever text the holder gave.
		out.Text = inputBrokenText
		once = true
	case wire.ReasonResync:
		// The holder dropped output this client could not take and sends a
		// new snapshot on this connection: the screen is behind until it
		// is applied, so input is not sent meanwhile.
		resyncing = true
	case wire.ReasonTooSlow:
		out.Code = terminalconsole.NoticeTooSlow
		s.hint = "too slow"
	case wire.ReasonStalled:
		s.hint = "too slow"
	}
	c.mutate(func() {
		if refused {
			c.st.RefusedInput++
		}
		if once {
			if c.inputBrokenShown {
				return
			}
			c.inputBrokenShown = true
		}
		if resyncing && c.st.Conn == terminalconsole.LiveAttached {
			c.st.Conn, c.st.Reason = terminalconsole.LiveResyncing, "the holder is sending a new snapshot"
		}
		c.addNoticeLocked(out)
	})
	return nil
}

func (s *session) exit(payload []byte) *outcome {
	c := s.c
	e, err := wire.DecodeExit(payload)
	if err != nil {
		return protocolError("the holder sent a bad exit frame")
	}
	status, known := 0, e.ExitStatus != nil
	if known {
		status = *e.ExitStatus
	}
	c.pane.End(status, known)
	list, cut := sortedCounters(e.Counters)
	c.mutate(func() {
		c.st.End = endFacts(e.Facts, e.ExitStatus)
		c.holderCounters, c.cnt.holderCut = list, cut
		c.lastScreen = e.LastScreen
		c.st.Conn, c.st.Reason = terminalconsole.LiveDisconnected, "session ended"
		c.lastReason = "session ended"
	})
	return &outcome{kind: outEnded, reason: "session ended"}
}
