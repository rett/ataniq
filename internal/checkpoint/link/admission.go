package link

import (
	"net"
	"sync"
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
