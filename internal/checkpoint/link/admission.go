package link

import (
	"net"
	"net/netip"
	"sync"
	"time"
)

// Each listener owns its pre-authentication budget. Only active sources
// occupy map entries, and a completed handshake immediately releases both limits.
type handshakes struct {
	mu      sync.Mutex
	active  int
	sources map[string]int
}

func (h *handshakes) acquire(source string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.active >= 4 || h.sources[source] >= 2 {
		return false
	}
	if h.sources == nil {
		h.sources = make(map[string]int)
	}
	h.sources[source]++
	h.active++
	return true
}
func (h *handshakes) release(source string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active--
	h.sources[source]--
	if h.sources[source] == 0 {
		delete(h.sources, source)
	}
}
func peerSource(c net.Conn) string {
	if addr, ok := c.RemoteAddr().(*net.TCPAddr); ok {
		return addr.IP.String()
	}
	if unix, ok := c.(*net.UnixConn); ok {
		return unixPeer(unix)
	}
	return "local"
}

// TrustSources are the witness source networks provisioned by the owner.
// A source match reserves handshake capacity. TLS authenticates the peer.
type TrustSources []netip.Prefix

type admission func(net.Conn) (release func())

func homeAdmission(trust TrustSources) admission {
	var ordinary, reserved handshakes
	return func(c net.Conn) func() {
		source := peerSource(c)
		pool := &ordinary
		if addr, ok := c.RemoteAddr().(*net.TCPAddr); ok {
			ip := addr.AddrPort().Addr().Unmap()
			for _, prefix := range trust {
				if prefix.Contains(ip) {
					pool = &reserved
					break
				}
			}
		}
		if !pool.acquire(source) {
			return nil
		}
		return func() { pool.release(source) }
	}
}

// TLS consumes the first byte normally. Once it arrives, restore the absolute
// handshake deadline for subsequent reads.
type firstByteConn struct {
	net.Conn
	deadline time.Time
}

func (c *firstByteConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 && !c.deadline.IsZero() {
		if e := c.Conn.SetReadDeadline(c.deadline); e != nil {
			return n, e
		}
		c.deadline = time.Time{}
	}
	return n, err
}
