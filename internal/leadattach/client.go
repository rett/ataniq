package leadattach

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"ataniq/internal/leadholder/wire"
	"ataniq/internal/terminalconsole"
	"ataniq/internal/terminalconsole/sessionpane"
)

var _ terminalconsole.LiveSource = (*Client)(nil)

// Client is the console's attach client: a terminalconsole.LiveSource that
// speaks to a holder over its attach socket. Every method is safe for
// concurrent use.
type Client struct {
	cfg   Config
	clock Clock
	sink  *inputSink

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	started bool
	closed  bool
	pane    *sessionpane.Pane
	wake    func()
	colours sessionpane.Colours
	// wantCols and wantRows are the size last asked for, which a Hello
	// carries.
	wantCols, wantRows int

	st terminalconsole.LiveStatus
	// modes is the full mode set the holder reported; st.Modes is derived.
	modes ModeSet
	// role is the role the holder granted, caps its limits.
	role wire.Role
	caps wire.Caps
	// cur is the connection being made or kept; input goes to it while the
	// state is attached.
	cur *conn
	// lastReason is why the last connection ended.
	lastReason     string
	holderCounters []terminalconsole.LiveCounter
	lastScreen     string
	notices        []terminalconsole.LiveNotice
	// inputBrokenShown is set once the notice that the agent's input is
	// broken was made: it is said once for the life of the client.
	inputBrokenShown bool
	cnt              counters
}

// counters are the client's own counters, shown with the holder's.
type counters struct {
	positionGaps    uint64
	checksSkipped   uint64
	snapshots       uint64
	inputDropped    uint64
	noticesDropped  uint64
	pasteRefused    uint64
	keysUnencodable uint64
	holderCut       uint64
}

// New returns a client for cfg. It connects nothing until Start.
func New(cfg Config) *Client {
	if cfg.Mode != ModeWriter {
		cfg.Mode = ModeViewer
	}
	cfg.Name = cleanName(cfg.Name)
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = defaultHandshake
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = defaultWrite
	}
	if cfg.Dial == nil {
		cfg.Dial = dialUnix
	}
	c := &Client{cfg: cfg, clock: cfg.Clock}
	if c.clock == nil {
		c.clock = realClock{}
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.sink = &inputSink{c: c}
	c.st.Conn = terminalconsole.LiveConnecting
	return c
}

// cleanName makes the client name fit a Hello: clean, at most 64 bytes, and
// never empty.
func cleanName(s string) string {
	s = strings.TrimSpace(wire.Sanitize(s, wire.MaxClientName, false))
	if s == "" {
		return "console"
	}
	return s
}

// Start implements terminalconsole.LiveSource. It never blocks: a goroutine
// asks for a ticket and connects, and the status says how that goes.
func (c *Client) Start(s terminalconsole.LiveStart) (sessionpane.InputSink, error) {
	switch {
	case s.Pane == nil:
		return nil, errors.New("leadattach: no pane to feed")
	case c.cfg.Socket == "":
		return nil, errors.New("leadattach: no socket path")
	case c.cfg.Tickets == nil:
		return nil, errors.New("leadattach: no ticket source")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("leadattach: the client is closed")
	}
	if c.started {
		return nil, errors.New("leadattach: the client is already started")
	}
	c.started = true
	c.pane, c.wake, c.colours = s.Pane, s.Wake, s.Colours
	c.wantCols, c.wantRows = clampSize(s.Cols, s.Rows)
	c.wg.Add(1)
	go c.run()
	return c.sink, nil
}

func clampSize(cols, rows int) (int, int) {
	return min(max(cols, sessionpane.MinCols), sessionpane.MaxCols), min(max(rows, sessionpane.MinRows), sessionpane.MaxRows)
}

// Resize implements terminalconsole.LiveSource: it asks the holder for the
// size, as the writer with the connection up. The newest size waiting wins,
// so a burst of window changes is one frame. The holder decides the size and
// says it in a Size frame, which the pane follows.
func (c *Client) Resize(cols, rows int) {
	cols, rows = clampSize(cols, rows)
	c.mu.Lock()
	c.wantCols, c.wantRows = cols, rows
	cn := c.inputConnLocked()
	same := c.st.Cols == cols && c.st.Rows == rows
	c.mu.Unlock()
	if cn == nil || (same && !cn.q.resizePending()) {
		return
	}
	cn.q.setResize(wire.Size{Columns: cols, Rows: rows})
}

// Status implements terminalconsole.LiveSource.
func (c *Client) Status() terminalconsole.LiveStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := c.st
	st.Counters = c.countersLocked()
	return st
}

