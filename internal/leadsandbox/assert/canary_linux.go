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
)

// Canary is the target of assertion 3, owned by the caller (the holder, or a
// test): a TCP listener on a loopback address and an abstract unix socket with
// a random name. Both accept a connection, close it at once without reading or
// sending, and count it. The caller puts TCP and Abstract in the spec; inside
// the sandbox neither must be reachable, so after the probe has run the count
// is 0 whatever the probe reported. A count above 0 means a connection reached
// the host.
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
	c := &Canary{TCP: tl.Addr().String(), Abstract: name, lns: []net.Listener{tl, ul}}
	c.serve(tl, &c.tcpHits)
	c.serve(ul, &c.absHits)
	return c, nil
}

func (c *Canary) serve(l net.Listener, hits *atomic.Int64) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
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

// Close stops both listeners and waits for their loops to end. It is safe to
// call more than once.
func (c *Canary) Close() error {
	var err error
	c.once.Do(func() {
		for _, l := range c.lns {
			if e := l.Close(); e != nil && err == nil {
				err = e
			}
		}
		c.wg.Wait()
	})
	return err
}
