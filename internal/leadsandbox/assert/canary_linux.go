//go:build linux

package assert

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// DefaultSettleTimeout is how long CheckCanary waits for the canary to have
// accepted its markers (see Canary.Settle).
const DefaultSettleTimeout = 5 * time.Second

// The accept loop of a listener survives an error of Accept (out of
// descriptors, say) and waits before it tries again: first acceptBackoffMin,
// doubling to acceptBackoffMax. Each error is counted (AcceptErrors).
const (
	acceptBackoffMin = time.Millisecond
	acceptBackoffMax = 100 * time.Millisecond
)

// Canary is the target of assertion 3, owned by the caller (the holder, or a
// test): a TCP listener on the IPv4 loopback address, one on the IPv6 loopback
// address when the host has it, and an abstract unix socket with a random
// name. Both accept a connection, close it at once without reading or
// sending, and count it. The caller puts TCP, TCP2 (when it is not empty) and
// Abstract in the spec; inside
// the sandbox neither must be reachable, so after the probe has run the count
// is 0 whatever the probe reported. A count above 0 means a connection reached
// the host.
//
// A transient error of Accept does not end the loop of a listener: it is
// counted (AcceptErrors) and the loop tries again after a bounded back-off. A
// count read while errors have happened may be short, since a connection that
// the kernel completed in the backlog may not have been counted, so CheckCanary
// does not call such a count ok.
//
// A connection that the kernel has completed may still sit in the backlog of a
// listener when the count is read, so Settle waits until every listener has
// accepted what was there before it; CheckCanary calls it first.
//
// Loopback and abstract sockets are not namespaces: creating a Canary needs no
// privilege.
type Canary struct {
	// TCP is the loopback address and port, "127.0.0.1:port".
	TCP string
	// TCP2 is the IPv6 loopback address and port, "[::1]:port", or "" when
	// the host has no IPv6 loopback: the canary then listens on IPv4 only,
	// and says nothing about it.
	TCP2 string
	// Abstract is the socket name without the leading @.
	Abstract string

	tcpHits  atomic.Int64
	tcp2Hits atomic.Int64
	absHits  atomic.Int64
	lns      []net.Listener
	wg       sync.WaitGroup
	once     sync.Once

	// targets are the addresses Settle dials, one per listener.
	targets []settleTarget
	// markers are the connections of Settle that are not yet accepted, by the
	// address they come from (network and address); the accept loops do not
	// count them as hits.
	markMu  sync.Mutex
	markers map[string]chan struct{}
	settle  sync.Mutex // one Settle at a time

	acceptErrors atomic.Int64
	ended        atomic.Int32 // loops that have ended
	done         chan struct{}
	backoffMin   time.Duration
	backoffMax   time.Duration
}

// NewCanary opens both listeners, on port 0 and under a random name.
func NewCanary() (*Canary, error) {
	tl, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("assert: canary: %w", err)
	}
	// The IPv6 loopback is optional: a host without it is no failure.
	t2l, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		if !errors.Is(err, syscall.EADDRNOTAVAIL) && !errors.Is(err, syscall.EAFNOSUPPORT) && !errors.Is(err, syscall.EPROTONOSUPPORT) {
			tl.Close()
			return nil, fmt.Errorf("assert: canary: %w", err)
		}
		t2l = nil
	}
	closeAll := func() {
		tl.Close()
		if t2l != nil {
			t2l.Close()
		}
	}
	var ul net.Listener
	var name string
	for try := 0; try < 8; try++ {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			closeAll()
			return nil, fmt.Errorf("assert: canary: random name: %w", err)
		}
		name = "ataniq-canary-" + hex.EncodeToString(b[:])
		ul, err = net.Listen("unix", "@"+name)
		if err == nil || !errors.Is(err, syscall.EADDRINUSE) {
			break
		}
	}
	if err != nil {
		closeAll()
		return nil, fmt.Errorf("assert: canary: %w", err)
	}
	c := &Canary{
		TCP: tl.Addr().String(), Abstract: name, lns: []net.Listener{tl, ul},
		done: make(chan struct{}), backoffMin: acceptBackoffMin, backoffMax: acceptBackoffMax,
	}
	c.targets = []settleTarget{{"tcp", c.TCP}, {"unix", "@" + name}}
	c.serve(tl, &c.tcpHits)
	if t2l != nil {
		c.TCP2 = t2l.Addr().String()
		c.targets = append(c.targets, settleTarget{"tcp", c.TCP2})
		c.lns = append(c.lns, t2l)
		c.serve(t2l, &c.tcp2Hits)
	}
	c.serve(ul, &c.absHits)
	return c, nil
}

func (c *Canary) serve(l net.Listener, hits *atomic.Int64) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer c.ended.Add(1)
		delay := c.backoffMin
		for {
			conn, err := l.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return
				}
				c.acceptErrors.Add(1)
				t := time.NewTimer(delay)
				select {
				case <-c.done:
					t.Stop()
					return
				case <-t.C:
				}
				if delay *= 2; delay > c.backoffMax {
					delay = c.backoffMax
				}
				continue
			}
			delay = c.backoffMin
			if !c.takeMarker(conn.RemoteAddr()) {
				hits.Add(1)
			}
			conn.Close()
		}
	}()
}

// Hits is the number of connections all listeners accepted.
func (c *Canary) Hits() int64 { return c.tcpHits.Load() + c.tcp2Hits.Load() + c.absHits.Load() }

