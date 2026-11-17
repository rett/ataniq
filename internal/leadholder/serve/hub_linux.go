//go:build linux

package serve

import (
	"fmt"
	"net"
	"slices"
	"time"

	"ataniq/internal/leadholder/eventlog"
	"ataniq/internal/leadholder/session"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole/sessionpane"
)

// posLen is the position that starts an Output and a Snapshot payload, for the
// queue's accounting.
const posLen = 8

// item is one queued frame. An Output or Snapshot item holds the raw data and
// its position (the sender encodes the payload, off the hub lock); any other
// item holds its encoded payload in data.
type item struct {
	typ  byte
	pos  uint64
	data []byte
	snap bool // a snapshot: not counted against the queue's bound
}

func (it *item) cost() int {
	switch {
	case it.snap:
		return 0
	case it.typ == wire.TypeOutput:
		return posLen + len(it.data)
	}
	return len(it.data)
}

func (it *item) payload() ([]byte, error) {
	switch it.typ {
	case wire.TypeOutput:
		return wire.EncodeOutput(it.pos, it.data)
	case wire.TypeSnapshot:
		return wire.EncodeSnapshot(it.pos, it.data)
	}
	return it.data, nil
}

// client is one attached connection. Fields after mu's mark are guarded by
// Server.mu.
type client struct {
	s    *Server
	conn *net.UnixConn
	name string
	pid  int
	cmd  string
	done chan struct{} // closed when the sender returned

	// Guarded by Server.mu.
	role         wire.Role
	q            []item
	qBytes       int
	wake         chan struct{}
	wantSnap     bool      // waiting for a snapshot point
	snapSince    time.Time // when the wait began
	qSince       time.Time // when the parser was first seen quiescent in this wait; never reset by a later point
	noticed      bool      // the snapshot-waiting notice was sent
	streaming    bool      // gets Output from now on
	snapUnsent   bool      // a snapshot is queued or being written
	lastOverflow time.Time
	nextCheck    time.Time
	viewerLogged bool
	removed      bool
	dead         bool   // removed: the sender finishes and closes
	draining     bool   // the session is over: the sender closes after the queue
	fin          []byte // encoded Notice to write last, best effort
	reason       string // why the client left
}

