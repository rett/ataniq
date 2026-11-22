//go:build linux && ataniq_ownerchannel

package leaddaemon

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"ataniq/internal/approval/gatehost"
	"ataniq/internal/seatgate"
)

// This file is the seat gate of the daemon: the TCP listener on which the seat
// app of the authority host reaches the owner channel's service, through
// package seatgate (pinned-key TLS and strict frames) and the handler of
// package gatehost. It is built with the ataniq_ownerchannel tag only, and the
// command opens it only when the owner asks for it (run --owner-channel
// --seat-gate ADDR). The gate serves the capabilities of gatehost.Caps, the
// pairing call and the reads of the cards; it decides nothing.

const (
	// SeatGateDirName is the name of the gate's folder in the owner folder
	// of the state folder. It holds the gate's identity (seatgate.CertFile
	// and seatgate.KeyFile, made by seatgate.WriteIdentityFiles) and the pin
	// file.
	SeatGateDirName = "gate"
	// SeatPinsFileName is the name of the pin file in the gate's folder:
	// the fingerprints of the seat keys the gate lets in, one per line.
	SeatPinsFileName = "seat-pins"
	// MaxSeatPins is the most pins the file may hold: room for the seat's
	// key and its next one during a rotation, and two more.
	MaxSeatPins = 4
	// SeatGateIdentityName is the name in the gate's certificate.
	SeatGateIdentityName = "ataniq-gate"

	// maxSeatPinsBytes is the most the pin file may hold.
	maxSeatPinsBytes = 4 << 10
	// seatGateStopWait bounds how long stopping the gate waits for its
	// server, which lets the requests in flight finish for its own shutdown
	// grace (5 s) first.
	seatGateStopWait = 10 * time.Second
)

// ParseSeatGateAddr reads the address the gate listens on: one IP address
// and a port, such as 192.0.2.10:7443 or [2001:db8::10]:7443. A host name, a
// missing or zero port, an address with a zone, and an address that is not
// one host's (the wildcard of either family, a multicast address, the
// broadcast address) are refused. An IPv4 address written in IPv6 form is
// taken as the IPv4 address.
func ParseSeatGateAddr(s string) (netip.AddrPort, error) {
	ap, err := netip.ParseAddrPort(s)
	if err != nil {
		return netip.AddrPort{}, errors.New("the seat gate's address must be one IP address and a port, such as 192.0.2.10:7443 or [2001:db8::10]:7443; a host name is not taken")
	}
	a := ap.Addr()
	if a.Zone() != "" {
		return netip.AddrPort{}, errors.New("the seat gate's address must have no zone")
	}
	a = a.Unmap()
	if a.IsUnspecified() || a.IsMulticast() || a == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return netip.AddrPort{}, errors.New("the seat gate's address must be one address of this host, not a wildcard, multicast or broadcast address")
	}
	if ap.Port() == 0 {
		return netip.AddrPort{}, errors.New("the seat gate's address must name its port")
	}
	return netip.AddrPortFrom(a, ap.Port()), nil
}

// ReadSeatPins reads the pin file at path: a regular file, not a link (of
// either kind), owned by the daemon's user and not accessible to group or
// other (mode 0600 or narrower), of at most 4 KiB. Each line holds one
// fingerprint of a seat key, 64 lower-case hexadecimal digits; a blank line
// and a line that starts with # are skipped, and spaces around a line are
// not read. A line that is not a fingerprint, a fingerprint given twice and
// more than [MaxSeatPins] fingerprints are refused, and the error names the
// line by its number, never by what it holds. An empty list is not an error
// here; the gate refuses to start with one.
func ReadSeatPins(path string) ([]string, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		switch {
		case errors.Is(err, syscall.ELOOP):
			return nil, errors.New("the seat pin file is a symbolic link")
		case errors.Is(err, os.ErrNotExist):
			return nil, errors.New("there is no seat pin file")
		}
		return nil, errors.New("the seat pin file cannot be opened")
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, errors.New("the seat pin file cannot be read")
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	switch {
	case !st.Mode().IsRegular():
		return nil, errors.New("the seat pin file is not a regular file")
	case !ok || sys.Nlink != 1:
		return nil, errors.New("the seat pin file has another link")
	case int64(sys.Uid) != int64(os.Geteuid()):
		return nil, errors.New("the seat pin file is not the daemon user's")
	case st.Mode().Perm()&0o077 != 0:
		return nil, errors.New("the seat pin file is open to group or other; it must be mode 0600")
	case st.Size() > maxSeatPinsBytes:
		return nil, fmt.Errorf("the seat pin file holds more than %d bytes", maxSeatPinsBytes)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxSeatPinsBytes+1))
	if err != nil {
		return nil, errors.New("the seat pin file cannot be read")
	}
	if len(data) > maxSeatPinsBytes {
		return nil, fmt.Errorf("the seat pin file holds more than %d bytes", maxSeatPinsBytes)
	}
	pins := []string{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		switch {
		case !seatgate.ValidFingerprint(line):
			return nil, fmt.Errorf("line %d of the seat pin file is not a key fingerprint (64 lower-case hexadecimal digits)", i+1)
		case len(pins) == MaxSeatPins:
			return nil, fmt.Errorf("the seat pin file holds more than %d pins", MaxSeatPins)
		}
		for _, p := range pins {
			if p == line {
				return nil, fmt.Errorf("line %d of the seat pin file repeats a pin", i+1)
			}
		}
		pins = append(pins, line)
	}
	return pins, nil
}

