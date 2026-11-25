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
	"ataniq/internal/approval/ownerchannel"
	"ataniq/internal/genesis/thread"
	"ataniq/internal/seatgate"
)

// This file is the seat gate of the daemon: the TCP listener on which the seat
// app of the authority host reaches the owner channel's service, through
// package seatgate (pinned-key TLS and strict frames) and the handler of
// package gatehost. It is built with the ataniq_ownerchannel tag only, and the
// command opens it only when the owner asks for it (run --owner-channel
// --seat-gate ADDR). The gate serves the pairing call and the reads of the
// cards (gatehost.Caps) and, when the seat origin is configured and the
// service can decide, a login and a decision from the phone as well
// (gatehost.DecidingCaps). When the service can wait for a change of the
// pending cards (gatehost.WaitPort, as the owner channel's service does), it
// serves the capability watch too (gatehost.CapWatch), the seat's long wait
// that lets the phone's pages refresh by themselves. When the gate has a
// folder for the guided new-project interview (SeatGate.GenesisDir), it opens
// the thread store there and serves the capability genesis (gatehost.CapGenesis),
// the interview's one operation, seat.genesis. A thread store that does not
// open is logged and leaves the interview out: the gate serves the rest. When
// the gate is given a chat port (SeatGate.Chat: the command gives one to the
// phone trial, the STAND-IN sample leads of package chat/standin, and, in a
// build with the canary tag too, to a scratch daemon, its leads' replies
// through [LeadChat]), it serves the capability chat (gatehost.CapChat), the
// seat's read-only view of the leads' conversations, seat.chat; every other
// gate serves no chat.

const (
	// SeatGateDirName is the name of the gate's folder in the owner folder
	// of the state folder. It holds the gate's identity (seatgate.CertFile
	// and seatgate.KeyFile, made by seatgate.WriteIdentityFiles) and the pin
	// file.
	SeatGateDirName = "gate"
	// GenesisDirName is the name of the thread store's folder in the owner
	// folder of the state folder: the guided new-project interview's threads,
	// one file each (package genesis/thread).
	GenesisDirName = thread.DirName
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

// CanonicalSeatOrigin makes a seat origin canonical, by the daemon's rules: it
// takes https://HOST or https://HOST:443, HOST a lower-case domain name of
// letters, digits, dots and dashes (labels of 1 to 63 bytes that neither start
// nor end with a dash, at most 253 bytes, the last label not all digits), and
// no address literal, other port, path, query, fragment or user information.
// It returns https://HOST and the relying party id, HOST. The error is fixed
// and holds nothing of s.
//
// The seat app does not call it: seatapp.New checks its own origin with rules
// of its own (built on net/url), which take some hosts this function refuses
// (an underscore, a trailing dot, a last label of digits alone), and the
// desk's pairing view has a third check. They agree on the canonical form of
// an origin they all take, and a disagreement only makes the logins and
// decisions of such a seat fail, since the gate verifies against its own
// origin (docs/design/seat-gate.md, "The seat origin", lists the
// disagreements; the one function all three were to call is not built).
func CanonicalSeatOrigin(s string) (origin, rpID string, err error) {
	bad := errors.New("the seat origin is not https:// and a lower-case domain name, with no port but 443, path, query, fragment or user information")
	host, ok := strings.CutPrefix(s, "https://")
	if !ok {
		return "", "", bad
	}
	host = strings.TrimSuffix(host, ":443")
	if host == "" || len(host) > 253 {
		return "", "", bad
	}
	labels := strings.Split(host, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", "", bad
		}
		for i := 0; i < len(l); i++ {
			switch c := l[i]; {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-':
			default:
				return "", "", bad
			}
		}
	}
	// A name whose last label is a number is read by a browser as an
	// address: a passkey is bound to a domain name.
	if strings.Trim(labels[len(labels)-1], "0123456789") == "" {
		return "", "", bad
	}
	return "https://" + host, host, nil
}