func (c *client) poke() {
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// stateKey is what a State frame says; it is compared before anything is
// encoded.
type stateKey struct {
	title string
	facts wire.Facts
	exit  int // -1 when unknown
}

// ticketRec is one registered, unused ticket.
type ticketRec struct {
	ticket  string
	role    wire.Role
	expires time.Time
}

func wireFact(t session.Tri) wire.Fact {
	switch t {
	case session.Yes:
		return wire.FactYes
	case session.No:
		return wire.FactNo
	}
	return wire.FactUnknown
}

// wireFacts converts the session's facts, and gives the exit status when the
// TUI's status is known.
func wireFacts(f session.Facts) (wire.Facts, *int) {
	out := wire.Facts{StreamEnded: wireFact(f.StreamEnded), TUIExited: wireFact(f.TUIExited), TreeGone: wireFact(f.TreeGone)}
	if f.Status != nil && f.Status.Status >= 0 && f.Status.Status <= 255 {
		v := f.Status.Status
		return out, &v
	}
	return out, nil
}

func (s *Server) keyLocked() stateKey {
	k := stateKey{facts: s.facts, exit: -1}
	if s.exitStatus != nil {
		k.exit = *s.exitStatus
	}
	return k
}

// frame helpers: they encode and, on an error that cannot happen for what the
// service builds, return false.

func (s *Server) notice(r wire.Reason, text string) (item, bool) {
	p, err := wire.EncodeNotice(wire.Notice{Reason: r, Text: text})
	return item{typ: wire.TypeNotice, data: p}, err == nil
}

// roleItem is the Role frame for client c.
func (s *Server) roleItemLocked(c *client) (item, bool) {
	ri := wire.RoleInfo{Role: c.role}
	if w := s.writer; w != nil {
		ri.WriterName, ri.WriterPID, ri.WriterCommand = w.name, w.pid, w.cmd
	}
	p, err := wire.EncodeRole(ri)
	return item{typ: wire.TypeRole, data: p}, err == nil
}

// pushLocked queues a frame. A frame that is not forced goes only to a client
// that is streaming, and overflows its queue; a forced one goes to any client
// and is not bounded (a notice that waits for a snapshot, the end of the
// session).
func (s *Server) pushLocked(c *client, it item, force bool) bool {
	if c.removed {
		return false
	}
	if !force {
		if !c.streaming {
			return false
		}
		if c.qBytes+it.cost() > s.cfg.QueueBytes {
			s.overflowLocked(c)
			return false
		}
	}
	c.q = append(c.q, it)
	c.qBytes += it.cost()
	c.poke()
	return true
}

// pushOutputLocked queues PTY bytes for a streaming client, in frames of at
// most the Output cap. Bytes that continue the last queued Output frame are
// appended to it, so that many small chunks do not make many queue entries.
func (s *Server) pushOutputLocked(c *client, pos uint64, data []byte) {
	for len(data) > 0 && c.streaming {
		n := min(len(data), wire.MaxOutputData)
		if last := len(c.q) - 1; last >= 0 {
			l := &c.q[last]
			if l.typ == wire.TypeOutput && l.pos+uint64(len(l.data)) == pos && len(l.data)+n <= wire.MaxOutputData {
				if c.qBytes+n > s.cfg.QueueBytes {
					s.overflowLocked(c)
					return
				}
				l.data = append(l.data, data[:n]...)
				c.qBytes += n
				c.poke()
				pos += uint64(n)
				data = data[n:]
				continue
			}
		}
		if !s.pushLocked(c, item{typ: wire.TypeOutput, pos: pos, data: append(make([]byte, 0, n), data[:n]...)}, false) {
			return
		}
		pos += uint64(n)
		data = data[n:]
	}
}

// overflowLocked handles a queue that would grow past its bound (design 2.5).
// The first overflow drops the queued output, tells the client and asks for one
// fresh snapshot; an overflow while a snapshot is unsent, or a second one
// within the window, disconnects the client.
func (s *Server) overflowLocked(c *client) {
	now := s.now()
	if c.snapUnsent || (!c.lastOverflow.IsZero() && now.Sub(c.lastOverflow) < s.cfg.OverflowWindow) {
		s.removeLocked(c, "too-slow", wire.ReasonTooSlow, "the client reads too slowly", "")
		s.count(cntTooSlow)
		return
	}
	c.lastOverflow = now
	c.q, c.qBytes = nil, 0
	c.streaming = false
	s.count(cntResyncs)
	s.emit(eventlog.EventResync, eventlog.Fields{ClientName: c.name, ClientPID: c.pid, Reason: "overflow"})
	if it, ok := s.notice(wire.ReasonResync, "the client fell behind; a new snapshot follows"); ok {
		s.pushLocked(c, it, true)
	}
	s.startWaitLocked(c, now)
}

// startWaitLocked puts a client in the wait for a snapshot point.
func (s *Server) startWaitLocked(c *client, now time.Time) {
	c.wantSnap, c.snapSince, c.qSince, c.noticed = true, now, time.Time{}, false
	s.kickKeeper()
}

// onChunk is the session's subscriber: it runs under the session's output lock
// for each PTY chunk and must return at once. It copies the chunk into the
// queues of the clients that stream and looks at the emulator's new state.
func (s *Server) onChunk(pos uint64, chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.finishing || len(s.clients) == 0 {
		return
	}
	p, now := s.pane, s.now()
	// A resize happened between two chunks: tell the clients before the
	// output that follows it.
	s.syncSizeLocked(p)
	for c := range s.clients {
		if c.streaming {
			s.pushOutputLocked(c, pos, chunk)
		}
	}
	s.pointLocked(p, pos+uint64(len(chunk)), now)
}

// pointLocked is called with the emulator at a chunk end (or between two
// chunks), at position end, under the session's output lock. It sends what
// changed, takes the snapshots that are due and sends the checks that are due.
func (s *Server) pointLocked(p *sessionpane.Pane, end uint64, now time.Time) {
	s.syncSizeLocked(p)
	s.syncModesLocked(p)
	s.syncStateLocked(p)
	var quiescent, known bool
	isQuiescent := func() bool {
		if !known {
			quiescent, known = p.Quiescent(), true
		}
		return quiescent
	}
	var ready []*client
	for c := range s.clients {
		if !c.wantSnap {
			continue
		}
		if isQuiescent() {
			if c.qSince.IsZero() {
				c.qSince = now
			}
			// Mode 2026 is a frame in progress: wait for its end, but only for
			// the hold time, counted from the first quiescent point of the
			// wait. A later point that is not quiescent does not restart it,
			// else output that keeps the mode set and breaks off in the middle
			// of a sequence now and then would never get its snapshot. The
			// snapshot is of the real state either way.
			if !s.lastModes.Has(2026, false) || now.Sub(c.qSince) >= s.cfg.SnapshotHold {
				ready = append(ready, c)
				continue
			}
		}
		if !c.noticed && now.Sub(c.snapSince) >= s.cfg.SnapshotNotice {
			c.noticed = true
			s.count(cntSnapshotWaiting)
			if it, ok := s.notice(wire.ReasonSnapshotWaiting, "waiting for a quiet point in the output to take a snapshot"); ok {
				s.pushLocked(c, it, true)
			}
		}
	}
	var digest string
	getDigest := func() string {
		if digest == "" {
			digest = wire.DigestHex(p.Digest())
		}
		return digest
	}
	if len(ready) > 0 {
		s.snapshotLocked(p, end, now, ready, getDigest)
	}
	for c := range s.clients {
		if c.streaming && !c.wantSnap && !now.Before(c.nextCheck) && isQuiescent() {
			if s.pushCheckLocked(c, end, getDigest(), now) {
				s.count(cntChecks)
			}
		}
	}
}

func (s *Server) pushCheckLocked(c *client, end uint64, digest string, now time.Time) bool {
	c.nextCheck = now.Add(s.cfg.CheckInterval)
	cp, err := wire.EncodeCheck(wire.Check{Position: end, Digest: digest})
	if err != nil {
		return false
	}
	return s.pushLocked(c, item{typ: wire.TypeCheck, data: cp}, false)
}

// snapshotLocked takes one snapshot at the point and queues it, with the
// frames that go with it, for each client in ready.
func (s *Server) snapshotLocked(p *sessionpane.Pane, end uint64, now time.Time, ready []*client, getDigest func() string) {
	buf, err := p.AppendSnapshot(make([]byte, 0, s.snapHint))
	if err == nil && len(buf) > wire.MaxSnapshotData {
		err = fmt.Errorf("snapshot of %d bytes is over the frame cap of %d", len(buf), wire.MaxSnapshotData)
	}
	if err != nil {
		if err == sessionpane.ErrNotQuiescent {
			return // not at a point after all: the next one will do
		}
		s.count(cntSnapshotErrors)
		s.emit(eventlog.EventRefusal, eventlog.Fields{Reason: "snapshot-failed"})
		for _, c := range ready {
			s.removeLocked(c, "snapshot-failed", "", "", "")
		}
		return
	}
	s.snapHint = len(buf)
	digest := getDigest()
	for _, c := range ready {
		c.wantSnap, c.streaming, c.snapUnsent = false, true, true
		s.count(cntSnapshotsTaken)
		s.pushLocked(c, item{typ: wire.TypeSnapshot, pos: end, data: buf, snap: true}, true)
		s.pushLocked(c, item{typ: wire.TypeModes, data: s.modesPayload}, true)
		s.pushLocked(c, item{typ: wire.TypeSize, data: s.sizePayload}, true)
		s.pushLocked(c, item{typ: wire.TypeState, data: s.statePayload}, true)
		if it, ok := s.roleItemLocked(c); ok {
			s.pushLocked(c, it, true)
		}
		if s.pushCheckLocked(c, end, digest, now) {
			s.count(cntChecks)
		}
	}
}

// syncModesLocked sends the full mode set to the streaming clients when it
// changed since the last point.
func (s *Server) syncModesLocked(p *sessionpane.Pane) {
	ms := p.ModeSet()
	if s.modesPayload != nil && slices.Equal(ms.DEC, s.lastModes.DEC) && slices.Equal(ms.ANSI, s.lastModes.ANSI) {
		return
	}
	payload, err := wire.EncodeModes(wire.Modes{DEC: ms.DEC, ANSI: ms.ANSI})
	if err != nil {
		s.count(cntSnapshotErrors)
		return
	}
	first := s.modesPayload == nil
	s.lastModes, s.modesPayload = ms, payload
	if first {
		return // nobody streams before the first point
	}
	for c := range s.clients {
		s.pushLocked(c, item{typ: wire.TypeModes, data: payload}, false)
	}
}

// syncSizeLocked sends the size to the streaming clients when it changed.
func (s *Server) syncSizeLocked(p *sessionpane.Pane) {
	cols, rows := p.Size()
	sz := wire.Size{Columns: cols, Rows: rows}
	if s.sizePayload != nil && sz == s.lastSize {
		return
	}
	payload, err := wire.EncodeSize(sz)
	if err != nil {
		return
	}
	first := s.sizePayload == nil
	s.lastSize, s.sizePayload = sz, payload
	if first {
		return
	}
	for c := range s.clients {
		s.pushLocked(c, item{typ: wire.TypeSize, data: payload}, false)
	}
}

// syncStateLocked sends a State frame to the streaming clients when the title,
// a lifetime fact or the exit status changed.
func (s *Server) syncStateLocked(p *sessionpane.Pane) {
	title := wire.Sanitize(p.Title(), wire.MaxTitle, false)
	k := s.keyLocked()
	k.title = title
	if s.statePayload != nil && k == s.state {
		return
	}
	st := wire.State{Facts: s.facts, ExitStatus: s.exitStatus, Idle: wire.IdleUnknown, Title: title}
	payload, err := wire.EncodeState(st)
	if err != nil {
		return
	}
	first := s.statePayload == nil
	s.state, s.statePayload = k, payload
	if first {
		return
	}
	for c := range s.clients {
		s.pushLocked(c, item{typ: wire.TypeState, data: payload}, false)
	}
}

// removeLocked takes a client out: it frees its slot, drops its queue, tells
// the others when it was the writer, and wakes its sender, which writes the
// final notice (when there is one) and closes the connection. cmdID is the
// control command that caused it, if any. It is called at most once for the
// effect.
func (s *Server) removeLocked(c *client, reason string, finReason wire.Reason, finText, cmdID string) {
	if c.removed {
		return
	}
	c.removed, c.dead = true, true
	if c.reason == "" {
		c.reason = reason
	}
	c.q, c.qBytes = nil, 0
	c.streaming, c.wantSnap = false, false
	if finReason != "" {
		if p, err := wire.EncodeNotice(wire.Notice{Reason: finReason, Text: finText}); err == nil {
			c.fin = p
			time.AfterFunc(s.cfg.FlushWait, func() { c.conn.Close() })
		}
	}
	delete(s.clients, c)
	wasWriter := s.writer == c
	if wasWriter {
		s.writer = nil
	}
	if s.viewer == c {
		s.viewer = nil
	}
	c.poke()
	s.emit(eventlog.EventDetach, eventlog.Fields{ClientName: c.name, ClientPID: c.pid, Reason: c.reason, CommandID: cmdID})
	if wasWriter {
		text := fmt.Sprintf("the writer %s (pid %d) is gone; there is no writer", c.name, c.pid)
		if reason == "released" {
			text = fmt.Sprintf("the writer %s (pid %d) was released; there is no writer", c.name, c.pid)
		}
		s.writerChangedLocked(c, "writer-left", wire.ReasonWriterReleased, text, cmdID)
	}
	s.maybeGoneLocked()
}

// writerChangedLocked records a change of writer: a counter, an event with the
// client's pid and command line, and a Role frame and a Notice to every
// attached client that streams. who is the client the event is about.
func (s *Server) writerChangedLocked(who *client, why string, r wire.Reason, text, cmdID string) {
	s.count(cntWriterChanges)
	s.emit(eventlog.EventWriterChange, eventlog.Fields{
		ClientName: who.name, ClientPID: who.pid, ClientCommand: who.cmd, Reason: why, CommandID: cmdID,
	})
	for c := range s.clients {
		if !c.streaming {
			continue
		}
		if it, ok := s.roleItemLocked(c); ok {
			s.pushLocked(c, it, false)
		}
		if it, ok := s.notice(r, text); ok {
			s.pushLocked(c, it, false)
		}
	}
}

// maybeGoneLocked is called when a client left; it lets finishSession know
// when the last one has.
func (s *Server) maybeGoneLocked() {
	if s.finishing && len(s.clients) == 0 {
		select {
		case s.gone <- struct{}{}:
		default:
		}
	}
}

// releaseWriterLocked is the release-writer command: the writer becomes the
// viewer when there is none, else it is closed. The result says whether there
// was a writer.
func (s *Server) releaseWriterLocked(cmdID string) bool {
	w := s.writer
	if w == nil {
		return false
	}
	text := fmt.Sprintf("the writer %s (pid %d) was released; there is no writer", w.name, w.pid)
	if s.viewer == nil {
		s.writer, s.viewer = nil, w
		w.role = wire.RoleViewer
		s.writerChangedLocked(w, "release-writer", wire.ReasonWriterReleased, text, cmdID)
		return true
	}
	s.removeLocked(w, "released", wire.ReasonWriterReleased, "you were released as the writer", cmdID)
	return true
}

// claimLocked takes the slot of a role; it reports false when it is taken.
func (s *Server) claimLocked(c *client, role wire.Role) bool {
	switch role {
	case wire.RoleWriter:
		if s.writer != nil {
			return false
		}
		s.writer = c
	default:
		if s.viewer != nil {
			return false
		}
		s.viewer = c
	}
	c.role = role
	return true
}

// registerLocked makes a claimed client part of the fan-out: it waits for its
// first snapshot. A new writer is announced to the others.
func (s *Server) registerLocked(c *client) bool {
	if s.closed || s.finishing || c.removed {
		return false
	}
	now := s.now()
	s.clients[c] = struct{}{}
	s.startWaitLocked(c, now)
	if c.role == wire.RoleWriter {
		text := fmt.Sprintf("the writer is now %s (pid %d)", c.name, c.pid)
		s.writerChangedLocked(c, "writer-attached", wire.ReasonWriterChanged, text, "")
	}
	return true
}
