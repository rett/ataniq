package leadattach

import (
	"context"
	"net"
	"time"
)

// Mode is the attach role the client wants.
type Mode string

// The modes. The zero value is a viewer: a client that was not told it may
// write does not send input.
const (
	ModeViewer Mode = "viewer"
	ModeWriter Mode = "writer"
)

// Clock is the client's source of timers: the wait between asks for a ticket.
// Deadlines on the socket use the real time.
type Clock interface {
	NewTimer(d time.Duration) Timer
}

// Timer is a one-shot timer.
type Timer interface {
	// C is the channel the time arrives on, once.
	C() <-chan time.Time
	// Stop stops the timer; it reports whether it stopped it before it fired.
	Stop() bool
}

type realClock struct{}

func (realClock) NewTimer(d time.Duration) Timer { return realTimer{time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }
func (r realTimer) Stop() bool          { return r.t.Stop() }

// DialFunc connects to the holder's attach socket at path. It must give up
// when ctx is done.
type DialFunc func(ctx context.Context, path string) (net.Conn, error)

func dialUnix(ctx context.Context, path string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", path)
}

// Config is what New needs. Only Socket and Tickets are required; the zero
// value of every other field is its default.
type Config struct {
	// Socket is the path of the holder's attach socket.
	Socket string
	// Mode is the attach role the client wants. The ticket binds the role,
	// not this field: the client sends input only when it wants to write and
	// the holder granted it.
	Mode Mode
	// Name is the client name the holder shows to the other pane when the
	// writer changes. It is cleaned and cut to 64 bytes; empty means
	// "console".
	Name string
	// Tickets returns a fresh single-use ticket, or an error meaning that
	// there is none yet (ErrNoTicket, or any other error, which the status
	// shows as the reason). It is called on the client's own goroutine, once
	// to start and then every 500 ms while it waits. FileTickets is the one
	// the console uses.
	Tickets func() (string, error)
	// Dial connects to Socket. Nil dials the unix socket.
	Dial DialFunc
	// Clock times the wait for a ticket. Nil is the real clock.
	Clock Clock
	// ShiftEnter is what Shift+Enter sends; the default is ESC CR.
	ShiftEnter ShiftEnter
	// HandshakeTimeout is how long the holder has to answer a Hello with a
	// Welcome or a refusal. Zero means 10 seconds.
	HandshakeTimeout time.Duration
	// WriteTimeout is how long one frame to the holder may take to be
	// written before the holder is taken for stuck and the connection is
	// closed. Zero means 30 seconds, the holder's own limit for a client that
	// does not read.
	WriteTimeout time.Duration
}

// The defaults and bounds of the client.
const (
	// TicketPoll is the wait between two asks for a ticket.
	TicketPoll = 500 * time.Millisecond

	defaultHandshake = 10 * time.Second
	defaultWrite     = 30 * time.Second
	dialTimeout      = 5 * time.Second
	detachTimeout    = 200 * time.Millisecond

	// maxBackoffPolls bounds the wait after consecutive protocol errors: the
	// poll interval times 2^n, at most this many polls.
	maxBackoffPolls = 60

	// maxQueued is how many frames wait to be sent; more are dropped and
	// counted.
	maxQueued = 256
	// maxNotices is the backlog of notices the cockpit has not taken.
	maxNotices = 64
	// maxHolderCounters is how many of the holder's counters are kept.
	maxHolderCounters = 128
)