// countersLocked lists the counters: the client's own first, then the
// holder's from its Exit, in name order.
func (c *Client) countersLocked() []terminalconsole.LiveCounter {
	n := c.cnt
	own := []terminalconsole.LiveCounter{
		{Name: "client-snapshots", Value: n.snapshots},
		{Name: "client-position-gaps", Value: n.positionGaps},
		{Name: "client-checks-skipped", Value: n.checksSkipped},
		{Name: "client-input-dropped", Value: n.inputDropped},
		{Name: "client-keys-unencodable", Value: n.keysUnencodable},
		{Name: "client-pastes-refused", Value: n.pasteRefused},
		{Name: "client-notices-dropped", Value: n.noticesDropped},
	}
	if n.holderCut > 0 {
		own = append(own, terminalconsole.LiveCounter{Name: "client-holder-counters-cut", Value: n.holderCut})
	}
	return append(own, c.holderCounters...)
}

// Notices implements terminalconsole.LiveSource.
func (c *Client) Notices() []terminalconsole.LiveNotice {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.notices
	c.notices = nil
	return out
}

// Close implements terminalconsole.LiveSource: it ends the connection, with
// a Detach when it is up, and waits until every goroutine of the client has
// returned. It may be called more than once, and before Start.
func (c *Client) Close() {
	c.mu.Lock()
	first := !c.closed
	c.closed = true
	cur := c.cur
	c.mu.Unlock()
	c.cancel()
	if cur != nil {
		cur.interrupt()
	}
	c.wg.Wait()
	if first {
		c.mu.Lock()
		if c.started && c.st.Reason != "session ended" {
			c.st.Conn = terminalconsole.LiveDisconnected
			c.st.Reason = "closed"
		}
		c.mu.Unlock()
	}
}

// LastScreen returns the last screen as text that the holder gave in its
// final record, or "" when the session has not ended.
func (c *Client) LastScreen() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastScreen
}

// mutate changes the state under the lock and then wakes the program, after
// the lock is released: the wake-up callback may read the status. Nothing
// changes and nobody is woken after Close.
func (c *Client) mutate(fn func()) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	fn()
	wake := c.wake
	c.mu.Unlock()
	if wake != nil {
		wake()
	}
}

// inputConnLocked returns the connection input may go to: the client wants
// to write, the holder granted it, and the state is attached. Otherwise nil.
func (c *Client) inputConnLocked() *conn {
	if c.cfg.Mode != ModeWriter || c.role != wire.RoleWriter || c.st.Conn != terminalconsole.LiveAttached || c.cur == nil {
		return nil
	}
	return c.cur
}

// addNoticeLocked keeps a notice for the cockpit. The backlog is bounded:
// the oldest goes, and is counted. A notice equal to the last one that the
// cockpit has not taken yet is not kept twice.
func (c *Client) addNoticeLocked(n terminalconsole.LiveNotice) {
	if l := len(c.notices); l > 0 && c.notices[l-1] == n {
		return
	}
	c.notices = append(c.notices, n)
	if over := len(c.notices) - maxNotices; over > 0 {
		c.notices = append([]terminalconsole.LiveNotice(nil), c.notices[over:]...)
		c.cnt.noticesDropped += uint64(over)
	}
}

// setConnecting sets the state of a connection that is being made. A
// connection that replaces one that failed its check stays resyncing.
func (c *Client) setConnecting(resync bool) {
	c.mutate(func() {
		if resync {
			c.st.Conn = terminalconsole.LiveResyncing
		} else {
			c.st.Conn = terminalconsole.LiveConnecting
		}
		c.st.Reason = ""
	})
}

// setResyncWait says in the reason that the next connection of a repeated
// resync is made after d. The state stays resyncing.
func (c *Client) setResyncWait(d time.Duration) {
	c.mutate(func() {
		if c.st.Conn == terminalconsole.LiveResyncing {
			c.st.Reason += "; trying again in " + d.String()
		}
	})
}

// setDisconnected records why a connection ended.
func (c *Client) setDisconnected(reason string) {
	c.mutate(func() {
		c.st.Conn = terminalconsole.LiveDisconnected
		c.st.Reason = reason
		c.lastReason = reason
	})
}

// setWaiting says that the client waits for a ticket. The reason is that, and
// why the source gave none when it said (not ErrNoTicket), or else why the
// last connection ended.
func (c *Client) setWaiting(err error) {
	c.mutate(func() {
		reason := "waiting for a ticket"
		switch {
		case err != nil && !errors.Is(err, ErrNoTicket):
			reason += ": " + shorten(err.Error(), 100)
		case c.lastReason != "":
			reason += " (" + shorten(c.lastReason, 100) + ")"
		}
		c.st.Conn = terminalconsole.LiveWaitingTicket
		c.st.Reason = reason
	})
}

func liveNotice(code, text string) terminalconsole.LiveNotice {
	return terminalconsole.LiveNotice{Code: code, Text: text}
}

// shorten cleans s for a status line and cuts it to n bytes.
func shorten(s string, n int) string {
	return strings.TrimSpace(wire.Sanitize(s, n, false))
}