// TCPHits is the number of connections the IPv4 loopback listener accepted.
func (c *Canary) TCPHits() int64 { return c.tcpHits.Load() }

// TCP2Hits is the number of connections the IPv6 loopback listener accepted.
func (c *Canary) TCP2Hits() int64 { return c.tcp2Hits.Load() }

// AbstractHits is the number of connections the abstract socket accepted.
func (c *Canary) AbstractHits() int64 { return c.absHits.Load() }

// AcceptErrors is the number of errors the accept loops met and went on
// from. When it is above 0, Hits may be short.
func (c *Canary) AcceptErrors() int64 { return c.acceptErrors.Load() }

// Stopped says an accept loop has ended: after Close, or because its listener
// was closed some other way. A connection that comes after is not counted.
func (c *Canary) Stopped() bool { return c.ended.Load() > 0 }

// Close stops the listeners and waits for their loops to end, also when one
// is waiting out an error. It is safe to call more than once.
func (c *Canary) Close() error {
	var err error
	c.once.Do(func() {
		close(c.done)
		for _, l := range c.lns {
			if e := l.Close(); e != nil && err == nil {
				err = e
			}
		}
		c.wg.Wait()
	})
	return err
}

type settleTarget struct{ network, addr string }

// takeMarker says the connection comes from the address of a marker of Settle,
// and tells Settle it was accepted.
func (c *Canary) takeMarker(from net.Addr) bool {
	if from == nil {
		return false
	}
	key := from.Network() + " " + from.String()
	c.markMu.Lock()
	defer c.markMu.Unlock()
	ch, ok := c.markers[key]
	if ok {
		delete(c.markers, key)
		close(ch)
	}
	return ok
}

func (c *Canary) addMarker(key string) chan struct{} {
	ch := make(chan struct{})
	c.markMu.Lock()
	if c.markers == nil {
		c.markers = map[string]chan struct{}{}
	}
	c.markers[key] = ch
	c.markMu.Unlock()
	return ch
}

func (c *Canary) dropMarker(key string) {
	c.markMu.Lock()
	delete(c.markers, key)
	c.markMu.Unlock()
}

// Settle makes one marker connection to each listener and waits, within the
// timeout, until each has been accepted. The accept queue of a listener is
// first in, first out, so a marker that is accepted comes after every
// connection that was completed before it: when Settle returns nil, every
// connection that reached a listener before the call has been counted.
// Markers are not counted in Hits. A canary that was stopped, that has no
// listener, whose marker could not be made or was not accepted in time gets an
// error: the count may be short. Settle may be called more than once.
func (c *Canary) Settle(timeout time.Duration) error {
	c.settle.Lock()
	defer c.settle.Unlock()
	switch {
	case timeout <= 0:
		return errors.New("assert: canary settle: the timeout must be positive")
	case len(c.targets) == 0:
		return errors.New("assert: canary settle: no listener to settle")
	case c.Stopped():
		return errors.New("assert: canary settle: the canary was stopped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for _, t := range c.targets {
		if err := c.settleOne(ctx, t); err != nil {
			return fmt.Errorf("assert: canary settle: %s %s: %w", t.network, t.addr, err)
		}
	}
	return nil
}

// settleOne connects to one listener from an address it knows beforehand, and
// waits until the accept loop has taken that connection.
func (c *Canary) settleOne(ctx context.Context, t settleTarget) error {
	var key string
	var ch chan struct{}
	d := net.Dialer{}
	switch t.network {
	case "unix":
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		local := "@ataniq-canary-marker-" + hex.EncodeToString(b[:])
		key = "unix " + local
		d.LocalAddr = &net.UnixAddr{Name: local, Net: "unix"}
		ch = c.addMarker(key)
	default:
		remote, err := netip.ParseAddrPort(t.addr)
		if err != nil {
			return err
		}
		// Bind the socket to an ephemeral port of the same loopback address
		// before it connects, so the marker is known before the accept loop
		// can see it.
		d.ControlContext = func(_ context.Context, _, _ string, rc syscall.RawConn) error {
			var berr error
			cerr := rc.Control(func(fd uintptr) {
				var sa unix.Sockaddr
				if remote.Addr().Is4() {
					sa = &unix.SockaddrInet4{Addr: remote.Addr().As4()}
				} else {
					sa = &unix.SockaddrInet6{Addr: remote.Addr().As16()}
				}
				if berr = unix.Bind(int(fd), sa); berr != nil {
					return
				}
				var got unix.Sockaddr
				if got, berr = unix.Getsockname(int(fd)); berr != nil {
					return
				}
				switch g := got.(type) {
				case *unix.SockaddrInet4:
					key = "tcp " + netip.AddrPortFrom(netip.AddrFrom4(g.Addr), uint16(g.Port)).String()
				case *unix.SockaddrInet6:
					key = "tcp " + netip.AddrPortFrom(netip.AddrFrom16(g.Addr), uint16(g.Port)).String()
				default:
					berr = errors.New("the marker socket has no address")
					return
				}
				ch = c.addMarker(key)
			})
			if berr != nil {
				return berr
			}
			return cerr
		}
	}
	conn, err := d.DialContext(ctx, t.network, t.addr)
	if err != nil {
		if key != "" {
			c.dropMarker(key)
		}
		return err
	}
	defer conn.Close()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		c.dropMarker(key)
		return errors.New("the marker was not accepted in time")
	case <-c.done:
		c.dropMarker(key)
		return errors.New("the canary was stopped")
	}
}
