package hookport

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ataniq/internal/agentsession"
	"ataniq/internal/agentsession/hookwire"
	"ataniq/internal/leadholder/wire"
	"ataniq/internal/pack"
)

const (
	firstByte   = 250 * time.Millisecond
	exchange    = time.Second
	tokenPeriod = time.Second / 50
	burst       = 100
	ringSize    = 256
	maxText     = 1024
	Source      = "reported from inside the sandbox, not checked"
)

type Timer interface{ Stop() bool }
type Clock interface {
	Now() time.Time
	AfterFunc(time.Duration, func()) Timer
}
type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

func (wallClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

// Config is fixed for one launch intent. Group is the holder's independently
// verified unit path. Table and Decode belong to the selected build. Decode
// is a bounded, in-memory pack operation on complete events only. Its frame
// aliases the port's read buffer and is valid only for the call.
type Config struct {
	Group       string
	Table       hookwire.Table
	SessionForm agentsession.IDForm
	Decode      func(pack.HookRow, hookwire.Request, []byte) (agentsession.HookReport, string, error)
	Peers       PeerReader // nil selects the kernel
	Clock       Clock      // nil selects the monotonic wall clock
}

type Counters struct {
	Accepted, Peer, Deadline, Size, Malformed, Row uint64
	Cut, Dropped, Nested, Foreign                  uint64
	Paused                                         time.Duration
}

// Write describes only this connection's kernel write. A complete write
// says nothing about the helper reading the frame. S1 only writes none.
type Write struct {
	Connection uint64
	State      string // not-started, writing, handed-out, write-uncertain
	At         time.Time
	Bytes      int // count returned by the write call, including the length prefix
	Late       bool
}

type Event struct {
	At                       time.Time
	Source, Row, Text, Class string
	Report                   agentsession.HookReport
	Size                     int
	Cut                      bool
}

type Snapshot struct {
	Counters Counters
	Write    Write
	Bound    string
	Events   []Event // oldest first
}

type Port struct {
	cfg         Config
	started     atomic.Bool
	buf         [4 + hookwire.MaxRequestBytes]byte
	none        []byte
	binding     agentsession.HookView // only the serving goroutine binds
	mu          sync.Mutex
	counts      Counters
	write       Write
	bound       string
	ring        [ringSize]Event
	head, count int
}

func New(cfg Config) (*Port, error) {
	if !withinGroup(cfg.Group, cfg.Group) || cfg.Table == nil || cfg.Decode == nil {
		return nil, errors.New("hookport: missing unit reference or pack decoder")
	}
	binding, err := agentsession.NewHookView(0, cfg.SessionForm)
	if err != nil {
		return nil, err
	}
	if cfg.Clock == nil {
		cfg.Clock = wallClock{}
	}
	if cfg.Peers == nil {
		cfg.Peers = KernelPeers{}
	}
	none, err := hookwire.EncodeReply(hookwire.Reply{Version: hookwire.Version, Kind: hookwire.None})
	if err != nil {
		return nil, err
	}
	return &Port{cfg: cfg, binding: binding, none: none}, nil
}

func (p *Port) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := Snapshot{Counters: p.counts, Write: p.write, Bound: p.bound, Events: make([]Event, p.count)}
	for i := range s.Events {
		s.Events[i] = p.ring[(p.head+i)%ringSize]
	}
	return s
}

// Filtered is returned only after the holder's process-wide filter is installed.
type Filtered struct{ digest [32]byte }

// Serve takes ownership of the listener on success of its entry checks. It
// runs once, in its caller's dedicated goroutine, and closes on cancellation.
func (p *Port) Serve(ctx context.Context, l net.Listener, filter Filtered) error {
	if filter == (Filtered{}) {
		return errors.New("hookport: holder filter required")
	}
	if l == nil || !p.started.CompareAndSwap(false, true) {
		return errors.New("hookport: listener missing or already served")
	}
	closeListener := sync.OnceFunc(func() { _ = l.Close() })
	defer closeListener()
	stop := context.AfterFunc(ctx, closeListener)
	defer stop()
	clock := p.cfg.Clock
	// fullAt is when all spent tokens will have been replenished. One
	// token is available when at most burst-1 are still outstanding.
	fullAt := clock.Now()
	for ctx.Err() == nil {
		now := clock.Now()
		nextToken := fullAt.Add(-(burst - 1) * tokenPeriod)
		if now.Before(nextToken) {
			resume := make(chan struct{})
			timer := clock.AfterFunc(nextToken.Sub(now), func() { close(resume) })
			select {
			case <-resume:
			case <-ctx.Done():
			}
			timer.Stop()
			p.mu.Lock()
			p.counts.Paused += clock.Now().Sub(now)
			p.mu.Unlock()
			continue
		}
		if fullAt.Before(now) {
			fullAt = now
		}
		fullAt = fullAt.Add(tokenPeriod)
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		accepted := clock.Now()
		// The token reserved for a blocked Accept stays spent until a
		// connection actually arrives, even after a long idle interval.
		if refill := accepted.Add(tokenPeriod); fullAt.Before(refill) {
			fullAt = refill
		}
		p.handle(ctx, c, accepted)
	}
	return ctx.Err()
}

