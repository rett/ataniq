package desklink

import (
	"context"
	"errors"
	"maps"
	"net"
	"strings"
	"sync"

	"ataniq/internal/seatgate"
)

// Principal identifies a device's enrolled principal and role at this desk.
// Both strings come from the local enrollment authority.
type Principal struct {
	ID   string
	Role string
}

// Peer binds the authenticated TLS key to its enrollment for this connection.
type Peer struct {
	Fingerprint string
	Principal   Principal
}

// Registry holds the desk's device enrollments and their open connections in
// memory. Share one registry across connections. Its zero value admits nobody.
type Registry struct {
	mu      sync.Mutex
	devices map[string]*device
}

type device struct {
	principal Principal
	live      map[*session]struct{}
}

type session struct {
	raw    net.Conn
	cancel context.CancelFunc
}

// Enroll pins a device's SPKI fingerprint to p. A duplicate pin is an error,
// including an attempt to change its principal or role.
func (r *Registry) Enroll(fingerprint string, p Principal) error {
	if !seatgate.ValidFingerprint(fingerprint) {
		return errors.New("desklink: invalid device fingerprint")
	}
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.Role) == "" {
		return errors.New("desklink: missing principal ID or role")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.devices[fingerprint] != nil {
		return errors.New("desklink: device already enrolled")
	}
	if r.devices == nil {
		r.devices = make(map[string]*device)
	}
	r.devices[fingerprint] = &device{principal: p, live: make(map[*session]struct{})}
	return nil
}

// Revoke removes every enrollment with the given principal ID, cancels its
// handlers and closes their transports. It reports whether it removed any pins.
func (r *Registry) Revoke(principalID string) bool {
	r.mu.Lock()
	var live []*session
	removed := false
	for pin, d := range r.devices {
		if d.principal.ID == principalID {
			delete(r.devices, pin)
			for s := range d.live {
				live = append(live, s)
			}
			removed = true
		}
	}
	r.mu.Unlock()
	for _, s := range live {
		s.cancel()
		s.raw.Close()
	}
	return removed
}

// snapshot preserves enrollment identity across the TLS handshake. If the
// same key is revoked and re-enrolled, it gets a different *device.
func (r *Registry) snapshot() map[string]*device {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps.Clone(r.devices)
}

func (r *Registry) attach(pin string, d *device, s *session) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d == nil || r.devices[pin] != d {
		return false
	}
	d.live[s] = struct{}{}
	return true
}

func (r *Registry) detach(d *device, s *session) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(d.live, s)
}
