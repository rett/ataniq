//go:build linux

package assert

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// The accept loop of a listener survives an error of Accept (out of
// descriptors, say) and waits before it tries again: first acceptBackoffMin,
// doubling to acceptBackoffMax. Each error is counted (AcceptErrors).
const (
	acceptBackoffMin = time.Millisecond
	acceptBackoffMax = 100 * time.Millisecond
)

// Canary is the target of assertion 3, owned by the caller (the holder, or a
// test): a TCP listener on a loopback address and an abstract unix socket with
// a random name. Both accept a connection, close it at once without reading or
// sending, and count it. The caller puts TCP and Abstract in the spec; inside
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
// Loopback and abstract sockets are not namespaces: creating a Canary needs no
// privilege.
type Canary struct {
	// TCP is the loopback address and port, "127.0.0.1:port".
	TCP string
	// Abstract is the socket name without the leading @.
	Abstract string

	tcpHits atomic.Int64
	absHits atomic.Int64
	lns     []net.Listener
	wg      sync.WaitGroup
	once    sync.Once

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
	var ul net.Listener
	var name string
	for try := 0; try < 8; try++ {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			tl.Close()
			return nil, fmt.Errorf("assert: canary: random name: %w", err)
		}
		name = "ataniq-canary-" + hex.EncodeToString(b[:])
		ul, err = net.Listen("unix", "@"+name)
		if err == nil || !errors.Is(err, syscall.EADDRINUSE) {
			break
		}
	}
	if err != nil {
		tl.Close()
		return nil, fmt.Errorf("assert: canary: %w", err)
	}
	c := &Canary{
		TCP: tl.Addr().String(), Abstract: name, lns: []net.Listener{tl, ul},
		done: make(chan struct{}), backoffMin: acceptBackoffMin, backoffMax: acceptBackoffMax,
	}
	c.serve(tl, &c.tcpHits)
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
			hits.Add(1)
			conn.Close()
		}
	}()
}

// Hits is the number of connections both listeners accepted.
func (c *Canary) Hits() int64 { return c.tcpHits.Load() + c.absHits.Load() }

// TCPHits is the number of connections the TCP listener accepted.
func (c *Canary) TCPHits() int64 { return c.tcpHits.Load() }

// AbstractHits is the number of connections the abstract socket accepted.
func (c *Canary) AbstractHits() int64 { return c.absHits.Load() }

// AcceptErrors is the number of errors the accept loops met and went on
// from. When it is above 0, Hits may be short.
func (c *Canary) AcceptErrors() int64 { return c.acceptErrors.Load() }

// Stopped says an accept loop has ended: after Close, or because its listener
// was closed some other way. A connection that comes after is not counted.
func (c *Canary) Stopped() bool { return c.ended.Load() > 0 }

// Close stops both listeners and waits for their loops to end, also when one
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