// sleep waits for d on the client's clock, and reports false when the client
// was closed first.
func (c *Client) sleep(d time.Duration) bool {
	t := c.clock.NewTimer(d)
	select {
	case <-t.C():
		return c.ctx.Err() == nil
	case <-c.ctx.Done():
		t.Stop()
		return false
	}
}

// run is the client's goroutine: it asks for a ticket, connects, and serves
// the connection until it ends, then does it again unless the session ended,
// the holder speaks another version or the client was closed.
func (c *Client) run() {
	defer c.wg.Done()
	var (
		resync bool
		// fails counts consecutive protocol errors and resyncs counts
		// consecutive client-side resyncs; both go back to zero only when a
		// Check past the snapshot's position matched on a connection, not
		// when a snapshot was applied, and not on the Check the holder sends
		// right after each snapshot: a holder whose output the replica does
		// not follow would otherwise be asked for a snapshot in a tight
		// loop.
		fails, resyncs int
		wait           time.Duration
	)
	for {
		if wait > 0 && !c.sleep(wait) {
			return
		}
		wait = 0
		ticket, ok := c.nextTicket(resync)
		if !ok {
			return
		}
		c.setConnecting(resync)
		nc, err := c.dial()
		if err != nil {
			if c.ctx.Err() != nil {
				return
			}
			c.setDisconnected("cannot connect: " + netReason(err))
			resync, wait = false, TicketPoll
			continue
		}
		out := c.attach(nc, ticket, resync)
		resync = false
		if out.checked {
			fails, resyncs = 0, 0
		}
		switch out.kind {
		case outClosed:
			return
		case outEnded, outStop:
			c.setDisconnected(out.reason)
			return
		case outResync:
			resync = true
			resyncs++
			if resyncs > 1 {
				// The first resync is at once; from the second the wait
				// doubles, so a mismatch that repeats is not a hot loop.
				wait = TicketPoll * time.Duration(min(1<<min(resyncs-1, 6), maxBackoffPolls))
				c.setResyncWait(wait)
			}
		case outProtocol:
			fails++
			c.setDisconnected(out.reason)
			wait = TicketPoll * time.Duration(min(1<<min(fails, 6), maxBackoffPolls))
		default:
			if ended, _, _ := c.pane.Ended(); ended {
				// The holder said the stream ended (a State frame) and the
				// connection broke before its Exit: there is nothing to
				// attach to again.
				c.setDisconnected("session ended")
				return
			}
			c.setDisconnected(out.reason)
			wait = TicketPoll
		}
	}
}

// nextTicket asks the source until it has a ticket, every TicketPoll. A
// client that is resyncing keeps that state through the first ask.
func (c *Client) nextTicket(resync bool) (string, bool) {
	first := true
	for {
		if c.ctx.Err() != nil {
			return "", false
		}
		t, err := c.cfg.Tickets()
		if err == nil {
			if wire.CheckTicket(t) == nil {
				return t, true
			}
			err = errors.New("the ticket source gave a malformed ticket")
		}
		if !(resync && first) {
			c.setWaiting(err)
		}
		first = false
		if !c.sleep(TicketPoll) {
			return "", false
		}
	}
}

func (c *Client) dial() (net.Conn, error) {
	ctx, cancel := context.WithTimeout(c.ctx, dialTimeout)
	defer cancel()
	return c.cfg.Dial(ctx, c.cfg.Socket)
}

// netReason is why a network call failed, without the address.
func netReason(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, os.ErrDeadlineExceeded):
		return "timed out"
	}
	var oe *net.OpError
	if errors.As(err, &oe) && oe.Err != nil {
		return shorten(oe.Err.Error(), 100)
	}
	return shorten(err.Error(), 100)
}

// hexColour formats a colour as #rrggbb, or "" for none.
func hexColour(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8)
}

// helloLocked builds the first frame. The caller holds c.mu.
func (c *Client) helloLocked(ticket string) wire.Hello {
	return wire.Hello{
		Version: wire.ProtocolVersion,
		Ticket:  ticket,
		Name:    c.cfg.Name,
		Columns: c.wantCols,
		Rows:    c.wantRows,
		Colors: wire.Colors{
			Foreground: hexColour(c.colours.Foreground),
			Background: hexColour(c.colours.Background),
			Cursor:     hexColour(c.colours.Cursor),
		},
	}
}

// sortedCounters turns the counters of an Exit into a list in name order,
// bounded: what is over maxHolderCounters is cut and counted.
func sortedCounters(m map[string]uint64) (list []terminalconsole.LiveCounter, cut uint64) {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	if len(names) > maxHolderCounters {
		cut = uint64(len(names) - maxHolderCounters)
		names = names[:maxHolderCounters]
	}
	for _, k := range names {
		list = append(list, terminalconsole.LiveCounter{Name: k, Value: m[k]})
	}
	return list, cut
}
