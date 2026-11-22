package seatapp

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// maxSources bounds how many sources a limiter remembers; with the table full
// of live entries a new source is refused, which fails toward limiting.
const maxSources = 4096

// limiter is a sliding-window limit per key, in memory. Only an allowed event
// is recorded, so a refused one does not lengthen the wait.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, hits: map[string][]time.Time{}}
}

// allow records an event of key at now and reports whether it is within the
// limit.
func (l *limiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, known := l.hits[key]; !known && len(l.hits) >= maxSources {
		for k, ts := range l.hits {
			if len(l.live(ts, now)) == 0 {
				delete(l.hits, k)
			}
		}
		if len(l.hits) >= maxSources {
			return false
		}
	}
	ts := l.live(l.hits[key], now)
	if len(ts) >= l.max {
		l.hits[key] = ts
		return false
	}
	l.hits[key] = append(ts, now)
	return true
}

// live returns the events of ts inside the window ending at now.
func (l *limiter) live(ts []time.Time, now time.Time) []time.Time {
	cut := now.Add(-l.window)
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	return ts[i:]
}

// source names the client of a request for the limits: the address in the
// configured proxy header when there is one that parses (its last element,
// the one the nearest proxy wrote), else the peer's address. An IPv6 client
// is counted by its /64.
func source(r *http.Request, header string) string {
	if header != "" {
		v := r.Header.Get(header)
		if i := strings.LastIndexByte(v, ','); i >= 0 {
			v = v[i+1:]
		}
		if a, err := netip.ParseAddr(strings.TrimSpace(v)); err == nil {
			return normalize(a)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if a, err := netip.ParseAddr(host); err == nil {
		return normalize(a)
	}
	return "peer"
}

func normalize(a netip.Addr) string {
	a = a.Unmap().WithZone("")
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}