// SeatGate is the gate of one daemon: where it listens, the key it shows, the
// seat keys it lets in and the seat origin. The pins and the origin are read
// once, when the daemon starts: a change of either file takes effect at the
// next start.
type SeatGate struct {
	// Addr is where the gate listens; see [ParseSeatGateAddr]. Required.
	Addr netip.AddrPort
	// Identity is the gate's key. Required.
	Identity seatgate.Identity
	// Pins are the fingerprints of the seat keys the gate lets in, 1 to
	// [MaxSeatPins] of them, each valid. Required.
	Pins []string
	// Origin is the seat origin in its canonical form (see
	// [CanonicalSeatOrigin]), read once when the daemon started, or "" when
	// none is configured. Only with an origin, and a service that can
	// decide, does the gate serve a login and a decision from the phone.
	Origin string
	// NoOrigin says, for the start line, why there is no origin: the file
	// is missing, refused, or holds no canonical origin. It holds nothing
	// of the file.
	NoOrigin string
	// GenesisDir is the folder of the thread store of the guided new-project
	// interview, or "" for a gate that serves none. The gate opens the store
	// there when it starts (making the folder, mode 0700, if it is not there;
	// package genesis/thread refuses a link, a folder that is not the daemon
	// user's and one open to writing) and closes it when it stops, after the
	// requests in flight ended. A store that cannot be opened is logged, in
	// one line of fixed words with the store's reason word, and the gate
	// starts without it: it does not serve genesis (the phone then shows no
	// "New project"), and nothing in the folder is read or written. The rest
	// of the gate, and the daemon, are not stopped for it.
	GenesisDir string
	// Chat is the chat port of the gate, or nil for a gate that serves no
	// chat. With one the gate serves the capability chat, last in its hello,
	// and seat.chat, the seat's read-only view of the leads' conversations,
	// with or without an origin: a read decides nothing. The command gives
	// one to the phone trial (run --owner-channel --trial), the STAND-IN
	// sample leads of package chat/standin, and, in a build with both tags,
	// to a scratch daemon (run --scratch CONFIG --owner-channel), the
	// read-only view of its leads' replies ([LeadChat], which the scratch
	// mode binds); the two exclude each other, and every other daemon serves
	// no chat.
	Chat gatehost.ChatPort
	// ChatNote, when set and the gate serves chat, is a line the gate writes
	// after its start line, to say what the chat port is (the trial's says it
	// serves samples, a scratch daemon's that it serves the leads' replies).
	ChatNote string
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
// with no pin, more than [MaxSeatPins] or one that is not a fingerprint, an
// origin that is not canonical, and an address that cannot be listened on.
//
// The gate serves the capabilities pair and read. It serves login and decide
// as well only when it has an origin and svc can decide from the phone (it
// is a gatehost.SeatPort, as the owner channel's service is), and watch when
// svc can wait (a gatehost.WaitPort, as the owner channel's service is), with
// or without an origin: a wait decides nothing. It serves genesis, the guided
// new-project interview, when the gate has a GenesisDir whose thread store
// opens, with or without an origin: an answer decides nothing either. A store
// that does not open is not an error of the start: the gate logs it and
// serves the rest without genesis. It serves chat when it was given a chat
// port (SeatGate.Chat), with or without an origin: a read decides nothing. The
// calls of login and decide are bound to the gate's own key fingerprint and
// checked against the origin and its host, the relying party id. It writes one line once the
// gate listens, which names the address, the gate's key fingerprint, the
// origin when it serves login and decide, and what it serves (the
// capabilities of the handler's hello, in its order: watch, genesis and chat
// among them when it serves them), with why it serves no login and decide only
// when it does not; never a pin. With chat and a ChatNote it writes the note
// as a second line.
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
	var opts []gatehost.Option
	var store *thread.Store
	if g.GenesisDir != "" {
		s, err := thread.Open(g.GenesisDir, thread.Options{Log: g.Log})
		if err != nil {
			g.logf("seat gate: the guided interview is not served: its thread store did not open (%s); the phone offers no New project until the folder is fixed and the daemon restarted",
				storeReason(err))
		} else {
			store = s
			opts = append(opts, gatehost.WithGenesis(store))
		}
	}
	if g.Chat != nil {
		opts = append(opts, gatehost.WithChat(g.Chat))
	}
	closeStore := func() {
		if store != nil {
			_ = store.Close()
		}
	}
	var h *gatehost.Handler
	var serves string
	seat, canDecide := svc.(gatehost.SeatPort)
	// The handler lists watch when its service can wait (gatehost.New),
	// genesis when it was given the thread store and chat when it was given
	// the chat port; the start line says what it lists.
	switch {
	case g.Origin == "":
		why := g.NoOrigin
		if why == "" {
			why = "no seat origin is configured"
		}
		h, err = gatehost.New(svc, pinnedIn(pins), g.Log, opts...)
		if err == nil {
			serves = "; it serves " + capWords(h.Caps()) + " only: " + why
		}
	case !canDecide:
		h, err = gatehost.New(svc, pinnedIn(pins), g.Log, opts...)
		if err == nil {
			serves = "; it serves " + capWords(h.Caps()) + " only: this service cannot take a decision from the phone"
		}
	default:
		origin, rpID, cerr := CanonicalSeatOrigin(g.Origin)
		if cerr != nil || origin != g.Origin {
			closeStore()
			return nil, errors.New("seat gate: the seat origin is not in its canonical form")
		}
		gate := ownerchannel.Gate{Instance: g.Identity.Fingerprint(), Origin: origin, RPID: rpID}
		h, err = gatehost.NewDeciding(svc, seat, gate, pinnedIn(pins), g.Log, opts...)
		if err == nil {
			serves = ", seat origin " + origin + "; it serves " + capWords(h.Caps())
		}
	}
	if err != nil {
		closeStore()
		return nil, fmt.Errorf("seat gate: %w", err)
	}
	listen := g.Listen
	if listen == nil {
		listen = net.Listen
	}
	ln, err := listen("tcp", g.Addr.String())
	if err != nil {
		closeStore()
		return nil, fmt.Errorf("seat gate: cannot listen on %s: %s", g.Addr, SafeText(err.Error()))
	}
	cfg := seatgate.ServerConfig{
		TLS:  seatgate.ServerTLS(g.Identity, append([]string(nil), g.Pins...)),
		Caps: h.Caps(),
		// The server retries a failed accept until one succeeds; this line
		// says when a streak of failures starts, so that a lasting one shows.
		AcceptFailed: func() {
			g.logf("seat gate: accepting a connection failed; retrying until one succeeds")
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		if err := seatgate.Serve(ctx, ln, cfg, h); err != nil {
			g.logf("seat gate: stopped: %s", SafeText(err.Error()))
		}
	}()
	g.logf("seat gate: listening on %s, key fingerprint %s%s", ln.Addr(), g.Identity.Fingerprint(), SafeText(serves))
	if g.Chat != nil && g.ChatNote != "" {
		g.logf("seat gate: %s", SafeText(g.ChatNote))
	}
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
			closeStore()
		})
	}, nil
}

// storeReason is the reason word of a thread store that did not open (see
// thread.OpenError), or "unknown" for an error of another kind: one of the
// store's fixed words, never anything of the folder's path.
func storeReason(err error) string {
	var oe *thread.OpenError
	if errors.As(err, &oe) && oe.Reason != "" && strings.Trim(oe.Reason, "abcdefghijklmnopqrstuvwxyz-") == "" {
		return oe.Reason
	}
	return "unknown"
}

// capWords says a capability list in words, as the start line does: "pair and
// read", "pair, read and watch", "pair, read, login, decide, watch, genesis
// and chat".
func capWords(caps []string) string {
	switch len(caps) {
	case 0:
		return "nothing"
	case 1:
		return caps[0]
	}
	return strings.Join(caps[:len(caps)-1], ", ") + " and " + caps[len(caps)-1]
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