func (p *Port) handle(ctx context.Context, c net.Conn, accepted time.Time) {
	end := accepted.Add(exchange)
	closeConn := sync.OnceFunc(func() { _ = c.Close() })
	timer := p.cfg.Clock.AfterFunc(end.Sub(p.cfg.Clock.Now()), closeConn)
	stop := context.AfterFunc(ctx, closeConn)
	defer stop()
	defer timer.Stop()
	defer closeConn()
	p.mu.Lock()
	p.counts.Accepted++
	p.write = Write{Connection: p.counts.Accepted, State: "not-started", At: accepted}
	p.mu.Unlock()
	reason := p.exchange(c, accepted, end)
	p.mu.Lock()
	defer p.mu.Unlock()
	switch reason {
	case "peer":
		p.counts.Peer++
	case "deadline":
		p.counts.Deadline++
	case "size":
		p.counts.Size++
	case "malformed":
		p.counts.Malformed++
	case "row":
		p.counts.Row++
	}
}

func (p *Port) exchange(c net.Conn, accepted, end time.Time) string {
	if err := admit(c, p.cfg.Peers, p.cfg.Group); err != nil {
		return "peer"
	}
	first := accepted.Add(firstByte)
	if !p.cfg.Clock.Now().Before(first) {
		return "deadline"
	}
	if err := c.SetReadDeadline(first); err != nil {
		return p.readFailure(err, first)
	}
	if _, err := io.ReadFull(c, p.buf[:1]); err != nil {
		return p.readFailure(err, first)
	}
	if !p.cfg.Clock.Now().Before(first) {
		return "deadline"
	}
	if err := c.SetDeadline(end); err != nil {
		return p.readFailure(err, end)
	}
	if _, err := io.ReadFull(c, p.buf[1:4]); err != nil {
		return p.readFailure(err, end)
	}
	size := binary.BigEndian.Uint32(p.buf[:4])
	if size > hookwire.MaxRequestBytes {
		return "size"
	}
	frame := p.buf[:4+size]
	if _, err := io.ReadFull(c, frame[4:]); err != nil {
		return p.readFailure(err, end)
	}
	if !p.cfg.Clock.Now().Before(end) {
		return "deadline"
	}

	req, err := p.cfg.Table.DecodeRequest(frame)
	if err != nil {
		if _, syntax := hookwire.DecodeRequest(frame); syntax == nil {
			return "row"
		}
		return "malformed"
	}
	ev := Event{At: accepted, Source: Source, Row: req.Row, Size: req.Size, Cut: req.Cut}
	binding := p.binding
	if req.Kind == hookwire.Event && !req.Cut {
		row, _ := p.cfg.Table.Lookup(req.Row)
		report, summary, err := p.cfg.Decode(row, *req, frame)
		if err != nil {
			return "row"
		}
		// Both the binding and the ring outlive this frame. A pack may
		// return strings backed by the port's reusable read buffer.
		report.Kind = agentsession.HookKind(strings.Clone(string(report.Kind)))
		report.Session = strings.Clone(report.Session)
		binding, err = agentsession.Bind(binding, report)
		if err != nil {
			return "row"
		}
		ev.Report = report
		ev.Text = wire.Sanitize(summary, maxText, false)
		ev.Class = "bound"
		if agentsession.Observation(binding).Bound != report.Session {
			ev.Class = "foreign"
			if report.Kind == "session-start" {
				ev.Class = "nested"
			}
		}
		// The event lives in the ring. Keep the epoch binding past eviction.
		binding = agentsession.Drop(binding, 0)
	}
	if !p.cfg.Clock.Now().Before(end) {
		return "deadline"
	}
	p.binding = binding
	p.record(ev)
	if req.Kind == hookwire.Fetch {
		return p.reply(c, end)
	}
	return ""
}

func (p *Port) readFailure(err error, deadline time.Time) string {
	var timeout net.Error
	if !p.cfg.Clock.Now().Before(deadline) || errors.As(err, &timeout) && timeout.Timeout() {
		return "deadline"
	}
	return "malformed"
}

func (p *Port) record(ev Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.bound = agentsession.Observation(p.binding).Bound
	if ev.Cut {
		p.counts.Cut++
	}
	switch ev.Class {
	case "nested":
		p.counts.Nested++
	case "foreign":
		p.counts.Foreign++
	}
	if p.count == ringSize {
		p.head = (p.head + 1) % ringSize
		p.count--
		p.counts.Dropped++
	}
	p.ring[(p.head+p.count)%ringSize] = ev
	p.count++
}

func (p *Port) reply(c net.Conn, end time.Time) string {
	p.mu.Lock()
	now := p.cfg.Clock.Now()
	if !now.Before(end) {
		p.mu.Unlock()
		return "deadline"
	}
	p.write.State = "writing"
	p.write.At = now
	p.mu.Unlock()
	// net.Conn.Write either writes the frame or returns a short count and an
	// error. Even a zero-byte failed call leaves the write uncertain.
	n, err := c.Write(p.none)
	finished := p.cfg.Clock.Now()
	p.mu.Lock()
	p.write.Bytes = n
	p.write.At = finished
	if n == len(p.none) {
		p.write.State = "handed-out"
		p.write.Late = finished.After(end)
	} else {
		p.write.State = "write-uncertain"
	}
	p.mu.Unlock()
	var timeout net.Error
	if !finished.Before(end) || errors.As(err, &timeout) && timeout.Timeout() {
		return "deadline"
	}
	return ""
}