// SeatGate is the gate of one daemon: where it listens, the key it shows and
// the seat keys it lets in. The pins are read once, when the daemon starts:
// a change of the pin file takes effect at the next start.
type SeatGate struct {
	// Addr is where the gate listens; see [ParseSeatGateAddr]. Required.
	Addr netip.AddrPort
	// Identity is the gate's key. Required.
	Identity seatgate.Identity
	// Pins are the fingerprints of the seat keys the gate lets in, 1 to
	// [MaxSeatPins] of them, each valid. Required.
	Pins []string
	// Log receives the gate's lines: the one start line, the handler's
	// refusals and a stop that did not go as asked; nil discards them. No
	// line holds a pin.
	Log func(format string, a ...any)
	// Listen opens the listener; nil means net.Listen.
	Listen func(network, address string) (net.Listener, error)
}

func (g *SeatGate) logf(format string, a ...any) {
	if g.Log != nil {
		g.Log(format, a...)
	}
}

// Start opens the gate's listener and serves the gate on it with svc until
// the returned stop is called. It refuses a gate without a valid identity,
// with no pin, more than [MaxSeatPins] or one that is not a fingerprint, and
// an address that cannot be listened on. It writes one line once the gate
// listens, which names the address and the gate's key fingerprint and no
// pin.
//
// Stop stops accepting, lets the requests in flight finish for the server's
// shutdown grace, closes every connection and returns once the server has
// ended, or after a bound when it does not end (it is logged); it may be
// called more than once. A listener that fails while the daemon runs ends the
// gate and is logged; the daemon goes on without it.
func (g *SeatGate) Start(svc gatehost.Port) (stop func(), err error) {
	if !g.Addr.IsValid() {
		return nil, errors.New("seat gate: no address")
	}
	if !seatgate.ValidFingerprint(g.Identity.Fingerprint()) {
		return nil, errors.New("seat gate: no key")
	}
	if len(g.Pins) == 0 {
		return nil, errors.New("seat gate: no seat key is pinned")
	}
	if len(g.Pins) > MaxSeatPins {
		return nil, fmt.Errorf("seat gate: more than %d seat keys are pinned", MaxSeatPins)
	}
	pins := make([][]byte, len(g.Pins))
	for i, p := range g.Pins {
		if !seatgate.ValidFingerprint(p) {
			return nil, errors.New("seat gate: a pin is not a key fingerprint")
		}
		pins[i] = []byte(p)
	}
	h, err := gatehost.New(svc, pinnedIn(pins), g.Log)
	if err != nil {
		return nil, fmt.Errorf("seat gate: %w", err)
	}
	listen := g.Listen
	if listen == nil {
		listen = net.Listen
	}
	ln, err := listen("tcp", g.Addr.String())
	if err != nil {
		return nil, fmt.Errorf("seat gate: cannot listen on %s: %s", g.Addr, SafeText(err.Error()))
	}
	cfg := seatgate.ServerConfig{
		TLS:  seatgate.ServerTLS(g.Identity, append([]string(nil), g.Pins...)),
		Caps: gatehost.Caps(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		if err := seatgate.Serve(ctx, ln, cfg, h); err != nil {
			g.logf("seat gate: stopped: %s", SafeText(err.Error()))
		}
	}()
	g.logf("seat gate: listening on %s, key fingerprint %s", ln.Addr(), g.Identity.Fingerprint())
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			t := time.NewTimer(seatGateStopWait)
			defer t.Stop()
			select {
			case <-ended:
			case <-t.C:
				g.logf("seat gate: the server did not stop within %s; its connections end with this process", seatGateStopWait)
			}
		})
	}, nil
}

// pinnedIn is the pin check the handler asks on every call: whether fp is one
// of pins. It compares every pin, in constant time each.
func pinnedIn(pins [][]byte) func(fp string) bool {
	return func(fp string) bool {
		found := 0
		for _, p := range pins {
			found |= subtle.ConstantTimeCompare(p, []byte(fp))
		}
		return found == 1
	}
}
