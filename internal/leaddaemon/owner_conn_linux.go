//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"net"
	"runtime"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

// This file is the daemon's knowledge of one connection, which the client
// protocol's extension seam does not carry: an extension's Handle gets a
// context, the operation and the params, and nothing that says which
// connection the request came from. The owner channel needs that (the decider
// is one connection, and the session of owner.decider belongs to it), so the
// daemon wraps the listener: each accepted connection is an [ownerConn], and
// the first Read on it, which the server makes in the connection's own
// goroutine, records that goroutine's id against the connection. The seam's
// documentation says Handle runs in the connection's goroutine, so a Handle
// finds its connection by the id of the goroutine it runs in. A goroutine id
// is never reused in a process, a connection that cannot be found is refused
// (the lookup fails closed), and a test runs the real server to hold the
// assumption.

// ownerConn is one connection of the owner channel's server.
type ownerConn struct {
	net.Conn
	reg *ownerConns

	// pid is the peer's process id from SO_PEERCRED when the connection was
	// accepted, 0 when it could not be read; acceptedAt is when.
	pid        int
	acceptedAt time.Time
	// fallback is the session id the daemon made for this connection, used
	// in the journal's refusals before (or without) an owner.decider call.
	fallback string

	bindOnce  sync.Once
	closeOnce sync.Once
	gid       uint64 // set by bindOnce

	// The fields below are guarded by reg.mu.
	closed  bool
	asked   bool   // owner.decider was called: client and session are set
	client  string // the peer's declared client name
	session string // the session of owner.decider
	tty     string // the terminal the inspector found, when decider
	decider bool
}

// Read binds the calling goroutine to the connection on the first call, then
// reads.
func (c *ownerConn) Read(p []byte) (int, error) {
	c.bindOnce.Do(func() { c.reg.bind(c) })
	return c.Conn.Read(p)
}

// Close unbinds the connection, gives up the decider's place if it holds it,
// and closes the socket. It may be called more than once, from any goroutine.
func (c *ownerConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		c.reg.release(c)
		err = c.Conn.Close()
	})
	return err
}

// ownerConns knows the connections of one server and who is the decider.
type ownerConns struct {
	mu    sync.Mutex
	byGID map[uint64]*ownerConn
	// seat is the connection that is the decider, nil when there is none.
	seat *ownerConn
}

func newOwnerConns() *ownerConns { return &ownerConns{byGID: map[uint64]*ownerConn{}} }

// wrap makes the connection of an accepted socket.
func (r *ownerConns) wrap(c net.Conn, now time.Time) *ownerConn {
	oc := &ownerConn{Conn: c, reg: r, acceptedAt: now, fallback: randomSession()}
	oc.pid = peerPID(c)
	return oc
}

// bind records the calling goroutine as the connection's.
func (r *ownerConns) bind(c *ownerConn) {
	id := goroutineID()
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.closed || id == 0 {
		return
	}
	c.gid = id
	r.byGID[id] = c
}

// release forgets the connection and frees the decider's place if it was its.
func (r *ownerConns) release(c *ownerConn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c.closed = true
	if c.gid != 0 && r.byGID[c.gid] == c {
		delete(r.byGID, c.gid)
	}
	if r.seat == c {
		r.seat = nil
	}
	c.decider = false
}

// current returns the connection of the calling goroutine, nil when there is
// none (a call made outside the server's connection goroutine, or on a
// connection already closed).
func (r *ownerConns) current() *ownerConn {
	id := goroutineID()
	if id == 0 {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.byGID[id]
	if c == nil || c.closed {
		return nil
	}
	return c
}

// isDecider reports whether the connection holds the decider's place now.
func (r *ownerConns) isDecider(c *ownerConn) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !c.closed && r.seat == c && c.decider
}

// ownerSeat is the answer of [ownerConns.grant].
type ownerSeat int

const (
	seatGranted ownerSeat = iota
	seatTaken             // another connection is the decider
	seatClosed            // this connection ended meanwhile
)

// grant gives the connection the decider's place when it is free or already
// its own, and records what the inspector found.
func (r *ownerConns) grant(c *ownerConn, tty string) ownerSeat {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case c.closed:
		return seatClosed
	case r.seat != nil && r.seat != c:
		return seatTaken
	}
	r.seat = c
	c.decider = true
	c.tty = tty
	return seatGranted
}

// declare keeps the client and the session of the first owner.decider call
// for the connection. A later call with another session changes nothing and
// returns false.
func (r *ownerConns) declare(c *ownerConn, client, session string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.asked {
		return c.session == session
	}
	c.asked, c.client, c.session = true, client, session
	return true
}

// view is what a request of the connection needs, read at once.
type ownerView struct {
	decider    bool
	session    string
	client     string
	tty        string
	pid        int
	declared   bool
	connection *ownerConn
}

func (r *ownerConns) view(c *ownerConn) ownerView {
	r.mu.Lock()
	defer r.mu.Unlock()
	v := ownerView{
		decider: !c.closed && r.seat == c && c.decider, client: c.client, tty: c.tty,
		pid: c.pid, declared: c.asked, session: c.session, connection: c,
	}
	if !c.asked {
		v.session = c.fallback
	}
	return v
}

// ownerListener wraps the server's listener so that every connection is an
// ownerConn.
type ownerListener struct {
	net.Listener
	reg *ownerConns
	now func() time.Time
}

func (l *ownerListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return l.reg.wrap(c, l.now()), nil
}

// peerPID reads the peer's process id with SO_PEERCRED, 0 when it cannot.
func peerPID(c net.Conn) int {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0
	}
	pid := 0
	if err := raw.Control(func(fd uintptr) {
		if cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED); err == nil && cred.Pid > 0 {
			pid = int(cred.Pid)
		}
	}); err != nil {
		return 0
	}
	return pid
}

// randomSession returns 16 lower-case hexadecimal digits.
func randomSession() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("leaddaemon: no randomness: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// goroutineID returns the id of the calling goroutine, 0 when it cannot be
// read. The runtime's stack header is "goroutine N [running]:".
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	rest, ok := bytes.CutPrefix(buf[:n], []byte("goroutine "))
	if !ok {
		return 0
	}
	end := bytes.IndexByte(rest, ' ')
	if end <= 0 {
		return 0
	}
	id, err := strconv.ParseUint(string(rest[:end]), 10, 64)
	if err != nil {
		return 0
	}
	return id
}
